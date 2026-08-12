package worktree

import (
	"context"
	"sync"
)

// baseLocks serializes provisioning against a project root's shared bare
// clone, keyed by that root. Agent runs lock per repo+branch, but .base is per
// repository, so without this two branches starting at once would race each
// other inside git. See provision.
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
