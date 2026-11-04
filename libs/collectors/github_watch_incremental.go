package collectors

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// A warm watch scan replaces the full scan with two small searches, run side
// by side:
//
//   - The update search lists PRs the query matches that changed since shortly
//     before the last scan. A push, a retarget, a new PR, or a reopen all
//     change a PR's updatedAt, so every PR whose membership could have moved
//     shows up here, and only its membership is recomputed.
//   - The reconciliation slice walks the query's PRs by creation date, a few
//     pages per run, resuming where the last run stopped. It finds what the
//     update search can't: PRs that left the query without anything updating
//     them (a deleted repository, lost access) and, after a truncated full
//     scan, PRs beyond GitHub's 1000-result cap. Every matching PR is checked
//     once per lap.
//
// A PR known only from the cache still gets its detail looked up like any
// other, and that lookup settles the rest: a closed PR is evicted, and so is
// one updated since the last scan that the update search didn't return,
// because it no longer matches the query.

const (
	// watchCacheMaxAge forces a full scan at least daily, bounding how long
	// any mistake in the cache can last.
	watchCacheMaxAge = 24 * time.Hour
	// watchUpdatedOverlap reaches the update search back before the last scan,
	// to cover clock skew and search indexing lag.
	watchUpdatedOverlap = 10 * time.Minute
	// watchIndexGrace is how recent an update must be for its absence from the
	// update search to be put down to indexing lag rather than the PR leaving
	// the query. Such a PR stays one more run and is settled on the next.
	watchIndexGrace = 5 * time.Minute
	// watchSlicePages bounds each run's reconciliation slice, in search pages.
	watchSlicePages = 3
)

// watchScanJob is one watch query being scanned.
type watchScanJob struct {
	spec    prReviewSpec
	path    string
	started time.Time
	// cache is the earlier scan's state, for a warm scan.
	cache *watchCache
}

// watchCacheable reports whether a watch query can be scanned incrementally.
// The warm searches add their own date range and sort, so a query that sets
// either itself is always scanned in full.
func watchCacheable(args []string) bool {
	for _, a := range args {
		a = strings.ToLower(strings.TrimPrefix(a, "-"))
		if strings.HasPrefix(a, "sort:") || strings.HasPrefix(a, "updated:") || strings.HasPrefix(a, "created:") {
			return false
		}
	}
	return true
}

// loadWatchCache returns the cache at path when a warm scan can use it: it
// exists, is readable, and is recent enough.
func (r ghRun) loadWatchCache(path string, now time.Time) *watchCache {
	if path == "" {
		return nil
	}
	c, err := loadWatchCache(path)
	if err != nil {
		loggerFor(r.opts, r.directive.ID).Note("github: ignoring unreadable watch cache %s: %v", path, err)
		return nil
	}
	if c == nil || c.ScannedAt.After(now) || now.Sub(c.ScannedAt) > watchCacheMaxAge {
		return nil
	}
	return c
}

