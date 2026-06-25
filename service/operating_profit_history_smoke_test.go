package service

import (
	"path/filepath"
	"testing"

	"jiaming2012/sales-processor/models"
)

// TestBackfillSmoke is a light "does it actually parse" check against the
// real PDFs under output/payroll/. Skipped automatically when the
// directory is empty (CI / fresh checkouts).
func TestBackfillSmoke(t *testing.T) {
	matches, err := filepath.Glob("../output/payroll/payroll_*.pdf")
	if err != nil || len(matches) == 0 {
		t.Skip("no payroll PDFs available")
	}
	h := &models.OperatingProfitHistory{}
	added, status := BackfillOperatingProfitFromPDFs(h, "../output/payroll", 3)
	if added == 0 {
		t.Fatalf("expected at least 1 backfill entry, got 0 (status: %q)", status)
	}
	for _, e := range h.Entries {
		if e.WeekEnding == "" {
			t.Errorf("entry missing week ending: %+v", e)
		}
		if e.Sales <= 0 {
			t.Errorf("entry %s has non-positive Sales: %f", e.WeekEnding, e.Sales)
		}
	}
	t.Logf("backfilled %d entries (status: %q)", added, status)
	for _, e := range h.Entries {
		t.Logf("  %s: sales=$%.2f cogs=$%.2f labor=$%.2f cc=$%.2f op_profit=$%.2f",
			e.WeekEnding, e.Sales, e.COGSExclTax, e.TotalLabor, e.CCFees, e.OperatingProfit)
	}
}
