package controllers

import (
	"math"
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

func setupInvoicePartyBalanceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Party{},
		&models.Invoice{},
		&models.InvoiceItem{},
		&models.InvoiceSettings{},
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

func TestLoadInvoicePartyBalance(t *testing.T) {
	db := setupInvoicePartyBalanceTestDB(t)
	userID := uuid.New()
	partyID := uuid.New()
	base := time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC)

	// Ledger: opening 500, INV-1 1000 (300 paid against it), INV-2 next day
	// 200, then a standalone payment of 100. Stored balance = 500+1000-300+200-100.
	mustCreate := func(v interface{}) {
		t.Helper()
		if err := db.Create(v).Error; err != nil {
			t.Fatalf("create %T: %v", v, err)
		}
	}
	mustCreate(&models.Party{ID: partyID, UserID: userID, Name: "Acme", PartyType: "customer",
		OpeningBalance: 500, Balance: 1300})
	inv1 := models.Invoice{ID: uuid.New(), UserID: userID, PartyID: partyID, InvoiceNumber: "INV-1",
		Date: base, Status: "sent", TotalAmount: 1000, AmountPaid: 300, CreatedAt: base}
	mustCreate(&inv1)
	mustCreate(&models.Payment{ID: uuid.New(), UserID: userID, PartyID: partyID, InvoiceID: &inv1.ID,
		Date: base, AmountReceived: 300, CreatedAt: base.Add(time.Second)})
	inv2 := models.Invoice{ID: uuid.New(), UserID: userID, PartyID: partyID, InvoiceNumber: "INV-2",
		Date: base.AddDate(0, 0, 1), Status: "sent", TotalAmount: 200, CreatedAt: base.AddDate(0, 0, 1)}
	mustCreate(&inv2)
	mustCreate(&models.Payment{ID: uuid.New(), UserID: userID, PartyID: partyID,
		Date: base.AddDate(0, 0, 2), AmountReceived: 100, CreatedAt: base.AddDate(0, 0, 2)})

	near := func(a, b float64) bool { return math.Abs(a-b) < 0.001 }

	pb, ok := loadInvoicePartyBalance(inv1)
	if !ok || !near(pb.Previous, 500) || !near(pb.Current, 1200) {
		t.Fatalf("INV-1 balance = %+v ok=%v, want previous 500 current 1200", pb, ok)
	}
	pb, ok = loadInvoicePartyBalance(inv2)
	if !ok || !near(pb.Previous, 1200) || !near(pb.Current, 1400) {
		t.Fatalf("INV-2 balance = %+v ok=%v, want previous 1200 current 1400", pb, ok)
	}

	// Drafts are not posted: previous is today's balance, current projects it.
	draft := models.Invoice{ID: uuid.New(), UserID: userID, PartyID: partyID, InvoiceNumber: "INV-3",
		Date: base.AddDate(0, 0, 3), Status: "draft", TotalAmount: 50, CreatedAt: base.AddDate(0, 0, 3)}
	mustCreate(&draft)
	pb, ok = loadInvoicePartyBalance(draft)
	if !ok || !near(pb.Previous, 1300) || !near(pb.Current, 1350) {
		t.Fatalf("draft balance = %+v ok=%v, want previous 1300 current 1350", pb, ok)
	}

	inv1.Status = "cancelled"
	if _, ok := loadInvoicePartyBalance(inv1); ok {
		t.Fatalf("cancelled invoice should not report a party balance")
	}
}

func TestInvoicePDFPartyBalanceToggle(t *testing.T) {
	db := setupInvoicePartyBalanceTestDB(t)
	userID := uuid.New()
	partyID := uuid.New()
	base := time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC)
	if err := db.Create(&models.Party{ID: partyID, UserID: userID, Name: "Acme", PartyType: "customer", Balance: 700}).Error; err != nil {
		t.Fatalf("party: %v", err)
	}
	inv := models.Invoice{ID: uuid.New(), UserID: userID, PartyID: partyID, InvoiceNumber: "INV-1",
		Date: base, Status: "sent", TotalAmount: 200, CreatedAt: base}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatalf("invoice: %v", err)
	}

	render := func(show bool) string {
		custom := utils.DefaultInvoiceTemplateCustomization()
		custom.ThemeSettings.ShowPartyBalance = show
		return InvoicePDFHTMLWithOptions(inv, invoicePDFOptions{
			Settings: models.InvoiceSettings{Customization: utils.EncodeInvoiceTemplateCustomization(custom)},
		})
	}
	if out := render(false); strings.Contains(out, "Previous Balance") {
		t.Fatalf("party balance rendered while disabled")
	}
	out := render(true)
	if !strings.Contains(out, "Previous Balance") || !strings.Contains(out, "₹ 500") ||
		!strings.Contains(out, "Current Balance") || !strings.Contains(out, "₹ 700") {
		t.Fatalf("party balance rows missing or wrong when enabled")
	}
}
