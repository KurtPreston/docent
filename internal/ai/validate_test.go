package ai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kurt/slakkr-ai/internal/userdata"
)

func TestValidateRuleBasedHasNoDeps(t *testing.T) {
	issues := Validate(context.Background(), userdata.AIConfig{Provider: "rule-based"}, nil)
	if len(issues) != 0 {
		t.Fatalf("expected no issues for rule-based, got %#v", issues)
	}
	if issues := Validate(context.Background(), userdata.AIConfig{}, nil); len(issues) != 0 {
		t.Fatalf("expected no issues for empty provider, got %#v", issues)
	}
}

func TestValidateCursorMissingBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // empty PATH so cursor-agent definitely missing
	issues := Validate(context.Background(), userdata.AIConfig{Provider: "cursor"}, nil)
	if len(issues) == 0 || !strings.Contains(issues[0].Message, "cursor-agent") {
		t.Fatalf("expected cursor-agent missing issue, got %#v", issues)
	}
	if issues[0].Provider != "cursor" {
		t.Fatalf("expected provider 'cursor', got %q", issues[0].Provider)
	}
}

func TestValidateCursorCustomCommand(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	issues := Validate(context.Background(), userdata.AIConfig{
		Provider: "cursor",
		Cursor:   userdata.AIProviderCursor{Command: "totally-fake-binary-12345"},
	}, nil)
	if len(issues) == 0 || !strings.Contains(issues[0].Message, "totally-fake-binary-12345") {
		t.Fatalf("expected issue mentioning custom command, got %#v", issues)
	}
}

// stubCursorOnPath drops an empty (but executable) `cursor-agent` file in a
// temp dir and points PATH at it so exec.LookPath succeeds. The auth probe
// itself is mocked separately via cursorStatusRunner, so this stub never
// actually runs.
func stubCursorOnPath(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	stub := filepath.Join(dir, "cursor-agent")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("PATH", dir)
}

func mockCursorStatus(t *testing.T, out []byte, err error) {
	t.Helper()
	orig := cursorStatusRunner
	t.Cleanup(func() { cursorStatusRunner = orig })
	cursorStatusRunner = func(ctx context.Context, cmd string) ([]byte, error) {
		return out, err
	}
}

func TestValidateCursorLoggedIn(t *testing.T) {
	stubCursorOnPath(t)
	mockCursorStatus(t, []byte(`{"status":"authenticated","isAuthenticated":true,"message":"Logged in as kurt"}`), nil)

	issues := Validate(context.Background(), userdata.AIConfig{Provider: "cursor"}, nil)
	if len(issues) != 0 {
		t.Fatalf("expected no issues when logged in, got %#v", issues)
	}
}

func TestValidateCursorNotLoggedIn(t *testing.T) {
	stubCursorOnPath(t)
	mockCursorStatus(t, []byte(`{"status":"unauthenticated","isAuthenticated":false,"message":"Not logged in"}`), nil)

	issues := Validate(context.Background(), userdata.AIConfig{Provider: "cursor"}, nil)
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %#v", issues)
	}
	if issues[0].Provider != "cursor" {
		t.Fatalf("expected provider 'cursor', got %q", issues[0].Provider)
	}
	if !strings.Contains(strings.ToLower(issues[0].Message), "not logged in") {
		t.Fatalf("expected message to mention 'not logged in', got %q", issues[0].Message)
	}
	if !strings.Contains(issues[0].Remediation, "login") {
		t.Fatalf("expected remediation to mention login, got %q", issues[0].Remediation)
	}
}

func TestValidateCursorStatusProbeFails(t *testing.T) {
	stubCursorOnPath(t)
	mockCursorStatus(t, nil, errors.New("boom"))

	issues := Validate(context.Background(), userdata.AIConfig{Provider: "cursor"}, nil)
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %#v", issues)
	}
	if !strings.Contains(issues[0].Message, "could not verify") {
		t.Fatalf("expected 'could not verify' message, got %q", issues[0].Message)
	}
}

func TestValidateCursorStatusUnparseable(t *testing.T) {
	stubCursorOnPath(t)
	mockCursorStatus(t, []byte("not json at all"), nil)

	issues := Validate(context.Background(), userdata.AIConfig{Provider: "cursor"}, nil)
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %#v", issues)
	}
	if !strings.Contains(issues[0].Message, "could not parse") {
		t.Fatalf("expected 'could not parse' message, got %q", issues[0].Message)
	}
}

func TestValidateOllamaProbeOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer srv.Close()
	cfg := userdata.AIConfig{
		Provider: "ollama",
		Ollama:   userdata.AIProviderOllama{BaseURL: srv.URL, Model: "llama3"},
	}
	if issues := Validate(context.Background(), cfg, srv.Client()); len(issues) != 0 {
		t.Fatalf("expected no issues for reachable Ollama, got %#v", issues)
	}
}

func TestValidateOllamaUnreachable(t *testing.T) {
	// 127.0.0.1:1 is reliably refused on most systems.
	cfg := userdata.AIConfig{
		Provider: "ollama",
		Ollama:   userdata.AIProviderOllama{BaseURL: "http://127.0.0.1:1"},
	}
	issues := Validate(context.Background(), cfg, nil)
	if len(issues) == 0 {
		t.Fatalf("expected Ollama probe failure, got none")
	}
	if issues[0].Provider != "ollama" {
		t.Fatalf("expected provider 'ollama', got %q", issues[0].Provider)
	}
}

func TestValidateUnknownProvider(t *testing.T) {
	issues := Validate(context.Background(), userdata.AIConfig{Provider: "gemini"}, nil)
	if len(issues) == 0 || !strings.Contains(issues[0].Message, "unknown") {
		t.Fatalf("expected unknown provider issue, got %#v", issues)
	}
}

func TestValidateClaudeMissingBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // empty PATH so claude definitely missing
	issues := Validate(context.Background(), userdata.AIConfig{Provider: "claude"}, nil)
	if len(issues) == 0 || !strings.Contains(issues[0].Message, "claude") {
		t.Fatalf("expected claude missing issue, got %#v", issues)
	}
	if issues[0].Provider != "claude" {
		t.Fatalf("expected provider 'claude', got %q", issues[0].Provider)
	}
}

func TestValidateClaudeCustomCommand(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	issues := Validate(context.Background(), userdata.AIConfig{
		Provider: "claude",
		Claude:   userdata.AIProviderClaude{Command: "totally-fake-claude-12345"},
	}, nil)
	if len(issues) == 0 || !strings.Contains(issues[0].Message, "totally-fake-claude-12345") {
		t.Fatalf("expected issue mentioning custom command, got %#v", issues)
	}
}

func TestValidateClaudeOnPath(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "claude")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("PATH", dir)
	issues := Validate(context.Background(), userdata.AIConfig{Provider: "claude"}, nil)
	if len(issues) != 0 {
		t.Fatalf("expected no issues when claude is on PATH, got %#v", issues)
	}
}
