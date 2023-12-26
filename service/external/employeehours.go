package external

import (
	"bytes"
	"encoding/json"
	"fmt"
	"jiaming2012/sales-processor/models"
	"net/http"
	"time"

	log "github.com/sirupsen/logrus"
)

type NetworkCalls interface {
	FetchTimesheet() (models.Timesheet, error)
}

type TimesheetStub struct{}

type SlingItemID struct {
	ID int `json:"id"`
}

type SlingTimesheetItemDTO struct {
	User        SlingItemID                   `json:"user"`
	Position    SlingItemID                   `json:"position"`
	Projections []SlingTimesheetProjectionDTO `json:"timesheetProjections"`
}

type SlingTimesheetProjectionDTO struct {
	ClockIn      time.Time `json:"clockIn"`
	ClockOut     time.Time `json:"clockOut"`
	Status       *string   `json:"status"`
	BreakMinutes int       `json:"breakMinutes"`
	PaidMinutes  int       `json:"paidMinutes"`
}

func (dto *SlingTimesheetItemDTO) ConvertToSlingTimesheetItemShift() (*SlingTimesheetItemShift, error) {
	isApproved := false

	if len(dto.Projections) != 1 {
		return nil, fmt.Errorf("expected number of timesheet projections to be one. Found %v", len(dto.Projections))
	}

	timesheet := dto.Projections[0]

	if timesheet.Status != nil && *timesheet.Status == "approved" {
		isApproved = true
	}

	return &SlingTimesheetItemShift{
		ClockIn:    timesheet.ClockIn,
		ClockOut:   timesheet.ClockOut,
		IsApproved: isApproved,
		Hours:      float64(timesheet.PaidMinutes) / 60.0,
	}, nil
}

type SlingTimesheetItemShift struct {
	ClockIn    time.Time
	ClockOut   time.Time
	IsApproved bool
	Hours      float64
}

type SlingTimesheetItemShifts []SlingTimesheetItemShift

func (stubs SlingTimesheetItemShifts) GetTotalHours() (float64, error) {
	total := 0.0

	for _, stub := range stubs {
		if !stub.IsApproved {
			return 0, fmt.Errorf("user contains an unapproved shift")
		}

		total += stub.Hours
	}

	return total, nil
}

type SlingPayroll map[models.SlingUser][]SlingTimesheetItemShift

type slingTimesheetClient struct {
	baseURL string
	authKey string
	users   map[int]models.SlingUser
}

