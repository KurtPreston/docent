package collectors

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/KurtPreston/docent/libs/config/userdata"
	"github.com/KurtPreston/docent/libs/prstatus"
)

func TestBuildGitHubSearchSpecsSelf(t *testing.T) {
	prs, commits := buildGitHubSearchSpecs(ScopeSelf, "alice", "2026-05-01", nil)
	if len(prs) != 1 {
		t.Fatalf("self should yield 1 search (authored prs), got %d: %+v", len(prs), prs)
	}
	if prs[0].itemKind != "authored_pr" || !prs[0].userAnchored {
		t.Errorf("expected user-anchored authored_pr query, got %+v", prs[0])
	}
	if !containsArg(prs[0].args, "--author") {
		t.Errorf("self prs query missing --author: %v", prs[0].args)
	}
	if len(commits) != 1 || !commits[0].userAnchored || !containsArg(commits[0].args, "--author") {
		t.Errorf("self commits should be 1 user-anchored --author search, got %+v", commits)
	}
}

func TestBuildGitHubSearchSpecsInvolved(t *testing.T) {
	prs, commits := buildGitHubSearchSpecs(ScopeInvolved, "alice", "2026-05-01", nil)
	// The user-anchored involved set is 5 PR/issue searches: --author,
	// --reviewed-by, --involves issues, is:issue --commenter, --commenter prs.
	if len(prs) != 5 {
		t.Fatalf("involved should yield 5 PR/issue searches, got %d", len(prs))
	}
	for i, q := range prs {
		if !q.userAnchored {
			t.Errorf("involved query[%d] should be user-anchored: %+v", i, q)
		}
	}
	if len(commits) != 1 {
		t.Fatalf("involved should yield 1 commits search, got %d", len(commits))
	}
}

func TestBuildGitHubSearchSpecsAllNoFollowedRepos(t *testing.T) {
	prs, commits := buildGitHubSearchSpecs(ScopeAll, "alice", "2026-05-01", nil)
	involvedPRs, involvedCommits := buildGitHubSearchSpecs(ScopeInvolved, "alice", "2026-05-01", nil)
	if len(prs) != len(involvedPRs) {
		t.Errorf("all without followed_repos should fall back to involved: prs=%d involved=%d", len(prs), len(involvedPRs))
	}
	if len(commits) != len(involvedCommits) {
		t.Errorf("all without followed_repos should fall back to involved: commits=%d involved=%d", len(commits), len(involvedCommits))
	}
}

func TestBuildGitHubSearchSpecsAllWithFollowedRepos(t *testing.T) {
	prs, commits := buildGitHubSearchSpecs(ScopeAll, "alice", "2026-05-01", []string{"rust-lang/rust", "golang/go"})

	// 5 involved searches + 2 followed repos * 2 (prs + issues) = 9.
	if len(prs) != 9 {
		t.Fatalf("all+2 followed_repos should yield 9 PR/issue searches, got %d: %+v", len(prs), prs)
	}
	// 1 involved commit search + 2 followed repo commit searches = 3.
	if len(commits) != 3 {
		t.Fatalf("all+2 followed_repos should yield 3 commit searches, got %d", len(commits))
	}

	// Repo-scoped searches must NOT be marked user-anchored (so IsSelf
	// is decided by author equality, not assumed).
	var repoScoped int
	for _, q := range prs {
		if !q.userAnchored {
			repoScoped++
			if !containsArg(q.args, "--repo") {
				t.Errorf("non-user-anchored query missing --repo: %v", q.args)
			}
		}
	}
	if repoScoped != 4 {
		t.Errorf("expected 4 repo-scoped pr/issue searches, got %d", repoScoped)
	}
	var repoScopedCommits int
	for _, q := range commits {
		if !q.userAnchored {
			repoScopedCommits++
			if !containsArg(q.args, "--repo") {
				t.Errorf("non-user-anchored commit query missing --repo: %v", q.args)
			}
		}
	}
	if repoScopedCommits != 2 {
		t.Errorf("expected 2 repo-scoped commit searches, got %d", repoScopedCommits)
	}
}