// scanWatchIncremental runs a warm scan. ok is false when the cache can't be
// brought up to date this way, and the caller should scan in full.
func (c GitHubCollector) scanWatchIncremental(ctx context.Context, run ghRun, job watchScanJob) (rows []ghSearchActivityRow, ok bool, err error) {
	spec, cache := job.spec, job.cache
	name := watchName(spec)
	cutoff := cache.ScannedAt.Add(-watchUpdatedOverlap)

	// Run the reconciliation slice beside the update search
	var slice watchSlice
	var sliceErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		slice, sliceErr = c.fetchWatchSlice(ctx, run, spec.args, cache.Cursor)
	}()
	updated, err := c.searchWatchUpdates(ctx, run, spec.args, cutoff)
	wg.Wait()
	if err != nil {
		return nil, false, err
	}
	if updated.matched > updated.returned {
		loggerFor(run.opts, run.directive.ID).Note("github: watch %q changed too much since the last scan to catch up; scanning in full", name)
		return nil, false, nil
	}

	// Gather the PRs either search returned, in search order
	fresh := updated.nodes
	seen := make(map[string]bool, len(fresh)+len(slice.nodes))
	for _, n := range fresh {
		seen[n.URL] = true
	}
	if sliceErr == nil {
		for _, n := range slice.nodes {
			if !seen[n.URL] {
				seen[n.URL] = true
				fresh = append(fresh, n)
			}
		}
	} else {
		run.gaps.add(fmt.Sprintf("watch %q reconciliation failed", name), fmt.Sprintf(
			"could not check part of %q against the cached watched set, so PRs that left it without an update may still be listed: %v",
			watchSearchQ(spec.args), sliceErr), "warning")
	}

	// Recompute membership where the head or base moved, or was never known
	failed := cache.refresh(fresh, spec.paths, func(url string) ([]string, error) {
		return c.listPRFiles(ctx, run, url)
	})
	if len(failed) > 0 {
		run.gaps.add(fmt.Sprintf("watch %q file listings failed", name), fmt.Sprintf(
			"could not list the files of %d PR(s), so they were left out of the watched set: %s",
			len(failed), strings.Join(failed, "; ")), "error")
	}

	// Drop what the slice showed is gone, and move it along
	if sliceErr == nil {
		cache.reconcile(slice, seen)
	}
	if cache.Truncated && !cache.Covered {
		run.gaps.add(fmt.Sprintf("watch %q scan truncated", name), fmt.Sprintf(
			"search %q matches more open PRs than GitHub search returns at once; the ones it left out are still being checked against the watched paths, a few pages per run",
			watchSearchQ(spec.args)), "warning")
	}

	// Hold the cache until detail lookups settle the PRs no search returned
	cache.ScannedAt = job.started.UTC()
	cachedOnly := map[string]bool{}
	for url, e := range cache.PRs {
		if !seen[url] && e.Touches && !e.Unknown {
			cachedOnly[url] = true
		}
	}
	run.watch.hold(ctx, run, &watchScanState{job: job, cache: cache, cachedOnly: cachedOnly, cutoff: cutoff})
	return cache.touchingRows(), true, nil
}

// searchWatchUpdates lists the open PRs a watch query matches that were
// updated at or after since.
func (c GitHubCollector) searchWatchUpdates(ctx context.Context, run ghRun, args []string, since time.Time) (watchScan, error) {
	q := fmt.Sprintf("is:pr is:open %s updated:>=%s sort:updated-desc", strings.Join(args, " "), since.UTC().Format(time.RFC3339))
	cmd := []string{"api", "graphql", "--paginate", "-f", "q=" + q, "-f", "query=" + watchSearchQuery}
	out, err := runAndLogExecContext(ctx, "gh", cmd, "", run.env, run.opts, run.directive.ID)
	if err != nil {
		return watchScan{}, fmt.Errorf("gh api graphql (search %q): %w", q, err)
	}
	scan, err := parseWatchSearchPages(out)
	if err != nil {
		return watchScan{}, fmt.Errorf("parse search %q: %w", q, err)
	}
	return scan, nil
}

// watchSlice is one run's stretch of the reconciliation walk.
type watchSlice struct {
	// upper is the creation time the slice started at, or zero for the newest.
	upper time.Time
	nodes []watchSearchNode
	// last is the oldest creation time the slice reached.
	last time.Time
	// reachedEnd is set when the slice ran out of PRs, completing a lap.
	reachedEnd bool
}

