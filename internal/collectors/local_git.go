package collectors

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kurt/slakkr-ai/internal/userdata"
)

type LocalGitCollector struct {
	Clock func() time.Time
}

func (c LocalGitCollector) Collect(ctx context.Context, directive userdata.Directive, opts *CollectOpts) ([]StatusItem, error) {
	expand := defaultExpandRepoPath(opts)
	projectID := strings.TrimSpace(directive.Target["project_id"])
	repoID := strings.TrimSpace(directive.Target["repo_id"])
	if projectID == "" || repoID == "" {
		return nil, fmt.Errorf("local-git requires target project_id and repo_id")
	}
	if opts == nil || opts.HostID == "" {
		return nil, fmt.Errorf("local-git requires host context (SLAKKR_HOST or hostname)")
	}
	candidates, err := userdata.ExpandedRepoWorktrees(opts.Projects, projectID, repoID, opts.HostID, expand)
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, dir := range candidates {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			dirs = append(dirs, dir)
		}
	}
	if len(dirs) == 0 {
		return nil, fmt.Errorf("no existing working tree on this host for project_id=%s repo_id=%s", projectID, repoID)
	}

	var out []StatusItem
	for _, abs := range dirs {
		item, err := c.collectOne(ctx, directive, abs)
		if err != nil {
			return nil, err
		}
		if len(dirs) > 1 {
			item.Title = fmt.Sprintf("%s (%s)", directive.Name, filepath.Base(abs))
		}
		out = append(out, item)
	}
	return out, nil
}

func defaultExpandRepoPath(opts *CollectOpts) func(string) string {
	if opts != nil && opts.ExpandRepoPath != nil {
		return opts.ExpandRepoPath
	}
	return func(s string) string {
		s = strings.TrimSpace(s)
		if s == "" {
			return ""
		}
		abs, err := filepath.Abs(s)
		if err != nil {
			return filepath.Clean(s)
		}
		return abs
	}
}

func (c LocalGitCollector) collectOne(ctx context.Context, directive userdata.Directive, abs string) (StatusItem, error) {
	branch, _ := gitOutput(ctx, abs, "branch", "--show-current")
	status, err := gitOutput(ctx, abs, "status", "--porcelain=v1")
	if err != nil {
		return StatusItem{}, err
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
	return StatusItem{
		DirectiveID: directive.ID,
		ProjectID:   directive.ProjectID,
		Source:      "local-git",
		Kind:        "repository_status",
		Title:       directive.Name,
		Summary:     summary,
		Severity:    severity,
		ObservedAt:  c.Clock(),
		Fields:      fields,
	}, nil
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
