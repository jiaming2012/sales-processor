package models

type Server string

func (s Server) IsDeliveryService() bool {
	result, _ := IsDeliveryServiceName(string(s))
	return result
}
