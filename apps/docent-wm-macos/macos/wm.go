package macos

import (
	"fmt"
	"os/exec"
	"strings"
)

// ProcessName is the Cursor process name on macOS.
const ProcessName = "Cursor"

func escapeAppleScript(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}

func runOsascript(script string) (string, error) {
	cmd := exec.Command("osascript", "-e", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("osascript: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// ListWindowTitles returns Cursor window titles.
func ListWindowTitles() ([]string, error) {
	proc := escapeAppleScript(ProcessName)
	script := fmt.Sprintf(`
set titles to {}
tell application "System Events"
  repeat with p in (every process whose name is "%s")
    repeat with w in (windows of p)
      set end of titles to (name of w)
    end repeat
  end repeat
end tell
set AppleScript's text item delimiters to linefeed
return titles as text
`, proc)
	out, err := runOsascript(script)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	var titles []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			titles = append(titles, line)
		}
	}
	return titles, nil
}

// FocusWindow raises a window whose title contains leaf.
func FocusWindow(leaf string) error {
	proc := escapeAppleScript(ProcessName)
	leafEsc := escapeAppleScript(leaf)
	script := fmt.Sprintf(`
tell application "System Events"
  repeat with p in (every process whose name is "%s")
    repeat with w in (windows of p)
      if (name of w) contains "%s" then
        perform action "AXRaise" of w
        set frontmost of p to true
        return "1"
      end if
    end repeat
  end repeat
end tell
return "0"
`, proc, leafEsc)
	out, err := runOsascript(script)
	if err != nil {
		return err
	}
	if out != "1" {
		return fmt.Errorf("no window matching %q", leaf)
	}
	return nil
}

// OpenWorkspace launches Cursor with a folder URI.
func OpenWorkspace(uri, leaf string) error {
	// Prefer cursor CLI when available.
	if path, err := exec.LookPath("cursor"); err == nil {
		cmd := exec.Command(path, "--new-window", "--folder-uri", uri)
		if err := cmd.Run(); err != nil {
			return err
		}
	} else {
		cmd := exec.Command("open", "-na", "Cursor", "--args", "--new-window", "--folder-uri", uri)
		if err := cmd.Run(); err != nil {
			return err
		}
	}
	// Best-effort: poll briefly and focus.
	for i := 0; i < 50; i++ {
		if titles, _ := ListWindowTitles(); len(titles) > 0 {
			for _, t := range titles {
				if strings.Contains(t, leaf) {
					return FocusWindow(leaf)
				}
			}
		}
	}
	return nil
}
