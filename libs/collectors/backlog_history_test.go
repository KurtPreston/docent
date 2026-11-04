package collectors

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type historyFixture struct {
	t    *testing.T
	root string
}

func newHistoryFixture(t *testing.T) historyFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := historyFixture{t: t, root: root}
	f.git("init", "-q", "-b", "main")
	f.git("config", "user.name", "Test User")
	f.git("config", "user.email", "test@example.com")
	f.git("config", "commit.gpgsign", "false")
	return f
}

func (f historyFixture) git(args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = f.root
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f historyFixture) write(name, content string) {
	f.t.Helper()
	p := filepath.Join(f.root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		f.t.Fatal(err)
	}
}

func (f historyFixture) commit(date string) string {
	f.t.Helper()
	f.git("add", ".")
	cmd := exec.Command("git", "-c", "core.hooksPath=/dev/null", "commit", "-qm", "Record board changes")
	cmd.Dir = f.root
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	if out, err := cmd.CombinedOutput(); err != nil {
		f.t.Fatalf("commit: %v: %s", err, out)
	}
	return f.git("rev-parse", "HEAD")
}

func historyTask(status, body string) string {
	return "---\nid: TASK-1\ntitle: Improve reports\nstatus: " + status + "\n---\n\n" + body + "\n"
}

func (f historyFixture) collect(board string, opts *CollectOpts) []StatusItem {
	f.t.Helper()
	cfg := map[string]string{"board": board, "git_events": "true", "statuses": "To Do"}
	items, err := (BacklogCollector{}).CollectEvents(context.Background(), backlogDirective(cfg), opts)
	if err != nil {
		f.t.Fatal(err)
	}
	return items
}

func eventChanges(t *testing.T, item StatusItem) map[string]backlogChange {
	t.Helper()
	var changes map[string]backlogChange
	if err := json.Unmarshal([]byte(item.Fields["changes"]), &changes); err != nil {
		t.Fatal(err)
	}
	return changes
}

func TestBacklogHistoryLifecycle(t *testing.T) {
	f := newHistoryFixture(t)
	name := "backlog/tasks/task-1 - Report.md"
	f.write(name, historyTask("To Do", "## Implementation Notes\nOld note."))
	f.commit("2026-01-01T12:00:00Z")
	f.write(name, historyTask("In Progress", "## Implementation Notes\nOld note.\nStarted implementation."))
	started := f.commit("2026-01-02T12:00:00Z")
	f.write(name, historyTask("Done", "## Implementation Notes\nOld note.\nStarted implementation.\n## Final Summary\nImplemented history reports."))
	done := f.commit("2026-01-03T12:00:00Z")
	f.git("mv", name, "backlog/tasks/task-1 - Renamed.md")
	name = "backlog/tasks/task-1 - Renamed.md"
	f.commit("2026-01-04T12:00:00Z")
	f.write(name, historyTask("In Progress", "## Implementation Notes\nOld note.\nStarted implementation.\n## Final Summary\nImplemented history reports."))
	reopened := f.commit("2026-01-05T12:00:00Z")
	f.write("backlog/archive/tasks/.keep", "")
	f.git("mv", name, "backlog/archive/tasks/task-1 - Renamed.md")
	f.commit("2026-01-06T12:00:00Z")
	// Dirty current data cannot leak into historical events.
	f.write("backlog/archive/tasks/task-1 - Renamed.md", historyTask("Done", "UNCOMMITTED SECRET"))
	opts := &CollectOpts{Since: time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC), Until: time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)}
	items := f.collect(f.root, opts)
	if len(items) != 3 {
		t.Fatalf("want three inclusive-window transitions, got %+v", items)
	}
	for i, hash := range []string{reopened, done, started} {
		if items[i].Fields["commit"] != hash || items[i].Kind != "task_activity" || !items[i].IsSelf {
			t.Fatalf("unexpected event: %+v", items[i])
		}
		if items[i].Fields["queued"] != "" || items[i].Fields["path"] != "" || strings.Contains(items[i].Fields["changes"], "UNCOMMITTED") {
			t.Fatalf("queue/path/current data leaked: %+v", items[i])
		}
	}
	if c := eventChanges(t, items[2])["section: implementation notes"]; c.Before != "" || c.After != "Started implementation." {
		t.Fatalf("not an incremental note: %+v", c)
	}
	if c := eventChanges(t, items[0]); len(c) != 1 || c["status"].Before != "Done" || c["status"].After != "In Progress" {
		t.Fatalf("reopen repeated old content: %+v", c)
	}
	if again := f.collect(f.root, opts); !reflect.DeepEqual(items, again) {
		t.Fatal("repeated reads changed event identities/content")
	}
}

