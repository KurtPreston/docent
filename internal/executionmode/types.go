// Package executionmode defines the declarative ExecutionMode interface that
// drives a slakkr run. An ExecutionMode bundles up the four properties that
// vary between built-in flows (daily-plan / recent-activity / custom-prompt)
// and any user-declared flows: a lookback window, an activity formatter, an
// LLM prompt, and a collection Scope. Any property left unset is filled in
// interactively at runtime via the Resolve function.
//
// Scope describes how broadly each collector should gather data for a run.
// All three values are honored by the collectors themselves; see Scope below
// for the canonical semantics.
package executionmode

import (
	"fmt"
	"regexp"
	"strings"
)

// Built-in mode IDs are referenced by providers that still need to switch on
// the mode (today: the rule-based deterministic renderer in internal/ai).
const (
	BuiltinDailyPlan      = "daily-plan"
	BuiltinRecentActivity = "recent-activity"
	BuiltinCustomPrompt   = "custom-prompt"
)

// Lookback kinds (extensible — add "duration", "previous-week", … without
// breaking existing YAML).
const (
	LookbackKindDays            = "days"
	LookbackKindPreviousWeekday = "previous-weekday"
)

// Scope describes how broadly a collector should gather data for a run.
//
//   - ScopeSelf: only the configured user's own activity.
//   - ScopeInvolved: the user's activity plus activity adjacent to their
//     work (PRs they review, issues they're assigned, branches they've
//     touched, etc.). This is the default for the built-in execution modes.
//   - ScopeAll: every signal the collector can reasonably surface within
//     the time window, broadened via the new `followed_repos` /
//     `followed_projects` directive config.
type Scope string

const (
	ScopeUnset    Scope = ""
	ScopeSelf     Scope = "self"
	ScopeInvolved Scope = "involved"
	ScopeAll      Scope = "all"
)

// ExecutionMode is one declaratively-described slakkr run shape. All fields
// are optional except ID; anything omitted is filled in at runtime by
// Resolve.
type ExecutionMode struct {
	ID        string    `yaml:"id"`
	Name      string    `yaml:"name,omitempty"`
	Lookback  *Lookback `yaml:"lookback,omitempty"`
	Formatter string    `yaml:"formatter,omitempty"`
	Prompt    *Prompt   `yaml:"prompt,omitempty"`
	Scope     Scope     `yaml:"scope,omitempty"`
}

// Lookback is a tagged union over the supported lookback strategies.
type Lookback struct {
	Kind string `yaml:"kind"`
	Days int    `yaml:"days,omitempty"`
}

// Prompt holds the LLM instruction text for a mode. This PR does not do
// templating; the string is passed to the provider verbatim.
type Prompt struct {
	Instruction string `yaml:"instruction"`
}

// idPattern matches the lowercase-kebab IDs used elsewhere in the schema.
var idPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// Display returns the human label for the interactive menu, falling back to
// the ID when Name is empty.
func (m ExecutionMode) Display() string {
	if strings.TrimSpace(m.Name) != "" {
		return m.Name
	}
	return m.ID
}

// Validate checks the structural invariants of a single mode: ID shape,
// lookback coherence, scope enum membership. Field-omitted cases are valid
// (they get prompted at runtime).
func (m ExecutionMode) Validate() error {
	id := strings.TrimSpace(m.ID)
	if id == "" {
		return fmt.Errorf("id is required")
	}
	if !idPattern.MatchString(id) {
		return fmt.Errorf("id %q must match %s", id, idPattern.String())
	}
	if m.Lookback != nil {
		if err := m.Lookback.Validate(); err != nil {
			return fmt.Errorf("lookback: %w", err)
		}
	}
	if m.Prompt != nil && strings.TrimSpace(m.Prompt.Instruction) == "" {
		return fmt.Errorf("prompt.instruction must be non-empty when prompt is set")
	}
	if err := m.Scope.Validate(); err != nil {
		return err
	}
	return nil
}

// Validate checks lookback kind and the days field's coherence with it.
func (l Lookback) Validate() error {
	switch l.Kind {
	case LookbackKindDays:
		if l.Days < 1 {
			return fmt.Errorf("kind=days requires days >= 1 (got %d)", l.Days)
		}
	case LookbackKindPreviousWeekday:
		if l.Days != 0 {
			return fmt.Errorf("kind=previous-weekday does not accept days (got %d)", l.Days)
		}
	case "":
		return fmt.Errorf("kind is required")
	default:
		return fmt.Errorf("unknown kind %q (expected %s or %s)", l.Kind, LookbackKindDays, LookbackKindPreviousWeekday)
	}
	return nil
}

// Validate ensures the scope is one of the known values (the empty string
// is allowed and means "resolve to the default").
func (s Scope) Validate() error {
	switch s {
	case ScopeUnset, ScopeSelf, ScopeInvolved, ScopeAll:
		return nil
	default:
		return fmt.Errorf("unknown scope %q (expected one of: %s, %s, %s)", string(s), ScopeSelf, ScopeInvolved, ScopeAll)
	}
}
