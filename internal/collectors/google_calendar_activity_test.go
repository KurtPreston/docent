package collectors

import (
	"testing"
	"time"
)

func TestParseICSEventsInWindow(t *testing.T) {
	since := time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	raw := `BEGIN:VCALENDAR
BEGIN:VEVENT
DTSTART;VALUE=DATE:20260420
SUMMARY:Too early
END:VEVENT
BEGIN:VEVENT
DTSTART;VALUE=DATE:20260428
SUMMARY:In window
END:VEVENT
BEGIN:VEVENT
DTSTART;VALUE=DATE:20260510
SUMMARY:Future
END:VEVENT
END:VCALENDAR`
	evs := parseICSEventsInWindow(raw, since, now)
	if len(evs) != 1 {
		t.Fatalf("want 1 event, got %d %#v", len(evs), evs)
	}
	if evs[0].summary != "In window" {
		t.Fatalf("got %q", evs[0].summary)
	}
}
