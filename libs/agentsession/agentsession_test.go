package agentsession

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// collect runs a parser over a fixture stream and returns the normalized events
// plus the terminal result, mirroring what runStreaming does.
func collect(t *testing.T, parse lineParser, lines []string) ([]Event, TurnResult, bool) {
	t.Helper()
	var evs []Event
	var res TurnResult
	var got bool
	for _, line := range lines {
		e, r, ok := parse(line)
		evs = append(evs, e...)
		if ok {
			res, got = r, true
		}
	}
	return evs, res, got
}

func textOf(evs []Event, kind EventKind) string {
	var b strings.Builder
	for _, e := range evs {
		if e.Kind == kind {
			b.WriteString(e.Text)
		}
	}
	return b.String()
}

func kinds(evs []Event) []EventKind {
	out := make([]EventKind, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Kind)
	}
	return out
}

// claudeStream is the shape captured from claude 2.1.212 running a real turn
// (`claude -p --output-format stream-json --include-partial-messages --verbose`),
// trimmed to one of each envelope docent reads.
var claudeStream = []string{
	`{"type":"system","subtype":"init","cwd":"/tmp/wt","session_id":"e0fde9e4-6def-4a46-9762-d9103ed2e80d","tools":["Read","Write"]}`,
	`{"type":"system","subtype":"status","session_id":"e0fde9e4-6def-4a46-9762-d9103ed2e80d"}`,
	`{"type":"stream_event","event":{"type":"message_start"},"session_id":"e0fde9e4-6def-4a46-9762-d9103ed2e80d"}`,
	`{"type":"stream_event","event":{"type":"content_block_start","index":0},"session_id":"e0fde9e4-6def-4a46-9762-d9103ed2e80d"}`,
	`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"I'll create"}},"session_id":"e0fde9e4-6def-4a46-9762-d9103ed2e80d"}`,
	`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" the file."}},"session_id":"e0fde9e4-6def-4a46-9762-d9103ed2e80d"}`,
	`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"I'll create the file."}]},"session_id":"e0fde9e4-6def-4a46-9762-d9103ed2e80d"}`,
	`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Write","id":"toolu_1","input":{"file_path":"hello.txt"}}]},"session_id":"e0fde9e4-6def-4a46-9762-d9103ed2e80d"}`,
	`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_1","type":"tool_result","content":"File created successfully at: /tmp/wt/hello.txt"}]},"session_id":"e0fde9e4-6def-4a46-9762-d9103ed2e80d"}`,
	`{"type":"stream_event","event":{"type":"message_stop"},"session_id":"e0fde9e4-6def-4a46-9762-d9103ed2e80d"}`,
	`{"type":"result","subtype":"success","is_error":false,"duration_ms":4878,"num_turns":2,"result":"DONE","session_id":"e0fde9e4-6def-4a46-9762-d9103ed2e80d","total_cost_usd":0.219645,"usage":{"input_tokens":4,"output_tokens":138}}`,
}

func TestClaudeStreamNormalizes(t *testing.T) {
	evs, res, got := collect(t, parseClaudeLine, claudeStream)
	if !got {
		t.Fatal("no terminal result parsed")
	}
	if want := []EventKind{
		KindStarted, KindText, KindText, KindTool, KindToolResult, KindDone,
	}; !sameKinds(kinds(evs), want) {
		t.Fatalf("kinds = %v, want %v", kinds(evs), want)
	}
	// The prose must arrive exactly once. Claude reports each block twice (as
	// deltas, then whole on an assistant line), and emitting both would duplicate
	// every sentence in the transcript.
	if got := textOf(evs, KindText); got != "I'll create the file." {
		t.Errorf("text = %q, want the deltas exactly once", got)
	}
	if res.Text != "DONE" || res.IsError {
		t.Errorf("result = %+v", res)
	}
	if res.SessionID != "e0fde9e4-6def-4a46-9762-d9103ed2e80d" {
		t.Errorf("sessionID = %q", res.SessionID)
	}
	if res.DurationMS != 4878 || res.NumTurns != 2 || res.OutputTokens != 138 {
		t.Errorf("metrics lost: %+v", res)
	}
	if res.CostUSD == 0 {
		t.Error("cost lost")
	}
	for _, e := range evs {
		if e.Kind == KindToolResult && !strings.Contains(e.Text, "hello.txt") {
			t.Errorf("tool result text = %q", e.Text)
		}
		if e.Kind == KindTool && e.Tool != "Write" {
			t.Errorf("tool name = %q, want Write", e.Tool)
		}
	}
}

