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

func openPurchaseReturnTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// Shared cache so reads through utils.DB see rows written on other
	// pooled connections.
	db, err := gorm.Open(sqlite.Open("file:purchasereturn?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Party{},
		&models.PurchaseReturn{},
		&models.PurchaseReturnItem{},
		&models.Payment{},
		&models.CashTransaction{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// The purchase return import must create one processed PurchaseReturn per
// "Purchase Return" row, skip other transaction types, never touch payments,
// cash transactions or party balances, and be a no-op on re-run.
func TestImportPurchaseReturnsRows(t *testing.T) {
	db := openPurchaseReturnTestDB(t)
	previousDB := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = previousDB })

	userID := uuid.New()

	vendor := models.Party{ID: uuid.New(), UserID: userID, Name: "Vendor Co", PartyType: "vendor", IsActive: true}
	if err := db.Create(&vendor).Error; err != nil {
		t.Fatalf("seed vendor: %v", err)
	}

	// Pre-existing returns exercise the dedupe rules:
	//   PR-0001 — unrelated app-created return occupying serial 1's number;
	//             serial 1 must still import under a suffixed number.
	//   PR-0002 — leftover from an earlier padded-scheme migration; the CSV
	//             row for serial 2 carries the same party+date+amount, so it
	//             must be recognised as already imported and skipped.
	//   PR-9    — unrelated document; serial 9's PR-0009 is free anyway.
	d := func(s string) time.Time {
		tm, err := time.Parse("02/01/2006", s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		return tm
	}
	for _, r := range []models.PurchaseReturn{
		{ID: uuid.New(), UserID: userID, PartyID: vendor.ID, ReturnNumber: "PR-0001", Date: d("01/01/2024"), Amount: 999, Status: "processed"},
		{ID: uuid.New(), UserID: userID, PartyID: vendor.ID, ReturnNumber: "PR-0002", Date: d("22/10/2025"), Amount: 99.65, Status: "processed"},
		{ID: uuid.New(), UserID: userID, PartyID: vendor.ID, ReturnNumber: "PR-0009", Date: d("05/05/2024"), Amount: 777, Status: "processed"},
	} {
		if err := db.Create(&r).Error; err != nil {
			t.Fatalf("seed return %s: %v", r.ReturnNumber, err)
		}
	}

	content := []byte(`Company Name: HONEYS DATES & NUTS
Phone No: 6282078186

Purchase Return Report
Dated: 01/09/2024 - 20/09/2026

Date,Name,Transaction Type,Sr No.,Total Amount,Money In,Money Out,Balance Amount,Created By
16/10/2025,Vendor Co,Purchase Return,1,54.54,0.0,0.0,54.54,Admin
22/10/2025,Vendor Co,Purchase Return,2,99.65,0.0,0.0,99.65,Admin
29/10/2025,New Vendor,Purchase Return,3,2320.0,2320.0,0.0,0.0,Admin
10/11/2025,Vendor Co,Purchase Return,4,432.0,0.0,0.0,432.0,Admin
30/10/2025,Vendor Co,Purchase Return,9,10.0,0.0,0.0,10.0,Admin
11/11/2026,Acme,Sales Invoice,50,400.0,400.0,0.0,0.0,Admin
12/11/2026,Vendor Co,Payment-in,9,100.0,100.0,0.0,0.0,Admin
`)

	result, errs, err := importPurchaseReturnsRows(userID, content, nil, nil)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("import errors = %v, want none", errs)
	}
	if got := result["imported"].(int); got != 4 {
		t.Fatalf("imported = %d, want 4", got)
	}
	if got := result["skipped"].(int); got != 3 {
		t.Fatalf("skipped = %d, want 3 (serial 2 already migrated + 2 other types)", got)
	}

	// Returns landed as processed documents numbered PR-<Sr No.> padded to
	// the app's convention — suffixed when the base number is already taken
	// by an unrelated document (serial 1 → PR-0001-2 here).
	var pr1, pr3 models.PurchaseReturn
	if err := db.Where("user_id = ? AND return_number = ?", userID, "PR-0001-2").First(&pr1).Error; err != nil {
		t.Fatalf("load PR-0001-2: %v", err)
	}
	if pr1.Amount != 54.54 || pr1.Status != "processed" || pr1.PartyID != vendor.ID {
		t.Fatalf("PR-0001-2 = %+v, want processed/54.54/vendor", pr1)
	}
	if pr1.RefundMode != "credit_note" {
		t.Fatalf("PR-0001-2 refund_mode = %q, want credit_note (no money in)", pr1.RefundMode)
	}
	if err := db.Where("user_id = ? AND return_number = ?", userID, "PR-0003").First(&pr3).Error; err != nil {
		t.Fatalf("load PR-0003: %v", err)
	}
	if pr3.RefundMode != "cash" {
		t.Fatalf("PR-0003 refund_mode = %q, want cash (money in moved)", pr3.RefundMode)
	}
	// Unknown vendor was created on demand.
	var nv models.Party
	if err := db.Where("user_id = ? AND name = ?", userID, "New Vendor").First(&nv).Error; err != nil {
		t.Fatalf("load New Vendor party: %v", err)
	}
	if pr3.PartyID != nv.ID {
		t.Fatalf("PR-3 party = %v, want New Vendor %v", pr3.PartyID, nv.ID)
	}

	// Serial 2's row was already migrated under the padded scheme — the
	// existing PR-0002 stands, and no second document may appear.
	var dupN int64
	db.Model(&models.PurchaseReturn{}).
		Where("user_id = ? AND party_id = ? AND amount = ?", userID, vendor.ID, 99.65).
		Count(&dupN)
	if dupN != 1 {
		t.Fatalf("returns with amount 99.65 = %d, want exactly 1 (the seeded PR-0002)", dupN)
	}

	// Serial 9's number also belongs to an unrelated document, so the row
	// lands under the suffixed PR-0009-2.
	var pr9 models.PurchaseReturn
	if err := db.Where("user_id = ? AND return_number = ?", userID, "PR-0009-2").First(&pr9).Error; err != nil {
		t.Fatalf("load PR-0009-2: %v", err)
	}
	if pr9.Amount != 10 {
		t.Fatalf("PR-0009-2 amount = %v, want 10", pr9.Amount)
	}

	// One summary line item per return.
	var items []models.PurchaseReturnItem
	if err := db.Where("return_id = ?", pr1.ID).Find(&items).Error; err != nil {
		t.Fatalf("load items: %v", err)
	}
	if len(items) != 1 || items[0].Total != 54.54 || items[0].Quantity != 1 {
		t.Fatalf("PR-0001-2 items = %+v, want one summary item of 54.54", items)
	}

	// No money movement: no payments, no cash transactions, no balance drift.
	var payN, ctN int64
	db.Model(&models.Payment{}).Where("user_id = ?", userID).Count(&payN)
	db.Model(&models.CashTransaction{}).Where("user_id = ?", userID).Count(&ctN)
	if payN != 0 || ctN != 0 {
		t.Fatalf("payments=%d cash_txns=%d, want 0/0", payN, ctN)
	}
	var v models.Party
	if err := db.First(&v, "id = ?", vendor.ID).Error; err != nil {
		t.Fatalf("load vendor: %v", err)
	}
	if v.Balance != 0 {
		t.Fatalf("vendor balance = %v, want 0 (balances are managed separately)", v.Balance)
	}
	if nv.Balance != 0 {
		t.Fatalf("new vendor balance = %v, want 0", nv.Balance)
	}

	// Re-running the same file must be a complete no-op.
	result, errs, err = importPurchaseReturnsRows(userID, content, nil, nil)
	if err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("re-import errors = %v, want none", errs)
	}
	if got := result["imported"].(int); got != 0 {
		t.Fatalf("re-import imported = %d, want 0", got)
	}
	var retN int64
	db.Model(&models.PurchaseReturn{}).Where("user_id = ?", userID).Count(&retN)
	if retN != 7 {
		t.Fatalf("after re-import: returns=%d, want 7 (3 seeded + 4 imported)", retN)
	}
}
