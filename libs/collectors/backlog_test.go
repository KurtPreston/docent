package collectors

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KurtPreston/docent/libs/config/userdata"
)

// fakeBacklog serves canned CLI output keyed by the joined command line and
// records every command it was asked to run.
type fakeBacklog struct {
	out   map[string]string
	errs  map[string]error
	calls []BacklogCommand
}

func (f *fakeBacklog) run(ctx context.Context, cmd BacklogCommand, opts *CollectOpts) ([]byte, error) {
	f.calls = append(f.calls, cmd)
	key := strings.Join(append([]string{cmd.Name}, cmd.Args...), " ")
	if err := f.errs[key]; err != nil {
		return nil, err
	}
	return []byte(f.out[key]), nil
}

const backlogListJSON = `{"tasks":[
 {"id":"TASK-1","title":"Queued task","status":"To Do","archived":false,"priority":"high","project":"P","labels":["agent","docs"],"dueDate":"2026-01-02","isReady":true},
 {"id":"TASK-2","title":"Blocked task","status":"To Do","archived":false,"labels":["agent"],"dueDate":null,"isReady":false},
 {"id":"TASK-3","title":"Running task","status":"In Progress","archived":false,"labels":["agent"],"isReady":true},
 {"id":"TASK-4","title":"Unlabeled","status":"To Do","archived":false,"labels":[],"isReady":true},
 {"id":"TASK-5","title":"Archived","status":"To Do","archived":true,"labels":["agent"],"isReady":true},
 {"id":"TASK-6","title":"Finished","status":"Done","archived":false,"labels":[],"isReady":true}
]}`

const backlogViewJSON = `{"schemaVersion":1,"kind":"task","task":{"id":"TASK-1","title":"Queued task","status":"To Do",
 "path":"backlog/tasks/task-1 - Queued-task.md",
 "implementationPlan":"\nRepo: my-org/my-repo\nBranch: my-branch\nBase-Ref: main\nStatus: ignored\n\nDo the thing.\nNot: a header"}}`

func newFakeBacklog() *fakeBacklog {
	return &fakeBacklog{
		out: map[string]string{
			"backlog task list --json":        backlogListJSON,
			"backlog task view TASK-1 --json": backlogViewJSON,
			"backlog task view TASK-2 --json": `{"id":"TASK-2","implementationPlan":"Repo: my-org/other"}`,
			"my-backlog -x task list --json":  `{"tasks":[]}`,
			"sh -c ./sync.sh":                 "",
			"backlog task view TASK-3 --json": `{"task":{"id":"TASK-3"}}`,
		},
		errs: map[string]error{},
	}
}

func backlogDirective(cfg map[string]string) userdata.Directive {
	return userdata.Directive{ID: "tasks", Collector: "backlog", Enabled: true, Config: cfg}
}

func collectBacklog(t *testing.T, fake *fakeBacklog, cfg map[string]string) map[string]StatusItem {
	t.Helper()
	c := BacklogCollector{Run: fake.run}
	items, err := c.CollectState(context.Background(), backlogDirective(cfg), &CollectOpts{})
	if err != nil {
		t.Fatalf("CollectState: %v", err)
	}
	byID := map[string]StatusItem{}
	for _, it := range items {
		byID[it.Fields["id"]] = it
	}
	return byID
}

func TestBacklogCollector_FieldsMapping(t *testing.T) {
	board := t.TempDir()
	fake := newFakeBacklog()
	items := collectBacklog(t, fake, map[string]string{"board": board, "queue_label": "agent"})

	// Archived tasks are dropped; everything else is reported
	if len(items) != 5 {
		t.Fatalf("got %d items, want 5: %v", len(items), items)
	}
	if _, ok := items["TASK-5"]; ok {
		t.Fatalf("archived task should be skipped")
	}

	// The item carries identity and the task's own fields
	it := items["TASK-1"]
	if it.Source != "backlog" || it.Kind != "task" || it.StableID != "task:tasks:TASK-1" || it.Title != "Queued task" {
		t.Fatalf("unexpected identity: %+v", it)
	}
	if !it.ObservedAt.IsZero() {
		t.Fatalf("ObservedAt should stay unset for state")
	}
	want := map[string]string{
		"status": "To Do", "labels": "agent,docs", "priority": "high", "project": "P",
		"due_date": "2026-01-02", "ready": "true", "queued": "true",
		"file": filepath.Join(board, "backlog/tasks/task-1 - Queued-task.md"),
	}
	for k, v := range want {
		if it.Fields[k] != v {
			t.Errorf("field %s = %q, want %q", k, it.Fields[k], v)
		}
	}
	if _, ok := it.Fields["path"]; ok {
		t.Errorf("path must not be set; automations read it as the work's checkout")
	}

	// Every command runs against the board
	for _, call := range fake.calls {
		if call.Dir != board || !containsString(call.Env, "BACKLOG_CWD="+board) || call.DirectiveID != "tasks" {
			t.Errorf("command %v not pointed at the board: dir=%q env=%v", call.Args, call.Dir, call.Env)
		}
	}
}

