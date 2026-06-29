//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package cli

import "golang.org/x/sys/unix"

// enableCbreak switches the terminal at fd into cbreak mode. See the
// Linux implementation for the rationale; the only difference here is the
// ioctl request constants used to read and write the termios struct on
// the BSD-derived platforms (including macOS).
func enableCbreak(fd int) (func(), error) {
	termios, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	if err != nil {
		return nil, err
	}
	old := *termios
	termios.Lflag &^= unix.ICANON | unix.ECHO
	termios.Cc[unix.VMIN] = 1
	termios.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, unix.TIOCSETA, termios); err != nil {
		return nil, err
	}
	restore := func() {
		saved := old
		_ = unix.IoctlSetTermios(fd, unix.TIOCSETA, &saved)
	}
	return restore, nil
}
