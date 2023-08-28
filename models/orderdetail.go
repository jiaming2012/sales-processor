package models

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

func parseDurationString(durationString string) (time.Duration, error) {
	parts := strings.Split(durationString, ":")
	if len(parts) != 3 {
		return 0, fmt.Errorf("Invalid duration format")
	}

	hours, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, fmt.Errorf("Invalid hours: %v", err)
	}

	minutes, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, fmt.Errorf("Invalid minutes: %v", err)
	}

	seconds, err := strconv.Atoi(parts[2])
	if err != nil {
		return 0, fmt.Errorf("Invalid seconds: %v", err)
	}

	duration := time.Duration(hours)*time.Hour +
		time.Duration(minutes)*time.Minute +
		time.Duration(seconds)*time.Second

	return duration, nil
}

type DateTime struct {
	time.Time
}

func (date *DateTime) MarshalCSV() (string, error) {
	return date.Time.Format("20060201"), nil
}

func (date *DateTime) UnmarshalCSV(csv string) (err error) {
	if len(csv) == 0 {
		date.Time = time.Time{}
		return nil
	}

	date.Time, err = time.Parse("1/2/06 3:04 PM", csv)
	return err
}

type OrderDuration struct {
	time.Duration
}

func (duration *OrderDuration) MarshalCSV() (string, error) {
	return duration.String(), nil
}

func (duration *OrderDuration) UnmarshalCSV(csv string) (err error) {
	if len(csv) == 0 {
		duration.Duration = 0
		return nil
	}

	duration.Duration, err = parseDurationString(csv)
	return err
}

type OrderDetail struct {
	Location      string        `csv:"Location"`
	OrderID       int           `csv:"Order Id"`
	OrderNumber   int           `csv:"Order #"`
	Checks        int           `csv:"Checks"`
	Opened        DateTime      `csv:"Opened"`
	TabNames      string        `csv:"Tab Names"`
	Server        string        `csv:"Server"`
	Service       string        `csv:"Service"`
	DiningOptions string        `csv:"Dining Options"`
	Discount      string        `csv:"Discount Amount"`
	Amount        float64       `csv:"Amount"`
	Tax           float64       `csv:"Tax"`
	Tip           float64       `csv:"Tip"`
	Total         float64       `csv:"Total"`
	Voided        bool          `csv:"Voided"`
	Paid          DateTime      `csv:"Paid"`
	Closed        DateTime      `csv:"Closed"`
	Duration      OrderDuration `csv:"Duration (Opened to Paid)"`
	OrderSource   string        `csv:"Order Source"`
}
