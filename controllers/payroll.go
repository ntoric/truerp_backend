package controllers

import (
	"fmt"
	"net/http"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// repairZeroNetPayrolls fixes records created with no attendance where net was stored as 0
// despite a positive basic salary (payable fell to 0 when attendance days were all zero).
func repairZeroNetPayrolls(userID uuid.UUID) {
	var broken []models.Payroll
	utils.DB.Where(
		"user_id = ? AND net_salary = 0 AND basic_salary > 0 AND working_days = 0",
		userID,
	).Find(&broken)

	for _, p := range broken {
		net := p.BasicSalary - p.Deductions + p.Bonus
		if net < 0 {
			net = 0
		}
		if net == 0 {
			continue
		}
		utils.DB.Model(&p).Update("net_salary", net)
	}
}

func nextExpenseNumber(tx *gorm.DB, userID uuid.UUID) string {
	var count int64
	tx.Model(&models.Expense{}).Where("user_id = ?", userID).Count(&count)
	return fmt.Sprintf("EXP-%04d", count+1)
}

// applyPayrollPayment creates a Payroll expense, deducts cash/bank, and posts GL.
func applyPayrollPayment(tx *gorm.DB, userID uuid.UUID, payroll *models.Payroll, staffName string) error {
	if payroll.Status != "paid" || payroll.NetSalary <= 0 {
		return nil
	}
	if payroll.ExpenseID != nil {
		return nil
	}

	desc := fmt.Sprintf("Payroll payment %s — %s", payroll.PaymentNumber, staffName)
	expense := models.Expense{
		ID:            uuid.New(),
		UserID:        userID,
		ExpenseNumber: nextExpenseNumber(tx, userID),
		Category:      "Payroll",
		Description:   desc,
		Amount:        payroll.NetSalary,
		SubTotal:      payroll.NetSalary,
		Date:          payroll.PaymentDate,
		Vendor:        staffName,
		PaymentMode:   payroll.PaymentMode,
		BankAccountID: payroll.BankAccountID,
		Notes:         payroll.Notes,
	}
	if err := tx.Create(&expense).Error; err != nil {
		return err
	}

	item := models.ExpenseItem{
		ID:          uuid.New(),
		ExpenseID:   expense.ID,
		Description: desc,
		Quantity:    1,
		UnitPrice:   payroll.NetSalary,
		Total:       payroll.NetSalary,
	}
	if err := tx.Create(&item).Error; err != nil {
		return err
	}

	if err := recordPayrollCashOut(
		tx, userID, payroll.BankAccountID, payroll.NetSalary,
		payroll.PaymentDate, payroll.PaymentNumber, desc,
	); err != nil {
		return err
	}

	if err := postPayrollSalaryAccounting(tx, userID, payroll, &expense); err != nil {
		return err
	}

	expenseID := expense.ID
	payroll.ExpenseID = &expenseID
	return tx.Model(payroll).Update("expense_id", expenseID).Error
}

// reversePayrollPayment restores cash/bank and removes the linked payroll expense.
func reversePayrollPayment(tx *gorm.DB, userID uuid.UUID, payroll *models.Payroll) error {
	if err := reversePayrollCashOut(tx, userID, payroll.PaymentNumber); err != nil {
		return err
	}
	if payroll.ExpenseID != nil {
		if err := tx.Where("user_id = ? AND id = ?", userID, *payroll.ExpenseID).Delete(&models.Expense{}).Error; err != nil {
			return err
		}
		payroll.ExpenseID = nil
		return tx.Model(payroll).Update("expense_id", nil).Error
	}
	return nil
}

func GetPayrolls(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	repairZeroNetPayrolls(userID)

	var payrolls []models.Payroll
	query := utils.DB.Where("user_id = ?", userID).Preload("Staff").Preload("BankAccount")

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
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Preload("Staff").Preload("BankAccount").First(&payroll).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Payroll not found"})
		return
	}

	c.JSON(http.StatusOK, payroll)
}

