package agentsession

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Cursor runs turns through cursor-agent. It exists for model variety (gpt-5,
// sonnet-thinking, and whatever else the account exposes), not for
// policy-sensitive work: cursor-agent has no per-invocation tool allowlist and no
// programmatic approval round-trip, so a turn is all-or-nothing under --force.
//
// It also ignores unknown flags silently, which means a mistake here costs
// behavior with no diagnostic. Every flag this file passes was checked against
// `cursor-agent --help` for that reason.
type Cursor struct {
	// Command is the binary, defaulting to "cursor-agent".
	Command string
	// Env, when non-nil, replaces the child's environment.
	Env []string
}

func (c Cursor) Provider() Provider { return ProviderCursor }

func (c Cursor) bin() string {
	if strings.TrimSpace(c.Command) != "" {
		return c.Command
	}
	return "cursor-agent"
}

// newSessionTimeout bounds the id round trip. Short on purpose: it is one API
// call, and a create-chat that hangs should fail fast rather than occupy a slot
// for half an hour.
const newSessionTimeout = 60 * time.Second

// NewSession asks cursor-agent for a chat id, since unlike Claude it will not
// accept one. This costs a process and an API round trip, which is why the id is
// minted once per session rather than per turn.
func (c Cursor) NewSession(ctx context.Context, dir string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, newSessionTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, c.bin(), "create-chat")
	if strings.TrimSpace(dir) != "" {
		cmd.Dir = dir
	}
	if c.Env != nil {
		cmd.Env = c.Env
	}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s create-chat: %w", c.bin(), err)
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return "", fmt.Errorf("%s create-chat returned no id", c.bin())
	}
	// The reply is a bare id on one line. Guarding against a future build that
	// adds a banner costs nothing and prevents passing a whole paragraph as an id.
	if i := strings.IndexAny(id, " \t\n"); i >= 0 {
		id = id[:i]
	}
	return id, nil
}

func (c Cursor) Turn(ctx context.Context, req TurnRequest, emit func(Event)) (TurnResult, error) {
	if err := validate(req); err != nil {
		return TurnResult{}, err
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = DefaultTurnTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// --resume on every turn including the first: the id came from create-chat, so
	// there is no "new session" invocation to distinguish. req.First is therefore
	// meaningless here, unlike for Claude.
	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--stream-partial-output",
		"--resume", req.SessionID,
		// There is no narrower option. Without --force the agent stops at the
		// first command needing approval and cannot ask, which in headless mode
		// means it silently accomplishes nothing.
		"--force",
	}
	if m := strings.TrimSpace(req.Model); m != "" {
		args = append(args, "--model", m)
	}

	cmd := exec.CommandContext(cctx, c.bin(), args...)
	cmd.Dir = req.Dir
	if c.Env != nil {
		cmd.Env = c.Env
	}
	return runStreaming(cctx, cmd, req.Prompt, parseCursorLine, emit)
}

// cursorLine is cursor-agent's stream-json envelope. It overlaps Claude's but is
// not the same: text deltas arrive directly on type=assistant rather than wrapped
// in a stream_event, and thinking is its own top-level type.
type cursorLine struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
	Message   struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
			Name string `json:"name"`
			// Cursor reports a tool result under either key depending on build,
			// so both are read and the first non-empty one wins.
			Content any `json:"content"`
			Result  any `json:"result"`
		} `json:"content"`
	} `json:"message"`
	IsError    bool   `json:"is_error"`
	Result     string `json:"result"`
	DurationMS int    `json:"duration_ms"`
	Usage      struct {
		InputTokens  int `json:"inputTokens"`
		OutputTokens int `json:"outputTokens"`
	} `json:"usage"`
}

func parseCursorLine(line string) ([]Event, TurnResult, bool) {
	var l cursorLine
	if !decode(line, &l) {
		return nil, TurnResult{}, false
	}
	switch l.Type {
	case "system":
		if l.Subtype == "init" {
			return []Event{{Kind: KindStarted, SessionID: l.SessionID}}, TurnResult{}, false
		}
		return nil, TurnResult{}, false

	case "thinking":
		// Only the deltas: the "completed" line repeats the whole block, which
		// would double every thought.
		if l.Subtype == "delta" && l.Text != "" {
			return []Event{{Kind: KindThinking, Text: l.Text, SessionID: l.SessionID}}, TurnResult{}, false
		}
		return nil, TurnResult{}, false

	case "assistant":
		// With --stream-partial-output each assistant line carries one delta, so
		// text blocks are emitted directly rather than deduplicated against a
		// separate stream (the shape Claude needs).
		var out []Event
		for _, b := range l.Message.Content {
			switch {
			case b.Type == "text" && b.Text != "":
				out = append(out, Event{Kind: KindText, Text: b.Text, SessionID: l.SessionID})
			case b.Type == "tool_use" && b.Name != "":
				out = append(out, Event{Kind: KindTool, Tool: b.Name, SessionID: l.SessionID})
			}
		}
		return out, TurnResult{}, false

	case "user":
		var out []Event
		for _, b := range l.Message.Content {
			if b.Type != "tool_result" {
				continue
			}
			payload := b.Content
			if payload == nil {
				payload = b.Result
			}
			out = append(out, Event{Kind: KindToolResult, Text: summarizeToolResult(payload), SessionID: l.SessionID})
		}
		return out, TurnResult{}, false

	case "result":
		res := TurnResult{
			Text:         l.Result,
			IsError:      l.IsError,
			SessionID:    l.SessionID,
			DurationMS:   l.DurationMS,
			InputTokens:  l.Usage.InputTokens,
			OutputTokens: l.Usage.OutputTokens,
		}
		ev := Event{Kind: KindDone, Text: l.Result, SessionID: l.SessionID, Result: &res, At: time.Now().UTC()}
		if l.IsError {
			ev.Kind = KindError
			ev.Error = firstNonEmpty(l.Result, l.Subtype, "agent reported failure")
		}
		return []Event{ev}, res, true
	}
	return nil, TurnResult{}, false
}
