# HQ Backend Change Handoff — Fix COGS Undercount on "Confirm Without Receipt"

> **Companion docs:**
> - [`cogs-hq-handoff.md`](./cogs-hq-handoff.md) — adds `by_vendor` to `/period-summary`
> - [`cogs-hq-receipt-gate.md`](./cogs-hq-receipt-gate.md) — surfaces unreceipted card transactions in the completeness gate
>
> This doc fixes a bug introduced by `cogs-hq-receipt-gate.md`. Ship after
> that one (or at the same time). Independent of `cogs-hq-handoff.md`.

Suggested workflow: feed this spec into `/gsd-quick` from the `hq/`
directory.

---

## Problem

`cogs-hq-receipt-gate.md` added an "empty-items resolution" path to
`ConfirmPendingPurchaseHandler` (`internal/inventory/handler.go:631+`):
when the operator confirms a Mercury card transaction as "this was
food, no receipt available", HQ creates a `purchase_events` row with
`total = abs(bank_total)`, `tax = 0`, and **no** `purchase_line_items`
rows.

This unblocks the completeness gate but **silently undercounts COGS**.
The `/api/v1/inventory/period-summary` aggregate query
(`internal/inventory/handler.go:1088-1103`) computes
`cogs_excl_tax` as:

```sql
SUM(pli.quantity * pli.price) FROM purchase_line_items
WHERE pli.purchase_event_id IN (events in period)
```

Events without line items contribute **$0** even though
`purchase_events.total` was recorded. Real-world impact: a $200
Restaurant Depot card swipe, triaged as "no receipt available", lands
in the database but vanishes from the food-cost ratio.

The original receipt-gate spec called this "acceptable" — that was
wrong. The operator needs the dollars to count even when itemization
isn't available.

---

## Solution

When the confirm handler takes the empty-items branch, also insert
**one placeholder `purchase_line_items` row** that mirrors the bank
total. The placeholder is linked to a stable seed `purchase_items` row
so it doesn't trip the `unlinked_line_item_ids` completeness check.

Concrete placeholder shape:

| Column | Value |
|---|---|
| `purchase_event_id` | the just-created event |
| `purchase_item_id` | seed UUID — see migration below |
| `description` | `'(no itemized receipt)'` (matches the seed catalog row) |
| `quantity` | `1` |
| `price` | `abs(bank_total)` |
| `is_case` | `false` |

Effect:
- `SUM(qty * price)` for the period now includes `abs(bank_total)`
  for every "confirm without receipt" event → `cogs_excl_tax` is
  correct.
- `purchase_item_id` is non-NULL → does NOT appear in
  `unlinked_line_item_ids` → completeness gate stays clean after
  triage.
- `purchase_events.total = abs(bank_total)` and the line-item total
  now match → consistent.
- Per-vendor totals (once `cogs-hq-handoff.md` ships) sum correctly
  by vendor.

---

## Files to change

| File | What changes |
|---|---|
| `internal/db/migrations/00XX_no_itemized_receipt_seed.sql` *(new)* | Seed `purchase_items` row with a stable UUID and the placeholder description. |
| `internal/inventory/handler.go` (`ConfirmPendingPurchaseHandler` around line 687) | After `INSERT INTO purchase_events ...`, when `emptyResolution` is true, insert one placeholder `purchase_line_items` row referencing the seed. |
| `internal/inventory/period_summary_test.go` | Add a case asserting `cogs_excl_tax` increases by `abs(bank_total)` after an empty-items confirm. |

---

## 1. Migration — seed the placeholder row **and** backfill past events

The migration does two things in one transaction: creates the seed
`purchase_items` row that the new handler code links against, and
backfills placeholder `purchase_line_items` rows for every existing
`purchase_event` that has no line items (i.e. past empty-resolution
confirms made before this fix shipped). Goose runs Up once per
environment, tracks it in `goose_db_version`, and never re-runs — so
this is a clean one-shot operation with no separate script needed.

New file `internal/db/migrations/00XX_no_itemized_receipt_seed.sql`:

