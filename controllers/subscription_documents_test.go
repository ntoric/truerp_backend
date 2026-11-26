package controllers

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"truerp/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestSubscriptionDocNumbersAreSequentialPerYearAndPrefix(t *testing.T) {
	db, store := setupSubscriptionTestDB(t)
	sub := makeSub(t, db, store, "2026-01-05", true, 999)
	rollSubscription(db, &sub, "2026-03-10") // cycles 2 and 3

	var cycles []models.SubscriptionCycle
	db.Order("cycle_number").Find(&cycles)
	want := []string{"SINV-2026-00001", "SINV-2026-00002", "SINV-2026-00003"}
	for i, c := range cycles {
		if c.InvoiceNumber != want[i] || c.InvoiceDate != c.PeriodStart {
			t.Fatalf("cycle %d: number %q date %q", c.CycleNumber, c.InvoiceNumber, c.InvoiceDate)
		}
	}

	p1, _ := recordSubPayment(db, &sub, cycles[0].ID, subscriptionPaymentInput{Amount: 999, PaymentDate: "2026-01-05", Method: "upi"}, uuid.New())
	p2, _ := recordSubPayment(db, &sub, cycles[1].ID, subscriptionPaymentInput{Amount: 999, PaymentDate: "2027-01-02", Method: "upi"}, uuid.New())
	if p1.ReceiptNumber != "SRCPT-2026-00001" || p2.ReceiptNumber != "SRCPT-2027-00001" {
		t.Fatalf("receipts: %q %q", p1.ReceiptNumber, p2.ReceiptNumber)
	}

	// A new prefix starts its own sequence.
	settings := models.DefaultSubscriptionBillingSettings()
	settings.ID, settings.InvoicePrefix, settings.NumberPadding = uuid.New(), "TRP", 4
	db.Create(&settings)
	n, err := models.NextSubscriptionDocNumber(db, "invoice", "2026-06-01")
	if err != nil || n != "TRP-2026-0001" {
		t.Fatalf("custom prefix number %q %v", n, err)
	}
}

func TestSubInvoiceTaxSplit(t *testing.T) {
	s := models.DefaultSubscriptionBillingSettings()
	s.TaxRate, s.StateCode = 18, "29"
	cy := models.SubscriptionCycle{Amount: 1180, AmountPaid: 500}

	intra := computeSubInvoiceTotals(s, models.Store{StateCode: "29"}, cy)
	if !intra.IntraState || intra.Taxable != 1000 || intra.CGST != 90 || intra.SGST != 90 || intra.Balance != 680 {
		t.Fatalf("intra-state: %+v", intra)
	}
	inter := computeSubInvoiceTotals(s, models.Store{StateCode: "27"}, cy)
	if inter.IntraState || inter.IGST != 180 || inter.CGST != 0 {
		t.Fatalf("inter-state: %+v", inter)
	}
	free := computeSubInvoiceTotals(s, models.Store{}, models.SubscriptionCycle{Amount: 0, WaivedAmount: 1180, Complimentary: true})
	if free.Total != 0 || free.Discount != 1180 || free.Gross != 1180 || free.IGST != 0 {
		t.Fatalf("complimentary: %+v", free)
	}
}

