package registry

import (
	"path/filepath"
	"testing"
	"time"
)

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

func TestIdentityKey(t *testing.T) {
	a := Identity{IDE: "Cursor", IDEHost: "Mac", TargetHost: "", Path: "/home/me/proj/"}
	b := Identity{IDE: "cursor", IDEHost: "mac", TargetHost: "", Path: "/home/me/proj"}
	if a.Key() != b.Key() {
		t.Fatalf("keys should normalize equal: %q vs %q", a.Key(), b.Key())
	}
	c := Identity{IDE: "vscode", IDEHost: "mac", Path: "/home/me/proj"}
	if a.Key() == c.Key() {
		t.Fatal("different IDE should produce a different key")
	}
	if a.Name() != "proj" {
		t.Fatalf("Name() = %q, want proj", a.Name())
	}
}

func TestApplyEventAndClose(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	id := Identity{IDE: "cursor", IDEHost: "mac", Path: "/code/proj"}
	if err := store.ApplyEvent(id, "open", "", ""); err != nil {
		t.Fatal(err)
	}
	rec, ok := store.Get(id.Key())
	if !ok {
		t.Fatal("record should exist after open")
	}
	if rec.Name != "proj" || rec.LastOpenAt == "" || rec.LastHeartbeatAt == "" {
		t.Fatalf("unexpected record after open: %+v", rec)
	}
	if _, ok := store.GetByName("proj"); !ok {
		t.Fatal("GetByName should find the record")
	}
	if err := store.ApplyEvent(id, "close", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get(id.Key()); ok {
		t.Fatal("record should be removed after close")
	}
}

func TestSweep(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	id := Identity{IDE: "cursor", IDEHost: "mac", Path: "/code/proj"}
	if err := store.ApplyEvent(id, "heartbeat", "", ""); err != nil {
		t.Fatal(err)
	}
	// Not yet stale.
	if removed := store.Sweep(time.Minute, time.Now()); len(removed) != 0 {
		t.Fatalf("fresh record should survive sweep, removed %v", removed)
	}
	// Stale relative to a future now.
	future := time.Now().Add(2 * time.Minute)
	if removed := store.Sweep(time.Minute, future); len(removed) != 1 {
		t.Fatalf("stale record should be swept, removed %v", removed)
	}
	if _, ok := store.Get(id.Key()); ok {
		t.Fatal("record should be gone after sweep")
	}
}
