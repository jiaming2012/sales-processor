# CLI & Environment Specification

## Invocation

```
go run main.go [flags]
```

## Flags

| Flag | Type | Default | Purpose |
|---|---|---|---|
| `--sandbox` | bool | `false` | Use Mercury's sandbox environment. Also routes env-file writes to `.env.sandbox` instead of `.env` |
| `--auto-approve-transfers` | bool | `false` | Skip the y/n prompt before dispatching each transfer batch |
| `--force-resend` | string | `""` | Comma-separated list of transfer kinds to clear from the ledger before this run. Accepts `sales_tax`, `deferred_taxes`, `rent_hold`, `deposit`, or `all` |
| `--skip-mercury` | bool | `false` | Preview mode: bypass Mercury account/recipient resolution, transfer dispatch, AND the classify pipeline (classify needs the Mercury client). The PDF/CSV still render. Use when iterating on report formatting without reachable Mercury (offline dev, blocked IP, etc.) |
| `--skip-classify` | bool | `false` | Run Mercury transfers but skip the Claude-based classify pipeline. Use when `claude` CLI is unavailable on the host (e.g., a prod cron without Claude Code installed). See [classification.md](classification.md) for full pipeline details. |

Removed (and intentionally not reintroduced):
- `--rent-hold-method` — replaced by `MERCURY_RENT_HOLD_METHOD` env var
  + interactive prompt. The CLI surface area for this decision is
  redundant with the recipient's routing info.

## Environment Variables

All env vars are loaded from `.env` (production) or `.env.sandbox`
(when `--sandbox` is passed) via `godotenv`.

### Mercury

| Variable | Required | Purpose |
|---|---|---|
| `MERCURY_API_KEY` | Yes | Bearer token for the Mercury Business workspace |
| `MERCURY_SANDBOX` | No | When `true`, env-file writes target `.env.sandbox`. Equivalent to but separate from the `--sandbox` flag |
| `MERCURY_SOURCE_ACCOUNT_ID` | No* | Source (Operations) account ID |
| `MERCURY_SALES_TAX_ACCOUNT_ID` | No* | Sales-tax destination account |
| `MERCURY_DEFERRED_TAX_ACCOUNT_ID` | No* | Deferred-taxes destination account |
| `MERCURY_RENT_HOLD_RECIPIENT_ID` | No* | Rent-hold recipient (Mercury Personal account via the recipient flow) |
| `MERCURY_RENT_HOLD_METHOD` | No | `ach` or `domesticWire`. When unset and the recipient supports both, the user is prompted and the choice is persisted here |
| `MERCURY_PERSONAL_API_KEY` | Yes | Bearer token for the Mercury Personal workspace — the deposit leg (Latanya's pay) dispatches from there |
| `MERCURY_PERSONAL_SOURCE_ACCOUNT_ID` | No* | Personal-workspace source account for the deposit leg |
| `MERCURY_DEPOSIT_RECIPIENT_ID` | No* | Deposit recipient (Latanya Mcgriff, in the Personal workspace's recipient list) |
| `MERCURY_DEPOSIT_METHOD` | No | `ach` or `domesticWire` for the deposit leg. Same resolution rules as `MERCURY_RENT_HOLD_METHOD` |

\* Not strictly required, but absence triggers an interactive picker
that writes the resulting ID back to the env file.

### Sling

| Variable | Required | Purpose |
|---|---|---|
| (none — credentials are currently hardcoded in `main.go`) | | TODO: extract to env |

### HQ (Cost of Goods Sold)

| Variable | Required | Purpose |
|---|---|---|
| `HQ_INVENTORY_SERVICE_TOKEN` | No\* | Bearer token for the HQ inventory service. Must match the value the HQ backend reads under the same name. When unset, the COGS section is omitted and the run continues |
| `HQ_BASE_URL` | No | Defaults to `http://localhost:8080`. Override when pointing at a non-local HQ deployment |

\* Not required to run the report. When unset, the PDF prints without
a Cost of Goods Sold section. When set, the report **fails fast** if HQ
reports incomplete data for the period (pending receipt review or
unlinked line items) — see Exit Behavior below.

## Output Files

| Path | Created by | Purpose |
|---|---|---|
| `output/payroll/payroll_<toDate>.pdf` | `writePDF` | Human-readable weekly report |
| `output/payroll/payroll_<toDate>.csv` | OnPay export | Machine import format |
| `output/transfers/transfers_<toDate>.json` | Transfer ledger | Idempotency state per pay period |

At end of run, the PDF and CSV paths are printed under an
`--- Output Files ---` block. The transfer ledger path is not printed
because it is operational state, not a user-facing artifact.

## Exit Behavior

- Any failure during account/recipient resolution or env-var validation
  exits via `log.Fatalf` with a descriptive message.
- A failed Mercury transfer logs the error but does *not* exit — the
  ledger records `status=failed` and processing continues with the
  remaining transfers. The failed kind will be retried on the next run.
- Save-ledger failures log an error but do not exit; the in-memory
  state is lost, so on the next run those transfers will re-attempt
  using their stable idempotency keys (Mercury will reject duplicates).
- HQ COGS failures (`HQ_INVENTORY_SERVICE_TOKEN` set, but HQ unreachable,
  returns an error, or reports `completeness.ready=false`) exit via
  `log.Fatalf` *before* the PDF is rendered. The fatal message includes
  the pending receipt IDs and unlinked line-item IDs so the operator can
  resolve them in the HQ Inventory dashboard. When the token is unset,
  the run continues without the COGS section — no error.

## Interactive Prompts

The script is partially interactive. Prompts appear in this order
during a typical run:

1. Mercury account picker(s) — only when the corresponding env var is
   unset. One picker per missing account (Business accounts first, then
   the Personal-workspace deposit source).
2. Mercury recipient picker — when `MERCURY_RENT_HOLD_RECIPIENT_ID` or
   `MERCURY_DEPOSIT_RECIPIENT_ID` is unset.
3. Method picker — when `MERCURY_RENT_HOLD_METHOD` /
   `MERCURY_DEPOSIT_METHOD` is unset *and* the recipient supports both
   ACH and wire.
4. Per-cash-employee net pay (currently dormant — `cashEmployees`
   slice is empty).
5. Transfer approval (`y/n`) — once for the internal-transfer batch,
   once for the external (rent-hold) transfer, and once for the deposit
   transfer. Suppressed by `--auto-approve-transfers`.

All picker selections are persisted to the env file so subsequent runs
are non-interactive unless the underlying Mercury data changes.
