package collectors

import (
	"strings"
	"testing"
	"time"
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

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if strings.EqualFold(a, want) {
			return true
		}
	}
	return false
}
