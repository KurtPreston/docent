package automation

import (
	"strings"
	"testing"
)

func TestRuleWarningsFlagsIncludeExistingOutsideTransitions(t *testing.T) {
	rules := []Rule{
		{ID: "queue", Trigger: Trigger{Type: "transition", IncludeExisting: true}},
		{ID: "signal", Trigger: Trigger{IncludeExisting: true}},
		{ID: "nightly", Trigger: Trigger{Type: "schedule", At: "09:00", IncludeExisting: true}},
		{ID: "plain", Trigger: Trigger{Type: "schedule", At: "09:00"}},
	}
	warnings := RuleWarnings(rules)
	if len(warnings) != 2 {
		t.Fatalf("warnings = %q, want 2", warnings)
	}

	// Each warning names its rule and trigger type
	if !strings.Contains(warnings[0], "(signal)") || !strings.Contains(warnings[0], "signal trigger") {
		t.Errorf("warnings[0] = %q, want it to name the signal rule", warnings[0])
	}
	if !strings.Contains(warnings[1], "(nightly)") || !strings.Contains(warnings[1], "schedule trigger") {
		t.Errorf("warnings[1] = %q, want it to name the schedule rule", warnings[1])
	}
}

func TestValidateRulesRejectsNegativeMaxConcurrent(t *testing.T) {
	rule := Rule{
		ID:         "queue",
		Trigger:    Trigger{Type: "transition", When: When{Field: "queued"}},
		Conditions: Conditions{MaxConcurrent: -1},
		Actions:    []Action{{Type: "shell", Command: "true"}},
	}
	err := ValidateRules([]Rule{rule})
	if err == nil || !strings.Contains(err.Error(), "max_concurrent") {
		t.Fatalf("err = %v, want a max_concurrent problem", err)
	}

	// Zero means no limit and is valid
	rule.Conditions.MaxConcurrent = 0
	if err := ValidateRules([]Rule{rule}); err != nil {
		t.Fatalf("max_concurrent 0: %v", err)
	}
}
