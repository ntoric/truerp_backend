package controllers

import (
	"truerp/models"
	"truerp/utils"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Audit Logs
func GetComplianceAuditLogs(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var logs []models.AuditLog
	query := utils.DB.Where("user_id = ?", userID)

	if action := c.Query("action"); action != "" {
		query = query.Where("action = ?", action)
	}
	if entityType := c.Query("entity_type"); entityType != "" {
		query = query.Where("entity_type = ?", entityType)
	}
	if fromDate := c.Query("from_date"); fromDate != "" {
		query = query.Where("created_at >= ?", fromDate)
	}
	if toDate := c.Query("to_date"); toDate != "" {
		query = query.Where("created_at <= ?", toDate)
	}

	if err := query.Order("created_at DESC").Limit(1000).Find(&logs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch audit logs"})
		return
	}

	c.JSON(http.StatusOK, logs)
}

func GetAuditLogStats(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var stats models.AuditLogStats

	// Total logs
	utils.DB.Model(&models.AuditLog{}).Where("user_id = ?", userID).Count(&stats.TotalLogs)

	// Today's logs
	today := time.Now().Format("2006-01-02")
	utils.DB.Model(&models.AuditLog{}).Where("user_id = ? AND DATE(created_at) = ?", userID, today).Count(&stats.TodayLogs)

	// Success/Failed counts
	utils.DB.Model(&models.AuditLog{}).Where("user_id = ? AND status = ?", userID, "success").Count(&stats.SuccessCount)
	utils.DB.Model(&models.AuditLog{}).Where("user_id = ? AND status = ?", userID, "failed").Count(&stats.FailedCount)

	// Top actions
	utils.DB.Model(&models.AuditLog{}).
		Select("action, COUNT(*) as count").
		Where("user_id = ?", userID).
		Group("action").
		Order("count DESC").
		Limit(5).
		Scan(&stats.TopActions)

	// Top users
	utils.DB.Model(&models.AuditLog{}).
		Select("user_name, COUNT(*) as count").
		Where("user_id = ?", userID).
		Group("user_name").
		Order("count DESC").
		Limit(5).
		Scan(&stats.TopUsers)

	c.JSON(http.StatusOK, stats)
}

// Role & Permission Management
func GetRoles(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var roles []models.Role
	if err := utils.DB.Where("user_id = ?", userID).Preload("Permissions").Find(&roles).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch roles"})
		return
	}

	if len(roles) == 0 {
		utils.EnsureDefaultRoles(utils.DB, userID)
		if err := utils.DB.Where("user_id = ?", userID).Preload("Permissions").Find(&roles).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch roles"})
			return
		}
	}

	c.JSON(http.StatusOK, roles)
}

func CreateRole(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		Name        string     `json:"name" binding:"required"`
		Description string     `json:"description"`
		Permissions []uuid.UUID `json:"permissions"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	role := models.Role{
		ID:          uuid.New(),
		UserID:      userID,
		Name:        input.Name,
		Description: input.Description,
		IsActive:    true,
	}

	if err := utils.DB.Create(&role).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create role"})
		return
	}

	// Associate permissions
	if len(input.Permissions) > 0 {
		var permissions []models.Permission
		utils.DB.Where("id IN ?", input.Permissions).Find(&permissions)
		utils.DB.Model(&role).Association("Permissions").Append(&permissions)
	}

	c.JSON(http.StatusCreated, role)
}

func UpdateRole(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var input struct {
		Name        string     `json:"name"`
		Description string     `json:"description"`
		IsActive    bool       `json:"is_active"`
		Permissions []uuid.UUID `json:"permissions"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var role models.Role
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&role).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Role not found"})
		return
	}

	updates := map[string]interface{}{
		"name":        input.Name,
		"description": input.Description,
		"is_active":   input.IsActive,
	}

	if err := utils.DB.Model(&role).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update role"})
		return
	}

	// Update permissions
	if input.Permissions != nil {
		var permissions []models.Permission
		utils.DB.Where("id IN ?", input.Permissions).Find(&permissions)
		utils.DB.Model(&role).Association("Permissions").Replace(&permissions)
	}

	c.JSON(http.StatusOK, role)
}

func DeleteRole(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var role models.Role
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&role).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Role not found"})
		return
	}

	if role.IsDefault {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot delete default role"})
		return
	}

	utils.DB.Delete(&role)
	c.JSON(http.StatusOK, gin.H{"message": "Role deleted"})
}

func GetPermissions(c *gin.Context) {
	var permissions []models.Permission
	if err := utils.DB.Order("resource, action").Find(&permissions).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch permissions"})
		return
	}

	c.JSON(http.StatusOK, permissions)
}

// IP Restrictions
func GetIPRestrictions(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var restrictions []models.IPRestriction
	if err := utils.DB.Where("user_id = ?", userID).Order("created_at DESC").Find(&restrictions).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch IP restrictions"})
		return
	}

	c.JSON(http.StatusOK, restrictions)
}

