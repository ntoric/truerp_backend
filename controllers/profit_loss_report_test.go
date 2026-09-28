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

func setupProfitLossTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Business{},
		&models.Product{},
		&models.StockEntry{},
		&models.Invoice{},
		&models.PurchaseBill{},
		&models.Expense{},
		&models.ExpenseItem{},
		&models.SalesReturn{},
		&models.CreditNote{},
		&models.PurchaseReturn{},
		&models.DebitNote{},
		&models.Account{},
		&models.Ledger{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	previousDB := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = previousDB })
	return db
}

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 12, 0, 0, 0, time.UTC)
}

func TestLoadProfitLossReport(t *testing.T) {
	db := setupProfitLossTestDB(t)
	userID := uuid.New()
	partyID := uuid.New()
	productID := uuid.New()

	if err := db.Create(&models.Product{ID: productID, UserID: userID, Name: "Widget", SKU: "W1", PurchasePrice: 100}).Error; err != nil {
		t.Fatalf("product: %v", err)
	}

	mkEntry := func(entryType string, qty, cost float64, d time.Time) {
		e := models.StockEntry{
			ID:             uuid.New(),
			UserID:         userID,
			ProductID:      &productID,
			ItemName:       "Widget",
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
	// Before period (Jan): opening 10 @ 100 = 1000 opening stock.
	mkEntry("opening", 10, 100, day(2025, 12, 20))
	// In period: +20 purchase, -5 sale → closing 25 @ 100 = 2500.
	mkEntry("purchase", 20, 100, day(2026, 1, 10))
	mkEntry("sale", -5, 200, day(2026, 1, 12))
	// Pending entry must be ignored.
	mkPending := models.StockEntry{
		ID: uuid.New(), UserID: userID, ProductID: &productID, ItemName: "Widget",
		OutletID: uuid.New(), EntryType: "purchase", Quantity: 50, CostPrice: 100,
		ApprovalStatus: "pending", EntryDate: day(2026, 1, 15),
	}
	if err := db.Create(&mkPending).Error; err != nil {
		t.Fatalf("pending entry: %v", err)
	}

	invoice := models.Invoice{
		ID: uuid.New(), UserID: userID, PartyID: partyID, InvoiceNumber: "INV-1",
		Date: day(2026, 1, 15), Status: "paid", TotalAmount: 10000,
	}
	if err := db.Create(&invoice).Error; err != nil {
		t.Fatalf("invoice: %v", err)
	}
	// Cancelled invoice must be excluded.
	if err := db.Create(&models.Invoice{
		ID: uuid.New(), UserID: userID, PartyID: partyID, InvoiceNumber: "INV-2",
		Date: day(2026, 1, 16), Status: "cancelled", TotalAmount: 9999,
	}).Error; err != nil {
		t.Fatalf("cancelled invoice: %v", err)
	}

	if err := db.Create(&models.SalesReturn{
		ID: uuid.New(), UserID: userID, PartyID: partyID, InvoiceID: invoice.ID,
		ReturnNumber: "SR-1", Date: day(2026, 1, 18), Amount: 1000, Status: "processed",
	}).Error; err != nil {
		t.Fatalf("sales return: %v", err)
	}
	if err := db.Create(&models.CreditNote{
		ID: uuid.New(), UserID: userID, PartyID: partyID, InvoiceID: invoice.ID,
		CreditNoteNumber: "CN-1", Date: day(2026, 1, 19), TotalAmount: 500, Status: "issued",
	}).Error; err != nil {
		t.Fatalf("credit note: %v", err)
	}
	if err := db.Create(&models.PurchaseBill{
		ID: uuid.New(), UserID: userID, PartyID: partyID, BillNumber: "PB-1",
		BillDate: day(2026, 1, 11), TotalAmount: 4000,
	}).Error; err != nil {
		t.Fatalf("purchase bill: %v", err)
	}
	if err := db.Create(&models.PurchaseReturn{
		ID: uuid.New(), UserID: userID, PartyID: partyID, PurchaseBillID: uuid.New(),
		ReturnNumber: "PR-1", Date: day(2026, 1, 20), Amount: 300, Status: "processed",
	}).Error; err != nil {
		t.Fatalf("purchase return: %v", err)
	}
	if err := db.Create(&models.DebitNote{
		ID: uuid.New(), UserID: userID, PartyID: partyID,
		DebitNoteNumber: "DN-1", Date: day(2026, 1, 21), TotalAmount: 200, Status: "issued",
	}).Error; err != nil {
		t.Fatalf("debit note: %v", err)
	}
	if err := db.Create(&models.Expense{
		ID: uuid.New(), UserID: userID, ExpenseNumber: "EXP-1", Category: "General",
		Date: day(2026, 1, 14), Amount: 800,
	}).Error; err != nil {
		t.Fatalf("expense: %v", err)
	}

	// GL accounts + ledger postings.
	salesAcc := models.Account{ID: uuid.New(), UserID: userID, Code: acCodeSales, Name: "Sales", AccountType: "income", IsActive: true}
	purchAcc := models.Account{ID: uuid.New(), UserID: userID, Code: acCodePurchase, Name: "Purchases", AccountType: "expense", IsActive: true}
	genExpAcc := models.Account{ID: uuid.New(), UserID: userID, Code: acCodeExpense, Name: "General Expenses", AccountType: "expense", IsActive: true}
	payrollAcc := models.Account{ID: uuid.New(), UserID: userID, Code: acCodePayroll, Name: "Payroll", AccountType: "expense", IsActive: true}
	interestAcc := models.Account{ID: uuid.New(), UserID: userID, Code: "4200", Name: "Interest Income", AccountType: "income", IsActive: true}
	rentAcc := models.Account{ID: uuid.New(), UserID: userID, Code: "5500", Name: "Rent", AccountType: "expense", IsActive: true}
	for _, a := range []models.Account{salesAcc, purchAcc, genExpAcc, payrollAcc, interestAcc, rentAcc} {
		if err := db.Create(&a).Error; err != nil {
			t.Fatalf("account %s: %v", a.Name, err)
		}
	}
	mkLedger := func(accountID uuid.UUID, refType string, debit, credit float64, d time.Time) {
		l := models.Ledger{
			ID: uuid.New(), UserID: userID, AccountID: accountID,
			TransactionDate: d, TransactionType: refType, ReferenceID: uuid.New(),
			Debit: debit, Credit: credit,
		}
		if err := db.Create(&l).Error; err != nil {
			t.Fatalf("ledger: %v", err)
		}
	}
	mkLedger(salesAcc.ID, "invoice", 0, 10000, day(2026, 1, 15))           // excluded: sales account
	mkLedger(purchAcc.ID, "purchase_bill", 4000, 0, day(2026, 1, 11))     // excluded: purchases account
	mkLedger(genExpAcc.ID, "expense", 800, 0, day(2026, 1, 14))           // excluded: expense-module posting
	mkLedger(genExpAcc.ID, "cash_reduce", 150, 0, day(2026, 1, 22))       // indirect expense
	mkLedger(interestAcc.ID, "journal_entry", 0, 2000, day(2026, 1, 25))  // other income
	mkLedger(rentAcc.ID, "journal_entry", 700, 0, day(2026, 1, 26))       // indirect expense
	mkLedger(payrollAcc.ID, "payroll", 3000, 0, day(2026, 1, 28))         // payroll → indirect expense

	report, err := loadProfitLossReport(userID, "custom", "", "2026-01-01", "2026-01-31")
	if err != nil {
		t.Fatalf("loadProfitLossReport: %v", err)
	}

	if got := report.Sales.TotalAmount; got != 10000 {
		t.Fatalf("sales = %v, want 10000", got)
	}
	if got := report.SalesReturns.TotalAmount; got != 1500 {
		t.Fatalf("sales returns = %v, want 1500", got)
	}
	if got := report.SalesReturns.Count; got != 2 {
		t.Fatalf("sales returns count = %v, want 2", got)
	}
	if got := report.Purchases.TotalAmount; got != 4000 {
		t.Fatalf("purchases = %v, want 4000", got)
	}
	if got := report.PurchaseReturns.TotalAmount; got != 500 {
		t.Fatalf("purchase returns = %v, want 500", got)
	}
	if got := report.OpeningStock; got != 1000 {
		t.Fatalf("opening stock = %v, want 1000", got)
	}
	if got := report.ClosingStock; got != 2500 {
		t.Fatalf("closing stock = %v, want 2500", got)
	}
	// Gross = netSales + closing − opening = 8500+2500−1000
	if got := report.GrossProfit; got != 10000 {
		t.Fatalf("gross profit = %v, want 10000", got)
	}
	if got := report.OtherIncome.TotalAmount; got != 2000 {
		t.Fatalf("other income = %v, want 2000", got)
	}
	// Indirect = cash_reduce 150 + journal rent 700; 'expense' and 'payroll'
	// postings are excluded because the Expenses section counts them.
	if got := report.IndirectExpenses.TotalAmount; got != 850 {
		t.Fatalf("indirect expenses = %v, want 850", got)
	}
	if got := report.Expenses.TotalAmount; got != 800 {
		t.Fatalf("expenses = %v, want 800", got)
	}
	// Net = 10000 + 2000 − 800 (indirect expenses are no longer deducted)
	if got := report.NetProfit; got != 11200 {
		t.Fatalf("net profit = %v, want 11200", got)
	}
	if len(report.IndirectExpenseLines) != 2 {
		t.Fatalf("indirect expense lines = %d, want 2", len(report.IndirectExpenseLines))
	}
	if len(report.OtherIncomeLines) != 1 {
		t.Fatalf("other income lines = %d, want 1", len(report.OtherIncomeLines))
	}
}
