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

	content := []byte(`Company Name: HONEYS DATES & NUTS
Phone No: 6282078186

Purchase Return Report
Dated: 01/09/2024 - 20/09/2026

Date,Name,Transaction Type,Sr No.,Total Amount,Money In,Money Out,Balance Amount,Created By
16/10/2025,Vendor Co,Purchase Return,1,54.54,0.0,0.0,54.54,Admin
29/10/2025,New Vendor,Purchase Return,3,2320.0,2320.0,0.0,0.0,Admin
10/11/2025,Vendor Co,Purchase Return,4,432.0,0.0,0.0,432.0,Admin
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
	if got := result["imported"].(int); got != 3 {
		t.Fatalf("imported = %d, want 3", got)
	}
	if got := result["skipped"].(int); got != 2 {
		t.Fatalf("skipped = %d, want 2 (sales invoice + payment-in rows)", got)
	}

	// Returns landed as processed documents numbered PR-<Sr No.>.
	var pr1, pr3 models.PurchaseReturn
	if err := db.Where("user_id = ? AND return_number = ?", userID, "PR-0001").First(&pr1).Error; err != nil {
		t.Fatalf("load PR-0001: %v", err)
	}
	if pr1.Amount != 54.54 || pr1.Status != "processed" || pr1.PartyID != vendor.ID {
		t.Fatalf("PR-0001 = %+v, want processed/54.54/vendor", pr1)
	}
	if pr1.RefundMode != "credit_note" {
		t.Fatalf("PR-0001 refund_mode = %q, want credit_note (no money in)", pr1.RefundMode)
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
		t.Fatalf("PR-0003 party = %v, want New Vendor %v", pr3.PartyID, nv.ID)
	}

	// One summary line item per return.
	var items []models.PurchaseReturnItem
	if err := db.Where("return_id = ?", pr1.ID).Find(&items).Error; err != nil {
		t.Fatalf("load items: %v", err)
	}
	if len(items) != 1 || items[0].Total != 54.54 || items[0].Quantity != 1 {
		t.Fatalf("PR-0001 items = %+v, want one summary item of 54.54", items)
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
	if retN != 3 {
		t.Fatalf("after re-import: returns=%d, want 3", retN)
	}
}
