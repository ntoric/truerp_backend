package controllers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openPaymentCashLedgerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// Shared-cache in-memory database: every pool connection sees the same
	// data, including rows written inside an open transaction on another conn
	// is not needed — but shared cache avoids the empty-DB reads that
	// file::memory: produces when gorm opens a second connection.
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Party{},
		&models.Invoice{},
		&models.Payment{},
		&models.PurchaseBill{},
		&models.PaymentOut{},
		&models.CashTransaction{},
		&models.BankAccount{},
		&models.PaymentMethodAccountMap{},
		&models.Account{},
		&models.JournalEntry{},
		&models.JournalEntryLine{},
		&models.Ledger{},
		&models.AuditLog{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	previousDB := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = previousDB })
	return db
}

func postJSONContext(method, path string, body interface{}) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	raw, _ := json.Marshal(body)
	c.Request = httptest.NewRequest(method, path, bytes.NewReader(raw))
	c.Request.Header.Set("Content-Type", "application/json")
	return c, rec
}

func deleteContext(path, id string) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("DELETE", path, nil)
	c.Params = gin.Params{{Key: "id", Value: id}}
	return c, rec
}

func seedCashLedgerVendor(t *testing.T, db *gorm.DB, userID uuid.UUID) models.Party {
	t.Helper()
	party := models.Party{ID: uuid.New(), UserID: userID, Name: "Vendor", PartyType: "vendor"}
	if err := db.Create(&party).Error; err != nil {
		t.Fatalf("create party: %v", err)
	}
	return party
}

func countCashTransactions(t *testing.T, db *gorm.DB, userID uuid.UUID) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&models.CashTransaction{}).Where("user_id = ?", userID).Count(&n).Error; err != nil {
		t.Fatalf("count cash txns: %v", err)
	}
	return n
}

func TestCreatePaymentOutWritesLinkedCashTransaction(t *testing.T) {
	db := openPaymentCashLedgerTestDB(t)
	userID := uuid.New()
	party := seedCashLedgerVendor(t, db, userID)

	c, rec := postJSONContext("POST", "/payment-outs", map[string]interface{}{
		"party_id":           party.ID,
		"amount_paid":        750,
		"payment_out_number": "POUT-TEST-1",
		"mode":               "cash",
		"date":               time.Now().Format(time.RFC3339),
	})
	c.Set("user_id", userID)
	CreatePaymentOut(c)

	if rec.Code != 201 {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}

	var txn models.CashTransaction
	if err := db.Where("user_id = ? AND transaction_type = ?", userID, "reduce").First(&txn).Error; err != nil {
		t.Fatalf("cash reduce txn not found: %v", err)
	}
	if txn.Amount != 750 || !txn.IsLinked || txn.Reference != "POUT-TEST-1" {
		t.Fatalf("unexpected txn: amount=%.2f linked=%v ref=%q", txn.Amount, txn.IsLinked, txn.Reference)
	}
	if txn.AccountID != nil {
		t.Fatalf("cash payment out should target cash in-hand, got account %v", txn.AccountID)
	}
	if got := sumSignedCashMovements(db, userID, nil, ""); got != -750 {
		t.Fatalf("cash in-hand = %.2f, want -750", got)
	}
}

func TestCreatePaymentOutBankModeDeductsAccount(t *testing.T) {
	db := openPaymentCashLedgerTestDB(t)
	userID := uuid.New()
	party := seedCashLedgerVendor(t, db, userID)

	account := models.BankAccount{
		ID:             uuid.New(),
		UserID:         userID,
		AccountName:    "Main",
		AccountNumber:  "1",
		BankName:       "Bank",
		OpeningBalance: 5000,
		Balance:        5000,
		IsActive:       true,
		IsPrimary:      true,
	}
	if err := db.Create(&account).Error; err != nil {
		t.Fatalf("create account: %v", err)
	}

	c, rec := postJSONContext("POST", "/payment-outs", map[string]interface{}{
		"party_id":           party.ID,
		"amount_paid":        300,
		"payment_out_number": "POUT-TEST-2",
		"mode":               "upi",
		"date":               time.Now().Format(time.RFC3339),
	})
	c.Set("user_id", userID)
	CreatePaymentOut(c)

	if rec.Code != 201 {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}

	var txn models.CashTransaction
	if err := db.Where("user_id = ? AND transaction_type = ?", userID, "reduce").First(&txn).Error; err != nil {
		t.Fatalf("cash reduce txn not found: %v", err)
	}
	if txn.AccountID == nil || *txn.AccountID != account.ID {
		t.Fatalf("txn account = %v, want %v", txn.AccountID, account.ID)
	}

	var reloaded models.BankAccount
	if err := db.First(&reloaded, "id = ?", account.ID).Error; err != nil {
		t.Fatalf("reload account: %v", err)
	}
	if reloaded.Balance != 4700 {
		t.Fatalf("bank balance = %.2f, want 4700", reloaded.Balance)
	}
}

