package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kurt/slakkr-ai/apps/docentd/internal/config"
	"github.com/kurt/slakkr-ai/apps/docentd/internal/registry"
	"github.com/kurt/slakkr-ai/libs/model"
)

func TestLaunchWorkItem_unknownKey(t *testing.T) {
	reg, err := registry.NewStore(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	e := New(config.DaemonConfig{}, reg)
	_, ok := e.LaunchWorkItem(context.Background(), "MISSING")
	if ok {
		t.Fatal("expected ok=false for unknown key")
	}
}

func TestLaunchWorkItem_missingScript(t *testing.T) {
	reg, err := registry.NewStore(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DaemonConfig{OnClickScript: filepath.Join(t.TempDir(), "nope.sh")}
	e := New(cfg, reg)
	seedWorkItem(e, "wb:org/repo@feature-x")

	res, ok := e.LaunchWorkItem(context.Background(), "wb:org/repo@feature-x")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if res.OK {
		t.Fatalf("expected failure, got %+v", res)
	}
	if res.Message == "" {
		t.Fatal("expected error message")
	}
}

func TestLaunchWorkItem_runsHook(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "onclick.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho launched-$DOCENT_BRANCH\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	reg, err := registry.NewStore(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DaemonConfig{OnClickScript: script, DocentWMURL: "http://127.0.0.1:39788"}
	e := New(cfg, reg)
	seedWorkItem(e, "wb:org/repo@feature-x")

	res, ok := e.LaunchWorkItem(context.Background(), "wb:org/repo@feature-x")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if !res.OK {
		t.Fatalf("expected success, got %+v", res)
	}
	if res.Message != "launched-feature-x" {
		t.Fatalf("message = %q, want launched-feature-x", res.Message)
	}
}

func seedWorkItem(e *Engine, key string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lastWorkItems = []model.WorkItem{{
		Key:      key,
		Title:    "feature-x",
		Repo:     "org/repo",
		Branch:   "feature-x",
		OpenPath: "/tmp/feature-x",
	}}
	e.lastDashboard.Groups = []DashboardGroup{{
		Key:    key,
		Repo:   "org/repo",
		Branch: "feature-x",
	}}
}
