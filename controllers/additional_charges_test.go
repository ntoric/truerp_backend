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

func TestNormalizeAdditionalCharges(t *testing.T) {
	items, err := models.NormalizeAdditionalCharges([]models.AdditionalCharge{
		{Label: "  Freight  ", Amount: 100},
		{Label: "", Amount: 50},
		{Label: "Empty", Amount: 0},
	})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(items))
	}
	if items[0].Label != "Freight" || items[0].Amount != 100 {
		t.Fatalf("unexpected first row: %+v", items[0])
	}
	if items[1].Label != "Additional Charge" {
		t.Fatalf("expected default label, got %q", items[1].Label)
	}
	if got := models.SumAdditionalCharges(items); got != 150 {
		t.Fatalf("sum = %v, want 150", got)
	}

	if _, err := models.NormalizeAdditionalCharges([]models.AdditionalCharge{{Label: "Bad", Amount: -5}}); err == nil {
		t.Fatal("expected error for negative amount")
	}
}

func TestInvoiceAdditionalChargeItemsRoundTrip(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	previousDB := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = previousDB })
	if err := db.AutoMigrate(&models.Invoice{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	invoice := models.Invoice{
		ID:            uuid.New(),
		UserID:        uuid.New(),
		InvoiceNumber: "INV-1",
		PartyID:       uuid.New(),
		Date:          time.Now(),
		AdditionalChargeItems: []models.AdditionalCharge{
			{Label: "Freight", Amount: 100},
			{Label: "Packing", Amount: 25.5},
		},
		AdditionalCharges: 125.5,
	}
	if err := db.Create(&invoice).Error; err != nil {
		t.Fatalf("create invoice: %v", err)
	}

	var loaded models.Invoice
	if err := db.First(&loaded, "id = ?", invoice.ID).Error; err != nil {
		t.Fatalf("load invoice: %v", err)
	}
	if len(loaded.AdditionalChargeItems) != 2 {
		t.Fatalf("expected 2 charge rows, got %d", len(loaded.AdditionalChargeItems))
	}
	if loaded.AdditionalChargeItems[0].Label != "Freight" || loaded.AdditionalChargeItems[1].Amount != 25.5 {
		t.Fatalf("unexpected rows: %+v", loaded.AdditionalChargeItems)
	}
}

func TestAdditionalChargeRowsFallback(t *testing.T) {
	// Legacy document: aggregate only, no labelled rows.
	rows := additionalChargeRows(nil, 75)
	if len(rows) != 1 || rows[0].Label != "Additional Charges" || rows[0].Amount != 75 {
		t.Fatalf("unexpected fallback rows: %+v", rows)
	}
	if rows := additionalChargeRows(nil, 0); len(rows) != 0 {
		t.Fatalf("expected no rows, got %+v", rows)
	}
	items := []models.AdditionalCharge{{Label: "Freight", Amount: 10}}
	if rows := additionalChargeRows(items, 999); len(rows) != 1 || rows[0].Label != "Freight" {
		t.Fatalf("expected labelled rows to win, got %+v", rows)
	}
}
