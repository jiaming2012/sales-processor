package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"flag"
	"encoding/csv"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"
	"github.com/gocarina/gocsv"
	"github.com/joho/godotenv"
	log "github.com/sirupsen/logrus"
	"google.golang.org/api/option"
	googlesheets "google.golang.org/api/sheets/v4"

	"jiaming2012/sales-processor/database"
	"jiaming2012/sales-processor/internal/classify"
	"jiaming2012/sales-processor/models"
	"jiaming2012/sales-processor/payroll"
	"jiaming2012/sales-processor/service"
	"jiaming2012/sales-processor/service/external"
	"jiaming2012/sales-processor/service/transferledger"
	"jiaming2012/sales-processor/sftp"
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

func fetchToastCSVReports(date string) []*models.OrderDetail {
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

	var orderDetails []*models.OrderDetail
	var paymentDetails []*models.PaymentDetail

	// SFTP transfers can drop mid-read ("connection lost"); retry each
	// file this many times (reconnecting between attempts) before giving up.
	const sftpReadAttempts = 4

	for _, localFileName := range []string{"OrderDetails.csv", "AllItemsReport.csv", "AccountingReport.xls", "ItemSelectionDetails.csv", "ModifiersSelectionDetails.csv", "PaymentDetails.csv", "TimeEntries.csv"} {
		// Download remote file.
		remoteFileName := fmt.Sprintf("/%s/%s/%s", exportId, date, localFileName)
		localFilePath := fmt.Sprintf("output/toast_reports/%s", date)
		cachedFilePath := fmt.Sprintf("%s/%s", localFilePath, localFileName)

		// Toast's SFTP only retains the last ~7 days. For older periods
		// (--weeks-ago >= 1) the download fails — fall back to the local
		// cache from a prior run so historical reports stay accurate.
		var bytes []byte
		file, err := client.Download(remoteFileName)
		if err != nil {
			cached, readErr := os.ReadFile(cachedFilePath)
			if readErr != nil {
				log.Debugf("skip %s: SFTP unavailable (%v) and no local cache at %s", remoteFileName, err, cachedFilePath)
				continue
			}
			log.Infof("using cached %s (SFTP returned: %v)", cachedFilePath, err)
			// Historical caches written before the O_TRUNC fix have
			// duplicated headers and data rows from O_APPEND. Each Toast
			// Order/Payment row has a unique ID, so byte-identical lines
			// can only come from append-mode duplication — safe to dedup.
			bytes = dedupCSVLines(cached)
		} else {
			// ReadAll can fail mid-transfer if the SSH session drops
			// ("connection lost"). Retry with a fresh handle — client.Download
			// calls connect(), which re-establishes a dead session before the
			// next read. Only a persistent failure is fatal.
			bytes, err = ioutil.ReadAll(file)
			for retry := 1; err != nil && retry < sftpReadAttempts; retry++ {
				file.Close()
				log.Warnf("read %s dropped (%v) — reconnecting, retry %d/%d", remoteFileName, err, retry, sftpReadAttempts-1)
				time.Sleep(time.Duration(retry) * 2 * time.Second)
				if file, err = client.Download(remoteFileName); err != nil {
					break
				}
				bytes, err = ioutil.ReadAll(file)
			}
			if err != nil {
				if file != nil {
					file.Close()
				}
				log.Fatalf("failed to read bytes from %s after %d attempts: %v", remoteFileName, sftpReadAttempts, err)
			}

			if err = os.MkdirAll(localFilePath, os.ModePerm); err != nil {
				file.Close()
				log.Fatal(err)
			}

			// O_TRUNC (not O_APPEND): each successful re-download replaces
			// the cached copy. Appending was a latent bug — re-running for
			// the same date doubled the file and corrupted the cache for
			// any future SFTP-failure fallback.
			f, err := os.OpenFile(cachedFilePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
			if err != nil {
				file.Close()
				log.Fatal(err)
			}
			f.Write(bytes)
			f.Close()
			file.Close()
		}

		// process order details
		if localFileName == "OrderDetails.csv" {
			if err = gocsv.UnmarshalBytes(bytes, &orderDetails); err != nil {
				log.Fatal(err)
			}
		}

		// process payment details
		if localFileName == "PaymentDetails.csv" {
			if err = gocsv.UnmarshalBytes(bytes, &paymentDetails); err != nil {
				log.Fatal(err)
			}

			// associate payment details with order details
			for _, paymentDetail := range paymentDetails {
				for _, orderDetail := range orderDetails {
					if paymentDetail.OrderID == orderDetail.OrderID {
						orderDetail.PaymentDetails = append(orderDetail.PaymentDetails, *paymentDetail)
						break
					}
				}
			}
		}
	}

	return orderDetails
}

// newToastSFTPClient builds an SFTP client against the Toast export
// bucket using the same creds/id_rsa identity fetchToastCSVReports uses.
// Returns nil if the private key is missing — callers should treat that
// as "SFTP not available, run locally only" rather than fatal so the
// report still ships in offline / dev environments.
func newToastSFTPClient() (*sftp.Client, error) {
	pk, err := ioutil.ReadFile("creds/id_rsa")
	if err != nil {
		return nil, fmt.Errorf("read SFTP private key: %w", err)
	}
	config := sftp.Config{
		Username:   "YumYumsExportUser",
		PrivateKey: string(pk),
		Server:     "s-9b0f88558b264dfda.server.transfer.us-east-1.amazonaws.com:22",
		Timeout:    time.Second * 30,
	}
	return sftp.New(config)
}

// loadOperatingProfitHistory bundles the load/backfill flow used before
// rendering the weekly summary. Returns the history and a status string
// suitable for direct display in the report (empty when everything went
// smoothly). Never fails — degraded paths surface in the status text so
// the report still ships when SFTP is unreachable or PDFs are missing.
func loadOperatingProfitHistory() (*models.OperatingProfitHistory, string) {
	store := service.OperatingProfitHistoryStore{
		RemotePath: fmt.Sprintf("/%s/%s", exportId, service.OperatingProfitHistoryFileName),
		LocalPath:  filepath.Join("output", "toast_reports", service.OperatingProfitHistoryFileName),
	}

	client, err := newToastSFTPClient()
	if err != nil {
		log.Warnf("operating-profit history: SFTP unavailable (%v) — using local cache only", err)
	} else {
		store.Client = client
		defer client.Close()
	}

	history, status, err := store.Load()
	if err != nil {
		log.Warnf("operating-profit history: load failed (%v) — starting empty", err)
		return &models.OperatingProfitHistory{}, fmt.Sprintf("history load failed: %v", err)
	}

	// Auto-backfill from the last 3 PDFs when we have nothing on hand —
	// gets the rolling chart populated on first run instead of waiting 4
	// weeks for it to fill in.
	if len(history.Entries) == 0 {
		added, backfillStatus := service.BackfillOperatingProfitFromPDFs(history, "output/payroll", 3)
		if added > 0 {
			log.Infof("operating-profit history: backfilled %d entries from prior PDFs", added)
			if saveStatus, err := store.Save(history); err == nil && saveStatus != "" {
				if status != "" {
					status += "; "
				}
				status += saveStatus
			} else if err != nil {
				log.Warnf("operating-profit history: save after backfill failed: %v", err)
			}
		}
		if backfillStatus != "" {
			if status != "" {
				status += "; "
			}
			status += backfillStatus
		}
	}

	return history, status
}

// dedupCSVLines removes byte-identical duplicate lines while preserving
// first-occurrence order. Used to repair Toast CSV caches corrupted by
// the legacy O_APPEND write path (each prior run concatenated another
// full copy of the file). Idempotent — uncorrupted files pass through
// with the same content.
func dedupCSVLines(data []byte) []byte {
	lines := strings.Split(string(data), "\n")
	seen := make(map[string]struct{}, len(lines))
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		// Blank trailing lines are common — keep one, drop the rest.
		if line == "" {
			if _, ok := seen[""]; ok {
				continue
			}
		}
		if _, ok := seen[line]; ok {
			continue
		}
		seen[line] = struct{}{}
		out = append(out, line)
	}
	return []byte(strings.Join(out, "\n"))
}

func groupOrderDetailsByServer(orderDetails []*models.OrderDetail) map[models.Server][]*models.OrderDetail {
	data := make(map[models.Server][]*models.OrderDetail)

	for _, o := range orderDetails {
		server := models.Server(o.Server)

		if found, company := models.IsDeliveryOrder(o); found {
			server = models.Server(company)
		}

		if _, found := data[server]; !found {
			data[server] = make([]*models.OrderDetail, 0)
		}

		data[server] = append(data[server], o)
	}

	return data
}

func getThirdPartyOrders(orderDetails []*models.OrderDetail) (models.ThirdPartyMerchantOrders, error) {
	orders := make(models.ThirdPartyMerchantOrders)

	for _, o := range orderDetails {
		if found, company := models.IsDeliveryOrder(o); found {
			switch company {
			case models.GrubHub:
				orders.Add(models.GrubHub, o)
			case models.UberEats:
				orders.Add(models.UberEats, o)
			case models.DoorDash:
				orders.Add(models.DoorDash, o)
			default:
				return models.ThirdPartyMerchantOrders{}, fmt.Errorf("getThirdPartyOrders: unknown company %v", company)
			}
		}
	}
	return orders, nil
}

func ProcessOrderDetails(orderDetails []*models.OrderDetail, tipsWithheldPercentage float64) (models.DailySummary, error) {
	serverDetails := groupOrderDetailsByServer(orderDetails)
	thirdPartyOrders, err := getThirdPartyOrders(orderDetails)
	if err != nil {
		return models.DailySummary{}, fmt.Errorf("ProcessOrderDetails: failed to get third party orders: %w", err)
	}

	var netSales, totalTaxes, totalTips, cashTendered, ccFees float64
	employeeDetails := make(map[models.Employee][]*models.OrderDetail)
	for server, details := range serverDetails {
		summary := models.OrderDetails(details).GetSummary(tipsWithheldPercentage)

		netSales += summary.TotalSales
		totalTips += summary.TotalTips
		cashTendered += summary.CashTendered
		ccFees += summary.CCFees
		employeeDetails[models.Employee(server)] = details

		if server.IsDeliveryService() {
			log.Debugf("ignores taxes of %.2f for %s delivery server", summary.TotalTaxes, server)
		} else {
			totalTaxes += summary.TotalTaxes
		}
	}

	return models.DailySummary{
		Sales:            netSales,
		Taxes:            totalTaxes,
		CashTendered:     cashTendered,
		CCFees:           ccFees,
		Tips:             totalTips,
		EmployeeDetails:  employeeDetails,
		ThirdPartyOrders: thirdPartyOrders,
	}, nil
}

