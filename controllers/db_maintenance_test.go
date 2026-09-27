package controllers

import (
	"testing"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupDBMaintenanceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.DBMaintenanceSettings{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	previousDB := utils.DB
	previousDialect := utils.CurrentDialect()
	utils.DB = db
	utils.SetDialect(utils.DialectSQLite)
	t.Cleanup(func() {
		utils.DB = previousDB
		utils.SetDialect(previousDialect)
	})
	return db
}

func TestDBMaintenanceRunTimeOrDefault(t *testing.T) {
	if got := dbMaintenanceRunTimeOrDefault(""); got != "01:00" {
		t.Fatalf("empty run time: got %q", got)
	}
	if got := dbMaintenanceRunTimeOrDefault("23:45"); got != "23:45" {
		t.Fatalf("valid run time: got %q", got)
	}
	if got := dbMaintenanceRunTimeOrDefault("9:99"); got != "01:00" {
		t.Fatalf("invalid run time: got %q", got)
	}
}

func TestNextDBMaintenanceRun(t *testing.T) {
	loc := dbMaintenanceLocation()

	// Before 01:00 IST -> runs today at 01:00.
	now := time.Date(2026, 9, 27, 0, 30, 0, 0, loc)
	next := nextDBMaintenanceRun(now, "01:00")
	if next.Format("15:04") != "01:00" || next.Format("2006-01-02") != "2026-09-27" {
		t.Fatalf("expected 2026-09-27 01:00, got %s", next)
	}

	// After 01:00 IST -> runs tomorrow at 01:00.
	now = time.Date(2026, 9, 27, 10, 0, 0, 0, loc)
	next = nextDBMaintenanceRun(now, "01:00")
	if next.Format("15:04") != "01:00" || next.Format("2006-01-02") != "2026-09-28" {
		t.Fatalf("expected 2026-09-28 01:00, got %s", next)
	}
}

func TestGetOrCreateDBMaintenanceSettings(t *testing.T) {
	setupDBMaintenanceTestDB(t)

	settings, err := GetOrCreateDBMaintenanceSettings()
	if err != nil {
		t.Fatalf("get-or-create: %v", err)
	}
	if settings.IsEnabled {
		t.Fatal("expected default settings to be disabled")
	}
	if settings.RunTime != "01:00" {
		t.Fatalf("expected default run time 01:00, got %q", settings.RunTime)
	}

	// Second call must return the same row (singleton).
	again, err := GetOrCreateDBMaintenanceSettings()
	if err != nil {
		t.Fatalf("get-or-create (second): %v", err)
	}
	if again.ID != settings.ID {
		t.Fatal("expected singleton settings row to be reused")
	}
}

func TestRunDBMaintenanceSQLite(t *testing.T) {
	setupDBMaintenanceTestDB(t)

	settings := models.DBMaintenanceSettings{ID: uuid.New(), RunTime: "01:00"}
	if err := utils.DB.Create(&settings).Error; err != nil {
		t.Fatalf("create settings: %v", err)
	}

	updated := runDBMaintenance(settings)
	if updated.LastRunStatus != "success" {
		t.Fatalf("expected success, got %q (error: %s)", updated.LastRunStatus, updated.LastRunError)
	}
	if updated.LastRunAt == nil {
		t.Fatal("expected last_run_at to be set")
	}
}

func TestProcessDueDBMaintenanceDisabled(t *testing.T) {
	setupDBMaintenanceTestDB(t)

	// Disabled (default) — the scheduler must not start a run.
	processDueDBMaintenance()

	var settings models.DBMaintenanceSettings
	if err := utils.DB.First(&settings).Error; err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if settings.LastRunAt != nil {
		t.Fatal("expected no run while disabled")
	}
}

func TestProcessDueDBMaintenanceRunsAfterRunTime(t *testing.T) {
	setupDBMaintenanceTestDB(t)

	loc := dbMaintenanceLocation()
	nowIST := time.Now().In(loc)

	// Configure a run time one minute before "now" so the job is due.
	runTime := nowIST.Add(-time.Minute).Format("15:04")
	settings := models.DBMaintenanceSettings{
		ID:        uuid.New(),
		IsEnabled: true,
		RunTime:   runTime,
	}
	if err := utils.DB.Create(&settings).Error; err != nil {
		t.Fatalf("create settings: %v", err)
	}

	processDueDBMaintenance()

	// The run happens in a background goroutine — wait briefly for it.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var refreshed models.DBMaintenanceSettings
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
	t.Fatal("expected maintenance run to complete")
}

func TestProcessDueDBMaintenanceSkipsBeforeRunTime(t *testing.T) {
	setupDBMaintenanceTestDB(t)

	loc := dbMaintenanceLocation()
	nowIST := time.Now().In(loc)

	// Configure a run time in the future (wrap around midnight safely).
	runTime := nowIST.Add(2 * time.Minute).Format("15:04")
	settings := models.DBMaintenanceSettings{
		ID:        uuid.New(),
		IsEnabled: true,
		RunTime:   runTime,
	}
	if err := utils.DB.Create(&settings).Error; err != nil {
		t.Fatalf("create settings: %v", err)
	}

	// If "now + 2min" crosses midnight, HH:MM comparison still holds (runTime
	// is later on the same minute-resolution day only if not wrapped).
	if nowIST.Format("15:04") < runTime {
		processDueDBMaintenance()
		var refreshed models.DBMaintenanceSettings
		if err := utils.DB.First(&refreshed).Error; err != nil {
			t.Fatalf("reload settings: %v", err)
		}
		if refreshed.LastRunAt != nil {
			t.Fatal("expected no run before configured run time")
		}
	}
}
