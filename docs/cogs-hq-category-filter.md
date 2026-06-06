# HQ Backend Change Handoff — Filter COGS by Mercury Category

> **Companion docs (all in this directory):**
> - [`cogs-hq-receipt-gate.md`](./cogs-hq-receipt-gate.md) — completeness gate for unreceipted card transactions (shipped).
> - [`cogs-hq-undercount-fix.md`](./cogs-hq-undercount-fix.md) — placeholder line items so confirm-without-receipt counts (shipped).
> - [`cogs-hq-handoff.md`](./cogs-hq-handoff.md) — `by_vendor` array on `/period-summary` (pending).
>
> This doc is the structural fix for the CubeSmart-in-COGS class of bug.
> It composes cleanly with `cogs-hq-handoff.md` — if shipped together, the
> `by_vendor` SQL also gets the new category filter.

Suggested workflow: feed this spec into `/gsd-quick` from the `hq/` directory.

---

## Problem

HQ's `purchase_events` table currently conflates two distinct concepts:

1. *"I have a receipt I want to track"* — useful for bookkeeping, applies to
   storage rent, fuel, office supplies, food.
2. *"This is a Cost of Goods Sold expense"* — only food vendors for a
   restaurant.

Today they're the same row. The receipt worker ingests every card
transaction with an attachment into `purchase_events`, and the
`/api/v1/inventory/period-summary` handler aggregates every event into
`cogs_excl_tax`. So a CubeSmart storage rental shows up as $251 of food
cost on the weekly payroll PDF.

The operator's only escape is to manually discard non-food events in the
HQ Inventory dashboard each week. That's toil, and a single mis-click
silently corrupts the food-cost ratio.

The categorization knowledge **already exists upstream**: the
sales-processor's classify pipeline (see `docs/classification.md`)
writes Mercury's `categoryData` field on every card transaction via
Claude. HQ just needs to read it.

## Solution

Cache Mercury's category on each `purchase_events` row, then filter the
COGS aggregate to a configurable allowlist. CubeSmart (Mercury category:
"Rent & Utilities") gets stored as a `purchase_event` like today — so
the receipt stays searchable for bookkeeping — but doesn't roll up into
the COGS numerator because its category isn't on the allowlist.

Single source of truth: Mercury holds the category, HQ caches it, the
sales-processor consumes the filtered aggregate. No new state machine,
no new pipeline — one column, one `WHERE` clause, one decode-field
change in the worker.

## Out of scope (Phase 2)

- **Operator UX for categories**: surfacing `mercury_category` in the HQ
  Inventory dashboard so the operator can see at a glance which events
  did/didn't count toward COGS. UI-only, can ship later.
- **Manual override**: a `cogs_override BOOLEAN` column letting the
  operator force-include or force-exclude an event regardless of
  Mercury's category. Solves the "Mercury was wrong and I can't easily
  fix it in Mercury" case. Defer until that case is felt.
- **Backfilling historical events** beyond the natural re-sync window.
  See "Migration order" below — the receipt worker's existing 14-day
  lookback handles recent events automatically.

---

## Scope

Single behavioral change with four touch-points: one schema migration,
one decode change, one INSERT change, one new UPDATE pass in the
existing worker loop, and a filter clause on the period-summary handler.

### Files to change

| File | What changes |
|---|---|
| `internal/db/migrations/00XX_mercury_category_on_purchase_events.sql` *(new)* | `ALTER TABLE purchase_events ADD COLUMN mercury_category TEXT`. |
| `internal/receipt/types.go` | Decode `categoryData` from Mercury's transaction response. |
| `internal/receipt/worker.go` | Pass `mercury_category` through `createPurchaseEvent`. Add a re-sync pass that UPDATEs the column on existing events visible in the current poll. |
| `internal/inventory/handler.go` | `PeriodSummaryHandler` gains an allowlist filter on the events CTE. |
| `cmd/server/main.go` | Load `HQ_COGS_CATEGORY_ALLOWLIST` env var, default `"COGS"`, pass to `PeriodSummaryHandler`. |
| `internal/inventory/period_summary_test.go` | Existing fixtures get `mercury_category='COGS'`; new test cases assert the filter excludes non-allowlisted rows. |

No new background job — the existing `internal/receipt` worker (already
running every 6h) gets a small extension to re-sync categories on rows
it sees during its poll. Avoids a parallel scheduler.

---

## 1. Migration — add the column

New file `internal/db/migrations/00XX_mercury_category_on_purchase_events.sql`:

