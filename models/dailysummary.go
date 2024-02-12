package models

import (
	"fmt"
	"strings"
)

type DailySummary struct {
	Sales            float64
	CashTendered     float64
	CCFees           float64
	Taxes            float64
	Tips             float64
	EmployeeDetails  map[Employee][]*OrderDetail
	ThirdPartyOrders ThirdPartyMerchantOrders
}

func (s DailySummary) Show(tipsWithheldPercentage float64) string {
	output := strings.Builder{}
	deliveryOutput := strings.Builder{}

	for employee, details := range s.EmployeeDetails {
		summary := OrderDetails(details).GetSummary(tipsWithheldPercentage)

		if found, company := IsDeliveryServiceName(string(employee)); found {
			deliveryOutput.WriteString(fmt.Sprintf("-> %v: $%.2f\n", company, summary.TotalSales))
		}

		if summary.Voids > 0 {
			output.WriteString(fmt.Sprintf("%v voided %v order(s)\n", employee, summary.Voids))
		}

		if len(summary.MissedPayments) > 0 {
			output.WriteString(fmt.Sprintf("%v had %v missed payment(s)\n", employee, len(summary.MissedPayments)))
			for _, missedPayment := range summary.MissedPayments {
				output.WriteString(fmt.Sprintf("-> Order #%v: $%.2f\n", missedPayment.OrderNumber, missedPayment.Amount))
			}
		}
	}

	output.WriteString(fmt.Sprintf("Sales: $%.2f\n", s.Sales))
	output.WriteString(fmt.Sprintf("Cash Tendered: $%.2f\n", s.CashTendered))
	output.WriteString(fmt.Sprintf("Credit Card Fees: $%.2f\n", s.CCFees))
	output.WriteString(deliveryOutput.String())

	return output.String()
}
