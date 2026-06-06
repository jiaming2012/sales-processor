# HQ Backend Change Handoff — Expose `pending_review_details` on `/period-summary`

> **Companion docs (all in this directory):**
> - [`cogs-hq-receipt-gate.md`](./cogs-hq-receipt-gate.md) — completeness gate for unreceipted card txns (shipped; amendment shipped).
> - [`cogs-hq-undercount-fix.md`](./cogs-hq-undercount-fix.md) — placeholder line items (shipped).
> - [`cogs-hq-category-filter.md`](./cogs-hq-category-filter.md) — Mercury category allowlist (shipped).
> - [`cogs-hq-handoff.md`](./cogs-hq-handoff.md) — `by_vendor` array (shipped).
> - [`cogs-hq-tracked-tx-ids.md`](./cogs-hq-tracked-tx-ids.md) — `tracked_bank_tx_ids` array (shipped).
>
> This doc is independent — purely additive on the response. Safe to ship anytime. The sales-processor side degrades gracefully when the new field is absent (renders the legacy UUID list, same as today).

Suggested workflow: feed this spec into `/gsd-quick` from the `hq/` directory.

---

## Problem

When `/period-summary` reports the completeness gate is closed, the
response gives sales-processor a list of bare UUIDs:

```json
"completeness": {
  "ready": false,
  "pending_review_ids": [
    "3fcf6c3c-b5b0-4b60-b28f-1ebf64472f59",
    "c9cf1d0b-b42f-4d3d-92e5-4bb7027d841e",
    "89e585db-574a-40f2-858c-9a0c084c7da9"
  ],
  "unlinked_line_item_ids": []
}
```

Sales-processor surfaces those IDs verbatim in the operator's
fail-fast message. Real-world impact: the operator sees three UUIDs,
has no idea which Mercury txns they correspond to, and either has to
(a) click into HQ Inventory and hunt manually, or (b) cross-reference
in their head — which often means re-running the receipt worker
"just in case" the gate clears on its own.

`/api/v1/inventory/purchases/pending` already returns full pending
details (vendor, event_date, bank_total, reason) — but it's gated on
cookie/session auth (`/inventory` group at
`cmd/server/main.go:418-429`). Sales-processor authenticates via
service-token middleware (`/inventory/period-summary` at
`cmd/server/main.go:354-358`) and can't reach the richer endpoint
without operator credentials.

The minimal fix is to expose the same info on `/period-summary`
itself, behind the same service-token gate, so the existing
sales-processor caller can render it directly.

---

## Scope

Single additive change. The existing pending-IDs query already touches
every column we need; just SELECT more columns and emit them as
a parallel `pending_review_details` array.

### Response shape (additive — no breaking change)

```json
{
  "from": "2026-05-25",
  "to": "2026-05-31",
  "cogs_excl_tax": 0,
  "cogs_incl_tax": 0,
  "purchase_event_count": 0,
  "by_vendor": [],
  "tracked_bank_tx_ids": ["tx_…"],
  "completeness": {
    "ready": false,
    "pending_review_ids": [
      "3fcf6c3c-…", "c9cf1d0b-…", "89e585db-…"
    ],
    "pending_review_details": [
      {
        "id": "3fcf6c3c-b5b0-4b60-b28f-1ebf64472f59",
        "bank_tx_id": "tx_01HXYZABCDEF",
        "vendor": "Restaurant Depot",
        "event_date": "2026-05-29",
        "bank_total": 391.96,
        "reason": "no_attachment_on_bank_tx"
      },
      …
    ],
    "unlinked_line_item_ids": []
  }
}
```

Notes on field semantics (matching the existing `PendingPurchase`
struct at `internal/inventory/types.go:77-94`):
- `id`: same UUID as in `pending_review_ids`. Order matches.
- `bank_tx_id`: Mercury's tx id. Useful for cross-referencing with
  sales-processor's Mercury client (it already has Mercury access).
- `vendor`: free-text vendor name as stored on `pending_purchases.vendor`
  (may be NULL if the receipt worker couldn't extract one — emit `""`
  in that case so the consumer can still render a row).
- `event_date`: `YYYY-MM-DD` string. May be NULL on legacy rows
  (pre-`event_date` column); fall back to
  `(created_at AT TIME ZONE 'America/Chicago')::date` so every row has
  a date to display.
- `bank_total`: dollars as a `float64`. This is the authoritative
  amount from Mercury, not the operator-entered `total`. Always set.
- `reason`: short string the receipt worker recorded
  (e.g. `"no_attachment_on_bank_tx"`, `"receipt_parse_failed"`). May
  be NULL — emit `null` or omit; the consumer treats both as "no
  reason given".

`pending_review_ids` stays in the response — exact same content,
unchanged order — so any consumer that only reads the bare-IDs list
keeps working without changes.

`pending_review_details` is emitted as `[]` (never `null`) for an
empty list, same convention as the other array fields on this response.

---

## Files to change

| File | What changes |
|---|---|
| `internal/inventory/types.go` | Add `PendingReviewDetails []PendingReviewDetail` field on `CompletenessBlock`. Add `PendingReviewDetail` struct. |
| `internal/inventory/handler.go` | Extend the pending-IDs query in `PeriodSummaryHandler` to SELECT more columns; scan into both `pendingIDs` and `pendingDetails`. |
| `internal/inventory/period_summary_test.go` | Add cases asserting the new field. |

No schema migration — all source columns already exist on
`pending_purchases`.

---

## 1. `types.go` — struct update

Current `CompletenessBlock` (`internal/inventory/types.go:183-187`):