//	type TipShare struct {
//		Total
//	}
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

// 6+ -> evenly
// 4 - 6 -> 66%
// 2 - 4 -> 33%
// <2 -> 0%
func CalculateWeeklyReport(dailyReport map[time.Time]models.DailySummary, timesheet models.Timesheet, employeeHours []models.EmployeeHours, previousEmployeeHours []models.EmployeeHours, payrollThisCycleHours []models.EmployeeHours, cashEmployeesPay []models.CashEmployeePay, tipsWithheldPercentage float64) models.WeeklySummary {
	var tipDetails models.TipDetails
	tipDetails.Details = make(map[models.Employee]float64)
	totalSales := 0.0
	totalTaxes := 0.0
	totalCashTendered := 0.0
	totalCCFees := 0.0
	voidedTotal := 0.0
	var voidedOrders []*models.OrderDetail

	for reportTime, summary := range dailyReport {
		for _, details := range summary.EmployeeDetails {
			perEmployee := models.OrderDetails(details).GetSummary(tipsWithheldPercentage)
			for _, v := range perEmployee.VoidedOrders {
				voidedTotal += v.Amount
				voidedOrders = append(voidedOrders, v)
			}
		}
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

		for employee := range schedule.Shifts {
			if employee == "Latanya Mcgriff" {
				tipDetails.Details[employee] += (float64(tipPool) / float64(tipPool)) * summary.Tips
			} else {
				tipDetails.Details[employee] += 0.0
			}
			// todo: reenable when we start sharing tips
			// tipDetails.Details[employee] += (float64(tipsShare[employee]) / float64(tipPool)) * summary.Tips
		}

		totalSales += summary.Sales
		totalTaxes += summary.Taxes
		totalCashTendered += summary.CashTendered
		totalCCFees += summary.CCFees
		tipDetails.Total += summary.Tips
	}

	return models.WeeklySummary{
		Tips:             tipDetails,
		Sales:            totalSales,
		SalesTax:         totalTaxes,
		CashTendered:     totalCashTendered,
		CCFees:           totalCCFees,
		VoidedTotal:      voidedTotal,
		VoidedOrders:     voidedOrders,
		Hours:            employeeHours,
		PayrollThisCycle: payrollThisCycleHours,
		CashEmployeesPay: cashEmployeesPay,
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

//type ThirdPartyOrdersReportItem

//func (it *ThirdPartyOrdersReportItem) Add(date time.Time, merchantOrders models.ThirdPartyMerchantOrders) {
//	orders := (*it)[date]
//	orders.AddThirdPartyMerchantOrders(merchantOrders)
//}

func (r *ThirdPartyOrdersReport) GetOrders() models.OrderDetails {
	var orderDetails []*models.OrderDetail

	for _, thirdPartyReportItem := range *r {
		for _, thirdPartyMerchantOrders := range thirdPartyReportItem {
			for _, o := range thirdPartyMerchantOrders {
				orderDetails = append(orderDetails, o)
			}
		}
	}

	return orderDetails
}

type ThirdPartyOrdersReport map[time.Time]models.ThirdPartyMerchantOrders

func IsOrderPaid(response string) (bool, error) {
	responseLower := strings.ToLower(response)

	if len(responseLower) == 1 {
		if strings.Index(responseLower, "y") >= 0 {
			return true, nil
		}

		if strings.Index(responseLower, "n") >= 0 {
			return false, nil
		}
	} else if len(responseLower) == 2 {
		if strings.Index(responseLower, "no") >= 0 {
			return false, nil
		}
	} else if len(responseLower) == 3 {
		if strings.Index(responseLower, "yes") >= 0 {
			return true, nil
		}
	}

	return false, fmt.Errorf("invalid user input: %s", responseLower)
}

func (r *ThirdPartyOrdersReport) GetUnpaidOrders() ThirdPartyOrdersReport {
	o := make(ThirdPartyOrdersReport, 0)
	for _, thirdPartyMerchant := range []models.ThirdPartyMerchant{models.UberEats, models.GrubHub, models.DoorDash} {
		for date, merchantOrders := range *r {
			thirdPartyMerchantOrders := make(models.ThirdPartyMerchantOrders)

			if len(merchantOrders[thirdPartyMerchant]) > 0 {
				fmt.Printf("Was the following %v order(s) paid on %s? (y)es or (n)o\n", thirdPartyMerchant, date.Format("01/02"))
			}

			for _, orderDetail := range merchantOrders[thirdPartyMerchant] {
				fmt.Println(orderDetail.Show())

				for {
					// var then variable name then variable type
					var response string

					// Taking input from user
					fmt.Scanln(&response)

					isOrderPaid, err := IsOrderPaid(response)
					if err != nil {
						fmt.Println(err.Error())
						continue
					}

					if !isOrderPaid {
						thirdPartyMerchantOrders.Add(thirdPartyMerchant, orderDetail)
					}

					break
				}
			}

			o.Add(date, thirdPartyMerchantOrders)
		}
	}

	return o
}

func (r *ThirdPartyOrdersReport) Add(date time.Time, orders models.ThirdPartyMerchantOrders) {
	if data, found := (*r)[date]; found {
		data.AddThirdPartyMerchantOrders(orders)
	} else {
		(*r)[date] = orders
	}
}

func (r *ThirdPartyOrdersReport) GetOrderedDates() []time.Time {
	var sortedDates []time.Time

	for date, _ := range *r {
		sortedDates = append(sortedDates, date)
	}

	sort.Slice(sortedDates, func(i, j int) bool {
		return sortedDates[i].Before(sortedDates[j])
	})

	return sortedDates
}

func (r *ThirdPartyOrdersReport) Show(title string) string {
	report := strings.Builder{}

	report.WriteString(fmt.Sprintf("\n%s\n", title))
	report.WriteString("-----------------------\n")

	for _, date := range r.GetOrderedDates() {
		orders := (*r)[date]

		if len(orders[models.UberEats]) > 0 || len(orders[models.GrubHub]) > 0 || len(orders[models.DoorDash]) > 0 {
			report.WriteString(fmt.Sprintf("%v %v\n", date.Weekday(), date.Format("2006/01/02")))
			report.WriteString("-----------------------\n")
		}

		ordersCount := 0

		if len(orders[models.UberEats]) > 0 {
			report.WriteString("Uber Orders:\n")
			for _, o := range orders[models.UberEats] {
				report.WriteString(o.Show())
				report.WriteString("\n")
				ordersCount += 1
			}
			report.WriteString("\n")
		}

		if len(orders[models.GrubHub]) > 0 {
			report.WriteString("Grubhub Orders:\n")
			for _, o := range orders[models.GrubHub] {
				report.WriteString(o.Show())
				report.WriteString("\n")
				ordersCount += 1
			}
			report.WriteString("\n")
		}

		if len(orders[models.DoorDash]) > 0 {
			report.WriteString("DoorDash Orders:\n")
			for _, o := range orders[models.DoorDash] {
				report.WriteString(o.Show())
				report.WriteString("\n")
				ordersCount += 1
			}
			report.WriteString("\n")
		}
	}

	return report.String()
}

func getCashEmployeeWages(cashEmployees []models.CashEmployeeInputParam, defaultEmployeeTaxRate float64) []models.CashEmployeePay {
	var cashEmployeesPay []models.CashEmployeePay

	for _, employee := range cashEmployees {
		// Ask the user to enter a withdrawal amount from stdin
		fmt.Printf("Enter %s's net pay (or -1 to quit):\n", employee.Name)

		var netPay float64
		if _, err := fmt.Scanln(&netPay); err != nil {
			panic(err)
		}

		if netPay < 0 {
			break
		}

		taxes := netPay * employee.TaxRate

		cashEmployeesPay = append(cashEmployeesPay, models.CashEmployeePay{
			Name:   employee.Name,
			NetPay: netPay,
			Taxes:  taxes,
		})
	}

	return cashEmployeesPay
}

func getCashHeld() []float64 {
	return make([]float64, 0)
}

// promptWeeksAgo reads a non-negative integer from stdin selecting how many
// weeks before the current pay period to process. Empty input (just Enter)
// defaults to 0 (current week). Invalid input re-prompts. EOF is treated
// as the default so piped/non-interactive runs without --weeks-ago don't
// hang.
func promptWeeksAgo() int {
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("Process which pay period? [0=current, 1=last week, 2=two weeks ago, ...] (default 0): ")
		line, err := reader.ReadString('\n')
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if err != nil {
				log.Info("No input on stdin — defaulting to current week (--weeks-ago=0)")
			}
			return 0
		}
		n, parseErr := strconv.Atoi(trimmed)
		if parseErr != nil || n < 0 {
			fmt.Println("Invalid input — enter a non-negative integer (or press Enter for 0).")
			continue
		}
		return n
	}
}

func pickMercuryAccount(accounts []external.MercuryAccount, label string) external.MercuryAccount {
	fmt.Printf("\nSelect %s account:\n", label)
	for i, acct := range accounts {
		fmt.Printf("  %d) %s (%s)\n", i+1, acct.Name, acct.ID)
	}

	var choice int
	for {
		fmt.Printf("Enter choice (1-%d): ", len(accounts))
		if _, err := fmt.Scanln(&choice); err != nil || choice < 1 || choice > len(accounts) {
			fmt.Println("Invalid choice, try again.")
			continue
		}
		break
	}

	return accounts[choice-1]
}

func pickMercuryRecipient(recipients []external.MercuryRecipient, label string) external.MercuryRecipient {
	fmt.Printf("\nSelect %s recipient:\n", label)
	for i, r := range recipients {
		var details []string
		if bank := r.BankName(); bank != "" {
			details = append(details, bank)
		}
		if last4 := r.AccountLast4(); last4 != "" {
			details = append(details, "••"+last4)
		}
		if methods := r.SupportedMethods(); methods != "" {
			details = append(details, "["+methods+"]")
		}
		details = append(details, r.ID)
		fmt.Printf("  %d) %s (%s)\n", i+1, r.Name, strings.Join(details, " · "))
	}

	var choice int
	for {
		fmt.Printf("Enter choice (1-%d): ", len(recipients))
		if _, err := fmt.Scanln(&choice); err != nil || choice < 1 || choice > len(recipients) {
			fmt.Println("Invalid choice, try again.")
			continue
		}
		break
	}

	return recipients[choice-1]
}

type transferOutcome struct {
	Attempted bool
	Sent      bool
	Err       error
}

