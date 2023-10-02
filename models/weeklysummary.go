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
	Sales float64
	Taxes float64
	Tips  TipDetails
	Hours []EmployeeHours
}

func (s *WeeklySummary) Show() string {
	output := strings.Builder{}
	wageOutput := strings.Builder{}

	wages := 0.0
	for _, employeeHours := range s.Hours {
		wage := employeeHours.Hours * employeeHours.Employee.Rate
		tips := s.Tips.Details[employeeHours.Employee.Employee()]
		totalComp := wage + tips
		wageOutput.WriteString(fmt.Sprintf("%v: %.2f hours @ $%.2f/hr = $%.2f + $%.2f tips = $%.2f total compensation\n", employeeHours.Employee.Name(), employeeHours.Hours, employeeHours.Employee.Rate, wage, tips, totalComp))
		wageOutput.WriteString("\n")
		wages += wage
	}

	output.WriteString("Summary\n")
	output.WriteString("-----------------------\n")
	output.WriteString(fmt.Sprintf("Net Sales: $%.2f\n", s.Sales))
	output.WriteString(fmt.Sprintf("Wages as a Percentage of Sales: %%%.0f\n", (wages/s.Sales)*100.0))
	output.WriteString(fmt.Sprintf("Tips: $%.2f\n", s.Tips.Total))
	output.WriteString(fmt.Sprintf("Sales Tax: $%.2f\n", s.Taxes))
	output.WriteString("\n")
	output.WriteString("\n")

	output.WriteString("Tips Breakdown\n")
	output.WriteString("-----------------------\n")
	for employee, amount := range s.Tips.Details {
		output.WriteString(fmt.Sprintf("%s: $%.2f\n", employee, amount))
		output.WriteString("\n")
	}
	output.WriteString("\n")
	output.WriteString("\n")

	output.WriteString("Wages Breakdown\n")
	output.WriteString("-----------------------\n")
	output.WriteString(wageOutput.String())

	return output.String()
}
