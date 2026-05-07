package cli

import "time"

// PreviousWeekdayStart returns midnight local time on the previous "work day"
// for planning: Mon → Fri 00:00; Sat/Sun → Fri 00:00; Tue–Fri → yesterday 00:00.
func PreviousWeekdayStart(now time.Time) time.Time {
	loc := now.Location()
	local := now.In(loc)
	y, m, d := local.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, loc)
	switch today.Weekday() {
	case time.Monday:
		return today.AddDate(0, 0, -3)
	case time.Sunday:
		return today.AddDate(0, 0, -2)
	case time.Saturday:
		return today.AddDate(0, 0, -1)
	default:
		return today.AddDate(0, 0, -1)
	}
}

func lookbackSince(now time.Time, days int) time.Time {
	if days < 1 {
		days = 1
	}
	return now.Add(-time.Duration(days) * 24 * time.Hour)
}