func TestCreatePaymentOutInitialInvestmentWritesNoCashTransaction(t *testing.T) {
	db := openPaymentCashLedgerTestDB(t)
	userID := uuid.New()
	party := seedCashLedgerVendor(t, db, userID)

	c, rec := postJSONContext("POST", "/payment-outs", map[string]interface{}{
		"party_id":    party.ID,
		"amount_paid": 900,
		"mode":        "initial_investment",
		"date":        time.Now().Format(time.RFC3339),
	})
	c.Set("user_id", userID)
	CreatePaymentOut(c)

	if rec.Code != 201 {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if n := countCashTransactions(t, db, userID); n != 0 {
		t.Fatalf("cash transactions = %d, want 0", n)
	}
}

func TestDeletePaymentOutReversesCashTransaction(t *testing.T) {
	db := openPaymentCashLedgerTestDB(t)
	userID := uuid.New()
	party := seedCashLedgerVendor(t, db, userID)

	c, rec := postJSONContext("POST", "/payment-outs", map[string]interface{}{
		"party_id":           party.ID,
		"amount_paid":        400,
		"payment_out_number": "POUT-TEST-3",
		"mode":               "cash",
		"date":               time.Now().Format(time.RFC3339),
	})
	c.Set("user_id", userID)
	CreatePaymentOut(c)
	if rec.Code != 201 {
		t.Fatalf("create status = %d, want 201: %s", rec.Code, rec.Body.String())
	}

	var created models.PaymentOut
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created payment out: %v", err)
	}

	dc, drec := deleteContext("/payment-outs/"+created.ID.String(), created.ID.String())
	dc.Set("user_id", userID)
	DeletePaymentOut(dc)
	if drec.Code != 200 {
		t.Fatalf("delete status = %d, want 200: %s", drec.Code, drec.Body.String())
	}

	if n := countCashTransactions(t, db, userID); n != 0 {
		t.Fatalf("cash transactions after delete = %d, want 0", n)
	}
	if got := sumSignedCashMovements(db, userID, nil, ""); got != 0 {
		t.Fatalf("cash in-hand after delete = %.2f, want 0", got)
	}
	var ledgerCount int64
	db.Model(&models.Ledger{}).Where("user_id = ? AND transaction_type = ?", userID, "payment_out_record").Count(&ledgerCount)
	if ledgerCount != 0 {
		t.Fatalf("payment_out_record ledgers after delete = %d, want 0", ledgerCount)
	}
}

func TestCreatePaymentWritesLinkedCashTransaction(t *testing.T) {
	db := openPaymentCashLedgerTestDB(t)
	userID := uuid.New()
	party := models.Party{ID: uuid.New(), UserID: userID, Name: "Customer", PartyType: "customer"}
	if err := db.Create(&party).Error; err != nil {
		t.Fatalf("create party: %v", err)
	}

	c, rec := postJSONContext("POST", "/payments", map[string]interface{}{
		"party_id":        party.ID,
		"amount_received": 600,
		"mode":            "cash",
		"date":            time.Now().Format(time.RFC3339),
	})
	c.Set("user_id", userID)
	c.Set("user_name", "tester")
	CreatePayment(c)

	if rec.Code != 201 {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}

	var txn models.CashTransaction
	if err := db.Where("user_id = ? AND transaction_type = ?", userID, "add").First(&txn).Error; err != nil {
		t.Fatalf("cash add txn not found: %v", err)
	}
	if txn.Amount != 600 || !txn.IsLinked {
		t.Fatalf("unexpected txn: amount=%.2f linked=%v", txn.Amount, txn.IsLinked)
	}
	if got := sumSignedCashMovements(db, userID, nil, ""); got != 600 {
		t.Fatalf("cash in-hand = %.2f, want 600", got)
	}
}

