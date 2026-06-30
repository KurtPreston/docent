package executionmode

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Prompter is the minimal interactive surface Resolve needs to ask the user
// for missing properties. cli.StdioPrompter satisfies it.
//
// Ask requests a free-form string (with an optional default rendered as
// "[default]"). Select renders a menu of options with one of them
// pre-highlighted as the default; the returned string MUST be one of the
// provided options (StdioPrompter uses survey.Select under the hood, which
// enforces this).
type Prompter interface {
	Ask(prompt, defaultValue string) (string, error)
	Select(prompt string, options []string, defaultValue string) (string, error)
}

// ResolveOpts carries everything Resolve needs that isn't part of the
// ExecutionMode itself: the clock, CLI flag overrides, and (optionally) a
// Prompter for interactive fills. When Prompter is nil and a property is
// unset, Resolve returns an error explaining what's missing.
type ResolveOpts struct {
	Now      time.Time
	Prompter Prompter

	// DaysOverride is the --days CLI flag. When > 0, the resolved lookback
	// is forced to {kind: days, days: DaysOverride} regardless of what the
	// mode declared. This is the user's per-run escape hatch.
	DaysOverride int

	// PromptOverride is the --prompt / --prompt-file payload. When
	// non-empty it replaces mode.Prompt.Instruction for this run.
	PromptOverride string

	// ConfigActivityFormatter is the ai.activity_formatter value from
	// userdata/config.yaml. Used as the fallback when the mode does not
	// override the formatter. Empty falls through to the AI package's own
	// default.
	ConfigActivityFormatter string
}

// ResolvedRun is the fully-resolved description of a single slakkr run.
// Every field is concrete (no more "ask the user" remaining).
type ResolvedRun struct {
	ModeID       string
	ModeName     string
	Since        time.Time
	Until        time.Time
	LookbackDays int    // 0 when the lookback is not days-based (e.g. previous-weekday)
	Formatter    string // resolved formatter name; "" => provider/global default
	Instruction  string // LLM instruction (verbatim; provider appends activity body)
	Scope        Scope
	// Collectors restricts collection to these collector types (by their
	// directive `collector` value). Empty means "all enabled directives".
	Collectors []string
}

// Resolve produces a ResolvedRun from a mode + per-run options. It is
// responsible for asking the user via opts.Prompter for any property the
// mode left unspecified.
func Resolve(mode ExecutionMode, opts ResolveOpts) (ResolvedRun, error) {
	if err := mode.Validate(); err != nil {
		return ResolvedRun{}, err
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}

	since, days, err := resolveLookback(mode.Lookback, opts, now)
	if err != nil {
		return ResolvedRun{}, err
	}

	scope, err := resolveScope(mode.Scope, opts)
	if err != nil {
		return ResolvedRun{}, err
	}

	instruction, err := resolveInstruction(mode.Prompt, opts)
	if err != nil {
		return ResolvedRun{}, err
	}

	formatter := strings.TrimSpace(mode.Formatter)
	if formatter == "" {
		formatter = strings.TrimSpace(opts.ConfigActivityFormatter)
	}

	var collectorTypes []string
	for _, c := range mode.Collectors {
		if c = strings.TrimSpace(c); c != "" {
			collectorTypes = append(collectorTypes, c)
		}
	}

	return ResolvedRun{
		ModeID:       mode.ID,
		ModeName:     mode.Display(),
		Since:        since,
		Until:        now,
		LookbackDays: days,
		Formatter:    formatter,
		Instruction:  instruction,
		Scope:        scope,
		Collectors:   collectorTypes,
	}, nil
}

func resolveLookback(l *Lookback, opts ResolveOpts, now time.Time) (time.Time, int, error) {
	// --days N is a hard override: it always wins, regardless of what the
	// mode declared. This gives the user a per-run escape hatch (e.g.
	// running daily-plan against a 14-day window for catch-up).
	if opts.DaysOverride > 0 {
		return lookbackSinceDays(now, opts.DaysOverride), opts.DaysOverride, nil
	}

	if l == nil {
		days, err := askDays(opts.Prompter, 7)
		if err != nil {
			return time.Time{}, 0, err
		}
		return lookbackSinceDays(now, days), days, nil
	}

	switch l.Kind {
	case LookbackKindDays:
		return lookbackSinceDays(now, l.Days), l.Days, nil
	case LookbackKindPreviousWeekday:
		return previousWeekdayStart(now), 0, nil
	default:
		return time.Time{}, 0, fmt.Errorf("unknown lookback kind %q", l.Kind)
	}
}

