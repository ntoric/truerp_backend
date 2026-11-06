package controllers

import (
	"strings"
	"testing"
	"time"
	"truerp/models"
	"truerp/utils"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestAmountInWordsINR(t *testing.T) {
	cases := map[float64]string{
		0:         "Rupees Zero Only",
		1:         "Rupees One Only",
		1234:      "Rupees One Thousand Two Hundred Thirty Four Only",
		123456.78: "Rupees One Lakh Twenty Three Thousand Four Hundred Fifty Six and Paise Seventy Eight Only",
		10000000:  "Rupees One Crore Only",
		250.5:     "Rupees Two Hundred Fifty and Paise Fifty Only",
	}
	for amount, want := range cases {
		if got := amountInWordsINR(amount); got != want {
			t.Errorf("amountInWordsINR(%v) = %q, want %q", amount, got, want)
		}
	}
	if got := amountWordsRupees(700); got != "Seven Hundred Rupees" {
		t.Errorf("amountWordsRupees(700) = %q", got)
	}
}

func TestFinancialYearLabel(t *testing.T) {
	if got := financialYearLabel(time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)); got != "FY 2026-27" {
		t.Fatalf("expected FY 2026-27, got %s", got)
	}
	if got := financialYearLabel(time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC)); got != "FY 2026-27" {
		t.Fatalf("expected FY 2026-27, got %s", got)
	}
	if got := financialYearLabel(time.Date(2027, 4, 1, 0, 0, 0, 0, time.UTC)); got != "FY 2027-28" {
		t.Fatalf("expected FY 2027-28, got %s", got)
	}
}

func TestInvoiceDocumentTitle(t *testing.T) {
	if got := invoiceDocumentTitle("tax_invoice"); got != "TAX INVOICE" {
		t.Fatalf("got %s", got)
	}
	if got := invoiceDocumentTitle("bill_of_supply"); got != "BILL OF SUPPLY" {
		t.Fatalf("got %s", got)
	}
	if got := invoiceDocumentTitle(""); got != "TAX INVOICE" {
		t.Fatalf("got %s", got)
	}
}

func TestPlaceOfSupplyText(t *testing.T) {
	inv := models.Invoice{PlaceOfSupply: "27"}
	if got := placeOfSupplyText(inv); got != "Maharashtra (27)" {
		t.Fatalf("expected state name + code, got %q", got)
	}
	inv = models.Invoice{Party: models.Party{StateCode: "33"}}
	if got := placeOfSupplyText(inv); got != "Tamil Nadu (33)" {
		t.Fatalf("expected party state fallback, got %q", got)
	}
	inv = models.Invoice{Party: models.Party{GSTIN: "29ABCDE1234F1Z5"}}
	if got := placeOfSupplyText(inv); got != "Karnataka (29)" {
		t.Fatalf("expected GSTIN-derived state, got %q", got)
	}
}

func TestPANFromGSTIN(t *testing.T) {
	if got := panFromGSTIN("33GCXPP7235R1ZC"); got != "GCXPP7235R" {
		t.Fatalf("got %q", got)
	}
	if got := panFromGSTIN("short"); got != "" {
		t.Fatalf("expected empty PAN for invalid GSTIN, got %q", got)
	}
}

func gstTestInvoice() models.Invoice {
	return models.Invoice{
		InvoiceNumber: "INV-1",
		InvoiceType:   "tax_invoice",
		Date:          time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC),
		Status:        "paid",
		SubTotal:      1000,
		TaxTotal:      180,
		CGSTTotal:     90,
		SGSTTotal:     90,
		TotalAmount:   1180,
		PlaceOfSupply: "27",
		Party: models.Party{
			Name:      "Acme Traders",
			Address:   "12 MG Road",
			City:      "Pune",
			State:     "Maharashtra",
			Pincode:   "411001",
			StateCode: "27",
			GSTIN:     "27ABCDE1234F1Z5",
		},
		Items: []models.InvoiceItem{
			{
				Description: "Widget",
				Quantity:    2,
				Unit:        "PCS",
				UnitPrice:   500,
				TaxRate:     18,
				CGST:        90,
				SGST:        90,
				HSNCode:     "8471",
				Total:       1180,
				Product:     models.Product{MRP: 550},
			},
		},
	}
}

