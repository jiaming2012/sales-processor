package models

import (
	"fmt"
	"strconv"
	"time"
)

type Row []interface{}
type Rows [][]interface{}

func inTimeSpan(start, end, check time.Time) bool {
	if start.Before(end) {
		return !check.Before(start) && !check.After(end)
	}
	if start.Equal(end) {
		return check.Equal(start)
	}
	return !start.After(check) || !end.Before(check)
}

func parseStringTimestamp(input string) (time.Time, error) {
	var formats = []string{"1/2/2006 15:04:05", "1/2/2006", "2006-01-02T15:04:05"}

	for _, format := range formats {
		t, err := time.Parse(format, input)
		if err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized time format")
}

func (r *Rows) ConvertToCashWithdrawals(start time.Time, end time.Time) ([]CashWithdrawal, error) {
	var cashWithdrawals []CashWithdrawal

	for _, row := range *r {
		timestampStr, ok := row[0].(string)
		if !ok {
			return nil, fmt.Errorf("failed to parse row[0]=%v", row[0])
		}

		timestamp, timeErr := parseStringTimestamp(timestampStr)
		if timeErr != nil {
			return nil, fmt.Errorf("ConvertToEmployeeHours::failed to parse timestamp: %w", timeErr)
		}

		if !inTimeSpan(start, end, timestamp) {
			continue
		}

		amountStr, ok := row[1].(string)
		if !ok {
			return nil, fmt.Errorf("failed to parse row[1]=%v", row[1])
		}

		amount, parseErr := strconv.ParseFloat(amountStr, 64)
		if parseErr != nil {
			return nil, fmt.Errorf("ConvertToEmployeeHours::failed to convert %v to float64: %w", amountStr, parseErr)
		}

		recipient, ok := row[2].(string)
		if !ok {
			return nil, fmt.Errorf("failed to parse row[2]=%v", row[2])
		}

		cashWithdrawals = append(cashWithdrawals, CashWithdrawal{
			Employee: Employee(recipient),
			Amount:   amount,
		})
	}

	return cashWithdrawals, nil
}
