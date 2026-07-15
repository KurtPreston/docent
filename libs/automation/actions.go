package automation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	defaultActionTimeout = 60 * time.Second
	defaultShellTimeout  = 5 * time.Minute
)

// Runner executes a single action against an event context.
type Runner interface {
	Run(ctx context.Context, action Action, ev Event) error
}

// RunnerFunc adapts a function to Runner.
type RunnerFunc func(ctx context.Context, action Action, ev Event) error

func (f RunnerFunc) Run(ctx context.Context, action Action, ev Event) error {
	return f(ctx, action, ev)
}

// Registry maps action type → Runner.
type Registry struct {
	runners map[string]Runner
}

// NewRegistry returns a Registry with the built-in safe runners (webhook, shell).
func NewRegistry() *Registry {
	r := &Registry{runners: map[string]Runner{}}
	r.Register("webhook", WebhookRunner{})
	r.Register("shell", ShellRunner{})
	return r
}

// Register adds or replaces a runner for an action type.
func (r *Registry) Register(typ string, runner Runner) {
	if r.runners == nil {
		r.runners = map[string]Runner{}
	}
	r.runners[typ] = runner
}

// Run dispatches an action to its registered runner.
func (r *Registry) Run(ctx context.Context, action Action, ev Event) error {
	typ := strings.TrimSpace(action.Type)
	runner, ok := r.runners[typ]
	if !ok {
		return fmt.Errorf("no runner registered for action type %q", typ)
	}
	return runner.Run(ctx, action, ev)
}

// WebhookRunner POSTs a JSON (or templated) body to action.URL.
type WebhookRunner struct {
	HTTP *http.Client
}

func (w WebhookRunner) client() *http.Client {
	if w.HTTP != nil {
		return w.HTTP
	}
	return http.DefaultClient
}

func (w WebhookRunner) Run(ctx context.Context, action Action, ev Event) error {
	actx := EventContext(ev)
	url, err := RenderTemplate(action.URL, actx)
	if err != nil {
		return err
	}
	url = strings.TrimSpace(url)
	if url == "" {
		return fmt.Errorf("webhook url is empty after templating")
	}
	bodyTmpl := action.Body
	if bodyTmpl == "" {
		payload := map[string]any{
			"rule_id":   actx.RuleID,
			"source":    actx.Source,
			"kind":      actx.Kind,
			"title":     actx.Title,
			"summary":   actx.Summary,
			"url":       actx.URL,
			"repo":      actx.Repo,
			"branch":    actx.Branch,
			"ticket":    actx.Ticket.Key,
			"stable_id": actx.StableID,
			"from":      actx.From,
			"to":        actx.To,
			"fired_at":  actx.FiredAt.UTC().Format(time.RFC3339),
			"fields":    actx.Fields,
		}
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		bodyTmpl = string(b)
	} else {
		bodyTmpl, err = RenderTemplate(bodyTmpl, actx)
		if err != nil {
			return err
		}
	}
	cctx, cancel := context.WithTimeout(ctx, defaultActionTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, url, strings.NewReader(bodyTmpl))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "docent-automations")
	for k, v := range action.Headers {
		rendered, err := RenderTemplate(v, actx)
		if err != nil {
			return err
		}
		req.Header.Set(k, rendered)
	}
	res, err := w.client().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("webhook %s returned %s", url, res.Status)
	}
	return nil
}

// ShellRunner executes action.Command with DOCENT_* env vars.
type ShellRunner struct{}

func (ShellRunner) Run(ctx context.Context, action Action, ev Event) error {
	actx := EventContext(ev)
	cmdName, err := RenderTemplate(action.Command, actx)
	if err != nil {
		return err
	}
	cmdName = strings.TrimSpace(cmdName)
	if cmdName == "" {
		return fmt.Errorf("shell command is empty after templating")
	}
	args := make([]string, 0, len(action.Args))
	for _, a := range action.Args {
		rendered, err := RenderTemplate(a, actx)
		if err != nil {
			return err
		}
		args = append(args, rendered)
	}
	cctx, cancel := context.WithTimeout(ctx, defaultShellTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, cmdName, args...)
	cmd.Env = append(os.Environ(), EnvPairs(actx)...)
	configureProcGroup(cmd)
	cmd.WaitDelay = 10 * time.Second
	if cwd := strings.TrimSpace(action.Cwd); cwd != "" {
		rendered, err := RenderTemplate(cwd, actx)
		if err != nil {
			return err
		}
		cmd.Dir = rendered
	}
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	if err := cmd.Run(); err != nil {
		out := strings.TrimSpace(combined.String())
		if out != "" {
			return fmt.Errorf("shell %q: %w\n%s", cmdName, err, out)
		}
		if cctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("shell %q timed out", cmdName)
		}
		return fmt.Errorf("shell %q: %w", cmdName, err)
	}
	return nil
}
