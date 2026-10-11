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

func openReturnBalanceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Party{},
		&models.SalesReturn{},
		&models.SalesReturnItem{},
		&models.PurchaseReturn{},
		&models.PurchaseReturnItem{},
		&models.StockEntry{},
		&models.Warehouse{},
		&models.InventoryStock{},
		&models.InvoiceItem{},
		&models.CashTransaction{},
		&models.BankAccount{},
		&models.PurchaseBill{},
		&models.PurchaseBillItem{},
		&models.Product{},
		&models.PaymentMethodAccountMap{},
		&models.Payment{},
		&models.PaymentOut{},
		&models.Invoice{},
		&models.CreditNote{},
		&models.CreditNoteItem{},
		&models.DebitNote{},
		&models.DebitNoteItem{},
		&models.Account{},
		&models.JournalEntry{},
		&models.JournalEntryLine{},
		&models.Ledger{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	previousDB := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = previousDB })
	return db
}

func processContext(path, id string) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", path, nil)
	c.Params = gin.Params{{Key: "id", Value: id}}
	return c, rec
}

func createContext(path string, payload interface{}) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body, _ := json.Marshal(payload)
	c.Request = httptest.NewRequest("POST", path, bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c, rec
}

func reloadPartyBalance(t *testing.T, db *gorm.DB, partyID uuid.UUID) float64 {
	t.Helper()
	var party models.Party
	if err := db.First(&party, "id = ?", partyID).Error; err != nil {
		t.Fatalf("reload party: %v", err)
	}
	return party.Balance
}

func TestProcessSalesReturnReducesCustomerBalance(t *testing.T) {
	db := openReturnBalanceTestDB(t)
	userID := uuid.New()

	party := models.Party{ID: uuid.New(), UserID: userID, Name: "Customer", PartyType: "customer", Balance: 1000}
	if err := db.Create(&party).Error; err != nil {
		t.Fatalf("create party: %v", err)
	}

	salesReturn := models.SalesReturn{
		ID:             uuid.New(),
		UserID:         userID,
		PartyID:        party.ID,
		ReturnNumber:   "SR-0001",
		Date:           time.Now(),
		Amount:         300,
		DeductionTotal: 50,
		Status:         "draft",
		Items: []models.SalesReturnItem{{
			ID:          uuid.New(),
			Description: "Widget",
			Quantity:    2,
			UnitPrice:   150,
			Total:       300,
		}},
	}
	if err := db.Create(&salesReturn).Error; err != nil {
		t.Fatalf("create sales return: %v", err)
	}

	c, rec := processContext("/sales-returns/"+salesReturn.ID.String()+"/process", salesReturn.ID.String())
	c.Set("user_id", userID)
	ProcessSalesReturn(c)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	// Net refund = 300 - 50 deductions = 250 credited back to the customer.
	if got := reloadPartyBalance(t, db, party.ID); got != 750 {
		t.Fatalf("party balance = %.2f, want 750", got)
	}
}

