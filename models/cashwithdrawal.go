package models

type CashWithdrawal struct {
	Employee Employee
	Amount   float64
}

type CashWithdrawals []CashWithdrawal

func (withdrawals *CashWithdrawals) Sum() map[Employee]float64 {
	result := make(map[Employee]float64)

	for _, w := range *withdrawals {
		if _, found := result[w.Employee]; found {
			result[w.Employee] += w.Amount
		} else {
			result[w.Employee] = w.Amount
		}
	}

	return result
}
