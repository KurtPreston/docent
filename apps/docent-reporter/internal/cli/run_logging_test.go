package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KurtPreston/docent/libs/ai"
	"github.com/KurtPreston/docent/libs/collectors"
	"github.com/KurtPreston/docent/libs/config/userdata"
)

// fakeCollector emits a single status item and records both an HTTP
// and an exec entry to the per-directive logger so the smoke test
// can verify those wires.
type fakeCollector struct{}

func (fakeCollector) CollectEvents(_ context.Context, d userdata.Directive, opts *collectors.CollectOpts) ([]collectors.StatusItem, error) {
	if opts != nil && opts.RunLog != nil {
		l := opts.RunLog.Directive(d.ID)
		if l != nil {
			l.LogHTTP("GET", "https://api.example.com/x", 0, 200, 42, 10*time.Millisecond, nil)
			l.LogExec("/usr/bin/true", []string{"--flag"}, 0, 5, 0, 5*time.Millisecond, nil)
		}
	}
	return []collectors.StatusItem{{
		DirectiveID: d.ID,
		Source:      "fake",
		Kind:        "fake_item",
		Title:       "hello",
		ObservedAt:  time.Now().UTC(),
		IsSelf:      true,
	}}, nil
}

func TestRunWritesRunLogDirectory(t *testing.T) {
	tmpHome := t.TempDir()
	userdataDir := filepath.Join(tmpHome, "userdata")
	if err := os.MkdirAll(userdataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Validation requires a directive shape that matches one of the
	// known collector schemas; override the local-git collector
	// registry entry with a fake so we don't have to spin up actual
	// git infrastructure to drive an end-to-end run.
	repoRoot := filepath.Join(tmpHome, "fakerepo")
	if err := os.MkdirAll(repoRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "ai:\n  provider: rule-based\ndirectives:\n  - id: fake-one\n    name: Fake collector\n    collector: local-git\n    enabled: true\n    paths:\n      - " + repoRoot + "\n"
	if err := os.WriteFile(filepath.Join(userdataDir, "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	fixedNow := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	var stdout, stderr bytes.Buffer
	app := &App{
		Out: &stdout,
		Err: &stderr,
		In:  bytes.NewReader(nil),
		Now: func() time.Time { return fixedNow },
		AI:  ai.RuleBasedProvider{},
		Reg: collectors.NewRegistry(func() time.Time { return fixedNow }),
	}
	app.Reg.Register("local-git", fakeCollector{})

	args := []string{
		"--userdata", userdataDir,
		"--mode", "daily-plan",
		"--days", "1",
		"--skip-check",
		"--no-save",
	}
	if err := app.Run(context.Background(), args); err != nil {
		t.Fatalf("Run: %v\nstderr:\n%s", err, stderr.String())
	}

	runDir := filepath.Join(userdataDir, "logs", "2026-05-18-daily-plan")
	if _, err := os.Stat(runDir); err != nil {
		t.Fatalf("run dir missing: %v", err)
	}

	runLogContent, err := os.ReadFile(filepath.Join(runDir, "run.log"))
	if err != nil {
		t.Fatalf("read run.log: %v", err)
	}
	for _, want := range []string{
		"# docent run 2026-05-18T12:00:00Z",
		"id:           daily-plan",
		"## Directives",
		"fake-one",
		"collector=local-git",
		"## Collection summary",
		"## AI summary",
		"output:        (no-save)",
	} {
		if !strings.Contains(string(runLogContent), want) {
			t.Errorf("run.log missing %q\ncontent:\n%s", want, runLogContent)
		}
	}

	directiveLog, err := os.ReadFile(filepath.Join(runDir, "fake-one.log"))
	if err != nil {
		t.Fatalf("read fake-one.log: %v", err)
	}
	got := string(directiveLog)
	if !strings.Contains(got, "HTTP GET https://api.example.com/x") {
		t.Errorf("directive log missing HTTP line: %s", got)
	}
	if !strings.Contains(got, "EXEC /usr/bin/true --flag") {
		t.Errorf("directive log missing EXEC line: %s", got)
	}

	// Rule-based provider writes no AI debug files.
	for _, name := range []string{"cursor-summary-request.json", "ollama-summary-request.json"} {
		if _, err := os.Stat(filepath.Join(runDir, name)); err == nil {
			t.Errorf("unexpected AI debug file: %s", name)
		}
	}

	// .cache must have been pruned (or never created).
	if _, err := os.Stat(filepath.Join(userdataDir, ".cache")); err == nil {
		t.Error("userdata/.cache should not exist after run")
	}
}

func TestRunPrunesOldLogDirsToTwenty(t *testing.T) {
	tmpHome := t.TempDir()
	userdataDir := filepath.Join(tmpHome, "userdata")
	if err := os.MkdirAll(userdataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Join(tmpHome, "fakerepo")
	if err := os.MkdirAll(repoRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "ai:\n  provider: rule-based\ndirectives:\n  - id: fake-one\n    name: Fake collector\n    collector: local-git\n    enabled: true\n    paths:\n      - " + repoRoot + "\n"
	if err := os.WriteFile(filepath.Join(userdataDir, "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	logsRoot := filepath.Join(userdataDir, "logs")
	if err := os.MkdirAll(logsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 25; i++ {
		p := filepath.Join(logsRoot, "placeholder-"+dirName(i))
		if err := os.Mkdir(p, 0o755); err != nil {
			t.Fatal(err)
		}
		mod := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
	}

	fixedNow := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	var stdout, stderr bytes.Buffer
	app := &App{
		Out: &stdout,
		Err: &stderr,
		In:  bytes.NewReader(nil),
		Now: func() time.Time { return fixedNow },
		AI:  ai.RuleBasedProvider{},
		Reg: collectors.NewRegistry(func() time.Time { return fixedNow }),
	}
	app.Reg.Register("local-git", fakeCollector{})
	args := []string{
		"--userdata", userdataDir,
		"--mode", "daily-plan",
		"--days", "1",
		"--skip-check",
		"--no-save",
	}
	if err := app.Run(context.Background(), args); err != nil {
		t.Fatalf("Run: %v\nstderr: %s", err, stderr.String())
	}

	entries, err := os.ReadDir(logsRoot)
	if err != nil {
		t.Fatal(err)
	}
	// 25 placeholder dirs + 1 new run dir = 26 candidates; prune keeps 20.
	if len(entries) != 20 {
		t.Fatalf("expected 20 run dirs after prune, got %d", len(entries))
	}

	// The freshly-created run dir must survive the prune.
	if _, err := os.Stat(filepath.Join(logsRoot, "2026-05-18-daily-plan")); err != nil {
		t.Errorf("new run dir should have survived prune: %v", err)
	}
}

func dirName(i int) string {
	return string(rune('a'+i/26)) + string(rune('a'+i%26))
}
