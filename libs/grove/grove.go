// Package grove drives the grove CLI so docent's agent worktrees are the same
// worktrees the developer already works in.
//
// # Why delegate instead of provisioning
//
// docent used to clone every repository a second time, into $STATE/repos and
// $STATE/worktrees. That worked but produced a parallel universe: the agent's
// checkout was not the one in the editor, it had none of grove's per-worktree
// setup (copied .env files, the deterministic branch color the IDE title bar
// uses), and a lane in the cockpit pointed at a directory the developer had
// never seen. Delegating to `grove path BRANCH` puts the agent in
// ~/Code/<project>/<branch> instead, so promoting a session to a Cursor window
// is opening a directory that is already correct rather than reconciling two.
//
// grove also already solves the parts that are easy to get subtly wrong:
// resolve-or-create, basing a new branch off a chosen ref, reconciling worktrees
// deleted outside git, and never removing branch refs.
//
// # How a project is located
//
// grove infers its project from the working directory, walking up for the .base
// bare clone, so every call needs a directory inside the project. docent knows
// repositories by "owner/repo", so this package bridges the two: it discovers
// projects under the roots local-git already scans and matches them by their
// origin remote.
package grove

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/KurtPreston/docent/libs/model"
)

// BaseDir is the bare clone at a grove project root. Its presence is what marks
// a directory as a project, and is how grove itself identifies one.
const BaseDir = ".base"

// DefaultTimeout bounds one grove invocation. Generous because creating a
// worktree on a large repository fetches from the remote and copies ignored
// files; bounded because a git credential prompt would otherwise hang the daemon
// forever.
const DefaultTimeout = 10 * time.Minute

// CLI invokes the grove binary.
type CLI struct {
	// Command is the binary, defaulting to "grove".
	Command string
	// Env, when non-nil, replaces the child's environment. Leave it nil in the
	// daemon: grove needs PATH to find git, and git needs HOME to find the
	// credential helper.
	Env []string
	// Timeout bounds one invocation. Zero means DefaultTimeout.
	Timeout time.Duration
}

func (c CLI) bin() string {
	if s := strings.TrimSpace(c.Command); s != "" {
		return s
	}
	return "grove"
}

// ErrNotAProject is returned when a directory is not inside a grove project.
// Recovering from it means running `grove clone`, which is a deliberate one-time
// act by the developer, not something docent should do behind their back.
var ErrNotAProject = errors.New("grove: not inside a grove project (no .base found)")

// Path resolves branch's worktree inside projectDir, creating it if needed, and
// returns its absolute path.
//
// from, when set, is the ref a brand-new branch is based on. Passing it matters
// for a daemon: without --from and without a TTY grove bases new branches on the
// default branch, which is right for fresh work and wrong for a backport.
func (c CLI) Path(ctx context.Context, projectDir, branch, from string) (string, error) {
	branch = strings.Trim(strings.TrimSpace(branch), "/")
	if branch == "" {
		return "", errors.New("grove: path requires a branch")
	}
	if err := requireProject(projectDir); err != nil {
		return "", err
	}
	args := []string{"path", branch}
	if f := strings.TrimSpace(from); f != "" {
		args = append(args, "--from", f)
	}
	out, err := c.run(ctx, projectDir, args...)
	if err != nil {
		return "", err
	}
	// grove prints progress to stderr and the path to stdout, so the last
	// non-empty stdout line is the answer even if a future version adds chatter.
	path := lastLine(out)
	if path == "" {
		return "", fmt.Errorf("grove path %s: no path printed", branch)
	}
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("grove path %s printed %q, which is not usable: %w", branch, path, err)
	}
	return path, nil
}

// Worktree is one entry from `grove list --porcelain`.
type Worktree struct {
	Branch string
	Path   string
}

// Worktrees lists the project's existing worktrees without creating anything.
// Useful for answering "is this branch already checked out somewhere" before
// deciding to run an agent in it.
func (c CLI) Worktrees(ctx context.Context, projectDir string) ([]Worktree, error) {
	if err := requireProject(projectDir); err != nil {
		return nil, err
	}
	out, err := c.run(ctx, projectDir, "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var wts []Worktree
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		branch, path, ok := strings.Cut(sc.Text(), "\t")
		if !ok {
			continue
		}
		branch, path = strings.TrimSpace(branch), strings.TrimSpace(path)
		if branch != "" && path != "" {
			wts = append(wts, Worktree{Branch: branch, Path: path})
		}
	}
	return wts, sc.Err()
}

func (c CLI) run(ctx context.Context, dir string, args ...string) (string, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, c.bin(), args...)
	cmd.Dir = dir
	if c.Env != nil {
		cmd.Env = c.Env
	}
	// grove reads a base-branch choice from stdin when it is a TTY. /dev/null
	// guarantees the non-interactive path rather than a child blocked on a read
	// nobody will answer.
	cmd.Stdin = nil
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	configureProcGroup(cmd)
	cmd.WaitDelay = 10 * time.Second

	if err := cmd.Run(); err != nil {
		if cctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("grove %s timed out after %s (a credential prompt?)%s",
				strings.Join(args, " "), timeout, detail(stderr.String()))
		}
		return "", fmt.Errorf("grove %s: %w%s", strings.Join(args, " "), err, detail(stderr.String()))
	}
	return stdout.String(), nil
}

