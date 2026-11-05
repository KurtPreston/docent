package worktree

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// lastUsedFile is written into a worktree's private git directory each time
// docent hands the worktree out. It lives there rather than in the checkout so
// it never shows up in `git status` or gets swept into a turn's commit.
const lastUsedFile = "docent-last-used"

// minReapIdle is the shortest idle time Reap honours. An automation run marks
// its worktree once, at provisioning, and may then work in it for up to half an
// hour; a shorter retention would delete a tree out from under a running agent.
const minReapIdle = time.Hour

// ReapRequest describes a sweep of docent's own worktrees.
type ReapRequest struct {
	// MaxIdle is how long a worktree may go unprovisioned before it is removed.
	// Values below an hour are raised to an hour. Zero means no idle limit.
	MaxIdle time.Duration
	// MaxBytes caps the disk docent's worktrees may use together. Over it, the
	// least recently used trees are removed, oldest first, until usage is back
	// under, however young they are, down to the same one-hour floor as
	// MaxIdle. Zero means no cap.
	MaxBytes int64
	// Keep reports directories that must survive regardless of age, such as
	// those an agent session is bound to. Optional.
	Keep func(dir string) bool
	// Now is the sweep's clock. Zero means time.Now.
	Now time.Time
	// StateRoot overrides the docent state directory, for tests.
	StateRoot string
}

// ReapResult reports what a sweep did.
type ReapResult struct {
	// Removed are the worktrees deleted for sitting idle past MaxIdle.
	Removed []string
	// Evicted are the worktrees deleted to bring usage under MaxBytes.
	Evicted []string
	// UsedBytes is the disk docent's worktrees use after the sweep. Only
	// measured when MaxBytes is set.
	UsedBytes int64
	// Dirty are idle worktrees left in place because they hold uncommitted
	// changes, which removing would destroy.
	Dirty []string
	// Errors are per-worktree failures. One bad tree does not stop the sweep.
	Errors []error
}

// Reap removes docent's own worktrees that have sat idle past MaxIdle, then,
// if the rest still use more than MaxBytes, the least recently used of those.
//
// Every branch docent works on gets a full checkout under its state directory,
// with whatever the setup hook installs on top, and nothing else ever removes
// them; left alone they fill the disk. Removing one is cheap to undo: the branch
// and its commits stay in the shared bare repository, and the next provisioning
// recreates the checkout. What cannot be undone is losing uncommitted edits, so
// dirty trees are skipped and reported instead.
func Reap(ctx context.Context, req ReapRequest) (ReapResult, error) {
	var res ReapResult
	now := req.Now
	if now.IsZero() {
		now = time.Now()
	}

	// Gather every docent-owned tree across repository roots.
	trees, bases, err := collectTrees(ctx, req, &res)
	if err != nil {
		return res, err
	}

	// Remove trees idle past the retention, keeping the rest for the budget.
	var live []*reapTree
	for _, t := range trees {
		if req.MaxIdle > 0 && !t.kept {
			t.outcome = reapOne(ctx, t, max(req.MaxIdle, minReapIdle), now, &res)
		}
		if t.outcome == reapRemoved {
			res.Removed = append(res.Removed, t.dir)
			continue
		}
		live = append(live, t)
	}
	if req.MaxBytes > 0 {
		evictOverBudget(ctx, budgetSweep{trees: live, maxBytes: req.MaxBytes, now: now}, &res)
	}

	// Let git forget the checkouts that are gone.
	for _, base := range bases {
		if err := gitRun(ctx, base, gitTimeout, "worktree", "prune"); err != nil {
			res.Errors = append(res.Errors, err)
		}
	}
	return res, nil
}

// reapTree is one docent-owned worktree a sweep considers.
type reapTree struct {
	root    string
	base    string
	dir     string
	kept    bool
	outcome reapOutcome
}

// reapOutcome is what reapOne did with a tree.
type reapOutcome int

const (
	reapUntouched reapOutcome = iota
	reapRemoved
	reapDirty
	reapFailed
)

