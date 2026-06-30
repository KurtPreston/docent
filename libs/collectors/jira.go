package collectors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kurt/slakkr-ai/libs/config/userdata"
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
	Issues []jiraIssue `json:"issues"`
}

type jiraIssue struct {
	Key    string `json:"key"`
	Fields struct {
		Summary string `json:"summary"`
		Status  struct {
			Name string `json:"name"`
		} `json:"status"`
		Priority struct {
			Name string `json:"name"`
		} `json:"priority"`
		Updated  string `json:"updated"`
		Assignee struct {
			Name         string `json:"name"`
			AccountID    string `json:"accountId"`
			EmailAddress string `json:"emailAddress"`
		} `json:"assignee"`
		Reporter struct {
			Name         string `json:"name"`
			AccountID    string `json:"accountId"`
			EmailAddress string `json:"emailAddress"`
		} `json:"reporter"`
	} `json:"fields"`
}

// CollectEvents runs JQL restricted to issues updated on or after opts.Since,
// emitting one issue_activity signal per matching issue.
//
// Scope shapes the user clause inside the composed JQL:
//   - ScopeSelf: assignee or reporter is the current user.
//   - ScopeInvolved (default): assignee, reporter, or watcher is the user.
//   - ScopeAll: ScopeInvolved expanded with `project in (followed_projects)`
//     when config.followed_projects is set; falls back to involved when no
//     projects are configured.
func (c JiraCollector) CollectEvents(ctx context.Context, directive userdata.Directive, opts *CollectOpts) ([]StatusItem, error) {
	since := time.Time{}
	if opts != nil {
		since = opts.Since
	}
	now := c.Clock()
	if opts != nil {
		now = opts.windowEnd(c.Clock)
	}
	scope := opts.EffectiveScope()
	followedProjects := parseFollowedList(directive.Config["followed_projects"])
	jql := buildJiraActivityJQL(strings.TrimSpace(directive.Config["query"]), since, scope, followedProjects)
	parsed, base, err := c.runJiraSearch(ctx, directive, opts, jql)
	if err != nil {
		return nil, err
	}
	email := strings.TrimSpace(directive.Config["email"])
	items := make([]StatusItem, 0, len(parsed.Issues))
	for _, iss := range parsed.Issues {
		obs, err := jiraParseUpdated(iss.Fields.Updated)
		if err != nil || obs.Before(since) || obs.After(now) {
			continue
		}
		items = append(items, buildJiraItem(directive, base, iss, "issue_activity", obs, jiraIsSelf(iss, scope, email)))
	}
	return items, nil
}

// CollectState runs JQL for the current set of issues matching the directive's
// query and scope with no time-window filter, emitting one issue signal per
// matching issue. This is the "what is true right now" view (e.g. tickets
// assigned to me in a given status), independent of when they last changed.
func (c JiraCollector) CollectState(ctx context.Context, directive userdata.Directive, opts *CollectOpts) ([]StatusItem, error) {
	scope := opts.EffectiveScope()
	followedProjects := parseFollowedList(directive.Config["followed_projects"])
	jql := buildJiraStateJQL(strings.TrimSpace(directive.Config["query"]), scope, followedProjects)
	parsed, base, err := c.runJiraSearch(ctx, directive, opts, jql)
	if err != nil {
		return nil, err
	}
	email := strings.TrimSpace(directive.Config["email"])
	items := make([]StatusItem, 0, len(parsed.Issues))
	for _, iss := range parsed.Issues {
		obs, perr := jiraParseUpdated(iss.Fields.Updated)
		if perr != nil {
			obs = c.Clock()
		}
		items = append(items, buildJiraItem(directive, base, iss, "issue", obs, jiraIsSelf(iss, scope, email)))
	}
	return items, nil
}

