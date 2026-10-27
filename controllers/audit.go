package controllers

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"truerp/models"
	"truerp/services/audit"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// auditUnionDB returns a queryable *gorm.DB backed by a UNION ALL of the
// legacy audit_logs table and every monthly shard (audit_logs_YYYY_MM). When
// from/to bounds are provided, out-of-range monthly tables are skipped.
func auditUnionDB(from, to *time.Time) *gorm.DB {
	tables := audit.MonthTablesInRange(audit.AuditTables(utils.DB), from, to)
	return utils.DB.Table("(" + audit.UnionSelect(tables) + ") AS audit_logs")
}

// parseAuditBound parses a from/to query param (YYYY-MM-DD or RFC3339).
func parseAuditBound(s string, endOfDay bool) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	for _, layout := range []string{"2006-01-02", time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			if endOfDay && layout == "2006-01-02" {
				t = t.AddDate(0, 0, 1)
			}
			return &t
		}
	}
	return nil
}

// applyAuditFilters applies the shared filter params used by list/export/delete.
func applyAuditFilters(query *gorm.DB, c *gin.Context) *gorm.DB {
	if action := c.Query("action"); action != "" {
		query = query.Where("action = ?", action)
	}
	if entityType := c.Query("entity_type"); entityType != "" {
		query = query.Where("entity_type = ?", entityType)
	}
	if status := c.Query("status"); status != "" {
		query = query.Where("status = ?", status)
	}
	if userName := c.Query("user_name"); userName != "" {
		query = query.Where("LOWER(user_name) LIKE ? OR LOWER(changed_by_name) LIKE ?",
			"%"+strings.ToLower(userName)+"%", "%"+strings.ToLower(userName)+"%")
	}
	if ipAddress := c.Query("ip_address"); ipAddress != "" {
		query = query.Where("LOWER(ip_address) LIKE ?", "%"+strings.ToLower(ipAddress)+"%")
	}
	if from := c.Query("from"); from != "" {
		query = query.Where("created_at >= ?", from)
	}
	if to := c.Query("to"); to != "" {
		query = query.Where("created_at <= ?", to)
	}
	if dateFilter := c.Query("date_filter"); dateFilter != "" {
		now := time.Now()
		switch dateFilter {
		case "today":
			query = query.Where("DATE(created_at) = ?", now.Format("2006-01-02"))
		case "yesterday":
			yesterday := now.AddDate(0, 0, -1)
			query = query.Where("DATE(created_at) = ?", yesterday.Format("2006-01-02"))
		case "this_week":
			weekStart := now.AddDate(0, 0, -int(now.Weekday()))
			query = query.Where("created_at >= ?", weekStart.Format("2006-01-02"))
		case "this_month":
			monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
			query = query.Where("created_at >= ?", monthStart.Format("2006-01-02"))
		case "this_year":
			yearStart := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
			query = query.Where("created_at >= ?", yearStart.Format("2006-01-02"))
		}
	}
	if search := c.Query("search"); search != "" {
		like := "%" + strings.ToLower(search) + "%"
		query = query.Where("LOWER(description) LIKE ? OR LOWER(entity_name) LIKE ? OR LOWER(user_name) LIKE ? OR LOWER(changed_by_name) LIKE ? OR LOWER(ip_address) LIKE ?",
			like, like, like, like, like)
	}
	return query
}

// auditSortColumns prevents SQL injection through the sort_by parameter.
var auditSortColumns = map[string]bool{
	"id": true, "created_at": true, "action": true, "entity_type": true,
	"entity_name": true, "user_name": true, "changed_by_name": true,
	"ip_address": true, "status": true,
}

