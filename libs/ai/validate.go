package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/KurtPreston/docent/libs/config/userdata"
)

// Issue describes a problem with the configured AI provider that would prevent
// (or degrade) generation. The CLI converts these into the unified
// collectors.ValidationIssue shape for rendering alongside directive issues.
type Issue struct {
	Provider    string // "cursor", "claude", "ollama", "rule-based"
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
		if iss := validateCursorAuth(ctx, cmd); iss != nil {
			return []Issue{*iss}
		}
		return nil
	case "claude":
		cmd := strings.TrimSpace(cfg.Claude.Command)
		if cmd == "" {
			cmd = "claude"
		}
		if _, err := exec.LookPath(cmd); err != nil {
			return []Issue{{
				Provider:    "claude",
				Field:       "ai.claude.command",
				Message:     fmt.Sprintf("%q not found on PATH", cmd),
				Remediation: "install the Claude Code CLI (https://docs.claude.com/en/docs/claude-code) or set ai.claude.command to a binary on PATH",
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
			Remediation: "set ai.provider to one of: cursor, claude, ollama, rule-based",
		}}
	}
}

// cursorAuthStatus mirrors the JSON shape of `cursor-agent status --format json`.
// Only the fields docent cares about are decoded; extras are ignored.
type cursorAuthStatus struct {
	IsAuthenticated bool   `json:"isAuthenticated"`
	Status          string `json:"status"`
	Message         string `json:"message"`
}

// cursorStatusRunner runs the cursor-agent auth-status probe. It is exposed as
// a package-level variable so tests can stub the actual exec without needing a
// real cursor-agent binary on PATH.
var cursorStatusRunner = func(ctx context.Context, cmd string) ([]byte, error) {
	return exec.CommandContext(ctx, cmd, "status", "--format", "json").Output()
}

// validateCursorAuth verifies the user is logged in to cursor-agent. It runs
// `<cmd> status --format json` with a short timeout and inspects
// `isAuthenticated`. Returns nil when authenticated. When the probe itself
// fails (binary doesn't speak the status protocol, network issue, etc.) a
// softer "could not verify" issue is returned so users with custom wrappers
// aren't blocked outright.
func validateCursorAuth(ctx context.Context, cmd string) *Issue {
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	out, err := cursorStatusRunner(probeCtx, cmd)
	if err != nil {
		detail := err.Error()
		if exitErr, ok := err.(*exec.ExitError); ok {
			if stderr := strings.TrimSpace(string(exitErr.Stderr)); stderr != "" {
				detail = firstLine(stderr)
			}
		}
		return &Issue{
			Provider:    "cursor",
			Field:       "ai.cursor.command",
			Message:     fmt.Sprintf("could not verify %s auth: %s", cmd, detail),
			Remediation: fmt.Sprintf("run `%s status` manually to debug, or `%s login` if signed out", cmd, cmd),
		}
	}

	var status cursorAuthStatus
	if err := json.Unmarshal(out, &status); err != nil {
		return &Issue{
			Provider:    "cursor",
			Field:       "ai.cursor.command",
			Message:     fmt.Sprintf("could not parse %s status output: %v", cmd, err),
			Remediation: fmt.Sprintf("run `%s status --format json` manually to debug", cmd),
		}
	}
	if !status.IsAuthenticated {
		msg := strings.TrimSpace(status.Message)
		if msg == "" {
			msg = "not logged in"
		}
		return &Issue{
			Provider:    "cursor",
			Field:       "ai.cursor.command",
			Message:     fmt.Sprintf("%s reports %s", cmd, msg),
			Remediation: fmt.Sprintf("run `%s login` to authenticate", cmd),
		}
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
