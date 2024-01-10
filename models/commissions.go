package models

import (
	"fmt"
	"strings"
	"time"
)

type CommissionSalesItem interface {
	IsSatisfied(sales float64) bool
	GetSalesCommissionPercentage() float64
}

type CommissionSalesIsLessThan struct {
	SalesThreshold            float64
	SalesCommissionPercentage float64
}

func (i CommissionSalesIsLessThan) IsSatisfied(sales float64) bool {
	return sales < i.SalesThreshold
}

func (i CommissionSalesIsLessThan) GetSalesCommissionPercentage() float64 {
	return i.SalesCommissionPercentage
}

func (i CommissionSalesIsLessThan) String() string {
	return fmt.Sprintf("%.0f%% if net sales < $%.2f", i.SalesCommissionPercentage, i.SalesThreshold)
}

type CommissionSalesIsGreaterThanOrEqual struct {
	SalesThreshold            float64
	SalesCommissionPercentage float64
}

func (i CommissionSalesIsGreaterThanOrEqual) IsSatisfied(sales float64) bool {
	return sales >= i.SalesThreshold
}

func (i CommissionSalesIsGreaterThanOrEqual) GetSalesCommissionPercentage() float64 {
	return i.SalesCommissionPercentage
}

func (i CommissionSalesIsGreaterThanOrEqual) String() string {
	return fmt.Sprintf("%.0f%% if net sales >= $%.2f", i.SalesCommissionPercentage, i.SalesThreshold)
}

type CommissionSalesStructure []CommissionSalesItem

func (i CommissionSalesStructure) GetSalesCommissionPercentage(sales float64) (float64, error) {
	for _, structure := range i {
		if structure.IsSatisfied(sales) {
			return structure.GetSalesCommissionPercentage(), nil
		}
	}

	return 0, fmt.Errorf("sales of %.2f did not satify any CommissionSalesItem: %v", sales, i)
}

type CommissionBasedEmployee struct {
	Id                       int
	Name                     string
	CommissionSalesStructure *CommissionSalesStructure
}

type commissionBasedEmployeesTopLineSummary struct {
	FromDate                  time.Time
	ToDate                    time.Time
	Name                      string
	NetSales                  float64
	Tips                      float64
	SalesCommissionPercentage float64
	CashHeld                  []float64
	Taxes                     float64
}

func (s commissionBasedEmployeesTopLineSummary) GetCommission() float64 {
	return s.NetSales * s.SalesCommissionPercentage
}

func (s commissionBasedEmployeesTopLineSummary) GetPretaxPay() float64 {
	return s.GetCommission() + s.Tips
}

func (s commissionBasedEmployeesTopLineSummary) Show() string {
	output := strings.Builder{}

	output.WriteString(fmt.Sprintf("PAY for %s %s - %s\n\n", s.Name, s.FromDate.Format("01/02"), s.ToDate.Format("01/02")))

	commission := s.GetCommission()
	output.WriteString(fmt.Sprintf("Sales: $%.2f * %.0f%% = $%.2f\n", s.NetSales, s.SalesCommissionPercentage*100, commission))
	output.WriteString(fmt.Sprintf("Tips: $%.2f\n", s.Tips))

	preTaxPay := commission + s.Tips
	output.WriteString(fmt.Sprintf("\nPretax Pay: $%.2f\n", preTaxPay))
	output.WriteString(fmt.Sprintf("Taxes: -$%.2f\n", s.Taxes))
	output.WriteString(fmt.Sprintf("Net Pay: $%.2f\n", preTaxPay-s.Taxes))

	output.WriteString("\nCash:\n")

	totalCashHeld := 0.0

	if len(s.CashHeld) == 0 {
		output.WriteString("No cash taken\n")
	} else {
		for _, cash := range s.CashHeld {
			output.WriteString(fmt.Sprintf("  -$%.2f\n", cash))
			totalCashHeld += cash
		}
	}

	output.WriteString(fmt.Sprintf("\nDeposit: -$%.2f\n", commission+s.Tips-s.Taxes-totalCashHeld))

	return output.String()
}

func NewCommissionBasedEmployeesTopLineSummary(fromDate time.Time, toDate time.Time, name string, netSales float64, tips float64, salesCommissionPercentage float64, cashHeld []float64) *commissionBasedEmployeesTopLineSummary {
	s := &commissionBasedEmployeesTopLineSummary{
		FromDate:                  fromDate,
		ToDate:                    toDate,
		Name:                      name,
		NetSales:                  netSales,
		Tips:                      tips,
		CashHeld:                  cashHeld,
		SalesCommissionPercentage: salesCommissionPercentage,
	}

	// todo: taxes should be grabbed before summary is creted
	var taxes float64
	fmt.Printf("Enter taxes for %s with pretax pay $%.2f: \n", name, s.GetPretaxPay())
	if _, err := fmt.Scanln(&taxes); err != nil {
		panic(err)
	}

	if taxes < 0 {
		panic("taxes must be positive")
	}

	s.Taxes = taxes

	return s
}
