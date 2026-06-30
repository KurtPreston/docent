package runlog

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewRunCreatesDirAndRunLog(t *testing.T) {
	tmp := t.TempDir()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	run, err := NewRun(tmp, "2026-05-18-recent-activity", now)
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	defer run.Close()

	dir := run.Dir()
	wantDir := filepath.Join(tmp, "logs", "2026-05-18-recent-activity")
	if dir != wantDir {
		t.Errorf("Dir() = %q, want %q", dir, wantDir)
	}
	if _, err := os.Stat(filepath.Join(dir, "run.log")); err != nil {
		t.Errorf("run.log missing: %v", err)
	}
}

func TestNewRunRejectsEmptyBasename(t *testing.T) {
	if _, err := NewRun(t.TempDir(), "", time.Now()); err == nil {
		t.Fatal("expected error for empty basename")
	}
}

func TestDirectiveLoggerWritesLines(t *testing.T) {
	tmp := t.TempDir()
	run, err := NewRun(tmp, "run", time.Now())
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	defer run.Close()

	l := run.Directive("github")
	l.LogHTTP("GET", "https://api.example.com/x", 0, 200, 1024, 150*time.Millisecond, nil)
	l.LogExec("/usr/bin/gh", []string{"search", "prs"}, 0, 2048, 0, 800*time.Millisecond, nil)
	l.Note("note text")
	run.Close()

	data, err := os.ReadFile(filepath.Join(run.Dir(), "github.log"))
	if err != nil {
		t.Fatalf("read github.log: %v", err)
	}
	content := string(data)
	for _, want := range []string{
		"HTTP GET https://api.example.com/x",
		"status=200",
		"res=1.0KB",
		"EXEC /usr/bin/gh search prs",
		"stdout=2.0KB",
		"NOTE note text",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("missing %q in:\n%s", want, content)
		}
	}
}

func TestDirectiveLoggerReusesFile(t *testing.T) {
	tmp := t.TempDir()
	run, err := NewRun(tmp, "run", time.Now())
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	defer run.Close()

	a := run.Directive("gitea")
	b := run.Directive("gitea")
	if a != b {
		t.Fatalf("Directive(gitea) returned different loggers: %p vs %p", a, b)
	}
}

func TestLoggerSanitizesDirectiveID(t *testing.T) {
	tmp := t.TempDir()
	run, err := NewRun(tmp, "run", time.Now())
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	defer run.Close()

	run.Directive("weird/../id").LogExec("/bin/true", nil, 0, 0, 0, time.Millisecond, nil)
	run.Close()

	if _, err := os.Stat(filepath.Join(run.Dir(), "weird_.._id.log")); err != nil {
		t.Errorf("sanitized filename missing: %v", err)
	}
}

func TestLoggerConcurrentWrites(t *testing.T) {
	tmp := t.TempDir()
	run, err := NewRun(tmp, "run", time.Now())
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	defer run.Close()

	l := run.Directive("github")
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			l.LogHTTP("GET", "https://example.com/", 0, 200, 1, time.Millisecond, nil)
		}(i)
	}
	wg.Wait()
	run.Close()

	data, err := os.ReadFile(filepath.Join(run.Dir(), "github.log"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 50 {
		t.Fatalf("expected 50 lines, got %d", len(lines))
	}
	for i, line := range lines {
		if !strings.Contains(line, "HTTP GET https://example.com/") {
			t.Errorf("line %d malformed: %q", i, line)
		}
	}
}

func TestPruneRunLogsKeepsNewest(t *testing.T) {
	tmp := t.TempDir()
	logsRoot := filepath.Join(tmp, "logs")
	if err := os.MkdirAll(logsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	names := []string{"a", "b", "c", "d", "e"}
	base := time.Date(2026, 5, 18, 0, 0, 0, 0, time.UTC)
	for i, name := range names {
		p := filepath.Join(logsRoot, name)
		if err := os.Mkdir(p, 0o755); err != nil {
			t.Fatal(err)
		}
		mod := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	if err := PruneRunLogs(tmp, 2); err != nil {
		t.Fatalf("PruneRunLogs: %v", err)
	}
	entries, err := os.ReadDir(logsRoot)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range entries {
		got[e.Name()] = true
	}
	wantKept := map[string]bool{"d": true, "e": true}
	if len(got) != len(wantKept) {
		t.Fatalf("expected %d dirs after prune, got %d (%v)", len(wantKept), len(got), got)
	}
	for k := range wantKept {
		if !got[k] {
			t.Errorf("expected %s to be kept", k)
		}
	}
}

func TestPruneRunLogsZeroKeepDoesNothing(t *testing.T) {
	tmp := t.TempDir()
	logsRoot := filepath.Join(tmp, "logs")
	if err := os.MkdirAll(filepath.Join(logsRoot, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := PruneRunLogs(tmp, 0); err != nil {
		t.Fatalf("PruneRunLogs: %v", err)
	}
	if _, err := os.Stat(filepath.Join(logsRoot, "a")); err != nil {
		t.Errorf("dir 'a' should have been kept: %v", err)
	}
}

func TestPruneRunLogsHandlesMissingDir(t *testing.T) {
	tmp := t.TempDir()
	if err := PruneRunLogs(tmp, 5); err != nil {
		t.Errorf("PruneRunLogs on missing dir: %v", err)
	}
}

func TestRedactURLDropsKnownSecrets(t *testing.T) {
	in := "https://example.com/x?token=abc&safe=ok&api_key=xyz"
	out := redactURL(in)
	if !strings.Contains(out, "token=REDACTED") {
		t.Errorf("token not redacted: %s", out)
	}
	if !strings.Contains(out, "api_key=REDACTED") {
		t.Errorf("api_key not redacted: %s", out)
	}
	if !strings.Contains(out, "safe=ok") {
		t.Errorf("non-secret param mangled: %s", out)
	}
}

func TestRedactArg(t *testing.T) {
	cases := map[string]string{
		"GITHUB_TOKEN=abc":     "GITHUB_TOKEN=REDACTED",
		"PATH=/usr/bin":        "PATH=/usr/bin",
		"SLAKKR_API_KEY=zzz":   "SLAKKR_API_KEY=REDACTED",
		"SLAKKR_SECRET=ssh":    "SLAKKR_SECRET=REDACTED",
		"PASSWORD=hunter2":     "PASSWORD=REDACTED",
		"AUTHORIZATION=Bearer": "AUTHORIZATION=REDACTED",
		"foo":                  "foo",
	}
	for in, want := range cases {
		got := redactArg(in)
		if got != want {
			t.Errorf("redactArg(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoggingTransportRecordsRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(204)
	}))
	defer srv.Close()

	tmp := t.TempDir()
	run, err := NewRun(tmp, "run", time.Now())
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	defer run.Close()
	client := WrapHTTPClient(nil, run.Directive("probe"))

	resp, err := client.Get(srv.URL + "/path")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	resp.Body.Close()
	run.Close()

	data, err := os.ReadFile(filepath.Join(run.Dir(), "probe.log"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "HTTP GET") {
		t.Errorf("missing GET line: %s", got)
	}
	if !strings.Contains(got, "status=204") {
		t.Errorf("missing status: %s", got)
	}
}

func TestNopLoggerSafeOnNilRun(t *testing.T) {
	var run *Run
	l := run.Directive("nope")
	l.LogHTTP("GET", "https://x", 0, 200, 1, time.Millisecond, nil)
	l.LogExec("echo", []string{"hi"}, 0, 1, 0, time.Millisecond, nil)
	l.Note("ok")
}
