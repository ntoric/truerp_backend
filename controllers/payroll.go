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
		"user_id = ? AND net_salary = 0 AND basic_salary > 0 AND working_days = 0 AND is_settlement = ?",
		userID, false,
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

// payrollPaymentStatus derives the payroll status from how much of the net
// salary has actually been paid out.
func payrollPaymentStatus(netSalary, paidAmount float64) string {
	switch {
	case paidAmount >= netSalary:
		return "paid"
	case paidAmount <= 0:
		return "pending"
	default:
		return "partial"
	}
}

// payrollPaymentInput is one payout against a payroll (full or partial).
type payrollPaymentInput struct {
	Amount        float64
	PaymentDate   time.Time
	PaymentMode   string
	BankAccountID *uuid.UUID
	Reference     string
	Notes         string
}

// recordPayrollPaymentTx records a single salary payment: it creates the
// payroll_payments row, a Payroll-category expense, a linked cash/bank
// transaction and the GL posting. Roll the totals into the payroll afterwards
// with refreshPayrollPaymentState.
func recordPayrollPaymentTx(tx *gorm.DB, userID uuid.UUID, payroll *models.Payroll, staffName string, in payrollPaymentInput) (*models.PayrollPayment, error) {
	if in.Amount <= 0 {
		return nil, nil
	}
	if in.PaymentDate.IsZero() {
		in.PaymentDate = payroll.PaymentDate
	}
	if in.PaymentMode == "" {
		in.PaymentMode = payroll.PaymentMode
	}

	var count int64
	if err := tx.Model(&models.PayrollPayment{}).
		Where("user_id = ? AND payroll_id = ?", userID, payroll.ID).
		Count(&count).Error; err != nil {
		return nil, err
	}

	payment := models.PayrollPayment{
		ID:            uuid.New(),
		UserID:        userID,
		PayrollID:     payroll.ID,
		PaymentNumber: fmt.Sprintf("%s/%d", payroll.PaymentNumber, count+1),
		Amount:        in.Amount,
		PaymentDate:   in.PaymentDate,
		PaymentMode:   in.PaymentMode,
		BankAccountID: in.BankAccountID,
		Reference:     in.Reference,
		Notes:         in.Notes,
	}
	if err := tx.Create(&payment).Error; err != nil {
		return nil, err
	}

	desc := fmt.Sprintf("Payroll payment %s — %s", payment.PaymentNumber, staffName)
	expense := models.Expense{
		ID:            uuid.New(),
		UserID:        userID,
		ExpenseNumber: nextExpenseNumber(tx, userID),
		Category:      "Payroll",
		Description:   desc,
		Amount:        payment.Amount,
		SubTotal:      payment.Amount,
		Date:          payment.PaymentDate,
		Vendor:        staffName,
		PaymentMode:   payment.PaymentMode,
		BankAccountID: payment.BankAccountID,
		Notes:         payment.Notes,
	}
	if err := tx.Create(&expense).Error; err != nil {
		return nil, err
	}

	item := models.ExpenseItem{
		ID:          uuid.New(),
		ExpenseID:   expense.ID,
		Description: desc,
		Quantity:    1,
		UnitPrice:   payment.Amount,
		Total:       payment.Amount,
	}
	if err := tx.Create(&item).Error; err != nil {
		return nil, err
	}

	if err := recordPayrollCashOut(
		tx, userID, payment.BankAccountID, payment.Amount,
		payment.PaymentDate, payment.PaymentNumber, desc,
	); err != nil {
		return nil, err
	}

	if err := postPayrollPaymentAccounting(tx, userID, &payment, desc); err != nil {
		return nil, err
	}

	expenseID := expense.ID
	payment.ExpenseID = &expenseID
	if err := tx.Model(&payment).Update("expense_id", expenseID).Error; err != nil {
		return nil, err
	}
	return &payment, nil
}

