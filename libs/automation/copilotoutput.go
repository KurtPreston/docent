package automation

import (
	"bufio"
	"encoding/json"
	"strings"
)

// copilotEvent is the part of one line of copilot's `--output-format json`
// stream that finalCopilotMessage reads.
type copilotEvent struct {
	Type string `json:"type"`
	Data struct {
		Content          string `json:"content"`
		ParentToolCallID string `json:"parentToolCallId"`
	} `json:"data"`
}

// finalCopilotMessage returns the last non-empty top-level assistant message in
// copilot's JSONL output. Text mode prints every assistant message, including
// progress notes between tool calls, so the final answer has to be picked out
// of the event stream. Output with no assistant messages, such as plain text
// from an older CLI, is returned unchanged.
func finalCopilotMessage(stdout string) string {
	final, found := "", false
	sc := bufio.NewScanner(strings.NewReader(stdout))
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)

	// Keep the latest main-agent message, skipping subagents' and tool-only turns
	for sc.Scan() {
		var ev copilotEvent
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Type != "assistant.message" {
			continue
		}
		if ev.Data.ParentToolCallID != "" || strings.TrimSpace(ev.Data.Content) == "" {
			continue
		}
		final, found = ev.Data.Content, true
	}
	if !found {
		return stdout
	}
	return final
}