func CreateIPRestriction(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		IPAddress   string `json:"ip_address" binding:"required"`
		Description string `json:"description"`
		IsAllowed   bool   `json:"is_allowed"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	restriction := models.IPRestriction{
		ID:          uuid.New(),
		UserID:      userID,
		IPAddress:   input.IPAddress,
		Description: input.Description,
		IsAllowed:   input.IsAllowed,
	}

	if err := utils.DB.Create(&restriction).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create IP restriction"})
		return
	}

	c.JSON(http.StatusCreated, restriction)
}

func DeleteIPRestriction(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Delete(&models.IPRestriction{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete IP restriction"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "IP restriction deleted"})
}

// Backup & Restore
func CreateBackup(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		Type        string `json:"type" binding:"required,oneof=full incremental"`
		Description string `json:"description"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Create backup directory
	backupDir := "./backups"
	os.MkdirAll(backupDir, 0755)

	fileName := fmt.Sprintf("backup_%s_%d.json", input.Type, time.Now().Unix())
	filePath := fmt.Sprintf("%s/%s", backupDir, fileName)

	// Export data (simplified - in production use proper database dump)
	backupData := map[string]interface{}{
		"user_id":     userID,
		"type":        input.Type,
		"created_at":  time.Now(),
		"description": input.Description,
	}

	jsonData, _ := json.MarshalIndent(backupData, "", "  ")
	os.WriteFile(filePath, jsonData, 0644)

	fileInfo, _ := os.Stat(filePath)

	backup := models.DataBackup{
		ID:          uuid.New(),
		UserID:      userID,
		FileName:    fileName,
		FilePath:    filePath,
		FileSize:    fileInfo.Size(),
		Type:        input.Type,
		Status:      "completed",
		Description: input.Description,
	}

	if err := utils.DB.Create(&backup).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create backup record"})
		return
	}

	c.JSON(http.StatusCreated, backup)
}

func GetBackups(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var backups []models.DataBackup
	if err := utils.DB.Where("user_id = ?", userID).Order("created_at DESC").Find(&backups).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch backups"})
		return
	}

	c.JSON(http.StatusOK, backups)
}

func RestoreBackup(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var backup models.DataBackup
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&backup).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Backup not found"})
		return
	}

	// Check if file exists
	if _, err := os.Stat(backup.FilePath); os.IsNotExist(err) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Backup file not found"})
		return
	}

	// Restore data (simplified - in production use proper database restore)
	// This is a placeholder for the actual restore logic

	c.JSON(http.StatusOK, gin.H{
		"message": "Backup restore initiated",
		"backup_id": backup.ID,
	})
}

// GDPR Compliance
func CreateGDPRRequest(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		RequestType string `json:"request_type" binding:"required,oneof=data_export data_deletion"`
		Reason      string `json:"reason"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	request := models.GDPRRequest{
		ID:          uuid.New(),
		UserID:      userID,
		RequestType: input.RequestType,
		Status:      "pending",
		Reason:      input.Reason,
	}

	if err := utils.DB.Create(&request).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create GDPR request"})
		return
	}

	c.JSON(http.StatusCreated, request)
}

func GetGDPRRequests(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var requests []models.GDPRRequest
	if err := utils.DB.Where("user_id = ?", userID).Order("created_at DESC").Find(&requests).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch GDPR requests"})
		return
	}

	c.JSON(http.StatusOK, requests)
}

func ProcessGDPRRequest(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var request models.GDPRRequest
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&request).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "GDPR request not found"})
		return
	}

	if request.Status != "pending" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Request already processed"})
		return
	}

	now := time.Now()
	request.Status = "processing"
	request.ProcessedAt = &now
	utils.DB.Save(&request)

	// Process request based on type
	if request.RequestType == "data_export" {
		// Export user data
		exportDir := "./gdpr_exports"
		os.MkdirAll(exportDir, 0755)
		fileName := fmt.Sprintf("export_%s_%d.json", userID, time.Now().Unix())
		filePath := fmt.Sprintf("%s/%s", exportDir, fileName)

		exportData := map[string]interface{}{
			"user_id":    userID,
			"exported_at": time.Now(),
			"data":       "User data would be exported here",
		}

		jsonData, _ := json.MarshalIndent(exportData, "", "  ")
		os.WriteFile(filePath, jsonData, 0644)

		request.FilePath = filePath
		request.Status = "completed"
	} else if request.RequestType == "data_deletion" {
		// Mark for deletion (actual deletion should be reviewed and approved)
		request.Status = "completed"
		request.Notes = "Data deletion request completed. Manual review required for actual deletion."
	}

	utils.DB.Save(&request)

	c.JSON(http.StatusOK, request)
}
