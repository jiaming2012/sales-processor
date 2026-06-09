# Payroll HQ Handoff — Replacing Sling as the Employee Source of Truth

> **Companion doc:** [`payroll-report.md`](./payroll-report.md) — the
> report's section-by-section semantics. This doc is the **data contract**
> the payroll run depends on; that doc is the **output shape**.

This document captures every employee-data primitive the weekly payroll
run currently consumes from [Sling](https://getsling.com), so that an HQ
employee module can be designed to replace it without behavioural
regressions. Anything HQ needs to support in order for `sales-processor`
to swap its `slingTimesheetClient` for an `hqEmployeeClient` is enumerated
here.

The sales-processor never persists this data — it re-fetches every run.
HQ becoming the source of truth means a one-time migration of the tag
state described below, plus a stable API that returns the same fields.

---

## TL;DR Migration Checklist

1. **Mirror three free-form tags** in HQ with these exact names (the
   strings are constants in `models/slinguser.go` — see [Tag Vocabulary](#tag-vocabulary)):
   - `commission`
   - `owner`
   - `primary pay schedule`
2. **Mirror two scalar fields** on every active employee:
   - `hireDate` (ISO date, `YYYY-MM-DD`)
   - `employeeId` (integer; the payroll system's stable employee ID,
     not HQ's internal user ID)
3. **Mirror the wage history** (effective-dated rates) — at least one
   entry per active employee.
4. **Expose three endpoints** with the response shapes documented in
   [API Surface](#api-surface).
5. **Re-tag the current set:**
   - Jamal Cole: `commission` + `owner`
   - Latanya Mcgriff: `commission`
   - Anyone with ≥3 months tenure who is hourly: `primary pay schedule`
6. Swap the `external.NewSlingTimesheet(...)` constructor in `main.go`
   for an HQ equivalent that returns the same interface
   (`PopulateUsers() error`, `Users() []SlingUser`, `GetPayroll(from,to)`).

---

## Tag Vocabulary

These three tag names are **load-bearing constants**. They live in
`models/slinguser.go` and any divergence in spelling means the rule
silently stops applying. HQ must let an administrator attach these
exact strings (or HQ must define a stable mapping from its native
concepts to these strings before serialising).

| Tag | Constant | Drives |
|---|---|---|
| `commission` | `models.TagCommission` | Employee is paid via the commission flow (not hourly). Excluded from hourly hours aggregation. Unapproved shifts are allowed (commission folks set their own hours). Required for the per-employee "Sales Commission Breakdown" section. |
| `owner` | `models.TagOwner` | Employee is the company owner. Always combined with `commission`. The owner's commission structure is forced to 0% (`commissionSalesStructureOwner`); they are suppressed from the aggregate commission cost AND from the per-employee Sales Commission Breakdown — they pay themselves outside payroll. |
| `primary pay schedule` | `models.TagPrimarySchedule` | Employee is paid the same cycle they work. Absence of this tag = "new-employee" schedule (1-week pay hold). See [Schedule Routing](#schedule-routing). |

**Tag semantics are additive:**
- `commission` alone → standard tiered commission structure
- `commission` + `owner` → owner commission structure (0%, suppressed)
- `primary pay schedule` alone → hourly, paid same cycle
- (no tags) → hourly, paid one cycle in arrears (the held schedule)
- `commission` + `primary pay schedule` → the schedule tag is ignored
  for commission employees (their payment cadence is governed by the
  commission flow, not the hourly schedule).

---

## Required Per-Employee Fields

For every employee returned by the user list, these fields must be
present and accurate:

| Field | Type | Required? | Used for |
|---|---|---|---|
| `id` | int | yes | Sling internal user ID; only used for keying timesheet entries. HQ can substitute its own stable integer. |
| `name` (first) | string | yes | Display |
| `lastname` | string | yes | Display |
| `email` | string \| null | no | Display only |
| `active` | bool | yes | Inactive users are skipped entirely |
| `employeeId` | string-of-int \| null | **yes for active users** | Joins to tip exclusions, business-side IDs, payroll provider records. **Must be a parseable integer.** Active users with null `employeeId` trigger a per-user prompt to skip-or-fail. |
| `hireDate` | ISO date string `YYYY-MM-DD` \| null | **yes for active users** | Drives the tenure display ("joined 8 months ago"), the 3-month schedule-warning threshold, and the `<1 month → N days` rendering. **Missing values are a hard-fail.** See [Validation](#validation--hard-fail-conditions). |
| `groupIds` | []int | yes | Joined against `/groups` to resolve free-form tag names. |
| `wages.base[]` | array | **yes for active users** | Effective-dated wage history. Active users with zero wages are rejected. We pick the entry with the latest `dateEffective` ≤ now. Each entry needs `regularRate` (string parseable as float) and `dateEffective` (ISO date string). |

---

## API Surface

The current Sling integration hits three endpoints. HQ needs equivalents
returning the documented shapes (the field names below are what
`models/slinguser.go` and `service/external/employeehours.go` unmarshal).

### 1. Authentication

```
POST /v1/account/login
Body: {"email": "...", "password": "..."}
→ Response header: Authorization: <token>
```

HQ may substitute any auth scheme; the integration only needs to obtain
a string token to attach to subsequent requests.

### 2. User list

```
GET /v1/users/concise
Authorization: <token>
→ {"users": [<UserDTO>, ...]}
```

`UserDTO` shape (only fields the integration reads):

```json
{
  "id": 18333144,
  "name": "Jamal",
  "lastname": "Cole",
  "email": "jamal@yumyums.kitchen",
  "active": true,
  "employeeId": "100",
  "hireDate": "2023-06-19",
  "groupIds": [14016658, 32561010, 32564613],
  "hoursCap": 0,
  "wages": {
    "base": [
      {"id": 1, "dateEffective": "2023-06-19", "regularRate": "25.00"},
      {"id": 2, "dateEffective": "2025-01-01", "regularRate": "28.00"}
    ]
  }
}
```

All other Sling fields (`avatar`, `timezone`, `birthdayDate`, etc.) are
ignored and don't need to be replicated.

### 3. Groups (tag definitions)

```
GET /v1/groups
Authorization: <token>
→ [<GroupDTO>, ...]
```

`GroupDTO` shape:

```json
{
  "id": 32561010,
  "type": "group",
  "name": "commission",
  "archivedAt": null,
  "memberCount": 2
}
```

**Only entries with `"type": "group"` are loaded into the tag map.**
Sling's `position`, `location`, and `everyone` types are ignored on
purpose — they're org-structure metadata, not employee-classification
tags. HQ should either:

- Use a `type` discriminator in the same shape, OR
- Expose a dedicated endpoint that returns only free-form tags.

### 4. Timesheet (pay period)

```
GET /v1/reports/timesheets?dates=<from>T00:00:00Z/<to>T23:59:59Z
Authorization: <token>
→ [<TimesheetItemDTO>, ...]
```

`TimesheetItemDTO` shape:

```json
{
  "user": {"id": 18333144},
  "position": {"id": 14018494},
  "timesheetProjections": [
    {
      "clockIn":  "2026-06-02T12:00:00Z",
      "clockOut": "2026-06-02T20:00:00Z",
      "status":   "approved",
      "breakMinutes": 0,
      "paidMinutes": 480
    }
  ]
}
```

`status == "approved"` is the gate for hourly employees — unapproved
shifts hard-fail unless the user is `commission`-tagged. `paidMinutes`
is the only minute field the integration uses (divided by 60 for hours).

The endpoint is called twice per run: once for the current pay period
and once for the previous (the previous-week pull is needed to build
the held-employees' Payroll This Cycle entries).

---

## Validation / Hard-Fail Conditions

These run **before** the 90-second Claude classification pipeline, so
data hygiene problems surface in seconds, not minutes.

| Condition | Behaviour |
|---|---|
| Active employee with `employeeId == null` | Per-user prompt "skip user? (y/n)" — n aborts |
| Active employee with `wages.base[]` empty | Per-user prompt as above |
| Active employee with `hireDate == null` | **All** offenders collected, single aggregated error with sorted names, `panic`. Run must be re-attempted after backfill in the source system. |
| Hourly employee (no `commission`/`owner` tag) with tenure ≥3 months AND no `primary pay schedule` tag | Interactive batched warning: lists offenders, prompts "Exit so you can tag them in [source] first? (y/n):" — `y` aborts via `log.Fatalf`, anything else proceeds with held schedule. **Silenced by `--skip-schedule-warning`.** |
| Unapproved shift on a non-`commission`-tagged user | `panic` with the user name and shift time range |

The 3-month threshold lives in `models.PrimaryScheduleAge` (currently
`3`). If HQ exposes a "probation period" concept, sync it to this
constant rather than duplicating the rule.

---

## Schedule Routing

Two distinct views of labour exist in the report and they intentionally
differ:

1. **Accrued labour (P&L view)** — `Total Labor` in the Summary section
   and Operating Profit are computed from **every hour worked this week**
   × rate × `(1 + 0.106 employer tax)` + commission cost. This is what
   the week actually cost the business.

2. **Cash payroll (operational view)** — the `Payroll This Cycle`
   section is what cheques get cut for this cycle. Routing depends on
   the `primary pay schedule` tag:

   | Tag state | What appears in Payroll This Cycle |
   |---|---|
   | `primary pay schedule` set | This week's worked hours |
   | Tag absent (held) | **Previous** week's worked hours (we owed them last week, paying now) |
   | Tag absent and zero previous-week hours | Nothing (e.g., first week of employment) |

The delta between accrued labour and cash payroll = the change in the
**held-wage liability**, displayed in the `Held Hours (Liability — paid
next cycle)` section. Steady state with stable held headcount → the two
views converge.

---

## Sections of the Report Driven by Tags

| Section | Inputs from this contract |
|---|---|
| Summary `Total Labor` / `Operating Profit` | All current-week hourly hours (accrual) + commission cost |
| Labor Detail | Same as Summary, broken out into wages / payroll taxes / commission |
| **Held Hours (Liability — paid next cycle)** | Current-week hours for users **without** `primary pay schedule` tag |
| **Payroll This Cycle (what we pay)** | Primary-tagged users' current-week hours + held users' previous-week hours; each row labelled `(primary pay schedule · joined X ago)` or `(held from week ending DATE · joined X ago)` |
| Sales Commission Breakdown | Users with `commission` tag, owner-tagged users excluded |
| Total Commission Employee Cost (Labor Detail) | Users with `commission` tag, owner-tagged users excluded |

---

## CLI Flags Related to This Contract

| Flag | Default | Effect |
|---|---|---|
| `--skip-schedule-warning` | false | Silences the interactive ≥3-months-without-`primary pay schedule` warning. Run still routes those employees as held. Use for unattended scheduled runs. |
| `--skip-mercury` | false | Unrelated to this contract; gates banking integration. Listed here only because it shares the early-validation block. |
| `--skip-classify` | false | Unrelated; skips the Claude classify pipeline. |

---

## Constants Reference

Defined in `models/slinguser.go`:

```go
const (
    TagCommission      = "commission"
    TagOwner           = "owner"
    TagPrimarySchedule = "primary pay schedule"
    PrimaryScheduleAge = 3 // months
)
```

Defined in `models/commissions.go`:

```go
type CommissionBasedEmployee struct {
    Id                       int
    Name                     string
    CommissionSalesStructure *CommissionSalesStructure
    IsOwner                  bool // set true when the user is also `owner`-tagged
}
```

Defined in `models/weeklysummary.go`:

```go
type WeeklySummary struct {
    Hours            []EmployeeHours // accrual: this week's worked hours
    PayrollThisCycle []EmployeeHours // cash: what we pay this cycle
    // ... other fields ...
}
```

---

## Open Questions for HQ Migration

1. **Wage history mutability.** Sling allows future-dated `dateEffective`
   entries; we pick the latest one ≤ now. Does HQ have the same model,
   or does it only store the "current" rate? If the latter, raises need
   to be coordinated with pay-period boundaries.

2. **Tip exclusions.** Currently a hardcoded `[]TipExclusion` in
   `main.go:1090` keyed by `EmployeeID` + weekday. If HQ models
   role-based tipping rules, this hardcode could be retired too — but
   that's out of scope for the initial migration.

3. **Cash employees.** `[]CashEmployeeInputParam` is currently empty
   in `main.go:1050`. If HQ models cash-paid employees, the integration
   could populate this from HQ rather than the hardcoded literal.

4. **Approval workflow.** Sling's `status == "approved"` is the gate for
   non-commission shifts. HQ needs an equivalent shift-approval signal
   or the hard-fail rule needs to be relaxed.

5. **One source for both classification and payroll.** Today the
   Mercury classification snapshot includes `Payroll` as a category
   (see `internal/classify/categories.go`). When HQ owns employees,
   `Payroll` outflows in Mercury should be reconcilable against HQ
   pay-cycle records — out of scope here but worth flagging.
