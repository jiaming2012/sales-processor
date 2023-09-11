package models

import (
	"strings"
)

func IsDeliveryServiceName(payload string) (bool, ThirdPartyMerchant) {
	lower := strings.ToLower(payload)
	if strings.Index(lower, "grubhub") >= 0 {
		return true, GrubHub
	} else if strings.Index(lower, "uber eats") >= 0 {
		return true, UberEats
	} else if strings.Index(lower, "doordash") >= 0 {
		return true, DoorDash
	} else {
		return false, UnknownMerchant
	}
}

func IsDeliveryOrder(detail *OrderDetail) (bool, ThirdPartyMerchant) {
	return IsDeliveryServiceName(detail.DiningOptions)
}
