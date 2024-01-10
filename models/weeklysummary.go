package models

import (
	"fmt"
	"strings"

	log "github.com/sirupsen/logrus"
)

type TipDetails struct {
	Details map[Employee]float64
	Total   float64
}

type WeeklySummary struct {
	Sales            float64
	SalesTax         float64
	Tips             TipDetails
	Hours            []EmployeeHours
	CashEmployeesPay []CashEmployeePay
}

func (s *WeeklySummary) Show() string {
	output := strings.Builder{}
	wageOutput := strings.Builder{}

	wages := 0.0
	totalPayrollTaxes := 0.0
	for _, employeeHours := range s.Hours {
		wage := employeeHours.Hours * employeeHours.Employee.Rate
		tips := s.Tips.Details[employeeHours.Employee.Employee()]
		totalComp := wage + tips
		wageOutput.WriteString(fmt.Sprintf("%v: %.2f hours @ $%.2f/hr = $%.2f + $%.2f tips = $%.2f total compensation\n", employeeHours.Employee.Name(), employeeHours.Hours, employeeHours.Employee.Rate, wage, tips, totalComp))
		wageOutput.WriteString("\n")
		wages += wage

		// todo: this should be passed in as a parameter to the report
		payrollTaxes := wage * 0.0765
		totalPayrollTaxes += payrollTaxes
		log.Infof("%s payrollTaxes: %.2f", employeeHours.Employee.FirstName, payrollTaxes)
	}

	for _, cashEmployee := range s.CashEmployeesPay {
		totalComp := cashEmployee.NetPay + cashEmployee.Taxes
		wageOutput.WriteString(fmt.Sprintf("%v: $%.2f pay + $%.2f taxes = $%.2f total compensation\n", cashEmployee.Name, cashEmployee.NetPay, cashEmployee.Taxes, totalComp))
		wageOutput.WriteString("\n")
		wages += totalComp

		// todo: this should be passed in as a parameter to the report
		totalPayrollTaxes += cashEmployee.Taxes
		log.Infof("%s payrollTaxes: %.2f", cashEmployee.Name, cashEmployee.Taxes)
	}

	employeeCosts := wages + totalPayrollTaxes

	output.WriteString("Summary\n")
	output.WriteString("-----------------------\n")
	output.WriteString(fmt.Sprintf("Wages: $%.2f\n", wages))
	output.WriteString(fmt.Sprintf("Payroll Taxes: $%.2f\n", totalPayrollTaxes))
	output.WriteString(fmt.Sprintf("Total Employee Costs: $%.2f\n", employeeCosts))
	output.WriteString(fmt.Sprintf("Net Sales: $%.2f\n", s.Sales))
	output.WriteString(fmt.Sprintf("Employee Costs as a Percentage of Sales: %%%.0f\n", (employeeCosts/s.Sales)*100.0))
	output.WriteString(fmt.Sprintf("Tips: $%.2f\n", s.Tips.Total))
	output.WriteString(fmt.Sprintf("Sales Tax: $%.2f\n", s.SalesTax))
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
