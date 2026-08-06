// Package agentsession runs coding-agent turns as subprocesses and normalizes
// their streaming output into one event vocabulary, so docent can host agent
// sessions itself instead of only launching an IDE and hoping.
//
// # Why per-turn subprocesses
//
// Neither Claude Code nor cursor-agent offers a long-lived server mode. Both do
// offer a headless print mode that resumes a prior conversation by id, so a
// "session" here is a durable id plus a series of short-lived processes, one per
// turn. That is a better fit than a persistent child anyway: a turn that hangs
// can be killed without losing the conversation, and docentd restarting does not
// end a session.
//
// # Caller-minted ids
//
// The session id is chosen before the first process starts. That is what lets a
// docent record, an SSE stream, and the CLI's own on-disk transcript share a key
// from the very first event, instead of docent having to scrape an id out of a
// process it already started and then reconcile.
//
// # Verified behavior these runners are built around
//
// Checked against claude 2.1.212 and cursor-agent 2026.08.04, because the docs
// for both are thin and several of these are silent failures rather than errors:
//
//   - The prompt goes on stdin, never as a positional argument. Claude's
//     --allowedTools is variadic (<tools...>), so a trailing positional prompt is
//     swallowed as a tool name and the process dies with "Input must be provided
//     either through stdin or as a prompt argument". Stdin also sidesteps argv
//     limits on the long prompts a seeded follow-up produces.
//   - Claude takes --session-id <uuid> on the first turn and --resume <uuid>
//     afterwards; passing both, or --session-id twice, is an error.
//   - cursor-agent cannot be told an id. `cursor-agent create-chat` returns one,
//     and every turn including the first uses --resume.
//   - cursor-agent silently ignores unknown flags, so a typo there costs
//     behavior with no diagnostic. Its `ls` and `resume` subcommands need a PTY
//     and crash without one, so neither is used.
//   - Both stream newline-delimited JSON whose envelopes differ; normalizing is
//     the point of this package.
package agentsession

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Provider names a runner implementation.
type Provider string

const (
	// ProviderClaude is Claude Code, the default: it supports caller-minted
	// session ids and a per-invocation tool allowlist, so it is the one safe for
	// policy-sensitive work.
	ProviderClaude Provider = "claude"
	// ProviderCursor is cursor-agent. Useful for model variety, but it has no
	// per-invocation tool allowlist and no programmatic approval round-trip, so
	// it runs all-or-nothing.
	ProviderCursor Provider = "cursor"
)

// EventKind is the normalized vocabulary both CLIs are translated into.
type EventKind string

const (
	// KindPrompt is the message that opened a turn. It comes from docent rather
	// than from the CLI, but belongs in the transcript: without it the record
	// shows an agent acting for no visible reason.
	KindPrompt EventKind = "prompt"
	// KindStarted is the session announcing itself, carrying the id the CLI
	// actually used. Worth surfacing: it is the confirmation that a caller-minted
	// id was honored rather than silently replaced.
	KindStarted EventKind = "started"
	// KindText is assistant prose, streamed in whatever granularity the CLI
	// flushes.
	KindText EventKind = "text"
	// KindThinking is reasoning output, kept separate so a UI can collapse it.
	KindThinking EventKind = "thinking"
	// KindTool is the agent invoking a tool.
	KindTool EventKind = "tool"
	// KindToolResult is what the tool returned.
	KindToolResult EventKind = "tool-result"
	// KindDone is the terminal success event, carrying the turn's result.
	KindDone EventKind = "done"
	// KindError is the terminal failure event.
	KindError EventKind = "error"
	// KindStopped marks a turn cancelled on request. Distinct from KindError
	// because the conversation survives inside the CLI: this is a pause, and the
	// next turn resumes from it.
	KindStopped EventKind = "stopped"
	// KindStatus reports the session's new status after a turn settles. It is
	// what lets a subscriber update a lane without re-fetching the record.
	KindStatus EventKind = "status"
)

// Event is one normalized thing that happened during a turn.
type Event struct {
	Kind      EventKind `json:"kind"`
	Text      string    `json:"text,omitempty"`
	Tool      string    `json:"tool,omitempty"`
	SessionID string    `json:"sessionId,omitempty"`
	Error     string    `json:"error,omitempty"`
	// Result is set only on KindDone.
	Result *TurnResult `json:"result,omitempty"`
	At     time.Time   `json:"at"`
}

// TurnResult is the outcome of one turn.
type TurnResult struct {
	// Text is the agent's final answer.
	Text    string `json:"text,omitempty"`
	IsError bool   `json:"isError,omitempty"`
	// SessionID is the id the CLI reported, which is the one to resume with.
	SessionID    string  `json:"sessionId,omitempty"`
	DurationMS   int     `json:"durationMs,omitempty"`
	CostUSD      float64 `json:"costUsd,omitempty"`
	InputTokens  int     `json:"inputTokens,omitempty"`
	OutputTokens int     `json:"outputTokens,omitempty"`
	NumTurns     int     `json:"numTurns,omitempty"`
}

