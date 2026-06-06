# HQ Backend Change Handoff — Narrow `completeness.ready` to "COGS Has No Missing Receipts"

> **Companion docs (all in this directory):**
> - [`cogs-hq-receipt-gate.md`](./cogs-hq-receipt-gate.md) — surfaces unreceipted card txns into pending_purchases (shipped; amendment shipped).
> - [`cogs-hq-category-filter.md`](./cogs-hq-category-filter.md) — Mercury-category allowlist for COGS (shipped).
> - [`cogs-hq-pending-details.md`](./cogs-hq-pending-details.md) — `pending_review_details` array (shipped).
>
> Sales-processor needs **no code changes** for this to engage — it already
> hard-fails on `completeness.ready=false` and renders
> `pending_review_details` as the failure body. HQ just narrows what the
> two fields mean, and sales-processor automatically blocks on fewer
> (the right) rows.

Suggested workflow: feed this spec into `/gsd-quick` from the `hq/` directory.

---

## Problem

`completeness.ready` fires false today whenever ANY pending receipt
exists for the period. In practice that means non-food card swipes
(Amazon, fuel for personal car) and food-vendor card swipes with
already-attached-but-unparseable receipts both block the weekly
payroll run, even though neither represents a data-correctness issue:

- **Non-food pending** (Amazon $14 refund): not in COGS at all.
  Whether the operator triages it this week or next week makes no
  difference to the payroll PDF.
- **Food pending with attached receipt that didn't parse** (Save A
  Lot $19 with subtotal mismatch): the receipt photo IS in HQ; the
  parser just couldn't itemise it. Operator can confirm-with-photo
  at leisure. The bank charge is real and known.
- **Food pending with NO receipt attached** (Restaurant Depot $392):
  this IS a real blocker — there's no proof of purchase to attach
  to the books. The operator needs to either upload the receipt
  photo or accept the bank-only entry (confirm-without-receipt).

The right gate is the third bullet only. Today's logic conflates
all three.

---

## Goal

Two coupled behavioural changes on `/period-summary`:

**(a) Narrow `completeness.ready` so it returns `false` iff:**

```
∃ pending_purchases row p in the period where
    p.mercury_category ∈ cogsAllowlist        -- it's a COGS-category txn
    AND p.reason = 'no_attachment_on_bank_tx' -- no receipt was uploaded
  OR
∃ unlinked line item (existing condition, unchanged)
```

Every other pending case stops blocking.

**(b) Auto-include the non-blocking food-category pending rows in
COGS** so the PDF reflects them at their bank totals — by vendor —
without waiting on operator triage:

```
cogs_excl_tax = SUM(purchase_events.line_items)
              + SUM(pending_purchases.bank_total
                    WHERE in period, food category,
                          receipt IS attached but unparsed,
                          not confirmed/discarded)

by_vendor    = same UNION, grouped by vendor (case-insensitive match
               against vendors.name; unmatched names get their own row)
```

Net effect on this morning's failure scenario:

| Row | Today | After (a) | After (a)+(b) |
|---|---|---|---|
| RD pending, no receipt, food | blocks | **still blocks** | **still blocks** |
| Save A Lot pending, parse-failed receipt, food | blocks | runs (not blocking) | runs + **\$19.28 in COGS** |
| Amazon pending, no receipt, non-food | blocks | runs (not blocking) | runs (not in COGS) |

Sales-processor needs no code changes — its existing fail-fast path
keeps working, and the PDF already reads `cogs_excl_tax` + `by_vendor`
straight from this response.

---

## Scope

Smallest possible change with two touch-points: a new column on
`pending_purchases` so the WHERE clause has something to filter on,
and a tightened pending-IDs query.

