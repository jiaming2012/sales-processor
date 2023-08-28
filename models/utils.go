package models

import (
	"strings"
)

func IsDeliveryServiceName(payload string) (bool, string) {
	lower := strings.ToLower(payload)
	if strings.Index(lower, "grubhub") >= 0 {
		return true, "Grubhub"
	} else if strings.Index(lower, "uber eats") >= 0 {
		return true, "Uber Eats"
	} else if strings.Index(lower, "doordash") >= 0 {
		return true, "DoorDash"
	} else {
		return false, ""
	}
}

func IsDeliveryOrder(detail *OrderDetail) (bool, string) {
	return IsDeliveryServiceName(detail.DiningOptions)
}
