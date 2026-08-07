package worktree

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// handoffFixture is the situation the open button exists for: the developer has
// a worktree project, and docent has been working on a branch in its own tree.
func handoffFixture(t *testing.T, branch string) (code, project, state, docentDir string) {
	t.Helper()
	requireGit(t)
	f := newForge(t)
	remote := f.repo("Chip/salsa")
	code = t.TempDir()
	project = filepath.Join(code, "salsa")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	gitAt(t, project, "clone", "--bare", "-q", remote, filepath.Join(project, ".base"))

	state = t.TempDir()
	res, err := Resolve(context.Background(), Request{
		Repo: "Chip/salsa", Branch: branch,
		Roots: []string{code}, StateRoot: state,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	commitIn(t, res.Dir, "agent.txt", "what the agent did")
	return code, project, state, res.Dir
}

func TestOpenInProjectCreatesTheBranchAtDocentsTip(t *testing.T) {
	code, project, state, _ := handoffFixture(t, "salsa-1/fix")

	res, err := OpenInProject(context.Background(), OpenRequest{
		Repo: "Chip/salsa", Branch: "salsa-1/fix",
		Roots: []string{code}, StateRoot: state,
	})
	if err != nil {
		t.Fatalf("OpenInProject: %v", err)
	}
	if !res.Created {
		t.Error("Created = false on a branch the developer has never had")
	}
	if !SamePath(res.Dir, filepath.Join(project, "salsa-1-fix")) {
		t.Errorf("Dir = %q", res.Dir)
	}
	// Nothing of theirs existed, so there is nothing to overwrite and the
	// agent's work is simply there.
	if got := headSubject(t, res.Dir); got != "agent.txt" {
		t.Errorf("HEAD subject = %q, want the agent's commit", got)
	}
	if res.Ahead != 0 {
		t.Errorf("Ahead = %d, want 0 once the branch is at docent's tip", res.Ahead)
	}
}

// The developer's branch is theirs. docent's tip arrives as a remote-tracking
// ref they can diff and merge when they choose, and the difference is reported
// rather than resolved.
func TestOpenInProjectNeverMergesIntoAnExistingBranch(t *testing.T) {
	code, project, state, _ := handoffFixture(t, "salsa-1/fix")
	// The developer already has the branch, somewhere else, with their own work.
	base := filepath.Join(project, ".base")
	scratch := filepath.Join(t.TempDir(), "scratch")
	gitAt(t, base, "worktree", "add", "-q", "-b", "salsa-1/fix", scratch)
	commitIn(t, scratch, "mine.txt", "what I did")
	// Free the branch so OpenInProject has to place it itself.
	gitAt(t, base, "worktree", "remove", "--force", scratch)

	res, err := OpenInProject(context.Background(), OpenRequest{
		Repo: "Chip/salsa", Branch: "salsa-1/fix",
		Roots: []string{code}, StateRoot: state,
	})
	if err != nil {
		t.Fatalf("OpenInProject: %v", err)
	}
	if got := headSubject(t, res.Dir); got != "mine.txt" {
		t.Errorf("HEAD subject = %q; the developer's branch was moved", got)
	}
	if res.Ahead != 1 {
		t.Errorf("Ahead = %d, want the one commit docent has and they do not", res.Ahead)
	}
	// Fetchable, so acting on the report is one command away.
	if !hasRef(context.Background(), base, "refs/remotes/docent/salsa-1/fix") {
		t.Error("docent's tip is not reachable from the developer's repository")
	}
}

func TestOpenInProjectReusesAWorktreeThatIsAlreadyThere(t *testing.T) {
	code, project, state, _ := handoffFixture(t, "salsa-1/fix")
	existing := filepath.Join(project, "somewhere-else")
	gitAt(t, filepath.Join(project, ".base"), "worktree", "add", "-q", "-b", "salsa-1/fix", existing)

	res, err := OpenInProject(context.Background(), OpenRequest{
		Repo: "Chip/salsa", Branch: "salsa-1/fix",
		Roots: []string{code}, StateRoot: state,
	})
	if err != nil {
		t.Fatalf("OpenInProject: %v", err)
	}
	if res.Created {
		t.Error("Created = true for a worktree that already existed")
	}
	if !SamePath(res.Dir, existing) {
		t.Errorf("Dir = %q, want the worktree git already had at %q", res.Dir, existing)
	}
}

// An ordinary clone has one working tree and the developer is in it. Switching
// it out from under them on a click is not something a button should do.
func TestOpenInProjectRefusesAnOrdinaryCheckout(t *testing.T) {
	requireGit(t)
	f := newForge(t)
	remote := f.repo("Chip/salsa")
	code := t.TempDir()
	f.clone(code, "salsa", remote)

	_, err := OpenInProject(context.Background(), OpenRequest{
		Repo: "Chip/salsa", Branch: "salsa-1/fix",
		Roots: []string{code}, StateRoot: t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "ordinary checkout") {
		t.Fatalf("err = %v, want a refusal naming the shape of the repository", err)
	}
}

// docent may never have touched this repository, and a worktree the developer
// can use is worth having either way.
func TestOpenInProjectWorksWithNothingOfDocents(t *testing.T) {
	requireGit(t)
	f := newForge(t)
	remote := f.repo("Chip/salsa")
	code := t.TempDir()
	project := filepath.Join(code, "salsa")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	gitAt(t, project, "clone", "--bare", "-q", remote, filepath.Join(project, ".base"))

	res, err := OpenInProject(context.Background(), OpenRequest{
		Repo: "Chip/salsa", Branch: "salsa-1/fix",
		Roots: []string{code}, StateRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("OpenInProject: %v", err)
	}
	if !res.Created || currentBranch(t, res.Dir) != "salsa-1/fix" {
		t.Errorf("res = %+v, branch = %q", res, currentBranch(t, res.Dir))
	}
}
