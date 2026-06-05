# HQ Backend Change Handoff — `by_vendor` on `/period-summary`

> **Companion doc:** [`cogs-hq-receipt-gate.md`](./cogs-hq-receipt-gate.md)
> covers the parallel HQ change that makes the completeness gate fail on
> unreceipted Mercury card transactions. The two are independent — either
> can ship first.

This document specifies a small change to the **Yumyums HQ backend**
(`/Users/jamal/projects/yumyums/hq/backend`) so that the sales-processor's
weekly payroll PDF can render a per-vendor Cost of Goods Sold table
("Restaurant Depot $X, Save-A-Lot $Y, …").

The sales-processor side ships independently and degrades gracefully when
`by_vendor` is absent (the COGS summary still prints; only the per-vendor
table is omitted). So this change is non-blocking but completes the feature.

Suggested workflow (HQ has `.planning/`): feed the spec below into
`/gsd-quick` from the `hq/` directory.

---

## Scope

Extend the JSON response of:

```
GET /api/v1/inventory/period-summary?from=YYYY-MM-DD&to=YYYY-MM-DD
```

…to include a new `by_vendor` array — one row per vendor with at least one
purchase event in the period, ordered by spend descending.

### Current response shape

```json
{
  "from": "2026-05-27",
  "to": "2026-06-02",
  "cogs_excl_tax": 2640.85,
  "cogs_incl_tax": 2826.71,
  "purchase_event_count": 9,
  "completeness": {
    "ready": true,
    "pending_review_ids": [],
    "unlinked_line_item_ids": []
  }
}
```

### New response shape (additive — no breaking change)

```json
{
  "from": "2026-05-27",
  "to": "2026-06-02",
  "cogs_excl_tax": 2640.85,
  "cogs_incl_tax": 2826.71,
  "purchase_event_count": 9,
  "by_vendor": [
    {
      "vendor_id": "0b1f…",
      "vendor_name": "Restaurant Depot",
      "total_excl_tax": 1842.40,
      "total_incl_tax": 1971.37,
      "trip_count": 5
    },
    {
      "vendor_id": "9c4d…",
      "vendor_name": "Save-A-Lot",
      "total_excl_tax": 612.18,
      "total_incl_tax": 655.03,
      "trip_count": 3
    }
  ],
  "completeness": { "ready": true, "pending_review_ids": [], "unlinked_line_item_ids": [] }
}
```

Existing top-level totals (`cogs_excl_tax`, `cogs_incl_tax`,
`purchase_event_count`) MUST equal the sums across `by_vendor` rows.

---

## Files to change

| File | What changes |
|---|---|
| `internal/inventory/types.go` | Add `VendorCOGS` struct + `ByVendor []VendorCOGS` field on `PeriodSummary`. |
| `internal/inventory/handler.go` | New SQL block (between current steps 1 and 2) that aggregates per vendor; populate `resp.ByVendor`. |
| `internal/inventory/period_summary_test.go` | Add cases asserting `by_vendor` rows + invariant that vendor sums equal top-level totals. |

---

## 1. `types.go` — struct update

Current (`internal/inventory/types.go:153-163`):

```go
// PeriodSummary is the response body for GET /api/v1/inventory/period-summary.
// COGS aggregates use purchase_events.event_date (DATE — no TZ).
// Completeness gate uses pending_purchases.created_at cast to America/Chicago calendar date.
type PeriodSummary struct {
	From               string            `json:"from"`                 // YYYY-MM-DD
	To                 string            `json:"to"`                   // YYYY-MM-DD
	COGSExclTax        float64           `json:"cogs_excl_tax"`
	COGSInclTax        float64           `json:"cogs_incl_tax"`
	PurchaseEventCount int               `json:"purchase_event_count"`
	Completeness       CompletenessBlock `json:"completeness"`
}
```

After:

```go
type PeriodSummary struct {
	From               string            `json:"from"`
	To                 string            `json:"to"`
	COGSExclTax        float64           `json:"cogs_excl_tax"`
	COGSInclTax        float64           `json:"cogs_incl_tax"`
	PurchaseEventCount int               `json:"purchase_event_count"`
	ByVendor           []VendorCOGS      `json:"by_vendor"`
	Completeness       CompletenessBlock `json:"completeness"`
}

// VendorCOGS is one row of the per-vendor breakdown returned by
// /period-summary. trip_count counts distinct purchase_events for the vendor
// in the period; tax is allocated per event (not per line item).
type VendorCOGS struct {
	VendorID     string  `json:"vendor_id"`
	VendorName   string  `json:"vendor_name"`
	TotalExclTax float64 `json:"total_excl_tax"`
	TotalInclTax float64 `json:"total_incl_tax"`
	TripCount    int     `json:"trip_count"`
}
```