func (c *slingTimesheetClient) GetPayroll(fromDate string, toDate string) (SlingPayroll, error) {
	timesheetURL := fmt.Sprintf("%s/reports/timesheets?dates=%sT00:00:00Z/%sT23:59:59Z", c.baseURL, fromDate, toDate)

	client := &http.Client{}
	req, err := http.NewRequest("GET", timesheetURL, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", c.authKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var itemsDTO []SlingTimesheetItemDTO

	if err = json.NewDecoder(resp.Body).Decode(&itemsDTO); err != nil {
		return nil, fmt.Errorf("json decode failure for SlingTimesheetItemDTO: %w", err)
	}

	slingPayroll := make(SlingPayroll)
	userIDCache := make(map[int]struct{})
	for _, dto := range itemsDTO {
		if len(dto.Projections) == 0 {
			log.Debugf("skipping %v because it does not have any projections", dto.User)
			continue
		}

		itemShift, convErr := dto.ConvertToSlingTimesheetItemShift()

		if convErr != nil {
			return nil, fmt.Errorf("failed to convert user id=%v: %w", dto.User, convErr)
		}

		if itemShift.Hours == 0 {
			continue
		}

		user, ok := c.users[dto.User.ID]
		if !ok {
			return nil, fmt.Errorf("failed to find user with user.id=%v", dto.User.ID)
		}

		if !itemShift.IsApproved {
			if user.CommissionSalesStructure != nil {
				log.Debugf("surpressing error: commission based employee, %v, is allowed to have unapproved shift %v -> %v", user, itemShift.ClockIn, itemShift.ClockOut)
			} else {
				return nil, fmt.Errorf("unapproved shift found for %v from %v -> %v", user.Name(), itemShift.ClockIn, itemShift.ClockOut)
			}
		}

		if _, found := userIDCache[dto.User.ID]; !found {
			// make singleton list
			slingPayroll[user] = []SlingTimesheetItemShift{
				*itemShift,
			}

			// update the cache
			userIDCache[dto.User.ID] = struct{}{}
		} else {
			slingPayroll[user] = append(slingPayroll[user], *itemShift)
		}
	}

	return slingPayroll, nil
}

func (c *slingTimesheetClient) PopulateUsers(commissionBasedEmployees []models.CommissionBasedEmployee) error {
	usersURL := fmt.Sprintf("%s/users/concise", c.baseURL)

	c.users = make(map[int]models.SlingUser)

	client := &http.Client{}
	req, err := http.NewRequest("GET", usersURL, nil)
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", c.authKey)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var slingDTO models.SlingsUsersDTO
	if err = json.NewDecoder(resp.Body).Decode(&slingDTO); err != nil {
		return err
	}

	for _, dto := range slingDTO.Users {
		user, found, dtoErr := dto.ToSlingUser(commissionBasedEmployees)
		if dtoErr != nil {
			return fmt.Errorf("failed to convert dto: %w", dtoErr)
		}

		if found {
			c.users[user.ID] = *user
		}
	}

	return nil
}

func (c *slingTimesheetClient) initiate(email string, password string) error {
	loginURL := fmt.Sprintf("%s/account/login", c.baseURL)

	postBody, _ := json.Marshal(map[string]string{
		"email":    email,
		"password": password,
	})

	responseBody := bytes.NewBuffer(postBody)
	resp, err := http.Post(loginURL, "application/json", responseBody)
	if err != nil {
		return err
	}

	authHeaders := resp.Header.Values("Authorization")
	authHeaders = []string{"cabf4df3cf2642d293bb956ac328afd9"}
	if len(authHeaders) == 0 {
		return fmt.Errorf("failed to login: count not find auth key in headers")
	}

	c.authKey = authHeaders[0]

	return nil
}

func NewSlingTimesheet(baseURL string, email string, password string) (*slingTimesheetClient, error) {
	client := &slingTimesheetClient{
		baseURL: baseURL,
	}

	if err := client.initiate(email, password); err != nil {
		return nil, err
	}

	return client, nil
}

func (p SlingPayroll) FetchTimesheet(tipExclusions []models.TipExclusion) (models.Timesheet, error) {
	timesheet := make(models.Timesheet)

	for user, shifts := range p {
		for _, shift := range shifts {
			weekday := shift.ClockIn.Weekday()
			employee := models.Employee(user.Name())

			// todo: remove shift object
			s := models.Shift{
				Start:    shift.ClockIn,
				End:      shift.ClockOut,
				IsTipped: true,
			}

			// todo: add role to allow changing tips
			for _, exclusion := range tipExclusions {
				if user.EmployeeID == exclusion.EmployeeID && weekday == exclusion.Day {
					s.IsTipped = false
				}
			}

			timesheet.Add(weekday, employee, s)
		}
	}

	return timesheet, nil
}

func (e TimesheetStub) FetchTimesheet() (models.Timesheet, error) {
	return models.Timesheet{
		time.Thursday: models.Schedule{
			Shifts: map[models.Employee][]models.Shift{
				"Latanya Mcgriff": {
					{
						Start: time.Date(2023, time.June, 15, 12, 0, 0, 0, time.Local),
						End:   time.Date(2023, time.June, 15, 20, 0, 0, 0, time.Local),
					},
				},
				"Rashid Blackmon": {
					{
						Start: time.Date(2023, time.June, 15, 12, 0, 0, 0, time.Local),
						End:   time.Date(2023, time.June, 15, 17, 0, 0, 0, time.Local),
					},
				},
			},
		},
		time.Friday: models.Schedule{
			Shifts: map[models.Employee][]models.Shift{
				"Latanya Mcgriff": {
					{
						Start: time.Date(2023, time.June, 15, 12, 0, 0, 0, time.Local),
						End:   time.Date(2023, time.June, 15, 20, 0, 0, 0, time.Local),
					},
				},
				"Rashid Blackmon": {
					{
						Start: time.Date(2023, time.June, 15, 12, 0, 0, 0, time.Local),
						End:   time.Date(2023, time.June, 15, 17, 0, 0, 0, time.Local),
					},
				},
			},
		},
		time.Saturday: models.Schedule{
			Shifts: map[models.Employee][]models.Shift{
				"Latanya Mcgriff": {
					{
						Start: time.Date(2023, time.June, 16, 12, 0, 0, 0, time.Local),
						End:   time.Date(2023, time.June, 16, 20, 0, 0, 0, time.Local),
					},
				},
				"Rashid Blackmon": {
					{
						Start: time.Date(2023, time.June, 16, 12, 0, 0, 0, time.Local),
						End:   time.Date(2023, time.June, 16, 17, 0, 0, 0, time.Local),
					},
				},
				"Benjamin Daniels": {
					{
						Start: time.Date(2023, time.June, 17, 11, 41, 0, 0, time.Local),
						End:   time.Date(2023, time.June, 17, 19, 0, 0, 0, time.Local),
					},
				},
			},
		},
		time.Sunday: models.Schedule{
			Shifts: map[models.Employee][]models.Shift{
				"Latanya Mcgriff": {
					{
						Start: time.Date(2023, time.June, 16, 12, 0, 0, 0, time.Local),
						End:   time.Date(2023, time.June, 16, 20, 0, 0, 0, time.Local),
					},
				},
				"Rashid Blackmon": {
					{
						Start: time.Date(2023, time.June, 16, 12, 0, 0, 0, time.Local),
						End:   time.Date(2023, time.June, 16, 17, 0, 0, 0, time.Local),
					},
				},
				"Benjamin Daniels": {
					{
						Start: time.Date(2023, time.June, 18, 11, 47, 0, 0, time.Local),
						End:   time.Date(2023, time.June, 18, 18, 51, 0, 0, time.Local),
					},
				},
			},
		},
	}, nil
}