func recordOutcome(ledger *transferledger.Ledger, kind transferledger.Kind, amount float64, method, destination, idempotencyKey string, outcome transferOutcome) {
	if !outcome.Attempted {
		return
	}
	entry := transferledger.Entry{
		Amount:         amount,
		Method:         method,
		Destination:    destination,
		SentAt:         time.Now().UTC(),
		IdempotencyKey: idempotencyKey,
	}
	if outcome.Sent {
		entry.Status = transferledger.StatusSent
	} else {
		entry.Status = transferledger.StatusFailed
		if outcome.Err != nil {
			entry.Error = outcome.Err.Error()
		}
	}
	ledger.Record(kind, entry)
}

func executeTransfers(mercuryClient *external.MercuryClient, sourceAccount external.MercuryAccount, transfers []external.MercuryTransferRequest, autoApprove bool) []transferOutcome {
	outcomes := make([]transferOutcome, len(transfers))
	if len(transfers) == 0 {
		return outcomes
	}

	fmt.Println("\n--- Pending Transfers ---")
	for _, t := range transfers {
		dest := t.ToAccountName
		if dest == "" {
			dest = t.ToAccountID
		}
		fmt.Printf("  $%.2f from %s → %s (%s)\n", t.Amount, sourceAccount.Name, dest, t.Note)
	}
	fmt.Println()

	for i, t := range transfers {
		dest := t.ToAccountName
		if dest == "" {
			dest = t.ToAccountID
		}

		if !autoApprove {
			fmt.Printf("Send $%.2f → %s (%s)? (y/n): ", t.Amount, dest, t.Note)
			var answer string
			fmt.Scanln(&answer)
			if strings.ToLower(answer) != "y" {
				fmt.Printf("  Skipped: %s\n", t.Note)
				continue
			}
		}

		err := mercuryClient.CreateInternalTransfer(t)
		outcomes[i] = transferOutcome{Attempted: true, Sent: err == nil, Err: err}
		if err != nil {
			log.Errorf("transfer failed (%s): %v", t.Note, err)
		} else {
			fmt.Printf("  Transferred $%.2f → %s\n", t.Amount, t.Note)
		}
	}
	return outcomes
}

func saveEnvVar(envVar string, value string) {
	envFile := ".env"
	if os.Getenv("MERCURY_SANDBOX") == "true" {
		envFile = ".env.sandbox"
	}

	data, err := os.ReadFile(envFile)
	if err != nil {
		log.Errorf("failed to read %s: %v", envFile, err)
		return
	}

	lines := strings.Split(string(data), "\n")
	found := false
	for i, line := range lines {
		if strings.HasPrefix(line, envVar+"=") {
			lines[i] = envVar + "=" + value
			found = true
			break
		}
	}
	if !found {
		lines = append(lines, envVar+"="+value)
	}

	if err := os.WriteFile(envFile, []byte(strings.Join(lines, "\n")), 0644); err != nil {
		log.Errorf("failed to write %s: %v", envFile, err)
	}
}

func resolveMercuryAccount(accounts []external.MercuryAccount, envVar string, label string) external.MercuryAccount {
	if id := os.Getenv(envVar); id != "" {
		for _, acct := range accounts {
			if acct.ID == id {
				log.Infof("Using %s account: %s (%s)", label, acct.Name, acct.ID)
				return acct
			}
		}
		log.Fatalf("%s=%s not found in Mercury accounts", envVar, id)
	}
	acct := pickMercuryAccount(accounts, label)
	saveEnvVar(envVar, acct.ID)
	return acct
}

func pickExternalMethod(recipient external.MercuryRecipient, label string) string {
	fmt.Printf("\nSelect %s transfer method for %s:\n", label, recipient.Name)
	fmt.Printf("  1) ACH (free, 0-1 business days)\n")
	fmt.Printf("  2) Domestic wire (same day, may incur fee)\n")
	var choice int
	for {
		fmt.Print("Enter choice (1-2): ")
		if _, err := fmt.Scanln(&choice); err != nil || choice < 1 || choice > 2 {
			fmt.Println("Invalid choice, try again.")
			continue
		}
		break
	}
	if choice == 1 {
		return external.MercuryPaymentMethodACH
	}
	return external.MercuryPaymentMethodDomesticWire
}

func resolveExternalMethod(recipient external.MercuryRecipient, envVar string, label string) string {
	supportsACH := recipient.ElectronicRoutingInfo != nil
	supportsWire := recipient.DomesticWireRoutingInfo != nil

	if !supportsACH && !supportsWire {
		log.Fatalf("%s recipient %s (%s) has no ACH or domestic wire routing — add routing in Mercury", label, recipient.Name, recipient.ID)
	}

	if method := os.Getenv(envVar); method != "" {
		switch method {
		case external.MercuryPaymentMethodACH:
			if !supportsACH {
				log.Fatalf("%s=%s but recipient %s does not support ACH", envVar, method, recipient.Name)
			}
		case external.MercuryPaymentMethodDomesticWire:
			if !supportsWire {
				log.Fatalf("%s=%s but recipient %s does not support domestic wire", envVar, method, recipient.Name)
			}
		default:
			log.Fatalf("%s=%s is invalid (must be %s or %s)", envVar, method, external.MercuryPaymentMethodACH, external.MercuryPaymentMethodDomesticWire)
		}
		log.Infof("Using %s method: %s", label, method)
		return method
	}

	if supportsACH && !supportsWire {
		log.Infof("Using %s method: %s (only method supported by recipient)", label, external.MercuryPaymentMethodACH)
		return external.MercuryPaymentMethodACH
	}
	if supportsWire && !supportsACH {
		log.Infof("Using %s method: %s (only method supported by recipient)", label, external.MercuryPaymentMethodDomesticWire)
		return external.MercuryPaymentMethodDomesticWire
	}

	method := pickExternalMethod(recipient, label)
	saveEnvVar(envVar, method)
	return method
}

func resolveMercuryRecipient(recipients []external.MercuryRecipient, envVar string, label string) external.MercuryRecipient {
	if id := os.Getenv(envVar); id != "" {
		for _, r := range recipients {
			if r.ID == id {
				log.Infof("Using %s recipient: %s (%s)", label, r.Name, r.ID)
				return r
			}
		}
		log.Fatalf("%s=%s not found in Mercury recipients", envVar, id)
	}
	r := pickMercuryRecipient(recipients, label)
	saveEnvVar(envVar, r.ID)
	return r
}

func formatRecipientDest(recipient external.MercuryRecipient) string {
	var details []string
	if bank := recipient.BankName(); bank != "" {
		details = append(details, bank)
	}
	if last4 := recipient.AccountLast4(); last4 != "" {
		details = append(details, "••"+last4)
	}
	if len(details) == 0 {
		return recipient.Name
	}
	return fmt.Sprintf("%s · %s", recipient.Name, strings.Join(details, " "))
}

func executeExternalTransfer(mercuryClient *external.MercuryClient, sourceAccount external.MercuryAccount, recipient external.MercuryRecipient, transfer external.MercuryExternalTransferRequest, autoApprove bool) transferOutcome {
	dest := formatRecipientDest(recipient)
	fmt.Println("\n--- Pending External Transfer ---")
	fmt.Printf("  $%.2f from %s → %s [%s] (%s)\n", transfer.Amount, sourceAccount.Name, dest, transfer.PaymentMethod, transfer.Note)

	if !autoApprove {
		fmt.Print("\nExecute external transfer? (y/n): ")
		var answer string
		fmt.Scanln(&answer)

		if strings.ToLower(answer) != "y" {
			fmt.Println("External transfer skipped.")
			return transferOutcome{}
		}
	}

	err := mercuryClient.CreateExternalTransfer(transfer)
	out := transferOutcome{Attempted: true, Sent: err == nil, Err: err}
	if err != nil {
		log.Errorf("external transfer failed (%s): %v", transfer.Note, err)
		return out
	}
	fmt.Printf("  Transferred $%.2f → %s [%s]\n", transfer.Amount, dest, transfer.PaymentMethod)
	return out
}

