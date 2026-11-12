package controllers

import (
	"testing"
	"time"
	"truerp/models"

	"github.com/google/uuid"
)

func TestPartyBalanceReportMatchesLedgerAndSkipsZero(t *testing.T) {
	db := openPartyLedgerTestDB(t)
	userID := uuid.New()
	day := func(d int) time.Time { return time.Date(2026, 9, d, 10, 0, 0, 0, time.UTC) }
	mk := func(v interface{}) {
		t.Helper()
		if err := db.Create(v).Error; err != nil {
			t.Fatalf("create %T: %v", v, err)
		}
	}

	customer := models.Party{ID: uuid.New(), UserID: userID, Name: "Customer A", PartyType: "customer", OpeningBalance: 1000}
	vendor := models.Party{ID: uuid.New(), UserID: userID, Name: "Vendor B", PartyType: "vendor"}
	settled := models.Party{ID: uuid.New(), UserID: userID, Name: "Zero C", PartyType: "customer"}
	imported := models.Party{ID: uuid.New(), UserID: userID, Name: "Imported D", PartyType: "customer", Balance: 750}
	otherUser := models.Party{ID: uuid.New(), UserID: uuid.New(), Name: "Other", PartyType: "customer", OpeningBalance: 99}
	for _, p := range []*models.Party{&customer, &vendor, &settled, &imported, &otherUser} {
		mk(p)
	}

	// Customer: invoice before the day, payment + sales return on the day,
	// invoice after the day (ignored), a cancelled invoice (ignored).
	mk(&models.Invoice{ID: uuid.New(), UserID: userID, PartyID: customer.ID, InvoiceNumber: "1", Date: day(1), Status: "unpaid", TotalAmount: 5000})
	mk(&models.Payment{ID: uuid.New(), UserID: userID, PartyID: customer.ID, PaymentInNumber: "P1", Date: day(5), AmountReceived: 2000, PaymentInDiscount: 100})
	mk(&models.SalesReturn{ID: uuid.New(), UserID: userID, PartyID: customer.ID, ReturnNumber: "SR1", Date: day(5), Status: "processed", Amount: 500, DeductionTotal: 50})
	mk(&models.Invoice{ID: uuid.New(), UserID: userID, PartyID: customer.ID, InvoiceNumber: "2", Date: day(6), Status: "unpaid", TotalAmount: 9999})
	mk(&models.Invoice{ID: uuid.New(), UserID: userID, PartyID: customer.ID, InvoiceNumber: "3", Date: day(2), Status: "cancelled", TotalAmount: 7777})

	// Vendor: bill before, payment out on the day.
	mk(&models.PurchaseBill{ID: uuid.New(), UserID: userID, PartyID: vendor.ID, BillNumber: "B1", BillDate: day(2), Status: "unpaid", TotalAmount: 3000})
	mk(&models.PaymentOut{ID: uuid.New(), UserID: userID, PartyID: vendor.ID, PaymentOutNumber: "PO1", Date: day(5), AmountPaid: 1000})

	// Settled: invoice fully paid before the day → zero both sides.
	mk(&models.Invoice{ID: uuid.New(), UserID: userID, PartyID: settled.ID, InvoiceNumber: "4", Date: day(1), Status: "paid", TotalAmount: 400})
	mk(&models.Payment{ID: uuid.New(), UserID: userID, PartyID: settled.ID, PaymentInNumber: "P2", Date: day(1), AmountReceived: 400})

	report, err := loadPartyBalanceReport(userID, "2026-09-05", "all", "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(report.Lines) != 3 {
		t.Fatalf("want 3 lines (zero-balance party skipped), got %d: %+v", len(report.Lines), report.Lines)
	}

	byName := map[string]PartyBalanceLine{}
	for _, l := range report.Lines {
		byName[l.PartyName] = l
	}
	check := func(name string, opening, debit, credit, closing float64) {
		t.Helper()
		l, ok := byName[name]
		if !ok {
			t.Fatalf("missing line %q", name)
		}
		if l.OpeningBalance != opening || l.Debit != debit || l.Credit != credit || l.ClosingBalance != closing {
			t.Fatalf("%s: got open=%v dr=%v cr=%v close=%v; want %v %v %v %v",
				name, l.OpeningBalance, l.Debit, l.Credit, l.ClosingBalance, opening, debit, credit, closing)
		}
	}
	check("Customer A", 6000, 0, 2350, 3650)
	check("Vendor B", -3000, 1000, 0, -2000)
	check("Imported D", 750, 0, 0, 750)
	if byName["Customer A"].Difference != -2350 || byName["Vendor B"].Difference != 1000 || byName["Imported D"].Difference != 0 {
		t.Fatalf("difference: %+v", report.Lines)
	}
	if report.NetDifference != 3650+750-2000-(6000+750-3000) {
		t.Fatalf("net difference: %v", report.NetDifference)
	}

	// Each line reconciles with the party ledger for the same day.
	for _, p := range []models.Party{customer, vendor, imported} {
		ledger, err := loadPartyLedger(userID, p.ID, "2026-09-05", "2026-09-05")
		if err != nil {
			t.Fatalf("ledger: %v", err)
		}
		l := byName[p.Name]
		if ledger.OpeningBalance != l.OpeningBalance || ledger.ClosingBalance != l.ClosingBalance {
			t.Fatalf("%s: ledger %v→%v, report %v→%v", p.Name, ledger.OpeningBalance, ledger.ClosingBalance, l.OpeningBalance, l.ClosingBalance)
		}
	}

	if report.OpeningReceivable != 6750 || report.OpeningPayable != 3000 ||
		report.ClosingReceivable != 4400 || report.ClosingPayable != 2000 {
		t.Fatalf("totals: %+v", report)
	}

	vendorsOnly, _ := loadPartyBalanceReport(userID, "2026-09-05", "vendor", "")
	if len(vendorsOnly.Lines) != 1 || vendorsOnly.Lines[0].PartyName != "Vendor B" {
		t.Fatalf("vendor filter: %+v", vendorsOnly.Lines)
	}

	if _, err := buildPartyBalanceReportPDF(report); err != nil {
		t.Fatalf("pdf: %v", err)
	}
}
