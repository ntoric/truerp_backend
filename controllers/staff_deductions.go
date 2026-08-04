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

func GetStaffDeductions(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var deductions []models.StaffDeduction
	query := utils.DB.Where("user_id = ?", userID).Preload("Staff")

	if staffID := c.Query("staff_id"); staffID != "" {
		query = query.Where("staff_id = ?", staffID)
	}

	if deductionType := c.Query("deduction_type"); deductionType != "" {
		query = query.Where("deduction_type = ?", deductionType)
	}

	if status := c.Query("status"); status != "" {
		query = query.Where("status = ?", status)
	}

	if from := c.Query("from"); from != "" {
		query = query.Where("deduction_date >= ?", from)
	}

	if to := c.Query("to"); to != "" {
		query = query.Where("deduction_date <= ?", to)
	}

	if search := c.Query("search"); search != "" {
		query = query.Where("description ILIKE ? OR deduction_number ILIKE ?", "%"+search+"%", "%"+search+"%")
	}

	if err := query.Order("deduction_date DESC, created_at DESC").Find(&deductions).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch deductions"})
		return
	}

	c.JSON(http.StatusOK, deductions)
}

func GetStaffDeduction(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var deduction models.StaffDeduction
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Preload("Staff").First(&deduction).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deduction not found"})
		return
	}

	c.JSON(http.StatusOK, deduction)
}

func CreateStaffDeduction(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	userName := ""
	if name, exists := c.Get("user_name"); exists {
		userName = name.(string)
	}

	var input struct {
		StaffID            uuid.UUID  `json:"staff_id" binding:"required"`
		DeductionType      string     `json:"deduction_type" binding:"required"`
		Amount             float64    `json:"amount" binding:"required"`
		Description        string     `json:"description"`
		DeductionDate      time.Time  `json:"deduction_date" binding:"required"`
		IsRecurring        bool       `json:"is_recurring"`
		RecurringPeriod    string     `json:"recurring_period"`
		TotalInstallments  int        `json:"total_installments"`
		Notes              string     `json:"notes"`
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

	// Generate deduction number
	var count int64
	utils.DB.Model(&models.StaffDeduction{}).Where("user_id = ?", userID).Count(&count)
	deductionNumber := fmt.Sprintf("DED-%04d", count+1)

	deduction := models.StaffDeduction{
		ID:                uuid.New(),
		UserID:            userID,
		StaffID:           input.StaffID,
		DeductionNumber:   deductionNumber,
		DeductionType:     input.DeductionType,
		Amount:            input.Amount,
		Description:       input.Description,
		DeductionDate:     input.DeductionDate,
		IsRecurring:       input.IsRecurring,
		RecurringPeriod:   input.RecurringPeriod,
		TotalInstallments: input.TotalInstallments,
		InstallmentsPaid:  0,
		Status:            "active",
		Notes:             input.Notes,
	}

	if err := utils.DB.Create(&deduction).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create deduction"})
		return
	}

	// Log deduction creation
	CreateAuditLog(
		userID,
		userName,
		"create",
		"staff_deduction",
		&deduction.ID,
		deductionNumber,
		fmt.Sprintf("Created staff deduction: %s - %s for %s", deductionNumber, input.DeductionType, staff.Name),
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		map[string]interface{}{
			"staff_id":       input.StaffID,
			"deduction_type": input.DeductionType,
			"amount":         input.Amount,
		},
		"success",
		"",
	)

	c.JSON(http.StatusCreated, deduction)
}