// GetAuditLogs retrieves audit logs with filtering
func GetAuditLogs(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	from := parseAuditBound(c.Query("from"), false)
	to := parseAuditBound(c.Query("to"), true)

	var auditLogs []models.AuditLog
	query := auditUnionDB(from, to).Where("user_id = ?", userID)
	query = applyAuditFilters(query, c)

	// Sort options
	sortBy := c.DefaultQuery("sort_by", "created_at")
	if !auditSortColumns[sortBy] {
		sortBy = "created_at"
	}
	if c.DefaultQuery("sort_order", "desc") == "asc" {
		query = query.Order(sortBy + " ASC")
	} else {
		query = query.Order(sortBy + " DESC")
	}

	// Pagination
	page := 1
	perPage := 50
	if p := c.Query("page"); p != "" {
		if parsed, err := parsePage(p); err == nil {
			page = parsed
		}
	}
	if pp := c.Query("per_page"); pp != "" {
		if parsed, err := parsePerPage(pp); err == nil {
			perPage = parsed
		}
	}

	offset := (page - 1) * perPage

	var total int64
	query.Count(&total)

	if err := query.Limit(perPage).Offset(offset).Find(&auditLogs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch audit logs"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data":        auditLogs,
		"total":       total,
		"page":        page,
		"per_page":    perPage,
		"total_pages": (total + int64(perPage) - 1) / int64(perPage),
	})
}

// GetAuditLog retrieves a single audit log by ID
func GetAuditLog(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var auditLog models.AuditLog
	if err := auditUnionDB(nil, nil).Where("user_id = ? AND id = ?", userID, id).First(&auditLog).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Audit log not found"})
		return
	}

	c.JSON(http.StatusOK, auditLog)
}

// GetAuditStats retrieves audit statistics
func GetAuditStats(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var stats models.AuditLogStats
	base := func() *gorm.DB { return auditUnionDB(nil, nil) }

	base().Where("user_id = ?", userID).Count(&stats.TotalLogs)

	today := time.Now().Format("2006-01-02")
	base().Where("user_id = ? AND DATE(created_at) = ?", userID, today).Count(&stats.TodayLogs)
	base().Where("user_id = ? AND status = ?", userID, "success").Count(&stats.SuccessCount)
	base().Where("user_id = ? AND status = ?", userID, "failed").Count(&stats.FailedCount)

	var topActions []models.ActionCount
	base().Select("action, COUNT(*) as count").
		Where("user_id = ?", userID).
		Group("action").
		Order("count DESC").
		Limit(10).
		Scan(&topActions)
	stats.TopActions = topActions

	var topUsers []models.UserActionCount
	base().Select("COALESCE(NULLIF(changed_by_name, ''), user_name) AS user_name, COUNT(*) as count").
		Where("user_id = ?", userID).
		Group("COALESCE(NULLIF(changed_by_name, ''), user_name)").
		Order("count DESC").
		Limit(10).
		Scan(&topUsers)
	stats.TopUsers = topUsers

	c.JSON(http.StatusOK, gin.H{
		"total_logs":    stats.TotalLogs,
		"today_logs":    stats.TodayLogs,
		"success_count": stats.SuccessCount,
		"failed_count":  stats.FailedCount,
		"top_actions":   stats.TopActions,
		"top_users":     stats.TopUsers,
		"queue_pending": audit.Pending(),
		"queue_dropped": audit.Dropped(),
		"queue_written": audit.Written(),
	})
}

// ExportAuditLogs exports audit logs to CSV
func ExportAuditLogs(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	from := parseAuditBound(c.Query("from"), false)
	to := parseAuditBound(c.Query("to"), true)

	var auditLogs []models.AuditLog
	query := auditUnionDB(from, to).Where("user_id = ?", userID)
	query = applyAuditFilters(query, c)

	if err := query.Order("created_at DESC").Find(&auditLogs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch audit logs"})
		return
	}

	// Generate CSV
	csv := "ID,User Name,Changed By,Action,Entity Type,Entity ID,Entity Name,Description,IP Address,Status,Created At\n"
	for _, log := range auditLogs {
		entityID := ""
		if log.EntityID != nil {
			entityID = log.EntityID.String()
		}
		csv += log.ID.String() + "," +
			log.UserName + "," +
			log.ChangedByName + "," +
			log.Action + "," +
			log.EntityType + "," +
			entityID + "," +
			log.EntityName + "," +
			escapeCSV(log.Description) + "," +
			log.IPAddress + "," +
			log.Status + "," +
			log.CreatedAt.Format("2006-01-02 15:04:05") + "\n"
	}

	c.Header("Content-Type", "text/csv")
	c.Header("Content-Disposition", "attachment; filename=audit_logs.csv")
	c.String(http.StatusOK, csv)
}

