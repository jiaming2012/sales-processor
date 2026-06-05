# Payroll Report Specification

## Purpose

Produce a weekly payroll PDF (and complementary CSV for OnPay import)
that summarizes a pay period's sales, labor cost, voided activity, cash
position, and per-employee commission breakdowns.

## Outputs

| File | Path | Notes |
|---|---|---|
| PDF report | `output/payroll/payroll_<toDate>.pdf` | Primary human-readable artifact |
| OnPay CSV | `output/payroll/payroll_<toDate>.csv` | Machine import format for OnPay; columns owned by `payroll` package |

Both paths are printed to stdout at end of run under an
`--- Output Files ---` block.

## Report Section Order

The PDF is built by concatenating section blocks in this fixed order:

1. **Title** — `Sales Report for <fromDate> - <toDate>`
2. **Daily blocks** — one block per day in the pay period, each emitted
   by `DailySummary.Show()`
3. **Summary** — sales and labor roll-up for the period
4. **Tips Breakdown** — per-employee tip allocation
5. **This Week's Hours** — hourly wage breakdown for the current pay period
6. **Previous Week's Hours** — hourly wage breakdown for the prior period
   (informational; not paid out this cycle)
7. **Cost of Goods Sold** — food cost ratio + per-vendor breakdown,
   sourced from the HQ inventory service. Omitted when
   `HQ_INVENTORY_SERVICE_TOKEN` is unset (see [CLI doc](cli.md))
8. **Voided Orders** — every voided order with timestamp + total voided
9. **Cash** — period cash position
10. **Sales Commission Breakdown** — per-employee commission detail

> The Cash and Sales Commission sections are intentionally separated:
> cash is a period-level fact, not a per-employee fact.

## Section Conventions

### Heading hierarchy

The PDF renderer recognizes two levels:

- **Top-level heading**: a non-empty line followed by a dashes-only line
  (≥ 5 hyphens). Renders bold at the heading font size; the dashes are
  consumed and not drawn.
- **Sub-heading**: a non-indented, non-blank line that is not a table
  row. Renders bold at the body font size.

Headings are emitted with `<title>\n-----------------------\n` exactly.

### Table rows

Any line of the form `<label>: <value>` (with non-empty content on both
sides of the colon) is parsed as a table row. Consecutive table rows
form a *table block* with auto-fit column widths:

- **Label column**: width = max label width in the block + a small
  right-pad gap. Left-aligned.
- **Value column**: fills the remaining page width. Left-aligned; long
  values (e.g. the hourly take-home expression) wrap inside the column
  via `MultiCell`.

Blank lines *inside* a table block render as a small vertical gap but
do not break the block — column widths remain consistent across the
gap. A heading, sub-heading, or non-table line ends the block.

### Indentation

Leading spaces in source text translate to a proportional left indent
(2.25 mm per space). Used to visually nest rows under sub-headings (e.g.
the `Sales` and `Labor` groups inside the Summary).

## Typography

| Element | Family | Style | Size |
|---|---|---|---|
| Title | Helvetica | Bold | 24 pt |
| Top-level heading | Helvetica | Bold | 30 pt |
| Sub-heading | Helvetica | Bold | 18 pt |
| Body / table cell | Helvetica | Regular | 18 pt |

- Family is Helvetica throughout for legibility on screen and print.
  Courier was tried for alignment but produced terminal-style output;
  alignment is now handled by table cells, not by monospaced padding.
- The title is intentionally smaller than section headings so the
  report stays one line wide on A4 portrait.

## Per-Section Contracts

### Summary

Two sub-groups, rendered as two table blocks under sub-headings:

```
Summary
-----------------------
Sales
  Net Sales:        $...
  Tips:             $...
  Sales Tax:        $...
  Cash Tendered:    $...
  Credit Card Fees: $...
  Voided Sales:     $...

Labor
  This Week's Wages:      $...
  Previous Week's Wages:  $...
  Payroll Taxes:          $...
  Total Employee Costs:   $...
  Employee Costs / Sales: ..%
```

`Employee Costs / Sales` is rendered as an integer percent (no decimal
places).

### Tips Breakdown

Flat list of `<employee>: $<amount>` rows under the heading. One row
per employee with a tip allocation. Currently only Latanya Mcgriff
receives a non-zero share (see [calculations](payroll-calculations.md)).

### This Week's Hours / Previous Week's Hours

Each employee renders as a 4-line block:

```
<Employee Name>
  Take-home pay: <hours> hours @ $<rate>/hr + $<tips> tips = $<gross>
  Employer taxes: $<employer-side payroll tax>
  Total cost to business: $<gross + employer taxes>
```

