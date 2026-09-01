package main

import (
	"strings"
	"testing"

	"jiaming2012/sales-processor/service/external"
)

func strptr(s string) *string { return &s }

// TestFormatHQCompletenessFailure_Cardholder pins the behaviour behind the
// operator ask: when a pending (unreceipted) card purchase can be traced to
// a cardholder, the failure banner names them (e.g. "· Jamal Cole") so the
// operator knows whose receipt to chase; when it can't, the row renders
// exactly as before with no attribution.
func TestFormatHQCompletenessFailure_Cardholder(t *testing.T) {
	summary := &external.HQPeriodSummary{
		From: "2026-08-25",
		To:   "2026-08-31",
		Completeness: external.HQCompletenessBlock{
			Ready:            false,
			PendingReviewIDs: []string{"id_1", "id_2"},
			PendingReviewDetails: []external.HQPendingReviewDetail{
				{
					ID:        "id_1",
					BankTxID:  "tx_jamal",
					Vendor:    "SAVE A LOT 3025",
					EventDate: "2026-08-27",
					BankTotal: -22.04,
					Reason:    strptr("no_attachment_on_bank_tx"),
				},
				{
					ID:        "id_2",
					BankTxID:  "tx_unknown",
					Vendor:    "RESTAURANT DEPOT",
					EventDate: "2026-08-28",
					BankTotal: -391.96,
					Reason:    strptr("no_attachment_on_bank_tx"),
				},
			},
		},
	}
	cardholders := map[string]string{"tx_jamal": "Jamal Cole"}

	out := formatHQCompletenessFailure("https://hq.example", summary, cardholders)

	lines := strings.Split(out, "\n")
	var jamalLine, depotLine string
	for _, ln := range lines {
		switch {
		case strings.Contains(ln, "SAVE A LOT 3025"):
			jamalLine = ln
		case strings.Contains(ln, "RESTAURANT DEPOT"):
			depotLine = ln
		}
	}

	if jamalLine == "" || depotLine == "" {
		t.Fatalf("expected both pending rows in banner, got:\n%s", out)
	}

	// Attributed row names the cardholder, before the reason suffix.
	if !strings.Contains(jamalLine, "· Jamal Cole") {
		t.Errorf("expected cardholder attribution on SAVE A LOT row, got: %q", jamalLine)
	}
	if i, j := strings.Index(jamalLine, "Jamal Cole"), strings.Index(jamalLine, "no receipt attached"); i < 0 || j < 0 || i > j {
		t.Errorf("cardholder should render before the reason suffix, got: %q", jamalLine)
	}

	// Unmatched row is untouched — no stray attribution bullet.
	if strings.Contains(depotLine, "·") {
		t.Errorf("row with no known cardholder should carry no attribution, got: %q", depotLine)
	}
}

// TestFormatHQCompletenessFailure_NilCardholders guards the degraded path
// (--skip-mercury / Mercury unreachable): a nil map must render the banner
// without panicking and without any attribution.
func TestFormatHQCompletenessFailure_NilCardholders(t *testing.T) {
	summary := &external.HQPeriodSummary{
		From: "2026-08-25",
		To:   "2026-08-31",
		Completeness: external.HQCompletenessBlock{
			Ready:            false,
			PendingReviewIDs: []string{"id_1"},
			PendingReviewDetails: []external.HQPendingReviewDetail{
				{ID: "id_1", BankTxID: "tx_jamal", Vendor: "SAVE A LOT 3025", EventDate: "2026-08-27", BankTotal: -22.04},
			},
		},
	}

	out := formatHQCompletenessFailure("https://hq.example", summary, nil)
	if !strings.Contains(out, "SAVE A LOT 3025") {
		t.Fatalf("expected pending row in banner, got:\n%s", out)
	}
	if strings.Contains(out, "·") {
		t.Errorf("nil cardholder map should produce no attribution, got:\n%s", out)
	}
}
