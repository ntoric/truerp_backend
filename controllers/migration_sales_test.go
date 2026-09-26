package controllers

import (
	"strings"
	"testing"
	"time"

	"truerp/models"
	"truerp/utils"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openSalesImportTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:salesimport?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Party{},
		&models.Invoice{},
		&models.InvoiceItem{},
		&models.Payment{},
		&models.PaymentOut{},
		&models.PurchaseBill{},
		&models.PurchaseBillItem{},
		&models.Expense{},
		&models.ExpenseItem{},
		&models.ExpenseCategory{},
		&models.CashTransaction{},
		&models.BankAccount{},
		&models.PaymentMethodAccountMap{},
		&models.Warehouse{},
		&models.Account{},
		&models.JournalEntry{},
		&models.JournalEntryLine{},
		&models.Ledger{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// The sales summary import must update the stored invoice link on an
// already-imported invoice (matched prefix-agnostic) instead of re-creating
// it, and list unmatched rows without importing them until confirmed via
// importUnmatched.
func TestImportSalesRows_MatchAndUnmatched(t *testing.T) {
	db := openSalesImportTestDB(t)
	previousDB := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = previousDB })

	userID := uuid.New()

	customer := models.Party{ID: uuid.New(), UserID: userID, Name: "Acme", PartyType: "customer", IsActive: true}
	if err := db.Create(&customer).Error; err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	existing := models.Invoice{
		ID:            uuid.New(),
		UserID:        userID,
		InvoiceNumber: "INV-0001",
		InvoiceType:   "tax_invoice",
		PartyID:       customer.ID,
		Date:          time.Now(),
		Status:        "sent",
		TotalAmount:   5000,
		Notes:         "Created by: Admin",
	}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatalf("seed invoice: %v", err)
	}

	csv := "Invoice No,Invoice Date,Contact Name,Amount,Remaining Amount,Invoice Status,Due Date,Invoice Link,Payment Type,Party Category,Created by\n" +
		"1,01/09/2025,Acme,5000,0,Paid,15/09/2025,https://mybillbook.in/csi/abc123,Cash,Customer,Admin\n" +
		"2,02/09/2025,Other Customer,800,800,Unpaid,16/09/2025,https://mybillbook.in/csi/def456,Cash,Customer,Admin\n"

	res, errs, err := importSalesRows(userID, []byte(csv), false, nil)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("unexpected row errors: %v", errs)
	}
	if got := res["imported"].(int); got != 0 {
		t.Fatalf("imported = %d, want 0", got)
	}
	if got := res["updated"].(int); got != 1 {
		t.Fatalf("updated = %d, want 1", got)
	}
	unmatched, ok := res["unmatched"].([]unmatchedDocRow)
	if !ok || len(unmatched) != 1 || unmatched[0].Ref != "2" || unmatched[0].PartyName != "Other Customer" {
		t.Fatalf("unmatched = %+v, want row for Invoice No 2", res["unmatched"])
	}

	var updatedInv models.Invoice
	if err := db.Where("user_id = ? AND invoice_number = ?", userID, "INV-0001").First(&updatedInv).Error; err != nil {
		t.Fatalf("load invoice: %v", err)
	}
	if updatedInv.SourceURL != "https://mybillbook.in/csi/abc123" {
		t.Fatalf("source_url = %q, want invoice link", updatedInv.SourceURL)
	}
	if !strings.Contains(updatedInv.Notes, "Created by: Admin") {
		t.Fatalf("notes = %q, want preserved Created by segment", updatedInv.Notes)
	}

	// The unmatched row must not have been created yet.
	var count int64
	db.Model(&models.Invoice{}).Where("user_id = ?", userID).Count(&count)
	if count != 1 {
		t.Fatalf("invoice count = %d, want 1 (unmatched row must wait for confirmation)", count)
	}

	// Confirmed re-run imports the unmatched row.
	res, errs, err = importSalesRows(userID, []byte(csv), true, nil)
	if err != nil {
		t.Fatalf("confirmed import: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("unexpected row errors on confirm: %v", errs)
	}
	if got := res["imported"].(int); got != 1 {
		t.Fatalf("confirmed imported = %d, want 1", got)
	}
	if len(res["unmatched"].([]unmatchedDocRow)) != 0 {
		t.Fatalf("confirmed unmatched = %+v, want empty", res["unmatched"])
	}
	var created models.Invoice
	if err := db.Where("user_id = ? AND invoice_number = ?", userID, "2").First(&created).Error; err != nil {
		t.Fatalf("expected invoice 2 to be created: %v", err)
	}
	if created.SourceURL != "https://mybillbook.in/csi/def456" {
		t.Fatalf("created source_url = %q", created.SourceURL)
	}
}

