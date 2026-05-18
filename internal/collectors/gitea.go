package collectors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kurt/slakkr-ai/internal/userdata"
)

type GiteaCollector struct {
	Clock func() time.Time
	HTTP  *http.Client
}

type giteaRepo struct {
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	HTMLURL       string `json:"html_url"`
	Updated       string `json:"updated_at"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
}

// giteaIssue is one row from `/api/v1/repos/issues/search` or the per-repo
// `/api/v1/repos/{owner}/{repo}/issues` listing. PullRequest is non-nil for
// PRs and nil/absent for plain issues (Gitea's data model treats both as
// "issues").
type giteaIssue struct {
	Number     int    `json:"number"`
	Title      string `json:"title"`
	State      string `json:"state"`
	HTMLURL    string `json:"html_url"`
	Updated    string `json:"updated_at"`
	User       struct {
		Login string `json:"login"`
	} `json:"user"`
	Assignees []struct {
		Login string `json:"login"`
	} `json:"assignees"`
	Repository struct {
		FullName string `json:"full_name"`
		Name     string `json:"name"`
		Owner    string `json:"owner"`
	} `json:"repository"`
	PullRequest *struct{} `json:"pull_request,omitempty"`
}

func (c GiteaCollector) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// Collect emits issue, pull-request, and repository-update items for the
// resolved owner (and, in ScopeAll, the directive's followed_repos).
//
// Scope semantics:
//   - ScopeSelf: repos owned by the user; issues + PRs the user authored.
//   - ScopeInvolved (default): self UNION issues/PRs assigned to the user
//     UNION issues/PRs that mention the user. De-duped by (repo, number,
//     kind); IsSelf=true wins so a row that started as "mentioned" still
//     reads as user activity if the user also authored it.
//   - ScopeAll: involved UNION per-repo issue/PR listings for each entry in
//     config.followed_repos. Bare-owner entries fan out via owner filter;
//     owner/repo entries pin to a single repo. Repo-updated items expand
//     to include followed repos' updated_at signals.
//
// When target.owner is empty, the authenticated user is resolved via
// /api/v1/user and used as the owner so the directive defaults to the
// caller's own repositories and activity.
func (c GiteaCollector) Collect(ctx context.Context, directive userdata.Directive, opts *CollectOpts) ([]StatusItem, error) {
	base := strings.TrimSpace(directive.Config["base_url"])
	owner := strings.TrimSpace(directive.Target["owner"])
	if base == "" {
		return nil, fmt.Errorf("config.base_url is required")
	}
	tokenKey := directive.CredentialRefs["token"]
	userdataDir := ""
	if opts != nil {
		userdataDir = opts.UserdataDir
	}
	token := userdata.ResolveEnv(userdataDir, tokenKey)
	if token == "" {
		return nil, fmt.Errorf("gitea token missing (set %s in environment or userdata/.env)", tokenKey)
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid base_url")
	}
	apiBase := strings.TrimRight(u.String(), "/")
	if owner == "" {
		login, err := c.fetchAuthenticatedLogin(ctx, apiBase, token, opts, directive.ID)
		if err != nil {
			return nil, fmt.Errorf("resolve authenticated gitea user: %w", err)
		}
		owner = login
	}
	since := time.Time{}
	if opts != nil {
		since = opts.Since
	}
	now := c.Clock()
	if opts != nil {
		now = opts.windowEnd(c.Clock)
	}
	scope := opts.EffectiveScope()
	followedRepos := parseFollowedList(directive.Config["followed_repos"])

	// Repo-updated items: always include the resolved owner's repos. In
	// ScopeAll, also include each followed entry so the "this followed
	// repo got pushed" signal lands alongside its issue/PR activity.
	var items []StatusItem
	ownerRepos, err := c.fetchReposForOwner(ctx, apiBase, owner, token, opts, directive.ID)
	if err != nil {
		return nil, err
	}
	items = append(items, c.repoItemsFor(directive, apiBase, owner, ownerRepos, since, now)...)
	if scope == ScopeAll {
		for _, entry := range followedRepos {
			repos, err := c.resolveFollowedRepoEntry(ctx, apiBase, token, entry, opts, directive.ID)
			if err != nil {
				return nil, err
			}
			if len(repos) == 0 {
				continue
			}
			entryOwner := entry
			if slash := strings.Index(entry, "/"); slash > 0 {
				entryOwner = entry[:slash]
			}
			items = append(items, c.repoItemsFor(directive, apiBase, entryOwner, repos, since, now)...)
		}
	}

	// Issue / PR items. Order matters for de-dup: user-anchored queries go
	// first so their IsSelf=true entries win over repo-scoped duplicates.
	issueQueries := buildGiteaIssueQueries(scope, owner, followedRepos, since)
	for _, q := range issueQueries {
		rows, err := c.fetchIssues(ctx, apiBase, token, q, opts, directive.ID)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			obs, perr := parseGiteaTime(row.Updated)
			if perr != nil || obs.Before(since) || obs.After(now) {
				continue
			}
			items = append(items, buildGiteaIssueItem(directive, apiBase, owner, q, row, obs))
		}
	}
	return dedupeGiteaItems(items), nil
}

func (c GiteaCollector) repoItemsFor(directive userdata.Directive, apiBase, owner string, repos []giteaRepo, since, now time.Time) []StatusItem {
	var items []StatusItem
	for _, r := range repos {
		updatedAt, err := parseGiteaTime(r.Updated)
		if err != nil || updatedAt.Before(since) || updatedAt.After(now) {
			continue
		}
		title := r.FullName
		if title == "" {
			title = r.Name
		}
		summary := fmt.Sprintf("updated=%s branch=%s", r.Updated, r.DefaultBranch)
		if r.Private {
			summary += " private=true"
		}
		repoKey := strings.TrimSpace(r.FullName)
		if repoKey == "" {
			repoKey = r.Name
		}
		items = append(items, StatusItem{
			DirectiveID: directive.ID,
			Repository:  repoKey,
			Source:      "gitea",
			Kind:        "repository_updated",
			Title:       title,
			Summary:     summary,
			URL:         r.HTMLURL,
			Severity:    "info",
			ObservedAt:  updatedAt.UTC(),
			Author:      owner,
			IsSelf:      strings.EqualFold(owner, gitOwnerFromFullName(r.FullName)),
			Fields: map[string]string{
				"name":           r.Name,
				"full_name":      r.FullName,
				"updated_at":     r.Updated,
				"default_branch": r.DefaultBranch,
				"private":        fmt.Sprintf("%t", r.Private),
				"owner":          owner,
				"gitea_base":     apiBase,
			},
		})
	}
	return items
}

// giteaIssueQuery is one `/api/v1/repos/issues/search` call. anchor is the
// summary label used for the resulting StatusItem.Fields["query"] entry.
type giteaIssueQuery struct {
	// Mutually exclusive user filter (only one is non-empty).
	createdBy   string
	assignedBy  string
	mentionedBy string
	// Optional repo scoping for the search.
	owner string
	repo  string
	// type: "issues" or "pulls".
	issueType string
	since     time.Time
	itemKind  string
	anchor    string
	// userAnchored is true when the query is filtered by createdBy /
	// assignedBy / mentionedBy. Rows from such queries are IsSelf=true.
	userAnchored bool
}

func buildGiteaIssueQueries(scope Scope, user string, followedRepos []string, since time.Time) []giteaIssueQuery {
	authoredIssues := giteaIssueQuery{
		createdBy: user, issueType: "issues", since: since,
		itemKind: "gitea_issue", anchor: fmt.Sprintf("created_by:%s type:issues", user),
		userAnchored: true,
	}
	authoredPRs := giteaIssueQuery{
		createdBy: user, issueType: "pulls", since: since,
		itemKind: "gitea_pr", anchor: fmt.Sprintf("created_by:%s type:pulls", user),
		userAnchored: true,
	}
	if scope == ScopeSelf {
		return []giteaIssueQuery{authoredIssues, authoredPRs}
	}
	out := []giteaIssueQuery{
		authoredIssues, authoredPRs,
		{
			assignedBy: user, issueType: "issues", since: since,
			itemKind: "gitea_issue", anchor: fmt.Sprintf("assigned_by:%s type:issues", user),
			userAnchored: true,
		},
		{
			assignedBy: user, issueType: "pulls", since: since,
			itemKind: "gitea_pr", anchor: fmt.Sprintf("assigned_by:%s type:pulls", user),
			userAnchored: true,
		},
		{
			mentionedBy: user, issueType: "issues", since: since,
			itemKind: "gitea_issue", anchor: fmt.Sprintf("mentioned_by:%s type:issues", user),
			userAnchored: true,
		},
		{
			mentionedBy: user, issueType: "pulls", since: since,
			itemKind: "gitea_pr", anchor: fmt.Sprintf("mentioned_by:%s type:pulls", user),
			userAnchored: true,
		},
	}
	if scope != ScopeAll {
		return out
	}
	for _, entry := range followedRepos {
		entryOwner, entryRepo := splitFollowedRepo(entry)
		out = append(out,
			giteaIssueQuery{
				owner: entryOwner, repo: entryRepo, issueType: "issues", since: since,
				itemKind: "gitea_issue", anchor: fmt.Sprintf("repo:%s type:issues", entry),
			},
			giteaIssueQuery{
				owner: entryOwner, repo: entryRepo, issueType: "pulls", since: since,
				itemKind: "gitea_pr", anchor: fmt.Sprintf("repo:%s type:pulls", entry),
			},
		)
	}
	return out
}

// splitFollowedRepo parses an `owner` or `owner/repo` entry from
// config.followed_repos. A bare owner yields ("owner", "").
func splitFollowedRepo(entry string) (owner, repo string) {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return "", ""
	}
	if slash := strings.Index(entry, "/"); slash > 0 {
		return strings.TrimSpace(entry[:slash]), strings.TrimSpace(entry[slash+1:])
	}
	return entry, ""
}

func gitOwnerFromFullName(fullName string) string {
	if slash := strings.Index(fullName, "/"); slash > 0 {
		return fullName[:slash]
	}
	return fullName
}

func (c GiteaCollector) fetchIssues(ctx context.Context, apiBase, token string, q giteaIssueQuery, opts *CollectOpts, directiveID string) ([]giteaIssue, error) {
	values := url.Values{}
	values.Set("type", q.issueType)
	values.Set("state", "all")
	values.Set("limit", "50")
	if !q.since.IsZero() {
		values.Set("since", q.since.UTC().Format(time.RFC3339))
	}
	if q.createdBy != "" {
		values.Set("created_by", q.createdBy)
	}
	if q.assignedBy != "" {
		values.Set("assigned_by", q.assignedBy)
	}
	if q.mentionedBy != "" {
		values.Set("mentioned_by", q.mentionedBy)
	}
	if q.owner != "" {
		values.Set("owner", q.owner)
	}
	if q.repo != "" {
		values.Set("repo", q.repo)
	}
	rawURL := apiBase + "/api/v1/repos/issues/search?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/json")
	res, body, err := doAndReadHTTP(c.client(), req, 8<<20, opts, directiveID)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("gitea %s: %s — %s", res.Status, rawURL, strings.TrimSpace(string(body)))
	}
	var rows []giteaIssue
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("parse gitea issues: %w", err)
	}
	// For PR/issue parity, Gitea returns both kinds from /issues/search
	// when type is omitted; we always set type, but defensive filter:
	if q.issueType == "issues" {
		filtered := rows[:0]
		for _, row := range rows {
			if row.PullRequest == nil {
				filtered = append(filtered, row)
			}
		}
		rows = filtered
	} else if q.issueType == "pulls" {
		filtered := rows[:0]
		for _, row := range rows {
			if row.PullRequest != nil {
				filtered = append(filtered, row)
			}
		}
		rows = filtered
	}
	return rows, nil
}

func (c GiteaCollector) resolveFollowedRepoEntry(ctx context.Context, apiBase, token, entry string, opts *CollectOpts, directiveID string) ([]giteaRepo, error) {
	entryOwner, entryRepo := splitFollowedRepo(entry)
	if entryOwner == "" {
		return nil, nil
	}
	if entryRepo == "" {
		return c.fetchReposForOwner(ctx, apiBase, entryOwner, token, opts, directiveID)
	}
	rawURL := fmt.Sprintf("%s/api/v1/repos/%s/%s", apiBase, url.PathEscape(entryOwner), url.PathEscape(entryRepo))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/json")
	res, body, err := doAndReadHTTP(c.client(), req, 1<<20, opts, directiveID)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("gitea %s: %s — %s", res.Status, rawURL, strings.TrimSpace(string(body)))
	}
	var repo giteaRepo
	if err := json.Unmarshal(body, &repo); err != nil {
		return nil, fmt.Errorf("parse gitea repo %s: %w", entry, err)
	}
	return []giteaRepo{repo}, nil
}

func buildGiteaIssueItem(directive userdata.Directive, apiBase, user string, q giteaIssueQuery, row giteaIssue, obs time.Time) StatusItem {
	repoKey := strings.TrimSpace(row.Repository.FullName)
	if repoKey == "" {
		repoKey = row.Repository.Name
	}
	authorLogin := strings.TrimSpace(row.User.Login)
	isSelf := q.userAnchored
	if !isSelf && user != "" && strings.EqualFold(authorLogin, user) {
		isSelf = true
	}
	title := fmt.Sprintf("#%d %s", row.Number, row.Title)
	if repoKey != "" {
		title = fmt.Sprintf("[%s] %s", repoKey, title)
	}
	summary := fmt.Sprintf("%s state=%s updated=%s author=%s", q.anchor, row.State, row.Updated, authorLogin)
	fields := map[string]string{
		"query":      q.anchor,
		"username":   user,
		"repo":       repoKey,
		"number":     fmt.Sprintf("%d", row.Number),
		"state":      row.State,
		"updated_at": row.Updated,
		"author":     authorLogin,
		"gitea_base": apiBase,
	}
	if len(row.Assignees) > 0 {
		var names []string
		for _, a := range row.Assignees {
			names = append(names, a.Login)
		}
		fields["assignees"] = strings.Join(names, ",")
	}
	return StatusItem{
		DirectiveID: directive.ID,
		Repository:  repoKey,
		Source:      "gitea",
		Kind:        q.itemKind,
		Title:       title,
		Summary:     summary,
		URL:         row.HTMLURL,
		Severity:    "info",
		ObservedAt:  obs.UTC(),
		Author:      authorLogin,
		IsSelf:      isSelf,
		Fields:      fields,
	}
}

// dedupeGiteaItems keys off (kind, repo, number) for issues/PRs and URL
// for repository_updated rows. When duplicates collide, IsSelf=true wins.
func dedupeGiteaItems(items []StatusItem) []StatusItem {
	if len(items) <= 1 {
		return items
	}
	seen := make(map[string]int, len(items))
	out := make([]StatusItem, 0, len(items))
	for _, it := range items {
		var key string
		switch it.Kind {
		case "gitea_issue", "gitea_pr":
			key = fmt.Sprintf("%s|%s|%s", it.Kind, it.Repository, it.Fields["number"])
		default:
			key = it.URL
			if key == "" {
				key = fmt.Sprintf("%s|%s|%s", it.Kind, it.Repository, it.Title)
			}
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

// ValidateDirective checks base_url is a valid URL, the token env value is
// populated, and the configured token can reach `/api/v1/user`.
func (c GiteaCollector) ValidateDirective(ctx context.Context, directive userdata.Directive, opts *ValidateOpts) []ValidationIssue {
	var issues []ValidationIssue
	base := strings.TrimSpace(directive.Config["base_url"])
	if base == "" {
		return []ValidationIssue{{
			Field:       "config.base_url",
			Message:     "Gitea base_url is required",
			Remediation: "set config.base_url to your Gitea server (e.g. https://gitea.example.com)",
		}}
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return []ValidationIssue{{
			Field:       "config.base_url",
			Message:     fmt.Sprintf("base_url %q is not a valid URL", base),
			Remediation: "use the form https://gitea.example.com (no trailing /api/v1)",
		}}
	}
	tokenKey := strings.TrimSpace(directive.CredentialRefs["token"])
	if tokenKey == "" {
		return []ValidationIssue{{
			Field:       "credential_refs.token",
			Message:     "Gitea token credential is not configured",
			Remediation: "add credential_refs.token (e.g. SLAKKR_GITEA_TOKEN) and put the value in userdata/.env",
		}}
	}
	userdataDir := ""
	if opts != nil {
		userdataDir = opts.UserdataDir
	}
	token := userdata.ResolveEnv(userdataDir, tokenKey)
	if token == "" {
		return []ValidationIssue{{
			Field:       "credential_refs.token",
			Message:     fmt.Sprintf("Gitea token env %q is empty", tokenKey),
			Remediation: fmt.Sprintf("set %s in your environment or in %s/.env", tokenKey, userdataDir),
		}}
	}
	apiBase := strings.TrimRight(u.String(), "/")
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := c.fetchAuthenticatedLogin(probeCtx, apiBase, token, nil, directive.ID); err != nil {
		issues = append(issues, ValidationIssue{
			Field:       "auth",
			Message:     fmt.Sprintf("Gitea auth probe failed: %v", err),
			Remediation: fmt.Sprintf("verify %s is reachable and %s has read access to your account", apiBase, tokenKey),
		})
	}
	return issues
}

func parseGiteaTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty time")
	}
	// RFC3339 from Gitea API
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02T15:04:05Z07:00", s)
}

func (c GiteaCollector) fetchAuthenticatedLogin(ctx context.Context, apiBase, token string, opts *CollectOpts, directiveID string) (string, error) {
	rawURL := apiBase + "/api/v1/user"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/json")
	res, body, err := doAndReadHTTP(c.client(), req, 1<<20, opts, directiveID)
	if err != nil {
		return "", err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("gitea %s: %s", res.Status, strings.TrimSpace(string(body)))
	}
	var who struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(body, &who); err != nil {
		return "", fmt.Errorf("parse gitea user: %w", err)
	}
	if strings.TrimSpace(who.Login) == "" {
		return "", fmt.Errorf("gitea user response missing login")
	}
	return who.Login, nil
}

func (c GiteaCollector) fetchReposForOwner(ctx context.Context, apiBase, owner, token string, opts *CollectOpts, directiveID string) ([]giteaRepo, error) {
	userURL := fmt.Sprintf("%s/api/v1/users/%s/repos?limit=20&order=updated", apiBase, url.PathEscape(owner))
	orgURL := fmt.Sprintf("%s/api/v1/orgs/%s/repos?limit=20&order=updated", apiBase, url.PathEscape(owner))
	repos, errU := c.fetchReposURL(ctx, userURL, token, opts, directiveID)
	if errU == nil && len(repos) > 0 {
		return repos, nil
	}
	reposO, errO := c.fetchReposURL(ctx, orgURL, token, opts, directiveID)
	if errO != nil {
		if errU != nil {
			return nil, errU
		}
		return nil, errO
	}
	if len(reposO) == 0 && errU != nil {
		return nil, errU
	}
	return reposO, nil
}

func (c GiteaCollector) fetchReposURL(ctx context.Context, rawURL, token string, opts *CollectOpts, directiveID string) ([]giteaRepo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/json")
	res, body, err := doAndReadHTTP(c.client(), req, 4<<20, opts, directiveID)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("gitea %s: %s — %s", res.Status, rawURL, strings.TrimSpace(string(body)))
	}
	var repos []giteaRepo
	if err := json.Unmarshal(body, &repos); err != nil {
		return nil, fmt.Errorf("parse gitea repos: %w", err)
	}
	return repos, nil
}
