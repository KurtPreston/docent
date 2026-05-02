package collectors

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kurt/slakkr-ai/internal/userdata"
)

func TestManualCollectorProducesStatusItem(t *testing.T) {
	now := time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC)
	collector := ManualCollector{Clock: func() time.Time { return now }}
	items, err := collector.Collect(context.Background(), userdata.Directive{
		ID:        "manual",
		Name:      "Manual",
		Collector: "manual",
		Enabled:   true,
		Target: map[string]string{
			"prompt": "What changed?",
		},
	}, nil)
	if err != nil {
		t.Fatalf("collect manual: %v", err)
	}
	if len(items) != 1 || items[0].Summary != "What changed?" || items[0].Kind != "manual_prompt" {
		t.Fatalf("unexpected items: %#v", items)
	}
}

func TestManualCollectorWithManualAnswer(t *testing.T) {
	now := time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC)
	collector := ManualCollector{Clock: func() time.Time { return now }}
	opts := &CollectOpts{
		ManualAnswer: func(_ context.Context, _ userdata.Directive, question string) (string, error) {
			if question != "Hours?" {
				t.Fatalf("question = %q", question)
			}
			return "about two", nil
		},
	}
	items, err := collector.Collect(context.Background(), userdata.Directive{
		ID:        "m1",
		Name:      "Check-in",
		Collector: "manual",
		Enabled:   true,
		Target: map[string]string{
			"prompt": "Hours?",
		},
	}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Kind != "manual_response" {
		t.Fatalf("unexpected items: %#v", items)
	}
	want := "Q: Hours? | A: about two"
	if items[0].Summary != want {
		t.Fatalf("summary = %q want %q", items[0].Summary, want)
	}
}

func TestRegistryRunsManualCollectorsSequentiallyWhenManualAnswerSet(t *testing.T) {
	clock := func() time.Time { return time.Unix(0, 0).UTC() }
	reg := NewRegistry(clock)
	var order []string
	opts := &CollectOpts{
		ManualAnswer: func(_ context.Context, d userdata.Directive, _ string) (string, error) {
			order = append(order, d.ID)
			return "ok", nil
		},
	}
	directives := []userdata.Directive{
		{ID: "a", Name: "A", Collector: "manual", Enabled: true, Target: map[string]string{"prompt": "p1"}},
		{ID: "b", Name: "B", Collector: "manual", Enabled: true, Target: map[string]string{"prompt": "p2"}},
	}
	items, err := reg.Collect(context.Background(), directives, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("len=%d items=%#v", len(items), items)
	}
	if len(order) != 2 || order[0] != "a" || order[1] != "b" {
		t.Fatalf("callback order %v", order)
	}
}

// seqTestCollector appends a marker when Collect runs; used to assert manual runs before parallel work.
type seqTestCollector struct {
	clock func() time.Time
	mark  string
	order *[]string
}

func (c seqTestCollector) Collect(_ context.Context, d userdata.Directive, _ *CollectOpts) ([]StatusItem, error) {
	*c.order = append(*c.order, c.mark)
	return []StatusItem{{
		DirectiveID: d.ID,
		Source:      "seq-test",
		Kind:        "test",
		Title:       d.Name,
		Summary:     c.mark,
		ObservedAt:  c.clock(),
	}}, nil
}

