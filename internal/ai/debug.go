package ai

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// aiDebugLogRetention caps how many request/response files we keep per
// provider inside userdata/.cache/ai-debug. Older entries are pruned in
// modification-time order on every write.
const aiDebugLogRetention = 20

// writeAIDebugLog appends one JSON entry to the shared ai-debug directory.
// providerSlug becomes the filename prefix (e.g. "ollama", "cursor") so
// per-provider retention windows don't interfere with each other.
//
// Errors are intentionally swallowed: debug logging must never fail a
// real request.
func writeAIDebugLog(debugDir, providerSlug, stage string, payload map[string]any) {
	if strings.TrimSpace(debugDir) == "" {
		return
	}
	if strings.TrimSpace(providerSlug) == "" {
		return
	}
	if err := os.MkdirAll(debugDir, 0o755); err != nil {
		return
	}
	filename := fmt.Sprintf("%s-%s-%s.json",
		providerSlug,
		time.Now().UTC().Format("20060102T150405.000000000Z"),
		stage,
	)
	content, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(debugDir, filename), append(content, '\n'), 0o644)
	pruneAIDebugLogs(debugDir, providerSlug, aiDebugLogRetention)
}

// pruneAIDebugLogs keeps the `keep` most recent JSON files whose names
// start with `providerSlug-` and end in `.json`, deleting the rest. Other
// providers' logs in the same directory are left untouched.
func pruneAIDebugLogs(debugDir, providerSlug string, keep int) {
	if keep <= 0 {
		return
	}
	entries, err := os.ReadDir(debugDir)
	if err != nil {
		return
	}
	prefix := providerSlug + "-"
	type logFile struct {
		path    string
		modTime time.Time
	}
	files := make([]logFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, logFile{
			path:    filepath.Join(debugDir, name),
			modTime: info.ModTime(),
		})
	}
	if len(files) <= keep {
		return
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.After(files[j].modTime)
	})
	for _, stale := range files[keep:] {
		_ = os.Remove(stale.path)
	}
}
