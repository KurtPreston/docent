package agentsession

import (
	"context"
	"crypto/rand"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Claude runs turns through Claude Code. It is the default provider: it accepts
// a caller-minted session id and a per-invocation tool allowlist, which together
// are what make a turn attributable and bounded.
type Claude struct {
	// Command is the binary, defaulting to "claude".
	Command string
	// Env, when non-nil, replaces the child's environment.
	Env []string
	// PermissionMode is Claude's permission mode, defaulting to acceptEdits.
	//
	// acceptEdits is the only workable setting for a headless turn in this build:
	// there is no --permission-prompt-tool, so a turn that needs approval cannot
	// ask and would simply stall. "needs a decision" is therefore a reason to
	// promote the session to a Cursor window, not something to solve with a
	// stricter mode here.
	PermissionMode string
}

func (c Claude) Provider() Provider { return ProviderClaude }

func (c Claude) bin() string {
	if strings.TrimSpace(c.Command) != "" {
		return c.Command
	}
	return "claude"
}

// NewSession mints a UUID locally. Claude accepts it verbatim via --session-id,
// so no process needs to run: the id exists before anything is spawned, which is
// what lets the docent record and the CLI's own transcript at
// ~/.claude/projects/<dir-slug>/<id>.jsonl share a key from the outset.
func (c Claude) NewSession(context.Context, string) (string, error) {
	return newUUID()
}

// newUUID returns a random (version 4) UUID. Claude validates the format, so
// this is a real UUID rather than docent's usual hex job id — and a dependency
// for sixteen bytes of randomness is not worth it.
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("agentsession: generating a session id: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func (c Claude) Turn(ctx context.Context, req TurnRequest, emit func(Event)) (TurnResult, error) {
	if err := validate(req); err != nil {
		return TurnResult{}, err
	}
	// req.Mode is ignored: Claude's plan permission mode has no headless approval
	// round-trip, so a turn that needs a decision would stall for the same reason
	// acceptEdits is the only workable PermissionMode here.
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = DefaultTurnTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	mode := strings.TrimSpace(c.PermissionMode)
	if mode == "" {
		mode = "acceptEdits"
	}
	args := []string{
		"-p",
		"--output-format", "stream-json",
		// stream-json refuses to run without --verbose, which is a hard
		// requirement rather than a preference.
		"--verbose",
		"--include-partial-messages",
		"--permission-mode", mode,
	}
	// --session-id on the opening turn, --resume after. Passing both is an error,
	// and passing --session-id twice for the same id is too, so the distinction
	// has to be tracked by the caller rather than guessed here.
	if req.First {
		args = append(args, "--session-id", req.SessionID)
	} else {
		args = append(args, "--resume", req.SessionID)
	}
	if m := strings.TrimSpace(req.Model); m != "" {
		args = append(args, "--model", m)
	}
	args = append(args, addDirArgs(AttachmentDirs(req.Attachments))...)
	// --allowedTools is variadic, so it goes last among the flags and the prompt
	// travels on stdin. Any flag after it would be eaten as a tool name.
	if len(req.AllowedTools) > 0 {
		args = append(args, "--allowedTools")
		args = append(args, req.AllowedTools...)
	}

	cmd := exec.CommandContext(cctx, c.bin(), args...)
	cmd.Dir = req.Dir
	if c.Env != nil {
		cmd.Env = c.Env
	}
	return runStreaming(cctx, cmd, req.Prompt, parseClaudeLine, emit)
}

// claudeLine is the union of the stream-json envelopes docent reads. Claude emits
// several shapes on one stream; fields absent from a given shape decode as zero.
type claudeLine struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	// Message carries assistant blocks and tool results.
	Message struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
			Name string `json:"name"`
			// Content is a tool_result payload, which is a string in the common
			// case and an array of blocks otherwise, so it stays raw.
			Content any `json:"content"`
		} `json:"content"`
	} `json:"message"`
	// Event is the raw Anthropic streaming envelope, present only on
	// type=stream_event.
	Event struct {
		Type  string `json:"type"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	} `json:"event"`
	// Terminal fields, present only on type=result.
	IsError    bool    `json:"is_error"`
	Result     string  `json:"result"`
	DurationMS int     `json:"duration_ms"`
	NumTurns   int     `json:"num_turns"`
	TotalCost  float64 `json:"total_cost_usd"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func parseClaudeLine(line string) ([]Event, TurnResult, bool) {
	var l claudeLine
	if !decode(line, &l) {
		return nil, TurnResult{}, false
	}
	switch l.Type {
	case "system":
		if l.Subtype == "init" {
			return []Event{{Kind: KindStarted, SessionID: l.SessionID}}, TurnResult{}, false
		}
		// system/status and friends are progress chatter with nothing a user can
		// act on; dropping them keeps the transcript readable.
		return nil, TurnResult{}, false

	case "stream_event":
		// Partial text as it is generated. The complete block arrives again on a
		// later type=assistant line, so only the deltas are surfaced and the
		// assistant text blocks are dropped, or every sentence would appear twice.
		if l.Event.Type == "content_block_delta" &&
			(l.Event.Delta.Type == "text_delta" || l.Event.Delta.Type == "") &&
			l.Event.Delta.Text != "" {
			return []Event{{Kind: KindText, Text: l.Event.Delta.Text, SessionID: l.SessionID}}, TurnResult{}, false
		}
		if l.Event.Type == "content_block_delta" && l.Event.Delta.Type == "thinking_delta" && l.Event.Delta.Text != "" {
			return []Event{{Kind: KindThinking, Text: l.Event.Delta.Text, SessionID: l.SessionID}}, TurnResult{}, false
		}
		return nil, TurnResult{}, false

	case "assistant":
		// Tool calls only: the text already streamed as deltas above.
		var out []Event
		for _, b := range l.Message.Content {
			if b.Type == "tool_use" && b.Name != "" {
				out = append(out, Event{Kind: KindTool, Tool: b.Name, SessionID: l.SessionID})
			}
		}
		return out, TurnResult{}, false

	case "user":
		var out []Event
		for _, b := range l.Message.Content {
			if b.Type == "tool_result" {
				out = append(out, Event{
					Kind: KindToolResult,
					Text: summarizeToolResult(b.Content),
					// The tool's name is not repeated on the result line; the
					// preceding KindTool event carries it.
					SessionID: l.SessionID,
				})
			}
		}
		return out, TurnResult{}, false

	case "result":
		res := TurnResult{
			Text:         l.Result,
			IsError:      l.IsError,
			SessionID:    l.SessionID,
			DurationMS:   l.DurationMS,
			CostUSD:      l.TotalCost,
			InputTokens:  l.Usage.InputTokens,
			OutputTokens: l.Usage.OutputTokens,
			NumTurns:     l.NumTurns,
		}
		kind := KindDone
		ev := Event{Kind: kind, Text: l.Result, SessionID: l.SessionID, Result: &res, At: time.Now().UTC()}
		if l.IsError {
			ev.Kind = KindError
			ev.Error = firstNonEmpty(l.Result, l.Subtype, "agent reported failure")
		}
		return []Event{ev}, res, true
	}
	return nil, TurnResult{}, false
}

// summarizeToolResult renders a tool_result payload as a short string. The
// payload is a plain string in the common case and an array of content blocks
// otherwise, and it can be a whole file, so it is truncated: the transcript is a
// record of what happened, not a place to re-read the repo.
func summarizeToolResult(v any) string {
	var s string
	switch t := v.(type) {
	case string:
		s = t
	case []any:
		var parts []string
		for _, b := range t {
			m, ok := b.(map[string]any)
			if !ok {
				continue
			}
			if txt, ok := m["text"].(string); ok {
				parts = append(parts, txt)
			}
		}
		s = strings.Join(parts, "\n")
	default:
		return ""
	}
	return truncate(strings.TrimSpace(s), toolResultLimit)
}

const toolResultLimit = 2000

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n]) + "…"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