func TestProcessSalesReturnCreditNoteCreatesIssuedCreditNote(t *testing.T) {
	db := openReturnBalanceTestDB(t)
	userID := uuid.New()

	party := models.Party{ID: uuid.New(), UserID: userID, Name: "Customer", PartyType: "customer", Balance: 1000}
	if err := db.Create(&party).Error; err != nil {
		t.Fatalf("create party: %v", err)
	}
	invoice := models.Invoice{
		ID:            uuid.New(),
		UserID:        userID,
		PartyID:       party.ID,
		InvoiceNumber: "INV-0001",
		TotalAmount:   300,
		Status:        "sent",
	}
	if err := db.Create(&invoice).Error; err != nil {
		t.Fatalf("create invoice: %v", err)
	}

	salesReturn := models.SalesReturn{
		ID:             uuid.New(),
		UserID:         userID,
		PartyID:        party.ID,
		InvoiceID:      invoice.ID,
		ReturnNumber:   "SR-0010",
		Date:           time.Now(),
		Amount:         300,
		DeductionTotal: 50,
		DeductionItems: []models.AdditionalCharge{{Label: "Restocking fee", Amount: 50}},
		Status:         "draft",
		RefundMode:     "credit_note",
		Items: []models.SalesReturnItem{{
			ID:          uuid.New(),
			Description: "Widget",
			Quantity:    2,
			UnitPrice:   150,
			Total:       300,
		}},
	}
	if err := db.Create(&salesReturn).Error; err != nil {
		t.Fatalf("create sales return: %v", err)
	}

	c, rec := processContext("/sales-returns/"+salesReturn.ID.String()+"/process", salesReturn.ID.String())
	c.Set("user_id", userID)
	ProcessSalesReturn(c)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	// Net refund = 300 - 50 = 250 credited to the customer.
	if got := reloadPartyBalance(t, db, party.ID); got != 750 {
		t.Fatalf("party balance = %.2f, want 750", got)
	}
	var reloaded models.Invoice
	if err := db.First(&reloaded, "id = ?", invoice.ID).Error; err != nil {
		t.Fatalf("reload invoice: %v", err)
	}
	if reloaded.AmountPaid != 250 || reloaded.Status != "partial" {
		t.Fatalf("invoice = paid %.2f status %s, want paid 250 partial", reloaded.AmountPaid, reloaded.Status)
	}

	var creditNote models.CreditNote
	if err := db.Where("user_id = ? AND invoice_id = ?", userID, invoice.ID).Preload("Items").First(&creditNote).Error; err != nil {
		t.Fatalf("credit note not created: %v", err)
	}
	if creditNote.Status != "issued" || creditNote.TotalAmount != 250 || creditNote.PartyID != party.ID {
		t.Fatalf("credit note = %+v, want issued 250 for party %s", creditNote, party.ID)
	}
	if creditNote.CreditNoteNumber != "CN-0001" {
		t.Fatalf("credit note number = %s, want CN-0001", creditNote.CreditNoteNumber)
	}
	// Items reconcile to the net amount: the 300 widget line plus a -50
	// deduction line.
	if len(creditNote.Items) != 2 {
		t.Fatalf("credit note items = %d, want 2", len(creditNote.Items))
	}
	var itemSum float64
	for _, it := range creditNote.Items {
		itemSum += it.Total
	}
	if itemSum != 250 {
		t.Fatalf("credit note item total = %.2f, want 250", itemSum)
	}
}

func TestProcessSalesReturnCashRefundDoesNotCreateCreditNote(t *testing.T) {
	db := openReturnBalanceTestDB(t)
	userID := uuid.New()

	party := models.Party{ID: uuid.New(), UserID: userID, Name: "Customer", PartyType: "customer", Balance: 1000}
	if err := db.Create(&party).Error; err != nil {
		t.Fatalf("create party: %v", err)
	}

	salesReturn := models.SalesReturn{
		ID:           uuid.New(),
		UserID:       userID,
		PartyID:      party.ID,
		ReturnNumber: "SR-0011",
		Date:         time.Now(),
		Amount:       300,
		Status:       "draft",
		RefundMode:   "cash",
		Items: []models.SalesReturnItem{{
			ID:          uuid.New(),
			Description: "Widget",
			Quantity:    2,
			UnitPrice:   150,
			Total:       300,
		}},
	}
	if err := db.Create(&salesReturn).Error; err != nil {
		t.Fatalf("create sales return: %v", err)
	}

	c, rec := processContext("/sales-returns/"+salesReturn.ID.String()+"/process", salesReturn.ID.String())
	c.Set("user_id", userID)
	ProcessSalesReturn(c)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var count int64
	db.Model(&models.CreditNote{}).Where("user_id = ?", userID).Count(&count)
	if count != 0 {
		t.Fatalf("credit notes = %d, want 0 for cash refund", count)
	}
}