func TestBacklogCollector_QueuedLogic(t *testing.T) {
	fake := newFakeBacklog()
	items := collectBacklog(t, fake, map[string]string{"board": t.TempDir(), "queue_label": "agent"})
	cases := map[string]string{
		"TASK-1": "true",  // labeled, To Do, ready
		"TASK-2": "false", // not ready
		"TASK-3": "false", // wrong status
		"TASK-4": "false", // no label
		"TASK-6": "false", // done, unlabeled
	}
	for id, want := range cases {
		if got := items[id].Fields["queued"]; got != want {
			t.Errorf("%s queued = %q, want %q", id, got, want)
		}
	}

	// Only labeled tasks in the queue status are viewed
	var viewed []string
	for _, call := range fake.calls {
		if len(call.Args) > 2 && call.Args[1] == "view" {
			viewed = append(viewed, call.Args[2])
		}
	}
	if strings.Join(viewed, ",") != "TASK-1,TASK-2" {
		t.Fatalf("viewed %v, want TASK-1 and TASK-2", viewed)
	}

	// A custom queue status moves the queue to that column
	items = collectBacklog(t, newFakeBacklog(), map[string]string{"board": t.TempDir(), "queue_label": "agent", "queue_status": "in progress"})
	if items["TASK-3"].Fields["queued"] != "true" || items["TASK-1"].Fields["queued"] != "false" {
		t.Errorf("queue_status should move the queue to In Progress")
	}
}

func TestBacklogCollector_NoQueueLabel(t *testing.T) {
	fake := newFakeBacklog()
	items := collectBacklog(t, fake, map[string]string{"board": t.TempDir()})
	for id, it := range items {
		if it.Fields["queued"] != "false" {
			t.Errorf("%s queued without a queue_label", id)
		}
	}
	if len(fake.calls) != 1 {
		t.Fatalf("got %d calls, want only the list", len(fake.calls))
	}
}

func TestBacklogCollector_StatusFilter(t *testing.T) {
	items := collectBacklog(t, newFakeBacklog(), map[string]string{"board": t.TempDir(), "statuses": "to do, IN PROGRESS"})
	if len(items) != 4 {
		t.Fatalf("got %d items, want 4 (TASK-1 to TASK-4)", len(items))
	}
	if _, ok := items["TASK-6"]; ok {
		t.Fatalf("Done task should be filtered out")
	}
}

func TestBacklogCollector_PlanHeaders(t *testing.T) {
	items := collectBacklog(t, newFakeBacklog(), map[string]string{"board": t.TempDir(), "queue_label": "agent"})

	// Leading header lines become fields; reserved names are kept
	it := items["TASK-1"]
	if it.Fields["repo"] != "my-org/my-repo" || it.Fields["branch"] != "my-branch" || it.Fields["base_ref"] != "main" {
		t.Fatalf("headers not parsed: %v", it.Fields)
	}
	if it.Fields["status"] != "To Do" {
		t.Fatalf("header must not override status: %q", it.Fields["status"])
	}
	if _, ok := it.Fields["not"]; ok {
		t.Fatalf("lines after the header block must not become fields")
	}
	if !strings.Contains(it.Fields["plan"], "Do the thing.") {
		t.Fatalf("plan text missing: %q", it.Fields["plan"])
	}

	// A bare (unwrapped) view response is accepted too
	if items["TASK-2"].Fields["repo"] != "my-org/other" {
		t.Fatalf("bare view not parsed: %v", items["TASK-2"].Fields)
	}
}