// runJiraSearch resolves credentials, executes the JQL search, and returns the
// parsed result plus the trimmed base URL. Auth prefers a Personal Access
// Token (Bearer); it falls back to email + API token (Basic) for legacy
// Atlassian Cloud configs.
func (c JiraCollector) runJiraSearch(ctx context.Context, directive userdata.Directive, opts *CollectOpts, jql string) (jiraSearchResult, string, error) {
	var parsed jiraSearchResult
	base := strings.TrimSpace(directive.Config["base_url"])
	if base == "" {
		return parsed, "", fmt.Errorf("config.base_url is required")
	}
	userdataDir := ""
	if opts != nil {
		userdataDir = opts.UserdataDir
	}
	patKey := strings.TrimSpace(directive.CredentialRefs["pat"])
	tokenKey := strings.TrimSpace(directive.CredentialRefs["token"])
	email := strings.TrimSpace(directive.Config["email"])
	var (
		secret    string
		useBearer bool
	)
	switch {
	case patKey != "":
		secret = userdata.ResolveEnv(userdataDir, patKey)
		if secret == "" {
			return parsed, "", fmt.Errorf("jira pat env %q is empty", patKey)
		}
		useBearer = true
	case tokenKey != "":
		secret = userdata.ResolveEnv(userdataDir, tokenKey)
		if secret == "" {
			return parsed, "", fmt.Errorf("jira token env %q is empty", tokenKey)
		}
		if email == "" {
			return parsed, "", fmt.Errorf("config.email is required for Jira API token (Basic) auth")
		}
	default:
		return parsed, "", fmt.Errorf("jira credential missing (set credential_refs.pat in userdata/.env)")
	}
	api := strings.TrimRight(base, "/") + "/rest/api/2/search"
	q := url.Values{}
	q.Set("jql", jql)
	q.Set("maxResults", "50")
	q.Set("fields", "summary,status,priority,updated,assignee,reporter")
	reqURL := api + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return parsed, "", err
	}
	if useBearer {
		req.Header.Set("Authorization", "Bearer "+secret)
	} else {
		req.SetBasicAuth(email, secret)
	}
	req.Header.Set("Accept", "application/json")
	res, body, err := doAndReadHTTP(c.client(), req, 4<<20, opts, directive.ID)
	if err != nil {
		return parsed, "", err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return parsed, "", fmt.Errorf("jira search %s: %s", res.Status, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return parsed, "", fmt.Errorf("parse jira response: %w", err)
	}
	return parsed, strings.TrimRight(base, "/"), nil
}

// jiraIsSelf applies the IsSelf rules:
//   - self/involved: JQL guaranteed the user matched (assignee / reporter /
//     watcher), so mark true.
//   - all: only true when assignee or reporter email matches the Basic-auth
//     identity; otherwise the row likely came from the followed-projects
//     expansion and isn't the user's own activity.
func jiraIsSelf(iss jiraIssue, scope Scope, email string) bool {
	if scope != ScopeAll {
		return true
	}
	if email != "" {
		if strings.EqualFold(iss.Fields.Assignee.EmailAddress, email) ||
			strings.EqualFold(iss.Fields.Reporter.EmailAddress, email) {
			return true
		}
	}
	return false
}

func buildJiraItem(directive userdata.Directive, base string, iss jiraIssue, kind string, obs time.Time, isSelf bool) StatusItem {
	priority := iss.Fields.Priority.Name
	summary := fmt.Sprintf("status=%s priority=%s updated=%s", iss.Fields.Status.Name, priority, iss.Fields.Updated)
	sev := "info"
	if strings.EqualFold(iss.Fields.Status.Name, "blocked") || strings.Contains(strings.ToLower(iss.Fields.Status.Name), "block") {
		sev = "warning"
	}
	return StatusItem{
		DirectiveID: directive.ID,
		Source:      "jira",
		Kind:        kind,
		Title:       iss.Key + " " + iss.Fields.Summary,
		Summary:     summary,
		URL:         base + "/browse/" + iss.Key,
		Severity:    sev,
		ObservedAt:  obs.UTC(),
		IsSelf:      isSelf,
		Fields: map[string]string{
			"key":      iss.Key,
			"status":   iss.Fields.Status.Name,
			"priority": priority,
			"updated":  iss.Fields.Updated,
			"assignee": iss.Fields.Assignee.Name,
			"reporter": iss.Fields.Reporter.Name,
		},
	}
}

