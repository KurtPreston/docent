package agentsession

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Manager owns running agent sessions: it provisions their worktrees, runs turns
// in the background, persists every event, and fans them out to live subscribers.
//
// Execution lives here rather than in a separate worker process. The queue-to-disk
// handoff docent had before assumed a worker that was never installed, so every
// agent action queued forever and nothing said so. A session is more useful as a
// visible lane you can watch than as a job you hope someone drains.
type Manager struct {
	// Store persists sessions and transcripts. Required.
	Store *Store
	// Runners maps a provider to its implementation. Required for the providers
	// actually used; a missing one is an error at start rather than a panic.
	Runners map[Provider]Runner
	// Provision resolves the worktree a session runs in. It is a function rather
	// than a dependency on the provisioning package so the manager stays
	// testable, and so the policy for where an agent runs stays with the caller
	// that knows the config.
	Provision func(ctx context.Context, req ProvisionRequest) (ProvisionResult, error)
	// ForeignAgent reports an agent docent does not control -- an IDE window's
	// own -- that appears to be working in dir, as a phrase to put in the error,
	// or "" when the worktree looks free. Optional: nil means docent guards only
	// against its own sessions, which is what it did before.
	//
	// A function for the same reason Provision is one. The evidence lives in the
	// daemon's session registry, and how stale a report may be before it is
	// ignored is config the manager should not have to know.
	ForeignAgent func(dir string) string
	// PreTurn runs before a turn is admitted, only for a session whose worktree
	// is docent's own, and is skipped when the caller forced. Returning an error
	// refuses the turn: this is where a branch that has forked from the
	// developer's copy is caught, before an agent adds to the fork.
	//
	// Optional, and a function for the same reason Provision is one -- the
	// manager stays free of git.
	PreTurn func(ctx context.Context, sess Session, force bool) error
	// AfterTurn runs once a turn has finished, whatever its outcome, and only
	// for a session whose worktree is docent's own. It is where the turn's edits
	// are committed, so nothing an agent did is left only in a directory the
	// developer has never opened.
	//
	// Its error is recorded on the transcript and otherwise ignored: the turn
	// has already happened, and reporting it as failed because the bookkeeping
	// afterwards did not work would misdescribe it.
	AfterTurn func(ctx context.Context, sess Session, res *TurnResult, turnErr error) error
	// Now is overridable for tests.
	Now func() time.Time
	// TurnTimeout bounds a single turn. Zero means DefaultTurnTimeout.
	TurnTimeout time.Duration
	// AllowedTools restricts Claude turns. Empty means the CLI's default.
	AllowedTools []string

	mu sync.Mutex
	// live tracks in-flight turns by session id, which is what makes "one agent
	// per worktree" enforceable and stopping possible.
	live map[string]*liveTurn
	// subs holds SSE subscribers by session id. Kept out of liveTurn so a
	// subscriber can attach to an idle session and still receive the next turn.
	subs map[string]map[chan Event]struct{}
}

// ProvisionRequest asks for the worktree a session should run in.
type ProvisionRequest struct {
	Repo     string
	Branch   string
	BaseRef  string
	OpenPath string
	// Target is the placement the user picked, opaque here: the manager passes
	// it to the provisioner, which is the only part that knows what the
	// placements are.
	Target string
}

// ProvisionResult is a resolved worktree.
type ProvisionResult struct {
	// Dir is the worktree the agent edits.
	Dir string
	// Project is the root that owns Dir, for display and for handoff.
	Project string
	// Owned reports that Dir is docent's own directory. Stated by whoever
	// provisioned it rather than inferred here from the shape of a path.
	Owned bool
}

type liveTurn struct {
	cancel context.CancelFunc
	// dir is recorded so a second session in the same worktree can be refused
	// even though it has a different session id.
	dir string
	// done closes once the turn goroutine has written its last event, which is
	// what lets Delete wait rather than race the transcript it is removing.
	done chan struct{}
}

// StartRequest opens a new session.
type StartRequest struct {
	Provider Provider
	Model    string
	Mode     Mode
	Title    string
	Repo     string
	Branch   string
	// Dir skips provisioning and runs in this directory. Provisioning is the
	// normal path; this exists for a session against an existing checkout.
	Dir string
	// BaseRef is the ref a brand-new branch is created from.
	BaseRef string
	// OpenPath is a known local path for this work item, the best hint for which
	// project to provision in.
	OpenPath string
	// Target is where the agent should run, from the placements docent offered
	// for this repo and branch. Empty means the provisioner's default.
	Target string
	// Prompt, when set, is run as the opening turn.
	Prompt string
	// StagedAttachmentIDs are upload ids to promote into this session before the
	// opening turn runs.
	StagedAttachmentIDs []string
	// Color is the lane color, normally derived from the branch name.
	Color string
	// Force starts even when ForeignAgent says someone else is working in the
	// worktree. It does not override docent's own in-flight turns.
	Force bool
}