Initialise `ByVendor` to `[]VendorCOGS{}` (never `nil`) so the JSON renders
as `[]` not `null` when there is no spend in the period.

---

## 2. `handler.go` — new SQL block

The current aggregate query (`handler.go:1088-1103`) computes one row
covering the whole period. Add a second query that returns one row per
vendor. Place it between the existing step 1 (aggregate) and step 2
(pending IDs) — vendor breakdown is the natural extension of the aggregate.

Suggested SQL:

```sql
SELECT
    v.id::text                                                                AS vendor_id,
    v.name                                                                    AS vendor_name,
    ROUND(COALESCE(SUM(pli.quantity * pli.price), 0)::numeric, 2)             AS total_excl_tax,
    ROUND(
      COALESCE(SUM(pli.quantity * pli.price), 0)::numeric
      + COALESCE(
          (SELECT SUM(pe2.tax)
             FROM purchase_events pe2
            WHERE pe2.vendor_id = v.id
              AND pe2.event_date BETWEEN $1 AND $2),
          0
        )::numeric,
      2
    )                                                                         AS total_incl_tax,
    COUNT(DISTINCT pe.id)                                                     AS trip_count
FROM purchase_events pe
JOIN vendors v               ON v.id = pe.vendor_id
LEFT JOIN purchase_line_items pli ON pli.purchase_event_id = pe.id
WHERE pe.event_date BETWEEN $1 AND $2
GROUP BY v.id, v.name
ORDER BY total_excl_tax DESC, v.name ASC;
```

Notes:
- `LEFT JOIN purchase_line_items` so a vendor with a `purchase_event` but
  zero line items still appears (its `total_excl_tax` will be `0` but
  `trip_count` will still be 1 — useful for spotting receipts that bypassed
  line-item parsing).
- Tax is summed via correlated subquery so it isn't multiplied by the
  line-item join cardinality. Equivalent rewrites (subquery on
  `purchase_events`, then JOIN to lines) are fine — pick whichever the
  team prefers.
- The same `$1`/`$2` date params already in scope for the aggregate query
  are reused — no new parameter parsing.
- Order: spend desc, name asc as tiebreaker (deterministic for tests and
  the PDF table).

Scan loop (mirrors the pattern at lines 1115-1141):

```go
byVendor := []VendorCOGS{}
rowsV, err := pool.Query(r.Context(), `...SQL above...`, fromStr, toStr)
if err != nil {
    log.Printf("PeriodSummary by-vendor query: %v", err)
    writeError(w, http.StatusInternalServerError, "internal_error")
    return
}
defer rowsV.Close()
for rowsV.Next() {
    var v VendorCOGS
    if err := rowsV.Scan(&v.VendorID, &v.VendorName, &v.TotalExclTax, &v.TotalInclTax, &v.TripCount); err != nil {
        log.Printf("PeriodSummary by-vendor scan: %v", err)
        writeError(w, http.StatusInternalServerError, "internal_error")
        return
    }
    byVendor = append(byVendor, v)
}
if err := rowsV.Err(); err != nil {
    log.Printf("PeriodSummary by-vendor rows.Err: %v", err)
    writeError(w, http.StatusInternalServerError, "internal_error")
    return
}
```

Then in the response struct literal (`handler.go:1175-1186`), add:

```go
ByVendor: byVendor,
```

---

## 3. Tests — `period_summary_test.go`

Existing tests at `internal/inventory/period_summary_test.go` already
fixture purchase events with multiple vendors. Add (or extend an existing
test):

1. **Shape**: response includes `by_vendor` as a non-nil array (use
   `json.RawMessage` or struct decode).
2. **Sums match invariant**: `Σ row.TotalExclTax == COGSExclTax` and
   `Σ row.TotalInclTax == COGSInclTax` (within `0.01` to absorb rounding).
3. **Order**: rows ordered by `TotalExclTax DESC`, then `VendorName ASC`.
4. **Empty period**: `by_vendor` is `[]`, not `null`, when no events fall in
   the period.
5. **Zero-line-items receipt**: a `purchase_event` whose lines were never
   parsed still produces a row with `TotalExclTax = 0` and `TripCount = 1`
   (regression guard for the LEFT JOIN behavior).

---

## Consumer

Once this ships, the sales-processor will start rendering a per-vendor table
in the weekly payroll PDF — see `sales-processor/service/external/hq.go`
and `sales-processor/docs/payroll-report.md` (Cost of Goods Sold section).
Until then the sales-processor degrades gracefully: the COGS summary
(food cost %, gross profit) still prints; only the per-vendor table is
omitted.

No version bump or feature flag is required — the field is additive and the
consumer treats it as optional.
