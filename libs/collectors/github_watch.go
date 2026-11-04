package collectors

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KurtPreston/docent/libs/config/userdata"
)

// Watch queries follow a set of open PRs regardless of who wrote them, such as
// every PR that touches part of a repository or asks a team for review. They
// exist so an automation can act on a team's PRs, not just the user's own,
// without those PRs being mistaken for the user's work.

// ghRun is what every `gh` call from the collector needs besides its arguments.
type ghRun struct {
	env       []string
	directive userdata.Directive
	opts      *CollectOpts
	gaps      *watchGaps
}

// watchGaps records the open PRs a watch scan could not check, so a scan that
// came back incomplete says so instead of passing for the whole watched set.
type watchGaps struct {
	directive userdata.Directive
	at        time.Time
	mu        sync.Mutex
	items     []StatusItem
}

// add records one gap as a collector_error row. The title is stable for a
// given gap, so docentd offers it to automations once when it first appears
// rather than on every poll.
func (g *watchGaps) add(title, summary, severity string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.items = append(g.items, StatusItem{
		DirectiveID: g.directive.ID,
		Source:      g.directive.Collector,
		Kind:        "collector_error",
		Title:       g.directive.Name + ": " + title,
		Summary:     summary,
		Severity:    severity,
		ObservedAt:  g.at,
		IsSelf:      true,
	})
}

// list returns the recorded gaps.
func (g *watchGaps) list() []StatusItem {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]StatusItem(nil), g.items...)
}

// watchPRReviewSpecs plans one spec per declared watch query. They are searched
// before `authored` so a user's own PR in the watched set carries the watch
// relation, which is what a rule over that set matches on; forRow keeps it
// owned.
func watchPRReviewSpecs(extra []userdata.PRQuery) []prReviewSpec {
	var specs []prReviewSpec
	for _, q := range extra {
		qualifiers := strings.Fields(q.Qualifiers)
		if !q.Watch || len(qualifiers) == 0 {
			continue
		}
		specs = append(specs, prReviewSpec{
			relation: strings.TrimSpace(q.Relation),
			args:     qualifiers,
			watch:    true,
			paths:    normalizeWatchPaths(q.Paths),
		})
	}
	return specs
}

// forRow settles ownership for one PR a spec found. Every other spec decides it
// up front, but a watch query mixes the user's PRs with other people's: the
// user's own stay owned, so rules guarded by `self: true` still see them, and
// the rest are review candidates.
func (s prReviewSpec) forRow(row ghSearchActivityRow, user string) prReviewSpec {
	if !s.watch {
		return s
	}
	mine := strings.EqualFold(strings.TrimSpace(row.Author.Login), strings.TrimSpace(user))
	s.mine, s.reviewable = mine, !mine
	return s
}

// listPRReviewSpec lists the open PRs one spec finds. A path filter can't be
// expressed in GitHub search, so those specs list through a GraphQL search that
// returns each PR's changed paths in the same round trip.
func (c GitHubCollector) listPRReviewSpec(ctx context.Context, run ghRun, spec prReviewSpec) ([]ghSearchActivityRow, error) {
	if len(spec.paths) == 0 {
		return c.listOpenPRs(ctx, run.env, run.directive, run.opts, spec.args...)
	}
	return c.listOpenPRsTouching(ctx, run, spec)
}

//go:embed github_watch_search.graphql
var watchSearchQuery string

// watchSearchPage is one page of watchSearchQuery's reply.
type watchSearchPage struct {
	Data struct {
		Search struct {
			IssueCount int               `json:"issueCount"`
			Nodes      []watchSearchNode `json:"nodes"`
		} `json:"search"`
	} `json:"data"`
}

// watchScan is what a paginated watch search returned: the PR nodes, how many
// results the search reported matching, and how many it actually returned.
type watchScan struct {
	nodes    []watchSearchNode
	matched  int
	returned int
}

