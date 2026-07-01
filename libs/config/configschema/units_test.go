package configschema_test

import (
	"strings"
	"testing"

	"github.com/kurt/slakkr-ai/libs/config/configschema"
)

func TestValidateYAML_modeBlocksAccepted(t *testing.T) {
	doc := strings.TrimSpace(`
ai:
  provider: rule-based
directives:
  - id: jira
    name: Jira
    collector: jira
    enabled: true
    config:
      base_url: https://jira.example
    credential_refs:
      pat: SLAKKR_JIRA_PAT
    state:
      query: "assignee = currentUser() AND status = 'In Development'"
      poll: { interval: 5m, on_request: true }
    events:
      lookback: 7d
      poll: { interval: 15m }
`)
	if err := configschema.ValidateYAML([]byte(doc)); err != nil {
		t.Fatalf("mode blocks should validate: %v", configschema.ValidationProblems(err))
	}
}

func TestValidateYAML_modeBlockRejectsUnknownPollField(t *testing.T) {
	doc := strings.TrimSpace(`
ai:
  provider: rule-based
directives:
  - id: jira
    name: Jira
    collector: jira
    enabled: true
    config:
      base_url: https://jira.example
    credential_refs:
      pat: SLAKKR_JIRA_PAT
    state:
      poll: { interval: 5m, bogus: true }
`)
	if err := configschema.ValidateYAML([]byte(doc)); err == nil {
		t.Fatal("expected schema rejection of unknown poll field")
	}
}
