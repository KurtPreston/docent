package userdata

import (
	"testing"
	"time"

	"github.com/KurtPreston/docent/libs/config/configschema"
	"gopkg.in/yaml.v3"
)

func TestSessionsConfigTTLAndRetention(t *testing.T) {
	var def SessionsConfig
	if got := def.TTL(); got != DefaultHeartbeatInterval*time.Duration(DefaultMissedHeartbeats) {
		t.Errorf("default TTL = %s, want %s", got, DefaultHeartbeatInterval*time.Duration(DefaultMissedHeartbeats))
	}
	if got := def.RetentionDuration(); got != DefaultIdleRetention {
		t.Errorf("default retention = %s, want %s", got, DefaultIdleRetention)
	}
	// Retention is honored when set and longer than the liveness TTL.
	c := SessionsConfig{IdleRetention: "45m"}
	if got := c.RetentionDuration(); got != 45*time.Minute {
		t.Errorf("retention = %s, want 45m", got)
	}
	// Retention can never be shorter than the liveness TTL.
	short := SessionsConfig{HeartbeatInterval: "1m", MissedHeartbeats: 5, IdleRetention: "10s"}
	if got := short.RetentionDuration(); got != short.TTL() {
		t.Errorf("retention %s should floor at TTL %s", got, short.TTL())
	}
}

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

func TestOpenTriggerCursorWriteColorEnabled(t *testing.T) {
	var c OpenTriggerCursor
	if !c.WriteColorEnabled() {
		t.Fatal("nil WriteColor should default to true")
	}
	no := false
	cNo := OpenTriggerCursor{WriteColor: &no}
	if cNo.WriteColorEnabled() {
		t.Fatal("WriteColor false should disable color write")
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
