package agentsession

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Status is where a session stands. It is deliberately about whose move it is
// rather than about the process, because that is the question the cockpit asks:
// a lane is worth looking at when the agent has stopped and you have not.
type Status string

const (
	// StatusRunning means a turn is in flight.
	StatusRunning Status = "running"
	// StatusIdle means the last turn finished and the session is waiting for
	// another prompt. In cockpit terms this is "your move".
	StatusIdle Status = "idle"
	// StatusFailed means the last turn could not be run or completed.
	StatusFailed Status = "failed"
	// StatusStopped means a turn was cancelled. The session is still resumable:
	// the CLI keeps the conversation, so stopping is not ending.
	StatusStopped Status = "stopped"
)

// Session is one durable agent conversation, bound to one worktree.
type Session struct {
	// ID is the provider's session id, which is also this record's key and the
	// name of its directory. Sharing one key with the CLI's own transcript is
	// the point of minting it up front.
	ID       string   `json:"id"`
	Provider Provider `json:"provider"`
	Model    string   `json:"model,omitempty"`
	Mode     Mode     `json:"mode,omitempty"`
	// Title is a human label for the lane, usually the ticket or PR it came from.
	Title string `json:"title,omitempty"`
	Repo  string `json:"repo,omitempty"`
	// Branch names the lane; Dir is the worktree the agent edits and Project is
	// the root that owns it.
	Branch  string `json:"branch,omitempty"`
	Dir     string `json:"dir,omitempty"`
	Project string `json:"project,omitempty"`
	// Owned reports that Dir is docent's own directory rather than one the
	// developer may have open in an editor. Every turn-boundary guard keys off
	// it: committing dirty state, syncing with the developer's copy, and healing
	// a broken checkout are all right in docent's tree and destructive in theirs.
	Owned bool `json:"owned,omitempty"`
	// Color is the branch's color, so a lane in the cockpit matches the editor's
	// title bar for the same branch.
	Color  string `json:"color,omitempty"`
	Status Status `json:"status"`
	// Error is the last failure, kept after the fact so a failed lane can say
	// why without replaying the whole transcript.
	Error string `json:"error,omitempty"`
	// Turns counts completed turns, which is what distinguishes the opening turn
	// (Claude's --session-id) from every later one (--resume).
	Turns      int         `json:"turns"`
	LastResult *TurnResult `json:"lastResult,omitempty"`
	CreatedAt  time.Time   `json:"createdAt"`
	UpdatedAt  time.Time   `json:"updatedAt"`
}

// Active reports whether a turn is currently running.
func (s Session) Active() bool { return s.Status == StatusRunning }

// Store persists sessions and their transcripts under a root directory.
//
// Automation job history is in-memory with a 24h TTL and dies on restart, which
// is fine for a job whose whole output is a git commit. An agent session is not
// that: it is a conversation you come back to, and the id in it is the key that
// resumes a real conversation inside the CLI. Losing the record on restart would
// orphan a session that still exists.
//
// The format is a directory per session holding a JSON record and an append-only
// JSONL transcript. Plain files rather than a database because the transcript is
// naturally append-only, the record is small, and being able to read either with
// `cat` while debugging an agent is worth more here than query support.
type Store struct {
	root string
	// mu serializes writes. Sessions are independent, but a single lock is
	// simpler than per-id locks and these writes are small and infrequent
	// relative to the minutes a turn takes.
	mu sync.Mutex
}

const (
	sessionFile = "session.json"
	eventsFile  = "events.jsonl"
)

// ErrNotFound is returned for an unknown session id.
var ErrNotFound = errors.New("agentsession: no such session")

// NewStore opens (creating if needed) a store rooted at dir.
func NewStore(dir string) (*Store, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, errors.New("agentsession: store needs a directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("agentsession: creating %s: %w", dir, err)
	}
	return &Store{root: dir}, nil
}

// Root is the directory the store writes under.
func (s *Store) Root() string { return s.root }

