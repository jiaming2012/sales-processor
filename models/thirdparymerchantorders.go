package models

type ThirdPartyMerchantOrders map[ThirdPartyMerchant][]*OrderDetail

func (o *ThirdPartyMerchantOrders) Add(merchant ThirdPartyMerchant, orderDetail *OrderDetail) {
	(*o)[merchant] = append((*o)[merchant], orderDetail)
}

func (o *ThirdPartyMerchantOrders) BulkAdd(merchant ThirdPartyMerchant, orderDetails []*OrderDetail) {
	(*o)[merchant] = append((*o)[merchant], orderDetails...)
}

func (o *ThirdPartyMerchantOrders) AddThirdPartyMerchantOrders(orders ThirdPartyMerchantOrders) {
	for k, v := range orders {
		o.BulkAdd(k, v)
	}
}

/*
orders := make(map[ThirdPartyMerchant][]*OrderDetail)

	for _, o := range thirdPartyMerchantOrders.Uber {
		orders[UberEats] = append(orders[UberEats], o)
	}

	for _, o := range thirdPartyMerchantOrders.DoorDash {
		orders[DoorDash] = append(orders[DoorDash], o)
	}

	for _, o := range thirdPartyMerchantOrders.Grubhub {
		orders[GrubHub] = append(orders[GrubHub], o)
	}

	return orders
*/
