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

func openSalesReturnTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// Shared cache so reads through utils.DB see rows written on other
	// pooled connections.
	db, err := gorm.Open(sqlite.Open("file:salesreturn?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Party{},
		&models.SalesReturn{},
		&models.SalesReturnItem{},
		&models.Payment{},
		&models.PaymentOut{},
		&models.CashTransaction{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// The sales return import must create one processed SalesReturn per
// "Sales Return" row, skip other transaction types, never touch payments,
// cash transactions or party balances, and be a no-op on re-run.
func TestImportSalesReturnsRows(t *testing.T) {
	db := openSalesReturnTestDB(t)
	previousDB := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = previousDB })

	userID := uuid.New()

	customer := models.Party{ID: uuid.New(), UserID: userID, Name: "KARTHIK", PartyType: "customer", IsActive: true}
	if err := db.Create(&customer).Error; err != nil {
		t.Fatalf("seed customer: %v", err)
	}

	content := []byte(`Company Name: HONEYS DATES & NUTS
Phone No: 6282078186

Sales Return Report
Dated: 01/09/2024 - 20/09/2026

Date,Name,Transaction Type,Sr No.,Total Amount,Money In,Money Out,Balance Amount,Created By
16/10/2025,KARTHIK,Sales Return,1,1080.0,0.0,1080.0,0.0,Honeys dats&nuts
29/10/2025,SHANKAR,Sales Return,2,260.0,0.0,0.0,260.0,Honeys dats&nuts
05/11/2025,Cash Sale,Sales Return,3,180.0,0.0,180.0,0.0,Honeys dats&nuts
11/11/2026,Acme,Sales Invoice,50,400.0,400.0,0.0,0.0,Admin
12/11/2026,KARTHIK,Payment-out,9,100.0,0.0,100.0,0.0,Admin
`)

	result, errs, err := importSalesReturnsRows(userID, content, nil, nil)
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
		t.Fatalf("skipped = %d, want 2 (sales invoice + payment-out rows)", got)
	}

	// Returns landed as processed documents numbered SR-<Sr No.>.
	var sr1, sr2 models.SalesReturn
	if err := db.Where("user_id = ? AND return_number = ?", userID, "SR-0001").First(&sr1).Error; err != nil {
		t.Fatalf("load SR-0001: %v", err)
	}
	if sr1.Amount != 1080.0 || sr1.Status != "processed" || sr1.PartyID != customer.ID {
		t.Fatalf("SR-0001 = %+v, want processed/1080.0/customer", sr1)
	}
	if sr1.RefundMode != "cash" {
		t.Fatalf("SR-0001 refund_mode = %q, want cash (money out moved)", sr1.RefundMode)
	}
	if err := db.Where("user_id = ? AND return_number = ?", userID, "SR-0002").First(&sr2).Error; err != nil {
		t.Fatalf("load SR-0002: %v", err)
	}
	if sr2.RefundMode != "credit_note" {
		t.Fatalf("SR-0002 refund_mode = %q, want credit_note (no money out)", sr2.RefundMode)
	}
	// Unknown customer was created on demand.
	var shankar models.Party
	if err := db.Where("user_id = ? AND name = ?", userID, "SHANKAR").First(&shankar).Error; err != nil {
		t.Fatalf("load SHANKAR party: %v", err)
	}
	if shankar.PartyType != "customer" {
		t.Fatalf("SHANKAR party_type = %q, want customer", shankar.PartyType)
	}
	if sr2.PartyID != shankar.ID {
		t.Fatalf("SR-0002 party = %v, want SHANKAR %v", sr2.PartyID, shankar.ID)
	}

	// One summary line item per return.
	var items []models.SalesReturnItem
	if err := db.Where("return_id = ?", sr1.ID).Find(&items).Error; err != nil {
		t.Fatalf("load items: %v", err)
	}
	if len(items) != 1 || items[0].Total != 1080.0 || items[0].Quantity != 1 {
		t.Fatalf("SR-0001 items = %+v, want one summary item of 1080.0", items)
	}

	// No money movement: no payments, no payment-outs, no cash transactions,
	// no balance drift.
	var pinN, poutN, ctN int64
	db.Model(&models.Payment{}).Where("user_id = ?", userID).Count(&pinN)
	db.Model(&models.PaymentOut{}).Where("user_id = ?", userID).Count(&poutN)
	db.Model(&models.CashTransaction{}).Where("user_id = ?", userID).Count(&ctN)
	if pinN != 0 || poutN != 0 || ctN != 0 {
		t.Fatalf("payments=%d payment_outs=%d cash_txns=%d, want 0/0/0", pinN, poutN, ctN)
	}
	var c models.Party
	if err := db.First(&c, "id = ?", customer.ID).Error; err != nil {
		t.Fatalf("load customer: %v", err)
	}
	if c.Balance != 0 {
		t.Fatalf("customer balance = %v, want 0 (balances are managed separately)", c.Balance)
	}
	if shankar.Balance != 0 {
		t.Fatalf("new customer balance = %v, want 0", shankar.Balance)
	}

	// Re-running the same file must be a complete no-op.
	result, errs, err = importSalesReturnsRows(userID, content, nil, nil)
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
	db.Model(&models.SalesReturn{}).Where("user_id = ?", userID).Count(&retN)
	if retN != 3 {
		t.Fatalf("after re-import: returns=%d, want 3", retN)
	}
}
