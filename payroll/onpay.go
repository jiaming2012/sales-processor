package payroll

type ItemType uint

const (
	PayItem ItemType = 1
)

type PayID uint

const (
	Regular PayID = 1
	Overtime
)

type Entry struct {
	Type           ItemType
	PayID          PayID
	EmployeeNumber int
	HoursAmount    uint
	Rate           float64
	TreatAsCash    bool
	CashAmount     float64
}
