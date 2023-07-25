package models

import "time"

type TipExclusion struct {
	UserID int
	Day    time.Weekday
}
