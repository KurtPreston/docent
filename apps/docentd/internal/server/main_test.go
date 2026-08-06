package server

import (
	"os"
	"testing"
)

// TestMain redirects docent's state directory for the whole package. Every test
// server builds an Engine, which opens durable stores under it (agent sessions),
// and a test run has no business writing into the developer's real
// ~/.local/state/docent.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "docent-server-state")
	if err != nil {
		panic(err)
	}
	os.Setenv("DOCENT_STATE_DIR", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