// cursorStream is the shape captured from cursor-agent 2026.08.04 running a real
// turn. Its envelopes overlap Claude's but are not the same: deltas arrive on
// assistant lines and thinking is a top-level type.
var cursorStream = []string{
	`{"type":"system","subtype":"init","session_id":"38058fda-aa45-4eac-a1be-18eccd2ce56a"}`,
	`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"prompt echo"}]},"session_id":"38058fda-aa45-4eac-a1be-18eccd2ce56a"}`,
	`{"type":"thinking","subtype":"delta","text":"The user requested the","session_id":"38058fda-aa45-4eac-a1be-18eccd2ce56a"}`,
	`{"type":"thinking","subtype":"delta","text":" single word.","session_id":"38058fda-aa45-4eac-a1be-18eccd2ce56a"}`,
	`{"type":"thinking","subtype":"completed","text":"The user requested the single word.","session_id":"38058fda-aa45-4eac-a1be-18eccd2ce56a"}`,
	`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"P"}]},"session_id":"38058fda-aa45-4eac-a1be-18eccd2ce56a"}`,
	`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"ONG"}]},"session_id":"38058fda-aa45-4eac-a1be-18eccd2ce56a"}`,
	`{"type":"result","subtype":"success","duration_ms":4180,"is_error":false,"result":"PONG","session_id":"38058fda-aa45-4eac-a1be-18eccd2ce56a","usage":{"inputTokens":7830,"outputTokens":48}}`,
}

func TestCursorStreamNormalizes(t *testing.T) {
	evs, res, got := collect(t, parseCursorLine, cursorStream)
	if !got {
		t.Fatal("no terminal result parsed")
	}
	if got := textOf(evs, KindText); got != "PONG" {
		t.Errorf("text = %q, want PONG", got)
	}
	// The completed line repeats the whole thought; counting it would double it.
	if got := textOf(evs, KindThinking); got != "The user requested the single word." {
		t.Errorf("thinking = %q, want the deltas exactly once", got)
	}
	if res.Text != "PONG" || res.IsError || res.DurationMS != 4180 {
		t.Errorf("result = %+v", res)
	}
	if res.InputTokens != 7830 || res.OutputTokens != 48 {
		t.Errorf("cursor reports usage in camelCase keys; got %+v", res)
	}
}

// A failed turn is a normal outcome to record, not an operational fault, so it
// surfaces as a result with IsError rather than as a parse failure.
func TestErrorResultIsTerminalNotFatal(t *testing.T) {
	for name, tc := range map[string]struct {
		parse lineParser
		line  string
	}{
		"claude": {parseClaudeLine, `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"tool limit reached","session_id":"s1"}`},
		"cursor": {parseCursorLine, `{"type":"result","subtype":"error","is_error":true,"result":"aborted","session_id":"s1"}`},
	} {
		t.Run(name, func(t *testing.T) {
			evs, res, got := collect(t, tc.parse, []string{tc.line})
			if !got {
				t.Fatal("error result must still be terminal")
			}
			if !res.IsError {
				t.Error("IsError not set")
			}
			if len(evs) != 1 || evs[0].Kind != KindError {
				t.Fatalf("events = %v, want a single KindError", kinds(evs))
			}
			if evs[0].Error == "" {
				t.Error("KindError with no message: the UI would show a blank failure")
			}
			if evs[0].Result == nil {
				t.Error("terminal event must carry the result")
			}
		})
	}
}

