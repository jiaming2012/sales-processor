package models

type ThirdPartyMerchant string

const (
	UberEats        ThirdPartyMerchant = "Uber Eats"
	GrubHub         ThirdPartyMerchant = "Grubhub"
	DoorDash        ThirdPartyMerchant = "DoorDash"
	UnknownMerchant ThirdPartyMerchant = "Unknown"
)