// Errors callers are expected to distinguish.
var (
	// ErrBusy means a turn is already running for this session or worktree. Two
	// agents in one worktree share a git index and corrupt each other, so this
	// is refused rather than queued.
	ErrBusy = errors.New("agentsession: a turn is already running in this worktree")
	// ErrForeignAgent means an agent docent does not control appears to be
	// mid-turn in the worktree. Kept distinct from ErrBusy because it is a
	// weaker claim: docent cannot stop that agent, and the report is inferred
	// from an editor's own reporting rather than known. Callers are expected to
	// offer the user an override instead of only refusing.
	ErrForeignAgent = errors.New("agentsession: another agent appears to be working in this worktree")
	// ErrDiverged means docent's copy of the branch and the developer's have
	// both moved since they last agreed. Refused rather than reconciled: the
	// only honest resolutions are a merge and a rebase, and doing either to
	// somebody's work while they are not looking is not docent's call. Callers
	// are expected to offer the same override ErrForeignAgent gets.
	ErrDiverged = errors.New("agentsession: this branch has diverged from your copy")
	// ErrNoRunner means no runner is registered for the requested provider.
	ErrNoRunner = errors.New("agentsession: no runner for provider")
)

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now().UTC()
	}
	return time.Now().UTC()
}

func (m *Manager) runnerFor(p Provider) (Runner, error) {
	if r, ok := m.Runners[p]; ok && r != nil {
		return r, nil
	}
	return nil, fmt.Errorf("%w %q", ErrNoRunner, p)
}

// Start opens a session: it provisions the worktree, mints a session id, writes
// the record, and (when a prompt is given) begins the opening turn in the
// background. It returns as soon as the record exists, so the caller can hand
// back an id and let the UI subscribe to the transcript.
func (m *Manager) Start(ctx context.Context, req StartRequest) (Session, error) {
	if m.Store == nil {
		return Session{}, errors.New("agentsession: manager has no store")
	}
	provider := req.Provider
	if provider == "" {
		provider = ProviderClaude
	}
	runner, err := m.runnerFor(provider)
	if err != nil {
		return Session{}, err
	}

	// A caller-supplied directory is never docent's: it is an existing checkout
	// somebody named, so ownership has to be claimed by a provisioner rather
	// than assumed.
	dir, project, owned := strings.TrimSpace(req.Dir), "", false
	if dir == "" {
		if m.Provision == nil {
			return Session{}, errors.New("agentsession: no dir given and no provisioner configured")
		}
		res, err := m.Provision(ctx, ProvisionRequest{
			Repo: req.Repo, Branch: req.Branch, BaseRef: req.BaseRef,
			OpenPath: req.OpenPath, Target: req.Target,
		})
		if err != nil {
			return Session{}, err
		}
		dir, project, owned = res.Dir, res.Project, res.Owned
	}
	if dir == "" {
		return Session{}, errors.New("agentsession: provisioning produced no directory")
	}

	// Refuse before minting an id: a session that can never run its first turn
	// is worse than no session, because it looks like a lane that is merely idle.
	if busy := m.dirBusy(dir); busy != "" {
		return Session{}, fmt.Errorf("%w (session %s)", ErrBusy, busy)
	}
	if !req.Force {
		if who := m.foreignAgent(dir); who != "" {
			return Session{}, fmt.Errorf("%w: %s", ErrForeignAgent, who)
		}
	}

	id, err := runner.NewSession(ctx, dir)
	if err != nil {
		return Session{}, err
	}
	if err := validID(id); err != nil {
		return Session{}, fmt.Errorf("agentsession: %s returned an unusable session id %q", provider, id)
	}

	now := m.now()
	sess := Session{
		ID: id, Provider: provider, Model: req.Model, Mode: req.Mode, Title: req.Title,
		Repo: req.Repo, Branch: req.Branch, Dir: dir, Project: project, Owned: owned,
		Color: req.Color, Status: StatusIdle, CreatedAt: now, UpdatedAt: now,
	}
	if err := m.Store.Save(sess); err != nil {
		return Session{}, err
	}
	atts, err := m.promoteAttachments(id, req.StagedAttachmentIDs)
	if err != nil {
		return sess, err
	}
	if strings.TrimSpace(req.Prompt) == "" && len(atts) == 0 {
		return sess, nil
	}
	// The opening turn inherits Force: having already accepted the conflict and
	// created the session, refusing its first turn would leave exactly the
	// idle-looking lane the check above exists to prevent.
	if err := m.turn(id, req.Prompt, req.Force, atts...); err != nil {
		return sess, err
	}
	return m.Store.Get(id)
}

