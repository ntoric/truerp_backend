package controllers

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Scheduled database backups share the maintenance scheduler's timezone
// (Asia/Kolkata — no DST) and the same run-once-per-slot semantics.
const (
	defaultDBBackupRunTime = "02:00"
	dbBackupRecordCap      = 200 // history rows kept after retention pruning
)

var dbBackupFrequencies = map[string]bool{"daily": true, "weekly": true, "monthly": true}

var dbBackupDestinations = map[string]bool{
	"local": true, "s3": true, "gdrive": true, "mega": true, "telegram": true, "custom": true,
}

// dbBackupMu serializes backup runs so a scheduled run and a manual
// "run now" trigger can never overlap.
var (
	dbBackupMu      sync.Mutex
	dbBackupRunning atomic.Bool
)

// dbBackupDir returns the directory where local backup files are stored.
// It defaults under the data directory (a persistent volume in Docker) and
// can be overridden with DB_BACKUP_DIR.
func dbBackupDir() string {
	if dir := strings.TrimSpace(os.Getenv("DB_BACKUP_DIR")); dir != "" {
		return dir
	}
	return filepath.Join("data", "backups")
}

// GetOrCreateDBBackupSettings returns the singleton backup settings row,
// creating a disabled default row if one does not yet exist.
func GetOrCreateDBBackupSettings() (models.DBBackupSettings, error) {
	var settings models.DBBackupSettings
	if err := utils.DB.Order("created_at asc").First(&settings).Error; err == nil {
		return settings, nil
	}
	settings = models.DBBackupSettings{
		ID:              uuid.New(),
		Frequency:       "daily",
		RunTime:         defaultDBBackupRunTime,
		Weekday:         1,
		MonthDay:        1,
		RetentionCount:  10,
		DestinationType: "local",
	}
	if err := utils.DB.Create(&settings).Error; err != nil {
		return settings, err
	}
	return settings, nil
}

// populateDBBackupSecretFlags sets the Has* booleans the UI uses to show
// "configured" placeholders for secrets that are never sent back.
func populateDBBackupSecretFlags(s *models.DBBackupSettings) {
	s.HasS3Secret = s.S3SecretKey != ""
	s.HasGDriveCredentials = s.GDriveServiceAccountJSON != ""
	s.HasMegaPassword = s.MegaPassword != ""
	s.HasTelegramBotToken = s.TelegramBotToken != ""
	s.HasCustomAuthHeader = s.CustomAuthHeader != ""
}

func dbBackupFrequencyOrDefault(freq string) string {
	if dbBackupFrequencies[freq] {
		return freq
	}
	return "daily"
}

func dbBackupDestinationOrDefault(dest string) string {
	if dbBackupDestinations[dest] {
		return dest
	}
	return "local"
}

// dbBackupLastScheduledInstant returns the most recent scheduled run instant
// at or before now, in IST. A backup is due when LastRunAt is older than this
// instant (or never ran), which also provides catch-up for runs missed while
// the server was down.
func dbBackupLastScheduledInstant(now time.Time, s models.DBBackupSettings) time.Time {
	loc := dbMaintenanceLocation()
	now = now.In(loc)
	parsed, err := time.Parse("15:04", dbMaintenanceRunTimeOrDefault(s.RunTime))
	if err != nil {
		parsed, _ = time.Parse("15:04", defaultDBBackupRunTime)
	}
	at := func(t time.Time) time.Time {
		return time.Date(t.Year(), t.Month(), t.Day(), parsed.Hour(), parsed.Minute(), 0, 0, loc)
	}

	switch dbBackupFrequencyOrDefault(s.Frequency) {
	case "weekly":
		wd := s.Weekday
		if wd < 0 || wd > 6 {
			wd = 1
		}
		diff := (int(now.Weekday()) - wd + 7) % 7
		cand := at(now.AddDate(0, 0, -diff))
		if now.Before(cand) {
			cand = cand.AddDate(0, 0, -7)
		}
		return cand
	case "monthly":
		day := s.MonthDay
		if day < 1 || day > 28 {
			day = 1
		}
		cand := time.Date(now.Year(), now.Month(), day,
			parsed.Hour(), parsed.Minute(), 0, 0, loc)
		if now.Before(cand) {
			cand = cand.AddDate(0, -1, 0)
		}
		return cand
	default: // daily
		cand := at(now)
		if now.Before(cand) {
			cand = cand.AddDate(0, 0, -1)
		}
		return cand
	}
}