func main() {
	autoApproveTransfers := flag.Bool("auto-approve-transfers", false, "automatically approve Mercury transfers without prompting")
	mercurySandbox := flag.Bool("sandbox", false, "use Mercury sandbox environment")
	forceResend := flag.String("force-resend", "", "comma-separated transfer kinds to force re-send (sales_tax, deferred_taxes, rent_hold, deposit, all)")
	skipMercury := flag.Bool("skip-mercury", false, "skip Mercury account resolution and transfer dispatch (preview mode — no bank movement)")
	skipClassify := flag.Bool("skip-classify", false, "skip the Mercury transaction classify pipeline (Pull → Claude → Apply)")
	skipReceipts := flag.Bool("skip-receipts", false, "skip the HQ COGS fetch and Mercury↔HQ gap check (ship payroll without a COGS section — food cost numbers will be missing)")
	weeksAgo := flag.Int("weeks-ago", 0, "process the pay period N weeks before the current one (0 = current week, 1 = last week, ...)")
	skipScheduleWarning := flag.Bool("skip-schedule-warning", false, "silence the interactive warning when an hourly employee has ≥3 months tenure but no 'primary pay schedule' tag in Sling")
	flag.Parse()

	weeksAgoSet := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "weeks-ago" {
			weeksAgoSet = true
		}
	})
	if !weeksAgoSet {
		*weeksAgo = promptWeeksAgo()
	} else if *weeksAgo < 0 {
		log.Fatalf("--weeks-ago must be >= 0, got %d", *weeksAgo)
	}

	forcedKinds, err := transferledger.ParseKinds(*forceResend)
	if err != nil {
		log.Fatalf("--force-resend: %v", err)
	}
	forcedSet := map[transferledger.Kind]bool{}
	for _, k := range forcedKinds {
		forcedSet[k] = true
	}

	if *mercurySandbox {
		godotenv.Load(".env.sandbox")
	} else {
		godotenv.Load()
	}

	//--- Variables ---
	baseURL := "https://api.getsling.com/v1"
	slingEmail := "jamal@yumyums.kitchen"
	slingPassword := "9@^P9bZR7RGu37zk"
	tipsWithheldPercentage := 0.03
	cashEmployees := []models.CashEmployeeInputParam{
		// {
		// 	Name:    "Aly",
		// 	TaxRate: 0.25 + 0.0765, // 22% federal + 7.65% payroll + 3% buffer
		// },
	}
	defaultEmployeeTaxRate := 0.25
	commissionEmployeeTaxRate := 0.20
	rentHoldAmount := 475.0

	commissionSalesStructureStandard := &models.CommissionSalesStructure{
		models.CommissionSalesIsLessThan{
			SalesThreshold: 8000.0,
			// SalesCommissionPercentage: 0.3,
		},
		models.CommissionSalesIsGreaterThanOrEqual{
			SalesThreshold: 8000,
			// SalesCommissionPercentage: 0.25,
		},
	}

	commissionSalesStructureOwner := &models.CommissionSalesStructure{
		models.CommissionSalesIsGreaterThanOrEqual{
			SalesThreshold:            0,
			SalesCommissionPercentage: 0.0,
		},
	}

	// commissionBasedEmployees is populated below from Sling tags once the
	// user set is fetched; previously hardcoded here.

	//cashWithdrawalResponsesID := "1v3mSj-ZeKcDkplaAZBuva1dVOe7_Hf9O9z2o8YW_zfk"
	exclusions := []models.TipExclusion{
		{
			EmployeeID: 100,
			Day:        time.Wednesday,
		},
		{
			EmployeeID: 100,
			Day:        time.Thursday,
		},
		{
			EmployeeID: 100,
			Day:        time.Friday,
		},
		{
			EmployeeID: 100,
			Day:        time.Saturday,
		},
		{
			EmployeeID: 100,
			Day:        time.Sunday,
		},
		{
			EmployeeID: 112,
			Day:        time.Wednesday,
		},
		{
			EmployeeID: 112,
			Day:        time.Thursday,
		},
		{
			EmployeeID: 112,
			Day:        time.Friday,
		},
		{
			EmployeeID: 112,
			Day:        time.Saturday,
		},
		{
			EmployeeID: 112,
			Day:        time.Sunday,
		},
	}

	// fetch dates in reporting period
	// todo: we should dump these into a database the next day. Toast only keeps the last 7 days

	// setup google sheets
	//sheetsSrv, err := setup(ctx)
	//if err != nil {
	//	panic(err)
	//}
	//
	//sheetsClients := sheets.NewClient(sheetsSrv)

	//// fetch cash with held
	//rows, err := sheetsClients.FetchRows(ctx, cashWithdrawalResponsesID, "Withdrawals", sheetsSpreadsheetAllCells)
	//if err != nil {
	//	panic(err)
	//}

	//--- Mercury Setup ---
	// When --skip-mercury is set, all Mercury vars stay at zero values and
	// the transfer-dispatch block below is gated off the same flag. This
	// lets operators preview the report (e.g., iterate on COGS formatting)
	// without reachable banking infrastructure.
	var (
		mercuryClient      *external.MercuryClient
		sourceAccount      external.MercuryAccount
		salesTaxAccount    external.MercuryAccount
		deferredTaxAccount external.MercuryAccount
		rentHoldRecipient  external.MercuryRecipient
		rentHoldMethod     string

		mercuryPersonalClient *external.MercuryClient
		depositSourceAccount  external.MercuryAccount
		depositRecipient      external.MercuryRecipient
		depositMethod         string
	)
	if *skipMercury {
		log.Warn("--skip-mercury set: skipping Mercury account resolution and transfer dispatch (no bank movement this run)")
	} else {
		mercuryAPIKey := os.Getenv("MERCURY_API_KEY")
		if mercuryAPIKey == "" {
			log.Fatal("MERCURY_API_KEY environment variable is required")
		}

		mercuryClient = external.NewMercuryClient(mercuryAPIKey, *mercurySandbox)
		mercuryAccounts, err := mercuryClient.ListAccounts()
		if err != nil {
			log.Fatalf("failed to list Mercury accounts: %v", err)
		}

		sourceAccount = resolveMercuryAccount(mercuryAccounts, "MERCURY_SOURCE_ACCOUNT_ID", "source (Operations)")
		salesTaxAccount = resolveMercuryAccount(mercuryAccounts, "MERCURY_SALES_TAX_ACCOUNT_ID", "sales tax")
		deferredTaxAccount = resolveMercuryAccount(mercuryAccounts, "MERCURY_DEFERRED_TAX_ACCOUNT_ID", "deferred taxes")

		mercuryRecipients, err := mercuryClient.ListRecipients()
		if err != nil {
			log.Fatalf("failed to list Mercury recipients: %v", err)
		}

		// Show all recipients with any routing info — including wire-only or ACH-only.
		var routableRecipients []external.MercuryRecipient
		for _, r := range mercuryRecipients {
			if r.ElectronicRoutingInfo != nil || r.DomesticWireRoutingInfo != nil {
				routableRecipients = append(routableRecipients, r)
			}
		}

		if len(routableRecipients) == 0 {
			log.Fatalf("no Mercury recipients have ACH or domestic wire routing configured")
		}

		rentHoldRecipient = resolveMercuryRecipient(routableRecipients, "MERCURY_RENT_HOLD_RECIPIENT_ID", "rent hold (Personal Vacation Fun)")
		rentHoldMethod = resolveExternalMethod(rentHoldRecipient, "MERCURY_RENT_HOLD_METHOD", "rent hold")

		// The deposit leg leaves from the Personal workspace (where the
		// rent hold lands). That workspace has its own API key — the
		// business key cannot see personal accounts.
		mercuryPersonalAPIKey := os.Getenv("MERCURY_PERSONAL_API_KEY")
		if mercuryPersonalAPIKey == "" {
			log.Fatal("MERCURY_PERSONAL_API_KEY environment variable is required (Mercury Personal workspace key — the deposit wire to Latanya dispatches from there)")
		}

		mercuryPersonalClient = external.NewMercuryClient(mercuryPersonalAPIKey, *mercurySandbox)
		personalAccounts, err := mercuryPersonalClient.ListAccounts()
		if err != nil {
			log.Fatalf("failed to list Mercury Personal accounts: %v", err)
		}
		depositSourceAccount = resolveMercuryAccount(personalAccounts, "MERCURY_PERSONAL_SOURCE_ACCOUNT_ID", "Latanya's payroll deposit source (Personal — wires out from here)")

		personalRecipients, err := mercuryPersonalClient.ListRecipients()
		if err != nil {
			log.Fatalf("failed to list Mercury Personal recipients: %v", err)
		}

		var routablePersonalRecipients []external.MercuryRecipient
		for _, r := range personalRecipients {
			if r.ElectronicRoutingInfo != nil || r.DomesticWireRoutingInfo != nil {
				routablePersonalRecipients = append(routablePersonalRecipients, r)
			}
		}
		if len(routablePersonalRecipients) == 0 {
			log.Fatalf("no Mercury Personal recipients have ACH or domestic wire routing configured — add Latanya Mcgriff as a recipient in the Personal workspace")
		}

		depositRecipient = resolveMercuryRecipient(routablePersonalRecipients, "MERCURY_DEPOSIT_RECIPIENT_ID", "deposit (Latanya Mcgriff)")
		depositMethod = resolveExternalMethod(depositRecipient, "MERCURY_DEPOSIT_METHOD", "deposit")
	}

	//--- Cash Held ---
	cashHeld := getCashHeld()

	//--- Get Cash Employee Wages ---
	cashEmployeeWages := getCashEmployeeWages(cashEmployees, defaultEmployeeTaxRate)

	//--- Report Headers (Current Timesheet) ---
	now := time.Now().AddDate(0, 0, -7*(*weeksAgo))
	if *weeksAgo > 0 {
		log.Infof("Processing pay period %d week(s) before today (anchor date: %s)", *weeksAgo, now.Format("2006-01-02"))
	}
	sunday := service.GetDateLastSunday(now)
	dates := service.GetDatesStartingFromPreviousMonday(sunday)
	fromDate := dates[0].Format("2006-01-02")
	toDate := dates[len(dates)-1].Format("2006-01-02")

	//--- Sling client + user validation (run before the ~90s classify
	// pipeline so missing hireDate / wage / employeeId fails fast).
	slingClient, err := external.NewSlingTimesheet(baseURL, slingEmail, slingPassword)
	if err != nil {
		panic(err)
	}

	if err = slingClient.PopulateUsers(); err != nil {
		panic(err)
	}

	// Build commission-based employee set from Sling tags:
	//   - "commission" tag → included in the commission flow
	//   - "owner" tag      → owner structure (0%), suppressed from cost
	//                        aggregation and per-employee breakdown
	//   - "commission" alone → standard tiered structure
	var commissionBasedEmployees []models.CommissionBasedEmployee
	for _, u := range slingClient.Users() {
		if !u.HasTag(models.TagCommission) {
			continue
		}
		structure := commissionSalesStructureStandard
		isOwner := u.HasTag(models.TagOwner)
		if isOwner {
			structure = commissionSalesStructureOwner
		}
		commissionBasedEmployees = append(commissionBasedEmployees, models.CommissionBasedEmployee{
			Id:                       u.EmployeeID,
			Name:                     u.Name(),
			CommissionSalesStructure: structure,
			IsOwner:                  isOwner,
		})
	}

	// Schedule warning: any hourly employee (not commission, not owner) with
	// ≥3 months tenure who isn't tagged 'primary pay schedule' is still on
	// the new-employee (held) schedule. Surface a single batched y/n so the
	// operator can either go tag them in Sling (n → abort) or proceed.
	// Silenced by --skip-schedule-warning for unattended runs.
	if !*skipScheduleWarning {
		payPeriodEnd := dates[len(dates)-1]
		var overdue []string
		for _, u := range slingClient.Users() {
			if u.HasTag(models.TagCommission) || u.HasTag(models.TagOwner) {
				continue
			}
			if u.IsPrimarySchedule() {
				continue
			}
			if u.TenureAtLeastMonths(payPeriodEnd, models.PrimaryScheduleAge) {
				overdue = append(overdue, fmt.Sprintf("%s (%s)", u.Name(), u.Tenure(payPeriodEnd)))
			}
		}
		if len(overdue) > 0 {
			sort.Strings(overdue)
			fmt.Printf("\nWARNING: %d hourly employee(s) have ≥%d months tenure but no '%s' tag in Sling:\n",
				len(overdue), models.PrimaryScheduleAge, models.TagPrimarySchedule)
			for _, line := range overdue {
				fmt.Printf("  - %s\n", line)
			}
			fmt.Printf("These employees will continue on the new-employee schedule (1-week pay hold).\n")
			fmt.Printf("To put them on the primary schedule, add the '%s' tag in Sling and re-run.\n", models.TagPrimarySchedule)
			fmt.Print("Exit so you can tag them in Sling first? (y/n): ")
			var response string
			fmt.Scanln(&response)
			if strings.ToLower(strings.TrimSpace(response)) == "y" {
				log.Fatalf("aborted by operator — tag the listed employees in Sling and re-run, or pass --skip-schedule-warning")
			}
		}
	}

	//--- COGS (from HQ inventory) ---
	// HQ_INVENTORY_SERVICE_TOKEN gates the integration. When unset, the
	// run continues without a COGS section so dev environments without
	// HQ access aren't blocked. When set, an incomplete period (pending
	// receipts or unlinked line items) is treated as a hard failure —
	// food cost numbers would be misleading.
	// Runs before the AI classify pipeline so pending-receipt failures
	// surface fast — operators shouldn't wait through a ~90s Claude run
	// only to be told to go review receipts in HQ.
	// --skip-receipts bypasses the fetch entirely (PDF ships without a
	// COGS section); use when HQ is down or to publish without waiting
	// on receipt review.
	var hqSummary *external.HQPeriodSummary
	if *skipReceipts {
		log.Warn("--skip-receipts set: skipping HQ COGS fetch and Mercury↔HQ gap check (PDF will have no COGS section)")
	} else {
		hqSummary = fetchHQPeriodSummary(mercuryClient, dates[0], dates[len(dates)-1])
	}

	//--- Fetch Timesheets (current + previous) ---
	// Runs before the ~90s Claude classify so unapproved shifts and other
	// Sling failures surface immediately instead of after the AI call.
	lastWeek := now.AddDate(0, 0, -7)
	previousSunday := service.GetDateLastSunday(lastWeek)
	previousDates := service.GetDatesStartingFromPreviousMonday(previousSunday)
	previousFromDate := previousDates[0].Format("2006-01-02")
	previousToDate := previousDates[len(previousDates)-1].Format("2006-01-02")

	currentTimesheet, err := slingClient.GetPayroll(fromDate, toDate)
	if err != nil {
		panic(err)
	}

	previousTimesheet, err := slingClient.GetPayroll(previousFromDate, previousToDate)
	if err != nil {
		panic(err)
	}

	//--- Mercury Transaction Classification (Claude via CLI) ---
	// Pull every card tx in the pay period, hand the snapshot to Claude via
	// the `claude` CLI (one-shot session), then PATCH Mercury with the
	// proposed categories. Skipped when --skip-mercury (no Mercury client)
	// or --skip-classify (operator opt-out). Fails hard at every step:
	// missing CLI, Mercury error, malformed proposals, PATCH error.
	if !*skipMercury && !*skipClassify {
		classifyMercuryTransactions(mercuryClient, dates[0], dates[len(dates)-1])
	}

	//--- Process Timesheets ---
	var employeeHours []models.EmployeeHours
	for _, entry := range currentTimesheet {
		user := entry.User
		if user.HasTag(models.TagCommission) {
			log.Debugf("skip summing hours for commission based employee %v", user)
			continue
		}

		hours, err := external.SlingTimesheetItemShifts(entry.Shifts).GetTotalHours()
		if err != nil {
			panic(err)
		}

		employeeHours = append(employeeHours, models.EmployeeHours{
			Employee: user,
			Hours:    hours,
		})
	}

	var previousEmployeeHours []models.EmployeeHours
	for _, entry := range previousTimesheet {
		user := entry.User
		if user.HasTag(models.TagCommission) {
			log.Debugf("skip summing hours for commission based employee %v", user)
			continue
		}

		hours, err := external.SlingTimesheetItemShifts(entry.Shifts).GetTotalHours()
		if err != nil {
			panic(err)
		}

		previousEmployeeHours = append(previousEmployeeHours, models.EmployeeHours{
			Employee: user,
			Hours:    hours,
		})
	}

	// Build payrollThisCycleHours: what we actually pay out this cycle.
	// Primary-scheduled employees → their current-week hours.
	// Held (new-employee) staff → their previous-week hours.
	// Held employees with no previous-week hours (e.g., very first week)
	// are simply absent from the payout this cycle.
	currentHoursByID := make(map[int]models.EmployeeHours, len(employeeHours))
	for _, eh := range employeeHours {
		currentHoursByID[eh.Employee.EmployeeID] = eh
	}
	previousHoursByID := make(map[int]models.EmployeeHours, len(previousEmployeeHours))
	for _, eh := range previousEmployeeHours {
		previousHoursByID[eh.Employee.EmployeeID] = eh
	}
	seen := make(map[int]struct{})
	var payrollThisCycleHours []models.EmployeeHours
	for _, eh := range employeeHours {
		seen[eh.Employee.EmployeeID] = struct{}{}
		if eh.Employee.IsPrimarySchedule() {
			payrollThisCycleHours = append(payrollThisCycleHours, eh)
		} else if held, ok := previousHoursByID[eh.Employee.EmployeeID]; ok {
			payrollThisCycleHours = append(payrollThisCycleHours, held)
		}
	}
	// Held employees who didn't work the current week but did work last
	// week still get paid this cycle for that prior work.
	for _, eh := range previousEmployeeHours {
		if _, already := seen[eh.Employee.EmployeeID]; already {
			continue
		}
		if !eh.Employee.IsPrimarySchedule() {
			payrollThisCycleHours = append(payrollThisCycleHours, eh)
		}
	}

	dailyReport := make(map[time.Time]models.DailySummary)

	var reportOutput strings.Builder

	//--- Process Third Party Delivery Orders
	thirdPartyOrdersReport := make(ThirdPartyOrdersReport)
	for _, date := range dates {
		reportOutput.WriteString(fmt.Sprintf("%s - %s\n", date.Format("2006/01/02"), date.Weekday()))

		//--- Fetch order details from toast
		orderDetails := fetchToastCSVReports(date.Format("20060102"))
		summary, err := ProcessOrderDetails(orderDetails, tipsWithheldPercentage)
		if err != nil {
			panic(err)
		}

		reportOutput.WriteString(summary.Show(tipsWithheldPercentage))
		reportOutput.WriteString("\n")

		thirdPartyOrdersReport.Add(date, summary.ThirdPartyOrders)

		dailyReport[date] = summary
	}

	//--- Verify Delivery Orders
	unpaidOrdersReport := thirdPartyOrdersReport.GetUnpaidOrders()
	unpaidOrdersSummary := unpaidOrdersReport.GetOrders().GetSummary(tipsWithheldPercentage)

	//--- Fetch Timesheets ---
	ts, err := currentTimesheet.FetchTimesheet(exclusions)
	if err != nil {
		panic(err)
	}

	weeklySummary := CalculateWeeklyReport(dailyReport, ts, employeeHours, previousEmployeeHours, payrollThisCycleHours, cashEmployeeWages, tipsWithheldPercentage)

	//--- todo: wait for manual input

	weeklySummary.Sales -= unpaidOrdersSummary.TotalSales

	// Pre-compute the aggregate commission-based employee cost so the Labor
	// section can show a "Commission Employees Cost / Sales" line above
	// the per-employee Sales Commission Breakdown below. Tips are excluded
	// from the aggregate cost (they come from customers, not the business);
	// owners are excluded because they pay themselves outside payroll. The
	// per-employee loop below reconstructs the summaries with the same
	// inputs — math is cheap, type is unexported.
	for _, empl := range commissionBasedEmployees {
		if empl.IsOwner {
			continue
		}
		tips := weeklySummary.Tips.Details[models.Employee(empl.Name)]
		salesCommissionPercentage, err := empl.CommissionSalesStructure.GetSalesCommissionPercentage(&weeklySummary)
		if err != nil {
			log.Fatal(err)
		}
		s := models.NewCommissionBasedEmployeesTopLineSummary(previousDates[0], previousDates[len(previousDates)-1], empl.Name, weeklySummary.Sales, tips, salesCommissionPercentage, cashHeld, weeklySummary.CashTendered, rentHoldAmount, commissionEmployeeTaxRate)
		weeklySummary.CommissionEmployeesCost += s.GetBasePay() + s.GetCommission()
	}
	if hqSummary != nil {
		weeklySummary.COGSExclTax = hqSummary.COGSExclTax
		weeklySummary.COGSInclTax = hqSummary.COGSInclTax
	}

	// Operating-profit history: load before Show() so the trailing 4-week
	// rolling chart can render under Operating Profit. The new entry for
	// this week is saved AFTER the PDF is written so we don't pollute
	// history with an aborted run.
	weekEnding := dates[len(dates)-1]
	weekEndingStr := weekEnding.Format("2006-01-02")
	opProfitHistory, opProfitHistoryStatus := loadOperatingProfitHistory()
	weeklySummary.WeekEnding = weekEnding
	// Filter out any prior entry for this same week (re-run case) so the
	// chart's current row isn't duplicated against last-run's stored row.
	priorEntries := make([]models.OperatingProfitEntry, 0, len(opProfitHistory.Entries))
	for _, e := range opProfitHistory.Entries {
		if e.WeekEnding == weekEndingStr {
			continue
		}
		priorEntries = append(priorEntries, e)
	}
	if len(priorEntries) > 3 {
		priorEntries = priorEntries[len(priorEntries)-3:]
	}
	weeklySummary.PriorOperatingProfits = priorEntries
	weeklySummary.OperatingProfitHistoryStatus = opProfitHistoryStatus

	// Report layout: Summary first (high-level dashboard with Operating
	// Profit), then drill-down sections in the order the manager is
	// likely to want them — COGS and Labor flow directly into the
	// Operating Profit calculation, so they come first.
	reportOutput.WriteString(weeklySummary.Show())
	reportOutput.WriteString("\n")

	if hqSummary != nil {
		reportOutput.WriteString(renderCOGSSection(hqSummary))
		reportOutput.WriteString("\n")
	}

	reportOutput.WriteString(weeklySummary.ShowLaborDetail())
	reportOutput.WriteString("\n")

	reportOutput.WriteString(weeklySummary.ShowHeldHoursLiability(dates[len(dates)-1]))
	reportOutput.WriteString("\n")

	reportOutput.WriteString(weeklySummary.ShowPayrollThisCycle(dates[len(dates)-1], previousDates[len(previousDates)-1]))
	reportOutput.WriteString("\n")

	reportOutput.WriteString(weeklySummary.ShowTipsBreakdown())
	reportOutput.WriteString("\n")

	//--- Voided Orders ---
	reportOutput.WriteString("Voided Orders\n")
	reportOutput.WriteString("-----------------------\n")
	reportOutput.WriteString("\n")
	if len(weeklySummary.VoidedOrders) == 0 {
		reportOutput.WriteString("No voided orders.\n")
	} else {
		for _, v := range weeklySummary.VoidedOrders {
			opened := v.Opened.Format("01/02 3:04 PM")
			if v.TabNames == "" {
				reportOutput.WriteString(fmt.Sprintf("  Order #%d - %s: $%.2f\n", v.OrderNumber, opened, v.Amount))
			} else {
				reportOutput.WriteString(fmt.Sprintf("  Order #%d - %s - %s: $%.2f\n", v.OrderNumber, opened, v.TabNames, v.Amount))
			}
		}
	}
	reportOutput.WriteString(fmt.Sprintf("Total Voided: $%.2f\n", weeklySummary.VoidedTotal))
	reportOutput.WriteString("\n")

	//--- Cash ---
	reportOutput.WriteString("Cash\n")
	reportOutput.WriteString("-----------------------\n")
	reportOutput.WriteString("\n")
	totalCashHeld := 0.0
	if len(cashHeld) == 0 {
		reportOutput.WriteString("No cash taken.\n")
	} else {
		for _, cash := range cashHeld {
			reportOutput.WriteString(fmt.Sprintf("  -$%.2f\n", cash))
			totalCashHeld += cash
		}
	}
	if weeklySummary.CashTendered > totalCashHeld {
		cashLeftover := weeklySummary.CashTendered - totalCashHeld
		reportOutput.WriteString(fmt.Sprintf("Cash Left in Register: $%.2f\n", cashLeftover))
	}
	reportOutput.WriteString("\n")

	//--- Sales Commission Breakdown ---
	reportOutput.WriteString("Sales Commission Breakdown\n")
	reportOutput.WriteString("-----------------------\n")
	reportOutput.WriteString("\n")
	// Latanya's "Deposit" line (net pay after the rent hold is removed) —
	// captured here so the Mercury dispatch block below can wire it out.
	depositAmount := 0.0
	for _, empl := range commissionBasedEmployees {
		// todo: unify all employee models
		if empl.IsOwner {
			continue
		}

		tips := weeklySummary.Tips.Details[models.Employee(empl.Name)]

		salesCommissionPercentage, err := empl.CommissionSalesStructure.GetSalesCommissionPercentage(&weeklySummary)
		if err != nil {
			log.Fatal(err)
		}

		// todo: cash held should be broken down by employee
		commissionBasedEmployeesSummary := models.NewCommissionBasedEmployeesTopLineSummary(previousDates[0], previousDates[len(previousDates)-1], empl.Name, weeklySummary.Sales, tips, salesCommissionPercentage, cashHeld, weeklySummary.CashTendered, rentHoldAmount, commissionEmployeeTaxRate)

		// todo: make employee conversion less janky
		if empl.Name == "Latanya Mcgriff" {
			pretaxPay := commissionBasedEmployeesSummary.GetPretaxPay()
			employerTaxes := pretaxPay * 0.0765
			netPay := pretaxPay + commissionBasedEmployeesSummary.GetBasePay()
			cashEmployeeWages = append(cashEmployeeWages, models.CashEmployeePay{
				Name:   empl.Name,
				NetPay: netPay,
				Taxes:  commissionBasedEmployeesSummary.Taxes + employerTaxes,
			})
			depositAmount = commissionBasedEmployeesSummary.GetDeposit()
		}

		reportOutput.WriteString(commissionBasedEmployeesSummary.Show())
		reportOutput.WriteString("\n")
	}

	log.Debug(thirdPartyOrdersReport.Show("Paid Delivery Orders"))

	log.Debug(unpaidOrdersReport.Show("Cancelled Delivery Orders"))
	//cashWithdrawals, err := rows.ConvertToCashWithdrawals(dates[0], dates[len(dates)-1])
	//if err != nil {
	//	panic(err)
	//}
	//
	//cash := models.CashWithdrawals(cashWithdrawals)
	//for employee, amount := range cash.Sum() {
	//	fmt.Printf("%v: $%.2f\n", employee, amount)
	//}

	//--- Export to CSV ---
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

	pdfPath := writePDF(reportOutput.String(), fromDate, toDate)

	csvPath := fmt.Sprintf("output/payroll/payroll_%v.csv", toDate)
	f, err := os.Create(csvPath)
	if err != nil {
		panic(err)
	}

	if err = payroll.Entries(entries).ToCSV(f); err != nil {
		panic(err)
	}

	fmt.Println("\n--- Output Files ---")
	fmt.Printf("  PDF: %s\n", pdfPath)
	fmt.Printf("  CSV: %s\n", csvPath)

	// Persist this week's operating-profit entry so the next run's
	// rolling chart has another data point. Saved AFTER the PDF/CSV
	// write so a panic above this point doesn't leave history desynced
	// from what the operator actually saw.
	if weeklySummary.COGSExclTax > 0 {
		_, totalLabor := weeklySummary.LaborBreakdown()
		entry := models.MakeOperatingProfitEntry(
			weekEnding,
			weeklySummary.Sales,
			weeklySummary.COGSExclTax,
			totalLabor,
			weeklySummary.CCFees,
		)
		opProfitHistory.Upsert(entry)
		store := service.OperatingProfitHistoryStore{
			RemotePath: fmt.Sprintf("/%s/%s", exportId, service.OperatingProfitHistoryFileName),
			LocalPath:  filepath.Join("output", "toast_reports", service.OperatingProfitHistoryFileName),
		}
		if client, err := newToastSFTPClient(); err == nil {
			store.Client = client
			defer client.Close()
		}
		if status, err := store.Save(opProfitHistory); err != nil {
			log.Warnf("operating-profit history: save failed: %v", err)
		} else if status != "" {
			log.Warnf("operating-profit history: %s", status)
		}
	}

	//--- Mercury Transfers ---
	// Gated by --skip-mercury: when set, the setup block above left
	// mercuryClient and the account/recipient vars at zero values, so
	// dispatching here would panic. The whole block — including ledger
	// updates — is skipped together so a preview run doesn't desync the
	// ledger from real bank state.
	if !*skipMercury {
		payrollTaxes := 0.0
		for _, employee := range cashEmployeeWages {
			payrollTaxes += employee.Taxes
		}

		ledger, err := transferledger.Load("output/transfers", toDate)
		if err != nil {
			log.Fatalf("load transfer ledger: %v", err)
		}
		if len(forcedKinds) > 0 {
			ledger.Clear(forcedKinds...)
			log.Infof("--force-resend cleared ledger entries: %v", forcedKinds)
		}

		logSkipped := func(kind transferledger.Kind, e transferledger.Entry) {
			fmt.Printf("[skipped] %s already sent on %s ($%.2f → %s) — use --force-resend=%s to re-send\n",
				kind, e.SentAt.Format("2006-01-02"), e.Amount, e.Destination, kind)
		}

		type plannedInternal struct {
			kind    transferledger.Kind
			request external.MercuryTransferRequest
		}
		var planned []plannedInternal
		if weeklySummary.SalesTax > 0 {
			if e, sent := ledger.Sent(transferledger.KindSalesTax); sent {
				logSkipped(transferledger.KindSalesTax, e)
			} else {
				planned = append(planned, plannedInternal{
					kind: transferledger.KindSalesTax,
					request: external.MercuryTransferRequest{
						FromAccountID:  sourceAccount.ID,
						ToAccountID:    salesTaxAccount.ID,
						ToAccountName:  salesTaxAccount.Name,
						Amount:         weeklySummary.SalesTax,
						Note:           fmt.Sprintf("Sales tax %s - %s", fromDate, toDate),
						IdempotencyKey: transferledger.IdempotencyKey(toDate, transferledger.KindSalesTax, forcedSet[transferledger.KindSalesTax]),
					},
				})
			}
		}
		if payrollTaxes > 0 {
			if e, sent := ledger.Sent(transferledger.KindDeferredTaxes); sent {
				logSkipped(transferledger.KindDeferredTaxes, e)
			} else {
				planned = append(planned, plannedInternal{
					kind: transferledger.KindDeferredTaxes,
					request: external.MercuryTransferRequest{
						FromAccountID:  sourceAccount.ID,
						ToAccountID:    deferredTaxAccount.ID,
						ToAccountName:  deferredTaxAccount.Name,
						Amount:         payrollTaxes,
						Note:           fmt.Sprintf("Deferred taxes %s - %s", fromDate, toDate),
						IdempotencyKey: transferledger.IdempotencyKey(toDate, transferledger.KindDeferredTaxes, forcedSet[transferledger.KindDeferredTaxes]),
					},
				})
			}
		}

		if len(planned) > 0 {
			requests := make([]external.MercuryTransferRequest, len(planned))
			for i, p := range planned {
				requests[i] = p.request
			}
			outcomes := executeTransfers(mercuryClient, sourceAccount, requests, *autoApproveTransfers)
			for i, p := range planned {
				recordOutcome(ledger, p.kind, p.request.Amount, "internal", p.request.ToAccountName, p.request.IdempotencyKey, outcomes[i])
			}
		}

		if rentHoldAmount > 0 {
			if e, sent := ledger.Sent(transferledger.KindRentHold); sent {
				logSkipped(transferledger.KindRentHold, e)
			} else {
				rentHoldTransfer := external.MercuryExternalTransferRequest{
					FromAccountID:  sourceAccount.ID,
					RecipientID:    rentHoldRecipient.ID,
					Amount:         rentHoldAmount,
					Note:           fmt.Sprintf("Rent hold %s - %s", fromDate, toDate),
					PaymentMethod:  rentHoldMethod,
					IdempotencyKey: transferledger.IdempotencyKey(toDate, transferledger.KindRentHold, forcedSet[transferledger.KindRentHold]),
				}
				if rentHoldMethod == external.MercuryPaymentMethodDomesticWire {
					rentHoldTransfer.Purpose = &external.MercurySendMoneyPurpose{
						Simple: external.MercurySendMoneyPurposeSimple{
							Category:       external.MercuryPurposeTransferToMyExternalAccount,
							AdditionalInfo: fmt.Sprintf("Rent hold %s - %s", fromDate, toDate),
						},
					}
				}
				outcome := executeExternalTransfer(mercuryClient, sourceAccount, rentHoldRecipient, rentHoldTransfer, *autoApproveTransfers)
				recordOutcome(ledger, transferledger.KindRentHold, rentHoldAmount, rentHoldMethod, formatRecipientDest(rentHoldRecipient), rentHoldTransfer.IdempotencyKey, outcome)
			}
		}

		// Deposit — Latanya's net pay after the rent hold is removed,
		// dispatched from the Personal workspace. Gated on the rent-hold
		// leg being sent (checked against the ledger AFTER the block
		// above, so a rent hold sent this run unblocks it immediately).
		// When it has to wait, the ledger is left untouched so the
		// deposit is re-attempted on the next run.
		rentHoldSent := rentHoldAmount == 0
		if _, sent := ledger.Sent(transferledger.KindRentHold); sent {
			rentHoldSent = true
		}
		if depositAmount > 0 {
			if e, sent := ledger.Sent(transferledger.KindDeposit); sent {
				logSkipped(transferledger.KindDeposit, e)
			} else if !rentHoldSent {
				fmt.Printf("[pending] deposit ($%.2f → %s) waiting on rent hold — re-run after the rent-hold transfer is sent\n",
					depositAmount, formatRecipientDest(depositRecipient))
			} else {
				depositTransfer := external.MercuryExternalTransferRequest{
					FromAccountID:  depositSourceAccount.ID,
					RecipientID:    depositRecipient.ID,
					Amount:         depositAmount,
					Note:           fmt.Sprintf("Pay deposit %s - %s", fromDate, toDate),
					PaymentMethod:  depositMethod,
					IdempotencyKey: transferledger.IdempotencyKey(toDate, transferledger.KindDeposit, forcedSet[transferledger.KindDeposit]),
				}
				if depositMethod == external.MercuryPaymentMethodDomesticWire {
					depositTransfer.Purpose = &external.MercurySendMoneyPurpose{
						Simple: external.MercurySendMoneyPurposeSimple{
							Category:       external.MercuryPurposeEmployee,
							AdditionalInfo: fmt.Sprintf("Pay deposit %s - %s", fromDate, toDate),
						},
					}
				}
				outcome := executeExternalTransfer(mercuryPersonalClient, depositSourceAccount, depositRecipient, depositTransfer, *autoApproveTransfers)
				recordOutcome(ledger, transferledger.KindDeposit, depositAmount, depositMethod, formatRecipientDest(depositRecipient), depositTransfer.IdempotencyKey, outcome)
			}
		}

		if err := ledger.Save(); err != nil {
			log.Errorf("save transfer ledger: %v", err)
		}
	}
}

