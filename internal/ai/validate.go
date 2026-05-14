package ai

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/kurt/slakkr-ai/internal/userdata"
)

// Issue describes a problem with the configured AI provider that would prevent
// (or degrade) generation. The CLI converts these into the unified
// collectors.ValidationIssue shape for rendering alongside directive issues.
type Issue struct {
	Provider    string // "cursor", "ollama", "rule-based"
	Field       string // pointer at the offending config field
	Message     string
	Remediation string
}

// Validate inspects the AI provider configuration: it confirms the right
// binary is on PATH for shell-based providers, and pings the configured base
// URL for network-based providers. Returns an empty slice when nothing
// actionable is wrong.
func Validate(ctx context.Context, cfg userdata.AIConfig, httpClient *http.Client) []Issue {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	provider := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(cfg.Provider), "_", "-"))
	switch provider {
	case "cursor":
		cmd := strings.TrimSpace(cfg.Cursor.Command)
		if cmd == "" {
			cmd = "cursor-agent"
		}
		if _, err := exec.LookPath(cmd); err != nil {
			return []Issue{{
				Provider:    "cursor",
				Field:       "ai.cursor.command",
				Message:     fmt.Sprintf("%q not found on PATH", cmd),
				Remediation: "install cursor-agent (https://docs.cursor.com/cli) or set ai.cursor.command to a binary on PATH",
			}}
		}
		return nil
	case "ollama":
		base := strings.TrimRight(strings.TrimSpace(cfg.Ollama.BaseURL), "/")
		if base == "" {
			base = "http://127.0.0.1:11434"
		}
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, base+"/api/tags", nil)
		if err != nil {
			return []Issue{{
				Provider:    "ollama",
				Field:       "ai.ollama.base_url",
				Message:     fmt.Sprintf("could not build probe request: %v", err),
				Remediation: "fix ai.ollama.base_url",
			}}
		}
		res, err := httpClient.Do(req)
		if err != nil {
			return []Issue{{
				Provider:    "ollama",
				Field:       "ai.ollama.base_url",
				Message:     fmt.Sprintf("Ollama probe failed: %v", err),
				Remediation: fmt.Sprintf("start the Ollama server and verify with `curl %s/api/tags`", base),
			}}
		}
		defer res.Body.Close()
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			return []Issue{{
				Provider:    "ollama",
				Field:       "ai.ollama.base_url",
				Message:     fmt.Sprintf("Ollama returned %s for /api/tags", res.Status),
				Remediation: "check ai.ollama.base_url and that the model is pulled",
			}}
		}
		return nil
	case "", "rule-based":
		return nil
	default:
		return []Issue{{
			Provider:    provider,
			Field:       "ai.provider",
			Message:     fmt.Sprintf("unknown AI provider %q", provider),
			Remediation: "set ai.provider to one of: cursor, ollama, rule-based",
		}}
	}
}
