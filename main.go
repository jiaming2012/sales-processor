package main

import (
	"context"
	"encoding/base64"
	"encoding/csv"
	"fmt"
	"github.com/gocarina/gocsv"
	log "github.com/sirupsen/logrus"
	"google.golang.org/api/option"
	googlesheets "google.golang.org/api/sheets/v4"
	"io/ioutil"
	"jiaming2012/sales-processor/database"
	"jiaming2012/sales-processor/models"
	"jiaming2012/sales-processor/payroll"
	"jiaming2012/sales-processor/service"
	"jiaming2012/sales-processor/service/external"
	"jiaming2012/sales-processor/service/sheets"
	"jiaming2012/sales-processor/sftp"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	exportId                  = "113866"
	sheetsSpreadsheetAllCells = "2:1010"
)

func Marshall(headers []string, row []string, position int) (*models.Sale, error) {
	var item models.Sale
	item.Position = uint(position)

	for i, header := range headers {
		switch header {
		case "Order Id":
			item.OrderId = row[i]
		case "Order #":
			val, err := strconv.Atoi(row[i])
			if err != nil {
				return nil, err
			}
			item.OrderNumber = uint(val)
		case "Sent Date":
			layout := "1/02/06 3:04 PM"
			tt, err := time.Parse(layout, row[i])
			if err != nil {
				return nil, err
			}
			item.Timestamp = tt
		case "Item Id":
			item.ItemId = row[i]
		case "Menu Item":
			item.Name = row[i]
		case "Menu Subgroup(s)":
			item.MenuSubgroups = row[i]
		case "Menu Group":
			item.MenuGroup = row[i]
		case "Menu":
			continue
		case "Sales Category":
			item.SalesCategory = row[i]
		case "Gross RequestedPrice":
			val, err := strconv.ParseFloat(row[i], 64)
			if err != nil {
				return nil, err
			}
			item.GrossPrice = val
		case "Discount":
			val, err := strconv.ParseFloat(row[i], 64)
			if err != nil {
				return nil, err
			}
			item.Discount = val
		case "Net RequestedPrice":
			val, err := strconv.ParseFloat(row[i], 64)
			if err != nil {
				return nil, err
			}
			item.NetPrice = val
		case "Qty":
			val, err := strconv.ParseFloat(row[i], 64)
			if err != nil {
				return nil, err
			}
			item.Quantity = val
		case "Taxes":
			val, err := strconv.ParseFloat(row[i], 64)
			if err != nil {
				return nil, err
			}
			item.Tax = val
		case "Void?":
			val, err := strconv.ParseBool(row[i])
			if err != nil {
				return nil, err
			}
			item.Void = val
		default:
			return nil, fmt.Errorf("unknown header %s", header)
		}
	}

	return &item, nil
}

func setupDB() {
	log.Info("Setting up database ...")
	if err := database.Setup(); err != nil {
		log.Errorf("failed to setup database: %v", err)
		return
	}
	db := database.GetDB()
	defer database.ReleaseDB()

	db.AutoMigrate(&models.Sale{})
}

func getNewFilePath(oldPath string) (string, error) {
	parts := strings.Split(oldPath, "/")
	if len(parts) != 3 {
		return "", fmt.Errorf("unexpected number of parts %d for filepath %s", len(parts), oldPath)
	}

	return fmt.Sprintf("%s/processed/%s", parts[0], parts[2]), nil
}

func iterateDirectory(path string) {
	if fileWalkErr := filepath.Walk(path, func(filePath string, info os.FileInfo, fileErr error) error {
		if fileErr != nil {
			log.Fatalf(fileErr.Error())
		}

		if strings.Index(filePath, ".csv") > 0 {
			if runErr := run(filePath); runErr != nil {
				panic(runErr)
			}

			newPath, err := getNewFilePath(filePath)
			if err != nil {
				panic(err)
			}

			if err = os.Rename(filePath, newPath); err != nil {
				panic(err)
			}
		}

		return nil
	}); fileWalkErr != nil {
		panic(fileWalkErr)
	}
}