// pdfNonCP1252Replacer maps UTF-8 characters that aren't in cp1252 (the
// encoding gofpdf's core Helvetica font expects) down to ASCII. cp1252
// characters like em-dash, smart quotes, middle dot etc. are handled by
// gofpdf's UnicodeTranslator (set up inside writePDF); this replacer only
// catches the chars the translator can't represent.
var pdfNonCP1252Replacer = strings.NewReplacer(
	"≥", ">=", // U+2265 — not in cp1252
	"≤", "<=", // U+2264 — not in cp1252
)

func writePDF(report string, fromDate string, toDate string) string {
	const (
		bodyFontSize    = 18.0
		headingFontSize = 30.0
		titleFontSize   = 24.0
		bodyFontFamily  = "Helvetica"
	)

	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.AddPage()

	// gofpdf's core fonts (Helvetica etc.) speak cp1252. The translator
	// converts UTF-8 input (em-dash, middle dot, smart quotes, …) to the
	// right cp1252 bytes. Anything outside cp1252 is normalised to ASCII
	// by pdfNonCP1252Replacer first so the translator doesn't drop it.
	tr := pdf.UnicodeTranslatorFromDescriptor("")
	sanitize := func(s string) string { return tr(pdfNonCP1252Replacer.Replace(s)) }

	pdf.SetFont(bodyFontFamily, "B", titleFontSize)
	pdf.MultiCell(0, 12, sanitize(fmt.Sprintf("Sales Report for %s - %s", fromDate, toDate)), "", "", false)
	pdf.Ln(5)

	renderReport(pdf, sanitize(report), bodyFontFamily, bodyFontSize, headingFontSize)

	path := fmt.Sprintf("output/payroll/payroll_%v.pdf", toDate)
	if err := pdf.OutputFileAndClose(path); err != nil {
		panic(err)
	}
	return path
}

