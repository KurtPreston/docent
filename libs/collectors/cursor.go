package collectors

import (
	"context"
	"fmt"
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
		}
		if s.Host != "" {
			fields["host"] = s.Host
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