func CreatePayroll(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		StaffID       uuid.UUID  `json:"staff_id" binding:"required"`
		PaymentDate   time.Time  `json:"payment_date" binding:"required"`
		StartDate     time.Time  `json:"start_date" binding:"required"`
		EndDate       time.Time  `json:"end_date" binding:"required"`
		BasicSalary   float64    `json:"basic_salary"`
		Deductions    float64    `json:"deductions"`
		Bonus         float64    `json:"bonus"`
		PaymentMode   string     `json:"payment_mode"`
		BankAccountID *uuid.UUID `json:"bank_account_id"`
		Reference     string     `json:"reference"`
		Notes         string     `json:"notes"`
		Status        string     `json:"status"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := validateUserBankAccount(userID, input.BankAccountID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid bank account"})
		return
	}

	var staff models.Staff
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, input.StaffID).First(&staff).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Staff not found"})
		return
	}

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

	basicSalary := input.BasicSalary
	if basicSalary == 0 {
		basicSalary = staff.Salary
	}

	var payableAmount float64
	if workingDays == 0 {
		payableAmount = basicSalary
	} else {
		dailyRate := basicSalary
		if staff.SalaryType == "monthly" {
			dailyRate = basicSalary / 30
		}
		payableDays := float64(presentDays) + float64(halfDays)*0.5 + float64(paidLeaveDays) + float64(weeklyOffDays)
		payableAmount = payableDays * dailyRate
	}

	var totalPeriodDeductions float64
	utils.DB.Model(&models.StaffDeduction{}).
		Where("user_id = ? AND staff_id = ? AND deduction_date >= ? AND deduction_date <= ? AND status = ?",
			userID, input.StaffID, startDateStr, endDateStr, "active").
		Select("COALESCE(SUM(amount), 0)").
		Scan(&totalPeriodDeductions)

	var totalAdvanceRecovery float64
	utils.DB.Model(&models.StaffAdvancePayment{}).
		Where("user_id = ? AND staff_id = ? AND advance_date >= ? AND advance_date <= ? AND status IN ?",
			userID, input.StaffID, startDateStr, endDateStr, []string{"pending", "partial"}).
		Select("COALESCE(SUM(pending_amount), 0)").
		Scan(&totalAdvanceRecovery)

	totalDeductions := input.Deductions + totalPeriodDeductions + totalAdvanceRecovery

	netSalary := payableAmount - totalDeductions + input.Bonus
	if netSalary < 0 {
		netSalary = 0
	}

	paymentMode := input.PaymentMode
	if paymentMode == "" {
		if input.BankAccountID == nil {
			paymentMode = "cash"
		} else {
			paymentMode = "bank_transfer"
		}
	}

	status := input.Status
	if status != "pending" {
		status = "paid"
	}

	var lastPayroll models.Payroll
	utils.DB.Where("user_id = ?", userID).Order("created_at DESC").First(&lastPayroll)
	paymentNumber := "PAY-001"
	if lastPayroll.ID != uuid.Nil {
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
		PaymentMode:   paymentMode,
		BankAccountID: input.BankAccountID,
		Reference:     input.Reference,
		Notes:         input.Notes,
		Status:        status,
	}

	err := utils.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&payroll).Error; err != nil {
			return err
		}
		return applyPayrollPayment(tx, userID, &payroll, staff.Name)
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create payroll: " + err.Error()})
		return
	}

	utils.DB.Preload("Staff").Preload("BankAccount").First(&payroll, payroll.ID)
	c.JSON(http.StatusCreated, payroll)
}

func UpdatePayroll(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var input struct {
		PaymentDate   time.Time  `json:"payment_date"`
		Deductions    float64    `json:"deductions"`
		Bonus         float64    `json:"bonus"`
		PaymentMode   string     `json:"payment_mode"`
		BankAccountID *uuid.UUID `json:"bank_account_id"`
		Reference     string     `json:"reference"`
		Notes         string     `json:"notes"`
		Status        string     `json:"status"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := validateUserBankAccount(userID, input.BankAccountID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid bank account"})
		return
	}

	var payroll models.Payroll
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Preload("Staff").First(&payroll).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Payroll not found"})
		return
	}

	netSalary := payroll.BasicSalary - input.Deductions + input.Bonus
	if netSalary < 0 {
		netSalary = 0
	}

	status := input.Status
	if status != "pending" && status != "paid" {
		status = payroll.Status
	}

	staffName := ""
	if payroll.Staff.Name != "" {
		staffName = payroll.Staff.Name
	}

	err := utils.DB.Transaction(func(tx *gorm.DB) error {
		wasPaid := payroll.Status == "paid" && payroll.ExpenseID != nil
		if wasPaid {
			if err := reversePayrollPayment(tx, userID, &payroll); err != nil {
				return err
			}
		}

		payroll.PaymentDate = input.PaymentDate
		payroll.Deductions = input.Deductions
		payroll.Bonus = input.Bonus
		payroll.NetSalary = netSalary
		payroll.PaymentMode = input.PaymentMode
		payroll.BankAccountID = input.BankAccountID
		payroll.Reference = input.Reference
		payroll.Notes = input.Notes
		payroll.Status = status

		if err := tx.Model(&payroll).Updates(map[string]interface{}{
			"payment_date":    payroll.PaymentDate,
			"deductions":      payroll.Deductions,
			"bonus":           payroll.Bonus,
			"net_salary":      payroll.NetSalary,
			"payment_mode":    payroll.PaymentMode,
			"bank_account_id": payroll.BankAccountID,
			"reference":       payroll.Reference,
			"notes":           payroll.Notes,
			"status":          payroll.Status,
			"expense_id":      payroll.ExpenseID,
		}).Error; err != nil {
			return err
		}

		if status == "paid" {
			return applyPayrollPayment(tx, userID, &payroll, staffName)
		}
		return nil
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update payroll: " + err.Error()})
		return
	}

	utils.DB.Preload("Staff").Preload("BankAccount").First(&payroll, payroll.ID)
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

	err := utils.DB.Transaction(func(tx *gorm.DB) error {
		if err := reversePayrollPayment(tx, userID, &payroll); err != nil {
			return err
		}
		return tx.Delete(&payroll).Error
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete payroll: " + err.Error()})
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

	err := utils.DB.Transaction(func(tx *gorm.DB) error {
		var payrolls []models.Payroll
		if err := tx.Where("user_id = ? AND id IN ?", userID, input.IDs).Find(&payrolls).Error; err != nil {
			return err
		}
		for i := range payrolls {
			if err := reversePayrollPayment(tx, userID, &payrolls[i]); err != nil {
				return err
			}
		}
		return tx.Where("user_id = ? AND id IN ?", userID, input.IDs).Delete(&models.Payroll{}).Error
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete payrolls: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Payrolls deleted successfully",
		"deleted": len(input.IDs),
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
	if input.Status != "paid" && input.Status != "pending" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Status must be paid or pending"})
		return
	}

	err := utils.DB.Transaction(func(tx *gorm.DB) error {
		var payrolls []models.Payroll
		if err := tx.Where("user_id = ? AND id IN ?", userID, input.IDs).Preload("Staff").Find(&payrolls).Error; err != nil {
			return err
		}
		for i := range payrolls {
			p := &payrolls[i]
			if input.Status == "pending" && p.Status == "paid" {
				if err := reversePayrollPayment(tx, userID, p); err != nil {
					return err
				}
			}
			p.Status = input.Status
			if err := tx.Model(p).Updates(map[string]interface{}{
				"status":     p.Status,
				"expense_id": p.ExpenseID,
			}).Error; err != nil {
				return err
			}
			if input.Status == "paid" {
				if err := applyPayrollPayment(tx, userID, p, p.Staff.Name); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update payroll status: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Payroll status updated successfully",
		"updated": len(input.IDs),
	})
}

func GetPayrollStats(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	repairZeroNetPayrolls(userID)

	var stats struct {
		TotalPayments float64 `json:"total_payments"`
		TotalPayrolls int64   `json:"total_payrolls"`
		ThisMonth     float64 `json:"this_month"`
	}

	utils.DB.Model(&models.Payroll{}).Where("user_id = ?", userID).Select("COALESCE(SUM(net_salary), 0)").Scan(&stats.TotalPayments)
	utils.DB.Model(&models.Payroll{}).Where("user_id = ?", userID).Count(&stats.TotalPayrolls)

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
