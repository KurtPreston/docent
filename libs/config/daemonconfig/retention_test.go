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

func TestWorktreeMaxDiskBytes(t *testing.T) {
	cases := map[string]int64{
		"":       DefaultWorktreeMaxDisk,
		"200GB":  200 << 30,
		"200 GB": 200 << 30,
		"1.5t":   3 << 39,
		"512MiB": 512 << 20,
		"4096":   4096,
		"0":      0,
		"off":    0,
		// A typo must not silently disable the cap.
		"lots":   DefaultWorktreeMaxDisk,
		"-5GB":   DefaultWorktreeMaxDisk,
		"NaNGB":  DefaultWorktreeMaxDisk,
		"1e30TB": DefaultWorktreeMaxDisk,
	}
	for in, want := range cases {
		got := DaemonConfig{WorktreeMaxDisk: in}.WorktreeMaxDiskBytes()
		if got != want {
			t.Errorf("WorktreeMaxDisk %q = %d; want %d", in, got, want)
		}
	}
}
