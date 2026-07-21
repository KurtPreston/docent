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
	if _, err := store.ApplyEvent(id, "open", "", ""); err != nil {
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
	if _, err := store.ApplyEvent(id, "close", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get(id.Key()); ok {
		t.Fatal("record should be removed after close")
	}
}

func TestRemoteEventBindsToExtensionRecord(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Client-side extension window: concrete host + ssh alias (a remote session).
	ext := Identity{IDE: "cursor", IDEHost: "mac", TargetHost: "desktop", Path: "/home/me/proj"}
	if _, err := store.ApplyEvent(ext, "open", "", ""); err != nil {
		t.Fatal(err)
	}
	// Remote hook event: knows it is remote, but not its host or the ssh alias.
	hook := Identity{IDE: "cursor", Remote: true, Path: "/home/me/proj"}
	if _, err := store.ApplyEvent(hook, "agent_response_received", "", ""); err != nil {
		t.Fatal(err)
	}
	if len(store.data) != 1 {
		t.Fatalf("remote event should bind, not fork: got %d records %+v", len(store.data), store.data)
	}
	rec, ok := store.Get(ext.Key())
	if !ok {
		t.Fatal("extension record should still exist")
	}
	if rec.LastAgentStopAt == "" {
		t.Fatal("agent stop should be recorded on the bound extension record")
	}
}

func TestRemoteEventPrefersMostRecentRemote(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	older := Identity{IDE: "cursor", IDEHost: "mac", TargetHost: "desktop", Path: "/home/me/proj"}
	newer := Identity{IDE: "cursor", IDEHost: "laptop", TargetHost: "desktop", Path: "/home/me/proj"}
	store.data[older.Key()] = Record{IDE: "cursor", IDEHost: "mac", TargetHost: "desktop", Path: "/home/me/proj", LastHeartbeatAt: "2026-01-01T00:00:00Z"}
	store.data[newer.Key()] = Record{IDE: "cursor", IDEHost: "laptop", TargetHost: "desktop", Path: "/home/me/proj", LastHeartbeatAt: "2026-01-02T00:00:00Z"}

	hook := Identity{IDE: "cursor", Remote: true, Path: "/home/me/proj"}
	if _, err := store.ApplyEvent(hook, "agent_request_sent", "", ""); err != nil {
		t.Fatal(err)
	}
	if store.data[newer.Key()].LastPromptAt == "" {
		t.Fatal("remote event should bind to the most-recently-active remote record")
	}
	if store.data[older.Key()].LastPromptAt != "" {
		t.Fatal("older remote record should be untouched")
	}
}

func TestRemoteEventIgnoresLocalSession(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	// A local session at the same path (no ssh alias).
	local := Identity{IDE: "cursor", IDEHost: "devbox", Path: "/home/me/proj"}
	if _, err := store.ApplyEvent(local, "open", "", ""); err != nil {
		t.Fatal(err)
	}
	hook := Identity{IDE: "cursor", Remote: true, Path: "/home/me/proj"}
	if _, err := store.ApplyEvent(hook, "agent_response_received", "", ""); err != nil {
		t.Fatal(err)
	}
	if store.data[local.Key()].LastAgentStopAt != "" {
		t.Fatal("remote hook event must not bind to a local (non-remote) session")
	}
	if len(store.data) != 2 {
		t.Fatalf("remote event should create its own fallback record: got %d", len(store.data))
	}
}

func TestRemoteEventFallbackCreatesRecord(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	hook := Identity{IDE: "cursor", Remote: true, Path: "/home/me/proj"}
	if _, err := store.ApplyEvent(hook, "agent_request_sent", "", ""); err != nil {
		t.Fatal(err)
	}
	rec, ok := store.Get(hook.Key())
	if !ok {
		t.Fatal("fallback record should be created when no remote session exists")
	}
	if !rec.Remote {
		t.Fatal("fallback record should carry Remote=true")
	}
	if rec.LastPromptAt == "" {
		t.Fatal("prompt time should be recorded on the fallback record")
	}
}

func TestSweep(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	id := Identity{IDE: "cursor", IDEHost: "mac", Path: "/code/proj"}
	if _, err := store.ApplyEvent(id, "heartbeat", "", ""); err != nil {
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