type pdfTableRow struct {
	isGap  bool
	indent int
	label  string
	value  string
}

func renderReport(pdf *fpdf.Fpdf, report string, family string, bodyFontSize, headingFontSize float64) {
	lines := strings.Split(report, "\n")
	i := 0
	for i < len(lines) {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		// Heading: text line followed by a dashes line.
		if i+1 < len(lines) && trimmed != "" && isDashLine(lines[i+1]) {
			pdf.SetFont(family, "B", headingFontSize)
			pdf.MultiCell(0, 16, trimmed, "", "L", false)
			pdf.Ln(3)
			i += 2
			continue
		}

		// Standalone dashes line: skip — likely an orphan separator.
		if isDashLine(line) {
			i++
			continue
		}

		// Try to collect a table block starting here.
		rows, advance := collectPdfTableBlock(lines, i)
		if len(rows) > 0 {
			renderPdfTableBlock(pdf, rows, family, bodyFontSize)
			i += advance
			continue
		}

		// Blank line outside a table block: small vertical spacer.
		if trimmed == "" {
			pdf.Ln(4)
			i++
			continue
		}

		// Sub-heading (non-indented standalone label, often introduces a table block).
		if !startsWithSpaces(line) {
			pdf.SetFont(family, "B", bodyFontSize)
			pdf.MultiCell(0, 10, trimmed, "", "L", false)
			pdf.Ln(1)
		} else {
			pdf.SetFont(family, "", bodyFontSize)
			pdf.MultiCell(0, 10, line, "", "L", false)
		}
		i++
	}
}

