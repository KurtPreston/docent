package collectors

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// gitCountLog counts Git subcommands run through the collector exec logger.
type gitCountLog struct {
	mu    sync.Mutex
	count map[string]int
}

func (l *gitCountLog) Directive(string) DirectiveLogger                              { return l }
func (l *gitCountLog) LogHTTP(string, string, int, int, int64, time.Duration, error) {}
func (l *gitCountLog) Note(string, ...any)                                           {}
func (l *gitCountLog) LogExec(_ string, args []string, _, _, _ int, _ time.Duration, _ error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(args) > 1 {
		l.count[args[1]]++
	}
}

func (l *gitCountLog) get(sub string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.count[sub]
}

func numberedTask(id int, status, body string) string {
	return fmt.Sprintf("---\nid: TASK-%d\ntitle: Task %d\nstatus: %s\n---\n\n%s\n", id, id, status, body)
}

func historyFor(t *testing.T, board string, opts *CollectOpts) *backlogHistory {
	t.Helper()
	cfg, err := parseBacklogConfig(backlogDirective(map[string]string{"board": board, "git_events": "true"}))
	if err != nil {
		t.Fatal(err)
	}
	return newBacklogHistory(cfg, opts)
}

// referenceBacklogEvents recomputes every comparison with fresh, uncached
// histories, matching the collector's behavior before snapshot reuse.
func referenceBacklogEvents(t *testing.T, board string, since, until time.Time) ([]StatusItem, error) {
	t.Helper()
	ctx := context.Background()
	h := historyFor(t, board, nil)
	revisions, err := h.revisions(ctx)
	if err != nil {
		return nil, err
	}
	var items []StatusItem
	for _, rev := range revisions {
		if rev.at.Before(since) || rev.at.After(until) {
			continue
		}
		before := map[string]backlogSnapshotTask{}
		if rev.parent != "" {
			fresh := historyFor(t, board, nil)
			fresh.prefix = h.prefix
			if before, err = fresh.snapshot(ctx, rev.parent); err != nil {
				return nil, err
			}
		}
		fresh := historyFor(t, board, nil)
		fresh.prefix = h.prefix
		after, err := fresh.snapshot(ctx, rev.hash)
		if err != nil {
			return nil, err
		}
		items = append(items, backlogRevisionEvents(h.cfg.directiveID, rev, backlogSnapshots{before, after})...)
	}
	return items, nil
}

func TestBacklogHistoryReusesParsedTasksAndSnapshots(t *testing.T) {
	f := newHistoryFixture(t)
	for id := 1; id <= 3; id++ {
		f.write(fmt.Sprintf("backlog/tasks/task-%d.md", id), numberedTask(id, "To Do", ""))
	}
	f.commit("2026-01-01T12:00:00Z")
	for day, id := range []int{1, 2, 3} {
		f.write(fmt.Sprintf("backlog/tasks/task-%d.md", id), numberedTask(id, "Done", ""))
		f.commit(fmt.Sprintf("2026-01-0%dT12:00:00Z", day+2))
	}

	// Three in-window commits need four distinct snapshots and six blobs.
	log := &gitCountLog{count: map[string]int{}}
	h := historyFor(t, f.root, &CollectOpts{RunLog: log})
	since, until := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 9, 0, 0, 0, 0, time.UTC)
	items, err := h.events(context.Background(), since, until)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || h.snapshots != 4 || h.parses != 6 || log.get("ls-tree") != 4 {
		t.Fatalf("want 3 events from 4 snapshots, 6 parses, 4 ls-tree; got %d, %d, %d, %d",
			len(items), h.snapshots, h.parses, log.get("ls-tree"))
	}

	// A repeated run reuses every parsed task and returns identical events.
	again, err := h.events(context.Background(), since, until)
	if err != nil || h.parses != 6 || !reflect.DeepEqual(items, again) {
		t.Fatalf("cached rerun changed output or reparsed: parses=%d err=%v", h.parses, err)
	}
	want, err := referenceBacklogEvents(t, f.root, since, until)
	if err != nil || !reflect.DeepEqual(items, want) {
		t.Fatalf("reuse diverged from uncached reference: %v", err)
	}
}

