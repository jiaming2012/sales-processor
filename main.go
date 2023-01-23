package main

import (
	"encoding/csv"
	"fmt"
	log "github.com/sirupsen/logrus"
	"jiaming2012/sales-processor/database"
	"jiaming2012/sales-processor/models"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
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
		case "Gross Price":
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
		case "Net Price":
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
		case "Tax":
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

func main() {
	setupDB()
	iterateDirectory("sales/unprocessed")
	log.Info("Successfully ran sales processor")
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
