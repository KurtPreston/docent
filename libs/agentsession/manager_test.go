package agentsession

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRunner is a scripted Runner. turn is called in place of a real CLI, so
// timing, failure, and cancellation are all deterministic.
type fakeRunner struct {
	provider Provider
	newID    func() string
	turn     func(ctx context.Context, req TurnRequest, emit func(Event)) (TurnResult, error)

	mu   sync.Mutex
	reqs []TurnRequest
}

func (f *fakeRunner) Provider() Provider { return f.provider }

func (f *fakeRunner) NewSession(context.Context, string) (string, error) {
	if f.newID != nil {
		return f.newID(), nil
	}
	return "sess-1", nil
}

func (f *fakeRunner) Turn(ctx context.Context, req TurnRequest, emit func(Event)) (TurnResult, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	if f.turn != nil {
		return f.turn(ctx, req, emit)
	}
	emit(Event{Kind: KindText, Text: "ok"})
	return TurnResult{Text: "ok"}, nil
}

func (f *fakeRunner) requests() []TurnRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]TurnRequest, len(f.reqs))
	copy(out, f.reqs)
	return out
}

func newManager(t *testing.T, r *fakeRunner) *Manager {
	t.Helper()
	return &Manager{
		Store:   newStore(t),
		Runners: map[Provider]Runner{r.provider: r},
		Provision: func(context.Context, ProvisionRequest) (ProvisionResult, error) {
			return ProvisionResult{Dir: t.TempDir(), Project: "/home/k/Code/salsa"}, nil
		},
	}
}

// waitFor polls until cond holds, so tests do not depend on a fixed sleep.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func waitStatus(t *testing.T, m *Manager, id string, want Status) Session {
	t.Helper()
	var got Session
	waitFor(t, "session "+id+" to become "+string(want), func() bool {
		s, err := m.Store.Get(id)
		if err != nil {
			return false
		}
		got = s
		return s.Status == want
	})
	return got
}

func TestStartCreatesAResumableSession(t *testing.T) {
	r := &fakeRunner{provider: ProviderClaude, newID: func() string { return "abc-123" }}
	m := newManager(t, r)

	sess, err := m.Start(context.Background(), StartRequest{
		Provider: ProviderClaude, Repo: "Chip/salsa", Branch: "SALSA-1/fix",
		Title: "fix the thing", Color: "#abcdef",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sess.ID != "abc-123" {
		t.Errorf("id = %q, want the runner's", sess.ID)
	}
	// Idle, not running: a session with no prompt has nothing in flight, and
	// showing it as running would be a lane that never finishes.
	if sess.Status != StatusIdle {
		t.Errorf("status = %q, want idle", sess.Status)
	}
	if sess.Dir == "" || sess.Project == "" {
		t.Errorf("provisioning result not recorded: %+v", sess)
	}
	if sess.Branch != "SALSA-1/fix" || sess.Color != "#abcdef" || sess.Title != "fix the thing" {
		t.Errorf("lane fields lost: %+v", sess)
	}
	// It survives the manager: that is the whole point of persisting.
	fresh := &Manager{Store: m.Store, Runners: m.Runners}
	if got, err := fresh.Get("abc-123"); err != nil || got.Branch != "SALSA-1/fix" {
		t.Errorf("not readable by a new manager: %+v (err %v)", got, err)
	}
}

func TestStartWithAPromptRunsTheOpeningTurn(t *testing.T) {
	r := &fakeRunner{provider: ProviderClaude}
	m := newManager(t, r)

	if _, err := m.Start(context.Background(), StartRequest{Prompt: "do the thing"}); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, "sess-1", StatusIdle)

	reqs := r.requests()
	if len(reqs) != 1 {
		t.Fatalf("ran %d turns, want 1", len(reqs))
	}
	if reqs[0].Prompt != "do the thing" {
		t.Errorf("prompt = %q", reqs[0].Prompt)
	}
	// Claude refuses --session-id twice, so the opening turn must be marked.
	if !reqs[0].First {
		t.Error("the opening turn was not marked First")
	}
	if reqs[0].Dir == "" {
		t.Error("the turn ran with no directory")
	}
}

// A prompt that only opened a session is invisible without this, and the
// transcript would show an agent acting for no reason.
func TestThePromptIsInTheTranscript(t *testing.T) {
	r := &fakeRunner{provider: ProviderClaude}
	m := newManager(t, r)
	if _, err := m.Start(context.Background(), StartRequest{Prompt: "fix a.ts"}); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, "sess-1", StatusIdle)

	evs, err := m.Store.Events("sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) == 0 || evs[0].Kind != KindPrompt || evs[0].Text != "fix a.ts" {
		t.Fatalf("first event = %+v, want the prompt", evs)
	}
	if !hasKind(evs, KindText) {
		t.Error("the agent's own output is missing from the transcript")
	}
	if !hasKind(evs, KindStatus) {
		t.Error("no terminal status event: a subscriber would never learn the turn ended")
	}
}

