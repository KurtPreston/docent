package engine

import (
	"context"
	"fmt"
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
				Roots:    roots,
				Hook:     cfg.WorktreeHook,
			})
			if err != nil {
				return agentsession.ProvisionResult{}, err
			}
			return agentsession.ProvisionResult{Dir: wd.Path, Project: wd.ProjectDir, Owned: wd.Owned}, nil
		},
	}, nil
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
// a run nobody watched; a session is watched, and the agent can be told to
// commit and push in its prompt.
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
