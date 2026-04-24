package recipes

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/kurt/slakkr-ai/internal/userdata"
	"gopkg.in/yaml.v3"
)

type Recipe struct {
	ID             string          `yaml:"id"`
	Name           string          `yaml:"name"`
	Description    string          `yaml:"description,omitempty"`
	Collector      string          `yaml:"collector"`
	RequiredConfig []ConfigField   `yaml:"required_config,omitempty"`
	RequiredTarget []TargetField   `yaml:"required_target,omitempty"`
	Defaults       RecipeDefaults  `yaml:"defaults,omitempty"`
	Examples       []RecipeExample `yaml:"examples,omitempty"`
}

type ConfigField struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description,omitempty"`
	Secret      bool   `yaml:"secret,omitempty"`
	Required    bool   `yaml:"required,omitempty"`
}

type TargetField struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description,omitempty"`
	Required    bool   `yaml:"required,omitempty"`
}

type RecipeDefaults struct {
	Schedule      string            `yaml:"schedule,omitempty"`
	Config        map[string]string `yaml:"config,omitempty"`
	Target        map[string]string `yaml:"target,omitempty"`
	SummaryPrompt string            `yaml:"summary_prompt,omitempty"`
}

type RecipeExample struct {
	Name   string            `yaml:"name"`
	Config map[string]string `yaml:"config,omitempty"`
	Target map[string]string `yaml:"target,omitempty"`
}

type InstantiateInput struct {
	ID             string
	Name           string
	ProjectID      string
	Config         map[string]string
	Target         map[string]string
	CredentialRefs map[string]string
	Enabled        bool
}

func LoadDir(root string) ([]Recipe, error) {
	var loaded []Recipe
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}
		recipe, err := LoadFile(path)
		if err != nil {
			return err
		}
		loaded = append(loaded, recipe)
		return nil
	})
	if os.IsNotExist(err) {
		return nil, nil
	}
	return loaded, err
}

func LoadFile(path string) (Recipe, error) {
	var recipe Recipe
	content, err := os.ReadFile(path)
	if err != nil {
		return recipe, err
	}
	if err := yaml.Unmarshal(content, &recipe); err != nil {
		return recipe, fmt.Errorf("%s: %w", path, err)
	}
	if err := recipe.Validate(); err != nil {
		return recipe, fmt.Errorf("%s: %w", path, err)
	}
	return recipe, nil
}

func (r Recipe) Validate() error {
	var problems []string
	if r.ID == "" {
		problems = append(problems, "id is required")
	}
	if r.Name == "" {
		problems = append(problems, "name is required")
	}
	if r.Collector == "" {
		problems = append(problems, "collector is required")
	}
	if len(problems) > 0 {
		return fmt.Errorf(strings.Join(problems, "; "))
	}
	return nil
}

func (r Recipe) Instantiate(input InstantiateInput) (userdata.Directive, error) {
	config := mergeMaps(r.Defaults.Config, input.Config)
	target := mergeMaps(r.Defaults.Target, input.Target)
	var problems []string
	for _, field := range r.RequiredConfig {
		if field.Required && config[field.Name] == "" && input.CredentialRefs[field.Name] == "" {
			problems = append(problems, "missing config "+field.Name)
		}
	}
	for _, field := range r.RequiredTarget {
		if field.Required && target[field.Name] == "" {
			problems = append(problems, "missing target "+field.Name)
		}
	}
	if len(problems) > 0 {
		return userdata.Directive{}, fmt.Errorf(strings.Join(problems, "; "))
	}
	id := input.ID
	if id == "" {
		id = r.ID
	}
	name := input.Name
	if name == "" {
		name = r.Name
	}
	return userdata.Directive{
		ID:             id,
		RecipeID:       r.ID,
		Name:           name,
		Collector:      r.Collector,
		Enabled:        input.Enabled,
		Schedule:       r.Defaults.Schedule,
		ProjectID:      input.ProjectID,
		Target:         target,
		Config:         config,
		CredentialRefs: input.CredentialRefs,
		SummaryPrompt:  r.Defaults.SummaryPrompt,
	}, nil
}

func mergeMaps(base, override map[string]string) map[string]string {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	merged := map[string]string{}
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range override {
		merged[key] = value
	}
	return merged
}
