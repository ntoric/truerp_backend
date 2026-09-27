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

func openAdvanceTestDB(t *testing.T) *gorm.DB {
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
		&models.StaffAdvancePayment{},
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

func TestApplyStaffAdvanceExpense(t *testing.T) {
	db := openAdvanceTestDB(t)
	userID := uuid.New()
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)

	advance := models.StaffAdvancePayment{
		ID:            uuid.New(),
		UserID:        userID,
		StaffID:       uuid.New(),
		AdvanceNumber: "ADV-0001",
		Amount:        3000,
		AdvanceDate:   now,
		PendingAmount: 3000,
		PaymentMode:   "cash",
		Status:        "pending",
	}
	if err := db.Create(&advance).Error; err != nil {
		t.Fatalf("create advance: %v", err)
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		return applyStaffAdvanceExpense(tx, userID, &advance, "Ravi Kumar")
	})
	if err != nil {
		t.Fatalf("apply advance expense: %v", err)
	}

	var expense models.Expense
	if err := db.Where("user_id = ?", userID).First(&expense).Error; err != nil {
		t.Fatalf("expense not created: %v", err)
	}
	if expense.Category != "Staff Advance" || expense.Amount != 3000 || expense.Vendor != "Ravi Kumar" {
		t.Fatalf("unexpected expense: %+v", expense)
	}

	var txn models.CashTransaction
	if err := db.Where("user_id = ? AND reference = ?", userID, expense.ExpenseNumber).First(&txn).Error; err != nil {
		t.Fatalf("cash transaction not created: %v", err)
	}
	if txn.TransactionType != "expense" || txn.Amount != 3000 {
		t.Fatalf("unexpected cash transaction: %+v", txn)
	}

	var reloaded models.StaffAdvancePayment
	if err := db.First(&reloaded, advance.ID).Error; err != nil {
		t.Fatalf("reload advance: %v", err)
	}
	if reloaded.ExpenseID == nil || *reloaded.ExpenseID != expense.ID {
		t.Fatalf("advance not linked to expense")
	}
}

func TestPayrollAdvanceRecoveryRoundTrip(t *testing.T) {
	db := openAdvanceTestDB(t)
	userID := uuid.New()
	payrollID := uuid.New()

	advance := models.StaffAdvancePayment{
		ID:            uuid.New(),
		UserID:        userID,
		StaffID:       uuid.New(),
		AdvanceNumber: "ADV-0002",
		Amount:        5000,
		AdvanceDate:   time.Now(),
		PendingAmount: 5000,
		Status:        "pending",
	}
	if err := db.Create(&advance).Error; err != nil {
		t.Fatalf("create advance: %v", err)
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		return applyPayrollAdvanceRecovery(tx, []models.StaffAdvancePayment{advance}, payrollID)
	})
	if err != nil {
		t.Fatalf("apply recovery: %v", err)
	}

	var recovered models.StaffAdvancePayment
	db.First(&recovered, advance.ID)
	if recovered.PendingAmount != 0 || recovered.RecoveredAmount != 5000 ||
		recovered.Status != "recovered" || !recovered.IsRecovered {
		t.Fatalf("advance not marked recovered: %+v", recovered)
	}
	if recovered.RecoveredByPayrollID == nil || *recovered.RecoveredByPayrollID != payrollID {
		t.Fatalf("advance not linked to payroll")
	}
	if recovered.PayrollRecoveryAmount != 5000 {
		t.Fatalf("payroll recovery amount = %.2f, want 5000", recovered.PayrollRecoveryAmount)
	}

	payroll := models.Payroll{ID: payrollID, UserID: userID}
	err = db.Transaction(func(tx *gorm.DB) error {
		return reversePayrollAdvanceRecovery(tx, userID, &payroll)
	})
	if err != nil {
		t.Fatalf("reverse recovery: %v", err)
	}

	var restored models.StaffAdvancePayment
	db.First(&restored, advance.ID)
	if restored.PendingAmount != 5000 || restored.RecoveredAmount != 0 ||
		restored.Status != "pending" || restored.IsRecovered {
		t.Fatalf("advance not restored: %+v", restored)
	}
	if restored.RecoveredByPayrollID != nil || restored.PayrollRecoveryAmount != 0 {
		t.Fatalf("payroll link not cleared: %+v", restored)
	}
}
