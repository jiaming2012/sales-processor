# Sales-Processor Change — Mercury ↔ HQ Gap Check Before COGS

> **Companion docs:**
> - [`cogs-hq-tracked-tx-ids.md`](./cogs-hq-tracked-tx-ids.md) — HQ-side
>   field this doc depends on. **Ship HQ first**, then this one.
> - [`cogs-hq-receipt-gate.md`](./cogs-hq-receipt-gate.md) — surfaces
>   already-ingested-but-untriaged Mercury txns (shipped; amendment
>   pending). The check here handles the *not-yet-ingested* case the
>   receipt-gate cannot see.
>
> Suggested workflow: feed this spec into `/gsd-quick` from the
> `sales-processor/` directory.

---

## Problem

After all the HQ-side work that's shipped, sales-processor still produces
a payroll PDF with silently undercounted COGS in one specific scenario:

**Mercury has a card transaction. HQ's receipt worker hasn't polled
since the transaction landed.**

In that window, HQ has no row for the transaction — not in
`purchase_events`, not in `pending_purchases`. `/period-summary` returns
`completeness.ready=true` because nothing's pending. Sales-processor
trusts it, renders the PDF, and Restaurant Depot vanishes from food
cost.

The HQ-side amendment ([`cogs-hq-receipt-gate.md`](./cogs-hq-receipt-gate.md))
fixes the date-window bug for transactions HQ has ingested. It does **not**
help when HQ hasn't ingested the transaction at all.

Sales-processor is the right place to close this last gap: it already
talks to Mercury directly (`service/external/mercury.go:421`,
`ListTransactionsInPeriod`), so it can ask Mercury "what card
transactions exist for this period?" and cross-check against HQ's
`tracked_bank_tx_ids` list.

If any Mercury tx is missing from HQ's view → hard-fail the run.
Operator either (a) waits ~6h for the next receipt worker poll and
re-runs, or (b) kicks the receipt worker manually.

---

## Goal

```
Before:
  fetchHQPeriodSummary()
    └── if !summary.Completeness.Ready → log.Fatalf

After:
  fetchHQPeriodSummary()
    ├── if !summary.Completeness.Ready → log.Fatalf  (existing)
    └── if Mercury has card txn(s) not in summary.TrackedBankTxIDs
        → log.Fatalf with the missing IDs + dashboard links (new)
```

Both checks are blocking. Together they catch:

| Scenario | Caught by |
|---|---|
| Tx ingested, awaiting triage | existing `completeness.ready=false` |
| Tx ingested late (worker noticed after period end) | receipt-gate amendment (HQ side) |
| **Tx in Mercury, never ingested** | **this doc** |

---

## Scope

Single behavioral change. One new Mercury fetch, one diff, one
hard-fail path. No new dependencies.

### Files to change

| File | What changes |
|---|---|
| `service/external/hq.go` | Add `TrackedBankTxIDs []string` field on `HQPeriodSummary`. Optional in JSON tag (`omitempty`) so older HQ deployments don't break decode. |
| `service/external/mercury.go` *(no change)* | `ListTransactionsInPeriod` already returns everything we need. |
| `main.go` | In `fetchHQPeriodSummary`, after the existing `completeness.ready` check, call Mercury, filter to supported kinds, diff against `summary.TrackedBankTxIDs`, fail-fast on non-empty diff. |
| `main_test.go` *(or new `payroll_gap_check_test.go`)* | Cases on each of the four corners of `(mercury_has, hq_has)`. |

No new env vars. The check is gated on `HQ_INVENTORY_SERVICE_TOKEN` (same
as the rest of the HQ integration) and on Mercury credentials being
present (already required for the classify pipeline; see
`docs/classification.md`).

---

## 1. `service/external/hq.go` — struct update

Current (`service/external/hq.go:23-31`):

```go
type HQPeriodSummary struct {
	From               string             `json:"from"`
	To                 string             `json:"to"`
	COGSExclTax        float64            `json:"cogs_excl_tax"`
	COGSInclTax        float64            `json:"cogs_incl_tax"`
	PurchaseEventCount int                `json:"purchase_event_count"`
	ByVendor           []HQVendorCOGS     `json:"by_vendor,omitempty"`
	Completeness       HQCompletenessBlock `json:"completeness"`
}
```

After:

```go
type HQPeriodSummary struct {
	From               string             `json:"from"`
	To                 string             `json:"to"`
	COGSExclTax        float64            `json:"cogs_excl_tax"`
	COGSInclTax        float64            `json:"cogs_incl_tax"`
	PurchaseEventCount int                `json:"purchase_event_count"`
	ByVendor           []HQVendorCOGS     `json:"by_vendor,omitempty"`
	// TrackedBankTxIDs is every Mercury bank_tx_id HQ has touched for
	// this period across all states (confirmed/pending/discarded). Used
	// by the gap check in fetchHQPeriodSummary to detect Mercury txns
	// the HQ receipt worker hasn't ingested yet. Omitempty so older HQ
	// deployments (pre cogs-hq-tracked-tx-ids handoff) decode cleanly —
	// the gap check degrades to a warning when this is nil.
	TrackedBankTxIDs   []string           `json:"tracked_bank_tx_ids,omitempty"`
	Completeness       HQCompletenessBlock `json:"completeness"`
}
```