// A CLI that interleaves a non-JSON line (a warning, a progress bar) must cost
// that line and not the turn.
func TestGarbageLinesAreSkipped(t *testing.T) {
	lines := []string{
		"Warning: something happened",
		`{"type":"system","subtype":"init","session_id":"s1"}`,
		`{"not":"an envelope we know"}`,
		`{"type":"result","subtype":"success","result":"ok","session_id":"s1"}`,
	}
	for name, parse := range map[string]lineParser{"claude": parseClaudeLine, "cursor": parseCursorLine} {
		t.Run(name, func(t *testing.T) {
			evs, res, got := collect(t, parse, lines)
			if !got || res.Text != "ok" {
				t.Fatalf("result = %+v, got = %v", res, got)
			}
			if !sameKinds(kinds(evs), []EventKind{KindStarted, KindDone}) {
				t.Errorf("kinds = %v", kinds(evs))
			}
		})
	}
}

// A tool result can be a whole file. Truncating keeps one Read from dominating a
// transcript that is streamed to a browser.
func TestToolResultsAreTruncated(t *testing.T) {
	big := strings.Repeat("x", toolResultLimit*2)
	if got := summarizeToolResult(big); len(got) > toolResultLimit+4 {
		t.Fatalf("len = %d, want <= %d", len(got), toolResultLimit+4)
	}
	if !strings.HasSuffix(summarizeToolResult(big), "…") {
		t.Error("truncation is not marked, so a clipped result reads as complete")
	}
	// The block-array form must flatten rather than render as Go syntax.
	blocks := []any{
		map[string]any{"type": "text", "text": "line one"},
		map[string]any{"type": "text", "text": "line two"},
	}
	if got := summarizeToolResult(blocks); got != "line one\nline two" {
		t.Errorf("block array = %q", got)
	}
	if got := summarizeToolResult(nil); got != "" {
		t.Errorf("nil payload = %q, want empty", got)
	}
}

func TestClaudeMintsAValidUUID(t *testing.T) {
	id, err := Claude{}.NewSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	// Claude validates the format, so a hex job id would be rejected at runtime.
	if len(id) != 36 || strings.Count(id, "-") != 4 {
		t.Fatalf("id = %q, want a 36-char dashed UUID", id)
	}
	if id[14] != '4' {
		t.Errorf("id = %q, want version 4 in the third group", id)
	}
	if c := id[19]; c != '8' && c != '9' && c != 'a' && c != 'b' {
		t.Errorf("id = %q, want an RFC 4122 variant nibble", id)
	}
	other, _ := Claude{}.NewSession(context.Background(), "")
	if id == other {
		t.Error("two sessions got the same id")
	}
}

// A turn without a session id or a directory must be refused rather than run:
// the first would start a conversation nobody can resume, the second would let
// an agent edit whatever directory the daemon happens to be in.
func TestTurnRefusesMissingSessionOrDir(t *testing.T) {
	runners := map[string]Runner{"claude": Claude{}, "cursor": Cursor{}}
	for name, r := range runners {
		t.Run(name+"/no session", func(t *testing.T) {
			if _, err := r.Turn(context.Background(), TurnRequest{Dir: "/tmp"}, func(Event) {}); err != ErrNoSessionID {
				t.Fatalf("err = %v, want ErrNoSessionID", err)
			}
		})
		t.Run(name+"/no dir", func(t *testing.T) {
			if _, err := r.Turn(context.Background(), TurnRequest{SessionID: "s"}, func(Event) {}); err != ErrNoDir {
				t.Fatalf("err = %v, want ErrNoDir", err)
			}
		})
	}
}

