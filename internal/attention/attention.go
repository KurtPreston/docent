package attention

import (
	"strings"

	"github.com/kurt/slakkr-ai/internal/collectors"
)

// Classify sets AttentionClass on each status item (urgent, waiting_on_me, blocked, stale, informational, deferrable, delegable).
func Classify(items []collectors.StatusItem) {
	for i := range items {
		items[i].AttentionClass = classifyOne(items[i])
	}
}

func classifyOne(s collectors.StatusItem) string {
	if s.Kind == "collector_error" || s.Severity == "error" {
		return "urgent"
	}
	if s.Kind == "not_configured" {
		return "informational"
	}
	if s.Kind == "manual_prompt" {
		return "informational"
	}
	if strings.Contains(strings.ToLower(s.Summary), "blocked") {
		return "blocked"
	}
	switch s.ChangeState {
	case "new":
		if s.Severity == "warning" {
			return "urgent"
		}
		return "waiting_on_me"
	case "updated":
		return "waiting_on_me"
	case "unchanged":
		if s.Severity == "warning" {
			return "deferrable"
		}
		return "informational"
	default:
		return "informational"
	}
}
