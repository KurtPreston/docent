package collectors

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/KurtPreston/docent/libs/config/userdata"
	"github.com/KurtPreston/docent/libs/wmclient"
)

// WSMCollector polls the local wsm window-manager REST service for live Cursor
// windows. The collector directive is named "wsm".
type WSMCollector struct {
	Clock func() time.Time
}

func (c WSMCollector) clock() func() time.Time {
	if c.Clock != nil {
		return c.Clock
	}
	return time.Now
}

func (c WSMCollector) CollectState(ctx context.Context, directive userdata.Directive, opts *CollectOpts) ([]StatusItem, error) {
	base := strings.TrimSpace(directive.Config["base_url"])
	if base == "" {
		return nil, fmt.Errorf("config.base_url is required for wsm collector")
	}
	machine := strings.TrimSpace(directive.Config["machine"])
	if machine == "" {
		machine = directive.ID
	}
	client := wmclient.New(base)
	windows, err := client.ListWindows(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]StatusItem, 0, len(windows))
	for _, win := range windows {
		leaf, host := wmclient.ParseCursorTitle(win.Title)
		if leaf == "" {
			leaf = win.ID
		}
		fields := map[string]string{
			"window_id": win.ID,
			"machine":   machine,
			"live":      "true",
		}
		if host != "" {
			fields["host"] = host
		} else if win.Host != "" {
			fields["host"] = win.Host
		}
		// A live-window listing is a state ("this Cursor window is open"),
		// not an activity event, so we deliberately leave ObservedAt unset.
		// Real session activity time comes from the hook timestamps in the
		// session registry, which the engine stamps onto the entity during
		// correlation. Stamping poll time here would make every live window
		// perpetually report activity "just now".
		items = append(items, StatusItem{
			DirectiveID: directive.ID,
			Source:      "wsm",
			Kind:        "session",
			Title:       leaf,
			Summary:     win.Title,
			StableID:    fmt.Sprintf("session:%s:%s", machine, win.ID),
			Fields:      fields,
			IsSelf:      true,
		})
	}
	return items, nil
}

func (c WSMCollector) ValidateDirective(ctx context.Context, directive userdata.Directive, opts *ValidateOpts) []ValidationIssue {
	if strings.TrimSpace(directive.Config["base_url"]) == "" {
		return []ValidationIssue{{
			DirectiveID: directive.ID,
			Field:       "config.base_url",
			Message:     "wsm collector requires config.base_url",
			Remediation: "set config.base_url to the wsm HTTP base (e.g. http://127.0.0.1:39788)",
		}}
	}
	return nil
}
