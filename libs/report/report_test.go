package report

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/KurtPreston/docent/libs/ai"
	"github.com/KurtPreston/docent/libs/config/executionmode"
	"github.com/KurtPreston/docent/libs/config/userdata"
)

// recProvider records the RunInput it was handed so tests can assert the
// pipeline wired the resolved run + collected statuses through correctly.
type recProvider struct {
	in     ai.RunInput
	output string
}

func (p *recProvider) RunMode(_ context.Context, in ai.RunInput) (string, error) {
	p.in = in
	return p.output, nil
}

// forcesFallback selects the AI fallback provider: an unknown provider name
// makes SelectProvider fall through to opts.Provider (our recorder) instead
// of the built-in rule-based renderer.
func forcesFallback() userdata.AIConfig { return userdata.AIConfig{Provider: "x-test-fallback"} }

func TestGenerateWiresResolvedRunThroughProvider(t *testing.T) {
	now := time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC)
	rec := &recProvider{output: "# Title\n\nbody   "}
	cfg := userdata.ConfigFile{AI: forcesFallback()} // no directives

	res, err := Generate(context.Background(), cfg, Options{
		ModeID:   executionmode.BuiltinRecentActivity,
		Days:     14,
		Scope:    executionmode.ScopeAll,
		Now:      now,
		Provider: rec,
		LogsDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// Markdown is trimmed and newline-terminated.
	if res.Markdown != "# Title\n\nbody\n" {
		t.Fatalf("markdown: %q", res.Markdown)
	}
	if res.Statuses != 0 {
		t.Fatalf("expected 0 statuses with no directives, got %d", res.Statuses)
	}
	// Scope override + days override flow into the resolved run.
	if res.Run.Scope != executionmode.ScopeAll {
		t.Fatalf("resolved scope: got %q want all", res.Run.Scope)
	}
	if res.Run.LookbackDays != 14 {
		t.Fatalf("resolved lookback: got %d want 14", res.Run.LookbackDays)
	}
	// The provider saw the resolved run.
	if rec.in.ModeID != executionmode.BuiltinRecentActivity {
		t.Fatalf("provider ModeID: %q", rec.in.ModeID)
	}
	if rec.in.LookbackDays != 14 {
		t.Fatalf("provider LookbackDays: %d", rec.in.LookbackDays)
	}
	if !rec.in.Now.Equal(now) {
		t.Fatalf("provider Now: got %v want %v", rec.in.Now, now)
	}
	if len(rec.in.Statuses) != 0 {
		t.Fatalf("provider Statuses: %d", len(rec.in.Statuses))
	}
	if rec.in.DebugDir == "" {
		t.Fatal("expected Generate to provision DebugDir for AI logging")
	}
}

func TestGenerateRuleBasedDeterministic(t *testing.T) {
	now := time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC)
	cfg := userdata.ConfigFile{} // empty AI => built-in rule-based provider

	res, err := Generate(context.Background(), cfg, Options{
		ModeID:  executionmode.BuiltinRecentActivity,
		Days:    7,
		Now:     now,
		LogsDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if strings.TrimSpace(res.Markdown) == "" {
		t.Fatal("expected non-empty rule-based markdown")
	}
	if !strings.HasSuffix(res.Markdown, "\n") {
		t.Fatalf("markdown should be newline-terminated: %q", res.Markdown)
	}
	if res.Run.LookbackDays != 7 {
		t.Fatalf("lookback: %d", res.Run.LookbackDays)
	}
}

func TestGenerateUnknownModeErrors(t *testing.T) {
	_, err := Generate(context.Background(), userdata.ConfigFile{}, Options{
		ModeID:  "does-not-exist",
		Now:     time.Now(),
		LogsDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected error for unknown mode")
	}
	if !strings.Contains(err.Error(), "unknown mode") {
		t.Fatalf("error should mention unknown mode, got: %v", err)
	}
}