func TestBacklogHistoryLayoutsAndNoise(t *testing.T) {
	for _, layout := range []string{"backlog", ".backlog", "planning/board"} {
		t.Run(layout, func(t *testing.T) {
			f := newHistoryFixture(t)
			board := filepath.Join(f.root, "nested")
			if layout == "planning/board" {
				f.write("nested/backlog.config.yml", "project_name: Example\nbacklog_directory: planning/board\n")
			}
			name := "nested/" + layout + "/tasks/Tâsk space.md"
			f.write(name, historyTask("To Do", "## Description\nImprove report details."))
			f.write("nested/"+layout+"/docs/note.md", "Not a task.")
			f.commit("2026-01-01T12:00:00Z")
			f.write(name, strings.Replace(historyTask("To Do", "## Description\nImprove   report details."), "status:", "updated_date: '2026-01-02'\nordinal: 1\nlabels: [docs]\nstatus:", 1))
			f.commit("2026-01-02T12:00:00Z")
			f.write(name, historyTask("Done", "## Description\nImprove report details.\n## Final Summary\nDelivered.\n## Acceptance Criteria\n- [x] #1 Reports are useful."))
			f.commit("2026-01-03T12:00:00Z")
			f.write("nested/"+layout+"/completed/.keep", "")
			f.git("mv", name, "nested/"+layout+"/completed/Tâsk space.md")
			f.commit("2026-01-04T12:00:00Z")
			f.git("rm", "nested/"+layout+"/completed/Tâsk space.md")
			f.commit("2026-01-05T12:00:00Z")
			items := f.collect(board, nil)
			if len(items) != 2 {
				t.Fatalf("want baseline and work update, not metadata/moves/deletion: %+v", items)
			}
			if !strings.Contains(items[1].Fields["changes"], "initial recorded baseline") {
				t.Fatalf("root falsely treated as new work: %+v", items[1])
			}
		})
	}
}

func TestBacklogHistoryNonMonotonicAndMerge(t *testing.T) {
	f := newHistoryFixture(t)
	name := "backlog/tasks/task-1.md"
	f.write(name, historyTask("To Do", ""))
	f.commit("2026-01-01T12:00:00Z")
	f.git("switch", "-qc", "feature")
	f.write(name, historyTask("In Progress", ""))
	f.commit("2026-01-03T12:00:00Z")
	f.write(name, historyTask("Done", ""))
	f.commit("2026-01-02T12:00:00Z")
	f.git("switch", "-q", "main")
	f.git("merge", "--no-ff", "--no-commit", "feature")
	merged := f.commit("2026-01-04T12:00:00Z")
	f.write(name, historyTask("In Progress", ""))
	f.commit("2026-01-01T13:00:00Z")
	items := f.collect(f.root, &CollectOpts{
		Since: time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC),
	})
	if len(items) != 1 || items[0].Fields["commit"] != merged {
		t.Fatalf("merge duplicated or traversal stopped at old time: %+v", items)
	}
	c := eventChanges(t, items[0])["status"]
	if c.Before != "To Do" || c.After != "Done" {
		t.Fatalf("not a first-parent comparison: %+v", c)
	}
}

