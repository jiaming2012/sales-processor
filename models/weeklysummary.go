package models

import (
	"fmt"
	"strings"
	"time"
)

type TipDetails struct {
	Details map[Employee]float64
	Total   float64
}

type WeeklySummary struct {
	Sales        float64
	SalesTax     float64
	CashTendered float64
	CCFees       float64
	VoidedTotal  float64
	VoidedOrders []*OrderDetail
	Tips         TipDetails
	// Hours is the set of hours actually worked this week (the accrual
	// view). Drives Total Labor / Operating Profit and the Held Hours
	// liability section. Previous-week hours don't live on the summary
	// anymore — main.go consumes them once to build PayrollThisCycle and
	// they're not rendered.
	Hours []EmployeeHours
	// PayrollThisCycle is what we are actually paying in this pay cycle —
	// for primary-scheduled employees, their current-week hours; for
	// new-employee (held) staff, their previous-week hours. Drives the
	// Payroll This Cycle section (cash-out view).
	PayrollThisCycle []EmployeeHours
	CashEmployeesPay []CashEmployeePay
	// CommissionEmployeesCost is the aggregate base + commission cost for
	// all non-owner commission-based employees, excluding tips (tips come
	// from customers, not the business). Populated externally before Show().
	CommissionEmployeesCost float64
	// COGSExclTax is the pay-period cost of goods sold pulled from HQ;
	// populated externally when the HQ integration is available. Used to
	// render Operating Profit = Sales − COGS − Total Labor.
	COGSExclTax float64
	// COGSInclTax mirrors HQ's COGS-with-tax for the period; rendered in
	// the COGS detail section. Populated externally alongside COGSExclTax.
	COGSInclTax float64
}

func (s *WeeklySummary) TotalHourlyWorkersExpense() float64 {
	totalComp := 0.0
	totalPayrollTaxes := 0.0
	for _, employeeHours := range s.Hours {
		wage := employeeHours.Hours * employeeHours.Employee.Rate
		tips := s.Tips.Details[employeeHours.Employee.Employee()]
		totalComp += wage + tips

		// todo: this should be passed in as a parameter to the report
		payrollTaxes := wage * 0.106
		totalPayrollTaxes += payrollTaxes
	}

	for _, cashEmployee := range s.CashEmployeesPay {
		totalComp += cashEmployee.NetPay + cashEmployee.Taxes

		// todo: this should be passed in as a parameter to the report
		totalPayrollTaxes += cashEmployee.Taxes
	}

	return totalComp + totalPayrollTaxes
}

func formatHourlyWageLines(name, tenure, schedule string, hours, rate, wage, tips, employerTaxes float64) string {
	takeHome := wage + tips
	totalCost := takeHome + employerTaxes
	var bits []string
	if schedule != "" {
		bits = append(bits, schedule)
	}
	if tenure != "" {
		if tenure == "today" {
			bits = append(bits, "joined today")
		} else {
			bits = append(bits, fmt.Sprintf("joined %s ago", tenure))
		}
	}
	header := name
	if len(bits) > 0 {
		header = fmt.Sprintf("%s (%s)", name, strings.Join(bits, " · "))
	}
	return fmt.Sprintf(
		"%s\n  Take-home pay: %.2f hours @ $%.2f/hr + $%.2f tips = $%.2f\n  Employer taxes: $%.2f\n  Total cost to business: $%.2f\n",
		header, hours, rate, tips, takeHome, employerTaxes, totalCost,
	)
}

// computeAccruedHourlyTotals returns the accrued labor cost for THIS week —
// every hour worked this week × rate × employer tax rate, regardless of
// when those wages will actually be paid. This is the P&L view: the
// week's economics. Cash employees are rolled in (they're labor expense
// the same way).
//
// Note: this differs from PayrollThisCycle (the cash-out view, which
// includes held employees' PREVIOUS week's hours and excludes their
// current-week hours). The delta between the two is the change in the
// held-wage liability shown in ShowHeldHoursLiability.
func (s *WeeklySummary) computeAccruedHourlyTotals() (wages, payrollTaxes float64) {
	for _, employeeHours := range s.Hours {
		wage := employeeHours.Hours * employeeHours.Employee.Rate
		wages += wage
		payrollTaxes += wage * 0.106
	}
	for _, cashEmployee := range s.CashEmployeesPay {
		wages += cashEmployee.NetPay + cashEmployee.Taxes
		payrollTaxes += cashEmployee.Taxes
	}
	return
}

