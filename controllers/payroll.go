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

func GetPayrolls(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var payrolls []models.Payroll
	query := utils.DB.Where("user_id = ?", userID).Preload("Staff")

	if staffID := c.Query("staff_id"); staffID != "" {
		query = query.Where("staff_id = ?", staffID)
	}

	if startDate := c.Query("start_date"); startDate != "" {
		query = query.Where("payment_date >= ?", startDate)
	}

	if endDate := c.Query("end_date"); endDate != "" {
		query = query.Where("payment_date <= ?", endDate)
	}

	if err := query.Order("payment_date DESC, created_at DESC").Find(&payrolls).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch payrolls"})
		return
	}

	c.JSON(http.StatusOK, payrolls)
}

func GetPayroll(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var payroll models.Payroll
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Preload("Staff").First(&payroll).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Payroll not found"})
		return
	}

	c.JSON(http.StatusOK, payroll)
}

func CreatePayroll(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		StaffID       uuid.UUID `json:"staff_id" binding:"required"`
		PaymentDate   time.Time `json:"payment_date" binding:"required"`
		StartDate     time.Time `json:"start_date" binding:"required"`
		EndDate       time.Time `json:"end_date" binding:"required"`
		BasicSalary   float64   `json:"basic_salary"`
		Deductions    float64   `json:"deductions"`
		Bonus         float64   `json:"bonus"`
		PaymentMode   string    `json:"payment_mode"`
		Reference     string    `json:"reference"`
		Notes         string    `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Get staff details
	var staff models.Staff
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, input.StaffID).First(&staff).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Staff not found"})
		return
	}

	// Calculate attendance for the period
	startDateStr := input.StartDate.Format("2006-01-02")
	endDateStr := input.EndDate.Format("2006-01-02")

	var attendances []models.Attendance
	utils.DB.Where("user_id = ? AND staff_id = ? AND date >= ? AND date <= ?", userID, input.StaffID, startDateStr, endDateStr).Find(&attendances)

	workingDays := 0
	presentDays := 0
	absentDays := 0
	halfDays := 0
	paidLeaveDays := 0
	weeklyOffDays := 0

	for _, att := range attendances {
		workingDays++
		switch att.Status {
		case "present":
			presentDays++
		case "absent":
			absentDays++
		case "half_day":
			halfDays++
		case "paid_leave":
			paidLeaveDays++
		case "weekly_off":
			weeklyOffDays++
		}
	}

	// Calculate salary
	basicSalary := input.BasicSalary
	if basicSalary == 0 {
		basicSalary = staff.Salary
	}

	// Calculate daily rate based on salary type
	dailyRate := basicSalary
	if staff.SalaryType == "monthly" {
		dailyRate = basicSalary / 30
	}

	// Calculate payable amount
	payableAmount := (float64(presentDays) + float64(halfDays)*0.5 + float64(paidLeaveDays) + float64(weeklyOffDays)) * dailyRate

	// Calculate total deductions from staff deductions for the period
	var totalPeriodDeductions float64
	utils.DB.Model(&models.StaffDeduction{}).
		Where("user_id = ? AND staff_id = ? AND deduction_date >= ? AND deduction_date <= ? AND status = ?", 
			userID, input.StaffID, startDateStr, endDateStr, "active").
		Select("COALESCE(SUM(amount), 0)").
		Scan(&totalPeriodDeductions)

	// Calculate total advance recoveries for the period
	var totalAdvanceRecovery float64
	utils.DB.Model(&models.StaffAdvancePayment{}).
		Where("user_id = ? AND staff_id = ? AND advance_date >= ? AND advance_date <= ? AND status IN ?", 
			userID, input.StaffID, startDateStr, endDateStr, []string{"pending", "partial"}).
		Select("COALESCE(SUM(pending_amount), 0)").
		Scan(&totalAdvanceRecovery)

	// Total deductions = manual input + system deductions + advance recovery
	totalDeductions := input.Deductions + totalPeriodDeductions + totalAdvanceRecovery

	netSalary := payableAmount - totalDeductions + input.Bonus

	// Generate payment number
	var lastPayroll models.Payroll
	utils.DB.Where("user_id = ?", userID).Order("created_at DESC").First(&lastPayroll)
	paymentNumber := "PAY-001"
	if lastPayroll.ID != uuid.Nil {
		// Extract number and increment
		num := 1
		fmt.Sscanf(lastPayroll.PaymentNumber, "PAY-%d", &num)
		num++
		paymentNumber = fmt.Sprintf("PAY-%03d", num)
	}

	payroll := models.Payroll{
		ID:            uuid.New(),
		UserID:        userID,
		StaffID:       input.StaffID,
		PaymentNumber: paymentNumber,
		PaymentDate:   input.PaymentDate,
		StartDate:     input.StartDate,
		EndDate:       input.EndDate,
		BasicSalary:   basicSalary,
		WorkingDays:   workingDays,
		PresentDays:   presentDays,
		AbsentDays:    absentDays,
		HalfDays:      halfDays,
		PaidLeaveDays: paidLeaveDays,
		WeeklyOffDays: weeklyOffDays,
		Deductions:    totalDeductions,
		Bonus:         input.Bonus,
		NetSalary:     netSalary,
		PaymentMode:   input.PaymentMode,
		Reference:     input.Reference,
		Notes:         input.Notes,
		Status:        "paid",
	}

	if err := utils.DB.Create(&payroll).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create payroll"})
		return
	}

	c.JSON(http.StatusCreated, payroll)
}

func UpdatePayroll(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var input struct {
		PaymentDate   time.Time `json:"payment_date"`
		Deductions    float64   `json:"deductions"`
		Bonus         float64   `json:"bonus"`
		PaymentMode   string    `json:"payment_mode"`
		Reference     string    `json:"reference"`
		Notes         string    `json:"notes"`
		Status        string    `json:"status"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var payroll models.Payroll
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&payroll).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Payroll not found"})
		return
	}

	// Recalculate net salary
	netSalary := payroll.BasicSalary - input.Deductions + input.Bonus

	updates := map[string]interface{}{
		"payment_date": input.PaymentDate,
		"deductions":   input.Deductions,
		"bonus":        input.Bonus,
		"net_salary":   netSalary,
		"payment_mode": input.PaymentMode,
		"reference":    input.Reference,
		"notes":        input.Notes,
		"status":       input.Status,
	}

	if err := utils.DB.Model(&payroll).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update payroll"})
		return
	}

	c.JSON(http.StatusOK, payroll)
}

