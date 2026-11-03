package automation

import (
	"testing"

	"github.com/KurtPreston/docent/libs/model"
)

func TestIncludeExistingRules(t *testing.T) {
	rules := []Rule{
		{ID: "a", Trigger: Trigger{Type: "transition", IncludeExisting: true}},
		{ID: "b", Trigger: Trigger{Type: "transition"}},
		{ID: "c", Trigger: Trigger{Type: "signal", IncludeExisting: true}},
	}
	got := IncludeExistingRules(rules)
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("got %+v, want only rule a", got)
	}
}

// An empty previous snapshot makes every entity already in when.to (and
// passing match.fields) a transition from "".
func TestMatchTransitions_emptyPreviousSnapshot(t *testing.T) {
	rule := Rule{ID: "q", Enabled: true, Trigger: Trigger{
		Type: "transition", Source: "backlog", Kind: KindSpec{"task"}, IncludeExisting: true,
		When:  When{Field: "queued", To: "true"},
		Match: Match{Fields: map[string]string{"project": "P"}},
	}}
	next := map[string]model.Entity{
		"t1": {ID: "t1", Kind: "task", State: map[string]string{"queued": "true", "project": "P"}},
		"t2": {ID: "t2", Kind: "task", State: map[string]string{"queued": "false", "project": "P"}},
		"t3": {ID: "t3", Kind: "task", State: map[string]string{"queued": "true", "project": "Q"}},
	}
	evs := MatchTransitions(IncludeExistingRules([]Rule{rule}), map[string]model.Entity{}, next, MatchOpts{})
	if len(evs) != 1 || evs[0].Entity.ID != "t1" || evs[0].From != "" || evs[0].To != "true" {
		t.Fatalf("got %+v, want one event for t1", evs)
	}
}
