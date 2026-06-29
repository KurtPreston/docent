package cli

import (
	"io"
	"os"
	"sync"
	"time"

	"golang.org/x/term"
)

// abortKeyHint is the one-line prompt shown before collection starts so
// the user knows they can cut a slow run short.
const abortKeyHint = "Press 'c' to abort pending collection and continue with whatever has been gathered so far."

// startAbortListener watches the terminal for the abort key ('c'/'C')
// while collection runs. When pressed, onAbort is invoked exactly once —
// the CLI uses this to cancel the collection context so collectors return
// the partial data they have already gathered instead of finishing every
// pending request.
//
// The terminal is put into cbreak mode (canonical line buffering and echo
// off, but signal handling left intact so Ctrl-C still interrupts the
// process) for the duration of the listen. The returned stop function
// restores the terminal and ends the watcher; it is safe to call more
// than once.
//
// When in is not a TTY (piped stdin, tests) the listener is a no-op and
// stop does nothing, so non-interactive runs behave exactly as before.
func startAbortListener(in io.Reader, onAbort func()) (stop func()) {
	f, ok := in.(*os.File)
	if !ok || f == nil || !term.IsTerminal(int(f.Fd())) {
		return func() {}
	}
	fd := int(f.Fd())
	restoreTTY, err := enableCbreak(fd)
	if err != nil {
		return func() {}
	}

	var (
		once    sync.Once
		abortCB sync.Once
	)
	stopped := make(chan struct{})
	stop = func() {
		once.Do(func() {
			restoreTTY()
			close(stopped)
			// Best effort: nudge a blocked Read so the watcher goroutine
			// can exit promptly. Terminals that don't support deadlines
			// simply ignore this and the goroutine unwinds at exit.
			_ = f.SetReadDeadline(time.Now())
		})
	}

	go func() {
		buf := make([]byte, 1)
		for {
			n, err := f.Read(buf)
			if err != nil {
				return
			}
			select {
			case <-stopped:
				return
			default:
			}
			if n == 0 {
				continue
			}
			if buf[0] == 'c' || buf[0] == 'C' {
				abortCB.Do(onAbort)
				return
			}
		}
	}()

	return stop
}