// ValidateDirective checks base_url is well-formed, the configured credential
// (PAT preferred, else email+token) resolves to non-empty values, and the
// credential can reach `/rest/api/2/myself`.
func (c JiraCollector) ValidateDirective(ctx context.Context, directive userdata.Directive, opts *ValidateOpts) []ValidationIssue {
	base := strings.TrimSpace(directive.Config["base_url"])
	if base == "" {
		return []ValidationIssue{{
			Field:       "config.base_url",
			Message:     "Jira base_url is required",
			Remediation: "set config.base_url to your Jira URL (e.g. https://example.atlassian.net)",
		}}
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return []ValidationIssue{{
			Field:       "config.base_url",
			Message:     fmt.Sprintf("Jira base_url %q is not a valid URL", base),
			Remediation: "use the form https://example.atlassian.net",
		}}
	}
	userdataDir := ""
	if opts != nil {
		userdataDir = opts.UserdataDir
	}
	patKey := strings.TrimSpace(directive.CredentialRefs["pat"])
	tokenKey := strings.TrimSpace(directive.CredentialRefs["token"])
	email := strings.TrimSpace(directive.Config["email"])
	var (
		secret    string
		useBearer bool
	)
	switch {
	case patKey != "":
		secret = userdata.ResolveEnv(userdataDir, patKey)
		if secret == "" {
			return []ValidationIssue{{
				Field:       "credential_refs.pat",
				Message:     fmt.Sprintf("Jira PAT env %q is empty", patKey),
				Remediation: fmt.Sprintf("set %s in your environment or in %s/.env", patKey, userdataDir),
			}}
		}
		useBearer = true
	case tokenKey != "":
		secret = userdata.ResolveEnv(userdataDir, tokenKey)
		if secret == "" {
			return []ValidationIssue{{
				Field:       "credential_refs.token",
				Message:     fmt.Sprintf("Jira API token env %q is empty", tokenKey),
				Remediation: fmt.Sprintf("set %s in your environment or in %s/.env", tokenKey, userdataDir),
			}}
		}
		if email == "" {
			return []ValidationIssue{{
				Field:       "config.email",
				Message:     "Jira Basic auth requires config.email",
				Remediation: "add config.email to the directive, or switch to credential_refs.pat",
			}}
		}
	default:
		return []ValidationIssue{{
			Field:       "credential_refs",
			Message:     "no Jira credentials configured",
			Remediation: "add credential_refs.pat (preferred) or credential_refs.token + config.email",
		}}
	}

	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	api := strings.TrimRight(base, "/") + "/rest/api/2/myself"
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, api, nil)
	if err != nil {
		return []ValidationIssue{{
			Field:       "auth",
			Message:     fmt.Sprintf("Jira auth probe request build failed: %v", err),
			Remediation: "verify config.base_url",
		}}
	}
	if useBearer {
		req.Header.Set("Authorization", "Bearer "+secret)
	} else {
		req.SetBasicAuth(email, secret)
	}
	req.Header.Set("Accept", "application/json")
	res, body, err := doAndReadHTTP(c.client(), req, 1<<20, nil, directive.ID)
	if err != nil {
		return []ValidationIssue{{
			Field:       "auth",
			Message:     fmt.Sprintf("Jira auth probe failed: %v", err),
			Remediation: fmt.Sprintf("verify %s is reachable", base),
		}}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return []ValidationIssue{{
			Field:       "auth",
			Message:     fmt.Sprintf("Jira auth probe returned %s", res.Status),
			Remediation: "regenerate the PAT/API token and update userdata/.env",
		}}
	}
	// Some Jira instances (especially behind SSO) return 200 OK with an HTML
	// login/landing page when an unauthenticated request slips past the API
	// auth layer. Detect non-JSON bodies so we surface the real problem here
	// instead of letting collection fail with `invalid character '<'`.
	if !json.Valid(body) {
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > 120 {
			snippet = snippet[:120] + "..."
		}
		return []ValidationIssue{{
			Field:       "auth",
			Message:     fmt.Sprintf("Jira auth probe returned %s but body is not JSON (likely SSO login or expired credential). Got: %s", res.Status, snippet),
			Remediation: "regenerate the PAT, ensure it has API access (not just web SSO), and update userdata/.env",
		}}
	}
	return nil
}

