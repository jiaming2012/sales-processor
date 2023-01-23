package models

import (
	"fmt"
	"gorm.io/gorm"
	"strconv"
	"time"
)

type Sale struct {
	gorm.Model
	OrderId       string
	OrderNumber   uint
	Name          string
	Position      uint      `gorm:"uniqueIndex:compositeItemDetail;not null"`
	Timestamp     time.Time `gorm:"uniqueIndex:compositeItemDetail;not null"`
	ItemId        string
	MenuSubgroups string
	MenuGroup     string
	SalesCategory string
	GrossPrice    float64
	Discount      float64
	NetPrice      float64
	Quantity      float64
	Tax           float64
	Void          bool
}

func padZeros(val int) string {
	if val < 10 {
		return fmt.Sprintf("0%d", val)
	}

	return strconv.Itoa(val)
}

func getStartOfDayTimestamp(ts time.Time) (time.Time, error) {
	startYear, startMonth, startDay := ts.Date()
	startDayStr := fmt.Sprintf("%d-%s-%s", startYear, padZeros(int(startMonth)), padZeros(startDay))
	beginTimestamp, err := time.Parse("2006-01-02", startDayStr)
	if err != nil {
		return time.Date(0, 0, 0, 0, 0, 0, 0, nil), err
	}

	return beginTimestamp, nil
}

func DeleteSalesAbove(position int, timestamp time.Time, db *gorm.DB) error {
	beginTimestamp, err := getStartOfDayTimestamp(timestamp)
	if err != nil {
		return err
	}

	tx := db.Unscoped().Where("timestamp >= ?", beginTimestamp).Where("timestamp < ?", beginTimestamp.Add(24*time.Hour)).Where("position >= ?", position).Delete(&Sale{})

	return tx.Error
}

func FetchTotalSales(timestamp time.Time, db *gorm.DB) (int64, error) {
	var count int64

	beginTimestamp, err := getStartOfDayTimestamp(timestamp)
	if err != nil {
		return 0, err
	}

	tx := db.Model(Sale{}).Where("timestamp >= ?", beginTimestamp).Where("timestamp < ?", beginTimestamp.Add(24*time.Hour)).Count(&count)

	if tx.Error != nil {
		return 0, tx.Error
	}

	return count, nil
}
