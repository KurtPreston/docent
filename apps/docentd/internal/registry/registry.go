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
// RemoteAuthority is how the editor itself names the window's workspace, e.g.
// "ssh-remote+desktop" or the hex-encoded "ssh-remote+7b22686f...7d". It is
// deliberately not part of Key: it is a second spelling of TargetHost, only the
// IDE extension can see it, and keying on it would fork a record every time a
// reporter that cannot report it (the agent hook) touches the same window.
type Identity struct {
	IDE             string
	IDEHost         string
	TargetHost      string
	RemoteAuthority string
	Path            string
	Remote          bool
}

// Key returns the stable composite key for this identity.
func (id Identity) Key() string {
	return strings.Join([]string{
		norm(id.IDE),
		norm(id.IDEHost),
		norm(id.TargetHost),
		NormPath(id.Path),
	}, keyDelim)
}

// Name returns the workspace leaf name for display, derived from the path.
func (id Identity) Name() string {
	return leaf(id.Path)
}

func norm(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// NormPath canonicalizes a session path to forward slashes with no trailing
// separator.
//
// The separator rewrite is not cosmetic. A single window is reported by two
// clients on two different machines: the IDE extension (client-side, so on
// Windows for a Remote-SSH window) and the Cursor agent hook (on the remote).
// A client-side extension asked for a remote path in local convention answers
// \home\me\Code\x, while the hook can only ever say /home/me/Code/x. Paths are
// part of the session identity, so without normalization the same window
// arrives as two records and agent status attaches to neither the window the
// user sees nor the work item it belongs to. Normalizing here means an older
// extension build still joins correctly instead of failing silently.
func NormPath(p string) string {
	return strings.TrimRight(strings.ReplaceAll(strings.TrimSpace(p), `\`, "/"), "/")
}

func leaf(path string) string {
	p := NormPath(path)
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
	IDE             string `json:"ide,omitempty"`
	IDEHost         string `json:"ideHost,omitempty"`
	TargetHost      string `json:"targetHost,omitempty"`
	RemoteAuthority string `json:"remoteAuthority,omitempty"`
	Path            string `json:"path,omitempty"`
	Remote          bool   `json:"remote,omitempty"`

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
	if err := json.Unmarshal(b, &s.data); err != nil {
		return err
	}
	s.migratePaths()
	return nil
}

// migratePaths rewrites records persisted before paths were normalized. Both the
// stored path and the composite key embed the separator, so a record written by
// an older client-side extension keeps its \home\me\Code\x spelling — and its
// key — until it is rewritten here. Without this, existing sessions stay
// unmatchable by the agent hook and keep producing malformed deep links until
// each window happens to be reopened.
func (s *Store) migratePaths() {
	for key, rec := range s.data {
		normalized := NormPath(rec.Path)
		if normalized == rec.Path {
			continue
		}
		rec.Path = normalized
		delete(s.data, key)
		want := Identity{
			IDE:        rec.IDE,
			IDEHost:    rec.IDEHost,
			TargetHost: rec.TargetHost,
			Path:       rec.Path,
			Remote:     rec.Remote,
		}.Key()
		// Prefer whichever record has seen activity more recently, so a merge
		// cannot resurrect a stale duplicate over a live session.
		if existing, ok := s.data[want]; ok && parseISO(LatestActivity(existing)).After(parseISO(LatestActivity(rec))) {
			continue
		}
		s.data[want] = rec
	}
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

// RemoteAuthorityForPath returns the remote authority reported by the
// most-recently-active window open on path, or "" when no window is open there
// or none has reported one. It is how a deep link built from a work item's
// checkout path (rather than from a session) still addresses the window that
// already has that folder open.
func (s *Store) RemoteAuthorityForPath(path string) string {
	want := NormPath(path)
	if want == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var best string
	var bestTime time.Time
	for _, r := range s.data {
		if r.RemoteAuthority == "" || NormPath(r.Path) != want {
			continue
		}
		if t := parseISO(r.LastHeartbeatAt); best == "" || t.After(bestTime) {
			best, bestTime = r.RemoteAuthority, t
		}
	}
	return best
}

// AgentWorkingAt returns a window open on path whose agent is mid-turn, or false
// when nothing appears to be editing there.
//
// This is what lets docent decline to run its own agent in a worktree an IDE
// agent already has: two agents sharing a git index corrupt each other, and
// docent's session manager can only see its own sessions. The evidence is the
// same the cockpit already shows -- a prompt with no response after it -- so a
// lane that reads "working" and a refused start agree about why.
//
// Both bounds are needed, because either timestamp can strand a record in
// "working" forever. ttl is heartbeat freshness, for a window that died mid-turn
// without delivering its "close" event: whatever it was doing, it is not doing it
// now. maxTurnAge bounds the turn itself, which is the case that actually happens
// -- the agent hook is best-effort (no jq, no curl, not installed, docentd
// restarting between the prompt and the stop), and a single lost stop event would
// otherwise lock docent out of the directory until someone found the record and
// cleared it by hand. A non-positive maxTurnAge disables that bound.
//
// When several windows are open on the same path, the one whose turn started most
// recently is reported: it is the freshest evidence, and the caller only needs one
// reason to stay out.
func (s *Store) AgentWorkingAt(path string, ttl, maxTurnAge time.Duration, now time.Time) (Record, bool) {
	want := NormPath(path)
	if want == "" {
		return Record{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var (
		best   Record
		bestAt time.Time
		found  bool
	)
	for _, r := range s.data {
		if NormPath(r.Path) != want || SessionStatus(r) != "working" || !IsFresh(r, ttl, now) {
			continue
		}
		// Non-zero by construction: "working" means a prompt was seen.
		promptAt := parseISO(r.LastPromptAt)
		if maxTurnAge > 0 && now.Sub(promptAt) > maxTurnAge {
			continue
		}
		if !found || promptAt.After(bestAt) {
			best, bestAt, found = r, promptAt, true
		}
	}
	return best, found
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
	wantPath := NormPath(id.Path)
	var bestKey string
	var bestTime time.Time
	for k, r := range s.data {
		if norm(r.IDE) != wantIDE {
			continue
		}
		if NormPath(r.Path) != wantPath {
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
			Path:       NormPath(id.Path),
			Remote:     id.Remote,
			CreatedAt:  now,
		}
	}
	// Only the IDE extension knows the authority, and it may reach a record the
	// agent hook created first, so fill it in whenever it arrives — but never
	// blank it, or the hook's next heartbeat would undo the deep link.
	if id.RemoteAuthority != "" {
		rec.RemoteAuthority = id.RemoteAuthority
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

// ParseTime reads one of a Record's timestamp fields. The fields are exported
// strings, so every consumer has to parse them; this is the one place that knows
// which formats are written.
func ParseTime(s string) time.Time {
	return parseISO(s)
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
