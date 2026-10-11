package controllers

import (
	"testing"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openPayrollPaymentTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	previousDB := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = previousDB })
	if err := db.AutoMigrate(
		&models.Staff{},
		&models.Payroll{},
		&models.PayrollPayment{},
		&models.Attendance{},
		&models.StaffDeduction{},
		&models.StaffAdvancePayment{},
		&models.StaffExtraAmount{},
		&models.Expense{},
		&models.ExpenseItem{},
		&models.CashTransaction{},
		&models.BankAccount{},
		&models.Account{},
		&models.JournalEntry{},
		&models.JournalEntryLine{},
		&models.Ledger{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestPayrollPartialPayments(t *testing.T) {
	db := openPayrollPaymentTestDB(t)
	userID := uuid.New()
	now := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)

	payroll := models.Payroll{
		ID:            uuid.New(),
		UserID:        userID,
		StaffID:       uuid.New(),
		PaymentNumber: "PAY-001",
		PaymentDate:   now,
		StartDate:     now,
		EndDate:       now,
		BasicSalary:   10000,
		NetSalary:     10000,
		PaymentMode:   "cash",
		Status:        "pending",
	}
	if err := db.Create(&payroll).Error; err != nil {
		t.Fatalf("create payroll: %v", err)
	}

	// First partial payment of 4000.
	err := db.Transaction(func(tx *gorm.DB) error {
		if _, err := recordPayrollPaymentTx(tx, userID, &payroll, "Ravi Kumar", payrollPaymentInput{
			Amount:      4000,
			PaymentDate: now,
			PaymentMode: "cash",
		}); err != nil {
			return err
		}
		return refreshPayrollPaymentState(tx, &payroll)
	})
	if err != nil {
		t.Fatalf("record partial payment: %v", err)
	}

	var reloaded models.Payroll
	if err := db.First(&reloaded, payroll.ID).Error; err != nil {
		t.Fatalf("reload payroll: %v", err)
	}
	if reloaded.PaidAmount != 4000 || reloaded.Status != "partial" {
		t.Fatalf("expected partial with paid 4000, got paid=%.2f status=%s", reloaded.PaidAmount, reloaded.Status)
	}

	var expense models.Expense
	if err := db.Where("user_id = ?", userID).First(&expense).Error; err != nil {
		t.Fatalf("expense not created: %v", err)
	}
	if expense.Category != "Payroll" || expense.Amount != 4000 {
		t.Fatalf("unexpected expense: %+v", expense)
	}

	var txn models.CashTransaction
	if err := db.Where("user_id = ? AND reference = ?", userID, "PAY-001/1").First(&txn).Error; err != nil {
		t.Fatalf("cash transaction not created: %v", err)
	}
	if txn.TransactionType != "payroll" || txn.Amount != 4000 {
		t.Fatalf("unexpected cash transaction: %+v", txn)
	}

	// Second payment settles the remaining 6000.
	err = db.Transaction(func(tx *gorm.DB) error {
		if _, err := recordPayrollPaymentTx(tx, userID, &payroll, "Ravi Kumar", payrollPaymentInput{
			Amount:      6000,
			PaymentDate: now.AddDate(0, 0, 3),
			PaymentMode: "cash",
		}); err != nil {
			return err
		}
		return refreshPayrollPaymentState(tx, &payroll)
	})
	if err != nil {
		t.Fatalf("record settling payment: %v", err)
	}

	if err := db.First(&reloaded, payroll.ID).Error; err != nil {
		t.Fatalf("reload payroll: %v", err)
	}
	if reloaded.PaidAmount != 10000 || reloaded.Status != "paid" {
		t.Fatalf("expected paid with paid 10000, got paid=%.2f status=%s", reloaded.PaidAmount, reloaded.Status)
	}

	var paymentCount int64
	db.Model(&models.PayrollPayment{}).Where("payroll_id = ?", payroll.ID).Count(&paymentCount)
	if paymentCount != 2 {
		t.Fatalf("expected 2 payment rows, got %d", paymentCount)
	}

	// Reversing the payroll removes both payments, expenses and cash txns.
	err = db.Transaction(func(tx *gorm.DB) error {
		return reversePayrollPayment(tx, userID, &reloaded)
	})
	if err != nil {
		t.Fatalf("reverse payroll payment: %v", err)
	}

	db.Model(&models.PayrollPayment{}).Where("payroll_id = ?", payroll.ID).Count(&paymentCount)
	if paymentCount != 0 {
		t.Fatalf("expected payments removed, got %d", paymentCount)
	}
	var expenseCount int64
	db.Model(&models.Expense{}).Where("user_id = ? AND category = 'Payroll'", userID).Count(&expenseCount)
	if expenseCount != 0 {
		t.Fatalf("expected payroll expenses removed, got %d", expenseCount)
	}
	var txnCount int64
	db.Model(&models.CashTransaction{}).Where("user_id = ? AND transaction_type = 'payroll'", userID).Count(&txnCount)
	if txnCount != 0 {
		t.Fatalf("expected payroll cash txns removed, got %d", txnCount)
	}
}

func TestPayrollPayAllDue(t *testing.T) {
	db := openPayrollPaymentTestDB(t)
	userID := uuid.New()
	payDate := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)

	staff := models.Staff{
		ID:         uuid.New(),
		UserID:     userID,
		Name:       "Ravi Kumar",
		Salary:     30000,
		SalaryType: "monthly", // daily rate 1000
	}
	if err := db.Create(&staff).Error; err != nil {
		t.Fatalf("create staff: %v", err)
	}

	// Existing payroll, partially paid: net 10000, one 4000 payment → 6000 due.
	oldPayroll := models.Payroll{
		ID:            uuid.New(),
		UserID:        userID,
		StaffID:       staff.ID,
		PaymentNumber: "PAY-001",
		PaymentDate:   time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC),
		StartDate:     time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		EndDate:       time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC),
		BasicSalary:   10000,
		NetSalary:     10000,
		PaymentMode:   "cash",
		Status:        "pending",
	}
	if err := db.Create(&oldPayroll).Error; err != nil {
		t.Fatalf("create payroll: %v", err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if _, err := recordPayrollPaymentTx(tx, userID, &oldPayroll, staff.Name, payrollPaymentInput{
			Amount:      4000,
			PaymentDate: oldPayroll.PaymentDate,
			PaymentMode: "cash",
		}); err != nil {
			return err
		}
		return refreshPayrollPaymentState(tx, &oldPayroll)
	}); err != nil {
		t.Fatalf("record partial payment: %v", err)
	}

	// Three uncovered present days on Sep 10-12 → earned unpaid 3000.
	for _, day := range []int{10, 11, 12} {
		att := models.Attendance{
			ID:      uuid.New(),
			UserID:  userID,
			StaffID: staff.ID,
			Date:    time.Date(2026, 9, day, 0, 0, 0, 0, time.UTC),
			Status:  "present",
		}
		if err := db.Create(&att).Error; err != nil {
			t.Fatalf("create attendance: %v", err)
		}
	}

	// Pending advance 1000 and uncovered deduction 500 → net due =
	// 6000 (payroll remainder) + 3000 (uncovered) - 1000 - 500 = 7500.
	advance := models.StaffAdvancePayment{
		ID:            uuid.New(),
		UserID:        userID,
		StaffID:       staff.ID,
		AdvanceNumber: "ADV-0001",
		Amount:        1000,
		AdvanceDate:   time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC),
		PendingAmount: 1000,
		Status:        "pending",
	}
	if err := db.Create(&advance).Error; err != nil {
		t.Fatalf("create advance: %v", err)
	}
	deduction := models.StaffDeduction{
		ID:              uuid.New(),
		UserID:          userID,
		StaffID:         staff.ID,
		DeductionNumber: "DED-0001",
		DeductionType:   "penalty",
		Amount:          500,
		DeductionDate:   time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC),
		Status:          "active",
	}
	if err := db.Create(&deduction).Error; err != nil {
		t.Fatalf("create deduction: %v", err)
	}

	var totalPaid float64
	var settle *models.Payroll
	err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		totalPaid, settle, err = settleStaffDuesTx(tx, userID, &staff, payrollPaymentInput{
			PaymentDate: payDate,
			PaymentMode: "cash",
		}, "PAY-002")
		return err
	})
	if err != nil {
		t.Fatalf("settle dues: %v", err)
	}
	if totalPaid != 7500 {
		t.Fatalf("expected total paid 7500, got %.2f", totalPaid)
	}

	// Old payroll fully settled by its own payment row.
	var reloaded models.Payroll
	if err := db.First(&reloaded, oldPayroll.ID).Error; err != nil {
		t.Fatalf("reload payroll: %v", err)
	}
	if reloaded.PaidAmount != 10000 || reloaded.Status != "paid" {
		t.Fatalf("expected old payroll paid, got paid=%.2f status=%s", reloaded.PaidAmount, reloaded.Status)
	}

	// Settlement payroll covers the uncovered dues: payable 3000, folded
	// deductions+advance 1500 → net 1500, fully paid.
	if settle == nil {
		t.Fatalf("expected a settlement payroll")
	}
	if !settle.IsSettlement {
		t.Fatalf("expected is_settlement on the settlement payroll")
	}
	if settle.NetSalary != 1500 || settle.PaidAmount != 1500 || settle.Status != "paid" {
		t.Fatalf("unexpected settle payroll: net=%.2f paid=%.2f status=%s", settle.NetSalary, settle.PaidAmount, settle.Status)
	}
	if settle.Deductions != 1500 {
		t.Fatalf("expected folded deductions 1500, got %.2f", settle.Deductions)
	}

	// Advance marked recovered by the settlement payroll.
	var advReload models.StaffAdvancePayment
	if err := db.First(&advReload, advance.ID).Error; err != nil {
		t.Fatalf("reload advance: %v", err)
	}
	if advReload.PendingAmount != 0 || advReload.RecoveredByPayrollID == nil || *advReload.RecoveredByPayrollID != settle.ID {
		t.Fatalf("advance not recovered: %+v", advReload)
	}

	// Staff balance is fully zeroed.
	var payrolls []models.Payroll
	db.Where("user_id = ? AND staff_id = ?", userID, staff.ID).Order("start_date ASC").Find(&payrolls)
	var attendances []models.Attendance
	db.Where("user_id = ? AND staff_id = ?", userID, staff.ID).Find(&attendances)
	var deductions []models.StaffDeduction
	db.Where("user_id = ? AND staff_id = ? AND status = ?", userID, staff.ID, "active").Find(&deductions)
	extrasAdd, extrasDeduct := pendingExtras(userID, staff.ID)
	bal := computeStaffBalance(staff, payrolls, attendances, pendingAdvances(userID, staff.ID), deductions, extrasAdd, extrasDeduct)
	if bal.Balance != 0 {
		t.Fatalf("expected zero balance, got %.2f", bal.Balance)
	}

	// Cash left through three payment records: the initial 4000 partial, the
	// 6000 settle on the old payroll and the 1500 on the settlement payroll.
	var txnCount int64
	db.Model(&models.CashTransaction{}).Where("user_id = ? AND transaction_type = 'payroll'", userID).Count(&txnCount)
	if txnCount != 3 {
		t.Fatalf("expected 3 payroll cash txns, got %d", txnCount)
	}
}

