package collectors

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KurtPreston/docent/libs/config/userdata"
)

func TestLocalGitValidateMissingConfig(t *testing.T) {
	c := LocalGitCollector{Clock: time.Now}
	issues := c.ValidateDirective(context.Background(), userdata.Directive{
		ID:        "local",
		Name:      "Local",
		Collector: "local-git",
		Enabled:   true,
	}, nil)
	if !hasField(issues, "code_home") {
		t.Fatalf("expected code_home issue when no paths/code_home set; got %#v", issues)
	}
}

func TestLocalGitValidateCodeHomeMissing(t *testing.T) {
	c := LocalGitCollector{Clock: time.Now}
	issues := c.ValidateDirective(context.Background(), userdata.Directive{
		ID:        "local",
		Name:      "Local",
		Collector: "local-git",
		Enabled:   true,
		CodeHome:  "/definitely/not/a/real/path/here/xyz",
	}, nil)
	if !hasMessageContains(issues, "does not exist") {
		t.Fatalf("expected code_home missing issue, got %#v", issues)
	}
}

func TestLocalGitValidateCodeHomeWithRepo(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "myrepo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := LocalGitCollector{Clock: time.Now}
	issues := c.ValidateDirective(context.Background(), userdata.Directive{
		ID:        "local",
		Name:      "Local",
		Collector: "local-git",
		Enabled:   true,
		CodeHome:  dir,
	}, &ValidateOpts{ExpandRepoPath: func(s string) string { return s }})
	for _, iss := range issues {
		if iss.Field == "code_home" {
			t.Fatalf("did not expect code_home issue: %#v", issues)
		}
	}
}

func TestLocalGitValidateRunsGitProbe(t *testing.T) {
	// A directory containing an empty `.git` folder passes the structural
	// checks (".git exists, parent is a directory") but `git rev-parse` will
	// reject it with `fatal: not a git repository`. The probe should surface
	// that as an actionable issue instead of silently passing.
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary unavailable")
	}
	dir := t.TempDir()
	repo := filepath.Join(dir, "fakerepo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := LocalGitCollector{Clock: time.Now}
	issues := c.ValidateDirective(context.Background(), userdata.Directive{
		ID:        "local",
		Name:      "Local",
		Collector: "local-git",
		Enabled:   true,
		Paths:     []string{repo},
	}, &ValidateOpts{ExpandRepoPath: func(s string) string { return s }})
	if !hasField(issues, "git") {
		t.Fatalf("expected git probe issue for fake .git folder, got %#v", issues)
	}
}

func TestLocalGitValidateBadPath(t *testing.T) {
	c := LocalGitCollector{Clock: time.Now}
	issues := c.ValidateDirective(context.Background(), userdata.Directive{
		ID:        "local",
		Name:      "Local",
		Collector: "local-git",
		Enabled:   true,
		Paths:     []string{"/definitely/not/a/real/path/zzz"},
	}, &ValidateOpts{ExpandRepoPath: func(s string) string { return s }})
	if !hasField(issues, "paths") {
		t.Fatalf("expected paths issue, got %#v", issues)
	}
}

func TestGiteaValidateMissingBaseURL(t *testing.T) {
	c := GiteaCollector{Clock: time.Now}
	issues := c.ValidateDirective(context.Background(), userdata.Directive{
		ID:        "gitea",
		Collector: "gitea",
		Enabled:   true,
	}, nil)
	if !hasField(issues, "config.base_url") {
		t.Fatalf("expected base_url issue, got %#v", issues)
	}
}

func TestGiteaValidateInvalidBaseURL(t *testing.T) {
	c := GiteaCollector{Clock: time.Now}
	issues := c.ValidateDirective(context.Background(), userdata.Directive{
		ID:        "gitea",
		Collector: "gitea",
		Enabled:   true,
		Config:    map[string]string{"base_url": "not a url"},
	}, nil)
	if !hasField(issues, "config.base_url") {
		t.Fatalf("expected base_url issue, got %#v", issues)
	}
}

func TestGiteaValidateMissingTokenRef(t *testing.T) {
	c := GiteaCollector{Clock: time.Now}
	issues := c.ValidateDirective(context.Background(), userdata.Directive{
		ID:        "gitea",
		Collector: "gitea",
		Enabled:   true,
		Config:    map[string]string{"base_url": "https://gitea.example.com"},
	}, nil)
	if !hasField(issues, "credential_refs.token") {
		t.Fatalf("expected credential_refs.token issue, got %#v", issues)
	}
}

func TestGiteaValidateEmptyTokenEnv(t *testing.T) {
	t.Setenv("STUB_GITEA_TOKEN", "")
	c := GiteaCollector{Clock: time.Now}
	issues := c.ValidateDirective(context.Background(), userdata.Directive{
		ID:             "gitea",
		Collector:      "gitea",
		Enabled:        true,
		Config:         map[string]string{"base_url": "https://gitea.example.com"},
		CredentialRefs: map[string]string{"token": "STUB_GITEA_TOKEN"},
	}, &ValidateOpts{UserdataDir: t.TempDir()})
	if !hasMessageContains(issues, "STUB_GITEA_TOKEN") {
		t.Fatalf("expected empty-env issue mentioning token key, got %#v", issues)
	}
}

func TestGiteaValidateAuthSucceeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/user" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "token ") {
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"login":"someone"}`))
	}))
	defer srv.Close()

	t.Setenv("STUB_GITEA_TOKEN", "secret-value")
	c := GiteaCollector{Clock: time.Now, HTTP: srv.Client()}
	issues := c.ValidateDirective(context.Background(), userdata.Directive{
		ID:             "gitea",
		Collector:      "gitea",
		Enabled:        true,
		Config:         map[string]string{"base_url": srv.URL},
		CredentialRefs: map[string]string{"token": "STUB_GITEA_TOKEN"},
	}, &ValidateOpts{UserdataDir: t.TempDir()})
	if len(issues) != 0 {
		t.Fatalf("expected no issues for successful auth, got %#v", issues)
	}
}

func TestJiraValidateMissingBaseURL(t *testing.T) {
	c := JiraCollector{Clock: time.Now}
	issues := c.ValidateDirective(context.Background(), userdata.Directive{
		ID: "jira", Collector: "jira", Enabled: true,
	}, nil)
	if !hasField(issues, "config.base_url") {
		t.Fatalf("expected base_url issue, got %#v", issues)
	}
}

func TestJiraValidateNoCredentials(t *testing.T) {
	c := JiraCollector{Clock: time.Now}
	issues := c.ValidateDirective(context.Background(), userdata.Directive{
		ID:        "jira",
		Collector: "jira",
		Enabled:   true,
		Config:    map[string]string{"base_url": "https://example.atlassian.net"},
	}, nil)
	if !hasField(issues, "credential_refs") {
		t.Fatalf("expected credential_refs issue, got %#v", issues)
	}
}

func TestJiraValidateTokenRequiresEmail(t *testing.T) {
	t.Setenv("STUB_JIRA_TOKEN", "x")
	c := JiraCollector{Clock: time.Now}
	issues := c.ValidateDirective(context.Background(), userdata.Directive{
		ID:             "jira",
		Collector:      "jira",
		Enabled:        true,
		Config:         map[string]string{"base_url": "https://example.atlassian.net"},
		CredentialRefs: map[string]string{"token": "STUB_JIRA_TOKEN"},
	}, &ValidateOpts{UserdataDir: t.TempDir()})
	if !hasField(issues, "config.email") {
		t.Fatalf("expected config.email issue, got %#v", issues)
	}
}

func TestJiraValidateRejectsHTMLResponse(t *testing.T) {
	// Some Jira instances (especially behind SSO) return 200 OK with an HTML
	// login page when the API auth slips. Validation should treat this as an
	// auth issue, not "looks fine".
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body>Please log in</body></html>`))
	}))
	defer srv.Close()
	t.Setenv("STUB_JIRA_PAT", "secret")
	c := JiraCollector{Clock: time.Now, HTTP: srv.Client()}
	issues := c.ValidateDirective(context.Background(), userdata.Directive{
		ID:             "jira",
		Collector:      "jira",
		Enabled:        true,
		Config:         map[string]string{"base_url": srv.URL},
		CredentialRefs: map[string]string{"pat": "STUB_JIRA_PAT"},
	}, &ValidateOpts{UserdataDir: t.TempDir()})
	if !hasMessageContains(issues, "not JSON") {
		t.Fatalf("expected HTML-body issue, got %#v", issues)
	}
}

func TestJiraValidatePATAuthSucceeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/2/myself" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
			http.Error(w, "missing pat", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accountId":"abc"}`))
	}))
	defer srv.Close()

	t.Setenv("STUB_JIRA_PAT", "secret")
	c := JiraCollector{Clock: time.Now, HTTP: srv.Client()}
	issues := c.ValidateDirective(context.Background(), userdata.Directive{
		ID:             "jira",
		Collector:      "jira",
		Enabled:        true,
		Config:         map[string]string{"base_url": srv.URL},
		CredentialRefs: map[string]string{"pat": "STUB_JIRA_PAT"},
	}, &ValidateOpts{UserdataDir: t.TempDir()})
	if len(issues) != 0 {
		t.Fatalf("expected no issues, got %#v", issues)
	}
}

func TestGoogleCalendarValidateMissingURL(t *testing.T) {
	c := GoogleCalendarCollector{Clock: time.Now}
	issues := c.ValidateDirective(context.Background(), userdata.Directive{
		ID: "cal", Collector: "google-calendar", Enabled: true,
	}, nil)
	if !hasField(issues, "config.ical_url") {
		t.Fatalf("expected ical_url issue, got %#v", issues)
	}
}

func TestGoogleCalendarValidateProbeOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("BEGIN:VCALENDAR\nEND:VCALENDAR\n"))
	}))
	defer srv.Close()

	c := GoogleCalendarCollector{Clock: time.Now, HTTP: srv.Client()}
	issues := c.ValidateDirective(context.Background(), userdata.Directive{
		ID:        "cal",
		Collector: "google-calendar",
		Enabled:   true,
		Config:    map[string]string{"ical_url": srv.URL},
	}, nil)
	if len(issues) != 0 {
		t.Fatalf("expected no issues, got %#v", issues)
	}
}

func hasField(issues []ValidationIssue, field string) bool {
	for _, iss := range issues {
		if iss.Field == field {
			return true
		}
	}
	return false
}

func hasMessageContains(issues []ValidationIssue, sub string) bool {
	for _, iss := range issues {
		if strings.Contains(iss.Message, sub) {
			return true
		}
	}
	return false
}
