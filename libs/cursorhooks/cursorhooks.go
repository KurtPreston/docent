// Package cursorhooks owns the Cursor agent-activity hook: the canonical
// script, its installation into ~/.cursor, and a freshness check.
//
// The hook exists because Cursor does not expose agent request/response events
// to the extension API, so agent status ("working" / "needs-followup") can only
// come from a hook. It must run on the machine where the *agent* executes,
// which for a Remote-SSH window is the remote box — the opposite host from the
// docent IDE extension, which is a UI extension and always runs client-side.
// The two reporters are reconciled by the session registry, so a drifted or
// missing hook silently costs all agent status while window lifecycle keeps
// working, which is exactly the failure this package exists to make visible.
package cursorhooks

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// script is the canonical hook, baked into the binary so an installed docentd
// can both install and verify it without needing the source checkout.
//
//go:embed docent-notify.sh
var script []byte

// ScriptName is the hook's on-disk filename, also the marker used to find
// docent's own entries when merging hooks.json.
const ScriptName = "docent-notify.sh"

// events maps each Cursor hook event docent listens to onto the session event
// the script reports. Anything else is retired wiring and gets stripped on
// install.
var events = []struct{ CursorEvent, SessionEvent string }{
	{"beforeSubmitPrompt", "agent_request_sent"},
	{"stop", "agent_response_received"},
}

// retiredEvents are events an older hook wired itself into, before the IDE
// extension took over window lifecycle and heartbeats. Install strips docent's
// entries from these so a re-install cleans up after the old layout.
var retiredEvents = []string{"sessionStart", "sessionEnd", "afterShellExecution"}

// Script returns the canonical hook script.
func Script() []byte {
	return append([]byte(nil), script...)
}

// Dir is the Cursor configuration root (~/.cursor). CURSOR_CONFIG_DIR
// overrides it, which is also how the tests stay off the real home dir.
func Dir() string {
	if v := os.Getenv("CURSOR_CONFIG_DIR"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".cursor"
	}
	return filepath.Join(home, ".cursor")
}

// ScriptPath is where the hook script belongs.
func ScriptPath() string {
	return filepath.Join(Dir(), "hooks", ScriptName)
}

// ConfigPath is Cursor's hook registration file.
func ConfigPath() string {
	return filepath.Join(Dir(), "hooks.json")
}

// TokenPath is the mode-600 file the hook reads its docentd bearer token from.
func TokenPath() string {
	return filepath.Join(Dir(), "docent-token")
}

// ScriptState describes how the installed script compares to the canonical one.
type ScriptState string

const (
	// ScriptCurrent means the installed script is byte-identical to canonical.
	ScriptCurrent ScriptState = "current"
	// ScriptStale means a script is installed but its contents differ. This is
	// the dangerous state: the hook still runs, still exits 0, and still
	// reports nothing usable.
	ScriptStale ScriptState = "stale"
	// ScriptMissing means no script is installed.
	ScriptMissing ScriptState = "missing"
)

// Status is the result of Check: what is installed, and how it is wired.
type Status struct {
	ScriptPath  string
	ConfigPath  string
	Script      ScriptState
	Executable  bool
	WiredEvents []string // Cursor events correctly pointing at the script.
	MissingWire []string // Cursor events docent needs but that are not wired.
	StaleWire   []string // Cursor events carrying retired docent entries.
}

// OK reports whether the hook is fully installed and wired.
func (s Status) OK() bool {
	return s.Script == ScriptCurrent && s.Executable &&
		len(s.MissingWire) == 0 && len(s.StaleWire) == 0
}

// Summary renders the status as human-readable lines for `docentd doctor`.
func (s Status) Summary() []string {
	var out []string
	switch s.Script {
	case ScriptCurrent:
		out = append(out, fmt.Sprintf("script up to date (%s)", s.ScriptPath))
	case ScriptStale:
		out = append(out, fmt.Sprintf("script DIFFERS from the version built into this binary (%s)", s.ScriptPath))
	case ScriptMissing:
		out = append(out, fmt.Sprintf("script NOT INSTALLED (%s)", s.ScriptPath))
	}
	if s.Script != ScriptMissing && !s.Executable {
		out = append(out, "script is not executable")
	}
	if len(s.MissingWire) > 0 {
		out = append(out, fmt.Sprintf("not wired into %s in %s", strings.Join(s.MissingWire, ", "), s.ConfigPath))
	}
	if len(s.StaleWire) > 0 {
		out = append(out, fmt.Sprintf("retired wiring still present on %s", strings.Join(s.StaleWire, ", ")))
	}
	return out
}

// Check inspects the installed hook without changing anything.
func Check() Status {
	st := Status{ScriptPath: ScriptPath(), ConfigPath: ConfigPath(), Script: ScriptMissing}

	if info, err := os.Stat(st.ScriptPath); err == nil && info.Mode().IsRegular() {
		st.Executable = info.Mode().Perm()&0o111 != 0
		if got, err := os.ReadFile(st.ScriptPath); err == nil {
			if bytes.Equal(got, script) {
				st.Script = ScriptCurrent
			} else {
				st.Script = ScriptStale
			}
		}
	}

	cfg, _ := loadConfig(st.ConfigPath)
	hooks := cfg.hooks()
	for _, ev := range events {
		if wiredFor(hooks[ev.CursorEvent], ev.SessionEvent) {
			st.WiredEvents = append(st.WiredEvents, ev.CursorEvent)
		} else {
			st.MissingWire = append(st.MissingWire, ev.CursorEvent)
		}
	}
	for _, ev := range retiredEvents {
		if hasDocentEntry(hooks[ev]) {
			st.StaleWire = append(st.StaleWire, ev)
		}
	}
	return st
}