func (m *Manager) promoteAttachments(sessionID string, stagedIDs []string) ([]Attachment, error) {
	if len(stagedIDs) == 0 {
		return nil, nil
	}
	store, err := NewAttachmentStore(m.Store.Root())
	if err != nil {
		return nil, err
	}
	return store.Promote(sessionID, stagedIDs)
}

// Turn starts a turn in the background and returns once it is running, so an
// HTTP caller is not held for the minutes a turn takes. Progress arrives through
// Subscribe and is persisted regardless of whether anyone is listening.
func (m *Manager) Turn(id, prompt string, atts ...Attachment) error {
	return m.turn(id, prompt, false, atts...)
}

// TurnForce is Turn with the foreign-agent check skipped, for a user who has
// been shown what else is running there and asked for it anyway. docent's own
// in-flight turns are still refused: that conflict is certain rather than
// inferred, and there is nothing to weigh.
func (m *Manager) TurnForce(id, prompt string, atts ...Attachment) error {
	return m.turn(id, prompt, true, atts...)
}

// SetMode updates a session's cursor-agent execution mode. Refused while a turn
// is running, because the flag is per-invocation and the current process is
// already in flight.
func (m *Manager) SetMode(id string, mode Mode) (Session, error) {
	if _, err := m.Store.Get(id); err != nil {
		return Session{}, err
	}
	m.mu.Lock()
	if _, running := m.live[id]; running {
		m.mu.Unlock()
		return Session{}, fmt.Errorf("%w (session %s)", ErrBusy, id)
	}
	m.mu.Unlock()
	return m.Store.Update(id, func(s *Session) {
		s.Mode = mode
	})
}

