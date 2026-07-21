package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// keyDelim separates the composite-key components. It is the ASCII unit
// separator so it can never collide with a filesystem path.
const keyDelim = "\x1f"

// Identity is the composite key for a session: which IDE, on which host, is
// working on which path (optionally targeting a remote server). It is the
// canonical session identity shared by the ingest API and the collectors.
type Identity struct {
	IDE        string
	IDEHost    string
	TargetHost string
	Path       string
}

// Key returns the stable composite key for this identity.
func (id Identity) Key() string {
	return strings.Join([]string{
		norm(id.IDE),
		norm(id.IDEHost),
		norm(id.TargetHost),
		strings.TrimRight(strings.TrimSpace(id.Path), "/"),
	}, keyDelim)
}

// Name returns the workspace leaf name for display, derived from the path.
func (id Identity) Name() string {
	return leaf(id.Path)
}

func norm(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func leaf(path string) string {
	p := strings.TrimRight(strings.TrimSpace(path), "/")
	if p == "" {
		return ""
	}
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// Record holds per-session metadata and activity timestamps, keyed by the
// composite Identity.Key().
type Record struct {
	// Identity fields.
	IDE        string `json:"ide,omitempty"`
	IDEHost    string `json:"ideHost,omitempty"`
	TargetHost string `json:"targetHost,omitempty"`
	Path       string `json:"path,omitempty"`

	Name        string `json:"name,omitempty"`
	Color       string `json:"color,omitempty"`
	ColorSource string `json:"colorSource,omitempty"`
	FG          string `json:"fg,omitempty"`
	Ticket      string `json:"ticket,omitempty"`

	CreatedAt       string `json:"createdAt,omitempty"`
	LastHeartbeatAt string `json:"lastHeartbeatAt,omitempty"`
	LastOpenAt      string `json:"lastOpenAt,omitempty"`
	LastCloseAt     string `json:"lastCloseAt,omitempty"`
	LastPromptAt    string `json:"lastPromptAt,omitempty"`
	LastAgentStopAt string `json:"lastAgentStopAt,omitempty"`
	LastFocusedAt   string `json:"lastFocusedAt,omitempty"`
}

// Store persists session records keyed by the composite Identity.Key().
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

// Get returns the record for a composite key.
func (s *Store) Get(key string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.data[key]
	return r, ok
}

// GetByName returns the first record whose workspace leaf name matches. It is
// used to enrich collector-provided session entities, which only carry a leaf
// name (not a full composite identity).
func (s *Store) GetByName(name string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.data {
		if r.Name == name {
			return r, true
		}
	}
	return Record{}, false
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

// ApplyEvent records a session event against the composite identity. A "close"
// event removes the record entirely. Every other event refreshes the heartbeat
// timestamp (any signal from a session proves it is alive) and stamps the
// event-specific timestamp.
func (s *Store) ApplyEvent(id Identity, event, name, color string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := id.Key()
	now := nowISO()

	if event == "close" {
		delete(s.data, key)
		return s.save()
	}

	rec, ok := s.data[key]
	if !ok {
		rec = Record{
			IDE:        id.IDE,
			IDEHost:    id.IDEHost,
			TargetHost: id.TargetHost,
			Path:       strings.TrimRight(strings.TrimSpace(id.Path), "/"),
			CreatedAt:  now,
		}
	}
	if name != "" {
		rec.Name = name
	} else if rec.Name == "" {
		rec.Name = id.Name()
	}
	rec.LastHeartbeatAt = now
	switch event {
	case "open":
		rec.LastOpenAt = now
	case "agent_request_sent":
		rec.LastPromptAt = now
	case "agent_response_received":
		rec.LastAgentStopAt = now
	case "heartbeat":
		// heartbeat only refreshes LastHeartbeatAt (already set above).
	}
	if color != "" {
		rec.Color = color
		rec.ColorSource = "hook"
	}
	s.data[key] = rec
	return s.save()
}

// Sweep removes records whose most recent heartbeat is older than ttl and
// returns the removed keys. A non-positive ttl disables sweeping.
func (s *Store) Sweep(ttl time.Duration, now time.Time) []string {
	if ttl <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var removed []string
	for k, r := range s.data {
		hb := parseISO(r.LastHeartbeatAt)
		if hb.IsZero() {
			hb = parseISO(r.CreatedAt)
		}
		if !hb.IsZero() && now.Sub(hb) > ttl {
			delete(s.data, k)
			removed = append(removed, k)
		}
	}
	if len(removed) > 0 {
		_ = s.save()
	}
	return removed
}

// IsFresh reports whether the record's heartbeat is within ttl of now. A
// non-positive ttl means heartbeating is disabled, so any record is fresh.
func IsFresh(r Record, ttl time.Duration, now time.Time) bool {
	if ttl <= 0 {
		return true
	}
	hb := parseISO(r.LastHeartbeatAt)
	if hb.IsZero() {
		return false
	}
	return now.Sub(hb) <= ttl
}

// SetFocused stamps the focus timestamp for a composite key.
func (s *Store) SetFocused(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.data[key]
	if !ok {
		return nil
	}
	rec.LastFocusedAt = nowISO()
	s.data[key] = rec
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
	for _, v := range []string{
		r.LastPromptAt,
		r.LastAgentStopAt,
		r.LastFocusedAt,
		r.LastOpenAt,
		r.LastHeartbeatAt,
		r.CreatedAt,
	} {
		t := parseISO(v)
		if !t.IsZero() && (latest.IsZero() || t.After(latest)) {
			latest = t
		}
	}
	if latest.IsZero() {
		return ""
	}
	return latest.UTC().Format(time.RFC3339Nano)
}
