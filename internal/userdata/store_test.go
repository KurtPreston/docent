package userdata

import (
	"context"
	"testing"
)

func TestEnsureCreatesConfigAndOutput(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	if err := store.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AI.Provider != "rule-based" {
		t.Fatalf("default provider: %q", cfg.AI.Provider)
	}
}