// fetchWatchSlice reads up to watchSlicePages pages of a watch query's PRs,
// newest created first, starting at cursor.
func (c GitHubCollector) fetchWatchSlice(ctx context.Context, run ghRun, args []string, cursor string) (watchSlice, error) {
	s := watchSlice{upper: parseGitHubTime(cursor)}
	q := "is:pr is:open " + strings.Join(args, " ")
	if !s.upper.IsZero() {
		q += " created:<=" + s.upper.Format(time.RFC3339)
	}
	q += " sort:created-desc"

	// Page by hand, so the slice stops after a few pages
	after := ""
	returned := 0
	for page := 0; page < watchSlicePages; page++ {
		cmd := []string{"api", "graphql", "-f", "q=" + q, "-f", "query=" + watchSearchQuery}
		if after != "" {
			cmd = append(cmd, "-f", "endCursor="+after)
		}
		out, err := runAndLogExecContext(ctx, "gh", cmd, "", run.env, run.opts, run.directive.ID)
		if err != nil {
			return watchSlice{}, fmt.Errorf("gh api graphql (search %q): %w", q, err)
		}
		p, err := parseWatchSearchPage(out)
		if err != nil {
			return watchSlice{}, fmt.Errorf("parse search %q: %w", q, err)
		}

		// Keep the PRs, noting how far back they reach
		for _, n := range p.Data.Search.Nodes {
			if n.URL != "" {
				s.nodes = append(s.nodes, n)
				s.last = parseGitHubTime(n.CreatedAt)
			}
		}

		// The last page ends the lap only if the search's cap didn't cut it
		returned += len(p.Data.Search.Nodes)
		if !p.Data.Search.PageInfo.HasNextPage {
			s.reachedEnd = returned >= p.Data.Search.IssueCount
			break
		}
		after = p.Data.Search.PageInfo.EndCursor
	}
	if !s.reachedEnd && s.last.IsZero() {
		return watchSlice{}, fmt.Errorf("search %q returned pages without PRs", q)
	}
	return s, nil
}

// refresh records fresh search nodes in the cache, reusing each one's
// membership while its head and base are unchanged and recomputing it
// otherwise. Cached PRs whose files couldn't be listed before are retried.
// It returns the listings that failed, as "URL: error".
func (c *watchCache) refresh(fresh []watchSearchNode, paths []string, listFiles func(url string) ([]string, error)) []string {
	var need []watchSearchNode
	inFresh := make(map[string]bool, len(fresh))
	for _, n := range fresh {
		inFresh[n.URL] = true
		e, ok := c.PRs[n.URL]
		if ok && !e.Unknown && n.HeadRefOid != "" && e.HeadRefOid == n.HeadRefOid && e.BaseRefName == n.BaseRefName {
			c.PRs[n.URL] = n.cacheEntry(touchVerdict{touches: e.Touches})
			continue
		}
		need = append(need, n)
	}

	// Retry earlier failures, forcing a full listing since no paths are shown
	var retry []string
	for url, e := range c.PRs {
		if e.Unknown && !inFresh[url] {
			retry = append(retry, url)
		}
	}
	sort.Strings(retry)
	for _, url := range retry {
		e := c.PRs[url]
		n := watchSearchNode{URL: url, HeadRefOid: e.HeadRefOid, BaseRefName: e.BaseRefName}
		n.Files.TotalCount = 1 << 30
		need = append(need, n)
	}

	// Classify what's left, keeping each retried PR's cached row
	verdicts := classifyTouching(need, paths, listFiles)
	var failed []string
	for i, n := range need {
		entry := n.cacheEntry(verdicts[i])
		if !inFresh[n.URL] {
			entry.Row = c.PRs[n.URL].Row
		}
		c.PRs[n.URL] = entry
		if verdicts[i].err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", n.URL, verdicts[i].err))
		}
	}
	return failed
}

// reconcile evicts cached PRs created within the slice's span that no search
// returned this run, since they no longer match the query, then moves the
// cursor on. Completing a lap means every matching PR has been seen.
func (c *watchCache) reconcile(s watchSlice, seen map[string]bool) {
	for url, e := range c.PRs {
		created := parseGitHubTime(e.Row.CreatedAt)
		switch {
		case seen[url] || created.IsZero():
		case !s.upper.IsZero() && created.After(s.upper):
		case !s.reachedEnd && !created.After(s.last):
		default:
			delete(c.PRs, url)
		}
	}

	// Resume below the slice; step past a second the slice didn't get beyond
	if s.reachedEnd {
		c.Cursor, c.Covered = "", true
		return
	}
	next := s.last
	if next.Equal(s.upper) {
		next = next.Add(-time.Second)
	}
	c.Cursor = next.Format(time.RFC3339)
}

// watchSession holds a run's watch caches between discovery and the detail
// lookups that settle them.
type watchSession struct {
	mu    sync.Mutex
	scans []*watchScanState
}

