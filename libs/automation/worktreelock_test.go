package automation

import (
	"context"
	"errors"
	"testing"
	"time"
)

// newTestKeyedMutex returns a fresh, isolated keyedMutex so tests don't share
// state with the package-level worktreeLocks (or each other).
func newTestKeyedMutex() *keyedMutex {
	return &keyedMutex{}
}

func TestKeyedMutex_SameKeySerializes(t *testing.T) {
	km := newTestKeyedMutex()

	release, err := km.acquire(context.Background(), "same")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	acquired := make(chan struct{})
	go func() {
		release2, err := km.acquire(context.Background(), "same")
		if err != nil {
			t.Errorf("second acquire: %v", err)
			return
		}
		close(acquired)
		release2()
	}()

	// The second acquire must not complete while the first holds the lock.
	select {
	case <-acquired:
		t.Fatal("second acquire completed while first still held the lock")
	case <-time.After(100 * time.Millisecond):
	}

	release()

	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("second acquire did not complete after the first released")
	}
}

func TestKeyedMutex_DifferentKeysParallel(t *testing.T) {
	km := newTestKeyedMutex()

	releaseA, err := km.acquire(context.Background(), "a")
	if err != nil {
		t.Fatalf("acquire a: %v", err)
	}
	defer releaseA()

	done := make(chan struct{})
	go func() {
		releaseB, err := km.acquire(context.Background(), "b")
		if err != nil {
			t.Errorf("acquire b: %v", err)
			return
		}
		defer releaseB()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("acquire of a different key blocked on an unrelated held key")
	}
}

func TestKeyedMutex_AcquireReturnsCtxErrWhenBlocked(t *testing.T) {
	km := newTestKeyedMutex()

	release, err := km.acquire(context.Background(), "busy")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err = km.acquire(ctx, "busy")
	if err == nil {
		t.Fatal("expected an error when the key stays busy past the context deadline")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}

func TestWorktreeLockKey(t *testing.T) {
	if got := worktreeLockKey(WorkdirWorktree, "Chip/salsa", "salsa-123-fix", ""); got != "wt:Chip-salsa@salsa-123-fix" {
		t.Fatalf("worktree key = %q", got)
	}
	if got := worktreeLockKey(WorkdirOpenPath, "Chip/salsa", "salsa-123-fix", "/home/dev/salsa"); got != "path:/home/dev/salsa" {
		t.Fatalf("open_path key = %q", got)
	}
	// Same repo+branch always resolves to the same key regardless of other
	// event fields, since that's the identity ProvisionWorkdir keys on.
	k1 := worktreeLockKey(WorkdirWorktree, "Chip/salsa", "salsa-123-fix", "")
	k2 := worktreeLockKey(WorkdirWorktree, "Chip/salsa", "salsa-123-fix", "/some/unrelated/path")
	if k1 != k2 {
		t.Fatalf("worktree-mode key should ignore openPath: k1=%q k2=%q", k1, k2)
	}
}
