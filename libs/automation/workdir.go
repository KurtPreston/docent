package automation

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/KurtPreston/docent/libs/config/docentconfig"
)

// WorkdirMode selects how an agent action provisions its working directory.
const (
	WorkdirWorktree = "worktree"  // docent-owned bare clone + branch worktree
	WorkdirOpenPath = "open_path" // developer's existing checkout
)

// WorkdirRequest describes a directory to provision for an agent job.
type WorkdirRequest struct {
	Mode      string // worktree | open_path
	Repo      string // owner/repo
	Branch    string
	RemoteURL string // git remote URL for cloning
	OpenPath  string // developer's checkout (for open_path, or object reference)
	StateDir  string // override state root; empty → docentconfig.StateDir()
}

// WorkdirResult is a provisioned working directory.
type WorkdirResult struct {
	Path       string
	ClonePath  string // bare clone path when Mode=worktree
	Cleanup    func() error
	RemoveTree bool // if true, Cleanup removes the worktree
}

// ProvisionWorkdir creates or reuses a working directory for an agent job.
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
		return WorkdirResult{Path: path, Cleanup: func() error { return nil }}, nil
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
	remote := strings.TrimSpace(req.RemoteURL)
	if remote == "" {
		return WorkdirResult{}, fmt.Errorf("workdir worktree requires RemoteURL")
	}
	state := strings.TrimSpace(req.StateDir)
	if state == "" {
		state = docentconfig.StateDir()
	}
	repoKey := sanitizePath(req.Repo)
	if repoKey == "" {
		repoKey = sanitizePath(filepath.Base(strings.TrimSuffix(remote, ".git")))
	}
	clonePath := filepath.Join(state, "repos", repoKey+".git")
	wtPath := filepath.Join(state, "worktrees", repoKey, sanitizePath(branch))

	if err := os.MkdirAll(filepath.Dir(clonePath), 0o755); err != nil {
		return WorkdirResult{}, err
	}
	if err := ensureBareClone(ctx, clonePath, remote, req.OpenPath); err != nil {
		return WorkdirResult{}, err
	}
	if _, err := os.Stat(wtPath); err == nil {
		if worktreeIsValid(ctx, wtPath) {
			// Reuse existing worktree; hard-reset it to the freshly fetched tip.
			// Fetch inside the worktree so FETCH_HEAD updates without trying to
			// move the (checked-out) branch ref in the bare clone.
			if err := runGit(ctx, wtPath, "fetch", "origin", branch); err != nil {
				return WorkdirResult{}, fmt.Errorf("git fetch (reuse) %s: %w", branch, err)
			}
			if err := runGit(ctx, wtPath, "reset", "--hard", "FETCH_HEAD"); err != nil {
				return WorkdirResult{}, fmt.Errorf("git reset (reuse) %s: %w", branch, err)
			}
			return WorkdirResult{
				Path:      wtPath,
				ClonePath: clonePath,
				Cleanup:   func() error { return nil },
			}, nil
		}
		// Corrupted leftover (e.g. an orphaned process recreated files under a
		// path a prior run's cleanup had deleted): discard it and fall through
		// to the fresh worktree-add path below instead of failing the run.
		_ = os.RemoveAll(wtPath)
		_ = runGit(ctx, clonePath, "worktree", "prune")
	}

	// Fetch the branch into a local head ref in the bare clone. A bare clone
	// does not keep remote-tracking (origin/*) refs, and `git fetch origin
	// <branch>` only updates FETCH_HEAD — neither lets `git worktree add
	// <branch>` resolve a branch created *after* the clone. An explicit
	// refspec writes refs/heads/<branch> so the worktree can check it out.
	if err := runGit(ctx, clonePath, "fetch", "origin", "+refs/heads/"+branch+":refs/heads/"+branch); err != nil {
		// Fall back to a full fetch (covers unusual ref layouts).
		if err2 := runGit(ctx, clonePath, "fetch", "origin"); err2 != nil {
			return WorkdirResult{}, fmt.Errorf("git fetch %s: %v (also: %v)", branch, err, err2)
		}
	}

	if err := os.MkdirAll(filepath.Dir(wtPath), 0o755); err != nil {
		return WorkdirResult{}, err
	}
	if err := runGit(ctx, clonePath, "worktree", "add", "--force", wtPath, branch); err != nil {
		return WorkdirResult{}, fmt.Errorf("git worktree add %s: %w", branch, err)
	}
	return WorkdirResult{
		Path:       wtPath,
		ClonePath:  clonePath,
		RemoveTree: true,
		Cleanup: func() error {
			_ = runGit(context.Background(), clonePath, "worktree", "remove", "--force", wtPath)
			_ = os.RemoveAll(wtPath)
			return nil
		},
	}, nil
}

// worktreeIsValid reports whether path is a usable git worktree, as opposed
// to a leftover partial directory (e.g. an orphaned process recreated files
// under it after a prior run's cleanup deleted the worktree, or docentd was
// killed mid-provision). rev-parse is the same check that failed in the
// SALSA-12529 incident ("fatal: not a git repository").
func worktreeIsValid(ctx context.Context, path string) bool {
	out, err := exec.CommandContext(ctx, "git", "-C", path, "rev-parse", "--is-inside-work-tree").Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

func ensureBareClone(ctx context.Context, clonePath, remote, reference string) error {
	if _, err := os.Stat(filepath.Join(clonePath, "HEAD")); err == nil {
		return nil
	}
	_ = os.RemoveAll(clonePath)
	args := []string{"clone", "--bare", "--filter=blob:none"}
	if ref := strings.TrimSpace(reference); ref != "" {
		if _, err := os.Stat(ref); err == nil {
			args = append(args, "--reference", ref)
		}
	}
	args = append(args, remote, clonePath)
	cmd := exec.CommandContext(ctx, "git", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git clone --bare: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func runGit(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func sanitizePath(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		case r == '/' || r == ':' || r == ' ':
			b.WriteByte('-')
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// ResolveRemoteURL tries to get origin URL from an existing checkout.
func ResolveRemoteURL(ctx context.Context, openPath string) (string, error) {
	if strings.TrimSpace(openPath) == "" {
		return "", fmt.Errorf("empty open path")
	}
	cmd := exec.CommandContext(ctx, "git", "-C", openPath, "remote", "get-url", "origin")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git remote get-url: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}