func hasKind(evs []Event, k EventKind) bool {
	for _, e := range evs {
		if e.Kind == k {
			return true
		}
	}
	return false
}

// The second turn must resume rather than reopen, and the turn count is the only
// record of which this is: the process that ran the first turn is long gone.
func TestLaterTurnsResume(t *testing.T) {
	r := &fakeRunner{provider: ProviderClaude}
	m := newManager(t, r)
	if _, err := m.Start(context.Background(), StartRequest{Prompt: "one"}); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, "sess-1", StatusIdle)
	if err := m.Turn("sess-1", "two"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the second turn", func() bool { return len(r.requests()) == 2 })
	waitStatus(t, m, "sess-1", StatusIdle)

	reqs := r.requests()
	if reqs[0].First == reqs[1].First {
		t.Fatalf("both turns had First=%v", reqs[0].First)
	}
	if reqs[1].First {
		t.Error("the second turn tried to open the session again")
	}
	if sess, _ := m.Store.Get("sess-1"); sess.Turns != 2 {
		t.Errorf("turns = %d, want 2", sess.Turns)
	}
}

// A failed opening turn still consumed the session id, so a retry has to resume.
func TestARetryAfterAFailedFirstTurnResumes(t *testing.T) {
	var n int
	r := &fakeRunner{provider: ProviderClaude}
	r.turn = func(context.Context, TurnRequest, func(Event)) (TurnResult, error) {
		n++
		if n == 1 {
			return TurnResult{}, errors.New("network blip")
		}
		return TurnResult{Text: "ok"}, nil
	}
	m := newManager(t, r)
	if _, err := m.Start(context.Background(), StartRequest{Prompt: "one"}); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, "sess-1", StatusFailed)

	if err := m.Turn("sess-1", "again"); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, "sess-1", StatusIdle)
	reqs := r.requests()
	if len(reqs) != 2 || reqs[1].First {
		t.Errorf("the retry re-opened the session: %+v", reqs)
	}
}

func TestFailedTurnRecordsWhy(t *testing.T) {
	r := &fakeRunner{provider: ProviderClaude}
	r.turn = func(context.Context, TurnRequest, func(Event)) (TurnResult, error) {
		return TurnResult{}, errors.New("claude: not logged in")
	}
	m := newManager(t, r)
	if _, err := m.Start(context.Background(), StartRequest{Prompt: "go"}); err != nil {
		t.Fatal(err)
	}
	sess := waitStatus(t, m, "sess-1", StatusFailed)
	if !strings.Contains(sess.Error, "not logged in") {
		t.Errorf("error = %q, want the reason", sess.Error)
	}
	evs, _ := m.Store.Events("sess-1")
	if !hasKind(evs, KindError) {
		t.Error("no error event in the transcript")
	}
}

// An agent that ran and reported failure is a completed turn with a bad outcome,
// not a broken session, and the distinction decides whether a retry resumes.
func TestAgentReportedFailureIsAFinishedTurn(t *testing.T) {
	r := &fakeRunner{provider: ProviderClaude}
	r.turn = func(_ context.Context, _ TurnRequest, emit func(Event)) (TurnResult, error) {
		res := TurnResult{Text: "hit the tool limit", IsError: true}
		emit(Event{Kind: KindError, Error: res.Text, Result: &res})
		return res, nil
	}
	m := newManager(t, r)
	if _, err := m.Start(context.Background(), StartRequest{Prompt: "go"}); err != nil {
		t.Fatal(err)
	}
	sess := waitStatus(t, m, "sess-1", StatusFailed)
	if sess.Turns != 1 {
		t.Errorf("turns = %d, want the turn to count", sess.Turns)
	}
	if sess.LastResult == nil || !sess.LastResult.IsError {
		t.Errorf("result not kept: %+v", sess.LastResult)
	}
}