// refreshPayrollPaymentState rolls the payroll's payment rows into paid_amount
// and status, and points the payroll's payment fields at the latest payment.
func refreshPayrollPaymentState(tx *gorm.DB, payroll *models.Payroll) error {
	var total float64
	if err := tx.Model(&models.PayrollPayment{}).
		Where("payroll_id = ?", payroll.ID).
		Select("COALESCE(SUM(amount), 0)").
		Scan(&total).Error; err != nil {
		return err
	}
	payroll.PaidAmount = total
	payroll.Status = payrollPaymentStatus(payroll.NetSalary, total)

	var last models.PayrollPayment
	if err := tx.Where("payroll_id = ?", payroll.ID).
		Order("payment_date DESC, created_at DESC").
		First(&last).Error; err == nil {
		payroll.PaymentMode = last.PaymentMode
		payroll.BankAccountID = last.BankAccountID
		payroll.ExpenseID = last.ExpenseID
	} else {
		payroll.ExpenseID = nil
	}

	return tx.Model(payroll).Updates(map[string]interface{}{
		"paid_amount":     payroll.PaidAmount,
		"status":          payroll.Status,
		"payment_mode":    payroll.PaymentMode,
		"bank_account_id": payroll.BankAccountID,
		"expense_id":      payroll.ExpenseID,
	}).Error
}

// reversePayrollPaymentTx undoes one payroll payment: restores cash/bank,
// removes the linked expense and reverses its GL posting.
func reversePayrollPaymentTx(tx *gorm.DB, userID uuid.UUID, payment *models.PayrollPayment) error {
	if err := reversePayrollCashOut(tx, userID, payment.PaymentNumber); err != nil {
		return err
	}
	if payment.ExpenseID != nil {
		if err := tx.Where("user_id = ? AND id = ?", userID, *payment.ExpenseID).Delete(&models.Expense{}).Error; err != nil {
			return err
		}
	}
	if err := reverseAccountingByRef(tx, userID, "payroll", payment.ID); err != nil {
		return err
	}
	return tx.Delete(payment).Error
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

// reversePayrollPayment undoes every payment recorded against the payroll:
// restores cash/bank, removes the linked expenses, reverses the GL postings
// and deletes the payment rows. It also cleans up the legacy single
// expense/cash/GL posting for payrolls paid before payments were tracked
// individually.
func reversePayrollPayment(tx *gorm.DB, userID uuid.UUID, payroll *models.Payroll) error {
	var payments []models.PayrollPayment
	if err := tx.Where("user_id = ? AND payroll_id = ?", userID, payroll.ID).Find(&payments).Error; err != nil {
		return err
	}
	for i := range payments {
		p := &payments[i]
		if err := reversePayrollCashOut(tx, userID, p.PaymentNumber); err != nil {
			return err
		}
		if p.ExpenseID != nil {
			if err := tx.Where("user_id = ? AND id = ?", userID, *p.ExpenseID).Delete(&models.Expense{}).Error; err != nil {
				return err
			}
		}
		if err := reverseAccountingByRef(tx, userID, "payroll", p.ID); err != nil {
			return err
		}
	}
	if err := tx.Where("user_id = ? AND payroll_id = ?", userID, payroll.ID).Delete(&models.PayrollPayment{}).Error; err != nil {
		return err
	}

	// Legacy paid payrolls keyed cash, expense and GL to the payroll itself.
	if err := reversePayrollCashOut(tx, userID, payroll.PaymentNumber); err != nil {
		return err
	}
	if err := reverseAccountingByRef(tx, userID, "payroll", payroll.ID); err != nil {
		return err
	}
	if payroll.ExpenseID != nil {
		if err := tx.Where("user_id = ? AND id = ?", userID, *payroll.ExpenseID).Delete(&models.Expense{}).Error; err != nil {
			return err
		}
		payroll.ExpenseID = nil
	}
	payroll.PaidAmount = 0
	return tx.Model(payroll).Updates(map[string]interface{}{
		"expense_id":  nil,
		"paid_amount": 0,
	}).Error
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
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).
		Preload("Staff").Preload("BankAccount").
		Preload("Payments", func(db *gorm.DB) *gorm.DB {
			return db.Order("payment_date ASC, created_at ASC")
		}).
		Preload("Payments.BankAccount").
		First(&payroll).Error; err != nil {
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

// createPayrollInput is the CreatePayroll request body. PayAllDue switches the
// endpoint to settlement mode: start/end dates and salary inputs are ignored
// and the staff's outstanding balance is paid instead.
type createPayrollInput struct {
	StaffID       uuid.UUID  `json:"staff_id" binding:"required"`
	PaymentDate   time.Time  `json:"payment_date" binding:"required"`
	StartDate     time.Time  `json:"start_date"`
	EndDate       time.Time  `json:"end_date"`
	BasicSalary   float64    `json:"basic_salary"`
	Deductions    float64    `json:"deductions"`
	Bonus         float64    `json:"bonus"`
	PaidAmount    float64    `json:"paid_amount"`
	PaymentMode   string     `json:"payment_mode"`
	BankAccountID *uuid.UUID `json:"bank_account_id"`
	Reference     string     `json:"reference"`
	Notes         string     `json:"notes"`
	Status        string     `json:"status"`
	PayAllDue     bool       `json:"pay_all_due"`
}

func nextPayrollPaymentNumber(userID uuid.UUID) string {
	var lastPayroll models.Payroll
	utils.DB.Where("user_id = ?", userID).Order("created_at DESC").First(&lastPayroll)
	if lastPayroll.ID == uuid.Nil {
		return "PAY-001"
	}
	num := 1
	fmt.Sscanf(lastPayroll.PaymentNumber, "PAY-%d", &num)
	return fmt.Sprintf("PAY-%03d", num+1)
}

func CreatePayroll(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input createPayrollInput
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

	if input.PayAllDue {
		handlePayAllDue(c, userID, &staff, input)
		return
	}

	if input.StartDate.IsZero() || input.EndDate.IsZero() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "start_date and end_date are required"})
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

	// Amount to pay out now. "paid" with no explicit amount means the full net
	// salary; a lower paid_amount leaves the payroll partially paid.
	payAmount := input.PaidAmount
	if input.Status == "pending" {
		payAmount = 0
	} else if payAmount <= 0 {
		payAmount = netSalary
	}
	if payAmount > netSalary {
		payAmount = netSalary
	}

	paymentNumber := nextPayrollPaymentNumber(userID)

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
		Status:        "pending",
	}

	err := utils.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&payroll).Error; err != nil {
			return err
		}
		if err := applyPayrollAdvanceRecovery(tx, outstandingAdvances, payroll.ID); err != nil {
			return err
		}
		if _, err := recordPayrollPaymentTx(tx, userID, &payroll, staff.Name, payrollPaymentInput{
			Amount:        payAmount,
			PaymentDate:   payroll.PaymentDate,
			PaymentMode:   paymentMode,
			BankAccountID: input.BankAccountID,
			Reference:     input.Reference,
			Notes:         input.Notes,
		}); err != nil {
			return err
		}
		return refreshPayrollPaymentState(tx, &payroll)
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create payroll: " + err.Error()})
		return
	}

	utils.DB.Preload("Staff").Preload("BankAccount").First(&payroll, payroll.ID)
	c.JSON(http.StatusCreated, payroll)
}