// A Branch header git would refuse holds the task out of the queue and says why,
// instead of starting an agent whose worktree can never be created.
func TestBacklogCollector_InvalidBranchHeader(t *testing.T) {
	fake := newFakeBacklog()
	fake.out["backlog task view TASK-1 --json"] = `{"task":{"id":"TASK-1",` +
		`"implementationPlan":"Repo: my-org/my-repo\nBranch: fix-login (existing PR branch)\n\nDo it."}}`
	items := collectBacklog(t, fake, map[string]string{"board": t.TempDir(), "queue_label": "agent"})

	it := items["TASK-1"]
	if it.Fields["queued"] != "false" || it.Fields["plan_valid"] != "false" {
		t.Fatalf("queued=%q plan_valid=%q, want both false", it.Fields["queued"], it.Fields["plan_valid"])
	}
	if _, ok := it.Fields["branch"]; ok {
		t.Errorf("an invalid branch must not reach automations: %q", it.Fields["branch"])
	}
	if want := `Branch header: branch name "fix-login (existing PR branch)" contains ' '`; it.Fields["plan_error"] != want {
		t.Errorf("plan_error = %q, want %q", it.Fields["plan_error"], want)
	}
	if it.Fields["repo"] != "my-org/my-repo" {
		t.Errorf("other headers should still be read: %v", it.Fields)
	}

	// A valid brief is marked so, with no error
	ok := collectBacklog(t, newFakeBacklog(), map[string]string{"board": t.TempDir(), "queue_label": "agent"})["TASK-1"]
	if ok.Fields["plan_valid"] != "true" || ok.Fields["plan_error"] != "" || ok.Fields["queued"] != "true" {
		t.Errorf("valid plan: plan_valid=%q plan_error=%q queued=%q", ok.Fields["plan_valid"], ok.Fields["plan_error"], ok.Fields["queued"])
	}
}

// A brief without a Branch: header gets a branch elected from its title, and
// stays queued; one with a header keeps it.
func TestBacklogCollector_ElectsMissingBranch(t *testing.T) {
	fake := newFakeBacklog()
	fake.out["backlog task view TASK-1 --json"] = `{"task":{"id":"TASK-1","implementationPlan":"Repo: my-org/my-repo\n\nDo it."}}`
	it := collectBacklog(t, fake, map[string]string{"board": t.TempDir(), "queue_label": "agent"})["TASK-1"]
	if it.Fields["branch"] != "queued-task" || it.Fields["branch_elected"] != "true" {
		t.Fatalf("branch=%q branch_elected=%q", it.Fields["branch"], it.Fields["branch_elected"])
	}
	if it.Fields["queued"] != "true" || it.Fields["plan_valid"] != "true" {
		t.Errorf("queued=%q plan_valid=%q, want both true", it.Fields["queued"], it.Fields["plan_valid"])
	}

	// A configured prefix starts the elected name
	fake = newFakeBacklog()
	fake.out["backlog task view TASK-1 --json"] = `{"task":{"id":"TASK-1","implementationPlan":"Repo: my-org/my-repo\n\nDo it."}}`
	cfg := map[string]string{"board": t.TempDir(), "queue_label": "agent", "branch_prefix": "agent/"}
	if got := collectBacklog(t, fake, cfg)["TASK-1"].Fields["branch"]; got != "agent/queued-task" {
		t.Errorf("prefixed branch = %q", got)
	}

	// A header's own branch is used as written, prefix or not
	named := collectBacklog(t, newFakeBacklog(), cfg)["TASK-1"]
	if named.Fields["branch"] != "my-branch" || named.Fields["branch_elected"] != "" {
		t.Errorf("branch=%q branch_elected=%q", named.Fields["branch"], named.Fields["branch_elected"])
	}
}