// Two agents in one worktree share a git index and corrupt each other, so a
// second turn is refused rather than queued behind the first.
func TestOneAgentPerWorktree(t *testing.T) {
	release := make(chan struct{})
	r := &fakeRunner{provider: ProviderClaude}
	r.turn = func(ctx context.Context, _ TurnRequest, _ func(Event)) (TurnResult, error) {
		select {
		case <-release:
		case <-ctx.Done():
			return TurnResult{}, ctx.Err()
		}
		return TurnResult{Text: "ok"}, nil
	}
	m := newManager(t, r)
	if _, err := m.Start(context.Background(), StartRequest{Prompt: "one"}); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, "sess-1", StatusRunning)

	if err := m.Turn("sess-1", "two"); !errors.Is(err, ErrBusy) {
		t.Fatalf("second turn err = %v, want ErrBusy", err)
	}
	close(release)
	waitStatus(t, m, "sess-1", StatusIdle)
	// Once the turn is done the session accepts work again.
	if err := m.Turn("sess-1", "three"); err != nil {
		t.Fatalf("turn after completion: %v", err)
	}
	waitStatus(t, m, "sess-1", StatusIdle)
}

// The rule is per worktree, not per session: two sessions sharing a directory
// would collide just as badly as two turns in one session.
func TestASecondSessionInTheSameWorktreeIsRefused(t *testing.T) {
	release := make(chan struct{})
	ids := []string{"sess-a", "sess-b"}
	var n int
	r := &fakeRunner{provider: ProviderClaude, newID: func() string {
		id := ids[n%len(ids)]
		n++
		return id
	}}
	r.turn = func(ctx context.Context, _ TurnRequest, _ func(Event)) (TurnResult, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return TurnResult{Text: "ok"}, nil
	}
	shared := t.TempDir()
	m := &Manager{
		Store:   newStore(t),
		Runners: map[Provider]Runner{ProviderClaude: r},
		Provision: func(context.Context, ProvisionRequest) (ProvisionResult, error) {
			return ProvisionResult{Dir: shared}, nil
		},
	}
	if _, err := m.Start(context.Background(), StartRequest{Prompt: "one"}); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, "sess-a", StatusRunning)

	_, err := m.Start(context.Background(), StartRequest{Prompt: "two"})
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
	// And it left no half-made record: a lane that can never run its first turn
	// looks merely idle, which is worse than no lane.
	if _, err := m.Store.Get("sess-b"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a session was created anyway: %v", err)
	}
	// Let the held turn finish and settle before the test's temp dirs are
	// removed, so cleanup does not race the store's last write.
	close(release)
	waitStatus(t, m, "sess-a", StatusIdle)
}

// An agent docent did not start is the conflict it cannot see from its own
// bookkeeping, and the one most likely to happen: the developer opens the
// worktree in Cursor and prompts it there.
func TestStartRefusesAWorktreeAForeignAgentIsWorkingIn(t *testing.T) {
	r := &fakeRunner{provider: ProviderClaude}
	m := newManager(t, r)
	var asked []string
	m.ForeignAgent = func(dir string) string {
		asked = append(asked, dir)
		return "a cursor agent, since 14:02"
	}

	_, err := m.Start(context.Background(), StartRequest{Prompt: "go"})
	if !errors.Is(err, ErrForeignAgent) {
		t.Fatalf("err = %v, want ErrForeignAgent", err)
	}
	// The reason is the whole value of the refusal: "busy" with no account of
	// what is busy leaves the user with nothing to act on.
	if !strings.Contains(err.Error(), "since 14:02") {
		t.Errorf("error should carry the reason, got %q", err)
	}
	if _, err := m.Store.Get("sess-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a session was created anyway: %v", err)
	}
	if len(asked) != 1 {
		t.Errorf("asked about %d dirs, want the provisioned one only: %v", len(asked), asked)
	}
}