// handlePayAllDue pays a staff member's outstanding balance in one action.
// Existing payrolls with unpaid remainders are settled oldest-first via normal
// payroll payments, then a settlement payroll (is_settlement) is created for
// dues not yet formalized: attendance days uncovered by any payroll, uncovered
// active deductions and outstanding advances. The staff's balance drops by
// exactly the amount paid.
func handlePayAllDue(c *gin.Context, userID uuid.UUID, staff *models.Staff, input createPayrollInput) {
	if input.PaymentDate.IsZero() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "payment_date is required"})
		return
	}

	paymentMode := input.PaymentMode
	if paymentMode == "" {
		if input.BankAccountID == nil {
			paymentMode = "cash"
		} else {
			paymentMode = "bank_transfer"
		}
	}

	paymentNumber := nextPayrollPaymentNumber(userID)

	var totalPaid float64
	var settlePayroll *models.Payroll
	err := utils.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		totalPaid, settlePayroll, err = settleStaffDuesTx(tx, userID, staff, payrollPaymentInput{
			Amount:        input.PaidAmount,
			PaymentDate:   input.PaymentDate,
			PaymentMode:   paymentMode,
			BankAccountID: input.BankAccountID,
			Reference:     input.Reference,
			Notes:         input.Notes,
		}, paymentNumber)
		return err
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"total_paid": totalPaid,
		"payroll":    settlePayroll,
	})
}

