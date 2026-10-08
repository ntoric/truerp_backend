package controllers

import (
	"testing"
	"truerp/models"

	"github.com/google/uuid"
)

// Migrated stock lives only in inventory_stocks (no ledger entries); the
// snapshot baseline must surface it as closing stock, and reservation/release
// ledger noise must not move the position.
func TestStockSnapshotsMigratedBaseline(t *testing.T) {
	db := setupProfitLossTestDB(t)
	userID := uuid.New()
	productID := uuid.New()
	outletID := uuid.New()

	if err := db.Create(&models.Product{ID: productID, UserID: userID, Name: "Migrated", SKU: "M1", PurchasePrice: 10}).Error; err != nil {
		t.Fatalf("product: %v", err)
	}
	// Stock imported on 2026-09-27 at qty 100 @ 20 — no opening ledger entries
	// exist. Quantity is 90 because the post-migration sale below already
	// decremented live stock (mirroring applyInvoiceSaleStock).
	if err := db.Create(&models.InventoryStock{
		ID: uuid.New(), UserID: userID, ProductID: productID, OutletID: outletID,
		Quantity: 90, InitialQuantity: 100, AvailableQty: 90, AverageCost: 20,
		CreatedAt: day(2026, 9, 27), LastUpdated: day(2026, 9, 29),
	}).Error; err != nil {
		t.Fatalf("stock: %v", err)
	}
	// Reservation noise: -5 then release +5 — physical stock never moved.
	for _, e := range []models.StockEntry{
		{ID: uuid.New(), UserID: userID, ProductID: &productID, ItemName: "Migrated", OutletID: outletID, EntryType: "reservation", Quantity: -5, ApprovalStatus: "approved", EntryDate: day(2026, 9, 28)},
		{ID: uuid.New(), UserID: userID, ProductID: &productID, ItemName: "Migrated", OutletID: outletID, EntryType: "release", Quantity: 5, ApprovalStatus: "approved", EntryDate: day(2026, 9, 28)},
	} {
		if err := db.Create(&e).Error; err != nil {
			t.Fatalf("entry: %v", err)
		}
	}
	// A real sale after migration: -10 on 2026-09-29.
	saleProduct := productID
	if err := db.Create(&models.StockEntry{
		ID: uuid.New(), UserID: userID, ProductID: &saleProduct, ItemName: "Migrated", OutletID: outletID,
		EntryType: "sale", Quantity: -10, CostPrice: 30, ApprovalStatus: "approved", EntryDate: day(2026, 9, 29),
	}).Error; err != nil {
		t.Fatalf("sale entry: %v", err)
	}

	ensureStockSnapshots(userID)

	// Before the stock row existed: nothing.
	if pos := snapshotPositionsAsOf(userID, "2026-09-26")[productID]; pos.Qty != 0 {
		t.Fatalf("pre-migration qty = %v, want 0", pos.Qty)
	}
	// On migration day: 100 @ 20 = 2000.
	if pos := snapshotPositionsAsOf(userID, "2026-09-27")[productID]; pos.Qty != 100 || pos.Value != 2000 {
		t.Fatalf("migration-day position = %v qty / %v value, want 100/2000", pos.Qty, pos.Value)
	}
	// After the sale: 90 @ 20 = 1800 (reservation noise ignored).
	if pos := snapshotPositionsAsOf(userID, "2026-09-30")[productID]; pos.Qty != 90 || pos.Value != 1800 {
		t.Fatalf("closing position = %v qty / %v value, want 90/1800", pos.Qty, pos.Value)
	}

	// A row exists for every day since first activity — including quiet days
	// like Sep 28 (reservation/release day, no physical movement).
	var snaps []models.StockDailySnapshot
	if err := db.Where("user_id = ? AND product_id = ?", userID, productID).
		Order("snapshot_date").Find(&snaps).Error; err != nil {
		t.Fatalf("snapshots: %v", err)
	}
	byDay := map[string]models.StockDailySnapshot{}
	for _, s := range snaps {
		byDay[s.SnapshotDate.Format("2006-01-02")] = s
	}
	quiet, ok := byDay["2026-09-28"]
	if !ok {
		t.Fatal("expected a snapshot row for quiet day 2026-09-28")
	}
	if quiet.OpeningQty != 100 || quiet.ClosingQty != 100 || quiet.InQty != 0 || quiet.OutQty != 0 {
		t.Fatalf("quiet-day row = open %v close %v in %v out %v, want 100/100/0/0",
			quiet.OpeningQty, quiet.ClosingQty, quiet.InQty, quiet.OutQty)
	}
	if day29, ok := byDay["2026-09-29"]; !ok || day29.OpeningQty != 100 || day29.ClosingQty != 90 || day29.OutQty != 10 {
		t.Fatalf("sale-day row = %+v, want opening 100 closing 90 out 10", day29)
	}
	// In/out over the whole window: baseline in 100, sale out 10.
	mv := snapshotMovements(userID, "2026-09-01", "2026-09-30")[productID]
	if mv[0] != 100 || mv[1] != 10 {
		t.Fatalf("movements = in %v / out %v, want 100/10", mv[0], mv[1])
	}

	report, err := loadStockReport(userID, "custom", "", "2026-09-01", "2026-09-30")
	if err != nil {
		t.Fatalf("loadStockReport: %v", err)
	}
	if report.ClosingStockQty != 90 || report.ClosingStock != 1800 {
		t.Fatalf("report closing = %v qty / %v value, want 90/1800", report.ClosingStockQty, report.ClosingStock)
	}
}

