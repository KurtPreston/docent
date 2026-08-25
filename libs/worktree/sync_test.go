package worktree

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// commitIn writes a file and commits it, standing in for either party's work.
func commitIn(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	gitAt(t, dir, "add", name)
	gitAt(t, dir, "commit", "-q", "-m", name)
}

// twoCopies sets up the situation the whole design is about: a repository the
// developer has a checkout of, and docent's own worktree of the same branch.
func twoCopies(t *testing.T, branch string) (local, docent string) {
	t.Helper()
	f := newForge(t)
	remote := f.repo("Chip/salsa")
	code := t.TempDir()
	local = f.clone(code, "salsa", remote)
	gitAt(t, local, "checkout", "-q", "-b", branch)
	commitIn(t, local, "shared.txt", "shared")
	gitAt(t, local, "push", "-q", "origin", branch)

	res, err := Resolve(context.Background(), Request{
		Repo: "Chip/salsa", Branch: branch,
		Roots: []string{code}, StateRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return local, res.Dir
}

func TestSyncFastForwardsOntoTheDeveloperCommits(t *testing.T) {
	requireGit(t)
	local, docent := twoCopies(t, "salsa-1/fix")
	// Committed and pushed nowhere: reaching this over remote.local rather than
	// over the forge is the point of registering it.
	commitIn(t, local, "theirs.txt", "theirs")

	res, err := Sync(context.Background(), SyncRequest{Dir: docent, Branch: "salsa-1/fix"})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if !res.FastForwarded {
		t.Errorf("FastForwarded = false (behind %d, ahead %d, note %q)", res.Behind, res.Ahead, res.Note)
	}
	if res.Diverged {
		t.Error("Diverged = true; only one side moved")
	}
	if got := headSubject(t, docent); got != "theirs.txt" {
		t.Errorf("docent is at %q, want the developer's commit", got)
	}
}

func TestSyncReportsADivergence(t *testing.T) {
	requireGit(t)
	local, docent := twoCopies(t, "salsa-1/fix")
	commitIn(t, local, "theirs.txt", "theirs")
	commitIn(t, docent, "ours.txt", "ours")

	res, err := Sync(context.Background(), SyncRequest{Dir: docent, Branch: "salsa-1/fix"})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if !res.Diverged {
		t.Fatalf("Diverged = false (behind %d, ahead %d)", res.Behind, res.Ahead)
	}
	if res.Behind != 1 || res.Ahead != 1 {
		t.Errorf("behind %d ahead %d, want 1 and 1", res.Behind, res.Ahead)
	}
	if res.FastForwarded {
		t.Error("FastForwarded = true; a fork must never be resolved silently")
	}
	if got := headSubject(t, docent); got != "ours.txt" {
		t.Errorf("docent moved to %q; a refused sync must leave the tree alone", got)
	}
}

// Being behind is recoverable; losing an agent's uncommitted edits to a merge is
// not, so the fast-forward stands down rather than the other way round.
func TestSyncLeavesADirtyTreeBehind(t *testing.T) {
	requireGit(t)
	local, docent := twoCopies(t, "salsa-1/fix")
	commitIn(t, local, "theirs.txt", "theirs")
	if err := os.WriteFile(filepath.Join(docent, "wip.txt"), []byte("mid-turn"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Sync(context.Background(), SyncRequest{Dir: docent, Branch: "salsa-1/fix"})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if res.FastForwarded {
		t.Error("FastForwarded = true over uncommitted work")
	}
	if res.Behind != 1 {
		t.Errorf("Behind = %d, want 1", res.Behind)
	}
	if !strings.Contains(res.Note, "dirty") {
		t.Errorf("Note = %q, want it to say why nothing moved", res.Note)
	}
}

// The fast-forward moves whatever HEAD is on, so a worktree an agent left on
// another branch would have that branch advanced onto this one's commits -- and
// the comparison deciding on it would be between two unrelated lines of work.
func TestSyncLeavesADriftedWorktreeAlone(t *testing.T) {
	requireGit(t)
	local, docent := twoCopies(t, "salsa-1/fix")
	commitIn(t, local, "theirs.txt", "theirs")
	gitAt(t, docent, "checkout", "-q", "-b", "release-next", "origin/main")
	before := headSubject(t, docent)

	res, err := Sync(context.Background(), SyncRequest{Dir: docent, Branch: "salsa-1/fix"})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if res.FastForwarded {
		t.Error("FastForwarded = true; the branch that moved was not the one asked about")
	}
	if got := currentBranch(t, docent); got != "release-next" {
		t.Errorf("worktree is on %q; Sync is not the place that puts drift right", got)
	}
	if got := headSubject(t, docent); got != before {
		t.Errorf("HEAD moved from %q to %q on a branch nobody asked about", before, got)
	}
	if !strings.Contains(res.Note, "release-next") {
		t.Errorf("Note = %q, want it to name what is checked out instead", res.Note)
	}
}

func TestSyncIsQuietWhenTheBranchIsOnlyDocents(t *testing.T) {
	requireGit(t)
	f := newForge(t)
	remote := f.repo("Chip/salsa")
	code := t.TempDir()
	f.clone(code, "salsa", remote)
	res, err := Resolve(context.Background(), Request{
		Repo: "Chip/salsa", Branch: "salsa-9/new",
		Roots: []string{code}, StateRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	got, err := Sync(context.Background(), SyncRequest{Dir: res.Dir, Branch: "salsa-9/new"})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got.Diverged || got.Behind != 0 || got.Ahead != 0 {
		t.Errorf("Sync = %+v, want nothing to compare against", got)
	}
}

func TestCommitAllSnapshotsTheTurn(t *testing.T) {
	requireGit(t)
	_, docent := twoCopies(t, "salsa-1/fix")
	if err := os.WriteFile(filepath.Join(docent, "new.txt"), []byte("agent"), 0o644); err != nil {
		t.Fatal(err)
	}

	committed, err := CommitAll(context.Background(), docent, "docent: turn 1 (abc)")
	if err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	if !committed {
		t.Fatal("committed = false with an untracked file present")
	}
	if got := headSubject(t, docent); got != "docent: turn 1 (abc)" {
		t.Errorf("HEAD subject = %q", got)
	}
	if dirty, err := IsDirty(context.Background(), docent); err != nil || dirty {
		t.Errorf("dirty = %v (err %v) after the snapshot", dirty, err)
	}
}

// A turn that read code and wrote nothing must not leave an empty commit behind:
// the history is the developer's to read, and noise in it costs them.
func TestCommitAllDoesNothingWhenClean(t *testing.T) {
	requireGit(t)
	_, docent := twoCopies(t, "salsa-1/fix")
	before := headSubject(t, docent)

	committed, err := CommitAll(context.Background(), docent, "docent: turn 1 (abc)")
	if err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	if committed {
		t.Error("committed = true on a clean tree")
	}
	if got := headSubject(t, docent); got != before {
		t.Errorf("HEAD moved from %q to %q", before, got)
	}
}

// Pre-commit hooks belong to the developer's contributions, not to a WIP
// checkpoint an agent leaves behind: one that rejects would strand the turn.
func TestCommitAllSkipsHooks(t *testing.T) {
	requireGit(t)
	_, docent := twoCopies(t, "salsa-1/fix")
	hooks, err := gitOutput(context.Background(), docent, gitTimeout, "rev-parse", "--git-path", "hooks")
	if err != nil {
		t.Fatal(err)
	}
	hookDir := strings.TrimSpace(hooks)
	if !filepath.IsAbs(hookDir) {
		hookDir = filepath.Join(docent, hookDir)
	}
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hookDir, "pre-commit"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docent, "new.txt"), []byte("agent"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := CommitAll(context.Background(), docent, "docent: turn 1 (abc)"); err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	if got := headSubject(t, docent); got != "docent: turn 1 (abc)" {
		t.Errorf("HEAD subject = %q; the hook was not bypassed", got)
	}
}
