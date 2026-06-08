package models

import (
	"fmt"
	"strings"
)

type TipDetails struct {
	Details map[Employee]float64
	Total   float64
}

type WeeklySummary struct {
	Sales            float64
	SalesTax         float64
	CashTendered     float64
	CCFees           float64
	VoidedTotal      float64
	VoidedOrders     []*OrderDetail
	Tips             TipDetails
	Hours            []EmployeeHours
	PreviousHours    []EmployeeHours
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

func formatHourlyWageLines(name string, hours, rate, wage, tips, employerTaxes float64) string {
	takeHome := wage + tips
	totalCost := takeHome + employerTaxes
	return fmt.Sprintf(
		"%s\n  Take-home pay: %.2f hours @ $%.2f/hr + $%.2f tips = $%.2f\n  Employer taxes: $%.2f\n  Total cost to business: $%.2f\n",
		name, hours, rate, tips, takeHome, employerTaxes, totalCost,
	)
}

// computeHourlyTotals returns the current week's hourly wages and payroll
// taxes (including cash employees rolled in as in the rest of the report).
func (s *WeeklySummary) computeHourlyTotals() (wages, payrollTaxes float64) {
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

func (s *WeeklySummary) computePreviousHourlyTotals() (wages, payrollTaxes float64) {
	for _, employeeHours := range s.PreviousHours {
		wage := employeeHours.Hours * employeeHours.Employee.Rate
		wages += wage
		payrollTaxes += wage * 0.106
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

	wages, payrollTaxes := s.computeHourlyTotals()
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

// ShowLaborDetail renders the Labor Detail drill-down: hourly wage breakdown,
// payroll taxes, and hourly-vs-commission totals.
func (s *WeeklySummary) ShowLaborDetail() string {
	wages, payrollTaxes := s.computeHourlyTotals()
	previousWages, _ := s.computePreviousHourlyTotals()
	hourlyCost := wages + payrollTaxes
	totalLabor := hourlyCost + s.CommissionEmployeesCost

	out := strings.Builder{}
	out.WriteString("Labor Detail\n")
	out.WriteString("-----------------------\n")
	out.WriteString("\n")
	out.WriteString(fmt.Sprintf("  This Week's Wages:               $%.2f\n", wages))
	out.WriteString(fmt.Sprintf("  Previous Week's Wages:           $%.2f\n", previousWages))
	out.WriteString(fmt.Sprintf("  Payroll Taxes:                   $%.2f\n", payrollTaxes))
	out.WriteString(fmt.Sprintf("  Total Hourly Employee Cost:      $%.2f\n", hourlyCost))
	out.WriteString(fmt.Sprintf("  Total Commission Employee Cost:  $%.2f\n", s.CommissionEmployeesCost))
	out.WriteString(fmt.Sprintf("  Total Labor:                     $%.2f\n", totalLabor))
	out.WriteString("\n")
	return out.String()
}

// ShowCurrentHoursDetail renders the per-employee hours/wage detail for
// the current week, including cash employees rolled in as total-cost rows.
func (s *WeeklySummary) ShowCurrentHoursDetail() string {
	out := strings.Builder{}
	out.WriteString("Hours - This Week\n")
	out.WriteString("-----------------------\n")
	out.WriteString("\n")
	for _, employeeHours := range s.Hours {
		wage := employeeHours.Hours * employeeHours.Employee.Rate
		tips := s.Tips.Details[employeeHours.Employee.Employee()]
		payrollTaxes := wage * 0.106
		out.WriteString(formatHourlyWageLines(employeeHours.Employee.Name(), employeeHours.Hours, employeeHours.Employee.Rate, wage, tips, payrollTaxes))
		out.WriteString("\n")
	}
	for _, cashEmployee := range s.CashEmployeesPay {
		totalComp := cashEmployee.NetPay + cashEmployee.Taxes
		out.WriteString(fmt.Sprintf("%v: $%.2f pay + $%.2f taxes = $%.2f total cost\n", cashEmployee.Name, cashEmployee.NetPay, cashEmployee.Taxes, totalComp))
		out.WriteString("\n")
	}
	return out.String()
}

// ShowPreviousHoursDetail renders per-employee hours/wage detail for the
// previous week.
func (s *WeeklySummary) ShowPreviousHoursDetail() string {
	out := strings.Builder{}
	out.WriteString("Hours - Previous Week\n")
	out.WriteString("-----------------------\n")
	out.WriteString("\n")
	for _, employeeHours := range s.PreviousHours {
		wage := employeeHours.Hours * employeeHours.Employee.Rate
		tips := s.Tips.Details[employeeHours.Employee.Employee()]
		payrollTaxes := wage * 0.106
		out.WriteString(formatHourlyWageLines(employeeHours.Employee.Name(), employeeHours.Hours, employeeHours.Employee.Rate, wage, tips, payrollTaxes))
		out.WriteString("\n")
	}
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
