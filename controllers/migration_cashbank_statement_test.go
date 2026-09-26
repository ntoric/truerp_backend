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

func openCashBankStatementTestDB(t *testing.T, name string) *gorm.DB {
	t.Helper()
	// Shared cache: handlers read through utils.DB while holding a tx on
	// another pooled connection — a private :memory: DB per connection would
	// hide seeded rows from those lookups.
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{
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

const daybookFixture = `Company Name: HONEYS DATES & NUTS
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
`

func seedStatementBase(t *testing.T, db *gorm.DB) (uuid.UUID, models.BankAccount) {
	t.Helper()
	userID := uuid.New()

	party := models.Party{ID: uuid.New(), UserID: userID, Name: "Acme", PartyType: "customer", IsActive: true}
	vendor := models.Party{ID: uuid.New(), UserID: userID, Name: "Vendor Co", PartyType: "vendor", IsActive: true}
	if err := db.Create(&party).Error; err != nil {
		t.Fatalf("seed party: %v", err)
	}
	if err := db.Create(&vendor).Error; err != nil {
		t.Fatalf("seed vendor: %v", err)
	}

	// Two open invoices for FIFO payment-in allocation and one open bill.
	for _, inv := range []models.Invoice{
		{ID: uuid.New(), UserID: userID, InvoiceNumber: "100", PartyID: party.ID,
			Date: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), Status: "sent", SubTotal: 500, TotalAmount: 500},
		{ID: uuid.New(), UserID: userID, InvoiceNumber: "101", PartyID: party.ID,
			Date: time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC), Status: "sent", SubTotal: 300, TotalAmount: 300},
	} {
		if err := db.Create(&inv).Error; err != nil {
			t.Fatalf("seed invoice %s: %v", inv.InvoiceNumber, err)
		}
	}
	bill5 := models.PurchaseBill{
		ID: uuid.New(), UserID: userID, PartyID: vendor.ID, BillNumber: "P-0005",
		BillDate: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), Status: "unpaid",
		TotalAmount: 800, BalanceDue: 800,
	}
	if err := db.Create(&bill5).Error; err != nil {
		t.Fatalf("seed bill: %v", err)
	}

	// Transactions imported by the daybook (all transactions) pass — every
	// money movement lands in cash in hand because the export has no Mode
	// column.
	if _, errs, err := importDaybookRows(userID, []byte(daybookFixture), nil, nil); err != nil || len(errs) != 0 {
		t.Fatalf("daybook import: err=%v errs=%v", err, errs)
	}

	bank := models.BankAccount{
		ID: uuid.New(), UserID: userID, AccountName: "Main", AccountNumber: "1",
		BankName: "Bank", AccountType: "current", IsActive: true, IsPrimary: true,
	}
	if err := db.Create(&bank).Error; err != nil {
		t.Fatalf("seed bank account: %v", err)
	}
	// Payment-method mapping: UPI -> Main bank account.
	if err := db.Create(&models.PaymentMethodAccountMap{
		ID: uuid.New(), UserID: userID, PaymentMethod: "upi", BankAccountID: &bank.ID,
	}).Error; err != nil {
		t.Fatalf("seed upi mapping: %v", err)
	}
	return userID, bank
}

