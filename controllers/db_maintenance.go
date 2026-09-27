package controllers

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// dbMaintenanceTimezone is the IANA timezone the daily run time is
// interpreted in. It is fixed to IST (Asia/Kolkata, UTC+5:30 — no DST).
const dbMaintenanceTimezone = "Asia/Kolkata"

const defaultDBMaintenanceRunTime = "01:00"

// dbMaintenanceMu serializes maintenance runs so a scheduled run and a
// manual "run now" trigger can never overlap. dbMaintenanceRunning exposes
// the same state to API responses so the UI can show progress.
var (
	dbMaintenanceMu      sync.Mutex
	dbMaintenanceRunning atomic.Bool
)

// dbMaintenanceLocation returns the *time.Location used by the scheduler.
// Falls back to a fixed +05:30 zone when the tz database is unavailable
// (identical to Asia/Kolkata, which never observes DST).
func dbMaintenanceLocation() *time.Location {
	loc, err := time.LoadLocation(dbMaintenanceTimezone)
	if err != nil {
		return time.FixedZone("IST", 5*3600+30*60)
	}
	return loc
}

// dbMaintenanceRunTimeOrDefault validates/pads the configured run time.
func dbMaintenanceRunTimeOrDefault(runTime string) string {
	runTime = strings.TrimSpace(runTime)
	if _, err := time.Parse("15:04", runTime); err != nil {
		return defaultDBMaintenanceRunTime
	}
	return runTime
}

// nextDBMaintenanceRun computes the next instant (in the scheduler timezone)
// at which the job will fire given the configured HH:MM run time.
func nextDBMaintenanceRun(now time.Time, runTime string) time.Time {
	loc := dbMaintenanceLocation()
	now = now.In(loc)
	parsed, err := time.Parse("15:04", dbMaintenanceRunTimeOrDefault(runTime))
	if err != nil {
		parsed, _ = time.Parse("15:04", defaultDBMaintenanceRunTime)
	}
	next := time.Date(now.Year(), now.Month(), now.Day(),
		parsed.Hour(), parsed.Minute(), 0, 0, loc)
	if !now.Before(next) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

// GetOrCreateDBMaintenanceSettings returns the singleton maintenance settings
// row, creating a disabled default row if one does not yet exist.
func GetOrCreateDBMaintenanceSettings() (models.DBMaintenanceSettings, error) {
	var settings models.DBMaintenanceSettings
	if err := utils.DB.Order("created_at asc").First(&settings).Error; err == nil {
		return settings, nil
	}
	settings = models.DBMaintenanceSettings{
		ID:      uuid.New(),
		RunTime: defaultDBMaintenanceRunTime,
	}
	if err := utils.DB.Create(&settings).Error; err != nil {
		return settings, err
	}
	return settings, nil
}

// GetDBMaintenanceSettingsHandler returns the maintenance configuration plus
// the current IST clock and the computed next run instant.
func GetDBMaintenanceSettingsHandler(c *gin.Context) {
	settings, err := GetOrCreateDBMaintenanceSettings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load maintenance settings"})
		return
	}

	loc := dbMaintenanceLocation()
	now := time.Now().In(loc)
	c.JSON(http.StatusOK, gin.H{
		"settings":     settings,
		"running":      dbMaintenanceRunning.Load(),
		"ist_time":     now.Format("2006-01-02 15:04:05"),
		"ist_timezone": loc.String(),
		"next_run_at":  nextDBMaintenanceRun(now, settings.RunTime).Format(time.RFC3339),
	})
}

