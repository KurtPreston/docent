package executionmode

import (
	"strings"
	"testing"
)

func TestLoadAppendsUserModes(t *testing.T) {
	user := []ExecutionMode{
		{
			ID:       "repo-activity",
			Name:     "Repo activity",
			Lookback: &Lookback{Kind: LookbackKindDays, Days: 14},
			Prompt:   &Prompt{Instruction: "Summarize repo activity."},
			Scope:    ScopeInvolved,
		},
	}
	modes, err := Load(BuiltinModes(), user)
	if err != nil {
		t.Fatal(err)
	}
	if len(modes) != 4 {
		t.Fatalf("expected 4 modes, got %d", len(modes))
	}
	if modes[3].ID != "repo-activity" {
		t.Fatalf("user mode should be appended last, got %q", modes[3].ID)
	}
	// Built-in order preserved.
	for i, want := range []string{BuiltinDailyPlan, BuiltinRecentActivity, BuiltinCustomPrompt} {
		if modes[i].ID != want {
			t.Fatalf("position %d: got %q want %q", i, modes[i].ID, want)
		}
	}
}

func TestLoadUserOverridesBuiltinByID(t *testing.T) {
	user := []ExecutionMode{
		{
			ID:     BuiltinDailyPlan,
			Name:   "My daily plan",
			Prompt: &Prompt{Instruction: "Custom daily-plan instruction"},
		},
	}
	modes, err := Load(BuiltinModes(), user)
	if err != nil {
		t.Fatal(err)
	}
	if len(modes) != 3 {
		t.Fatalf("expected 3 modes after override, got %d", len(modes))
	}
	if modes[0].ID != BuiltinDailyPlan {
		t.Fatalf("override should preserve position, got %q at index 0", modes[0].ID)
	}
	if modes[0].Name != "My daily plan" {
		t.Fatalf("override should replace built-in fields, got Name=%q", modes[0].Name)
	}
	if modes[0].Prompt == nil || modes[0].Prompt.Instruction != "Custom daily-plan instruction" {
		t.Fatalf("override should replace prompt, got %+v", modes[0].Prompt)
	}
}

func TestLoadRejectsUserDuplicateID(t *testing.T) {
	user := []ExecutionMode{
		{ID: "x", Lookback: &Lookback{Kind: LookbackKindDays, Days: 1}},
		{ID: "x", Lookback: &Lookback{Kind: LookbackKindDays, Days: 2}},
	}
	_, err := Load(BuiltinModes(), user)
	if err == nil {
		t.Fatal("expected duplicate id error")
	}
	if !strings.Contains(err.Error(), "duplicate execution mode id") {
		t.Fatalf("expected 'duplicate execution mode id' in error, got %q", err.Error())
	}
}

func TestLoadAggregatesInvalidUserModes(t *testing.T) {
	user := []ExecutionMode{
		{ID: "good", Lookback: &Lookback{Kind: LookbackKindDays, Days: 1}},
		{ID: "Bad-ID"},
		{ID: "weird", Scope: "team"},
	}
	_, err := Load(BuiltinModes(), user)
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"Bad-ID", "weird", "unknown scope"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error missing %q: %s", want, err.Error())
		}
	}
}

func TestFind(t *testing.T) {
	modes := BuiltinModes()
	got, ok := Find(modes, BuiltinRecentActivity)
	if !ok {
		t.Fatal("expected to find recent-activity")
	}
	if got.ID != BuiltinRecentActivity {
		t.Fatalf("got %q", got.ID)
	}
	if _, ok := Find(modes, "nope"); ok {
		t.Fatal("expected not found")
	}
}
