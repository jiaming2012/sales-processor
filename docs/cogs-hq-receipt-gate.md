# HQ Backend Change Handoff — Receipt-Gate Mercury Card Transactions

This document specifies a second change to the **Yumyums HQ backend**
(`/Users/jamal/projects/yumyums/hq/backend`). It's a companion to
[`cogs-hq-handoff.md`](./cogs-hq-handoff.md) (per-vendor breakdown) but
independent — either can ship first.

Suggested workflow: feed the spec below into `/gsd-quick` from the
`hq/` directory.

---

## Problem

Today the HQ receipt worker silently drops every Mercury
card/debit-card transaction that arrives without a file attachment.
That means:

- Cash purchases at Restaurant Depot / Save-A-Lot (never on Mercury at all)
- Card purchases where the operator forgot to attach a receipt photo
- Refunds / corrections that never had a receipt

…all vanish from COGS. The sales-processor's weekly payroll PDF then
shows a food cost % that's wildly understated (e.g. 3.6% on $1.7k in
sales when real food cost is closer to 30%).

The current `completeness.ready` gate only catches failures that
**started with a receipt** — it can't fail on receipts that never
existed.

**Goal:** make the gate fail when Mercury shows card/debit-card
transactions in the period that have not been resolved by HQ — either
(a) a receipt is attached and processed, (b) the operator confirms
"this was food, no receipt available", or (c) the operator dismisses
"this was not food".

The payroll script already fails when `completeness.ready=false`. So
once HQ surfaces unreceipted transactions, the sales-processor side
needs no changes.

> Cash transactions remain invisible by definition (Mercury doesn't see
> them). The mitigation is operator discipline: pay with the business
> card so Mercury catches it. This change ensures every card swipe is
> at least accounted for.

---

## Out of scope (Phase 2)