// watchScanState is one watch query's cache awaiting settling.
type watchScanState struct {
	job   watchScanJob
	cache *watchCache
	// cachedOnly lists the PRs output from the cache alone, which no search
	// returned this run.
	cachedOnly map[string]bool
	// cutoff is where the update search began.
	cutoff time.Time
}

// hold keeps a scan's cache until the run settles it. Without a session there
// is nothing to settle against, so the cache is saved at once.
func (s *watchSession) hold(ctx context.Context, run ghRun, st *watchScanState) {
	if s == nil {
		if ctx.Err() == nil {
			run.saveWatchCache(st.job.path, st.cache)
		}
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scans = append(s.scans, st)
}

// settle applies resolved PR details to the held caches. Closed PRs are
// evicted from every cache. A PR output from a cache alone is evicted when its
// detail shows an update the update search should have returned, because it
// no longer matches the query; the returned keys name those rows.
func (s *watchSession) settle(rows []prReviewRow) map[watchRowKey]bool {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	left := map[watchRowKey]bool{}
	for _, r := range rows {
		for _, st := range s.scans {
			if _, ok := st.cache.PRs[r.row.URL]; !ok {
				continue
			}
			if r.detail.closed() {
				delete(st.cache.PRs, r.row.URL)
				continue
			}

			// Only the row's own query decides whether it left that query
			if !r.spec.watch || st.key() != specKey(r.spec) || !st.cachedOnly[r.row.URL] {
				continue
			}
			at := r.detail.UpdatedAt
			if !at.IsZero() && !at.Before(st.cutoff) && !at.After(st.job.started.Add(-watchIndexGrace)) {
				delete(st.cache.PRs, r.row.URL)
				delete(st.cachedOnly, r.row.URL)
				left[watchRowKey{spec: st.key(), url: r.row.URL}] = true
			}
		}
	}
	return left
}

// save writes every held cache, unless the run was cut short.
func (s *watchSession) save(ctx context.Context, run ghRun) {
	if s == nil || ctx.Err() != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range s.scans {
		run.saveWatchCache(st.job.path, st.cache)
	}
}

// watchRowKey names one watch query's row for a PR.
type watchRowKey struct {
	spec string
	url  string
}

// key identifies the scan's watch query.
func (st *watchScanState) key() string {
	return specKey(st.job.spec)
}

// specKey identifies a watch query by its search and paths.
func specKey(spec prReviewSpec) string {
	return watchSearchQ(spec.args) + "\x00" + strings.Join(spec.paths, "\x00")
}

// settleWatchRows drops the watch rows a detail lookup showed have left their
// query. A PR another search also found then takes that search's row instead,
// as a full scan would have given it, reusing the detail already fetched where
// it covers what that search needs.
func (c GitHubCollector) settleWatchRows(ctx context.Context, run ghRun, found, rows []prReviewRow) []prReviewRow {
	for {
		left := run.watch.settle(rows)
		if len(left) == 0 {
			return rows
		}

		// Plan the rows again without the ones that left
		kept := found[:0:0]
		for _, r := range found {
			if !(r.spec.watch && left[watchRowKey{spec: specKey(r.spec), url: r.row.URL}]) {
				kept = append(kept, r)
			}
		}
		found = kept
		next := capReviewCandidates(dedupePRReviewRows(found))

		// Reuse resolved details, then look up whatever is still missing
		full := map[string]prDetail{}
		head := map[string]prDetail{}
		for _, r := range rows {
			if r.spec.mine || r.spec.reviewable {
				full[r.row.URL] = r.detail
			} else {
				head[r.row.URL] = r.detail
			}
		}
		var missing []int
		for i := range next {
			url, needsFull := next[i].row.URL, next[i].spec.mine || next[i].spec.reviewable
			if d, ok := full[url]; ok {
				next[i].detail = d
				if !needsFull {
					next[i].detail = prDetail{HeadBranch: d.HeadBranch}
				}
			} else if d, ok := head[url]; ok && !needsFull {
				next[i].detail = d
			} else {
				missing = append(missing, i)
			}
		}
		c.resolveReviewRows(ctx, run, reviewBatch{rows: next, indices: missing})
		rows = next
	}
}
