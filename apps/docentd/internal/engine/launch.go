package engine

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const launchTimeout = 30 * time.Second

// LaunchResult is returned when a work-item launch hook runs.
type LaunchResult struct {
	OK       bool   `json:"ok"`
	Message  string `json:"message,omitempty"`
	Strategy string `json:"strategy,omitempty"`
}

// LaunchWorkItem runs the configured onclick hook for one work item.
// Returns ok=false when no work item with that key exists.
func (e *Engine) LaunchWorkItem(ctx context.Context, key string) (LaunchResult, bool) {
	detail, found := e.WorkItem(key)
	if !found {
		return LaunchResult{}, false
	}

	script := strings.TrimSpace(e.cfg.OnClickScript)
	if script == "" {
		return LaunchResult{OK: false, Message: "no onclick script configured"}, true
	}
	info, err := os.Stat(script)
	if err != nil {
		if os.IsNotExist(err) {
			return LaunchResult{OK: false, Message: "onclick script not found: " + script}, true
		}
		return LaunchResult{OK: false, Message: "onclick script: " + err.Error()}, true
	}
	if info.IsDir() {
		return LaunchResult{OK: false, Message: "onclick script is a directory: " + script}, true
	}
	if info.Mode()&0o111 == 0 {
		return LaunchResult{OK: false, Message: "onclick script is not executable: " + script}, true
	}

	env := os.Environ()
	env = append(env, launchEnv(detail, e.cfg.WSMURL, e.cfg.SSHHost)...)

	cctx, cancel := context.WithTimeout(ctx, launchTimeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, script)
	cmd.Env = env
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined

	err = cmd.Run()
	out := strings.TrimSpace(combined.String())
	msg := lastNonEmptyLine(out)
	if msg == "" && err != nil {
		msg = err.Error()
	}
	if msg == "" {
		msg = "launch hook finished"
	}

	if err != nil {
		if cctx.Err() == context.DeadlineExceeded {
			return LaunchResult{OK: false, Message: "onclick script timed out"}, true
		}
		return LaunchResult{OK: false, Message: msg}, true
	}
	return LaunchResult{OK: true, Message: msg}, true
}

func launchEnv(d WorkItemDetail, wsmURL, sshHost string) []string {
	ticket := d.Ticket
	if ticket == "" {
		ticket = d.Key
	}
	summary := d.Summary
	if summary == "" {
		summary = d.Title
	}
	title := d.Title
	if title == "" {
		title = summary
	}
	pairs := []struct{ k, v string }{
		{"DOCENT_KEY", d.Key},
		{"DOCENT_TICKET", ticket},
		{"DOCENT_TITLE", title},
		{"DOCENT_SUMMARY", summary},
		{"DOCENT_REPO", d.Repo},
		{"DOCENT_BRANCH", d.Branch},
		{"DOCENT_OPEN_PATH", d.OpenPath},
		{"DOCENT_STATUS", d.Status},
		{"DOCENT_JIRA_URL", d.JiraURL},
		{"DOCENT_COLOR", d.Color},
		{"DOCENT_FG", d.FG},
		{"DOCENT_LAST_ACTIVITY", d.LastActivity},
		{"WSM_URL", wsmURL},
		{"DOCENT_HOST", sshHost},
	}
	out := make([]string, 0, len(pairs))
	for _, p := range pairs {
		if p.v == "" {
			continue
		}
		out = append(out, fmt.Sprintf("%s=%s", p.k, p.v))
	}
	return out
}

func lastNonEmptyLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return ""
}
