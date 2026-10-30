package collectors

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KurtPreston/docent/libs/config/userdata"
)

// AzurePipelinesCollector watches Azure DevOps pipeline runs and emits one
// `test_failure` signal per spec file that failed or flaked in a run.
//
// Flakes are the point: CI retries, so a spec that fails and then passes on
// retry leaves the build green and the GitHub check reporting success. Only
// the run's log says it flaked, so the collector reads the logs rather than
// trusting the build result.
//
// It only understands Playwright's `list` reporter output (plus the `github`
// reporter's `::error` annotations); runs with other test runners yield nothing.
type AzurePipelinesCollector struct {
	Clock func() time.Time
	HTTP  *http.Client
	// Token overrides how a bearer token is obtained when no PAT is
	// configured (tests). Nil shells out to `az`.
	Token func(ctx context.Context, opts *CollectOpts, directiveID string) (string, error)
}

const (
	adoDefaultBaseURL = "https://dev.azure.com"
	adoAPIVersion     = "7.1"
	// adoResourceID is Azure DevOps' Entra resource, the audience `az`
	// mints tokens for.
	adoResourceID = "499b84ac-1321-427f-aa17-267ca6975798"
	// adoDefaultPlaywrightTask matches the timeline tasks whose logs are parsed
	// when config.playwright_task is unset.
	adoDefaultPlaywrightTask = `(?i)playwright|e2e|test`
	// adoMaxBuildsPerCollect bounds one poll; ADO's own page size.
	adoMaxBuildsPerCollect = 100
	// adoMaxErrorsLen caps the combined `errors` field of one signal.
	adoMaxErrorsLen = 8000
)

type adoConfig struct {
	baseURL      string
	org          string
	project      string
	definitions  []int
	repo         string
	taskRE       *regexp.Regexp
	branchPrefix string
	baseBranch   string
}