Note `omitempty`: this is the inverse of how the HQ-side type is
declared (HQ always emits `[]`). It exists here so a pre-tracked-tx-ids
HQ response decodes to `nil`, distinguishable from a real empty list
that comes back as `[]string{}` after the JSON round-trip. The gap
check uses that distinction to log a "DEGRADED" warning vs. proceeding
normally.

---

## 2. `main.go` — add the gap check

The check goes inside `fetchHQPeriodSummary`
(`main.go:1775-1803`), after the existing `Completeness.Ready` check
(currently lines 1790-1800). That ordering matters: the existing check
is cheap (already in the response), the new check requires a Mercury
HTTP call.

### Helper — `isSupportedMercuryKind`

Sales-processor doesn't currently export an `isSupportedKind` equivalent
(the classify pipeline filters loosely). Add one to `main.go` or
preferably to `service/external/mercury.go` next to
`ListTransactionsInPeriod`. Mirror HQ's set at
`hq/backend/internal/receipt/mercury.go:77-84` exactly so both sides
agree on what counts as "a card transaction":

```go
// IsSupportedCardKind reports whether a Mercury transaction Kind is one
// of the card/debit-card kinds that HQ's receipt worker ingests. The
// payroll gap check uses this to filter Mercury's transaction list
// before diffing against HQ's tracked_bank_tx_ids. Keep in sync with
// hq/backend/internal/receipt/mercury.go:isSupportedKind.
func IsSupportedCardKind(kind string) bool {
	switch kind {
	case "creditCardTransaction", "debitCardTransaction",
		"creditCardCredit", "debitCardCredit":
		return true
	}
	return false
}
```

Also require `tx.Status == "sent"` in the gap check — pending/cancelled
Mercury rows aren't real and HQ filters them out at ingest. Pulling
both filters into a single helper (`IsCountableMercuryCardTx(tx)`) is
cleaner than open-coding the AND.

### Gap check itself

Insert at the bottom of `fetchHQPeriodSummary`, before `return summary`:

```go
// Gap check: Mercury knows about every card transaction the moment the
// bank acks it. HQ only knows about transactions its receipt worker
// has polled. If Mercury has a card tx that HQ hasn't ingested yet,
// the existing completeness.ready check returns true even though COGS
// will silently undercount. Cross-check Mercury → HQ here and fail
// fast.
//
// Degraded mode: pre cogs-hq-tracked-tx-ids HQ deployments don't emit
// the field. nil slice → log a warning and skip. This avoids a deploy
// ordering trap where rolling sales-processor before HQ would break
// every weekly run. Once HQ ships, the field becomes non-nil and the
// check engages automatically.
if summary.TrackedBankTxIDs == nil {
    log.Warnf(
        "HQ /period-summary response missing tracked_bank_tx_ids — "+
            "Mercury sync gap check is DEGRADED. Update HQ to the "+
            "cogs-hq-tracked-tx-ids handoff to re-enable. Proceeding "+
            "with pre-check semantics for %s–%s.",
        summary.From, summary.To,
    )
    return summary
}

mercuryClient, err := external.NewMercuryClient(/* args — see existing classify wiring */)
if err != nil {
    log.Fatalf("init Mercury client for HQ gap check: %v", err)
}

txns, err := mercuryClient.ListTransactionsInPeriod(from, to)
if err != nil {
    log.Fatalf("fetch Mercury transactions for HQ gap check: %v", err)
}

trackedSet := make(map[string]struct{}, len(summary.TrackedBankTxIDs))
for _, id := range summary.TrackedBankTxIDs {
    trackedSet[id] = struct{}{}
}

type missing struct {
    ID            string
    BankDescr     string
    Amount        float64
    CreatedAt     string
    DashboardLink string
}
var gap []missing
for _, tx := range txns {
    if tx.Status != "sent" {
        continue
    }
    if !external.IsSupportedCardKind(tx.Kind) {
        continue
    }
    if _, seen := trackedSet[tx.ID]; seen {
        continue
    }
    gap = append(gap, missing{
        ID:            tx.ID,
        BankDescr:     tx.BankDescription,
        Amount:        tx.Amount,
        CreatedAt:     tx.CreatedAt,
        DashboardLink: tx.DashboardLink,
    })
}

if len(gap) > 0 {
    var b strings.Builder
    fmt.Fprintf(&b,
        "Mercury has %d card transaction(s) HQ's receipt worker "+
            "hasn't ingested yet for %s–%s. Wait for the next "+
            "receipt-worker poll (~6h) and re-run, or kick the "+
            "worker manually.\n",
        len(gap), summary.From, summary.To,
    )
    for _, m := range gap {
        fmt.Fprintf(&b,
            "  - %s  $%.2f  %s  (%s)  %s\n",
            m.CreatedAt, m.Amount, m.BankDescr, m.ID, m.DashboardLink,
        )
    }
    log.Fatal(b.String())
}
```

