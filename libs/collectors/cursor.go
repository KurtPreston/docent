package collectors

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/KurtPreston/docent/libs/config/userdata"
	"github.com/KurtPreston/docent/libs/sessionmanager"
)

// CursorCollector lists live Cursor windows via the sessionmanager Cursor
// provider (which shells `cursor --status`). The collector directive is named
// "cursor" and must be declared explicitly in config.yaml to poll live windows.
type CursorCollector struct {
	Clock func() time.Time

	// lister overrides how sessions are listed; injected in tests. When nil, a
	// CursorManager built from the directive config is used.
	lister func(ctx context.Context, directive userdata.Directive) ([]sessionmanager.Session, error)
}

func (c CursorCollector) list(ctx context.Context, directive userdata.Directive) ([]sessionmanager.Session, error) {
	if c.lister != nil {
		return c.lister(ctx, directive)
	}
	mgr := &sessionmanager.CursorManager{
		Command: strings.TrimSpace(directive.Config["command"]),
		Host:    strings.TrimSpace(directive.Config["host"]),
	}
	return mgr.List(ctx)
}

func (c CursorCollector) CollectState(ctx context.Context, directive userdata.Directive, opts *CollectOpts) ([]StatusItem, error) {
	machine := strings.TrimSpace(directive.Config["machine"])
	if machine == "" {
		machine = directive.ID
	}
	// ideHost is the machine the Cursor GUI runs on — the same host docentd (and
	// this collector) run on. It anchors the composite session identity.
	ideHost := localHostname()
	sessions, err := c.list(ctx, directive)
	if err != nil {
		return nil, err
	}
	items := make([]StatusItem, 0, len(sessions))
	for _, s := range sessions {
		leaf := s.Leaf
		if leaf == "" {
			leaf = s.Title
		}
		fields := map[string]string{
			"window_id": s.ID,
			"machine":   machine,
			"live":      "true",
			"ide":       "cursor",
			"ideHost":   ideHost,
		}
		// s.Host, when present, is the remote server the window edits.
		if s.Host != "" {
			fields["host"] = s.Host
			fields["targetHost"] = s.Host
		}
		// Like the wsm collector, a live-window listing is a state, not an
		// activity event, so ObservedAt is deliberately left unset — real
		// session activity time comes from the session registry during
		// correlation.
		items = append(items, StatusItem{
			DirectiveID: directive.ID,
			Source:      "cursor",
			Kind:        "session",
			Title:       leaf,
			Summary:     s.Title,
			StableID:    fmt.Sprintf("session:%s:%s", machine, s.ID),
			Fields:      fields,
			IsSelf:      true,
		})
	}
	return items, nil
}

// localHostname returns this machine's hostname, or "localhost" if unavailable.
// It is the ideHost anchor for locally-run editor collectors.
func localHostname() string {
	if h, err := os.Hostname(); err == nil && strings.TrimSpace(h) != "" {
		return h
	}
	return "localhost"
}