| File | What changes |
|---|---|
| `internal/db/migrations/00XX_mercury_category_on_pending_purchases.sql` *(new)* | `ALTER TABLE pending_purchases ADD COLUMN mercury_category TEXT`. |
| `internal/receipt/worker.go` | Add a refresh pass for `pending_purchases.mercury_category` mirroring the existing `purchase_events` pass (`worker.go:80-95`). Set the column on `insertPendingPurchase` insert. |
| `internal/inventory/handler.go` | (a) Tighten the pending-IDs query in `PeriodSummaryHandler` (around `handler.go:1201-1228`). (b) UNION eligible-and-unblocked pending into the COGS aggregate (`handler.go:1118-1142`). (c) UNION eligible-and-unblocked pending into `by_vendor` (`handler.go:1144-1195`). |
| `internal/inventory/period_summary_test.go` | Coverage on the four blocking cases (food+no-receipt, food+receipt, non-food+no-receipt, non-food+receipt) plus the rolled-into-COGS aggregate/by_vendor cases. |

No change to `cogs_excl_tax` / `by_vendor` / `pending_review_details`
shape. No change to sales-processor. No change to `completeness.Ready`
construction — the SQL change is enough because the result feeds
through the same `len(pendingIDs) == 0 && len(unlinkedIDs) == 0`
logic untouched.

---

## 1. Migration — `mercury_category` on `pending_purchases`

New file: `internal/db/migrations/00XX_mercury_category_on_pending_purchases.sql`.

Mirror migration 0065 (`0065_mercury_category_on_purchase_events.sql`):

```sql
-- +migrate Up
ALTER TABLE pending_purchases
  ADD COLUMN mercury_category TEXT;

COMMENT ON COLUMN pending_purchases.mercury_category IS
  'Mercury''s custom category for the bank transaction at ingest/refresh time. '
  'Cached so /period-summary can decide whether a pending row blocks payroll '
  '(COGS-category rows without receipts block; others don''t). '
  'See cogs-hq-pending-in-cogs.md.';

-- +migrate Down
ALTER TABLE pending_purchases
  DROP COLUMN mercury_category;
```

Nullable: NULL means "Mercury hadn't classified yet at insert + last
refresh". The new WHERE clause treats NULL as not-in-allowlist (per
Postgres `= ANY` semantics), which means a NULL-category no-receipt
pending row does NOT block — consistent with the principle that
uncategorised txns are operator-triage chores, not data blockers.

---

## 2. `worker.go` — populate + refresh

### 2a. Insert path

`insertPendingPurchase` (around `worker.go:333-382`) — extend the
INSERT column list to include `mercury_category`:

```go
_, err = pool.Exec(ctx,
    `INSERT INTO pending_purchases
     (bank_tx_id, bank_total, vendor, event_date, tax, total, items, reason, receipt_url, mercury_category)
     VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
     ON CONFLICT DO NOTHING`,
    tx.ID,
    tx.Amount,
    vendor,
    nullableString(eventDate),
    nullableFloat64(summary.Tax),
    nullableFloat64(summary.Total),
    itemsJSON,
    nullableString(reason),
    nullableString(receiptURL),
    nullableMercuryCategory(tx),   // new
)
```

Where `nullableMercuryCategory(tx)` returns `tx.CategoryData.Name`
when non-nil, else `sql.NullString{}` (or whatever idiom the file
already uses for `tax`/`total`). Mirror the safety of the existing
`purchase_events` INSERT (`worker.go:272-292`).

### 2b. Refresh pass

After the existing `purchase_events` mercury_category refresh
(`worker.go:80-95`), add the analogous pass for pending:

```go
if tx.CategoryData != nil {
    _, refreshErr := cfg.Pool.Exec(ctx,
        `UPDATE pending_purchases
         SET mercury_category = $1
         WHERE bank_tx_id = $2
           AND (mercury_category IS DISTINCT FROM $1)`,
        tx.CategoryData.Name, tx.ID)
    if refreshErr != nil {
        log.Printf("receipt worker: refresh pending_purchases.mercury_category for tx %s: %v (continuing)", tx.ID, refreshErr)
    }
}
```

`IS DISTINCT FROM` keeps it idempotent and cheap on the no-op case.
Catches the race where a row was created before the classify
pipeline assigned a category — next worker poll backfills it.

---

## 3. `handler.go` — three query updates

### 3a. Tighten the pending query (gate narrowing)


Current pending-IDs query (`handler.go:1201-1228`, post-amendment +
post-pending-details):

```sql
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
         created_at
```

