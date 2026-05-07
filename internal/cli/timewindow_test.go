package cli

import (
	"testing"
	"time"
)

func TestPreviousWeekdayStart(t *testing.T) {
	loc := time.FixedZone("x", -7*3600)
	// Monday 10:00 -> expect Friday 00:00 same week previous
	mon := time.Date(2026, 5, 4, 10, 0, 0, 0, loc)
	got := PreviousWeekdayStart(mon)
	want := time.Date(2026, 5, 1, 0, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("Mon: got %v want %v", got, want)
	}
	// Tuesday -> Monday 00:00
	tue := time.Date(2026, 5, 5, 10, 0, 0, 0, loc)
	got = PreviousWeekdayStart(tue)
	want = time.Date(2026, 5, 4, 0, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("Tue: got %v want %v", got, want)
	}
}
