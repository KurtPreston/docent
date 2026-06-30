package collectors

import (
	"context"
	"fmt"
	"time"

	"github.com/kurt/slakkr-ai/internal/userdata"
	"github.com/kurt/slakkr-ai/libs/webhook"
)

// WebhookCollector drains the shared webhook inbox into signals.
type WebhookCollector struct {
	Clock func() time.Time
	Inbox *webhook.Inbox
}

func (c WebhookCollector) inbox() *webhook.Inbox {
	if c.Inbox != nil {
		return c.Inbox
	}
	return webhook.Default
}

func (c WebhookCollector) clock() func() time.Time {
	if c.Clock != nil {
		return c.Clock
	}
	return time.Now
}

func (c WebhookCollector) Collect(ctx context.Context, directive userdata.Directive, opts *CollectOpts) ([]StatusItem, error) {
	events := c.inbox().Drain()
	items := make([]StatusItem, 0, len(events))
	for _, ev := range events {
		name := ev.Name
		if name == "" && ev.Path != "" {
			name = leafName(ev.Path)
		}
		if name == "" {
			continue
		}
		fields := map[string]string{}
		for k, v := range ev.Fields {
			fields[k] = v
		}
		if ev.Host != "" {
			fields["host"] = ev.Host
		}
		if ev.Path != "" {
			fields["path"] = ev.Path
		}
		if ev.Color != "" {
			fields["color"] = ev.Color
		}
		items = append(items, StatusItem{
			DirectiveID: directive.ID,
			Source:      ev.Source,
			Kind:        ev.Kind,
			Title:       name,
			Summary:     fmt.Sprintf("%s %s", ev.Source, ev.Kind),
			ObservedAt:  ev.ReceivedAt,
			StableID:    fmt.Sprintf("webhook:%s:%s:%d", ev.Source, name, ev.ReceivedAt.UnixNano()),
			Fields:      fields,
			IsSelf:      true,
		})
	}
	return items, nil
}

func leafName(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}
