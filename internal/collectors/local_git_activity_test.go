package collectors

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kurt/slakkr-ai/internal/userdata"
)

func TestLocalGitCollectActivityCommitsInWindow(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(env []string, name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
	}
	run(nil, "git", "init")
	run(nil, "git", "config", "user.email", "t@example.com")
	run(nil, "git", "config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(nil, "git", "add", "f.txt")
	run([]string{
		"GIT_AUTHOR_DATE=2020-01-01T12:00:00",
		"GIT_COMMITTER_DATE=2020-01-01T12:00:00",
	}, "git", "commit", "-m", "old-commit")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(nil, "git", "add", "f.txt")
	run([]string{
		"GIT_AUTHOR_DATE=2026-04-30T12:00:00",
		"GIT_COMMITTER_DATE=2026-04-30T12:00:00",
	}, "git", "commit", "-m", "recent-commit")

	clock := func() time.Time { return time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC) }
	since := time.Date(2026, 4, 28, 0, 0, 0, 0, time.UTC)
	hostID := "test-host"
	projects := userdata.ProjectsFile{
		Projects: []userdata.Project{{
			ID:   "p1",
			Name: "Proj",
			Repos: []userdata.Repo{{
				ID:          "r1",
				Name:        "r1",
				PathsByHost: map[string][]string{hostID: {dir}},
			}},
		}},
	}
	collector := LocalGitCollector{Clock: clock}
	opts := &CollectOpts{
		HostID:   hostID,
		Projects: projects,
	}
	items, err := collector.CollectActivity(context.Background(), userdata.Directive{
		ID:          "lg",
		Name:        "Local",
		Collector:   "local-git",
		Enabled:     true,
		ProjectID:   "p1",
	}, since, opts)
	if err != nil {
		t.Fatal(err)
	}
	var commits, reflog int
	for _, it := range items {
		switch it.Kind {
		case "commit":
			commits++
			if !strings.Contains(it.Title, "recent-commit") {
				t.Errorf("unexpected commit title: %q", it.Title)
			}
		case "reflog":
			reflog++
		}
	}
	if commits != 1 {
		t.Fatalf("want 1 commit in window, got commits=%d items=%d", commits, len(items))
	}
	if reflog < 1 {
		t.Fatalf("expected at least one reflog line, got %d", reflog)
	}
}