- **Merchant learning rules** (e.g. "always auto-dismiss `NETFLIX.COM
  USD`"). Without this the operator will have to triage every personal
  / non-food card use the first time it appears. That's a real UX cost
  but a separable feature — design notes at the bottom of this doc.
- **Cash purchase capture**. Manual entry already exists via the
  Inventory dashboard; nothing about that flow changes here.
- **Refunds / negative amounts**. Mercury's `creditCardCredit` /
  `debitCardCredit` kinds are already accepted by `isSupportedKind`;
  they'll show up in pending_purchases too. Triaging them is the
  operator's job for now.

---

## Scope

Single behavioral change with a one-line filter relaxation plus a new
worker branch. Almost all infrastructure is already in place
(`pending_purchases` table, `insertPendingPurchase` helper,
`completeness.ready` gate).

### Files to change

| File | What changes |
|---|---|
| `internal/receipt/mercury.go` | Drop the `len(tx.Attachments) > 0` filter so the worker sees every supported transaction. |
| `internal/receipt/worker.go` | New branch in the main loop: when a transaction has no attachment, call `insertPendingPurchase` with a sentinel reason and empty items/summary. |
| `internal/receipt/worker_test.go` *(new file)* | Cover the new branch + idempotency on rerun. |
| `internal/inventory/period_summary_test.go` | Add a case asserting the gate blocks when an unreceipted pending row exists. |

No database migration required — `pending_purchases` already has the
nullable columns this needs and a unique index on `bank_tx_id`.

---

## 1. `mercury.go` — drop the attachment filter

Current (`internal/receipt/mercury.go:59-69`):

```go
var out []MercuryTransaction
for _, tx := range envelope.Transactions {
    if tx.Status != mercuryStatusSent {
        continue
    }
    if !isSupportedKind(tx.Kind) {
        continue
    }
    if len(tx.Attachments) > 0 {
        out = append(out, tx)
    }
}
```

After:

```go
var out []MercuryTransaction
for _, tx := range envelope.Transactions {
    if tx.Status != mercuryStatusSent {
        continue
    }
    if !isSupportedKind(tx.Kind) {
        continue
    }
    // Attachment-or-not classification is the worker's job now —
    // see worker.go. Both branches need to be tracked so the
    // completeness gate can fail on unreceipted spend.
    out = append(out, tx)
}
```

Note: the existing pagination guard at the top of `FetchTransactions`
(errors when the response hits Mercury's page limit) becomes more
likely to trip after this change — every card swipe now counts toward
that limit, not just the photographed ones. If it starts triggering in
practice, implement cursor pagination. For now leaving the loud error
in place is the right move.

---

## 2. `worker.go` — branch on attachment presence

In the per-transaction loop (around `internal/receipt/worker.go:96`,
just inside the `for _, tx := range txns` block), add an early branch
**before** the existing attachment-download → parse → validate flow:

```go
if len(tx.Attachments) == 0 {
    if routeErr := insertPendingPurchase(
        ctx, cfg.Pool, tx,
        nil,              // items — unknown without receipt
        ReceiptSummary{}, // summary — unknown without receipt
        "",               // receiptURL — none
        "no_attachment_on_bank_tx",
    ); routeErr != nil {
        log.Printf("receipt worker: insertPendingPurchase (no-attachment) for tx %s: %v", tx.ID, routeErr)
    }
    pendingReview++
    continue
}
```

Why this works without further changes:

- `insertPendingPurchase` already uses `ON CONFLICT DO NOTHING` on
  `bank_tx_id`, so the worker can re-poll the same transaction every
  6 hours without duplicating the pending row.
- The helper marshals `nil` items to `[]` JSON (line 297-300 of
  `worker.go`) and the `nullable*` wrappers handle empty `summary`
  fields → `NULL` columns. So the call works as-is with empty inputs.
- The new sentinel `reason="no_attachment_on_bank_tx"` distinguishes
  these rows from existing routing reasons ("Receipt could not be
  parsed automatically", validation messages, etc.) so the operator UI
  can render them differently if desired.

The existing log line at the bottom of the loop (`processed N
transactions, X auto-created, Y pending review, Z already cached`) now
naturally reflects the no-attachment count in `pendingReview`.

---

## 3. Confirm / discard endpoints — handle empty-receipt resolution

The existing `/api/v1/inventory/purchases/confirm` handler
(`internal/inventory/handler.go:631-740`) currently expects a parsed
receipt — vendor, line items, total. For no-attachment rows the
operator has three resolution paths:

| Path | Effect | API call |
|---|---|---|
| **Attach a receipt now** | Operator uploads receipt photo, HQ parses, then confirms with parsed items | New flow: `POST /pending/{id}/attach-receipt` (or extend `/confirm` to accept a `receipt_url` upload). Out of scope for this MVP — operator can manually upload via the existing photo flow then confirm. |
| **Confirm as food, no receipt** | Creates a `purchase_event` with `total=bank_total`, `tax=0`, **no line items**. Loses item-level detail but at least the spend lands in COGS. | Extend `/confirm` to accept a body with `confirm_without_receipt: true` and use `bank_total` as the event total. |
| **Dismiss as not food** | Marks `discarded_at`; no purchase_event created. Already supported via `/discard`. | No change. |

**Minimum change** for the gate to function: extend the confirm
handler so it doesn't error on rows where `items` is empty and
`total` is null. Treat `bank_total` as the source of truth in that
case, and write a single `purchase_event` with no line items.

A `purchase_event` with no `purchase_line_items` means
`cogs_excl_tax` *under-counts* by the unrecorded item total —
acceptable since the operator chose "no receipt available", and the
tax-inclusive total is still captured via `tax=0, total=bank_total`.

If future scope wants stricter behavior, route these to a "needs
itemization later" follow-up bucket. For MVP the lossy version is
fine.

---

## 4. Tests

### `internal/receipt/worker_test.go` (new)

Cover the new branch:

1. **Inserts pending row when no attachment**: feed a mocked
   `MercuryTransaction` with `Attachments=[]`, run the worker step,
   assert `pending_purchases` has one row with
   `reason='no_attachment_on_bank_tx'`,
   `total IS NULL`, `items='[]'`, `bank_total=<tx.Amount>`.
2. **Idempotent on rerun**: run the worker step twice with the same
   transaction, assert exactly one pending row exists.
3. **Coexists with attachment branch**: feed a mixed batch (one with
   attachment that parses successfully, one without). Assert the
   first creates a `purchase_event`, the second creates a
   `pending_purchases` row, neither blocks the other.

### `internal/inventory/period_summary_test.go`

Add a case to the existing test file:

4. **Unreceipted transaction blocks completeness**: insert a
   `pending_purchases` row with
   `reason='no_attachment_on_bank_tx'`, `confirmed_at IS NULL`,
   `discarded_at IS NULL`, in the period. Call
   `/period-summary`. Assert `completeness.ready=false` and the row's
   ID appears in `completeness.pending_review_ids`.

The existing completeness query (`handler.go:1115-1141`) filters on
`confirmed_at IS NULL AND discarded_at IS NULL` regardless of
`reason`, so no query change is needed.

---

## 5. Operator UX note (informational, no implementation required)

After this ships, the HQ Inventory dashboard's pending-review queue
becomes the operator's "did I forget any receipts this week?" UI. The
weekly cadence is:

1. Run the receipt worker (auto, every 6h) — populates pending queue
   from Mercury.
2. Operator opens HQ Inventory → Pending Review at end of week.
3. For each row: attach receipt photo (re-parse + confirm), confirm as
   food without receipt, or dismiss as not food.
4. Run sales-processor — only succeeds when pending queue is empty for
   the period.

**Expected pain on first run**: every personal card use, every gas
station purchase, every subscription renewal will appear in the queue.
The operator dismisses them one at a time. This is the user-accepted
trade-off ("better to triage non-food than to silently miss food").

**Phase 2 — merchant learning rules** (design sketch only, do NOT
implement here):

- New table: `merchant_dismissal_rules` (`pattern TEXT`, e.g. a
  substring or regex against `bank_description`, `created_at`,
  `created_by`)
- When operator dismisses a row, prompt: "Always dismiss transactions
  matching `<bank_description>`?"
- Worker, when inserting a no-attachment row, first checks rules; if
  any match, auto-`discarded_at = now()` with a sentinel `confirmed_by`
  so the dismissal is auditable.
- After ~2 weeks the queue stabilizes to real food vendors only.

---

## Consumer

No change required in the sales-processor — its existing fatal-on-
incomplete behavior already covers this case. After this lands the
fatal message will read something like:

```
HQ COGS data incomplete for 2026-05-25–2026-05-31: 47 receipt(s) pending review, 0 line item(s) unlinked.
```

The 47 will start dropping as the operator triages the queue.

If readability becomes a problem we can later have HQ split
`pending_review_ids` by `reason` and surface the counts separately
(e.g. "12 receipts failed to parse, 35 card transactions with no
receipt"). Not needed for v1 — the operator opens the HQ dashboard
either way.
