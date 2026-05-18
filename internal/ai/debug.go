package ai

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// writeAIDebugLog writes one JSON entry under the run-log directory.
// The filename is fixed: `<providerSlug>-summary-<stage>.json`. Stage
// is one of: "request", "response", "error". The run-log dir is
// owned by the CLI's runlog.Run; retention is handled at the directory
// level (oldest run dirs are pruned), so there is no per-file
// retention here.
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
	stage = strings.TrimSpace(stage)
	if stage == "" {
		return
	}
	if err := os.MkdirAll(debugDir, 0o755); err != nil {
		return
	}
	filename := fmt.Sprintf("%s-summary-%s.json", providerSlug, stage)
	content, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(debugDir, filename), append(content, '\n'), 0o644)
}
