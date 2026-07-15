//go:build unix

package automation

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestConfigureProcGroupKillsGrandchildren reproduces the orphaned-grandchild
// bug: a timed-out command backgrounds a grandchild (simulating e.g. yarn
// spawning vitest) that would otherwise outlive a plain SIGKILL of the direct
// child. configureProcGroup must ensure the whole group dies with it.
func TestConfigureProcGroupKillsGrandchildren(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// The backgrounded "sleep 0.3 && touch marker" is a grandchild relative
	// to this test: it's forked by sh, not exec'd directly by Go. It inherits
	// sh's process group (we never call setpgid on it ourselves), so a
	// group-wide kill reaches it; a kill of only sh's pid would not.
	cmd := exec.CommandContext(ctx, "sh", "-c", "sleep 0.3 && touch "+marker+" & sleep 5")
	configureProcGroup(cmd)

	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	_ = cmd.Wait()

	// Grace period comfortably longer than the grandchild's 0.3s sleep, so a
	// surviving orphan would have had time to create the marker.
	time.Sleep(600 * time.Millisecond)

	if _, err := os.Stat(marker); err == nil {
		t.Fatal("marker file was created: grandchild survived the process-group kill")
	} else if !os.IsNotExist(err) {
		t.Fatalf("unexpected stat error: %v", err)
	}
}