```sql
-- +goose Up
BEGIN;

-- Mercury's categoryData.name at the time HQ ingested the receipt.
-- Nullable because: (a) existing rows pre-date this column, (b) future
-- rows where Mercury hasn't been categorized yet by sales-processor's
-- classify pipeline. The receipt worker re-syncs the column on every
-- poll for events within its lookback window (14 days), so NULLs
-- self-heal as Mercury catches up.
ALTER TABLE purchase_events
  ADD COLUMN mercury_category TEXT;

COMMIT;

-- +goose Down
BEGIN;

ALTER TABLE purchase_events
  DROP COLUMN mercury_category;

COMMIT;
```

Use the next migration number in sequence (look at the highest existing
`NNNN_` prefix and add one).

No index. The `purchase_events` table is small (10s–100s of rows per
month) and the existing `event_date` index already narrows the
period-summary scan; an additional index on `mercury_category` would be
wasted cost.

---

## 2. `types.go` — decode `categoryData`

Current `MercuryTransaction` struct (`internal/receipt/types.go:10-26`)
omits `categoryData`. Add it:

```go
type MercuryTransaction struct {
    ID              string                  `json:"id"`
    Amount          float64                 `json:"amount"`
    BankDescription string                  `json:"bankDescription"`
    Status          string                  `json:"status"`
    Kind            string                  `json:"kind"`
    Attachments     []Attachment            `json:"attachments"`
    Note            string                  `json:"note"`
    CreatedAt       string                  `json:"createdAt"`
    CategoryData    *MercuryCategoryData    `json:"categoryData"` // nullable
}

// MercuryCategoryData mirrors Mercury's per-transaction categoryData
// field. Set by the sales-processor classify pipeline (Claude) via
// PATCH /transaction/{id}. Null until classified.
type MercuryCategoryData struct {
    ID   string `json:"id"`
    Name string `json:"name"`
}
```

Other Mercury fields (`mercuryCategory` enum, `merchant`, `glAllocations`)
can stay un-decoded — none are needed for this change.

---

## 3. `worker.go` — write at ingest, refresh on every poll

Two changes inside `internal/receipt/worker.go`.

### 3a. Pass category through `createPurchaseEvent`

Current INSERT (`worker.go:257-262`):

```go
err = dbTx.QueryRow(ctx,
    `INSERT INTO purchase_events (vendor_id, bank_tx_id, event_date, tax, total, receipt_url)
     VALUES ($1, $2, $3, $4, $5, $6)
     RETURNING id`,
    vendorID, tx.ID, eventDate, summary.Tax, summary.Total, nullableString(receiptURL),
).Scan(&eventID)
```

After:

```go
var mercuryCategory string
if tx.CategoryData != nil {
    mercuryCategory = tx.CategoryData.Name
}

err = dbTx.QueryRow(ctx,
    `INSERT INTO purchase_events (vendor_id, bank_tx_id, event_date, tax, total, receipt_url, mercury_category)
     VALUES ($1, $2, $3, $4, $5, $6, $7)
     RETURNING id`,
    vendorID, tx.ID, eventDate, summary.Tax, summary.Total, nullableString(receiptURL), nullableString(mercuryCategory),
).Scan(&eventID)
```

The same change applies to `ConfirmPendingPurchaseHandler` in
`internal/inventory/handler.go` — its INSERT into `purchase_events`
(around line 708) also needs the new column. The pending row already
has `bank_tx_id`; look up Mercury's category via a lightweight client
call before INSERT, or accept it on the request body if the UI already
knows it.

> **Implementation note**: the pending-purchase confirm path runs less
> often than the worker. If it's awkward to thread Mercury through the
> handler, leave the INSERT writing `mercury_category = NULL` for now —
> the worker's re-sync pass (3b) will populate it on the next 6-hour
> tick.

### 3b. Re-sync category on existing rows during the normal poll

The worker's main loop iterates `txns` returned by `FetchTransactions`.
Today it only acts on transactions HQ hasn't seen before (uses the
bank_tx_id uniqueness check). Add a parallel pass that updates
`mercury_category` for transactions HQ *has* seen — so a category set
by the classify pipeline a day after the event arrives gets picked up
on the next worker tick.

Inside the main per-transaction loop, before (or after) the
attachment-driven branch, add:

