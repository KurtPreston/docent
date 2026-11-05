package worktree

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// reapFixture provisions one worktree per branch in a fresh state root.
func reapFixture(t *testing.T, branches ...string) (state string, dirs map[string]string) {
	t.Helper()
	f := newForge(t)
	remote := f.repo("Chip/salsa")
	code := t.TempDir()
	f.clone(code, "salsa", remote)
	state = t.TempDir()

	// Provision each branch the way an agent run would.
	dirs = map[string]string{}
	for _, b := range branches {
		res, err := Resolve(context.Background(), Request{
			Repo: "Chip/salsa", Branch: b, Roots: []string{code}, StateRoot: state,
		})
		if err != nil {
			t.Fatalf("Resolve %s: %v", b, err)
		}
		dirs[b] = res.Dir
	}
	return state, dirs
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// An idle, clean worktree is removed, but its branch survives in the bare
// repository so the next provisioning can bring it back.
func TestReapRemovesIdleCleanWorktrees(t *testing.T) {
	requireGit(t)
	state, dirs := reapFixture(t, "old-work")
	dir := dirs["old-work"]

	res, err := Reap(context.Background(), ReapRequest{
		MaxIdle: 2 * time.Hour, Now: time.Now().Add(3 * time.Hour), StateRoot: state,
	})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if len(res.Removed) != 1 || exists(dir) {
		t.Fatalf("Removed = %v, dir exists = %v; want the idle tree gone", res.Removed, exists(dir))
	}
	if len(res.Errors) != 0 {
		t.Errorf("Errors = %v", res.Errors)
	}

	// The branch is still there, and provisioning recreates the checkout.
	base := filepath.Join(state, projectsDir, "Chip-salsa", baseDirName)
	if !hasRef(context.Background(), base, "refs/heads/old-work") {
		t.Error("branch was deleted along with its worktree")
	}
	again, err := Resolve(context.Background(), Request{Repo: "Chip/salsa", Branch: "old-work", StateRoot: state})
	if err != nil || !again.Created {
		t.Fatalf("re-provision: created=%v err=%v", again.Created, err)
	}
}

// Recently provisioned trees, dirty trees and kept trees all survive.
func TestReapSparesRecentDirtyAndKeptWorktrees(t *testing.T) {
	requireGit(t)
	state, dirs := reapFixture(t, "recent", "dirty", "kept")
	if err := os.WriteFile(filepath.Join(dirs["dirty"], "wip.txt"), []byte("unsaved"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Backdate every mark except "recent", so only it looks fresh.
	for _, b := range []string{"dirty", "kept"} {
		gitDir, err := worktreeGitDir(context.Background(), dirs[b])
		if err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-10 * time.Hour)
		if err := os.Chtimes(filepath.Join(gitDir, lastUsedFile), old, old); err != nil {
			t.Fatal(err)
		}
	}

	res, err := Reap(context.Background(), ReapRequest{
		MaxIdle:   5 * time.Hour,
		StateRoot: state,
		Keep:      func(dir string) bool { return SamePath(dir, dirs["kept"]) },
	})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if len(res.Removed) != 0 {
		t.Errorf("Removed = %v; want nothing", res.Removed)
	}
	if len(res.Dirty) != 1 || !SamePath(res.Dirty[0], dirs["dirty"]) {
		t.Errorf("Dirty = %v; want the tree with uncommitted work", res.Dirty)
	}
	for b, dir := range dirs {
		if !exists(dir) {
			t.Errorf("%s was removed", b)
		}
	}
}

// A retention shorter than an automation run could delete a tree mid-run, so
// it is raised to the floor.
func TestReapHonoursMinimumIdle(t *testing.T) {
	requireGit(t)
	state, dirs := reapFixture(t, "busy")

	res, err := Reap(context.Background(), ReapRequest{
		MaxIdle: time.Second, Now: time.Now().Add(time.Minute), StateRoot: state,
	})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if len(res.Removed) != 0 || !exists(dirs["busy"]) {
		t.Errorf("Removed = %v; a tree used a minute ago must survive", res.Removed)
	}
}

// A state directory with no projects yet is not an error.
func TestReapWithNoProjects(t *testing.T) {
	res, err := Reap(context.Background(), ReapRequest{MaxIdle: time.Hour, StateRoot: t.TempDir()})
	if err != nil || len(res.Removed) != 0 {
		t.Fatalf("Reap = %+v, %v", res, err)
	}
}

// backdate makes dir look last provisioned age ago.
func backdate(t *testing.T, dir string, age time.Duration) {
	t.Helper()
	gitDir, err := worktreeGitDir(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-age)
	if err := os.Chtimes(filepath.Join(gitDir, lastUsedFile), old, old); err != nil {
		t.Fatal(err)
	}
}

// Over the disk cap, the least recently used trees go first, and only until
// usage fits, even though none has reached its idle retention.
func TestReapEvictsLeastRecentlyUsedOverBudget(t *testing.T) {
	requireGit(t)
	state, dirs := reapFixture(t, "older", "newer")
	backdate(t, dirs["older"], 5*time.Hour)
	backdate(t, dirs["newer"], 2*time.Hour)

	// Set the cap just under what both trees use together.
	seen := map[fileID]bool{}
	total := diskUsage(context.Background(), dirs["older"], seen) + diskUsage(context.Background(), dirs["newer"], seen)
	res, err := Reap(context.Background(), ReapRequest{
		MaxIdle: 72 * time.Hour, MaxBytes: total - 1, StateRoot: state,
	})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}

	// Only the older tree is evicted, and nothing counts as idle-removed.
	if len(res.Evicted) != 1 || !SamePath(res.Evicted[0], dirs["older"]) || exists(dirs["older"]) {
		t.Fatalf("Evicted = %v; want only the older tree", res.Evicted)
	}
	if len(res.Removed) != 0 || !exists(dirs["newer"]) {
		t.Errorf("Removed = %v, newer exists = %v", res.Removed, exists(dirs["newer"]))
	}
	if res.UsedBytes <= 0 || res.UsedBytes > total-1 {
		t.Errorf("UsedBytes = %d; want under %d", res.UsedBytes, total-1)
	}
}

// The cap never removes a tree used within the hour, a kept tree, or one with
// uncommitted work, though all of them count toward usage.
func TestReapBudgetSparesRecentKeptAndDirty(t *testing.T) {
	requireGit(t)
	state, dirs := reapFixture(t, "recent", "kept", "dirty")
	backdate(t, dirs["kept"], 5*time.Hour)
	backdate(t, dirs["dirty"], 5*time.Hour)
	if err := os.WriteFile(filepath.Join(dirs["dirty"], "wip.txt"), []byte("unsaved"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Reap(context.Background(), ReapRequest{
		MaxBytes:  1,
		StateRoot: state,
		Keep:      func(dir string) bool { return SamePath(dir, dirs["kept"]) },
	})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if len(res.Evicted) != 0 || len(res.Removed) != 0 {
		t.Errorf("Evicted = %v, Removed = %v; want nothing", res.Evicted, res.Removed)
	}
	if len(res.Dirty) != 1 || res.UsedBytes <= 1 {
		t.Errorf("Dirty = %v, UsedBytes = %d", res.Dirty, res.UsedBytes)
	}
	for b, dir := range dirs {
		if !exists(dir) {
			t.Errorf("%s was removed", b)
		}
	}
}
