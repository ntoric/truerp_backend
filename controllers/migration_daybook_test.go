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

func openDaybookTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// Shared cache: handlers read through utils.DB while holding a tx on
	// another pooled connection — a private :memory: DB per connection would
	// hide seeded rows from those lookups.
	db, err := gorm.Open(sqlite.Open("file:daybook?mode=memory&cache=shared"), &gorm.Config{
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

// The daybook import must create every document type plus the cash/bank
// ledger entries, allocate payments to open documents oldest-first, and
// re-running the same file must be a no-op.
func TestImportDaybookRows(t *testing.T) {
	db := openDaybookTestDB(t)
	previousDB := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = previousDB })

	userID := uuid.New()

	party := models.Party{ID: uuid.New(), UserID: userID, Name: "Acme", PartyType: "customer", IsActive: true}
	vendor := models.Party{ID: uuid.New(), UserID: userID, Name: "Vendor Co", PartyType: "vendor", IsActive: true}
	if err := db.Create(&party).Error; err != nil {
		t.Fatalf("seed party: %v", err)
	}
	if err := db.Create(&vendor).Error; err != nil {
		t.Fatalf("seed vendor: %v", err)
	}

	// Two open invoices for FIFO payment-in allocation.
	inv100 := models.Invoice{
		ID: uuid.New(), UserID: userID, InvoiceNumber: "100", PartyID: party.ID,
		Date: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), Status: "sent",
		SubTotal: 500, TotalAmount: 500,
	}
	inv101 := models.Invoice{
		ID: uuid.New(), UserID: userID, InvoiceNumber: "101", PartyID: party.ID,
		Date: time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC), Status: "sent",
		SubTotal: 300, TotalAmount: 300,
	}
	if err := db.Create(&inv100).Error; err != nil {
		t.Fatalf("seed invoice 100: %v", err)
	}
	if err := db.Create(&inv101).Error; err != nil {
		t.Fatalf("seed invoice 101: %v", err)
	}
	// One open bill for FIFO payment-out allocation.
	bill5 := models.PurchaseBill{
		ID: uuid.New(), UserID: userID, PartyID: vendor.ID, BillNumber: "P-0005",
		BillDate: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), Status: "unpaid",
		TotalAmount: 800, BalanceDue: 800,
	}
	if err := db.Create(&bill5).Error; err != nil {
		t.Fatalf("seed bill: %v", err)
	}

	content := []byte(`Company Name: HONEYS DATES & NUTS
Phone No: 6282078186

Daybook Report
Dated: 01/09/2024 - 20/09/2026
Net Amount: 95110.74

Date,Name,Transaction Type,Sr No.,Total Amount,Money In,Money Out,Balance Amount,Created By
01/09/2026,Acme,Sales Invoice,50,400.0,400.0,0.0,0.0,Admin
02/09/2026,Acme,Sales Invoice,51,250.0,0.0,0.0,250.0,Admin
03/09/2026,Acme,Sales Invoice,52,900.0,0.0,0.0,900.0,Admin
03/09/2026,Vendor Co,Purchase Bill,7,1200.0,0.0,400.0,800.0,Admin
04/09/2026,Acme,Payment-in,1,800.0,800.0,0.0,0.0,Admin
05/09/2026,Acme,Payment-in,2,1000.0,1000.0,0.0,0.0,Admin
06/09/2026,New Customer,Payment-in,3,50.0,50.0,0.0,0.0,Admin
05/09/2026,Vendor Co,Payment-out,2,900.0,0.0,900.0,0.0,Admin
06/09/2026,Rent,Expense,1,4800.0,0.0,4800.0,0.0,Admin
07/09/2026,"",Add Money,1,5000.0,5000.0,0.0,0.0,Admin
08/09/2026,"",Reduce Money,2,600.0,0.0,600.0,0.0,Admin
09/09/2026,Acme,Sales Return,1,140.0,0.0,140.0,0.0,Admin
10/09/2026,Vendor Co,Purchase Return,3,110.0,110.0,0.0,0.0,Admin
11/09/2026,Vendor Co,Purchase Return,4,55.0,0.0,0.0,55.0,Admin
`)

	result, errs, err := importDaybookRows(userID, content, nil, nil)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("import errors = %v, want none", errs)
	}

	// 13 money rows imported; the credit-only purchase return is skipped.
	if got := result["imported"].(int); got != 13 {
		t.Fatalf("imported = %d, want 13", got)
	}
	if got := result["skipped"].(int); got != 1 {
		t.Fatalf("skipped = %d, want 1", got)
	}
	if got := result["sales_invoices"].(int); got != 3 {
		t.Fatalf("sales_invoices = %d, want 3", got)
	}
	if got := result["purchase_bills"].(int); got != 1 {
		t.Fatalf("purchase_bills = %d, want 1", got)
	}
	// payment_in: pin1 (2 linked) + pin2 (2 linked) + pin3 (1 unlinked)
	// + PR-3 = 6
	if got := result["payment_in"].(int); got != 6 {
		t.Fatalf("payment_in = %d, want 6", got)
	}
	// payment_out: pout2 (2 linked) + SR-1 = 3 (the bill's own POUT is
	// counted under purchase_bills).
	if got := result["payment_out"].(int); got != 3 {
		t.Fatalf("payment_out = %d, want 3", got)
	}
	if got := result["expenses"].(int); got != 1 {
		t.Fatalf("expenses = %d, want 1", got)
	}
	if got := result["cash_transactions"].(int); got != 2 {
		t.Fatalf("cash_transactions = %d, want 2", got)
	}

	// Invoice 50: created fully paid.
	var inv50 models.Invoice
	if err := db.Where("user_id = ? AND invoice_number = ?", userID, "50").First(&inv50).Error; err != nil {
		t.Fatalf("load invoice 50: %v", err)
	}
	if inv50.Status != "paid" || inv50.TotalAmount != 400 || inv50.AmountPaid != 400 {
		t.Fatalf("invoice 50 = %+v, want paid/400/400", inv50)
	}
	// Invoice 51: created unpaid, then settled by payment-in 2. Invoice 52
	// got only part of the remainder and stays partial.
	var inv51, inv52 models.Invoice
	if err := db.Where("user_id = ? AND invoice_number = ?", userID, "51").First(&inv51).Error; err != nil {
		t.Fatalf("load invoice 51: %v", err)
	}
	if err := db.Where("user_id = ? AND invoice_number = ?", userID, "52").First(&inv52).Error; err != nil {
		t.Fatalf("load invoice 52: %v", err)
	}
	if inv51.Status != "paid" || inv51.AmountPaid != 250 {
		t.Fatalf("invoice 51 = %+v, want paid/250", inv51)
	}
	if inv52.Status != "partial" || inv52.AmountPaid != 750 {
		t.Fatalf("invoice 52 = %+v, want partial/750", inv52)
	}
	// Payment-in 3 for a party with no open invoices lands unlinked.
	var pin3 []models.Payment
	if err := db.Where("user_id = ? AND payment_in_number = ?", userID, "3").Find(&pin3).Error; err != nil {
		t.Fatalf("load payment-in 3: %v", err)
	}
	if len(pin3) != 1 || pin3[0].InvoiceID != nil || pin3[0].AmountReceived != 50 {
		t.Fatalf("payment-in 3 = %+v, want one unlinked payment of 50", pin3)
	}
	// Payment-in 1 allocated 500 -> invoice 100 and 300 -> invoice 101.
	var inv1, inv2 models.Invoice
	if err := db.First(&inv1, "id = ?", inv100.ID).Error; err != nil {
		t.Fatalf("load invoice 100: %v", err)
	}
	if err := db.First(&inv2, "id = ?", inv101.ID).Error; err != nil {
		t.Fatalf("load invoice 101: %v", err)
	}
	if inv1.Status != "paid" || inv1.AmountPaid != 500 {
		t.Fatalf("invoice 100 = %+v, want paid/500", inv1)
	}
	if inv2.Status != "paid" || inv2.AmountPaid != 300 {
		t.Fatalf("invoice 101 = %+v, want paid/300", inv2)
	}

	// Bill P-0007: created partial (paid 400, due 800), then payment-out 2
	// allocated 800 to seeded P-0005 first and 100 to P-0007.
	var b5, b7 models.PurchaseBill
	if err := db.First(&b5, "id = ?", bill5.ID).Error; err != nil {
		t.Fatalf("load bill 5: %v", err)
	}
	if err := db.Where("user_id = ? AND bill_number = ?", userID, "P-0007").First(&b7).Error; err != nil {
		t.Fatalf("load bill 7: %v", err)
	}
	if b5.Status != "paid" || b5.PaidAmount != 800 || b5.BalanceDue != 0 {
		t.Fatalf("bill 5 = %+v, want paid/800/0", b5)
	}
	if b7.Status != "partial" || b7.PaidAmount != 500 || b7.BalanceDue != 700 {
		t.Fatalf("bill 7 = %+v, want partial/500/700", b7)
	}
	// The bill's own paid portion produced a linked payment-out row.
	var billPOs []models.PaymentOut
	if err := db.Where("user_id = ? AND purchase_bill_id = ?", userID, b7.ID).Find(&billPOs).Error; err != nil {
		t.Fatalf("load bill 7 payments: %v", err)
	}
	if len(billPOs) != 2 {
		t.Fatalf("bill 7 payment-outs = %d, want 2 (400 at purchase + 100 allocated)", len(billPOs))
	}

	// Expense created under a "Rent" category with the cash entry.
	var exp models.Expense
	if err := db.Where("user_id = ? AND expense_number = ?", userID, "EXP-1").First(&exp).Error; err != nil {
		t.Fatalf("load EXP-1: %v", err)
	}
	if exp.Amount != 4800 || exp.Category != "Rent" {
		t.Fatalf("EXP-1 = %+v, want 4800/Rent", exp)
	}

	// Manual cash rows land in cash in hand, unlinked.
	var addTxn models.CashTransaction
	if err := db.Where("user_id = ? AND reference = ? AND transaction_type = 'add'", userID, "CB-ADD-1").First(&addTxn).Error; err != nil {
		t.Fatalf("load CB-ADD-1: %v", err)
	}
	if addTxn.Amount != 5000 || addTxn.IsLinked || addTxn.AccountID != nil {
		t.Fatalf("CB-ADD-1 = %+v, want unlinked 5000 cash-in-hand add", addTxn)
	}

	// Return refunds landed in the payment tables.
	var sr []models.PaymentOut
	if err := db.Where("user_id = ? AND payment_out_number = ?", userID, "SR-1").Find(&sr).Error; err != nil {
		t.Fatalf("load SR-1: %v", err)
	}
	if len(sr) != 1 || sr[0].AmountPaid != 140 {
		t.Fatalf("SR-1 = %+v, want one payment-out of 140", sr)
	}
	var pr []models.Payment
	if err := db.Where("user_id = ? AND payment_in_number = ?", userID, "PR-3").Find(&pr).Error; err != nil {
		t.Fatalf("load PR-3: %v", err)
	}
	if len(pr) != 1 || pr[0].AmountReceived != 110 {
		t.Fatalf("PR-3 = %+v, want one payment-in of 110", pr)
	}

	// Cash ledger: adds = invoice 50 (400) + pin1 (500+300) + pin2 (250+750)
	// + pin3 (50) + PR-3 (110) + add-money (5000) = 8.
	// reduce = bill 7 purchase payment (400) + pout2 (800+100) + SR-1 (140)
	// + reduce-money (600) = 5. expense = 1.
	var addN, reduceN, expenseN int64
	db.Model(&models.CashTransaction{}).Where("user_id = ? AND transaction_type = 'add'", userID).Count(&addN)
	db.Model(&models.CashTransaction{}).Where("user_id = ? AND transaction_type = 'reduce'", userID).Count(&reduceN)
	db.Model(&models.CashTransaction{}).Where("user_id = ? AND transaction_type = 'expense'", userID).Count(&expenseN)
	if addN != 8 || reduceN != 5 || expenseN != 1 {
		t.Fatalf("cash txns add=%d reduce=%d expense=%d, want 8/5/1", addN, reduceN, expenseN)
	}

	// Party balances: Acme -= paid-to-us amounts and += refund paid back.
	var acme models.Party
	if err := db.First(&acme, "id = ?", party.ID).Error; err != nil {
		t.Fatalf("load party: %v", err)
	}
	if acme.Balance != -2060 {
		t.Fatalf("Acme balance = %v, want -2060", acme.Balance)
	}

	// Re-running the same file must be a complete no-op.
	result, errs, err = importDaybookRows(userID, content, nil, nil)
	if err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("re-import errors = %v, want none", errs)
	}
	if got := result["imported"].(int); got != 0 {
		t.Fatalf("re-import imported = %d, want 0", got)
	}
	var invN, payN, poN, billN, expN, ctN int64
	db.Model(&models.Invoice{}).Where("user_id = ?", userID).Count(&invN)
	db.Model(&models.Payment{}).Where("user_id = ?", userID).Count(&payN)
	db.Model(&models.PaymentOut{}).Where("user_id = ?", userID).Count(&poN)
	db.Model(&models.PurchaseBill{}).Where("user_id = ?", userID).Count(&billN)
	db.Model(&models.Expense{}).Where("user_id = ?", userID).Count(&expN)
	db.Model(&models.CashTransaction{}).Where("user_id = ?", userID).Count(&ctN)
	if invN != 5 || payN != 7 || poN != 4 || billN != 2 || expN != 1 || ctN != 14 {
		t.Fatalf("after re-import: invoices=%d payments=%d payment_outs=%d bills=%d expenses=%d cash_txns=%d, want 5/7/4/2/1/14",
			invN, payN, poN, billN, expN, ctN)
	}
}
