package worktree

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveDeveloper provisions a working directory in the developer's own copy of
// a repository, for the targets that place an agent there.
//
// Everything it returns is unowned, so none of the turn-boundary machinery
// touches it: docent does not commit into a directory somebody may have open,
// does not fetch into their repository behind their back, and does not judge
// their working tree against its own. The self-healing that Resolve does -- the
// occupied path it deletes and rebuilds -- is likewise absent, because "this is
// not the checkout I expected" is a reason to stop, not to reach for rm -rf, in
// a directory that is not docent's.
//
// The target must be one docent offered. Both placements are real changes to the
// developer's repository, and neither should be arrived at by defaulting.
func ResolveDeveloper(ctx context.Context, req Request) (Result, error) {
	repo := strings.Trim(strings.TrimSpace(req.Repo), "/")
	branch := strings.Trim(strings.TrimSpace(req.Branch), "/")
	if branch == "" {
		return Result{}, fmt.Errorf("worktree: a branch is required")
	}
	project, ok := DeveloperProject(req.OpenPath, req.Roots, repo)
	if !ok {
		return Result{}, fmt.Errorf("worktree: no local copy of %s to work in", repo)
	}

	switch req.Target {
	case TargetExisting:
		return existingWorktree(ctx, project, branch)
	case TargetCreate:
		return createWorktree(ctx, project, branch, req)
	case TargetInPlace:
		return checkoutInPlace(ctx, project, branch, req)
	default:
		return Result{}, fmt.Errorf("worktree: %q is not a placement in the developer's project", req.Target)
	}
}

// existingWorktree returns the directory that already has the branch checked
// out, untouched: no hook, no fetch, no reset. It is a directory the developer
// has been working in, and it is already set up by definition.
func existingWorktree(ctx context.Context, p Project, branch string) (Result, error) {
	layout, err := List(ctx, p.GitDir)
	if err != nil {
		return Result{}, err
	}
	dir, ok := layout.ByBranch[branch]
	if !ok {
		return Result{}, fmt.Errorf("worktree: %s has no worktree on %s any more", Display(p.Dir), branch)
	}
	return Result{Dir: dir, Project: p.Dir}, nil
}

// createWorktree adds a worktree to the developer's project.
//
// Plain `git worktree add`, the same call Resolve makes, so the result is a
// first-class worktree to anything that reads `git worktree list` rather than
// something only docent understands.
func createWorktree(ctx context.Context, p Project, branch string, req Request) (Result, error) {
	if p.Kind != KindWorktrees {
		return Result{}, fmt.Errorf("worktree: %s is an ordinary checkout, not a project worktrees can be added to",
			Display(p.Dir))
	}
	dir := filepath.Join(p.Dir, SanitizePath(branch))
	if _, err := os.Stat(dir); err == nil {
		return Result{}, fmt.Errorf("worktree: %s already exists", Display(dir))
	}
	if err := addWorktree(ctx, p.GitDir, dir, branch, req.BaseRef); err != nil {
		return Result{}, err
	}
	res := Result{Dir: dir, Project: p.Dir, Created: true}
	res.SetupErr = RunHook(ctx, HookRequest{
		Script:     req.Hook,
		Dir:        dir,
		Branch:     branch,
		Repo:       p.Repo,
		ProjectDir: p.Dir,
		BaseRef:    req.BaseRef,
		Reference:  referenceFor(ctx, p, true, p.GitDir, dir),
		Owned:      false,
	})
	return res, nil
}

// checkoutInPlace switches an ordinary clone onto the branch.
//
// This is the one placement that changes something the developer is looking at,
// so the tree has to be clean -- rechecked here because the picker's answer is
// as old as the page -- and the branch they were on is reported back so they can
// be told. It is not switched back afterwards: the agent's work would then be on
// a branch nothing has checked out, which is the surprise this option exists to
// avoid.
func checkoutInPlace(ctx context.Context, p Project, branch string, req Request) (Result, error) {
	if p.Kind == KindWorktrees {
		return Result{}, fmt.Errorf("worktree: %s keeps its branches in separate worktrees; add one instead",
			Display(p.Dir))
	}
	dirty, err := IsDirty(ctx, p.Dir)
	if err != nil {
		return Result{}, err
	}
	if dirty {
		return Result{}, fmt.Errorf("worktree: %s has uncommitted changes; commit or stash them first",
			Display(p.Dir))
	}

	was := ""
	if out, err := gitOutput(ctx, p.Dir, gitTimeout, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		was = strings.TrimSpace(out)
	}
	created := false
	if hasRef(ctx, p.Dir, "refs/heads/"+branch) {
		if err := gitRun(ctx, p.Dir, gitTimeout, "checkout", "--quiet", branch); err != nil {
			return Result{}, err
		}
	} else {
		if err := checkoutNewBranch(ctx, p.Dir, branch, req.BaseRef); err != nil {
			return Result{}, err
		}
		created = true
	}

	res := Result{Dir: p.Dir, Project: p.Dir, PreviousBranch: was}
	if created {
		// The hook runs for a branch that did not exist, because that is when a
		// checkout may need files it has never had. An existing branch is one
		// the developer has already been on.
		res.SetupErr = RunHook(ctx, HookRequest{
			Script:     req.Hook,
			Dir:        p.Dir,
			Branch:     branch,
			Repo:       p.Repo,
			ProjectDir: p.Dir,
			BaseRef:    req.BaseRef,
			Owned:      false,
		})
	}
	return res, nil
}

// checkoutNewBranch creates the branch in an ordinary clone, from the remote's
// copy when there is one so a later push is a fast-forward.
func checkoutNewBranch(ctx context.Context, dir, branch, baseRef string) error {
	remoteRef := "refs/remotes/origin/" + branch
	_ = gitRun(ctx, dir, fetchTimeout, "fetch", "--quiet", "origin", "+refs/heads/"+branch+":"+remoteRef)
	if hasRef(ctx, dir, remoteRef) {
		return gitRun(ctx, dir, gitTimeout, "checkout", "--quiet", "--track", "-b", branch, "origin/"+branch)
	}
	start, err := startPoint(ctx, dir, baseRef)
	if err != nil {
		return err
	}
	return gitRun(ctx, dir, gitTimeout, "checkout", "--quiet", "-b", branch, start)
}
