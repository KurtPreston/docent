package worktree

import (
	"context"
	"fmt"
	"strings"
)

// addSpec is one `git worktree add`: which repository, where, which branch, and
// what to narrow the new tree to.
type addSpec struct {
	// base is the repository the worktree is added to.
	base string
	// dir is where the worktree goes.
	dir string
	// branch is the branch checked out in it.
	branch string
	// baseRef is the ref a brand-new branch is created from.
	baseRef string
	// sparse are the cone directories to limit the tree to. Empty means all.
	sparse []string
}

// add runs `git worktree add` with args and, given a cone, narrows the result.
//
// With a cone the add skips its checkout, so the excluded directories are never
// written, and the checkout runs once the cone is set. A tree whose narrowing
// fails is removed rather than handed out: it would otherwise sit there empty,
// and the next provisioning would reuse it as it stands.
func (s addSpec) add(ctx context.Context, args ...string) error {
	if len(s.sparse) == 0 {
		return gitRun(ctx, s.base, gitTimeout, append([]string{"worktree", "add", "--quiet"}, args...)...)
	}
	add := append([]string{"worktree", "add", "--quiet", "--no-checkout"}, args...)
	if err := gitRun(ctx, s.base, gitTimeout, add...); err != nil {
		return err
	}

	// Set the cone, then populate the tree within it.
	cone := append([]string{"sparse-checkout", "set", "--cone", "--"}, s.sparse...)
	err := gitRun(ctx, s.dir, gitTimeout, cone...)
	if err == nil {
		err = gitRun(ctx, s.dir, gitTimeout, "checkout", "--quiet")
	}
	if err != nil {
		_ = removeWorktree(ctx, s.base, s.dir)
		return fmt.Errorf("worktree: sparse checkout of %s: %w", s.dir, err)
	}
	return nil
}

// SparseFor returns the sparse directories configured for repo, matching the
// "owner/name" key case-insensitively, or nil for a full checkout.
func SparseFor(sparse map[string][]string, repo string) []string {
	repo = strings.Trim(strings.TrimSpace(repo), "/")
	for key, dirs := range sparse {
		if strings.EqualFold(strings.Trim(strings.TrimSpace(key), "/"), repo) {
			return cleanSparse(dirs)
		}
	}
	return nil
}

// cleanSparse trims each directory and drops empty and duplicate entries, so a
// stray blank line in the config cannot widen the cone to the whole repository.
func cleanSparse(dirs []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, d := range dirs {
		d = strings.Trim(strings.TrimSpace(d), "/")
		if d == "" || d == "." || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}