// watchSearchNode is one PR from watchSearchQuery.
type watchSearchNode struct {
	Title     string  `json:"title"`
	URL       string  `json:"url"`
	IsDraft   bool    `json:"isDraft"`
	CreatedAt string  `json:"createdAt"`
	UpdatedAt string  `json:"updatedAt"`
	Author    ghActor `json:"author"`
	Assignees struct {
		Nodes []struct {
			Login string `json:"login"`
		} `json:"nodes"`
	} `json:"assignees"`
	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
	Files struct {
		TotalCount int `json:"totalCount"`
		Nodes      []struct {
			Path string `json:"path"`
		} `json:"nodes"`
	} `json:"files"`
}

// listOpenPRsTouching runs a path-filtered watch query: every page of the
// GraphQL search, then the PRs that change a file under one of its paths.
// PRs the scan could not check are recorded in run.gaps rather than dropped
// silently.
func (c GitHubCollector) listOpenPRsTouching(ctx context.Context, run ghRun, spec prReviewSpec) ([]ghSearchActivityRow, error) {
	q := watchSearchQ(spec.args)
	args := []string{"api", "graphql", "--paginate", "-f", "q=" + q, "-f", "query=" + watchSearchQuery}
	out, err := runAndLogExecContext(ctx, "gh", args, "", run.env, run.opts, run.directive.ID)
	if err != nil {
		return nil, fmt.Errorf("gh api graphql (search %q): %w", q, err)
	}
	scan, err := parseWatchSearchPages(out)
	if err != nil {
		return nil, fmt.Errorf("parse search %q: %w", q, err)
	}

	// Name the scan the way the config does, falling back to its search
	name := spec.relation
	if name == "" {
		name = q
	}
	if scan.matched > scan.returned {
		run.gaps.add(fmt.Sprintf("watch %q scan truncated", name), fmt.Sprintf(
			"search %q matched %d open PRs but GitHub search returned only %d; the last %d in search order were not checked against the watched paths",
			q, scan.matched, scan.returned, scan.matched-scan.returned), "warning")
	}

	// Filter by path, then report any PR whose file list could not be read
	rows, failed := filterTouching(scan.nodes, spec.paths, func(url string) ([]string, error) {
		return c.listPRFiles(ctx, run, url)
	})
	if len(failed) > 0 {
		run.gaps.add(fmt.Sprintf("watch %q file listings failed", name), fmt.Sprintf(
			"could not list the files of %d PR(s), so they were left out of the watched set: %s",
			len(failed), strings.Join(failed, "; ")), "error")
	}
	return rows, nil
}

// watchSearchQ builds the search string for a watch query. GitHub search
// returns at most 1000 results, so unless the query picks its own order the
// most recently updated PRs come first and any cut falls on the stalest ones.
func watchSearchQ(args []string) string {
	q := "is:pr is:open " + strings.Join(args, " ")
	for _, a := range args {
		if strings.HasPrefix(a, "sort:") {
			return q
		}
	}
	return q + " sort:updated-desc"
}

// filterTouching keeps the search nodes that change a file under one of paths,
// in search order. The search returns at most 100 paths per PR; a larger PR
// that shows no match among them is listed in full with listFiles before it is
// ruled out. Those listings are independent round trips, and a busy repository
// can need dozens, so they run concurrently. A PR whose listing fails is left
// out and returned in failed, as "URL: error", in search order.
func filterTouching(nodes []watchSearchNode, paths []string, listFiles func(url string) ([]string, error)) (rows []ghSearchActivityRow, failed []string) {
	keep := make([]bool, len(nodes))
	errs := make([]error, len(nodes))
	var full []int
	for i, n := range nodes {
		shown := n.filePaths()
		switch {
		case touchesWatchPaths(shown, paths):
			keep[i] = true
		case n.Files.TotalCount > len(shown):
			full = append(full, i)
		}
	}

	// List the large PRs' files on a bounded pool of workers
	work := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < prDetailWorkers && w < len(full); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				files, err := listFiles(nodes[i].URL)
				keep[i], errs[i] = err == nil && touchesWatchPaths(files, paths), err
			}
		}()
	}
	for _, i := range full {
		work <- i
	}
	close(work)
	wg.Wait()

	// Return the matches and failures in the order the search gave them
	for i, n := range nodes {
		switch {
		case keep[i]:
			rows = append(rows, n.searchRow())
		case errs[i] != nil:
			failed = append(failed, fmt.Sprintf("%s: %v", n.URL, errs[i]))
		}
	}
	return rows, failed
}

