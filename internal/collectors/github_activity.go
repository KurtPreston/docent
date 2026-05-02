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

type ghSearchActivityRow struct {
	Title     string `json:"title"`
	URL       string `json:"url"`
	State     string `json:"state"`
	UpdatedAt string `json:"updatedAt"`
	ClosedAt  string `json:"closedAt"`
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
	// Use gh's qualifier flags instead of a single search string. Combining
	// review-requested:user with is:open in the free-text query makes GitHub
	// quote the qualifier incorrectly and the search fails.
	appendSearch := func(kind, q string, qualifiers ...string) error {
		args := []string{"search", kind, "--limit", "12", "--json", "title,url"}
		args = append(args, qualifiers...)
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
	if err := appendSearch("prs", fmt.Sprintf("review-requested:%s is:open", user), "--review-requested", user, "--state", "open"); err != nil {
		return nil, err
	}
	if err := appendSearch("prs", fmt.Sprintf("author:%s is:pr is:open", user), "--author", user, "--state", "open"); err != nil {
		return nil, err
	}
	if err := appendSearch("issues", fmt.Sprintf("assignee:%s is:issue is:open", user), "--assignee", user, "--state", "open"); err != nil {
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

// CollectActivity runs scoped GitHub search queries for PR/issue activity updated since `since`.
func (c GitHubActivityCollector) CollectActivity(ctx context.Context, directive userdata.Directive, since time.Time, opts *CollectOpts) ([]StatusItem, error) {
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
	dateStr := since.Format("2006-01-02")
	env := os.Environ()
	if token != "" {
		env = append(env, "GITHUB_TOKEN="+token)
	}
	var items []StatusItem
	trySearch := func(kind, query, itemKind, jsonFields string) error {
		args := []string{"search", kind, query, "--limit", "25", "--json", jsonFields}
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
				Summary:     fmt.Sprintf("%s state=%s updated=%s", query, row.State, row.UpdatedAt),
				URL:         row.URL,
				Severity:    "info",
				ObservedAt:  obs.UTC(),
				Fields: map[string]string{
					"query":      query,
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
	if err := trySearch("prs", fmt.Sprintf("author:%s updated:>=%s", user, dateStr), "authored_pr", "title,url,state,updatedAt,closedAt"); err != nil {
		return nil, err
	}
	if err := trySearch("prs", fmt.Sprintf("reviewed-by:%s updated:>=%s", user, dateStr), "reviewed_pr", "title,url,state,updatedAt"); err != nil {
		return nil, err
	}
	if err := trySearch("issues", fmt.Sprintf("involves:%s updated:>=%s", user, dateStr), "involved_issue", "title,url,state,updatedAt"); err != nil {
		return nil, err
	}
	return items, nil
}
