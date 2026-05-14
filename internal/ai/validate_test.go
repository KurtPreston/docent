package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
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
	issues := Validate(context.Background(), userdata.AIConfig{Provider: "claude"}, nil)
	if len(issues) == 0 || !strings.Contains(issues[0].Message, "unknown") {
		t.Fatalf("expected unknown provider issue, got %#v", issues)
	}
}
