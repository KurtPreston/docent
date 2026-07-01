package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Record holds per-session metadata and activity timestamps.
type Record struct {
	Name            string `json:"name"`
	Host            string `json:"host,omitempty"`
	Path            string `json:"path,omitempty"`
	Color           string `json:"color,omitempty"`
	ColorSource     string `json:"colorSource,omitempty"`
	FG              string `json:"fg,omitempty"`
	Ticket          string `json:"ticket,omitempty"`
	CreatedAt       string `json:"createdAt,omitempty"`
	LastOpenedAt    string `json:"lastOpenedAt,omitempty"`
	LastPromptAt    string `json:"lastPromptAt,omitempty"`
	LastAgentStopAt string `json:"lastAgentStopAt,omitempty"`
	LastShellDoneAt string `json:"lastShellDoneAt,omitempty"`
	LastFocusedAt   string `json:"lastFocusedAt,omitempty"`
}

// Store persists session records keyed by name.
type Store struct {
	mu   sync.Mutex
	path string
	data map[string]Record
}

func NewStore(path string) (*Store, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		path = filepath.Join(home, ".config", "docent", "sessions.json")
	}
	s := &Store{path: path, data: map[string]Record{}}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, &s.data)
}

func (s *Store) save() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) Get(name string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.data[name]
	return r, ok
}

func (s *Store) All() map[string]Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]Record, len(s.data))
	for k, v := range s.data {
		out[k] = v
	}
	return out
}

func (s *Store) ApplyEvent(name, kind, host, path, color string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.data[name]
	if !ok {
		rec = Record{Name: name, CreatedAt: nowISO()}
	}
	now := nowISO()
	switch kind {
	case "prompt-submit":
		rec.LastPromptAt = now
	case "agent-stop":
		rec.LastAgentStopAt = now
	case "shell-done":
		rec.LastShellDoneAt = now
	case "session-start":
		rec.LastOpenedAt = now
	}
	if host != "" {
		rec.Host = host
	}
	if path != "" {
		rec.Path = path
	}
	if color != "" {
		rec.Color = color
		rec.ColorSource = "hook"
	}
	if !ok {
		s.data[name] = rec
	}
	s.data[name] = rec
	return s.save()
}

func (s *Store) SetFocused(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.data[name]
	if !ok {
		rec = Record{Name: name, CreatedAt: nowISO()}
	}
	rec.LastFocusedAt = nowISO()
	s.data[name] = rec
	return s.save()
}

// SessionStatus derives activity status from timestamps.
func SessionStatus(r Record) string {
	promptAt := parseISO(r.LastPromptAt)
	stopAt := parseISO(r.LastAgentStopAt)
	focusAt := parseISO(r.LastFocusedAt)
	if stopAt.IsZero() {
		if !promptAt.IsZero() {
			return "working"
		}
		return "idle"
	}
	if !promptAt.IsZero() && !promptAt.Before(stopAt) {
		return "working"
	}
	if !focusAt.IsZero() && !focusAt.Before(stopAt) {
		return "idle"
	}
	return "needs-followup"
}

func nowISO() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func parseISO(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, _ = time.Parse(time.RFC3339, s)
	}
	return t
}

func LatestActivity(r Record) string {
	latest := time.Time{}
	field := ""
	for _, pair := range []struct {
		f string
		v string
	}{
		{"lastPromptAt", r.LastPromptAt},
		{"lastAgentStopAt", r.LastAgentStopAt},
		{"lastShellDoneAt", r.LastShellDoneAt},
		{"lastFocusedAt", r.LastFocusedAt},
		{"lastOpenedAt", r.LastOpenedAt},
		{"createdAt", r.CreatedAt},
	} {
		t := parseISO(pair.v)
		if !t.IsZero() && (latest.IsZero() || t.After(latest)) {
			latest = t
			field = pair.v
		}
	}
	_ = field
	if latest.IsZero() {
		return ""
	}
	return latest.UTC().Format(time.RFC3339Nano)
}