func TestProcessPurchaseReturnReducesVendorPayable(t *testing.T) {
	db := openReturnBalanceTestDB(t)
	userID := uuid.New()

	vendor := models.Party{ID: uuid.New(), UserID: userID, Name: "Vendor", PartyType: "vendor", Balance: -5000}
	if err := db.Create(&vendor).Error; err != nil {
		t.Fatalf("create vendor: %v", err)
	}

	purchaseReturn := models.PurchaseReturn{
		ID:           uuid.New(),
		UserID:       userID,
		PartyID:      vendor.ID,
		ReturnNumber: "PR-0001",
		Date:         time.Now(),
		Amount:       1200,
		Status:       "draft",
		Items: []models.PurchaseReturnItem{{
			ID:          uuid.New(),
			Description: "Raw material",
			Quantity:    10,
			UnitPrice:   120,
			Total:       1200,
		}},
	}
	if err := db.Create(&purchaseReturn).Error; err != nil {
		t.Fatalf("create purchase return: %v", err)
	}

	c, rec := processContext("/purchase-returns/"+purchaseReturn.ID.String()+"/process", purchaseReturn.ID.String())
	c.Set("user_id", userID)
	ProcessPurchaseReturn(c)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	// Vendor payable shrinks: -5000 + 1200 = -3800.
	if got := reloadPartyBalance(t, db, vendor.ID); got != -3800 {
		t.Fatalf("vendor balance = %.2f, want -3800", got)
	}
}

func TestProcessPurchaseReturnCreditNoteSettlesUnpaidBill(t *testing.T) {
	db := openReturnBalanceTestDB(t)
	userID := uuid.New()

	vendor := models.Party{ID: uuid.New(), UserID: userID, Name: "Vendor", PartyType: "vendor", Balance: -5000}
	if err := db.Create(&vendor).Error; err != nil {
		t.Fatalf("create vendor: %v", err)
	}
	bill := models.PurchaseBill{
		ID:          uuid.New(),
		UserID:      userID,
		PartyID:     vendor.ID,
		BillNumber:  "PB-0002",
		Status:      "unpaid",
		TotalAmount: 5000,
		BalanceDue:  5000,
	}
	if err := db.Create(&bill).Error; err != nil {
		t.Fatalf("create bill: %v", err)
	}

	purchaseReturn := models.PurchaseReturn{
		ID:             uuid.New(),
		UserID:         userID,
		PartyID:        vendor.ID,
		PurchaseBillID: bill.ID,
		ReturnNumber:   "PR-0004",
		Date:           time.Now(),
		Amount:         1200,
		Status:         "draft",
		RefundMode:     "credit_note",
		Items: []models.PurchaseReturnItem{{
			ID:          uuid.New(),
			Description: "Raw material",
			Quantity:    10,
			UnitPrice:   120,
			Total:       1200,
		}},
	}
	if err := db.Create(&purchaseReturn).Error; err != nil {
		t.Fatalf("create purchase return: %v", err)
	}

	c, rec := processContext("/purchase-returns/"+purchaseReturn.ID.String()+"/process", purchaseReturn.ID.String())
	c.Set("user_id", userID)
	ProcessPurchaseReturn(c)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := reloadPartyBalance(t, db, vendor.ID); got != -3800 {
		t.Fatalf("vendor balance = %.2f, want -3800", got)
	}
	var reloaded models.PurchaseBill
	if err := db.First(&reloaded, "id = ?", bill.ID).Error; err != nil {
		t.Fatalf("reload bill: %v", err)
	}
	// The credit covers 1200 of the unpaid bill: balance_due drops and the
	// bill reflects partial settlement.
	if reloaded.BalanceDue != 3800 || reloaded.PaidAmount != 1200 || reloaded.Status != "partial" {
		t.Fatalf("bill = paid %.2f due %.2f status %s, want paid 1200 due 3800 partial",
			reloaded.PaidAmount, reloaded.BalanceDue, reloaded.Status)
	}

	var debitNote models.DebitNote
	if err := db.Where("user_id = ? AND purchase_bill_id = ?", userID, bill.ID).First(&debitNote).Error; err != nil {
		t.Fatalf("debit note not created: %v", err)
	}
	if debitNote.Status != "issued" || debitNote.TotalAmount != 1200 || debitNote.PartyID != vendor.ID {
		t.Fatalf("debit note = %+v, want issued 1200 for vendor %s", debitNote, vendor.ID)
	}
	if debitNote.DebitNoteNumber != "DN-0001" {
		t.Fatalf("debit note number = %s, want DN-0001", debitNote.DebitNoteNumber)
	}
}

