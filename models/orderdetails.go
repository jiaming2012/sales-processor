package models

import (
	"time"
)

type OrderDetails []*OrderDetail

func calculateAverageDuration(durations []time.Duration) time.Duration {
	var totalDuration time.Duration

	// Sum up all the durations
	for _, duration := range durations {
		totalDuration += duration
	}

	// Calculate the average duration
	if len(durations) == 0 {
		return 0
	}

	averageDuration := totalDuration / time.Duration(len(durations))

	return averageDuration
}

func (orders OrderDetails) GetSummary(tipsWithheldPercentage float64) OrderSummary {
	var amount, taxes, tips float64 = 0, 0, 0
	var voids = 0
	missed := make([]*OrderDetail, 0)
	durations := make([]time.Duration, 0)

	for _, o := range orders {
		if o.Voided {
			voids += 1
			continue
		}

		if o.Paid.IsZero() {
			if o.Total > 0 {
				missed = append(missed, o)
			}

			continue
		}

		amount += o.Amount
		taxes += o.Tax
		tips += o.Tip * (1 - tipsWithheldPercentage)
		durations = append(durations, o.Duration.Duration)
	}

	return OrderSummary{
		TotalSales:     amount,
		TotalTaxes:     taxes,
		TotalTips:      tips,
		AvgDuration:    calculateAverageDuration(durations),
		Voids:          voids,
		MissedPayments: missed,
	}
}