```go
// Refresh mercury_category on existing events so values set by the
// sales-processor classify pipeline (run async, weekly or nightly)
// propagate into HQ without a separate scheduler.
if tx.CategoryData != nil {
    _, refreshErr := cfg.Pool.Exec(ctx,
        `UPDATE purchase_events
         SET mercury_category = $1
         WHERE bank_tx_id = $2
           AND (mercury_category IS DISTINCT FROM $1)`,
        tx.CategoryData.Name, tx.ID)
    if refreshErr != nil {
        log.Printf("receipt worker: refresh mercury_category for tx %s: %v (continuing)", tx.ID, refreshErr)
    }
}
```

`IS DISTINCT FROM` means UPDATE is a no-op when the value already
matches (cheap, idempotent). The worker's existing 14-day lookback
window (`WorkerConfig.LookbackDays`, default 14) bounds the work. For
events older than 14 days, mercury_category stays at whatever was last
written.

If you want to widen the re-sync window beyond the natural 14-day
lookback (e.g., to retroactively classify older events when the
allowlist changes), bump `LookbackDays` or add a one-off backfill task
— neither is required for this change.

---

## 4. `handler.go` — filter the COGS aggregate

Current aggregate query
(`internal/inventory/handler.go:1122-1136`):

```sql
WITH events AS (
    SELECT id, tax
    FROM purchase_events
    WHERE event_date BETWEEN $1 AND $2
),
lines AS (
    SELECT ROUND(COALESCE(SUM(pli.quantity * pli.price), 0)::numeric, 2) AS total
    FROM purchase_line_items pli
    WHERE pli.purchase_event_id IN (SELECT id FROM events)
)
SELECT
    (SELECT total FROM lines)                         AS cogs_excl_tax,
    (SELECT total FROM lines) + COALESCE(SUM(tax), 0) AS cogs_incl_tax,
    COUNT(*)                                          AS event_count
FROM events
```

After — add `AND mercury_category = ANY($3)` to the events CTE:

```sql
WITH events AS (
    SELECT id, tax
    FROM purchase_events
    WHERE event_date BETWEEN $1 AND $2
      AND mercury_category = ANY($3)
),
...
```