// fakeCLI writes a shell script that echoes the given stdout lines, records its
// own argv and stdin, and exits with the given code. It lets the subprocess
// plumbing be tested without calling a real agent.
func fakeCLI(t *testing.T, stdoutLines []string, stderr string, exitCode int) (bin, argvFile, stdinFile string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake CLI is a shell script")
	}
	dir := t.TempDir()
	bin = filepath.Join(dir, "fake-agent")
	argvFile = filepath.Join(dir, "argv")
	stdinFile = filepath.Join(dir, "stdin")

	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("printf '%s\\n' \"$@\" > " + argvFile + "\n")
	b.WriteString("cat > " + stdinFile + "\n")
	for _, l := range stdoutLines {
		// Single quotes are safe: the fixtures are JSON without any.
		b.WriteString("echo '" + l + "'\n")
	}
	if stderr != "" {
		b.WriteString("echo '" + stderr + "' >&2\n")
	}
	b.WriteString("exit " + itoa(exitCode) + "\n")

	if err := os.WriteFile(bin, []byte(b.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argvFile, stdinFile
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var out []byte
	for n > 0 {
		out = append([]byte{byte('0' + n%10)}, out...)
		n /= 10
	}
	return string(out)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// The prompt must travel on stdin, never as a positional argument: Claude's
// --allowedTools is variadic, so a trailing prompt is swallowed as a tool name
// and the turn dies with "Input must be provided...". This was a real failure.
func TestPromptGoesOnStdinNotArgv(t *testing.T) {
	bin, argvFile, stdinFile := fakeCLI(t, claudeStream, "", 0)
	r := Claude{Command: bin}
	prompt := "Fix the failing test in a.ts"

	res, err := r.Turn(context.Background(), TurnRequest{
		SessionID: "e0fde9e4-6def-4a46-9762-d9103ed2e80d",
		Prompt:    prompt, Dir: t.TempDir(), First: true,
		AllowedTools: []string{"Read", "Write"},
	}, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "DONE" {
		t.Errorf("result = %+v", res)
	}
	if got := readFile(t, stdinFile); strings.TrimSpace(got) != prompt {
		t.Errorf("stdin = %q, want the prompt", got)
	}
	argv := readFile(t, argvFile)
	if strings.Contains(argv, prompt) {
		t.Errorf("the prompt reached argv, where --allowedTools would eat it:\n%s", argv)
	}
	// --allowedTools must be last, for the same reason.
	lines := strings.Fields(strings.TrimSpace(argv))
	at := indexOf(lines, "--allowedTools")
	if at < 0 {
		t.Fatalf("--allowedTools missing from argv:\n%s", argv)
	}
	for _, after := range lines[at+1:] {
		if strings.HasPrefix(after, "--") {
			t.Errorf("flag %q follows the variadic --allowedTools and would be eaten as a tool name:\n%s", after, argv)
		}
	}
}

// --session-id opens a session and --resume continues one. Passing both, or
// --session-id for an existing session, is an error, so the opening turn has to
// be distinguished rather than guessed.
func TestClaudeUsesSessionIDThenResume(t *testing.T) {
	for _, tc := range []struct {
		name       string
		first      bool
		want, deny string
	}{
		{"opening turn", true, "--session-id", "--resume"},
		{"later turn", false, "--resume", "--session-id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin, argvFile, _ := fakeCLI(t, claudeStream, "", 0)
			_, err := Claude{Command: bin}.Turn(context.Background(), TurnRequest{
				SessionID: "sess-1", Prompt: "hi", Dir: t.TempDir(), First: tc.first,
			}, func(Event) {})
			if err != nil {
				t.Fatal(err)
			}
			argv := readFile(t, argvFile)
			if !strings.Contains(argv, tc.want) {
				t.Errorf("argv missing %s:\n%s", tc.want, argv)
			}
			if strings.Contains(argv, tc.deny) {
				t.Errorf("argv must not contain %s:\n%s", tc.deny, argv)
			}
		})
	}
}

// stream-json refuses to run without --verbose, so its absence would break every
// turn at runtime.
func TestClaudeAlwaysPassesVerboseWithStreamJSON(t *testing.T) {
	bin, argvFile, _ := fakeCLI(t, claudeStream, "", 0)
	_, err := Claude{Command: bin}.Turn(context.Background(), TurnRequest{
		SessionID: "s", Prompt: "hi", Dir: t.TempDir(), First: true,
	}, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	argv := readFile(t, argvFile)
	for _, want := range []string{"--verbose", "stream-json", "--include-partial-messages", "--permission-mode", "acceptEdits"} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv missing %q:\n%s", want, argv)
		}
	}
}

// Cursor resumes on every turn, including the first: the id came from
// create-chat, so there is no separate "open a session" invocation.
func TestCursorAlwaysResumes(t *testing.T) {
	for _, first := range []bool{true, false} {
		bin, argvFile, _ := fakeCLI(t, cursorStream, "", 0)
		_, err := Cursor{Command: bin}.Turn(context.Background(), TurnRequest{
			SessionID: "chat-1", Prompt: "hi", Dir: t.TempDir(), First: first,
		}, func(Event) {})
		if err != nil {
			t.Fatal(err)
		}
		argv := readFile(t, argvFile)
		if !strings.Contains(argv, "--resume") || !strings.Contains(argv, "chat-1") {
			t.Errorf("First=%v: argv missing --resume chat-1:\n%s", first, argv)
		}
		if !strings.Contains(argv, "--force") {
			t.Errorf("First=%v: without --force a headless cursor turn stalls at the first approval:\n%s", first, argv)
		}
	}
}

func TestCursorNewSessionParsesTheID(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-cursor")
	script := "#!/bin/sh\necho '  cf349c65-0fb2-439f-9e4d-a3002e7d0118  '\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	id, err := Cursor{Command: bin}.NewSession(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if id != "cf349c65-0fb2-439f-9e4d-a3002e7d0118" {
		t.Fatalf("id = %q, want the trimmed id", id)
	}
}