// The refusal is a warning, not a lock: docent is inferring this from an
// editor's own reporting, so the user has to be able to overrule it.
func TestForceStartsDespiteAForeignAgent(t *testing.T) {
	r := &fakeRunner{provider: ProviderClaude}
	m := newManager(t, r)
	m.ForeignAgent = func(string) string { return "a cursor agent" }

	if _, err := m.Start(context.Background(), StartRequest{Prompt: "go", Force: true}); err != nil {
		t.Fatal(err)
	}
	// Force has to reach the opening turn too, or the session exists but its
	// first turn was refused -- the idle-looking dead lane.
	waitStatus(t, m, "sess-1", StatusIdle)
	if len(r.requests()) != 1 {
		t.Errorf("opening turn did not run: %+v", r.requests())
	}
}

func TestTurnRefusesAndTurnForceOverrides(t *testing.T) {
	r := &fakeRunner{provider: ProviderClaude}
	m := newManager(t, r)
	if _, err := m.Start(context.Background(), StartRequest{}); err != nil {
		t.Fatal(err)
	}
	m.ForeignAgent = func(string) string { return "a cursor agent" }

	if err := m.Turn("sess-1", "follow up"); !errors.Is(err, ErrForeignAgent) {
		t.Fatalf("turn err = %v, want ErrForeignAgent", err)
	}
	if err := m.TurnForce("sess-1", "follow up anyway"); err != nil {
		t.Fatalf("forced turn: %v", err)
	}
	waitStatus(t, m, "sess-1", StatusIdle)
}

// docent's own in-flight turn is a certainty, not an inference, so Force has
// nothing to weigh and must not override it.
func TestForceDoesNotOverrideDocentsOwnTurn(t *testing.T) {
	release := make(chan struct{})
	r := &fakeRunner{provider: ProviderClaude}
	r.turn = func(ctx context.Context, _ TurnRequest, _ func(Event)) (TurnResult, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return TurnResult{Text: "ok"}, nil
	}
	m := newManager(t, r)
	if _, err := m.Start(context.Background(), StartRequest{Prompt: "one"}); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, "sess-1", StatusRunning)

	if err := m.TurnForce("sess-1", "two"); !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
	close(release)
	waitStatus(t, m, "sess-1", StatusIdle)
}

// Stopping is a pause, not an end: the conversation lives inside the CLI, so the
// session must stay resumable.
func TestStopLeavesTheSessionResumable(t *testing.T) {
	started := make(chan struct{})
	r := &fakeRunner{provider: ProviderClaude}
	var once sync.Once
	r.turn = func(ctx context.Context, req TurnRequest, _ func(Event)) (TurnResult, error) {
		if req.First {
			once.Do(func() { close(started) })
			<-ctx.Done()
			return TurnResult{}, ctx.Err()
		}
		return TurnResult{Text: "resumed"}, nil
	}
	m := newManager(t, r)
	if _, err := m.Start(context.Background(), StartRequest{Prompt: "long one"}); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := m.Stop("sess-1"); err != nil {
		t.Fatal(err)
	}
	sess := waitStatus(t, m, "sess-1", StatusStopped)
	if sess.Error != "" {
		t.Errorf("stopping recorded an error %q; a deliberate stop is not a failure", sess.Error)
	}
	evs, _ := m.Store.Events("sess-1")
	if !hasKind(evs, KindStopped) {
		t.Error("no stopped event in the transcript")
	}
	if err := m.Turn("sess-1", "carry on"); err != nil {
		t.Fatalf("could not resume after stopping: %v", err)
	}
	waitStatus(t, m, "sess-1", StatusIdle)
}

func TestStopWithNothingRunningIsAnError(t *testing.T) {
	r := &fakeRunner{provider: ProviderClaude}
	m := newManager(t, r)
	if _, err := m.Start(context.Background(), StartRequest{}); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop("sess-1"); err == nil {
		t.Fatal("want an error")
	}
}

