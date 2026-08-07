package worktree

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func targetOf(ts []Target, kind string) (Target, bool) {
	for _, t := range ts {
		if t.Kind == kind {
			return t, true
		}
	}
	return Target{}, false
}

func kinds(ts []Target) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.Kind)
	}
	return out
}

// A worktree project offers one placement in the developer's own directory --
// reuse it when the branch is there, add one when it is not -- plus docent's.
func TestTargetsForAWorktreeProject(t *testing.T) {
	requireGit(t)
	f := newForge(t)
	remote := f.repo("Chip/salsa")
	code := t.TempDir()
	project := filepath.Join(code, "salsa")
	base := bareCloneInto(t, project, ".base", remote)
	gitAt(t, base, "worktree", "add", "-q", filepath.Join(project, "release-next"), "-b", "release/next")

	snap := NewSnapshot([]string{code}, t.TempDir())

	got := snap.Targets(context.Background(), "Chip/salsa", "release/next")
	if diff := strings.Join(kinds(got), ","); diff != TargetExisting+","+TargetIsolated {
		t.Fatalf("kinds = %s", diff)
	}
	existing, _ := targetOf(got, TargetExisting)
	if !SamePath(existing.Dir, filepath.Join(project, "release-next")) {
		t.Errorf("existing dir = %q", existing.Dir)
	}
	if existing.Owned {
		t.Error("the developer's own worktree reported as docent's")
	}

	got = snap.Targets(context.Background(), "Chip/salsa", "salsa-1/fix")
	if diff := strings.Join(kinds(got), ","); diff != TargetCreate+","+TargetIsolated {
		t.Fatalf("kinds = %s", diff)
	}
	create, _ := targetOf(got, TargetCreate)
	if !SamePath(create.Dir, filepath.Join(project, "salsa-1-fix")) {
		t.Errorf("create dir = %q, want the sanitized branch under the project", create.Dir)
	}
	if create.Disabled != "" {
		t.Errorf("create disabled: %q", create.Disabled)
	}
}

