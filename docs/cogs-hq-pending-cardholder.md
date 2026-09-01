# HQ Backend Change Handoff — Add `cardholder` to `pending_review_details`

> **Companion docs (all in this directory):**
> - [`cogs-hq-pending-details.md`](./cogs-hq-pending-details.md) — the `pending_review_details` array this change extends (shipped).
> - [`cogs-hq-receipt-gate.md`](./cogs-hq-receipt-gate.md) — completeness gate for unreceipted card txns (shipped).
> - [`cogs-hq-tracked-tx-ids.md`](./cogs-hq-tracked-tx-ids.md) — `tracked_bank_tx_ids` array (shipped).
>
> This doc is independent and purely additive on the response. Safe to ship
> anytime. **There is no urgency:** sales-processor already resolves the
> cardholder itself (see "Already handled on the consumer side" below), so
> this change is an optional consolidation, not a fix for a live gap.

Suggested workflow: feed this spec into `/gsd-quick` from the `hq/` directory.

---

## Problem

When the receipt-completeness gate closes, sales-processor prints a
fail-fast banner listing every unreceipted card purchase so the operator
knows what to chase:

```
Pending receipts:
  - 2026-08-27  SAVE A LOT 3025         $-22.04  (no receipt attached)
```

The operator's next question is always the same: **whose card was this?**
YumYums runs several physical cards, one per person (Jamal, Latanya, …).
Knowing the cardholder turns "hunt through Mercury" into "go ask Jamal for
the Save-A-Lot receipt."

The `pending_review_details` rows already carry `bank_tx_id` — Mercury's
transaction id — but no card attribution. The card behind a purchase is
available on the Mercury transaction itself (`cardId`), and the cardholder
name is one Cards-API join away (`GET /cards` → `nameOnCard`).

---

## Already handled on the consumer side

sales-processor now performs this join client-side and renders:

```
Pending receipts:
  - 2026-08-27  SAVE A LOT 3025         $-22.04  · Jamal Cole  (no receipt attached)
```

See, in this repo:
- `service/external/mercury.go` — `MercuryTransactionLite.CardID` (`json:"cardId"`),
  the `MercuryCard` struct + `ListCards()` (`GET /cards`), and the pure
  `CardholderByBankTx(txns, cards)` join.
- `main.go` — `resolvePendingCardholders(...)` (best-effort; degrades to no
  attribution under `--skip-mercury` or if Mercury is unreachable) and the
  `cardholderByBankTx` parameter on `formatHQCompletenessFailure`.

So the operator-facing outcome already works. The reason to move it into HQ
is ownership and simplification, not capability:

- **Removes a Mercury round-trip from the payroll run.** sales-processor
  currently fetches `/transactions` + `/cards` at gate-failure time purely
  to attribute cards. If HQ ships the field, the consumer can drop that
  join and read `detail.cardholder` directly.
- **Single source of truth.** HQ already ingests these transactions in its
  receipt worker; attributing the card at ingest keeps the mapping next to
  the data instead of re-deriving it downstream.

If HQ never ships this, nothing breaks — the consumer keeps its own join.

---

## Scope

Additive change on `pending_review_details`. Two pieces of work:

1. **Capture the card at ingest.** The receipt worker already reads the
   Mercury transaction; persist its `cardId` onto `pending_purchases`
   (new nullable column `card_id`). Card kinds have it; non-card rows are
   null.
2. **Resolve + emit the cardholder.** On `/period-summary`, join `card_id`
   against Mercury's card list (`GET /cards` → `nameOnCard` / `lastFour`)
   and emit `cardholder` on each detail row.

### Response shape (additive — no breaking change)

```json
"pending_review_details": [
  {
    "id": "3fcf6c3c-…",
    "bank_tx_id": "8caa1ff0-9b39-11f1-bc34-57243dc089c1",
    "vendor": "SAVE A LOT 3025",
    "event_date": "2026-08-27",
    "bank_total": -22.04,
    "reason": "no_attachment_on_bank_tx",
    "cardholder": "Jamal Cole"
  }
]
```

Field semantics:
- `cardholder`: human label for the person who made the purchase.
  Prefer the card's `nameOnCard` (e.g. `"Jamal Cole"`); when the name is
  blank fall back to `"card ••<lastFour>"`; emit `""` when the row has no
  resolvable card (non-card purchase, or the Mercury card list didn't
  contain `card_id`). Never `null` — emit `""` so the consumer can render
  a row unconditionally. This mirrors the `vendor` "" convention.

`pending_review_ids` and the existing detail fields are unchanged.

---

## Files to change (HQ repo)

| File | What changes |
|---|---|
| migration | Add nullable `card_id text` to `pending_purchases`. |
| receipt worker (`internal/receipt/…`) | Persist the Mercury transaction's `cardId` onto the pending row at ingest. |
| Mercury client (`internal/receipt/mercury.go` or equivalent) | Add a `ListCards()` call (`GET /cards`) returning `id`/`nameOnCard`/`lastFour`. Cache per request. |
| `internal/inventory/types.go` | Add `Cardholder string \`json:"cardholder"\`` to `PendingReviewDetail`. |
| `internal/inventory/handler.go` | In `PeriodSummaryHandler`, fetch the card list once, build `card_id → label`, set `Cardholder` on each detail. |
| `internal/inventory/period_summary_test.go` | Assert cardholder population + fallbacks. |

Keep the Mercury `cardId` field name and the `nameOnCard`/`lastFour` card
fields in sync with the consumer's `service/external/mercury.go`.

---

## Tests (HQ repo)

1. **Name path**: pending row whose `card_id` maps to a card with
   `nameOnCard = "Jamal Cole"` → `cardholder == "Jamal Cole"`.
2. **Last-four fallback**: card with blank `nameOnCard`, `lastFour = "9876"`
   → `cardholder == "card ••9876"`.
3. **No card**: row with `card_id IS NULL` → `cardholder == ""` (not null).
4. **Unknown card**: `card_id` not present in the Mercury card list →
   `cardholder == ""`.
5. **Shape**: `cardholder` is always a string, never omitted/null.

---

## Consumer follow-up (this repo, after HQ ships)

Once `cardholder` is populated on `pending_review_details`, sales-processor
can simplify: read `detail.Cardholder` directly in
`formatHQCompletenessFailure` and delete `resolvePendingCardholders`, the
`ListCards`/`CardholderByBankTx` join, and the `CardID` field — dropping the
extra Mercury fetch from the payroll run. Until then, the client-side join
stays and both paths agree on the same `"Name" | "card ••1234" | ""`
labelling, so deploy ordering is safe in either direction.
