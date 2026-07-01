package correlation

import (
	"testing"

	"github.com/kurt/slakkr-ai/libs/model"
)

func TestSignalToEntityLinksTicket(t *testing.T) {
	cfg := Config{}
	s := model.Signal{
		StableID: "jira:SALSA-42",
		Source:   "jira",
		Kind:     "issue",
		Title:    "SALSA-42 do the thing",
	}
	ent := SignalToEntity(s, cfg)
	if ent.ID != "jira:SALSA-42" {
		t.Errorf("entity id = %q, want jira:SALSA-42", ent.ID)
	}
	if ent.Coordinates["ticket"] != "SALSA-42" {
		t.Errorf("ticket coord = %q, want SALSA-42", ent.Coordinates["ticket"])
	}
	// The entity must group under the SALSA-42 work item.
	if got := GroupKey(ent, cfg); got != "SALSA-42" {
		t.Errorf("group key = %q, want SALSA-42", got)
	}
}

func TestSignalToEntityFallbackID(t *testing.T) {
	ent := SignalToEntity(model.Signal{Source: "slack", Kind: "message", Title: "hello"}, Config{})
	if ent.ID != "slack:message:hello" {
		t.Errorf("fallback id = %q, want slack:message:hello", ent.ID)
	}
}
