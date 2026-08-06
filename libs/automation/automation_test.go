package automation_test

import (
	"context"
	"fmt"
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

func TestMatchTransitions_realGithubKind(t *testing.T) {
	// Real PR entities have Kind "pr_review_status" (not "pr"), and a
	// source: github gate must not reject them.
	rules := []automation.Rule{{
		ID:      "autofix-pr",
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
		"pr:Chip/salsa#1": {ID: "pr:Chip/salsa#1", Kind: "pr_review_status", State: map[string]string{"checks": "passing", "is_self": "true"}},
	}
	next := map[string]model.Entity{
		"pr:Chip/salsa#1": {ID: "pr:Chip/salsa#1", Kind: "pr_review_status", State: map[string]string{"checks": "failing", "is_self": "true"}, Coordinates: map[string]string{"repo": "Chip/salsa"}},
	}
	evs := automation.MatchTransitions(rules, prev, next, automation.MatchOpts{})
	if len(evs) != 1 {
		t.Fatalf("got %d events, want 1", len(evs))
	}
}

func TestMatchTransitions_assigneeToMe(t *testing.T) {
	// Real JIRA state entities have Kind "issue"; assignee -> me must fire
	// only when the entity is the current user's (is_self), not for any
	// assignment to anyone.
	rules := []automation.Rule{{
		ID:      "jira-assigned",
		Enabled: true,
		Trigger: automation.Trigger{
			Type:   "transition",
			Source: "jira",
			Kind:   automation.KindSpec{"issue"},
			When:   automation.When{Field: "assignee", To: "me"},
		},
		Actions: []automation.Action{{Type: "shell", Command: "true"}},
	}}
	prev := map[string]model.Entity{
		"jira:SALSA-1": {ID: "jira:SALSA-1", Kind: "issue", State: map[string]string{"assignee": ""}},
		"jira:SALSA-2": {ID: "jira:SALSA-2", Kind: "issue", State: map[string]string{"assignee": "someone"}},
	}
	next := map[string]model.Entity{
		// Newly assigned to me (is_self set by the engine).
		"jira:SALSA-1": {ID: "jira:SALSA-1", Kind: "issue", State: map[string]string{"assignee": "kpreston", "is_self": "true"}},
		// Reassigned to another person on a ticket that isn't mine.
		"jira:SALSA-2": {ID: "jira:SALSA-2", Kind: "issue", State: map[string]string{"assignee": "other"}},
	}
	evs := automation.MatchTransitions(rules, prev, next, automation.MatchOpts{})
	if len(evs) != 1 {
		t.Fatalf("got %d events, want 1", len(evs))
	}
	if evs[0].Entity.ID != "jira:SALSA-1" {
		t.Fatalf("fired for %s, want jira:SALSA-1", evs[0].Entity.ID)
	}
}

func TestMatchTransitions_matchFields(t *testing.T) {
	// A transition rule must be able to narrow to the PRs a pr_queries entry
	// labels, the way a signal rule can. Every open PR is is_self, and
	// `relation` is not expressible in a `when` clause, so without this gate a
	// draft -> ready rule aimed at a bot's backports also fires for every PR
	// the developer authored or was asked to review.
	rules := []automation.Rule{{
		ID:      "backport-review",
		Enabled: true,
		Trigger: automation.Trigger{
			Type:   "transition",
			Source: "github-enterprise",
			Kind:   automation.KindSpec{"pr_review_status"},
			Match:  automation.Match{Fields: map[string]string{"relation": "backport"}},
			When:   automation.When{Field: "is_draft", To: "false"},
		},
		Actions: []automation.Action{{Type: "shell", Command: "true"}},
	}}
	// Neither PR exists in prev: both are newly observed, so is_draft reads as
	// "" -> its current value. Only the backport may fire.
	next := map[string]model.Entity{
		"backport": {ID: "backport", Kind: "pr_review_status", State: map[string]string{
			"relation": "backport", "is_draft": "false", "is_self": "true",
		}},
		"authored": {ID: "authored", Kind: "pr_review_status", State: map[string]string{
			"relation": "authored", "is_draft": "false", "is_self": "true",
		}},
	}
	evs := automation.MatchTransitions(rules, map[string]model.Entity{}, next, automation.MatchOpts{})
	if len(evs) != 1 || evs[0].Entity.ID != "backport" {
		t.Fatalf("got %+v, want a single event for backport", evs)
	}

	// The other half of the rule's job: a conflicted backport opens as a draft
	// (no fire), then fires once the developer resolves it and marks it ready.
	prev := map[string]model.Entity{
		"backport": {ID: "backport", Kind: "pr_review_status", State: map[string]string{
			"relation": "backport", "is_draft": "true", "is_self": "true",
		}},
	}
	evs = automation.MatchTransitions(rules, prev, map[string]model.Entity{
		"backport": next["backport"],
	}, automation.MatchOpts{})
	if len(evs) != 1 || evs[0].From != "true" || evs[0].To != "false" {
		t.Fatalf("got %+v, want a single true->false event", evs)
	}
}

func TestMatchTransitions_selfCondition(t *testing.T) {
	self := true
	rules := []automation.Rule{{
		ID:         "self-only",
		Enabled:    true,
		Trigger:    automation.Trigger{Type: "transition", Source: "github", Kind: automation.KindSpec{"pr_review_status"}, When: automation.When{Field: "checks", To: "failing"}},
		Conditions: automation.Conditions{Self: &self},
		Actions:    []automation.Action{{Type: "shell", Command: "true"}},
	}}
	prev := map[string]model.Entity{
		"a": {ID: "a", Kind: "pr_review_status", State: map[string]string{"checks": "passing", "is_self": "true"}},
		"b": {ID: "b", Kind: "pr_review_status", State: map[string]string{"checks": "passing"}},
	}
	next := map[string]model.Entity{
		"a": {ID: "a", Kind: "pr_review_status", State: map[string]string{"checks": "failing", "is_self": "true"}},
		"b": {ID: "b", Kind: "pr_review_status", State: map[string]string{"checks": "failing"}},
	}
	evs := automation.MatchTransitions(rules, prev, next, automation.MatchOpts{})
	if len(evs) != 1 || evs[0].Entity.ID != "a" {
		t.Fatalf("got %+v, want single event for a", evs)
	}
}

func TestEventContext_signalFields(t *testing.T) {
	ev := automation.Event{
		Rule:    automation.Rule{ID: "r"},
		Trigger: "signal",
		Signal: &model.Signal{
			Source: "local-git",
			Kind:   "commit",
			Title:  "SALSA-900 fix things",
			Fields: map[string]string{
				"path":   "/code/demo",
				"ticket": "SALSA-900",
				"branch": "main",
			},
		},
	}
	ctx := automation.EventContext(ev)
	if ctx.OpenPath != "/code/demo" {
		t.Errorf("OpenPath = %q, want /code/demo (agent workdir open_path needs this)", ctx.OpenPath)
	}
	if ctx.Ticket.Key != "SALSA-900" {
		t.Errorf("Ticket.Key = %q, want SALSA-900", ctx.Ticket.Key)
	}
	if ctx.Branch != "main" {
		t.Errorf("Branch = %q, want main", ctx.Branch)
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

func TestRunRuleNowPropagatesPriorActionError(t *testing.T) {
	d := automation.NewDispatcher(nil)
	d.Registry.Register("report", automation.RunnerFunc(func(ctx context.Context, action automation.Action, ev automation.Event) error {
		if ev.PriorActionError != "" {
			t.Fatalf("expected no prior error before the first action, got %q", ev.PriorActionError)
		}
		return fmt.Errorf("cursor-agent exited with code 1: stderr:\nError: Authentication required")
	}))
	var seenEnv, seenTemplateErr string
	d.Registry.Register("shell", automation.RunnerFunc(func(ctx context.Context, action automation.Action, ev automation.Event) error {
		seenEnv = envValue(automation.EnvPairs(automation.EventContext(ev)), "DOCENT_ACTION_ERROR")
		seenTemplateErr = automation.EventContext(ev).ActionError
		return nil
	}))
	ev := automation.Event{
		Rule: automation.Rule{
			ID: "daily-standup",
			Actions: []automation.Action{
				{Type: "report"},
				{Type: "shell", Command: "notify-slack.sh"},
			},
		},
		FiredAt: time.Now(),
	}
	job := d.RunRuleNow(context.Background(), ev)
	if job.Status != automation.JobError {
		t.Fatalf("job status = %v, want error", job.Status)
	}
	if !strings.Contains(seenEnv, "Authentication required") {
		t.Fatalf("DOCENT_ACTION_ERROR = %q, want it to mention the report action's failure", seenEnv)
	}
	if !strings.HasPrefix(seenEnv, "report: ") {
		t.Fatalf("DOCENT_ACTION_ERROR = %q, want it prefixed with the failing action's type", seenEnv)
	}
	if seenTemplateErr != seenEnv {
		t.Fatalf("Context.ActionError = %q, want it to match the env value %q", seenTemplateErr, seenEnv)
	}
}

func envValue(pairs []string, key string) string {
	prefix := key + "="
	for _, p := range pairs {
		if strings.HasPrefix(p, prefix) {
			return strings.TrimPrefix(p, prefix)
		}
	}
	return ""
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