- "Take-home pay" is the user's preferred phrasing for what the
  employee receives before OnPay handles withholding. Despite the
  label it is pre-withholding gross.
- Three labeled lines (vs. one cramped line) so the employee/employer
  split is immediately visible.
- Tips appear in the formula even when zero; the field is always
  present for consistency.

Cash employees follow the same heading + table-row pattern but with a
single combined line: `<name>: $<pay> pay + $<taxes> taxes = $<total>`.

### Cost of Goods Sold

Sourced from `GET /api/v1/inventory/period-summary?from=&to=` on the HQ
backend. The section is omitted entirely when
`HQ_INVENTORY_SERVICE_TOKEN` is unset; when the token *is* set but HQ
reports the period as incomplete (pending receipts or unlinked line
items), the run fails before any PDF is written.

```
Cost of Goods Sold
-----------------------

  Net Sales:        $<weekly net sales>
  COGS:             $<HQ cogs_excl_tax>
  Tax:              $<HQ cogs_incl_tax − cogs_excl_tax>
  Gross Profit:     $<Net Sales − COGS>
  Food Cost %:      <COGS / Net Sales, 1 decimal>
  Receipts in HQ:   <HQ purchase_event_count>

By Vendor
  <Vendor Name> (<N> trips): $<vendor pre-tax> pre-tax ($<vendor incl tax> incl tax)
  ... one row per vendor with at least one receipt in the period ...
```

- `Net Sales` is `weeklySummary.Sales` *after* unpaid-delivery
  adjustment — the same number used by the Summary section.
- `COGS` is the pre-tax line-item total. `Tax` is the difference
  between HQ's `cogs_incl_tax` and `cogs_excl_tax` — i.e. the sales tax
  paid to vendors, surfaced as its own line so the COGS row matches
  the "food cost" mental model.
- `Food Cost %` uses the pre-tax COGS so the ratio matches industry
  convention (tax-inclusive would overstate food cost relative to
  net-of-tax sales).
- When `Net Sales` is `$0` (slow week, demo data), `Food Cost %` renders
  as `n/a` and `Gross Profit` falls back to `-COGS`.
- `Receipts in HQ` is HQ's `purchase_event_count`. It's printed as a
  cross-check signal: HQ can only report on receipts it has ingested
  (Mercury card transactions with attachments, plus manual entries).
  Cash purchases and card transactions without photographed receipts
  are invisible — a wildly low food-cost % usually means receipts are
  missing, not that purchasing didn't happen.
- The `By Vendor` sub-block requires HQ's `by_vendor` response field
  (see `docs/cogs-hq-handoff.md`). Until HQ ships that field, only the
  summary block prints — the section degrades gracefully rather than
  breaking the report.
- Rows are pre-sorted by HQ descending on pre-tax spend; the consumer
  preserves that order.
- Colons in vendor names are replaced with ` -` to keep the table
  renderer's label/value split intact.

### Voided Orders

```
Voided Orders
-----------------------

Order #<orderNumber> - <MM/DD H:MM AM/PM> - <tab name>: $<amount>
... one line per voided order ...
Total Voided: $<sum>
```

- "Opened" timestamp is used (voided orders have no Paid time).
- Time format: `MM/DD H:MM AM/PM` — month/day with 12-hour clock,
  intentionally compact since the year is implicit in the report title.
- When the tab is empty the literal `(no tab)` is rendered.
- An empty void list renders `No voided orders.` followed by
  `Total Voided: $0.00`.

### Cash

```
Cash
-----------------------

No cash taken.                    | OR | -$<amount>  (one per held entry)
Cash Left in Register: $<amount>
```

- Cash held entries (when present) are listed indented as negative
  amounts to communicate "removed from register".
- `Cash Left in Register` is only emitted when `cashTendered >
  totalCashHeld`.

### Sales Commission Breakdown

For each commission-based employee, in order:

```
PAY for <Employee Name> <fromDate> - <toDate>

Base Pay: $...
Sales: $<netSales> * <commissionPct>% = $<commission>
Tips: $...

Pretax Pay: $...
Taxes: -$...
Net Pay: $...

Rent Hold: -$...   (only when > 0)

Deposit: $...
```

The commission flow is intentionally contiguous (no Cash sub-block) so
a PDF page break does not split the calc trail.

## Rendering Implementation Notes

- The renderer reads the assembled report as a single string and walks
  it line-by-line; section structure is inferred from the text
  conventions above, not from explicit markup.
- This keeps the section emitters (`weeklysummary.Show()`,
  `commissions.Show()`, the inline blocks in `main.go`) as simple
  string concatenation. The rendering pipeline is the one place that
  knows about PDF semantics.