func TestCursorNewSessionRejectsEmptyOutput(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-cursor")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := (Cursor{Command: bin}).NewSession(context.Background(), dir); err == nil {
		t.Fatal("an empty create-chat reply must be an error, not an empty session id")
	}
}

// A result the agent already reported is the truth about the turn even if the
// process then exits non-zero; reporting the exit code instead would discard the
// agent's own account of what happened.
func TestResultWinsOverNonZeroExit(t *testing.T) {
	bin, _, _ := fakeCLI(t, claudeStream, "some warning", 1)
	res, err := Claude{Command: bin}.Turn(context.Background(), TurnRequest{
		SessionID: "s", Prompt: "hi", Dir: t.TempDir(), First: true,
	}, func(Event) {})
	if err != nil {
		t.Fatalf("err = %v, want the reported result to win", err)
	}
	if res.Text != "DONE" {
		t.Errorf("result = %+v", res)
	}
}

// A non-zero exit with no result at all is a real failure, and stderr is the only
// place it explains itself, so it has to reach the caller.
func TestFailureWithoutResultSurfacesStderr(t *testing.T) {
	bin, _, _ := fakeCLI(t, nil, "Error: not logged in", 1)
	_, err := Claude{Command: bin}.Turn(context.Background(), TurnRequest{
		SessionID: "s", Prompt: "hi", Dir: t.TempDir(), First: true,
	}, func(Event) {})
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("err = %v, want it to carry stderr", err)
	}
}

// Exit 0 with no result means the CLI's output contract changed, which deserves
// its own message rather than looking like an empty but successful turn.
func TestCleanExitWithoutResultIsAnError(t *testing.T) {
	bin, _, _ := fakeCLI(t, []string{`{"type":"system","subtype":"init","session_id":"s"}`}, "", 0)
	_, err := Claude{Command: bin}.Turn(context.Background(), TurnRequest{
		SessionID: "s", Prompt: "hi", Dir: t.TempDir(), First: true,
	}, func(Event) {})
	if err == nil || !strings.Contains(err.Error(), "no result event") {
		t.Fatalf("err = %v, want a no-result-event error", err)
	}
}