// settleStaffDuesTx performs the pay-all-dues work inside a transaction and
// returns the total actually paid plus the settlement payroll, if one was
// created.
func settleStaffDuesTx(tx *gorm.DB, userID uuid.UUID, staff *models.Staff, in payrollPaymentInput, paymentNumber string) (float64, *models.Payroll, error) {
	var payrolls []models.Payroll
	if err := tx.Where("user_id = ? AND staff_id = ?", userID, staff.ID).
		Order("start_date ASC, created_at ASC").Find(&payrolls).Error; err != nil {
		return 0, nil, err
	}
	var attendances []models.Attendance
	if err := tx.Where("user_id = ? AND staff_id = ?", userID, staff.ID).Find(&attendances).Error; err != nil {
		return 0, nil, err
	}
	var deductions []models.StaffDeduction
	if err := tx.Where("user_id = ? AND staff_id = ? AND status = ?", userID, staff.ID, "active").
		Find(&deductions).Error; err != nil {
		return 0, nil, err
	}
	var advances []models.StaffAdvancePayment
	if err := tx.Where("user_id = ? AND staff_id = ? AND status IN ? AND pending_amount > 0",
		userID, staff.ID, []string{"pending", "partial"}).Order("advance_date ASC").Find(&advances).Error; err != nil {
		return 0, nil, err
	}
	var advancesPending float64
	for _, a := range advances {
		advancesPending += a.PendingAmount
	}

	balance := computeStaffBalance(*staff, payrolls, attendances, advancesPending, deductions)
	if balance.Balance <= 0 {
		return 0, nil, fmt.Errorf("no dues pending for this staff")
	}
	amount := in.Amount
	if amount <= 0 || amount > balance.Balance {
		amount = balance.Balance
	}
	remaining := amount
	payDateStr := in.PaymentDate.Format("2006-01-02")

	// Settle unpaid payrolls, oldest first.
	for i := range payrolls {
		p := &payrolls[i]
		due := p.NetSalary - p.PaidAmount
		if due <= 0 || remaining <= 0 {
			continue
		}
		pay := due
		if pay > remaining {
			pay = remaining
		}
		payIn := in
		payIn.Amount = pay
		if _, err := recordPayrollPaymentTx(tx, userID, p, staff.Name, payIn); err != nil {
			return 0, nil, err
		}
		if err := refreshPayrollPaymentState(tx, p); err != nil {
			return 0, nil, err
		}
		remaining -= pay
	}

	// Anything uncovered by a payroll period is formalized into a settlement
	// payroll so uncovered days and pending deductions/advances stop floating.
	covered := map[string]bool{}
	for _, p := range payrolls {
		for d := p.StartDate; !d.After(p.EndDate); d = d.AddDate(0, 0, 1) {
			covered[d.Format("2006-01-02")] = true
		}
	}

	var uncoveredAtt []models.Attendance
	settleStart := in.PaymentDate
	for _, a := range attendances {
		ds := a.Date.Format("2006-01-02")
		if covered[ds] || ds > payDateStr {
			continue
		}
		uncoveredAtt = append(uncoveredAtt, a)
		if a.Date.Before(settleStart) {
			settleStart = a.Date
		}
	}
	var foldDeductions float64
	for _, d := range deductions {
		ds := d.DeductionDate.Format("2006-01-02")
		if covered[ds] || ds > payDateStr {
			continue
		}
		foldDeductions += d.Amount
		if d.DeductionDate.Before(settleStart) {
			settleStart = d.DeductionDate
		}
	}
	var foldAdvances []models.StaffAdvancePayment
	var foldAdvanceTotal float64
	for _, adv := range advances {
		if adv.AdvanceDate.Format("2006-01-02") > payDateStr {
			continue
		}
		foldAdvances = append(foldAdvances, adv)
		foldAdvanceTotal += adv.PendingAmount
	}

	var settlePayroll *models.Payroll
	if len(uncoveredAtt) > 0 || foldDeductions > 0 || foldAdvanceTotal > 0 {
		var workingDays, presentDays, absentDays, halfDays, paidLeaveDays, weeklyOffDays int
		var payableDays float64
		for _, a := range uncoveredAtt {
			workingDays++
			switch a.Status {
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
			payableDays += payableWeight(a.Status)
		}
		totalDeductions := foldDeductions + foldAdvanceTotal
		net := payableDays*staffDailyRate(*staff) - totalDeductions
		if net < 0 {
			net = 0
		}

		sp := models.Payroll{
			ID:            uuid.New(),
			UserID:        userID,
			StaffID:       staff.ID,
			PaymentNumber: paymentNumber,
			PaymentDate:   in.PaymentDate,
			StartDate:     settleStart,
			EndDate:       in.PaymentDate,
			BasicSalary:   staff.Salary,
			WorkingDays:   workingDays,
			PresentDays:   presentDays,
			AbsentDays:    absentDays,
			HalfDays:      halfDays,
			PaidLeaveDays: paidLeaveDays,
			WeeklyOffDays: weeklyOffDays,
			Deductions:    totalDeductions,
			NetSalary:     net,
			PaymentMode:   in.PaymentMode,
			BankAccountID: in.BankAccountID,
			Reference:     in.Reference,
			Notes:         in.Notes,
			IsSettlement:  true,
			Status:        "pending",
		}
		if err := tx.Create(&sp).Error; err != nil {
			return 0, nil, err
		}
		if err := applyPayrollAdvanceRecovery(tx, foldAdvances, sp.ID); err != nil {
			return 0, nil, err
		}
		pay := net
		if pay > remaining {
			pay = remaining
		}
		payIn := in
		payIn.Amount = pay
		if _, err := recordPayrollPaymentTx(tx, userID, &sp, staff.Name, payIn); err != nil {
			return 0, nil, err
		}
		if err := refreshPayrollPaymentState(tx, &sp); err != nil {
			return 0, nil, err
		}
		remaining -= pay
		settlePayroll = &sp
	}

	return amount - remaining, settlePayroll, nil
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
	if payroll.IsSettlement {
		// Settlement payrolls earn only for attendance days not covered by
		// other payrolls; their basic salary is informational.
		var attendances []models.Attendance
		utils.DB.Where("user_id = ? AND staff_id = ?", userID, payroll.StaffID).Find(&attendances)
		var others []models.Payroll
		utils.DB.Where("user_id = ? AND staff_id = ? AND id != ?", userID, payroll.StaffID, payroll.ID).Find(&others)
		covered := map[string]bool{}
		for _, p := range others {
			for d := p.StartDate; !d.After(p.EndDate); d = d.AddDate(0, 0, 1) {
				covered[d.Format("2006-01-02")] = true
			}
		}
		startStr := payroll.StartDate.Format("2006-01-02")
		endStr := payroll.EndDate.Format("2006-01-02")
		var payableDays float64
		for _, a := range attendances {
			ds := a.Date.Format("2006-01-02")
			if covered[ds] || ds < startStr || ds > endStr {
				continue
			}
			payableDays += payableWeight(a.Status)
		}
		netSalary = payableDays*staffDailyRate(payroll.Staff) - input.Deductions + input.Bonus
	}
	if netSalary < 0 {
		netSalary = 0
	}

	staffName := payroll.Staff.Name

	err := utils.DB.Transaction(func(tx *gorm.DB) error {
		// Marking pending reverses every payment made so far; marking paid tops
		// up any remaining balance. Payment history is otherwise preserved.
		if input.Status == "pending" {
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

		if err := tx.Model(&payroll).Updates(map[string]interface{}{
			"payment_date":    payroll.PaymentDate,
			"deductions":      payroll.Deductions,
			"bonus":           payroll.Bonus,
			"net_salary":      payroll.NetSalary,
			"payment_mode":    payroll.PaymentMode,
			"bank_account_id": payroll.BankAccountID,
			"reference":       payroll.Reference,
			"notes":           payroll.Notes,
		}).Error; err != nil {
			return err
		}

		if input.Status == "paid" {
			remaining := payroll.NetSalary - payroll.PaidAmount
			if _, err := recordPayrollPaymentTx(tx, userID, &payroll, staffName, payrollPaymentInput{
				Amount:        remaining,
				PaymentDate:   payroll.PaymentDate,
				PaymentMode:   payroll.PaymentMode,
				BankAccountID: payroll.BankAccountID,
				Reference:     payroll.Reference,
				Notes:         payroll.Notes,
			}); err != nil {
				return err
			}
		}
		return refreshPayrollPaymentState(tx, &payroll)
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update payroll: " + err.Error()})
		return
	}

	utils.DB.Preload("Staff").Preload("BankAccount").First(&payroll, payroll.ID)
	c.JSON(http.StatusOK, payroll)
}

// GetPayrollPayments lists the individual payments made against a payroll.
// GET /payroll/:id/payments
func GetPayrollPayments(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var payroll models.Payroll
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&payroll).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Payroll not found"})
		return
	}

	var payments []models.PayrollPayment
	if err := utils.DB.Where("user_id = ? AND payroll_id = ?", userID, payroll.ID).
		Preload("BankAccount").
		Order("payment_date ASC, created_at ASC").
		Find(&payments).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch payments"})
		return
	}
	c.JSON(http.StatusOK, payments)
}

