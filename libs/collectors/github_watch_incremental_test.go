package collectors

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

var hubT0 = time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)

// hubPRs builds n open PRs created a day apart, oldest first, and updated in
// the opposite order, so creation and update order disagree. Every one
// touches the watched path unless touching says otherwise.
func hubPRs(n int, touching func(int) bool) []hubPR {
	prs := make([]hubPR, 0, n)
	for i := 1; i <= n; i++ {
		path := "docs/readme.md"
		if touching == nil || touching(i) {
			path = "services/api/main.go"
		}
		prs = append(prs, hubPR{
			Number: i, Author: "teammate", Paths: []string{path},
			Created: hubT0.Add(-time.Duration(48-i) * 24 * time.Hour),
			Updated: hubT0.Add(-time.Duration(i) * time.Hour),
			Head:    fmt.Sprintf("h%d-1", i), Base: "main",
		})
	}
	return prs
}

// pollHub collects state at a moment, with or without a cache directory, and
// returns the items and the `gh` calls it made.
func pollHub(t *testing.T, hub *fakeHub, at time.Time, cacheDir string) ([]StatusItem, []string) {
	t.Helper()
	hub.sync()
	before := len(hub.calls())
	opts := &CollectOpts{Mode: ModeState, Scope: ScopeInvolved, Until: at, CacheDir: cacheDir}
	items, err := GitHubCollector{Clock: func() time.Time { return at }}.CollectState(context.Background(), watchDirective(), opts)
	if err != nil {
		t.Fatalf("CollectState at %s: %v", at.Format(time.Kitchen), err)
	}
	return sortedItems(items), hub.calls()[before:]
}

// checkPoll runs a cached poll and a full scan of the same PRs, and fails
// unless they report the same items and those match the PRs the fixture says
// are watched. It returns the cached poll's calls.
func checkPoll(t *testing.T, hub *fakeHub, at time.Time, cacheDir string) []string {
	t.Helper()
	warm, calls := pollHub(t, hub, at, cacheDir)
	cold, _ := pollHub(t, hub, at, "")
	if !reflect.DeepEqual(warm, cold) {
		t.Fatalf("at %s the cached poll differs from a full scan:\ncached %s\n  full %s", at.Format(time.Kitchen), describe(warm), describe(cold))
	}
	if got, want := watchedNumbers(warm), hubTruth(hub); !reflect.DeepEqual(got, want) {
		t.Fatalf("at %s: watched %v, want %v", at.Format(time.Kitchen), got, want)
	}
	return calls
}

// hubTruth lists the PRs a complete scan should report: open, matching, and
// touching the watched path.
func hubTruth(hub *fakeHub) []int {
	var out []int
	for _, p := range hub.State.PRs {
		if !p.Closed && !p.Unmatched && touchesWatchPaths(p.Paths, []string{"services/api"}) {
			out = append(out, p.Number)
		}
	}
	sort.Ints(out)
	return out
}

// watchedNumbers lists the PR numbers among the review rows.
func watchedNumbers(items []StatusItem) []int {
	var out []int
	for _, it := range items {
		if it.Kind == "pr_review_status" {
			var n int
			fmt.Sscanf(it.URL[strings.LastIndex(it.URL, "/")+1:], "%d", &n)
			out = append(out, n)
		}
	}
	sort.Ints(out)
	return out
}

// sortedItems orders items so two collections can be compared.
func sortedItems(items []StatusItem) []StatusItem {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		if items[i].URL != items[j].URL {
			return items[i].URL < items[j].URL
		}
		return items[i].Title < items[j].Title
	})
	return items
}

// describe summarizes items for a failure message.
func describe(items []StatusItem) string {
	var parts []string
	for _, it := range items {
		parts = append(parts, it.Kind+" "+it.URL+" "+it.Title)
	}
	return strings.Join(parts, " | ")
}

// countMatching counts calls containing every one of substrs.
func countMatching(calls []string, substrs ...string) int {
	n := 0
	for _, c := range calls {
		all := true
		for _, s := range substrs {
			all = all && strings.Contains(c, s)
		}
		if all {
			n++
		}
	}
	return n
}

