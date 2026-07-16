package userdata

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/KurtPreston/docent/libs/automation"
	"github.com/KurtPreston/docent/libs/config/executionmode"
)

const DefaultDir = "userdata"

// ConfigFile is the single userdata configuration file: AI + directives +
// optional user-declared execution modes and automations.
type ConfigFile struct {
	AI             AIConfig                      `yaml:"ai,omitempty"`
	SessionManager SessionManagerConfig          `yaml:"session_manager,omitempty"`
	Directives     []Directive                   `yaml:"directives,omitempty"`
	ExecutionModes []executionmode.ExecutionMode `yaml:"execution_modes,omitempty"`
	Automations    []automation.Rule             `yaml:"automations,omitempty"`
	// OutputDir overrides where docent-reporter writes generated markdown.
	// Supports a leading ~ for the home directory. When empty, the reporter
	// falls back to its --out-dir flag, then ~/docent.
	OutputDir string `yaml:"output_dir,omitempty"`
}

type Directive struct {
	ID             string            `yaml:"id"`
	Name           string            `yaml:"name"`
	Collector      string            `yaml:"collector"`
	Enabled        bool              `yaml:"enabled"`
	CodeHome       string            `yaml:"code_home,omitempty"` // local-git: parent dir of immediate child repos when paths empty
	Paths          []string          `yaml:"paths,omitempty"`     // local-git: explicit repo roots; if empty, use code_home scan
	Target         map[string]string `yaml:"target,omitempty"`
	Config         map[string]string `yaml:"config,omitempty"`
	CredentialRefs map[string]string `yaml:"credential_refs,omitempty"`

	// State and Events configure the directive's collection units. A
	// directive can fan out into a state unit, an events unit, or both.
	// When neither is set, the engine creates a single default unit for
	// whatever capability the collector supports.
	State  *ModeConfig `yaml:"state,omitempty"`
	Events *ModeConfig `yaml:"events,omitempty"`
}

// ModeConfig configures one collection mode (state or events) of a directive.
type ModeConfig struct {
	Poll PollConfig `yaml:"poll,omitempty"`
	// Query overrides config.query for this mode (e.g. a state-view JQL).
	Query string `yaml:"query,omitempty"`
	// Lookback is the initial/first-poll window for an events unit (e.g.
	// "7d", "1h"). Ignored for state units. Empty falls back to the engine
	// default.
	Lookback string `yaml:"lookback,omitempty"`
}

// PollConfig controls how often a collection unit is polled.
type PollConfig struct {
	// Interval is the background poll cadence (e.g. "15m", "1h"). Empty or
	// "0" disables background polling (manual/on-request only).
	Interval string `yaml:"interval,omitempty"`
	// OnRequest collects the unit inline when a page is requested. Reserved
	// for fast, high-priority sources (e.g. wsm).
	OnRequest bool `yaml:"on_request,omitempty"`
	// OnLoad collects the unit once at daemon startup so the cache isn't
	// empty before the first interval elapses.
	OnLoad bool `yaml:"on_load,omitempty"`
}

// ParseDuration parses a poll interval / lookback. It accepts everything
// time.ParseDuration does, plus a trailing "d" for whole days (e.g. "7d").
// An empty string or "0" parses to a zero duration.
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return 0, nil
	}
	if strings.HasSuffix(s, "d") {
		days, err := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(s, "d")))
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

type AIConfig struct {
	Provider          string           `yaml:"provider,omitempty"`
	ActivityFormatter string           `yaml:"activity_formatter,omitempty"`
	Ollama            AIProviderOllama `yaml:"ollama,omitempty"`
	Cursor            AIProviderCursor `yaml:"cursor,omitempty"`
	Claude            AIProviderClaude `yaml:"claude,omitempty"`
}

type AIProviderOllama struct {
	BaseURL string `yaml:"base_url,omitempty"`
	Model   string `yaml:"model,omitempty"`
}

type AIProviderCursor struct {
	Command string   `yaml:"command,omitempty"`
	Args    []string `yaml:"args,omitempty"`
}

type AIProviderClaude struct {
	Command string   `yaml:"command,omitempty"`
	Args    []string `yaml:"args,omitempty"`
}

