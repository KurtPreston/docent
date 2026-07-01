package userdata

import (
	"testing"
	"time"
)

func TestParseDuration(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"", 0, false},
		{"0", 0, false},
		{"15m", 15 * time.Minute, false},
		{"1h", time.Hour, false},
		{"7d", 7 * 24 * time.Hour, false},
		{"2d", 48 * time.Hour, false},
		{"banana", 0, true},
		{"5x", 0, true},
	}
	for _, tc := range cases {
		got, err := ParseDuration(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseDuration(%q): expected error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseDuration(%q): unexpected error %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseDuration(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestValidateModeConfigDurations(t *testing.T) {
	good := ConfigFile{
		AI: AIConfig{Provider: "rule-based"},
		Directives: []Directive{{
			ID: "jira", Name: "Jira", Collector: "jira", Enabled: true,
			State:  &ModeConfig{Poll: PollConfig{Interval: "5m", OnRequest: true}},
			Events: &ModeConfig{Poll: PollConfig{Interval: "15m"}, Lookback: "7d"},
		}},
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid mode config rejected: %v", err)
	}

	bad := ConfigFile{
		AI: AIConfig{Provider: "rule-based"},
		Directives: []Directive{{
			ID: "jira", Name: "Jira", Collector: "jira", Enabled: true,
			State:  &ModeConfig{Poll: PollConfig{Interval: "nope"}},
			Events: &ModeConfig{Lookback: "soon"},
		}},
	}
	if err := bad.Validate(); err == nil {
		t.Fatal("expected validation errors for bad interval/lookback")
	}
}
