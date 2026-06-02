package models

import (
	"fmt"
	"strconv"
	"time"

	log "github.com/sirupsen/logrus"
)

type SlingsUsersDTO struct {
	Users []SlingUserDTO
}

type SlingBaseWage struct {
	Id            int    `json:"id"`
	DateEffective string `json:"dateEffective"`
	RegularRate   string `json:"regularRate"`
}

type SlingWages struct {
	Base []SlingBaseWage `json:"base"`
}

type SlingUserDTO struct {
	ID         int        `json:"id"`
	Type       string     `json:"type"`
	FirstName  string     `json:"name"`
	LastName   string     `json:"lastname"`
	Email      string     `json:"email"`
	HoursCap   int        `json:"hoursCap"`
	Active     bool       `json:"active"`
	EmployeeID *string    `json:"employeeId"`
	Wages      SlingWages `json:"wages"`
}

type SlingUser struct {
	ID                       int    `json:"id"`
	Type                     string `json:"type"`
	FirstName                string `json:"name"`
	LastName                 string `json:"lastname"`
	Email                    string `json:"email"`
	HoursCap                 int    `json:"hoursCap"`
	Active                   bool   `json:"active"`
	CommissionSalesStructure *CommissionSalesStructure
	EmployeeID               int     `json:"employeeId"`
	Rate                     float64 `json:"rate"`
}

func (u *SlingUser) Employee() Employee {
	return Employee(fmt.Sprintf("%s %s", u.FirstName, u.LastName))
}

// ToSlingUser todo: write how to correct in case this happens
func (dto *SlingUserDTO) ToSlingUser(commissionBasedEmployees []CommissionBasedEmployee) (*SlingUser, bool, error) {
	if !dto.Active {
		log.Debugf("ignoring user %v because the user is not active", dto)
		return nil, false, nil
	}

	if dto.EmployeeID == nil {
		return nil, false, fmt.Errorf("user %v %v does not have an employee ID", dto.FirstName, dto.LastName)
	}

	employeeID, err := strconv.Atoi(*dto.EmployeeID)
	if err != nil {
		return nil, false, fmt.Errorf("failed to parse employeeID: %w", err)
	}

	var commissionSalesStructure *CommissionSalesStructure
	for _, commissionBasedEmployee := range commissionBasedEmployees {
		if employeeID == commissionBasedEmployee.Id {
			commissionSalesStructure = commissionBasedEmployee.CommissionSalesStructure
			break
		}
	}

	if len(dto.Wages.Base) == 0 {
		return nil, false, fmt.Errorf("wage not set in sling for %s %s. Found 0 wages", dto.FirstName, dto.LastName)
	}

	var wage float64
	previousWageDateEffective := time.Time{}
	for _, baseWage := range dto.Wages.Base {
		// convert baseWage to time.Time
		dateEffective, err := time.Parse("2006-01-02", baseWage.DateEffective)
		if err != nil {
			return nil, false, fmt.Errorf("failed to parse dateEffective: %w", err)
		}

		if dateEffective.After(previousWageDateEffective) {
			wage, err = strconv.ParseFloat(baseWage.RegularRate, 64)
			if err != nil {
				return nil, false, fmt.Errorf("failed to parse wage: %w", err)
			}

			previousWageDateEffective = dateEffective
		}
	}

	if wage == 0 {
		return nil, false, fmt.Errorf("expected to find a wage for %s %s. Found %v", dto.FirstName, dto.LastName, wage)
	}

	return &SlingUser{
		ID:                       dto.ID,
		Type:                     dto.Type,
		FirstName:                dto.FirstName,
		LastName:                 dto.LastName,
		Email:                    dto.Email,
		HoursCap:                 dto.HoursCap,
		Active:                   dto.Active,
		EmployeeID:               employeeID,
		CommissionSalesStructure: commissionSalesStructure,
		Rate:                     wage,
	}, true, nil
}

func (u *SlingUser) Name() string {
	return fmt.Sprintf("%s %s", u.FirstName, u.LastName)
}