func run(filename string) error {
	sales, fileErr := readData(filename)
	if fileErr != nil {
		return fileErr
	}

	db := database.GetDB()
	defer database.ReleaseDB()

	if len(sales) == 0 {
		log.Warn("No sales data found")
		os.Exit(0)
	}

	beginTimestamp := sales[0].Timestamp
	for _, sale := range sales {
		var detailsSaved models.Sale
		tx := db.Where(models.Sale{
			Timestamp: sale.Timestamp,
			Position:  sale.Position,
		}).Find(&detailsSaved)

		if tx.Error != nil {
			return tx.Error
		}

		rowsAffected := tx.RowsAffected

		if rowsAffected == 0 {
			db.Create(&sale)
		}
	}

	salesCount, err := models.FetchTotalSales(beginTimestamp, db)
	if err != nil {
		return err
	}

	if salesCount > int64(len(sales)) {
		if err = models.DeleteSalesAbove(len(sales), beginTimestamp, db); err != nil {
			return err
		}
	}

	log.Infof("Finished processing %s", filename)
	return nil
}

func fetchOrderDetails(date string) []*models.OrderDetail {
	pk, err := ioutil.ReadFile("creds/id_rsa") // required only if private key authentication is to be used
	if err != nil {
		log.Fatalln(err)
	}

	config := sftp.Config{
		Username:   "YumYumsExportUser",
		PrivateKey: string(pk), // required only if private key authentication is to be used
		Server:     "s-9b0f88558b264dfda.server.transfer.us-east-1.amazonaws.com:22",
		Timeout:    time.Second * 30, // 0 for not timeout
	}

	client, err := sftp.New(config)
	if err != nil {
		log.Fatalln(err)
	}
	defer client.Close()

	// Download remote file.
	fName := fmt.Sprintf("/%s/%s/OrderDetails.csv", exportId, date)
	file, err := client.Download(fName)
	if err != nil {
		log.Fatalln(fmt.Errorf("failed to download %v: %w", fName, err))
	}
	defer file.Close()

	var orderDetails []*models.OrderDetail

	if err = gocsv.Unmarshal(file, &orderDetails); err != nil {
		log.Fatal(err)
	}

	return orderDetails
}

func groupOrderDetailsByServer(orderDetails []*models.OrderDetail) map[string][]*models.OrderDetail {
	data := make(map[string][]*models.OrderDetail)

	for _, o := range orderDetails {
		if _, found := data[o.Server]; !found {
			data[o.Server] = make([]*models.OrderDetail, 0)
		}

		data[o.Server] = append(data[o.Server], o)
	}

	return data
}

func ProcessOrderDetails(orderDetails []*models.OrderDetail) models.DailySummary {
	serverDetails := groupOrderDetailsByServer(orderDetails)

	var netSales, totalTaxes, totalTips float64
	employeeDetails := make(map[models.Employee][]*models.OrderDetail)
	for server, details := range serverDetails {
		summary := models.OrderDetails(details).GetSummary()

		netSales += summary.TotalSales
		totalTaxes += summary.TotalTaxes
		totalTips += summary.TotalTips
		employeeDetails[models.Employee(server)] = details
	}

	return models.DailySummary{
		Sales:           netSales,
		Taxes:           totalTaxes,
		Tips:            totalTips,
		EmployeeDetails: employeeDetails,
	}
}

//type TipShare struct {
//	Total
//}
func CalcTipShare(durationWorked time.Duration) int {
	if durationWorked.Hours() >= 6 {
		return 3
	} else if durationWorked.Hours() >= 4 {
		return 2
	} else if durationWorked.Hours() >= 2 {
		return 1
	} else {
		return 0
	}
}

//6+ -> evenly
//4 - 6 -> 66%
//2 - 4 -> 33%
//<2 -> 0%

