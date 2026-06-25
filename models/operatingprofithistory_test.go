package models

import (
	"strings"
	"testing"
	"time"
)

func TestRollingOperatingProfitRender(t *testing.T) {
	ws := WeeklySummary{
		Sales:       1000,
		COGSExclTax: 400,
		CCFees:      50,
		WeekEnding:  mustDate("2026-06-21"),
		PriorOperatingProfits: []OperatingProfitEntry{
			{WeekEnding: "2026-05-31", OperatingProfit: 100, Sales: 1000, COGSExclTax: 400, TotalLabor: 450, CCFees: 50},
			{WeekEnding: "2026-06-07", OperatingProfit: 200, Sales: 1100, COGSExclTax: 400, TotalLabor: 450, CCFees: 50},
			{WeekEnding: "2026-06-14", OperatingProfit: 150, Sales: 1050, COGSExclTax: 400, TotalLabor: 450, CCFees: 50},
		},
	}
	got := ws.renderRollingOperatingProfit(250.0)
	t.Log("\n" + got)

	wantFragments := []string{
		"Operating Profit Trend (4-Week Rolling)",
		"Week of 2026-05-31: $100.00 (rolling avg: $100.00)",
		"Week of 2026-06-07: $200.00 (rolling avg: $150.00)",
		"Week of 2026-06-14: $150.00 (rolling avg: $150.00)",
		"Week ending 2026-06-21: $250.00 (rolling avg: $175.00)",
	}
	for _, f := range wantFragments {
		if !strings.Contains(got, f) {
			t.Errorf("rendered chart missing fragment %q", f)
		}
	}
}

func TestRollingOperatingProfitEmptyPriors(t *testing.T) {
	ws := WeeklySummary{
		WeekEnding:                   mustDate("2026-06-21"),
		OperatingProfitHistoryStatus: "could not read history from SFTP",
	}
	got := ws.renderRollingOperatingProfit(250.0)
	if !strings.Contains(got, "No prior operating-profit history available") {
		t.Errorf("expected empty-history note, got:\n%s", got)
	}
	if !strings.Contains(got, "could not read history from SFTP") {
		t.Errorf("expected status note to surface, got:\n%s", got)
	}
}

func mustDate(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}
