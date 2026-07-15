package automation

import (
	"context"
	"strings"
	"sync"
)

// worktreeLocks serializes agent runs that target the same provisioned
// working directory (see ProvisionWorkdir), keyed by worktreeLockKey. Two
// automation rules can independently match the same PR (e.g. autofix-pr and
// autofix-pr-conflicts) and fire close enough in time that their agent runs
// overlap; without this, both would provision/reset/clean up the same
// worktree concurrently. Package-level because AgentRunner is stored and
// copied by value in the action Registry, so a struct field would not be
// shared across copies.
var worktreeLocks = &keyedMutex{}

// keyedMutex is a map of independent, on-demand mutexes identified by an
// arbitrary string key. Unlike sync.Mutex, acquire is context-aware so a
// caller can bound how long it waits for a busy key.
type keyedMutex struct {
	mu sync.Mutex
	ch map[string]chan struct{}
}

// acquire blocks until key's lock is free or ctx is done. On success it
// returns a release func that must be called exactly once to unlock; on
// failure it returns ctx.Err(). The lock map is never evicted (bounded by
// the number of distinct repo/branch or open_path targets ever seen, which
// is negligible).
func (k *keyedMutex) acquire(ctx context.Context, key string) (func(), error) {
	k.mu.Lock()
	if k.ch == nil {
		k.ch = map[string]chan struct{}{}
	}
	c, ok := k.ch[key]
	if !ok {
		c = make(chan struct{}, 1)
		k.ch[key] = c
	}
	k.mu.Unlock()

	select {
	case c <- struct{}{}:
		return func() { <-c }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// worktreeLockKey identifies the working directory an agent action will
// provision, mirroring the identity ProvisionWorkdir resolves to: worktree
// mode keys on repo+branch (matching the wtPath it computes), open_path mode
// keys on the literal path (a developer's own checkout has no branch
// isolation to key on).
func worktreeLockKey(mode, repo, branch, openPath string) string {
	if mode == WorkdirOpenPath {
		return "path:" + strings.TrimSpace(openPath)
	}
	return "wt:" + sanitizePath(repo) + "@" + sanitizePath(branch)
}
