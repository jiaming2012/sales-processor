package service

import "time"

func GetDateLastSunday(since time.Time) time.Time {
	weekday := since.Weekday()
	daysSinceSunday := (int(weekday) + 7) % 7

	return since.AddDate(0, 0, -daysSinceSunday)
}

func GetDatesStartingFromPreviousMonday(sunday time.Time) []time.Time {
	var dates []time.Time

	// Calculate the previous Monday
	weekday := sunday.Weekday()
	daysSinceMonday := (int(weekday) + 6) % 7
	previousMonday := sunday.AddDate(0, 0, -daysSinceMonday)

	// Generate dates starting from the previous Monday
	for i := 2; i < 7; i++ {
		date := previousMonday.AddDate(0, 0, i)
		dates = append(dates, date)
	}

	return dates
}
