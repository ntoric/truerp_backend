package controllers

import (
	"testing"
	"time"
	"truerp/models"

	"github.com/google/uuid"
)

func TestFYLabelAndParse(t *testing.T) {
	if got := fyLabel(2026); got != "2026-27" {
		t.Fatalf("fyLabel(2026) = %q, want 2026-27", got)
	}
	y, err := parseFY("2026-27")
	if err != nil || y != 2026 {
		t.Fatalf("parseFY(2026-27) = %d, %v", y, err)
	}
	for _, bad := range []string{"2026-28", "2026", "26-27", "abcd-ef", "2026-2027"} {
		if _, err := parseFY(bad); err == nil {
			t.Fatalf("parseFY(%q) expected error", bad)
		}
	}
}

func TestCurrentIndianFY(t *testing.T) {
	if got := currentIndianFY(time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)); got != "2026-27" {
		t.Fatalf("Apr 2026 -> %q, want 2026-27", got)
	}
	if got := currentIndianFY(time.Date(2027, 3, 31, 0, 0, 0, 0, time.UTC)); got != "2026-27" {
		t.Fatalf("Mar 2027 -> %q, want 2026-27", got)
	}
	if got := currentIndianFY(time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)); got != "2025-26" {
		t.Fatalf("Jan 2026 -> %q, want 2025-26", got)
	}
}

func TestFYBounds(t *testing.T) {
	start, end, err := fyBounds("2026-27")
	if err != nil {
		t.Fatal(err)
	}
	if start != time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("fy start = %v", start)
	}
	if end.Year() != 2027 || end.Month() != 3 || end.Day() != 31 {
		t.Fatalf("fy end = %v", end)
	}
}

func TestQuarterBounds(t *testing.T) {
	cases := []struct {
		q        int
		wantS    time.Time
		wantE    time.Time
	}{
		{1, time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 6, 30, 23, 59, 59, 0, time.UTC)},
		{2, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 30, 23, 59, 59, 0, time.UTC)},
		{3, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC)},
		{4, time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 3, 31, 23, 59, 59, 0, time.UTC)},
	}
	for _, tc := range cases {
		s, e, err := quarterBounds("2026-27", tc.q)
		if err != nil {
			t.Fatal(err)
		}
		if !s.Equal(tc.wantS) || !e.Equal(tc.wantE) {
			t.Fatalf("Q%d = %v..%v, want %v..%v", tc.q, s, e, tc.wantS, tc.wantE)
		}
	}
	if _, _, err := quarterBounds("2026-27", 5); err == nil {
		t.Fatal("quarter 5 should error")
	}
}

func TestCompositionRateFor(t *testing.T) {
	reg := &models.GSTRegistration{CompositionCategory: "trader"}
	if r := compositionRateFor(reg); r != 1.0 {
		t.Fatalf("trader rate = %v, want 1", r)
	}
	reg.CompositionCategory = "restaurant"
	if r := compositionRateFor(reg); r != 5.0 {
		t.Fatalf("restaurant rate = %v, want 5", r)
	}
	reg.CompositionCategory = "service_provider"
	if r := compositionRateFor(reg); r != 6.0 {
		t.Fatalf("service rate = %v, want 6", r)
	}
	reg.CompositionCategory = ""
	if r := compositionRateFor(reg); r != 0 {
		t.Fatalf("empty category rate = %v, want 0", r)
	}
	// Explicit override wins.
	reg.CompositionRate = 2.5
	if r := compositionRateFor(reg); r != 2.5 {
		t.Fatalf("override rate = %v, want 2.5", r)
	}
}

func TestSummarizeSales(t *testing.T) {
	rows := []gstSalesRegisterRow{
		{DocumentType: "sales_invoice", InvoiceStatus: "paid", IncludedInTotals: true,
			GrossAmount: 10000, DiscountAmount: 500, NetAmount: 9500,
			TaxableValue: 8000, NilRatedValue: 1500},
		{DocumentType: "sales_invoice", InvoiceStatus: "cancelled", IncludedInTotals: false,
			GrossAmount: 4000, NetAmount: 4000, TaxableValue: 4000},
		{DocumentType: "sales_return", InvoiceStatus: "processed", IncludedInTotals: true,
			NetAmount: -1000, TaxableValue: -1000},
		{DocumentType: "credit_note", InvoiceStatus: "issued", IncludedInTotals: true,
			NetAmount: -500, TaxableValue: -500},
	}
	s := summarizeSales(rows, "test")
	if s.InvoiceCount != 1 {
		t.Fatalf("invoice_count = %d, want 1", s.InvoiceCount)
	}
	if s.CancelledInvoiceCount != 1 {
		t.Fatalf("cancelled = %d, want 1", s.CancelledInvoiceCount)
	}
	if s.SalesReturnAmount != 1000 {
		t.Fatalf("sales_return_amount = %v, want 1000", s.SalesReturnAmount)
	}
	if s.CreditNoteValue != 500 {
		t.Fatalf("credit_note_value = %v, want 500", s.CreditNoteValue)
	}
	// Taxable: 8000 - 1000 - 500 = 6500
	if s.TaxableSupplyValue != 6500 {
		t.Fatalf("taxable = %v, want 6500", s.TaxableSupplyValue)
	}
	// Turnover basis: taxable 6500 + nil 1500 = 8000
	if s.CompositionTurnoverBasis != 8000 {
		t.Fatalf("basis = %v, want 8000", s.CompositionTurnoverBasis)
	}
}

