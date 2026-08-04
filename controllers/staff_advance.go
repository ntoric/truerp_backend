package controllers

import (
	"truerp/models"
	"truerp/utils"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func GetStaffAdvancePayments(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var advances []models.StaffAdvancePayment
	query := utils.DB.Where("user_id = ?", userID).Preload("Staff")

	if staffID := c.Query("staff_id"); staffID != "" {
		query = query.Where("staff_id = ?", staffID)
	}

	if status := c.Query("status"); status != "" {
		query = query.Where("status = ?", status)
	}

	if from := c.Query("from"); from != "" {
		query = query.Where("advance_date >= ?", from)
	}

	if to := c.Query("to"); to != "" {
		query = query.Where("advance_date <= ?", to)
	}

	if search := c.Query("search"); search != "" {
		query = query.Where("reason ILIKE ? OR advance_number ILIKE ?", "%"+search+"%", "%"+search+"%")
	}

	if err := query.Order("advance_date DESC, created_at DESC").Find(&advances).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch advance payments"})
		return
	}

	c.JSON(http.StatusOK, advances)
}

func GetStaffAdvancePayment(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var advance models.StaffAdvancePayment
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Preload("Staff").First(&advance).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Advance payment not found"})
		return
	}

	c.JSON(http.StatusOK, advance)
}

func CreateStaffAdvancePayment(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	userName := ""
	if name, exists := c.Get("user_name"); exists {
		userName = name.(string)
	}

	var input struct {
		StaffID               uuid.UUID  `json:"staff_id" binding:"required"`
		Amount                float64    `json:"amount" binding:"required"`
		Reason                string     `json:"reason"`
		AdvanceDate           time.Time  `json:"advance_date" binding:"required"`
		ExpectedRecoveryDate  *time.Time `json:"expected_recovery_date"`
		PaymentMode           string     `json:"payment_mode"`
		Reference             string     `json:"reference"`
		Notes                 string     `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Validate staff exists
	var staff models.Staff
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, input.StaffID).First(&staff).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Staff not found"})
		return
	}

	// Generate advance number
	var count int64
	utils.DB.Model(&models.StaffAdvancePayment{}).Where("user_id = ?", userID).Count(&count)
	advanceNumber := fmt.Sprintf("ADV-%04d", count+1)

	advance := models.StaffAdvancePayment{
		ID:                   uuid.New(),
		UserID:               userID,
		StaffID:              input.StaffID,
		AdvanceNumber:        advanceNumber,
		Amount:               input.Amount,
		Reason:               input.Reason,
		AdvanceDate:          input.AdvanceDate,
		ExpectedRecoveryDate: input.ExpectedRecoveryDate,
		IsRecovered:          false,
		RecoveredAmount:      0,
		PendingAmount:        input.Amount,
		PaymentMode:          input.PaymentMode,
		Reference:            input.Reference,
		Notes:                input.Notes,
		Status:               "pending",
	}

	if err := utils.DB.Create(&advance).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create advance payment"})
		return
	}

	// Log advance payment creation
	CreateAuditLog(
		userID,
		userName,
		"create",
		"staff_advance",
		&advance.ID,
		advanceNumber,
		fmt.Sprintf("Created staff advance: %s - %s for %s", advanceNumber, input.Reason, staff.Name),
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		map[string]interface{}{
			"staff_id": input.StaffID,
			"amount":   input.Amount,
			"reason":   input.Reason,
		},
		"success",
		"",
	)

	c.JSON(http.StatusCreated, advance)
}

func UpdateStaffAdvancePayment(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	userName := ""
	if name, exists := c.Get("user_name"); exists {
		userName = name.(string)
	}
	id := c.Param("id")

	var input struct {
		Amount                float64    `json:"amount"`
		Reason                string     `json:"reason"`
		AdvanceDate           time.Time  `json:"advance_date"`
		ExpectedRecoveryDate  *time.Time `json:"expected_recovery_date"`
		IsRecovered           bool       `json:"is_recovered"`
		RecoveredAmount       float64    `json:"recovered_amount"`
		PendingAmount         float64    `json:"pending_amount"`
		PaymentMode           string     `json:"payment_mode"`
		Reference             string     `json:"reference"`
		Status                string     `json:"status"`
		Notes                 string     `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var advance models.StaffAdvancePayment
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&advance).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Advance payment not found"})
		return
	}

	updates := map[string]interface{}{
		"amount":                input.Amount,
		"reason":                input.Reason,
		"advance_date":          input.AdvanceDate,
		"expected_recovery_date": input.ExpectedRecoveryDate,
		"is_recovered":          input.IsRecovered,
		"recovered_amount":      input.RecoveredAmount,
		"pending_amount":        input.PendingAmount,
		"payment_mode":          input.PaymentMode,
		"reference":             input.Reference,
		"status":                input.Status,
		"notes":                 input.Notes,
	}

	// Auto-update status based on recovery
	if input.RecoveredAmount >= input.Amount && input.Amount > 0 {
		updates["status"] = "recovered"
		updates["is_recovered"] = true
		updates["pending_amount"] = 0
	} else if input.RecoveredAmount > 0 && input.RecoveredAmount < input.Amount {
		updates["status"] = "partial"
		updates["pending_amount"] = input.Amount - input.RecoveredAmount
	}

	if err := utils.DB.Model(&advance).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update advance payment"})
		return
	}

	// Log advance payment update
	CreateAuditLog(
		userID,
		userName,
		"update",
		"staff_advance",
		&advance.ID,
		advance.AdvanceNumber,
		fmt.Sprintf("Updated staff advance: %s", advance.AdvanceNumber),
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		map[string]interface{}{
			"amount":   input.Amount,
			"status":   input.Status,
			"recovered_amount": input.RecoveredAmount,
		},
		"success",
		"",
	)

	c.JSON(http.StatusOK, advance)
}

