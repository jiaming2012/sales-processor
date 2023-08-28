package models

type ThirdPartyMerchantOrders struct {
	Uber     []*OrderDetail
	DoorDash []*OrderDetail
	Grubhub  []*OrderDetail
}
