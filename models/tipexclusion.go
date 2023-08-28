package models

import "time"

type TipExclusion struct {
	EmployeeID int
	Day        time.Weekday
}
