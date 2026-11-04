package engine

import (
	"context"
	"log"
	"time"

	"github.com/KurtPreston/docent/libs/worktree"
)

// worktreeReapInterval is how often the daemon sweeps its own worktrees. Idle
// retention is measured in days, so hourly is plenty, and each sweep costs a
// `git status` per idle tree.
const worktreeReapInterval = time.Hour

// startWorktreeReaper sweeps docent's idle worktrees once at startup and then
// hourly, until ctx is cancelled. A zero retention disables it.
func (e *Engine) startWorktreeReaper(ctx context.Context) {
	retention := e.cfg.WorktreeRetentionDuration()
	if retention <= 0 {
		return
	}
	go func() {
		e.reapWorktrees(ctx, retention)
		ticker := time.NewTicker(worktreeReapInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				e.reapWorktrees(ctx, retention)
			}
		}
	}()
}

// reapWorktrees runs one sweep and logs what it did.
//
// Directories bound to an agent session are kept whatever their age: a session
// records its directory once and resumes into it, so removing it would strand
// the lane. Without the session store there is no way to tell which those are,
// so the sweep is skipped entirely rather than guessed at.
func (e *Engine) reapWorktrees(ctx context.Context, retention time.Duration) {
	keep, ok := e.sessionDirs()
	if !ok {
		return
	}
	res, err := worktree.Reap(ctx, worktree.ReapRequest{
		MaxIdle: retention,
		Keep:    func(dir string) bool { return keepsDir(keep, dir) },
	})
	if err != nil {
		log.Printf("worktrees: sweep: %v", err)
		return
	}

	// Report removals, trees kept for uncommitted work, and failures.
	for _, dir := range res.Removed {
		log.Printf("worktrees: removed %s (idle more than %s)", dir, retention)
	}
	for _, dir := range res.Dirty {
		log.Printf("worktrees: kept idle %s: it has uncommitted changes", dir)
	}
	for _, err := range res.Errors {
		log.Printf("worktrees: sweep: %v", err)
	}
}

// keepsDir reports whether dir is one of keep, comparing through symlinks: a
// session may have recorded the path by a different route than git prints it.
func keepsDir(keep []string, dir string) bool {
	for _, k := range keep {
		if worktree.SamePath(k, dir) {
			return true
		}
	}
	return false
}

// sessionDirs is the directories agent sessions are bound to. ok is false when
// the session store is unavailable or unreadable.
func (e *Engine) sessionDirs() ([]string, bool) {
	if e.agents == nil || e.agents.Store == nil {
		return nil, false
	}
	sessions, err := e.agents.Store.List()
	if err != nil {
		log.Printf("worktrees: listing agent sessions: %v", err)
		return nil, false
	}
	var dirs []string
	for _, s := range sessions {
		if s.Dir != "" {
			dirs = append(dirs, s.Dir)
		}
	}
	return dirs, true
}