func UpdateStaffDeduction(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	userName := ""
	if name, exists := c.Get("user_name"); exists {
		userName = name.(string)
	}
	id := c.Param("id")

	var input struct {
		DeductionType      string     `json:"deduction_type"`
		Amount             float64    `json:"amount"`
		Description        string     `json:"description"`
		DeductionDate      time.Time  `json:"deduction_date"`
		IsRecurring        bool       `json:"is_recurring"`
		RecurringPeriod    string     `json:"recurring_period"`
		TotalInstallments  int        `json:"total_installments"`
		InstallmentsPaid   int        `json:"installments_paid"`
		Status             string     `json:"status"`
		Notes              string     `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var deduction models.StaffDeduction
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&deduction).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deduction not found"})
		return
	}

	updates := map[string]interface{}{
		"deduction_type":      input.DeductionType,
		"amount":              input.Amount,
		"description":         input.Description,
		"deduction_date":      input.DeductionDate,
		"is_recurring":        input.IsRecurring,
		"recurring_period":    input.RecurringPeriod,
		"total_installments":  input.TotalInstallments,
		"installments_paid":   input.InstallmentsPaid,
		"status":              input.Status,
		"notes":               input.Notes,
	}

	// Auto-update status if all installments paid
	if input.InstallmentsPaid >= input.TotalInstallments && input.TotalInstallments > 0 {
		updates["status"] = "completed"
	}

	if err := utils.DB.Model(&deduction).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update deduction"})
		return
	}

	// Log deduction update
	CreateAuditLog(
		userID,
		userName,
		"update",
		"staff_deduction",
		&deduction.ID,
		deduction.DeductionNumber,
		fmt.Sprintf("Updated staff deduction: %s", deduction.DeductionNumber),
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		map[string]interface{}{
			"deduction_type": input.DeductionType,
			"amount":         input.Amount,
			"status":         input.Status,
		},
		"success",
		"",
	)

	c.JSON(http.StatusOK, deduction)
}

func DeleteStaffDeduction(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	userName := ""
	if name, exists := c.Get("user_name"); exists {
		userName = name.(string)
	}
	id := c.Param("id")

	var deduction models.StaffDeduction
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&deduction).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deduction not found"})
		return
	}

	if err := utils.DB.Delete(&deduction).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete deduction"})
		return
	}

	// Log deduction deletion
	CreateAuditLog(
		userID,
		userName,
		"delete",
		"staff_deduction",
		&deduction.ID,
		deduction.DeductionNumber,
		fmt.Sprintf("Deleted staff deduction: %s", deduction.DeductionNumber),
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		map[string]interface{}{
			"deduction_type": deduction.DeductionType,
			"amount":         deduction.Amount,
		},
		"success",
		"",
	)

	c.JSON(http.StatusOK, gin.H{"message": "Deduction deleted successfully"})
}

func GetStaffDeductionStats(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	staffID := c.Query("staff_id")

	var stats struct {
		TotalDeductions float64 `json:"total_deductions"`
		ActiveDeductions int64  `json:"active_deductions"`
		CompletedDeductions int64 `json:"completed_deductions"`
		ThisMonth       float64 `json:"this_month"`
	}

	query := utils.DB.Model(&models.StaffDeduction{}).Where("user_id = ?", userID)
	if staffID != "" {
		query = query.Where("staff_id = ?", staffID)
	}

	// Total deductions
	query.Select("COALESCE(SUM(amount), 0)").Scan(&stats.TotalDeductions)
	
	// Active and completed counts
	query.Where("status = ?", "active").Count(&stats.ActiveDeductions)
	query.Where("status = ?", "completed").Count(&stats.CompletedDeductions)

	// This month
	now := time.Now()
	startOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	monthQuery := utils.DB.Model(&models.StaffDeduction{}).Where("user_id = ? AND deduction_date >= ?", userID, startOfMonth)
	if staffID != "" {
		monthQuery = monthQuery.Where("staff_id = ?", staffID)
	}
	monthQuery.Select("COALESCE(SUM(amount), 0)").Scan(&stats.ThisMonth)

	c.JSON(http.StatusOK, stats)
}

func GetNextDeductionNumber(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var count int64
	utils.DB.Model(&models.StaffDeduction{}).Where("user_id = ?", userID).Count(&count)

	nextNum := fmt.Sprintf("DED-%04d", count+1)
	c.JSON(http.StatusOK, gin.H{"deduction_number": nextNum})
}