func tinyPNGDataURL(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 40, 20))
	for x := 0; x < 40; x++ {
		for y := 0; y < 20; y++ {
			img.Set(x, y, color.RGBA{200, 50, 50, 255})
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestSubscriptionPDFsRender(t *testing.T) {
	db, store := setupSubscriptionTestDB(t)
	s := models.DefaultSubscriptionBillingSettings()
	s.ID, s.TaxRate, s.GSTIN, s.LogoData, s.ShowBankDetails, s.BankName, s.InvoiceTerms =
		uuid.New(), 18, "29ABCDE1234F1Z5", tinyPNGDataURL(t), true, "HDFC Bank", "Payable within 7 days."
	db.Create(&s)

	sub := makeSub(t, db, store, "2026-10-01", true, 1499)
	var cycle models.SubscriptionCycle
	db.First(&cycle)
	pay, _ := recordSubPayment(db, &sub, cycle.ID, subscriptionPaymentInput{Amount: 500, PaymentDate: "2026-10-02", Method: "upi", Reference: "UPI-1"}, uuid.New())

	d, err := loadSubDoc(sub.ID, cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.invoiceTitle() != "Tax Invoice" {
		t.Fatalf("title %q", d.invoiceTitle())
	}
	inv, err := buildSubscriptionInvoicePDF(d)
	if err != nil || !bytes.HasPrefix(inv, []byte("%PDF")) || len(inv) < 2000 {
		t.Fatalf("invoice pdf: %v (%d bytes)", err, len(inv))
	}
	d.Payment = &pay
	rc, err := buildSubscriptionReceiptPDF(d)
	if err != nil || !bytes.HasPrefix(rc, []byte("%PDF")) {
		t.Fatalf("receipt pdf: %v", err)
	}

	// Complimentary invoice renders too.
	d.Cycle.Complimentary, d.Cycle.WaivedAmount, d.Cycle.Amount, d.Payments = true, 1499, 0, nil
	if out, err := buildSubscriptionInvoicePDF(d); err != nil || !bytes.HasPrefix(out, []byte("%PDF")) {
		t.Fatalf("complimentary invoice: %v", err)
	}
}

func TestMyPlanDocumentsAreStoreScoped(t *testing.T) {
	db, store := setupSubscriptionTestDB(t)
	sub := makeSub(t, db, store, "2026-10-01", true, 999)
	var cycle models.SubscriptionCycle
	db.First(&cycle)

	other := models.Store{ID: uuid.New(), Name: "Other", Code: "OTHER", OwnerUserID: uuid.New(), IsActive: true}
	db.Create(&other)

	get := func(storeID uuid.UUID) int {
		gin.SetMode(gin.TestMode)
		r := gin.New()
		r.Use(func(c *gin.Context) { c.Set("store_id", storeID); c.Next() })
		r.GET("/my-plan/invoices/:cycleId", MyPlanInvoicePDF)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/my-plan/invoices/"+cycle.ID.String(), nil))
		if w.Code == http.StatusOK && !strings.HasPrefix(w.Body.String(), "%PDF") {
			t.Fatal("expected a PDF body")
		}
		return w.Code
	}
	if code := get(store.ID); code != http.StatusOK {
		t.Fatalf("own store: %d", code)
	}
	if code := get(other.ID); code != http.StatusNotFound {
		t.Fatalf("other store should not see the invoice, got %d", code)
	}
	_ = sub
}

func TestValidateBillingSettings(t *testing.T) {
	base := func() models.SubscriptionBillingSettings { return models.DefaultSubscriptionBillingSettings() }
	cases := map[string]func(*models.SubscriptionBillingSettings){
		"same prefix":  func(s *models.SubscriptionBillingSettings) { s.ReceiptPrefix = s.InvoicePrefix },
		"bad colour":   func(s *models.SubscriptionBillingSettings) { s.AccentColor = "navy" },
		"bad tax":      func(s *models.SubscriptionBillingSettings) { s.TaxRate = 80 },
		"no name":      func(s *models.SubscriptionBillingSettings) { s.IssuerName = " " },
		"bad prefix":   func(s *models.SubscriptionBillingSettings) { s.InvoicePrefix = "INV-#" },
		"not an image": func(s *models.SubscriptionBillingSettings) { s.LogoData = "data:text/plain;base64,aGk=" },
	}
	for name, mut := range cases {
		s := base()
		mut(&s)
		if validateBillingSettings(&s) == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	ok := base()
	ok.InvoicePrefix, ok.GSTIN = "inv", "29abcde1234f1z5"
	if err := validateBillingSettings(&ok); err != nil || ok.InvoicePrefix != "INV" || ok.GSTIN != "29ABCDE1234F1Z5" {
		t.Fatalf("valid settings rejected or not normalised: %v %+v", err, ok)
	}
}
