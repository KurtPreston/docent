package executionmode

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// scriptedPrompter answers Ask() and Select() calls from a shared queue of
// canned responses. Select records the *label* it returned in selectPicks
// so tests can assert on the choice that was made; when the canned answer
// happens to be a bare scope id like "all", the resolver also accepts it
// (see resolveScope's fallback branch).
type scriptedPrompter struct {
	answers     []string
	calls       []string
	selectCalls []selectCall
}

type selectCall struct {
	prompt       string
	options      []string
	defaultValue string
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

func (p *scriptedPrompter) Select(prompt string, options []string, defaultValue string) (string, error) {
	p.calls = append(p.calls, prompt)
	p.selectCalls = append(p.selectCalls, selectCall{prompt: prompt, options: options, defaultValue: defaultValue})
	if len(p.answers) == 0 {
		return defaultValue, nil
	}
	ans := p.answers[0]
	p.answers = p.answers[1:]
	return ans, nil
}

func TestResolvePRsDoesNotPromptAndCarriesCollectors(t *testing.T) {
	mode := mustFindBuiltin(t, BuiltinPRs)
	now := time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC)
	prompter := &scriptedPrompter{}
	res, err := Resolve(mode, ResolveOpts{Now: now, Prompter: prompter})
	if err != nil {
		t.Fatal(err)
	}
	if len(prompter.calls) != 0 {
		t.Fatalf("prs must not prompt the user, got calls: %v", prompter.calls)
	}
	if res.Scope != ScopeSelf {
		t.Fatalf("prs scope: %q", res.Scope)
	}
	if res.Collect != CollectState {
		t.Fatalf("prs collect: %q", res.Collect)
	}
	want := map[string]bool{"github": true, "github-enterprise": true}
	if len(res.Collectors) != len(want) {
		t.Fatalf("prs collectors: %v", res.Collectors)
	}
	for _, c := range res.Collectors {
		if !want[c] {
			t.Fatalf("unexpected collector %q in resolved run", c)
		}
	}
}