func TestProcessPurchaseReturnCashRefundAddsCashTxn(t *testing.T) {
	db := openReturnBalanceTestDB(t)
	userID := uuid.New()

	vendor := models.Party{ID: uuid.New(), UserID: userID, Name: "Vendor", PartyType: "vendor", Balance: -5000}
	if err := db.Create(&vendor).Error; err != nil {
		t.Fatalf("create vendor: %v", err)
	}

	purchaseReturn := models.PurchaseReturn{
		ID:           uuid.New(),
		UserID:       userID,
		PartyID:      vendor.ID,
		ReturnNumber: "PR-0002",
		Date:         time.Now(),
		Amount:       1200,
		Status:       "draft",
		RefundMode:   "cash",
		Items: []models.PurchaseReturnItem{{
			ID:          uuid.New(),
			Description: "Raw material",
			Quantity:    10,
			UnitPrice:   120,
			Total:       1200,
		}},
	}
	if err := db.Create(&purchaseReturn).Error; err != nil {
		t.Fatalf("create purchase return: %v", err)
	}

	c, rec := processContext("/purchase-returns/"+purchaseReturn.ID.String()+"/process", purchaseReturn.ID.String())
	c.Set("user_id", userID)
	ProcessPurchaseReturn(c)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	// Cash refund settles in money: the vendor payable is unchanged.
	if got := reloadPartyBalance(t, db, vendor.ID); got != -5000 {
		t.Fatalf("vendor balance = %.2f, want -5000", got)
	}
	var txn models.CashTransaction
	if err := db.Where("user_id = ? AND reference = ?", userID, "PR-0002").First(&txn).Error; err != nil {
		t.Fatalf("load cash txn: %v", err)
	}
	if txn.TransactionType != "add" || txn.Amount != 1200 {
		t.Fatalf("cash txn = %+v, want add 1200", txn)
	}
	if txn.AccountID != nil {
		t.Fatalf("cash refund should land in cash in hand, got account %v", txn.AccountID)
	}
}

func TestProcessPurchaseReturnOriginalPaymentRefundCreditsBank(t *testing.T) {
	db := openReturnBalanceTestDB(t)
	userID := uuid.New()

	vendor := models.Party{ID: uuid.New(), UserID: userID, Name: "Vendor", PartyType: "vendor", Balance: -5000}
	if err := db.Create(&vendor).Error; err != nil {
		t.Fatalf("create vendor: %v", err)
	}
	bank := models.BankAccount{
		ID:            uuid.New(),
		UserID:        userID,
		AccountName:   "Main",
		AccountNumber: "001",
		BankName:      "Test Bank",
		IsActive:      true,
		IsPrimary:     true,
		Balance:       10000,
	}
	if err := db.Create(&bank).Error; err != nil {
		t.Fatalf("create bank account: %v", err)
	}
	bill := models.PurchaseBill{
		ID:            uuid.New(),
		UserID:        userID,
		PartyID:       vendor.ID,
		BillNumber:    "PB-0001",
		PaymentMode:   "bank_transfer",
		BankAccountID: &bank.ID,
	}
	if err := db.Create(&bill).Error; err != nil {
		t.Fatalf("create bill: %v", err)
	}

	purchaseReturn := models.PurchaseReturn{
		ID:             uuid.New(),
		UserID:         userID,
		PartyID:        vendor.ID,
		PurchaseBillID: bill.ID,
		ReturnNumber:   "PR-0003",
		Date:           time.Now(),
		Amount:         1200,
		Status:         "draft",
		RefundMode:     "original_payment",
		Items: []models.PurchaseReturnItem{{
			ID:          uuid.New(),
			Description: "Raw material",
			Quantity:    10,
			UnitPrice:   120,
			Total:       1200,
		}},
	}
	if err := db.Create(&purchaseReturn).Error; err != nil {
		t.Fatalf("create purchase return: %v", err)
	}

	c, rec := processContext("/purchase-returns/"+purchaseReturn.ID.String()+"/process", purchaseReturn.ID.String())
	c.Set("user_id", userID)
	ProcessPurchaseReturn(c)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := reloadPartyBalance(t, db, vendor.ID); got != -5000 {
		t.Fatalf("vendor balance = %.2f, want -5000", got)
	}
	var account models.BankAccount
	if err := db.First(&account, "id = ?", bank.ID).Error; err != nil {
		t.Fatalf("reload bank: %v", err)
	}
	if account.Balance != 11200 {
		t.Fatalf("bank balance = %.2f, want 11200", account.Balance)
	}
	var txn models.CashTransaction
	if err := db.Where("user_id = ? AND reference = ?", userID, "PR-0003").First(&txn).Error; err != nil {
		t.Fatalf("load cash txn: %v", err)
	}
	if txn.TransactionType != "add" || txn.Amount != 1200 || txn.AccountID == nil || *txn.AccountID != bank.ID {
		t.Fatalf("cash txn = %+v, want add 1200 on bank %s", txn, bank.ID)
	}
}