// UpdateDBMaintenanceSettingsHandler enables/disables the nightly maintenance
// job and configures the daily run time (HH:MM, interpreted in IST).
func UpdateDBMaintenanceSettingsHandler(c *gin.Context) {
	var input struct {
		IsEnabled  bool   `json:"is_enabled"`
		RunTime    string `json:"run_time"`
		VacuumFull bool   `json:"vacuum_full"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	runTime := strings.TrimSpace(input.RunTime)
	if runTime == "" {
		runTime = defaultDBMaintenanceRunTime
	}
	if _, err := time.Parse("15:04", runTime); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "run_time must be in HH:MM (24h) format, e.g. 01:00"})
		return
	}

	settings, err := GetOrCreateDBMaintenanceSettings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load maintenance settings"})
		return
	}

	settings.IsEnabled = input.IsEnabled
	settings.RunTime = runTime
	settings.VacuumFull = input.VacuumFull

	if err := utils.DB.Save(&settings).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save maintenance settings"})
		return
	}

	c.JSON(http.StatusOK, settings)
}

// RunDBMaintenanceNowHandler manually triggers a maintenance run. The run
// executes in the background; poll GET /db-maintenance for status.
func RunDBMaintenanceNowHandler(c *gin.Context) {
	settings, err := GetOrCreateDBMaintenanceSettings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load maintenance settings"})
		return
	}

	if !dbMaintenanceMu.TryLock() {
		c.JSON(http.StatusConflict, gin.H{"error": "A maintenance run is already in progress"})
		return
	}

	go func() {
		defer dbMaintenanceMu.Unlock()
		runDBMaintenance(settings)
	}()

	c.JSON(http.StatusAccepted, gin.H{"message": "Maintenance run started"})
}

// StartDBMaintenanceScheduler launches a background goroutine that checks the
// maintenance settings every minute and runs the dead-tuple/bloat cleanup
// once per day after the configured run time (IST). It should be called once
// at application startup. An immediate check runs on startup so a run time
// that passed while the server was down is still honored (catch-up).
func StartDBMaintenanceScheduler() {
	go func() {
		// Recover from panics so the scheduler goroutine never silently dies.
		defer func() {
			if r := recover(); r != nil {
				log.Printf("db maintenance scheduler: PANIC recovered: %v", r)
			}
		}()

		loc := dbMaintenanceLocation()
		log.Printf("db maintenance scheduler: started (timezone: %s, server time: %s, IST: %s)",
			loc.String(),
			time.Now().Format("2006-01-02 15:04:05"),
			time.Now().In(loc).Format("2006-01-02 15:04:05"))

		// Catch up immediately on startup if today's run was missed while
		// the server was down.
		processDueDBMaintenance()

		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("db maintenance scheduler: PANIC in tick: %v", r)
					}
				}()
				processDueDBMaintenance()
			}()
		}
	}()
}

// processDueDBMaintenance runs the maintenance job when it is enabled, the
// configured IST run time has been reached, and the job has not already run
// today. Settings are reloaded each tick so toggling them takes effect
// without a restart.
func processDueDBMaintenance() {
	settings, err := GetOrCreateDBMaintenanceSettings()
	if err != nil {
		log.Printf("db maintenance scheduler: load settings failed: %v", err)
		return
	}
	if !settings.IsEnabled {
		return
	}

	loc := dbMaintenanceLocation()
	now := time.Now().In(loc)
	runTime := dbMaintenanceRunTimeOrDefault(settings.RunTime)

	// Only run once the configured time has been reached (HH:MM compared in IST).
	if now.Format("15:04") < runTime {
		return
	}

	// Skip if the job already ran today (any run — scheduled or manual — counts).
	if settings.LastRunAt != nil &&
		settings.LastRunAt.In(loc).Format("2006-01-02") == now.Format("2006-01-02") {
		return
	}

	if !dbMaintenanceMu.TryLock() {
		return // a run is already in progress
	}
	go func() {
		defer dbMaintenanceMu.Unlock()
		runDBMaintenance(settings)
	}()
}

// runDBMaintenance performs the vacuum pass, persists the outcome on the
// settings row, and returns the updated settings.
func runDBMaintenance(settings models.DBMaintenanceSettings) models.DBMaintenanceSettings {
	dbMaintenanceRunning.Store(true)
	defer dbMaintenanceRunning.Store(false)

	start := time.Now()
	log.Printf("db maintenance: starting (dialect=%s, vacuum_full=%v)", utils.CurrentDialect(), settings.VacuumFull)

	tables := 0
	var deadTuples int64
	var runErr error

	switch {
	case utils.IsPostgres():
		tables, deadTuples, runErr = vacuumPostgresTables(settings.VacuumFull)
	case utils.IsSQLite():
		runErr = vacuumSQLiteDatabase()
	default:
		runErr = fmt.Errorf("unsupported database dialect %q", utils.CurrentDialect())
	}

	duration := time.Since(start)
	now := time.Now()
	settings.LastRunAt = &now
	settings.LastRunTables = tables
	settings.LastRunDurationMs = duration.Milliseconds()
	settings.LastRunError = ""
	switch {
	case runErr != nil && tables > 0:
		settings.LastRunStatus = "partial"
		settings.LastRunError = runErr.Error()
	case runErr != nil:
		settings.LastRunStatus = "failed"
		settings.LastRunError = runErr.Error()
	default:
		settings.LastRunStatus = "success"
	}
	if err := utils.DB.Save(&settings).Error; err != nil {
		log.Printf("db maintenance: failed to persist run status: %v", err)
	}

	if runErr != nil {
		log.Printf("db maintenance: finished with errors in %s (tables=%d, status=%s): %v",
			duration, tables, settings.LastRunStatus, runErr)
	} else {
		log.Printf("db maintenance: completed in %s (tables=%d, dead tuples observed=%d)",
			duration, tables, deadTuples)
	}
	return settings
}

// pgTableStat mirrors one row of pg_stat_user_tables (aliased columns).
type pgTableStat struct {
	SchemaName string `gorm:"column:schema_name"`
	TableName  string `gorm:"column:table_name"`
	DeadTuples int64  `gorm:"column:dead_tuples"`
	LiveTuples int64  `gorm:"column:live_tuples"`
}

// quotePGIdent double-quotes a PostgreSQL identifier.
func quotePGIdent(ident string) string {
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}

// vacuumPostgresTables runs VACUUM (ANALYZE) — or VACUUM (FULL, ANALYZE) when
// full is set — on every user table in the database. Per-table vacuum is used
// instead of a database-wide VACUUM so progress is logged per table and
// individual failures don't abort the whole pass. Regular VACUUM removes
// dead tuples and marks their space reusable (preventing further bloat)
// without blocking reads/writes; VACUUM FULL additionally rewrites each
// table to actually shrink files, at the cost of an exclusive lock.
//
// Note: VACUUM cannot run inside a transaction block — gorm.Exec issues each
// statement in autocommit mode, which satisfies that requirement.
func vacuumPostgresTables(full bool) (tables int, deadTuples int64, err error) {
	var stats []pgTableStat
	if err := utils.DB.Raw(`
		SELECT schemaname AS schema_name,
		       relname    AS table_name,
		       n_dead_tup AS dead_tuples,
		       n_live_tup AS live_tuples
		FROM pg_stat_user_tables
		ORDER BY n_dead_tup DESC
	`).Scan(&stats).Error; err != nil {
		return 0, 0, fmt.Errorf("list user tables: %w", err)
	}

	verb := "VACUUM (ANALYZE)"
	if full {
		verb = "VACUUM (FULL, ANALYZE)"
	}

	var firstErr error
	for _, t := range stats {
		deadTuples += t.DeadTuples
		qualified := quotePGIdent(t.SchemaName) + "." + quotePGIdent(t.TableName)
		if vErr := utils.DB.Exec(verb + " " + qualified).Error; vErr != nil {
			log.Printf("db maintenance: %s on %s failed (dead_tuples=%d): %v",
				verb, qualified, t.DeadTuples, vErr)
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", qualified, vErr)
			}
			continue
		}
		tables++
		log.Printf("db maintenance: %s on %s ok (dead_tuples=%d, live_tuples=%d)",
			verb, qualified, t.DeadTuples, t.LiveTuples)
	}
	return tables, deadTuples, firstErr
}

// vacuumSQLiteDatabase rebuilds the SQLite database file (reclaiming freelist
// pages / bloat) and refreshes the query planner statistics.
func vacuumSQLiteDatabase() error {
	if err := utils.DB.Exec("VACUUM").Error; err != nil {
		return fmt.Errorf("VACUUM: %w", err)
	}
	if err := utils.DB.Exec("ANALYZE").Error; err != nil {
		return fmt.Errorf("ANALYZE: %w", err)
	}
	return nil
}