// A record saying "running" with no process behind it is the worst lie a cockpit
// can tell: it shows work in progress that will never finish.
func TestARestartReconcilesStaleRunningSessions(t *testing.T) {
	st := newStore(t)
	if err := st.Save(Session{ID: "ghost", Status: StatusRunning, Branch: "b"}); err != nil {
		t.Fatal(err)
	}
	m := &Manager{Store: st, Runners: map[Provider]Runner{}}

	got, err := m.Get("ghost")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusStopped {
		t.Errorf("status = %q, want stopped", got.Status)
	}
	if !strings.Contains(got.Error, "restart") {
		t.Errorf("error = %q, want it to say why", got.Error)
	}
	// And the fix is persisted, not just returned.
	if reread, _ := st.Get("ghost"); reread.Status != StatusStopped {
		t.Errorf("not persisted: %+v", reread)
	}
	list, err := m.Sessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Status != StatusStopped {
		t.Errorf("Sessions did not reconcile: %+v", list)
	}
}

// A genuinely running session must not be reconciled out from under itself.
func TestALiveSessionIsNotReconciled(t *testing.T) {
	release := make(chan struct{})
	r := &fakeRunner{provider: ProviderClaude}
	r.turn = func(ctx context.Context, _ TurnRequest, _ func(Event)) (TurnResult, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return TurnResult{}, nil
	}
	m := newManager(t, r)
	if _, err := m.Start(context.Background(), StartRequest{Prompt: "go"}); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, "sess-1", StatusRunning)

	got, err := m.Get("sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusRunning {
		t.Fatalf("a live session was reconciled to %q", got.Status)
	}
	close(release)
	waitStatus(t, m, "sess-1", StatusIdle)
}

// A subscriber gets the whole story: what already happened, then what happens
// next, with nothing lost in between.
func TestSubscribeReplaysThenStreams(t *testing.T) {
	gate := make(chan struct{})
	r := &fakeRunner{provider: ProviderClaude}
	r.turn = func(_ context.Context, req TurnRequest, emit func(Event)) (TurnResult, error) {
		if !req.First {
			<-gate
			emit(Event{Kind: KindText, Text: "second turn"})
		}
		return TurnResult{Text: "ok"}, nil
	}
	m := newManager(t, r)
	if _, err := m.Start(context.Background(), StartRequest{Prompt: "one"}); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, "sess-1", StatusIdle)

	replay, ch, cancel, err := m.Subscribe("sess-1")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if len(replay) == 0 || replay[0].Kind != KindPrompt {
		t.Fatalf("replay = %+v, want the first turn's transcript", replay)
	}

	if err := m.Turn("sess-1", "two"); err != nil {
		t.Fatal(err)
	}
	close(gate)

	var live []Event
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-ch:
			live = append(live, ev)
			// A turn brackets itself with two status events; the terminal one
			// is what ends the stream for this test.
			if ev.Kind == KindStatus && ev.Text != string(StatusRunning) {
				if !hasKind(live, KindText) {
					t.Errorf("live events missed the agent's output: %+v", live)
				}
				if live[0].Kind != KindPrompt {
					t.Errorf("the second turn's prompt did not stream: %+v", live[0])
				}
				if live[1].Kind != KindStatus || live[1].Text != string(StatusRunning) {
					t.Errorf("the turn did not announce itself as running: %+v", live[1])
				}
				return
			}
		case <-deadline:
			t.Fatalf("timed out; got %+v", live)
		}
	}
}

