package collectors

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
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
	if len(items) != 1 || items[0].Summary != "What changed?" {
		t.Fatalf("unexpected items: %#v", items)
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
