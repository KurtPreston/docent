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

// GitHubCollector returns activity (PRs, issues, comments, commits) authored
// by, reviewed by, or commented on by a user. When target.username is empty,
// the GitHub search literal "@me" is used so that the authenticated `gh` user
// is queried. The same struct backs both the `github` and `github-enterprise`
// directive types; enterprise hosts route requests via config.base_url.
type GitHubCollector struct {
	Clock func() time.Time
}

type ghSearchActivityRow struct {
	Title      string `json:"title"`
	URL        string `json:"url"`
	State      string `json:"state"`
	UpdatedAt  string `json:"updatedAt"`
	ClosedAt   string `json:"closedAt"`
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

// Collect runs scoped GitHub search queries for PR/issue/comment/commit
// activity in opts.Since → window end.
func (c GitHubCollector) Collect(ctx context.Context, directive userdata.Directive, opts *CollectOpts) ([]StatusItem, error) {
	user := strings.TrimSpace(directive.Target["username"])
	if user == "" {
		user = "@me"
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
	updatedFilter := ">=" + dateStr

	env := os.Environ()
	if token != "" {
		env = append(env, "GITHUB_TOKEN="+token)
	}
	// `gh search` (and most other `gh` commands) target the host indicated by
	// the GH_HOST environment variable; the `--hostname` flag is only honored
	// by a handful of commands like `gh auth` and `gh api`.
	if host != "" && host != "github.com" {
		env = append(env, "GH_HOST="+host)
	}

	var items []StatusItem

	trySearch := func(kind string, filterArgs []string, queryForSummary, itemKind, jsonFields string) error {
		args := []string{"search", kind}
		args = append(args, filterArgs...)
		args = append(args, "--limit", "25", "--json", jsonFields)
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

// ValidateDirective checks that `gh` is installed, the optional token env var
// is populated, and `gh auth status` succeeds for the target host.
func (c GitHubCollector) ValidateDirective(ctx context.Context, directive userdata.Directive, opts *ValidateOpts) []ValidationIssue {
	var issues []ValidationIssue
	if _, err := exec.LookPath("gh"); err != nil {
		return []ValidationIssue{{
			Field:       "gh",
			Message:     "`gh` CLI not found on PATH",
			Remediation: "install GitHub CLI (https://cli.github.com/) and run `gh auth login`",
		}}
	}

	baseURL := strings.TrimSpace(directive.Config["base_url"])
	if baseURL == "" {
		baseURL = "https://github.com"
	}
	host := hostname(baseURL)

	userdataDir := ""
	if opts != nil {
		userdataDir = opts.UserdataDir
	}

	tokenKey := strings.TrimSpace(directive.CredentialRefs["token"])
	tokenVal := ""
	if tokenKey != "" {
		tokenVal = userdata.ResolveEnv(userdataDir, tokenKey)
		if tokenVal == "" {
			issues = append(issues, ValidationIssue{
				Field:       "credential_refs.token",
				Message:     fmt.Sprintf("token env %q is empty", tokenKey),
				Remediation: fmt.Sprintf("set %s in your environment or in %s/.env", tokenKey, userdataDir),
			})
		}
	}

	args := []string{"auth", "status"}
	if host != "" && host != "github.com" {
		args = append(args, "--hostname", host)
	}
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, "gh", args...)
	env := os.Environ()
	if tokenVal != "" {
		env = append(env, "GITHUB_TOKEN="+tokenVal)
	}
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		remediation := "run `gh auth login`"
		if host != "" && host != "github.com" {
			remediation = fmt.Sprintf("run `gh auth login --hostname %s`", host)
		}
		issues = append(issues, ValidationIssue{
			Field:       "gh auth",
			Message:     fmt.Sprintf("`gh %s` failed: %s", strings.Join(args, " "), strings.TrimSpace(string(out))),
			Remediation: remediation,
		})
	}
	return issues
}

func hostname(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return raw
	}
	return parsed.Hostname()
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
