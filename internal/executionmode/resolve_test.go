package executionmode

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// scriptedPrompter answers Ask() calls from a queue of canned responses.
type scriptedPrompter struct {
	answers []string
	calls   []string
}

func (p *scriptedPrompter) Ask(prompt, defaultValue string) (string, error) {
	p.calls = append(p.calls, prompt)
	if len(p.answers) == 0 {
		return defaultValue, nil
	}
	ans := p.answers[0]
	p.answers = p.answers[1:]
	return ans, nil
}

func TestResolveDailyPlanDoesNotPrompt(t *testing.T) {
	mode := mustFindBuiltin(t, BuiltinDailyPlan)
	now := time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC) // Tuesday
	prompter := &scriptedPrompter{}
	res, err := Resolve(mode, ResolveOpts{Now: now, Prompter: prompter})
	if err != nil {
		t.Fatal(err)
	}
	if len(prompter.calls) != 0 {
		t.Fatalf("daily-plan must not prompt the user, got calls: %v", prompter.calls)
	}
	// previous-weekday from Tuesday => Monday 00:00 UTC.
	want := time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC)
	if !res.Since.Equal(want) {
		t.Fatalf("since: got %v want %v", res.Since, want)
	}
	if !res.Until.Equal(now) {
		t.Fatalf("until: got %v want %v", res.Until, now)
	}
	if res.LookbackDays != 0 {
		t.Fatalf("daily-plan LookbackDays should be 0, got %d", res.LookbackDays)
	}
	if res.Scope != ScopeSelf {
		t.Fatalf("daily-plan scope: %q", res.Scope)
	}
}

func TestResolvePreviousWeekdayFromMonday(t *testing.T) {
	mode := mustFindBuiltin(t, BuiltinDailyPlan)
	now := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC) // Monday
	res, err := Resolve(mode, ResolveOpts{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC) // previous Friday
	if !res.Since.Equal(want) {
		t.Fatalf("Mon: got %v want %v", res.Since, want)
	}
}

func TestResolveRecentActivityUsesBuiltinDefault(t *testing.T) {
	mode := mustFindBuiltin(t, BuiltinRecentActivity)
	now := time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC)
	res, err := Resolve(mode, ResolveOpts{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if res.LookbackDays != 7 {
		t.Fatalf("expected default 7 days, got %d", res.LookbackDays)
	}
	want := now.Add(-7 * 24 * time.Hour)
	if !res.Since.Equal(want) {
		t.Fatalf("since: got %v want %v", res.Since, want)
	}
}

func TestResolveDaysOverrideForcesDaysLookback(t *testing.T) {
	// Even for daily-plan (previous-weekday), --days N overrides to days mode.
	mode := mustFindBuiltin(t, BuiltinDailyPlan)
	now := time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC)
	res, err := Resolve(mode, ResolveOpts{Now: now, DaysOverride: 14})
	if err != nil {
		t.Fatal(err)
	}
	if res.LookbackDays != 14 {
		t.Fatalf("override LookbackDays: got %d", res.LookbackDays)
	}
	want := now.Add(-14 * 24 * time.Hour)
	if !res.Since.Equal(want) {
		t.Fatalf("since: got %v want %v", res.Since, want)
	}
}

func TestResolveCustomPromptAsksForPrompt(t *testing.T) {
	mode := mustFindBuiltin(t, BuiltinCustomPrompt)
	prompter := &scriptedPrompter{answers: []string{"summarize me"}}
	res, err := Resolve(mode, ResolveOpts{
		Now:      time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC),
		Prompter: prompter,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Instruction != "summarize me" {
		t.Fatalf("instruction: got %q", res.Instruction)
	}
	if res.Scope != ScopeAll {
		t.Fatalf("custom-prompt scope: %q", res.Scope)
	}
	if len(prompter.calls) != 1 {
		t.Fatalf("expected exactly one prompt ask, got %v", prompter.calls)
	}
}

func TestResolveCustomPromptOverrideSkipsAsk(t *testing.T) {
	mode := mustFindBuiltin(t, BuiltinCustomPrompt)
	prompter := &scriptedPrompter{}
	res, err := Resolve(mode, ResolveOpts{
		Now:            time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC),
		Prompter:       prompter,
		PromptOverride: "from flag",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Instruction != "from flag" {
		t.Fatalf("instruction: got %q", res.Instruction)
	}
	if len(prompter.calls) != 0 {
		t.Fatalf("override should skip prompt; calls: %v", prompter.calls)
	}
}

func TestResolveCustomPromptWithoutPrompterErrors(t *testing.T) {
	mode := mustFindBuiltin(t, BuiltinCustomPrompt)
	_, err := Resolve(mode, ResolveOpts{
		Now: time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC),
	})
	if err == nil {
		t.Fatal("expected error when no Prompter and no override")
	}
	if !strings.Contains(err.Error(), "--prompt") {
		t.Fatalf("error should mention --prompt, got %q", err.Error())
	}
}

func TestResolveScopeDefaultsToSelfWhenUnset(t *testing.T) {
	mode := ExecutionMode{
		ID:       "untyped",
		Lookback: &Lookback{Kind: LookbackKindDays, Days: 3},
		Prompt:   &Prompt{Instruction: "x"},
	}
	res, err := Resolve(mode, ResolveOpts{
		Now: time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Scope != ScopeSelf {
		t.Fatalf("expected default scope=self, got %q", res.Scope)
	}
}

func TestResolveFormatterFallsBackToConfig(t *testing.T) {
	mode := ExecutionMode{
		ID:       "x",
		Lookback: &Lookback{Kind: LookbackKindDays, Days: 1},
		Prompt:   &Prompt{Instruction: "x"},
	}
	res, err := Resolve(mode, ResolveOpts{
		Now:                     time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC),
		ConfigActivityFormatter: "json-signal-list",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Formatter != "json-signal-list" {
		t.Fatalf("formatter: got %q", res.Formatter)
	}
}

func TestResolveLookbackPromptParsing(t *testing.T) {
	mode := ExecutionMode{ID: "ask-me", Prompt: &Prompt{Instruction: "x"}}
	prompter := &scriptedPrompter{answers: []string{"3"}}
	res, err := Resolve(mode, ResolveOpts{
		Now:      time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC),
		Prompter: prompter,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.LookbackDays != 3 {
		t.Fatalf("expected 3 days, got %d", res.LookbackDays)
	}

	bad := &scriptedPrompter{answers: []string{"oops"}}
	_, err = Resolve(mode, ResolveOpts{
		Now:      time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC),
		Prompter: bad,
	})
	if err == nil || !strings.Contains(err.Error(), "positive integer") {
		t.Fatalf("expected positive-integer error, got %v", err)
	}
}

func TestResolvePropagatesPrompterError(t *testing.T) {
	mode := mustFindBuiltin(t, BuiltinCustomPrompt)
	bang := errors.New("boom")
	_, err := Resolve(mode, ResolveOpts{
		Now:      time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC),
		Prompter: errorPrompter{err: bang},
	})
	if !errors.Is(err, bang) {
		t.Fatalf("expected boom, got %v", err)
	}
}

type errorPrompter struct{ err error }

func (e errorPrompter) Ask(string, string) (string, error) { return "", e.err }

func mustFindBuiltin(t *testing.T, id string) ExecutionMode {
	t.Helper()
	m, ok := Find(BuiltinModes(), id)
	if !ok {
		t.Fatalf("builtin %q missing", id)
	}
	return m
}