(`log` is already imported as the `sirupsen/logrus` alias used elsewhere
in `main.go`; `strings` is already imported. The Mercury client
constructor signature lives next to the existing classify call —
mirror that wiring rather than introducing new env-var parsing.)

### Mercury client reuse

The classify pipeline already constructs a `MercuryClient` somewhere in
`main.go` (around the `classify Apply` call near line 1765). Two
options:

1. **Lazy-construct here** — simplest, repeated cost of one env-var
   read and one struct alloc per run. Recommended.
2. **Plumb the existing client through** — requires a refactor of
   `fetchHQPeriodSummary`'s signature and the call site. Not worth it.

Go with option 1.

---

## 3. Tests

Add to `main_test.go` or a new `payroll_gap_check_test.go`. Use
`httptest.NewServer` for both HQ and Mercury. Four cases on the
`(mercury_has, hq_tracks)` axis:

| Case | Mercury returns | HQ `tracked_bank_tx_ids` | Expected |
|---|---|---|---|
| 1 | `[A, B]` (both supported kinds) | `[A, B]` | ✅ pass — no gap |
| 2 | `[A, B]` | `[A]` | ❌ `log.Fatal` with B listed |
| 3 | `[A, B]` (B is `sent=false` or unsupported kind) | `[A]` | ✅ pass — B filtered before diff |
| 4 | `[A]` | `nil` (older HQ — field absent) | ✅ pass with warning logged |

Plus:

- **Empty period**: Mercury `[]`, HQ `tracked: []` → pass, no Mercury
  call required beyond the empty response.
- **Mercury page limit error**: existing `ListTransactionsInPeriod`
  errors when hitting 1000 rows; gap-check propagates via `log.Fatal`
  with the original error.

Use `log` injection (or capture via `os.Stderr` pipe) to assert the
fatal message contains the missing IDs.

---

## Out of scope (worth being honest about)

- **Cash purchases at Restaurant Depot / Save-A-Lot.** Mercury doesn't
  see them, neither does HQ, no diff possible. Mitigation lives in
  operator discipline (card-only) — same as already documented in
  [`cogs-hq-receipt-gate.md`](./cogs-hq-receipt-gate.md).
- **Mercury hasn't synced from the bank yet.** Both Mercury and HQ are
  blind for the window between the swipe and Mercury's bank-feed
  reconciliation (~minutes to ~1h typically). The diff returns "no
  gap" in that window. Real-world impact is low for a weekly run that
  isn't time-critical to the minute.
- **Receipt worker is permanently broken.** The gap-check makes this
  *visible* (every run fails until a human notices) but doesn't fix
  it. That's intentional — visibility is the point.
- **Refunds (`creditCardCredit` / `debitCardCredit`).** Included in
  the check (same `isSupportedKind` set as HQ). They should be in
  `tracked_bank_tx_ids` once the worker processes them, just like any
  other card kind.

---

## Verification after deploy

Local dev:

```bash
# 1. Confirm degraded-mode path works against an old HQ:
#    (force HQ to omit tracked_bank_tx_ids in a test fixture)
go test ./... -run TestGapCheckDegradedMode

# 2. Confirm gap-fail path against a fresh HQ + Mercury fixture:
go test ./... -run TestGapCheckMissingTx

# 3. Local end-to-end:
HQ_INVENTORY_SERVICE_TOKEN=<dev-token> \
HQ_BASE_URL=http://localhost:8080 \
MERCURY_API_KEY=<dev-token> \
  go run . payroll
```

Production:

The first weekly run after deploy is the verification. Either it
succeeds (HQ caught up — normal case) or it fails with the new gap
message (worker lag — exactly the bug we're fixing). The on-call
playbook for the latter is "check receipt-worker logs in HQ; kick if
needed; re-run".

---

## Why not solve this entirely HQ-side

Considered: have HQ proactively poll Mercury inside `/period-summary`
to detect its own gaps. Rejected because:

1. It moves Mercury-API rate-limit pressure to a synchronous request
   path. The receipt worker already polls Mercury every 6h asynchronously
   — duplicating that on every payroll call doubles the rate burn.
2. The completeness check would now depend on HQ holding Mercury
   credentials *and* having network egress to Mercury — additional
   failure modes for a query that's currently purely DB-local.
3. Sales-processor *already* has Mercury credentials and a Mercury
   client (classify pipeline). Putting the diff there is zero new
   surface area.

The current shape (HQ publishes its inventory; sales-processor compares
against authoritative Mercury) is the right separation of concerns.
