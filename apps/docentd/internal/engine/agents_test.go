package engine

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KurtPreston/docent/apps/docentd/internal/registry"
	"github.com/KurtPreston/docent/libs/config/userdata"
)

// The staleness bounds themselves belong to the registry and are tested there.
// What matters here is the sentence the user ends up reading, and that a daemon
// with no registry or no directory to check simply does not object.
func TestForeignAgentAt(t *testing.T) {
	store, err := registry.NewStore(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	const dir = "/home/k/Code/salsa/SALSA-1"
	id := registry.Identity{IDE: "cursor", IDEHost: "mac", Path: dir}
	if _, err := store.ApplyEvent(id, "agent_request_sent", "", ""); err != nil {
		t.Fatal(err)
	}
	var cfg userdata.SessionsConfig
	now := time.Now()

	got := foreignAgentAt(store, cfg, dir, now)
	if !strings.Contains(got, "cursor") {
		t.Errorf("description should name the editor, got %q", got)
	}
	if !strings.Contains(got, "started") {
		t.Errorf("description should say when the turn began, got %q", got)
	}

	if got := foreignAgentAt(store, cfg, "/home/k/Code/salsa/SALSA-2", now); got != "" {
		t.Errorf("a sibling worktree is a different directory, got %q", got)
	}
	if got := foreignAgentAt(store, cfg, "", now); got != "" {
		t.Errorf("no directory means nothing to object to, got %q", got)
	}
	if got := foreignAgentAt(nil, cfg, dir, now); got != "" {
		t.Errorf("no registry means no evidence, got %q", got)
	}
	// A turn nobody ever reported finishing must not hold the worktree forever.
	if got := foreignAgentAt(store, cfg, dir, now.Add(foreignTurnMaxAge+time.Minute)); got != "" {
		t.Errorf("a turn past the max age should be let go, got %q", got)
	}

	if _, err := store.ApplyEvent(id, "agent_response_received", "", ""); err != nil {
		t.Fatal(err)
	}
	if got := foreignAgentAt(store, cfg, dir, now); got != "" {
		t.Errorf("a finished turn should release the worktree, got %q", got)
	}
}
