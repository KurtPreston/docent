package correlation

import (
	"testing"

	"github.com/kurt/slakkr-ai/libs/model"
)

func TestParseTicketKey(t *testing.T) {
	cfg := Config{}
	tests := []struct {
		in   string
		want string
	}{
		{"salsa-12345-foo-bar", "SALSA-12345"},
		{"SALSA-1", "SALSA-1"},
		{"no-ticket-here", ""},
		{"", ""},
	}
	for _, tc := range tests {
		got := ParseTicketKey(tc.in, cfg)
		if got != tc.want {
			t.Errorf("ParseTicketKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBuildWorkItems_ticketGrouping(t *testing.T) {
	cfg := Config{}
	entities := []model.Entity{
		{ID: "jira:SALSA-1", Kind: "ticket", Title: "Fix widget NPE", Coordinates: map[string]string{"ticket": "SALSA-1"}},
		{ID: "pr:org/repo#5", Kind: "pr", Title: "salsa-1 fix", Coordinates: map[string]string{"ticket": "SALSA-1", "repo": "org/repo", "number": "5"}},
		{ID: "session:foo", Kind: "session", Title: "salsa-1-foo", Coordinates: map[string]string{"ticket": "SALSA-1"}},
	}
	items := BuildWorkItems(entities, cfg)
	if len(items) != 1 {
		t.Fatalf("got %d work items, want 1", len(items))
	}
	if items[0].Key != "SALSA-1" {
		t.Errorf("key = %q, want SALSA-1", items[0].Key)
	}
	if len(items[0].Entities) != 3 {
		t.Errorf("entities = %d, want 3", len(items[0].Entities))
	}
}

func TestBuildWorkItems_noTicketRepoFallback(t *testing.T) {
	cfg := Config{}
	entities := []model.Entity{
		{ID: "pr:org/repo#9", Kind: "pr", Title: "quick fix", Coordinates: map[string]string{"repo": "org/repo", "number": "9"}},
	}
	items := BuildWorkItems(entities, cfg)
	if len(items) != 1 {
		t.Fatalf("got %d work items, want 1", len(items))
	}
	if items[0].Key != "pr:org/repo#9" {
		t.Errorf("key = %q, want pr:org/repo#9", items[0].Key)
	}
}

func TestBuildWorkItems_multiTicketPrimary(t *testing.T) {
	cfg := Config{}
	// Entity with ticket in title uses that ticket as anchor.
	entities := []model.Entity{
		{ID: "pr:1", Kind: "pr", Title: "SALSA-2 and SALSA-3 combined", Coordinates: map[string]string{"repo": "org/r", "number": "1"}},
	}
	items := BuildWorkItems(entities, cfg)
	if len(items) != 1 {
		t.Fatalf("got %d items", len(items))
	}
	if items[0].Key != "SALSA-2" {
		t.Errorf("primary ticket key = %q, want SALSA-2 (first match)", items[0].Key)
	}
}

func TestSignalsToEntities_session(t *testing.T) {
	cfg := Config{}
	signals := []model.Signal{
		{
			Source: "docent-wm",
			Kind:   "session",
			Title:  "my-feature",
			Fields: map[string]string{"window_id": "w1", "machine": "mac"},
		},
	}
	ents := SignalsToEntities(signals, cfg)
	if len(ents) != 1 {
		t.Fatalf("got %d entities", len(ents))
	}
	if ents[0].WindowID != "w1" {
		t.Errorf("window_id = %q", ents[0].WindowID)
	}
	if ents[0].Kind != "session" {
		t.Errorf("kind = %q", ents[0].Kind)
	}
}