// Show renders the top-of-report Summary section: high-level revenue,
// costs, key ratios, and Operating Profit at the bottom as the punchline.
// Operating Profit deducts CC fees as a real operating expense (the
// processor takes them out before money hits the bank); Net Sales stays
// a clean top-line figure so ratios like Food Cost % aren't distorted.
// Detail sections (Labor Detail, Hours, Tips, etc.) render separately
// via the ShowXxx methods below.
func (s *WeeklySummary) Show() string {
	output := strings.Builder{}

	wages, payrollTaxes := s.computeAccruedHourlyTotals()
	hourlyCost := wages + payrollTaxes
	totalLabor := hourlyCost + s.CommissionEmployeesCost

	output.WriteString("Summary\n")
	output.WriteString("-----------------------\n")
	output.WriteString("\n")
	output.WriteString(fmt.Sprintf("  Net Sales:                $%.2f\n", s.Sales))
	output.WriteString(fmt.Sprintf("  Sales Tax:                $%.2f\n", s.SalesTax))
	output.WriteString(fmt.Sprintf("  Tips:                     $%.2f\n", s.Tips.Total))
	output.WriteString(fmt.Sprintf("  Cash Tendered:            $%.2f\n", s.CashTendered))
	output.WriteString(fmt.Sprintf("  Credit Card Fees:         $%.2f\n", s.CCFees))
	output.WriteString(fmt.Sprintf("  Voided Sales:             $%.2f\n", s.VoidedTotal))
	output.WriteString("\n")
	if s.COGSExclTax > 0 {
		output.WriteString(fmt.Sprintf("  Cost of Goods Sold:       $%.2f\n", s.COGSExclTax))
	}
	output.WriteString(fmt.Sprintf("  Total Labor:              $%.2f\n", totalLabor))
	output.WriteString("\n")
	if s.Sales > 0 {
		output.WriteString(fmt.Sprintf("  Labor Cost %%:             %.0f%%\n", (totalLabor/s.Sales)*100.0))
		if s.COGSExclTax > 0 {
			output.WriteString(fmt.Sprintf("  Food Cost %%:              %.1f%%\n", (s.COGSExclTax/s.Sales)*100.0))
		}
		if s.CCFees > 0 {
			output.WriteString(fmt.Sprintf("  Credit Card Cost %%:       %.1f%%\n", (s.CCFees/s.Sales)*100.0))
		}
	}
	if s.COGSExclTax > 0 {
		operatingProfit := s.Sales - s.COGSExclTax - totalLabor - s.CCFees
		output.WriteString("\n")
		output.WriteString(fmt.Sprintf("  Operating Profit*:        $%.2f\n", operatingProfit))
		if s.Sales > 0 {
			output.WriteString(fmt.Sprintf("  Operating Profit %%:       %.1f%%\n", (operatingProfit/s.Sales)*100.0))
		}
		output.WriteString("\n")
		output.WriteString("  * Operating Profit = Net Sales - COGS - Total Labor - Credit Card Fees\n")
	}
	output.WriteString("\n")
	return output.String()
}

// ShowLaborDetail renders the Labor Detail drill-down: accrued hourly
// wages for this week (the P&L view), payroll taxes, commission cost,
// and the rollup that drives Total Labor / Operating Profit in Summary.
func (s *WeeklySummary) ShowLaborDetail() string {
	wages, payrollTaxes := s.computeAccruedHourlyTotals()
	hourlyCost := wages + payrollTaxes
	totalLabor := hourlyCost + s.CommissionEmployeesCost

	out := strings.Builder{}
	out.WriteString("Labor Detail\n")
	out.WriteString("-----------------------\n")
	out.WriteString("\n")
	out.WriteString(fmt.Sprintf("  Wages Worked This Week:          $%.2f\n", wages))
	out.WriteString(fmt.Sprintf("  Payroll Taxes:                   $%.2f\n", payrollTaxes))
	out.WriteString(fmt.Sprintf("  Total Hourly Employee Cost:      $%.2f\n", hourlyCost))
	out.WriteString(fmt.Sprintf("  Total Commission Employee Cost:  $%.2f\n", s.CommissionEmployeesCost))
	out.WriteString(fmt.Sprintf("  Total Labor:                     $%.2f\n", totalLabor))
	out.WriteString("\n")
	return out.String()
}