func TestProcessSalesReturnCashRefundPaysOutCash(t *testing.T) {
	db := openReturnBalanceTestDB(t)
	userID := uuid.New()

	party := models.Party{ID: uuid.New(), UserID: userID, Name: "Customer", PartyType: "customer", Balance: 1000}
	if err := db.Create(&party).Error; err != nil {
		t.Fatalf("create party: %v", err)
	}

	salesReturn := models.SalesReturn{
		ID:             uuid.New(),
		UserID:         userID,
		PartyID:        party.ID,
		ReturnNumber:   "SR-0003",
		Date:           time.Now(),
		Amount:         300,
		DeductionTotal: 50,
		Status:         "draft",
		RefundMode:     "cash",
		Items: []models.SalesReturnItem{{
			ID:          uuid.New(),
			Description: "Widget",
			Quantity:    2,
			UnitPrice:   150,
			Total:       300,
		}},
	}
	if err := db.Create(&salesReturn).Error; err != nil {
		t.Fatalf("create sales return: %v", err)
	}

	c, rec := processContext("/sales-returns/"+salesReturn.ID.String()+"/process", salesReturn.ID.String())
	c.Set("user_id", userID)
	ProcessSalesReturn(c)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	// Cash refund pays the customer out: receivable is unchanged.
	if got := reloadPartyBalance(t, db, party.ID); got != 1000 {
		t.Fatalf("party balance = %.2f, want 1000", got)
	}
	var txn models.CashTransaction
	if err := db.Where("user_id = ? AND reference = ?", userID, "SR-0003").First(&txn).Error; err != nil {
		t.Fatalf("load cash txn: %v", err)
	}
	// Net refund = 300 - 50 deductions = 250 paid out.
	if txn.TransactionType != "reduce" || txn.Amount != 250 {
		t.Fatalf("cash txn = %+v, want reduce 250", txn)
	}
	if txn.AccountID != nil {
		t.Fatalf("cash refund should pay out of cash in hand, got account %v", txn.AccountID)
	}

	// The refund is documented by a PaymentOut to the customer linked back
	// to the sales return.
	var paymentOut models.PaymentOut
	if err := db.Where("user_id = ? AND reference = ?", userID, "SR-0003").First(&paymentOut).Error; err != nil {
		t.Fatalf("load refund payment out: %v", err)
	}
	if paymentOut.AmountPaid != 250 || paymentOut.Mode != "cash" || paymentOut.PartyID != party.ID {
		t.Fatalf("payment out = %+v, want cash 250 for party %s", paymentOut, party.ID)
	}
	if paymentOut.SalesReturnID == nil || *paymentOut.SalesReturnID != salesReturn.ID {
		t.Fatalf("payment out sales_return_id = %v, want %s", paymentOut.SalesReturnID, salesReturn.ID)
	}
	if paymentOut.PaymentOutNumber == "" {
		t.Fatal("payment out number is empty")
	}
}

