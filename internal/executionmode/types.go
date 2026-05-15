// Package executionmode defines the declarative ExecutionMode interface that
// drives a slakkr run. An ExecutionMode bundles up the three properties that
// vary between built-in flows (daily-plan / recent-activity / custom-prompt)
// and any user-declared flows: a lookback window, an activity formatter, and
// an LLM prompt. Any property left unset is filled in interactively at
// runtime via the Resolve function.
//
// A fourth field, Scope, is a placeholder for an upcoming "how broadly should
// collectors gather data" effort. Today it only gates the existing
// FilterToSelf step in the CLI; collectors do not branch on it yet.
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
// Today only ScopeSelf has observable behavior (gates FilterToSelf in the
// CLI). The other values are placeholders for collector-side work.
type Scope string

const (
	ScopeUnset Scope = ""
	ScopeSelf  Scope = "self"
	ScopeRepo  Scope = "repo"
	ScopeAll   Scope = "all"
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

// Validate ensures the scope is one of the known placeholder values (the
// empty string is allowed and means "resolve to the default").
func (s Scope) Validate() error {
	switch s {
	case ScopeUnset, ScopeSelf, ScopeRepo, ScopeAll:
		return nil
	default:
		return fmt.Errorf("unknown scope %q (expected one of: %s, %s, %s)", string(s), ScopeSelf, ScopeRepo, ScopeAll)
	}
}