// The statement import re-modes already-imported transactions: non-cash rows
// move to the account mapped for the payment method, cash rows stay in hand,
// unmatched rows warn, and re-running the same file is a no-op.
func TestImportCashBankStatementRows(t *testing.T) {
	db := openCashBankStatementTestDB(t, "cashbankstmt_main")
	previousDB := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = previousDB })

	userID, bank := seedStatementBase(t, db)

	content := []byte(`Date,Type,Txn No,Party,Invoice numbers,Mode,Paid,Received,Balance,Notes
01/09/2026,Sales Invoice,50,Acme,"",Upi,0,400.0,-,""
02/09/2026,Sales Invoice,51,Acme,"",Cash,0,0.0,-,""
03/09/2026,Purchase Bill,7,Vendor Co,"",Upi,400.0,0,-,""
04/09/2026,Payment-in,1,Acme,"100, 101",Cash,0,800.0,-,""
05/09/2026,Payment-in,2,Acme,"",Upi,0,1000.0,-,""
06/09/2026,Payment-in,3,New Customer,"",Upi,0,50.0,-,""
05/09/2026,Payment-out,2,Vendor Co,"5, 7",Upi,900.0,0,-,""
06/09/2026,Expense,1,"",,Upi,4800.0,0,-,""
07/09/2026,Add Money,1,"",,Cash,0,5000.0,-,Opening cash
08/09/2026,Reduce Money,2,"",,Upi,600.0,0,-,Deposit
09/09/2026,Sales Return,1,Acme,"",Cash,140.0,0,-,""
10/09/2026,Purchase Return,3,Vendor Co,"",Upi,0,110.0,-,""
12/09/2026,Sales Invoice,9999,Ghost,"",Cash,0,50.0,-,""
,Opening Balance,,,,,,-,
`)

	result, errs, err := importCashBankStatementRows(userID, content, nil, nil)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(errs) != 1 || !strings.Contains(errs[0], "9999") {
		t.Fatalf("import errors = %v, want one unmatched-row warning for 9999", errs)
	}

	// 8 rows updated; unchanged rows (cash payment-in 1, cash add-money,
	// cash sales-return), the zero-amount invoice row and the opening
	// balance row are skipped.
	if got := result["imported"].(int); got != 8 {
		t.Fatalf("imported = %d, want 8", got)
	}
	if got := result["skipped"].(int); got != 5 {
		t.Fatalf("skipped = %d, want 5", got)
	}

	// Invoice 50 -> UPI on the mapped bank account.
	var inv50 models.Invoice
	if err := db.Where("user_id = ? AND invoice_number = ?", userID, "50").First(&inv50).Error; err != nil {
		t.Fatalf("load invoice 50: %v", err)
	}
	if inv50.PaymentMode != "upi" || inv50.BankAccountID == nil || *inv50.BankAccountID != bank.ID {
		t.Fatalf("invoice 50 = mode %s account %v, want upi/%v", inv50.PaymentMode, inv50.BankAccountID, bank.ID)
	}
	// Its sale-time payment and cash transaction moved too.
	var pin50 []models.Payment
	if err := db.Where("user_id = ? AND invoice_id = ?", userID, inv50.ID).Find(&pin50).Error; err != nil {
		t.Fatalf("load inv50 payments: %v", err)
	}
	if len(pin50) != 1 || pin50[0].Mode != "upi" {
		t.Fatalf("invoice 50 payments = %+v, want one upi payment", pin50)
	}
	var saleTxn models.CashTransaction
	if err := db.Where("user_id = ? AND description = ? AND transaction_type = 'add'", userID, "Sales invoice 50").First(&saleTxn).Error; err != nil {
		t.Fatalf("load sale txn: %v", err)
	}
	if saleTxn.AccountID == nil || *saleTxn.AccountID != bank.ID {
		t.Fatalf("sale txn account = %v, want %v", saleTxn.AccountID, bank.ID)
	}

	// Bill P-0007 -> UPI; the bill-time POUT payment moved, the allocated
	// payment-out "2" is re-moded by its own row.
	var bill7 models.PurchaseBill
	if err := db.Where("user_id = ? AND bill_number = ?", userID, "P-0007").First(&bill7).Error; err != nil {
		t.Fatalf("load bill 7: %v", err)
	}
	if bill7.PaymentMode != "upi" || bill7.BankAccountID == nil || *bill7.BankAccountID != bank.ID {
		t.Fatalf("bill 7 = mode %s account %v, want upi/%v", bill7.PaymentMode, bill7.BankAccountID, bank.ID)
	}
	var billPOs []models.PaymentOut
	if err := db.Where("user_id = ? AND purchase_bill_id = ?", userID, bill7.ID).Find(&billPOs).Error; err != nil {
		t.Fatalf("load bill payments: %v", err)
	}
	if len(billPOs) != 2 {
		t.Fatalf("bill 7 payment-outs = %d, want 2", len(billPOs))
	}
	for _, po := range billPOs {
		if po.Mode != "upi" {
			t.Fatalf("bill 7 payment-out %s mode = %s, want upi", po.PaymentOutNumber, po.Mode)
		}
	}

	// Payment-in 3 (unlinked) -> UPI, including its standalone GL leg.
	var pin3 []models.Payment
	if err := db.Where("user_id = ? AND payment_in_number = ?", userID, "3").Find(&pin3).Error; err != nil {
		t.Fatalf("load payment-in 3: %v", err)
	}
	if len(pin3) != 1 || pin3[0].Mode != "upi" {
		t.Fatalf("payment-in 3 = %+v, want upi", pin3)
	}
	var bankGL models.Account
	if err := db.Where("user_id = ? AND code = ?", userID, acCodeBank).First(&bankGL).Error; err != nil {
		t.Fatalf("load bank GL account: %v", err)
	}
	var recLedger []models.Ledger
	if err := db.Where("user_id = ? AND transaction_type = ? AND reference_id = ?",
		userID, "payment_in_record", pin3[0].ID).Find(&recLedger).Error; err != nil {
		t.Fatalf("load payment_in_record ledger: %v", err)
	}
	if len(recLedger) != 2 || recLedger[0].AccountID != bankGL.ID {
		t.Fatalf("payment-in 3 GL = %+v, want asset leg on bank account", recLedger)
	}

	// Expense EXP-1 -> UPI with its cash transaction on the bank account.
	var exp1 models.Expense
	if err := db.Where("user_id = ? AND expense_number = ?", userID, "EXP-1").First(&exp1).Error; err != nil {
		t.Fatalf("load EXP-1: %v", err)
	}
	if exp1.PaymentMode != "upi" || exp1.BankAccountID == nil || *exp1.BankAccountID != bank.ID {
		t.Fatalf("EXP-1 = mode %s account %v, want upi/%v", exp1.PaymentMode, exp1.BankAccountID, bank.ID)
	}

	// Bank balance: +400 inv50 -400 bill POUT +250+750 pin2 +50 pin3
	// -800-100 pout2 -4800 expense -600 reduce-money +110 purchase return.
	var acct models.BankAccount
	if err := db.First(&acct, "id = ?", bank.ID).Error; err != nil {
		t.Fatalf("load bank: %v", err)
	}
	if acct.Balance != -5140 {
		t.Fatalf("bank balance = %v, want -5140", acct.Balance)
	}
	// Cash in hand keeps only the cash rows: pin1 500+300, add-money 5000,
	// less the sales-return refund 140.
	if got := sumSignedCashMovements(db, userID, nil); got != 5660 {
		t.Fatalf("cash in hand = %v, want 5660", got)
	}

	// Re-running the same file must be a complete no-op.
	result, errs, err = importCashBankStatementRows(userID, content, nil, nil)
	if err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if len(errs) != 1 {
		t.Fatalf("re-import errors = %v, want the single unmatched warning", errs)
	}
	if got := result["imported"].(int); got != 0 {
		t.Fatalf("re-import imported = %d, want 0", got)
	}
	var ctN int64
	db.Model(&models.CashTransaction{}).Where("user_id = ?", userID).Count(&ctN)
	if ctN != 14 {
		t.Fatalf("after re-import cash_txns = %d, want 14", ctN)
	}
}

