package engine

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/KurtPreston/docent/apps/docentd/internal/config"
	"github.com/KurtPreston/docent/apps/docentd/internal/registry"
	"github.com/KurtPreston/docent/libs/agentsession"
	"github.com/KurtPreston/docent/libs/automation"
	"github.com/KurtPreston/docent/libs/collectors"
	"github.com/KurtPreston/docent/libs/config/docentconfig"
	"github.com/KurtPreston/docent/libs/config/userdata"
	"github.com/KurtPreston/docent/libs/model"
	"github.com/KurtPreston/docent/libs/worktree"
)

// agentSessionsDir holds sessions and transcripts, under the same state root as
// the rest of docent's durable data.
const agentSessionsDir = "agent-sessions"

// newAgentManager wires the session manager from daemon config: a store under
// the state dir, a runner per provider, and a worktree of docent's own per
// branch.
//
// sessions is the IDE session registry, consulted so docent does not start an
// agent in a worktree an editor's own agent is already editing. That guard is
// about the developer's directories; it can never fire for docent's own tree,
// where nobody else works.
func newAgentManager(cfg config.DaemonConfig, sessions *registry.Store) (*agentsession.Manager, error) {
	store, err := agentsession.NewStore(filepath.Join(docentconfig.StateDir(), agentSessionsDir))
	if err != nil {
		return nil, err
	}
	// Resolved once at startup, like the rest of the daemon's view of config.
	roots := collectors.LocalGitRoots(cfg.Directives)
	return &agentsession.Manager{
		Store: store,
		Runners: map[agentsession.Provider]agentsession.Runner{
			agentsession.ProviderClaude: agentsession.Claude{Command: cfg.AI.Claude.Command},
			agentsession.ProviderCursor: agentsession.Cursor{Command: cfg.AI.Cursor.Command},
		},
		ForeignAgent: func(dir string) string {
			return foreignAgentAt(sessions, cfg.Sessions, dir, time.Now())
		},
		Provision: func(ctx context.Context, req agentsession.ProvisionRequest) (agentsession.ProvisionResult, error) {
			wd, err := automation.ProvisionWorkdir(ctx, automation.WorkdirRequest{
				Mode:     automation.WorkdirWorktree,
				Repo:     req.Repo,
				Branch:   req.Branch,
				From:     req.BaseRef,
				OpenPath: req.OpenPath,
				Target:   req.Target,
				Roots:    roots,
				Hook:     cfg.WorktreeHook,
			})
			if err != nil {
				return agentsession.ProvisionResult{}, err
			}
			return agentsession.ProvisionResult{Dir: wd.Path, Project: wd.ProjectDir, Owned: wd.Owned}, nil
		},
		PreTurn:   syncBeforeTurn,
		AfterTurn: commitAfterTurn,
	}, nil
}

// syncBeforeTurn pulls the developer's commits into docent's worktree and
// refuses the turn when the two have forked.
//
// Only reached for docent's own directories; the manager checks that. The
// refusal is the point: a fork that nobody notices ends with two versions of a
// branch, each a plausible candidate for the real one. Better to stop and say so
// while both are still small, with force available for the case where the user
// has looked and wants the turn anyway.
func syncBeforeTurn(ctx context.Context, sess agentsession.Session, _ bool) error {
	res, err := worktree.Sync(ctx, worktree.SyncRequest{Dir: sess.Dir, Branch: sess.Branch})
	if err != nil {
		// Reported, not fatal: an agent that cannot run because docent could
		// not read a ref is a worse outcome than one running from stale objects.
		log.Printf("agents: pre-turn sync of %s (%s): %v", sess.Branch, sess.Dir, err)
		return nil
	}
	if res.Note != "" {
		log.Printf("agents: pre-turn sync of %s: %s", sess.Branch, res.Note)
	}
	if res.Diverged {
		return fmt.Errorf("%w: docent is %d commit(s) ahead and %d behind %s. "+
			"Reconcile them, or send anyway to add to docent's copy",
			agentsession.ErrDiverged, res.Ahead, res.Behind, sess.Branch)
	}
	return nil
}

// commitAfterTurn snapshots the worktree so the turn's work is fetchable.
//
// docent's worktree is a directory the developer has never opened, so anything
// left uncommitted in it is invisible to every git command they run. This is the
// step that makes the open button and the divergence check above mean anything:
// both compare committed tips.
func commitAfterTurn(ctx context.Context, sess agentsession.Session, _ *agentsession.TurnResult, _ error) error {
	msg := fmt.Sprintf("docent: turn %d (%s)", sess.Turns+1, sess.ID)
	if _, err := worktree.CommitAll(ctx, sess.Dir, msg); err != nil {
		return fmt.Errorf("could not commit this turn's changes in %s: %w", sess.Dir, err)
	}
	return nil
}

// foreignTurnMaxAge is how long an IDE's reported turn may keep claiming a
// worktree before docent stops believing it. Pinned to the longest a turn docent
// runs itself may take: past that, "still working" is far likelier to be a lost
// stop event than an agent still going, and a dropped event must not lock the
// developer out of their own directory.
const foreignTurnMaxAge = agentsession.DefaultTurnTimeout