// nextDBBackupRun computes the next future scheduled run instant for display.
func nextDBBackupRun(now time.Time, s models.DBBackupSettings) time.Time {
	loc := dbMaintenanceLocation()
	now = now.In(loc)
	parsed, err := time.Parse("15:04", dbMaintenanceRunTimeOrDefault(s.RunTime))
	if err != nil {
		parsed, _ = time.Parse("15:04", defaultDBBackupRunTime)
	}
	hh, mm := parsed.Hour(), parsed.Minute()

	switch dbBackupFrequencyOrDefault(s.Frequency) {
	case "weekly":
		wd := s.Weekday
		if wd < 0 || wd > 6 {
			wd = 1
		}
		for i := 0; i <= 7; i++ {
			day := now.AddDate(0, 0, i)
			if int(day.Weekday()) != wd {
				continue
			}
			cand := time.Date(day.Year(), day.Month(), day.Day(), hh, mm, 0, 0, loc)
			if now.Before(cand) {
				return cand
			}
		}
		return time.Date(now.Year(), now.Month(), now.Day(), hh, mm, 0, 0, loc).AddDate(0, 0, 7)
	case "monthly":
		day := s.MonthDay
		if day < 1 || day > 28 {
			day = 1
		}
		y, m := now.Year(), now.Month()
		for i := 0; i < 14; i++ {
			cand := time.Date(y, m, day, hh, mm, 0, 0, loc)
			if now.Before(cand) {
				return cand
			}
			m++
			if m > 12 {
				m = 1
				y++
			}
		}
		return time.Date(y, m, day, hh, mm, 0, 0, loc)
	default: // daily
		cand := time.Date(now.Year(), now.Month(), now.Day(), hh, mm, 0, 0, loc)
		if !now.Before(cand) {
			cand = cand.AddDate(0, 0, 1)
		}
		return cand
	}
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// GetDBBackupSettingsHandler returns the backup configuration plus scheduler
// state and recent backup history. Secrets are never returned — only Has*
// flags.
func GetDBBackupSettingsHandler(c *gin.Context) {
	settings, err := GetOrCreateDBBackupSettings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load backup settings"})
		return
	}
	populateDBBackupSecretFlags(&settings)

	loc := dbMaintenanceLocation()
	now := time.Now().In(loc)

	var records []models.DBBackupRecord
	utils.DB.Order("created_at desc").Limit(20).Find(&records)
	markDBBackupLocalAvailability(records)

	c.JSON(http.StatusOK, gin.H{
		"settings":     settings,
		"running":      dbBackupRunning.Load(),
		"ist_time":     now.Format("2006-01-02 15:04:05"),
		"ist_timezone": loc.String(),
		"next_run_at":  nextDBBackupRun(now, settings).Format(time.RFC3339),
		"backup_dir":   dbBackupDir(),
		"dialect":      string(utils.CurrentDialect()),
		"records":      records,
	})
}

// markDBBackupLocalAvailability reports whether each record's dump file still
// exists on disk (it may have been pruned by retention or removed manually).
func markDBBackupLocalAvailability(records []models.DBBackupRecord) {
	for i := range records {
		if records[i].FilePath == "" {
			continue
		}
		if st, err := os.Stat(records[i].FilePath); err == nil && !st.IsDir() {
			records[i].LocalAvailable = true
		}
	}
}

type dbBackupSettingsInput struct {
	IsEnabled       bool   `json:"is_enabled"`
	Frequency       string `json:"frequency"`
	RunTime         string `json:"run_time"`
	Weekday         int    `json:"weekday"`
	MonthDay        int    `json:"month_day"`
	RetentionCount  int    `json:"retention_count"`
	DestinationType string `json:"destination_type"`

	S3Endpoint  string `json:"s3_endpoint"`
	S3Region    string `json:"s3_region"`
	S3Bucket    string `json:"s3_bucket"`
	S3AccessKey string `json:"s3_access_key"`
	S3SecretKey string `json:"s3_secret_key"` // plaintext in; stored encrypted; empty = keep
	S3Prefix    string `json:"s3_prefix"`

	GDriveServiceAccountJSON string `json:"gdrive_service_account_json"` // empty = keep
	GDriveFolderID           string `json:"gdrive_folder_id"`

	MegaEmail    string `json:"mega_email"`
	MegaPassword string `json:"mega_password"` // empty = keep

	TelegramBotToken string `json:"telegram_bot_token"` // empty = keep
	TelegramChatID   string `json:"telegram_chat_id"`

	CustomURL        string `json:"custom_url"`
	CustomHeaders    string `json:"custom_headers"`
	CustomAuthHeader string `json:"custom_auth_header"` // empty = keep
}

