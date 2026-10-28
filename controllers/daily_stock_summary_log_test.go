package controllers

import (
	"testing"
	"time"
	"truerp/models"

	"github.com/google/uuid"
)

// The first run seeds a row per day since the earliest snapshot; later runs
// refresh the previous day's closing and open the new day with it.
func TestDailyStockSummaryLogWriter(t *testing.T) {
	db := setupProfitLossTestDB(t)
	userID := uuid.New()
	storeID := uuid.New()
	productID := uuid.New()
	outletID := uuid.New()

	if err := db.Create(&models.Store{
		ID: storeID, Name: "Store A", Code: "store-a", OwnerUserID: userID, IsActive: true,
	}).Error; err != nil {
		t.Fatalf("store: %v", err)
	}

	if err := db.Create(&models.Product{ID: productID, UserID: userID, Name: "P", PurchasePrice: 50}).Error; err != nil {
		t.Fatalf("product: %v", err)
	}
	mkEntry := func(entryType string, qty, cost float64, y int, m time.Month, d int) {
		e := models.StockEntry{
			ID:             uuid.New(),
			UserID:         userID,
			ProductID:      &productID,
			ItemName:       "P",
			OutletID:       outletID,
			EntryType:      entryType,
			Quantity:       qty,
			CostPrice:      cost,
			ApprovalStatus: "approved",
			EntryDate:      day(y, m, d),
		}
		if err := db.Create(&e).Error; err != nil {
			t.Fatalf("stock entry: %v", err)
		}
	}
	mkEntry("opening", 10, 50, 2026, 1, 1)
	mkEntry("purchase", 10, 70, 2026, 1, 15)
	mkEntry("sale", -5, 0, 2026, 1, 20)

	// Day-20 business activity: an invoice (1000 net of 200 tax), a purchase
	// bill (500 net of 50 tax), and a 300 expense. COGS = 2 × purchase price
	// 50 = 100. A cancelled invoice on the same day must not count.
	invID := uuid.New()
	if err := db.Create(&models.Invoice{
		ID: invID, UserID: userID, PartyID: uuid.New(), InvoiceNumber: "INV-1",
		Date: day(2026, 1, 20), Status: "paid", TotalAmount: 1200, TaxTotal: 200,
	}).Error; err != nil {
		t.Fatalf("invoice: %v", err)
	}
	if err := db.Create(&models.InvoiceItem{
		ID: uuid.New(), InvoiceID: invID, ProductID: &productID, Quantity: 2,
	}).Error; err != nil {
		t.Fatalf("invoice item: %v", err)
	}
	if err := db.Create(&models.Invoice{
		ID: uuid.New(), UserID: userID, PartyID: uuid.New(), InvoiceNumber: "INV-2",
		Date: day(2026, 1, 20), Status: "cancelled", TotalAmount: 9999,
	}).Error; err != nil {
		t.Fatalf("cancelled invoice: %v", err)
	}
	if err := db.Create(&models.PurchaseBill{
		ID: uuid.New(), UserID: userID, PartyID: uuid.New(), BillNumber: "PB-1",
		BillDate: day(2026, 1, 20), TotalAmount: 550, TaxTotal: 50,
	}).Error; err != nil {
		t.Fatalf("purchase bill: %v", err)
	}
	if err := db.Create(&models.Expense{
		ID: uuid.New(), UserID: userID, ExpenseNumber: "EXP-1", Category: "General",
		Date: day(2026, 1, 20), Amount: 300,
	}).Error; err != nil {
		t.Fatalf("expense: %v", err)
	}

	if err := writeDailyStockSummaryLogs(userID, "2026-02-01"); err != nil {
		t.Fatalf("write: %v", err)
	}

	var rows []models.DailyStockSummaryLog
	if err := db.Where("user_id = ?", userID).Order("log_date").Find(&rows).Error; err != nil {
		t.Fatalf("load rows: %v", err)
	}
	if len(rows) != 31 { // Jan 1 .. Jan 31 — entries are dated the day they close
		t.Fatalf("rows = %d, want 31", len(rows))
	}
	byDay := map[string]models.DailyStockSummaryLog{}
	for _, r := range rows {
		byDay[r.LogDate.Format("2006-01-02")] = r
	}
	check := func(d string, oq, ov, cq, cv float64) {
		t.Helper()
		r, ok := byDay[d]
		if !ok {
			t.Fatalf("missing row for %s", d)
		}
		if r.OpeningQty != oq || r.OpeningValue != ov || r.ClosingQty != cq || r.ClosingValue != cv {
			t.Fatalf("%s = open %v/%v close %v/%v, want %v/%v %v/%v",
				d, r.OpeningQty, r.OpeningValue, r.ClosingQty, r.ClosingValue, oq, ov, cq, cv)
		}
	}

	check("2026-01-01", 0, 0, 10, 500)       // opening entry day
	check("2026-01-14", 10, 500, 10, 500)    // quiet day carries forward
	check("2026-01-15", 10, 500, 20, 1200)   // purchase re-rates avg to 60
	check("2026-01-20", 20, 1200, 15, 900)   // sale day
	// Latest row (Jan 31): opening = Jan 30's closing, closing = current
	// stock at run time.
	check("2026-01-31", 15, 900, 15, 900)
	if _, ok := byDay["2026-02-01"]; ok {
		t.Fatal("unexpected row for 2026-02-01 — entries are dated the previous day")
	}

	// Every row is store-specific: stamped with the owning store's ID.
	for _, r := range rows {
		if r.StoreID == nil || *r.StoreID != storeID {
			t.Fatalf("row %s has store_id %v, want %s", r.LogDate.Format("2006-01-02"), r.StoreID, storeID)
		}
	}

	// Day aggregates: sales 1200 − 200 tax = 1000; COGS 2 × 50 = 100;
	// purchase 550 − 50 tax = 500; expenses 300. Cancelled invoice excluded.
	jan20 := byDay["2026-01-20"]
	if jan20.Sales != 1000 || jan20.COGS != 100 || jan20.Purchase != 500 || jan20.Expenses != 300 {
		t.Fatalf("jan 20 aggregates = sales %v cogs %v purchase %v expenses %v, want 1000/100/500/300",
			jan20.Sales, jan20.COGS, jan20.Purchase, jan20.Expenses)
	}
	if quiet := byDay["2026-01-14"]; quiet.Sales != 0 || quiet.COGS != 0 || quiet.Purchase != 0 || quiet.Expenses != 0 {
		t.Fatalf("quiet-day aggregates = %+v, want all zero", quiet)
	}

	// Re-running for the same day upserts — no duplicates, and still no
	// row dated today.
	if err := writeDailyStockSummaryLogs(userID, "2026-02-01"); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	var count int64
	db.Model(&models.DailyStockSummaryLog{}).Where("user_id = ?", userID).Count(&count)
	if count != 31 {
		t.Fatalf("rows after rewrite = %d, want 31", count)
	}

	// Next day's run writes the Feb 1 row: its opening equals Jan 31's
	// recorded closing (15/900) and its closing is the current position
	// after a sale of 2 on Feb 1 (15 − 2 @ 60 = 13/780).
	mkEntry("sale", -2, 0, 2026, 2, 1)
	if err := writeDailyStockSummaryLogs(userID, "2026-02-02"); err != nil {
		t.Fatalf("next-day write: %v", err)
	}
	db.Model(&models.DailyStockSummaryLog{}).Where("user_id = ?", userID).Count(&count)
	if count != 32 {
		t.Fatalf("rows after next-day write = %d, want 32", count)
	}
	if err := db.Where("user_id = ?", userID).Order("log_date").Find(&rows).Error; err != nil {
		t.Fatalf("reload rows: %v", err)
	}
	for _, r := range rows {
		byDay[r.LogDate.Format("2006-01-02")] = r
	}
	check("2026-02-01", 15, 900, 13, 780)
}
