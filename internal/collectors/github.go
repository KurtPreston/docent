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
// activity in opts.Since → window end. The exact set of queries depends on
// the resolved scope:
//
//   - ScopeSelf: only queries anchored on the user (`--author`).
//   - ScopeInvolved (default): user-anchored queries plus reviewed-by,
//     commenter, and involves queries.
//   - ScopeAll: ScopeInvolved queries plus per-repo searches for each
//     entry in `config.followed_repos` (no user filter). With no
//     followed_repos configured, ScopeAll behaves identically to
//     ScopeInvolved.
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

	scope := opts.EffectiveScope()
	followedRepos := parseFollowedList(directive.Config["followed_repos"])
	searches, commitSearches := buildGitHubSearchSpecs(scope, user, dateStr, followedRepos)

	var items []StatusItem
	for _, spec := range searches {
		batch, err := runGitHubSearch(ctx, env, spec, directive, user, host, since, now)
		if err != nil {
			return nil, err
		}
		items = append(items, batch...)
	}
	for _, spec := range commitSearches {
		batch, err := runGitHubCommitSearch(ctx, env, spec, directive, user, host, since, now)
		if err != nil {
			return nil, err
		}
		items = append(items, batch...)
	}
	return dedupeGitHubItems(items), nil
}

// ghSearchSpec describes a single `gh search prs|issues` invocation.
//
// userAnchored is true for queries that are scoped to the configured user
// (author/reviewer/commenter/involves). Rows from those queries are
// IsSelf=true unconditionally. Repo-scoped queries (used in ScopeAll) set
// IsSelf based on whether the row's repository and the resolved user
// match.
type ghSearchSpec struct {
	queryType    string // "prs" or "issues"
	args         []string
	summary      string
	itemKind     string
	jsonFields   string
	userAnchored bool
}

// ghCommitSearchSpec mirrors ghSearchSpec for `gh search commits`, which
// returns a different JSON shape.
type ghCommitSearchSpec struct {
	args         []string
	summary      string
	itemKind     string
	userAnchored bool
}

// buildGitHubSearchSpecs returns the list of gh search invocations to run
// for the given scope. Exported (lowercase) for tests in this package.
func buildGitHubSearchSpecs(scope Scope, user, dateStr string, followedRepos []string) ([]ghSearchSpec, []ghCommitSearchSpec) {
	updatedFilter := ">=" + dateStr
	authoredPRs := ghSearchSpec{
		queryType:    "prs",
		args:         []string{"--author", user, "--updated", updatedFilter},
		summary:      fmt.Sprintf("author:%s updated:>=%s", user, dateStr),
		itemKind:     "authored_pr",
		jsonFields:   "title,url,state,updatedAt,closedAt,repository",
		userAnchored: true,
	}
	authoredCommits := ghCommitSearchSpec{
		args:         []string{"--author", user, "--author-date", updatedFilter},
		summary:      fmt.Sprintf("author:%s author-date:>=%s", user, dateStr),
		itemKind:     "github_commit",
		userAnchored: true,
	}

	if scope == ScopeSelf {
		return []ghSearchSpec{authoredPRs}, []ghCommitSearchSpec{authoredCommits}
	}

	// ScopeInvolved and ScopeAll both include the user-anchored set;
	// ScopeAll layers repo-scoped queries on top.
	involved := []ghSearchSpec{
		authoredPRs,
		{
			queryType:    "prs",
			args:         []string{"--reviewed-by", user, "--updated", updatedFilter},
			summary:      fmt.Sprintf("reviewed-by:%s updated:>=%s", user, dateStr),
			itemKind:     "reviewed_pr",
			jsonFields:   "title,url,state,updatedAt,repository",
			userAnchored: true,
		},
		{
			queryType:    "issues",
			args:         []string{"--involves", user, "--updated", updatedFilter},
			summary:      fmt.Sprintf("involves:%s updated:>=%s", user, dateStr),
			itemKind:     "involved_issue",
			jsonFields:   "title,url,state,updatedAt,repository",
			userAnchored: true,
		},
		// GitHub issue search requires is:issue or is:pull-request in the query;
		// --include-prs does not satisfy that, so split issues vs PRs.
		{
			queryType:    "issues",
			args:         []string{"is:issue", "--commenter", user, "--updated", updatedFilter},
			summary:      fmt.Sprintf("is:issue commenter:%s updated:>=%s", user, dateStr),
			itemKind:     "left_comment",
			jsonFields:   "title,url,state,updatedAt,repository",
			userAnchored: true,
		},
		{
			queryType:    "prs",
			args:         []string{"--commenter", user, "--updated", updatedFilter},
			summary:      fmt.Sprintf("is:pull-request commenter:%s updated:>=%s", user, dateStr),
			itemKind:     "left_comment",
			jsonFields:   "title,url,state,updatedAt,repository",
			userAnchored: true,
		},
	}
	commits := []ghCommitSearchSpec{authoredCommits}

	if scope != ScopeAll || len(followedRepos) == 0 {
		return involved, commits
	}

	for _, repo := range followedRepos {
		involved = append(involved,
			ghSearchSpec{
				queryType:  "prs",
				args:       []string{"--repo", repo, "--updated", updatedFilter},
				summary:    fmt.Sprintf("repo:%s is:pull-request updated:>=%s", repo, dateStr),
				itemKind:   "repo_pr",
				jsonFields: "title,url,state,updatedAt,closedAt,repository",
			},
			ghSearchSpec{
				queryType:  "issues",
				args:       []string{"is:issue", "--repo", repo, "--updated", updatedFilter},
				summary:    fmt.Sprintf("repo:%s is:issue updated:>=%s", repo, dateStr),
				itemKind:   "repo_issue",
				jsonFields: "title,url,state,updatedAt,repository",
			},
		)
		commits = append(commits, ghCommitSearchSpec{
			args:     []string{"--repo", repo, "--author-date", updatedFilter},
			summary:  fmt.Sprintf("repo:%s author-date:>=%s", repo, dateStr),
			itemKind: "repo_commit",
		})
	}
	return involved, commits
}