// collectPdfTableBlock gathers consecutive `label: value` lines starting at i,
// allowing blank-line spacers between rows within the block. Returns the row
// slice and the number of input lines consumed.
func collectPdfTableBlock(lines []string, i int) ([]pdfTableRow, int) {
	var rows []pdfTableRow
	j := i
	pendingGaps := 0

	for j < len(lines) {
		line := lines[j]
		trimmed := strings.TrimSpace(line)

		// Heading boundary: line followed by dashes ends this block.
		if j+1 < len(lines) && trimmed != "" && isDashLine(lines[j+1]) {
			break
		}
		if isDashLine(line) {
			break
		}

		if trimmed == "" {
			if len(rows) == 0 {
				// Leading blanks belong to the caller, not the block.
				return nil, 0
			}
			pendingGaps++
			j++
			continue
		}

		row, ok := parsePdfTableRow(line)
		if !ok {
			break
		}
		for k := 0; k < pendingGaps; k++ {
			rows = append(rows, pdfTableRow{isGap: true})
		}
		pendingGaps = 0
		rows = append(rows, row)
		j++
	}

	if len(rows) == 0 {
		return nil, 0
	}
	// Don't consume trailing blanks — let the caller render them as the
	// inter-block gap.
	return rows, j - i - pendingGaps
}

func parsePdfTableRow(line string) (pdfTableRow, bool) {
	indent := 0
	for indent < len(line) && line[indent] == ' ' {
		indent++
	}
	content := line[indent:]
	idx := strings.Index(content, ":")
	if idx <= 0 || idx >= len(content)-1 {
		return pdfTableRow{}, false
	}
	// Reject sentence-like lines that happen to contain a colon (e.g. voided
	// orders with embedded timestamps: "Order #2 - 05/28 12:34 PM - $0.00").
	// True table labels are short noun phrases without "X - Y" segments — the
	// time-colon would otherwise be misread as the label/value boundary and
	// render with a giant column gap between the hour and minute.
	if strings.Contains(content[:idx], " - ") {
		return pdfTableRow{}, false
	}
	label := strings.TrimSpace(content[:idx]) + ":"
	value := strings.TrimSpace(content[idx+1:])
	if value == "" {
		return pdfTableRow{}, false
	}
	return pdfTableRow{indent: indent, label: label, value: value}, true
}

func renderPdfTableBlock(pdf *fpdf.Fpdf, rows []pdfTableRow, family string, bodyFontSize float64) {
	pdf.SetFont(family, "", bodyFontSize)

	const (
		indentStep    = 2.25 // mm per leading space
		labelPadRight = 6.0  // mm gap between label and value columns
		rowHeight     = 10.5
		gapHeight     = 4.5
	)

	// Auto-fit label column to the widest label (including its indent) in this block.
	var maxLabelWidth float64
	for _, r := range rows {
		if r.isGap {
			continue
		}
		w := pdf.GetStringWidth(r.label) + float64(r.indent)*indentStep
		if w > maxLabelWidth {
			maxLabelWidth = w
		}
	}
	labelColWidth := maxLabelWidth + labelPadRight

	leftMargin, _, rightMargin, _ := pdf.GetMargins()
	pageW, _ := pdf.GetPageSize()
	valueColWidth := pageW - leftMargin - rightMargin - labelColWidth

	for _, r := range rows {
		if r.isGap {
			pdf.Ln(gapHeight)
			continue
		}
		indentWidth := float64(r.indent) * indentStep
		if indentWidth > 0 {
			pdf.CellFormat(indentWidth, rowHeight, "", "", 0, "L", false, 0, "")
		}
		pdf.CellFormat(labelColWidth-indentWidth, rowHeight, r.label, "", 0, "L", false, 0, "")

		// Use MultiCell for the value so long expressions wrap inside the column.
		x := pdf.GetX()
		y := pdf.GetY()
		pdf.MultiCell(valueColWidth, rowHeight, r.value, "", "L", false)
		// If the value wrapped, MultiCell already moved Y. Otherwise force a newline.
		if pdf.GetY() == y+rowHeight {
			// Single-line value — Y advanced exactly one row, all good.
		}
		_ = x
	}
}

func startsWithSpaces(s string) bool {
	return len(s) > 0 && s[0] == ' '
}

func isDashLine(s string) bool {
	t := strings.TrimSpace(s)
	if len(t) < 5 {
		return false
	}
	for _, r := range t {
		if r != '-' {
			return false
		}
	}
	return true
}

// classifyMercuryTransactions runs the three-phase classify pipeline inline:
// Pull the period's card txs to a snapshot file → invoke `claude` to write
// the proposals file → Apply the proposals back to Mercury. Fail-hard at
// every step. The trade-off is operator velocity: any infrastructure issue
// (missing claude CLI, Mercury 401, malformed Claude output) kills the
// weekly run, but a successful run guarantees Mercury is fully categorized
// for the period before the report renders.
func classifyMercuryTransactions(client *external.MercuryClient, from, to time.Time) {
	if _, err := exec.LookPath("claude"); err != nil {
		log.Fatalf("classify: `claude` CLI not found in PATH — install Claude Code or pass --skip-classify")
	}

	log.Infof("classify: pulling Mercury card transactions for %s..%s", from.Format("2006-01-02"), to.Format("2006-01-02"))
	snapshotPath, err := classify.Pull(client, from, to)
	if err != nil {
		log.Fatalf("classify Pull: %v", err)
	}
	log.Infof("classify: snapshot written → %s", snapshotPath)

	proposalsPath := classify.ProposalsPath(to)
	// Pre-clear any stale proposals from a previous failed run — Claude
	// won't overwrite (it's writing fresh) but leftover applied.json from
	// a successful prior run shouldn't confuse us either.
	_ = os.Remove(proposalsPath)

	log.Infof("classify: invoking `claude` to classify (may take a moment)")
	cmd := exec.Command("claude", classify.PromptForPeriod(to))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("classify: `claude` invocation failed: %v", err)
	}

	if _, err := os.Stat(proposalsPath); err != nil {
		log.Fatalf("classify: Claude did not write the expected proposals file at %s (stat: %v) — re-run or pass --skip-classify to bypass", proposalsPath, err)
	}

	stats, err := classify.Apply(client, to)
	if err != nil {
		log.Fatalf("classify Apply: %v", err)
	}
	log.Infof("classify: complete — %d transactions patched, %d already correct", stats.Patched, stats.Skipped)
}

