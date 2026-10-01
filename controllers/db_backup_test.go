package controllers

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupDBBackupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// Shared-cache in-memory database with a unique name per invocation:
	// file::memory: gives every pooled connection its own empty DB, so the
	// scheduler goroutine and the test's polling queries would race onto
	// different databases. Pinning the pool to one connection additionally
	// serializes access against VACUUM INTO.
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:dbbackup-%s?mode=memory&cache=shared", uuid.New())), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&models.DBBackupSettings{}, &models.DBBackupRecord{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	previousDB := utils.DB
	previousDialect := utils.CurrentDialect()
	utils.DB = db
	utils.SetDialect(utils.DialectSQLite)
	t.Cleanup(func() {
		// Wait for any in-flight scheduled run: it keeps using utils.DB, so
		// restoring globals or closing the pool first would race/panic.
		dbBackupMu.Lock()
		dbBackupMu.Unlock()
		utils.DB = previousDB
		utils.SetDialect(previousDialect)
		sqlDB.Close()
	})
	return db
}

func TestDBBackupLastScheduledInstantDaily(t *testing.T) {
	loc := dbMaintenanceLocation()
	s := models.DBBackupSettings{Frequency: "daily", RunTime: "02:00"}

	// After 02:00 — most recent scheduled instant is today 02:00.
	now := time.Date(2026, 9, 27, 10, 0, 0, 0, loc)
	got := dbBackupLastScheduledInstant(now, s)
	if got.Format("2006-01-02 15:04") != "2026-09-27 02:00" {
		t.Fatalf("daily after runtime: got %s", got)
	}

	// Before 02:00 — most recent scheduled instant is yesterday 02:00.
	now = time.Date(2026, 9, 27, 1, 0, 0, 0, loc)
	got = dbBackupLastScheduledInstant(now, s)
	if got.Format("2006-01-02 15:04") != "2026-09-26 02:00" {
		t.Fatalf("daily before runtime: got %s", got)
	}
}

func TestDBBackupLastScheduledInstantWeekly(t *testing.T) {
	loc := dbMaintenanceLocation()
	// Weekly on Monday (weekday=1) at 03:30.
	s := models.DBBackupSettings{Frequency: "weekly", RunTime: "03:30", Weekday: 1}

	// Wednesday 12:00 → most recent is Monday 03:30 (2026-09-28 is a Monday;
	// 2026-09-30 is a Wednesday).
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, loc)
	got := dbBackupLastScheduledInstant(now, s)
	if got.Format("2006-01-02 15:04") != "2026-09-28 03:30" {
		t.Fatalf("weekly mid-week: got %s", got)
	}

	// Monday before 03:30 → most recent is previous Monday.
	now = time.Date(2026, 9, 28, 1, 0, 0, 0, loc)
	got = dbBackupLastScheduledInstant(now, s)
	if got.Format("2006-01-02 15:04") != "2026-09-21 03:30" {
		t.Fatalf("weekly before runtime: got %s", got)
	}
}

func TestDBBackupLastScheduledInstantMonthly(t *testing.T) {
	loc := dbMaintenanceLocation()
	// Monthly on day 15 at 02:00.
	s := models.DBBackupSettings{Frequency: "monthly", RunTime: "02:00", MonthDay: 15}

	now := time.Date(2026, 9, 20, 12, 0, 0, 0, loc)
	got := dbBackupLastScheduledInstant(now, s)
	if got.Format("2006-01-02 15:04") != "2026-09-15 02:00" {
		t.Fatalf("monthly after day: got %s", got)
	}

	now = time.Date(2026, 9, 10, 12, 0, 0, 0, loc)
	got = dbBackupLastScheduledInstant(now, s)
	if got.Format("2006-01-02 15:04") != "2026-08-15 02:00" {
		t.Fatalf("monthly before day: got %s", got)
	}
}

func TestNextDBBackupRun(t *testing.T) {
	loc := dbMaintenanceLocation()

	daily := models.DBBackupSettings{Frequency: "daily", RunTime: "02:00"}
	now := time.Date(2026, 9, 27, 1, 0, 0, 0, loc)
	if got := nextDBBackupRun(now, daily); got.Format("2006-01-02 15:04") != "2026-09-27 02:00" {
		t.Fatalf("daily next: got %s", got)
	}
	now = time.Date(2026, 9, 27, 3, 0, 0, 0, loc)
	if got := nextDBBackupRun(now, daily); got.Format("2006-01-02 15:04") != "2026-09-28 02:00" {
		t.Fatalf("daily next tomorrow: got %s", got)
	}

	weekly := models.DBBackupSettings{Frequency: "weekly", RunTime: "03:30", Weekday: 1}
	now = time.Date(2026, 9, 30, 12, 0, 0, 0, loc) // Wednesday
	if got := nextDBBackupRun(now, weekly); got.Format("2006-01-02 15:04") != "2026-10-05 03:30" {
		t.Fatalf("weekly next: got %s", got)
	}

	monthly := models.DBBackupSettings{Frequency: "monthly", RunTime: "02:00", MonthDay: 15}
	now = time.Date(2026, 9, 20, 12, 0, 0, 0, loc)
	if got := nextDBBackupRun(now, monthly); got.Format("2006-01-02 15:04") != "2026-10-15 02:00" {
		t.Fatalf("monthly next: got %s", got)
	}
}

