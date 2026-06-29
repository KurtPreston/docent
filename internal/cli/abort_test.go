package cli

import (
	"strings"
	"testing"
)

// TestStartAbortListenerNonTTY confirms the listener degrades to a no-op
// when stdin is not a terminal: stop must be safe to call and onAbort
// must never fire, so piped/non-interactive runs behave exactly as before.
func TestStartAbortListenerNonTTY(t *testing.T) {
	aborted := false
	stop := startAbortListener(strings.NewReader("ccc"), func() { aborted = true })
	if stop == nil {
		t.Fatal("expected a non-nil stop function")
	}
	stop()
	stop() // idempotent: calling twice must not panic
	if aborted {
		t.Fatal("onAbort must not be invoked for a non-TTY reader")
	}
}
