package controllers

import (
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

func openPartyLedgerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
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
		&models.PaymentOut{},
		&models.SalesReturn{},
		&models.PurchaseBill{},
		&models.PurchaseReturn{},
		&models.CreditNote{},
		&models.DebitNote{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	previousDB := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = previousDB })
	return db
}

func ledgerGetContext(path, partyID string) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("GET", path, nil)
	c.Params = gin.Params{{Key: "id", Value: partyID}}
	return c, rec
}

func TestPartyLedgerChronologyAndTotals(t *testing.T) {
	db := openPartyLedgerTestDB(t)
	userID := uuid.New()
	day := func(d int) time.Time { return time.Date(2026, 9, d, 10, 0, 0, 0, time.UTC) }

	party := models.Party{
		ID: uuid.New(), UserID: userID, Name: "EDAYAR PALAYAM MARIO",
		PartyType: "customer", OpeningBalance: 15000, Balance: 63360,
	}
	if err := db.Create(&party).Error; err != nil {
		t.Fatalf("create party: %v", err)
	}

	mk := func(v interface{}) {
		t.Helper()
		if err := db.Create(v).Error; err != nil {
			t.Fatalf("create %T: %v", v, err)
		}
	}

	mk(&models.Invoice{ID: uuid.New(), UserID: userID, PartyID: party.ID,
		InvoiceNumber: "1216", Date: day(2), Status: "paid", TotalAmount: 38588, AmountPaid: 38588})
	mk(&models.Payment{ID: uuid.New(), UserID: userID, PartyID: party.ID,
		PaymentInNumber: "565", Date: day(4), Mode: "cash", AmountReceived: 9500})
	mk(&models.Invoice{ID: uuid.New(), UserID: userID, PartyID: party.ID,
		InvoiceNumber: "1242", Date: day(4), Status: "paid", TotalAmount: 4125, AmountPaid: 4125})
	mk(&models.SalesReturn{ID: uuid.New(), UserID: userID, PartyID: party.ID,
		ReturnNumber: "7", Date: day(14), Status: "processed", Amount: 1495})
	// Draft invoice must not appear.
	mk(&models.Invoice{ID: uuid.New(), UserID: userID, PartyID: party.ID,
		InvoiceNumber: "9999", Date: day(15), Status: "draft", TotalAmount: 777})

	c, rec := ledgerGetContext("/parties/"+party.ID.String()+"/ledger?from_date=2026-09-01&to_date=2026-09-30", party.ID.String())
	c.Set("user_id", userID)
	GetPartyLedger(c)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var ledger PartyLedger
	if err := json.Unmarshal(rec.Body.Bytes(), &ledger); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if ledger.OpeningBalance != 15000 {
		t.Fatalf("opening = %.2f, want 15000", ledger.OpeningBalance)
	}
	// 15000 + 38588 - 9500 + 4125 - 1495 = 46718
	if ledger.ClosingBalance != 46718 {
		t.Fatalf("closing = %.2f, want 46718", ledger.ClosingBalance)
	}
	if ledger.TotalSales != 42713 {
		t.Fatalf("total_sales = %.2f, want 42713", ledger.TotalSales)
	}
	if ledger.TotalReceived != 9500 {
		t.Fatalf("total_received = %.2f, want 9500", ledger.TotalReceived)
	}
	if len(ledger.Entries) != 6 { // opening + 4 docs + closing
		t.Fatalf("entries = %d, want 6", len(ledger.Entries))
	}
	if ledger.Entries[0].Type != "opening_balance" || ledger.Entries[len(ledger.Entries)-1].Type != "closing_balance" {
		t.Fatalf("first/last entry types = %s/%s", ledger.Entries[0].Type, ledger.Entries[len(ledger.Entries)-1].Type)
	}
	if ledger.Entries[1].Voucher != "Sales Invoices" || ledger.Entries[1].RefNumber != "1216" {
		t.Fatalf("first doc entry = %s %s", ledger.Entries[1].Voucher, ledger.Entries[1].RefNumber)
	}
	if ledger.Entries[2].Voucher != "Payment In" || ledger.Entries[2].PaymentMode != "cash" {
		t.Fatalf("payment entry = %s mode %s", ledger.Entries[2].Voucher, ledger.Entries[2].PaymentMode)
	}
	if ledger.Entries[4].Voucher != "Sales Return" || ledger.Entries[4].Credit != 1495 {
		t.Fatalf("return entry = %s credit %.2f", ledger.Entries[4].Voucher, ledger.Entries[4].Credit)
	}
	if ledger.Entries[4].Balance != 46718 {
		t.Fatalf("running balance = %.2f, want 46718", ledger.Entries[4].Balance)
	}
}

