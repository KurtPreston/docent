package automation

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/KurtPreston/docent/libs/grove"
)

// WorkdirMode selects how an agent action provisions its working directory.
const (
	WorkdirWorktree = "worktree"  // a grove worktree in the developer's own project
	WorkdirOpenPath = "open_path" // an existing checkout, used as-is
)

// WorkdirRequest describes a directory to provision for an agent job.
type WorkdirRequest struct {
	Mode   string // worktree | open_path
	Repo   string // owner/repo
	Branch string
	// From is the ref a brand-new branch is based on. Empty means grove's
	// default branch, which is right for fresh work and wrong for a backport.
	From string
	// OpenPath is a developer checkout. In open_path mode it is the directory to
	// use; in worktree mode it is the best hint for which grove project to work
	// in, since it is a worktree of the project already.
	OpenPath string
	// GroveRoots are directories to search for grove projects when OpenPath does
	// not resolve one. These are the same roots local-git scans.
	GroveRoots []string
	// Grove overrides the CLI, for tests.
	Grove grove.CLI
}

// WorkdirResult is a provisioned working directory.
type WorkdirResult struct {
	// Path is the directory the agent runs in.
	Path string
	// ProjectDir is the grove project root that owns Path, empty in open_path
	// mode. Callers that want grove's own view of the worktree start here.
	ProjectDir string
}

// ProvisionWorkdir resolves the working directory for an agent job.
//
// Worktree mode delegates to grove rather than provisioning anything itself.
// docent used to keep its own bare clone and worktree tree under $STATE, which
// meant the agent edited a checkout the developer had never opened, without the
// per-worktree setup (copied .env files, the branch color the editor's title bar
// uses) that makes a worktree usable. Now an agent lands in
// ~/Code/<project>/<branch> -- the same directory a human would get -- so
// promoting a session to an editor is opening a directory that is already right.
//
// The tradeoff is that the repository must already be a grove project. That is
// deliberate: cloning it silently is what produced the second universe, and
// `grove clone` is a one-time act better left to the developer.
//
// Nothing is cleaned up afterwards. These are the developer's real worktrees, and
// deleting one because an agent finished with it would take uncommitted work with
// it. Reclaiming space is `grove prune`, which understands what is merged.
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
		return WorkdirResult{Path: path}, nil
	case WorkdirWorktree:
		return provisionWorktree(ctx, req)
	default:
		return WorkdirResult{}, fmt.Errorf("unknown workdir mode %q", mode)
	}
}

func provisionWorktree(ctx context.Context, req WorkdirRequest) (WorkdirResult, error) {
	branch := strings.TrimSpace(req.Branch)
	if branch == "" {
		return WorkdirResult{}, fmt.Errorf("workdir worktree requires Branch")
	}
	project, err := ResolveGroveProject(req.OpenPath, req.GroveRoots, req.Repo)
	if err != nil {
		return WorkdirResult{}, err
	}
	path, err := req.Grove.Path(ctx, project, branch, req.From)
	if err != nil {
		return WorkdirResult{}, err
	}
	return WorkdirResult{Path: path, ProjectDir: project}, nil
}

// ResolveGroveProject finds the grove project an agent should work in.
//
// openPath wins when it resolves, because it is evidence about this specific
// work item rather than an inference from a repo name: it is the worktree the
// developer's own activity happened in. Only when there is none -- the common
// case for a PR event with no local commits -- does it fall back to matching the
// repository against the configured roots.
func ResolveGroveProject(openPath string, roots []string, repo string) (string, error) {
	if dir, ok := grove.FindProject(openPath); ok {
		return dir, nil
	}
	if dir, ok := grove.ProjectForRepo(roots, repo); ok {
		return dir, nil
	}
	switch {
	case strings.TrimSpace(repo) == "":
		return "", fmt.Errorf("workdir worktree: no grove project found (no open path, and no repo to look one up by)")
	case len(roots) == 0:
		return "", fmt.Errorf("workdir worktree: no grove project for %q, and no roots to search "+
			"(set code_home or paths on a local-git directive)", repo)
	default:
		return "", fmt.Errorf("workdir worktree: no grove project for %q under %s "+
			"(clone it with `grove clone`)", repo, strings.Join(roots, ", "))
	}
}
