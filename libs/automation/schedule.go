package automation

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// MatchSchedule returns events for schedule-type rules that are due at `now`
// and were not already fired in the current minute (tracked via lastFire).
// lastFire maps rule ID → last fire time; the caller should update it for
// returned events.
func MatchSchedule(rules []Rule, now time.Time, lastFire map[string]time.Time) []Event {
	var out []Event
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		if strings.TrimSpace(rule.Trigger.Type) != "schedule" {
			continue
		}
		if !scheduleDue(rule.Trigger, now) {
			continue
		}
		// Dedup within the same minute.
		if t, ok := lastFire[rule.ID]; ok {
			if t.Truncate(time.Minute).Equal(now.Truncate(time.Minute)) {
				continue
			}
		}
		out = append(out, Event{
			Rule:    rule,
			Trigger: "schedule",
			FiredAt: now,
		})
	}
	return out
}

func scheduleDue(tr Trigger, now time.Time) bool {
	if cron := strings.TrimSpace(tr.Cron); cron != "" {
		return cronMatches(cron, now)
	}
	at := strings.TrimSpace(tr.At)
	if at == "" {
		return false
	}
	parts := strings.Split(at, ":")
	if len(parts) != 2 {
		return false
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	if now.Hour() != h || now.Minute() != m {
		return false
	}
	if wd := strings.TrimSpace(tr.Weekday); wd != "" {
		want := strings.ToLower(wd)
		got := strings.ToLower(now.Weekday().String())
		if want != got && !strings.HasPrefix(got, want) {
			return false
		}
	}
	return true
}

// cronMatches evaluates a 5-field cron (min hour dom month dow). Supports
// "*", ranges (1-5), lists (1,2,3), and steps (*/5). Dow: 0=Sunday.
func cronMatches(expr string, now time.Time) bool {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return false
	}
	checks := []struct {
		field string
		value int
		min   int
		max   int
	}{
		{fields[0], now.Minute(), 0, 59},
		{fields[1], now.Hour(), 0, 23},
		{fields[2], now.Day(), 1, 31},
		{fields[3], int(now.Month()), 1, 12},
		{fields[4], int(now.Weekday()), 0, 6},
	}
	for _, c := range checks {
		ok, err := cronFieldMatches(c.field, c.value, c.min, c.max)
		if err != nil || !ok {
			return false
		}
	}
	return true
}

func cronFieldMatches(field string, value, min, max int) (bool, error) {
	field = strings.TrimSpace(field)
	if field == "*" {
		return true, nil
	}
	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "*/") {
			step, err := strconv.Atoi(strings.TrimPrefix(part, "*/"))
			if err != nil || step <= 0 {
				return false, fmt.Errorf("bad step %q", part)
			}
			if (value-min)%step == 0 {
				return true, nil
			}
			continue
		}
		if strings.Contains(part, "-") {
			bounds := strings.SplitN(part, "-", 2)
			lo, err1 := strconv.Atoi(bounds[0])
			hi, err2 := strconv.Atoi(bounds[1])
			if err1 != nil || err2 != nil {
				return false, fmt.Errorf("bad range %q", part)
			}
			if value >= lo && value <= hi {
				return true, nil
			}
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return false, err
		}
		if n == value {
			return true, nil
		}
	}
	return false, nil
}
