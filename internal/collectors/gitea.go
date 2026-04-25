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

func (c GiteaCollector) Collect(ctx context.Context, directive userdata.Directive, opts *CollectOpts) ([]StatusItem, error) {
	base := strings.TrimSpace(directive.Config["base_url"])
	owner := strings.TrimSpace(directive.Target["owner"])
	if base == "" {
		return nil, fmt.Errorf("config.base_url is required")
	}
	if owner == "" {
		return nil, fmt.Errorf("target.owner is required")
	}
	tokenKey := directive.CredentialRefs["token"]
	token := userdata.ResolveEnv(opts.UserdataDir, tokenKey)
	if token == "" {
		return nil, fmt.Errorf("gitea token missing (set %s in environment or userdata/.env)", tokenKey)
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid base_url")
	}
	apiBase := strings.TrimRight(u.String(), "/")

	repos, err := c.fetchReposForOwner(ctx, apiBase, owner, token)
	if err != nil {
		return nil, err
	}
	now := c.Clock()
	items := make([]StatusItem, 0, len(repos))
	for _, r := range repos {
		title := r.FullName
		if title == "" {
			title = r.Name
		}
		summary := fmt.Sprintf("updated=%s branch=%s", r.Updated, r.DefaultBranch)
		if r.Private {
			summary += " private=true"
		}
		items = append(items, StatusItem{
			DirectiveID: directive.ID,
			ProjectID:   directive.ProjectID,
			Source:      "gitea",
			Kind:        "repository",
			Title:       title,
			Summary:     summary,
			URL:         r.HTMLURL,
			Severity:    "info",
			ObservedAt:  now,
			Fields: map[string]string{
				"name":             r.Name,
				"full_name":        r.FullName,
				"updated_at":       r.Updated,
				"default_branch":   r.DefaultBranch,
				"private":          fmt.Sprintf("%t", r.Private),
				"owner":            owner,
				"gitea_base":       apiBase,
				"credential_scope": tokenKey,
			},
		})
	}
	if len(items) == 0 {
		return []StatusItem{{
			DirectiveID: directive.ID,
			ProjectID:   directive.ProjectID,
			Source:      "gitea",
			Kind:        "repository_list",
			Title:       directive.Name,
			Summary:     fmt.Sprintf("No repositories returned for owner %q (check owner name and token access).", owner),
			Severity:    "info",
			ObservedAt:  now,
		}}, nil
	}
	return items, nil
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
