package goals

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// Goal is one long-lived objective the developer wants to stay focused on.
type Goal struct {
	ID          string   `yaml:"id" json:"id"`
	Title       string   `yaml:"title" json:"title"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	Repos       []string `yaml:"repos,omitempty" json:"repos,omitempty"`
	Labels      []string `yaml:"labels,omitempty" json:"labels,omitempty"`
	TicketKeys  []string `yaml:"ticket_keys,omitempty" json:"ticketKeys,omitempty"`
	Active      *bool    `yaml:"active,omitempty" json:"active,omitempty"`
}

// File is the on-disk goals.yaml shape.
type File struct {
	Goals []Goal `yaml:"goals,omitempty" json:"goals,omitempty"`
}

// IsActive reports whether the goal is active (default true).
func (g Goal) IsActive() bool {
	return g.Active == nil || *g.Active
}

// Path returns the default goals.yaml path under configDir.
func Path(configDir string) string {
	return filepath.Join(configDir, "goals.yaml")
}

// Load reads goals.yaml. Missing file yields an empty File.
func Load(path string) (File, error) {
	var f File
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return f, nil
		}
		return f, err
	}
	return LoadFromBytes(b)
}

// LoadFromBytes parses goals YAML from memory.
func LoadFromBytes(b []byte) (File, error) {
	var f File
	if len(strings.TrimSpace(string(b))) == 0 {
		return f, nil
	}
	if err := yaml.Unmarshal(b, &f); err != nil {
		return f, err
	}
	if err := Validate(f); err != nil {
		return f, err
	}
	return f, nil
}

// Validate checks goal IDs and required fields.
func Validate(f File) error {
	var problems []string
	seen := map[string]bool{}
	for i, g := range f.Goals {
		path := fmt.Sprintf("goals[%d]", i)
		id := strings.TrimSpace(g.ID)
		if id == "" {
			problems = append(problems, path+".id is required")
		} else if !idPattern.MatchString(id) {
			problems = append(problems, fmt.Sprintf("%s.id %q is invalid", path, id))
		} else if seen[id] {
			problems = append(problems, fmt.Sprintf("%s.id is duplicated (%q)", path, id))
		} else {
			seen[id] = true
		}
		if strings.TrimSpace(g.Title) == "" {
			problems = append(problems, path+".title is required")
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(problems, "; "))
}

// ActiveGoals returns goals with Active unset or true.
func ActiveGoals(f File) []Goal {
	var out []Goal
	for _, g := range f.Goals {
		if g.IsActive() {
			out = append(out, g)
		}
	}
	return out
}

// AlignmentPrompt builds an AI instruction for a weekly goal check-in.
func AlignmentPrompt(goals []Goal) string {
	var b strings.Builder
	b.WriteString("You are reviewing a developer's week against their stated goals.\n")
	b.WriteString("Goals:\n")
	for _, g := range goals {
		b.WriteString(fmt.Sprintf("- %s (%s)", g.Title, g.ID))
		if g.Description != "" {
			b.WriteString(": ")
			b.WriteString(g.Description)
		}
		b.WriteByte('\n')
	}
	b.WriteString("\nUsing the activity below, write a Markdown report with:\n")
	b.WriteString("1. How well this week's work aligned with each goal\n")
	b.WriteString("2. What slid or got neglected\n")
	b.WriteString("3. Concrete suggestions for next week to stay focused on the goals\n")
	b.WriteString("Be succinct and practical.\n")
	return b.String()
}