func TestBacklogHistoryMatchesUncachedReference(t *testing.T) {
	f := newHistoryFixture(t)
	f.write("board/backlog/tasks/a.md", numberedTask(1, "To Do", "## Description\nShared body"))
	f.write("board/backlog/tasks/b.md", numberedTask(2, "To Do", "## Description\nShared body"))
	f.write("board/backlog/tasks/c.md", numberedTask(3, "To Do", ""))
	f.commit("2026-01-01T12:00:00Z")
	f.write("board/backlog/tasks/a.md", numberedTask(1, "In Progress", "## Description\nShared body\n## Implementation Notes\nStarted."))
	f.commit("2026-01-03T12:00:00Z")

	// A side branch with out-of-order times merges as one first-parent change.
	f.git("switch", "-qc", "feature")
	f.write("board/backlog/tasks/b.md", numberedTask(2, "Done", "## Description\nShared body"))
	f.commit("2026-01-05T12:00:00Z")
	f.git("switch", "-q", "main")
	f.write("board/backlog/tasks/c.md", numberedTask(3, "Waiting", ""))
	f.commit("2026-01-02T12:00:00Z")
	f.git("merge", "--no-ff", "--no-commit", "feature")
	f.commit("2026-01-04T12:00:00Z")

	// Heavy rename, archive move, and a historical layout move.
	f.git("rm", "-q", "board/backlog/tasks/a.md")
	f.write("board/backlog/archive/tasks/renamed.md", numberedTask(1, "Done", "## Final Summary\nRewritten."))
	f.commit("2026-01-06T12:00:00Z")
	f.write("board/backlog.config.yml", "backlog_directory: planning\n")
	f.git("mv", "board/backlog", "board/planning")
	f.write("board/planning/completed/c.md", numberedTask(3, "Done", ""))
	f.git("rm", "-qf", "board/planning/tasks/c.md")
	f.commit("2026-01-07T12:00:00Z")
	f.write("board/planning/tasks/b.md", numberedTask(2, "Done", "## Description\nShared body\n## Final Summary\nShipped."))
	f.commit("2026-01-08T12:00:00Z")

	// Compare full, inclusive-boundary, and gap-producing windows.
	board := f.root + "/board"
	day := func(d, h int) time.Time { return time.Date(2026, 1, d, h, 0, 0, 0, time.UTC) }
	windows := [][2]time.Time{{{}, day(31, 0)}, {day(3, 12), day(7, 12)}, {day(4, 0), day(9, 0)}, {day(2, 0), day(3, 12)}}
	for _, w := range windows {
		got, err := historyFor(t, board, nil).events(context.Background(), w[0], w[1])
		if err != nil {
			t.Fatal(err)
		}
		want, err := referenceBacklogEvents(t, board, w[0], w[1])
		if err != nil {
			t.Fatal(err)
		}
		if len(got) == 0 || !reflect.DeepEqual(got, want) {
			t.Fatalf("window %v: cached events differ from reference\n got %+v\nwant %+v", w, got, want)
		}
	}
}

func TestBacklogHistoryCachedBlobsKeepDuplicateAndParseErrors(t *testing.T) {
	f := newHistoryFixture(t)
	f.write("backlog/tasks/a.md", numberedTask(1, "To Do", ""))
	f.commit("2026-01-01T12:00:00Z")
	f.write("backlog/tasks/b.md", numberedTask(2, "To Do", ""))
	f.commit("2026-01-02T12:00:00Z")

	// The duplicate reuses an already parsed blob but must still be rejected.
	f.write("backlog/archive/tasks/a-copy.md", numberedTask(1, "To Do", ""))
	f.commit("2026-01-03T12:00:00Z")
	_, err := historyFor(t, f.root, nil).events(context.Background(), time.Time{}, time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
	if err == nil || !strings.Contains(err.Error(), "duplicate task ID TASK-1") {
		t.Fatalf("cached blob hid duplicate ID: %v", err)
	}
	_, refErr := referenceBacklogEvents(t, f.root, time.Time{}, time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
	if refErr == nil || refErr.Error() != err.Error() {
		t.Fatalf("error differs from reference: %v vs %v", err, refErr)
	}

	// A parse failure is not cached as a task in later lookups.
	f.git("rm", "-q", "backlog/archive/tasks/a-copy.md")
	f.commit("2026-01-04T12:00:00Z")
	f.write("backlog/tasks/c.md", "not frontmatter")
	f.commit("2026-01-05T12:00:00Z")
	h := historyFor(t, f.root, nil)
	for range 2 {
		_, err = h.events(context.Background(), time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC), time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
		if err == nil || !strings.Contains(err.Error(), "frontmatter") {
			t.Fatalf("want repeatable parse error, got %v", err)
		}
	}
	if len(h.tasks) != 2 {
		t.Fatalf("invalid blob cached: %d tasks", len(h.tasks))
	}
}