func TestBuildGitHubSearchFieldsPR(t *testing.T) {
	var row ghSearchActivityRow
	row.State = "open"
	row.IsDraft = true
	row.CreatedAt = "2026-05-01T10:00:00Z"
	row.UpdatedAt = "2026-05-02T10:00:00Z"
	spec := ghSearchSpec{queryType: "prs", summary: "author:alice"}

	fields := buildGitHubSearchFields(spec, "alice", "github.com", "o/r", row)
	if fields["is_draft"] != "true" {
		t.Errorf("is_draft = %q, want true", fields["is_draft"])
	}
	if fields["created_at"] != "2026-05-01T10:00:00Z" {
		t.Errorf("created_at = %q", fields["created_at"])
	}
	if fields["state"] != "open" || fields["updated_at"] != "2026-05-02T10:00:00Z" {
		t.Errorf("unexpected base fields: %v", fields)
	}
}

func TestBuildGitHubSearchFieldsIssueOmitsPRFields(t *testing.T) {
	var row ghSearchActivityRow
	row.State = "open"
	spec := ghSearchSpec{queryType: "issues", summary: "involves:alice"}

	fields := buildGitHubSearchFields(spec, "alice", "github.com", "o/r", row)
	if _, ok := fields["is_draft"]; ok {
		t.Errorf("issue rows should not carry is_draft: %v", fields)
	}
	if _, ok := fields["created_at"]; ok {
		t.Errorf("issue rows should not carry created_at: %v", fields)
	}
}

func TestBuildGitHubSearchFieldsPROmitsEmptyCreatedAt(t *testing.T) {
	var row ghSearchActivityRow
	row.State = "closed"
	spec := ghSearchSpec{queryType: "prs", summary: "author:alice"}

	fields := buildGitHubSearchFields(spec, "alice", "github.com", "o/r", row)
	if _, ok := fields["created_at"]; ok {
		t.Errorf("empty createdAt should be omitted: %v", fields)
	}
	if fields["is_draft"] != "false" {
		t.Errorf("is_draft = %q, want false", fields["is_draft"])
	}
}

func TestDedupeGitHubItems(t *testing.T) {
	now := time.Now().UTC()
	items := []StatusItem{
		{Kind: "authored_pr", URL: "https://github.com/o/r/pull/1", Title: "feat", ObservedAt: now, IsSelf: true},
		{Kind: "reviewed_pr", URL: "https://github.com/o/r/pull/1", Title: "feat", ObservedAt: now, IsSelf: true},
		{Kind: "repo_pr", URL: "https://github.com/o/r/pull/1", Title: "feat", ObservedAt: now, IsSelf: false},
		{Kind: "repo_pr", URL: "https://github.com/o/r/pull/2", Title: "other", ObservedAt: now, IsSelf: false},
	}
	out := dedupeGitHubItems(items)
	if len(out) != 2 {
		t.Fatalf("expected 2 unique URLs, got %d: %#v", len(out), out)
	}
	if !out[0].IsSelf {
		t.Errorf("first URL kept IsSelf=true winner: %#v", out[0])
	}
}

func TestDedupeGitHubItemsIsSelfWins(t *testing.T) {
	now := time.Now().UTC()
	items := []StatusItem{
		{Kind: "repo_pr", URL: "https://github.com/o/r/pull/1", ObservedAt: now, IsSelf: false},
		{Kind: "authored_pr", URL: "https://github.com/o/r/pull/1", ObservedAt: now, IsSelf: true},
	}
	out := dedupeGitHubItems(items)
	if len(out) != 1 {
		t.Fatalf("expected 1 deduped item, got %d", len(out))
	}
	if !out[0].IsSelf {
		t.Fatal("IsSelf=true should win after merge")
	}
}

