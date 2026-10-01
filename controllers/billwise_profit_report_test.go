package controllers

import (
	"testing"
	"truerp/models"
	"truerp/utils"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupBillwiseProfitTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Business{},
		&models.Product{},
		&models.Party{},
		&models.Invoice{},
		&models.InvoiceItem{},
		&models.SalesReturn{},
		&models.SalesReturnItem{},
		&models.CreditNote{},
		&models.CreditNoteItem{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	previousDB := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = previousDB })
	return db
}

func TestLoadBillwiseProfitReport(t *testing.T) {
	db := setupBillwiseProfitTestDB(t)
	userID := uuid.New()
	partyID := uuid.New()
	productID := uuid.New()

	if err := db.Create(&models.Party{ID: partyID, UserID: userID, Name: "Acme Stores", PartyType: "customer"}).Error; err != nil {
		t.Fatalf("party: %v", err)
	}
	if err := db.Create(&models.Product{ID: productID, UserID: userID, Name: "Widget", SKU: "W1", PurchasePrice: 100}).Error; err != nil {
		t.Fatalf("product: %v", err)
	}

	// INV-1: 10 units @ 200 with 10% line discount → sale 1800, cost 1000,
	// plus ₹50 invoice-level discount.
	inv1 := models.Invoice{
		ID: uuid.New(), UserID: userID, PartyID: partyID, InvoiceNumber: "INV-1",
		Date: day(2026, 1, 15), Status: "paid", TotalAmount: 1900, InvoiceDiscount: 50,
		Items: []models.InvoiceItem{
			{ID: uuid.New(), ProductID: &productID, Description: "Widget", Quantity: 10, UnitPrice: 200, Discount: 10},
		},
	}
	if err := db.Create(&inv1).Error; err != nil {
		t.Fatalf("inv1: %v", err)
	}
	// INV-2: 2 units @ 150, no discount → sale 300, cost 200.
	inv2 := models.Invoice{
		ID: uuid.New(), UserID: userID, PartyID: partyID, InvoiceNumber: "INV-2",
		Date: day(2026, 1, 20), Status: "sent", TotalAmount: 330,
		Items: []models.InvoiceItem{
			{ID: uuid.New(), ProductID: &productID, Description: "Widget", Quantity: 2, UnitPrice: 150},
		},
	}
	if err := db.Create(&inv2).Error; err != nil {
		t.Fatalf("inv2: %v", err)
	}
	// Cancelled invoice must be excluded.
	if err := db.Create(&models.Invoice{
		ID: uuid.New(), UserID: userID, PartyID: partyID, InvoiceNumber: "INV-3",
		Date: day(2026, 1, 21), Status: "cancelled", TotalAmount: 999,
		Items: []models.InvoiceItem{
			{ID: uuid.New(), ProductID: &productID, Description: "Widget", Quantity: 5, UnitPrice: 200},
		},
	}).Error; err != nil {
		t.Fatalf("cancelled invoice: %v", err)
	}

	// Sales return of 1 unit of INV-2 @ 150 → reverses sale 150, cost 100.
	sr := models.SalesReturn{
		ID: uuid.New(), UserID: userID, PartyID: partyID, InvoiceID: inv2.ID,
		ReturnNumber: "SR-1", Date: day(2026, 2, 2), Amount: 150, Status: "processed",
		Items: []models.SalesReturnItem{
			{ID: uuid.New(), InvoiceItemID: inv2.Items[0].ID, ProductID: &productID, Description: "Widget", Quantity: 1, UnitPrice: 150},
		},
	}
	if err := db.Create(&sr).Error; err != nil {
		t.Fatalf("sales return: %v", err)
	}
	// Credit note on INV-1: 1 unit @ 180 → reverses sale 180, cost 100.
	cn := models.CreditNote{
		ID: uuid.New(), UserID: userID, PartyID: partyID, InvoiceID: inv1.ID,
		CreditNoteNumber: "CN-1", Date: day(2026, 2, 3), TotalAmount: 180, Status: "issued",
		Items: []models.CreditNoteItem{
			{ID: uuid.New(), InvoiceItemID: inv1.Items[0].ID, Description: "Widget", Quantity: 1, UnitPrice: 180},
		},
	}
	if err := db.Create(&cn).Error; err != nil {
		t.Fatalf("credit note: %v", err)
	}

	report, err := loadBillwiseProfitReport(userID, "custom", "", "2026-01-01", "2026-01-31", "", "", "newest")
	if err != nil {
		t.Fatalf("loadBillwiseProfitReport: %v", err)
	}
	if report.BillCount != 2 {
		t.Fatalf("bill count = %d, want 2", report.BillCount)
	}

	// Newest first → INV-2, then INV-1.
	inv2Row := report.Bills[0]
	if inv2Row.InvoiceNumber != "INV-2" {
		t.Fatalf("first bill = %s, want INV-2", inv2Row.InvoiceNumber)
	}
	// INV-2: net sale 300−150 = 150, cost 200−100 = 100 → profit 50.
	if got := inv2Row.SaleAmount; got != 300 {
		t.Fatalf("inv2 sale = %v, want 300", got)
	}
	if got := inv2Row.ReturnsAmount; got != 150 {
		t.Fatalf("inv2 returns = %v, want 150", got)
	}
	if got := inv2Row.CostAmount; got != 200 {
		t.Fatalf("inv2 cost = %v, want 200", got)
	}
	if got := inv2Row.Profit; got != 50 {
		t.Fatalf("inv2 profit = %v, want 50", got)
	}

	inv1Row := report.Bills[1]
	if inv1Row.InvoiceNumber != "INV-1" {
		t.Fatalf("second bill = %s, want INV-1", inv1Row.InvoiceNumber)
	}
	// INV-1: sale 1800 − discount 50 − returns 180 = 1570; cost 1000−100 = 900 → profit 670.
	if got := inv1Row.SaleAmount; got != 1800 {
		t.Fatalf("inv1 sale = %v, want 1800", got)
	}
	if got := inv1Row.Discount; got != 50 {
		t.Fatalf("inv1 discount = %v, want 50", got)
	}
	if got := inv1Row.ReturnsAmount; got != 180 {
		t.Fatalf("inv1 returns = %v, want 180", got)
	}
	if got := inv1Row.Profit; got != 670 {
		t.Fatalf("inv1 profit = %v, want 670", got)
	}

	// Totals: sale 2100, returns 330, discount 50, profit 720.
	if got := report.SaleAmount; got != 2100 {
		t.Fatalf("total sale = %v, want 2100", got)
	}
	if got := report.Profit; got != 720 {
		t.Fatalf("total profit = %v, want 720", got)
	}

	// Search filter by invoice number.
	filtered, err := loadBillwiseProfitReport(userID, "custom", "", "2026-01-01", "2026-01-31", "", "inv-2", "newest")
	if err != nil {
		t.Fatalf("filtered load: %v", err)
	}
	if filtered.BillCount != 1 || filtered.Bills[0].InvoiceNumber != "INV-2" {
		t.Fatalf("search filter returned %d bills", filtered.BillCount)
	}

	// Sorting: profit ascending puts INV-2 (50) before INV-1 (670).
	sorted, err := loadBillwiseProfitReport(userID, "custom", "", "2026-01-01", "2026-01-31", "", "", "profit_asc")
	if err != nil {
		t.Fatalf("sorted load: %v", err)
	}
	if sorted.Bills[0].InvoiceNumber != "INV-2" {
		t.Fatalf("profit_asc first = %s, want INV-2", sorted.Bills[0].InvoiceNumber)
	}
}
