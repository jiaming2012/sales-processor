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

// Free-form Sling group tag names recognised by this project.
const (
	TagCommission       = "commission"
	TagOwner            = "owner"
	TagPrimarySchedule  = "primary pay schedule"
	PrimaryScheduleAge  = 3 // months of tenure expected before primary schedule
)

type SlingUserDTO struct {
	ID         int        `json:"id"`
	Type       string     `json:"type"`
	FirstName  string     `json:"name"`
	LastName   string     `json:"lastname"`
	Email      string     `json:"email"`
	HoursCap   int        `json:"hoursCap"`
	Active     bool       `json:"active"`
	EmployeeID *string    `json:"employeeId"`
	HireDate   *string    `json:"hireDate"`
	GroupIDs   []int      `json:"groupIds"`
	Wages      SlingWages `json:"wages"`
}

type SlingUser struct {
	ID         int       `json:"id"`
	Type       string    `json:"type"`
	FirstName  string    `json:"name"`
	LastName   string    `json:"lastname"`
	Email      string    `json:"email"`
	HoursCap   int       `json:"hoursCap"`
	Active     bool      `json:"active"`
	EmployeeID int       `json:"employeeId"`
	Rate       float64   `json:"rate"`
	HireDate   time.Time `json:"hireDate"`
	// Tags holds the names of any free-form Sling groups (type="group") the
	// user belongs to; assigned by the caller after looking up groupIds.
	Tags []string
}

func (u *SlingUser) Employee() Employee {
	return Employee(fmt.Sprintf("%s %s", u.FirstName, u.LastName))
}

// ToSlingUser converts the raw DTO into a SlingUser. tags is the resolved
// list of free-form Sling group names (type="group") the user belongs to;
// the caller looks these up from groupIds before calling.
func (dto *SlingUserDTO) ToSlingUser(tags []string) (*SlingUser, bool, error) {
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

	// Commission/owner users are paid via commission or pay themselves
	// outside payroll, so they may legitimately have no base wage in Sling.
	isCommissionOrOwner := false
	for _, t := range tags {
		if t == TagCommission || t == TagOwner {
			isCommissionOrOwner = true
			break
		}
	}

	var wage float64
	if !isCommissionOrOwner {
		if len(dto.Wages.Base) == 0 {
			return nil, false, fmt.Errorf("wage not set in sling for %s %s. Found 0 wages", dto.FirstName, dto.LastName)
		}

		previousWageDateEffective := time.Time{}
		for _, baseWage := range dto.Wages.Base {
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
	}

	var hireDate time.Time
	if dto.HireDate != nil && *dto.HireDate != "" {
		hireDate, err = time.Parse("2006-01-02", *dto.HireDate)
		if err != nil {
			return nil, false, fmt.Errorf("failed to parse hireDate for %s %s: %w", dto.FirstName, dto.LastName, err)
		}
	}

	return &SlingUser{
		ID:         dto.ID,
		Type:       dto.Type,
		FirstName:  dto.FirstName,
		LastName:   dto.LastName,
		Email:      dto.Email,
		HoursCap:   dto.HoursCap,
		Active:     dto.Active,
		EmployeeID: employeeID,
		Rate:       wage,
		HireDate:   hireDate,
		Tags:       tags,
	}, true, nil
}

// HasTag reports whether the user belongs to the named free-form Sling group.
func (u *SlingUser) HasTag(name string) bool {
	for _, t := range u.Tags {
		if t == name {
			return true
		}
	}
	return false
}

// IsPrimarySchedule reports whether the user is on the primary pay schedule
// (paid in the same cycle they work). Untagged users are on the new-employee
// schedule by default (pay held one cycle).
func (u *SlingUser) IsPrimarySchedule() bool {
	return u.HasTag(TagPrimarySchedule)
}

// TenureAtLeastMonths reports whether the user's tenure as of asOf is at
// least months months. Returns false when HireDate is unset.
func (u *SlingUser) TenureAtLeastMonths(asOf time.Time, months int) bool {
	if u.HireDate.IsZero() {
		return false
	}
	return !asOf.Before(u.HireDate.AddDate(0, months, 0))
}

// Tenure renders the user's length of employment as of asOf. Under one
// year it reads as "N month(s)" (e.g. "1 month", "7 months") since the
// year value would just be "0y" noise; at one year or more it reads as
// "Xy Ym" (e.g. "2y 11m"). Returns an empty string if HireDate is unset.
func (u *SlingUser) Tenure(asOf time.Time) string {
	if u.HireDate.IsZero() {
		return ""
	}
	years := asOf.Year() - u.HireDate.Year()
	months := int(asOf.Month()) - int(u.HireDate.Month())
	if asOf.Day() < u.HireDate.Day() {
		months--
	}
	if months < 0 {
		years--
		months += 12
	}
	if years < 0 {
		return ""
	}
	if years == 0 {
		if months == 0 {
			days := int(asOf.Sub(u.HireDate).Hours() / 24)
			if days <= 0 {
				return "today"
			}
			if days == 1 {
				return "1 day"
			}
			return fmt.Sprintf("%d days", days)
		}
		if months == 1 {
			return "1 month"
		}
		return fmt.Sprintf("%d months", months)
	}
	return fmt.Sprintf("%dy %dm", years, months)
}

func (u *SlingUser) Name() string {
	return fmt.Sprintf("%s %s", u.FirstName, u.LastName)
}
