package workflow

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kurt/slakkr-ai/internal/collectors"
	"github.com/kurt/slakkr-ai/internal/userdata"
	"gopkg.in/yaml.v3"
)

func TestBuildRecentActivityInputRunsCollectActivity(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	if err := userdata.NewStore(dir).Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	projects := userdata.ProjectsFile{Projects: []userdata.Project{{ID: "p1", Name: "P", Repos: []userdata.Repo{}}}}
	if err := writeYAML(filepath.Join(dir, "projects.yaml"), projects); err != nil {
		t.Fatal(err)
	}
	tasks := userdata.TasksFile{Tasks: []userdata.Task{}}
	if err := writeYAML(filepath.Join(dir, "tasks.yaml"), tasks); err != nil {
		t.Fatal(err)
	}
	dirs := userdata.DirectivesFile{Directives: []userdata.Directive{}}
	if err := writeYAML(filepath.Join(dir, "directives.yaml"), dirs); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	in, err := BuildRecentActivityInput(ctx, Deps{
		Registry:       collectors.NewRegistry(func() time.Time { return now }),
		Now:            func() time.Time { return now },
		ExpandRepoPath: func(s string) string { return s },
	}, dir, now, 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if in.LookbackDays != 7 {
		t.Fatalf("lookback days: %d", in.LookbackDays)
	}
	if in.HostID == "" {
		t.Fatal("empty host")
	}
}

func writeYAML(path string, v any) error {
	b, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
