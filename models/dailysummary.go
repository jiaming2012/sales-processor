package models

import (
	"fmt"
	"strings"
)

type DailySummary struct {
	Sales           float64
	Taxes           float64
	Tips            float64
	EmployeeDetails map[Employee][]*OrderDetail
}

func (s DailySummary) Show() string {
	output := strings.Builder{}

	for employee, details := range s.EmployeeDetails {
		summary := OrderDetails(details).GetSummary()

		if summary.Voids > 0 {
			output.WriteString(fmt.Sprintf("%v voided %v order(s)\n", employee, summary.Voids))
		}

		if len(summary.MissedPayments) > 0 {
			output.WriteString(fmt.Sprintf("%v had %v missed payment(s)\n", employee, len(summary.MissedPayments)))
			for _, missedPayment := range summary.MissedPayments {
				output.WriteString(fmt.Sprintf("Order #%v: $%.2f\n", missedPayment.OrderNumber, missedPayment.Amount))
			}
		}
	}

	output.WriteString(fmt.Sprintf("Sales: $%.2f\n", s.Sales))

	return output.String()
}
