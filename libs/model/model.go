package model

import "time"

// Signal is an immutable, timestamped observation from one source.
type Signal struct {
	DirectiveID    string            `json:"directive_id"`
	Repository     string            `json:"repository,omitempty"`
	Source         string            `json:"source"`
	Kind           string            `json:"kind"`
	Title          string            `json:"title"`
	Summary        string            `json:"summary"`
	URL            string            `json:"url,omitempty"`
	Severity       string            `json:"severity,omitempty"`
	ObservedAt     time.Time         `json:"observed_at"`
	Fields         map[string]string `json:"fields,omitempty"`
	StableID       string            `json:"stable_id,omitempty"`
	AttentionClass string            `json:"attention_class,omitempty"`
	ChangeState    string            `json:"change_state,omitempty"`
	Author         string            `json:"author,omitempty"`
	IsSelf         bool              `json:"is_self,omitempty"`
}

// Entity is a persistent artifact with identity in some source and current state.
type Entity struct {
	ID          string            `json:"id"`
	Kind        string            `json:"kind"`
	Title       string            `json:"title"`
	URL         string            `json:"url,omitempty"`
	State       map[string]string `json:"state,omitempty"`
	Coordinates map[string]string `json:"coordinates,omitempty"`
	WindowID    string            `json:"window_id,omitempty"`
	Machine     string            `json:"machine,omitempty"`
}

// WorkItem is a source-agnostic unit of effort assembled by correlation.
type WorkItem struct {
	Key          string      `json:"key"`
	Title        string      `json:"title"`
	Entities     []Entity    `json:"entities"`
	Attention    string      `json:"attention"`
	Color        string      `json:"color"`
	FG           string      `json:"fg"`
	Repo         string      `json:"repo,omitempty"`
	Branch       string      `json:"branch,omitempty"`
	OpenPath     string      `json:"openPath,omitempty"`
	LastActivity string      `json:"lastActivity,omitempty"`
	Tickets      []TicketRef `json:"tickets,omitempty"`
}

// TicketRef is a JIRA (or ticket-pattern) link attached to a repo/branch work unit.
type TicketRef struct {
	Key    string `json:"key"`
	Title  string `json:"title,omitempty"`
	URL    string `json:"url,omitempty"`
	Status string `json:"status,omitempty"`
}