// foreignAgentAt describes an editor's agent that is mid-turn in dir, or "" when
// the worktree looks free.
//
// This is a warning rather than a lock, and the phrasing is doing real work: the
// caller turns it into the reason a start was refused, and the user decides from
// it whether to wait or override. "Something is busy" would leave them nothing to
// act on, so the editor and how long ago the turn began are both named -- a turn
// that started seconds ago is a real conflict, one from twenty minutes ago is
// worth a second look.
func foreignAgentAt(sessions *registry.Store, cfg userdata.SessionsConfig, dir string, now time.Time) string {
	if sessions == nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	rec, ok := sessions.AgentWorkingAt(dir, cfg.TTL(), foreignTurnMaxAge, now)
	if !ok {
		return ""
	}
	ide := strings.TrimSpace(rec.IDE)
	if ide == "" {
		// A reporter that did not name itself is still evidence of an agent.
		ide = "an editor"
	}
	started := registry.ParseTime(rec.LastPromptAt)
	if started.IsZero() {
		return fmt.Sprintf("%s has an agent running here", ide)
	}
	return fmt.Sprintf("%s has an agent running here, started %s ago",
		ide, now.Sub(started).Round(time.Second))
}

// sessionAgentRunner runs an `agent` automation action as a hosted session.
//
// This replaces the queue-to-disk handoff. That design assumed a worker process
// (`docent-automations`) that no installer ever installed, so every agent action
// configured here had been queuing forever with nothing to say so. A session is
// strictly better: it runs, it is visible as a lane while it runs, and its
// transcript survives a restart.
//
// The action's `post` steps (validate / commit / push / jira_comment) are not
// carried over. They were the queue worker's way of doing something useful with
// a run nobody watched, and a session is watched.
//
// Committing is no longer any of the prompt's business either: the session ends
// every turn with a commit of its own, because the worktree is docent's and
// leaving it dirty would hide the work. Pushing deliberately is not: a session's
// branch reaches the developer over the filesystem when they open it, and a
// daemon that pushes a WIP snapshot to a shared forge on a rule's schedule would
// publish work nobody has read. An action that wants a branch pushed says so in
// its prompt.
type sessionAgentRunner struct {
	manager *agentsession.Manager
	// err explains why manager is nil, so a rule firing against a broken setup
	// fails loudly rather than silently doing nothing -- which is exactly the
	// failure this whole change exists to end.
	err error
	// defaultProvider is the configured AI provider, used when the action does
	// not name one.
	defaultProvider string
}

func (r sessionAgentRunner) Run(ctx context.Context, action automation.Action, ev automation.Event) error {
	if r.manager == nil {
		if r.err != nil {
			return fmt.Errorf("agent: session hosting unavailable: %w", r.err)
		}
		return fmt.Errorf("agent: session hosting unavailable")
	}
	actx := automation.EventContext(ev)
	prompt, err := automation.RenderTemplate(action.Prompt, actx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(prompt) == "" {
		return fmt.Errorf("agent: prompt is empty")
	}
	baseRef, err := automation.RenderTemplate(action.BaseRef, actx)
	if err != nil {
		return err
	}

	provider := agentsession.Provider(strings.ToLower(strings.TrimSpace(action.Provider)))
	if provider == "" {
		provider = agentsession.Provider(strings.ToLower(strings.TrimSpace(r.defaultProvider)))
	}
	if provider != agentsession.ProviderCursor {
		// Claude is the default for anything not explicitly Cursor, including
		// the rule-based and ollama report providers, which are not coding
		// agents at all and cannot run a turn.
		provider = agentsession.ProviderClaude
	}

	// open_path mode runs in the developer's existing checkout; anything else
	// gets docent's own worktree, which is what Dir being empty asks for.
	dir := ""
	if strings.TrimSpace(action.Workdir) == automation.WorkdirOpenPath {
		dir = actx.OpenPath
		if strings.TrimSpace(dir) == "" {
			return fmt.Errorf("agent: workdir open_path but the work item has no local path")
		}
	}

	_, err = r.manager.Start(ctx, agentsession.StartRequest{
		Provider: provider,
		Title:    automationSessionTitle(actx),
		Repo:     actx.Repo,
		Branch:   actx.Branch,
		Dir:      dir,
		BaseRef:  strings.TrimSpace(baseRef),
		OpenPath: actx.OpenPath,
		Prompt:   prompt,
		Color:    model.ColorForName(actx.Branch),
	})
	return err
}

// automationSessionTitle labels the lane with something recognizable: the work
// item's own title when there is one, else the rule that fired.
func automationSessionTitle(actx automation.Context) string {
	if t := strings.TrimSpace(actx.Title); t != "" {
		return t
	}
	if k := strings.TrimSpace(actx.Ticket.Key); k != "" {
		return k
	}
	return strings.TrimSpace(actx.RuleID)
}
