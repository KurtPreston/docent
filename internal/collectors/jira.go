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

type JiraCollector struct {
	Clock func() time.Time
	HTTP  *http.Client
}

func (c JiraCollector) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

type jiraSearchResult struct {
	Issues []struct {
		Key    string `json:"key"`
		Fields struct {
			Summary string `json:"summary"`
			Status  struct {
				Name string `json:"name"`
			} `json:"status"`
			Priority struct {
				Name string `json:"name"`
			} `json:"priority"`
			Updated string `json:"updated"`
		} `json:"fields"`
	} `json:"issues"`
}

func (c JiraCollector) Collect(ctx context.Context, directive userdata.Directive, opts *CollectOpts) ([]StatusItem, error) {
	base := strings.TrimSpace(directive.Config["base_url"])
	jql := strings.TrimSpace(directive.Config["query"])
	if base == "" {
		return nil, fmt.Errorf("config.base_url is required")
	}
	if jql == "" {
		return nil, fmt.Errorf("config.query (JQL) is required")
	}
	email := strings.TrimSpace(directive.Config["email"])
	tokenKey := directive.CredentialRefs["token"]
	if tokenKey == "" {
		tokenKey = directive.CredentialRefs["pat"]
	}
	token := userdata.ResolveEnv(opts.UserdataDir, tokenKey)
	if token == "" {
		return nil, fmt.Errorf("jira token missing (set credential_refs token in userdata/.env)")
	}
	if email == "" {
		return nil, fmt.Errorf("config.email is required for Jira Cloud API token auth")
	}
	api := strings.TrimRight(base, "/") + "/rest/api/3/search"
	q := url.Values{}
	q.Set("jql", jql)
	q.Set("maxResults", "20")
	q.Set("fields", "summary,status,priority,updated")
	reqURL := api + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(email, token)
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
		return nil, fmt.Errorf("jira search %s: %s", res.Status, strings.TrimSpace(string(body)))
	}
	var parsed jiraSearchResult
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse jira response: %w", err)
	}
	now := c.Clock()
	items := make([]StatusItem, 0, len(parsed.Issues))
	for _, iss := range parsed.Issues {
		priority := iss.Fields.Priority.Name
		summary := fmt.Sprintf("status=%s priority=%s updated=%s", iss.Fields.Status.Name, priority, iss.Fields.Updated)
		sev := "info"
		if strings.EqualFold(iss.Fields.Status.Name, "blocked") || strings.Contains(strings.ToLower(iss.Fields.Status.Name), "block") {
			sev = "warning"
		}
		web := strings.TrimRight(base, "/") + "/browse/" + iss.Key
		items = append(items, StatusItem{
			DirectiveID: directive.ID,
			ProjectID:   directive.ProjectID,
			Source:      "jira",
			Kind:        "issue",
			Title:       iss.Key + " " + iss.Fields.Summary,
			Summary:     summary,
			URL:         web,
			Severity:    sev,
			ObservedAt:  now,
			Fields: map[string]string{
				"key":      iss.Key,
				"status":   iss.Fields.Status.Name,
				"priority": priority,
				"updated":  iss.Fields.Updated,
			},
		})
	}
	if len(items) == 0 {
		items = append(items, StatusItem{
			DirectiveID: directive.ID,
			ProjectID:   directive.ProjectID,
			Source:      "jira",
			Kind:        "issue_list",
			Title:       directive.Name,
			Summary:     "JQL returned no issues.",
			Severity:    "info",
			ObservedAt:  now,
		})
	}
	return items, nil
}
