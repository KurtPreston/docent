package executionmode

import (
	"fmt"
	"strings"
)

// Load merges built-in modes with user-declared modes from config.yaml.
// User-declared modes that share an ID with a built-in *override* the
// built-in in place (preserving menu position); brand-new IDs are appended.
// Returns a fresh slice; inputs are not mutated.
//
// Validation errors are accumulated and returned as a single error listing
// every problem so the user can fix them in one pass.
func Load(builtins []ExecutionMode, user []ExecutionMode) ([]ExecutionMode, error) {
	var problems []string

	out := make([]ExecutionMode, len(builtins))
	indexByID := make(map[string]int, len(builtins))
	for i, m := range builtins {
		if err := m.Validate(); err != nil {
			problems = append(problems, fmt.Sprintf("builtin %q: %v", m.ID, err))
			continue
		}
		out[i] = m
		indexByID[m.ID] = i
	}

	seenUser := map[string]bool{}
	for i, m := range user {
		label := strings.TrimSpace(m.ID)
		if label == "" {
			label = fmt.Sprintf("execution_modes[%d]", i)
		}
		if err := m.Validate(); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", label, err))
			continue
		}
		if seenUser[m.ID] {
			problems = append(problems, fmt.Sprintf("%s: duplicate execution mode id within user config", label))
			continue
		}
		seenUser[m.ID] = true
		if idx, ok := indexByID[m.ID]; ok {
			out[idx] = m
			continue
		}
		out = append(out, m)
		indexByID[m.ID] = len(out) - 1
	}

	if len(problems) > 0 {
		return nil, fmt.Errorf("invalid execution_modes:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return out, nil
}

// Find returns the mode with the given ID and true, or a zero value and
// false. Callers should use the returned mode for Resolve.
func Find(modes []ExecutionMode, id string) (ExecutionMode, bool) {
	for _, m := range modes {
		if m.ID == id {
			return m, true
		}
	}
	return ExecutionMode{}, false
}
