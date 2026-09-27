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

// applyPayrollAdvanceRecovery marks the deducted advances as recovered by the payroll,
// so the same advance is not deducted again in a later payroll.
func applyPayrollAdvanceRecovery(tx *gorm.DB, advances []models.StaffAdvancePayment, payrollID uuid.UUID) error {
	for _, adv := range advances {
		if err := tx.Model(&models.StaffAdvancePayment{}).
			Where("id = ?", adv.ID).
			Updates(map[string]interface{}{
				"recovered_amount":        adv.RecoveredAmount + adv.PendingAmount,
				"pending_amount":          0,
				"is_recovered":            true,
				"status":                  "recovered",
				"recovered_by_payroll_id": payrollID,
				"payroll_recovery_amount": adv.PendingAmount,
			}).Error; err != nil {
			return err
		}
	}
	return nil
}

// reversePayrollAdvanceRecovery restores advances that were marked recovered by a payroll,
// used when the payroll is deleted.
func reversePayrollAdvanceRecovery(tx *gorm.DB, userID uuid.UUID, payroll *models.Payroll) error {
	var advances []models.StaffAdvancePayment
	if err := tx.Where("user_id = ? AND recovered_by_payroll_id = ?", userID, payroll.ID).Find(&advances).Error; err != nil {
		return err
	}
	for _, adv := range advances {
		recovered := adv.PayrollRecoveryAmount
		newRecovered := adv.RecoveredAmount - recovered
		newPending := adv.PendingAmount + recovered
		status := "pending"
		if newRecovered > 0 {
			status = "partial"
		}
		if err := tx.Model(&models.StaffAdvancePayment{}).
			Where("id = ?", adv.ID).
			Updates(map[string]interface{}{
				"recovered_amount":        newRecovered,
				"pending_amount":          newPending,
				"is_recovered":            false,
				"status":                  status,
				"recovered_by_payroll_id": nil,
				"payroll_recovery_amount": 0,
			}).Error; err != nil {
			return err
		}
	}
	return nil
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

// payrollPeriodData is the attendance + deduction + advance picture for one
// staff over a payroll period. Shared by CreatePayroll (which persists it) and
// CalculatePayroll (which previews it for the form).
type payrollPeriodData struct {
	WorkingDays   int
	PresentDays   int
	AbsentDays    int
	HalfDays      int
	PaidLeaveDays int
	WeeklyOffDays int
	// PayableDays counts paid attendance: present + paid_leave + weekly_off
	// plus half days at 0.5. Absent days are unpaid.
	PayableDays         float64
	PeriodDeductions    float64
	AdvanceRecovery     float64
	OutstandingAdvances []models.StaffAdvancePayment
}

func computePayrollPeriodData(userID, staffID uuid.UUID, startDateStr, endDateStr string) payrollPeriodData {
	var data payrollPeriodData

	var attendances []models.Attendance
	utils.DB.Where("user_id = ? AND staff_id = ? AND date >= ? AND date <= ?", userID, staffID, startDateStr, endDateStr).Find(&attendances)

	for _, att := range attendances {
		data.WorkingDays++
		switch att.Status {
		case "present":
			data.PresentDays++
		case "absent":
			data.AbsentDays++
		case "half_day":
			data.HalfDays++
		case "paid_leave":
			data.PaidLeaveDays++
		case "weekly_off":
			data.WeeklyOffDays++
		}
	}
	data.PayableDays = float64(data.PresentDays) + float64(data.HalfDays)*0.5 + float64(data.PaidLeaveDays) + float64(data.WeeklyOffDays)

	utils.DB.Model(&models.StaffDeduction{}).
		Where("user_id = ? AND staff_id = ? AND deduction_date >= ? AND deduction_date <= ? AND status = ?",
			userID, staffID, startDateStr, endDateStr, "active").
		Select("COALESCE(SUM(amount), 0)").
		Scan(&data.PeriodDeductions)

	// Any advance still outstanding up to the period end is recovered through this payroll.
	utils.DB.Where("user_id = ? AND staff_id = ? AND advance_date <= ? AND status IN ? AND pending_amount > 0",
		userID, staffID, endDateStr, []string{"pending", "partial"}).
		Order("advance_date ASC").
		Find(&data.OutstandingAdvances)

	for _, adv := range data.OutstandingAdvances {
		data.AdvanceRecovery += adv.PendingAmount
	}

	return data
}

// payableSalary prorates the basic salary by attendance. With no attendance
// recorded the full basic is payable. Monthly salary uses a /30 daily rate;
// daily/hourly staff treat BasicSalary as the per-day rate (matches the payroll
// form's "Basic Salary" semantics).
func payableSalary(basicSalary float64, salaryType string, data payrollPeriodData) float64 {
	if data.WorkingDays == 0 {
		return basicSalary
	}
	dailyRate := basicSalary
	if salaryType == "monthly" {
		dailyRate = basicSalary / 30
	}
	return data.PayableDays * dailyRate
}

// CalculatePayroll previews the attendance-based salary computation for a staff
// + period so the payroll form can prefill the exact amounts CreatePayroll will
// store. GET /payroll/calculate?staff_id=&start_date=&end_date=
func CalculatePayroll(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	staffID, err := uuid.Parse(c.Query("staff_id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "staff_id is required"})
		return
	}
	startDate, err := time.Parse("2006-01-02", c.Query("start_date"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "valid start_date is required"})
		return
	}
	endDate, err := time.Parse("2006-01-02", c.Query("end_date"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "valid end_date is required"})
		return
	}

	var staff models.Staff
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, staffID).First(&staff).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Staff not found"})
		return
	}

	data := computePayrollPeriodData(userID, staff.ID, startDate.Format("2006-01-02"), endDate.Format("2006-01-02"))
	calculated := payableSalary(staff.Salary, staff.SalaryType, data)
	net := calculated - data.PeriodDeductions - data.AdvanceRecovery
	if net < 0 {
		net = 0
	}

	c.JSON(http.StatusOK, gin.H{
		"salary":            staff.Salary,
		"salary_type":       staff.SalaryType,
		"working_days":      data.WorkingDays,
		"present_days":      data.PresentDays,
		"absent_days":       data.AbsentDays,
		"half_days":         data.HalfDays,
		"paid_leave_days":   data.PaidLeaveDays,
		"weekly_off_days":   data.WeeklyOffDays,
		"payable_days":      data.PayableDays,
		"calculated_salary": calculated,
		"period_deductions": data.PeriodDeductions,
		"advance_recovery":  data.AdvanceRecovery,
		"advance_count":     len(data.OutstandingAdvances),
		"estimated_net":     net,
	})
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

	periodData := computePayrollPeriodData(userID, input.StaffID, startDateStr, endDateStr)
	workingDays := periodData.WorkingDays
	presentDays := periodData.PresentDays
	absentDays := periodData.AbsentDays
	halfDays := periodData.HalfDays
	paidLeaveDays := periodData.PaidLeaveDays
	weeklyOffDays := periodData.WeeklyOffDays
	totalPeriodDeductions := periodData.PeriodDeductions
	outstandingAdvances := periodData.OutstandingAdvances
	totalAdvanceRecovery := periodData.AdvanceRecovery

	basicSalary := input.BasicSalary
	if basicSalary == 0 {
		basicSalary = staff.Salary
	}

	payableAmount := payableSalary(basicSalary, staff.SalaryType, periodData)

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
		if err := applyPayrollAdvanceRecovery(tx, outstandingAdvances, payroll.ID); err != nil {
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
		if err := reversePayrollAdvanceRecovery(tx, userID, &payroll); err != nil {
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
			if err := reversePayrollAdvanceRecovery(tx, userID, &payrolls[i]); err != nil {
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
