package cursorhooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The embedded script must stay byte-identical to the one in hooks/, which is
// the copy the docs and installers point at. Two copies of a script whose whole
// job is reporting drift is the exact hazard this test closes.
func TestEmbeddedScriptMatchesRepoCopy(t *testing.T) {
	repo, err := os.ReadFile(filepath.Join("..", "..", "hooks", ScriptName))
	if err != nil {
		t.Fatalf("read repo copy: %v", err)
	}
	if string(repo) != string(script) {
		t.Fatalf("hooks/%s and libs/cursorhooks/%s differ; copy one over the other", ScriptName, ScriptName)
	}
}

// The script must not regress to detecting remote sessions solely via
// CURSOR_CODE_REMOTE, which cursor-server does not export.
func TestScriptDetectsRemoteBeyondCursorCodeRemote(t *testing.T) {
	for _, want := range []string{"CURSOR_CODE_REMOTE", "CURSOR_REMOTE_SSH_HOST", "VSCODE_AGENT_FOLDER"} {
		if !strings.Contains(string(script), want) {
			t.Errorf("script no longer consults %s", want)
		}
	}
	if !strings.Contains(string(script), "/api/sessions/events") {
		t.Error("script must post to /api/sessions/events")
	}
}

func TestInstallFromScratch(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CURSOR_CONFIG_DIR", dir)

	st, err := Install()
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !st.OK() {
		t.Fatalf("status not OK after install: %+v (%v)", st, st.Summary())
	}
	if st.Script != ScriptCurrent || !st.Executable {
		t.Errorf("script state %q executable=%v", st.Script, st.Executable)
	}

	hooks := readHooks(t, ConfigPath())
	for _, ev := range events {
		entries := hooks[ev.CursorEvent]
		if len(entries) != 1 {
			t.Fatalf("%s: got %d entries, want 1", ev.CursorEvent, len(entries))
		}
		if !strings.HasSuffix(entries[0].Command, ev.SessionEvent) {
			t.Errorf("%s: command %q does not pass %q", ev.CursorEvent, entries[0].Command, ev.SessionEvent)
		}
	}
}

func TestInstallIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CURSOR_CONFIG_DIR", dir)

	if _, err := Install(); err != nil {
		t.Fatalf("first install: %v", err)
	}
	first, err := os.ReadFile(ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Install(); err != nil {
		t.Fatalf("second install: %v", err)
	}
	second, err := os.ReadFile(ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("re-install changed hooks.json:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

// Install must replace the legacy wiring (retired events, and docent entries
// carrying the old prompt-submit/agent-stop argument aliases) while preserving
// hooks the user added themselves. This reproduces the layout found installed
// on the Ubuntu workstation.
func TestInstallReplacesLegacyWiringAndKeepsOthers(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CURSOR_CONFIG_DIR", dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{
  "version": 1,
  "hooks": {
    "beforeSubmitPrompt": [
      {"command": "./hooks/turn-timer-start.sh", "timeout": 5},
      {"command": "./hooks/docent-notify.sh prompt-submit", "timeout": 5}
    ],
    "stop": [
      {"command": "./hooks/slack-notify-on-long-task.sh", "timeout": 10},
      {"command": "./hooks/docent-notify.sh agent-stop", "timeout": 5}
    ],
    "sessionStart": [{"command": "./hooks/docent-notify.sh session-start", "timeout": 5}],
    "afterShellExecution": [
      {"command": "./hooks/docent-notify.sh shell-done", "timeout": 5},
      {"command": "./hooks/mine.sh", "timeout": 5}
    ]
  }
}`
	if err := os.WriteFile(ConfigPath(), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := Install()
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !st.OK() {
		t.Fatalf("status not OK: %+v (%v)", st, st.Summary())
	}

	hooks := readHooks(t, ConfigPath())

	if got := commands(hooks["beforeSubmitPrompt"]); len(got) != 2 ||
		got[0] != "./hooks/turn-timer-start.sh" ||
		!strings.HasSuffix(got[1], "agent_request_sent") {
		t.Errorf("beforeSubmitPrompt = %q", got)
	}
	if got := commands(hooks["stop"]); len(got) != 2 ||
		got[0] != "./hooks/slack-notify-on-long-task.sh" ||
		!strings.HasSuffix(got[1], "agent_response_received") {
		t.Errorf("stop = %q", got)
	}
	// Retired event whose only entry was docent's: dropped entirely.
	if entries, ok := hooks["sessionStart"]; ok {
		t.Errorf("sessionStart should be removed, got %q", commands(entries))
	}
	// Retired event with a user entry: docent's stripped, the user's kept.
	if got := commands(hooks["afterShellExecution"]); len(got) != 1 || got[0] != "./hooks/mine.sh" {
		t.Errorf("afterShellExecution = %q", got)
	}
}

// A stale script must be reported as stale rather than silently accepted: this
// is the state that cost all agent status on the workstation.
func TestCheckDetectsStaleScript(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CURSOR_CONFIG_DIR", dir)
	if _, err := Install(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ScriptPath(), []byte("#!/bin/sh\ncurl http://127.0.0.1:39787/event\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	st := Check()
	if st.Script != ScriptStale {
		t.Errorf("script state = %q, want %q", st.Script, ScriptStale)
	}
	if st.OK() {
		t.Error("status should not be OK with a stale script")
	}
	if len(st.Summary()) == 0 {
		t.Error("summary should explain the problem")
	}
}

func TestCheckMissing(t *testing.T) {
	t.Setenv("CURSOR_CONFIG_DIR", t.TempDir())
	st := Check()
	if st.Script != ScriptMissing {
		t.Errorf("script state = %q, want %q", st.Script, ScriptMissing)
	}
	if len(st.MissingWire) != len(events) {
		t.Errorf("MissingWire = %v, want all %d events", st.MissingWire, len(events))
	}
	if st.OK() {
		t.Error("status should not be OK with nothing installed")
	}
}

// Malformed hooks.json must abort rather than be overwritten; the user's other
// hooks are not ours to discard.
func TestInstallRefusesMalformedConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CURSOR_CONFIG_DIR", dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ConfigPath(), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(); err == nil {
		t.Fatal("expected an error for malformed hooks.json")
	}
	b, err := os.ReadFile(ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "{not json" {
		t.Errorf("malformed config was modified: %s", b)
	}
}

func TestFlatConfigLayoutPreserved(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CURSOR_CONFIG_DIR", dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ConfigPath(), []byte(`{"stop":[{"command":"./x.sh"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(); err != nil {
		t.Fatalf("install: %v", err)
	}
	var top map[string]json.RawMessage
	b, err := os.ReadFile(ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &top); err != nil {
		t.Fatal(err)
	}
	if _, nested := top["hooks"]; nested {
		t.Errorf("flat layout was converted to nested: %s", b)
	}
	if !Check().OK() {
		t.Errorf("flat layout not recognized as wired: %v", Check().Summary())
	}
}

func readHooks(t *testing.T, path string) map[string][]entry {
	t.Helper()
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("load %s: %v", path, err)
	}
	return cfg.hooks()
}

func commands(entries []entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Command)
	}
	return out
}
