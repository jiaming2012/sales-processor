package models

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"
)

// OperatingProfitEntry is one period (week) of operating-profit inputs and
// the resulting figure. Stored long-form so the rolling chart can recompute
// or sanity-check the number if any of the contributing fields are revised
// in a later run.
type OperatingProfitEntry struct {
	WeekEnding      string  `json:"week_ending"` // YYYY-MM-DD
	Sales           float64 `json:"sales"`
	COGSExclTax     float64 `json:"cogs_excl_tax"`
	TotalLabor      float64 `json:"total_labor"`
	CCFees          float64 `json:"cc_fees"`
	OperatingProfit float64 `json:"operating_profit"`
}

// OperatingProfitHistory is the on-disk JSON document. Entries are kept
// sorted ascending by WeekEnding.
type OperatingProfitHistory struct {
	Entries []OperatingProfitEntry `json:"entries"`
}

// Load parses a history document from r. An empty stream yields an empty
// history (not an error) so first-time callers can treat "file missing"
// and "file empty" the same way.
func LoadOperatingProfitHistory(r io.Reader) (*OperatingProfitHistory, error) {
	h := &OperatingProfitHistory{}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read operating profit history: %w", err)
	}
	if len(data) == 0 {
		return h, nil
	}
	if err := json.Unmarshal(data, h); err != nil {
		return nil, fmt.Errorf("decode operating profit history: %w", err)
	}
	h.sort()
	return h, nil
}

// Save writes the history as pretty-printed JSON to w.
func (h *OperatingProfitHistory) Save(w io.Writer) error {
	h.sort()
	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return fmt.Errorf("encode operating profit history: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("write operating profit history: %w", err)
	}
	return nil
}

// Upsert inserts or replaces the entry for the given week-ending date.
// Replaces (rather than skips) so re-running for an existing week picks up
// any corrected inputs from a later run.
func (h *OperatingProfitHistory) Upsert(e OperatingProfitEntry) {
	for i, existing := range h.Entries {
		if existing.WeekEnding == e.WeekEnding {
			h.Entries[i] = e
			return
		}
	}
	h.Entries = append(h.Entries, e)
	h.sort()
}

// HasEntry reports whether the history already carries a row for the
// given YYYY-MM-DD week ending.
func (h *OperatingProfitHistory) HasEntry(weekEnding string) bool {
	for _, e := range h.Entries {
		if e.WeekEnding == weekEnding {
			return true
		}
	}
	return false
}

func (h *OperatingProfitHistory) sort() {
	sort.Slice(h.Entries, func(i, j int) bool {
		return h.Entries[i].WeekEnding < h.Entries[j].WeekEnding
	})
}

// TrailingWindow returns the last n entries (or all of them when fewer
// exist), preserving ascending order.
func (h *OperatingProfitHistory) TrailingWindow(n int) []OperatingProfitEntry {
	if n <= 0 || len(h.Entries) == 0 {
		return nil
	}
	if len(h.Entries) <= n {
		out := make([]OperatingProfitEntry, len(h.Entries))
		copy(out, h.Entries)
		return out
	}
	out := make([]OperatingProfitEntry, n)
	copy(out, h.Entries[len(h.Entries)-n:])
	return out
}

// MakeOperatingProfitEntry builds an entry from the same inputs Summary
// already has on hand. Keeps WeekEnding as YYYY-MM-DD so it sorts as a
// string and serialises cleanly.
func MakeOperatingProfitEntry(weekEnding time.Time, sales, cogsExclTax, totalLabor, ccFees float64) OperatingProfitEntry {
	return OperatingProfitEntry{
		WeekEnding:      weekEnding.Format("2006-01-02"),
		Sales:           sales,
		COGSExclTax:     cogsExclTax,
		TotalLabor:      totalLabor,
		CCFees:          ccFees,
		OperatingProfit: sales - cogsExclTax - totalLabor - ccFees,
	}
}
