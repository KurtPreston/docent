package automation

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/KurtPreston/docent/libs/worktree"
)

// WorkdirMode selects how an agent action provisions its working directory.
//
// These are the automation-facing names, kept because they are written in
// people's config files. They map onto the target kinds the picker offers:
// worktree is worktree.TargetIsolated, open_path is a directory named outright.
const (
	WorkdirWorktree = "worktree"  // docent's own isolated worktree for the branch
	WorkdirOpenPath = "open_path" // an existing checkout, used as-is
)

// WorkdirRequest describes a directory to provision for an agent job.
type WorkdirRequest struct {
	Mode   string // worktree | open_path
	Repo   string // owner/repo
	Branch string
	// Target is the placement the user picked: one of the worktree.Target*
	// kinds. Empty means docent's own isolated worktree, which is what an
	// automation gets -- a rule fires unattended, and the placements that touch
	// the developer's repository are only ever chosen deliberately.
	Target string
	// From is the ref a brand-new branch is based on. Empty means the remote's
	// default branch, which is right for fresh work and wrong for a backport.
	From string
	// OpenPath is a developer checkout. In open_path mode it is the directory to
	// use; in worktree mode it is the best evidence of which local copy this work
	// item belongs to.
	OpenPath string
	// Roots are directories to search for the developer's own copy of Repo --
	// the same roots local-git scans.
	Roots []string
	// RemoteURL overrides where docent clones from, for a repository with no
	// local copy to read an origin off.
	RemoteURL string
	// Hook is the per-worktree setup script run on a freshly created directory.
	Hook string
	// StateRoot overrides docent's state directory, for tests.
	StateRoot string
}

// WorkdirResult is a provisioned working directory.
type WorkdirResult struct {
	// Path is the directory the agent runs in.
	Path string
	// ProjectDir is the root that owns Path, empty in open_path mode.
	ProjectDir string
	// Owned reports that Path is docent's own directory: safe to commit into and
	// to sync, as opposed to one the developer may have open in an editor.
	Owned bool
	// PreviousBranch is the branch an in-place checkout switched away from.
	PreviousBranch string
	// SetupErr is the setup hook's failure, if it had one. Not fatal.
	SetupErr error
}

// ProvisionWorkdir resolves the working directory for an agent job.
//
// Worktree mode gives the agent a checkout of its own, under docent's state
// directory, cloned from the same remote the developer uses. This is the third
// answer docent has had to the question, and the reasoning behind each is worth
// keeping:
//
// The first kept a second clone with no per-worktree setup, so the agent worked
// in a directory the developer had never opened and could not use -- a parallel
// universe. The second put the agent in the developer's own worktree, which
// solved that and created two new problems: git allows one worktree per branch,
// so an agent and a human could not hold the same branch at once, and an agent
// rewriting files under an open editor is destructive in a way no amount of
// care fixes.
//
// This is not a return to the first. What made it a parallel universe is
// addressed rather than tolerated: the developer's commits reach docent through
// a local remote before every turn, docent's reach them when they ask to open
// one, every turn ends in a commit so nothing is invisible, and a fork between
// the two is refused rather than merged. Setup is a configured hook's job, which
// is also the only honest place for it -- what makes a checkout usable is a
// property of the repository, not something docent can infer.
//
// Nothing is cleaned up afterwards. docent owns everything under its state
// directory, so unlike the second design a reclaim is at least possible later.
func ProvisionWorkdir(ctx context.Context, req WorkdirRequest) (WorkdirResult, error) {
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = WorkdirWorktree
	}
	switch mode {
	case WorkdirOpenPath:
		path := strings.TrimSpace(req.OpenPath)
		if path == "" {
			return WorkdirResult{}, fmt.Errorf("workdir open_path requires OpenPath")
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			return WorkdirResult{}, fmt.Errorf("open_path %q is not a directory: %w", path, err)
		}
		// The developer's directory. Everything keyed on Owned -- the turn-end
		// commit, the divergence guard -- stays out of it.
		return WorkdirResult{Path: path}, nil
	case WorkdirWorktree:
		return provisionWorktree(ctx, req)
	default:
		return WorkdirResult{}, fmt.Errorf("unknown workdir mode %q", mode)
	}
}

func provisionWorktree(ctx context.Context, req WorkdirRequest) (WorkdirResult, error) {
	wreq := worktree.Request{
		Repo:      req.Repo,
		Branch:    req.Branch,
		BaseRef:   req.From,
		OpenPath:  req.OpenPath,
		Roots:     req.Roots,
		RemoteURL: req.RemoteURL,
		Hook:      req.Hook,
		Target:    req.Target,
		StateRoot: req.StateRoot,
	}
	resolve := worktree.Resolve
	if t := strings.TrimSpace(req.Target); t != "" && t != worktree.TargetIsolated {
		resolve = worktree.ResolveDeveloper
	}
	res, err := resolve(ctx, wreq)
	if err != nil {
		return WorkdirResult{}, err
	}
	return WorkdirResult{
		Path:           res.Dir,
		ProjectDir:     res.Project,
		Owned:          res.Owned,
		PreviousBranch: res.PreviousBranch,
		SetupErr:       res.SetupErr,
	}, nil
}
