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
//
// Remote marks a reporter that knows it is a remote session but cannot name the
// host the editor's window/GUI runs on. The Cursor shell hooks are the
// motivating case: they run on the box where the agent executes — the remote
// for a Remote-SSH window — and so can see neither the client's hostname nor
// the ssh alias, leaving IDEHost empty. On ingest such an event is resolved to
// the most-recently-active live remote session matching by ide + path (see
// Store.resolveKeyLocked), so agent activity attaches to the session the
// (client-side) IDE extension created instead of forking a duplicate record.
type Identity struct {
	IDE        string
	IDEHost    string
	TargetHost string
	Path       string
	Remote     bool
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
	Remote     bool   `json:"remote,omitempty"`

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

// resolveKeyLocked maps an incoming identity to the storage key its event
// should apply to. It returns the exact composite key when a record for it
// already exists. For a Remote reporter that cannot name its host (IDEHost
// empty), it instead binds to the most-recently-active live remote session
// (targetHost set) with the same ide + path — the record the client-side IDE
// extension created — so agent activity lands there instead of forking a
// duplicate. With no such session it falls back to the composite key, creating
// a Remote-flagged record. Callers must hold s.mu.
func (s *Store) resolveKeyLocked(id Identity) string {
	direct := id.Key()
	if _, ok := s.data[direct]; ok {
		return direct
	}
	if !id.Remote {
		return direct
	}
	wantIDE := norm(id.IDE)
	wantPath := strings.TrimRight(strings.TrimSpace(id.Path), "/")
	var bestKey string
	var bestTime time.Time
	for k, r := range s.data {
		if norm(r.IDE) != wantIDE {
			continue
		}
		if strings.TrimRight(strings.TrimSpace(r.Path), "/") != wantPath {
			continue
		}
		if strings.TrimSpace(r.TargetHost) == "" {
			continue
		}
		t := parseISO(r.LastHeartbeatAt)
		if bestKey == "" || t.After(bestTime) {
			bestKey = k
			bestTime = t
		}
	}
	if bestKey != "" {
		return bestKey
	}
	return direct
}

// ApplyEvent records a session event against the composite identity. A "close"
// event removes the record entirely. Every other event refreshes the heartbeat
// timestamp (any signal from a session proves it is alive) and stamps the
// event-specific timestamp.
func (s *Store) ApplyEvent(id Identity, event, name, color string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := s.resolveKeyLocked(id)
	now := nowISO()

	if event == "close" {
		delete(s.data, key)
		return key, s.save()
	}

	rec, ok := s.data[key]
	if !ok {
		rec = Record{
			IDE:        id.IDE,
			IDEHost:    id.IDEHost,
			TargetHost: id.TargetHost,
			Path:       strings.TrimRight(strings.TrimSpace(id.Path), "/"),
			Remote:     id.Remote,
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
	case "focus":
		// A focus is the signal that the user has seen the window, clearing a
		// pending needs-followup (see SessionStatus). It is also liveness, so
		// LastHeartbeatAt is refreshed above.
		rec.LastFocusedAt = now
	case "heartbeat":
		// heartbeat only refreshes LastHeartbeatAt (already set above).
	}
	if color != "" {
		rec.Color = color
		rec.ColorSource = "hook"
	}
	s.data[key] = rec
	return key, s.save()
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