// When a money-moving row uses a mode with no account configured in the
// payment-method mapping, the whole import aborts and nothing is written.
func TestImportCashBankStatementUnmappedModeAborts(t *testing.T) {
	db := openCashBankStatementTestDB(t, "cashbankstmt_abort")
	previousDB := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = previousDB })

	userID, bank := seedStatementBase(t, db)

	content := []byte(`Date,Type,Txn No,Party,Invoice numbers,Mode,Paid,Received,Balance,Notes
01/09/2026,Sales Invoice,50,Acme,"",Upi,0,400.0,-,""
06/09/2026,Expense,1,"",,Card,4800.0,0,-,""
`)

	result, _, err := importCashBankStatementRows(userID, content, nil, nil)
	if err == nil {
		t.Fatalf("expected abort error, got result %v", result)
	}
	if !strings.Contains(err.Error(), "Card") {
		t.Fatalf("abort error %q should name the unmapped mode", err)
	}

	// Nothing was written: invoice 50 is still cash, the bank saw no money.
	var inv50 models.Invoice
	if err := db.Where("user_id = ? AND invoice_number = ?", userID, "50").First(&inv50).Error; err != nil {
		t.Fatalf("load invoice 50: %v", err)
	}
	if inv50.PaymentMode != "cash" || inv50.BankAccountID != nil {
		t.Fatalf("invoice 50 = mode %s account %v, want unchanged cash", inv50.PaymentMode, inv50.BankAccountID)
	}
	var acct models.BankAccount
	if err := db.First(&acct, "id = ?", bank.ID).Error; err != nil {
		t.Fatalf("load bank: %v", err)
	}
	if acct.Balance != 0 {
		t.Fatalf("bank balance = %v, want 0 after aborted import", acct.Balance)
	}
}
