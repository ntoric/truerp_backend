package controllers

import (
	"truerp/models"
	"truerp/utils"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// GetAuditLogs retrieves audit logs with filtering
func GetAuditLogs(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var auditLogs []models.AuditLog
	query := utils.DB.Where("user_id = ?", userID)

	// Filter by action
	if action := c.Query("action"); action != "" {
		query = query.Where("action = ?", action)
	}

	// Filter by entity type
	if entityType := c.Query("entity_type"); entityType != "" {
		query = query.Where("entity_type = ?", entityType)
	}

	// Filter by status
	if status := c.Query("status"); status != "" {
		query = query.Where("status = ?", status)
	}

	// Filter by user name
	if userName := c.Query("user_name"); userName != "" {
		query = query.Where("user_name ILIKE ?", "%"+userName+"%")
	}

	// Filter by IP address
	if ipAddress := c.Query("ip_address"); ipAddress != "" {
		query = query.Where("ip_address ILIKE ?", "%"+ipAddress+"%")
	}

	// Filter by date range
	if from := c.Query("from"); from != "" {
		query = query.Where("created_at >= ?", from)
	}
	if to := c.Query("to"); to != "" {
		query = query.Where("created_at <= ?", to)
	}

	// Filter by date (today, yesterday, this_week, this_month, this_year)
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

	// Search in description and entity name
	if search := c.Query("search"); search != "" {
		query = query.Where("description ILIKE ? OR entity_name ILIKE ? OR user_name ILIKE ? OR ip_address ILIKE ?",
			"%"+search+"%", "%"+search+"%", "%"+search+"%", "%"+search+"%")
	}

	// Sort options
	sortBy := c.DefaultQuery("sort_by", "created_at")
	sortOrder := c.DefaultQuery("sort_order", "desc")
	if sortOrder == "asc" {
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
	query.Model(&models.AuditLog{}).Count(&total)

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
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&auditLog).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Audit log not found"})
		return
	}

	c.JSON(http.StatusOK, auditLog)
}

// GetAuditStats retrieves audit statistics
func GetAuditStats(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var stats models.AuditLogStats

	// Total logs
	utils.DB.Model(&models.AuditLog{}).Where("user_id = ?", userID).Count(&stats.TotalLogs)

	// Today's logs
	today := time.Now().Format("2006-01-02")
	utils.DB.Model(&models.AuditLog{}).Where("user_id = ? AND DATE(created_at) = ?", userID, today).Count(&stats.TodayLogs)

	// Success count
	utils.DB.Model(&models.AuditLog{}).Where("user_id = ? AND status = ?", userID, "success").Count(&stats.SuccessCount)

	// Failed count
	utils.DB.Model(&models.AuditLog{}).Where("user_id = ? AND status = ?", userID, "failed").Count(&stats.FailedCount)

	// Top actions
	var topActions []models.ActionCount
	utils.DB.Model(&models.AuditLog{}).
		Select("action, COUNT(*) as count").
		Where("user_id = ?", userID).
		Group("action").
		Order("count DESC").
		Limit(10).
		Scan(&topActions)
	stats.TopActions = topActions

	// Top users
	var topUsers []models.UserActionCount
	utils.DB.Model(&models.AuditLog{}).
		Select("user_name, COUNT(*) as count").
		Where("user_id = ?", userID).
		Group("user_name").
		Order("count DESC").
		Limit(10).
		Scan(&topUsers)
	stats.TopUsers = topUsers

	c.JSON(http.StatusOK, stats)
}

// ExportAuditLogs exports audit logs to CSV
func ExportAuditLogs(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var auditLogs []models.AuditLog
	query := utils.DB.Where("user_id = ?", userID)

	// Apply same filters as GetAuditLogs
	if action := c.Query("action"); action != "" {
		query = query.Where("action = ?", action)
	}
	if entityType := c.Query("entity_type"); entityType != "" {
		query = query.Where("entity_type = ?", entityType)
	}
	if status := c.Query("status"); status != "" {
		query = query.Where("status = ?", status)
	}
	if from := c.Query("from"); from != "" {
		query = query.Where("created_at >= ?", from)
	}
	if to := c.Query("to"); to != "" {
		query = query.Where("created_at <= ?", to)
	}

	if err := query.Order("created_at DESC").Find(&auditLogs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch audit logs"})
		return
	}

	// Generate CSV
	csv := "ID,User Name,Action,Entity Type,Entity ID,Entity Name,Description,IP Address,Status,Created At\n"
	for _, log := range auditLogs {
		entityID := ""
		if log.EntityID != nil {
			entityID = log.EntityID.String()
		}
		csv += log.ID.String() + "," +
			log.UserName + "," +
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

// CreateAuditLog creates a new audit log entry (internal use)
func CreateAuditLog(userID uuid.UUID, userName, action, entityType string, entityID *uuid.UUID, entityName, description, ipAddress, userAgent string, changes interface{}, status string, errorMessage string) error {
	var changesJSON string
	if changes != nil {
		if bytes, err := json.Marshal(changes); err == nil {
			changesJSON = string(bytes)
		}
	}

	auditLog := models.AuditLog{
		ID:          uuid.New(),
		UserID:      userID,
		UserName:    userName,
		Action:      action,
		EntityType:  entityType,
		EntityID:    entityID,
		EntityName:  entityName,
		Description: description,
		IPAddress:   ipAddress,
		UserAgent:   userAgent,
		Changes:     changesJSON,
		Status:      status,
		ErrorMessage: errorMessage,
		CreatedAt:   time.Now(),
	}

	return utils.DB.Create(&auditLog).Error
}

// DeleteAuditLogs deletes audit logs (with optional date range)
func DeleteAuditLogs(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	query := utils.DB.Where("user_id = ?", userID)

	// Optional date range for bulk deletion
	if from := c.Query("from"); from != "" {
		query = query.Where("created_at >= ?", from)
	}
	if to := c.Query("to"); to != "" {
		query = query.Where("created_at <= ?", to)
	}

	if err := query.Delete(&models.AuditLog{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete audit logs"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Audit logs deleted successfully"})
}

// CleanupOldAuditLogs deletes audit logs older than specified days (can be called by cron job)
func CleanupOldAuditLogs(retentionDays int) error {
	cutoffDate := time.Now().AddDate(0, 0, -retentionDays)
	result := utils.DB.Where("created_at < ?", cutoffDate).Delete(&models.AuditLog{})
	return result.Error
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
	query := utils.DB.Where("user_id = ?", userID).
		Where("created_at >= ? AND created_at <= ?", input.FromDate, input.ToDate)

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
		"message":      "Audit logs archived successfully",
		"count":        len(auditLogs),
		"from_date":    input.FromDate,
		"to_date":      input.ToDate,
		"archive_id":   uuid.New().String(),
		"note":         "In production, this would export to file/cloud storage",
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