func TestPartyLedgerVendorAndOpeningDelta(t *testing.T) {
	db := openPartyLedgerTestDB(t)
	userID := uuid.New()
	day := func(d int) time.Time { return time.Date(2026, 9, d, 10, 0, 0, 0, time.UTC) }

	vendor := models.Party{ID: uuid.New(), UserID: userID, Name: "Vendor Co", PartyType: "vendor"}
	if err := db.Create(&vendor).Error; err != nil {
		t.Fatalf("create vendor: %v", err)
	}
	mk := func(v interface{}) {
		t.Helper()
		if err := db.Create(v).Error; err != nil {
			t.Fatalf("create %T: %v", v, err)
		}
	}

	// Pre-range purchase bill pushes payable into the opening balance.
	mk(&models.PurchaseBill{ID: uuid.New(), UserID: userID, PartyID: vendor.ID,
		BillNumber: "P-100", BillDate: day(-10), Status: "unpaid", TotalAmount: 20000})
	// In-range docs.
	mk(&models.PurchaseBill{ID: uuid.New(), UserID: userID, PartyID: vendor.ID,
		BillNumber: "P-101", BillDate: day(5), Status: "partial", TotalAmount: 10000, PaidAmount: 4000})
	mk(&models.PaymentOut{ID: uuid.New(), UserID: userID, PartyID: vendor.ID,
		PaymentOutNumber: "POUT-1", Date: day(6), Mode: "upi", AmountPaid: 4000})
	mk(&models.PurchaseReturn{ID: uuid.New(), UserID: userID, PartyID: vendor.ID,
		ReturnNumber: "PR-1", Date: day(7), Status: "processed", Amount: 1200})

	c, rec := ledgerGetContext("/parties/"+vendor.ID.String()+"/ledger?from_date=2026-09-01&to_date=2026-09-30", vendor.ID.String())
	c.Set("user_id", userID)
	GetPartyLedger(c)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var ledger PartyLedger
	if err := json.Unmarshal(rec.Body.Bytes(), &ledger); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if ledger.OpeningBalance != -20000 {
		t.Fatalf("opening = %.2f, want -20000 (pre-range bill)", ledger.OpeningBalance)
	}
	// -20000 - 10000 + 4000 + 1200 = -24800
	if ledger.ClosingBalance != -24800 {
		t.Fatalf("closing = %.2f, want -24800", ledger.ClosingBalance)
	}
	if ledger.TotalPurchases != 10000 || ledger.TotalPaid != 4000 {
		t.Fatalf("totals purchases/paid = %.2f/%.2f", ledger.TotalPurchases, ledger.TotalPaid)
	}
	if len(ledger.Entries) != 5 {
		t.Fatalf("entries = %d, want 5", len(ledger.Entries))
	}
	// Opening row shows the payable in the credit column.
	if ledger.Entries[0].Credit != 20000 {
		t.Fatalf("opening credit = %.2f, want 20000", ledger.Entries[0].Credit)
	}
}

func TestPartyLedgerUnknownParty(t *testing.T) {
	openPartyLedgerTestDB(t)
	userID := uuid.New()

	c, rec := ledgerGetContext("/parties/"+uuid.NewString()+"/ledger", uuid.NewString())
	c.Set("user_id", userID)
	GetPartyLedger(c)
	if rec.Code != 404 {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}
