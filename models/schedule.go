package models

import (
	"time"
)

type Shift struct {
	Start    time.Time
	End      time.Time
	IsTipped bool
}

func (s Shift) DurationElapsed() time.Duration {
	return s.End.Sub(s.Start)
}

type Schedule struct {
	Shifts map[Employee][]Shift
}

type Timesheet map[time.Weekday]Schedule

func (t *Timesheet) Add(weekday time.Weekday, employee Employee, shift Shift) {
	schedule, foundSchedule := (*t)[weekday]
	if !foundSchedule {
		(*t)[weekday] = Schedule{
			Shifts: make(map[Employee][]Shift),
		}

		schedule = (*t)[weekday]
	}

	_, foundShifts := schedule.Shifts[employee]
	if !foundShifts {
		schedule.Shifts[employee] = make([]Shift, 0)
	}

	schedule.Shifts[employee] = append(schedule.Shifts[employee], shift)
}