// Install writes the canonical script and merges docent's entries into
// hooks.json, preserving any non-docent hooks the user has configured and
// dropping docent's retired wiring. It is idempotent.
func Install() (Status, error) {
	path := ScriptPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Status{}, err
	}
	if err := os.WriteFile(path, script, 0o755); err != nil {
		return Status{}, err
	}
	// WriteFile honors the mode only when creating, so an existing file that
	// lost its +x keeps it. Chmod unconditionally.
	if err := os.Chmod(path, 0o755); err != nil {
		return Status{}, err
	}
	if err := mergeConfig(ConfigPath(), path); err != nil {
		return Status{}, err
	}
	return Check(), nil
}

// config models hooks.json across both layouts docent has seen in the wild:
// the current {"version":1,"hooks":{...}} object and an older flat map of
// event name to entries.
type config struct {
	raw  map[string]json.RawMessage
	flat bool
}

func loadConfig(path string) (config, error) {
	c := config{raw: map[string]json.RawMessage{}}
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		return c, err
	}
	if inner, ok := top["hooks"]; ok {
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(inner, &nested); err == nil {
			c.raw = nested
			return c, nil
		}
	}
	c.raw, c.flat = top, true
	return c, nil
}

func (c config) hooks() map[string][]entry {
	out := map[string][]entry{}
	for k, v := range c.raw {
		var entries []entry
		if err := json.Unmarshal(v, &entries); err != nil {
			continue
		}
		out[k] = entries
	}
	return out
}

// entry is one hook registration. Only command is load-bearing here; the rest
// is preserved verbatim so merging never drops fields docent does not model
// (matcher, failClosed, loop_limit, ...).
type entry struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
	rest    map[string]json.RawMessage
}

func (e *entry) UnmarshalJSON(b []byte) error {
	type shape struct {
		Command string `json:"command"`
		Timeout int    `json:"timeout,omitempty"`
	}
	var s shape
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	e.Command, e.Timeout = s.Command, s.Timeout
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		return err
	}
	delete(all, "command")
	delete(all, "timeout")
	e.rest = all
	return nil
}

func (e entry) MarshalJSON() ([]byte, error) {
	out := map[string]json.RawMessage{}
	for k, v := range e.rest {
		out[k] = v
	}
	cmd, err := json.Marshal(e.Command)
	if err != nil {
		return nil, err
	}
	out["command"] = cmd
	if e.Timeout > 0 {
		out["timeout"] = json.RawMessage(fmt.Sprintf("%d", e.Timeout))
	}
	return json.Marshal(out)
}

func isDocentEntry(e entry) bool {
	return strings.Contains(e.Command, ScriptName)
}

func hasDocentEntry(entries []entry) bool {
	for _, e := range entries {
		if isDocentEntry(e) {
			return true
		}
	}
	return false
}

// wiredFor reports whether some docent entry passes the given session event as
// its argument. A docent entry with the wrong argument (the legacy
// "prompt-submit" / "agent-stop" aliases, say) counts as not wired, so doctor
// flags it and install rewrites it.
func wiredFor(entries []entry, sessionEvent string) bool {
	for _, e := range entries {
		if !isDocentEntry(e) {
			continue
		}
		fields := strings.Fields(e.Command)
		if len(fields) > 0 && fields[len(fields)-1] == sessionEvent {
			return true
		}
	}
	return false
}

func mergeConfig(path, scriptPath string) error {
	cfg, err := loadConfig(path)
	// A missing file is the first-install case; malformed JSON is not, and
	// rewriting it would silently discard the user's other hooks.
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w (fix or remove it, then re-run)", path, err)
	}
	hooks := cfg.hooks()
	emptied := map[string]bool{}

	// Replace docent's entries rather than appending, so repeated installs and
	// argument renames converge instead of stacking duplicates.
	for _, ev := range events {
		kept := make([]entry, 0, len(hooks[ev.CursorEvent])+1)
		for _, e := range hooks[ev.CursorEvent] {
			if !isDocentEntry(e) {
				kept = append(kept, e)
			}
		}
		cmd := scriptPath + " " + ev.SessionEvent
		hooks[ev.CursorEvent] = append(kept, entry{Command: cmd, Timeout: 5})
	}
	for _, ev := range retiredEvents {
		entries, ok := hooks[ev]
		if !ok {
			continue
		}
		kept := make([]entry, 0, len(entries))
		for _, e := range entries {
			if !isDocentEntry(e) {
				kept = append(kept, e)
			}
		}
		hooks[ev] = kept
		emptied[ev] = len(kept) == 0
	}

	// Start from the raw file so keys docent does not model (version, hook
	// entries whose shape we could not parse) survive untouched.
	inner := map[string]json.RawMessage{}
	for k, v := range cfg.raw {
		inner[k] = v
	}
	for k, entries := range hooks {
		if emptied[k] {
			delete(inner, k)
			continue
		}
		b, err := json.Marshal(entries)
		if err != nil {
			return err
		}
		inner[k] = b
	}

	var out any
	if cfg.flat {
		out = inner
	} else {
		out = map[string]any{"version": 1, "hooks": inner}
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
