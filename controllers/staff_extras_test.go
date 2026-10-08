package controllers

import (
	"testing"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openExtrasTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	previousDB := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = previousDB })
	if err := db.AutoMigrate(
		&models.Staff{},
		&models.StaffExtraAmount{},
		&models.StaffAdvancePayment{},
		&models.StaffDeduction{},
		&models.Payroll{},
		&models.Attendance{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestPayrollExtrasRoundTrip(t *testing.T) {
	db := openExtrasTestDB(t)
	userID := uuid.New()
	payrollID := uuid.New()

	staff := models.Staff{
		ID:         uuid.New(),
		UserID:     userID,
		Name:       "Test Staff",
		Salary:     30000,
		SalaryType: "monthly",
	}
	if err := db.Create(&staff).Error; err != nil {
		t.Fatalf("create staff: %v", err)
	}

	entryDate := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	addExtra := models.StaffExtraAmount{
		ID:          uuid.New(),
		UserID:      userID,
		StaffID:     staff.ID,
		ExtraNumber: "EXT-0001",
		Direction:   "add",
		Amount:      2000,
		EntryDate:   entryDate,
		Status:      "pending",
	}
	deductExtra := models.StaffExtraAmount{
		ID:          uuid.New(),
		UserID:      userID,
		StaffID:     staff.ID,
		ExtraNumber: "EXT-0002",
		Direction:   "deduct",
		Amount:      500,
		EntryDate:   entryDate,
		Status:      "pending",
	}
	// A settled extra must not fold into the period data again.
	settledExtra := models.StaffExtraAmount{
		ID:          uuid.New(),
		UserID:      userID,
		StaffID:     staff.ID,
		ExtraNumber: "EXT-0003",
		Direction:   "add",
		Amount:      9999,
		EntryDate:   entryDate,
		Status:      "settled",
	}
	for _, e := range []models.StaffExtraAmount{addExtra, deductExtra, settledExtra} {
		if err := db.Create(&e).Error; err != nil {
			t.Fatalf("create extra: %v", err)
		}
	}

	data := computePayrollPeriodData(userID, staff.ID, "2026-09-01", "2026-09-30")
	if data.ExtraAdd != 2000 || data.ExtraDeduct != 500 {
		t.Fatalf("period extras = add %.2f deduct %.2f, want 2000/500", data.ExtraAdd, data.ExtraDeduct)
	}
	if len(data.OutstandingExtras) != 2 {
		t.Fatalf("expected 2 outstanding extras, got %d", len(data.OutstandingExtras))
	}

	// Extras dated after the period end must not fold in.
	data = computePayrollPeriodData(userID, staff.ID, "2026-09-01", "2026-09-10")
	if len(data.OutstandingExtras) != 0 {
		t.Fatalf("expected 0 outstanding extras before entry date, got %d", len(data.OutstandingExtras))
	}

	// Pending extras adjust the staff balance: +add, -deduct.
	bal := computeStaffBalance(staff, nil, nil, 0, nil, 2000, 500)
	if bal.Balance != 1500 {
		t.Fatalf("balance = %.2f, want 1500", bal.Balance)
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		return applyPayrollExtraSettlement(tx, []models.StaffExtraAmount{addExtra, deductExtra}, payrollID)
	})
	if err != nil {
		t.Fatalf("apply settlement: %v", err)
	}

	var settled models.StaffExtraAmount
	db.First(&settled, addExtra.ID)
	if settled.Status != "settled" || settled.SettledAmount != 2000 ||
		settled.SettledByPayrollID == nil || *settled.SettledByPayrollID != payrollID {
		t.Fatalf("extra not settled: %+v", settled)
	}

	payroll := models.Payroll{ID: payrollID, UserID: userID}
	err = db.Transaction(func(tx *gorm.DB) error {
		return reversePayrollExtraSettlement(tx, userID, &payroll)
	})
	if err != nil {
		t.Fatalf("reverse settlement: %v", err)
	}

	var restored models.StaffExtraAmount
	db.First(&restored, deductExtra.ID)
	if restored.Status != "pending" || restored.SettledAmount != 0 || restored.SettledByPayrollID != nil {
		t.Fatalf("extra not restored: %+v", restored)
	}
}