// CreatePayrollPayment records a (partial or settling) payment against a
// payroll that is not yet fully paid.
// POST /payroll/:id/payments
func CreatePayrollPayment(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var input struct {
		Amount        float64    `json:"amount" binding:"required"`
		PaymentDate   time.Time  `json:"payment_date"`
		PaymentMode   string     `json:"payment_mode"`
		BankAccountID *uuid.UUID `json:"bank_account_id"`
		Reference     string     `json:"reference"`
		Notes         string     `json:"notes"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if input.Amount <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Amount must be greater than zero"})
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

	remaining := payroll.NetSalary - payroll.PaidAmount
	if remaining <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Payroll is already fully paid"})
		return
	}
	if input.Amount > remaining {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Amount exceeds remaining balance of %.2f", remaining)})
		return
	}

	if input.PaymentMode == "" {
		input.PaymentMode = payroll.PaymentMode
	}

	err := utils.DB.Transaction(func(tx *gorm.DB) error {
		if _, err := recordPayrollPaymentTx(tx, userID, &payroll, payroll.Staff.Name, payrollPaymentInput{
			Amount:        input.Amount,
			PaymentDate:   input.PaymentDate,
			PaymentMode:   input.PaymentMode,
			BankAccountID: input.BankAccountID,
			Reference:     input.Reference,
			Notes:         input.Notes,
		}); err != nil {
			return err
		}
		return refreshPayrollPaymentState(tx, &payroll)
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to record payment: " + err.Error()})
		return
	}

	utils.DB.Preload("Staff").Preload("BankAccount").First(&payroll, payroll.ID)
	c.JSON(http.StatusCreated, payroll)
}

// DeletePayrollPayment reverses one payment against a payroll (cash/bank,
// expense and GL), returning the payroll to pending/partial.
// DELETE /payroll/:id/payments/:paymentId
func DeletePayrollPayment(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")
	paymentID := c.Param("paymentId")

	var payroll models.Payroll
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&payroll).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Payroll not found"})
		return
	}

	var payment models.PayrollPayment
	if err := utils.DB.Where("user_id = ? AND id = ? AND payroll_id = ?", userID, paymentID, payroll.ID).First(&payment).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Payment not found"})
		return
	}

	err := utils.DB.Transaction(func(tx *gorm.DB) error {
		if err := reversePayrollPaymentTx(tx, userID, &payment); err != nil {
			return err
		}
		return refreshPayrollPaymentState(tx, &payroll)
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete payment: " + err.Error()})
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
			if input.Status == "pending" {
				if err := reversePayrollPayment(tx, userID, p); err != nil {
					return err
				}
			} else if input.Status == "paid" {
				remaining := p.NetSalary - p.PaidAmount
				if _, err := recordPayrollPaymentTx(tx, userID, p, p.Staff.Name, payrollPaymentInput{
					Amount:        remaining,
					PaymentDate:   p.PaymentDate,
					PaymentMode:   p.PaymentMode,
					BankAccountID: p.BankAccountID,
					Reference:     p.Reference,
					Notes:         p.Notes,
				}); err != nil {
					return err
				}
			}
			if err := refreshPayrollPaymentState(tx, p); err != nil {
				return err
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

	utils.DB.Model(&models.Payroll{}).Where("user_id = ?", userID).Select("COALESCE(SUM(paid_amount), 0)").Scan(&stats.TotalPayments)
	utils.DB.Model(&models.Payroll{}).Where("user_id = ?", userID).Count(&stats.TotalPayrolls)

	now := time.Now()
	startOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	utils.DB.Model(&models.Payroll{}).Where("user_id = ? AND payment_date >= ?", userID, startOfMonth).Select("COALESCE(SUM(paid_amount), 0)").Scan(&stats.ThisMonth)

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
