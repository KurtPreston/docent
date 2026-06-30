package registry

import "testing"

func TestSessionStatus(t *testing.T) {
	if got := SessionStatus(Record{}); got != "idle" {
		t.Fatalf("empty = %q", got)
	}
	r := Record{LastAgentStopAt: "2026-01-01T00:00:00Z"}
	if got := SessionStatus(r); got != "needs-followup" {
		t.Fatalf("stop only = %q", got)
	}
	r.LastFocusedAt = "2026-01-02T00:00:00Z"
	if got := SessionStatus(r); got != "idle" {
		t.Fatalf("focused after stop = %q", got)
	}
}
