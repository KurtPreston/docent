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
	"github.com/KurtPreston/docent/libs/logparsers"
)

// AzurePipelinesCollector watches completed Azure DevOps pipeline runs.
//
// With config.log_parser set, it reads the logs of the tasks matching
// config.log_task and emits one `test_failure` signal per test file that
// failed or flaked. Flakes are why logs matter: CI retries leave a flaky run
// green, and only its log says a test needed a retry. A failed run with no
// test findings (or no parser) yields one `build_failure` signal instead.
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
	// adoDefaultBranchPrefix starts the fix branch suggested by test_failure
	// signals when config.branch_prefix is unset.
	adoDefaultBranchPrefix = "test-fix-"
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
	parserName   string
	parser       logparsers.Parser
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
		cfg.branchPrefix = adoDefaultBranchPrefix
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

	// log_parser names a built-in parser; without one no logs are read
	if name := strings.ToLower(strings.TrimSpace(d.Config["log_parser"])); name != "" {
		parser, ok := logparsers.Get(name)
		if !ok {
			return cfg, fmt.Errorf("config.log_parser: unknown parser %q (available: %s)", name, strings.Join(logparsers.Names(), ", "))
		}
		cfg.parserName, cfg.parser = name, parser
	}

	// log_task narrows which timeline tasks' logs are parsed; default all
	if expr := strings.TrimSpace(d.Config["log_task"]); expr != "" {
		re, err := regexp.Compile(expr)
		if err != nil {
			return cfg, fmt.Errorf("config.log_task: %w", err)
		}
		cfg.taskRE = re
	}
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

	// Read each run and turn its failures into signals
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
		rep, err := c.inspectBuild(ctx, cfg, auth, b, opts, directive.ID)
		if err != nil {
			return nil, err
		}
		items = append(items, adoBuildSignals(directive, cfg, b, rep)...)
	}
	return items, nil
}

// adoRunReport is what a run's timeline and logs add to its build record.
type adoRunReport struct {
	failedTasks []string
	tests       logparsers.Result
	logURLs     []string
}

// adoBuildFailed reports whether a run's result counts as a failure.
func adoBuildFailed(b adoBuild) bool {
	return b.Result == "failed" || b.Result == "partiallySucceeded"
}

// inspectBuild reads a run's timeline for failed tasks and, with a parser
// configured, parses the matching tasks' logs. A green run with no parser
// needs no requests.
func (c AzurePipelinesCollector) inspectBuild(ctx context.Context, cfg adoConfig, auth string, b adoBuild, opts *CollectOpts, directiveID string) (adoRunReport, error) {
	rep := adoRunReport{tests: logparsers.Result{Complete: true}}
	if cfg.parser == nil && !adoBuildFailed(b) {
		return rep, nil
	}
	buildURL := fmt.Sprintf("%s/_apis/build/builds/%d", cfg.projectURL(), b.ID)
	var timeline struct {
		Records []adoTimelineRecord `json:"records"`
	}
	if err := c.getJSON(ctx, buildURL+"/timeline?api-version="+adoAPIVersion, auth, &timeline, opts, directiveID); err != nil {
		return rep, err
	}

	// Failed tasks say what broke a run that has no test findings
	for _, rec := range timeline.Records {
		if rec.Type == "Task" && rec.Result == "failed" {
			rep.failedTasks = append(rep.failedTasks, rec.Name)
		}
	}
	if cfg.parser == nil {
		return rep, nil
	}

	// Merge parsed results across matching tasks (a pipeline may shard tests)
	for _, rec := range timeline.Records {
		if rec.Type != "Task" || rec.Log == nil || rec.Result == "skipped" {
			continue
		}
		if cfg.taskRE != nil && !cfg.taskRE.MatchString(rec.Name) {
			continue
		}
		logURL := fmt.Sprintf("%s/logs/%d?api-version=%s", buildURL, rec.Log.ID, adoAPIVersion)
		text, err := c.getText(ctx, logURL, auth, opts, directiveID)
		if err != nil {
			return rep, err
		}
		r := cfg.parser(adoStripTimestamps(text))
		if r.Empty() {
			continue
		}
		rep.logURLs = append(rep.logURLs, logURL)
		rep.tests.Merge(r)
	}
	return rep, nil
}

// adoTimestampRE matches the "2026-01-02T03:04:05.0000000Z " prefix ADO puts
// on every log line.
var adoTimestampRE = regexp.MustCompile(`(?m)^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z ?`)

// adoStripTimestamps hands parsers plain console text.
func adoStripTimestamps(log string) string {
	return adoTimestampRE.ReplaceAllString(log, "")
}

