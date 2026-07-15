package collectors

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KurtPreston/docent/libs/config/userdata"
)

// TestLocalGitCollectScopes drives the LocalGitCollector through scope=self,
// scope=involved, and scope=all against a real on-disk repo with two
// authors and an extra ref that does not belong to a local branch.
//
// Layout produced by the test:
//
//   - "mine" (config.user.email == kurt@example) on branch main: one commit
//   - "other" author on branch main: one commit
//   - "other" author on an extra ref refs/foreign/branch (created with
//     `git update-ref` so it shows up under --all but not --branches)
//
// Expected counts (commits only; reflog rows are always emitted but we
// ignore them here for clarity):
//
//   - scope=self     -> 1 commit (mine)
//   - scope=involved -> 2 commits (mine + other on main, because main is a
//     local branch); the extra ref is excluded.
//   - scope=all      -> 3 commits (everything).
func TestLocalGitCollectScopes(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	gitCmd := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_DATE=2026-05-13T12:00:00+00:00",
			"GIT_COMMITTER_DATE=2026-05-13T12:00:00+00:00",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	gitCmd("init", "--initial-branch=main", ".")
	gitCmd("config", "user.name", "Kurt")
	gitCmd("config", "user.email", "kurt@example")
	gitCmd("commit", "--allow-empty", "-m", "initial mine")

	// "other" commit on main.
	gitCmd("-c", "user.name=Other Person", "-c", "user.email=other@example",
		"commit", "--allow-empty", "-m", "other on main")

	// Capture the current HEAD so we can branch the foreign ref off it.
	headOut, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	mainTip := strings.TrimSpace(string(headOut))

	// Create an extra commit (via a detached worktree-style approach) and
	// point a non-branch ref at it. The simplest way: switch to a detached
	// HEAD, commit, save its SHA into a custom ref, then move HEAD back.
	gitCmd("checkout", "--detach", mainTip)
	gitCmd("-c", "user.name=Other Person", "-c", "user.email=other@example",
		"commit", "--allow-empty", "-m", "other on foreign ref")
	foreignOut, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	foreignSHA := strings.TrimSpace(string(foreignOut))
	gitCmd("update-ref", "refs/foreign/branch", foreignSHA)
	gitCmd("checkout", "main")

	directive := userdata.Directive{
		ID: "lg", Name: "Local", Collector: "local-git", Enabled: true,
		Paths: []string{dir},
	}
	clock := func() time.Time { return time.Date(2026, 5, 14, 0, 0, 0, 0, time.UTC) }
	c := LocalGitCollector{Clock: clock}

	since := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	collect := func(scope Scope) []StatusItem {
		items, err := c.CollectEvents(context.Background(), directive, &CollectOpts{
			Since: since,
			Until: clock(),
			Scope: scope,
		})
		if err != nil {
			t.Fatalf("Collect scope=%s: %v", scope, err)
		}
		return items
	}

	countCommits := func(items []StatusItem) int {
		n := 0
		for _, it := range items {
			if it.Kind == "commit" {
				n++
			}
		}
		return n
	}

	selfItems := collect(ScopeSelf)
	if got := countCommits(selfItems); got != 1 {
		t.Errorf("scope=self expected 1 commit, got %d: %#v", got, selfItems)
	}
	for _, it := range selfItems {
		if it.Kind == "commit" && !it.IsSelf {
			t.Errorf("scope=self emitted non-self commit: %+v", it)
		}
	}

	involvedItems := collect(ScopeInvolved)
	if got := countCommits(involvedItems); got != 2 {
		t.Errorf("scope=involved expected 2 commits (main branch), got %d: %#v", got, involvedItems)
	}

	allItems := collect(ScopeAll)
	if got := countCommits(allItems); got != 3 {
		t.Errorf("scope=all expected 3 commits (all refs), got %d: %#v", got, allItems)
	}
}

