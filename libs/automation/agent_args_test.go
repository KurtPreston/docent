package automation

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestAgentCommandModel(t *testing.T) {
	r := AgentRunner{}
	cases := []struct {
		provider, model string
		wantName        string
		wantArgs        []string
	}{
		// Unset model keeps the original arguments exactly
		{"cursor", "", "cursor-agent", []string{"-p", "--output-format=text", "--trust", "--force", "P"}},
		{"claude", "", "claude", []string{"--print", "--output-format=text", "--permission-mode", "acceptEdits", "P"}},
		{"copilot", "", "copilot", []string{"--allow-all", "--no-ask-user", "--silent", "--no-color", "-p", "P"}},

		// A pinned model goes in as --model, ahead of the prompt
		{"cursor", "m1", "cursor-agent", []string{"-p", "--output-format=text", "--trust", "--force", "--model", "m1", "P"}},
		{"claude", "m1", "claude", []string{"--print", "--output-format=text", "--permission-mode", "acceptEdits", "--model", "m1", "P"}},
		{"Copilot", "m1", "copilot", []string{"--allow-all", "--no-ask-user", "--silent", "--no-color", "--model", "m1", "-p", "P"}},
	}
	for _, c := range cases {
		name, args, err := r.agentCommand(c.provider, c.model, "P")
		if err != nil {
			t.Fatalf("%s/%q: %v", c.provider, c.model, err)
		}
		if name != c.wantName || !slices.Equal(args, c.wantArgs) {
			t.Errorf("%s/%q: got %s %q, want %s %q", c.provider, c.model, name, args, c.wantName, c.wantArgs)
		}
	}
}

func TestAgentCommandUnknownProvider(t *testing.T) {
	if _, _, err := (AgentRunner{}).agentCommand("ollama", "m1", "P"); err == nil {
		t.Fatal("expected an error for an unknown provider")
	}
}

func TestValidateRulesAcceptsModel(t *testing.T) {
	rule := Rule{
		ID:      "review",
		Trigger: Trigger{Type: "schedule", At: "04:00"},
		Actions: []Action{{Type: "agent-inline", Provider: "copilot", Model: "some-model", Prompt: "review"}},
	}
	if err := ValidateRules([]Rule{rule}); err != nil {
		t.Fatalf("ValidateRules: %v", err)
	}
}

func TestValidateRulesActionTimeout(t *testing.T) {
	cases := []struct {
		typ, timeout string
		wantErr      string
	}{
		// agent-inline takes any positive duration, including days
		{"agent-inline", "", ""},
		{"agent-inline", "90m", ""},
		{"agent-inline", "1d", ""},

		// Bad values and other action types are rejected
		{"agent-inline", "soon", "not a positive duration"},
		{"agent-inline", "-5m", "not a positive duration"},
		{"agent", "1h", "only supported on agent-inline"},
	}
	for _, c := range cases {
		rule := Rule{
			ID:      "r",
			Trigger: Trigger{Type: "schedule", At: "04:00"},
			Actions: []Action{{Type: c.typ, Prompt: "p", Timeout: c.timeout}},
		}
		err := ValidateRules([]Rule{rule})
		switch {
		case c.wantErr == "" && err != nil:
			t.Errorf("%s timeout %q: unexpected error %v", c.typ, c.timeout, err)
		case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
			t.Errorf("%s timeout %q: error %v, want %q", c.typ, c.timeout, err, c.wantErr)
		}
	}
}

func TestAgentRunTimeout(t *testing.T) {
	if got := agentRunTimeout(Action{}); got != defaultAgentTimeout {
		t.Errorf("unset timeout = %s, want %s", got, defaultAgentTimeout)
	}
	if got := agentRunTimeout(Action{Timeout: "2h"}); got != 2*time.Hour {
		t.Errorf("2h timeout = %s", got)
	}

	// The lock wait covers at least two default-length runs, and more for a
	// longer run of its own
	if got := worktreeAcquireTimeout(time.Minute); got != 2*defaultAgentTimeout {
		t.Errorf("short run lock wait = %s", got)
	}
	if got := worktreeAcquireTimeout(2 * time.Hour); got != 4*time.Hour {
		t.Errorf("long run lock wait = %s", got)
	}
}