// applyDBBackupSettingsInput copies validated input onto the settings row.
// Secret fields are only overwritten when a new value is supplied.
func applyDBBackupSettingsInput(s *models.DBBackupSettings, input dbBackupSettingsInput) error {
	s.IsEnabled = input.IsEnabled
	s.Frequency = dbBackupFrequencyOrDefault(input.Frequency)
	s.RunTime = strings.TrimSpace(input.RunTime)
	s.Weekday = input.Weekday
	s.MonthDay = input.MonthDay
	s.RetentionCount = input.RetentionCount
	s.DestinationType = dbBackupDestinationOrDefault(input.DestinationType)

	s.S3Endpoint = strings.TrimSpace(input.S3Endpoint)
	s.S3Region = strings.TrimSpace(input.S3Region)
	s.S3Bucket = strings.TrimSpace(input.S3Bucket)
	s.S3AccessKey = strings.TrimSpace(input.S3AccessKey)
	s.S3Prefix = strings.TrimSpace(input.S3Prefix)

	s.GDriveFolderID = strings.TrimSpace(input.GDriveFolderID)
	s.MegaEmail = strings.TrimSpace(input.MegaEmail)
	s.TelegramChatID = strings.TrimSpace(input.TelegramChatID)
	s.CustomURL = strings.TrimSpace(input.CustomURL)
	s.CustomHeaders = strings.TrimSpace(input.CustomHeaders)

	var err error
	if input.S3SecretKey != "" {
		if s.S3SecretKey, err = utils.Encrypt(input.S3SecretKey); err != nil {
			return err
		}
	}
	if input.GDriveServiceAccountJSON != "" {
		if s.GDriveServiceAccountJSON, err = utils.Encrypt(input.GDriveServiceAccountJSON); err != nil {
			return err
		}
	}
	if input.MegaPassword != "" {
		if s.MegaPassword, err = utils.Encrypt(input.MegaPassword); err != nil {
			return err
		}
	}
	if input.TelegramBotToken != "" {
		if s.TelegramBotToken, err = utils.Encrypt(input.TelegramBotToken); err != nil {
			return err
		}
	}
	if input.CustomAuthHeader != "" {
		if s.CustomAuthHeader, err = utils.Encrypt(input.CustomAuthHeader); err != nil {
			return err
		}
	}
	return nil
}

// validateDBBackupSchedule checks schedule fields before saving.
func validateDBBackupSchedule(input dbBackupSettingsInput) error {
	runTime := strings.TrimSpace(input.RunTime)
	if runTime == "" {
		runTime = defaultDBBackupRunTime
	}
	if _, err := time.Parse("15:04", runTime); err != nil {
		return fmt.Errorf("run_time must be in HH:MM (24h) format, e.g. 02:00")
	}
	if input.Frequency != "" && !dbBackupFrequencies[input.Frequency] {
		return fmt.Errorf("frequency must be daily, weekly, or monthly")
	}
	if input.Frequency == "weekly" && (input.Weekday < 0 || input.Weekday > 6) {
		return fmt.Errorf("weekday must be 0 (Sunday) through 6 (Saturday)")
	}
	if input.Frequency == "monthly" && (input.MonthDay < 1 || input.MonthDay > 28) {
		return fmt.Errorf("month_day must be between 1 and 28")
	}
	if input.RetentionCount < 0 {
		return fmt.Errorf("retention_count cannot be negative")
	}
	if input.DestinationType != "" && !dbBackupDestinations[input.DestinationType] {
		return fmt.Errorf("destination_type must be one of: local, s3, gdrive, mega, telegram, custom")
	}
	return nil
}

