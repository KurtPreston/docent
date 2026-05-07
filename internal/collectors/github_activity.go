package collectors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
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
	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
}

type ghSearchCommitRow struct {
	SHA        string `json:"sha"`
	URL        string `json:"url"`
	Repository struct {
		FullName string `json:"fullName"`
	} `json:"repository"`
	Commit struct {
		Message string `json:"message"`
		Author  struct {
			Date string `json:"date"`
		} `json:"author"`
	} `json:"commit"`
}

// Collect runs scoped GitHub search queries for PR/issue/comment/commit activity in opts.Since → now.
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
	updatedFilter := ">=" + dateStr
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
			repo := strings.TrimSpace(row.Repository.NameWithOwner)
			if repo == "" {
				repo = gitHubOwnerRepoFromURL(row.URL)
			}
			items = append(items, StatusItem{
				DirectiveID: directive.ID,
				Repository:  repo,
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
					"repo":       repo,
					"state":      row.State,
					"updated_at": row.UpdatedAt,
					"closed_at":  row.ClosedAt,
				},
			})
		}
		return nil
	}
	trySearchCommits := func(queryForSummary, itemKind string) error {
		args := []string{"search", "commits", "--author", user, "--author-date", updatedFilter, "--limit", "25", "--json", "sha,url,repository,commit"}
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
		var rows []ghSearchCommitRow
		if err := json.Unmarshal(out, &rows); err != nil {
			return err
		}
		for _, row := range rows {
			obs, err := time.Parse(time.RFC3339, strings.TrimSpace(row.Commit.Author.Date))
			if err != nil || obs.Before(since) || obs.After(now) {
				continue
			}
			msg := strings.TrimSpace(row.Commit.Message)
			title := msg
			if i := strings.IndexByte(msg, '\n'); i >= 0 {
				title = strings.TrimSpace(msg[:i])
			}
			if title == "" {
				title = row.SHA
			}
			repo := strings.TrimSpace(row.Repository.FullName)
			items = append(items, StatusItem{
				DirectiveID: directive.ID,
				Repository:  repo,
				Source:      directive.Collector,
				Kind:        itemKind,
				Title:       title,
				Summary:     fmt.Sprintf("%s repo=%s sha=%s authored=%s", queryForSummary, repo, row.SHA, row.Commit.Author.Date),
				URL:         row.URL,
				Severity:    "info",
				ObservedAt:  obs.UTC(),
				Fields: map[string]string{
					"query":       queryForSummary,
					"username":    user,
					"host":        host,
					"repo":        repo,
					"sha":         row.SHA,
					"authored_at": row.Commit.Author.Date,
				},
			})
		}
		return nil
	}
	if err := trySearch(
		"prs",
		[]string{"--author", user, "--updated", updatedFilter},
		fmt.Sprintf("author:%s updated:>=%s", user, dateStr),
		"authored_pr",
		"title,url,state,updatedAt,closedAt,repository",
	); err != nil {
		return nil, err
	}
	if err := trySearch(
		"prs",
		[]string{"--reviewed-by", user, "--updated", updatedFilter},
		fmt.Sprintf("reviewed-by:%s updated:>=%s", user, dateStr),
		"reviewed_pr",
		"title,url,state,updatedAt,repository",
	); err != nil {
		return nil, err
	}
	if err := trySearch(
		"issues",
		[]string{"--involves", user, "--updated", updatedFilter},
		fmt.Sprintf("involves:%s updated:>=%s", user, dateStr),
		"involved_issue",
		"title,url,state,updatedAt,repository",
	); err != nil {
		return nil, err
	}
	// GitHub issue search requires is:issue or is:pull-request in the query; --include-prs
	// does not satisfy that, so split issues vs PRs (gh search prs scopes to pull requests).
	if err := trySearch(
		"issues",
		[]string{"is:issue", "--commenter", user, "--updated", updatedFilter},
		fmt.Sprintf("is:issue commenter:%s updated:>=%s", user, dateStr),
		"left_comment",
		"title,url,state,updatedAt,repository",
	); err != nil {
		return nil, err
	}
	if err := trySearch(
		"prs",
		[]string{"--commenter", user, "--updated", updatedFilter},
		fmt.Sprintf("is:pull-request commenter:%s updated:>=%s", user, dateStr),
		"left_comment",
		"title,url,state,updatedAt,repository",
	); err != nil {
		return nil, err
	}
	if err := trySearchCommits(
		fmt.Sprintf("author:%s author-date:>=%s", user, dateStr),
		"github_commit",
	); err != nil {
		return nil, err
	}
	return items, nil
}

func gitHubOwnerRepoFromURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Path == "" {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[0] + "/" + parts[1]
}