// Snapshots must rebuild after stock data changes — a stale fingerprint must
// not serve outdated positions.
func TestStockSnapshotsRebuildOnChange(t *testing.T) {
	db := setupProfitLossTestDB(t)
	userID := uuid.New()
	productID := uuid.New()

	if err := db.Create(&models.Product{ID: productID, UserID: userID, Name: "P", PurchasePrice: 50}).Error; err != nil {
		t.Fatalf("product: %v", err)
	}
	if err := db.Create(&models.StockEntry{
		ID: uuid.New(), UserID: userID, ProductID: &productID, ItemName: "P", OutletID: uuid.New(),
		EntryType: "opening", Quantity: 10, CostPrice: 50, ApprovalStatus: "approved", EntryDate: day(2026, 1, 1),
	}).Error; err != nil {
		t.Fatalf("entry: %v", err)
	}

	ensureStockSnapshots(userID)
	if pos := snapshotPositionsAsOf(userID, "2026-01-31")[productID]; pos.Qty != 10 || pos.Value != 500 {
		t.Fatalf("initial position = %v qty / %v value, want 10/500", pos.Qty, pos.Value)
	}

	// New purchase changes the source fingerprint → next ensure rebuilds.
	if err := db.Create(&models.StockEntry{
		ID: uuid.New(), UserID: userID, ProductID: &productID, ItemName: "P", OutletID: uuid.New(),
		EntryType: "purchase", Quantity: 10, CostPrice: 70, ApprovalStatus: "approved", EntryDate: day(2026, 1, 15),
	}).Error; err != nil {
		t.Fatalf("purchase: %v", err)
	}
	ensureStockSnapshots(userID)
	// 10@50 + 10@70 → avg 60 → 20 × 60 = 1200.
	if pos := snapshotPositionsAsOf(userID, "2026-01-31")[productID]; pos.Qty != 20 || pos.Value != 1200 {
		t.Fatalf("rebuilt position = %v qty / %v value, want 20/1200", pos.Qty, pos.Value)
	}
}

