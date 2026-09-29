package controllers

import (
	"testing"
	"time"
	"truerp/models"

	"github.com/google/uuid"
)

func TestLoadStockReport(t *testing.T) {
	db := setupProfitLossTestDB(t)
	userID := uuid.New()
	widgetID := uuid.New()
	gadgetID := uuid.New()

	if err := db.Create(&models.Product{ID: widgetID, UserID: userID, Name: "Widget", SKU: "W1", PurchasePrice: 100}).Error; err != nil {
		t.Fatalf("product: %v", err)
	}
	if err := db.Create(&models.Product{ID: gadgetID, UserID: userID, Name: "Gadget", SKU: "G1", PurchasePrice: 50}).Error; err != nil {
		t.Fatalf("product: %v", err)
	}

	mkEntry := func(productID uuid.UUID, entryType string, qty, cost float64, d time.Time) {
		e := models.StockEntry{
			ID:             uuid.New(),
			UserID:         userID,
			ProductID:      &productID,
			ItemName:       "item",
			OutletID:       uuid.New(),
			EntryType:      entryType,
			Quantity:       qty,
			CostPrice:      cost,
			ApprovalStatus: "approved",
			EntryDate:      d,
		}
		if err := db.Create(&e).Error; err != nil {
			t.Fatalf("stock entry: %v", err)
		}
	}
	// Before period: widget opening 10 @ 100 = 1000 opening stock.
	mkEntry(widgetID, "opening", 10, 100, day(2025, 12, 20))
	// In period: widget +20 purchase, -5 sale → closing 25 @ 100 = 2500.
	mkEntry(widgetID, "purchase", 20, 100, day(2026, 1, 10))
	mkEntry(widgetID, "sale", -5, 200, day(2026, 1, 12))
	// Gadget stocked mid-period: +4 purchase, -1 sale → closing 3 @ 50 = 150.
	mkEntry(gadgetID, "purchase", 4, 50, day(2026, 1, 5))
	mkEntry(gadgetID, "sale", -1, 120, day(2026, 1, 15))
	// Pending entry must be ignored.
	if err := db.Create(&models.StockEntry{
		ID: uuid.New(), UserID: userID, ProductID: &widgetID, ItemName: "Widget",
		OutletID: uuid.New(), EntryType: "purchase", Quantity: 50, CostPrice: 100,
		ApprovalStatus: "pending", EntryDate: day(2026, 1, 15),
	}).Error; err != nil {
		t.Fatalf("pending entry: %v", err)
	}

	report, err := loadStockReport(userID, "custom", "", "2026-01-01", "2026-01-31")
	if err != nil {
		t.Fatalf("loadStockReport: %v", err)
	}

	if got := report.OpeningStockQty; got != 10 {
		t.Fatalf("opening qty = %v, want 10", got)
	}
	if got := report.OpeningStock; got != 1000 {
		t.Fatalf("opening stock = %v, want 1000", got)
	}
	if got := report.ClosingStockQty; got != 28 {
		t.Fatalf("closing qty = %v, want 28", got)
	}
	if got := report.ClosingStock; got != 2650 {
		t.Fatalf("closing stock = %v, want 2650", got)
	}
	if got := report.InQty; got != 24 {
		t.Fatalf("in qty = %v, want 24", got)
	}
	if got := report.OutQty; got != 6 {
		t.Fatalf("out qty = %v, want 6", got)
	}
	if got := report.StockChangeQty; got != 18 {
		t.Fatalf("stock change qty = %v, want 18", got)
	}
	if got := report.StockChange; got != 1650 {
		t.Fatalf("stock change = %v, want 1650", got)
	}

	if len(report.Lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(report.Lines))
	}
	// Sorted by product name: Gadget first, then Widget.
	gadget, widget := report.Lines[0], report.Lines[1]
	if gadget.ProductName != "Gadget" || widget.ProductName != "Widget" {
		t.Fatalf("lines sorted wrong: %q, %q", gadget.ProductName, widget.ProductName)
	}
	if gadget.OpeningQty != 0 || gadget.InQty != 4 || gadget.OutQty != 1 || gadget.ClosingQty != 3 {
		t.Fatalf("gadget movement = open %v in %v out %v close %v, want 0/4/1/3",
			gadget.OpeningQty, gadget.InQty, gadget.OutQty, gadget.ClosingQty)
	}
	if gadget.ClosingValue != 150 || gadget.ChangeValue != 150 {
		t.Fatalf("gadget value = closing %v change %v, want 150/150", gadget.ClosingValue, gadget.ChangeValue)
	}
	if widget.OpeningQty != 10 || widget.InQty != 20 || widget.OutQty != 5 || widget.ClosingQty != 25 {
		t.Fatalf("widget movement = open %v in %v out %v close %v, want 10/20/5/25",
			widget.OpeningQty, widget.InQty, widget.OutQty, widget.ClosingQty)
	}
	if widget.OpeningValue != 1000 || widget.ClosingValue != 2500 || widget.ChangeQty != 15 {
		t.Fatalf("widget values = open %v close %v change %v, want 1000/2500/15",
			widget.OpeningValue, widget.ClosingValue, widget.ChangeQty)
	}

	// Period filters must resolve through the shared range resolver.
	weekly, err := loadStockReport(userID, "weekly", "2026-01-08", "", "")
	if err != nil {
		t.Fatalf("loadStockReport weekly: %v", err)
	}
	if weekly.StartDate != "2026-01-05" || weekly.EndDate != "2026-01-11" {
		t.Fatalf("weekly range = %s to %s, want 2026-01-05 to 2026-01-11", weekly.StartDate, weekly.EndDate)
	}
	if _, err := loadStockReport(userID, "bogus", "", "", ""); err == nil {
		t.Fatal("expected error for invalid period")
	}
}
