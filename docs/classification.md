# Mercury Transaction Classification

## Purpose

Auto-categorize every Mercury card transaction in the pay period using
Claude, so that:

1. Mercury's "Category" column reflects what each charge actually was
   (Restaurant Depot → Food / COGS, CubeSmart → Storage / Rent, OnPay →
   Payroll, etc.) — useful for bookkeeping regardless of HQ.
2. A future HQ change can read `categoryData.name` from each Mercury
   transaction and filter receipt ingestion to food categories only,
   closing the loophole where storage rent ends up in COGS.

## Pipeline

Three phases — `pull` → analyze (Claude) → `apply` — run inline during
the weekly `payroll` task. The intermediate JSON files in
`output/classify/` are the contract between phases; an operator can
hand-edit `proposals_<to>.json` between analyze and apply if Claude's
verdict needs adjusting.

```
output/classify/
├── transactions_<to>.json          ← Pull writes: snapshot + category list
├── proposals_<to>.json             ← Claude writes: classification verdicts
└── proposals_<to>.applied.json     ← Apply renames on success
```

### Pull

`internal/classify.Pull` calls `EnsureCategories` (idempotent — creates
any missing canonical categories via `POST /categories`), then
`MercuryClient.ListTransactionsInPeriod` for the pay period. It filters
to **every sent transaction** regardless of kind — cards, wires, ACH,
internal transfers, and "other" (e.g. Toast deposits). Non-expense
buckets like `Revenue` and `Transfer` are part of the canonical list so
Claude can label those rows correctly. Pending/failed rows are excluded.

The snapshot is the only source of truth Apply consults for the
"current" categoryId, so a stale snapshot can cause spurious re-PATCHes.
In normal operation Pull/Apply run minutes apart in the same `payroll`
invocation; staleness is not a real concern.

### Analyze (Claude)

`main.go` invokes `claude "<prompt>"` via `os/exec`. The prompt is
self-contained — each `claude` invocation is a fresh session — and is
built in `internal/classify.PromptForPeriod`. It tells Claude exactly
which file to read, which file to write, the JSON schema, and the
canonical category list with descriptions.

Rules baked into the prompt:

1. Produce exactly one proposal per snapshot transaction (Apply fails
   hard if any tx is unproposed).
2. `categoryId` must be drawn from the snapshot's `categories[]` array.
3. Use `Other / Needs Review` when uncertain rather than guessing.
4. Output valid JSON only — no markdown fences, no commentary.

### Apply

`internal/classify.Apply` reads both files for the period, validates
every proposal against the snapshot, and PATCHes Mercury for proposals
whose `categoryId` differs from the snapshot's currently-assigned one.
Skipped when current == proposed (idempotent re-runs are cheap).

Each PATCH sets two fields:

- `categoryId` — the proposed category UUID
- `note` — `auto-classified by Claude (conf: 0.92) — <reasoning>`

The note is the source-of-truth marker that the row is Claude's work.
Operators can grep Mercury notes for `"auto-classified by Claude"` to
audit low-confidence decisions.

On success, `proposals_<to>.json` is renamed to
`proposals_<to>.applied.json`. The transactions snapshot is kept for
audit reference (and reuse by a future re-Apply if needed).

## Failure modes (all fail-hard)

The classify pipeline is treated as critical infrastructure — any
failure kills the weekly run before the PDF is generated. The rationale
matches the HQ COGS completeness gate: stale or partial categorization
makes downstream numbers misleading.

| Failure | Where | Exit |
|---|---|---|
| `claude` CLI not in PATH | `exec.LookPath` precheck | `log.Fatalf` |
| Mercury 4xx/5xx on list, create, or PATCH | classify package | `log.Fatalf` |
| Claude exits non-zero | `cmd.Run()` | `log.Fatalf` |
| Claude wrote no proposals file | `os.Stat` precheck before Apply | `log.Fatalf` |
| Claude proposal references unknown txId or categoryId | Apply validation | error → fatal |
| Some snapshot tx has no proposal | Apply validation | error → fatal |
| Duplicate proposals for one tx | Apply validation | error → fatal |