func DeleteStaffAdvancePayment(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	userName := ""
	if name, exists := c.Get("user_name"); exists {
		userName = name.(string)
	}
	id := c.Param("id")

	var advance models.StaffAdvancePayment
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&advance).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Advance payment not found"})
		return
	}

	if err := utils.DB.Delete(&advance).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete advance payment"})
		return
	}

	// Log advance payment deletion
	CreateAuditLog(
		userID,
		userName,
		"delete",
		"staff_advance",
		&advance.ID,
		advance.AdvanceNumber,
		fmt.Sprintf("Deleted staff advance: %s", advance.AdvanceNumber),
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		map[string]interface{}{
			"amount": advance.Amount,
			"reason": advance.Reason,
		},
		"success",
		"",
	)

	c.JSON(http.StatusOK, gin.H{"message": "Advance payment deleted successfully"})
}

func RecoverStaffAdvance(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	userName := ""
	if name, exists := c.Get("user_name"); exists {
		userName = name.(string)
	}
	id := c.Param("id")

	var input struct {
		RecoveryAmount float64 `json:"recovery_amount" binding:"required"`
		Notes          string  `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var advance models.StaffAdvancePayment
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&advance).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Advance payment not found"})
		return
	}

	// Validate recovery amount
	if input.RecoveryAmount <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Recovery amount must be greater than 0"})
		return
	}

	if input.RecoveryAmount > advance.PendingAmount {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Recovery amount cannot exceed pending amount"})
		return
	}

	// Update recovery details
	newRecoveredAmount := advance.RecoveredAmount + input.RecoveryAmount
	newPendingAmount := advance.PendingAmount - input.RecoveryAmount
	newStatus := "partial"

	if newPendingAmount == 0 {
		newStatus = "recovered"
	}

	updates := map[string]interface{}{
		"recovered_amount": newRecoveredAmount,
		"pending_amount":  newPendingAmount,
		"status":          newStatus,
	}

	if newPendingAmount == 0 {
		updates["is_recovered"] = true
	}

	if err := utils.DB.Model(&advance).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to recover advance payment"})
		return
	}

	// Log recovery
	CreateAuditLog(
		userID,
		userName,
		"update",
		"staff_advance",
		&advance.ID,
		advance.AdvanceNumber,
		fmt.Sprintf("Recovered %s from advance payment: %s", fmt.Sprintf("%.2f", input.RecoveryAmount), advance.AdvanceNumber),
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		map[string]interface{}{
			"recovery_amount": input.RecoveryAmount,
			"remaining_pending": newPendingAmount,
		},
		"success",
		"",
	)

	c.JSON(http.StatusOK, gin.H{
		"message": "Advance payment recovered successfully",
		"recovered_amount": newRecoveredAmount,
		"pending_amount": newPendingAmount,
		"status": newStatus,
	})
}

func GetStaffAdvanceStats(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	staffID := c.Query("staff_id")

	var stats struct {
		TotalAdvances    float64 `json:"total_advances"`
		TotalRecovered   float64 `json:"total_recovered"`
		TotalPending     float64 `json:"total_pending"`
		PendingAdvances  int64   `json:"pending_advances"`
		RecoveredAdvances int64  `json:"recovered_advances"`
		ThisMonth        float64 `json:"this_month"`
	}

	query := utils.DB.Model(&models.StaffAdvancePayment{}).Where("user_id = ?", userID)
	if staffID != "" {
		query = query.Where("staff_id = ?", staffID)
	}

	// Total advances
	query.Select("COALESCE(SUM(amount), 0)").Scan(&stats.TotalAdvances)
	
	// Total recovered and pending
	query.Select("COALESCE(SUM(recovered_amount), 0)").Scan(&stats.TotalRecovered)
	query.Select("COALESCE(SUM(pending_amount), 0)").Scan(&stats.TotalPending)
	
	// Pending and recovered counts
	query.Where("status = ?", "pending").Or("status = ?", "partial").Count(&stats.PendingAdvances)
	query.Where("status = ?", "recovered").Count(&stats.RecoveredAdvances)

	// This month
	now := time.Now()
	startOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	monthQuery := utils.DB.Model(&models.StaffAdvancePayment{}).Where("user_id = ? AND advance_date >= ?", userID, startOfMonth)
	if staffID != "" {
		monthQuery = monthQuery.Where("staff_id = ?", staffID)
	}
	monthQuery.Select("COALESCE(SUM(amount), 0)").Scan(&stats.ThisMonth)

	c.JSON(http.StatusOK, stats)
}

func GetNextAdvanceNumber(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var count int64
	utils.DB.Model(&models.StaffAdvancePayment{}).Where("user_id = ?", userID).Count(&count)

	nextNum := fmt.Sprintf("ADV-%04d", count+1)
	c.JSON(http.StatusOK, gin.H{"advance_number": nextNum})
}