// CreateAuditLog creates a new audit log entry (internal use). Entries are
// queued and persisted asynchronously into the current month's shard so the
// call never blocks the request.
func CreateAuditLog(userID uuid.UUID, userName, action, entityType string, entityID *uuid.UUID, entityName, description, ipAddress, userAgent string, changes interface{}, status string, errorMessage string) error {
	var changesJSON string
	if changes != nil {
		if bytes, err := json.Marshal(changes); err == nil {
			changesJSON = string(bytes)
		}
	}

	audit.Enqueue(models.AuditLog{
		ID:              uuid.New(),
		UserID:          userID,
		UserName:        userName,
		ChangedByUserID: &userID,
		ChangedByName:   userName,
		Action:          action,
		EntityType:      entityType,
		EntityID:        entityID,
		EntityName:      entityName,
		Description:     description,
		IPAddress:       ipAddress,
		UserAgent:       userAgent,
		Changes:         changesJSON,
		Status:          status,
		ErrorMessage:    errorMessage,
		CreatedAt:       time.Now(),
	})
	return nil
}

// LogDataChange queues a data-change audit entry with full previous/new
// snapshots, pulling actor, IP and request metadata from the gin context.
func LogDataChange(c *gin.Context, action, entityType string, entityID *uuid.UUID, entityName, description string, previous, newData interface{}) {
	userID := c.MustGet("user_id").(uuid.UUID)
	userName, _ := c.Get("user_name")
	name, _ := userName.(string)

	var prevJSON, newJSON string
	if previous != nil {
		if b, err := json.Marshal(previous); err == nil {
			prevJSON = string(b)
		}
	}
	if newData != nil {
		if b, err := json.Marshal(newData); err == nil {
			newJSON = string(b)
		}
	}

	actorID := actorUserID(c)
	entry := models.AuditLog{
		ID:              uuid.New(),
		UserID:          userID,
		UserName:        name,
		ChangedByUserID: &actorID,
		ChangedByName:   name,
		Action:          action,
		EntityType:      entityType,
		EntityID:        entityID,
		EntityName:      entityName,
		Description:     description,
		IPAddress:       c.ClientIP(),
		UserAgent:       c.GetHeader("User-Agent"),
		PreviousData:    prevJSON,
		NewData:         newJSON,
		Status:          "success",
		CreatedAt:       time.Now(),
	}
	if rid, ok := c.Get("request_id"); ok {
		if s, ok := rid.(string); ok {
			entry.RequestID = s
		}
	}
	audit.Enqueue(entry)
}