// resolveScope picks the collection scope for a run. When the mode pinned
// a scope (anything but ScopeUnset) we honor it verbatim. When it left the
// scope unset we prompt the user via opts.Prompter — defaulting the cursor
// to ScopeInvolved — so the interactive flow can broaden or narrow data
// collection per run. Non-interactive callers (Prompter == nil) silently
// inherit ScopeInvolved, preserving the historical default.
func resolveScope(declared Scope, opts ResolveOpts) (Scope, error) {
	if declared != ScopeUnset {
		return declared, nil
	}
	if opts.Prompter == nil {
		return ScopeInvolved, nil
	}
	labels, byLabel, defaultLabel := scopeMenu(ScopeInvolved)
	pick, err := opts.Prompter.Select("Scope", labels, defaultLabel)
	if err != nil {
		return "", err
	}
	pick = strings.TrimSpace(pick)
	if s, ok := byLabel[pick]; ok {
		return s, nil
	}
	// Fallback for prompters (tests, custom scripting) that return a bare
	// scope string rather than the descriptive label.
	if raw := Scope(pick); raw.Validate() == nil && raw != ScopeUnset {
		return raw, nil
	}
	return "", fmt.Errorf("invalid scope choice %q", pick)
}

// scopeMenu returns the labels shown in the interactive scope picker, a
// lookup table from label back to Scope, and the label corresponding to
// the requested default. Labels embed a short description so users who
// haven't read the README still get a sense of what each option collects.
func scopeMenu(defaultScope Scope) (labels []string, byLabel map[string]Scope, defaultLabel string) {
	items := []struct {
		scope Scope
		desc  string
	}{
		{ScopeSelf, "only my own activity"},
		{ScopeInvolved, "my activity plus adjacent context (reviews, mentions, assignments)"},
		{ScopeAll, "every signal in the window (broadened by followed_repos / followed_projects)"},
	}
	labels = make([]string, 0, len(items))
	byLabel = make(map[string]Scope, len(items))
	for _, it := range items {
		label := fmt.Sprintf("%s — %s", it.scope, it.desc)
		labels = append(labels, label)
		byLabel[label] = it.scope
		if it.scope == defaultScope {
			defaultLabel = label
		}
	}
	return labels, byLabel, defaultLabel
}

func resolveInstruction(p *Prompt, opts ResolveOpts) (string, error) {
	if strings.TrimSpace(opts.PromptOverride) != "" {
		return strings.TrimSpace(opts.PromptOverride), nil
	}
	if p != nil {
		return strings.TrimSpace(p.Instruction), nil
	}
	if opts.Prompter == nil {
		return "", fmt.Errorf("this mode requires a prompt; pass --prompt, --prompt-file, or run interactively")
	}
	value, err := opts.Prompter.Ask("Your prompt for the model", "")
	if err != nil {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("prompt is empty")
	}
	return value, nil
}

func askDays(p Prompter, defaultDays int) (int, error) {
	if p == nil {
		return defaultDays, nil
	}
	value, err := p.Ask("Lookback days", strconv.Itoa(defaultDays))
	if err != nil {
		return 0, err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultDays, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("lookback days must be a positive integer")
	}
	return n, nil
}

// lookbackSinceDays mirrors the historical cli.lookbackSince helper: at
// least 1 day, expressed as N * 24h before now.
func lookbackSinceDays(now time.Time, days int) time.Time {
	if days < 1 {
		days = 1
	}
	return now.Add(-time.Duration(days) * 24 * time.Hour)
}

// previousWeekdayStart returns midnight local time on the previous "work
// day" for planning, matching the historical cli.PreviousWeekdayStart
// semantics: Mon → Fri 00:00; Sat/Sun → Fri 00:00; Tue–Fri → yesterday 00:00.
func previousWeekdayStart(now time.Time) time.Time {
	loc := now.Location()
	local := now.In(loc)
	y, m, d := local.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, loc)
	switch today.Weekday() {
	case time.Monday:
		return today.AddDate(0, 0, -3)
	case time.Sunday:
		return today.AddDate(0, 0, -2)
	case time.Saturday:
		return today.AddDate(0, 0, -1)
	default:
		return today.AddDate(0, 0, -1)
	}
}