// An ordinary clone has one working tree, so the only way to run in it is to
// switch it -- and only when there is nothing uncommitted to lose.
func TestTargetsForASingleCheckout(t *testing.T) {
	requireGit(t)
	f := newForge(t)
	remote := f.repo("Chip/salsa")
	code := t.TempDir()
	local := f.clone(code, "salsa", remote)

	got := NewSnapshot([]string{code}, t.TempDir()).Targets(context.Background(), "Chip/salsa", "salsa-1/fix")
	if diff := strings.Join(kinds(got), ","); diff != TargetInPlace+","+TargetIsolated {
		t.Fatalf("kinds = %s", diff)
	}
	inPlace, _ := targetOf(got, TargetInPlace)
	if inPlace.Disabled != "" {
		t.Errorf("in_place disabled on a clean tree: %q", inPlace.Disabled)
	}
	if !SamePath(inPlace.Dir, local) {
		t.Errorf("in_place dir = %q, want %q", inPlace.Dir, local)
	}

	if err := os.WriteFile(filepath.Join(local, "wip.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got = NewSnapshot([]string{code}, t.TempDir()).Targets(context.Background(), "Chip/salsa", "salsa-1/fix")
	inPlace, _ = targetOf(got, TargetInPlace)
	// Offered but refused, with the reason: hiding it would look like a bug, and
	// the reason is something the user can clear in one command.
	if !strings.Contains(inPlace.Disabled, "uncommitted") {
		t.Errorf("in_place disabled = %q, want the dirty tree named", inPlace.Disabled)
	}
}

// The one placement that never depends on what the developer has on disk is
// also the one that cannot disturb them, so it is always there and always the
// default.
func TestTargetsAlwaysOfferIsolatedAsTheDefault(t *testing.T) {
	requireGit(t)
	state := t.TempDir()
	got := NewSnapshot([]string{t.TempDir()}, state).Targets(context.Background(), "Chip/salsa", "salsa-1/fix")
	if len(got) != 1 || got[0].Kind != TargetIsolated {
		t.Fatalf("targets = %+v, want only the isolated one", got)
	}
	if !got[0].Default || !got[0].Owned {
		t.Errorf("isolated target = %+v, want it default and owned", got[0])
	}
	want := filepath.Join(state, "projects", "Chip-salsa", "salsa-1-fix")
	if !SamePath(got[0].Dir, want) {
		t.Errorf("dir = %q, want %q", got[0].Dir, want)
	}
}

func TestTargetsNeedARepoAndABranch(t *testing.T) {
	snap := NewSnapshot(nil, t.TempDir())
	if got := snap.Targets(context.Background(), "Chip/salsa", ""); got != nil {
		t.Errorf("targets without a branch = %+v", got)
	}
	if got := snap.Targets(context.Background(), "", "main"); got != nil {
		t.Errorf("targets without a repo = %+v", got)
	}
}

func TestResolveDeveloperAddsAWorktreeToTheProject(t *testing.T) {
	requireGit(t)
	f := newForge(t)
	remote := f.repo("Chip/salsa")
	code := t.TempDir()
	project := filepath.Join(code, "salsa")
	bareCloneInto(t, project, ".base", remote)

	hook := writeHook(t, `echo "$DOCENT_WORKTREE_OWNED" > owned.txt`)
	res, err := ResolveDeveloper(context.Background(), Request{
		Repo: "Chip/salsa", Branch: "salsa-1/fix", Target: TargetCreate,
		Roots: []string{code}, Hook: hook,
	})
	if err != nil {
		t.Fatalf("ResolveDeveloper: %v", err)
	}
	if res.Owned {
		t.Error("Owned = true for the developer's own project")
	}
	if !SamePath(res.Dir, filepath.Join(project, "salsa-1-fix")) {
		t.Errorf("Dir = %q", res.Dir)
	}
	if got := currentBranch(t, res.Dir); got != "salsa-1/fix" {
		t.Errorf("checked out %q", got)
	}
	// A first-class worktree, so anything reading git sees it -- not something
	// only docent understands.
	layout, err := List(context.Background(), res.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := layout.ByBranch["salsa-1/fix"]; !ok {
		t.Errorf("git worktree list does not know about it: %+v", layout.ByBranch)
	}
	// The hook runs here too, told it is not docent's directory.
	b, err := os.ReadFile(filepath.Join(res.Dir, "owned.txt"))
	if err != nil {
		t.Fatalf("the hook did not run: %v", err)
	}
	if strings.TrimSpace(string(b)) != "0" {
		t.Errorf("DOCENT_WORKTREE_OWNED = %q, want 0", strings.TrimSpace(string(b)))
	}
}

func TestResolveDeveloperChecksOutInPlace(t *testing.T) {
	requireGit(t)
	f := newForge(t)
	remote := f.repo("Chip/salsa")
	code := t.TempDir()
	local := f.clone(code, "salsa", remote)
	was := currentBranch(t, local)

	res, err := ResolveDeveloper(context.Background(), Request{
		Repo: "Chip/salsa", Branch: "salsa-1/fix", Target: TargetInPlace,
		Roots: []string{code},
	})
	if err != nil {
		t.Fatalf("ResolveDeveloper: %v", err)
	}
	if !SamePath(res.Dir, local) {
		t.Errorf("Dir = %q, want the checkout itself", res.Dir)
	}
	if res.Owned {
		t.Error("Owned = true for the developer's checkout")
	}
	if got := currentBranch(t, local); got != "salsa-1/fix" {
		t.Errorf("on %q, want the requested branch", got)
	}
	// Reported so the user can be told their editor is no longer where they
	// left it, which is the whole risk of this placement.
	if res.PreviousBranch != was {
		t.Errorf("PreviousBranch = %q, want %q", res.PreviousBranch, was)
	}
}

// The picker's dirty check is as old as the page, so the real one is here.
func TestResolveDeveloperRefusesToCheckoutOverUncommittedWork(t *testing.T) {
	requireGit(t)
	f := newForge(t)
	remote := f.repo("Chip/salsa")
	code := t.TempDir()
	local := f.clone(code, "salsa", remote)
	was := currentBranch(t, local)
	if err := os.WriteFile(filepath.Join(local, "wip.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := ResolveDeveloper(context.Background(), Request{
		Repo: "Chip/salsa", Branch: "salsa-1/fix", Target: TargetInPlace,
		Roots: []string{code},
	})
	if err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("err = %v, want a refusal naming the uncommitted work", err)
	}
	if got := currentBranch(t, local); got != was {
		t.Errorf("branch moved to %q despite the refusal", got)
	}
}

// docent's self-healing -- the occupied path it deletes and rebuilds -- must
// never reach a directory the developer might have something in.
func TestResolveDeveloperNeverDeletesAnOccupiedPath(t *testing.T) {
	requireGit(t)
	f := newForge(t)
	remote := f.repo("Chip/salsa")
	code := t.TempDir()
	project := filepath.Join(code, "salsa")
	bareCloneInto(t, project, ".base", remote)
	occupied := filepath.Join(project, "salsa-1-fix")
	if err := os.MkdirAll(occupied, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(occupied, "notes.txt")
	if err := os.WriteFile(keep, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := ResolveDeveloper(context.Background(), Request{
		Repo: "Chip/salsa", Branch: "salsa-1/fix", Target: TargetCreate,
		Roots: []string{code},
	})
	if err == nil {
		t.Fatal("want a refusal, got success")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("the occupied directory was cleared out: %v", err)
	}
}

func TestResolveDeveloperNeedsAKnownPlacement(t *testing.T) {
	requireGit(t)
	f := newForge(t)
	remote := f.repo("Chip/salsa")
	code := t.TempDir()
	f.clone(code, "salsa", remote)

	for _, target := range []string{"", TargetIsolated, "wat"} {
		_, err := ResolveDeveloper(context.Background(), Request{
			Repo: "Chip/salsa", Branch: "salsa-1/fix", Target: target,
			Roots: []string{code},
		})
		if err == nil {
			t.Errorf("target %q was accepted", target)
		}
	}
}

func TestResolveDeveloperNeedsALocalCopy(t *testing.T) {
	requireGit(t)
	_, err := ResolveDeveloper(context.Background(), Request{
		Repo: "Chip/salsa", Branch: "salsa-1/fix", Target: TargetCreate,
		Roots: []string{t.TempDir()},
	})
	if err == nil || !strings.Contains(err.Error(), "no local copy") {
		t.Fatalf("err = %v, want it to say there is nothing to work in", err)
	}
}
