package external

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"

	"jiaming2012/sales-processor/models"
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

func (dto *SlingTimesheetItemDTO) ConvertToSlingTimesheetItemShift() ([]*SlingTimesheetItemShift, error) {
	var shifts []*SlingTimesheetItemShift

	for _, proj := range dto.Projections {
		isApproved := false
		if proj.Status != nil && *proj.Status == "approved" {
			isApproved = true
		}

		shifts = append(shifts, &SlingTimesheetItemShift{
			ClockIn:    proj.ClockIn,
			ClockOut:   proj.ClockOut,
			IsApproved: isApproved,
			Hours:      float64(proj.PaidMinutes) / 60.0,
		})
	}

	return shifts, nil
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

// SlingPayrollEntry bundles a Sling user with the shifts attributed to them
// in a given pay period; used as the value type in SlingPayroll because
// SlingUser is no longer comparable (it carries a Tags slice).
type SlingPayrollEntry struct {
	User   models.SlingUser
	Shifts []SlingTimesheetItemShift
}

type SlingPayroll map[int]SlingPayrollEntry

type slingTimesheetClient struct {
	baseURL string
	authKey string
	users   map[int]models.SlingUser
	// groups maps free-form Sling group IDs (type="group") to their names;
	// position/location/everyone groups are not stored here.
	groups map[int]string
}

// Users returns the populated set of active Sling users, in unspecified order.
func (c *slingTimesheetClient) Users() []models.SlingUser {
	out := make([]models.SlingUser, 0, len(c.users))
	for _, u := range c.users {
		out = append(out, u)
	}
	return out
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
	for _, dto := range itemsDTO {
		if len(dto.Projections) == 0 {
			log.Debugf("skipping %v because it does not have any projections", dto.User)
			continue
		}

		itemShifts, convErr := dto.ConvertToSlingTimesheetItemShift()

		if convErr != nil {
			return nil, fmt.Errorf("failed to convert user id=%v: %w", dto.User, convErr)
		}

		for _, itemShift := range itemShifts {
			if itemShift.Hours == 0 {
				continue
			}

			user, ok := c.users[dto.User.ID]
			if !ok {
				log.Infof("failed to find user with user.id=%v, skipping ...", dto.User.ID)
				continue
			}

			if !itemShift.IsApproved {
				if user.HasTag(models.TagCommission) {
					log.Debugf("surpressing error: commission based employee, %v, is allowed to have unapproved shift %v -> %v", user, itemShift.ClockIn, itemShift.ClockOut)
				} else {
					return nil, fmt.Errorf("unapproved shift found for %v from %v -> %v", user.Name(), itemShift.ClockIn, itemShift.ClockOut)
				}
			}

			entry, found := slingPayroll[dto.User.ID]
			if !found {
				entry = SlingPayrollEntry{User: user}
			}
			entry.Shifts = append(entry.Shifts, *itemShift)
			slingPayroll[dto.User.ID] = entry
		}
	}

	return slingPayroll, nil
}

// fetchGroups populates c.groups with the names of free-form Sling groups
// (type="group"); position/location/everyone groups are intentionally
// dropped since they aren't used as user tags by this project.
func (c *slingTimesheetClient) fetchGroups() error {
	groupsURL := fmt.Sprintf("%s/groups", c.baseURL)

	req, err := http.NewRequest("GET", groupsURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", c.authKey)

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to fetch groups: %v", resp.Status)
	}

	var groups []struct {
		ID   int    `json:"id"`
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&groups); err != nil {
		return fmt.Errorf("json decode failure for sling groups: %w", err)
	}

	c.groups = make(map[int]string, len(groups))
	for _, g := range groups {
		if g.Type == "group" {
			c.groups[g.ID] = g.Name
		}
	}
	return nil
}

func (c *slingTimesheetClient) PopulateUsers() error {
	if err := c.fetchGroups(); err != nil {
		return fmt.Errorf("failed to fetch sling groups: %w", err)
	}

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

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to fetch users: %v", resp.Status)
	}

	var slingDTO models.SlingsUsersDTO
	if err = json.NewDecoder(resp.Body).Decode(&slingDTO); err != nil {
		return err
	}

	for _, dto := range slingDTO.Users {
		var tags []string
		for _, gid := range dto.GroupIDs {
			if name, ok := c.groups[gid]; ok {
				tags = append(tags, name)
			}
		}

		user, found, dtoErr := dto.ToSlingUser(tags)
		if dtoErr != nil {
			fmt.Printf("ERROR: %v: skip user? (y/n)\n", dtoErr)
			var skip string
			fmt.Scanln(&skip)

			if strings.ToLower(skip) == "y" {
				continue
			}

			return fmt.Errorf("failed to convert dto: %w", dtoErr)
		}

		if found {
			c.users[user.ID] = *user
		}
	}

	var missingHireDate []string
	for _, u := range c.users {
		if u.HireDate.IsZero() {
			missingHireDate = append(missingHireDate, u.Name())
		}
	}
	if len(missingHireDate) > 0 {
		sort.Strings(missingHireDate)
		return fmt.Errorf("missing hireDate in Sling for %d employee(s); backfill in Sling and retry:\n  - %s",
			len(missingHireDate), strings.Join(missingHireDate, "\n  - "))
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

	for _, entry := range p {
		user := entry.User
		for _, shift := range entry.Shifts {
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