```go
type CompletenessBlock struct {
	Ready               bool     `json:"ready"`
	PendingReviewIDs    []string `json:"pending_review_ids"`
	UnlinkedLineItemIDs []string `json:"unlinked_line_item_ids"`
}
```

After:

```go
type CompletenessBlock struct {
	Ready               bool                  `json:"ready"`
	PendingReviewIDs    []string              `json:"pending_review_ids"`
	PendingReviewDetails []PendingReviewDetail `json:"pending_review_details"`
	UnlinkedLineItemIDs []string              `json:"unlinked_line_item_ids"`
}

// PendingReviewDetail is one row of operator-facing context per pending
// review. Exposed on /period-summary so service-token callers
// (sales-processor) can render a meaningful failure message without a
// second round trip to the cookie-auth-only /purchases/pending
// endpoint.
type PendingReviewDetail struct {
	ID        string  `json:"id"`
	BankTxID  string  `json:"bank_tx_id"`
	Vendor    string  `json:"vendor"`     // "" when receipt parser couldn't extract one
	EventDate string  `json:"event_date"` // YYYY-MM-DD; falls back to created_at::date
	BankTotal float64 `json:"bank_total"`
	Reason    *string `json:"reason,omitempty"`
}
```

Initialise `PendingReviewDetails` to `[]PendingReviewDetail{}` (never
`nil`) so empty periods render `[]`.

---

## 2. `handler.go` — extend the pending-IDs query

The current step 3 in `PeriodSummaryHandler` (`handler.go:1201-1228`,
post-amendment) reads:

```go
pendingIDs := []string{}
rows, err := pool.Query(r.Context(), `
    SELECT id::text
    FROM pending_purchases
    WHERE COALESCE(event_date, (created_at AT TIME ZONE 'America/Chicago')::date)
            BETWEEN $1 AND $2
      AND confirmed_at IS NULL
      AND discarded_at IS NULL
    ORDER BY COALESCE(event_date, (created_at AT TIME ZONE 'America/Chicago')::date),
             created_at`, fromStr, toStr)
…
```

Extend to also select `bank_tx_id`, `vendor`, `event_date` (with
fallback), `bank_total`, and `reason`, scanning into both `pendingIDs`
and `pendingDetails`:

```go
pendingIDs := []string{}
pendingDetails := []PendingReviewDetail{}
rows, err := pool.Query(r.Context(), `
    SELECT
        id::text                                                                     AS id,
        bank_tx_id,
        COALESCE(vendor, '')                                                         AS vendor,
        COALESCE(event_date, (created_at AT TIME ZONE 'America/Chicago')::date)::text AS event_date,
        bank_total,
        reason
    FROM pending_purchases
    WHERE COALESCE(event_date, (created_at AT TIME ZONE 'America/Chicago')::date)
            BETWEEN $1 AND $2
      AND confirmed_at IS NULL
      AND discarded_at IS NULL
    ORDER BY COALESCE(event_date, (created_at AT TIME ZONE 'America/Chicago')::date),
             created_at`, fromStr, toStr)
if err != nil { … same error path as before … }
defer rows.Close()

for rows.Next() {
    var d PendingReviewDetail
    if err := rows.Scan(
        &d.ID, &d.BankTxID, &d.Vendor, &d.EventDate, &d.BankTotal, &d.Reason,
    ); err != nil {
        log.Printf("PeriodSummary pending scan: %v", err)
        writeError(w, http.StatusInternalServerError, "internal_error")
        return
    }
    pendingIDs = append(pendingIDs, d.ID)
    pendingDetails = append(pendingDetails, d)
}
if err := rows.Err(); err != nil { … same error path as before … }
```

Then in the response struct literal where `Completeness` is built, add:

```go
Completeness: CompletenessBlock{
    Ready:                len(pendingIDs) == 0 && len(unlinkedIDs) == 0,
    PendingReviewIDs:     pendingIDs,
    PendingReviewDetails: pendingDetails,
    UnlinkedLineItemIDs:  unlinkedIDs,
},
```

`pending_review_ids` and `pending_review_details` are guaranteed
same-length and same-order by this construction. Tests should pin
that invariant.

---

## 3. Tests — `period_summary_test.go`

Add to `internal/inventory/period_summary_test.go`:

1. **Shape**: `pending_review_details` is a non-nil array.
2. **Parity with IDs**: `len(pending_review_details) ==
   len(pending_review_ids)` and `details[i].ID == ids[i]` for every i.
3. **Field population**: insert a pending row with known vendor /
   event_date / bank_total / reason, assert the detail row matches.
4. **Null event_date fallback**: insert a pending row with
   `event_date IS NULL` and `created_at = 2026-05-29 22:02 UTC`.
   Assert `details[0].EventDate == "2026-05-29"` (the
   America/Chicago-cast `created_at::date`).
5. **Null vendor**: insert a pending row with `vendor IS NULL`;
   assert `details[0].Vendor == ""` (not `null`, not omitted).
6. **Null reason**: insert a pending row with `reason IS NULL`;
   assert `details[0].Reason == nil` (omitted in JSON).
7. **Empty period**: `pending_review_details` is `[]`, not `null`.

---

## Consumer

Once this ships, sales-processor's HQ-completeness fail-fast banner
will render

```
- 2026-05-29  Restaurant Depot  $391.96  (no receipt attached)
- 2026-05-30  Save-A-Lot        $214.50  (receipt parse failed)
- …
```

instead of three bare UUIDs. The richer message lets operators decide
at a glance whether a pending row needs attention (real food vendor,
amount looks right) or can be dismissed (clearly a personal charge
that slipped through).

Until this ships, sales-processor falls back to printing the UUID
list — same as today. Deploy ordering is safe in either direction.

No version bump or feature flag needed — the field is additive and the
consumer treats it as optional.
