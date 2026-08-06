package agentsession

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	st, err := NewStore(filepath.Join(t.TempDir(), "agent-sessions"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestSaveAndGetRoundTrip(t *testing.T) {
	st := newStore(t)
	want := Session{
		ID: "s1", Provider: ProviderClaude, Model: "opus", Title: "SALSA-1 fix",
		Repo: "Chip/salsa", Branch: "SALSA-1/fix", Dir: "/home/k/Code/salsa/SALSA-1-fix",
		Project: "/home/k/Code/salsa", Color: "#aabbcc", Status: StatusIdle, Turns: 2,
		LastResult: &TurnResult{Text: "done", DurationMS: 900, CostUSD: 0.02},
	}
	if err := st.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := st.Get("s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Branch != want.Branch || got.Dir != want.Dir || got.Project != want.Project ||
		got.Provider != want.Provider || got.Turns != want.Turns || got.Color != want.Color {
		t.Errorf("round trip lost fields:\n got %+v\nwant %+v", got, want)
	}
	if got.LastResult == nil || got.LastResult.Text != "done" || got.LastResult.CostUSD == 0 {
		t.Errorf("last result lost: %+v", got.LastResult)
	}
	// Timestamps are filled in on write, since a record with a zero UpdatedAt
	// sorts unpredictably in the lane rail.
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Errorf("timestamps not set: %+v", got)
	}
}

func TestGetUnknownSessionIsNotFound(t *testing.T) {
	st := newStore(t)
	if _, err := st.Get("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if err := st.Delete("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete err = %v, want ErrNotFound", err)
	}
}

// Session ids arrive from URL paths, so an id must never be able to name a file
// outside the store.
func TestUnsafeIDsAreRejected(t *testing.T) {
	st := newStore(t)
	for _, id := range []string{
		"", "..", ".", "../escape", "a/b", `a\b`, ".hidden", "/etc/passwd",
	} {
		t.Run("id="+id, func(t *testing.T) {
			if err := st.Save(Session{ID: id}); err == nil {
				t.Error("Save accepted it")
			}
			if _, err := st.Get(id); err == nil {
				t.Error("Get accepted it")
			}
			if err := st.Append(id, Event{Kind: KindText}); err == nil {
				t.Error("Append accepted it")
			}
			if err := st.Delete(id); err == nil {
				t.Error("Delete accepted it")
			}
		})
	}
	// Nothing escaped into the parent directory.
	entries, err := os.ReadDir(filepath.Dir(st.Root()))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("store's parent has %d entries, want only the store: %+v", len(entries), entries)
	}
}

func TestAppendAndReadEvents(t *testing.T) {
	st := newStore(t)
	if err := st.Save(Session{ID: "s1", Status: StatusIdle}); err != nil {
		t.Fatal(err)
	}
	// No events yet is empty, not an error: a fresh session has an empty
	// transcript, and failing here would make a new lane look broken.
	evs, err := st.Events("s1")
	if err != nil || len(evs) != 0 {
		t.Fatalf("fresh session: evs=%v err=%v", evs, err)
	}
	for _, ev := range []Event{
		{Kind: KindPrompt, Text: "fix the test"},
		{Kind: KindText, Text: "looking"},
		{Kind: KindTool, Tool: "Read"},
		{Kind: KindDone, Text: "fixed", Result: &TurnResult{Text: "fixed", NumTurns: 1}},
	} {
		if err := st.Append("s1", ev); err != nil {
			t.Fatal(err)
		}
	}
	evs, err = st.Events("s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 4 {
		t.Fatalf("got %d events, want 4", len(evs))
	}
	if evs[0].Kind != KindPrompt || evs[3].Kind != KindDone {
		t.Errorf("order not preserved: %v", evs)
	}
	if evs[3].Result == nil || evs[3].Result.NumTurns != 1 {
		t.Errorf("result lost on the terminal event: %+v", evs[3])
	}
	for _, ev := range evs {
		if ev.At.IsZero() {
			t.Errorf("event %q stored with no timestamp; the transcript is ordered by it", ev.Kind)
		}
	}
	if _, err := st.Events("unknown"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown session events err = %v, want ErrNotFound", err)
	}
}

// A crash mid-append leaves a partial final line. The rest of the transcript is
// still the record of what happened, so it must survive.
func TestTruncatedTranscriptLineIsSkipped(t *testing.T) {
	st := newStore(t)
	if err := st.Save(Session{ID: "s1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Append("s1", Event{Kind: KindText, Text: "good"}); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(st.Root(), "s1", eventsFile), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"kind":"text","text":"trunc`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	evs, err := st.Events("s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Text != "good" {
		t.Fatalf("evs = %+v, want just the complete event", evs)
	}
}

func TestListIsMostRecentFirst(t *testing.T) {
	st := newStore(t)
	base := time.Now().UTC()
	for i, id := range []string{"old", "middle", "newest"} {
		s := Session{ID: id, Status: StatusIdle}
		if err := st.Save(s); err != nil {
			t.Fatal(err)
		}
		// Save stamps UpdatedAt itself, so drive the order by rewriting the file
		// with explicit times rather than sleeping.
		s, _ = st.Get(id)
		s.UpdatedAt = base.Add(time.Duration(i) * time.Minute)
		writeRaw(t, st, s)
	}
	got, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d sessions, want 3", len(got))
	}
	if got[0].ID != "newest" || got[2].ID != "old" {
		t.Errorf("order = %s, %s, %s; want newest first", got[0].ID, got[1].ID, got[2].ID)
	}
}

// writeRaw persists a session bypassing Save's timestamp stamping.
func writeRaw(t *testing.T, st *Store, s Session) {
	t.Helper()
	b := mustJSON(t, s)
	if err := os.WriteFile(filepath.Join(st.Root(), s.ID, sessionFile), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// One corrupt directory must not hide every other session from the cockpit.
func TestListSkipsUnreadableRecords(t *testing.T) {
	st := newStore(t)
	if err := st.Save(Session{ID: "good"}); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(st.Root(), "bad")
	if err := os.MkdirAll(bad, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, sessionFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A directory with no record at all, e.g. a half-finished create.
	if err := os.MkdirAll(filepath.Join(st.Root(), "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "good" {
		t.Fatalf("got %+v, want just the readable session", got)
	}
}

func TestListOnAFreshStoreIsEmpty(t *testing.T) {
	st := newStore(t)
	got, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %+v, want none", got)
	}
}

func TestUpdateIsReadModifyWrite(t *testing.T) {
	st := newStore(t)
	if err := st.Save(Session{ID: "s1", Status: StatusIdle, Turns: 1}); err != nil {
		t.Fatal(err)
	}
	got, err := st.Update("s1", func(s *Session) {
		s.Status = StatusRunning
		s.Turns++
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusRunning || got.Turns != 2 {
		t.Errorf("update = %+v", got)
	}
	reread, _ := st.Get("s1")
	if reread.Status != StatusRunning || reread.Turns != 2 {
		t.Errorf("not persisted: %+v", reread)
	}
	// A mutator must not be able to rename the record out from under its own
	// directory, which would strand the transcript beside a record nobody finds.
	if _, err := st.Update("s1", func(s *Session) { s.ID = "elsewhere" }); err != nil {
		t.Fatal(err)
	}
	if again, err := st.Get("s1"); err != nil || again.ID != "s1" {
		t.Errorf("id was changed: %+v (err %v)", again, err)
	}
	if _, err := st.Update("ghost", func(*Session) {}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update of an unknown session = %v, want ErrNotFound", err)
	}
}

func TestDeleteRemovesTheSessionAndItsTranscript(t *testing.T) {
	st := newStore(t)
	if err := st.Save(Session{ID: "s1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Append("s1", Event{Kind: KindText, Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete("s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get("s1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("still readable: %v", err)
	}
	if _, err := os.Stat(filepath.Join(st.Root(), "s1")); !os.IsNotExist(err) {
		t.Errorf("directory survived: %v", err)
	}
}

// A transcript can hold prompts and file contents. Anyone with the daemon's
// state directory can read it, but it should not be world-readable.
func TestStoredFilesArePrivate(t *testing.T) {
	st := newStore(t)
	if err := st.Save(Session{ID: "s1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Append("s1", Event{Kind: KindText, Text: "secret"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{sessionFile, eventsFile} {
		info, err := os.Stat(filepath.Join(st.Root(), "s1", name))
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("%s is %v, want no group or other access", name, perm)
		}
	}
}

// A temp file left behind by an interrupted write must not be mistaken for a
// session, and the store's own bookkeeping must not leak into List.
func TestAtomicWriteLeavesNoStrayFiles(t *testing.T) {
	st := newStore(t)
	for i := 0; i < 5; i++ {
		if err := st.Save(Session{ID: "s1", Turns: i}); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(st.Root(), "s1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
	if len(entries) != 1 {
		t.Errorf("session dir has %d files, want only the record", len(entries))
	}
}

func TestNewStoreRejectsAnEmptyDir(t *testing.T) {
	if _, err := NewStore("  "); err == nil {
		t.Fatal("want an error")
	}
}
