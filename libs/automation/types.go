// Package automation implements Docent Automations: IFTTT-style rules that
// react to signals, state transitions, or a schedule and fire actions
// (webhook, shell, jira-comment, slack-post, agent, report, open).
package automation

import (
	"regexp"
	"time"

	"github.com/KurtPreston/docent/libs/model"
)

// Rule is one automation: a trigger, optional conditions, and one or more actions.
type Rule struct {
	ID         string     `yaml:"id" json:"id"`
	Name       string     `yaml:"name,omitempty" json:"name,omitempty"`
	Enabled    bool       `yaml:"enabled" json:"enabled"`
	Trigger    Trigger    `yaml:"trigger" json:"trigger"`
	Conditions Conditions `yaml:"conditions,omitempty" json:"conditions,omitempty"`
	Actions    []Action   `yaml:"actions" json:"actions"`
}

// Trigger selects when a rule fires.
type Trigger struct {
	// Type is "signal", "transition", or "schedule".
	Type string `yaml:"type" json:"type"`

	// Signal / transition fields.
	Source string   `yaml:"source,omitempty" json:"source,omitempty"`
	Kind   KindSpec `yaml:"kind,omitempty" json:"kind,omitempty"`
	Match  Match    `yaml:"match,omitempty" json:"match,omitempty"`
	When   When     `yaml:"when,omitempty" json:"when,omitempty"`

	// Schedule fields (cron-like or time-of-day).
	// Cron is a 5-field cron expression (min hour dom month dow), e.g. "0 4 * * *".
	Cron string `yaml:"cron,omitempty" json:"cron,omitempty"`
	// At is a simpler daily time-of-day, e.g. "04:00" (local time).
	At string `yaml:"at,omitempty" json:"at,omitempty"`
	// Weekday restricts At to a day name (e.g. "friday"); empty means every day.
	Weekday string `yaml:"weekday,omitempty" json:"weekday,omitempty"`
}

// KindSpec accepts either a single kind string or a list of kinds in YAML.
type KindSpec []string

// UnmarshalYAML accepts kind: "foo" or kind: ["foo", "bar"].
func (k *KindSpec) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var one string
	if err := unmarshal(&one); err == nil {
		*k = KindSpec{one}
		return nil
	}
	var many []string
	if err := unmarshal(&many); err != nil {
		return err
	}
	*k = KindSpec(many)
	return nil
}

// Match filters signal content.
type Match struct {
	// Text is a regex matched against Title + Summary.
	Text string `yaml:"text,omitempty" json:"text,omitempty"`
	// TicketKey, when true, extracts a known JIRA ticket key from the signal
	// text via correlation.ParseTicketKey.
	TicketKey bool `yaml:"ticket_key,omitempty" json:"ticket_key,omitempty"`
	// Fields requires exact field equality (all listed must match).
	Fields map[string]string `yaml:"fields,omitempty" json:"fields,omitempty"`
}

// When describes a state transition (for type: transition).
type When struct {
	Field string `yaml:"field" json:"field"`
	From  string `yaml:"from,omitempty" json:"from,omitempty"`
	To    string `yaml:"to,omitempty" json:"to,omitempty"`
}

// Conditions gate a matched trigger before actions run.
type Conditions struct {
	Self      *bool    `yaml:"self,omitempty" json:"self,omitempty"`
	Repos     []string `yaml:"repos,omitempty" json:"repos,omitempty"`
	DedupeKey string   `yaml:"dedupe_key,omitempty" json:"dedupe_key,omitempty"`
	// Cooldown is a duration string (e.g. "30m") suppressing re-fires for the
	// same (rule, stableID) within that window.
	Cooldown string `yaml:"cooldown,omitempty" json:"cooldown,omitempty"`
}

// Action is one side-effect to run when a rule matches.
type Action struct {
	// Type is webhook | shell | jira-comment | slack-post | agent | report | open.
	Type string `yaml:"type" json:"type"`

	// webhook
	URL     string            `yaml:"url,omitempty" json:"url,omitempty"`
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	Body    string            `yaml:"body,omitempty" json:"body,omitempty"`

	// shell
	Command string   `yaml:"command,omitempty" json:"command,omitempty"`
	Args    []string `yaml:"args,omitempty" json:"args,omitempty"`
	Cwd     string   `yaml:"cwd,omitempty" json:"cwd,omitempty"`

	// jira-comment
	Issue string `yaml:"issue,omitempty" json:"issue,omitempty"`
	// Body is shared with webhook/jira-comment/slack-post (template string).

	// slack-post
	Channel string `yaml:"channel,omitempty" json:"channel,omitempty"`

	// agent
	Provider string            `yaml:"provider,omitempty" json:"provider,omitempty"`
	Workdir  string            `yaml:"workdir,omitempty" json:"workdir,omitempty"` // worktree | open_path
	Prompt   string            `yaml:"prompt,omitempty" json:"prompt,omitempty"`
	Post     map[string]string `yaml:"post,omitempty" json:"post,omitempty"`

	// report
	Mode    string `yaml:"mode,omitempty" json:"mode,omitempty"`       // execution mode id
	Deliver string `yaml:"deliver,omitempty" json:"deliver,omitempty"` // file | slack | webhook
	OutPath string `yaml:"out_path,omitempty" json:"out_path,omitempty"`
	Days    int    `yaml:"days,omitempty" json:"days,omitempty"`
}

// Event is a matched rule ready to dispatch.
type Event struct {
	Rule      Rule
	Trigger   string // "signal" | "transition" | "schedule"
	Signal    *model.Signal
	Entity    *model.Entity
	WorkItem  *model.WorkItem
	TicketKey string
	Match     []string // regex capture groups
	From      string   // transition from-value
	To        string   // transition to-value
	FiredAt   time.Time
}

// Context is the template/env context for an action.
type Context struct {
	RuleID   string
	Source   string
	Kind     string
	Title    string
	Summary  string
	URL      string
	Repo     string
	Branch   string
	OpenPath string
	Ticket   TicketRef
	Match    []string
	Fields   map[string]string
	From     string
	To       string
	StableID string
	IsSelf   bool
	FiredAt  time.Time
}

// TicketRef is a JIRA (or ticket-pattern) key attached to an event.
type TicketRef struct {
	Key   string
	Title string
	URL   string
}

// compiledText caches a compiled Match.Text regex.
type compiledText struct {
	re *regexp.Regexp
}
