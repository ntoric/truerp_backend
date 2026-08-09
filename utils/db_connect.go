package utils

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func resolveDatabaseURL() string {
	if custom := strings.TrimSpace(os.Getenv("DATABASE_URL")); custom != "" {
		return custom
	}

	host := strings.TrimSpace(os.Getenv("POSTGRES_HOST"))
	user := strings.TrimSpace(os.Getenv("POSTGRES_USER"))
	dbName := strings.TrimSpace(os.Getenv("POSTGRES_DB"))
	if host == "" || user == "" || dbName == "" {
		return ""
	}

	port := strings.TrimSpace(os.Getenv("POSTGRES_PORT"))
	if port == "" {
		port = "5432"
	}
	password := os.Getenv("POSTGRES_PASSWORD")

	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(user, password),
		Host:   fmt.Sprintf("%s:%s", host, port),
		Path:   "/" + dbName,
	}
	q := u.Query()
	if sslmode := strings.TrimSpace(os.Getenv("POSTGRES_SSLMODE")); sslmode != "" {
		q.Set("sslmode", sslmode)
	} else {
		q.Set("sslmode", "disable")
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func openPostgres(databaseURL string) (*gorm.DB, error) {
	log.Printf("Connecting to PostgreSQL")

	db, err := gorm.Open(postgres.Open(databaseURL), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Info),
	})
	if err != nil {
		return nil, err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(25)
	sqlDB.SetMaxIdleConns(5)

	if err := db.Exec(`CREATE EXTENSION IF NOT EXISTS "uuid-ossp"`).Error; err != nil {
		log.Printf("Warning: uuid-ossp extension setup failed (UUID defaults may require app-generated IDs): %v", err)
	}

	return db, nil
}

func openSQLite() (*gorm.DB, error) {
	dbPath := resolveDatabasePath()
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}

	log.Printf("Opening SQLite database: %s", dbPath)

	return gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Info),
	})
}

func openDatabase() (*gorm.DB, Dialect, error) {
	if databaseURL := resolveDatabaseURL(); databaseURL != "" {
		db, err := openPostgres(databaseURL)
		if err != nil {
			return nil, "", err
		}
		return db, DialectPostgres, nil
	}

	db, err := openSQLite()
	if err != nil {
		return nil, "", err
	}
	return db, DialectSQLite, nil
}
