package userdata

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const DefaultDir = "userdata"

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// ConfigFile is the single userdata configuration file: AI + optional code_home + directives.
type ConfigFile struct {
	CodeHome   string      `yaml:"code_home,omitempty"`
	AI         AIConfig    `yaml:"ai,omitempty"`
	Directives []Directive `yaml:"directives,omitempty"`
}

type Directive struct {
	ID             string            `yaml:"id"`
	Name           string            `yaml:"name"`
	Collector      string            `yaml:"collector"`
	Enabled        bool              `yaml:"enabled"`
	ProjectID      string            `yaml:"project_id,omitempty"` // optional label for grouping in reports
	Paths          []string          `yaml:"paths,omitempty"`      // local-git: explicit repo paths; if empty, scan code_home
	Target         map[string]string `yaml:"target,omitempty"`
	Config         map[string]string `yaml:"config,omitempty"`
	CredentialRefs map[string]string `yaml:"credential_refs,omitempty"`
}

type AIConfig struct {
	Provider string           `yaml:"provider,omitempty"`
	Ollama   AIProviderOllama `yaml:"ollama,omitempty"`
	Cursor   AIProviderCursor `yaml:"cursor,omitempty"`
}

type AIProviderOllama struct {
	BaseURL string `yaml:"base_url,omitempty"`
	Model   string `yaml:"model,omitempty"`
}

type AIProviderCursor struct {
	Command string   `yaml:"command,omitempty"`
	Args    []string `yaml:"args,omitempty"`
}

type ValidationError struct {
	Problems []string
}

func (e ValidationError) Error() string {
	return "validation failed: " + strings.Join(e.Problems, "; ")
}

func (f ConfigFile) Validate() error {
	var problems []string
	if f.AI.Provider != "" && !validAIProvider(f.AI.Provider) {
		problems = append(problems, "ai.provider is invalid")
	}
	seen := map[string]bool{}
	for i, d := range f.Directives {
		path := fmt.Sprintf("directives[%d]", i)
		problems = append(problems, validateID(path+".id", d.ID)...)
		if d.Name == "" {
			problems = append(problems, path+".name is required")
		}
		if d.Collector == "" {
			problems = append(problems, path+".collector is required")
		}
		if seen[d.ID] {
			problems = append(problems, path+".id is duplicated")
		}
		seen[d.ID] = true
	}
	return validationResult(problems)
}

func validAIProvider(p string) bool {
	switch strings.ToLower(strings.ReplaceAll(strings.TrimSpace(p), "_", "-")) {
	case "ollama", "cursor", "rule-based":
		return true
	default:
		return false
	}
}

func validateID(field, id string) []string {
	if id == "" {
		return []string{field + " is required"}
	}
	if !idPattern.MatchString(id) {
		return []string{field + " must match " + idPattern.String()}
	}
	return nil
}

func validationResult(problems []string) error {
	if len(problems) == 0 {
		return nil
	}
	return ValidationError{Problems: problems}
}

func IsValidationError(err error) bool {
	var validationErr ValidationError
	return errors.As(err, &validationErr)
}
