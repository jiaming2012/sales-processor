package models

import "fmt"

type SlingUser struct {
	ID                        int    `json:"id"`
	Type                      string `json:"type"`
	FirstName                 string `json:"name"`
	LastName                  string `json:"lastname"`
	Email                     string `json:"email"`
	HoursCap                  int    `json:"hoursCap"`
	Active                    bool   `json:"active"`
	IsCommissionBasedEmployee bool   `json:"IsCommissionBasedEmployee"`
}

func (u *SlingUser) Name() string {
	return fmt.Sprintf("%s %s", u.FirstName, u.LastName)
}