// TurnRequest describes one turn to run.
type TurnRequest struct {
	// SessionID is required. For Claude it is caller-minted; for Cursor it comes
	// from NewSession.
	SessionID string
	// Prompt is the user message. It is written to the child's stdin.
	Prompt string
	// Dir is the working directory, i.e. the worktree the agent may edit. It is
	// required: an agent run in an unexpected directory is the one failure mode
	// with real consequences, so this is never defaulted to the daemon's cwd.
	Dir string
	// First marks the opening turn of a session. Claude needs the distinction
	// (--session-id vs --resume); Cursor does not and ignores it.
	First bool
	// AllowedTools restricts the agent, Claude only. Empty means the CLI's own
	// default.
	AllowedTools []string
	// Model overrides the CLI default.
	Model string
	// Timeout bounds the turn. Zero means DefaultTurnTimeout.
	Timeout time.Duration
}

// DefaultTurnTimeout bounds a single turn. It is generous because a real agent
// turn on a large repo legitimately takes minutes, and a turn killed halfway
// leaves a half-edited worktree, which is worse than waiting.
const DefaultTurnTimeout = 30 * time.Minute

// Runner runs turns for one provider.
type Runner interface {
	// Provider names the implementation.
	Provider() Provider
	// NewSession returns an id for a fresh session. For Claude this is a locally
	// minted UUID and costs nothing; for Cursor it is a CLI round trip.
	NewSession(ctx context.Context, dir string) (string, error)
	// Turn runs one turn, calling emit for each normalized event as it arrives.
	// emit is called from the reading goroutine and must not block for long.
	//
	// The returned error is non-nil only when the turn could not be run or
	// completed. An agent that ran and reported failure returns a TurnResult with
	// IsError set and a nil error, since that is a normal outcome to record
	// rather than an operational fault.
	Turn(ctx context.Context, req TurnRequest, emit func(Event)) (TurnResult, error)
}

// ErrNoSessionID is returned when a turn is requested without a session id,
// which would otherwise start an untracked conversation the caller could never
// resume or attribute.
var ErrNoSessionID = errors.New("agentsession: turn requires a session id")

// ErrNoDir is returned when a turn is requested without a working directory.
var ErrNoDir = errors.New("agentsession: turn requires a working directory")

func validate(req TurnRequest) error {
	if strings.TrimSpace(req.SessionID) == "" {
		return ErrNoSessionID
	}
	if strings.TrimSpace(req.Dir) == "" {
		return ErrNoDir
	}
	return nil
}

// runStreaming spawns cmd with prompt on stdin, decodes newline-delimited JSON
// from stdout through parse, and emits normalized events.
//
// stderr is captured rather than streamed: both CLIs use it for progress noise,
// but it is the only place a startup failure explains itself, so it is kept to
// attach to the error.
func runStreaming(ctx context.Context, cmd *exec.Cmd, prompt string, parse lineParser, emit func(Event)) (TurnResult, error) {
	cmd.Stdin = strings.NewReader(prompt)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return TurnResult{}, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	configureProcGroup(cmd)

	if err := cmd.Start(); err != nil {
		return TurnResult{}, fmt.Errorf("start %s: %w", cmd.Path, err)
	}

	var (
		result TurnResult
		got    bool
	)
	// A scanner with a large buffer: a single tool result (a whole file's
	// contents, say) routinely exceeds bufio's 64KiB default, and a torn line
	// would desynchronize the rest of the stream.
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), maxStreamLine)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		evs, res, ok := parse(line)
		for _, ev := range evs {
			if ev.At.IsZero() {
				ev.At = time.Now().UTC()
			}
			emit(ev)
		}
		if ok {
			result, got = res, true
		}
	}
	scanErr := sc.Err()
	waitErr := cmd.Wait()

	// Order matters: a result the agent already reported is the truth about the
	// turn even if the process then exited non-zero, and reporting the exit code
	// instead would throw away the agent's own account of what happened.
	if got {
		return result, nil
	}
	switch {
	case scanErr != nil && errors.Is(scanErr, bufio.ErrTooLong):
		return TurnResult{}, fmt.Errorf("%s emitted a JSON line over %d bytes", cmd.Path, maxStreamLine)
	case scanErr != nil:
		return TurnResult{}, fmt.Errorf("reading %s output: %w", cmd.Path, scanErr)
	case ctx.Err() != nil:
		return TurnResult{}, ctx.Err()
	case waitErr != nil:
		return TurnResult{}, fmt.Errorf("%s: %w%s", cmd.Path, waitErr, stderrSuffix(stderr.String()))
	default:
		// Exit 0 with no result line means the CLI changed its output contract,
		// which is worth naming precisely rather than reporting as an empty turn.
		return TurnResult{}, fmt.Errorf("%s produced no result event%s", cmd.Path, stderrSuffix(stderr.String()))
	}
}

// maxStreamLine caps one JSON line. Generous, because tool results embed file
// contents; bounded, because an unbounded read on a runaway process is how a
// daemon dies.
const maxStreamLine = 8 << 20

func stderrSuffix(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if len(s) > 2000 {
		s = s[:2000] + "…"
	}
	return "\n" + s
}

// lineParser turns one JSON line into normalized events, and reports the turn
// result when the line is the terminal one.
type lineParser func(line string) ([]Event, TurnResult, bool)

// decode is a small helper so parsers can ignore malformed lines. A CLI that
// interleaves a non-JSON line (a warning, a progress bar) should cost that line,
// not the turn.
func decode(line string, into any) bool {
	return json.Unmarshal([]byte(line), into) == nil
}