func gstTestBusiness() *models.Business {
	return &models.Business{
		Name:      "Seller Enterprises",
		Address:   "1 Market Street",
		City:      "Mumbai",
		State:     "Maharashtra",
		Pincode:   "400001",
		StateCode: "27",
		GSTIN:     "27ZZZZZ9999Z1Z1",
		Phone:     "9999999999",
		UPIID:     "seller@upi",
		BankName:  "Test Bank",
	}
}

func TestInvoicePDFHTMLBillOfSupplyLayout(t *testing.T) {
	invoice := gstTestInvoice()
	invoice.InvoiceType = "bill_of_supply"
	invoice.TaxTotal, invoice.CGSTTotal, invoice.SGSTTotal = 0, 0, 0
	invoice.Items[0].CGST, invoice.Items[0].SGST, invoice.Items[0].TaxRate = 0, 0, 0
	out := InvoicePDFHTMLWithOptions(invoice, invoicePDFOptions{Business: gstTestBusiness()})
	for _, want := range []string{
		"BILL OF SUPPLY", "ORIGINAL FOR RECIPIENT",
		"Composition taxable person, not eligible to collect tax on supplies.",
		"Seller Enterprises", "GSTIN: 27ZZZZZ9999Z1Z1", "PAN Number: ZZZZZ9999Z",
		"Invoice No.: INV-1", "Invoice Date: 07/11/2026",
		"BILL TO", "SHIP TO", "Acme Traders",
		"GSTIN/UIN: 27ABCDE1234F1Z5", "Maharashtra (Code: 27)",
		"Place of Supply: Maharashtra (27)",
		"ITEMS", "QTY.", "MRP", "RATE", "AMOUNT", "SUBTOTAL",
		"Total Amount", "Total Amount (in words)",
		"One Thousand One Hundred Eighty Rupees",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("bill-of-supply HTML missing %q", want)
		}
	}
	// Bill of supply must not show the tax split columns or meta.
	for _, absent := range []string{"<th>CGST</th>", "<th>IGST</th>", "Reverse Charge:", "Financial Year"} {
		if strings.Contains(out, absent) {
			t.Errorf("bill-of-supply HTML should not contain %q", absent)
		}
	}
}

func TestInvoicePDFHTMLTaxInvoiceGSTLayout(t *testing.T) {
	out := InvoicePDFHTMLWithOptions(gstTestInvoice(), invoicePDFOptions{Business: gstTestBusiness()})
	for _, want := range []string{
		"TAX INVOICE", "ORIGINAL FOR RECIPIENT",
		"FY 2026-27", "Reverse Charge:", "Place of Supply",
		"<th>CGST</th>", "<th>SGST</th>", "HSN/SAC", "GROSS", "TAXABLE", "GST%",
		"Total Taxable Value", "SUBTOTAL",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("tax-invoice HTML missing %q", want)
		}
	}
	if strings.Contains(out, "Composition taxable person") {
		t.Error("tax invoice must not show the composition declaration")
	}
}

func TestBuildInvoiceDocumentPDFBillOfSupply(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	previousDB := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = previousDB })

	invoice := gstTestInvoice()
	invoice.InvoiceType = "bill_of_supply"
	invoice.TaxTotal, invoice.CGSTTotal, invoice.SGSTTotal = 0, 0, 0
	invoice.Items[0].CGST, invoice.Items[0].SGST, invoice.Items[0].TaxRate = 0, 0, 0
	invoice.ShippingAddress = "Warehouse 4, Nagpur 440001"

	pdfBytes, err := buildInvoiceDocumentPDF(invoice, gstTestBusiness(), models.PrintSettings{PrintHeader: true})
	if err != nil {
		t.Fatalf("buildInvoiceDocumentPDF: %v", err)
	}
	if len(pdfBytes) < 5 || string(pdfBytes[:5]) != "%PDF-" {
		t.Fatalf("expected PDF output, got %d bytes", len(pdfBytes))
	}

	// Taxable inter-state + cess variant must also render.
	invoice = gstTestInvoice()
	invoice.IsInterState = true
	invoice.Items[0].CessRate = 1
	invoice.Items[0].CessAmount = 10
	invoice.CessTotal = 10
	if _, err := buildInvoiceDocumentPDF(invoice, gstTestBusiness(), models.PrintSettings{PaperSize: "letter", Orientation: "landscape"}); err != nil {
		t.Fatalf("inter-state buildInvoiceDocumentPDF: %v", err)
	}
}
