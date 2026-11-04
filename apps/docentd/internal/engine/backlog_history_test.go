package engine

import (
	"testing"

	"github.com/KurtPreston/docent/libs/collectors"
	"github.com/KurtPreston/docent/libs/config/userdata"
)

func TestBacklogHistoryPreservesDefaultStateUnit(t *testing.T) {
	for _, enabled := range []string{"false", "true"} {
		e := newTestEngine(t)
		e.cfg.Directives = []userdata.Directive{{
			ID: "tasks", Collector: "backlog", Enabled: true,
			Config: map[string]string{"git_events": enabled},
		}}
		units := e.buildUnits()
		if len(units) != 1 || units[0].mode != collectors.ModeState {
			t.Fatalf("history=%s changed default state polling: %+v", enabled, units)
		}
		e.cfg.Directives[0].State = &userdata.ModeConfig{}
		e.cfg.Directives[0].Events = &userdata.ModeConfig{Lookback: "7d"}
		units = e.buildUnits()
		if len(units) != 2 || units[1].mode != collectors.ModeEvents {
			t.Fatalf("explicit event unit not supported: %+v", units)
		}
	}
}
