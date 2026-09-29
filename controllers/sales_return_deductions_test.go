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

func TestNormalizeDeductionItems(t *testing.T) {
	items, err := normalizeDeductionItems([]models.AdditionalCharge{
		{Label: "  Restocking fee  ", Amount: 100},
		{Label: "", Amount: 50},
		{Label: "Empty", Amount: 0},
	})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(items))
	}
	if items[0].Label != "Restocking fee" || items[0].Amount != 100 {
		t.Fatalf("unexpected first row: %+v", items[0])
	}
	if items[1].Label != "Deduction" {
		t.Fatalf("expected default label, got %q", items[1].Label)
	}
	if got := models.SumAdditionalCharges(items); got != 150 {
		t.Fatalf("sum = %v, want 150", got)
	}

	if _, err := normalizeDeductionItems([]models.AdditionalCharge{{Label: "Bad", Amount: -5}}); err == nil {
		t.Fatal("expected error for negative amount")
	}
}

func TestSalesReturnDeductionItemsRoundTrip(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	previousDB := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = previousDB })
	if err := db.AutoMigrate(&models.SalesReturn{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	sr := models.SalesReturn{
		ID:           uuid.New(),
		UserID:       uuid.New(),
		PartyID:      uuid.New(),
		ReturnNumber: "SR-0001",
		Date:         time.Now(),
		Amount:       1000,
		DeductionItems: []models.AdditionalCharge{
			{Label: "Restocking fee", Amount: 100},
			{Label: "Damage", Amount: 50},
		},
		DeductionTotal: 150,
	}
	if err := db.Create(&sr).Error; err != nil {
		t.Fatalf("create sales return: %v", err)
	}

	var loaded models.SalesReturn
	if err := db.First(&loaded, "id = ?", sr.ID).Error; err != nil {
		t.Fatalf("load sales return: %v", err)
	}
	if len(loaded.DeductionItems) != 2 {
		t.Fatalf("expected 2 deduction rows, got %d", len(loaded.DeductionItems))
	}
	if loaded.DeductionItems[0].Label != "Restocking fee" || loaded.DeductionItems[1].Amount != 50 {
		t.Fatalf("unexpected rows: %+v", loaded.DeductionItems)
	}
	if loaded.DeductionTotal != 150 {
		t.Fatalf("deduction_total = %v, want 150", loaded.DeductionTotal)
	}

	// RefundAmount is computed on read, not persisted.
	if loaded.RefundAmount != 0 {
		t.Fatalf("refund_amount should not be persisted, got %v", loaded.RefundAmount)
	}
	setSalesReturnRefund(&loaded)
	if loaded.RefundAmount != 850 {
		t.Fatalf("refund_amount = %v, want 850", loaded.RefundAmount)
	}
}
