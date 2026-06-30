package webhook

import (
	"sync"
	"time"
)

// Event is a generic ingested webhook payload.
type Event struct {
	Source         string            `json:"source"`
	Kind           string            `json:"kind"`
	Name           string            `json:"name"`
	Path           string            `json:"path,omitempty"`
	Host           string            `json:"host,omitempty"`
	Color          string            `json:"color,omitempty"`
	ConversationID string            `json:"conversationId,omitempty"`
	Fields         map[string]string `json:"fields,omitempty"`
	ReceivedAt     time.Time         `json:"received_at"`
}

// Inbox stores webhook events until collectors drain them.
type Inbox struct {
	mu     sync.Mutex
	events []Event
}

func NewInbox() *Inbox {
	return &Inbox{}
}

// Push appends an event.
func (in *Inbox) Push(ev Event) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if ev.ReceivedAt.IsZero() {
		ev.ReceivedAt = time.Now().UTC()
	}
	in.events = append(in.events, ev)
}

// Drain returns and clears all pending events.
func (in *Inbox) Drain() []Event {
	in.mu.Lock()
	defer in.mu.Unlock()
	out := in.events
	in.events = nil
	return out
}

// Default is the process-wide inbox used by docentd and the webhook collector.
var Default = NewInbox()
