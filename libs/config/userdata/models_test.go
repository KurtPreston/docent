package userdata

import (
	"testing"

	"github.com/KurtPreston/docent/libs/config/configschema"
	"gopkg.in/yaml.v3"
)

func TestConfigValidateDirectives(t *testing.T) {
	cfg := ConfigFile{
		AI: AIConfig{Provider: "ollama"},
		Directives: []Directive{{
			ID:        "gitea-1",
			Name:      "Gitea",
			Collector: "gitea",
			Enabled:   true,
		}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid: %v", err)
	}
	cfg.Directives = append(cfg.Directives, Directive{ID: "gitea-1", Name: "Dup", Collector: "gitea"})
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected duplicate id")
	}
}

func TestAIProfilesAgainstSchema(t *testing.T) {
	for _, p := range []string{"ollama", "cursor", "claude", "rule-based"} {
		cfg := ConfigFile{AI: AIConfig{Provider: p}}
		if p == "ollama" {
			cfg.AI.Ollama.BaseURL = "http://127.0.0.1:11434"
			cfg.AI.Ollama.Model = "llama3"
		}
		if p == "cursor" {
			cfg.AI.Cursor.Command = "cursor-agent"
		}
		if p == "claude" {
			cfg.AI.Claude.Command = "claude"
		}
		raw, err := yaml.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := configschema.ValidateYAML(raw); err != nil {
			t.Fatalf("%q: %v (%v)", p, err, configschema.ValidationProblems(err))
		}
	}
	bad := ConfigFile{AI: AIConfig{Provider: "unknown"}}
	raw, err := yaml.Marshal(bad)
	if err != nil {
		t.Fatal(err)
	}
	if err := configschema.ValidateYAML(raw); err == nil {
		t.Fatal("expected schema rejection for unknown ai.provider")
	}
}