// fetchHQPeriodSummary returns the HQ-side COGS summary for the pay period
// or nil when the integration is not configured. On any other failure
// (HTTP error, decode error, or incomplete data) it terminates the run via
// log.Fatalf — the alternative is publishing a payroll PDF with silently
// wrong food cost numbers.
//
// mercuryClient is used for the Mercury↔HQ gap check that catches card
// transactions Mercury has surfaced but HQ's receipt worker hasn't
// ingested yet (see docs/payroll-mercury-gap-check.md). Pass nil to skip
// the gap check — appropriate when --skip-mercury is set and Mercury
// isn't reachable. In that mode COGS still ships but the late-arriving
// transaction class becomes blind.
func fetchHQPeriodSummary(mercuryClient *external.MercuryClient, from, to time.Time) *external.HQPeriodSummary {
	client, err := external.NewHQClientFromEnv()
	if err != nil {
		log.Fatalf("init HQ client: %v", err)
	}
	if client == nil {
		log.Info("HQ_INVENTORY_SERVICE_TOKEN not set — skipping Cost of Goods Sold section")
		return nil
	}

	summary, err := client.GetPeriodSummary(from, to)
	if err != nil {
		log.Fatalf("fetch HQ period summary: %v", err)
	}

	if !summary.Completeness.Ready {
		log.Fatal(formatHQCompletenessFailure(client.BaseURL(), summary))
	}

	// Mercury↔HQ gap check: Mercury sees card transactions the moment the
	// bank acks them; HQ only sees transactions its receipt worker has
	// polled. completeness.ready=true above means HQ has no unresolved
	// rows — but says nothing about transactions HQ hasn't ingested yet.
	// Cross-check Mercury against HQ's tracked_bank_tx_ids and fail-fast
	// on any gap so Restaurant Depot doesn't silently vanish from COGS.
	if mercuryClient == nil {
		log.Warn("Mercury client unavailable (--skip-mercury) — skipping Mercury↔HQ gap check")
		return summary
	}
	if summary.TrackedBankTxIDs == nil {
		log.Warnf(
			"HQ /period-summary response missing tracked_bank_tx_ids — "+
				"Mercury sync gap check is DEGRADED. Update HQ to ship the "+
				"cogs-hq-tracked-tx-ids change to re-enable. Proceeding for %s–%s.",
			summary.From, summary.To,
		)
		return summary
	}

	txns, err := mercuryClient.ListTransactionsInPeriod(from, to)
	if err != nil {
		log.Fatalf("fetch Mercury transactions for HQ gap check: %v", err)
	}

	gap := external.MercuryHQGap(txns, summary.TrackedBankTxIDs)
	if len(gap) > 0 {
		log.Fatal(formatMercuryGapFailure(summary.From, summary.To, gap))
	}

	return summary
}

// formatHQCompletenessFailure renders an operator-friendly multi-line
// banner for the "HQ has unresolved receipts" failure. The single-line
// FATA log buried the actionable bits (where to go, what to do) under
// UUID noise; this version leads with the count and a click-through to
// the inventory dashboard, and only dumps IDs at the bottom as debug
// detail. hqBaseURL is the HQ root (typically same origin as the API);
// HQ serves the static inventory.html UI from there.
func formatHQCompletenessFailure(hqBaseURL string, s *external.HQPeriodSummary) string {
	const bar = "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
	var b strings.Builder
	pending := len(s.Completeness.PendingReviewIDs)
	unlinked := len(s.Completeness.UnlinkedLineItemIDs)

	b.WriteString("\n")
	b.WriteString(bar + "\n")
	switch {
	case pending > 0 && unlinked > 0:
		fmt.Fprintf(&b, "  HQ COGS NOT READY — %d receipt(s) need review, %d line item(s) unlinked.\n", pending, unlinked)
	case pending > 0:
		fmt.Fprintf(&b, "  HQ COGS NOT READY — %d receipt(s) waiting for your review.\n", pending)
	case unlinked > 0:
		fmt.Fprintf(&b, "  HQ COGS NOT READY — %d line item(s) need a catalog match.\n", unlinked)
	}
	b.WriteString(bar + "\n\n")
	fmt.Fprintf(&b, "Pay period:  %s → %s\n\n", s.From, s.To)

	b.WriteString("What to do:\n")
	step := 1
	if pending > 0 {
		fmt.Fprintf(&b, "  %d. Open HQ Inventory → Purchases tab:\n", step)
		fmt.Fprintf(&b, "     %s/inventory.html#tab=1\n", hqBaseURL)
		fmt.Fprintf(&b, "  %d. Confirm or discard each pending receipt (%d total).\n", step+1, pending)
		step += 2
	}
	if unlinked > 0 {
		fmt.Fprintf(&b, "  %d. Link the %d unlinked line item(s) to catalog entries (same Purchases tab,\n", step, unlinked)
		b.WriteString("     scroll to the Unlinked section).\n")
		step++
	}
	fmt.Fprintf(&b, "  %d. Re-run sales-processor.\n\n", step)

	if pending > 0 {
		if len(s.Completeness.PendingReviewDetails) > 0 {
			b.WriteString("Pending receipts:\n")
			for _, d := range s.Completeness.PendingReviewDetails {
				reasonSuffix := ""
				if d.Reason != nil && *d.Reason != "" {
					reasonSuffix = "  (" + humanizePendingReason(*d.Reason) + ")"
				}
				vendor := d.Vendor
				if vendor == "" {
					vendor = "(vendor unknown)"
				}
				fmt.Fprintf(&b, "  - %s  %-22s  $%.2f%s\n", d.EventDate, vendor, d.BankTotal, reasonSuffix)
			}
		} else {
			// Older HQ (pre cogs-hq-pending-details handoff) — only UUIDs available.
			b.WriteString("Pending IDs (HQ doesn't expose details yet — update HQ to render vendor/date/$):\n")
			for _, id := range s.Completeness.PendingReviewIDs {
				fmt.Fprintf(&b, "  - %s\n", id)
			}
		}
	}
	if unlinked > 0 {
		b.WriteString("Unlinked line item IDs (for debugging):\n")
		for _, id := range s.Completeness.UnlinkedLineItemIDs {
			fmt.Fprintf(&b, "  - %s\n", id)
		}
	}
	return b.String()
}

// humanizePendingReason turns HQ's enum-ish reason strings into a short
// human-readable phrase for the failure banner. Unknown reasons pass
// through unchanged so they still convey *something* — better than
// dropping them silently.
func humanizePendingReason(reason string) string {
	switch reason {
	case "no_attachment_on_bank_tx":
		return "no receipt attached"
	case "receipt_parse_failed", "Receipt could not be parsed automatically":
		return "receipt couldn't be parsed"
	case "Receipt could not be saved automatically":
		return "receipt save failed"
	default:
		return reason
	}
}

// formatMercuryGapFailure renders the operator-friendly banner for the
// "Mercury saw card txns HQ hasn't ingested yet" failure. Same shape
// as formatHQCompletenessFailure so failures feel consistent across
// the two completeness gates.
func formatMercuryGapFailure(from, to string, gap []external.MercuryTransactionLite) string {
	const bar = "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
	var b strings.Builder

	b.WriteString("\n")
	b.WriteString(bar + "\n")
	fmt.Fprintf(&b, "  HQ COGS NOT READY — Mercury has %d card txn(s) HQ hasn't ingested yet.\n", len(gap))
	b.WriteString(bar + "\n\n")
	fmt.Fprintf(&b, "Pay period:  %s → %s\n\n", from, to)

	b.WriteString("What's happening:\n")
	b.WriteString("  Mercury surfaced new card transactions, but HQ's receipt worker hasn't\n")
	b.WriteString("  polled since. Continuing now would silently undercount COGS by the\n")
	b.WriteString("  amount(s) below.\n\n")

	b.WriteString("What to do:\n")
	b.WriteString("  1. Wait for the next receipt-worker poll (~6h) and re-run, OR\n")
	b.WriteString("  2. Kick the receipt worker manually on the HQ box, then re-run.\n\n")

	b.WriteString("Missing transactions:\n")
	for _, tx := range gap {
		fmt.Fprintf(&b, "  - %s  $%.2f  %s\n", tx.CreatedAt, tx.Amount, tx.BankDescription)
		fmt.Fprintf(&b, "    id=%s  %s\n", tx.ID, tx.DashboardLink)
	}
	return b.String()
}

// renderCOGSSection produces the Cost of Goods Sold drill-down section.
// Net Sales, total COGS, Food Cost %, and Operating Profit are rendered
// up front in the Summary section; this section adds the tax breakdown,
// receipt count, and per-vendor detail a manager would want when drilling
// into the COGS line.
func renderCOGSSection(s *external.HQPeriodSummary) string {
	var out strings.Builder
	out.WriteString("Cost of Goods Sold Detail\n")
	out.WriteString("-----------------------\n")
	out.WriteString("\n")

	tax := s.COGSInclTax - s.COGSExclTax

	out.WriteString(fmt.Sprintf("  COGS Pre-tax:    $%.2f\n", s.COGSExclTax))
	out.WriteString(fmt.Sprintf("  Tax:             $%.2f\n", tax))
	out.WriteString(fmt.Sprintf("  COGS Incl. Tax:  $%.2f\n", s.COGSInclTax))
	out.WriteString(fmt.Sprintf("  Receipts in HQ:  %d\n", s.PurchaseEventCount))

	if len(s.ByVendor) > 0 {
		out.WriteString("\n")
		out.WriteString("By Vendor\n")
		for _, v := range s.ByVendor {
			// Sanitize: colons in the vendor name would corrupt the
			// label/value split in renderReport's table parser.
			name := strings.ReplaceAll(v.VendorName, ":", " -")
			tripWord := "trips"
			if v.TripCount == 1 {
				tripWord = "trip"
			}
			out.WriteString(fmt.Sprintf("  %s (%d %s): $%.2f pre-tax ($%.2f incl tax)\n",
				name, v.TripCount, tripWord, v.TotalExclTax, v.TotalInclTax))
		}
	}

	return out.String()
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