```sql
-- +goose Up
BEGIN;

-- 1. Stable seed row referenced by the "confirm without receipt" path in
--    ConfirmPendingPurchaseHandler. Allows purchase_line_items to link to
--    a real purchase_items row even when the operator couldn't itemize the
--    receipt — keeps the completeness gate (unlinked_line_item_ids) clean
--    and lets the COGS aggregate sum the bank total via the standard
--    SUM(quantity * price) path.
INSERT INTO purchase_items (id, description, group_id)
VALUES (
  '00000000-0000-0000-0000-000000000001',
  '(no itemized receipt)',
  NULL
)
ON CONFLICT (description) DO NOTHING;

-- 2. Backfill: every existing purchase_event with no line items was
--    created by the pre-fix empty-resolution path. Insert one placeholder
--    line item per event so SUM(qty * price) finally matches
--    purchase_events.total. Past weekly reports become accurate
--    retroactively on next /period-summary fetch.
INSERT INTO purchase_line_items
  (purchase_event_id, purchase_item_id, description, quantity, price, is_case)
SELECT
  pe.id,
  '00000000-0000-0000-0000-000000000001',
  '(no itemized receipt)',
  1,
  pe.total,
  false
FROM purchase_events pe
LEFT JOIN purchase_line_items pli ON pli.purchase_event_id = pe.id
WHERE pli.id IS NULL
  AND pe.total > 0;

COMMIT;

-- +goose Down
BEGIN;

-- Down removes placeholder line items first (FK constraint requires it),
-- then the seed row. This deletes every placeholder ever created — by
-- the backfill AND by the new handler code path — so rolling back this
-- migration also rolls back the fix's effect on COGS. Intentional.
DELETE FROM purchase_line_items
WHERE purchase_item_id = '00000000-0000-0000-0000-000000000001';

DELETE FROM purchase_items
WHERE id = '00000000-0000-0000-0000-000000000001';

COMMIT;
```

Use whichever migration number is next in sequence (look at
`internal/db/migrations/` for the highest existing `NNNN_` prefix and
add one).