func DeletePayroll(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var payroll models.Payroll
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&payroll).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Payroll not found"})
		return
	}

	if err := utils.DB.Delete(&payroll).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete payroll"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Payroll deleted successfully"})
}

func BulkDeletePayrolls(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		IDs []uuid.UUID `json:"ids" binding:"required"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	result := utils.DB.Where("user_id = ? AND id IN ?", userID, input.IDs).Delete(&models.Payroll{})
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete payrolls"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Payrolls deleted successfully",
		"deleted": result.RowsAffected,
	})
}

func BulkUpdatePayrollStatus(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		IDs    []uuid.UUID `json:"ids" binding:"required"`
		Status string      `json:"status" binding:"required"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	result := utils.DB.Model(&models.Payroll{}).
		Where("user_id = ? AND id IN ?", userID, input.IDs).
		Update("status", input.Status)
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update payroll status"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Payroll status updated successfully",
		"updated": result.RowsAffected,
	})
}

func GetPayrollStats(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var stats struct {
		TotalPayments float64 `json:"total_payments"`
		TotalPayrolls int64   `json:"total_payrolls"`
		ThisMonth     float64 `json:"this_month"`
	}

	// Total payments
	utils.DB.Model(&models.Payroll{}).Where("user_id = ?", userID).Select("COALESCE(SUM(net_salary), 0)").Scan(&stats.TotalPayments)
	utils.DB.Model(&models.Payroll{}).Where("user_id = ?", userID).Count(&stats.TotalPayrolls)

	// This month
	now := time.Now()
	startOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	utils.DB.Model(&models.Payroll{}).Where("user_id = ? AND payment_date >= ?", userID, startOfMonth).Select("COALESCE(SUM(net_salary), 0)").Scan(&stats.ThisMonth)

	c.JSON(http.StatusOK, stats)
}

func GetNextPaymentNumber(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var lastPayroll models.Payroll
	utils.DB.Where("user_id = ?", userID).Order("created_at DESC").First(&lastPayroll)

	paymentNumber := "PAY-001"
	if lastPayroll.ID != uuid.Nil {
		num := 1
		fmt.Sscanf(lastPayroll.PaymentNumber, "PAY-%d", &num)
		num++
		paymentNumber = fmt.Sprintf("PAY-%03d", num)
	}

	c.JSON(http.StatusOK, gin.H{"payment_number": paymentNumber})
}
