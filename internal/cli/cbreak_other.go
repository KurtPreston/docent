//go:build !linux && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd

package cli

import "errors"

// enableCbreak is unsupported on this platform; the abort listener falls
// back to a no-op (collection still runs, just without the press-'c'
// shortcut).
func enableCbreak(fd int) (func(), error) {
	return nil, errors.New("cbreak mode not supported on this platform")
}