After — add two AND clauses to narrow the result to *blocking*
pending rows only (COGS-category + no receipt):

```sql
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
  AND mercury_category = ANY($3)                          -- NEW: COGS-category only
  AND reason = 'no_attachment_on_bank_tx'                 -- NEW: no receipt uploaded
ORDER BY COALESCE(event_date, (created_at AT TIME ZONE 'America/Chicago')::date),
         created_at
```

The `cogsAllowlist` parameter (`$3`) is already in scope — the COGS
aggregate query and `by_vendor` query both already take it. Pass it
through to this query the same way.

That's the entire behavioural change. `Completeness.Ready` is
computed from `len(pendingIDs)` unchanged — it just sees fewer
(the right) rows.

Non-blocking pending rows (food with parse-failed receipt,
non-food with no receipt, NULL category) are no longer returned
in `pending_review_ids` or `pending_review_details` on this
endpoint. They still exist in the table and still appear in the
operator's Inventory UI via the separate
`ListPendingPurchasesHandler` (`/api/v1/inventory/purchases/pending`)
which is unchanged.

### 3b. UNION eligible pending into the COGS aggregate

Current aggregate (`handler.go:1118-1142`) sums confirmed
`purchase_events.line_items` only. After this change it adds the
food-category, has-attached-receipt-but-unparsed, not-yet-triaged
pending rows at `ABS(bank_total)`:

```sql
WITH events AS (
    SELECT id, tax
    FROM purchase_events
    WHERE event_date BETWEEN $1 AND $2
      AND mercury_category = ANY($3)
),
lines AS (
    SELECT ROUND(COALESCE(SUM(pli.quantity * pli.price), 0)::numeric, 2) AS total
    FROM purchase_line_items pli
    WHERE pli.purchase_event_id IN (SELECT id FROM events)
),
pending AS (
    SELECT ROUND(COALESCE(SUM(ABS(bank_total)), 0)::numeric, 2) AS total,
           COUNT(*)                                              AS event_count
    FROM pending_purchases
    WHERE COALESCE(event_date, (created_at AT TIME ZONE 'America/Chicago')::date)
            BETWEEN $1 AND $2
      AND confirmed_at IS NULL
      AND discarded_at IS NULL
      AND mercury_category = ANY($3)
      AND reason != 'no_attachment_on_bank_tx'   -- skip the blocking ones
),
event_tax AS (
    SELECT COALESCE(SUM(tax), 0)::numeric AS total FROM events
)
SELECT
    (SELECT total FROM lines) + (SELECT total FROM pending)                       AS cogs_excl_tax,
    (SELECT total FROM lines) + (SELECT total FROM pending)
        + (SELECT total FROM event_tax)                                           AS cogs_incl_tax,
    (SELECT COUNT(*) FROM events) + (SELECT event_count FROM pending)             AS event_count
```

Notes:
- **`ABS(bank_total)`**: Mercury bank totals are negative for outgoing
  spend. The line-items sum is positive. Normalise to positive at the
  pending boundary so `cogs_excl_tax` stays positive across the union.
- **Tax**: pending rows contribute `bank_total` to both
  `cogs_excl_tax` and `cogs_incl_tax` identically. Tax separation
  requires a parsed receipt; for parse-failed-but-attached pending
  we assume tax is baked into `bank_total`. ~5% inaccuracy per
  pending row vs. excluding the row entirely.
- **`reason != 'no_attachment_on_bank_tx'`**: blocking pending rows
  (food + no receipt) don't roll into COGS. They'd be moot anyway
  since `Ready=false` halts sales-processor before the PDF renders,
  but excluding them keeps the data model honest: "pending without
  receipt" is a different state from "pending with receipt awaiting
  itemisation".

### 3c. UNION eligible pending into `by_vendor`

Current `by_vendor` query (`handler.go:1144-1195`) GROUPs by
`v.id, v.name` over confirmed events. To roll pending into the same
rows, match `pending_purchases.vendor` (free text) against
`vendors.name` case-insensitively + trimmed. Unmatched names go into
their own row with `vendor_id = ''`.

