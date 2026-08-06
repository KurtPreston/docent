package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/KurtPreston/docent/apps/docentd/internal/config"
	"github.com/KurtPreston/docent/libs/agentsession"
	"github.com/KurtPreston/docent/libs/automation"
	"github.com/KurtPreston/docent/libs/collectors"
	"github.com/KurtPreston/docent/libs/config/docentconfig"
	"github.com/KurtPreston/docent/libs/model"
)

// agentSessionsDir holds sessions and transcripts, under the same state root as
// the rest of docent's durable data.
const agentSessionsDir = "agent-sessions"

// newAgentManager wires the session manager from daemon config: a store under
// the state dir, a runner per provider, and worktrees provisioned through grove.
func newAgentManager(cfg config.DaemonConfig) (*agentsession.Manager, error) {
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
		Provision: func(ctx context.Context, req agentsession.ProvisionRequest) (agentsession.ProvisionResult, error) {
			wd, err := automation.ProvisionWorkdir(ctx, automation.WorkdirRequest{
				Mode:       automation.WorkdirWorktree,
				Repo:       req.Repo,
				Branch:     req.Branch,
				From:       req.BaseRef,
				OpenPath:   req.OpenPath,
				GroveRoots: roots,
			})
			if err != nil {
				return agentsession.ProvisionResult{}, err
			}
			return agentsession.ProvisionResult{Dir: wd.Path, Project: wd.ProjectDir}, nil
		},
	}, nil
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
	// resolves a grove worktree, which is what Dir being empty asks for.
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
