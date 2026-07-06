package model

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func readSettings(t *testing.T, dir string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, ".vscode", "settings.json"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("settings.json is not valid JSON: %v", err)
	}
	return m
}

func TestSyncVSCodeColorFresh(t *testing.T) {
	dir := t.TempDir()
	if err := SyncVSCodeColor(dir, "#123456", "#ffffff"); err != nil {
		t.Fatal(err)
	}
	cc, ok := readSettings(t, dir)["workbench.colorCustomizations"].(map[string]any)
	if !ok {
		t.Fatal("missing workbench.colorCustomizations")
	}
	if cc["titleBar.activeBackground"] != "#123456" || cc["activityBar.foreground"] != "#ffffff" {
		t.Fatalf("unexpected color keys: %+v", cc)
	}
	if len(cc) != 6 {
		t.Fatalf("want 6 color keys, got %d", len(cc))
	}
}

func TestSyncVSCodeColorPreservesAndOverwrites(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".vscode"), 0o755); err != nil {
		t.Fatal(err)
	}
	seed := map[string]any{
		"editor.fontSize": 14,
		"workbench.colorCustomizations": map[string]any{
			"titleBar.activeBackground": "#000000",
			"statusBar.background":      "#abcdef",
		},
	}
	b, _ := json.MarshalIndent(seed, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, ".vscode", "settings.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SyncVSCodeColor(dir, "#123456", "#000000"); err != nil {
		t.Fatal(err)
	}
	got := readSettings(t, dir)
	if got["editor.fontSize"].(float64) != 14 {
		t.Errorf("unrelated top-level key not preserved: %+v", got)
	}
	cc := got["workbench.colorCustomizations"].(map[string]any)
	if cc["statusBar.background"] != "#abcdef" {
		t.Errorf("unrelated color key not preserved: %+v", cc)
	}
	if cc["titleBar.activeBackground"] != "#123456" {
		t.Errorf("owned color key not overwritten: %+v", cc)
	}
}

func TestSyncVSCodeColorInvalidJSONUntouched(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".vscode"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".vscode", "settings.json")
	if err := os.WriteFile(path, []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SyncVSCodeColor(dir, "#123456", "#ffffff"); err == nil {
		t.Fatal("expected error on unparsable settings.json")
	}
	b, _ := os.ReadFile(path)
	if string(b) != "{ not json" {
		t.Fatalf("unparsable file was modified: %q", b)
	}
}

func TestSyncVSCodeColorGitExclude(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	if err := exec.Command("git", "-C", dir, "init").Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := SyncVSCodeColor(dir, "#123456", "#ffffff"); err != nil {
			t.Fatal(err)
		}
	}
	ex, err := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
	if err != nil {
		t.Fatalf("read info/exclude: %v", err)
	}
	n := strings.Count(string(ex), "/.vscode/settings.json")
	if n != 1 {
		t.Fatalf("want exactly one exclude entry, got %d:\n%s", n, ex)
	}
}