// validateDBBackupDestination returns an error when the selected destination
// is missing required configuration. Non-empty input secrets satisfy the
// requirement even before they are persisted.
func validateDBBackupDestination(s *models.DBBackupSettings) error {
	switch dbBackupDestinationOrDefault(s.DestinationType) {
	case "s3":
		if s.S3Bucket == "" {
			return fmt.Errorf("S3 bucket is required")
		}
		if s.S3AccessKey == "" || s.S3SecretKey == "" {
			return fmt.Errorf("S3 access key and secret key are required")
		}
	case "gdrive":
		if s.GDriveServiceAccountJSON == "" {
			return fmt.Errorf("Google Drive service-account JSON is required")
		}
	case "mega":
		if s.MegaEmail == "" || s.MegaPassword == "" {
			return fmt.Errorf("Mega email and password are required")
		}
	case "telegram":
		if s.TelegramBotToken == "" || s.TelegramChatID == "" {
			return fmt.Errorf("Telegram bot token and chat ID are required")
		}
	case "custom":
		if s.CustomURL == "" {
			return fmt.Errorf("Custom endpoint URL is required")
		}
	}
	return nil
}

// UpdateDBBackupSettingsHandler saves the backup schedule and destination
// configuration. Secret inputs are encrypted; empty secret inputs leave the
// stored value unchanged.
func UpdateDBBackupSettingsHandler(c *gin.Context) {
	var input dbBackupSettingsInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := validateDBBackupSchedule(input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	settings, err := GetOrCreateDBBackupSettings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load backup settings"})
		return
	}

	if err := applyDBBackupSettingsInput(&settings, input); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to encrypt backup secrets"})
		return
	}

	// When scheduled backups are being enabled for a cloud destination, make
	// sure the destination is fully configured so the first run cannot
	// silently produce files that go nowhere.
	if settings.IsEnabled {
		if err := validateDBBackupDestination(&settings); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}

	if err := utils.DB.Save(&settings).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save backup settings"})
		return
	}

	populateDBBackupSecretFlags(&settings)
	c.JSON(http.StatusOK, settings)
}

// RunDBBackupNowHandler manually triggers a backup run. The run executes in
// the background; poll GET /db-backup for status.
func RunDBBackupNowHandler(c *gin.Context) {
	settings, err := GetOrCreateDBBackupSettings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load backup settings"})
		return
	}

	if !dbBackupMu.TryLock() {
		c.JSON(http.StatusConflict, gin.H{"error": "A backup is already in progress"})
		return
	}

	go func() {
		defer dbBackupMu.Unlock()
		runDBBackup(settings, "manual")
	}()

	c.JSON(http.StatusAccepted, gin.H{"message": "Backup started"})
}

// DownloadDBBackupRecordHandler serves a locally-stored backup file.
func DownloadDBBackupRecordHandler(c *gin.Context) {
	id := c.Param("id")
	var record models.DBBackupRecord
	if err := utils.DB.Where("id = ?", id).First(&record).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Backup record not found"})
		return
	}
	if record.FilePath == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "No local file for this backup"})
		return
	}

	// Path safety: the stored path must resolve inside the backup directory.
	absDir, err := filepath.Abs(dbBackupDir())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to resolve backup directory"})
		return
	}
	absPath, err := filepath.Abs(record.FilePath)
	if err != nil || !strings.HasPrefix(absPath, absDir+string(os.PathSeparator)) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Invalid backup path"})
		return
	}
	if st, err := os.Stat(absPath); err != nil || st.IsDir() {
		c.JSON(http.StatusNotFound, gin.H{"error": "Backup file no longer exists on disk"})
		return
	}

	c.FileAttachment(absPath, record.FileName)
}

// DeleteDBBackupRecordHandler removes a backup record and its local file.
func DeleteDBBackupRecordHandler(c *gin.Context) {
	id := c.Param("id")
	var record models.DBBackupRecord
	if err := utils.DB.Where("id = ?", id).First(&record).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Backup record not found"})
		return
	}
	if record.FilePath != "" {
		if err := os.Remove(record.FilePath); err != nil && !os.IsNotExist(err) {
			log.Printf("db backup: failed to remove %s: %v", record.FilePath, err)
		}
	}
	if err := utils.DB.Delete(&record).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete backup record"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Backup deleted"})
}

// ---------------------------------------------------------------------------
// Scheduler
// ---------------------------------------------------------------------------

// StartDBBackupScheduler launches a background goroutine that checks the
// backup settings every minute and runs a backup once the configured schedule
// is due. An immediate check runs on startup so a scheduled run missed while
// the server was down is still honored (catch-up).
func StartDBBackupScheduler() {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("db backup scheduler: PANIC recovered: %v", r)
			}
		}()

		loc := dbMaintenanceLocation()
		log.Printf("db backup scheduler: started (timezone: %s, dir: %s)",
			loc.String(), dbBackupDir())

		processDueDBBackup()

		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("db backup scheduler: PANIC in tick: %v", r)
					}
				}()
				processDueDBBackup()
			}()
		}
	}()
}