// Events must arrive during the turn, not in a batch at the end: a cockpit lane
// showing a live transcript is the entire point of streaming.
func TestEventsAreEmittedWhileTheTurnRuns(t *testing.T) {
	bin, _, _ := fakeCLI(t, claudeStream, "", 0)
	var order []EventKind
	_, err := Claude{Command: bin}.Turn(context.Background(), TurnRequest{
		SessionID: "e0fde9e4-6def-4a46-9762-d9103ed2e80d", Prompt: "hi",
		Dir: t.TempDir(), First: true,
	}, func(e Event) { order = append(order, e.Kind) })
	if err != nil {
		t.Fatal(err)
	}
	if len(order) == 0 {
		t.Fatal("no events emitted")
	}
	if order[0] != KindStarted {
		t.Errorf("first event = %q, want %q", order[0], KindStarted)
	}
	if order[len(order)-1] != KindDone {
		t.Errorf("last event = %q, want %q", order[len(order)-1], KindDone)
	}
	for _, e := range order {
		if e == "" {
			t.Fatal("an event was emitted with no kind")
		}
	}
}

// Every emitted event needs a timestamp, since the transcript is ordered by it.
func TestEventsCarryTimestamps(t *testing.T) {
	bin, _, _ := fakeCLI(t, claudeStream, "", 0)
	before := time.Now().Add(-time.Second)
	var evs []Event
	if _, err := (Claude{Command: bin}).Turn(context.Background(), TurnRequest{
		SessionID: "s", Prompt: "hi", Dir: t.TempDir(), First: true,
	}, func(e Event) { evs = append(evs, e) }); err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if e.At.Before(before) {
			t.Fatalf("event %q has timestamp %v, want a real one", e.Kind, e.At)
		}
	}
}

// The turn runs in the requested worktree. Getting this wrong means an agent
// editing the wrong repo, which is the one failure here with real consequences.
func TestTurnRunsInTheRequestedDir(t *testing.T) {
	dir := t.TempDir()
	scriptDir := t.TempDir()
	bin := filepath.Join(scriptDir, "fake-agent")
	out := filepath.Join(scriptDir, "cwd")
	script := "#!/bin/sh\npwd > " + out + "\ncat > /dev/null\n" +
		`echo '{"type":"result","subtype":"success","result":"ok","session_id":"s"}'` + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := (Claude{Command: bin}).Turn(context.Background(), TurnRequest{
		SessionID: "s", Prompt: "hi", Dir: dir, First: true,
	}, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	// TempDir can sit under a symlinked /tmp, so compare resolved paths.
	want, _ := filepath.EvalSymlinks(dir)
	got, _ := filepath.EvalSymlinks(strings.TrimSpace(readFile(t, out)))
	if got != want {
		t.Fatalf("cwd = %q, want %q", got, want)
	}
}

// A turn that outruns its timeout must be killed rather than held forever, and
// the caller must learn it was the deadline.
func TestTurnTimeoutIsEnforced(t *testing.T) {
	scriptDir := t.TempDir()
	bin := filepath.Join(scriptDir, "hang")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ncat > /dev/null\nsleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := (Claude{Command: bin}).Turn(context.Background(), TurnRequest{
		SessionID: "s", Prompt: "hi", Dir: scriptDir, First: true,
		Timeout: 300 * time.Millisecond,
	}, func(Event) {})
	if err == nil {
		t.Fatal("want a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("took %v: the process was not killed", elapsed)
	}
}

func TestProviderNames(t *testing.T) {
	if got := (Claude{}).Provider(); got != ProviderClaude {
		t.Errorf("claude provider = %q", got)
	}
	if got := (Cursor{}).Provider(); got != ProviderCursor {
		t.Errorf("cursor provider = %q", got)
	}
}

func sameKinds(a, b []EventKind) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func indexOf(hay []string, needle string) int {
	for i, v := range hay {
		if v == needle {
			return i
		}
	}
	return -1
}
