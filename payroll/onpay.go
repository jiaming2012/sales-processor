package payroll

import (
	"github.com/gocarina/gocsv"
	"os"
)

type ItemType uint

const (
	PayItem ItemType = 1
)

type PayID uint

const (
	Regular        PayID = 1
	Overtime             = 2
	ControlledTips       = 208
)

type TreatAsCash string

const (
	RequiresHours       TreatAsCash = ""
	DoesNotRequireHours             = "1"
)

type Entry struct {
	Type           ItemType    `csv:"type"`
	PayID          PayID       `csv:"id"`
	EmployeeNumber int         `csv:"emp_num"`
	HoursAmount    float64     `csv:"hours"`
	Rate           float64     `csv:"rate"`
	TreatAsCash    TreatAsCash `csv:"treat_as_cash"`
	CashAmount     string      `csv:"cash_amount"`
}

type Entries []Entry

func (entries Entries) ToCSV(file *os.File) error {
	return gocsv.MarshalFile(entries, file)
}
