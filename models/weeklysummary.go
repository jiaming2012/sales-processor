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
	Tips             TipDetails
	Hours            []EmployeeHours
	PreviousHours    []EmployeeHours
	CashEmployeesPay []CashEmployeePay
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

func (s *WeeklySummary) Show() string {
	output := strings.Builder{}
	wageOutput := strings.Builder{}

	wages := 0.0
	totalPayrollTaxes := 0.0
	for _, employeeHours := range s.Hours {
		wage := employeeHours.Hours * employeeHours.Employee.Rate
		tips := s.Tips.Details[employeeHours.Employee.Employee()]
		payrollTaxes := wage * 0.106
		wageOutput.WriteString(formatHourlyWageLines(employeeHours.Employee.Name(), employeeHours.Hours, employeeHours.Employee.Rate, wage, tips, payrollTaxes))
		wageOutput.WriteString("\n")
		wages += wage

		totalPayrollTaxes += payrollTaxes
	}

	for _, cashEmployee := range s.CashEmployeesPay {
		totalComp := cashEmployee.NetPay + cashEmployee.Taxes
		wageOutput.WriteString(fmt.Sprintf("%v: $%.2f pay + $%.2f taxes = $%.2f total cost\n", cashEmployee.Name, cashEmployee.NetPay, cashEmployee.Taxes, totalComp))
		wageOutput.WriteString("\n")
		wages += totalComp

		totalPayrollTaxes += cashEmployee.Taxes
	}

	// -------
	wageOutput.WriteString("----------------------------\nPrevious Hours:\n")
	previousWages := 0.0
	previousTotalPayrollTaxes := 0.0
	for _, employeeHours := range s.PreviousHours {
		wage := employeeHours.Hours * employeeHours.Employee.Rate
		tips := s.Tips.Details[employeeHours.Employee.Employee()]
		payrollTaxes := wage * 0.106
		wageOutput.WriteString(formatHourlyWageLines(employeeHours.Employee.Name(), employeeHours.Hours, employeeHours.Employee.Rate, wage, tips, payrollTaxes))
		wageOutput.WriteString("\n")
		previousWages += wage

		previousTotalPayrollTaxes += payrollTaxes
	}

	employeeCosts := wages + totalPayrollTaxes

	output.WriteString("Summary\n")
	output.WriteString("-----------------------\n")

	output.WriteString("Sales\n")
	output.WriteString(fmt.Sprintf("  Net Sales:        $%.2f\n", s.Sales))
	output.WriteString(fmt.Sprintf("  Tips:             $%.2f\n", s.Tips.Total))
	output.WriteString(fmt.Sprintf("  Sales Tax:        $%.2f\n", s.SalesTax))
	output.WriteString(fmt.Sprintf("  Cash Tendered:    $%.2f\n", s.CashTendered))
	output.WriteString(fmt.Sprintf("  Credit Card Fees: $%.2f\n", s.CCFees))
	output.WriteString("\n")

	output.WriteString("Labor\n")
	output.WriteString(fmt.Sprintf("  Wages:                  $%.2f\n", wages))
	output.WriteString(fmt.Sprintf("  Previous Wages:         $%.2f\n", previousWages))
	output.WriteString(fmt.Sprintf("  Payroll Taxes:          $%.2f\n", totalPayrollTaxes))
	output.WriteString(fmt.Sprintf("  Total Employee Costs:   $%.2f\n", employeeCosts))
	output.WriteString(fmt.Sprintf("  Employee Costs / Sales: %.0f%%\n", (employeeCosts/s.Sales)*100.0))
	output.WriteString("\n")
	output.WriteString("\n")

	output.WriteString("Tips Breakdown\n")
	output.WriteString("-----------------------\n")
	for employee, amount := range s.Tips.Details {
		output.WriteString(fmt.Sprintf("%s: $%.2f\n", employee, amount))
	}
	output.WriteString("\n")
	output.WriteString("\n")

	output.WriteString("Wages Breakdown\n")
	output.WriteString("-----------------------\n")
	output.WriteString(wageOutput.String())

	return output.String()
}