func TestRegistryRunsManualBeforeParallelWhenManualAnswerSet(t *testing.T) {
	clock := func() time.Time { return time.Unix(0, 0).UTC() }
	reg := NewRegistry(clock)
	var order []string
	reg.Register("seq-test", seqTestCollector{clock: clock, mark: "parallel", order: &order})
	directives := []userdata.Directive{
		{ID: "m1", Name: "M", Collector: "manual", Enabled: true, Target: map[string]string{"prompt": "q"}},
		{ID: "p1", Name: "P", Collector: "seq-test", Enabled: true},
	}
	opts := &CollectOpts{
		ManualAnswer: func(_ context.Context, _ userdata.Directive, _ string) (string, error) {
			order = append(order, "manual")
			return "ok", nil
		},
	}
	_, err := reg.Collect(context.Background(), directives, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != "manual" || order[1] != "parallel" {
		t.Fatalf("order = %v want [manual parallel]", order)
	}
}

func TestLocalGitCollectorResolvesProjectRepoPaths(t *testing.T) {
	dir := workspaceTempDir(t)
	fakeGit := filepath.Join(dir, "git")
	if runtime.GOOS == "windows" {
		fakeGit += ".bat"
	}
	writeFile(t, fakeGit, fakeGitScript())
	if err := os.Chmod(fakeGit, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	host := "test-machine"
	collector := LocalGitCollector{Clock: time.Now}
	opts := &CollectOpts{
		HostID: host,
		Projects: userdata.ProjectsFile{Projects: []userdata.Project{{
			ID:   "p1",
			Name: "Project One",
			Repos: []userdata.Repo{{
				ID: "main",
				PathsByHost: map[string][]string{
					host: {dir},
				},
			}},
		}}},
	}
	items, err := collector.Collect(context.Background(), userdata.Directive{
		ID:          "repo",
		Name:        "Repo",
		Collector:   "local-git",
		Enabled:     true,
		ProjectID:   "p1",
		Config: map[string]string{
			"checks": "repository_status",
		},
		Target: map[string]string{
			"project_id": "p1",
			"repo_id":    "main",
		},
	}, opts)
	if err != nil {
		t.Fatalf("collect git: %v", err)
	}
	if len(items) != 1 || items[0].Severity != "warning" {
		t.Fatalf("expected dirty warning, got %#v", items)
	}
}

func TestLocalGitCollectorWithoutTargetScansAllHostRepos(t *testing.T) {
	dir := workspaceTempDir(t)
	fakeGit := filepath.Join(dir, "git")
	if runtime.GOOS == "windows" {
		fakeGit += ".bat"
	}
	writeFile(t, fakeGit, fakeGitScript())
	if err := os.Chmod(fakeGit, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	host := "test-machine"
	repoA := filepath.Join(dir, "repo-a")
	repoB := filepath.Join(dir, "repo-b")
	if err := os.MkdirAll(repoA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repoB, 0o755); err != nil {
		t.Fatal(err)
	}
	collector := LocalGitCollector{Clock: time.Now}
	opts := &CollectOpts{
		HostID: host,
		Projects: userdata.ProjectsFile{Projects: []userdata.Project{
			{
				ID: "p1",
				Repos: []userdata.Repo{{
					ID: "main",
					PathsByHost: map[string][]string{
						host: {repoA},
					},
				}},
			},
			{
				ID: "p2",
				Repos: []userdata.Repo{{
					ID: "main",
					PathsByHost: map[string][]string{
						host: {repoB},
					},
				}},
			},
		}},
	}
	items, err := collector.Collect(context.Background(), userdata.Directive{
		ID:        "repo",
		Name:      "Repo",
		Collector: "local-git",
		Enabled:   true,
		Config: map[string]string{
			"checks": "repository_status",
		},
	}, opts)
	if err != nil {
		t.Fatalf("collect git: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 repos, got %#v", items)
	}
}

func TestLocalGitCollectorRejectsPartialTarget(t *testing.T) {
	collector := LocalGitCollector{Clock: time.Now}
	_, err := collector.Collect(context.Background(), userdata.Directive{
		ID:        "repo",
		Name:      "Repo",
		Collector: "local-git",
		Enabled:   true,
		Target: map[string]string{
			"project_id": "p1",
		},
	}, &CollectOpts{
		HostID:   "host",
		Projects: userdata.ProjectsFile{},
	})
	if err == nil || !strings.Contains(err.Error(), "both project_id and repo_id") {
		t.Fatalf("expected partial target error, got %v", err)
	}
}

func TestGoogleCalendarCollectorReadsURLFromCredentialRef(t *testing.T) {
	now := time.Date(2026, 4, 25, 8, 0, 0, 0, time.UTC)
	ical := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nDTSTART:20260425T090000Z\r\nSUMMARY:Deep Work\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(ical))
	}))
	defer server.Close()

	userdataDir := workspaceTempDir(t)
	writeFile(t, filepath.Join(userdataDir, ".env"), "SLAKKR_TEST_ICAL_URL="+server.URL+"\n")
	collector := GoogleCalendarCollector{
		Clock: func() time.Time { return now },
		HTTP:  server.Client(),
	}
	items, err := collector.Collect(context.Background(), userdata.Directive{
		ID:        "cal",
		Name:      "Calendar",
		Collector: "google-calendar",
		Enabled:   true,
		CredentialRefs: map[string]string{
			"ical_url": "SLAKKR_TEST_ICAL_URL",
		},
	}, &CollectOpts{UserdataDir: userdataDir})
	if err != nil {
		t.Fatalf("collect calendar: %v", err)
	}
	if len(items) != 1 || items[0].Kind != "calendar_event" || items[0].Title != "Deep Work" {
		t.Fatalf("unexpected calendar items: %#v", items)
	}
}

