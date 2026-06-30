package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_usesConfigDirForDirectives(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(`directives:
  - id: one
    name: One
    collector: webhook
    enabled: true
`), 0o644); err != nil {
		t.Fatal(err)
	}
	docentd := filepath.Join(dir, "docentd.yaml")
	if err := os.WriteFile(docentd, []byte("configDir: "+dir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(docentd)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConfigDir != dir {
		t.Fatalf("configDir=%q", cfg.ConfigDir)
	}
	if len(cfg.Directives) != 1 || cfg.Directives[0].ID != "one" {
		t.Fatalf("directives=%#v", cfg.Directives)
	}
	if cfg.AI.Provider != "" {
		t.Fatalf("ai=%#v", cfg.AI)
	}
}

func TestLoad_userdataDirAlias(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(`directives:
  - id: legacy
    name: Legacy
    collector: webhook
    enabled: true
`), 0o644); err != nil {
		t.Fatal(err)
	}
	docentd := filepath.Join(dir, "docentd.yaml")
	if err := os.WriteFile(docentd, []byte("userdataDir: "+dir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(docentd)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConfigDir != dir {
		t.Fatalf("configDir=%q", cfg.ConfigDir)
	}
	if len(cfg.Directives) != 1 || cfg.Directives[0].ID != "legacy" {
		t.Fatalf("directives=%#v", cfg.Directives)
	}
}

func TestLoad_optionalAI(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(`ai:
  provider: rule-based
directives:
  - id: one
    name: One
    collector: webhook
    enabled: true
`), 0o644); err != nil {
		t.Fatal(err)
	}
	docentd := filepath.Join(dir, "docentd.yaml")
	if err := os.WriteFile(docentd, []byte("configDir: "+dir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(docentd)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AI.Provider != "rule-based" {
		t.Fatalf("ai=%#v", cfg.AI)
	}
}
