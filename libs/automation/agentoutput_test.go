package automation_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/KurtPreston/docent/libs/automation"
	"github.com/KurtPreston/docent/libs/model"
)

func TestMatchTransitions_change(t *testing.T) {
	rule := func(change string) []automation.Rule {
		return []automation.Rule{{
			ID:      "new-comments",
			Enabled: true,
			Trigger: automation.Trigger{
				Type:   "transition",
				Source: "github",
				Kind:   automation.KindSpec{"pr_review_status"},
				When:   automation.When{Field: "unresolved_mine", Change: change},
			},
			Actions: []automation.Action{{Type: "shell", Command: "true"}},
		}}
	}
	ent := func(v string) map[string]model.Entity {
		return map[string]model.Entity{"pr:a": {ID: "pr:a", Kind: "pr_review_status", State: map[string]string{"unresolved_mine": v}}}
	}
	cases := []struct {
		change, from, to string
		want             bool
	}{
		{"increase", "1", "2", true},
		{"increase", "2", "1", false},
		{"increase", "", "1", true},
		{"increase", "1", "", false},
		{"decrease", "2", "0", true},
		{"decrease", "0", "2", false},
		{"increase", "x", "2", false},
		{"", "2", "1", true},
	}
	for _, c := range cases {
		prev := ent(c.from)
		if c.from == "" {
			prev = map[string]model.Entity{}
		}
		evs := automation.MatchTransitions(rule(c.change), prev, ent(c.to), automation.MatchOpts{})
		if got := len(evs) == 1; got != c.want {
			t.Errorf("change=%q %q->%q: fired=%v, want %v", c.change, c.from, c.to, got, c.want)
		}
	}
}

func TestValidateRules_change(t *testing.T) {
	r := automation.Rule{
		ID:      "x",
		Enabled: true,
		Trigger: automation.Trigger{Type: "transition", When: automation.When{Field: "n", Change: "sideways"}},
		Actions: []automation.Action{{Type: "shell", Command: "true"}},
	}
	if err := automation.ValidateRules([]automation.Rule{r}); err == nil || !strings.Contains(err.Error(), "when.change") {
		t.Fatalf("expected a when.change problem, got %v", err)
	}
	r.Trigger.When.Change = "increase"
	if err := automation.ValidateRules([]automation.Rule{r}); err != nil {
		t.Fatalf("unexpected problems: %v", err)
	}
}

type outputRunner struct{ out string }

func (o outputRunner) Run(ctx context.Context, a automation.Action, ev automation.Event) error {
	return nil
}

func (o outputRunner) RunWithOutput(ctx context.Context, a automation.Action, ev automation.Event) (string, error) {
	return o.out, nil
}

func TestRunRuleNowPropagatesAgentOutput(t *testing.T) {
	d := automation.NewDispatcher(nil)
	d.Registry.Register("agent-inline", outputRunner{out: "  resolved 2 threads\n"})
	var body, env string
	d.Registry.Register("slack-post", automation.RunnerFunc(func(ctx context.Context, a automation.Action, ev automation.Event) error {
		var err error
		body, err = automation.RenderTemplate(a.Body, automation.EventContext(ev))
		env = envValue(automation.EnvPairs(automation.EventContext(ev)), "DOCENT_AGENT_OUTPUT")
		return err
	}))
	job := d.RunRuleNow(context.Background(), automation.Event{
		Rule: automation.Rule{ID: "r", Actions: []automation.Action{
			{Type: "agent-inline"},
			{Type: "slack-post", Body: "done: {{.AgentOutput}}"},
		}},
		FiredAt: time.Now(),
	})
	if job.Status == automation.JobError {
		t.Fatalf("job failed: %+v", job)
	}
	if body != "done: resolved 2 threads" {
		t.Fatalf("body = %q", body)
	}
	if env != "resolved 2 threads" {
		t.Fatalf("DOCENT_AGENT_OUTPUT = %q", env)
	}
}