func TestDeletePaymentReversesCashTransaction(t *testing.T) {
	db := openPaymentCashLedgerTestDB(t)
	userID := uuid.New()
	party := models.Party{ID: uuid.New(), UserID: userID, Name: "Customer", PartyType: "customer"}
	if err := db.Create(&party).Error; err != nil {
		t.Fatalf("create party: %v", err)
	}

	c, rec := postJSONContext("POST", "/payments", map[string]interface{}{
		"party_id":        party.ID,
		"amount_received": 250,
		"mode":            "cash",
		"date":            time.Now().Format(time.RFC3339),
	})
	c.Set("user_id", userID)
	c.Set("user_name", "tester")
	CreatePayment(c)
	if rec.Code != 201 {
		t.Fatalf("create status = %d, want 201: %s", rec.Code, rec.Body.String())
	}

	var created models.Payment
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created payment: %v", err)
	}
	dc, drec := deleteContext("/payments/"+created.ID.String(), created.ID.String())
	dc.Set("user_id", userID)
	dc.Set("user_name", "tester")
	DeletePayment(dc)
	if drec.Code != 200 {
		t.Fatalf("delete status = %d, want 200: %s", drec.Code, drec.Body.String())
	}

	if n := countCashTransactions(t, db, userID); n != 0 {
		t.Fatalf("cash transactions after delete = %d, want 0", n)
	}
	if got := sumSignedCashMovements(db, userID, nil, ""); got != 0 {
		t.Fatalf("cash in-hand after delete = %.2f, want 0", got)
	}
}

func seedUnpaidBill(t *testing.T, db *gorm.DB, userID, partyID uuid.UUID, total float64) models.PurchaseBill {
	t.Helper()
	bill := models.PurchaseBill{
		ID:          uuid.New(),
		UserID:      userID,
		PartyID:     partyID,
		BillNumber:  "BILL-" + uuid.NewString()[:8],
		BillDate:    time.Now(),
		TotalAmount: total,
		BalanceDue:  total,
		Status:      "unpaid",
	}
	if err := db.Create(&bill).Error; err != nil {
		t.Fatalf("create bill: %v", err)
	}
	return bill
}

func postPaymentOutForBill(t *testing.T, userID, partyID, billID uuid.UUID, amount float64) (*httptest.ResponseRecorder, models.PaymentOut) {
	t.Helper()
	c, rec := postJSONContext("POST", "/payment-outs", map[string]interface{}{
		"party_id":         partyID,
		"purchase_bill_id": billID,
		"amount_paid":      amount,
		"mode":             "cash",
		"date":             time.Now().Format(time.RFC3339),
	})
	c.Set("user_id", userID)
	CreatePaymentOut(c)
	var created models.PaymentOut
	if rec.Code == 201 {
		if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
			t.Fatalf("decode payment out: %v", err)
		}
	}
	return rec, created
}

func reloadBill(t *testing.T, db *gorm.DB, id uuid.UUID) models.PurchaseBill {
	t.Helper()
	var bill models.PurchaseBill
	if err := db.First(&bill, "id = ?", id).Error; err != nil {
		t.Fatalf("reload bill: %v", err)
	}
	return bill
}

func TestCreatePaymentOutMarksBillStatus(t *testing.T) {
	db := openPaymentCashLedgerTestDB(t)
	userID := uuid.New()
	party := seedCashLedgerVendor(t, db, userID)
	bill := seedUnpaidBill(t, db, userID, party.ID, 1000)

	rec, first := postPaymentOutForBill(t, userID, party.ID, bill.ID, 400)
	if rec.Code != 201 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	reloaded := reloadBill(t, db, bill.ID)
	if reloaded.Status != "partial" || reloaded.PaidAmount != 400 || reloaded.BalanceDue != 600 {
		t.Fatalf("after 400 payment: %s paid=%.2f due=%.2f", reloaded.Status, reloaded.PaidAmount, reloaded.BalanceDue)
	}

	rec, _ = postPaymentOutForBill(t, userID, party.ID, bill.ID, 600)
	if rec.Code != 201 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	reloaded = reloadBill(t, db, bill.ID)
	if reloaded.Status != "paid" || reloaded.PaidAmount != 1000 || reloaded.BalanceDue != 0 {
		t.Fatalf("after full payment: %s paid=%.2f due=%.2f", reloaded.Status, reloaded.PaidAmount, reloaded.BalanceDue)
	}

	// Deleting the first payment drops the bill back to partial.
	dc, drec := deleteContext("/payment-outs/"+first.ID.String(), first.ID.String())
	dc.Set("user_id", userID)
	DeletePaymentOut(dc)
	if drec.Code != 200 {
		t.Fatalf("delete = %d: %s", drec.Code, drec.Body.String())
	}
	reloaded = reloadBill(t, db, bill.ID)
	if reloaded.Status != "partial" || reloaded.PaidAmount != 600 || reloaded.BalanceDue != 400 {
		t.Fatalf("after delete: %s paid=%.2f due=%.2f", reloaded.Status, reloaded.PaidAmount, reloaded.BalanceDue)
	}
}