type adoBuild struct {
	ID            int       `json:"id"`
	BuildNumber   string    `json:"buildNumber"`
	Status        string    `json:"status"`
	Result        string    `json:"result"`
	Reason        string    `json:"reason"`
	SourceBranch  string    `json:"sourceBranch"`
	SourceVersion string    `json:"sourceVersion"`
	FinishTime    time.Time `json:"finishTime"`
	Definition    struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"definition"`
	Repository struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		URL  string `json:"url"`
	} `json:"repository"`
	TriggerInfo  map[string]string `json:"triggerInfo"`
	RequestedFor struct {
		DisplayName string `json:"displayName"`
		UniqueName  string `json:"uniqueName"`
	} `json:"requestedFor"`
}

type adoTimelineRecord struct {
	Type   string `json:"type"`
	Name   string `json:"name"`
	Result string `json:"result"`
	Log    *struct {
		ID int `json:"id"`
	} `json:"log"`
}

func (c AzurePipelinesCollector) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	// Don't follow ADO's sign-in redirect; get() reports it as an auth error
	return &http.Client{
		Timeout: 60 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (c AzurePipelinesCollector) clock() time.Time {
	if c.Clock != nil {
		return c.Clock()
	}
	return time.Now()
}

// parseADOConfig validates the directive's config map.
func parseADOConfig(d userdata.Directive) (adoConfig, error) {
	cfg := adoConfig{
		baseURL:      strings.TrimRight(strings.TrimSpace(d.Config["base_url"]), "/"),
		org:          strings.TrimSpace(d.Config["organization"]),
		project:      strings.TrimSpace(d.Config["project"]),
		repo:         strings.Trim(strings.TrimSpace(d.Config["repo"]), "/"),
		branchPrefix: strings.TrimSpace(d.Config["branch_prefix"]),
		baseBranch:   strings.TrimSpace(d.Config["base_branch"]),
	}
	if cfg.baseURL == "" {
		cfg.baseURL = adoDefaultBaseURL
	}
	if cfg.branchPrefix == "" {
		cfg.branchPrefix = "e2e-fix-"
	}
	if cfg.baseBranch == "" {
		cfg.baseBranch = "main"
	}
	if cfg.org == "" {
		return cfg, fmt.Errorf("config.organization is required")
	}
	if cfg.project == "" {
		return cfg, fmt.Errorf("config.project is required")
	}

	// Definitions are a comma/space separated list of numeric pipeline IDs
	for _, raw := range parseFollowedList(d.Config["definitions"]) {
		id, err := strconv.Atoi(raw)
		if err != nil || id <= 0 {
			return cfg, fmt.Errorf("config.definitions: %q is not a pipeline definition id", raw)
		}
		cfg.definitions = append(cfg.definitions, id)
	}
	if len(cfg.definitions) == 0 {
		return cfg, fmt.Errorf("config.definitions is required")
	}

	// playwright_task narrows which timeline tasks' logs are fetched and parsed
	expr := strings.TrimSpace(d.Config["playwright_task"])
	if expr == "" {
		expr = adoDefaultPlaywrightTask
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return cfg, fmt.Errorf("config.playwright_task: %w", err)
	}
	cfg.taskRE = re
	return cfg, nil
}

// projectURL is the API/web root for the configured project.
func (cfg adoConfig) projectURL() string {
	return cfg.baseURL + "/" + url.PathEscape(cfg.org) + "/" + url.PathEscape(cfg.project)
}

// CollectEvents emits failed/flaky spec signals for runs that finished in the
// window. Only completed runs are considered, so each run is read once.
func (c AzurePipelinesCollector) CollectEvents(ctx context.Context, directive userdata.Directive, opts *CollectOpts) ([]StatusItem, error) {
	// CI runs are nobody's personal activity; keep them out of self reports
	if opts.EffectiveScope() == ScopeSelf {
		return nil, nil
	}
	cfg, err := parseADOConfig(directive)
	if err != nil {
		return nil, err
	}
	auth, err := c.authHeader(ctx, directive, opts)
	if err != nil {
		return nil, err
	}

	// Window: builds that finished since the watermark (or a day back)
	now := c.clock()
	if opts != nil {
		now = opts.windowEnd(c.clock)
	}
	since := now.Add(-24 * time.Hour)
	if opts != nil && !opts.Since.IsZero() {
		since = opts.Since
	}
	builds, err := c.listBuilds(ctx, cfg, auth, since, opts, directive.ID)
	if err != nil {
		return nil, err
	}

	// Read each run's test logs and turn its failures into signals
	var items []StatusItem
	for i, b := range builds {
		if b.FinishTime.Before(since) || b.FinishTime.After(now) {
			continue
		}
		reportProgress(opts, DirectiveProgress{
			DirectiveID: directive.ID,
			Description: directive.Name,
			Status:      "running",
			Detail:      fmt.Sprintf("%s #%s", b.Definition.Name, b.BuildNumber),
			Completed:   i,
			Total:       len(builds),
		})
		result, logURLs, err := c.buildTestResult(ctx, cfg, auth, b, opts, directive.ID)
		if err != nil {
			return nil, err
		}
		items = append(items, adoBuildSignals(directive, cfg, b, result, logURLs)...)
	}
	return items, nil
}

// buildTestResult fetches and parses the logs of a run's test tasks.
func (c AzurePipelinesCollector) buildTestResult(ctx context.Context, cfg adoConfig, auth string, b adoBuild, opts *CollectOpts, directiveID string) (PlaywrightResult, []string, error) {
	buildURL := fmt.Sprintf("%s/_apis/build/builds/%d", cfg.projectURL(), b.ID)
	var timeline struct {
		Records []adoTimelineRecord `json:"records"`
	}
	if err := c.getJSON(ctx, buildURL+"/timeline?api-version="+adoAPIVersion, auth, &timeline, opts, directiveID); err != nil {
		return PlaywrightResult{}, nil, err
	}

	// Merge results across every matching task (a pipeline may shard tests)
	merged := PlaywrightResult{Errors: map[string]string{}, Summarized: true}
	var logURLs []string
	for _, rec := range timeline.Records {
		if rec.Type != "Task" || rec.Log == nil || !cfg.taskRE.MatchString(rec.Name) {
			continue
		}
		logURL := fmt.Sprintf("%s/logs/%d?api-version=%s", buildURL, rec.Log.ID, adoAPIVersion)
		text, err := c.getText(ctx, logURL, auth, opts, directiveID)
		if err != nil {
			return PlaywrightResult{}, nil, err
		}
		r := ParsePlaywrightLog(text)
		if r.Empty() {
			continue
		}
		logURLs = append(logURLs, logURL)
		merged.Failed = append(merged.Failed, r.Failed...)
		merged.Flaky = append(merged.Flaky, r.Flaky...)
		merged.Summarized = merged.Summarized && r.Summarized
		for k, v := range r.Errors {
			if _, ok := merged.Errors[k]; !ok {
				merged.Errors[k] = v
			}
		}
	}
	return merged, logURLs, nil
}

// adoBuildSignals turns one run's failures into a signal per spec file.
func adoBuildSignals(directive userdata.Directive, cfg adoConfig, b adoBuild, result PlaywrightResult, logURLs []string) []StatusItem {
	if result.Empty() {
		return nil
	}

	// Context shared by every spec signal from this run
	repo := cfg.repo
	if repo == "" && strings.EqualFold(b.Repository.Type, "GitHub") {
		repo = b.Repository.ID
	}
	prNumber := strings.TrimSpace(b.TriggerInfo["pr.number"])
	if prNumber == "" {
		prNumber = adoPRNumberFromRef(b.SourceBranch)
	}
	baseBranch := cfg.baseBranch
	if strings.HasPrefix(b.SourceBranch, "refs/heads/") {
		baseBranch = strings.TrimPrefix(b.SourceBranch, "refs/heads/")
	}
	commit := b.SourceVersion
	if sha := strings.TrimSpace(b.TriggerInfo["pr.sourceSha"]); sha != "" {
		commit = sha
	}
	author := b.TriggerInfo["pr.sender.name"]
	if author == "" {
		author = b.RequestedFor.DisplayName
	}
	buildURL := fmt.Sprintf("%s/_build/results?buildId=%d", cfg.projectURL(), b.ID)
	pipeline := b.Definition.Name
	if pipeline == "" {
		pipeline = fmt.Sprintf("definition %d", b.Definition.ID)
	}
	where := "on " + strings.TrimPrefix(b.SourceBranch, "refs/heads/")
	if prNumber != "" {
		where = "on PR #" + prNumber
	}

	// One signal per spec file, so each fix is scoped to a single spec
	var items []StatusItem
	for _, spec := range result.BySpecFile() {
		outcome := "flaky"
		if len(spec.Failed) > 0 {
			outcome = "failed"
		}
		fields := map[string]string{
			"repo":           repo,
			"branch":         cfg.branchPrefix + adoSpecSlug(spec.File) + "-" + b.FinishTime.UTC().Format("20060102"),
			"base_branch":    baseBranch,
			"build_id":       strconv.Itoa(b.ID),
			"build_number":   b.BuildNumber,
			"build_url":      buildURL,
			"pipeline":       pipeline,
			"definition_id":  strconv.Itoa(b.Definition.ID),
			"result":         b.Result,
			"reason":         b.Reason,
			"source_branch":  b.SourceBranch,
			"commit":         commit,
			"requested_by":   b.RequestedFor.UniqueName,
			"spec_file":      spec.File,
			"outcome":        outcome,
			"failed_tests":   playwrightTestList(spec.Failed),
			"flaky_tests":    playwrightTestList(spec.Flaky),
			"errors":         playwrightErrors(result.Errors, spec),
			"test_log_urls":  strings.Join(logURLs, "\n"),
			"summary_parsed": strconv.FormatBool(result.Summarized),
		}

		// PR context, when the run was for a pull request
		if prNumber != "" {
			fields["pr_number"] = prNumber
			fields["pr_url"] = adoPRURL(b.Repository.URL, repo, prNumber)
			fields["pr_title"] = b.TriggerInfo["pr.title"]
			fields["pr_head_branch"] = b.TriggerInfo["pr.sourceBranch"]
			fields["pr_author"] = b.TriggerInfo["pr.sender.name"]
		}

		n := len(spec.Failed) + len(spec.Flaky)
		items = append(items, StatusItem{
			DirectiveID: directive.ID,
			Repository:  repo,
			Source:      "azure-pipelines",
			Kind:        "test_failure",
			Title:       fmt.Sprintf("%s: %s %s", pipeline, outcome, spec.File),
			Summary:     fmt.Sprintf("Run %s %s: %d %s test(s) in %s", b.BuildNumber, where, n, outcome, spec.File),
			URL:         buildURL,
			Severity:    map[string]string{"failed": "error", "flaky": "warning"}[outcome],
			ObservedAt:  b.FinishTime,
			StableID:    fmt.Sprintf("azure-pipelines:%s/%s:%d:%s", cfg.org, cfg.project, b.ID, spec.File),
			Author:      author,
			Fields:      fields,
		})
	}
	return items
}

var adoPullRefRE = regexp.MustCompile(`^refs/pull/(\d+)/`)

func adoPRNumberFromRef(ref string) string {
	if m := adoPullRefRE.FindStringSubmatch(ref); m != nil {
		return m[1]
	}
	return ""
}

// adoPRURL builds the GitHub PR link from the run's repository URL, which ADO
// reports with an explicit port (https://github.com:443/owner/name).
func adoPRURL(repoURL, repo, number string) string {
	if repo == "" {
		return ""
	}
	host := "github.com"
	if u, err := url.Parse(repoURL); err == nil && u.Hostname() != "" {
		host = u.Hostname()
	}
	return "https://" + host + "/" + repo + "/pull/" + number
}

var adoSlugRE = regexp.MustCompile(`[^a-z0-9]+`)

// adoSpecSlug reduces "e2e/checkout-flow.spec.ts" to "checkout-flow" for a branch name.
func adoSpecSlug(file string) string {
	base := path.Base(file)
	if i := strings.Index(base, "."); i > 0 {
		base = base[:i]
	}
	slug := strings.Trim(adoSlugRE.ReplaceAllString(strings.ToLower(base), "-"), "-")
	if slug == "" {
		slug = "spec"
	}
	return slug
}

func playwrightTestList(tests []PlaywrightTest) string {
	names := make([]string, 0, len(tests))
	for _, t := range tests {
		names = append(names, t.Name())
	}
	return strings.Join(names, "\n")
}

// playwrightErrors renders each test's first error under a heading.
func playwrightErrors(errors map[string]string, spec PlaywrightSpecFailures) string {
	var b strings.Builder
	for _, t := range append(append([]PlaywrightTest{}, spec.Failed...), spec.Flaky...) {
		msg, ok := errors[t.Name()]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "### %s\n%s\n\n", t.Name(), msg)
	}
	return truncateRunes(strings.TrimSpace(b.String()), adoMaxErrorsLen)
}

// listBuilds returns completed runs of the configured definitions that
// finished at or after since, newest first.
func (c AzurePipelinesCollector) listBuilds(ctx context.Context, cfg adoConfig, auth string, since time.Time, opts *CollectOpts, directiveID string) ([]adoBuild, error) {
	defs := make([]string, len(cfg.definitions))
	for i, d := range cfg.definitions {
		defs[i] = strconv.Itoa(d)
	}
	q := url.Values{}
	q.Set("definitions", strings.Join(defs, ","))
	q.Set("statusFilter", "completed")
	q.Set("queryOrder", "finishTimeDescending")
	q.Set("minTime", since.UTC().Format(time.RFC3339))
	q.Set("$top", strconv.Itoa(adoMaxBuildsPerCollect))
	q.Set("api-version", adoAPIVersion)
	var page struct {
		Value []adoBuild `json:"value"`
	}
	if err := c.getJSON(ctx, cfg.projectURL()+"/_apis/build/builds?"+q.Encode(), auth, &page, opts, directiveID); err != nil {
		return nil, err
	}
	return page.Value, nil
}

func (c AzurePipelinesCollector) getJSON(ctx context.Context, rawURL, auth string, out any, opts *CollectOpts, directiveID string) error {
	body, err := c.get(ctx, rawURL, auth, "application/json", opts, directiveID)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("parse azure devops response from %s: %w", rawURL, err)
	}
	return nil
}

func (c AzurePipelinesCollector) getText(ctx context.Context, rawURL, auth string, opts *CollectOpts, directiveID string) (string, error) {
	body, err := c.get(ctx, rawURL, auth, "text/plain", opts, directiveID)
	return string(body), err
}

func (c AzurePipelinesCollector) get(ctx context.Context, rawURL, auth, accept string, opts *CollectOpts, directiveID string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", accept)
	res, body, err := doAndReadHTTP(c.client(), req, 64<<20, opts, directiveID)
	if err != nil {
		return nil, err
	}

	// ADO answers an unauthenticated request with a redirect to its sign-in page
	if res.StatusCode >= 300 && res.StatusCode < 400 || res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("azure devops %s for %s: not authorized (run `az login`, or check the PAT in credential_refs.token)", res.Status, rawURL)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("azure devops %s: %s — %s", res.Status, rawURL, truncateRunes(strings.TrimSpace(string(body)), 300))
	}
	return body, nil
}

// authHeader picks PAT (Basic) auth when credential_refs.token resolves,
// otherwise an Entra bearer token from `az`.
func (c AzurePipelinesCollector) authHeader(ctx context.Context, directive userdata.Directive, opts *CollectOpts) (string, error) {
	if key := strings.TrimSpace(directive.CredentialRefs["token"]); key != "" {
		userdataDir := ""
		if opts != nil {
			userdataDir = opts.UserdataDir
		}
		pat := userdata.ResolveEnv(userdataDir, key)
		if pat == "" {
			return "", fmt.Errorf("azure devops PAT missing (set %s in environment or userdata/.env, or remove credential_refs.token to use `az login`)", key)
		}
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(":"+pat)), nil
	}
	token := c.Token
	if token == nil {
		token = azAccessToken
	}
	t, err := token(ctx, opts, directive.ID)
	if err != nil {
		return "", err
	}
	return "Bearer " + t, nil
}

// azTokenCache holds the last `az` token so a poll doesn't fork az each time.
var azTokenCache struct {
	sync.Mutex
	token   string
	expires time.Time
}

// azAccessToken returns an Azure DevOps bearer token from the Azure CLI's
// login, reusing it until five minutes before it expires.
func azAccessToken(ctx context.Context, opts *CollectOpts, directiveID string) (string, error) {
	azTokenCache.Lock()
	defer azTokenCache.Unlock()
	if azTokenCache.token != "" && time.Until(azTokenCache.expires) > 5*time.Minute {
		return azTokenCache.token, nil
	}

	// Ask az for a token scoped to Azure DevOps
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "az", "account", "get-access-token", "--resource", adoResourceID, "-o", "json")
	out, err := runAndLogExec(cmd, opts, directiveID)
	if err != nil {
		return "", fmt.Errorf("az account get-access-token failed (run `az login`): %w", err)
	}
	var resp struct {
		AccessToken string          `json:"accessToken"`
		ExpiresOn   json.RawMessage `json:"expires_on"`
	}
	if err := json.Unmarshal(out, &resp); err != nil || resp.AccessToken == "" {
		return "", fmt.Errorf("az account get-access-token: unexpected output")
	}

	// expires_on is epoch seconds (a number, or a string in older az builds)
	expires := time.Now().Add(30 * time.Minute)
	if secs, err := strconv.ParseInt(strings.Trim(string(resp.ExpiresOn), `"`), 10, 64); err == nil && secs > 0 {
		expires = time.Unix(secs, 0)
	}
	azTokenCache.token, azTokenCache.expires = resp.AccessToken, expires
	return resp.AccessToken, nil
}