func TestResolveCollectOverridePrecedence(t *testing.T) {
	now := time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC)

	// Override beats a mode that pinned collect (daily-plan pins both).
	daily := mustFindBuiltin(t, BuiltinDailyPlan)
	res, err := Resolve(daily, ResolveOpts{Now: now, CollectOverride: CollectEvents})
	if err != nil {
		t.Fatal(err)
	}
	if res.Collect != CollectEvents {
		t.Fatalf("override should beat mode-pinned collect: got %q want events", res.Collect)
	}

	// Unset mode defaults to events.
	mode := ExecutionMode{
		ID:       "untyped",
		Lookback: &Lookback{Kind: LookbackKindDays, Days: 3},
		Prompt:   &Prompt{Instruction: "x"},
	}
	res, err = Resolve(mode, ResolveOpts{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if res.Collect != CollectEvents {
		t.Fatalf("default collect: got %q want events", res.Collect)
	}

	if _, err := Resolve(daily, ResolveOpts{Now: now, CollectOverride: Collect("bogus")}); err == nil {
		t.Fatal("expected error for invalid collect override")
	}
}

func TestResolveScopeOverridePrecedence(t *testing.T) {
	now := time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC)

	// Override beats a mode that pinned a scope (daily-plan pins involved).
	daily := mustFindBuiltin(t, BuiltinDailyPlan)
	res, err := Resolve(daily, ResolveOpts{Now: now, ScopeOverride: ScopeAll})
	if err != nil {
		t.Fatal(err)
	}
	if res.Scope != ScopeAll {
		t.Fatalf("override should beat mode-pinned scope: got %q want all", res.Scope)
	}

	// Override beats the interactive prompt: recent-activity leaves scope
	// unset (would normally Select), but a non-empty override must skip the
	// scope prompt entirely. --days keeps the lookback prompt from firing.
	recent := mustFindBuiltin(t, BuiltinRecentActivity)
	prompter := &scriptedPrompter{}
	res, err = Resolve(recent, ResolveOpts{
		Now:           now,
		Prompter:      prompter,
		DaysOverride:  3,
		ScopeOverride: ScopeSelf,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Scope != ScopeSelf {
		t.Fatalf("override should beat interactive scope prompt: got %q want self", res.Scope)
	}
	if len(prompter.selectCalls) != 0 {
		t.Fatalf("scope override must suppress the scope Select, got %d Select call(s)", len(prompter.selectCalls))
	}

	// An invalid override is rejected rather than silently ignored.
	if _, err := Resolve(daily, ResolveOpts{Now: now, ScopeOverride: Scope("bogus")}); err == nil {
		t.Fatal("expected error for invalid scope override")
	}
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
	if res.Scope != ScopeInvolved {
		t.Fatalf("daily-plan scope: %q", res.Scope)
	}
	if res.Collect != CollectBoth {
		t.Fatalf("daily-plan collect: %q", res.Collect)
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

func TestResolveRecentActivityNoPrompterUsesDefaultSeven(t *testing.T) {
	// Without a Prompter (e.g. non-interactive invocation with no --days
	// flag), recent-activity falls back to the documented 7-day default.
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

func TestResolveRecentActivityPromptsForDaysAndScope(t *testing.T) {
	// Interactive runs (Prompter set, --days unset, scope unset) must ask
	// the user for both the lookback window and the collection scope. This
	// is the documented "default 7, or prompt" lookback plus the
	// "ExecutionMode without scope → prompt" behavior.
	mode := mustFindBuiltin(t, BuiltinRecentActivity)
	now := time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC)
	prompter := &scriptedPrompter{answers: []string{"14", "all"}}
	res, err := Resolve(mode, ResolveOpts{Now: now, Prompter: prompter})
	if err != nil {
		t.Fatal(err)
	}
	if res.LookbackDays != 14 {
		t.Fatalf("expected 14 days from prompt, got %d", res.LookbackDays)
	}
	if res.Scope != ScopeAll {
		t.Fatalf("expected scope=all from prompt, got %q", res.Scope)
	}
	if len(prompter.calls) != 2 {
		t.Fatalf("expected days + scope prompts, got %v", prompter.calls)
	}
	if !strings.Contains(strings.ToLower(prompter.calls[0]), "lookback") {
		t.Fatalf("first prompt should be lookback, got %q", prompter.calls[0])
	}
	if !strings.Contains(strings.ToLower(prompter.calls[1]), "scope") {
		t.Fatalf("second prompt should be scope, got %q", prompter.calls[1])
	}
	if len(prompter.selectCalls) != 1 {
		t.Fatalf("expected exactly one Select call (for scope), got %d", len(prompter.selectCalls))
	}
	sc := prompter.selectCalls[0]
	if !strings.HasPrefix(sc.defaultValue, string(ScopeInvolved)) {
		t.Fatalf("scope select default should be the involved label, got %q", sc.defaultValue)
	}
	wantOptions := []Scope{ScopeSelf, ScopeInvolved, ScopeAll}
	if len(sc.options) != len(wantOptions) {
		t.Fatalf("scope menu options: got %v want %v", sc.options, wantOptions)
	}
	for i, want := range wantOptions {
		if !strings.HasPrefix(sc.options[i], string(want)) {
			t.Fatalf("scope option %d: got %q want prefix %q", i, sc.options[i], want)
		}
	}
}

func TestResolveRecentActivityScopePromptDefaultsToInvolved(t *testing.T) {
	// When the user accepts the highlighted default in the scope menu
	// (Select returns its defaultValue label verbatim), the resolver maps
	// it back to ScopeInvolved.
	mode := mustFindBuiltin(t, BuiltinRecentActivity)
	prompter := &scriptedPrompter{} // empty queue → Select returns defaultValue
	res, err := Resolve(mode, ResolveOpts{
		Now:      time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC),
		Prompter: prompter,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Scope != ScopeInvolved {
		t.Fatalf("default scope: got %q want involved", res.Scope)
	}
	if res.LookbackDays != 7 {
		t.Fatalf("default lookback days: got %d want 7", res.LookbackDays)
	}
}

func TestResolveRecentActivityDaysFlagSkipsLookbackPrompt(t *testing.T) {
	// --days N is an explicit override and must short-circuit the lookback
	// prompt. The scope prompt still fires because scope is unset; an
	// empty answer queue lets it accept the involved default.
	mode := mustFindBuiltin(t, BuiltinRecentActivity)
	now := time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC)
	prompter := &scriptedPrompter{}
	res, err := Resolve(mode, ResolveOpts{Now: now, Prompter: prompter, DaysOverride: 3})
	if err != nil {
		t.Fatal(err)
	}
	if res.LookbackDays != 3 {
		t.Fatalf("expected 3 days from override, got %d", res.LookbackDays)
	}
	for _, c := range prompter.calls {
		if strings.Contains(strings.ToLower(c), "lookback") {
			t.Fatalf("--days override should skip lookback prompt, got %q", c)
		}
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

func TestResolveCustomPromptAsksForLookbackAndPrompt(t *testing.T) {
	mode := mustFindBuiltin(t, BuiltinCustomPrompt)
	// Lookback is asked first (custom-prompt leaves it nil), then the
	// prompt instruction.
	prompter := &scriptedPrompter{answers: []string{"3", "summarize me"}}
	now := time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC)
	res, err := Resolve(mode, ResolveOpts{
		Now:      now,
		Prompter: prompter,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.LookbackDays != 3 {
		t.Fatalf("LookbackDays: got %d want 3", res.LookbackDays)
	}
	if want := now.Add(-3 * 24 * time.Hour); !res.Since.Equal(want) {
		t.Fatalf("since: got %v want %v", res.Since, want)
	}
	if res.Instruction != "summarize me" {
		t.Fatalf("instruction: got %q", res.Instruction)
	}
	if res.Scope != ScopeInvolved {
		t.Fatalf("custom-prompt scope: %q", res.Scope)
	}
	if len(prompter.calls) != 2 {
		t.Fatalf("expected lookback + prompt asks, got %v", prompter.calls)
	}
}

func TestResolveCustomPromptDefaultsLookbackToSeven(t *testing.T) {
	// An empty answer to the lookback ask keeps the documented 7-day default.
	mode := mustFindBuiltin(t, BuiltinCustomPrompt)
	prompter := &scriptedPrompter{answers: []string{"", "summarize me"}}
	now := time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC)
	res, err := Resolve(mode, ResolveOpts{Now: now, Prompter: prompter})
	if err != nil {
		t.Fatal(err)
	}
	if res.LookbackDays != 7 {
		t.Fatalf("expected default 7 days, got %d", res.LookbackDays)
	}
}

func TestResolveCustomPromptOverrideSkipsAsk(t *testing.T) {
	mode := mustFindBuiltin(t, BuiltinCustomPrompt)
	prompter := &scriptedPrompter{}
	// DaysOverride suppresses the lookback ask so this test isolates the
	// prompt-override behavior.
	res, err := Resolve(mode, ResolveOpts{
		Now:            time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC),
		Prompter:       prompter,
		DaysOverride:   7,
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

func TestResolveScopeDefaultsToInvolvedWhenUnset(t *testing.T) {
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
	if res.Scope != ScopeInvolved {
		t.Fatalf("expected default scope=involved, got %q", res.Scope)
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

func (e errorPrompter) Ask(string, string) (string, error)             { return "", e.err }
func (e errorPrompter) Select(string, []string, string) (string, error) { return "", e.err }

func mustFindBuiltin(t *testing.T, id string) ExecutionMode {
	t.Helper()
	m, ok := Find(BuiltinModes(), id)
	if !ok {
		t.Fatalf("builtin %q missing", id)
	}
	return m
}
