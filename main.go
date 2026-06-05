package main

import (
	"context"
	"encoding/base64"
	"flag"
	"encoding/csv"
	"fmt"
	"io/ioutil"
	"os"
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
	"jiaming2012/sales-processor/models"
	"jiaming2012/sales-processor/payroll"
	"jiaming2012/sales-processor/service"
	"jiaming2012/sales-processor/service/external"
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

	for _, localFileName := range []string{"OrderDetails.csv", "AllItemsReport.csv", "AccountingReport.xls", "ItemSelectionDetails.csv", "ModifiersSelectionDetails.csv", "PaymentDetails.csv", "TimeEntries.csv"} {
		// Download remote file.
		remoteFileName := fmt.Sprintf("/%s/%s/%s", exportId, date, localFileName)
		localFilePath := fmt.Sprintf("output/toast_reports/%s", date)

		file, err := client.Download(remoteFileName)
		if err != nil {
			continue
			log.Fatal(fmt.Errorf("failed to download %v: %w", remoteFileName, err))
		}

		bytes, err := ioutil.ReadAll(file)
		if err != nil {
			file.Close()
			log.Fatalf("failed to read bytes: %v", bytes)
		}

		// todo: save to database
		err = os.MkdirAll(localFilePath, os.ModePerm)
		if err != nil {
			file.Close()
			log.Fatal(err)
		}

		f, err := os.OpenFile(fmt.Sprintf("%s/%s", localFilePath, localFileName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			file.Close()
			log.Fatal(err)
		}

		f.Write(bytes)

		// process order details
		if localFileName == "OrderDetails.csv" {
			if err = gocsv.UnmarshalBytes(bytes, &orderDetails); err != nil {
				f.Close()
				file.Close()
				log.Fatal(err)
			}
		}

		// process payment details
		if localFileName == "PaymentDetails.csv" {
			if err = gocsv.UnmarshalBytes(bytes, &paymentDetails); err != nil {
				f.Close()
				file.Close()
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

		f.Close()
		file.Close()
	}

	return orderDetails
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
func CalculateWeeklyReport(dailyReport map[time.Time]models.DailySummary, timesheet models.Timesheet, employeeHours []models.EmployeeHours, previousEmployeeHours []models.EmployeeHours, cashEmployeesPay []models.CashEmployeePay) models.WeeklySummary {
	var tipDetails models.TipDetails
	tipDetails.Details = make(map[models.Employee]float64)
	totalSales := 0.0
	totalTaxes := 0.0
	totalCashTendered := 0.0
	totalCCFees := 0.0

	for reportTime, summary := range dailyReport {
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
		Hours:            employeeHours,
		PreviousHours:    previousEmployeeHours,
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

func executeTransfers(mercuryClient *external.MercuryClient, sourceAccount external.MercuryAccount, transfers []external.MercuryTransferRequest, autoApprove bool) {
	fmt.Println("\n--- Pending Transfers ---")
	for _, t := range transfers {
		fmt.Printf("  $%.2f from %s → %s (%s)\n", t.Amount, sourceAccount.Name, t.Note, t.ToAccountID)
	}

	if !autoApprove {
		fmt.Printf("\nExecute %d transfer(s)? (y/n): ", len(transfers))
		var answer string
		fmt.Scanln(&answer)

		if strings.ToLower(answer) != "y" {
			fmt.Println("Transfers skipped.")
			return
		}
	}

	for _, t := range transfers {
		if err := mercuryClient.CreateInternalTransfer(t); err != nil {
			log.Errorf("transfer failed (%s): %v", t.Note, err)
		} else {
			fmt.Printf("  Transferred $%.2f → %s\n", t.Amount, t.Note)
		}
	}
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

func executeExternalTransfer(mercuryClient *external.MercuryClient, sourceAccount external.MercuryAccount, recipient external.MercuryRecipient, transfer external.MercuryExternalTransferRequest, autoApprove bool) {
	fmt.Println("\n--- Pending External Transfer ---")
	fmt.Printf("  $%.2f from %s → %s (%s, %s)\n", transfer.Amount, sourceAccount.Name, recipient.Name, transfer.PaymentMethod, transfer.Note)

	if !autoApprove {
		fmt.Print("\nExecute external transfer? (y/n): ")
		var answer string
		fmt.Scanln(&answer)

		if strings.ToLower(answer) != "y" {
			fmt.Println("External transfer skipped.")
			return
		}
	}

	if err := mercuryClient.CreateExternalTransfer(transfer); err != nil {
		log.Errorf("external transfer failed (%s): %v", transfer.Note, err)
		return
	}
	fmt.Printf("  Transferred $%.2f → %s (%s)\n", transfer.Amount, recipient.Name, transfer.PaymentMethod)
}

func main() {
	autoApproveTransfers := flag.Bool("auto-approve-transfers", false, "automatically approve Mercury transfers without prompting")
	mercurySandbox := flag.Bool("sandbox", false, "use Mercury sandbox environment")
	rentHoldMethod := flag.String("rent-hold-method", external.MercuryPaymentMethodACH, "rent hold payment method: ach or domesticWire")
	flag.Parse()

	if *rentHoldMethod != external.MercuryPaymentMethodACH && *rentHoldMethod != external.MercuryPaymentMethodDomesticWire {
		log.Fatalf("invalid --rent-hold-method=%q (must be %q or %q)", *rentHoldMethod, external.MercuryPaymentMethodACH, external.MercuryPaymentMethodDomesticWire)
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

	commissionBasedEmployees := []models.CommissionBasedEmployee{
		{
			Id:                       100,
			Name:                     "Jamal Cole",
			CommissionSalesStructure: commissionSalesStructureOwner,
		},
		{
			Id:                       101,
			Name:                     "Latanya Mcgriff",
			CommissionSalesStructure: commissionSalesStructureStandard,
		},
	}

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
	mercuryAPIKey := os.Getenv("MERCURY_API_KEY")
	if mercuryAPIKey == "" {
		log.Fatal("MERCURY_API_KEY environment variable is required")
	}

	mercuryClient := external.NewMercuryClient(mercuryAPIKey, *mercurySandbox)
	mercuryAccounts, err := mercuryClient.ListAccounts()
	if err != nil {
		log.Fatalf("failed to list Mercury accounts: %v", err)
	}

	sourceAccount := resolveMercuryAccount(mercuryAccounts, "MERCURY_SOURCE_ACCOUNT_ID", "source (Operations)")
	salesTaxAccount := resolveMercuryAccount(mercuryAccounts, "MERCURY_SALES_TAX_ACCOUNT_ID", "sales tax")
	deferredTaxAccount := resolveMercuryAccount(mercuryAccounts, "MERCURY_DEFERRED_TAX_ACCOUNT_ID", "deferred taxes")

	mercuryRecipients, err := mercuryClient.ListRecipients()
	if err != nil {
		log.Fatalf("failed to list Mercury recipients: %v", err)
	}
	rentHoldRecipient := resolveMercuryRecipient(mercuryRecipients, "MERCURY_RENT_HOLD_RECIPIENT_ID", "rent hold (Personal Vacation Fun)")

	//--- Cash Held ---
	cashHeld := getCashHeld()

	//--- Get Cash Employee Wages ---
	cashEmployeeWages := getCashEmployeeWages(cashEmployees, defaultEmployeeTaxRate)

	//--- Report Headers (Current Timesheet) ---
	now := time.Now()
	sunday := service.GetDateLastSunday(now)
	dates := service.GetDatesStartingFromPreviousMonday(sunday)
	fromDate := dates[0].Format("2006-01-02")
	toDate := dates[len(dates)-1].Format("2006-01-02")

	//--- Report Headers (Previous Timesheet) ---
	lastWeek := now.AddDate(0, 0, -7)
	previousSunday := service.GetDateLastSunday(lastWeek)
	previousDates := service.GetDatesStartingFromPreviousMonday(previousSunday)
	previousFromDate := previousDates[0].Format("2006-01-02")
	previousToDate := previousDates[len(previousDates)-1].Format("2006-01-02")

	//--- Fetch Timesheets
	slingClient, err := external.NewSlingTimesheet(baseURL, slingEmail, slingPassword)
	if err != nil {
		panic(err)
	}

	if err = slingClient.PopulateUsers(commissionBasedEmployees); err != nil {
		panic(err)
	}

	currentTimesheet, err := slingClient.GetPayroll(fromDate, toDate)
	if err != nil {
		panic(err)
	}

	previousTimesheet, err := slingClient.GetPayroll(previousFromDate, previousToDate)
	if err != nil {
		panic(err)
	}

	//--- Process Timesheets ---
	var employeeHours []models.EmployeeHours
	for user, i := range currentTimesheet {
		if user.CommissionSalesStructure != nil {
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

	var previousEmployeeHours []models.EmployeeHours
	for user, i := range previousTimesheet {
		if user.CommissionSalesStructure != nil {
			log.Debugf("skip summing hours for commission based employee %v", user)
			continue
		}

		hours, err := external.SlingTimesheetItemShifts(i).GetTotalHours()
		if err != nil {
			panic(err)
		}

		previousEmployeeHours = append(previousEmployeeHours, models.EmployeeHours{
			Employee: user,
			Hours:    hours,
		})
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

	weeklySummary := CalculateWeeklyReport(dailyReport, ts, employeeHours, previousEmployeeHours, cashEmployeeWages)

	//--- todo: wait for manual input

	weeklySummary.Sales -= unpaidOrdersSummary.TotalSales
	reportOutput.WriteString(weeklySummary.Show())
	reportOutput.WriteString("\n")

	//--- Sales Commission Breakdown ---
	reportOutput.WriteString("Sales Commission Breakdown\n")
	reportOutput.WriteString("-----------------------\n")
	reportOutput.WriteString("\n")
	for _, empl := range commissionBasedEmployees {
		// todo: unify all employee models
		if empl.Name == "Jamal Cole" {
			continue
		}

		tips := weeklySummary.Tips.Details[models.Employee(empl.Name)]

		salesCommissionPercentage, err := empl.CommissionSalesStructure.GetSalesCommissionPercentage(&weeklySummary)
		if err != nil {
			log.Fatal(err)
		}

		// todo: cash held should be broken down by employee
		commissionBasedEmployeesSummary := models.NewCommissionBasedEmployeesTopLineSummary(previousDates[0], previousDates[len(previousDates)-1], empl.Name, weeklySummary.Sales, tips, salesCommissionPercentage, cashHeld, weeklySummary.CashTendered, rentHoldAmount, 0.20)

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
		}

		reportOutput.WriteString(commissionBasedEmployeesSummary.Show())
		reportOutput.WriteString("\n")
	}

	//--- Mercury Transfers ---
	payrollTaxes := 0.0
	for _, employee := range cashEmployeeWages {
		payrollTaxes += employee.Taxes
	}

	var transfers []external.MercuryTransferRequest
	if weeklySummary.SalesTax > 0 {
		transfers = append(transfers, external.MercuryTransferRequest{
			FromAccountID: sourceAccount.ID,
			ToAccountID:   salesTaxAccount.ID,
			Amount:        weeklySummary.SalesTax,
			Note:          fmt.Sprintf("Sales tax %s - %s", fromDate, toDate),
		})
	}
	if payrollTaxes > 0 {
		transfers = append(transfers, external.MercuryTransferRequest{
			FromAccountID: sourceAccount.ID,
			ToAccountID:   deferredTaxAccount.ID,
			Amount:        payrollTaxes,
			Note:          fmt.Sprintf("Deferred taxes %s - %s", fromDate, toDate),
		})
	}

	if len(transfers) > 0 {
		executeTransfers(mercuryClient, sourceAccount, transfers, *autoApproveTransfers)
	}

	if rentHoldAmount > 0 {
		rentHoldTransfer := external.MercuryExternalTransferRequest{
			FromAccountID: sourceAccount.ID,
			RecipientID:   rentHoldRecipient.ID,
			Amount:        rentHoldAmount,
			Note:          fmt.Sprintf("Rent hold %s - %s", fromDate, toDate),
			PaymentMethod: *rentHoldMethod,
		}
		if *rentHoldMethod == external.MercuryPaymentMethodDomesticWire {
			rentHoldTransfer.Purpose = &external.MercurySendMoneyPurpose{
				Simple: external.MercurySendMoneyPurposeSimple{
					Category:       external.MercuryPurposeTransferToMyExternalAccount,
					AdditionalInfo: fmt.Sprintf("Rent hold %s - %s", fromDate, toDate),
				},
			}
		}
		executeExternalTransfer(mercuryClient, sourceAccount, rentHoldRecipient, rentHoldTransfer, *autoApproveTransfers)
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

	writePDF(reportOutput.String(), previousFromDate, toDate)

	f, err := os.Create(fmt.Sprintf("output/payroll/payroll_%v.csv", toDate))
	if err != nil {
		panic(err)
	}

	if err = payroll.Entries(entries).ToCSV(f); err != nil {
		panic(err)
	}
}

func writePDF(report string, fromDate string, toDate string) {
	header := fmt.Sprintf("Sales Report for %s - %s\n\n", fromDate, toDate)

	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.AddPage()
	pdf.SetFont("Helvetica", "", 16)
	pdf.MultiCell(0, 10, header, "", "", false)
	pdf.MultiCell(0, 10, report, "", "", false)

	if err := pdf.OutputFileAndClose(fmt.Sprintf("output/payroll/payroll_%v.pdf", toDate)); err != nil {
		panic(err)
	}
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