func TestSummarizePurchases(t *testing.T) {
	rows := []gstPurchaseRegisterRow{
		{DocumentType: "purchase_bill", IncludedInTotals: true, SupplierRegistration: "registered",
			NetAmount: 20000, TaxableValue: 18000, ExemptValue: 2000,
			CGSTAmount: 900, SGSTAmount: 900},
		{DocumentType: "purchase_bill", IncludedInTotals: true, SupplierRegistration: "unregistered",
			NetAmount: 5000, TaxableValue: 5000, ReverseCharge: true,
			RCMTaxableValue: 5000, RCMTaxAmount: 250},
		{DocumentType: "purchase_return", IncludedInTotals: true,
			NetAmount: -2000, TaxableValue: -2000},
	}
	s := summarizePurchases(rows, "test")
	if s.PurchaseInvoiceCount != 2 {
		t.Fatalf("count = %d, want 2", s.PurchaseInvoiceCount)
	}
	if s.RegisteredSupplierPurchaseValue != 20000 {
		t.Fatalf("registered = %v, want 20000", s.RegisteredSupplierPurchaseValue)
	}
	if s.UnregisteredSupplierPurchaseValue != 5000 {
		t.Fatalf("unregistered = %v, want 5000", s.UnregisteredSupplierPurchaseValue)
	}
	if s.PurchaseReturnValue != 2000 {
		t.Fatalf("returns = %v, want 2000", s.PurchaseReturnValue)
	}
	// taxable: 18000 + 5000 - 2000 = 21000
	if s.TaxablePurchaseValue != 21000 {
		t.Fatalf("taxable = %v, want 21000", s.TaxablePurchaseValue)
	}
	if s.SupplierGSTAmount != 1800 {
		t.Fatalf("supplier gst = %v, want 1800", s.SupplierGSTAmount)
	}
	if s.RCMTaxLiability != 250 {
		t.Fatalf("rcm = %v, want 250", s.RCMTaxLiability)
	}
}

func TestComputeCompositionLiability(t *testing.T) {
	reg := &models.GSTRegistration{CompositionCategory: "trader"}
	ss := gstSalesSummary{CompositionTurnoverBasis: 800000}
	ps := gstPurchaseSummary{RCMTaxableValue: 50000, RCMTaxLiability: 2500}
	l := computeCompositionLiability(ss, ps, reg)
	if l.Total != 8000 {
		t.Fatalf("liability = %v, want 8000 (1%% of 800000)", l.Total)
	}
	if l.CGST != 4000 || l.SGST != 4000 {
		t.Fatalf("split = %v/%v, want 4000/4000", l.CGST, l.SGST)
	}
	if l.RCMLiability != 2500 {
		t.Fatalf("rcm = %v, want 2500", l.RCMLiability)
	}
	// Odd paise totals split CGST=floor, SGST=remainder.
	ss.CompositionTurnoverBasis = 333333
	l = computeCompositionLiability(ss, ps, reg)
	if l.Total != 3333.33 {
		t.Fatalf("total = %v, want 3333.33", l.Total)
	}
	if l.CGST+l.SGST != l.Total {
		t.Fatalf("split does not reconcile: %v+%v != %v", l.CGST, l.SGST, l.Total)
	}
	// Negative basis floors at zero.
	ss.CompositionTurnoverBasis = -500
	l = computeCompositionLiability(ss, ps, reg)
	if l.Total != 0 {
		t.Fatalf("negative basis liability = %v, want 0", l.Total)
	}
}

func TestClassifyLineBuckets(t *testing.T) {
	gstProduct := models.Product{ID: uuid.New(), GstEnabled: true, ItemType: "product"}
	nonGSTProduct := models.Product{ID: uuid.New(), GstEnabled: false, ItemType: "product"}
	exemptSvc := models.Product{ID: uuid.New(), GstEnabled: false, ItemType: "service"}

	taxable, nilR, exempt, nonGST := classifyLineBuckets(models.InvoiceItem{Quantity: 2, UnitPrice: 100, TaxRate: 5, Product: gstProduct})
	if taxable != 200 || nilR != 0 || exempt != 0 || nonGST != 0 {
		t.Fatalf("taxable line misclassified: %v %v %v %v", taxable, nilR, exempt, nonGST)
	}
	_, nilR, _, _ = classifyLineBuckets(models.InvoiceItem{Quantity: 2, UnitPrice: 100, TaxRate: 0, Product: gstProduct})
	if nilR != 200 {
		t.Fatalf("nil-rated line = %v, want 200", nilR)
	}
	_, _, _, nonGST = classifyLineBuckets(models.InvoiceItem{Quantity: 1, UnitPrice: 50, TaxRate: 0, Product: nonGSTProduct})
	if nonGST != 50 {
		t.Fatalf("non-gst line = %v, want 50", nonGST)
	}
	_, _, exempt, _ = classifyLineBuckets(models.InvoiceItem{Quantity: 1, UnitPrice: 75, TaxRate: 0, Product: exemptSvc})
	if exempt != 75 {
		t.Fatalf("exempt service line = %v, want 75", exempt)
	}
}

func TestClassifyBuckets(t *testing.T) {
	if got := classifyBuckets(100, 0, 0, 0); got != "taxable" {
		t.Fatalf("got %q", got)
	}
	if got := classifyBuckets(100, 50, 0, 0); got != "mixed" {
		t.Fatalf("got %q", got)
	}
	if got := classifyBuckets(0, 0, 0, 0); got != "unclassified" {
		t.Fatalf("got %q", got)
	}
}