// ShowHeldHoursLiability renders the per-employee hours worked THIS week
// by held (new-employee) staff — i.e., wages accrued this week that will
// be paid out next cycle. Primary-scheduled employees are absent (paid
// same cycle, no liability accrues). Cash employees are absent (paid in
// cash same cycle). asOf is the pay-period end date, used for tenure.
// The per-employee schedule label is omitted because every row in this
// section is by definition held.
func (s *WeeklySummary) ShowHeldHoursLiability(asOf time.Time) string {
	out := strings.Builder{}
	out.WriteString("Held Hours (Liability - paid next cycle)\n")
	out.WriteString("-----------------------\n")
	out.WriteString("\n")
	totalWages := 0.0
	totalTaxes := 0.0
	any := false
	for _, employeeHours := range s.Hours {
		if employeeHours.Employee.IsPrimarySchedule() {
			continue
		}
		any = true
		wage := employeeHours.Hours * employeeHours.Employee.Rate
		tips := s.Tips.Details[employeeHours.Employee.Employee()]
		payrollTaxes := wage * 0.106
		tenure := employeeHours.Employee.Tenure(asOf)
		out.WriteString(formatHourlyWageLines(employeeHours.Employee.Name(), tenure, "", employeeHours.Hours, employeeHours.Employee.Rate, wage, tips, payrollTaxes))
		out.WriteString("\n")
		totalWages += wage
		totalTaxes += payrollTaxes
	}
	if !any {
		out.WriteString("No held hours accrued this week.\n\n")
		return out.String()
	}
	out.WriteString(fmt.Sprintf("Total wages owed (paid next cycle):  $%.2f\n", totalWages))
	out.WriteString(fmt.Sprintf("Total payroll taxes (next cycle):    $%.2f\n", totalTaxes))
	out.WriteString(fmt.Sprintf("Total held liability this week:      $%.2f\n", totalWages+totalTaxes))
	out.WriteString("\n")
	return out.String()
}

// ShowPayrollThisCycle renders the per-employee payroll-this-cycle section
// — what's actually being paid out this cycle. Primary-scheduled employees
// show their current-week hours; held employees show their previous-week
// hours and are labelled with the week-ending date the hours came from.
// Held employees with no previous-week hours (e.g., first week) are
// omitted from this section. heldFromWeekEnd is the previous pay-period
// end date.
func (s *WeeklySummary) ShowPayrollThisCycle(asOf, heldFromWeekEnd time.Time) string {
	out := strings.Builder{}
	out.WriteString("Payroll This Cycle (what we pay)\n")
	out.WriteString("-----------------------\n")
	out.WriteString("\n")
	heldLabel := fmt.Sprintf("held from week ending %s", heldFromWeekEnd.Format("Jan 2"))
	totalWages := 0.0
	totalTaxes := 0.0
	for _, employeeHours := range s.PayrollThisCycle {
		wage := employeeHours.Hours * employeeHours.Employee.Rate
		tips := s.Tips.Details[employeeHours.Employee.Employee()]
		payrollTaxes := wage * 0.106
		tenure := employeeHours.Employee.Tenure(asOf)
		schedule := TagPrimarySchedule
		if !employeeHours.Employee.IsPrimarySchedule() {
			schedule = heldLabel
		}
		out.WriteString(formatHourlyWageLines(employeeHours.Employee.Name(), tenure, schedule, employeeHours.Hours, employeeHours.Employee.Rate, wage, tips, payrollTaxes))
		out.WriteString("\n")
		totalWages += wage
		totalTaxes += payrollTaxes
	}
	out.WriteString(fmt.Sprintf("Total wages paid this cycle:   $%.2f\n", totalWages))
	out.WriteString(fmt.Sprintf("Total payroll taxes:           $%.2f\n", totalTaxes))
	out.WriteString(fmt.Sprintf("Total payroll cost this cycle: $%.2f\n", totalWages+totalTaxes))
	out.WriteString("\n")
	return out.String()
}

// ShowTipsBreakdown renders the per-employee tips breakdown.
func (s *WeeklySummary) ShowTipsBreakdown() string {
	out := strings.Builder{}
	out.WriteString("Tips Breakdown\n")
	out.WriteString("-----------------------\n")
	out.WriteString("\n")
	for employee, amount := range s.Tips.Details {
		out.WriteString(fmt.Sprintf("%s: $%.2f\n", employee, amount))
	}
	out.WriteString("\n")
	return out.String()
}