func TestGetOrCreateDBBackupSettings(t *testing.T) {
	setupDBBackupTestDB(t)

	settings, err := GetOrCreateDBBackupSettings()
	if err != nil {
		t.Fatalf("get-or-create: %v", err)
	}
	if settings.IsEnabled {
		t.Fatal("expected default settings to be disabled")
	}
	if settings.DestinationType != "local" || settings.Frequency != "daily" {
		t.Fatalf("unexpected defaults: %+v", settings)
	}

	again, err := GetOrCreateDBBackupSettings()
	if err != nil {
		t.Fatalf("get-or-create (second): %v", err)
	}
	if again.ID != settings.ID {
		t.Fatal("expected singleton settings row to be reused")
	}
}

func TestRunDBBackupSQLite(t *testing.T) {
	setupDBBackupTestDB(t)
	t.Setenv("DB_BACKUP_DIR", t.TempDir())

	settings := models.DBBackupSettings{
		ID:              uuid.New(),
		Frequency:       "daily",
		RunTime:         "02:00",
		DestinationType: "local",
		RetentionCount:  5,
	}
	if err := utils.DB.Create(&settings).Error; err != nil {
		t.Fatalf("create settings: %v", err)
	}

	updated := runDBBackup(settings, "manual")
	if updated.LastRunStatus != "success" {
		t.Fatalf("expected success, got %q (error: %s)", updated.LastRunStatus, updated.LastRunError)
	}
	if updated.LastRunFile == "" || updated.LastRunSizeBytes <= 0 {
		t.Fatalf("expected file metadata, got file=%q size=%d",
			updated.LastRunFile, updated.LastRunSizeBytes)
	}

	var record models.DBBackupRecord
	if err := utils.DB.Where("file_name = ?", updated.LastRunFile).First(&record).Error; err != nil {
		t.Fatalf("load record: %v", err)
	}
	if _, err := os.Stat(record.FilePath); err != nil {
		t.Fatalf("backup file missing on disk: %v", err)
	}
	if filepath.Ext(record.FilePath) != ".gz" {
		t.Fatalf("expected compressed backup, got %s", record.FilePath)
	}

	// The archive must decompress to a valid SQLite database file.
	f, err := os.Open(record.FilePath)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	data, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	if !strings.HasPrefix(string(data), "SQLite format 3") {
		t.Fatal("expected decompressed backup to be a SQLite database")
	}
}

func TestProcessDueDBBackupDisabled(t *testing.T) {
	setupDBBackupTestDB(t)
	t.Setenv("DB_BACKUP_DIR", t.TempDir())

	processDueDBBackup()

	var settings models.DBBackupSettings
	if err := utils.DB.First(&settings).Error; err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if settings.LastRunAt != nil {
		t.Fatal("expected no run while disabled")
	}
}

func TestProcessDueDBBackupRunsWhenDue(t *testing.T) {
	setupDBBackupTestDB(t)
	t.Setenv("DB_BACKUP_DIR", t.TempDir())

	settings := models.DBBackupSettings{
		ID:              uuid.New(),
		IsEnabled:       true,
		Frequency:       "daily",
		RunTime:         "00:00", // already due today
		DestinationType: "local",
		RetentionCount:  5,
	}
	if err := utils.DB.Create(&settings).Error; err != nil {
		t.Fatalf("create settings: %v", err)
	}

	processDueDBBackup()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var refreshed models.DBBackupSettings
		if err := utils.DB.First(&refreshed).Error; err != nil {
			t.Fatalf("reload settings: %v", err)
		}
		if refreshed.LastRunAt != nil {
			if refreshed.LastRunStatus != "success" {
				t.Fatalf("expected success, got %q (error: %s)",
					refreshed.LastRunStatus, refreshed.LastRunError)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("expected backup run to complete")
}

func TestValidateDBBackupDestination(t *testing.T) {
	ok := models.DBBackupSettings{DestinationType: "local"}
	if err := validateDBBackupDestination(&ok); err != nil {
		t.Fatalf("local destination should need no config: %v", err)
	}

	missing := models.DBBackupSettings{DestinationType: "telegram"}
	if err := validateDBBackupDestination(&missing); err == nil {
		t.Fatal("expected telegram without token/chat to fail validation")
	}

	s3 := models.DBBackupSettings{DestinationType: "s3", S3Bucket: "b", S3AccessKey: "k", S3SecretKey: "enc"}
	if err := validateDBBackupDestination(&s3); err != nil {
		t.Fatalf("configured s3 destination failed validation: %v", err)
	}
}
