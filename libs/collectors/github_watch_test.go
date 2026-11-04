package collectors

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KurtPreston/docent/libs/config/userdata"
)

// watchQueries is a typical team-review setup: one relation unioning PRs that
// touch a directory with PRs that ask a team for review.
var watchQueries = []userdata.PRQuery{
	{Relation: "backport", Qualifiers: "author:app/ci-bot"},
	{Relation: "api", Qualifiers: "repo:o/r draft:false", Watch: true, Paths: []string{"/services/api/", " "}},
	{Relation: "api", Qualifiers: "repo:o/r team-review-requested:o/api-owners", Watch: true},
}

func TestBuildPRReviewSpecsWatchQueriesLead(t *testing.T) {
	specs := buildPRReviewSpecs(ScopeInvolved, "alice", watchQueries, nil)
	got := make([]string, 0, len(specs))
	for _, s := range specs {
		got = append(got, s.relation)
	}
	// Watch specs come before authored so a user's own watched PR keeps the
	// watch relation; owned declared queries keep their place after authored.
	want := "[api api authored backport review_requested reviewable]"
	if fmt.Sprint(got) != want {
		t.Fatalf("spec order = %v, want %s", got, want)
	}
	if !specs[0].watch || !specs[1].watch || specs[3].watch {
		t.Errorf("watch flags wrong: %+v", specs[:4])
	}
	// Paths are normalized to bare prefixes, and blanks dropped.
	if fmt.Sprint(specs[0].paths) != "[services/api]" {
		t.Errorf("paths = %v, want [services/api]", specs[0].paths)
	}
	if len(specs[1].paths) != 0 {
		t.Errorf("unfiltered watch query got paths %v", specs[1].paths)
	}

	// Watch queries follow other people's work, so self scope never runs them.
	if self := buildPRReviewSpecs(ScopeSelf, "alice", watchQueries, nil); len(self) != 1 || self[0].relation != "authored" {
		t.Errorf("self scope specs = %+v", self)
	}
}

// A closed watched PR is either the user's own, which the authored search
// already finds, or someone else's, which is not theirs to account for.
func TestBuildClosedPRSpecsSkipsWatchQueries(t *testing.T) {
	for _, s := range buildClosedPRSpecs(ScopeInvolved, "alice", watchQueries) {
		if s.relation == "api" {
			t.Errorf("closed search ran watch query: %+v", s)
		}
	}
}

func TestWatchSpecForRowSettlesOwnership(t *testing.T) {
	spec := prReviewSpec{relation: "api", watch: true}
	var mine, theirs ghSearchActivityRow
	mine.Author.Login = "Alice"
	theirs.Author.Login = "bob"

	own := spec.forRow(mine, "alice")
	if !own.mine || own.reviewable {
		t.Errorf("the user's own watched PR must stay owned: %+v", own)
	}
	other := spec.forRow(theirs, "alice")
	if other.mine || !other.reviewable {
		t.Errorf("someone else's watched PR must be a candidate: %+v", other)
	}

	// The resulting rows: only the user's own may pass a `self: true` guard.
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	d := userdata.Directive{ID: "gh", Collector: "github"}
	if it := prReviewItem(d, "alice", "github.com", now, theirs, other, prDetail{HeadSHA: "abc123"}); it.IsSelf || it.Author != "bob" || it.Fields["relation"] != "api" || it.Fields["head_sha"] != "abc123" {
		t.Errorf("other's row: IsSelf=%v Author=%q relation=%q head_sha=%q", it.IsSelf, it.Author, it.Fields["relation"], it.Fields["head_sha"])
	}
	if it := prReviewItem(d, "alice", "github.com", now, mine, own, prDetail{}); !it.IsSelf || it.Fields["mine"] != "true" {
		t.Errorf("own row: IsSelf=%v mine=%q", it.IsSelf, it.Fields["mine"])
	}

	// Specs other than watch queries decide ownership up front.
	backport := prReviewSpec{relation: "backport", mine: true}
	if got := backport.forRow(theirs, "alice"); !got.mine {
		t.Error("forRow changed a non-watch spec")
	}
}

func TestDedupeWatchBeatsAuthored(t *testing.T) {
	var r ghSearchActivityRow
	r.URL = "https://github.com/o/r/pull/1"
	rows := dedupePRReviewRows([]prReviewRow{
		{spec: prReviewSpec{relation: "api", watch: true, mine: true}, row: r},
		{spec: prReviewSpec{relation: "authored", mine: true}, row: r},
	})
	if len(rows) != 1 || rows[0].spec.relation != "api" || !rows[0].spec.mine {
		t.Fatalf("want the owned watch row to win, got %+v", rows)
	}
}

func TestCapReviewCandidatesExemptsWatchRows(t *testing.T) {
	var rows []prReviewRow
	for i := 0; i < maxReviewCandidates+5; i++ {
		var r ghSearchActivityRow
		r.URL = fmt.Sprintf("https://github.com/o/r/pull/%d", i)
		rows = append(rows, prReviewRow{spec: prReviewSpec{relation: "api", watch: true, reviewable: true}, row: r})
	}
	if out := capReviewCandidates(rows); len(out) != len(rows) {
		t.Errorf("cap dropped %d watched rows", len(rows)-len(out))
	}
}

