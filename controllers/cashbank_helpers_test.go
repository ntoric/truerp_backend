package controllers

import (
	"testing"
	"time"
	"truerp/models"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestInvoiceCashLedgerNeedsResync(t *testing.T) {
	bankA := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	bankB := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	partyA := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	partyB := uuid.MustParse("44444444-4444-4444-4444-444444444444")

	base := models.Invoice{
		InvoiceNumber: "INV-0001",
		AmountPaid:    100,
		PaymentMode:   "cash",
		PartyID:       partyA,
	}

	t.Run("unchanged", func(t *testing.T) {
		current := base
		if invoiceCashLedgerNeedsResync(&base, &current) {
			t.Fatal("expected no resync when payment fields are unchanged")
		}
	})

	t.Run("amount changed", func(t *testing.T) {
		current := base
		current.AmountPaid = 80
		if !invoiceCashLedgerNeedsResync(&base, &current) {
			t.Fatal("expected resync when amount paid changes")
		}
	})

	t.Run("payment method changed", func(t *testing.T) {
		current := base
		current.PaymentMode = "upi"
		current.BankAccountID = &bankA
		if !invoiceCashLedgerNeedsResync(&base, &current) {
			t.Fatal("expected resync when payment method changes")
		}
	})

	t.Run("bank account changed", func(t *testing.T) {
		previous := base
		previous.PaymentMode = "upi"
		previous.BankAccountID = &bankA
		current := previous
		current.BankAccountID = &bankB
		if !invoiceCashLedgerNeedsResync(&previous, &current) {
			t.Fatal("expected resync when destination account changes")
		}
	})

	t.Run("party changed", func(t *testing.T) {
		current := base
		current.PartyID = partyB
		if !invoiceCashLedgerNeedsResync(&base, &current) {
			t.Fatal("expected resync when party changes")
		}
	})

	t.Run("date changed", func(t *testing.T) {
		current := base
		current.Date = current.Date.AddDate(0, 0, 1)
		if !invoiceCashLedgerNeedsResync(&base, &current) {
			t.Fatal("expected resync when invoice date changes")
		}
	})

	t.Run("split methods changed", func(t *testing.T) {
		current := base
		current.PaymentSplits = []models.PaymentSplit{
			{Mode: "cash", Amount: 50},
			{Mode: "upi", Amount: 50},
		}
		if !invoiceCashLedgerNeedsResync(&base, &current) {
			t.Fatal("expected resync when payment splits change")
		}
	})
}

func TestParsePaymentNumberSequence(t *testing.T) {
	tests := []struct {
		number string
		prefix string
		want   int64
	}{
		{number: "PIN-0001", prefix: "PIN", want: 1},
		{number: "PIN-0042", prefix: "PIN", want: 42},
		{number: "pin-7", prefix: "PIN", want: 7},
		{number: "PIN-ABC", prefix: "PIN", want: 0},
		{number: "POUT-0001", prefix: "PIN", want: 0},
		{number: "", prefix: "PIN", want: 0},
	}
	for _, tt := range tests {
		if got := parsePaymentNumberSequence(tt.number, tt.prefix); got != tt.want {
			t.Errorf("parsePaymentNumberSequence(%q, %q) = %d, want %d", tt.number, tt.prefix, got, tt.want)
		}
	}
}

func openCashBankTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.BankAccount{}, &models.CashTransaction{}, &models.PaymentOut{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestBuildCashBankSummaryDeductsCashInHandExpense(t *testing.T) {
	db := openCashBankTestDB(t)
	userID := uuid.New()
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	add := models.CashTransaction{
		ID:              uuid.New(),
		UserID:          userID,
		TransactionType: "add",
		Amount:          1000,
		Date:            now,
		Description:     "Cash sale",
		Reference:       "INV-0001",
		IsLinked:        true,
	}
	if err := db.Create(&add).Error; err != nil {
		t.Fatalf("seed cash in-hand: %v", err)
	}
	if err := recordExpenseCashOut(db, userID, nil, 250, now, "EXP-0001", "Office supplies"); err != nil {
		t.Fatalf("record expense: %v", err)
	}

	summary := buildCashBankSummary(db, userID, nil)
	if summary.CashInHand != 750 {
		t.Fatalf("cash in-hand = %.2f, want 750", summary.CashInHand)
	}
	if summary.TotalBalance != 750 {
		t.Fatalf("total balance = %.2f, want 750", summary.TotalBalance)
	}
}

func TestBuildCashBankSummaryDeductsBankExpense(t *testing.T) {
	db := openCashBankTestDB(t)
	userID := uuid.New()
	accountID := uuid.New()
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	account := models.BankAccount{
		ID:             accountID,
		UserID:         userID,
		AccountName:    "HDFC Current",
		AccountNumber:  "123",
		BankName:       "HDFC",
		OpeningBalance: 5000,
		Balance:        5000,
		IsActive:       true,
	}
	if err := db.Create(&account).Error; err != nil {
		t.Fatalf("create account: %v", err)
	}
	if err := recordExpenseCashOut(db, userID, &accountID, 800, now, "EXP-0002", "Rent"); err != nil {
		t.Fatalf("record expense: %v", err)
	}

	summary := buildCashBankSummary(db, userID, []models.BankAccount{account})
	if summary.CashInHand != 0 {
		t.Fatalf("cash in-hand = %.2f, want 0", summary.CashInHand)
	}
	if len(summary.BankAccounts) != 1 || summary.BankAccounts[0].Balance != 4200 {
		t.Fatalf("bank balance = %.2f, want 4200", summary.BankAccounts[0].Balance)
	}
	if summary.TotalBalance != 4200 {
		t.Fatalf("total balance = %.2f, want 4200", summary.TotalBalance)
	}
}

func TestIsInitialInvestmentPayment(t *testing.T) {
	if !isInitialInvestmentPayment("initial_investment") {
		t.Fatal("expected initial_investment to be recognized")
	}
	if !isInitialInvestmentPayment(" Initial_Investment ") {
		t.Fatal("expected normalized initial_investment to be recognized")
	}
	if isInitialInvestmentPayment("cash") || isInitialInvestmentPayment("") {
		t.Fatal("cash / empty should not be treated as initial investment")
	}
}

func TestPaymentMethodLabelInitialInvestment(t *testing.T) {
	if got := paymentMethodLabel("initial_investment"); got != "Initial Investment" {
		t.Fatalf("label = %q, want Initial Investment", got)
	}
}

func TestResolveBankAccountForPaymentModeInitialInvestment(t *testing.T) {
	bankID := uuid.New()
	got, err := resolveBankAccountForPaymentMode(uuid.New(), "initial_investment", &bankID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != nil {
		t.Fatalf("got account %v, want nil so cash/bank are not used", *got)
	}
}

func openPurchasePaymentTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Party{},
		&models.PurchaseBill{},
		&models.PaymentOut{},
		&models.CashTransaction{},
		&models.Account{},
		&models.JournalEntry{},
		&models.JournalEntryLine{},
		&models.Ledger{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestCreateLinkedPurchasePaymentOutInitialInvestmentDoesNotMoveCash(t *testing.T) {
	db := openPurchasePaymentTestDB(t)
	userID := uuid.New()
	partyID := uuid.New()
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	party := models.Party{
		ID:        partyID,
		UserID:    userID,
		Name:      "Opening Vendor",
		PartyType: "vendor",
		IsActive:  true,
	}
	if err := db.Create(&party).Error; err != nil {
		t.Fatalf("create party: %v", err)
	}

	bill := models.PurchaseBill{
		ID:          uuid.New(),
		UserID:      userID,
		PartyID:     partyID,
		BillNumber:  "PINV-OPEN-1",
		BillDate:    now,
		TotalAmount: 5000,
		PaidAmount:  5000,
		PaymentMode: "initial_investment",
		Status:      "paid",
	}
	if err := db.Create(&bill).Error; err != nil {
		t.Fatalf("create bill: %v", err)
	}

	if err := createLinkedPurchasePaymentOut(db, userID, &bill, bill.PaidAmount, bill.BillDate, "opening stock"); err != nil {
		t.Fatalf("create payment out: %v", err)
	}

	summary := buildCashBankSummary(db, userID, nil)
	if summary.CashInHand != 0 {
		t.Fatalf("cash in-hand = %.2f, want 0", summary.CashInHand)
	}
	if summary.InitialInvestment != 5000 {
		t.Fatalf("initial investment = %.2f, want 5000", summary.InitialInvestment)
	}

	var txnCount int64
	if err := db.Model(&models.CashTransaction{}).Where("user_id = ?", userID).Count(&txnCount).Error; err != nil {
		t.Fatalf("count cash txns: %v", err)
	}
	if txnCount != 0 {
		t.Fatalf("cash transactions = %d, want 0", txnCount)
	}

	var paymentOut models.PaymentOut
	if err := db.Where("user_id = ? AND purchase_bill_id = ?", userID, bill.ID).First(&paymentOut).Error; err != nil {
		t.Fatalf("payment out: %v", err)
	}
	if paymentOut.Mode != "initial_investment" {
		t.Fatalf("payment out mode = %q, want initial_investment", paymentOut.Mode)
	}

	var equity models.Account
	if err := db.Where("user_id = ? AND code = ?", userID, acCodeEquity).First(&equity).Error; err != nil {
		t.Fatalf("equity account: %v", err)
	}
	if equity.Balance != 5000 {
		t.Fatalf("owner's equity = %.2f, want 5000", equity.Balance)
	}

	var cash models.Account
	if err := db.Where("user_id = ? AND code = ?", userID, acCodeCash).First(&cash).Error; err != nil {
		t.Fatalf("cash account: %v", err)
	}
	if cash.Balance != 0 {
		t.Fatalf("GL cash = %.2f, want 0", cash.Balance)
	}
}

func TestCreateLinkedPurchasePaymentOutCashReducesCashInHand(t *testing.T) {
	db := openPurchasePaymentTestDB(t)
	userID := uuid.New()
	partyID := uuid.New()
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	party := models.Party{
		ID:        partyID,
		UserID:    userID,
		Name:      "Cash Vendor",
		PartyType: "vendor",
		IsActive:  true,
	}
	if err := db.Create(&party).Error; err != nil {
		t.Fatalf("create party: %v", err)
	}

	bill := models.PurchaseBill{
		ID:          uuid.New(),
		UserID:      userID,
		PartyID:     partyID,
		BillNumber:  "PINV-CASH-1",
		BillDate:    now,
		TotalAmount: 1200,
		PaidAmount:  1200,
		PaymentMode: "cash",
		Status:      "paid",
	}
	if err := db.Create(&bill).Error; err != nil {
		t.Fatalf("create bill: %v", err)
	}

	if err := createLinkedPurchasePaymentOut(db, userID, &bill, bill.PaidAmount, bill.BillDate, "paid in cash"); err != nil {
		t.Fatalf("create payment out: %v", err)
	}

	summary := buildCashBankSummary(db, userID, nil)
	if summary.CashInHand != -1200 {
		t.Fatalf("cash in-hand = %.2f, want -1200", summary.CashInHand)
	}
	if summary.InitialInvestment != 0 {
		t.Fatalf("initial investment = %.2f, want 0", summary.InitialInvestment)
	}
}
