package worktree

import (
	"context"
	"sync"
)

// baseLocks serializes bare-clone provisioning for a project root. Agent runs
// lock per repo+branch, but the shared .base clone is per repository, so two
// branches starting at once would both pass isBareRepo and race git clone.
var baseLocks = &keyedMutex{}

// keyedMutex is a map of independent, on-demand mutexes identified by an
// arbitrary string key. Unlike sync.Mutex, acquire is context-aware so a
// caller can bound how long it waits for a busy key.
type keyedMutex struct {
	mu sync.Mutex
	ch map[string]chan struct{}
}

// acquire blocks until key's lock is free or ctx is done. On success it
// returns a release func that must be called exactly once to unlock.
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
