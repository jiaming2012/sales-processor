package service

import "time"

func getDateLastSunday() time.Time {
	now := time.Now()

	weekday := now.Weekday()
	daysSinceSunday := (int(weekday) + 7) % 7

	return now.AddDate(0, 0, -daysSinceSunday)
}

func GetDatesStartingFromPreviousMonday() []time.Time {
	var dates []time.Time

	// Get the current date and time
	sunday := getDateLastSunday()

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
