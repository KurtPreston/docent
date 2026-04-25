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
		return OllamaProvider{BaseURL: base, Model: model}
	case "cursor":
		cmd := strings.TrimSpace(cfg.Cursor.Command)
		if cmd == "" {
			cmd = "cursor-agent"
		}
		return CursorCLIProvider{Command: cmd, Args: cfg.Cursor.Args}
	case "", "rule-based":
		return RuleBasedProvider{}
	default:
		return fallback
	}
}
