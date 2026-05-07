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

type ghSearchActivityRow struct {
	Title     string `json:"title"`
	URL       string `json:"url"`
	State     string `json:"state"`
	UpdatedAt string `json:"updatedAt"`
	ClosedAt  string `json:"closedAt"`
}

// Collect runs scoped GitHub search queries for PR/issue activity updated since opts.Since.
func (c GitHubActivityCollector) Collect(ctx context.Context, directive userdata.Directive, opts *CollectOpts) ([]StatusItem, error) {
	user := strings.TrimSpace(directive.Target["username"])
	if user == "" {
		return nil, fmt.Errorf("target.username is required")
	}
	tokenKey := directive.CredentialRefs["token"]
	token := ""
	if opts != nil {
		token = userdata.ResolveEnv(opts.UserdataDir, tokenKey)
	}
	baseURL := strings.TrimSpace(directive.Config["base_url"])
	if baseURL == "" {
		baseURL = "https://github.com"
	}
	host := hostname(baseURL)
	since := time.Time{}
	if opts != nil {
		since = opts.Since
	}
	now := c.Clock()
	if opts != nil {
		now = opts.windowEnd(c.Clock)
	}
	dateStr := since.Format("2006-01-02")
	env := os.Environ()
	if token != "" {
		env = append(env, "GITHUB_TOKEN="+token)
	}
	var items []StatusItem
	trySearch := func(kind string, filterArgs []string, queryForSummary, itemKind, jsonFields string) error {
		args := []string{"search", kind}
		args = append(args, filterArgs...)
		args = append(args, "--limit", "25", "--json", jsonFields)
		if host != "" && host != "github.com" {
			args = append([]string{"--hostname", host}, args...)
		}
		cmd := exec.CommandContext(ctx, "gh", args...)
		cmd.Env = env
		out, err := cmd.Output()
		if err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				return fmt.Errorf("gh %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(exit.Stderr)))
			}
			return fmt.Errorf("gh %s: %w", strings.Join(args, " "), err)
		}
		var rows []ghSearchActivityRow
		if err := json.Unmarshal(out, &rows); err != nil {
			return err
		}
		for _, row := range rows {
			obs, err := time.Parse(time.RFC3339, strings.TrimSpace(row.UpdatedAt))
			if err != nil || obs.Before(since) || obs.After(now) {
				continue
			}
			items = append(items, StatusItem{
				DirectiveID: directive.ID,
				ProjectID:   directive.ProjectID,
				Source:      directive.Collector,
				Kind:        itemKind,
				Title:       row.Title,
				Summary:     fmt.Sprintf("%s state=%s updated=%s", queryForSummary, row.State, row.UpdatedAt),
				URL:         row.URL,
				Severity:    "info",
				ObservedAt:  obs.UTC(),
				Fields: map[string]string{
					"query":      queryForSummary,
					"username":   user,
					"host":       host,
					"state":      row.State,
					"updated_at": row.UpdatedAt,
					"closed_at":  row.ClosedAt,
				},
			})
		}
		return nil
	}
	updatedFilter := ">=" + dateStr
	if err := trySearch(
		"prs",
		[]string{"--author", user, "--updated", updatedFilter},
		fmt.Sprintf("author:%s updated:>=%s", user, dateStr),
		"authored_pr",
		"title,url,state,updatedAt,closedAt",
	); err != nil {
		return nil, err
	}
	if err := trySearch(
		"prs",
		[]string{"--reviewed-by", user, "--updated", updatedFilter},
		fmt.Sprintf("reviewed-by:%s updated:>=%s", user, dateStr),
		"reviewed_pr",
		"title,url,state,updatedAt",
	); err != nil {
		return nil, err
	}
	if err := trySearch(
		"issues",
		[]string{"--involves", user, "--updated", updatedFilter},
		fmt.Sprintf("involves:%s updated:>=%s", user, dateStr),
		"involved_issue",
		"title,url,state,updatedAt",
	); err != nil {
		return nil, err
	}
	return items, nil
}
