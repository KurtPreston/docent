package automation

import (
	"slices"
	"testing"
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