func TestCreatePaymentOutRejectsUnknownBill(t *testing.T) {
	db := openPaymentCashLedgerTestDB(t)
	userID := uuid.New()
	party := seedCashLedgerVendor(t, db, userID)

	rec, _ := postPaymentOutForBill(t, userID, party.ID, uuid.New(), 100)
	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var count int64
	db.Model(&models.PaymentOut{}).Where("user_id = ?", userID).Count(&count)
	if count != 0 {
		t.Fatalf("payment out should not be saved for an unknown bill")
	}
}

func TestCreatePaymentOutRejectsMismatchedVendor(t *testing.T) {
	db := openPaymentCashLedgerTestDB(t)
	userID := uuid.New()
	vendorA := seedCashLedgerVendor(t, db, userID)
	vendorB := seedCashLedgerVendor(t, db, userID)
	bill := seedUnpaidBill(t, db, userID, vendorB.ID, 500)

	rec, _ := postPaymentOutForBill(t, userID, vendorA.ID, bill.ID, 500)
	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if got := reloadBill(t, db, bill.ID); got.PaidAmount != 0 || got.Status != "unpaid" {
		t.Fatalf("mismatched payment touched the bill: %s paid=%.2f", got.Status, got.PaidAmount)
	}
}

func TestCreatePaymentMarksInvoicePartialThenPaid(t *testing.T) {
	db := openPaymentCashLedgerTestDB(t)
	userID := uuid.New()
	customer := models.Party{ID: uuid.New(), UserID: userID, Name: "Customer", PartyType: "customer"}
	if err := db.Create(&customer).Error; err != nil {
		t.Fatalf("create party: %v", err)
	}
	invoice := models.Invoice{
		ID:            uuid.New(),
		UserID:        userID,
		InvoiceNumber: "INV-STATUS-1",
		PartyID:       customer.ID,
		Date:          time.Now(),
		Status:        "sent",
		TotalAmount:   1000,
	}
	if err := db.Create(&invoice).Error; err != nil {
		t.Fatalf("create invoice: %v", err)
	}

	pay := func(amount float64) (*httptest.ResponseRecorder, models.Payment) {
		c, rec := postJSONContext("POST", "/payments", map[string]interface{}{
			"party_id":        customer.ID,
			"invoice_id":      invoice.ID,
			"amount_received": amount,
			"mode":            "cash",
			"date":            time.Now().Format(time.RFC3339),
		})
		c.Set("user_id", userID)
		c.Set("user_name", "tester")
		CreatePayment(c)
		var created models.Payment
		if rec.Code == 201 {
			if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
				t.Fatalf("decode payment: %v", err)
			}
		}
		return rec, created
	}
	reload := func() models.Invoice {
		var inv models.Invoice
		if err := db.First(&inv, "id = ?", invoice.ID).Error; err != nil {
			t.Fatalf("reload invoice: %v", err)
		}
		return inv
	}

	rec, first := pay(400)
	if rec.Code != 201 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if inv := reload(); inv.Status != "partial" || inv.AmountPaid != 400 {
		t.Fatalf("after 400 payment: %s paid=%.2f", inv.Status, inv.AmountPaid)
	}

	rec, _ = pay(600)
	if rec.Code != 201 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if inv := reload(); inv.Status != "paid" || inv.AmountPaid != 1000 {
		t.Fatalf("after full payment: %s paid=%.2f", inv.Status, inv.AmountPaid)
	}

	// Deleting the first payment leaves the remaining 600 as partial.
	dc, drec := deleteContext("/payments/"+first.ID.String(), first.ID.String())
	dc.Set("user_id", userID)
	dc.Set("user_name", "tester")
	DeletePayment(dc)
	if drec.Code != 200 {
		t.Fatalf("delete = %d: %s", drec.Code, drec.Body.String())
	}
	if inv := reload(); inv.Status != "partial" || inv.AmountPaid != 600 {
		t.Fatalf("after delete: %s paid=%.2f", inv.Status, inv.AmountPaid)
	}
}
