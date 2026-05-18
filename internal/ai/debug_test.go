package ai

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAIDebugLogUsesFixedFilename(t *testing.T) {
	dir := t.TempDir()
	writeAIDebugLog(dir, "cursor", "request", map[string]any{"prompt": "hi"})
	writeAIDebugLog(dir, "cursor", "response", map[string]any{"out": "hi"})

	for _, name := range []string{"cursor-summary-request.json", "cursor-summary-response.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("expected %s: %v", name, err)
		}
	}

	writeAIDebugLog(dir, "ollama", "error", map[string]any{"error": "boom"})
	if _, err := os.Stat(filepath.Join(dir, "ollama-summary-error.json")); err != nil {
		t.Errorf("missing ollama error file: %v", err)
	}
}

func TestWriteAIDebugLogIgnoresEmptyArgs(t *testing.T) {
	dir := t.TempDir()
	writeAIDebugLog("", "cursor", "request", nil)
	writeAIDebugLog(dir, "", "request", nil)
	writeAIDebugLog(dir, "cursor", "", nil)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected no files written, got %d", len(entries))
	}
}
