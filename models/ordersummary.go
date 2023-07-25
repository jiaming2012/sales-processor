package models

import (
	"fmt"
	"strings"
	"time"
)

type OrderSummary struct {
	TotalSales     float64
	TotalTaxes     float64
	TotalTips      float64
	AvgDuration    time.Duration
	Voids          int
	MissedPayments []*OrderDetail
}

func (o OrderSummary) String() string {
	s := strings.Builder{}

	for _, d := range o.MissedPayments {
		s.WriteString(fmt.Sprintf("order #%v - $%.2f | ", d.OrderNumber, d.Total))
	}

	return fmt.Sprintf("sales: $%.2f, tax: $%.2f, tips: $%.2f, average order time: %d mins, %d seconds, voids: %d, missed payments: %s", o.TotalSales, o.TotalTaxes, o.TotalTips, int(o.AvgDuration.Minutes()), int(o.AvgDuration.Seconds())%60, o.Voids, s.String())
}
