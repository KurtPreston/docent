package automation_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KurtPreston/docent/libs/automation"
	"github.com/KurtPreston/docent/libs/correlation"
	"github.com/KurtPreston/docent/libs/model"
	"gopkg.in/yaml.v3"
)

func TestMatchSignals_basic(t *testing.T) {
	rules := []automation.Rule{{
		ID:      "slack-ticket",
		Enabled: true,
		Trigger: automation.Trigger{
			Type:   "signal",
			Source: "slack",
			Kind:   automation.KindSpec{"slack_mention"},
			Match:  automation.Match{TicketKey: true},
		},
		Actions: []automation.Action{{Type: "webhook", URL: "http://example"}},
	}}
	sigs := []model.Signal{{
		Source:   "slack",
		Kind:     "slack_mention",
		Title:    "Can you look at SALSA-42?",
		StableID: "s1",
		IsSelf:   true,
	}}
	evs := automation.MatchSignals(rules, sigs, automation.MatchOpts{
		CorrCfg: correlation.Config{Projects: []string{"SALSA"}},
	})
	if len(evs) != 1 {
		t.Fatalf("got %d events, want 1", len(evs))
	}
	if evs[0].TicketKey != "SALSA-42" {
		t.Fatalf("ticket=%q", evs[0].TicketKey)
	}
}

func TestMatchSignals_selfCondition(t *testing.T) {
	self := true
	rules := []automation.Rule{{
		ID:         "only-self",
		Enabled:    true,
		Trigger:    automation.Trigger{Type: "signal", Source: "slack"},
		Conditions: automation.Conditions{Self: &self},
		Actions:    []automation.Action{{Type: "shell", Command: "true"}},
	}}
	sigs := []model.Signal{
		{Source: "slack", Kind: "slack_dm", Title: "hi", IsSelf: false, StableID: "a"},
		{Source: "slack", Kind: "slack_dm", Title: "hi", IsSelf: true, StableID: "b"},
	}
	evs := automation.MatchSignals(rules, sigs, automation.MatchOpts{})
	if len(evs) != 1 || evs[0].Signal.StableID != "b" {
		t.Fatalf("got %+v", evs)
	}
}

func TestMatchTransitions(t *testing.T) {
	rules := []automation.Rule{{
		ID:      "checks-failing",
		Enabled: true,
		Trigger: automation.Trigger{
			Type:   "transition",
			Source: "github",
			Kind:   automation.KindSpec{"pr_review_status"},
			When:   automation.When{Field: "checks", To: "failing"},
		},
		Actions: []automation.Action{{Type: "shell", Command: "true"}},
	}}
	prev := map[string]model.Entity{
		"pr:Chip/salsa#1": {ID: "pr:Chip/salsa#1", Kind: "pr", State: map[string]string{"checks": "passing"}},
	}
	next := map[string]model.Entity{
		"pr:Chip/salsa#1": {ID: "pr:Chip/salsa#1", Kind: "pr", State: map[string]string{"checks": "failing"}, Coordinates: map[string]string{"repo": "Chip/salsa"}},
	}
	evs := automation.MatchTransitions(rules, prev, next, automation.MatchOpts{})
	if len(evs) != 1 {
		t.Fatalf("got %d events", len(evs))
	}
	if evs[0].From != "passing" || evs[0].To != "failing" {
		t.Fatalf("from=%q to=%q", evs[0].From, evs[0].To)
	}
}

func TestValidateRules(t *testing.T) {
	err := automation.ValidateRules([]automation.Rule{{
		ID:      "bad",
		Enabled: true,
		Trigger: automation.Trigger{Type: "transition"},
		Actions: []automation.Action{{Type: "webhook"}},
	}})
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestKindSpecYAML(t *testing.T) {
	var r automation.Rule
	if err := yaml.Unmarshal([]byte("id: x\nenabled: true\ntrigger:\n  type: signal\n  kind: slack_mention\nactions:\n  - type: shell\n    command: true\n"), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Trigger.Kind) != 1 || r.Trigger.Kind[0] != "slack_mention" {
		t.Fatalf("kind=%v", r.Trigger.Kind)
	}
	if err := yaml.Unmarshal([]byte("id: y\nenabled: true\ntrigger:\n  type: signal\n  kind: [a, b]\nactions:\n  - type: shell\n    command: true\n"), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Trigger.Kind) != 2 {
		t.Fatalf("kind=%v", r.Trigger.Kind)
	}
}

func TestWebhookRunner(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	runner := automation.WebhookRunner{}
	ev := automation.Event{
		Rule:    automation.Rule{ID: "r1"},
		Trigger: "signal",
		Signal:  &model.Signal{Source: "slack", Kind: "slack_mention", Title: "hello", URL: "http://slack"},
		FiredAt: time.Now(),
	}
	if err := runner.Run(context.Background(), automation.Action{Type: "webhook", URL: srv.URL}, ev); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotBody, `"rule_id":"r1"`) {
		t.Fatalf("body=%s", gotBody)
	}
}

func TestShellRunner(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.txt")
	runner := automation.ShellRunner{}
	ev := automation.Event{
		Rule:   automation.Rule{ID: "r1"},
		Signal: &model.Signal{Title: "hi", StableID: "s1"},
	}
	// Use a portable shell write.
	script := filepath.Join(dir, "write.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$DOCENT_TITLE\" > \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), automation.Action{
		Type:    "shell",
		Command: script,
		Args:    []string{out},
	}, ev); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(b)) != "hi" {
		t.Fatalf("got %q", b)
	}
}

func TestStoreCooldown(t *testing.T) {
	s := automation.NewStore()
	now := time.Now()
	s.Start("j1", "r1", "r1:s1", now)
	if !s.ShouldSkip("r1:s1", "30m", now.Add(time.Minute)) {
		t.Fatal("expected skip within cooldown")
	}
	if s.ShouldSkip("r1:s1", "30m", now.Add(31*time.Minute)) {
		t.Fatal("expected no skip after cooldown")
	}
}

func TestDispatcherCooldown(t *testing.T) {
	var calls int
	d := automation.NewDispatcher([]automation.Rule{{
		ID:         "r1",
		Enabled:    true,
		Trigger:    automation.Trigger{Type: "signal", Source: "slack"},
		Conditions: automation.Conditions{Cooldown: "1h"},
		Actions:    []automation.Action{{Type: "shell", Command: "true"}},
	}})
	d.Registry.Register("shell", automation.RunnerFunc(func(ctx context.Context, action automation.Action, ev automation.Event) error {
		calls++
		return nil
	}))
	ev := automation.Event{
		Rule:    d.Rules[0],
		Trigger: "signal",
		Signal:  &model.Signal{Source: "slack", StableID: "s1"},
		FiredAt: time.Now(),
	}
	d.HandleEvents(context.Background(), []automation.Event{ev, ev})
	// Give goroutines a moment.
	time.Sleep(50 * time.Millisecond)
	if calls != 1 {
		t.Fatalf("calls=%d want 1", calls)
	}
}

func TestRenderTemplate(t *testing.T) {
	out, err := automation.RenderTemplate("Ticket {{.Ticket.Key}} url={{.URL}}", automation.Context{
		Ticket: automation.TicketRef{Key: "SALSA-1"},
		URL:    "http://x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != "Ticket SALSA-1 url=http://x" {
		t.Fatalf("got %q", out)
	}
}