func TestTouchesWatchPaths(t *testing.T) {
	watched := normalizeWatchPaths([]string{"services/api/", "README.md"})
	cases := map[string]bool{
		"services/api/main.go":         true,
		"services/api":                 true,
		"README.md":                    true,
		"services/api-gateway/main.go": false,
		"docs/services/api/x.md":       false,
		"README.md.bak":                false,
	}
	for path, want := range cases {
		if got := touchesWatchPaths([]string{"other.txt", path}, watched); got != want {
			t.Errorf("touchesWatchPaths(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestParseWatchSearchPages(t *testing.T) {
	// --paginate writes one JSON document per page back to back.
	out := []byte(`{"data":{"search":{"issueCount":5,"pageInfo":{"hasNextPage":true},"nodes":[
	  {"title":"Add endpoint","url":"https://github.com/o/r/pull/1","isDraft":false,
	   "createdAt":"2026-05-01T00:00:00Z","updatedAt":"2026-05-02T00:00:00Z",
	   "author":{"login":"renovate","__typename":"Bot"},
	   "assignees":{"nodes":[{"login":"bob"}]},
	   "repository":{"nameWithOwner":"o/r"},
	   "files":{"totalCount":2,"nodes":[{"path":"services/api/a.go"},{"path":"go.mod"}]}},
	  {}]}}}
	{"data":{"search":{"issueCount":4,"pageInfo":{"hasNextPage":false},"nodes":[
	  {"title":"Docs","url":"https://github.com/o/r/pull/2","author":{"login":"carol","__typename":"User"},
	   "repository":{"nameWithOwner":"o/r"},"files":{"totalCount":1,"nodes":[{"path":"docs/x.md"}]}}]}}}`)
	scan, err := parseWatchSearchPages(out)
	if err != nil {
		t.Fatal(err)
	}
	nodes := scan.nodes
	if scan.matched != 5 || scan.returned != 3 {
		t.Errorf("matched %d, returned %d; want 5 (largest issueCount) and 3 (every node, PR or not)", scan.matched, scan.returned)
	}
	if len(nodes) != 2 {
		t.Fatalf("got %d nodes, want 2 (the empty non-PR node is skipped)", len(nodes))
	}
	row := nodes[0].searchRow()
	if row.Title != "Add endpoint" || row.State != "open" || row.Repository.NameWithOwner != "o/r" {
		t.Errorf("row = %+v", row)
	}
	if !row.Author.IsBot || row.Author.Login != "renovate" {
		t.Errorf("author = %+v, want bot renovate", row.Author)
	}
	if assigneeLogins(row) != "bob" {
		t.Errorf("assignees = %q", assigneeLogins(row))
	}
	if fmt.Sprint(nodes[0].filePaths()) != "[services/api/a.go go.mod]" {
		t.Errorf("paths = %v", nodes[0].filePaths())
	}
	if _, err := parseWatchSearchPages([]byte(`{"data":`)); err == nil {
		t.Error("expected truncated output to fail")
	}
}

// TestWatchSearchQSortsByUpdated checks that a watch search asks for the most
// recently updated PRs first, so GitHub's 1000-result cap drops the stalest,
// unless the query already picks an order.
func TestWatchSearchQSortsByUpdated(t *testing.T) {
	if got, want := watchSearchQ([]string{"repo:my-org/my-repo", "draft:false"}), "is:pr is:open repo:my-org/my-repo draft:false sort:updated-desc"; got != want {
		t.Fatalf("q = %q, want %q", got, want)
	}
	if got, want := watchSearchQ([]string{"repo:my-org/my-repo", "sort:created-asc"}), "is:pr is:open repo:my-org/my-repo sort:created-asc"; got != want {
		t.Fatalf("q = %q, want %q", got, want)
	}
}

// TestFilterTouchingListsOnlyLargeMisses checks that a PR is kept when its
// listed paths match, that only a PR with more paths than the search showed is
// listed in full, that matches keep the search's order, and that a PR whose
// listing fails is left out and reported.
func TestFilterTouchingListsOnlyLargeMisses(t *testing.T) {
	node := func(url string, total int, paths ...string) watchSearchNode {
		n := watchSearchNode{URL: url}
		n.Files.TotalCount = total
		for _, p := range paths {
			n.Files.Nodes = append(n.Files.Nodes, struct {
				Path string `json:"path"`
			}{p})
		}
		return n
	}
	nodes := []watchSearchNode{
		node("u/1", 1, "services/api/a.go"),   // match in the search
		node("u/2", 1, "web/a.ts"),            // small miss, ruled out as-is
		node("u/3", 500, "web/b.ts"),          // large miss, match in full list
		node("u/4", 500, "web/c.ts"),          // large miss, no match in full list
		node("u/5", 300, "services/api/b.go"), // large but already matching
		node("u/6", 500, "web/d.ts"),          // large miss, listing fails
	}
	full := map[string][]string{"u/3": {"web/b.ts", "services/api/c.go"}, "u/4": {"web/c.ts"}}

	// Record which PRs were listed in full
	var mu sync.Mutex
	var listed []string
	rows, failed := filterTouching(nodes, []string{"services/api"}, func(url string) ([]string, error) {
		mu.Lock()
		defer mu.Unlock()
		listed = append(listed, url)
		if url == "u/6" {
			return nil, errors.New("HTTP 502")
		}
		return full[url], nil
	})

	// Expect matches in search order, and full listings only for large misses
	var got []string
	for _, r := range rows {
		got = append(got, r.URL)
	}
	sort.Strings(listed)
	if strings.Join(got, ",") != "u/1,u/3,u/5" || strings.Join(listed, ",") != "u/3,u/4,u/6" {
		t.Fatalf("rows = %v, listed = %v", got, listed)
	}
	if strings.Join(failed, ",") != "u/6: HTTP 502" {
		t.Fatalf("failed = %v", failed)
	}
}