func TestSubscribeToAnUnknownSessionFails(t *testing.T) {
	m := &Manager{Store: newStore(t)}
	if _, _, _, err := m.Subscribe("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// Cancelling twice happens naturally when a request ends while the stream is
// also shutting down, and a double close would take the daemon down with it.
func TestSubscribeCancelIsIdempotent(t *testing.T) {
	m := &Manager{Store: newStore(t), Runners: map[Provider]Runner{}}
	if err := m.Store.Save(Session{ID: "s1"}); err != nil {
		t.Fatal(err)
	}
	_, _, cancel, err := m.Subscribe("s1")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	cancel()
}

// A subscriber too slow to keep up must not stall the turn; the transcript on
// disk stays complete regardless.
func TestASlowSubscriberDoesNotBlockTheTurn(t *testing.T) {
	const n = 2000
	r := &fakeRunner{provider: ProviderClaude}
	r.turn = func(_ context.Context, _ TurnRequest, emit func(Event)) (TurnResult, error) {
		for i := 0; i < n; i++ {
			emit(Event{Kind: KindText, Text: "x"})
		}
		return TurnResult{Text: "ok"}, nil
	}
	m := newManager(t, r)
	if err := m.Store.Save(Session{ID: "sess-1", Provider: ProviderClaude, Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	_, _, cancel, err := m.Subscribe("sess-1")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	if err := m.Turn("sess-1", "flood"); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, "sess-1", StatusIdle)

	evs, err := m.Store.Events("sess-1")
	if err != nil {
		t.Fatal(err)
	}
	var texts int
	for _, e := range evs {
		if e.Kind == KindText {
			texts++
		}
	}
	if texts != n {
		t.Errorf("transcript has %d text events, want all %d", texts, n)
	}
}

func TestTurnRejectsAnEmptyPromptAndUnknownSession(t *testing.T) {
	r := &fakeRunner{provider: ProviderClaude}
	m := newManager(t, r)
	if _, err := m.Start(context.Background(), StartRequest{}); err != nil {
		t.Fatal(err)
	}
	if err := m.Turn("sess-1", "   "); err == nil {
		t.Error("an empty prompt was accepted")
	}
	if err := m.Turn("ghost", "hi"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
	if len(r.requests()) != 0 {
		t.Error("a runner was invoked for a rejected turn")
	}
}

func TestStartRequiresAKnownProvider(t *testing.T) {
	m := &Manager{Store: newStore(t), Runners: map[Provider]Runner{}}
	_, err := m.Start(context.Background(), StartRequest{Provider: "gemini"})
	if !errors.Is(err, ErrNoRunner) {
		t.Fatalf("err = %v, want ErrNoRunner", err)
	}
}

// Provisioning is where a missing grove project surfaces, and the reason has to
// reach the caller rather than becoming a session that cannot run.
func TestProvisioningFailureIsReported(t *testing.T) {
	r := &fakeRunner{provider: ProviderClaude}
	m := &Manager{
		Store:   newStore(t),
		Runners: map[Provider]Runner{ProviderClaude: r},
		Provision: func(context.Context, ProvisionRequest) (ProvisionResult, error) {
			return ProvisionResult{}, errors.New("no grove project for \"Chip/salsa\"")
		},
	}
	_, err := m.Start(context.Background(), StartRequest{Repo: "Chip/salsa", Branch: "b"})
	if err == nil || !strings.Contains(err.Error(), "no grove project") {
		t.Fatalf("err = %v", err)
	}
	if list, _ := m.Store.List(); len(list) != 0 {
		t.Errorf("a session was persisted despite provisioning failing: %+v", list)
	}
}

// An explicit directory skips provisioning, which is how a session against an
// existing checkout works.
func TestStartWithAnExplicitDirSkipsProvisioning(t *testing.T) {
	r := &fakeRunner{provider: ProviderClaude}
	dir := t.TempDir()
	m := &Manager{
		Store:   newStore(t),
		Runners: map[Provider]Runner{ProviderClaude: r},
		Provision: func(context.Context, ProvisionRequest) (ProvisionResult, error) {
			t.Error("provisioning ran despite an explicit dir")
			return ProvisionResult{}, nil
		},
	}
	sess, err := m.Start(context.Background(), StartRequest{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if sess.Dir != dir {
		t.Errorf("dir = %q, want %q", sess.Dir, dir)
	}
}

func TestStartWithoutADirOrProvisionerFails(t *testing.T) {
	r := &fakeRunner{provider: ProviderClaude}
	m := &Manager{Store: newStore(t), Runners: map[Provider]Runner{ProviderClaude: r}}
	if _, err := m.Start(context.Background(), StartRequest{}); err == nil {
		t.Fatal("want an error")
	}
}

// Deleting a lane must not leave an agent editing a worktree with no record of
// why it is doing so.
func TestDeleteStopsARunningTurn(t *testing.T) {
	stopped := make(chan struct{})
	r := &fakeRunner{provider: ProviderClaude}
	r.turn = func(ctx context.Context, _ TurnRequest, _ func(Event)) (TurnResult, error) {
		<-ctx.Done()
		close(stopped)
		return TurnResult{}, ctx.Err()
	}
	m := newManager(t, r)
	if _, err := m.Start(context.Background(), StartRequest{Prompt: "go"}); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, "sess-1", StatusRunning)
	if err := m.Delete("sess-1"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the turn was not cancelled")
	}
	if _, err := m.Store.Get("sess-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("session survived deletion: %v", err)
	}
}
