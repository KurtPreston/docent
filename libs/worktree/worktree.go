// Package worktree is docent's model of where a repository's working
// directories live on disk.
//
// # Two layouts, told apart by what git reports
//
// A repository on a developer's machine has one of two shapes, and docent
// handles both without knowing which tool produced either:
//
//   - A worktree project: a directory whose children are working trees, with
//     the repository itself in a bare git directory beside them. Detected
//     structurally -- some child is a bare repository -- so any layout of that
//     shape qualifies, whatever the bare directory is called and whichever tool
//     created it.
//   - A single checkout: an ordinary clone, its git directory at .git.
//
// Nothing here reads a third-party configuration file or matches a directory
// name against a known convention. The only inputs are the filesystem and git.
package worktree

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Layout is the parsed form of `git worktree list --porcelain`.
//
// One parser exists because two would be two ways for a repository to look like
// two different repositories. The collector that attributes commits to
// directories and the provisioner that creates those directories have to agree
// on what git said, down to which entry is the main one.
type Layout struct {
	// MainDir is the repository's main working tree, or the bare git directory
	// itself when the repository has no working tree. It is always git's first
	// entry.
	MainDir string
	// Bare reports that MainDir is a bare repository. A worktree project's main
	// entry is always bare, which is what distinguishes "the one working tree of
	// an ordinary clone" from "the object store a pile of worktrees share".
	Bare bool
	// ByBranch maps a local branch name to the absolute path of the worktree
	// that has it checked out. Bare and detached entries carry no branch line
	// and so are absent.
	ByBranch map[string]string
}

// Home is where a branch with no worktree of its own belongs: the repository's
// primary working tree, or "" when it has none.
//
// An ordinary clone's one working tree is the home for every branch it has, and
// stays so even after linked worktrees are added beside it. A repository whose
// main entry is bare has no home at all, and the directory a scan happened to
// start from is not an answer -- naming one worktree among many makes every
// merged and backported branch look like it lives there.
func (l Layout) Home() string {
	if l.Bare {
		return ""
	}
	return l.MainDir
}

// Parse reads `git worktree list --porcelain` output.
//
// Entries are separated by blank lines, but a `worktree` line also ends the
// previous entry, so output without a trailing separator parses the same.
func Parse(out string) Layout {
	l := Layout{ByBranch: map[string]string{}}
	var dir, branch string
	bare, first := false, true
	flush := func() {
		if dir != "" {
			if first {
				l.MainDir, l.Bare, first = dir, bare, false
			}
			if branch != "" {
				l.ByBranch[branch] = dir
			}
		}
		dir, branch, bare = "", "", false
	}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "worktree "):
			flush()
			dir = strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
		case line == "bare":
			bare = true
		case strings.HasPrefix(line, "branch "):
			branch = BranchName(strings.TrimPrefix(line, "branch "))
		}
	}
	flush()
	return l
}

// BranchName reduces a ref to a local branch name, or "" when it is not one.
func BranchName(ref string) string {
	ref = strings.TrimSpace(ref)
	const heads = "refs/heads/"
	if !strings.HasPrefix(ref, heads) {
		return ""
	}
	return strings.TrimPrefix(ref, heads)
}

// listTimeout bounds a worktree listing. Short because it reads local
// bookkeeping and nothing else; bounded at all because a wedged filesystem
// should not hang the daemon.
const listTimeout = 30 * time.Second

// List asks git for dir's worktrees. dir may be any working tree of the
// repository, or the git directory itself.
func List(ctx context.Context, dir string) (Layout, error) {
	out, err := gitOutput(ctx, dir, listTimeout, "worktree", "list", "--porcelain")
	if err != nil {
		return Layout{}, err
	}
	return Parse(out), nil
}

// SamePath reports whether two paths name the same directory, resolving
// symlinks when it can. A code_home reached through a symlink and the real path
// git prints are the same directory, and a comparison that says otherwise
// silently drops every attribution in that tree.
func SamePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		return false
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		return false
	}
	return ra == rb
}

// gitOutput runs git in dir and returns its stdout, folding stderr into the
// error because git explains itself there and nowhere else.
func gitOutput(ctx context.Context, dir string, timeout time.Duration, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", args...)
	cmd.Dir = dir
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = 10 * time.Second
	if err := cmd.Run(); err != nil {
		if cctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("git %s in %s timed out after %s%s",
				strings.Join(args, " "), dir, timeout, detail(stderr.String()))
		}
		return "", fmt.Errorf("git %s in %s: %w%s",
			strings.Join(args, " "), dir, err, detail(stderr.String()))
	}
	return stdout.String(), nil
}

func detail(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if len(s) > 2000 {
		s = s[:2000] + "…"
	}
	return "\n" + s
}