// TestLocalGitCollectResolvesWorktreePath reproduces the grove-style layout
// where several worktrees of one repository sit side by side under code_home,
// all sharing a single object store and refs. Because `git log --all` run in
// any worktree lists commits from every branch, a commit that belongs to
// branch B (checked out in worktree B) also shows up when scanning worktree A
// — and used to be tagged with worktree A's path. That mis-attribution made
// the dashboard open the wrong directory for B's work item. The collector must
// instead tag each commit with the path of the worktree that actually holds
// its branch.
func TestLocalGitCollectResolvesWorktreePath(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	codeHome := t.TempDir()
	mainDir := codeHome + "/main"
	featureDir := codeHome + "/feature"
	if err := os.MkdirAll(mainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_DATE=2026-05-13T12:00:00+00:00",
			"GIT_COMMITTER_DATE=2026-05-13T12:00:00+00:00",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	git(mainDir, "init", "--initial-branch=main", ".")
	git(mainDir, "config", "user.name", "Kurt")
	git(mainDir, "config", "user.email", "kurt@example")
	git(mainDir, "commit", "--allow-empty", "-m", "initial on main")
	// A sibling worktree checked out on its own branch, with a commit that is
	// unique to that branch (so `--source` attributes it to refs/heads/feature).
	git(mainDir, "worktree", "add", "-b", "feature", featureDir)
	git(featureDir, "config", "user.name", "Kurt")
	git(featureDir, "config", "user.email", "kurt@example")
	git(featureDir, "commit", "--allow-empty", "-m", "work on feature")

	directive := userdata.Directive{
		ID: "lg", Name: "Local", Collector: "local-git", Enabled: true,
		CodeHome: codeHome,
	}
	clock := func() time.Time { return time.Date(2026, 5, 14, 0, 0, 0, 0, time.UTC) }
	c := LocalGitCollector{Clock: clock}
	items, err := c.CollectEvents(context.Background(), directive, &CollectOpts{
		Since: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		Until: clock(),
		Scope: ScopeAll,
	})
	if err != nil {
		t.Fatalf("CollectEvents: %v", err)
	}

	featureCommits := 0
	for _, it := range items {
		if it.Kind != "commit" || it.Fields["branch"] != "feature" {
			continue
		}
		featureCommits++
		if base := filepath.Base(it.Fields["path"]); base != "feature" {
			t.Errorf("feature-branch commit tagged with path %q (base %q); want the feature worktree, not a sibling worktree",
				it.Fields["path"], base)
		}
	}
	if featureCommits == 0 {
		t.Fatalf("expected at least one commit attributed to the feature branch, got items=%#v", items)
	}
}

// TestLocalGitCollectDedupesSharedWorktreeCommits verifies the two halves of
// how the collector treats grove-style worktrees that share one object store:
// commits (visible in every worktree via `git log --all`) are scanned once and
// emitted a single time, while HEAD reflogs (which live in each worktree's own
// gitdir) are still collected per worktree. Without the shared-store dedup the
// feature branch's commit would be emitted once per sibling worktree.
func TestLocalGitCollectDedupesSharedWorktreeCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	codeHome := t.TempDir()
	mainDir := codeHome + "/main"
	featureDir := codeHome + "/feature"
	if err := os.MkdirAll(mainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// No faked GIT_*_DATE here: git stamps both commit times and reflog entry
	// times at wall-clock "now", so the real-time collection window below
	// captures the commits and the reflogs alike.
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	git(mainDir, "init", "--initial-branch=main", ".")
	git(mainDir, "config", "user.name", "Kurt")
	git(mainDir, "config", "user.email", "kurt@example")
	git(mainDir, "commit", "--allow-empty", "-m", "initial on main")
	// Sibling worktree on its own branch with a commit unique to that branch.
	git(mainDir, "worktree", "add", "-b", "feature", featureDir)
	git(featureDir, "config", "user.name", "Kurt")
	git(featureDir, "config", "user.email", "kurt@example")
	git(featureDir, "commit", "--allow-empty", "-m", "work on feature")

	directive := userdata.Directive{
		ID: "lg", Name: "Local", Collector: "local-git", Enabled: true,
		CodeHome: codeHome,
	}
	now := time.Now()
	c := LocalGitCollector{Clock: func() time.Time { return now }}
	items, err := c.CollectEvents(context.Background(), directive, &CollectOpts{
		Since: now.Add(-24 * time.Hour),
		Until: now.Add(24 * time.Hour),
		Scope: ScopeAll,
	})
	if err != nil {
		t.Fatalf("CollectEvents: %v", err)
	}

	// Commits: the feature branch's unique commit appears in every worktree's
	// `git log --all`, but must be emitted exactly once.
	featureCommits := 0
	for _, it := range items {
		if it.Kind == "commit" && it.Fields["subject"] == "work on feature" {
			featureCommits++
		}
	}
	if featureCommits != 1 {
		t.Errorf("feature commit emitted %d time(s); want exactly 1 (a shared object store must be scanned once)", featureCommits)
	}

	// Reflogs: each worktree's own HEAD reflog is still collected, so rows
	// tagged with both worktrees' paths must be present.
	reflogBases := map[string]bool{}
	for _, it := range items {
		if it.Kind == "reflog" {
			reflogBases[filepath.Base(it.Fields["path"])] = true
		}
	}
	for _, want := range []string{"main", "feature"} {
		if !reflogBases[want] {
			t.Errorf("no reflog rows from the %q worktree (per-worktree reflogs must be preserved); got bases %v", want, reflogBases)
		}
	}
}

// TestLocalGitCollectSkipsEmptyRepo verifies that a freshly-initialised repo
// with no commits yet is silently skipped instead of failing the whole
// directive. Empty repos surfaced as `git reflog ... exit status 128: fatal:
// your current branch '<name>' does not have any commits yet` before this was
// handled.
func TestLocalGitCollectSkipsEmptyRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	codeHome := t.TempDir()

	emptyRepo := codeHome + "/empty"
	if err := os.MkdirAll(emptyRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	initEmpty := exec.Command("git", "-C", emptyRepo, "init", "--initial-branch=main", ".")
	if out, err := initEmpty.CombinedOutput(); err != nil {
		t.Fatalf("git init empty: %v\n%s", err, out)
	}

	goodRepo := codeHome + "/good"
	if err := os.MkdirAll(goodRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitGood := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", goodRepo}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_DATE=2026-05-13T12:00:00+00:00",
			"GIT_COMMITTER_DATE=2026-05-13T12:00:00+00:00",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	gitGood("init", "--initial-branch=main", ".")
	gitGood("config", "user.name", "Kurt")
	gitGood("config", "user.email", "kurt@example")
	gitGood("commit", "--allow-empty", "-m", "initial")

	directive := userdata.Directive{
		ID: "lg", Name: "Local", Collector: "local-git", Enabled: true,
		CodeHome: codeHome,
	}
	clock := func() time.Time { return time.Date(2026, 5, 14, 0, 0, 0, 0, time.UTC) }
	c := LocalGitCollector{Clock: clock}

	items, err := c.CollectEvents(context.Background(), directive, &CollectOpts{
		Since: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		Until: clock(),
		Scope: ScopeAll,
	})
	if err != nil {
		t.Fatalf("Collect: %v (empty repo should be skipped, not fail directive)", err)
	}
	hasGoodCommit := false
	for _, it := range items {
		if it.Kind == "commit" {
			hasGoodCommit = true
		}
		if strings.Contains(it.Fields["path"], "/empty") {
			t.Errorf("empty repo leaked an item: %+v", it)
		}
	}
	if !hasGoodCommit {
		t.Errorf("expected at least one commit from the good repo, got items=%#v", items)
	}
}

// TestLocalGitCollectEventsOmitsBranchSnapshot verifies that CollectEvents
// does not emit a kind=branch status for the checked-out HEAD. Branch
// snapshots are current state (every repo under code_home), not lookback
// activity; emitting them flooded recent-activity prompts with branch×1 rows
// for untouched clones.
func TestLocalGitCollectEventsOmitsBranchSnapshot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	gitCmd := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_DATE=2020-01-01T00:00:00+00:00",
			"GIT_COMMITTER_DATE=2020-01-01T00:00:00+00:00",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	gitCmd("init", "--initial-branch=main", ".")
	gitCmd("config", "user.name", "Kurt")
	gitCmd("config", "user.email", "kurt@example")
	gitCmd("commit", "--allow-empty", "-m", "ancient tip")

	directive := userdata.Directive{
		ID: "lg", Name: "Local", Collector: "local-git", Enabled: true,
		Paths: []string{dir},
	}
	clock := func() time.Time { return time.Date(2026, 5, 14, 0, 0, 0, 0, time.UTC) }
	c := LocalGitCollector{Clock: clock}

	items, err := c.CollectEvents(context.Background(), directive, &CollectOpts{
		Since: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		Until: clock(),
		Scope: ScopeInvolved,
	})
	if err != nil {
		t.Fatalf("CollectEvents: %v", err)
	}
	for _, it := range items {
		if it.Kind == "branch" {
			t.Fatalf("CollectEvents emitted branch snapshot: %+v", it)
		}
	}
}
