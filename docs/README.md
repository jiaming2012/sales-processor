# Sales Processor — Specifications

This directory captures the design decisions and contracts that govern the
sales-processor application. Each document describes *what* the system
does and *why*, not implementation detail — refer to the code for
specifics. When behavior diverges between code and spec, treat the spec
as the intended contract and update the code (or update the spec if the
divergence is deliberate).

| Document | Scope |
|---|---|
| [Payroll Report](payroll-report.md) | PDF report structure, sections, layout rules, font/typography decisions |
| [Payroll Calculations](payroll-calculations.md) | Tax rates, wage formulas, commission math, deposit calculation |
| [Mercury Integration](mercury-integration.md) | Mercury Bank API client, account/recipient resolution, transfer dispatch, idempotency ledger |
| [CLI & Environment](cli.md) | Command-line flags, environment variables, output file paths |

## Conventions across all specs

- "Pay period" = the seven-day window ending on the most recent Sunday
  (`toDate`). All time-scoped outputs (PDF, CSV, transfer ledger) are
  keyed by `toDate`.
- "Previous week" = the seven-day window immediately preceding the pay
  period; used for the post-cycle hours and wage breakdowns.
- All dollar amounts are USD with two decimals of precision unless
  otherwise noted.
- All percentages in formulas are expressed as decimals (0.20 = 20%)
  unless the field name says otherwise.
