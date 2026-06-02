package models

type PaymentDetail struct {
	PaymentID        int      `csv:"Payment Id"`
	OrderID          string   `csv:"Order Id"`
	PaidDate         DateTime `csv:"Paid Date"`
	SwipedCardAmount float64  `csv:"Swiped Card Amount"`
	AmountTendered   float64  `csv:"Amount Tendered"`
	Status           string   `csv:"Status"`
	PaymentType      string   `csv:"Type"`
	CardType         string   `csv:"Card Type"`
	OtherType        string   `csv:"Other Type"`
	CardLast4        string   `csv:"Last 4 Card Digits"`
	CardFees         float64  `csv:"V/MC/D Fees"`
	Amount           float64  `csv:"Amount"`
	Tip              float64  `csv:"Tip"`
	Total            float64  `csv:"Total"`
}

type PaymentDetails []PaymentDetail
