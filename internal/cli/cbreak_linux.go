//go:build linux

package cli

import "golang.org/x/sys/unix"

// enableCbreak switches the terminal at fd into cbreak mode: canonical
// line buffering and echo are disabled so a single keypress is delivered
// immediately, but signal generation (ISIG) and output post-processing
// (OPOST/newline translation) are left intact. The latter matters here
// because the live collector progress table relies on normal "\n"
// handling, and we still want Ctrl-C to interrupt the process.
//
// It returns a function that restores the previous terminal settings.
func enableCbreak(fd int) (func(), error) {
	termios, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return nil, err
	}
	old := *termios
	termios.Lflag &^= unix.ICANON | unix.ECHO
	termios.Cc[unix.VMIN] = 1
	termios.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, termios); err != nil {
		return nil, err
	}
	restore := func() {
		saved := old
		_ = unix.IoctlSetTermios(fd, unix.TCSETS, &saved)
	}
	return restore, nil
}
