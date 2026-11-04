package report

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KurtPreston/docent/libs/ai"
	"github.com/KurtPreston/docent/libs/collectors"
	"github.com/KurtPreston/docent/libs/config/executionmode"
	"github.com/KurtPreston/docent/libs/config/userdata"
)

func reportGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_DATE=2026-01-02T12:00:00Z", "GIT_COMMITTER_DATE=2026-01-02T12:00:00Z",
		"GIT_AUTHOR_NAME=Test", "GIT_COMMITTER_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_EMAIL=test@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func reportBacklogBoard(t *testing.T) string {
	t.Helper()
	board := t.TempDir()
	dir := filepath.Join(board, "backlog", "tasks")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(dir, "task-1.md")
	before := "---\nid: TASK-1\ntitle: Improve reports\nstatus: In Progress\n---\n"
	if err := os.WriteFile(name, []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	reportGit(t, board, "init", "-q", "-b", "main")
	reportGit(t, board, "add", ".")
	reportGit(t, board, "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-qm", "Initial board")
	after := strings.Replace(before, "In Progress", "Done", 1) + "## Final Summary\nDelivered useful history reports.\n"
	if err := os.WriteFile(name, []byte(after), 0600); err != nil {
		t.Fatal(err)
	}
	reportGit(t, board, "add", ".")
	reportGit(t, board, "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-qm", "Finish reports")
	return board
}

type reportOtherCollector struct{}

func (reportOtherCollector) CollectEvents(context.Context, userdata.Directive, *collectors.CollectOpts) ([]collectors.StatusItem, error) {
	return []collectors.StatusItem{{Source: "other", Kind: "commit", Title: "Other source activity", StableID: "other:1"}}, nil
}

func TestBacklogReportHistoryAndCurrentState(t *testing.T) {
	board := reportBacklogBoard(t)
	reg := collectors.NewRegistry(nil)
	reg.Register("other", reportOtherCollector{})
	reg.Register("backlog", collectors.BacklogCollector{
		Run: func(_ context.Context, cmd collectors.BacklogCommand, _ *collectors.CollectOpts) ([]byte, error) {
			if strings.Join(cmd.Args, " ") != "task list --json" {
				t.Fatalf("unexpected command: %+v", cmd)
			}
			return []byte(`{"tasks":[{"id":"TASK-1","title":"Improve reports","status":"Done"},{"id":"TASK-2","title":"Untouched task","status":"To Do"}]}`), nil
		},
	})
	cfg := userdata.ConfigFile{
		AI: forcesFallback(),
		Directives: []userdata.Directive{
			{ID: "board", Collector: "backlog", Enabled: true, Config: map[string]string{"board": board, "git_events": "true"}},
			{ID: "other", Collector: "other", Enabled: true},
		},
	}
	for _, mode := range []string{executionmode.BuiltinRecentActivity, executionmode.BuiltinDailyPlan} {
		for _, formatter := range []string{"repo-chronological", "json-signal-list"} {
			t.Run(mode+"/"+formatter, func(t *testing.T) {
				rec := &recProvider{}
				result, err := Generate(context.Background(), cfg, Options{
					ModeID: mode, Days: 7, Now: time.Date(2026, 1, 3, 12, 0, 0, 0, time.UTC),
					Registry: reg, Provider: rec, LogsDir: t.TempDir(),
				})
				if err != nil {
					t.Fatal(err)
				}
				prompt, err := ai.BuildPrompt(rec.in.Instruction, rec.in, ai.SelectActivityFormatter(formatter))
				if err != nil {
					t.Fatal(err)
				}
				for _, want := range []string{"Delivered useful history reports.", "In Progress", "Done", "Other source activity", "commit", "not completed accomplishments"} {
					if !strings.Contains(prompt, want) {
						t.Fatalf("report dropped %q: %s", want, prompt)
					}
				}
				daily := mode == executionmode.BuiltinDailyPlan
				markdown, err := (ai.RuleBasedProvider{}).RunMode(context.Background(), rec.in)
				if err != nil || !strings.Contains(markdown, "Delivered useful history reports.") {
					t.Fatalf("deterministic renderer dropped task activity: %s %v", markdown, err)
				}
				if daily && !strings.Contains(markdown, "Current Backlog board (planning context, not activity)") {
					t.Fatalf("deterministic daily plan mislabeled state: %s", markdown)
				}
				if strings.Contains(prompt, "Untouched task") != daily {
					t.Fatalf("current state leaked into activity-only report or disappeared from daily plan: %s", prompt)
				}
				wantItems := 2
				if daily {
					wantItems = 3
				}
				if result.WorkItems != wantItems {
					t.Fatalf("task history/state did not group: %+v", result)
				}
			})
		}
	}
}

func TestBacklogReportCollectorAllowListAndOptIn(t *testing.T) {
	board := reportBacklogBoard(t)
	reg := collectors.NewRegistry(nil)
	reg.Register("other", reportOtherCollector{})
	cfg := userdata.ConfigFile{Directives: []userdata.Directive{
		{ID: "board", Collector: "backlog", Enabled: true, Config: map[string]string{"board": board}},
		{ID: "other", Collector: "other", Enabled: true},
	}}
	run := executionmode.ResolvedRun{Collect: executionmode.CollectEvents, Until: time.Now()}
	items, err := Collect(context.Background(), reg, cfg, run, CollectOptions{})
	if err != nil || len(items) != 1 || items[0].Source != "other" {
		t.Fatalf("default emitted history: %+v %v", items, err)
	}
	cfg.Directives[0].Config["git_events"] = "true"
	run.Collect = executionmode.CollectBoth
	run.Collectors = []string{"other"}
	items, err = Collect(context.Background(), reg, cfg, run, CollectOptions{})
	if err != nil || len(items) != 1 || items[0].Source != "other" {
		t.Fatalf("allow-list did not restrict both passes: %+v %v", items, err)
	}
	run.Collectors = []string{"github", "github-enterprise"}
	items, err = Collect(context.Background(), reg, cfg, run, CollectOptions{})
	if err != nil || len(items) != 0 {
		t.Fatalf("GitHub-only run included Backlog: %+v %v", items, err)
	}
}