func TestDraftSalesReturnDeleteLeavesBalanceUntouched(t *testing.T) {
	db := openReturnBalanceTestDB(t)
	userID := uuid.New()

	party := models.Party{ID: uuid.New(), UserID: userID, Name: "Customer", PartyType: "customer", Balance: 1000}
	if err := db.Create(&party).Error; err != nil {
		t.Fatalf("create party: %v", err)
	}

	salesReturn := models.SalesReturn{
		ID:           uuid.New(),
		UserID:       userID,
		PartyID:      party.ID,
		ReturnNumber: "SR-0002",
		Date:         time.Now(),
		Amount:       300,
		Status:       "draft",
	}
	if err := db.Create(&salesReturn).Error; err != nil {
		t.Fatalf("create sales return: %v", err)
	}

	c, rec := deleteContext("/sales-returns/"+salesReturn.ID.String(), salesReturn.ID.String())
	c.Set("user_id", userID)
	DeleteSalesReturn(c)
	if rec.Code != 200 {
		t.Fatalf("delete status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := reloadPartyBalance(t, db, party.ID); got != 1000 {
		t.Fatalf("party balance after draft delete = %.2f, want 1000", got)
	}
}

func TestProcessSalesReturnCreditSettlesOtherInvoice(t *testing.T) {
	db := openReturnBalanceTestDB(t)
	userID := uuid.New()

	party := models.Party{ID: uuid.New(), UserID: userID, Name: "Customer", PartyType: "customer", Balance: 0}
	if err := db.Create(&party).Error; err != nil {
		t.Fatalf("create party: %v", err)
	}

	// The source invoice is fully paid; the credit goes to a different
	// invoice (exchange scenario).
	sourceInvoice := models.Invoice{
		ID:            uuid.New(),
		UserID:        userID,
		PartyID:       party.ID,
		InvoiceNumber: "INV-0001",
		TotalAmount:   330,
		AmountPaid:    330,
		Status:        "paid",
	}
	if err := db.Create(&sourceInvoice).Error; err != nil {
		t.Fatalf("create source invoice: %v", err)
	}
	settleInvoice := models.Invoice{
		ID:            uuid.New(),
		UserID:        userID,
		PartyID:       party.ID,
		InvoiceNumber: "INV-0002",
		TotalAmount:   830,
		AmountPaid:    500,
		Status:        "partial",
	}
	if err := db.Create(&settleInvoice).Error; err != nil {
		t.Fatalf("create settle invoice: %v", err)
	}

	salesReturn := models.SalesReturn{
		ID:              uuid.New(),
		UserID:          userID,
		PartyID:         party.ID,
		InvoiceID:       sourceInvoice.ID,
		SettleInvoiceID: &settleInvoice.ID,
		ReturnNumber:    "SR-0020",
		Date:            time.Now(),
		Amount:          330,
		Status:          "draft",
		RefundMode:      "credit_note",
		Items: []models.SalesReturnItem{{
			ID:          uuid.New(),
			Description: "Returned item",
			Quantity:    1,
			UnitPrice:   330,
			Total:       330,
		}},
	}
	if err := db.Create(&salesReturn).Error; err != nil {
		t.Fatalf("create sales return: %v", err)
	}

	c, rec := processContext("/sales-returns/"+salesReturn.ID.String()+"/process", salesReturn.ID.String())
	c.Set("user_id", userID)
	ProcessSalesReturn(c)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// Settle invoice fully paid via credit; source invoice untouched.
	var settle models.Invoice
	if err := db.First(&settle, "id = ?", settleInvoice.ID).Error; err != nil {
		t.Fatalf("reload settle invoice: %v", err)
	}
	if settle.AmountPaid != 830 || settle.Status != "paid" {
		t.Fatalf("settle invoice = paid %.2f status %s, want paid 830 paid", settle.AmountPaid, settle.Status)
	}

	// The credit leg shows as a non-cash payment-in on the invoice.
	var payments []models.Payment
	if err := db.Where("user_id = ? AND invoice_id = ?", userID, settleInvoice.ID).Find(&payments).Error; err != nil {
		t.Fatalf("load payments: %v", err)
	}
	if len(payments) != 1 {
		t.Fatalf("payments = %d, want 1 credit settlement row", len(payments))
	}
	if payments[0].Mode != "credit_note" || payments[0].AmountReceived != 330 {
		t.Fatalf("settlement payment = %+v, want credit_note 330", payments[0])
	}

	// Credit settlements move no money: zero cash transactions.
	var txnCount int64
	db.Model(&models.CashTransaction{}).Where("user_id = ?", userID).Count(&txnCount)
	if txnCount != 0 {
		t.Fatalf("cash transactions = %d, want 0 for credit settlement", txnCount)
	}

	// The credit note links back to its sales return so reports can
	// exclude it from double-counted profit math.
	var creditNote models.CreditNote
	if err := db.Where("user_id = ? AND invoice_id = ?", userID, sourceInvoice.ID).First(&creditNote).Error; err != nil {
		t.Fatalf("credit note not created: %v", err)
	}
	if creditNote.SalesReturnID == nil || *creditNote.SalesReturnID != salesReturn.ID {
		t.Fatalf("credit note sales_return_id = %v, want %s", creditNote.SalesReturnID, salesReturn.ID)
	}

	// Party balance holds the granted credit: -330 (the settle invoice was
	// created directly, so it never added its +830 to the balance).
	if got := reloadPartyBalance(t, db, party.ID); got != -330 {
		t.Fatalf("party balance = %.2f, want -330", got)
	}
}

// Regression: creating a return without a linked bill used to fail because
// the zero UUID written to purchase_bill_id violated the FK, and items sent
// with only product_id (no description) failed binding.
func TestCreatePurchaseReturnWithoutBill(t *testing.T) {
	db := openReturnBalanceTestDB(t)
	userID := uuid.New()

	vendor := models.Party{ID: uuid.New(), UserID: userID, Name: "Vendor", PartyType: "vendor"}
	if err := db.Create(&vendor).Error; err != nil {
		t.Fatalf("create vendor: %v", err)
	}
	product := models.Product{ID: uuid.New(), UserID: userID, Name: "Raw material", SKU: "RM-1"}
	if err := db.Create(&product).Error; err != nil {
		t.Fatalf("create product: %v", err)
	}

	c, rec := createContext("/purchase-returns", map[string]interface{}{
		"party_id":         vendor.ID.String(),
		"purchase_bill_id": nil,
		"date":             "2026-10-11",
		"refund_mode":      "cash",
		"items": []map[string]interface{}{{
			"product_id": product.ID.String(),
			"quantity":   2,
			"unit_price": 0,
			"tax_rate":   0,
		}},
	})
	c.Set("user_id", userID)
	CreatePurchaseReturn(c)

	if rec.Code != 201 {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	var created models.PurchaseReturn
	if err := db.Preload("Items").First(&created, "user_id = ?", userID).Error; err != nil {
		t.Fatalf("load return: %v", err)
	}
	var nullCount int64
	db.Raw("SELECT COUNT(*) FROM purchase_returns WHERE id = ? AND purchase_bill_id IS NULL", created.ID).Scan(&nullCount)
	if nullCount != 1 {
		t.Fatalf("purchase_bill_id should be NULL when unlinked, got value %v", created.PurchaseBillID)
	}
	if len(created.Items) != 1 || created.Items[0].Description != "Raw material" {
		t.Fatalf("items = %+v, want description resolved from product", created.Items)
	}
}

func TestCreatePurchaseReturnLinkedBillStoresBillItemLink(t *testing.T) {
	db := openReturnBalanceTestDB(t)
	userID := uuid.New()

	vendor := models.Party{ID: uuid.New(), UserID: userID, Name: "Vendor", PartyType: "vendor"}
	if err := db.Create(&vendor).Error; err != nil {
		t.Fatalf("create vendor: %v", err)
	}
	bill := models.PurchaseBill{
		ID:         uuid.New(),
		UserID:     userID,
		PartyID:    vendor.ID,
		BillNumber: "PB-0001",
	}
	if err := db.Create(&bill).Error; err != nil {
		t.Fatalf("create bill: %v", err)
	}
	billItem := models.PurchaseBillItem{
		ID:          uuid.New(),
		BillID:      bill.ID,
		Description: "Steel rod",
		Quantity:    5,
		UnitPrice:   100,
	}
	if err := db.Create(&billItem).Error; err != nil {
		t.Fatalf("create bill item: %v", err)
	}

	c, rec := createContext("/purchase-returns", map[string]interface{}{
		"party_id":         vendor.ID.String(),
		"purchase_bill_id": bill.ID.String(),
		"date":             "2026-10-11",
		"refund_mode":      "debit_note",
		"items": []map[string]interface{}{{
			"purchase_bill_item_id": billItem.ID.String(),
			"quantity":              5,
			"unit_price":            100,
		}},
	})
	c.Set("user_id", userID)
	CreatePurchaseReturn(c)

	if rec.Code != 201 {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	var created models.PurchaseReturn
	if err := db.Preload("Items").First(&created, "user_id = ?", userID).Error; err != nil {
		t.Fatalf("load return: %v", err)
	}
	if created.PurchaseBillID != bill.ID {
		t.Fatalf("purchase_bill_id = %v, want %s", created.PurchaseBillID, bill.ID)
	}
	if len(created.Items) != 1 || created.Items[0].PurchaseBillItemID != billItem.ID {
		t.Fatalf("items = %+v, want bill item link %s", created.Items, billItem.ID)
	}
	if created.Items[0].Description != "Steel rod" {
		t.Fatalf("description = %q, want resolved from bill item", created.Items[0].Description)
	}
}

func TestCreatePurchaseReturnRejectsCrossVendorBill(t *testing.T) {
	db := openReturnBalanceTestDB(t)
	userID := uuid.New()

	vendor := models.Party{ID: uuid.New(), UserID: userID, Name: "Vendor", PartyType: "vendor"}
	otherVendor := models.Party{ID: uuid.New(), UserID: userID, Name: "Other", PartyType: "vendor"}
	for _, v := range []models.Party{vendor, otherVendor} {
		if err := db.Create(&v).Error; err != nil {
			t.Fatalf("create party: %v", err)
		}
	}
	bill := models.PurchaseBill{ID: uuid.New(), UserID: userID, PartyID: otherVendor.ID, BillNumber: "PB-0009"}
	if err := db.Create(&bill).Error; err != nil {
		t.Fatalf("create bill: %v", err)
	}

	c, rec := createContext("/purchase-returns", map[string]interface{}{
		"party_id":         vendor.ID.String(),
		"purchase_bill_id": bill.ID.String(),
		"date":             "2026-10-11",
		"items": []map[string]interface{}{{
			"description": "Widget",
			"quantity":    1,
			"unit_price":  10,
		}},
	})
	c.Set("user_id", userID)
	CreatePurchaseReturn(c)

	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

func TestCreatePurchaseReturnRejectsItemWithNoDescriptionOrProduct(t *testing.T) {
	db := openReturnBalanceTestDB(t)
	userID := uuid.New()

	vendor := models.Party{ID: uuid.New(), UserID: userID, Name: "Vendor", PartyType: "vendor"}
	if err := db.Create(&vendor).Error; err != nil {
		t.Fatalf("create vendor: %v", err)
	}

	c, rec := createContext("/purchase-returns", map[string]interface{}{
		"party_id": vendor.ID.String(),
		"date":     "2026-10-11",
		"items": []map[string]interface{}{{
			"quantity":   1,
			"unit_price": 10,
		}},
	})
	c.Set("user_id", userID)
	CreatePurchaseReturn(c)

	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

func TestInvoicePaymentsTotalSkipsCreditSettlements(t *testing.T) {
	payments := []models.Payment{
		{AmountReceived: 500, Mode: "cash"},
		{AmountReceived: 330, Mode: "credit_note"},
	}
	if got := invoicePaymentsTotal(payments); got != 500 {
		t.Fatalf("invoicePaymentsTotal = %.2f, want 500 (credit leg excluded)", got)
	}
}
