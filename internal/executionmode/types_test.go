package executionmode

import (
	"strings"
	"testing"
)

func TestExecutionModeValidate(t *testing.T) {
	cases := []struct {
		name    string
		mode    ExecutionMode
		wantErr string
	}{
		{
			name: "ok daily-plan",
			mode: ExecutionMode{ID: "daily-plan", Lookback: &Lookback{Kind: LookbackKindPreviousWeekday}, Prompt: &Prompt{Instruction: "Plan."}, Scope: ScopeSelf},
		},
		{
			name: "ok days",
			mode: ExecutionMode{ID: "x", Lookback: &Lookback{Kind: LookbackKindDays, Days: 14}, Scope: ScopeRepo},
		},
		{
			name: "ok empty optional fields",
			mode: ExecutionMode{ID: "abc"},
		},
		{
			name:    "missing id",
			mode:    ExecutionMode{},
			wantErr: "id is required",
		},
		{
			name:    "id pattern",
			mode:    ExecutionMode{ID: "Has-Caps"},
			wantErr: "must match",
		},
		{
			name:    "days zero",
			mode:    ExecutionMode{ID: "x", Lookback: &Lookback{Kind: LookbackKindDays, Days: 0}},
			wantErr: "days >= 1",
		},
		{
			name:    "prev-weekday with days",
			mode:    ExecutionMode{ID: "x", Lookback: &Lookback{Kind: LookbackKindPreviousWeekday, Days: 3}},
			wantErr: "does not accept days",
		},
		{
			name:    "unknown lookback kind",
			mode:    ExecutionMode{ID: "x", Lookback: &Lookback{Kind: "calendar-week"}},
			wantErr: "unknown kind",
		},
		{
			name:    "empty prompt",
			mode:    ExecutionMode{ID: "x", Prompt: &Prompt{Instruction: "  "}},
			wantErr: "prompt.instruction must be non-empty",
		},
		{
			name:    "unknown scope",
			mode:    ExecutionMode{ID: "x", Scope: "team"},
			wantErr: "unknown scope",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.mode.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestExecutionModeDisplay(t *testing.T) {
	if got := (ExecutionMode{ID: "x"}).Display(); got != "x" {
		t.Fatalf("got %q", got)
	}
	if got := (ExecutionMode{ID: "x", Name: "Pretty"}).Display(); got != "Pretty" {
		t.Fatalf("got %q", got)
	}
}

func TestBuiltinModesValid(t *testing.T) {
	modes := BuiltinModes()
	if len(modes) != 3 {
		t.Fatalf("expected 3 built-ins, got %d", len(modes))
	}
	for _, m := range modes {
		if err := m.Validate(); err != nil {
			t.Fatalf("builtin %s invalid: %v", m.ID, err)
		}
	}
	// Daily-plan should not declare days, recent-activity should default to 7.
	for _, m := range modes {
		switch m.ID {
		case BuiltinDailyPlan:
			if m.Lookback == nil || m.Lookback.Kind != LookbackKindPreviousWeekday {
				t.Fatalf("daily-plan lookback: %+v", m.Lookback)
			}
			if m.Prompt == nil {
				t.Fatal("daily-plan should have prompt")
			}
			if m.Scope != ScopeSelf {
				t.Fatalf("daily-plan scope: %q", m.Scope)
			}
		case BuiltinRecentActivity:
			if m.Lookback == nil || m.Lookback.Kind != LookbackKindDays || m.Lookback.Days != 7 {
				t.Fatalf("recent-activity lookback: %+v", m.Lookback)
			}
			if m.Scope != ScopeSelf {
				t.Fatalf("recent-activity scope: %q", m.Scope)
			}
		case BuiltinCustomPrompt:
			if m.Prompt != nil {
				t.Fatalf("custom-prompt should leave Prompt nil so the user is asked, got %+v", m.Prompt)
			}
			if m.Scope != ScopeAll {
				t.Fatalf("custom-prompt scope: %q", m.Scope)
			}
		}
	}
}
