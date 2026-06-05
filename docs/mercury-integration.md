# Mercury Integration Specification

## Purpose

Dispatch three categories of bank transfers per pay period from a
Mercury Business workspace, with idempotent reruns so re-running the
script does not duplicate transfers.

## Transfer Categories

| Kind | Destination type | API path | Triggering condition |
|---|---|---|---|
| `sales_tax` | Internal account (Business workspace) | `POST /transfer` (internal) | `weeklySummary.SalesTax > 0` |
| `deferred_taxes` | Internal account (Business workspace) | `POST /transfer` (internal) | `payrollTaxes > 0` |
| `rent_hold` | External recipient (Personal account) | `POST /account/{id}/transactions` (recipient flow) | `rentHoldAmount > 0` |

The rent-hold leg cannot use the internal-transfer endpoint because the
destination ("Personal Vacation Fun", ending ••6343) lives in a
separate Mercury Personal workspace not reachable via the business
workspace's API key.

## Resolution Flow

```
1. ListAccounts        → resolve source / sales-tax / deferred-tax accounts
                         via env var or interactive picker
2. ListRecipients      → resolve rent-hold recipient via env var or picker
3. resolveRentHoldMethod (ACH vs domesticWire) for the rent-hold leg
4. Load transfer ledger for this pay period
5. For each kind in order:
     - skip if ledger says status=sent
     - build request with stable idempotency key
     - dispatch through executeTransfers / executeExternalTransfer
     - record outcome in ledger
6. Save ledger
```

## Account & Recipient Resolution

Same pattern for both accounts and recipients:

1. If the env var is set and the ID exists in the fetched list, use it.
2. If the env var is set but the ID is missing, fail fast with a clear
   error.
3. Otherwise, prompt the user with a numbered picker. Save the chosen
   ID to the relevant env file (`.env` for production, `.env.sandbox`
   when `--sandbox` is set).

The recipient picker shows bank name + last-4 + method capability tag
(`[ACH]`, `[Wire]`, or `[ACH+Wire]`) per recipient — see
`pickMercuryRecipient` in `main.go`.

### Env vars

| Variable | Purpose |
|---|---|
| `MERCURY_API_KEY` | Mercury Business API key (required) |
| `MERCURY_SOURCE_ACCOUNT_ID` | Source (Operations) account |
| `MERCURY_SALES_TAX_ACCOUNT_ID` | Sales tax holding account |
| `MERCURY_DEFERRED_TAX_ACCOUNT_ID` | Deferred payroll tax account |
| `MERCURY_RENT_HOLD_RECIPIENT_ID` | External recipient for rent hold |
| `MERCURY_RENT_HOLD_METHOD` | `ach` or `domesticWire` (persisted choice) |
| `MERCURY_SANDBOX` | When `true`, all env-file writes target `.env.sandbox` |

The legacy `MERCURY_RENT_HOLD_ACCOUNT_ID` is dead and ignored.

## Rent-Hold Method Resolution

`resolveRentHoldMethod(recipient, envVar)` in `main.go`:

1. If `envVar` is set, validate it is `ach`/`domesticWire` *and* that
   the recipient has the matching routing on file. Fatal on mismatch.
2. If the recipient supports only one method, use that silently. No
   env-var write.
3. If the recipient supports both methods, prompt the user interactively
   and persist the choice to `envVar`.

Mercury's `recipient.defaultPaymentMethod` field is intentionally
ignored — choice must be explicit (env var or user prompt).

### Wire-specific requirement

When `method == "domesticWire"`, the transfer payload includes:

```json
{
  "purpose": {
    "simple": {
      "category": "transferToMyExternalAccount",
      "additionalInfo": "Rent hold <fromDate> - <toDate>"
    }
  }
}
```

Mercury rejects wires without `purpose`. ACH transfers omit it. This
validation is enforced inside the Mercury client as well.

## Transfer Ledger

### Purpose

Make script reruns idempotent within a pay period. Without this, a
second run would re-fire the same three transfers.

### File format

One JSON file per pay period at `output/transfers/transfers_<toDate>.json`:

```json
{
  "payPeriod": "2026-05-31",
  "transfers": {
    "sales_tax": {
      "status": "sent",
      "amount": 102.40,
      "method": "internal",
      "destination": "Mercury Savings ••7818",
      "sentAt": "2026-06-05T11:23:14Z",
      "idempotencyKey": "2026-05-31-sales_tax"
    },
    "deferred_taxes": { ... },
    "rent_hold": { ... }
  }
}
```

| Field | Meaning |
|---|---|
| `status` | `sent` (success) or `failed` (Mercury returned an error) |
| `error` | Error message, present only when `status=failed` |
| `idempotencyKey` | The key actually sent to Mercury (may include `-retry-<unix>` suffix when forced) |

### Idempotency rules

- Stable base key: `<payPeriod>-<kind>` (e.g. `2026-05-31-rent_hold`).
  Mercury's idempotency check rejects duplicates even if the local
  ledger is lost.
- On rerun:
  - `status=sent` → transfer is skipped with a `[skipped] ...` log line
  - `status=failed` or no entry → transfer is retried with the same
    stable key (Mercury treats it as a continuation of the same attempt)
- On `--force-resend=<kinds>`:
  - The named kinds' entries are cleared from the ledger before
    dispatch
  - Their idempotency keys get a `-retry-<unix>` suffix so Mercury
    accepts the new attempt as a distinct transaction

### Override CLI

`--force-resend=<comma-separated kinds>` where each kind is one of
`sales_tax`, `deferred_taxes`, `rent_hold`, or the literal `all`.

Examples:
- `--force-resend=rent_hold` — only reissue rent hold
- `--force-resend=sales_tax,deferred_taxes` — reissue both tax transfers
- `--force-resend=all` — clear the whole period and start over

## Approval UX

Internal and external transfers each have their own confirmation
prompt. The CLI `--auto-approve-transfers` flag skips both prompts.
Skipping at the prompt leaves the ledger untouched — the transfers
remain pending and will be re-attempted on the next run.

## Pending Transfer Summary Format

Internal block:

```
--- Pending Transfers ---
  $102.40 from Mercury Checking ••4613 → Mercury Savings ••7818 (Sales tax 2026-05-27 - 2026-05-31)
  $160.86 from Mercury Checking ••4613 → Mercury Savings ••7818 (Deferred taxes 2026-05-27 - 2026-05-31)

Execute 2 transfer(s)? (y/n):
```

External block:

```
--- Pending External Transfer ---
  $475.00 from Mercury Checking ••4613 → Jamal Cole · COLUMN N.A. ••6343 [domesticWire] (Rent hold 2026-05-27 - 2026-05-31)

Execute external transfer? (y/n):
```

Source and destination are both rendered as `<account name> ••<last4>`
for symmetric readability. External destinations additionally include
the bank name and the payment method tag in brackets.

## API Reference

Mercury endpoints used (Bearer-token authenticated):

| Endpoint | Purpose |
|---|---|
| `GET /api/v1/accounts` | List Business workspace accounts |
| `GET /api/v1/recipients` | List external recipients |
| `POST /api/v1/transfer` | Internal transfer (sourceAccountId → destinationAccountId) |
| `POST /api/v1/account/{accountId}/transactions` | External transfer to a recipient |

Sandbox swaps the base URL: `https://api-sandbox.mercury.com/api/v1`
when the `--sandbox` flag is set.
