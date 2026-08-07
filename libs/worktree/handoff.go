package worktree

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// docentRemote is where docent's own tips land in the developer's repository.
// A remote-tracking namespace rather than a branch, for the same reason
// origin/* is one: it is somebody else's copy, visible and fetchable, and never
// something a local branch is silently moved onto.
const docentRemote = "docent"

// OpenRequest asks for a worktree of a branch in the developer's own project.
type OpenRequest struct {
	// Repo is the host-relative repository identity. Required.
	Repo string
	// Branch is the branch to open. Required.
	Branch string
	// OpenPath and Roots locate the developer's copy, as in Request.
	OpenPath string
	Roots    []string
	// Hook is the per-worktree setup script, run on creation.
	Hook string
	// StateRoot overrides docent's state directory, for tests.
	StateRoot string
}

// OpenResult is a worktree in the developer's project.
type OpenResult struct {
	// Dir is the worktree, whether it was created here or already existed.
	Dir string
	// Created reports that this call made it.
	Created bool
	// Ahead counts commits docent has on this branch that the developer's copy
	// does not. Non-zero means there is work to pull over, and that docent did
	// not pull it over for them.
	Ahead int
	// SetupErr is the setup hook's failure, if it had one. Not fatal.
	SetupErr error
}

// OpenInProject gives the developer a worktree of a branch, in their own
// project, with whatever docent has done to it fetchable from there.
//
// This is the other half of the two-copy problem, and it is deliberately
// asymmetric with the pre-turn sync. docent pulls the developer's commits into
// its own tree automatically, because that tree is docent's to move. It does not
// push into theirs. Their branch is only ever created at their own tip, and
// docent's is offered beside it as refs/remotes/docent/<branch> -- fetchable,
// diffable, mergeable when they decide to. A merge nobody asked for, into a
// repository somebody has open, is the one thing this whole design is arranged
// to avoid.
//
// A branch that exists nowhere in their repository is the exception, and not
// really one: created at docent's tip, there is nothing of theirs to overwrite.
func OpenInProject(ctx context.Context, req OpenRequest) (OpenResult, error) {
	repo := strings.Trim(strings.TrimSpace(req.Repo), "/")
	branch := strings.Trim(strings.TrimSpace(req.Branch), "/")
	if repo == "" || branch == "" {
		return OpenResult{}, fmt.Errorf("worktree: a repository and a branch are required")
	}
	project, ok := DeveloperProject(req.OpenPath, req.Roots, repo)
	if !ok {
		return OpenResult{}, fmt.Errorf("worktree: no local copy of %s to open", repo)
	}
	if project.Kind != KindWorktrees {
		return OpenResult{}, fmt.Errorf("worktree: %s is an ordinary checkout; open it and switch branches yourself",
			Display(project.Dir))
	}

	theirs := "refs/heads/" + branch
	ours := "refs/remotes/" + docentRemote + "/" + branch
	fetchDocentTip(ctx, project.GitDir, req.StateRoot, repo, branch)

	layout, err := List(ctx, project.GitDir)
	if err != nil {
		return OpenResult{}, err
	}
	res := OpenResult{}
	if dir, ok := layout.ByBranch[branch]; ok {
		res.Dir = dir
	} else {
		dir := filepath.Join(project.Dir, SanitizePath(branch))
		if _, err := os.Stat(dir); err == nil {
			return OpenResult{}, fmt.Errorf("worktree: %s already exists", Display(dir))
		}
		if err := openWorktreeAt(ctx, project.GitDir, dir, branch, ours); err != nil {
			return OpenResult{}, err
		}
		res.Dir, res.Created = dir, true
		res.SetupErr = RunHook(ctx, HookRequest{
			Script:     req.Hook,
			Dir:        dir,
			Branch:     branch,
			Repo:       repo,
			ProjectDir: project.Dir,
			Reference:  referenceFor(ctx, project, true, project.GitDir, dir),
		})
	}
	if hasRef(ctx, project.GitDir, ours) && hasRef(ctx, project.GitDir, theirs) {
		if _, ahead, err := countRange(ctx, project.GitDir, theirs, ours); err == nil {
			res.Ahead = ahead
		}
	}
	return res, nil
}

// fetchDocentTip copies docent's tip for a branch into the developer's
// repository. Best effort: docent may never have worked on this repository, and
// a worktree they can use is worth having either way.
func fetchDocentTip(ctx context.Context, gitDir, stateRoot, repo, branch string) {
	base := IsolatedBase(stateRoot, repo)
	if !isBareRepo(base) {
		return
	}
	// By path rather than by a configured remote: this is a local directory on
	// the same machine, and nothing about it belongs in the developer's config.
	_ = gitRun(ctx, gitDir, fetchTimeout, "fetch", "--quiet", "--no-tags", base,
		"+refs/heads/"+branch+":refs/remotes/"+docentRemote+"/"+branch)
}

// openWorktreeAt creates the branch's worktree, at docent's tip only when the
// branch does not exist in the developer's repository at all.
func openWorktreeAt(ctx context.Context, gitDir, dir, branch, docentRef string) error {
	if hasRef(ctx, gitDir, "refs/heads/"+branch) {
		// Their branch, at their tip. docent's is beside it to look at.
		return gitRun(ctx, gitDir, gitTimeout, "worktree", "add", "--quiet", dir, branch)
	}
	if hasRef(ctx, gitDir, docentRef) {
		if err := gitRun(ctx, gitDir, gitTimeout, "worktree", "add", "--quiet", "-b", branch, dir, docentRef); err != nil {
			return err
		}
		// Upstream is the forge, not docent: a push from here should go where
		// the developer expects, and docent's copy is a peer rather than a
		// place to publish to.
		if hasRef(ctx, gitDir, "refs/remotes/origin/"+branch) {
			_ = gitRun(ctx, gitDir, gitTimeout, "branch", "--quiet",
				"--set-upstream-to=origin/"+branch, branch)
		}
		return nil
	}
	return addWorktree(ctx, gitDir, dir, branch, "")
}

// IsolatedBase is docent's bare repository for a repo, whether or not it exists.
func IsolatedBase(stateRootOverride, repo string) string {
	return filepath.Join(stateRoot(stateRootOverride), projectsDir,
		SanitizePath(strings.Trim(strings.TrimSpace(repo), "/")), baseDirName)
}
