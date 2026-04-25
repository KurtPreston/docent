package collectors

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/kurt/slakkr-ai/internal/userdata"
)

type GitHubActivityCollector struct {
	Clock func() time.Time
}

type ghSearchRow struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

func (c GitHubActivityCollector) Collect(ctx context.Context, directive userdata.Directive, opts *CollectOpts) ([]StatusItem, error) {
	user := strings.TrimSpace(directive.Target["username"])
	if user == "" {
		return nil, fmt.Errorf("target.username is required")
	}
	tokenKey := directive.CredentialRefs["token"]
	token := userdata.ResolveEnv(opts.UserdataDir, tokenKey)
	baseURL := strings.TrimSpace(directive.Config["base_url"])
	if baseURL == "" {
		baseURL = "https://github.com"
	}
	host := hostname(baseURL)
	now := c.Clock()
	env := os.Environ()
	if token != "" {
		env = append(env, "GITHUB_TOKEN="+token)
	}
	var items []StatusItem
	appendSearch := func(kind, q string) error {
		args := []string{"search", kind, "--limit", "12", "--json", "title,url", "--"}
		if host != "" && host != "github.com" {
			args = append([]string{"--hostname", host}, args...)
		}
		args = append(args, q)
		cmd := exec.CommandContext(ctx, "gh", args...)
		cmd.Env = env
		out, err := cmd.Output()
		if err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				return fmt.Errorf("gh %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(exit.Stderr)))
			}
			return fmt.Errorf("gh %s: %w", strings.Join(args, " "), err)
		}
		var rows []ghSearchRow
		if err := json.Unmarshal(out, &rows); err != nil {
			return err
		}
		for _, row := range rows {
			items = append(items, StatusItem{
				DirectiveID: directive.ID,
				ProjectID:   directive.ProjectID,
				Source:      directive.Collector,
				Kind:        kind + "_hit",
				Title:       row.Title,
				Summary:     q,
				URL:         row.URL,
				Severity:    "info",
				ObservedAt:  now,
				Fields: map[string]string{
					"query":    q,
					"username": user,
					"host":     host,
				},
			})
		}
		return nil
	}
	if err := appendSearch("prs", fmt.Sprintf("review-requested:%s is:open", user)); err != nil {
		return nil, err
	}
	if err := appendSearch("prs", fmt.Sprintf("author:%s is:pr is:open", user)); err != nil {
		return nil, err
	}
	if err := appendSearch("issues", fmt.Sprintf("assignee:%s is:issue is:open", user)); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		items = append(items, StatusItem{
			DirectiveID: directive.ID,
			ProjectID:   directive.ProjectID,
			Source:      directive.Collector,
			Kind:        "activity",
			Title:       directive.Name,
			Summary:     fmt.Sprintf("No open review requests, authored PRs, or assigned issues found for %s.", user),
			Severity:    "info",
			ObservedAt:  now,
		})
	}
	return items, nil
}
