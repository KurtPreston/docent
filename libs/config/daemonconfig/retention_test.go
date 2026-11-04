package daemonconfig

import (
	"testing"
	"time"
)

func TestWorktreeRetentionDuration(t *testing.T) {
	cases := map[string]time.Duration{
		"":      DefaultWorktreeRetention,
		"24h":   24 * time.Hour,
		"0":     0,
		"off":   0,
		"Never": 0,
		// A typo must not silently disable cleanup.
		"3 days": DefaultWorktreeRetention,
		"-1h":    DefaultWorktreeRetention,
	}
	for in, want := range cases {
		got := DaemonConfig{WorktreeRetention: in}.WorktreeRetentionDuration()
		if got != want {
			t.Errorf("WorktreeRetention %q = %s; want %s", in, got, want)
		}
	}
}