func TestCancelPayrollRefundsPayments(t *testing.T) {
	db := openPayrollPaymentTestDB(t)
	userID := uuid.New()
	now := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)

	bank := models.BankAccount{
		ID:          uuid.New(),
		UserID:      userID,
		AccountName: "Main",
		Balance:     50000,
		IsActive:    true,
	}
	if err := db.Create(&bank).Error; err != nil {
		t.Fatalf("create bank account: %v", err)
	}

	payroll := models.Payroll{
		ID:            uuid.New(),
		UserID:        userID,
		StaffID:       uuid.New(),
		PaymentNumber: "PAY-001",
		PaymentDate:   now,
		StartDate:     now,
		EndDate:       now,
		BasicSalary:   10000,
		NetSalary:     10000,
		PaymentMode:   "bank_transfer",
		Status:        "pending",
	}
	if err := db.Create(&payroll).Error; err != nil {
		t.Fatalf("create payroll: %v", err)
	}

	// Two bank payments: 4000 + 6000.
	for i, amount := range []float64{4000, 6000} {
		amt := amount
		err := db.Transaction(func(tx *gorm.DB) error {
			if _, err := recordPayrollPaymentTx(tx, userID, &payroll, "Ravi Kumar", payrollPaymentInput{
				Amount:        amt,
				PaymentDate:   now.AddDate(0, 0, i),
				PaymentMode:   "bank_transfer",
				BankAccountID: &bank.ID,
			}); err != nil {
				return err
			}
			return refreshPayrollPaymentState(tx, &payroll)
		})
		if err != nil {
			t.Fatalf("record payment %d: %v", i, err)
		}
	}

	var bankAfter models.BankAccount
	if err := db.First(&bankAfter, bank.ID).Error; err != nil {
		t.Fatalf("reload bank: %v", err)
	}
	if bankAfter.Balance != 40000 {
		t.Fatalf("expected bank balance 40000 after payouts, got %.2f", bankAfter.Balance)
	}

	// Cancel: refund payments (keep rows + outflows, write "add" reversals),
	// drop the linked expenses and mark the payroll cancelled.
	err := db.Transaction(func(tx *gorm.DB) error {
		var p models.Payroll
		if err := tx.First(&p, payroll.ID).Error; err != nil {
			return err
		}
		if err := refundPayrollPayments(tx, userID, &p, "Ravi Kumar"); err != nil {
			return err
		}
		return tx.Model(&p).Update("status", "cancelled").Error
	})
	if err != nil {
		t.Fatalf("cancel payroll: %v", err)
	}

	var reloaded models.Payroll
	if err := db.First(&reloaded, payroll.ID).Error; err != nil {
		t.Fatalf("reload payroll: %v", err)
	}
	if reloaded.Status != "cancelled" {
		t.Fatalf("expected cancelled status, got %s", reloaded.Status)
	}

	// Payment rows stay for history.
	var paymentCount int64
	db.Model(&models.PayrollPayment{}).Where("payroll_id = ?", payroll.ID).Count(&paymentCount)
	if paymentCount != 2 {
		t.Fatalf("expected payment rows kept, got %d", paymentCount)
	}

	// Original outflows stay, each matched by a compensating "add" reversal.
	var outflowCount int64
	db.Model(&models.CashTransaction{}).
		Where("user_id = ? AND transaction_type = 'payroll'", userID).Count(&outflowCount)
	if outflowCount != 2 {
		t.Fatalf("expected 2 original outflows kept, got %d", outflowCount)
	}
	var reversalCount int64
	var reversalSum float64
	db.Model(&models.CashTransaction{}).
		Where("user_id = ? AND transaction_type = 'add' AND is_linked = ?", userID, true).
		Count(&reversalCount)
	db.Model(&models.CashTransaction{}).
		Where("user_id = ? AND transaction_type = 'add' AND is_linked = ?", userID, true).
		Select("COALESCE(SUM(amount), 0)").Scan(&reversalSum)
	if reversalCount != 2 || reversalSum != 10000 {
		t.Fatalf("expected 2 reversal entries totalling 10000, got %d rows %.2f", reversalCount, reversalSum)
	}

	// Bank balance fully restored.
	if err := db.First(&bankAfter, bank.ID).Error; err != nil {
		t.Fatalf("reload bank: %v", err)
	}
	if bankAfter.Balance != 50000 {
		t.Fatalf("expected bank balance restored to 50000, got %.2f", bankAfter.Balance)
	}

	// Linked payroll expenses are removed.
	var expenseCount int64
	db.Model(&models.Expense{}).Where("user_id = ? AND category = 'Payroll'", userID).Count(&expenseCount)
	if expenseCount != 0 {
		t.Fatalf("expected payroll expenses removed, got %d", expenseCount)
	}

	// Deleting the cancelled payroll purges outflow + reversal pairs without
	// moving the bank balance again.
	var cancelled models.Payroll
	if err := db.First(&cancelled, payroll.ID).Error; err != nil {
		t.Fatalf("reload payroll: %v", err)
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		return reversePayrollPayment(tx, userID, &cancelled)
	})
	if err != nil {
		t.Fatalf("delete cancelled payroll: %v", err)
	}
	var txnCount int64
	db.Model(&models.CashTransaction{}).Where("user_id = ?", userID).Count(&txnCount)
	if txnCount != 0 {
		t.Fatalf("expected all cash txns purged, got %d", txnCount)
	}
	if err := db.First(&bankAfter, bank.ID).Error; err != nil {
		t.Fatalf("reload bank: %v", err)
	}
	if bankAfter.Balance != 50000 {
		t.Fatalf("expected bank balance still 50000, got %.2f", bankAfter.Balance)
	}
}