Notes:
- The all-zeros UUID with a `1` in the last position is intentionally
  not random — it's a sentinel value any developer can recognize and
  grep for. (`gen_random_uuid()` would also work but loses the "this
  is special" signal.)
- `group_id` stays NULL — no item_group needed for a placeholder.
  Existing schema allows NULL (`internal/db/migrations/0024_inventory.sql:30`).
- `ON CONFLICT (description) DO NOTHING` because
  `purchase_items.description` is `UNIQUE`. Safe to re-run the seed
  insert independently if ever needed (e.g. partial restore from
  backup).
- The backfill is naturally idempotent via the `LEFT JOIN … WHERE
  pli.id IS NULL` filter: once a placeholder exists for an event, it
  no longer qualifies. Goose only runs the migration once anyway, but
  the SQL is safe regardless.

---

## 2. Handler change — insert the placeholder line item

Current code path (`internal/inventory/handler.go:706-735`):

```go
// Create the real purchase event
var eventID string
err = tx.QueryRow(r.Context(), `
    INSERT INTO purchase_events (vendor_id, bank_tx_id, event_date, tax, total)
    VALUES ($1, $2, $3, $4, $5)
    RETURNING id`,
    vendorID, bankTxID, input.EventDate, eventTax, eventTotal,
).Scan(&eventID)
if err != nil {
    log.Printf("ConfirmPendingPurchase insert event: %v", err)
    writeError(w, http.StatusInternalServerError, "internal_error")
    return
}

if !emptyResolution {
    for _, li := range input.LineItems {
        desc := normalizeItemName(li.Description)
        _, err := tx.Exec(r.Context(), `
            INSERT INTO purchase_line_items
            (purchase_event_id, purchase_item_id, description, quantity, price, is_case)
            VALUES ($1, $2, $3, $4, $5, $6)`,
            eventID, li.PurchaseItemID, desc, li.Quantity, li.Price, li.IsCase,
        )
        if err != nil {
            log.Printf("ConfirmPendingPurchase insert line_item: %v", err)
            writeError(w, http.StatusInternalServerError, "internal_error")
            return
        }
    }
}
```

After — add an `else` branch that writes the placeholder:

```go
if !emptyResolution {
    for _, li := range input.LineItems {
        desc := normalizeItemName(li.Description)
        _, err := tx.Exec(r.Context(), `
            INSERT INTO purchase_line_items
            (purchase_event_id, purchase_item_id, description, quantity, price, is_case)
            VALUES ($1, $2, $3, $4, $5, $6)`,
            eventID, li.PurchaseItemID, desc, li.Quantity, li.Price, li.IsCase,
        )
        if err != nil {
            log.Printf("ConfirmPendingPurchase insert line_item: %v", err)
            writeError(w, http.StatusInternalServerError, "internal_error")
            return
        }
    }
} else {
    // Empty-items resolution: insert a single placeholder line item so
    // the bank total contributes to cogs_excl_tax. Linked to the seed
    // purchase_items row (see migration 00XX_no_itemized_receipt_seed.sql)
    // so unlinked_line_item_ids stays empty.
    const noItemizedReceiptItemID = "00000000-0000-0000-0000-000000000001"
    _, err := tx.Exec(r.Context(), `
        INSERT INTO purchase_line_items
        (purchase_event_id, purchase_item_id, description, quantity, price, is_case)
        VALUES ($1, $2, '(no itemized receipt)', 1, $3, false)`,
        eventID, noItemizedReceiptItemID, eventTotal,
    )
    if err != nil {
        log.Printf("ConfirmPendingPurchase insert placeholder line_item: %v", err)
        writeError(w, http.StatusInternalServerError, "internal_error")
        return
    }
}
```

The constant `noItemizedReceiptItemID` can live as a package-level
`const` if it gets referenced elsewhere (e.g. a future "exclude
placeholders from item-level analytics" query). Inline is fine for now.

---

## 3. Tests — `period_summary_test.go`

Add to the existing test file (`internal/inventory/period_summary_test.go`):

1. **Placeholder line item lands in cogs_excl_tax**: insert a
   `purchase_events` row in the period plus a single placeholder
   `purchase_line_items` row (qty=1, price=$50.00, linked to the seed
   id, description='(no itemized receipt)'). Call `/period-summary`,
   assert `cogs_excl_tax == 50.00`.

2. **Placeholder does NOT trip unlinked_line_item_ids**: same setup
   as #1 but also call the completeness gate. Assert
   `unlinked_line_item_ids` is empty and `ready` is true.

3. **End-to-end via confirm**: insert a `pending_purchases` row with
   `bank_total = 75.00`. POST `/api/v1/inventory/purchases/confirm`
   with empty `line_items`. Then GET `/period-summary` and assert
   `cogs_excl_tax == 75.00` and `purchase_event_count == 1`.

Optional (defensive):

4. **Seed row exists after migration**: query `purchase_items WHERE id
   = '00000000-0000-0000-0000-000000000001'`. Assert exactly one row,
   description `'(no itemized receipt)'`.

---

## Consumer

No change required in the sales-processor. The COGS section's "COGS",
"Tax", "Gross Profit", "Food Cost %", and "Receipts in HQ" lines will
all read correctly once HQ ships this. Same for `by_vendor` totals
once `cogs-hq-handoff.md` is also live — the placeholder line items
get grouped under their vendor like any other line item.

The placeholder rows are deliberately searchable
(`WHERE description = '(no itemized receipt)'`) so future analytics
can call out events that lack itemization without scanning every
purchase. If that signal becomes important enough to surface in the
weekly report, the sales-processor can render a small note in the
`By Vendor` block (out of scope here).

---

## Order of operations

Ship the migration and the handler change in the same PR. On deploy:

1. Goose runs the migration — seeds the placeholder catalog row AND
   backfills line items for every past empty-resolution event in one
   transaction.
2. New server binary starts with the updated `ConfirmPendingPurchase`
   handler. Going forward, every "confirm without receipt" creates a
   placeholder line item inline.

That's it — no separate backfill script to run, no operator step. The
next time sales-processor (or any caller) hits `/period-summary`, the
COGS aggregate reflects the corrected totals retroactively.

**Verification after deploy** — quick smoke test the operator can run
without psql:

```bash
curl -s -H "Authorization: Bearer $HQ_INVENTORY_SERVICE_TOKEN" \
  "https://<hq-host>/api/v1/inventory/period-summary?from=2026-05-25&to=2026-05-31" \
  | python3 -m json.tool
```

`cogs_excl_tax` should be higher than before the deploy if any past
empty-resolution events existed in that period. `completeness.ready`
should remain `true` (placeholders are linked, so they don't trip
`unlinked_line_item_ids`).