// collectTrees lists the branch checkouts under every repository root in the
// state directory, along with each root's bare repository.
func collectTrees(ctx context.Context, req ReapRequest, res *ReapResult) ([]*reapTree, []string, error) {
	projects := filepath.Join(stateRoot(req.StateRoot), projectsDir)
	entries, err := os.ReadDir(projects)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("worktree: reading %s: %w", projects, err)
	}

	// Each child of the projects directory is one repository's root.
	var trees []*reapTree
	var bases []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		root := filepath.Join(projects, e.Name())
		base := filepath.Join(root, baseDirName)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		layout, err := List(ctx, base)
		if err != nil {
			res.Errors = append(res.Errors, err)
			continue
		}
		bases = append(bases, base)

		// Only branch checkouts under this root are docent's to remove.
		for _, dir := range layout.ByBranch {
			if !underRoot(dir, root) {
				continue
			}
			kept := req.Keep != nil && req.Keep(dir)
			trees = append(trees, &reapTree{root: root, base: base, dir: dir, kept: kept})
		}
	}
	return trees, bases, nil
}

// reapOne removes t if it has been idle at least minIdle and is clean.
//
// The checks run under the same lock provisioning takes, so a branch being
// handed out right now is either marked fresh before the check or provisioned
// again after the removal, never deleted mid-handout.
func reapOne(ctx context.Context, t *reapTree, minIdle time.Duration, now time.Time, res *ReapResult) reapOutcome {
	release, err := baseLocks.acquire(ctx, t.root)
	if err != nil {
		res.Errors = append(res.Errors, err)
		return reapFailed
	}
	defer release()

	// Skip anything used recently or no longer a checkout at all.
	if _, err := os.Stat(t.dir); err != nil {
		return reapUntouched
	}
	if now.Sub(lastUsed(ctx, t.dir)) < minIdle {
		return reapUntouched
	}

	// Uncommitted edits are the one thing a removal loses for good.
	dirty, err := IsDirty(ctx, t.dir)
	if err != nil {
		res.Errors = append(res.Errors, err)
		return reapFailed
	}
	if dirty {
		res.Dirty = append(res.Dirty, t.dir)
		return reapDirty
	}
	if err := removeWorktree(ctx, t.base, t.dir); err != nil {
		res.Errors = append(res.Errors, err)
		return reapFailed
	}
	return reapRemoved
}

// markUsed records that dir was just handed out. Best effort: a worktree whose
// mark could not be written falls back to git's own timestamps in lastUsed.
func markUsed(ctx context.Context, dir string) {
	gitDir, err := worktreeGitDir(ctx, dir)
	if err != nil {
		return
	}
	stamp := time.Now().UTC().Format(time.RFC3339) + "\n"
	_ = os.WriteFile(filepath.Join(gitDir, lastUsedFile), []byte(stamp), 0o644)
}

// lastUsed is when dir was last handed out or touched by git.
//
// The mark markUsed writes is the primary answer. Worktrees made before marks
// existed fall back to the newest of git's own bookkeeping files, which move on
// every checkout, commit and status.
func lastUsed(ctx context.Context, dir string) time.Time {
	gitDir, err := worktreeGitDir(ctx, dir)
	if err != nil {
		return modTime(dir)
	}
	if t := modTime(filepath.Join(gitDir, lastUsedFile)); !t.IsZero() {
		return t
	}
	var newest time.Time
	for _, name := range []string{"HEAD", "index", "logs/HEAD"} {
		if t := modTime(filepath.Join(gitDir, name)); t.After(newest) {
			newest = t
		}
	}
	if newest.IsZero() {
		return modTime(dir)
	}
	return newest
}

// worktreeGitDir is the private git directory of the linked worktree at dir.
func worktreeGitDir(ctx context.Context, dir string) (string, error) {
	out, err := gitOutput(ctx, dir, listTimeout, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// modTime is path's modification time, or zero when it cannot be read.
func modTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// underRoot reports whether dir sits inside root.
func underRoot(dir, root string) bool {
	rel, err := filepath.Rel(root, dir)
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..")
}
