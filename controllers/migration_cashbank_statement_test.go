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

func openCashBankStatementTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// Shared cache: handlers read through utils.DB while holding a tx on
	// another pooled connection — a private :memory: DB per connection would
	// hide seeded rows from those lookups.
	db, err := gorm.Open(sqlite.Open("file:cashbankstmt?mode=memory&cache=shared"), &gorm.Config{
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
		&models.Expense{},
		&models.ExpenseItem{},
		&models.CashTransaction{},
		&models.BankAccount{},
		&models.PaymentMethodAccountMap{},
		&models.Account{},
		&models.JournalEntry{},
		&models.JournalEntryLine{},
		&models.Ledger{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// The statement import must create every document type plus the cash/bank
// ledger entries, and re-running the same file must be a no-op.
func TestImportCashBankStatementRows(t *testing.T) {
	db := openCashBankStatementTestDB(t)
	previousDB := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = previousDB })

	userID := uuid.New()

	// Pre-existing records the statement rows should link to.
	party := models.Party{ID: uuid.New(), UserID: userID, Name: "Acme", PartyType: "customer", IsActive: true}
	vendor := models.Party{ID: uuid.New(), UserID: userID, Name: "Vendor Co", PartyType: "vendor", IsActive: true}
	if err := db.Create(&party).Error; err != nil {
		t.Fatalf("seed party: %v", err)
	}
	if err := db.Create(&vendor).Error; err != nil {
		t.Fatalf("seed vendor: %v", err)
	}

	// Unpaid invoice 5000 (total 1000, 200 already paid).
	inv := models.Invoice{
		ID: uuid.New(), UserID: userID, InvoiceNumber: "5000", PartyID: party.ID,
		Date: time.Now(), Status: "partial", SubTotal: 1000, TotalAmount: 1000, AmountPaid: 200,
	}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatalf("seed invoice: %v", err)
	}
	// Fully-paid invoice 5001 (should be skipped by the sale row).
	paidInv := models.Invoice{
		ID: uuid.New(), UserID: userID, InvoiceNumber: "5001", PartyID: party.ID,
		Date: time.Now(), Status: "paid", SubTotal: 300, TotalAmount: 300, AmountPaid: 300,
	}
	if err := db.Create(&paidInv).Error; err != nil {
		t.Fatalf("seed paid invoice: %v", err)
	}
	// Two unpaid purchase bills for payment-out allocation.
	billA := models.PurchaseBill{
		ID: uuid.New(), UserID: userID, PartyID: vendor.ID, BillNumber: "P-0007",
		BillDate: time.Now(), Status: "unpaid", TotalAmount: 500, BalanceDue: 500,
	}
	billB := models.PurchaseBill{
		ID: uuid.New(), UserID: userID, PartyID: vendor.ID, BillNumber: "P-0008",
		BillDate: time.Now(), Status: "unpaid", TotalAmount: 300, BalanceDue: 300,
	}
	if err := db.Create(&billA).Error; err != nil {
		t.Fatalf("seed bill A: %v", err)
	}
	if err := db.Create(&billB).Error; err != nil {
		t.Fatalf("seed bill B: %v", err)
	}
	// An already-imported expense (as the expenses importer numbers it).
	exp := models.Expense{
		ID: uuid.New(), UserID: userID, ExpenseNumber: "EXP-55",
		Category: "General", Description: "Raw Material", Amount: 100, SubTotal: 100,
		Date: time.Now(), PaymentMode: "cash",
	}
	if err := db.Create(&exp).Error; err != nil {
		t.Fatalf("seed expense: %v", err)
	}
	// Primary bank account so non-cash modes route somewhere.
	bank := models.BankAccount{
		ID: uuid.New(), UserID: userID, AccountName: "Main", AccountNumber: "1",
		BankName: "Bank", AccountType: "current", IsActive: true, IsPrimary: true,
	}
	if err := db.Create(&bank).Error; err != nil {
		t.Fatalf("seed bank account: %v", err)
	}

	content := []byte(`Date,Type,Txn No,Party,Invoice numbers,Mode,Paid,Received,Balance,Notes
20/09/2026,Sales Invoice,5000,Acme,"",Cash,0,800.0,-,""
20/09/2026,Sales Invoice,5001,Acme,"",Cash,0,300.0,-,""
20/09/2026,Sales Invoice,1001,Cash Sale,"",Cash,0,300.0,-,""
20/09/2026,Sales Invoice,1002,UPI SALE,"",Upi,0,500.0,-,""
20/09/2026,Payment-in,10,Acme,5000,Cash,0,180.0,-,""
21/09/2026,Payment-in,11,Acme,9999,Upi,0,150.0,-,""
18/09/2026,Payment-out,20,Vendor Co,"7, 8",Cash,700.0,0,-,""
18/09/2026,Purchase Bill,42,Vendor Co,"",Upi,900.0,0,-,""
18/09/2026,Purchase Bill,20,Vendor Co,"",Cash,60.0,0,-,""
20/09/2026,Expense,55,"",,Cash,100.0,0,-,""
20/09/2026,Expense,56,"",,Cash,250.0,0,-,""
01/09/2026,Add Money,1,"",,Cash,0,5000.0,-,Opening cash
01/09/2026,Reduce Money,2,"",,Bank,1000.0,0,-,Deposit
05/07/2026,Sales Return,3,Cash Sale,"",Cash,140.0,0,-,""
05/01/2026,Purchase Return,4,Vendor Co,"",Cash,0,110.0,-,""
,Opening Balance,,,,,,-,
`)

	result, errs, err := importCashBankStatementRows(userID, content, map[string]string{}, nil)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("import errors = %v, want none", errs)
	}

	// 14 money rows imported; opening balance + already-paid invoice skipped.
	if got := result["imported"].(int); got != 14 {
		t.Fatalf("imported = %d, want 14", got)
	}
	if got := result["skipped"].(int); got != 2 {
		t.Fatalf("skipped = %d, want 2", got)
	}
	if got := result["sales_invoices"].(int); got != 3 {
		t.Fatalf("sales_invoices = %d, want 3", got)
	}

	// Invoice 5000 topped up and marked paid.
	var inv5000 models.Invoice
	if err := db.Where("user_id = ? AND invoice_number = ?", userID, "5000").First(&inv5000).Error; err != nil {
		t.Fatalf("load invoice 5000: %v", err)
	}
	if inv5000.Status != "paid" || inv5000.AmountPaid != 1000 {
		t.Fatalf("invoice 5000 = %+v, want paid/1000", inv5000)
	}
	// Invoice 1002 created paid with the bank account attached.
	var inv1002 models.Invoice
	if err := db.Where("user_id = ? AND invoice_number = ?", userID, "1002").First(&inv1002).Error; err != nil {
		t.Fatalf("load invoice 1002: %v", err)
	}
	if inv1002.Status != "paid" || inv1002.TotalAmount != 500 || inv1002.BankAccountID == nil || *inv1002.BankAccountID != bank.ID {
		t.Fatalf("invoice 1002 = %+v, want paid/500/bank", inv1002)
	}

	// Payment-in 10: 180 linked to invoice 5000 — but the invoice was already
	// topped up by its sale row, so the full 180 lands as an unlinked payment.
	var p10 []models.Payment
	if err := db.Where("user_id = ? AND payment_in_number = ?", userID, "10").Find(&p10).Error; err != nil {
		t.Fatalf("load payment 10: %v", err)
	}
	if len(p10) != 1 || p10[0].AmountReceived != 180 {
		t.Fatalf("payment 10 = %+v, want one unlinked payment of 180", p10)
	}

	// Payment-out 20 allocated 500 -> P-0007 (paid) and 200 -> P-0008 (partial).
	var bA, bB models.PurchaseBill
	if err := db.First(&bA, "id = ?", billA.ID).Error; err != nil {
		t.Fatalf("load bill A: %v", err)
	}
	if err := db.First(&bB, "id = ?", billB.ID).Error; err != nil {
		t.Fatalf("load bill B: %v", err)
	}
	if bA.Status != "paid" || bA.PaidAmount != 500 || bA.BalanceDue != 0 {
		t.Fatalf("bill A = %+v, want paid/500/0", bA)
	}
	if bB.Status != "partial" || bB.PaidAmount != 200 || bB.BalanceDue != 100 {
		t.Fatalf("bill B = %+v, want partial/200/100", bB)
	}

	// Purchase Bill rows get their own "PB-" payment-out numbering space —
	// statement txn numbers are a per-type serial, so Purchase Bill 20 must
	// not be deduped against Payment-out 20.
	var po42 []models.PaymentOut
	if err := db.Where("user_id = ? AND payment_out_number = ?", userID, "PB-42").Find(&po42).Error; err != nil {
		t.Fatalf("load payment-out PB-42: %v", err)
	}
	if len(po42) != 1 || po42[0].PurchaseBillID != nil || po42[0].AmountPaid != 900 {
		t.Fatalf("payment-out PB-42 = %+v, want one unlinked 900", po42)
	}
	var poPB20 []models.PaymentOut
	if err := db.Where("user_id = ? AND payment_out_number = ?", userID, "PB-20").Find(&poPB20).Error; err != nil {
		t.Fatalf("load payment-out PB-20: %v", err)
	}
	if len(poPB20) != 1 || poPB20[0].AmountPaid != 60 {
		t.Fatalf("payment-out PB-20 = %+v, want one unlinked 60", poPB20)
	}

	// Expense 55 gained a cash entry without a new expense row; 56 was created.
	var expCount int64
	db.Model(&models.Expense{}).Where("user_id = ?", userID).Count(&expCount)
	if expCount != 2 {
		t.Fatalf("expenses = %d, want 2", expCount)
	}
	var exp56 models.Expense
	if err := db.Where("user_id = ? AND expense_number = ?", userID, "EXP-56").First(&exp56).Error; err != nil {
		t.Fatalf("load EXP-56: %v", err)
	}
	if exp56.Amount != 250 || exp56.Category != "General" {
		t.Fatalf("EXP-56 = %+v", exp56)
	}

	// Cash ledger: sale adds (3: 5000 top-up + 1001 + 1002), payment-in adds
	// (180 + 150), purchase-return add (110), add-money (5000) = 7 "add".
	// reduce: payment-out 20 (2 splits) + bills PB-42/PB-20 + reduce-money +
	// sales return = 6. expense type rows = 2.
	var addN, reduceN, expenseN, payrollN int64
	db.Model(&models.CashTransaction{}).Where("user_id = ? AND transaction_type = 'add'", userID).Count(&addN)
	db.Model(&models.CashTransaction{}).Where("user_id = ? AND transaction_type = 'reduce'", userID).Count(&reduceN)
	db.Model(&models.CashTransaction{}).Where("user_id = ? AND transaction_type = 'expense'", userID).Count(&expenseN)
	db.Model(&models.CashTransaction{}).Where("user_id = ? AND transaction_type = 'payroll'", userID).Count(&payrollN)
	if addN != 7 || reduceN != 6 || expenseN != 2 || payrollN != 0 {
		t.Fatalf("cash txns add=%d reduce=%d expense=%d payroll=%d, want 7/6/2/0", addN, reduceN, expenseN, payrollN)
	}

	// UPI sale row routed to the primary bank account; cash rows stay in hand.
	var upiTxn models.CashTransaction
	if err := db.Where("user_id = ? AND reference = ? AND transaction_type = 'add'", userID, "1002").First(&upiTxn).Error; err != nil {
		t.Fatalf("load upi txn: %v", err)
	}
	if upiTxn.AccountID == nil || *upiTxn.AccountID != bank.ID {
		t.Fatalf("upi txn account = %v, want %v", upiTxn.AccountID, bank.ID)
	}
	var cashTxn models.CashTransaction
	if err := db.Where("user_id = ? AND reference = ? AND transaction_type = 'add'", userID, "1001").First(&cashTxn).Error; err != nil {
		t.Fatalf("load cash txn: %v", err)
	}
	if cashTxn.AccountID != nil {
		t.Fatalf("cash txn account = %v, want nil (cash in hand)", cashTxn.AccountID)
	}

	// Bank balance: +500 (UPI sale) +150 (UPI payment-in) -900 (UPI purchase
	// bill) -1000 (reduce money, mode Bank) = -1250.
	var acct models.BankAccount
	if err := db.First(&acct, "id = ?", bank.ID).Error; err != nil {
		t.Fatalf("load bank: %v", err)
	}
	if acct.Balance != -1250 {
		t.Fatalf("bank balance = %v, want -1250", acct.Balance)
	}

	// Party balances follow app conventions: a created paid invoice nets to
	// zero (sale +total, payment -total), vendor payments add paid.
	var upiSale, vendorCo models.Party
	if err := db.Where("user_id = ? AND name = ?", userID, "UPI SALE").First(&upiSale).Error; err != nil {
		t.Fatalf("load UPI SALE party: %v", err)
	}
	if upiSale.Balance != 0 {
		t.Fatalf("UPI SALE balance = %v, want 0 (paid invoice)", upiSale.Balance)
	}
	if err := db.First(&vendorCo, "id = ?", vendor.ID).Error; err != nil {
		t.Fatalf("load Vendor Co: %v", err)
	}
	if vendorCo.Balance != 1550 {
		t.Fatalf("Vendor Co balance = %v, want 1550 (700+900+60 paid, -110 return)", vendorCo.Balance)
	}

	// Re-running the same file must be a complete no-op.
	result, errs, err = importCashBankStatementRows(userID, content, map[string]string{}, nil)
	if err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("re-import errors = %v, want none", errs)
	}
	if got := result["imported"].(int); got != 0 {
		t.Fatalf("re-import imported = %d, want 0", got)
	}
	var invN, payN, poN, ctN, expN int64
	db.Model(&models.Invoice{}).Where("user_id = ?", userID).Count(&invN)
	db.Model(&models.Payment{}).Where("user_id = ?", userID).Count(&payN)
	db.Model(&models.PaymentOut{}).Where("user_id = ?", userID).Count(&poN)
	db.Model(&models.CashTransaction{}).Where("user_id = ?", userID).Count(&ctN)
	db.Model(&models.Expense{}).Where("user_id = ?", userID).Count(&expN)
	if invN != 4 || payN != 6 || poN != 5 || ctN != 15 || expN != 2 {
		t.Fatalf("after re-import: invoices=%d payments=%d payment_outs=%d cash_txns=%d expenses=%d, want 4/6/5/15/2",
			invN, payN, poN, ctN, expN)
	}
}
