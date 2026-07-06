package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// vscodeColorCustomizations returns the workbench.colorCustomizations keys
// docent owns. They mirror grove's editor theming so a repo opened by either
// tool looks the same. These keys are overwritten authoritatively on each sync
// (a work item's color can change), while any other keys in the file are left
// untouched.
func vscodeColorCustomizations(hex, fg string) map[string]any {
	return map[string]any{
		"titleBar.activeBackground":   hex,
		"titleBar.activeForeground":   fg,
		"titleBar.inactiveBackground": hex,
		"titleBar.inactiveForeground": fg,
		"activityBar.background":      hex,
		"activityBar.foreground":      fg,
	}
}

// SyncVSCodeColor writes hex/fg into <dir>/.vscode/settings.json under
// workbench.colorCustomizations (shared by VS Code and Cursor), overwriting the
// docent-owned color keys while preserving any other settings, then keeps the
// generated file out of `git status` via the repo's info/exclude.
//
// The color input is the work item's already-assigned color (see ColorForName /
// ForegroundForHex); this function only syncs it to disk and is intentionally
// decoupled from how the color was derived, so a future user-override path just
// changes the input. A settings.json that can't be parsed is left untouched and
// an error is returned rather than clobbering it.
func SyncVSCodeColor(dir, hex, fg string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return fmt.Errorf("model: SyncVSCodeColor requires a directory")
	}
	vsdir := filepath.Join(dir, ".vscode")
	if err := os.MkdirAll(vsdir, 0o755); err != nil {
		return err
	}
	file := filepath.Join(vsdir, "settings.json")

	settings := map[string]any{}
	if b, err := os.ReadFile(file); err == nil && len(bytes.TrimSpace(b)) > 0 {
		if err := json.Unmarshal(b, &settings); err != nil {
			return fmt.Errorf("model: parse %s: %w", file, err)
		}
	}

	existing, _ := settings["workbench.colorCustomizations"].(map[string]any)
	if existing == nil {
		existing = map[string]any{}
	}
	for k, v := range vscodeColorCustomizations(hex, fg) {
		existing[k] = v
	}
	settings["workbench.colorCustomizations"] = existing

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	if err := os.WriteFile(file, out, 0o644); err != nil {
		return err
	}
	addLocalGitExclude(dir, "/.vscode/settings.json")
	return nil
}

// addLocalGitExclude adds pattern to the repo's info/exclude so docent's
// generated settings.json doesn't show up as dirty in `git status`. It is a
// best-effort no-op outside a git repo, when the file is already tracked, or
// when the pattern is already excluded (mirrors grove's project.AddLocalExclude
// without the cross-module dependency).
func addLocalGitExclude(dir, pattern string) {
	if gitQuiet(dir, "ls-files", "--error-unmatch", strings.TrimPrefix(pattern, "/")) {
		return
	}
	out, err := gitOut(dir, "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return
	}
	ex := strings.TrimSpace(out)
	if ex == "" {
		return
	}
	if !filepath.IsAbs(ex) {
		ex = filepath.Join(dir, ex)
	}
	if b, err := os.ReadFile(ex); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.TrimSpace(line) == pattern {
				return
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(ex), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(ex, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(pattern + "\n")
}

func gitQuiet(dir string, args ...string) bool {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	return cmd.Run() == nil
}

func gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var buf strings.Builder
	cmd.Stdout = &buf
	cmd.Stderr = io.Discard
	err := cmd.Run()
	return buf.String(), err
}
