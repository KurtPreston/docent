package userdata

import "testing"

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

func TestValidAIProvider(t *testing.T) {
	for _, p := range []string{"ollama", "cursor", "rule-based"} {
		cfg := ConfigFile{AI: AIConfig{Provider: p}}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("%q: %v", p, err)
		}
	}
	bad := ConfigFile{AI: AIConfig{Provider: "unknown"}}
	if err := bad.Validate(); err == nil {
		t.Fatal("expected invalid provider")
	}
}