func TestBacklogHistoryDisabledAndErrors(t *testing.T) {
	ctx := context.Background()
	fake := newFakeBacklog()
	for _, setting := range []string{"", "false"} {
		items, err := (BacklogCollector{Run: fake.run}).CollectEvents(ctx, backlogDirective(map[string]string{
			"board": "/does-not-exist", "git_events": setting, "pull_command": "./sync.sh",
		}), nil)
		if err != nil || len(items) != 0 || len(fake.calls) != 0 {
			t.Fatalf("disabled events touched board: %v %v", items, err)
		}
	}
	for _, setting := range []string{"yes", "TRUE", "1"} {
		if _, err := parseBacklogConfig(backlogDirective(map[string]string{"board": ".", "git_events": setting})); err == nil {
			t.Fatalf("accepted invalid setting %q", setting)
		}
	}
	cfg := map[string]string{"board": t.TempDir(), "git_events": "true"}
	if _, err := (BacklogCollector{}).CollectEvents(ctx, backlogDirective(cfg), nil); err == nil {
		t.Fatal("non-Git board looked like successful empty history")
	}
	f := newHistoryFixture(t)
	if got := f.collect(f.root, nil); len(got) != 0 {
		t.Fatalf("unborn repo: %+v", got)
	}
	f.write("backlog/tasks/task-1.md", "not frontmatter")
	f.commit("2026-01-01T12:00:00Z")
	cfg["board"] = f.root
	if _, err := (BacklogCollector{}).CollectEvents(ctx, backlogDirective(cfg), nil); err == nil || !strings.Contains(err.Error(), "frontmatter") {
		t.Fatalf("want malformed task error, got %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := (BacklogCollector{}).CollectEvents(cancelled, backlogDirective(cfg), nil); err == nil {
		t.Fatal("cancelled collection succeeded")
	}
	cfg["pull_command"] = "exit 7"
	if _, err := (BacklogCollector{}).CollectEvents(ctx, backlogDirective(cfg), nil); err == nil || !strings.Contains(err.Error(), "pull_command") {
		t.Fatalf("want sync failure, got %v", err)
	}
}

func TestBacklogHistoryShallowBoundary(t *testing.T) {
	f := newHistoryFixture(t)
	f.write("backlog/tasks/task-1.md", historyTask("To Do", ""))
	f.commit("2026-01-01T12:00:00Z")
	f.write("backlog/tasks/task-1.md", historyTask("Done", ""))
	f.commit("2026-01-02T12:00:00Z")
	clone := filepath.Join(t.TempDir(), "shallow")
	f.git("clone", "-q", "--depth=1", "file://"+f.root, clone)
	cfg := map[string]string{"board": clone, "git_events": "true"}
	_, err := (BacklogCollector{}).CollectEvents(context.Background(), backlogDirective(cfg), nil)
	if err == nil || !strings.Contains(err.Error(), "shallow") {
		t.Fatalf("shallow boundary fabricated a creation: %v", err)
	}
}

func TestBacklogHistoryMultipleTasksAndHeavyRename(t *testing.T) {
	f := newHistoryFixture(t)
	f.write("backlog/tasks/a.md", historyTask("To Do", "## Description\nOld body"))
	f.commit("2026-01-01T12:00:00Z")
	f.git("rm", "backlog/tasks/a.md")
	f.write("backlog/completed/renamed.md", historyTask("Done", "## Description\nEntirely rewritten body\n## Final Summary\nDelivered new capability."))
	f.write("backlog/tasks/new.md", strings.Replace(historyTask("To Do", "## Description\nNew task"), "TASK-1", "CUSTOM-2.1", 1))
	f.commit("2026-01-02T12:00:00Z")
	items := f.collect(f.root, &CollectOpts{Since: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)})
	if len(items) != 2 {
		t.Fatalf("wanted one event per task: %+v", items)
	}
	for _, item := range items {
		c := eventChanges(t, item)
		switch item.Fields["id"] {
		case "TASK-1":
			if c["status"].Before != "To Do" || c["status"].After != "Done" || c["record"].After != "" {
				t.Fatalf("identity lost during heavy rename: %+v", c)
			}
		case "CUSTOM-2.1":
			if c["record"].After != "first recorded on board" {
				t.Fatalf("new task misidentified: %+v", c)
			}
		default:
			t.Fatalf("unexpected task: %+v", item)
		}
	}
}

func TestBacklogHistoryDuplicateIDsAndDoctor(t *testing.T) {
	f := newHistoryFixture(t)
	f.write("backlog/tasks/a.md", historyTask("To Do", ""))
	f.write("backlog/tasks/b.md", historyTask("Done", ""))
	f.commit("2026-01-01T12:00:00Z")
	cfg := map[string]string{"board": f.root, "git_events": "true", "command": "sh"}
	_, err := (BacklogCollector{}).CollectEvents(context.Background(), backlogDirective(cfg), nil)
	if err == nil || !strings.Contains(err.Error(), "duplicate task ID") {
		t.Fatalf("ambiguous task silently selected: %v", err)
	}
	cfg["board"] = t.TempDir()
	issues := (BacklogCollector{}).ValidateDirective(context.Background(), backlogDirective(cfg), nil)
	if len(issues) != 1 || issues[0].Field != "config.git_events" {
		t.Fatalf("non-Git board diagnostics: %+v", issues)
	}
	cfg["git_events"] = "invalid"
	issues = (BacklogCollector{}).ValidateDirective(context.Background(), backlogDirective(cfg), nil)
	if len(issues) != 1 || issues[0].Field != "config.git_events" {
		t.Fatalf("invalid config diagnostics: %+v", issues)
	}
}

func TestBacklogSnapshotSections(t *testing.T) {
	task, err := parseBacklogSnapshot([]byte(historyTask("To Do", "## Status\nNot a status field\n## Implementation Notes\n<!-- SECTION:NOTES:BEGIN -->\nNote\n<!-- SECTION:NOTES:END -->")))
	if err != nil || task.Sections["implementation notes"] != "Note" {
		t.Fatalf("sections: %+v %v", task, err)
	}
	for _, text := range []string{"---\nid: [invalid]\n---\n", "---\nid: x\n---bad", "---\nid: x"} {
		if _, err := parseBacklogSnapshot([]byte(text)); err == nil {
			t.Fatalf("accepted malformed task: %q", text)
		}
	}
}