func TestWatchCachePollsMatchFullScans(t *testing.T) {
	prs := hubPRs(8, func(i int) bool { return i != 2 && i != 6 })
	// PRs 3 and 4 change more files than a search node shows
	prs[2].Paths = []string{"docs/1.md", "docs/2.md", "services/api/z.go"}
	prs[3].Paths = []string{"docs/1.md", "docs/2.md", "docs/3.md"}
	hub := newFakeHub(t, hubState{PRs: prs, PageSize: 2, Shown: 2})
	cache := t.TempDir()
	at := func(m int) time.Time { return hubT0.Add(time.Duration(m) * time.Minute) }

	// A cold poll scans in full and lists the two large PRs' files
	calls := checkPoll(t, hub, at(0), cache)
	if n := countMatching(calls, "/files"); n != 2 {
		t.Fatalf("cold poll listed files %d times, want 2: %v", n, calls)
	}

	// An unchanged poll reuses every verdict and searches only for changes
	calls = checkPoll(t, hub, at(15), cache)
	if n := countMatching(calls, "/files"); n != 0 {
		t.Fatalf("unchanged poll listed files %d times: %v", n, calls)
	}
	if n := countMatching(calls, "updated:>="); n != 1 {
		t.Fatalf("want one update search, got %d: %v", n, calls)
	}
	if n := countMatching(calls, "sort:created-desc"); n != watchSlicePages {
		t.Fatalf("want %d slice pages, got %d: %v", watchSlicePages, n, calls)
	}
	if n := countMatching(calls, "sort:updated-desc") - countMatching(calls, "updated:>="); n != 0 {
		t.Fatalf("unchanged poll ran %d full scans: %v", n, calls)
	}
	if n := countMatching(calls, "-F number="); n != len(hubTruth(hub)) {
		t.Fatalf("want a detail lookup per watched PR (%d), got %d", len(hubTruth(hub)), n)
	}

	// A new PR, a push that brings a PR in, a retarget, and a close
	hub.State.PRs = append(hub.State.PRs, hubPR{Number: 9, Author: "teammate", Paths: []string{"services/api/new.go"}, Created: at(20), Updated: at(20), Head: "h9-1", Base: "main"})
	p2 := hub.pr(2)
	p2.Paths, p2.Head, p2.Updated = []string{"services/api/b.go"}, "h2-2", at(20)
	p4 := hub.pr(4)
	p4.Paths, p4.Base, p4.Updated = []string{"docs/1.md", "docs/2.md", "services/api/q.go"}, "release", at(20)
	p7 := hub.pr(7)
	p7.Closed, p7.Updated = true, at(20)
	calls = checkPoll(t, hub, at(30), cache)
	if got := countMatching(calls, "/files"); got != 1 || countMatching(calls, "/pulls/4/files") != 1 {
		t.Fatalf("want only the retargeted large PR relisted, got %v", calls)
	}

	// A label change that takes a PR out of the query
	p8 := hub.pr(8)
	p8.Unmatched, p8.Updated = true, at(35)
	checkPoll(t, hub, at(45), cache)

	// A push to a large PR whose listing then fails is reported, not dropped
	p3 := hub.pr(3)
	p3.Head, p3.Updated, p3.FailFiles = "h3-2", at(50), true
	warm, _ := pollHub(t, hub, at(60), cache)
	cold, _ := pollHub(t, hub, at(60), "")
	if !reflect.DeepEqual(warm, cold) {
		t.Fatalf("failed listing: cached %s\n full %s", describe(warm), describe(cold))
	}
	if got := watchedNumbers(warm); !reflect.DeepEqual(got, []int{1, 2, 4, 5, 9}) {
		t.Fatalf("failed listing: watched %v", got)
	}
	if errs := collectorErrors(warm); len(errs) != 1 || !strings.Contains(errs[0].Title, "file listings failed") {
		t.Fatalf("want the failed listing reported, got %+v", errs)
	}

	// Once listing works again the cached unknown is retried without an update
	p3.FailFiles = false
	calls = checkPoll(t, hub, at(75), cache)
	if countMatching(calls, "/pulls/3/files") != 1 {
		t.Fatalf("want the failed listing retried, got %v", calls)
	}
}

func TestWatchCacheReportsFailedSliceAndKeepsCursor(t *testing.T) {
	hub := newFakeHub(t, hubState{PRs: hubPRs(12, nil), PageSize: 2, Shown: 2})
	cache := t.TempDir()
	at := func(m int) time.Time { return hubT0.Add(time.Duration(m) * time.Minute) }
	checkPoll(t, hub, at(0), cache)
	checkPoll(t, hub, at(15), cache)
	before := readOnlyCache(t, cache)

	// A failed slice is a warning; the rest of the poll still matches
	hub.State.FailSlices = true
	warm, _ := pollHub(t, hub, at(30), cache)
	cold, _ := pollHub(t, hub, at(30), "")
	errs := collectorErrors(warm)
	if len(errs) != 1 || !strings.Contains(errs[0].Title, `watch "api" reconciliation failed`) || errs[0].Severity != "warning" {
		t.Fatalf("want one reconciliation warning, got %+v", errs)
	}
	if !reflect.DeepEqual(withoutErrors(warm), withoutErrors(cold)) {
		t.Fatalf("watched rows differ:\ncached %s\n  full %s", describe(warm), describe(cold))
	}
	if after := readOnlyCache(t, cache); after.Cursor != before.Cursor || after.Covered != before.Covered {
		t.Fatalf("failed slice moved the cursor from %q to %q", before.Cursor, after.Cursor)
	}
}

