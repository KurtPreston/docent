package collectors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KurtPreston/docent/libs/config/userdata"
)

// adoLog prefixes each line with an ADO-style timestamp, as pipeline logs do.
func adoLog(lines ...string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("2026-01-02T03:04:05.0000000Z " + l + "\n")
	}
	return b.String()
}

// adoFakeServer serves a project with two completed runs: a PR run with a
// flaky spec (green build) and a main run with a failed spec.
func adoFakeServer(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var paths []string
	finish := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
	builds := []map[string]any{
		{
			"id": 501, "buildNumber": "20260102.4", "status": "completed", "result": "succeeded",
			"reason": "pullRequest", "sourceBranch": "refs/pull/42/merge", "sourceVersion": "mergesha",
			"finishTime": finish, "definition": map[string]any{"id": 7, "name": "web-e2e"},
			"repository":  map[string]any{"id": "acme/web", "type": "GitHub", "url": "https://github.com:443/acme/web"},
			"triggerInfo": map[string]string{"pr.number": "42", "pr.sourceSha": "headsha", "pr.title": "Add gadgets", "pr.sourceBranch": "gadgets", "pr.sender.name": "octo"},
		},
		{
			"id": 502, "buildNumber": "20260102.5", "status": "completed", "result": "failed",
			"reason": "individualCI", "sourceBranch": "refs/heads/main", "sourceVersion": "mainsha",
			"finishTime": finish.Add(time.Hour), "definition": map[string]any{"id": 7, "name": "web-e2e"},
			"repository":   map[string]any{"id": "acme/web", "type": "GitHub", "url": "https://github.com:443/acme/web"},
			"requestedFor": map[string]string{"displayName": "CI", "uniqueName": "ci@example.com"},
		},
	}
	logs := map[string]string{
		"501": adoLog("  1 flaky", "    [chromium] › e2e/gadgets.spec.ts:40:1 › opens a gadget ", "  9 passed (1.0m)"),
		"502": adoLog("  1 failed", "    [chromium] › e2e/widgets.spec.ts:10:3 › adds a widget ", "  9 passed (1.0m)"),
	}

	// Route builds, timelines, and logs; anything else is a test failure
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer fake-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		const prefix = "/org/proj/_apis/build/"
		rest := strings.TrimPrefix(r.URL.Path, prefix)
		parts := strings.Split(rest, "/")
		switch {
		case rest == "builds":
			if r.URL.Query().Get("definitions") != "7" || r.URL.Query().Get("statusFilter") != "completed" {
				t.Errorf("unexpected builds query %s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"value": builds})
		case len(parts) == 3 && parts[2] == "timeline":
			_ = json.NewEncoder(w).Encode(map[string]any{"records": []map[string]any{
				{"type": "Task", "name": "Install dependencies", "result": "succeeded", "log": map[string]int{"id": 3}},
				{"type": "Task", "name": "Run Playwright tests", "result": map[bool]string{true: "failed", false: "succeeded"}[parts[1] == "502"], "log": map[string]int{"id": 9}},
			}})
		case len(parts) == 4 && parts[2] == "logs" && parts[3] == "9":
			_, _ = w.Write([]byte(logs[parts[1]]))
		case strings.HasPrefix(rest, "definitions/7"):
			_, _ = w.Write([]byte(`{"name":"web-e2e"}`))
		case strings.HasPrefix(rest, "definitions/"):
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &paths
}

func adoTestCollector(now time.Time) AzurePipelinesCollector {
	return AzurePipelinesCollector{
		Clock: func() time.Time { return now },
		Token: func(context.Context, *CollectOpts, string) (string, error) { return "fake-token", nil },
	}
}

func adoTestDirective(baseURL string) userdata.Directive {
	return userdata.Directive{ID: "e2e", Collector: "azure-pipelines", Config: map[string]string{
		"base_url": baseURL, "organization": "org", "project": "proj", "definitions": "7", "log_parser": "playwright", "log_task": "Playwright",
	}}
}

func TestAzurePipelinesCollectEvents(t *testing.T) {
	srv, paths := adoFakeServer(t)
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	items, err := adoTestCollector(now).CollectEvents(context.Background(), adoTestDirective(srv.URL), &CollectOpts{Since: now.Add(-6 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items: %+v", len(items), items)
	}

	// Only the matching test task's log is fetched
	for _, p := range *paths {
		if strings.HasSuffix(p, "/logs/3") {
			t.Fatalf("fetched non-test task log %s", p)
		}
	}

	// The green PR run still surfaces its flaky spec, with PR context
	pr := items[0]
	if pr.Source != "azure-pipelines" || pr.Kind != "test_failure" || pr.Severity != "warning" {
		t.Fatalf("pr item = %+v", pr)
	}
	want := map[string]string{
		"repo": "acme/web", "branch": "test-fix-gadgets-20260102", "base_branch": "main", "build_id": "501",
		"outcome": "flaky", "test_file": "e2e/gadgets.spec.ts", "commit": "headsha", "pr_number": "42",
		"pr_url": "https://github.com/acme/web/pull/42", "pr_head_branch": "gadgets", "pr_author": "octo",
		"flaky_tests": "e2e/gadgets.spec.ts:40:1 › opens a gadget", "failed_tests": "", "parse_complete": "true", "log_parser": "playwright",
	}
	for k, v := range want {
		if pr.Fields[k] != v {
			t.Errorf("pr field %s = %q, want %q", k, pr.Fields[k], v)
		}
	}
	if pr.StableID != "azure-pipelines:org/proj:501:e2e/gadgets.spec.ts" || pr.Author != "octo" {
		t.Errorf("pr identity = %q / %q", pr.StableID, pr.Author)
	}
	if !strings.HasSuffix(pr.URL, "/org/proj/_build/results?buildId=501") {
		t.Errorf("pr url = %q", pr.URL)
	}

	// The main run reports a failure, based on main, without PR fields
	main := items[1]
	if main.Severity != "error" || main.Fields["outcome"] != "failed" || main.Fields["base_branch"] != "main" || main.Fields["commit"] != "mainsha" {
		t.Fatalf("main item = %+v", main.Fields)
	}
	if _, ok := main.Fields["pr_number"]; ok {
		t.Errorf("main run should have no pr_number")
	}
}

func TestAzurePipelinesCollectEventsWindowAndScope(t *testing.T) {
	srv, _ := adoFakeServer(t)
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	c := adoTestCollector(now)

	// Runs that finished before the watermark are skipped
	items, err := c.CollectEvents(context.Background(), adoTestDirective(srv.URL), &CollectOpts{Since: now.Add(-90 * time.Minute)})
	if err != nil || len(items) != 1 || items[0].Fields["build_id"] != "502" {
		t.Fatalf("items = %+v, err = %v", items, err)
	}

	// CI runs never appear in self-scoped reports
	items, err = c.CollectEvents(context.Background(), adoTestDirective(srv.URL), &CollectOpts{Scope: ScopeSelf})
	if err != nil || items != nil {
		t.Fatalf("self scope items = %+v, err = %v", items, err)
	}
}

func TestAzurePipelinesAuthFailure(t *testing.T) {
	srv, _ := adoFakeServer(t)
	c := adoTestCollector(time.Now())
	c.Token = func(context.Context, *CollectOpts, string) (string, error) { return "wrong", nil }
	_, err := c.CollectEvents(context.Background(), adoTestDirective(srv.URL), &CollectOpts{})
	if err == nil || !strings.Contains(err.Error(), "not authorized") {
		t.Fatalf("err = %v", err)
	}
}

func TestAzurePipelinesValidateDirective(t *testing.T) {
	srv, _ := adoFakeServer(t)
	c := adoTestCollector(time.Now())
	if issues := c.ValidateDirective(context.Background(), adoTestDirective(srv.URL), nil); len(issues) != 0 {
		t.Fatalf("valid directive issues = %+v", issues)
	}

	// Config errors are reported before any network access
	cases := map[string]string{"definitions": "abc", "organization": "", "log_task": "(", "log_parser": "jest"}
	for key, val := range cases {
		d := adoTestDirective(srv.URL)
		d.Config[key] = val
		issues := c.ValidateDirective(context.Background(), d, nil)
		if len(issues) != 1 || !strings.Contains(issues[0].Message, key) {
			t.Errorf("%s=%q issues = %+v", key, val, issues)
		}
	}

	// An unknown definition id is caught by the probe
	d := adoTestDirective(srv.URL)
	d.Config["definitions"] = "7, 8"
	issues := c.ValidateDirective(context.Background(), d, nil)
	if len(issues) != 1 || !strings.Contains(issues[0].Message, "definition 8") {
		t.Fatalf("issues = %+v", issues)
	}
}

func TestADOSpecSlugAndPRRef(t *testing.T) {
	for in, want := range map[string]string{
		"e2e/checkout-flow.spec.ts": "checkout-flow", "e2e/Foo_Bar.test.tsx": "foo-bar",
	} {
		if got := adoTestSlug(in); got != want {
			t.Errorf("adoTestSlug(%q) = %q, want %q", in, got, want)
		}
	}
	if adoPRNumberFromRef("refs/pull/17/merge") != "17" || adoPRNumberFromRef("refs/heads/main") != "" {
		t.Errorf("adoPRNumberFromRef mismatch")
	}
}

func TestAzurePipelinesBuildFailureWithoutParser(t *testing.T) {
	srv, paths := adoFakeServer(t)
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	d := adoTestDirective(srv.URL)
	delete(d.Config, "log_parser")
	items, err := adoTestCollector(now).CollectEvents(context.Background(), d, &CollectOpts{Since: now.Add(-6 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}

	// The green run is skipped outright; the failed one names its failed task
	if len(items) != 1 {
		t.Fatalf("items = %+v", items)
	}
	it := items[0]
	if it.Kind != "build_failure" || it.Fields["build_id"] != "502" || it.Fields["failed_tasks"] != "Run Playwright tests" || it.Fields["branch"] != "main" {
		t.Fatalf("item = %+v", it)
	}
	if it.StableID != "azure-pipelines:org/proj:502" {
		t.Errorf("stable id = %q", it.StableID)
	}

	// Without a parser no logs are fetched, and green runs cost no requests
	for _, p := range *paths {
		if strings.Contains(p, "/logs/") || strings.Contains(p, "/501/") {
			t.Fatalf("unexpected request %s", p)
		}
	}
}
