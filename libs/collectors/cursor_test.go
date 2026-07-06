package collectors

import (
	"context"
	"errors"
	"testing"

	"github.com/KurtPreston/docent/libs/config/userdata"
	"github.com/KurtPreston/docent/libs/sessionmanager"
)

func TestCursorCollector_CollectState(t *testing.T) {
	c := CursorCollector{
		lister: func(ctx context.Context, d userdata.Directive) ([]sessionmanager.Session, error) {
			return []sessionmanager.Session{
				{ID: "3", Title: "whip.tsx - salsa-12656 [SSH: desktop] - Cursor", Leaf: "salsa-12656", Host: "desktop", App: "Cursor"},
				{ID: "1", Title: "main.go - proj - Cursor", Leaf: "proj", App: "Cursor"},
			}, nil
		},
	}
	dir := userdata.Directive{ID: "local-cursor", Collector: "cursor", Config: map[string]string{"machine": "local"}}
	items, err := c.CollectState(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}

	remote := items[0]
	if remote.Source != "cursor" || remote.Kind != "session" {
		t.Errorf("source/kind = %q/%q", remote.Source, remote.Kind)
	}
	if remote.Title != "salsa-12656" {
		t.Errorf("title = %q, want leaf salsa-12656", remote.Title)
	}
	if remote.Fields["live"] != "true" || remote.Fields["host"] != "desktop" || remote.Fields["window_id"] != "3" {
		t.Errorf("fields = %+v", remote.Fields)
	}
	if remote.StableID != "session:local:3" {
		t.Errorf("stable id = %q", remote.StableID)
	}
	if !remote.IsSelf {
		t.Error("cursor session should be IsSelf")
	}

	local := items[1]
	if _, ok := local.Fields["host"]; ok {
		t.Errorf("local window should not carry a host field: %+v", local.Fields)
	}
	if !remote.ObservedAt.IsZero() {
		t.Error("session state should leave ObservedAt unset")
	}
}

func TestCursorCollector_ListError(t *testing.T) {
	c := CursorCollector{
		lister: func(ctx context.Context, d userdata.Directive) ([]sessionmanager.Session, error) {
			return nil, errors.New("boom")
		},
	}
	if _, err := c.CollectState(context.Background(), userdata.Directive{ID: "x"}, nil); err == nil {
		t.Fatal("expected error propagated from lister")
	}
}
