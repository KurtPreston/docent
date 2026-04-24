package collectors

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kurt/slakkr-ai/internal/userdata"
)

type LocalGitCollector struct {
	Clock func() time.Time
}

func (c LocalGitCollector) Collect(ctx context.Context, directive userdata.Directive) ([]StatusItem, error) {
	path := directive.Target["path"]
	if path == "" {
		return nil, fmt.Errorf("target.path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	branch, _ := gitOutput(ctx, abs, "branch", "--show-current")
	status, err := gitOutput(ctx, abs, "status", "--porcelain=v1")
	if err != nil {
		return nil, err
	}
	recent, _ := gitOutput(ctx, abs, "log", "-1", "--pretty=format:%h %s")
	remote, _ := gitOutput(ctx, abs, "remote", "get-url", "origin")
	summary := "Working tree clean"
	severity := "info"
	if strings.TrimSpace(status) != "" {
		lines := strings.Split(strings.TrimSpace(status), "\n")
		summary = fmt.Sprintf("Working tree has %d changed file(s)", len(lines))
		severity = "warning"
	}
	fields := map[string]string{
		"path":          abs,
		"branch":        strings.TrimSpace(branch),
		"latest_commit": strings.TrimSpace(recent),
		"remote":        strings.TrimSpace(remote),
	}
	return []StatusItem{{
		DirectiveID: directive.ID,
		ProjectID:   directive.ProjectID,
		Source:      "local-git",
		Kind:        "repository_status",
		Title:       directive.Name,
		Summary:     summary,
		Severity:    severity,
		ObservedAt:  c.Clock(),
		Fields:      fields,
	}}, nil
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}