func CalculateWeeklyReport(weeklyReport map[time.Time]models.DailySummary, timesheet models.Timesheet, employeeHours []models.EmployeeHours) models.WeeklySummary {
	var tipDetails models.TipDetails
	tipDetails.Details = make(map[models.Employee]float64)
	totalSales := 0.0
	totalTaxes := 0.0

	for reportTime, summary := range weeklyReport {
		tipsShare := make(map[models.Employee]int)
		schedule := timesheet[reportTime.Weekday()]

		tipPool := 0
		for employee, shifts := range schedule.Shifts {
			for _, shift := range shifts {
				if shift.IsTipped {
					tips := CalcTipShare(shift.DurationElapsed())
					tipsShare[employee] = tips
					tipPool += tips
				}
			}
		}

		for employee, _ := range schedule.Shifts {
			tipDetails.Details[employee] += (float64(tipsShare[employee]) / float64(tipPool)) * summary.Tips
		}

		totalSales += summary.Sales
		totalTaxes += summary.Taxes
		tipDetails.Total += summary.Tips
	}

	return models.WeeklySummary{
		Tips:  tipDetails,
		Sales: totalSales,
		Taxes: totalTaxes,
		Hours: employeeHours,
	}
}

//type LaborReport []models.EmployeeHours

//func (r LaborReport) Show() string {

//}

func setup(ctx context.Context) (*googlesheets.Service, error) {
	// get bytes from base64 encoded google service accounts key
	credBytes, err := base64.StdEncoding.DecodeString(os.Getenv("KEY_JSON_BASE64"))
	if err != nil {
		return nil, fmt.Errorf("failed to base64 decode KEY_JSON_BASE64: %w", err)
	}

	// create new service using client
	sheetsSrv, err := googlesheets.NewService(ctx, option.WithCredentialsJSON(credBytes))
	if err != nil {
		return nil, fmt.Errorf("unable initiate google sheets client: %w", err)
	}

	return sheetsSrv, nil
}