// buildJiraActivityJQL composes the JQL that the directive runs. The
// shape is:
//
//	[(<user-supplied query>) AND] <scope clause> AND updated >= "<date>" ORDER BY updated DESC
//
// The scope clause is:
//   - ScopeSelf: assignee = currentUser() OR reporter = currentUser()
//   - ScopeInvolved (default): adds watcher = currentUser()
//   - ScopeAll: ScopeInvolved plus `project in (P1, P2, ...)` when
//     followedProjects is non-empty; falls back to ScopeInvolved otherwise.
//
// When the user supplies config.query, it is preserved as a sub-clause so
// the caller can scope freely (project, label, etc.) while the scope
// expansion still controls which actors count.
func buildJiraActivityJQL(userQuery string, since time.Time, scope Scope, followedProjects []string) string {
	date := since.Format("2006-01-02")
	scopeClause := buildJiraScopeClause(scope, followedProjects)
	base := strings.TrimSpace(userQuery)
	if base == "" {
		return fmt.Sprintf(`%s AND updated >= "%s" ORDER BY updated DESC`, scopeClause, date)
	}
	return fmt.Sprintf(`(%s) AND %s AND updated >= "%s" ORDER BY updated DESC`, base, scopeClause, date)
}

// buildJiraStateJQL composes the JQL for the current-state view: the scope
// clause (and optional user query) ordered by recency, with no `updated >=`
// window filter so issues that haven't changed recently still appear.
func buildJiraStateJQL(userQuery string, scope Scope, followedProjects []string) string {
	scopeClause := buildJiraScopeClause(scope, followedProjects)
	base := strings.TrimSpace(userQuery)
	if base == "" {
		return scopeClause + " ORDER BY updated DESC"
	}
	return fmt.Sprintf("(%s) AND %s ORDER BY updated DESC", base, scopeClause)
}

func buildJiraScopeClause(scope Scope, followedProjects []string) string {
	switch scope {
	case ScopeSelf:
		return "(assignee = currentUser() OR reporter = currentUser())"
	case ScopeAll:
		if len(followedProjects) == 0 {
			return "(assignee = currentUser() OR reporter = currentUser() OR watcher = currentUser())"
		}
		quoted := make([]string, 0, len(followedProjects))
		for _, p := range followedProjects {
			quoted = append(quoted, fmt.Sprintf("%q", p))
		}
		return fmt.Sprintf("(project in (%s) OR assignee = currentUser() OR reporter = currentUser() OR watcher = currentUser())", strings.Join(quoted, ", "))
	default: // ScopeInvolved / ScopeUnset
		return "(assignee = currentUser() OR reporter = currentUser() OR watcher = currentUser())"
	}
}

func jiraParseUpdated(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty updated")
	}
	layouts := []string{
		"2006-01-02T15:04:05.000-0700",
		time.RFC3339,
		"2006-01-02T15:04:05.000Z",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	if len(s) >= 19 {
		if t, err := time.Parse("2006-01-02T15:04:05", s[:19]); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("parse jira updated %q", s)
}