Two escape hatches when the failure is operational rather than
data-quality:

- `--skip-mercury` — bypasses Mercury entirely (also disables classify
  since it needs the Mercury client). Used during offline development
  or when the Mercury API token's IP whitelist is wrong.
- `--skip-classify` — runs Mercury transfers but skips classification.
  Used when `claude` is unavailable on the host (e.g., a prod cron
  without Claude Code installed).

## Re-classification on every run (by design)

Pull does **not** filter to transactions where `categoryData` is null.
Every transaction in the period goes into the snapshot and gets a fresh
proposal, even if Mercury already shows it as categorized. Reason:
Mercury's auto-classification is frequently wrong (it tags CubeSmart
as "Software" sometimes), and Claude's view is treated as authoritative.

Trade-off: a human edit in Mercury can be overwritten on the next
weekly run if Claude disagrees. To make a human override sticky for
now, either:

1. Edit `internal/classify/categories.go` so Claude's prompt biases
   correctly for that merchant pattern, or
2. Set `--skip-classify` for that week's run.

A more durable "manual override" mechanism (e.g., a `human-set:` note
marker that Apply respects) is a known follow-up — see the bottom of
this doc.

## Canonical category list

Edits live in `internal/classify/categories.go` (Go slice — recompile
to update). Names match Mercury's pre-existing defaults where possible
so the classifier writes into the same buckets Mercury would otherwise
auto-fill — avoiding duplicate-ish categories in the chart of accounts.

| Name | Source | Bucket |
|---|---|---|
| COGS | Mercury default | Wholesale and retail food vendors for the menu |
| Rent & Utilities | Mercury default | Storage units, kitchen rent, utilities, business phone/internet |
| Payroll | Mercury default | Wages, ACH to payroll providers, contractor payments |
| Travel & Transportation | Mercury default | Gas, vehicle maintenance, vehicle insurance, business travel |
| Software & Subscriptions | Mercury default | Recurring software subscriptions |
| Office Supplies & Equipment | Mercury default | Non-food operating supplies, packaging, kitchen equipment |
| Marketing & Advertising | Mercury default | Ad spend, signage, marketing materials |
| Legal & Professional Services | Mercury default | Lawyer, accountant, insurance, consultants |
| Owner Personal | Created by classify | Personal use that landed on the business card |
| Revenue | Mercury default | Money in from customers/sales channels (Toast deposits, DoorDash payouts) |
| Transfer | Mercury default | Money moving between accounts you own (Mercury internal transfers) |
| Other / Needs Review | Created by classify | Fallback when context is ambiguous |

Renaming a category in the Go list does **not** rename it in Mercury —
the new name is treated as a separate category by `EnsureCategories`.
After a rename: also delete the old category in Mercury, then re-run
classify so existing transactions migrate to the new id.

## Task targets

```
task payroll                         # full weekly run, includes classify
task payroll:preview                 # PDF only, skips Mercury entirely
task payroll:no-classify             # transfers + PDF, no classify
task classify:pull   FROM=… TO=…     # snapshot only
task classify:analyze TO=…           # run the claude prompt
task classify:apply  TO=…            # PATCH Mercury from proposals
task classify       FROM=… TO=…      # full three-phase run for an arbitrary period
```

The debug targets exist for re-running a single phase after hand-editing
the proposals file. The `payroll` umbrella is the normal path.

## Known follow-ups (not implemented)

- **Sticky human overrides.** Today Apply re-PATCHes on every run unless
  current == proposed. A `human-set:` marker on the existing note that
  Apply respects would let operators correct Claude permanently.
- **HQ filtering by category.** Once Mercury categorization is reliable,
  HQ's receipt worker should only ingest transactions where
  `categoryData.name == "Food / COGS"`. That eliminates the CubeSmart-
  in-COGS class of bug entirely. Out of scope here — see future HQ doc.
- **Wider periods.** Pull currently only fetches the pay period. If
  Mercury's auto-classification drifts on historical transactions, an
  operator may want `task classify FROM=2026-01-01 TO=2026-06-30` to
  re-do everything. The debug targets already support that.