func main() {
	//
	baseURL := "https://api.getsling.com/v1"
	slingEmail := "jamal@yumyums.kitchen"
	slingPassword := "9@^P9bZR7RGu37zk"
	commissionBasedEmployees := []string{"tanya@yumyums.kitchen"}
	cashWithdrawalResponsesID := "1v3mSj-ZeKcDkplaAZBuva1dVOe7_Hf9O9z2o8YW_zfk"
	exclusions := []models.TipExclusion{
		//{
		//	UserID: 14018513,
		//	Day:    time.Sunday,
		//},
	}

	ctx := context.Background()

	// fetch dates in reporting period
	// todo: we should dump these into a database the next day. Toast only keeps the last 7 days
	dates := service.GetDatesStartingFromPreviousMonday()

	// setup google sheets
	sheetsSrv, err := setup(ctx)
	if err != nil {
		panic(err)
	}

	sheetsClients := sheets.NewClient(sheetsSrv)

	// fetch cash with held
	rows, err := sheetsClients.FetchRows(ctx, cashWithdrawalResponsesID, "Withdrawals", sheetsSpreadsheetAllCells)
	if err != nil {
		panic(err)
	}

	slingClient, err := external.NewSlingTimesheet(baseURL, slingEmail, slingPassword)
	if err != nil {
		panic(err)
	}

	if err = slingClient.PopulateUsers(commissionBasedEmployees); err != nil {
		panic(err)
	}

	fromDate := dates[0].Format("2006-01-02")
	toDate := dates[len(dates)-1].Format("2006-01-02")

	timesheet, err := slingClient.GetPayroll(fromDate, toDate)
	if err != nil {
		panic(err)
	}

	var employeeHours []models.EmployeeHours
	for user, i := range timesheet {
		if user.IsCommissionBasedEmployee {
			log.Debugf("skip summing hours for commission based employee %v", user)
			continue
		}

		hours, err := external.SlingTimesheetItemShifts(i).GetTotalHours()
		if err != nil {
			panic(err)
		}

		employeeHours = append(employeeHours, models.EmployeeHours{
			Employee: user,
			Hours:    hours,
		})
	}

	dailyReport := make(map[time.Time]models.DailySummary)

	fmt.Printf("\n")

	for _, date := range dates {
		fmt.Printf("%s: %s\n", date.Weekday(), date.Format("2006/01/02"))
		fmt.Printf("-----------------------\n")
		orderDetails := fetchOrderDetails(date.Format("20060102"))
		summary := ProcessOrderDetails(orderDetails)
		fmt.Print(summary.Show())
		fmt.Printf("\n")
		fmt.Printf("\n")

		//fmt.Printf("sales tax: $%.2f\n", summary.Taxes)
		//fmt.Printf("total tips: $%.2f\n (C.C. Fee = $%.2f)\n", summary.Tips*0.97, summary.Tips*0.03)
		dailyReport[date] = summary
	}

	//timesheetStub := external.TimesheetStub{}
	//timesheet, err := timesheetStub.FetchTimesheet()
	if err != nil {
		panic(err)
	}

	ts, err := timesheet.FetchTimesheet(exclusions)
	if err != nil {
		panic(err)
	}

	weeklySummary := CalculateWeeklyReport(dailyReport, ts, employeeHours)
	fmt.Println(weeklySummary.Show())
	fmt.Printf("\n")
	fmt.Printf("\n")

	fmt.Println("Cash Held")
	fmt.Println("-----------------------")
	cashWithdrawals, err := rows.ConvertToCashWithdrawals(dates[0], dates[len(dates)-1])
	if err != nil {
		panic(err)
	}

	cash := models.CashWithdrawals(cashWithdrawals)
	for employee, amount := range cash.Sum() {
		fmt.Printf("%v: $%.2f\n", employee, amount)
	}

	// export to csv
	// todo: get rate from Sling
	var entries []payroll.Entry
	for _, empl := range weeklySummary.Hours {
		entries = append(entries, payroll.Entry{
			Type:           payroll.PayItem,
			PayID:          payroll.Regular,
			EmployeeNumber: empl.Employee.EmployeeID,
			HoursAmount:    empl.Hours,
			Rate:           empl.Employee.Rate,
			TreatAsCash:    payroll.RequiresHours,
			CashAmount:     "",
		})

		// todo: make employee conversion less janky
		employee := models.Employee(empl.Employee.Name())
		tip := weeklySummary.Tips.Details[employee]
		if tip > 0 {
			entries = append(entries, payroll.Entry{
				Type:           payroll.PayItem,
				PayID:          payroll.ControlledTips,
				EmployeeNumber: empl.Employee.EmployeeID,
				TreatAsCash:    payroll.DoesNotRequireHours,
				CashAmount:     strconv.FormatFloat(tip, 'f', 2, 64),
			})
		}
	}

	f, err := os.Create(fmt.Sprintf("payroll_%v.csv", toDate))
	if err != nil {
		panic(err)
	}

	if err := payroll.Entries(entries).ToCSV(f); err != nil {
		panic(err)
	}

	//setupDB()
	//iterateDirectory("sales/unprocessed")
	//log.Info("Successfully ran sales processor")
}

func readData(fileName string) ([]*models.Sale, error) {
	f, fileErr := os.Open(fileName)

	if fileErr != nil {
		return nil, fileErr
	}

	defer f.Close()

	r := csv.NewReader(f)

	headers, csvErr := r.Read()
	if csvErr != nil {
		return nil, csvErr
	}

	records, csvErr := r.ReadAll()

	if csvErr != nil {
		return nil, csvErr
	}

	var sales []*models.Sale
	for position, record := range records {
		detail, err := Marshall(headers, record, position)
		if err != nil {
			return nil, err
		}

		sales = append(sales, detail)
	}

	return sales, nil
}