// adoBuildSignals emits a test_failure per failing test file, or a single
// build_failure for a failed run with no test findings.
func adoBuildSignals(directive userdata.Directive, cfg adoConfig, b adoBuild, rep adoRunReport) []StatusItem {
	if rep.tests.Empty() && !adoBuildFailed(b) {
		return nil
	}

	// Who and what the run was for
	repo := cfg.repo
	if repo == "" && strings.EqualFold(b.Repository.Type, "GitHub") {
		repo = b.Repository.ID
	}
	prNumber := strings.TrimSpace(b.TriggerInfo["pr.number"])
	if prNumber == "" {
		prNumber = adoPRNumberFromRef(b.SourceBranch)
	}
	runBranch := ""
	baseBranch := cfg.baseBranch
	if strings.HasPrefix(b.SourceBranch, "refs/heads/") {
		runBranch = strings.TrimPrefix(b.SourceBranch, "refs/heads/")
		baseBranch = runBranch
	}
	commit := b.SourceVersion
	if sha := strings.TrimSpace(b.TriggerInfo["pr.sourceSha"]); sha != "" {
		commit = sha
	}
	author := b.TriggerInfo["pr.sender.name"]
	if author == "" {
		author = b.RequestedFor.DisplayName
	}

	// Labels shared by every signal from this run
	buildURL := fmt.Sprintf("%s/_build/results?buildId=%d", cfg.projectURL(), b.ID)
	pipeline := b.Definition.Name
	if pipeline == "" {
		pipeline = fmt.Sprintf("definition %d", b.Definition.ID)
	}
	where := "on " + strings.TrimPrefix(b.SourceBranch, "refs/heads/")
	if prNumber != "" {
		where = "on PR #" + prNumber
	}

	// Run fields every signal carries
	runFields := func() map[string]string {
		f := map[string]string{
			"repo":          repo,
			"base_branch":   baseBranch,
			"build_id":      strconv.Itoa(b.ID),
			"build_number":  b.BuildNumber,
			"build_url":     buildURL,
			"pipeline":      pipeline,
			"definition_id": strconv.Itoa(b.Definition.ID),
			"result":        b.Result,
			"reason":        b.Reason,
			"source_branch": b.SourceBranch,
			"commit":        commit,
			"requested_by":  b.RequestedFor.UniqueName,
			"failed_tasks":  strings.Join(rep.failedTasks, "\n"),
		}
		if prNumber != "" {
			f["pr_number"] = prNumber
			f["pr_url"] = adoPRURL(b.Repository.URL, repo, prNumber)
			f["pr_title"] = b.TriggerInfo["pr.title"]
			f["pr_head_branch"] = b.TriggerInfo["pr.sourceBranch"]
			f["pr_author"] = b.TriggerInfo["pr.sender.name"]
		}
		return f
	}
	signal := func(kind, title, summary, severity, stableID string, fields map[string]string) StatusItem {
		return StatusItem{
			DirectiveID: directive.ID,
			Repository:  repo,
			Source:      "azure-pipelines",
			Kind:        kind,
			Title:       title,
			Summary:     summary,
			URL:         buildURL,
			Severity:    severity,
			ObservedAt:  b.FinishTime,
			StableID:    stableID,
			Author:      author,
			Fields:      fields,
		}
	}

	// One test_failure per test file, so each fix is scoped to one file
	var items []StatusItem
	for _, file := range rep.tests.ByFile() {
		outcome, severity := "flaky", "warning"
		if len(file.Failed) > 0 {
			outcome, severity = "failed", "error"
		}
		fields := runFields()
		fields["branch"] = cfg.branchPrefix + adoTestSlug(file.File) + "-" + b.FinishTime.UTC().Format("20060102")
		fields["test_file"] = file.File
		fields["outcome"] = outcome
		fields["failed_tests"] = testNames(file.Failed)
		fields["flaky_tests"] = testNames(file.Flaky)
		fields["errors"] = testErrors(rep.tests.Errors, file)
		fields["test_log_urls"] = strings.Join(rep.logURLs, "\n")
		fields["log_parser"] = cfg.parserName
		fields["parse_complete"] = strconv.FormatBool(rep.tests.Complete)
		n := len(file.Failed) + len(file.Flaky)
		items = append(items, signal("test_failure",
			fmt.Sprintf("%s: %s %s", pipeline, outcome, file.File),
			fmt.Sprintf("Run %s %s: %d %s test(s) in %s", b.BuildNumber, where, n, outcome, file.File),
			severity,
			fmt.Sprintf("azure-pipelines:%s/%s:%d:%s", cfg.org, cfg.project, b.ID, file.File),
			fields))
	}
	if len(items) > 0 {
		return items
	}

	// A failed run with no test findings surfaces as one build failure
	fields := runFields()
	fields["branch"] = runBranch
	if prNumber != "" {
		fields["branch"] = b.TriggerInfo["pr.sourceBranch"]
	}
	summary := fmt.Sprintf("Run %s %s %s", b.BuildNumber, where, b.Result)
	if len(rep.failedTasks) > 0 {
		summary += ": " + strings.Join(rep.failedTasks, ", ")
	}
	return []StatusItem{signal("build_failure",
		fmt.Sprintf("%s: %s %s", pipeline, b.Result, where),
		summary,
		"error",
		fmt.Sprintf("azure-pipelines:%s/%s:%d", cfg.org, cfg.project, b.ID),
		fields)}
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

// adoTestSlug reduces "e2e/checkout-flow.spec.ts" to "checkout-flow" for a branch name.
func adoTestSlug(file string) string {
	base := path.Base(file)
	if i := strings.Index(base, "."); i > 0 {
		base = base[:i]
	}
	slug := strings.Trim(adoSlugRE.ReplaceAllString(strings.ToLower(base), "-"), "-")
	if slug == "" {
		slug = "test"
	}
	return slug
}

func testNames(tests []logparsers.Test) string {
	names := make([]string, 0, len(tests))
	for _, t := range tests {
		names = append(names, t.Name)
	}
	return strings.Join(names, "\n")
}

// testErrors renders each test's first error under a heading.
func testErrors(errors map[string]string, file logparsers.FileResult) string {
	var b strings.Builder
	for _, t := range append(append([]logparsers.Test{}, file.Failed...), file.Flaky...) {
		if msg, ok := errors[t.Name]; ok {
			fmt.Fprintf(&b, "### %s\n%s\n\n", t.Name, msg)
		}
	}
	return logparsers.Truncate(strings.TrimSpace(b.String()), adoMaxErrorsLen)
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
		return nil, fmt.Errorf("azure devops %s: %s — %s", res.Status, rawURL, logparsers.Truncate(strings.TrimSpace(string(body)), 300))
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