func TestRollupChecksState(t *testing.T) {
	cases := []struct {
		name   string
		rollup []ghCheckRollupEntry
		want   string
	}{
		{name: "empty is none", rollup: nil, want: "none"},
		{
			name: "all success passing",
			rollup: []ghCheckRollupEntry{
				{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "SUCCESS"},
				{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "SKIPPED"},
				{Typename: "StatusContext", State: "SUCCESS"},
			},
			want: "passing",
		},
		{
			name: "in progress pending",
			rollup: []ghCheckRollupEntry{
				{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "SUCCESS"},
				{Typename: "CheckRun", Status: "IN_PROGRESS"},
			},
			want: "pending",
		},
		{
			name: "any failure fails over pending",
			rollup: []ghCheckRollupEntry{
				{Typename: "CheckRun", Status: "IN_PROGRESS"},
				{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "FAILURE"},
			},
			want: "failing",
		},
		{
			name: "status context failure",
			rollup: []ghCheckRollupEntry{
				{Typename: "StatusContext", State: "FAILURE"},
			},
			want: "failing",
		},
		{
			name: "status context pending",
			rollup: []ghCheckRollupEntry{
				{Typename: "StatusContext", State: "PENDING"},
			},
			want: "pending",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rollupChecksState(tc.rollup); got != tc.want {
				t.Fatalf("rollupChecksState = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPrReviewItemFields(t *testing.T) {
	now := time.Now().UTC()
	var row ghSearchActivityRow
	row.Title = "feat: thing"
	row.URL = "https://github.com/o/r/pull/7"
	row.IsDraft = true
	row.CreatedAt = "2026-05-04T09:00:00Z"
	row.Repository.NameWithOwner = "o/r"

	authoredSpec := prReviewSpec{relation: "authored", mine: true}
	threads := []ReviewThread{
		{ID: "t1", Author: "bob", Body: "why?", Mine: false},
		{ID: "t2", Author: "bob", Body: "and this?", Mine: true},
	}
	authoredDetail := prDetail{
		Checks: "passing", ReviewDecision: "APPROVED", HeadBranch: "feature-branch",
		Mergeable: "conflicting", Threads: threads,
		Status: prstatus.Result{
			Bucket: prstatus.ReadyToMerge,
			Side:   prstatus.SideAuthor,
			At:     time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC),
		},
	}
	authored := prReviewItem(userdata.Directive{ID: "gh", Collector: "github"}, "alice", "github.com", now, row, authoredSpec, authoredDetail)
	if authored.Kind != "pr_review_status" {
		t.Fatalf("kind = %q", authored.Kind)
	}
	for k, want := range map[string]string{
		"relation": "authored", "mine": "true", "is_draft": "true", "checks": "passing",
		"review_decision": "APPROVED", "repo": "o/r",
		"head_branch": "feature-branch", "mergeable": "conflicting",
		"created_at": "2026-05-04T09:00:00Z",
		// A draft is never ready, whatever its checks say. That invariant now
		// lives here rather than at each call site.
		"ready": "false",
		// Both threads are unresolved, but only the one whose last comment is
		// someone else's is my turn.
		"unresolved": "2", "unresolved_mine": "1",
		// The classification travels alongside its inputs so consumers do not
		// re-derive it.
		"bucket": "ready_to_merge", "last_action": "author",
		"last_action_at": "2026-05-04T10:00:00Z",
	} {
		if authored.Fields[k] != want {
			t.Errorf("authored field %q = %q, want %q", k, authored.Fields[k], want)
		}
	}
	var gotThreads []ReviewThread
	if err := json.Unmarshal([]byte(authored.Fields["unresolved_threads"]), &gotThreads); err != nil {
		t.Fatalf("unresolved_threads is not valid JSON: %v", err)
	}
	if len(gotThreads) != 2 || gotThreads[0].ID != "t1" {
		t.Errorf("threads round-tripped as %+v", gotThreads)
	}

	// review-requested rows omit the owned-only fields but carry head_branch.
	rrSpec := prReviewSpec{relation: "review_requested", mine: false}
	rr := prReviewItem(userdata.Directive{ID: "gh", Collector: "github"}, "alice", "github.com", now, row, rrSpec, prDetail{HeadBranch: "their-branch"})
	if rr.Fields["relation"] != "review_requested" {
		t.Errorf("relation = %q", rr.Fields["relation"])
	}
	if rr.Fields["mine"] != "false" {
		t.Errorf("mine = %q, want false", rr.Fields["mine"])
	}
	if _, ok := rr.Fields["review_decision"]; ok {
		t.Errorf("review_requested row should not carry review_decision: %v", rr.Fields)
	}
	if _, ok := rr.Fields["mergeable"]; ok {
		t.Errorf("review_requested row should not carry mergeable: %v", rr.Fields)
	}
	if rr.Fields["head_branch"] != "their-branch" {
		t.Errorf("head_branch = %q, want their-branch", rr.Fields["head_branch"])
	}

	// A directive-declared query is owned, so it resolves the same status
	// fields as an authored PR under its own relation label.
	declared := prReviewSpec{relation: "backport", mine: true}
	bp := prReviewItem(userdata.Directive{ID: "gh", Collector: "github"}, "alice", "github.com", now, row, declared,
		prDetail{Checks: "failing", HeadBranch: "backport/pr-1", Mergeable: "mergeable"})
	for k, want := range map[string]string{
		"relation": "backport", "mine": "true", "checks": "failing",
		"mergeable": "mergeable", "head_branch": "backport/pr-1",
	} {
		if bp.Fields[k] != want {
			t.Errorf("declared-query field %q = %q, want %q", k, bp.Fields[k], want)
		}
	}
	// An unclassifiable PR omits the bucket rather than defaulting to one, so a
	// consumer can tell "docent could not tell" from "awaiting review".
	for _, k := range []string{"bucket", "last_action", "last_action_at"} {
		if _, ok := bp.Fields[k]; ok {
			t.Errorf("unclassified PR should omit %q, got %q", k, bp.Fields[k])
		}
	}
}

func TestPrReviewItemCandidate(t *testing.T) {
	now := time.Now().UTC()
	var row ghSearchActivityRow
	row.Title = "their feature"
	row.URL = "https://github.com/o/r/pull/9"
	row.Repository.NameWithOwner = "o/r"
	row.Author = ghSearchUser{Login: "bob"}
	row.Assignees = []ghSearchUser{{Login: "carol"}, {Login: "dave"}}

	spec := prReviewSpec{relation: relationReviewable, reviewable: true}
	detail := prDetail{
		Checks: "passing", ReviewDecision: "REVIEW_REQUIRED", Mergeable: "mergeable",
		Threads: []ReviewThread{{ID: "t1", Author: "carol"}},
		Status:  prstatus.Result{Bucket: prstatus.AwaitingReview, Side: prstatus.SideAuthor},
	}
	item := prReviewItem(userdata.Directive{ID: "gh", Collector: "github"}, "alice", "github.com", now, row, spec, detail)

	// The guard that keeps a rule like "fix my failing builds" from reaching
	// somebody else's PR. Losing it is the difference between a review queue
	// and an agent pushing commits at a teammate.
	if item.IsSelf {
		t.Error("a candidate PR must not be marked as the user's own activity")
	}
	if item.Author != "bob" {
		t.Errorf("author = %q, want the PR's own author", item.Author)
	}
	for k, want := range map[string]string{
		"relation": relationReviewable, "mine": "false", "reviewable": "true",
		"pr_author": "bob", "assignees": "carol,dave",
		// Resolved like an owned PR: the bucket is the whole reason a candidate
		// is worth listing, since it says whether anyone is still waiting.
		"checks": "passing", "bucket": "awaiting_review", "unresolved": "1",
	} {
		if item.Fields[k] != want {
			t.Errorf("candidate field %q = %q, want %q", k, item.Fields[k], want)
		}
	}
	// Owned-only fields stay owned-only: "ready" and "my turn" are statements
	// about the user, and the threads themselves are follow-up work they do not
	// have.
	for _, k := range []string{"ready", "unresolved_mine", "unresolved_threads"} {
		if _, ok := item.Fields[k]; ok {
			t.Errorf("candidate should omit %q, got %q", k, item.Fields[k])
		}
	}
}

func TestParsePRURL(t *testing.T) {
	cases := []struct {
		raw         string
		owner, name string
		number      int
		ok          bool
	}{
		// An enterprise host must parse the same as github.com.
		{"https://git.drwholdings.com/Chip/salsa/pull/7664", "Chip", "salsa", 7664, true},
		{"https://github.com/o/r/pull/7", "o", "r", 7, true},
		{"https://github.com/o/r/pull/7/files", "o", "r", 7, true},
		{"https://github.com/o/r/issues/7", "", "", 0, false},
		{"https://github.com/o/r/pull/notanumber", "", "", 0, false},
		{"", "", "", 0, false},
	}
	for _, tc := range cases {
		owner, name, number, ok := parsePRURL(tc.raw)
		if ok != tc.ok || owner != tc.owner || name != tc.name || number != tc.number {
			t.Errorf("parsePRURL(%q) = (%q,%q,%d,%v), want (%q,%q,%d,%v)",
				tc.raw, owner, name, number, ok, tc.owner, tc.name, tc.number, tc.ok)
		}
	}
}

func TestReviewThreadsResponseFiltersNoise(t *testing.T) {
	// Shape captured from a real gh api graphql reply.
	raw := `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[
    {"id":"open","isResolved":false,"isOutdated":false,"path":"a.ts","line":12,
     "comments":{"nodes":[
       {"author":{"login":"bob"},"body":"why this?","url":"u1","createdAt":"2026-01-01T00:00:00Z"}]}},
    {"id":"answered","isResolved":false,"isOutdated":false,"path":"b.ts","line":3,
     "comments":{"nodes":[
       {"author":{"login":"bob"},"body":"q","url":"u2","createdAt":"2026-01-01T00:00:00Z"},
       {"author":{"login":"alice"},"body":"answered","url":"u3","createdAt":"2026-01-02T00:00:00Z"}]}},
    {"id":"resolved","isResolved":true,"isOutdated":false,"path":"c.ts",
     "comments":{"nodes":[{"author":{"login":"bob"},"body":"x","url":"u4","createdAt":"2026-01-01T00:00:00Z"}]}},
    {"id":"outdated","isResolved":false,"isOutdated":true,"path":"d.ts",
     "comments":{"nodes":[{"author":{"login":"bob"},"body":"y","url":"u5","createdAt":"2026-01-01T00:00:00Z"}]}},
    {"id":"empty","isResolved":false,"isOutdated":false,"path":"e.ts","comments":{"nodes":[]}}
  ]}}}}}`

	var resp ghReviewThreadsResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatal(err)
	}
	got := filterUnresolvedThreads(resp.Data.Repository.PullRequest.ReviewThreads.Nodes, "alice")

	if len(got) != 2 {
		t.Fatalf("want 2 unresolved threads (resolved/outdated/empty dropped), got %d: %+v", len(got), got)
	}
	if got[0].ID != "open" || got[0].Mine {
		t.Errorf("a thread whose last word is a reviewer's is my turn: %+v", got[0])
	}
	if got[0].File != "a.ts" || got[0].Line != 12 {
		t.Errorf("location lost: %+v", got[0])
	}
	if got[1].ID != "answered" || !got[1].Mine {
		t.Errorf("a thread I replied to last is not my turn: %+v", got[1])
	}
	// Body and author come from the thread's opening comment, so the queue shows
	// what was originally asked rather than the latest "ok thanks".
	if got[1].Author != "bob" || got[1].Body != "q" {
		t.Errorf("thread should be summarized by its first comment: %+v", got[1])
	}
	if countThreadsAwaitingMe(got) != 1 {
		t.Errorf("countThreadsAwaitingMe = %d, want 1", countThreadsAwaitingMe(got))
	}
}

func TestTruncateThreadBody(t *testing.T) {
	if got := truncateThreadBody("  hi  "); got != "hi" {
		t.Errorf("got %q", got)
	}
	long := strings.Repeat("x", threadBodyLimit+50)
	got := truncateThreadBody(long)
	if len([]rune(got)) != threadBodyLimit+1 || !strings.HasSuffix(got, "…") {
		t.Errorf("long body truncated to %d runes: %q…", len([]rune(got)), got[:40])
	}
}

func TestNormalizeMergeable(t *testing.T) {
	cases := map[string]string{
		"MERGEABLE":   "mergeable",
		"CONFLICTING": "conflicting",
		"UNKNOWN":     "unknown",
		"":            "unknown",
		"conflicting": "conflicting",
		"weird-value": "unknown",
	}
	for in, want := range cases {
		if got := normalizeMergeable(in); got != want {
			t.Errorf("normalizeMergeable(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDedupePRReviewRowsKeepsHighestPrecedence(t *testing.T) {
	row := func(url string) ghSearchActivityRow {
		var r ghSearchActivityRow
		r.URL = url
		return r
	}
	// Collected in buildPRReviewSpecs order: authored, then declared queries,
	// then review-requested, then the candidate pool.
	rows := []prReviewRow{
		{spec: prReviewSpec{relation: "authored", mine: true}, row: row("https://github.com/o/r/pull/1")},
		{spec: prReviewSpec{relation: "review_requested"}, row: row("https://github.com/o/r/pull/1")},
		{spec: prReviewSpec{relation: "backport", mine: true}, row: row("https://github.com/o/r/pull/2")},
		{spec: prReviewSpec{relation: "review_requested"}, row: row("https://github.com/o/r/pull/2")},
		// The user's own PR also lives in a followed repo. It must stay theirs,
		// or it would move from their lanes into the review queue.
		{spec: prReviewSpec{relation: relationReviewable, reviewable: true}, row: row("https://github.com/o/r/pull/1")},
		{spec: prReviewSpec{relation: relationReviewable, reviewable: true}, row: row("https://github.com/o/r/pull/3")},
	}
	out := dedupePRReviewRows(rows)
	if len(out) != 3 {
		t.Fatalf("expected 3 deduped rows, got %d", len(out))
	}
	byURL := map[string]prReviewRow{}
	for _, r := range out {
		byURL[r.row.URL] = r
	}
	if got := byURL["https://github.com/o/r/pull/1"].spec.relation; got != "authored" {
		t.Errorf("authored should win the dedupe, got %q", got)
	}
	if got := byURL["https://github.com/o/r/pull/2"].spec.relation; got != "backport" {
		t.Errorf("declared query should beat review_requested, got %q", got)
	}
	if got := byURL["https://github.com/o/r/pull/3"].spec.relation; got != relationReviewable {
		t.Errorf("a PR no other search found should stay a candidate, got %q", got)
	}
}

func TestCapReviewCandidatesKeepsNewestAndLeavesOwnedRows(t *testing.T) {
	at := func(day int) string { return fmt.Sprintf("2026-05-%02dT00:00:00Z", day) }
	rows := []prReviewRow{{spec: prReviewSpec{relation: "authored", mine: true}}}
	// Oldest first, so a cap that ignored recency would keep exactly the wrong
	// ones.
	for i := 0; i < maxReviewCandidates+5; i++ {
		var r ghSearchActivityRow
		r.URL = fmt.Sprintf("https://github.com/o/r/pull/%d", i)
		r.UpdatedAt = at(i + 1)
		rows = append(rows, prReviewRow{spec: prReviewSpec{relation: relationReviewable, reviewable: true}, row: r})
	}
	out := capReviewCandidates(rows)
	if len(out) != maxReviewCandidates+1 {
		t.Fatalf("kept %d rows, want %d candidates plus the owned row", len(out), maxReviewCandidates)
	}
	if !out[0].spec.mine {
		t.Error("the owned row should survive the cap in place")
	}
	// The five oldest are the ones dropped.
	for _, r := range out[1:] {
		if r.row.UpdatedAt < at(6) {
			t.Errorf("kept a candidate updated %s, older than the newest %d", r.row.UpdatedAt, maxReviewCandidates)
		}
	}
}

func TestBuildPRReviewSpecsSelf(t *testing.T) {
	extra := []userdata.PRQuery{{Relation: "backport", Qualifiers: "author:app/ci-bot assignee:@me"}}
	specs := buildPRReviewSpecs(ScopeSelf, "alice", extra, []string{"o/r"})
	if len(specs) != 1 {
		t.Fatalf("self should yield only the authored search, got %d: %+v", len(specs), specs)
	}
	if specs[0].relation != "authored" || !specs[0].mine {
		t.Errorf("expected owned authored spec, got %+v", specs[0])
	}
	if !containsArg(specs[0].args, "--author") {
		t.Errorf("authored spec missing --author: %v", specs[0].args)
	}
}

func TestBuildPRReviewSpecsInvolvedIncludesDeclaredQueries(t *testing.T) {
	extra := []userdata.PRQuery{{Relation: "backport", Qualifiers: "author:app/ci-bot assignee:@me"}}
	for _, scope := range []Scope{ScopeInvolved, ScopeAll, ScopeUnset} {
		specs := buildPRReviewSpecs(scope, "alice", extra, nil)
		if len(specs) != 4 {
			t.Fatalf("scope %q should yield authored + backport + review-requested + assignee, got %d: %+v", scope, len(specs), specs)
		}
		// Order defines dedupe precedence: authored beats declared queries,
		// which beat review-requested, which beats the candidate pool.
		if specs[0].relation != "authored" || specs[1].relation != "backport" || specs[2].relation != "review_requested" {
			t.Errorf("scope %q spec order = %q/%q/%q", scope, specs[0].relation, specs[1].relation, specs[2].relation)
		}
		if !specs[1].mine {
			t.Errorf("scope %q: declared query should be owned", scope)
		}
		if specs[2].mine {
			t.Errorf("scope %q: review-requested should not be owned", scope)
		}
		// Qualifiers become separate positional args; gh collapses a single
		// combined string into one quoted keyword and rejects it.
		want := []string{"author:app/ci-bot", "assignee:@me"}
		if len(specs[1].args) != len(want) {
			t.Fatalf("scope %q qualifier args = %v, want %v", scope, specs[1].args, want)
		}
		for i := range want {
			if specs[1].args[i] != want[i] {
				t.Errorf("scope %q qualifier args = %v, want %v", scope, specs[1].args, want)
			}
		}
	}
}

func TestBuildPRReviewSpecsNoDeclaredQueries(t *testing.T) {
	specs := buildPRReviewSpecs(ScopeInvolved, "alice", nil, nil)
	if len(specs) != 3 {
		t.Fatalf("expected authored + review-requested + assignee, got %d: %+v", len(specs), specs)
	}
	if specs[0].relation != "authored" || specs[1].relation != "review_requested" {
		t.Errorf("spec order = %q/%q", specs[0].relation, specs[1].relation)
	}
}

// The candidate pool is what fills the review queue: every open non-draft PR in
// a followed repo, plus anything assigned to the user whether or not it is a
// draft.
func TestBuildPRReviewSpecsCandidatePool(t *testing.T) {
	specs := buildPRReviewSpecs(ScopeInvolved, "alice", nil, []string{"o/r", "o/other"})
	var candidates []prReviewSpec
	for _, s := range specs {
		if s.reviewable {
			candidates = append(candidates, s)
		}
		if s.reviewable && s.mine {
			t.Errorf("a candidate is not the user's own work: %+v", s)
		}
	}
	if len(candidates) != 3 {
		t.Fatalf("expected one search per followed repo plus the assignee search, got %+v", candidates)
	}
	// Last, so any earlier search keeps a PR they both matched.
	if !specs[len(specs)-1].reviewable {
		t.Errorf("candidate searches must sort last for dedupe: %+v", specs)
	}
	for i, want := range [][]string{{"--repo", "o/r", "--draft=false"}, {"--repo", "o/other", "--draft=false"}, {"--assignee", "alice"}} {
		if strings.Join(candidates[i].args, " ") != strings.Join(want, " ") {
			t.Errorf("candidate %d args = %v, want %v", i, candidates[i].args, want)
		}
	}
}

// followed_repos is shared with the gitea collector, which accepts a bare
// owner. `gh search prs --repo` does not, and this list now runs on every state
// collection, so one such entry must not be able to fail the whole thing.
func TestFollowedPRReposSkipsBareOwners(t *testing.T) {
	d := userdata.Directive{Config: map[string]string{"followed_repos": "o/r, someorg, o/other"}}
	got := followedPRRepos(d)
	if strings.Join(got, ",") != "o/r,o/other" {
		t.Errorf("followedPRRepos = %v, want the owner/repo entries only", got)
	}
}

func TestIsChecksPermissionError(t *testing.T) {
	cases := []struct {
		name   string
		stderr string
		want   bool
	}{
		{
			name:   "fine-grained PAT statusCheckRollup denial",
			stderr: "GraphQL: Resource not accessible by personal access token (repository.pullRequest.statusCheckRollup.nodes.0.commit.statusCheckRollup.contexts.nodes.0)",
			want:   true,
		},
		{
			name:   "denial on an unrelated field is not a checks issue",
			stderr: "GraphQL: Resource not accessible by personal access token (repository.pullRequest.author)",
			want:   false,
		},
		{
			name:   "statusCheckRollup mentioned without permission phrase",
			stderr: "some other error touching statusCheckRollup",
			want:   false,
		},
		{name: "empty", stderr: "", want: false},
		{name: "unrelated network error", stderr: "dial tcp: lookup api.github.com: no such host", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isChecksPermissionError(tc.stderr); got != tc.want {
				t.Fatalf("isChecksPermissionError(%q) = %v, want %v", tc.stderr, got, tc.want)
			}
		})
	}
}

func TestFirstLine(t *testing.T) {
	cases := map[string]string{
		"":                  "",
		"  single  ":        "single",
		"first\nsecond":     "first",
		"\n\nlead\ntrail\n": "lead",
	}
	for in, want := range cases {
		if got := firstLine(in); got != want {
			t.Errorf("firstLine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncateMessage(t *testing.T) {
	if got := truncateMessage("hello", 10); got != "hello" {
		t.Errorf("no truncation expected, got %q", got)
	}
	if got := truncateMessage("hello world", 5); got != "hello…" {
		t.Errorf("truncateMessage cut = %q, want %q", got, "hello…")
	}
	if got := truncateMessage("anything", 0); got != "" {
		t.Errorf("max<=0 should yield empty, got %q", got)
	}
	// Multi-byte runes must not be split mid-character.
	if got := truncateMessage("héllo", 2); got != "hé…" {
		t.Errorf("multibyte truncate = %q, want %q", got, "hé…")
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if strings.EqualFold(a, want) {
			return true
		}
	}
	return false
}