```sql
WITH confirmed AS (
    -- Existing by-vendor query body, unchanged.
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
                  AND pe2.event_date BETWEEN $1 AND $2
                  AND pe2.mercury_category = ANY($3)),
              0
            )::numeric,
          2
        )                                                                         AS total_incl_tax,
        COUNT(DISTINCT pe.id)                                                     AS trip_count
    FROM purchase_events pe
    JOIN vendors v                    ON v.id = pe.vendor_id
    LEFT JOIN purchase_line_items pli ON pli.purchase_event_id = pe.id
    WHERE pe.event_date BETWEEN $1 AND $2
      AND pe.mercury_category = ANY($3)
    GROUP BY v.id, v.name
),
pending_eligible AS (
    -- Pending rows that should roll into COGS.
    SELECT id, bank_total, vendor
    FROM pending_purchases
    WHERE COALESCE(event_date, (created_at AT TIME ZONE 'America/Chicago')::date)
            BETWEEN $1 AND $2
      AND confirmed_at IS NULL
      AND discarded_at IS NULL
      AND mercury_category = ANY($3)
      AND reason != 'no_attachment_on_bank_tx'
),
pending_matched AS (
    SELECT
        v.id::text                                            AS vendor_id,
        v.name                                                AS vendor_name,
        ROUND(SUM(ABS(pe.bank_total))::numeric, 2)            AS total_excl_tax,
        ROUND(SUM(ABS(pe.bank_total))::numeric, 2)            AS total_incl_tax,
        COUNT(*)                                              AS trip_count
    FROM pending_eligible pe
    JOIN vendors v ON LOWER(TRIM(v.name)) = LOWER(TRIM(pe.vendor))
    GROUP BY v.id, v.name
),
pending_unmatched AS (
    SELECT
        ''::text                                                                  AS vendor_id,
        COALESCE(NULLIF(TRIM(pe.vendor), ''), '(unknown vendor)')                 AS vendor_name,
        ROUND(SUM(ABS(pe.bank_total))::numeric, 2)                                AS total_excl_tax,
        ROUND(SUM(ABS(pe.bank_total))::numeric, 2)                                AS total_incl_tax,
        COUNT(*)                                                                  AS trip_count
    FROM pending_eligible pe
    WHERE NOT EXISTS (
        SELECT 1 FROM vendors v
        WHERE LOWER(TRIM(v.name)) = LOWER(TRIM(pe.vendor))
    )
    GROUP BY pe.vendor
)
SELECT vendor_id, vendor_name,
       SUM(total_excl_tax)  AS total_excl_tax,
       SUM(total_incl_tax)  AS total_incl_tax,
       SUM(trip_count)::int AS trip_count
FROM (
    SELECT vendor_id, vendor_name, total_excl_tax, total_incl_tax, trip_count FROM confirmed
    UNION ALL
    SELECT vendor_id, vendor_name, total_excl_tax, total_incl_tax, trip_count FROM pending_matched
    UNION ALL
    SELECT vendor_id, vendor_name, total_excl_tax, total_incl_tax, trip_count FROM pending_unmatched
) combined
GROUP BY vendor_id, vendor_name
ORDER BY total_excl_tax DESC, vendor_name ASC;
```

The outer `GROUP BY` collapses confirmed + matched-pending into one
row per real vendor (Restaurant Depot's $1842 confirmed + $19 Save A
Lot pending → one row at the per-vendor total). Unmatched pending
stays separate with `vendor_id = ''`.

---

## 4. Tests — `period_summary_test.go`

The 2×2 truth table on (mercury_category ∈ allowlist) × (reason ==
no_attachment):

| Case | Category | Reason | Blocks payroll? | Rolls into COGS? |
|---|---|---|---|---|
| A | food | `no_attachment_on_bank_tx` | **yes** (only blocker) | no |
| B | food | `Receipt could not be parsed automatically` | no | **yes, at `bank_total`** |
| C | non-food | `no_attachment_on_bank_tx` | no | no |
| D | non-food | parse-failed | no | no |

Add explicit cases for each, asserting both columns. Plus:

5. **NULL `mercury_category`** + `no_attachment_on_bank_tx`: NOT
   blocking, NOT in COGS (NULL fails `= ANY` for both queries —
   consistent with "uncategorised is operator chore not data
   blocker").
6. **Date filter still applies**: blocking row outside period →
   not returned. Pre-existing invariant; pin it.
7. **`pending_review_details` parity**: when case A row is the only
   pending, `pending_review_details` has exactly one entry with
   the same id, and `pending_review_ids` matches.
8. **Case B by_vendor match**: insert one confirmed RD event ($100)
   + one case-B Save A Lot pending row ($19, vendor matches
   `vendors.name` case-insensitively). Assert
   `cogs_excl_tax == 119`, RD shows $100 in by_vendor, Save A Lot
   shows $19 in by_vendor — both with a real `vendor_id`.
9. **Case B by_vendor unmatched**: insert one case-B pending row
   with `vendor = 'Brand New Vendor not in vendors table'`.
   Assert by_vendor has a row with `vendor_id == ""` and
   `vendor_name == "Brand New Vendor…"`.
10. **Vendor name fuzz**: insert case-B pending with `vendor = 'save
    a lot '` (lowercase, trailing space) against `vendors.name =
    'Save A Lot'`. Assert it joins, no duplicate row.

---

## Out of scope

- **Tax separation for pending rows.** We use `bank_total` for both
  `cogs_excl_tax` and `cogs_incl_tax`. ~5% inaccuracy on food-cost
  ratio per pending row, but the alternative is excluding the row
  entirely. Operator confirming the receipt later flips the row
  from `bank_total` to parsed `total + tax`, which is exact — the
  next weekly run has the right number.
- **Auto-creating vendors** from unmatched pending names. They
  appear in `by_vendor` with `vendor_id=""` — operator can promote
  to a real `vendors` row via the Inventory UI if desired.
- **Splitting `completeness` into `cogs_ready` + `audit_ready`.**
  Considered for clarity, but with this narrowing the existing
  single field captures the right semantics; an additional field
  would have no consumer.

---

## Verification after deploy

```bash
# 1. With a known mix in the period — say, the failure from this morning:
#    AMAZON pending (non-food), RESTAURANT DEPOT pending (food, no receipt),
#    Save A Lot pending (food, parse-failed receipt)
curl -s -H "Authorization: Bearer $HQ_INVENTORY_SERVICE_TOKEN" \
  "$HQ_BASE_URL/api/v1/inventory/period-summary?from=2026-05-25&to=2026-05-31" \
  | python3 -m json.tool | grep -E '(ready|pending_review_ids|pending_review_details)'

# Expected after this change ships:
#   "ready": false,
#   "pending_review_ids": ["<RD's id only>"],
#   "pending_review_details": [{..."Restaurant Depot"...}],
#   "cogs_excl_tax": <previous total + $19.28 Save A Lot pending>,
#   "by_vendor": [..., {"vendor_name": "Save A Lot", "total_excl_tax": 19.28, ...}, ...]
# Amazon stays out of pending_review_ids AND COGS (non-food).
# Save A Lot stays out of pending_review_ids (has receipt) but IS in COGS.
```

Then re-run sales-processor. The fail-fast banner should list only the
Restaurant Depot row. Confirm-without-receipt (or upload the photo)
on that one row → re-run → payroll completes with a COGS number that
already accounted for Save A Lot's $19.28 (even before the operator
triages Save A Lot's parsing issue).

---

## Consumer

Sales-processor needs **no code changes**. Its existing
`if !summary.Completeness.Ready { log.Fatal(...) }` path continues to
work; it just blocks less often (only when payroll actually can't
proceed).

The fail-fast banner's action text
(`formatHQCompletenessFailure` in `sales-processor/main.go`) says
"Confirm or discard each pending receipt". Once this ships, that
text becomes slightly imprecise — the right action for a blocking
row is "upload the receipt photo, or confirm-without-receipt if you
genuinely don't have one". One-line text tweak; can ship as a
follow-up commit on the sales-processor side at the operator's
convenience. Not blocking.

No version bump or feature flag needed — the change is purely a
narrowing of an existing boolean's truthful conditions.
