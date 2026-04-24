package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kurt/slakkr-ai/internal/userdata"
)

func TestSetupValidateAndStartDayDryRun(t *testing.T) {
	root := workspaceTempDir(t)
	userdataDir := filepath.Join(root, "userdata")
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := New(&out, &errOut, strings.NewReader(""))
	app.Now = func() time.Time { return time.Date(2026, 4, 24, 9, 0, 0, 0, time.UTC) }
	app.Git = noopGit{}
	if err := app.Run(context.Background(), []string{"setup", "--yes", "--userdata", userdataDir, "--recipes", filepath.Join("..", "..", "recipes")}); err != nil {
		t.Fatalf("setup: %v\nstderr: %s", err, errOut.String())
	}
	if err := app.Run(context.Background(), []string{"validate", "--userdata", userdataDir}); err != nil {
		t.Fatalf("validate: %v", err)
	}
	store := userdata.NewStore(userdataDir)
	projects := userdata.ProjectsFile{Projects: []userdata.Project{{ID: "slakkr-ai", Name: "Slakkr AI"}}}
	if err := store.SaveProjects(projects); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTasks(projects, userdata.TasksFile{Tasks: []userdata.Task{{
		ID:        "build-mvp",
		ProjectID: "slakkr-ai",
		Name:      "Build MVP",
		Status:    userdata.TaskStatusReady,
		Priority:  userdata.PriorityHigh,
		Delegation: userdata.Delegation{
			State:           userdata.DelegationCandidate,
			Reason:          "Well-scoped CLI work",
			SuggestedPrompt: "Implement the next CLI command and run tests.",
		},
	}}}); err != nil {
		t.Fatal(err)
	}
	if err := app.Run(context.Background(), []string{"start_day", "--userdata", userdataDir, "--date", "2026-04-24"}); err != nil {
		t.Fatalf("start_day: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(userdataDir, "daybook", "2026-04-24.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "AI Plan") || !strings.Contains(string(content), "build-mvp") {
		t.Fatalf("daybook did not contain plan:\n%s", content)
	}
}

type noopGit struct{}

func (noopGit) Init(context.Context, string) error {
	return nil
}

func (noopGit) CommitAll(context.Context, string, string) error {
	return nil
}

func (noopGit) AddRemote(context.Context, string, string, string) error {
	return nil
}

func workspaceTempDir(t *testing.T) string {
	t.Helper()
	base := filepath.Join("..", "..", ".cache", "tests")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(base, "cli-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