// detail appends grove's stderr to an error. grove explains itself there and
// nowhere else, so dropping it turns every failure into a bare exit status.
func detail(s string) string {
	s = strings.TrimSpace(stripANSI(s))
	if s == "" {
		return ""
	}
	if len(s) > 2000 {
		s = s[:2000] + "…"
	}
	return "\n" + s
}

// stripANSI removes the color escapes grove writes to stderr, which are noise in
// a log file or a JSON error field.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] == ';' || (s[j] >= '0' && s[j] <= '9')) {
				j++
			}
			if j < len(s) {
				j++ // the final byte of the escape
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func lastLine(s string) string {
	var last string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			last = l
		}
	}
	return last
}

// IsProject reports whether dir is a grove project root.
func IsProject(dir string) bool {
	if strings.TrimSpace(dir) == "" {
		return false
	}
	st, err := os.Stat(filepath.Join(dir, BaseDir))
	return err == nil && st.IsDir()
}

func requireProject(dir string) error {
	if IsProject(dir) {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrNotAProject, dir)
}

// FindProject walks up from start to the grove project containing it, so a path
// docent already knows -- a worktree from local-git, a session's directory -- can
// be turned into a project root. This is the same walk grove performs, repeated
// here so the caller can decide what to do when there is no project rather than
// discovering it through a failed invocation.
func FindProject(start string) (string, bool) {
	dir := strings.TrimSpace(start)
	if dir == "" {
		return "", false
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	for {
		if IsProject(dir) {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// Project pairs a grove project root with the repository its bare clone tracks.
type Project struct {
	// Dir is the project root, the directory holding .base.
	Dir string
	// Repo is the host-relative repository identity ("Chip/salsa"), or "" when
	// the bare clone has no usable origin.
	Repo string
}

// DiscoverProjects finds grove projects at or immediately below each root, and
// labels each with the repository it tracks.
//
// Both levels are checked because the two shapes appear together in a normal
// setup: ~/Code/salsa is itself a project whose children are worktrees, while
// ~/Code is a directory of sibling projects. Results are sorted by path so
// repeated calls agree.
//
// The search stops at one level down on purpose. A deeper walk would descend into
// every worktree of every project -- thousands of directories, each with a .git
// file -- to find nothing, on a path that runs while an automation is waiting.
func DiscoverProjects(roots []string) []Project {
	seen := map[string]bool{}
	var out []Project
	add := func(dir string) {
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
		if seen[dir] || !IsProject(dir) {
			return
		}
		seen[dir] = true
		out = append(out, Project{Dir: dir, Repo: repoForProject(dir)})
	}
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		add(root)
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				add(filepath.Join(root, e.Name()))
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dir < out[j].Dir })
	return out
}

// repoRemoteTimeout bounds the origin lookup. Short because it is a local config
// read, and because discovery runs this once per candidate directory.
const repoRemoteTimeout = 10 * time.Second

func repoForProject(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), repoRemoteTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", filepath.Join(dir, BaseDir), "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return model.RepoKeyFromRemote(strings.TrimSpace(string(out)))
}

// ProjectForRepo picks the project tracking repo from the given roots.
//
// One repository can legitimately be cloned twice (a ~/Code/salsa and a
// ~/Code/salsa2 for a long-running experiment), and an agent must not land in
// whichever one os.ReadDir happened to return first. The tie is broken toward the
// project whose directory name matches the repository name, then the shortest
// path, then lexically -- which picks salsa over salsa2 and stays stable across
// runs.
func ProjectForRepo(roots []string, repo string) (string, bool) {
	repo = strings.Trim(strings.TrimSpace(repo), "/")
	if repo == "" {
		return "", false
	}
	var matches []string
	for _, p := range DiscoverProjects(roots) {
		if strings.EqualFold(p.Repo, repo) {
			matches = append(matches, p.Dir)
		}
	}
	if len(matches) == 0 {
		return "", false
	}
	best := matches[0]
	for _, m := range matches[1:] {
		if better(m, best, repoName(repo)) {
			best = m
		}
	}
	return best, true
}

// UniqueByRepo collapses projects to one per repository, keeping the same one
// ProjectForRepo would resolve to.
//
// Offering a caller a choice between two clones of the same repository is a
// false choice: a start request names a repository, and provisioning resolves
// that to a project deterministically, so both options would do the identical
// thing. Projects with no usable origin are dropped, since nothing docent
// collects can be joined to them.
func UniqueByRepo(projects []Project) []Project {
	byRepo := map[string]Project{}
	var order []string
	for _, p := range projects {
		key := strings.ToLower(p.Repo)
		if key == "" {
			continue
		}
		cur, seen := byRepo[key]
		if !seen {
			byRepo[key], order = p, append(order, key)
			continue
		}
		if better(p.Dir, cur.Dir, repoName(p.Repo)) {
			byRepo[key] = p
		}
	}
	out := make([]Project, 0, len(order))
	for _, key := range order {
		out = append(out, byRepo[key])
	}
	return out
}

// repoName is the trailing segment of an owner/repo identity, which is what a
// project directory is usually named after.
func repoName(repo string) string {
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		return repo[i+1:]
	}
	return repo
}

func better(candidate, current, repoName string) bool {
	cExact := strings.EqualFold(filepath.Base(candidate), repoName)
	bExact := strings.EqualFold(filepath.Base(current), repoName)
	if cExact != bExact {
		return cExact
	}
	if len(candidate) != len(current) {
		return len(candidate) < len(current)
	}
	return candidate < current
}
