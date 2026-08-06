package agentsession

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHandoffWritesTheBriefingIntoTheWorktree(t *testing.T) {
	r := &fakeRunner{provider: ProviderClaude}
	r.turn = func(_ context.Context, _ TurnRequest, emit func(Event)) (TurnResult, error) {
		emit(Event{Kind: KindTool, Tool: "Edit", Text: "src/app.ts"})
		emit(Event{Kind: KindText, Text: "Renamed the handler."})
		return TurnResult{Text: "Renamed the handler."}, nil
	}
	m := newManager(t, r)
	sess, err := m.Start(context.Background(), StartRequest{
		Title: "SALSA-1 rename", Repo: "Chip/salsa", Branch: "salsa-1-rename", Prompt: "rename it",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, sess.ID, StatusIdle)

	got, path, err := m.Handoff(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(got.Dir, HandoffFile); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	// Everything the next agent needs to orient itself without the transcript:
	// what the work is, where it is, what was asked, and what was done.
	for _, want := range []string{
		"SALSA-1 rename", "Chip/salsa", "salsa-1-rename", "rename it",
		"Renamed the handler.", "Edit src/app.ts",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("handoff is missing %q:\n%s", want, body)
		}
	}
}

// Promotion means handing the worktree over, and two agents in one worktree
// corrupt each other, so the running turn has to be stopped -- not merely asked.
func TestHandoffStopsARunningTurnFirst(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	r := &fakeRunner{provider: ProviderClaude}
	r.turn = func(ctx context.Context, _ TurnRequest, _ func(Event)) (TurnResult, error) {
		close(started)
		select {
		case <-ctx.Done():
			return TurnResult{}, ctx.Err()
		case <-release:
			return TurnResult{Text: "done"}, nil
		}
	}
	defer close(release)

	m := newManager(t, r)
	sess, err := m.Start(context.Background(), StartRequest{Branch: "b", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	<-started

	got, path, err := m.Handoff(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == StatusRunning {
		t.Fatalf("status = %s, want the turn to have been stopped", got.Status)
	}
	if m.isLive(sess.ID) {
		t.Fatal("the turn is still live after a handoff")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("handoff file: %v", err)
	}
}

func TestHandoffNeedsAWorktree(t *testing.T) {
	m := newManager(t, &fakeRunner{provider: ProviderClaude})
	if err := m.Store.Save(Session{
		ID: "no-dir", Provider: ProviderClaude, Status: StatusIdle,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Handoff("no-dir"); err == nil {
		t.Fatal("expected an error for a session with no worktree")
	}
}

func TestHandoffOfAnUnknownSession(t *testing.T) {
	m := newManager(t, &fakeRunner{provider: ProviderClaude})
	if _, _, err := m.Handoff("nope"); err == nil {
		t.Fatal("expected ErrNotFound")
	}
}

// Only the last few turns are quoted: a handoff nobody reads is no handoff.
func TestHandoffTruncatesALongConversation(t *testing.T) {
	var events []Event
	for i := 0; i < handoffTurns+4; i++ {
		events = append(events,
			Event{Kind: KindPrompt, Text: "ask " + string(rune('a'+i))},
			Event{Kind: KindText, Text: "answer " + string(rune('a'+i))},
		)
	}
	body := renderHandoff(Session{Title: "long", Status: StatusIdle}, events)
	if !strings.Contains(body, "4 earlier turns omitted") {
		t.Errorf("expected the omission to be stated:\n%s", body)
	}
	if strings.Contains(body, "ask a") {
		t.Errorf("the oldest turn should have been dropped:\n%s", body)
	}
	if !strings.Contains(body, "ask g") {
		t.Errorf("the newest turn should have been kept:\n%s", body)
	}
}

// The likeliest thing a promoted agent does is `git add -A`. If the handoff is
// not hidden from git, the transcript summary lands in the branch and then the
// PR, which is the one outcome a handoff must not cause.
func TestHandoffIsHiddenFromGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	m := newManager(t, &fakeRunner{provider: ProviderClaude})
	if err := m.Store.Save(Session{
		ID: "s", Provider: ProviderClaude, Status: StatusIdle, Dir: dir,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Handoff("s"); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command("git", "-C", dir, "status", "--porcelain", "--untracked-files=all").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), HandoffFile) {
		t.Fatalf("git still sees the handoff:\n%s", out)
	}
}

func TestPromptTextNamesTheFileAndTheWork(t *testing.T) {
	got := PromptText(Session{Title: "SALSA-1 rename", Branch: "salsa-1-rename"})
	if !strings.Contains(got, HandoffFile) {
		t.Errorf("prompt does not name the file: %q", got)
	}
	if !strings.Contains(got, "SALSA-1 rename") {
		t.Errorf("prompt does not name the work: %q", got)
	}
	// A session with no title still gets a usable prompt rather than a dangling
	// preposition.
	bare := PromptText(Session{})
	if strings.Contains(bare, " on ,") || strings.Contains(bare, "on ,") {
		t.Errorf("prompt reads badly with no title: %q", bare)
	}
}