// validID rejects anything that could escape the store's root or collide with
// its own bookkeeping. Session ids reach this package from URL paths, so a "id"
// of "../../.ssh" must not be able to name a file.
func validID(id string) error {
	if id == "" {
		return errors.New("agentsession: empty session id")
	}
	if id != filepath.Base(id) || id == "." || id == ".." ||
		strings.ContainsAny(id, `/\`) || strings.HasPrefix(id, ".") {
		return fmt.Errorf("agentsession: unsafe session id %q", id)
	}
	return nil
}

func (s *Store) dir(id string) string { return filepath.Join(s.root, id) }

// Save writes the session record, creating its directory on first write.
func (s *Store) Save(sess Session) error {
	if err := validID(sess.ID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(sess)
}

func (s *Store) saveLocked(sess Session) error {
	dir := s.dir(sess.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	sess.UpdatedAt = time.Now().UTC()
	if sess.CreatedAt.IsZero() {
		sess.CreatedAt = sess.UpdatedAt
	}
	b, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, sessionFile), append(b, '\n'))
}

// writeFileAtomic writes via a temp file and a rename, so a crash mid-write
// cannot leave a truncated record that would read as a corrupt session.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op once the rename succeeds
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// Get reads one session record.
func (s *Store) Get(id string) (Session, error) {
	if err := validID(id); err != nil {
		return Session{}, err
	}
	b, err := os.ReadFile(filepath.Join(s.dir(id), sessionFile))
	if err != nil {
		if os.IsNotExist(err) {
			return Session{}, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return Session{}, err
	}
	var sess Session
	if err := json.Unmarshal(b, &sess); err != nil {
		return Session{}, fmt.Errorf("agentsession: reading session %s: %w", id, err)
	}
	return sess, nil
}

// Update reads, mutates, and writes a session under the store lock, so two
// concurrent updates cannot lose each other's changes.
func (s *Store) Update(id string, fn func(*Session)) (Session, error) {
	if err := validID(id); err != nil {
		return Session{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.getLocked(id)
	if err != nil {
		return Session{}, err
	}
	fn(&sess)
	sess.ID = id // a mutator must not be able to rename the record out from under its directory
	if err := s.saveLocked(sess); err != nil {
		return Session{}, err
	}
	return sess, nil
}

func (s *Store) getLocked(id string) (Session, error) {
	b, err := os.ReadFile(filepath.Join(s.dir(id), sessionFile))
	if err != nil {
		if os.IsNotExist(err) {
			return Session{}, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return Session{}, err
	}
	var sess Session
	err = json.Unmarshal(b, &sess)
	return sess, err
}

// List returns every session, most recently updated first, which is the order a
// lane rail wants. Unreadable records are skipped rather than failing the list:
// one corrupt directory should not hide every other session.
func (s *Store) List() ([]Session, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Session
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		sess, err := s.Get(e.Name())
		if err != nil {
			continue
		}
		out = append(out, sess)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

// Append adds one event to the session's transcript.
func (s *Store) Append(id string, ev Event) error {
	if err := validID(id); err != nil {
		return err
	}
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// The directory is not created here. Appending is always to a session that
	// was saved first, so a missing directory means the session was deleted --
	// and recreating it would resurrect a transcript with no record beside it,
	// which reads back as a corrupt session forever.
	f, err := os.OpenFile(filepath.Join(s.dir(id), eventsFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

// maxTranscriptLine caps one stored event when reading back. It matches the
// stream reader's cap, since that is the largest line that could have been
// written.
const maxTranscriptLine = 8 << 20

// Events reads a session's transcript. A truncated final line (a crash during
// append) is skipped rather than failing the read: the rest of the transcript is
// still the record of what happened.
func (s *Store) Events(id string) ([]Event, error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	f, err := os.Open(filepath.Join(s.dir(id), eventsFile))
	if err != nil {
		if os.IsNotExist(err) {
			// A session with no events yet is normal, not missing. Distinguish
			// the two by whether the record itself exists.
			if _, err := s.Get(id); err != nil {
				return nil, err
			}
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxTranscriptLine)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var ev Event
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		out = append(out, ev)
	}
	if err := sc.Err(); err != nil && !errors.Is(err, bufio.ErrTooLong) {
		return out, err
	}
	return out, nil
}

// Delete removes a session and its transcript. The worktree is untouched: it is
// docent's own, and outlives the session that used it.
func (s *Store) Delete(id string) error {
	if err := validID(id); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(s.dir(id)); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return err
	}
	return os.RemoveAll(s.dir(id))
}
