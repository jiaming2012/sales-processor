package models

import (
	"fmt"
	"log"
	"math"
	"strings"
	"time"
)

type CommissionSalesItem interface {
	IsSatisfied(sales float64) bool
	GetSalesCommissionPercentage(weeklySummary *WeeklySummary) float64
}

type CommissionSalesIsLessThan struct {
	SalesThreshold            float64
	SalesCommissionPercentage float64
}

func (i CommissionSalesIsLessThan) IsSatisfied(sales float64) bool {
	return sales < i.SalesThreshold
}

func CalculateSalesCommissionPercentage(weeklySummary *WeeklySummary) float64 {
	sales := weeklySummary.Sales
	hourlyWorkersComp := weeklySummary.TotalHourlyWorkersExpense()

	splitNumerator := (sales - hourlyWorkersComp) / 2.75
	return splitNumerator / sales
}

func (i CommissionSalesIsLessThan) GetSalesCommissionPercentage(weeklySummary *WeeklySummary) float64 {
	return CalculateSalesCommissionPercentage(weeklySummary)
}

func (i CommissionSalesIsLessThan) String() string {
	return fmt.Sprintf("%.0f%% if net sales < $%.2f", i.SalesCommissionPercentage, i.SalesThreshold)
}

type CommissionSalesIsGreaterThanOrEqual struct {
	SalesThreshold            float64
	SalesCommissionPercentage float64
}

func (i CommissionSalesIsGreaterThanOrEqual) IsSatisfied(sales float64) bool {
	log.Fatalf("Congrats on reaching the milestone of $%v weekly sales. Please implement a new compensation policy for this level.", i.SalesThreshold)
	return sales >= i.SalesThreshold
}

func (i CommissionSalesIsGreaterThanOrEqual) GetSalesCommissionPercentage(weeklySummary *WeeklySummary) float64 {
	return i.SalesCommissionPercentage
}

func (i CommissionSalesIsGreaterThanOrEqual) String() string {
	return fmt.Sprintf("%.0f%% if net sales >= $%.2f", i.SalesCommissionPercentage, i.SalesThreshold)
}

type CommissionSalesStructure []CommissionSalesItem

func (i CommissionSalesStructure) GetSalesCommissionPercentage(weeklySummary *WeeklySummary) (float64, error) {
	sales := weeklySummary.Sales
	for _, structure := range i {
		if structure.IsSatisfied(sales) {
			return structure.GetSalesCommissionPercentage(weeklySummary), nil
		}
	}

	return 0, fmt.Errorf("sales of %.2f did not satify any CommissionSalesItem: %v", sales, i)
}

type CommissionBasedEmployee struct {
	Id                       int
	Name                     string
	CommissionSalesStructure *CommissionSalesStructure
	// IsOwner marks the company owner; owners are tracked through the
	// commission flow only to suppress them from the hourly section, but
	// are excluded from labor-cost aggregation and the per-employee
	// commission breakdown (they pay themselves outside payroll).
	IsOwner bool
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
	CashTendered              float64
	RentHold                  float64
}

func (s commissionBasedEmployeesTopLineSummary) GetCommission() float64 {
	return s.NetSales * s.SalesCommissionPercentage
}

func (s commissionBasedEmployeesTopLineSummary) GetPretaxPay() float64 {
	return s.GetCommission() + s.Tips
}

func (s commissionBasedEmployeesTopLineSummary) GetBasePay() float64 {
	basePay := 300.0
	commission := s.GetCommission()

	if commission > basePay {
		diff := commission - basePay
		basePay -= diff
	}

	return math.Max(basePay, 0.0)
}

func (s commissionBasedEmployeesTopLineSummary) GetNetPay() float64 {
	return s.GetBasePay() + s.GetCommission() + s.Tips - s.Taxes
}

// GetDeposit is the amount actually owed to the employee once the rent
// hold is removed from net pay — the "Deposit" line on the report and
// the amount wired out via the Mercury deposit leg.
func (s commissionBasedEmployeesTopLineSummary) GetDeposit() float64 {
	return s.GetNetPay() - s.RentHold
}

func (s commissionBasedEmployeesTopLineSummary) Show() string {
	output := strings.Builder{}

	output.WriteString(fmt.Sprintf("PAY for %s %s - %s\n\n", s.Name, s.FromDate.Format("01/02"), s.ToDate.Format("01/02")))

	commission := s.GetCommission()
	basePay := s.GetBasePay()

	output.WriteString(fmt.Sprintf("Base Pay: $%.2f\n", basePay))

	output.WriteString(fmt.Sprintf("\nSales: $%.2f * %.0f%% = $%.2f\n", s.NetSales, s.SalesCommissionPercentage*100, commission))
	output.WriteString(fmt.Sprintf("Tips: $%.2f\n", s.Tips))

	preTaxPay := basePay + commission + s.Tips
	output.WriteString(fmt.Sprintf("\nPretax Pay: $%.2f\n", preTaxPay))
	output.WriteString(fmt.Sprintf("Taxes: -$%.2f\n", s.Taxes))
	output.WriteString(fmt.Sprintf("\nNet Pay: $%.2f\n", s.GetNetPay()))

	if s.RentHold > 0 {
		output.WriteString(fmt.Sprintf("\nRent Hold: -$%.2f\n", s.RentHold))
	}

	output.WriteString(fmt.Sprintf("\nDeposit: $%.2f\n", s.GetDeposit()))

	return output.String()
}

func NewCommissionBasedEmployeesTopLineSummary(fromDate time.Time, toDate time.Time, name string, netSales float64, tips float64, salesCommissionPercentage float64, cashHeld []float64, cashTendered float64, rentHold float64, employeeTaxRate float64) *commissionBasedEmployeesTopLineSummary {
	s := &commissionBasedEmployeesTopLineSummary{
		FromDate:                  fromDate,
		ToDate:                    toDate,
		Name:                      name,
		NetSales:                  netSales,
		Tips:                      tips,
		CashHeld:                  cashHeld,
		SalesCommissionPercentage: salesCommissionPercentage,
		CashTendered:              cashTendered,
		RentHold:                  rentHold,
	}

	s.Taxes = s.GetPretaxPay() * employeeTaxRate

	return s
}
