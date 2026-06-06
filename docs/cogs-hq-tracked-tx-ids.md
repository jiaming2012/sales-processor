# HQ Backend Change Handoff — Expose `tracked_bank_tx_ids` on `/period-summary`

> **Companion docs (all in this directory):**
> - [`cogs-hq-receipt-gate.md`](./cogs-hq-receipt-gate.md) — completeness gate for unreceipted card transactions (shipped; amendment pending).
> - [`cogs-hq-undercount-fix.md`](./cogs-hq-undercount-fix.md) — placeholder line items (shipped).
> - [`cogs-hq-category-filter.md`](./cogs-hq-category-filter.md) — Mercury-category allowlist for COGS (shipped).
> - [`cogs-hq-handoff.md`](./cogs-hq-handoff.md) — `by_vendor` array on `/period-summary` (shipped).
> - [`payroll-mercury-gap-check.md`](./payroll-mercury-gap-check.md) — the sales-processor side that consumes this field (pending).
>
> Ship this doc **before** `payroll-mercury-gap-check.md`. The sales-processor
> side degrades gracefully when this field is absent (logs a warning, skips
> the diff check), so HQ-first deploys are safe.
>
> Composes cleanly with the receipt-gate amendment: if the amendment ships
> first, the SQL block here can reuse the same `COALESCE(event_date,
> created_at::date)` date filter for the `pending_purchases` half of the
> UNION.

Suggested workflow: feed this spec into `/gsd-quick` from the `hq/`
directory.

---

## Problem

Today the `completeness.ready` gate on `/period-summary` only catches
transactions that **HQ already knows about** — either a `purchase_events`
row (confirmed) or a `pending_purchases` row (awaiting triage). It cannot
catch the transactions that **Mercury knows about but the receipt worker
hasn't ingested yet**.

Concrete reproduction:

- Operator swipes the Mercury card at Restaurant Depot, $391.96, Saturday
  afternoon.
- Mercury surfaces the transaction within ~1h.
- Sales-processor weekly run kicks off Sunday morning — **before** the
  receipt worker's next poll (every 6h).
- HQ has no row for that transaction yet. `completeness.ready` returns
  `true`. The weekly payroll PDF omits the $391.96. Food cost % is
  silently understated.

The same hole exists if the receipt worker is broken or lagging for any
reason (Mercury API outage, container crash-looped, etc).

**Goal:** give sales-processor the data it needs to diff Mercury's view of
the period against HQ's view, and hard-fail when Mercury has a card
transaction HQ hasn't seen yet. Sales-processor already has a Mercury
client (`sales-processor/service/external/mercury.go`); it just needs HQ
to publish the "what bank_tx_ids HQ has touched for this period" list.

The diff itself is the consumer's job — see
`payroll-mercury-gap-check.md`. This doc only specifies the new field on
the HQ response.

---

## Scope

Extend the JSON response of:

```
GET /api/v1/inventory/period-summary?from=YYYY-MM-DD&to=YYYY-MM-DD
```

…to include a new `tracked_bank_tx_ids` array — every `bank_tx_id` HQ
has touched for the period, across all states (confirmed/pending/
discarded). Sales-processor diffs this against Mercury's transaction list
for the same period; non-empty diff → fail-fast.

### Why all states (not just unresolved)

If HQ only returned `pending_purchases.bank_tx_id WHERE confirmed_at IS
NULL AND discarded_at IS NULL`, then:

- Resolved/confirmed txns (now in `purchase_events`) would look "unknown"
  to sales-processor → false-positive gap → fail forever after a
  successful triage.
- Discarded txns (e.g. NETFLIX, personal-fuel charges the operator
  dismissed once) would re-trigger the gap every week → operator
  re-triages the same things over and over.

The right comparison is: *"Mercury saw N transactions for the period; for
how many of those did HQ at any point in time create a row?"* If the
answer is "all of them", HQ is caught up. Otherwise there's a gap.

### Response shape (additive — no breaking change)

```json
{
  "from": "2026-05-25",
  "to": "2026-05-31",
  "cogs_excl_tax": 2640.85,
  "cogs_incl_tax": 2826.71,
  "purchase_event_count": 9,
  "by_vendor": [ … ],
  "tracked_bank_tx_ids": [
    "tx_01HXYZABCDEF…",
    "tx_01HXYZABCDEG…",
    "tx_01HXYZABCDEH…"
  ],
  "completeness": { "ready": true, "pending_review_ids": [], "unlinked_line_item_ids": [] }
}
```

Empty period → `tracked_bank_tx_ids: []` (never `null`).

Order is not load-bearing for the consumer (it builds a set), but pick a
deterministic order anyway (ASC by bank_tx_id) so the response is
diffable across calls and tests are stable.

---

## Files to change

| File | What changes |
|---|---|
| `internal/inventory/types.go` | Add `TrackedBankTxIDs []string` field on `PeriodSummary`. |
| `internal/inventory/handler.go` | New SQL block in `PeriodSummaryHandler` — UNION of `bank_tx_id` from `purchase_events` and `pending_purchases` for the period. Populate `resp.TrackedBankTxIDs`. |
| `internal/inventory/period_summary_test.go` | Cases asserting (a) shape, (b) confirmed + pending + discarded are all listed, (c) deduplication when a bank_tx_id appears in both tables, (d) ordering, (e) empty-period emits `[]`. |

No schema migration — `purchase_events.bank_tx_id` and
`pending_purchases.bank_tx_id` are existing columns, both populated by
the receipt worker.

---

## 1. `types.go` — struct update

