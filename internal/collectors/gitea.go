package collectors

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

func (c GiteaCollector) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// Collect keeps repositories whose updated_at is on or after opts.Since.
// When target.owner is empty, the authenticated user is resolved via
// /api/v1/user and used as the owner so the directive defaults to the
// caller's own repositories.
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
		login, err := c.fetchAuthenticatedLogin(ctx, apiBase, token)
		if err != nil {
			return nil, fmt.Errorf("resolve authenticated gitea user: %w", err)
		}
		owner = login
	}
	repos, err := c.fetchReposForOwner(ctx, apiBase, owner, token)
	if err != nil {
		return nil, err
	}
	since := time.Time{}
	if opts != nil {
		since = opts.Since
	}
	now := c.Clock()
	if opts != nil {
		now = opts.windowEnd(c.Clock)
	}
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
			IsSelf:      true,
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
	return items, nil
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
	if _, err := c.fetchAuthenticatedLogin(probeCtx, apiBase, token); err != nil {
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

func (c GiteaCollector) fetchAuthenticatedLogin(ctx context.Context, apiBase, token string) (string, error) {
	rawURL := apiBase + "/api/v1/user"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/json")
	res, err := c.client().Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
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

func (c GiteaCollector) fetchReposForOwner(ctx context.Context, apiBase, owner, token string) ([]giteaRepo, error) {
	userURL := fmt.Sprintf("%s/api/v1/users/%s/repos?limit=20&order=updated", apiBase, url.PathEscape(owner))
	orgURL := fmt.Sprintf("%s/api/v1/orgs/%s/repos?limit=20&order=updated", apiBase, url.PathEscape(owner))
	repos, errU := c.fetchReposURL(ctx, userURL, token)
	if errU == nil && len(repos) > 0 {
		return repos, nil
	}
	reposO, errO := c.fetchReposURL(ctx, orgURL, token)
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

func (c GiteaCollector) fetchReposURL(ctx context.Context, rawURL, token string) ([]giteaRepo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/json")
	res, err := c.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
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
