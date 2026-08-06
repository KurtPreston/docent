package registry

import (
	"os"
	"path/filepath"
	"strings"
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

// Only the IDE extension can see the remote authority. The agent hook reports
// the same window without one, so a heartbeat from it must not erase the value
// the deep link depends on.
func TestRemoteAuthorityIsRecordedAndNeverBlanked(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	const authority = "ssh-remote+7b22686f73744e616d65223a226465736b746f70227d"
	ext := Identity{IDE: "cursor", IDEHost: "mac", TargetHost: "desktop", RemoteAuthority: authority, Path: "/home/me/proj"}
	if _, err := store.ApplyEvent(ext, "open", "", ""); err != nil {
		t.Fatal(err)
	}
	if got := store.RemoteAuthorityForPath("/home/me/proj"); got != authority {
		t.Fatalf("RemoteAuthorityForPath = %q, want %q", got, authority)
	}

	hook := Identity{IDE: "cursor", Remote: true, Path: "/home/me/proj"}
	if _, err := store.ApplyEvent(hook, "agent_request_sent", "", ""); err != nil {
		t.Fatal(err)
	}
	if got := store.RemoteAuthorityForPath("/home/me/proj"); got != authority {
		t.Fatalf("hook event cleared the authority: got %q, want %q", got, authority)
	}
	// The authority is descriptive, not part of the identity: reporting the
	// same window with a different one must not fork a second record.
	if len(store.data) != 1 {
		t.Fatalf("got %d records, want 1: %+v", len(store.data), store.data)
	}
	if got := store.RemoteAuthorityForPath("/home/me/elsewhere"); got != "" {
		t.Fatalf("unknown path should have no authority, got %q", got)
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

func TestFocusClearsNeedsFollowup(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	id := Identity{IDE: "cursor", IDEHost: "mac", Path: "/code/proj"}
	if _, err := store.ApplyEvent(id, "agent_response_received", "", ""); err != nil {
		t.Fatal(err)
	}
	rec, _ := store.Get(id.Key())
	if got := SessionStatus(rec); got != "needs-followup" {
		t.Fatalf("after agent stop: got %q, want needs-followup", got)
	}
	if _, err := store.ApplyEvent(id, "focus", "", ""); err != nil {
		t.Fatal(err)
	}
	rec, _ = store.Get(id.Key())
	if got := SessionStatus(rec); got != "idle" {
		t.Fatalf("after focus: got %q, want idle", got)
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

// The real-world shape of the bug this reconciliation exists for: a Windows
// client-side extension reporting a remote path in Windows convention, and the
// agent hook on the Ubuntu box reporting the same path in POSIX convention.
// Before normalization these produced two records and no agent status.
func TestRemoteEventBindsAcrossPathSeparators(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	ext := Identity{IDE: "cursor", IDEHost: "REWWD-KPRESTON2", TargetHost: "desktop", Path: `\home\kpreston\Code\salsa\salsa-12722`}
	if _, err := store.ApplyEvent(ext, "open", "", ""); err != nil {
		t.Fatal(err)
	}
	hook := Identity{IDE: "cursor", Remote: true, TargetHost: "desktop", Path: "/home/kpreston/Code/salsa/salsa-12722"}
	if _, err := store.ApplyEvent(hook, "agent_request_sent", "", ""); err != nil {
		t.Fatal(err)
	}
	if len(store.data) != 1 {
		t.Fatalf("separator difference forked the session: got %d records %+v", len(store.data), store.data)
	}
	for _, rec := range store.data {
		if rec.LastPromptAt == "" {
			t.Error("agent activity did not attach to the window record")
		}
		if strings.Contains(rec.Path, `\`) {
			t.Errorf("stored path should be normalized, got %q", rec.Path)
		}
		if SessionStatus(rec) != "working" {
			t.Errorf("status = %q, want working", SessionStatus(rec))
		}
	}
}

// Records persisted before normalization must be re-keyed on load, otherwise
// existing sessions stay unmatchable until each window is reopened.
func TestLoadMigratesWindowsStylePaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	legacy := `{
  "cursor\u001frewwd-kpreston2\u001fdesktop\u001f\\home\\me\\Code\\proj": {
    "ide": "cursor",
    "ideHost": "REWWD-KPRESTON2",
    "targetHost": "desktop",
    "path": "\\home\\me\\Code\\proj",
    "name": "proj",
    "lastHeartbeatAt": "2026-01-02T00:00:00Z"
  }
}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	want := Identity{IDE: "cursor", IDEHost: "REWWD-KPRESTON2", TargetHost: "desktop", Path: "/home/me/Code/proj"}
	rec, ok := store.Get(want.Key())
	if !ok {
		t.Fatalf("record was not re-keyed; store = %+v", store.data)
	}
	if rec.Path != "/home/me/Code/proj" {
		t.Errorf("path = %q, want normalized", rec.Path)
	}
	if len(store.data) != 1 {
		t.Errorf("migration should not duplicate: %+v", store.data)
	}

	hook := Identity{IDE: "cursor", Remote: true, Path: "/home/me/Code/proj"}
	if _, err := store.ApplyEvent(hook, "agent_response_received", "", ""); err != nil {
		t.Fatal(err)
	}
	if len(store.data) != 1 {
		t.Errorf("migrated record should accept hook events, got %+v", store.data)
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

func TestAgentWorkingAt(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	id := Identity{IDE: "cursor", IDEHost: "mac", Path: "/code/proj"}
	if _, err := store.ApplyEvent(id, "agent_request_sent", "", ""); err != nil {
		t.Fatal(err)
	}
	now := time.Now()

	rec, ok := store.AgentWorkingAt("/code/proj", time.Minute, time.Hour, now)
	if !ok {
		t.Fatal("a window mid-turn should be reported")
	}
	if rec.IDE != "cursor" {
		t.Errorf("IDE = %q, want cursor", rec.IDE)
	}
	// The path is part of the answer, not just the query: a different worktree
	// in the same project is a different directory and must not match.
	if _, ok := store.AgentWorkingAt("/code/other", time.Minute, time.Hour, now); ok {
		t.Error("another path should not match")
	}
	// Trailing separators and Windows separators are the same path (see NormPath).
	if _, ok := store.AgentWorkingAt(`\code\proj/`, time.Minute, time.Hour, now); !ok {
		t.Error("a differently spelled path should match")
	}
	// A window silent past the liveness TTL is gone, whatever it was last doing.
	if _, ok := store.AgentWorkingAt("/code/proj", time.Minute, time.Hour, now.Add(2*time.Minute)); ok {
		t.Error("a window that stopped heartbeating should not hold the worktree")
	}
	// A turn older than any real turn means the stop event was lost. The window
	// is still heartbeating, so only maxTurnAge can free the directory.
	if _, ok := store.AgentWorkingAt("/code/proj", 0, time.Hour, now.Add(2*time.Hour)); ok {
		t.Error("a turn past maxTurnAge should be treated as a lost stop event")
	}
	if _, ok := store.AgentWorkingAt("/code/proj", 0, 0, now.Add(2*time.Hour)); !ok {
		t.Error("a non-positive maxTurnAge should disable the turn-age bound")
	}

	if _, err := store.ApplyEvent(id, "agent_response_received", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.AgentWorkingAt("/code/proj", time.Minute, time.Hour, now); ok {
		t.Error("a finished turn should release the worktree")
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
