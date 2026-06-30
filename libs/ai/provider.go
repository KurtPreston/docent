package ai

import (
	"strings"

	"github.com/kurt/slakkr-ai/internal/userdata"
)

// SelectProvider returns the AI implementation described by userdata config.
func SelectProvider(cfg userdata.AIConfig, fallback Provider) Provider {
	if fallback == nil {
		fallback = RuleBasedProvider{}
	}
	formatter := SelectActivityFormatter(cfg.ActivityFormatter)
	switch strings.ToLower(strings.ReplaceAll(strings.TrimSpace(cfg.Provider), "_", "-")) {
	case "ollama":
		base := strings.TrimRight(strings.TrimSpace(cfg.Ollama.BaseURL), "/")
		if base == "" {
			base = "http://127.0.0.1:11434"
		}
		model := strings.TrimSpace(cfg.Ollama.Model)
		if model == "" {
			model = "llama3"
		}
		return OllamaProvider{BaseURL: base, Model: model, Formatter: formatter}
	case "cursor":
		cmd := strings.TrimSpace(cfg.Cursor.Command)
		if cmd == "" {
			cmd = "cursor-agent"
		}
		return CursorCLIProvider{Command: cmd, Args: cfg.Cursor.Args, Formatter: formatter}
	case "claude":
		cmd := strings.TrimSpace(cfg.Claude.Command)
		if cmd == "" {
			cmd = "claude"
		}
		return ClaudeCLIProvider{Command: cmd, Args: cfg.Claude.Args, Formatter: formatter}
	case "", "rule-based":
		return RuleBasedProvider{Formatter: formatter}
	default:
		if rb, ok := fallback.(RuleBasedProvider); ok {
			rb.Formatter = formatter
			return rb
		}
		return fallback
	}
}
