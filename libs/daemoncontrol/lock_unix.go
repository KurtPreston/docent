//go:build !windows

package daemoncontrol

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// WithLock serializes CLI mutations per installation. Keep the lock file in
// place: removing it would let another process lock a different inode.
func WithLock(binary string, fn func() error) error {
	path := filepath.Join(filepath.Dir(binary), ".docent.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open installation lock: %w", err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("another lifecycle/update command may be running for this installation: %w", err)
	}
	return errors.Join(fn(), syscall.Flock(int(f.Fd()), syscall.LOCK_UN))
}