// ValidateDirective checks config and that each definition is readable.
func (c AzurePipelinesCollector) ValidateDirective(ctx context.Context, directive userdata.Directive, opts *ValidateOpts) []ValidationIssue {
	cfg, err := parseADOConfig(directive)
	if err != nil {
		return []ValidationIssue{{
			Field:       "config",
			Message:     err.Error(),
			Remediation: "set config.organization, config.project, and config.definitions (pipeline definition ids)",
		}}
	}
	copts := &CollectOpts{}
	if opts != nil {
		copts.UserdataDir = opts.UserdataDir
	}
	auth, err := c.authHeader(ctx, directive, copts)
	if err != nil {
		return []ValidationIssue{{
			Field:       "auth",
			Message:     err.Error(),
			Remediation: "run `az login` as the user docentd runs as, or set credential_refs.token to an env var holding a PAT with Build (Read) scope",
		}}
	}

	// Probe each definition so a typo'd id or missing access shows up now
	var issues []ValidationIssue
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for _, id := range cfg.definitions {
		var def struct {
			Name string `json:"name"`
		}
		u := fmt.Sprintf("%s/_apis/build/definitions/%d?api-version=%s", cfg.projectURL(), id, adoAPIVersion)
		if err := c.getJSON(probeCtx, u, auth, &def, nil, directive.ID); err != nil {
			issues = append(issues, ValidationIssue{
				Field:       "config.definitions",
				Message:     fmt.Sprintf("pipeline definition %d: %v", id, err),
				Remediation: fmt.Sprintf("check the id under %s/_build and that your account can read it", cfg.projectURL()),
			})
		}
	}
	return issues
}