Current (`internal/inventory/types.go:153-164`):

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
	// TrackedBankTxIDs is every Mercury bank_tx_id HQ has touched for
	// the period, across all states (confirmed in purchase_events,
	// pending/confirmed/discarded in pending_purchases). Consumers diff
	// this against Mercury's own transaction list for the same period
	// to detect "Mercury has it, HQ hasn't ingested it yet" gaps. See
	// sales-processor/docs/payroll-mercury-gap-check.md.
	TrackedBankTxIDs   []string          `json:"tracked_bank_tx_ids"`
	Completeness       CompletenessBlock `json:"completeness"`
}
```

Initialise `TrackedBankTxIDs` to `[]string{}` (never `nil`) so empty
periods render `[]` not `null`.

---

## 2. `handler.go` — new SQL block

Add a new step between step 3 (pending review IDs, `handler.go:1201-1228`)
and step 4 (unlinked line items, `handler.go:1233-1260`). The natural
place is right after step 3, because both queries touch
`pending_purchases`.

> **Interaction with the receipt-gate amendment**
> ([`cogs-hq-receipt-gate.md`](./cogs-hq-receipt-gate.md), Amendment
> section): if the amendment ships first, the `pending_purchases` half
> of the UNION should use the same `COALESCE(event_date, (created_at AT
> TIME ZONE 'America/Chicago')::date)` date expression to stay
> consistent with how the gate decides which pendings belong to the
> period. If the amendment hasn't shipped yet, use `(created_at AT TIME
> ZONE 'America/Chicago')::date` and update both queries together when
> the amendment lands. The SQL below assumes the amendment shipped.

Suggested SQL:

```sql
SELECT DISTINCT bank_tx_id
FROM (
    SELECT bank_tx_id
    FROM purchase_events
    WHERE event_date BETWEEN $1 AND $2

    UNION

    SELECT bank_tx_id
    FROM pending_purchases
    WHERE COALESCE(event_date, (created_at AT TIME ZONE 'America/Chicago')::date)
            BETWEEN $1 AND $2
) AS tracked
ORDER BY bank_tx_id ASC;
```

Notes:
- `UNION` (not `UNION ALL`) is the dedupe — a confirmed pending row
  produces both a `purchase_events` row and keeps its `pending_purchases`
  row, so the same `bank_tx_id` appears in both. `UNION` collapses
  it; explicit `DISTINCT` makes the intent obvious.
- No `mercury_category` filter — this list is for **completeness
  detection**, not COGS aggregation. CubeSmart counts as "HQ has seen
  this"; sales-processor doesn't want to gap-fail on a tx HQ already
  ingested-and-excluded-from-COGS.
- No filter on `confirmed_at` / `discarded_at` for `pending_purchases`
  — we want all touched rows.
- Same `$1`/`$2` params as the rest of the handler.

Scan loop (mirrors the `pendingIDs` pattern at `handler.go:1201-1228`):

```go
trackedTxIDs := []string{}
rowsT, err := pool.Query(r.Context(), `...SQL above...`, fromStr, toStr)
if err != nil {
    log.Printf("PeriodSummary tracked-tx-ids query: %v", err)
    writeError(w, http.StatusInternalServerError, "internal_error")
    return
}
defer rowsT.Close()
for rowsT.Next() {
    var id string
    if err := rowsT.Scan(&id); err != nil {
        log.Printf("PeriodSummary tracked-tx-ids scan: %v", err)
        writeError(w, http.StatusInternalServerError, "internal_error")
        return
    }
    trackedTxIDs = append(trackedTxIDs, id)
}
if err := rowsT.Err(); err != nil {
    log.Printf("PeriodSummary tracked-tx-ids rows.Err: %v", err)
    writeError(w, http.StatusInternalServerError, "internal_error")
    return
}
```

Then in the response struct literal (`handler.go:1262-1274`), add:

```go
TrackedBankTxIDs: trackedTxIDs,
```

---

## 3. Tests — `period_summary_test.go`

Add to `internal/inventory/period_summary_test.go`:

1. **Shape**: response includes `tracked_bank_tx_ids` as a non-nil array.
2. **All states listed**:
   - Insert a `purchase_events` row (confirmed) for tx A.
   - Insert a `pending_purchases` row (untouched — confirmed_at/discarded_at NULL) for tx B.
   - Insert a `pending_purchases` row with `discarded_at` set for tx C.
   - Insert a `pending_purchases` row WITH a matching `purchase_events` row (confirm path) for tx D.
   - Assert response contains `[A, B, C, D]` (sorted).
3. **Deduplication**: confirm-path tx D should appear exactly once.
4. **Ordering**: assert ASC lexicographic order.
5. **Empty period**: `tracked_bank_tx_ids` is `[]`, not `null`.
6. **Period boundary (post-amendment)**: extend the existing
   amendment test that asserts `pending_review_ids` filters on
   `COALESCE(event_date, created_at::date)` — verify
   `tracked_bank_tx_ids` uses the same date expression for the
   `pending_purchases` half of the UNION.

---

## Consumer

Once this ships, the sales-processor will fetch Mercury directly for the
pay period, filter to supported kinds (`creditCardTransaction`,
`debitCardTransaction`, `creditCardCredit`, `debitCardCredit` — same set
HQ's `isSupportedKind` accepts at `internal/receipt/mercury.go:77-84`),
and compare against `tracked_bank_tx_ids`. Any Mercury tx not in HQ's
list → fail-fast with a "wait for receipt worker to catch up" message
that lists the missing IDs and dashboard links.

Implementation details: see
[`payroll-mercury-gap-check.md`](./payroll-mercury-gap-check.md).

Until that ships, this field is unused by sales-processor (it'll JSON-decode
silently — no harm).

No version bump or feature flag is required — the field is additive and
the consumer treats it as optional during rollout.