// An overall opening-stock override re-anchors the aggregate position at the
// start of its effective date; computed movements on/after that day apply on
// top, and deleting the override restores the computed position.
func TestStockOpeningOverride(t *testing.T) {
	db := setupProfitLossTestDB(t)
	userID := uuid.New()
	productID := uuid.New()
	outletID := uuid.New()

	if err := db.Create(&models.Product{ID: productID, UserID: userID, Name: "P", PurchasePrice: 50}).Error; err != nil {
		t.Fatalf("product: %v", err)
	}
	for _, e := range []models.StockEntry{
		{ID: uuid.New(), UserID: userID, ProductID: &productID, ItemName: "P", OutletID: outletID,
			EntryType: "opening", Quantity: 10, CostPrice: 50, ApprovalStatus: "approved", EntryDate: day(2026, 1, 1)},
		{ID: uuid.New(), UserID: userID, ProductID: &productID, ItemName: "P", OutletID: outletID,
			EntryType: "sale", Quantity: -3, CostPrice: 80, ApprovalStatus: "approved", EntryDate: day(2026, 1, 5)},
	} {
		if err := db.Create(&e).Error; err != nil {
			t.Fatalf("entry: %v", err)
		}
	}

	ensureStockSnapshots(userID)

	// Computed: Jan 2 = 10/500; Jan 31 = 7/350.
	if qty, value := overallPositionAsOf(userID, "2026-01-31"); qty != 7 || value != 350 {
		t.Fatalf("computed position = %v qty / %v value, want 7/350", qty, value)
	}

	// Override: opening stock on Jan 3 is 100 units worth 4000 overall.
	override := models.StockOpeningOverride{
		ID: uuid.New(), UserID: userID,
		EffectiveDate: day(2026, 1, 3), Quantity: 100, Value: 4000, CreatedBy: userID,
	}
	if err := db.Create(&override).Error; err != nil {
		t.Fatalf("override: %v", err)
	}

	// "Opening on Jan 3 = 100/4000" is the closing position of Jan 2.
	if qty, value := overallPositionAsOf(userID, "2026-01-02"); qty != 100 || value != 4000 {
		t.Fatalf("override boundary position = %v qty / %v value, want 100/4000", qty, value)
	}
	// Jan 5 sale −3 @ 50 = −150 → 97 / 3850.
	if qty, value := overallPositionAsOf(userID, "2026-01-31"); qty != 97 || value != 3850 {
		t.Fatalf("post-override position = %v qty / %v value, want 97/3850", qty, value)
	}

	// Report opening stock for a period starting on the override date.
	report, err := loadStockReport(userID, "custom", "", "2026-01-03", "2026-01-31")
	if err != nil {
		t.Fatalf("loadStockReport: %v", err)
	}
	if report.OpeningStockQty != 100 || report.OpeningStock != 4000 {
		t.Fatalf("report opening = %v qty / %v value, want 100/4000", report.OpeningStockQty, report.OpeningStock)
	}
	if report.ClosingStockQty != 97 || report.ClosingStock != 3850 {
		t.Fatalf("report closing = %v qty / %v value, want 97/3850", report.ClosingStockQty, report.ClosingStock)
	}
	// The residual beyond the per-product positions is a visible line.
	var found bool
	for _, l := range report.Lines {
		if l.ProductName == "(Opening stock override)" {
			found = true
			if l.OpeningQty != 90 || l.OpeningValue != 3500 || l.ClosingQty != 90 || l.ClosingValue != 3500 {
				t.Fatalf("override line = %+v, want opening/closing 90/3500", l)
			}
		}
	}
	if !found {
		t.Fatal("expected an '(Opening stock override)' report line")
	}

	// P&L uses the same adjusted positions.
	plReport, err := loadProfitLossReport(userID, "custom", "", "2026-01-03", "2026-01-31")
	if err != nil {
		t.Fatalf("loadProfitLossReport: %v", err)
	}
	if plReport.OpeningStock != 4000 || plReport.ClosingStock != 3850 {
		t.Fatalf("P&L stock = open %v / close %v, want 4000/3850", plReport.OpeningStock, plReport.ClosingStock)
	}

	// Deleting the override restores the ledger-derived position (10 − 3 = 7 @ 50).
	if err := db.Delete(&override).Error; err != nil {
		t.Fatalf("delete override: %v", err)
	}
	if qty, value := overallPositionAsOf(userID, "2026-01-31"); qty != 7 || value != 350 {
		t.Fatalf("post-delete position = %v qty / %v value, want 7/350", qty, value)
	}
}

// A daily-profit range starting after an override's base day must open with
// the override-adjusted closing of the previous day — the first day's opening
// is the previous day's closing, so a month boundary must never jump.
func TestDailyStockPositionsOpeningCarriesOverride(t *testing.T) {
	db := setupProfitLossTestDB(t)
	userID := uuid.New()
	productID := uuid.New()
	outletID := uuid.New()

	if err := db.Create(&models.Product{ID: productID, UserID: userID, Name: "P", PurchasePrice: 50}).Error; err != nil {
		t.Fatalf("product: %v", err)
	}
	for _, e := range []models.StockEntry{
		{ID: uuid.New(), UserID: userID, ProductID: &productID, ItemName: "P", OutletID: outletID,
			EntryType: "opening", Quantity: 10, CostPrice: 50, ApprovalStatus: "approved", EntryDate: day(2026, 1, 1)},
		{ID: uuid.New(), UserID: userID, ProductID: &productID, ItemName: "P", OutletID: outletID,
			EntryType: "sale", Quantity: -3, CostPrice: 80, ApprovalStatus: "approved", EntryDate: day(2026, 1, 5)},
	} {
		if err := db.Create(&e).Error; err != nil {
			t.Fatalf("entry: %v", err)
		}
	}
	ensureStockSnapshots(userID)

	// Override effective Jan 3 → base Jan 2, computed base value 500,
	// delta = 4000 − 500 = 3500.
	if err := db.Create(&models.StockOpeningOverride{
		ID: uuid.New(), UserID: userID,
		EffectiveDate: day(2026, 1, 3), Quantity: 100, Value: 4000, CreatedBy: userID,
	}).Error; err != nil {
		t.Fatalf("override: %v", err)
	}

	positions := loadDailyStockPositions(db, userID, "2026-01-04", "2026-01-06")
	if pos := positions["2026-01-04"]; pos.Opening != 4000 || pos.Closing != 4000 {
		t.Fatalf("jan 4 = open %v / close %v, want 4000/4000", pos.Opening, pos.Closing)
	}
	if pos := positions["2026-01-05"]; pos.Opening != 4000 || pos.Closing != 3850 {
		t.Fatalf("jan 5 = open %v / close %v, want 4000/3850", pos.Opening, pos.Closing)
	}
	if pos := positions["2026-01-06"]; pos.Opening != 3850 || pos.Closing != 3850 {
		t.Fatalf("jan 6 = open %v / close %v, want 3850/3850", pos.Opening, pos.Closing)
	}
}