`$3` is the allowlist passed in from the handler — `[]string{"COGS"}`
for v1. Events whose `mercury_category` is NULL **do not match** any
allowlist value (Postgres `ANY` against NULL returns NULL, not true).
That's the correct conservative behavior — uncategorized events stay
out of COGS until Mercury (and HQ's re-sync) catches up.

Pass the allowlist through the handler signature:

```go
func PeriodSummaryHandler(pool *pgxpool.Pool, cogsAllowlist []string) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        // ...
        err := pool.QueryRow(r.Context(), `...sql above...`, fromStr, toStr, cogsAllowlist).Scan(...)
        // ...
    }
}
```

**Apply the same filter** to the `by_vendor` query (from
`cogs-hq-handoff.md`, if shipped). The events CTE pattern carries over
identically.

`purchase_event_count` should also reflect the filtered set so its
meaning stays consistent ("how many COGS events did I have this
period"). The query above already does this via `COUNT(*) FROM events`.

---

## 5. `cmd/server/main.go` — wire the allowlist from env

Where `PeriodSummaryHandler` is registered (currently
`cmd/server/main.go` around line 340):

```go
allowlist := strings.Split(getenvDefault("HQ_COGS_CATEGORY_ALLOWLIST", "COGS"), ",")
for i := range allowlist {
    allowlist[i] = strings.TrimSpace(allowlist[i])
}
// ...
r.Get("/api/v1/inventory/period-summary", inventory.PeriodSummaryHandler(pool, allowlist))
```

(`getenvDefault` is a tiny helper — define it if HQ doesn't have one
yet.)

Default `"COGS"` matches the canonical category name Mercury uses (and
that sales-processor's classify pipeline writes — see
`internal/classify/categories.go` over there). To include additional
buckets later — say, "Inventory & Materials" — set:

```
HQ_COGS_CATEGORY_ALLOWLIST=COGS,Inventory & Materials
```

No code change.

---

## 6. Tests — `period_summary_test.go`

Existing test fixtures insert `purchase_events` rows directly with
hardcoded fields. After this change those rows would default to
`mercury_category=NULL` and therefore disappear from COGS — every
existing test would fail.

Fix the fixtures: set `mercury_category='COGS'` on every
`purchase_events` row inserted by the test helpers. One-line per insert.

Then add new cases:

1. **Allowlist filter excludes non-COGS rows**: insert two events — one
   with `mercury_category='COGS'`, one with `mercury_category='Rent & Utilities'`.
   Call `/period-summary` with the default allowlist `["COGS"]`. Assert
   only the COGS event contributes to `cogs_excl_tax` and
   `purchase_event_count`.

2. **NULL category is excluded by default**: insert an event with
   `mercury_category=NULL`. Assert it's not counted (Postgres
   `ANY(NULL)` semantics).

3. **Custom allowlist via injection**: call the handler with a
   two-element allowlist `["COGS", "Other / Needs Review"]`. Assert
   events from both categories are included.

4. **Worker re-sync updates the column**: simulate a Mercury poll where
   a previously-seen tx now has `categoryData.name = 'COGS'`. After the
   worker pass, assert the `purchase_events` row's `mercury_category`
   matches.

---

## Consumer side — sales-processor (informational)

**No code change required.** sales-processor's `service/external/hq.go`
already decodes whatever `/period-summary` returns. After this ships:

- The weekly payroll PDF will only count COGS-categorized events
  toward the food-cost ratio.
- The `by_vendor` block (when `cogs-hq-handoff.md` also ships) will
  only show vendors whose Mercury category is on the allowlist.
- Receipts for non-COGS spend (CubeSmart storage, gas, office
  supplies) stay in HQ for bookkeeping — they're just invisible to the
  COGS aggregate.

**Recommended companion change** in the sales-processor environment
(not part of this HQ doc, but completes the loop): cron a nightly
classify run so the deferral window between "card swipe" and "Mercury
has a category" stays at hours rather than days. Suggested entry:

```cron
# Run nightly at 6:00 AM local — catches yesterday's card spend
# before the next HQ receipt-worker tick.
0 6 * * * cd /path/to/sales-processor && \
  task classify FROM=$(date -v-1d +\%Y-\%m-\%d) TO=$(date -v-1d +\%Y-\%m-\%d)
```

(macOS `date -v-1d` syntax; Linux is `date -d "yesterday"`.)

Without the nightly cron, the deferral window is one week (until the
next `task payroll`). With it, the window collapses to whatever's left
between the nightly classify run and HQ's next 6-hour worker tick — at
worst, ~6 hours.

---

## Migration order

Each step is independently deployable. The full sequence:

1. **HQ migration** (`00XX_mercury_category_on_purchase_events.sql`) —
   adds the nullable column. Zero behavioral change at this point;
   every existing row has `mercury_category = NULL`.
2. **HQ worker** (`worker.go` + `types.go` changes) — new events get
   `mercury_category` written at INSERT, existing recent events get
   updated on the worker's normal 6h tick.
3. **HQ handler + main.go wiring** — the filter goes live. **At this
   moment** the COGS aggregate drops to only allowlist-categorized
   events. Any unclassified event silently leaves COGS.
4. **(Optional) sales-processor cron** — nightly classify keeps the
   deferral window small.

The window between step 3 going live and the worker fully populating
categories (step 2 has had ~6 hours to run) is when you might see
*temporary* under-reporting of COGS. To avoid this, ship 1 + 2
together, let the worker run for a day to backfill, then ship 3.

After deploy, verification:

```bash
curl -s -H "Authorization: Bearer $HQ_INVENTORY_SERVICE_TOKEN" \
  "https://<hq-host>/api/v1/inventory/period-summary?from=2026-05-25&to=2026-05-31" \
  | python3 -m json.tool
```

`cogs_excl_tax` should drop to only food-vendor totals (CubeSmart's
$251 gone, leaving just the actual food purchases). `by_vendor` (once
that ships) will be similarly trimmed.

---

## Why this is the right shape

- **Single source of truth.** Mercury holds the category; HQ caches it;
  sales-processor reads the filtered aggregate. Each layer owns exactly
  one concern.
- **Audit trail preserved.** Receipts for non-food spend still live in
  `purchase_events` with their photos and parsed line items — useful
  for bookkeeping and search. Filtering happens at aggregation, not at
  ingest.
- **Reversible.** The allowlist is an env var. If the filter excludes
  too much (e.g., you want "Inventory & Materials" in COGS too), edit
  the env, restart, done. No schema or code change.
- **No new pipeline.** The existing 6-hour worker poll handles
  re-sync. No goroutine scheduler, no cron in HQ.
- **Operator stops being the bottleneck.** Today the only thing
  preventing CubeSmart-in-COGS is the operator dismissing each one
  manually. After this, it's handled by Mercury's category, which
  Claude maintains automatically.
