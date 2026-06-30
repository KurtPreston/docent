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

func TestResolveBindHost(t *testing.T) {
	cases := []struct {
		name     string
		cfg      DaemonConfig
		flagHost string
		want     string
	}{
		{"flag overrides everything", DaemonConfig{Token: "x", BindHost: "1.2.3.4"}, "9.9.9.9", "9.9.9.9"},
		{"bindHost over token default", DaemonConfig{Token: "x", BindHost: "1.2.3.4"}, "", "1.2.3.4"},
		{"bindHost forces loopback even with token", DaemonConfig{Token: "x", BindHost: "127.0.0.1"}, "", "127.0.0.1"},
		{"token implies all-interfaces", DaemonConfig{Token: "x"}, "", "0.0.0.0"},
		{"no token defaults to loopback", DaemonConfig{}, "", "127.0.0.1"},
	}
	for _, tc := range cases {
		if got := ResolveBindHost(tc.cfg, tc.flagHost); got != tc.want {
			t.Errorf("%s: ResolveBindHost = %q, want %q", tc.name, got, tc.want)
		}
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
