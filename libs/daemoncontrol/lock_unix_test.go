//go:build !windows

package daemoncontrol

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestInstallationLock(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "docent")
	err := WithLock(binary, func() error {
		if err := WithLock(binary, func() error { t.Error("overlapping operation entered"); return nil }); err == nil {
			t.Error("overlapping operation acquired lock")
		}
		return errors.New("operation failed")
	})
	if err == nil {
		t.Fatal("operation failure lost")
	}
	if err := WithLock(binary, func() error { return nil }); err != nil {
		t.Fatalf("lock leaked after failure: %v", err)
	}
}