// parseWatchSearchPages decodes `gh api graphql --paginate` output, which is one
// JSON document per page written back to back.
func parseWatchSearchPages(out []byte) (watchScan, error) {
	dec := json.NewDecoder(bytes.NewReader(out))
	var scan watchScan
	for {
		var page watchSearchPage
		err := dec.Decode(&page)
		if errors.Is(err, io.EOF) {
			return scan, nil
		}
		if err != nil {
			return watchScan{}, err
		}

		// Every page reports the total; keep the largest in case it moved
		scan.matched = max(scan.matched, page.Data.Search.IssueCount)
		scan.returned += len(page.Data.Search.Nodes)

		// Search can return non-PR nodes as empty objects; skip them.
		for _, n := range page.Data.Search.Nodes {
			if n.URL != "" {
				scan.nodes = append(scan.nodes, n)
			}
		}
	}
}

// filePaths returns the changed paths the search included.
func (n watchSearchNode) filePaths() []string {
	paths := make([]string, 0, len(n.Files.Nodes))
	for _, f := range n.Files.Nodes {
		paths = append(paths, f.Path)
	}
	return paths
}

// searchRow converts a search node into the row shape `gh search prs` yields,
// so the rest of the open-PR path treats both sources alike.
func (n watchSearchNode) searchRow() ghSearchActivityRow {
	row := ghSearchActivityRow{
		Title:     n.Title,
		URL:       n.URL,
		State:     "open",
		IsDraft:   n.IsDraft,
		CreatedAt: n.CreatedAt,
		UpdatedAt: n.UpdatedAt,
		Author:    ghSearchUser{Login: n.Author.Login, IsBot: strings.EqualFold(n.Author.Typename, "Bot")},
	}
	for _, a := range n.Assignees.Nodes {
		row.Assignees = append(row.Assignees, ghSearchUser{Login: a.Login})
	}
	row.Repository.NameWithOwner = n.Repository.NameWithOwner
	return row
}

// listPRFiles lists every path a PR changes through the REST API, which pages
// past the search's 100-path window. The caller decides what a failure costs;
// the watch scan leaves the PR out and reports it rather than failing the
// whole collection.
func (c GitHubCollector) listPRFiles(ctx context.Context, run ghRun, prURL string) ([]string, error) {
	owner, name, number, ok := parsePRURL(prURL)
	if !ok {
		return nil, fmt.Errorf("not a pull request URL")
	}
	endpoint := fmt.Sprintf("repos/%s/%s/pulls/%s/files", owner, name, strconv.Itoa(number))
	args := []string{"api", endpoint, "--paginate", "--jq", ".[].filename"}
	out, err := runAndLogExecContext(ctx, "gh", args, "", run.env, run.opts, run.directive.ID)
	if err != nil {
		loggerFor(run.opts, run.directive.ID).Note("github: could not list files of %s: %v", prURL, err)
		return nil, err
	}
	// One filename per line; names may contain spaces.
	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimRight(line, "\r"); line != "" {
			paths = append(paths, line)
		}
	}
	return paths, nil
}

// normalizeWatchPaths trims each configured path to its bare form without
// surrounding slashes, so "services/api/" and "services/api" mean the same.
func normalizeWatchPaths(paths []string) []string {
	var out []string
	for _, p := range paths {
		if p = strings.Trim(strings.TrimSpace(p), "/"); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// touchesWatchPaths reports whether any changed path is one of the watched
// paths or lies under one. Matching is by whole path segment, so "services/api"
// does not match "services/api-gateway/main.go".
func touchesWatchPaths(changed, watched []string) bool {
	for _, c := range changed {
		for _, w := range watched {
			if c == w || strings.HasPrefix(c, w+"/") {
				return true
			}
		}
	}
	return false
}