func TestGoogleCalendarCollector403Message(t *testing.T) {
	now := time.Unix(0, 0).UTC()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer server.Close()
	collector := GoogleCalendarCollector{
		Clock: func() time.Time { return now },
		HTTP:  server.Client(),
	}
	_, err := collector.Collect(context.Background(), userdata.Directive{
		ID:        "cal",
		Name:      "Test Cal",
		Collector: "google-calendar",
		Enabled:   true,
		Config:    map[string]string{"ical_url": server.URL},
	}, nil)
	if err == nil {
		t.Fatal("expected error for 403 response")
	}
	msg := err.Error()
	if !strings.Contains(msg, "403") || !strings.Contains(msg, "Re-copy") {
		t.Fatalf("expected 403 + guidance in error, got: %s", msg)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func workspaceTempDir(t *testing.T) string {
	t.Helper()
	base := filepath.Join("..", "..", ".cache", "tests")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(base, "collectors-")
	if err != nil {
		t.Fatal(err)
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func fakeGitScript() string {
	if runtime.GOOS == "windows" {
		return "@echo off\r\nif \"%1\"==\"branch\" echo main\r\nif \"%1\"==\"status\" echo M README.md\r\nif \"%1\"==\"log\" echo abc123 initial\r\nif \"%1\"==\"remote\" echo git@example.com:repo.git\r\n"
	}
	return "#!/usr/bin/env sh\ncase \"$1\" in\n  branch) echo main ;;\n  status) echo ' M README.md' ;;\n  log) echo 'abc123 initial' ;;\n  remote) echo 'git@example.com:repo.git' ;;\nesac\n"
}

func TestRegistryCollectActivitySkipsManual(t *testing.T) {
	clock := func() time.Time { return time.Unix(0, 0).UTC() }
	reg := NewRegistry(clock)
	var progress []string
	opts := &CollectOpts{
		OnDirectiveUpdate: func(p DirectiveProgress) {
			progress = append(progress, p.DirectiveID+":"+p.Status+":"+p.Detail)
		},
	}
	directives := []userdata.Directive{
		{ID: "m", Name: "M", Collector: "manual", Enabled: true, Target: map[string]string{"prompt": "x"}},
		{ID: "s", Name: "S", Collector: "slack", Enabled: true},
	}
	items, err := reg.CollectActivity(context.Background(), directives, time.Unix(0, 0), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("expected no items, got %#v", items)
	}
	var sawSkip bool
	for _, p := range progress {
		if strings.Contains(p, "skipped (no activity mode)") {
			sawSkip = true
		}
	}
	if !sawSkip {
		t.Fatalf("expected skipped manual progress, got %v", progress)
	}
}
