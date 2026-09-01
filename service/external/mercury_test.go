package external

import (
	"reflect"
	"sort"
	"testing"
)

func TestIsSupportedCardKind(t *testing.T) {
	for _, kind := range []string{
		"creditCardTransaction", "debitCardTransaction",
		"creditCardCredit", "debitCardCredit",
	} {
		if !IsSupportedCardKind(kind) {
			t.Errorf("IsSupportedCardKind(%q) = false, want true", kind)
		}
	}
	for _, kind := range []string{
		"", "externalTransfer", "internalTransfer", "wireTransfer",
		"fee", "interestPayment", "unknown",
	} {
		if IsSupportedCardKind(kind) {
			t.Errorf("IsSupportedCardKind(%q) = true, want false", kind)
		}
	}
}

// TestMercuryHQGap covers the gap-check diff that drives
// fetchHQPeriodSummary in main.go. The four-cell matrix is on
// (mercury_has, hq_tracks); plus filters for non-card kinds and
// non-sent statuses; plus the empty-period case.
func TestMercuryHQGap(t *testing.T) {
	cardA := MercuryTransactionLite{ID: "tx_A", Status: "sent", Kind: "creditCardTransaction"}
	cardB := MercuryTransactionLite{ID: "tx_B", Status: "sent", Kind: "debitCardTransaction"}
	refund := MercuryTransactionLite{ID: "tx_R", Status: "sent", Kind: "creditCardCredit"}
	transfer := MercuryTransactionLite{ID: "tx_X", Status: "sent", Kind: "externalTransfer"}
	pending := MercuryTransactionLite{ID: "tx_P", Status: "pending", Kind: "creditCardTransaction"}

	cases := []struct {
		name    string
		txns    []MercuryTransactionLite
		tracked []string
		wantIDs []string
	}{
		{
			name:    "all tracked → no gap",
			txns:    []MercuryTransactionLite{cardA, cardB},
			tracked: []string{"tx_A", "tx_B"},
			wantIDs: nil,
		},
		{
			name:    "one missing → gap of one",
			txns:    []MercuryTransactionLite{cardA, cardB},
			tracked: []string{"tx_A"},
			wantIDs: []string{"tx_B"},
		},
		{
			name:    "all missing → gap of all",
			txns:    []MercuryTransactionLite{cardA, cardB},
			tracked: nil,
			wantIDs: []string{"tx_A", "tx_B"},
		},
		{
			name:    "tracked has txn not in Mercury → no false positive",
			txns:    []MercuryTransactionLite{cardA},
			tracked: []string{"tx_A", "tx_GHOST"},
			wantIDs: nil,
		},
		{
			name:    "non-card kind filtered before diff",
			txns:    []MercuryTransactionLite{cardA, transfer},
			tracked: []string{"tx_A"},
			wantIDs: nil,
		},
		{
			name:    "non-sent status filtered before diff",
			txns:    []MercuryTransactionLite{cardA, pending},
			tracked: []string{"tx_A"},
			wantIDs: nil,
		},
		{
			name:    "refund kind included in gap",
			txns:    []MercuryTransactionLite{refund},
			tracked: nil,
			wantIDs: []string{"tx_R"},
		},
		{
			name:    "empty period → no gap, no panic",
			txns:    nil,
			tracked: nil,
			wantIDs: nil,
		},
		{
			name:    "tracked covers refund kind → no gap",
			txns:    []MercuryTransactionLite{cardA, refund},
			tracked: []string{"tx_A", "tx_R"},
			wantIDs: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gap := MercuryHQGap(tc.txns, tc.tracked)
			gotIDs := make([]string, len(gap))
			for i, tx := range gap {
				gotIDs[i] = tx.ID
			}
			sort.Strings(gotIDs)
			want := append([]string(nil), tc.wantIDs...)
			sort.Strings(want)
			if !reflect.DeepEqual(gotIDs, want) && !(len(gotIDs) == 0 && len(want) == 0) {
				t.Errorf("MercuryHQGap IDs = %v, want %v", gotIDs, want)
			}
		})
	}
}

// TestCardholderByBankTx covers the join that attributes an unreceipted
// card purchase to its cardholder for the HQ completeness failure banner:
// name preferred, last-four fallback, and the several ways a row is
// legitimately omitted (no card, unknown card, unlabelled card).
func TestCardholderByBankTx(t *testing.T) {
	cards := []MercuryCard{
		{ID: "card_jamal", NameOnCard: "Jamal Cole", LastFour: "1234"},
		{ID: "card_last4only", NameOnCard: "", LastFour: "9876"},
		{ID: "card_blank", NameOnCard: "", LastFour: ""},
	}
	txns := []MercuryTransactionLite{
		{ID: "tx_named", CardID: "card_jamal"},        // → name
		{ID: "tx_last4", CardID: "card_last4only"},    // → last-four fallback
		{ID: "tx_blankcard", CardID: "card_blank"},    // card carries no label → omit
		{ID: "tx_unknowncard", CardID: "card_ghost"},  // card not in list → omit
		{ID: "tx_nocard", CardID: ""},                 // non-card txn → omit
	}

	got := CardholderByBankTx(txns, cards)
	want := map[string]string{
		"tx_named": "Jamal Cole",
		"tx_last4": "card ••9876",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CardholderByBankTx = %#v, want %#v", got, want)
	}
}

// TestCardholderByBankTx_Empty guards the nil-input path: no cards and no
// txns must yield a usable (non-nil, empty) map, since the banner indexes
// into it directly.
func TestCardholderByBankTx_Empty(t *testing.T) {
	got := CardholderByBankTx(nil, nil)
	if got == nil {
		t.Fatal("CardholderByBankTx(nil, nil) = nil, want non-nil empty map")
	}
	if len(got) != 0 {
		t.Errorf("CardholderByBankTx(nil, nil) = %#v, want empty", got)
	}
}