func TestElectBranch(t *testing.T) {
	cases := []struct{ prefix, title, id, want string }{
		{"", "Fix the login timeout", "TASK-7", "fix-the-login-timeout"},
		{"", "  Café: über -- résumé!! ", "TASK-7", "caf-ber-r-sum"},
		{"", "Raise the login timeout to thirty seconds and cover it with a test", "TASK-7", "raise-the-login-timeout-to-thirty-seconds-and"},
		{"", "日本語", "TASK-7", "task-7"},
		{"", "", "", ""},
		{"agent/", "Fix it", "TASK-7", "agent/fix-it"},
	}
	for _, tc := range cases {
		if got := electBranch(tc.prefix, tc.title, tc.id); got != tc.want {
			t.Errorf("electBranch(%q, %q, %q) = %q, want %q", tc.prefix, tc.title, tc.id, got, tc.want)
		}
	}
}

func TestParsePlanHeaders(t *testing.T) {
	got := parsePlanHeaders("Repo: my-org/my-repo\nWork Dir:  services/api/  \n# Steps\nBranch: late")
	if len(got) != 2 || got["repo"] != "my-org/my-repo" || got["work_dir"] != "services/api/" {
		t.Fatalf("got %v", got)
	}
	if len(parsePlanHeaders("Just prose: with a colon, then more")) != 1 {
		t.Fatalf("a first line shaped like a header is a header")
	}
	if len(parsePlanHeaders("# Plan\nRepo: x")) != 0 {
		t.Fatalf("headers must lead the plan")
	}
}

func TestBacklogCollector_CustomCommandAndPull(t *testing.T) {
	fake := newFakeBacklog()
	cfg := map[string]string{"board": t.TempDir(), "command": "my-backlog -x", "pull_command": "./sync.sh"}
	collectBacklog(t, fake, cfg)
	if len(fake.calls) != 2 || fake.calls[0].Name != "sh" || fake.calls[1].Name != "my-backlog" {
		t.Fatalf("unexpected calls: %+v", fake.calls)
	}
}

func TestBacklogCollector_Errors(t *testing.T) {
	ctx := context.Background()

	// A failing pull_command surfaces as a collect error, before listing
	fake := newFakeBacklog()
	fake.errs["sh -c ./sync.sh"] = errors.New("exit status 1")
	c := BacklogCollector{Run: fake.run}
	_, err := c.CollectState(ctx, backlogDirective(map[string]string{"board": t.TempDir(), "pull_command": "./sync.sh"}), nil)
	if err == nil || !strings.Contains(err.Error(), "pull_command") {
		t.Fatalf("want pull_command error, got %v", err)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("list should not run after a failed pull")
	}

	// A missing board and unparseable output are errors too
	if _, err := c.CollectState(ctx, backlogDirective(nil), nil); err == nil {
		t.Fatalf("want error for missing board")
	}
	fake.out["backlog task list --json"] = "not json"
	if _, err := c.CollectState(ctx, backlogDirective(map[string]string{"board": t.TempDir()}), nil); err == nil {
		t.Fatalf("want error for bad JSON")
	}
}

func TestBacklogCollector_ValidateDirective(t *testing.T) {
	c := BacklogCollector{}
	ctx := context.Background()

	// Missing board
	issues := c.ValidateDirective(ctx, backlogDirective(nil), &ValidateOpts{})
	if len(issues) != 1 || issues[0].Field != "config.board" {
		t.Fatalf("got %+v", issues)
	}

	// Board that doesn't exist and a command that isn't installed
	missing := filepath.Join(t.TempDir(), "nope")
	issues = c.ValidateDirective(ctx, backlogDirective(map[string]string{"board": missing, "command": "no-such-backlog-cli"}), nil)
	if len(issues) != 2 {
		t.Fatalf("got %+v", issues)
	}

	// An existing board and a command on PATH pass
	issues = c.ValidateDirective(ctx, backlogDirective(map[string]string{"board": t.TempDir(), "command": "sh"}), nil)
	if len(issues) != 0 {
		t.Fatalf("got %+v", issues)
	}

	// A branch prefix no branch name could start with is reported
	issues = c.ValidateDirective(ctx, backlogDirective(map[string]string{"board": t.TempDir(), "branch_prefix": "my agent/"}), nil)
	if len(issues) != 1 || issues[0].Field != "config.branch_prefix" {
		t.Fatalf("got %+v", issues)
	}
}

func TestBacklogCollector_Registered(t *testing.T) {
	state, events := NewRegistry(nil).Capabilities("backlog")
	if !state || !events {
		t.Fatalf("backlog should support state and opt-in events, got state=%v events=%v", state, events)
	}
}
