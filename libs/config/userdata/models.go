package userdata

import (
	"errors"
	"fmt"
	"strings"

	"github.com/kurt/slakkr-ai/libs/config/executionmode"
)

const DefaultDir = "userdata"

// ConfigFile is the single userdata configuration file: AI + directives +
// optional user-declared execution modes.
type ConfigFile struct {
	AI             AIConfig                      `yaml:"ai,omitempty"`
	Directives     []Directive                   `yaml:"directives,omitempty"`
	ExecutionModes []executionmode.ExecutionMode `yaml:"execution_modes,omitempty"`
}

type Directive struct {
	ID             string            `yaml:"id"`
	Name           string            `yaml:"name"`
	Collector      string            `yaml:"collector"`
	Enabled        bool              `yaml:"enabled"`
	CodeHome       string            `yaml:"code_home,omitempty"`  // local-git: parent dir of immediate child repos when paths empty
	Paths          []string          `yaml:"paths,omitempty"`      // local-git: explicit repo roots; if empty, use code_home scan
	Target         map[string]string `yaml:"target,omitempty"`
	Config         map[string]string `yaml:"config,omitempty"`
	CredentialRefs map[string]string `yaml:"credential_refs,omitempty"`
}

type AIConfig struct {
	Provider          string           `yaml:"provider,omitempty"`
	ActivityFormatter string           `yaml:"activity_formatter,omitempty"`
	Ollama            AIProviderOllama `yaml:"ollama,omitempty"`
	Cursor            AIProviderCursor `yaml:"cursor,omitempty"`
	Claude            AIProviderClaude `yaml:"claude,omitempty"`
}

type AIProviderOllama struct {
	BaseURL string `yaml:"base_url,omitempty"`
	Model   string `yaml:"model,omitempty"`
}

type AIProviderCursor struct {
	Command string   `yaml:"command,omitempty"`
	Args    []string `yaml:"args,omitempty"`
}

type AIProviderClaude struct {
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
	seen := map[string]bool{}
	for i, d := range f.Directives {
		path := fmt.Sprintf("directives[%d]", i)
		if seen[d.ID] {
			problems = append(problems, fmt.Sprintf("%s.id is duplicated (%q)", path, d.ID))
		}
		if d.ID != "" {
			seen[d.ID] = true
		}
	}
	return validationResult(problems)
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