func runGitHubSearch(ctx context.Context, env []string, spec ghSearchSpec, directive userdata.Directive, user, host string, since, now time.Time) ([]StatusItem, error) {
	args := append([]string{"search", spec.queryType}, spec.args...)
	args = append(args, "--limit", "25", "--json", spec.jsonFields)
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("gh %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(exit.Stderr)))
		}
		return nil, fmt.Errorf("gh %s: %w", strings.Join(args, " "), err)
	}
	var rows []ghSearchActivityRow
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, err
	}
	var items []StatusItem
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
			Kind:        spec.itemKind,
			Title:       row.Title,
			Summary:     fmt.Sprintf("%s state=%s updated=%s", spec.summary, row.State, row.UpdatedAt),
			URL:         row.URL,
			Severity:    "info",
			ObservedAt:  obs.UTC(),
			Author:      user,
			IsSelf:      spec.userAnchored,
			Fields: map[string]string{
				"query":      spec.summary,
				"username":   user,
				"host":       host,
				"repo":       repo,
				"state":      row.State,
				"updated_at": row.UpdatedAt,
				"closed_at":  row.ClosedAt,
			},
		})
	}
	return items, nil
}

func runGitHubCommitSearch(ctx context.Context, env []string, spec ghCommitSearchSpec, directive userdata.Directive, user, host string, since, now time.Time) ([]StatusItem, error) {
	args := append([]string{"search", "commits"}, spec.args...)
	args = append(args, "--limit", "25", "--json", "sha,url,repository,commit")
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("gh %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(exit.Stderr)))
		}
		return nil, fmt.Errorf("gh %s: %w", strings.Join(args, " "), err)
	}
	var rows []ghSearchCommitRow
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, err
	}
	var items []StatusItem
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
			Kind:        spec.itemKind,
			Title:       title,
			Summary:     fmt.Sprintf("%s repo=%s sha=%s authored=%s", spec.summary, repo, row.SHA, row.Commit.Author.Date),
			URL:         row.URL,
			Severity:    "info",
			ObservedAt:  obs.UTC(),
			Author:      user,
			IsSelf:      spec.userAnchored,
			Fields: map[string]string{
				"query":       spec.summary,
				"username":    user,
				"host":        host,
				"repo":        repo,
				"sha":         row.SHA,
				"authored_at": row.Commit.Author.Date,
			},
		})
	}
	return items, nil
}

// dedupeGitHubItems merges duplicates that arise when the same PR / issue /
// commit shows up in multiple search results (e.g. a PR authored by the user
// is also reviewed by them, or repo-scoped queries surface the same PR an
// involves query already found). We key off URL when present, falling back
// to (kind, title, observedAt). When merging, IsSelf=true wins so the
// strongest signal sticks.
func dedupeGitHubItems(items []StatusItem) []StatusItem {
	if len(items) <= 1 {
		return items
	}
	seen := make(map[string]int, len(items))
	out := make([]StatusItem, 0, len(items))
	for _, it := range items {
		key := it.URL
		if key == "" {
			key = fmt.Sprintf("%s|%s|%s", it.Kind, it.Title, it.ObservedAt.UTC().Format(time.RFC3339Nano))
		}
		if idx, ok := seen[key]; ok {
			if it.IsSelf && !out[idx].IsSelf {
				out[idx].IsSelf = true
			}
			continue
		}
		seen[key] = len(out)
		out = append(out, it)
	}
	return out
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