func TestWatchCacheCoversTruncatedScan(t *testing.T) {
	hub := newFakeHub(t, hubState{PRs: hubPRs(10, nil), Cap: 4, PageSize: 2, Shown: 2})
	cache := t.TempDir()
	at := func(m int) time.Time { return hubT0.Add(time.Duration(m) * time.Minute) }

	// The cold scan sees only the cap, and says so
	warm, _ := pollHub(t, hub, at(0), cache)
	if got := watchedNumbers(warm); len(got) != 4 {
		t.Fatalf("cold scan watched %v, want the 4 under the cap", got)
	}
	if errs := collectorErrors(warm); len(errs) != 1 || !strings.Contains(errs[0].Title, "scan truncated") {
		t.Fatalf("want the truncation reported, got %+v", errs)
	}

	// Each poll's slice finds more, which the cap also limits, and the
	// warning stays until a lap completes
	polls := 0
	for len(collectorErrors(warm)) > 0 {
		if polls++; polls > 10 {
			t.Fatalf("no full lap after %d polls: %v", polls-1, watchedNumbers(warm))
		}
		warm, _ = pollHub(t, hub, at(15*polls), cache)
	}
	if polls < 2 {
		t.Fatalf("coverage claimed after %d poll(s), before a slice could reach every PR", polls)
	}
	if errs := collectorErrors(warm); len(errs) != 0 {
		t.Fatalf("after a full lap: want no warning, got %+v", errs)
	}
	if got, want := watchedNumbers(warm), hubTruth(hub); !reflect.DeepEqual(got, want) {
		t.Fatalf("after a full lap: watched %v, want %v", got, want)
	}
}

func TestWatchCacheSettlesRecentUpdatesNextPoll(t *testing.T) {
	hub := newFakeHub(t, hubState{PRs: hubPRs(6, nil), PageSize: 1, Shown: 2})
	cache := t.TempDir()
	at := func(m int) time.Time { return hubT0.Add(time.Duration(m) * time.Minute) }
	checkPoll(t, hub, at(0), cache)

	// PR 1, the oldest and outside this poll's slice, leaves the query just
	// before the poll: too recent to tell from indexing lag, so it stays
	p1 := hub.pr(1)
	p1.Unmatched, p1.Updated = true, at(14)
	warm, _ := pollHub(t, hub, at(15), cache)
	if got := watchedNumbers(warm); !reflect.DeepEqual(got, []int{1, 2, 3, 4, 5, 6}) {
		t.Fatalf("want PR 1 kept through the grace period, got %v", got)
	}

	// The next poll settles it
	checkPoll(t, hub, at(30), cache)
}

func TestWatchCacheHandsOwnPRThatLeftToAuthoredSearch(t *testing.T) {
	prs := hubPRs(6, nil)
	prs[0].Author = "my-user"
	hub := newFakeHub(t, hubState{PRs: prs, PageSize: 1, Shown: 2})
	cache := t.TempDir()
	at := func(m int) time.Time { return hubT0.Add(time.Duration(m) * time.Minute) }
	checkPoll(t, hub, at(0), cache)

	// The user's PR leaves the watch query but is still theirs and open
	p1 := hub.pr(1)
	p1.Unmatched, p1.Updated = true, at(5)
	warm, _ := pollHub(t, hub, at(15), cache)
	cold, _ := pollHub(t, hub, at(15), "")
	if !reflect.DeepEqual(warm, cold) {
		t.Fatalf("cached %s\n  full %s", describe(warm), describe(cold))
	}
	for _, it := range warm {
		if it.URL == hubURL(1) && it.Fields["relation"] != "authored" {
			t.Fatalf("want PR 1 reported by the authored search, got %q", it.Fields["relation"])
		}
	}
}

func TestWatchCacheSkipsQueriesWithTheirOwnDatesOrSort(t *testing.T) {
	for _, q := range []string{"sort:created-asc", "updated:>2026-01-01", "-created:<2025-01-01"} {
		if watchCacheable([]string{"repo:my-org/my-repo", q}) {
			t.Errorf("%q should be scanned in full", q)
		}
	}
	if !watchCacheable([]string{"repo:my-org/my-repo", "draft:false"}) {
		t.Error("a plain query should be cacheable")
	}
}

// readOnlyCache loads the single watch cache file under dir.
func readOnlyCache(t *testing.T, dir string) *watchCache {
	t.Helper()
	q := watchSearchQ([]string{"repo:my-org/my-repo", "draft:false"})
	c, err := loadWatchCache(watchCachePath(dir, watchCacheKey(watchViewer{"github.com", "my-user", "gh"}, q, []string{"services/api"})))
	if err != nil || c == nil {
		t.Fatalf("load cache: %v, %v", c, err)
	}
	return c
}