func (m *Manager) turn(id, prompt string, force bool, atts ...Attachment) error {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" && len(atts) == 0 {
		return errors.New("agentsession: a turn needs a prompt")
	}
	sess, err := m.Store.Get(id)
	if err != nil {
		return err
	}
	runner, err := m.runnerFor(sess.Provider)
	if err != nil {
		return err
	}
	// Checked outside the critical section below: it calls out to the caller's
	// registry, which takes its own lock, and holding m.mu across that invites a
	// lock-order problem for no benefit.
	if !force {
		if who := m.foreignAgent(sess.Dir); who != "" {
			return fmt.Errorf("%w: %s", ErrForeignAgent, who)
		}
	}
	if err := m.preTurn(sess, force); err != nil {
		return err
	}

	// Claim the worktree and the session in one critical section, so two
	// concurrent requests cannot both see it free.
	m.mu.Lock()
	if m.live == nil {
		m.live = map[string]*liveTurn{}
	}
	if _, running := m.live[id]; running {
		m.mu.Unlock()
		return fmt.Errorf("%w (session %s)", ErrBusy, id)
	}
	for other, lt := range m.live {
		if lt.dir == sess.Dir {
			m.mu.Unlock()
			return fmt.Errorf("%w (session %s)", ErrBusy, other)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	lt := &liveTurn{cancel: cancel, dir: sess.Dir, done: make(chan struct{})}
	m.live[id] = lt
	m.mu.Unlock()

	if _, err := m.Store.Update(id, func(s *Session) {
		s.Status = StatusRunning
		s.Error = ""
	}); err != nil {
		m.release(id)
		close(lt.done)
		cancel()
		return err
	}
	// The prompt is part of the transcript: without it the record shows an agent
	// acting for no visible reason.
	m.record(id, Event{Kind: KindPrompt, Text: prompt, Attachments: atts, SessionID: id, At: m.now()})
	// Paired with the one finish writes, so a subscriber tracks the lane's state
	// from the stream alone and never has to poll to notice a turn began.
	m.record(id, Event{Kind: KindStatus, Text: string(StatusRunning), SessionID: id, At: m.now()})

	go m.runTurn(ctx, lt, runner, sess, prompt, atts)
	return nil
}

func (m *Manager) runTurn(ctx context.Context, lt *liveTurn, runner Runner, sess Session, prompt string, atts []Attachment) {
	// Ordered so that by the time done closes, nothing will write again: release
	// first (freeing the worktree), then signal completion.
	defer close(lt.done)
	defer lt.cancel()
	defer m.release(sess.ID)

	stdinPrompt := PromptWithAttachments(prompt, atts)
	res, err := runner.Turn(ctx, TurnRequest{
		SessionID: sess.ID,
		Prompt:    stdinPrompt,
		Dir:       sess.Dir,
		Attachments: atts,
		// Claude distinguishes opening a session from resuming one, and the turn
		// count is the only reliable record of which this is: the process that
		// ran the first turn is long gone.
		First:        sess.Turns == 0,
		AllowedTools: m.AllowedTools,
		Model:        sess.Model,
		Mode:         sess.Mode,
		Timeout:      m.TurnTimeout,
	}, func(ev Event) {
		if ev.SessionID == "" {
			ev.SessionID = sess.ID
		}
		m.record(sess.ID, ev)
	})

	// Before releasing the worktree slot, so docent's own tree is still the one
	// being committed at the turn boundary.
	m.afterTurn(sess, &res, err)

	// Status idle must not be visible while the worktree is still reserved in
	// live: a subscriber (or the next Turn) that sees idle would otherwise race
	// the deferred release and get ErrBusy.
	m.release(sess.ID)

	switch {
	case err != nil && ctx.Err() != nil:
		// Cancelled by Stop. The conversation survives inside the CLI, so this
		// is a pause rather than an end, and the session stays resumable.
		m.record(sess.ID, Event{Kind: KindStopped, SessionID: sess.ID, At: m.now()})
		m.finish(sess.ID, StatusStopped, "", nil)
	case err != nil:
		msg := err.Error()
		m.record(sess.ID, Event{Kind: KindError, Error: msg, SessionID: sess.ID, At: m.now()})
		m.finish(sess.ID, StatusFailed, msg, nil)
	case res.IsError:
		// The agent ran and reported failure. That is a completed turn with a
		// bad outcome, not a broken session, so the transcript already carries
		// the error event the runner emitted.
		m.finish(sess.ID, StatusFailed, res.Text, &res)
	default:
		m.finish(sess.ID, StatusIdle, "", &res)
	}
}

// finish writes the terminal state and wakes subscribers. The turn count is
// incremented for any turn that actually ran, including a failed one: Claude
// will not accept --session-id twice, so a retried first turn must resume.
func (m *Manager) finish(id string, status Status, errMsg string, res *TurnResult) {
	_, _ = m.Store.Update(id, func(s *Session) {
		s.Status = status
		s.Error = errMsg
		s.Turns++
		if res != nil {
			s.LastResult = res
		}
	})
	// Persisted, not merely broadcast: a subscriber that reconnects after the
	// turn ended replays the transcript, and without this its last event would
	// be mid-turn output, leaving the lane looking stuck forever.
	m.record(id, Event{Kind: KindStatus, Text: string(status), Error: errMsg, SessionID: id, At: m.now()})
}

func (m *Manager) release(id string) {
	m.mu.Lock()
	delete(m.live, id)
	m.mu.Unlock()
}

// preTurn runs the pre-turn guard, which is a no-op for any worktree that is
// not docent's: the developer's directory is theirs to leave in whatever state
// they like, and docent has no business fetching into it or judging it.
func (m *Manager) preTurn(sess Session, force bool) error {
	if m.PreTurn == nil || !sess.Owned || force {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), turnBoundaryTimeout)
	defer cancel()
	return m.PreTurn(ctx, sess, force)
}

// afterTurn commits what the turn did. Same ownership rule as preTurn, and a
// context of its own: the turn's has usually just been cancelled, and a stopped
// turn is precisely the case where the snapshot matters most.
func (m *Manager) afterTurn(sess Session, res *TurnResult, turnErr error) {
	if m.AfterTurn == nil || !sess.Owned {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), turnBoundaryTimeout)
	defer cancel()
	if err := m.AfterTurn(ctx, sess, res, turnErr); err != nil {
		m.record(sess.ID, Event{Kind: KindError, Error: err.Error(), SessionID: sess.ID, At: m.now()})
	}
}

// turnBoundaryTimeout bounds the git work either side of a turn. Long enough
// for a fetch of a large repository over a slow link, short enough that a
// credential prompt nobody can answer does not hold a turn forever.
const turnBoundaryTimeout = 3 * time.Minute

