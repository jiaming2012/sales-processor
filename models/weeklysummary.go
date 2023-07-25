package models

type TipDetails struct {
	Details map[Employee]float64
	Total   float64
}

type WorkDetails struct {
}

type WeeklySummary struct {
	Sales float64
	Taxes float64
	Tips  TipDetails
}