// processDueDBBackup runs a backup when it is enabled and the last completed
// run is older than the most recent scheduled instant.
func processDueDBBackup() {
	settings, err := GetOrCreateDBBackupSettings()
	if err != nil {
		log.Printf("db backup scheduler: load settings failed: %v", err)
		return
	}
	if !settings.IsEnabled {
		return
	}

	due := dbBackupLastScheduledInstant(time.Now(), settings)
	if settings.LastRunAt != nil && !settings.LastRunAt.Before(due) {
		return // already ran for this scheduled slot
	}

	if !dbBackupMu.TryLock() {
		return
	}
	go func() {
		defer dbBackupMu.Unlock()
		runDBBackup(settings, "scheduled")
	}()
}

// runDBBackup produces a dump, optionally uploads it to the configured
// destination, records the outcome, and prunes old local backups.
func runDBBackup(settings models.DBBackupSettings, trigger string) models.DBBackupSettings {
	dbBackupRunning.Store(true)
	defer dbBackupRunning.Store(false)

	start := time.Now()
	log.Printf("db backup: starting (dialect=%s, trigger=%s, destination=%s)",
		utils.CurrentDialect(), trigger, dbBackupDestinationOrDefault(settings.DestinationType))

	record := models.DBBackupRecord{
		ID:          uuid.New(),
		Trigger:     trigger,
		Destination: dbBackupDestinationOrDefault(settings.DestinationType),
		CreatedAt:   start,
	}

	filePath, dumpErr := createDBBackupFile()
	switch {
	case dumpErr != nil:
		record.Status = "failed"
		record.Error = dumpErr.Error()
		settings.LastRunStatus = "failed"
		settings.LastRunError = dumpErr.Error()
		settings.LastRunFile = ""
		settings.LastRunSizeBytes = 0
	default:
		record.FileName = filepath.Base(filePath)
		record.FilePath = filePath
		if st, err := os.Stat(filePath); err == nil {
			record.SizeBytes = st.Size()
		}

		detail, uploadErr := uploadDBBackupToDestination(&settings, filePath)
		record.UploadDetail = detail
		if uploadErr != nil {
			record.Status = "partial"
			record.Error = uploadErr.Error()
			settings.LastRunStatus = "partial"
			settings.LastRunError = uploadErr.Error()
		} else {
			record.Status = "success"
			settings.LastRunStatus = "success"
			settings.LastRunError = ""
		}
		settings.LastRunFile = record.FileName
		settings.LastRunSizeBytes = record.SizeBytes
	}

	if err := utils.DB.Create(&record).Error; err != nil {
		log.Printf("db backup: failed to record history: %v", err)
	}

	now := time.Now()
	settings.LastRunAt = &now
	settings.LastRunDurationMs = time.Since(start).Milliseconds()
	if err := utils.DB.Save(&settings).Error; err != nil {
		log.Printf("db backup: failed to persist run status: %v", err)
	}

	pruneDBBackups(settings.RetentionCount)

	if settings.LastRunError != "" {
		log.Printf("db backup: finished in %s (status=%s): %s",
			time.Since(start), settings.LastRunStatus, settings.LastRunError)
	} else {
		log.Printf("db backup: completed in %s (file=%s, size=%d bytes, upload=%s)",
			time.Since(start), record.FileName, record.SizeBytes, record.UploadDetail)
	}
	return settings
}

// pruneDBBackups removes local dump files beyond the retention count and caps
// the history table at dbBackupRecordCap rows.
func pruneDBBackups(keep int) {
	var records []models.DBBackupRecord
	if err := utils.DB.Order("created_at desc").Find(&records).Error; err != nil {
		return
	}
	filesSeen := 0
	for i, rec := range records {
		hasFile := rec.FilePath != ""
		if st, err := os.Stat(rec.FilePath); err == nil && hasFile && !st.IsDir() {
			filesSeen++
			hasFile = true
		} else {
			hasFile = false
		}

		removeFile := hasFile && keep > 0 && filesSeen > keep
		dropRow := i >= dbBackupRecordCap

		if removeFile {
			if err := os.Remove(rec.FilePath); err != nil && !os.IsNotExist(err) {
				log.Printf("db backup: prune failed for %s: %v", rec.FilePath, err)
			}
		}
		if dropRow {
			utils.DB.Delete(&models.DBBackupRecord{}, "id = ?", rec.ID)
		}
	}
}