func (m *Manager) foreignAgent(dir string) string {
	if m.ForeignAgent == nil {
		return ""
	}
	return strings.TrimSpace(m.ForeignAgent(dir))
}

func (m *Manager) dirBusy(dir string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, lt := range m.live {
		if lt.dir == dir {
			return id
		}
	}
	return ""
}

// record persists an event and then fans it out. Persist-then-broadcast, so a
// subscriber that reconnects and replays from the store can never see less than
// it already saw live.
func (m *Manager) record(id string, ev Event) {
	if ev.At.IsZero() {
		ev.At = m.now()
	}
	_ = m.Store.Append(id, ev)
	m.broadcast(id, ev)
}

func (m *Manager) broadcast(id string, ev Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for ch := range m.subs[id] {
		select {
		case ch <- ev:
		default:
			// A subscriber too slow to keep up is dropped for this event rather
			// than allowed to block the turn. Nothing is lost: the transcript on
			// disk is the record, and a reconnect replays it.
		}
	}
}

// Subscribe returns the transcript so far plus a channel of future events, so a
// late subscriber sees the whole session and a live one sees it as it happens.
// cancel must be called when done.
func (m *Manager) Subscribe(id string) (replay []Event, ch <-chan Event, cancel func(), err error) {
	if _, err := m.Store.Get(id); err != nil {
		return nil, nil, nil, err
	}
	// Read the transcript while holding the lock so no event can slip between
	// the replay and the subscription, which would drop it from both.
	m.mu.Lock()
	replay, _ = m.Store.Events(id)
	out := make(chan Event, 256)
	if m.subs == nil {
		m.subs = map[string]map[chan Event]struct{}{}
	}
	if m.subs[id] == nil {
		m.subs[id] = map[chan Event]struct{}{}
	}
	m.subs[id][out] = struct{}{}
	m.mu.Unlock()

	var once sync.Once
	cancel = func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			if set := m.subs[id]; set != nil {
				delete(set, out)
				if len(set) == 0 {
					delete(m.subs, id)
				}
			}
			close(out)
		})
	}
	return replay, out, cancel, nil
}

// Stop cancels the running turn. The session remains resumable: the provider
// keeps the conversation, so the next Turn continues where this left off.
func (m *Manager) Stop(id string) error {
	m.mu.Lock()
	lt, ok := m.live[id]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("agentsession: session %s has no running turn", id)
	}
	lt.cancel()
	return nil
}

// Sessions lists persisted sessions, reconciling any that a restart left marked
// running. A record saying "running" with no process behind it is the worst
// possible lie for a cockpit: it shows work in progress that will never finish.
func (m *Manager) Sessions() ([]Session, error) {
	all, err := m.Store.List()
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	live := make(map[string]bool, len(m.live))
	for id := range m.live {
		live[id] = true
	}
	m.mu.Unlock()

	for i, s := range all {
		if s.Status != StatusRunning || live[s.ID] {
			continue
		}
		fixed, err := m.Store.Update(s.ID, func(u *Session) {
			u.Status = StatusStopped
			u.Error = "interrupted by a docentd restart"
		})
		if err == nil {
			all[i] = fixed
		}
	}
	return all, nil
}

// Get returns one session, reconciling a stale running status the same way
// Sessions does.
func (m *Manager) Get(id string) (Session, error) {
	sess, err := m.Store.Get(id)
	if err != nil {
		return Session{}, err
	}
	if sess.Status != StatusRunning || m.isLive(id) {
		return sess, nil
	}
	return m.Store.Update(id, func(u *Session) {
		u.Status = StatusStopped
		u.Error = "interrupted by a docentd restart"
	})
}

func (m *Manager) isLive(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.live[id]
	return ok
}

// Delete removes a session. A running turn is stopped and waited for first: the
// agent must not be left editing a worktree with no record of why, and removing
// the transcript while the turn is still appending to it would leave a directory
// behind that reads back as a corrupt session.
func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	lt := m.live[id]
	m.mu.Unlock()
	if lt != nil {
		lt.cancel()
		select {
		case <-lt.done:
		case <-time.After(deleteDrainTimeout):
			// The turn is wedged past cancellation. Deleting anyway is the right
			// call: the record is what the user asked to be rid of, and Append
			// refuses to recreate a deleted session's directory.
		}
	}
	return m.Store.Delete(id)
}

// deleteDrainTimeout bounds how long a delete waits for a cancelled turn to
// stop writing. Generous relative to how long a killed subprocess takes to
// unwind, short relative to a user waiting on an HTTP response.
const deleteDrainTimeout = 10 * time.Second