// The links importer must fill source_url only on invoices that have none,
// match invoice numbers prefix-agnostic, never create invoices, and report
// unmatched numbers.
func TestImportSalesInvoiceLinkRows(t *testing.T) {
	db := openSalesImportTestDB(t)
	previousDB := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = previousDB })

	userID := uuid.New()

	customer := models.Party{ID: uuid.New(), UserID: userID, Name: "Acme", PartyType: "customer", IsActive: true}
	if err := db.Create(&customer).Error; err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	noLink := models.Invoice{
		ID: uuid.New(), UserID: userID, InvoiceNumber: "INV-0010",
		InvoiceType: "tax_invoice", PartyID: customer.ID, Date: time.Now(),
		Status: "sent", TotalAmount: 100,
	}
	hasLink := models.Invoice{
		ID: uuid.New(), UserID: userID, InvoiceNumber: "11",
		InvoiceType: "tax_invoice", PartyID: customer.ID, Date: time.Now(),
		Status: "sent", TotalAmount: 200, SourceURL: "https://mybillbook.in/csi/keepme",
	}
	if err := db.Create(&noLink).Error; err != nil {
		t.Fatalf("seed invoice: %v", err)
	}
	if err := db.Create(&hasLink).Error; err != nil {
		t.Fatalf("seed invoice: %v", err)
	}

	csv := "Invoice No,Invoice Date,Contact Name,Amount,Remaining Amount,Invoice Status,Due Date,Invoice Link,Payment Type,Party Category,Created by\n" +
		"10,01/09/2025,Acme,100,0,Paid,15/09/2025,https://mybillbook.in/csi/new10,Cash,Customer,Admin\n" +
		"11,01/09/2025,Acme,200,0,Paid,15/09/2025,https://mybillbook.in/csi/other11,Cash,Customer,Admin\n" +
		"99,01/09/2025,Nobody,50,50,Unpaid,15/09/2025,https://mybillbook.in/csi/miss99,Cash,Customer,Admin\n"

	res, errs, err := importSalesInvoiceLinkRows(userID, []byte(csv), nil)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if got := res["updated"].(int); got != 1 {
		t.Fatalf("updated = %d, want 1", got)
	}
	if got := res["skipped"].(int); got != 1 {
		t.Fatalf("skipped = %d, want 1 (existing link left unchanged)", got)
	}
	if len(errs) != 1 || !strings.Contains(errs[0], "99") {
		t.Fatalf("errs = %v, want one not-found error for Invoice No 99", errs)
	}

	var filled models.Invoice
	if err := db.Where("user_id = ? AND invoice_number = ?", userID, "INV-0010").First(&filled).Error; err != nil {
		t.Fatalf("load invoice: %v", err)
	}
	if filled.SourceURL != "https://mybillbook.in/csi/new10" {
		t.Fatalf("source_url = %q, want backfilled link", filled.SourceURL)
	}
	var kept models.Invoice
	if err := db.Where("user_id = ? AND invoice_number = ?", userID, "11").First(&kept).Error; err != nil {
		t.Fatalf("load invoice: %v", err)
	}
	if kept.SourceURL != "https://mybillbook.in/csi/keepme" {
		t.Fatalf("source_url overwritten: %q", kept.SourceURL)
	}
	var count int64
	db.Model(&models.Invoice{}).Where("user_id = ?", userID).Count(&count)
	if count != 2 {
		t.Fatalf("invoice count = %d, want 2 (nothing created)", count)
	}
}