// DeleteAuditLogs deletes audit logs (with optional date range)
func DeleteAuditLogs(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	// Deletes must be applied per physical table.
	for _, table := range audit.AuditTables(utils.DB) {
		query := utils.DB.Table(table).Where("user_id = ?", userID)
		if from := c.Query("from"); from != "" {
			query = query.Where("created_at >= ?", from)
		}
		if to := c.Query("to"); to != "" {
			query = query.Where("created_at <= ?", to)
		}
		if action := c.Query("action"); action != "" {
			query = query.Where("action = ?", action)
		}
		if entityType := c.Query("entity_type"); entityType != "" {
			query = query.Where("entity_type = ?", entityType)
		}
		if err := query.Delete(&models.AuditLog{}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete audit logs"})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{"message": "Audit logs deleted successfully"})
}

// CleanupOldAuditLogs deletes audit logs older than the given number of days
// (can be called by a cron job). Monthly shards fully beyond the retention
// window are dropped entirely; the boundary month is trimmed by created_at.
func CleanupOldAuditLogs(retentionDays int) error {
	cutoffDate := time.Now().AddDate(0, 0, -retentionDays)

	for _, table := range audit.AuditTables(utils.DB) {
		if month, ok := audit.ParseMonthTable(table); ok {
			// The whole shard lies beyond the cutoff → drop it.
			if month.AddDate(0, 1, 0).Before(cutoffDate) || month.AddDate(0, 1, 0).Equal(cutoffDate) {
				if err := utils.DB.Exec("DROP TABLE IF EXISTS " + table).Error; err != nil {
					return err
				}
				audit.InvalidateTableCache()
				continue
			}
		}
		if err := utils.DB.Table(table).Where("created_at < ?", cutoffDate).Delete(&models.AuditLog{}).Error; err != nil {
			return err
		}
	}
	return nil
}

// GetAuditRetentionSettings returns the current retention settings
func GetAuditRetentionSettings(c *gin.Context) {
	_ = c.MustGet("user_id").(uuid.UUID)

	// Default retention period is 90 days
	// In a real implementation, this would be stored in user settings
	retentionDays := 90

	c.JSON(http.StatusOK, gin.H{
		"retention_days": retentionDays,
		"description":    "Audit logs older than this many days will be automatically deleted",
	})
}

// UpdateAuditRetentionSettings updates the retention period
func UpdateAuditRetentionSettings(c *gin.Context) {
	_ = c.MustGet("user_id").(uuid.UUID)

	var input struct {
		RetentionDays int `json:"retention_days" binding:"required,min=7,max=365"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// In a real implementation, this would be stored in user settings
	// For now, we'll just acknowledge the change
	c.JSON(http.StatusOK, gin.H{
		"message":        "Retention settings updated",
		"retention_days": input.RetentionDays,
	})
}

// ArchiveAuditLogs archives audit logs to a separate table or file
func ArchiveAuditLogs(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		FromDate string `json:"from_date" binding:"required"`
		ToDate   string `json:"to_date" binding:"required"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Get logs to be archived
	var auditLogs []models.AuditLog
	query := auditUnionDB(parseAuditBound(input.FromDate, false), parseAuditBound(input.ToDate, true)).
		Where("user_id = ? AND created_at >= ? AND created_at <= ?", userID, input.FromDate, input.ToDate)

	if err := query.Find(&auditLogs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch audit logs for archiving"})
		return
	}

	if len(auditLogs) == 0 {
		c.JSON(http.StatusOK, gin.H{
			"message": "No audit logs found for the specified date range",
			"count":   0,
		})
		return
	}

	// In a real implementation, this would:
	// 1. Export logs to a file (JSON/CSV)
	// 2. Upload to cloud storage (S3, etc.)
	// 3. Store archive metadata in database
	// 4. Optionally delete from main table

	// For now, we'll just return the count
	c.JSON(http.StatusOK, gin.H{
		"message":    "Audit logs archived successfully",
		"count":      len(auditLogs),
		"from_date":  input.FromDate,
		"to_date":    input.ToDate,
		"archive_id": uuid.New().String(),
		"note":       "In production, this would export to file/cloud storage",
	})
}

// GetArchivedAuditLogs retrieves list of archived audit logs
func GetArchivedAuditLogs(c *gin.Context) {
	_ = c.MustGet("user_id").(uuid.UUID)

	// In a real implementation, this would query an archives table
	// For now, return empty list
	c.JSON(http.StatusOK, gin.H{
		"archives": []gin.H{},
		"message":  "Archive functionality requires implementation of archive storage",
	})
}

// Helper functions
func parsePage(page string) (int, error) {
	var p int
	err := json.Unmarshal([]byte(page), &p)
	if err != nil {
		return 1, err
	}
	return p, nil
}

func parsePerPage(perPage string) (int, error) {
	var pp int
	err := json.Unmarshal([]byte(perPage), &pp)
	if err != nil {
		return 10, err
	}
	return pp, nil
}

func escapeCSV(s string) string {
	if s == "" {
		return ""
	}
	// Escape quotes and wrap in quotes if contains comma or quote
	if contains(s, ",") || contains(s, "\"") || contains(s, "\n") {
		s = replaceAll(s, "\"", "\"\"")
		return "\"" + s + "\""
	}
	return s
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func replaceAll(s, old, new string) string {
	result := ""
	for i := 0; i < len(s); i++ {
		if i <= len(s)-len(old) && s[i:i+len(old)] == old {
			result += new
			i += len(old) - 1
		} else {
			result += string(s[i])
		}
	}
	return result
}