// SessionManagerConfig selects how docent lists and opens editor windows for a
// work item. It mirrors AIConfig's discriminated shape: Provider is the
// discriminator, and each provider has its own nested block. An empty Provider
// means no session manager is configured — the dashboard shows no session
// column and no clickable open/focus links.
type SessionManagerConfig struct {
	// Provider is one of "cursor" or "wsm". Empty means no session manager.
	Provider string               `yaml:"provider,omitempty"`
	Cursor   SessionManagerCursor `yaml:"cursor,omitempty"`
	WSM      SessionManagerWSM    `yaml:"wsm,omitempty"`
}

// SessionManagerCursor configures the Cursor session provider.
type SessionManagerCursor struct {
	// Command overrides the Cursor CLI binary (default "cursor").
	Command string `yaml:"command,omitempty"`
	// Host is the ssh alias the local Cursor uses to reach this box, used to
	// build remote deep links. Falls back to docentd's sshHost when empty.
	Host string `yaml:"host,omitempty"`
	// WriteColor controls whether opening a work item syncs its color into the
	// repo's .vscode/settings.json. Nil means the default (true); set false to
	// disable the color write entirely.
	WriteColor *bool `yaml:"write_color,omitempty"`
	// PollStatus controls whether docent polls `cursor --status` to list live
	// windows. Nil means the default (true); set false to skip the poll (e.g. on
	// macOS where --status briefly spawns a second Cursor GUI).
	PollStatus *bool `yaml:"poll_status,omitempty"`
}

// SessionManagerWSM configures the wsm session provider.
type SessionManagerWSM struct {
	// BaseURL overrides the wsm HTTP base URL (default http://127.0.0.1:39788).
	BaseURL string `yaml:"base_url,omitempty"`
	// Token is an optional bearer token for the wsm API.
	Token string `yaml:"token,omitempty"`
}

// WriteColorEnabled reports whether opening a work item should sync its color
// into .vscode/settings.json. Defaults to true when unset.
func (c SessionManagerCursor) WriteColorEnabled() bool {
	return c.WriteColor == nil || *c.WriteColor
}

// PollStatusEnabled reports whether the cursor collector should poll
// `cursor --status` for live windows. Defaults to true when unset.
func (c SessionManagerCursor) PollStatusEnabled() bool {
	return c.PollStatus == nil || *c.PollStatus
}

type ValidationError struct {
	Problems []string
}

func (e ValidationError) Error() string {
	return "validation failed: " + strings.Join(e.Problems, "; ")
}

func (f ConfigFile) Validate() error {
	var problems []string
	seen := map[string]bool{}
	for i, d := range f.Directives {
		path := fmt.Sprintf("directives[%d]", i)
		if seen[d.ID] {
			problems = append(problems, fmt.Sprintf("%s.id is duplicated (%q)", path, d.ID))
		}
		if d.ID != "" {
			seen[d.ID] = true
		}
		problems = append(problems, validateModeConfig(path+".state", d.State)...)
		problems = append(problems, validateModeConfig(path+".events", d.Events)...)
	}
	if err := automation.ValidateRules(f.Automations); err != nil {
		var ve automation.ValidationError
		if errors.As(err, &ve) {
			problems = append(problems, ve.Problems...)
		} else {
			problems = append(problems, err.Error())
		}
	}
	return validationResult(problems)
}

func validateModeConfig(path string, m *ModeConfig) []string {
	if m == nil {
		return nil
	}
	var problems []string
	if _, err := ParseDuration(m.Poll.Interval); err != nil {
		problems = append(problems, fmt.Sprintf("%s.poll.interval %q is not a valid duration", path, m.Poll.Interval))
	}
	if _, err := ParseDuration(m.Lookback); err != nil {
		problems = append(problems, fmt.Sprintf("%s.lookback %q is not a valid duration", path, m.Lookback))
	}
	return problems
}

func validationResult(problems []string) error {
	if len(problems) == 0 {
		return nil
	}
	return ValidationError{Problems: problems}
}

func IsValidationError(err error) bool {
	var validationErr ValidationError
	return errors.As(err, &validationErr)
}
