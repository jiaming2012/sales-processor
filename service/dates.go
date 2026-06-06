package service

import "time"

func GetDateLastSunday(since time.Time) time.Time {
	weekday := since.Weekday()
	daysSinceSunday := (int(weekday) + 7) % 7

	return since.AddDate(0, 0, -daysSinceSunday)
}

// GetDatesStartingFromPreviousMonday returns the 7 days of the work week
// that ends on the given Sunday: Monday through Sunday inclusive.
//
// Example: passing Sun 2026-05-31 returns
// [Mon 2026-05-25, Tue 2026-05-26, …, Sun 2026-05-31].
//
// Callers that need just the boundary dates can use the first and last
// elements of the returned slice.
func GetDatesStartingFromPreviousMonday(sunday time.Time) []time.Time {
	var dates []time.Time

	// Calculate the previous Monday
	weekday := sunday.Weekday()
	daysSinceMonday := (int(weekday) + 6) % 7
	previousMonday := sunday.AddDate(0, 0, -daysSinceMonday)

	for i := 0; i < 7; i++ {
		date := previousMonday.AddDate(0, 0, i)
		dates = append(dates, date)
	}

	return dates
}
