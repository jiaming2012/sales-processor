package database

import (
	"os"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"sync"
)

var _db *gorm.DB
var mutex sync.Mutex

func Setup() error {
	var err error
	_db, err = gorm.Open(postgres.Open(os.Getenv("DATABASE_URL")), &gorm.Config{})
	if err != nil {
		return err
	}

	return nil
}

func GetDB() *gorm.DB {
	mutex.Lock()
	return _db
}

func ReleaseDB() {
	mutex.Unlock()
}
