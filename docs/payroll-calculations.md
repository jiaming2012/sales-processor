# Payroll Calculations Specification

## Scope

Defines every dollar figure produced by the report, with the formula
and the data source. Implementation lives in `models/commissions.go`,
`models/weeklysummary.go`, `models/orderdetails.go`, and `main.go`.

## Employee Categories

| Category | Examples | How they're paid |
|---|---|---|
| Hourly W-2 | Michael Thomas, Kyree Boone, Trevon Mcintyre, JJ Staton | Hours × rate via OnPay |
| Commission-based | Latanya Mcgriff | Base + commission + tips via OnPay |
| Cash | (none currently — slice is empty by default) | Off-books direct payment |

Each category has its own pay model and tax treatment.

## Tax Rates

| Constant | Value | Applies to | Where used |
|---|---|---|---|
| Hourly employer rate | 0.106 | Hourly W-2 employees | `wage × 0.106` in `WeeklySummary.Show` |
| Commission employee withholding | 0.20 | Commission-based employees | `commissionEmployeeTaxRate` in `main.go` |
| Commission employer FICA match | 0.0765 | Commission-based employees | Literal at Latanya's wage-assembly site |
| Cash employee default rate | 0.25 | Cash employees | `defaultEmployeeTaxRate` in `main.go` |

The 0.20 vs 0.25 split reflects the different tax bracket Latanya
falls into vs. the cash employees' "22% federal + 7.65% payroll + 3%
buffer" approximation.

## Hourly Wage Formula

For each hourly W-2 employee in `WeeklySummary.Hours` (current week) or
`WeeklySummary.PreviousHours` (prior week):

```
wage           = hours × rate
tips           = WeeklySummary.Tips.Details[employee]   (typically 0)
takeHome       = wage + tips
employerTaxes  = wage × 0.106
totalCost      = takeHome + employerTaxes
```

Reported as three labeled lines per employee. Previous week's hours are
informational only — they are not paid out this cycle.

## Commission Formula

Implemented in `models/commissions.go` (`commissionBasedEmployeesTopLineSummary`).

```
commission     = netSales × salesCommissionPercentage
basePay        = max(0, 300 - max(0, commission - 300))
                 = $300 base, reduced 1-for-1 once commission exceeds $300
preTaxPay      = basePay + commission + tips
employerTax    = preTaxPay × 0.0765
employeeTax    = preTaxPay × 0.20           (commissionEmployeeTaxRate)
netPay         = preTaxPay - employeeTax
deposit        = netPay - rentHold
```

Note: `basePay` is constructed so the total of base + commission caps at
$300 + (commission − $300) = commission for high-commission weeks. For
low weeks, the employee receives $300 base + their commission.

The amount written to the deferred-tax transfer ledger for this
employee is `employeeTax + employerTax = preTaxPay × 0.2765`.

## Tip Allocation

Tips are allocated weekly, not daily. Currently, the entire pooled tip
amount goes to Latanya Mcgriff:

```
for each employee in schedule:
    if employee == "Latanya Mcgriff":
        tips[employee] += summary.Tips
    else:
        tips[employee] += 0
```

The tip-share calculation that distributes by tipped hours is
implemented but currently commented out, pending a policy decision on
tip pooling. See `main.go` around the `CalcTipShare` call site.

`tipsWithheldPercentage = 0.03` is the share the business retains from
each order's tip before pooling. Applied in
`models/orderdetails.go:GetSummary`.

## Sales Tax / Deferred Tax / Rent Hold Transfers

These three transfers are derived from the weekly summary and are
dispatched to Mercury per [mercury-integration.md](mercury-integration.md).

| Transfer | Amount source |
|---|---|
| Sales tax | `weeklySummary.SalesTax` (sum of `summary.Taxes` per day) |
| Deferred taxes | Sum of `taxes` field across `cashEmployeeWages`. With no cash employees, this is just Latanya's combined employee + employer payroll tax (`preTaxPay × 0.2765`) |
| Rent hold | Hardcoded literal `rentHoldAmount = 475.0` in `main.go` |

## Voided Sales

Each `OrderDetail.Voided == true` order is collected (not just counted)
into `OrderSummary.VoidedOrders`. The weekly aggregate walks every day's
`EmployeeDetails`, runs `GetSummary`, and accumulates voided orders +
their `Amount` field into `WeeklySummary.VoidedOrders` and `VoidedTotal`.

Reported in two places:
- Summary > Sales > "Voided Sales: $..."
- Standalone "Voided Orders" section with per-order detail

> Caveat: in current Toast data the void rows show `Amount=$0.00` and
> `(no tab)`. If Toast actually encodes the voided value in a different
> field, the `Amount` reference will need to change.

## Employee Costs / Sales Ratio

```
employeeCosts = wages + totalPayrollTaxes
ratio         = (employeeCosts / netSales) × 100
```

Rendered as an integer percentage in the Summary > Labor block.
Excludes the previous week's wages from the ratio (those are
informational).
