package controllers

import (
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

func setupGSTTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:gstcomp-%s?mode=memory&cache=shared", uuid.New())), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	err = db.AutoMigrate(
		&models.Invoice{}, &models.InvoiceItem{},
		&models.PurchaseBill{}, &models.PurchaseBillItem{},
		&models.SalesReturn{}, &models.SalesReturnItem{},
		&models.PurchaseReturn{}, &models.PurchaseReturnItem{},
		&models.CreditNote{}, &models.CreditNoteItem{},
		&models.DebitNote{}, &models.DebitNoteItem{},
		&models.Party{}, &models.Product{}, &models.Business{},
		&models.GSTRegistration{}, &models.GSTReturnPeriod{},
		&models.GSTCalculationSnapshot{}, &models.GSTValidationIssue{},
		&models.GSTTaxPayment{}, &models.GSTReconciliationRecord{},
		&models.GSTReportExport{},
	)
	if err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	prev := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = prev })
	return db
}

type gstFixture struct {
	userID       uuid.UUID
	reg          *models.GSTRegistration
	customer     models.Party
	supplierReg  models.Party
	supplierURD  models.Party
	productGST   models.Product
	productNoGST models.Product
}

func seedGSTFixture(t *testing.T, db *gorm.DB) *gstFixture {
	f := &gstFixture{userID: uuid.New()}
	reg := models.GSTRegistration{
		ID: uuid.New(),
		UserID: f.userID, GSTIN: "33ABCDE1234FAZ1", LegalName: "Test Trader",
		StateCode: "33", State: "Tamil Nadu", RegistrationType: "composition",
		CompositionCategory: "trader",
		EffectiveFrom:       time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		Status:              "active",
	}
	if err := db.Create(&reg).Error; err != nil {
		t.Fatal(err)
	}
	f.reg = &reg

	f.customer = models.Party{ID: uuid.New(), UserID: f.userID, Name: "Walk-in Customer", PartyType: "customer", StateCode: "33", State: "Tamil Nadu"}
	f.supplierReg = models.Party{ID: uuid.New(), UserID: f.userID, Name: "Registered Supplier", PartyType: "vendor", GSTIN: "33FFFFF1234F1Z1", StateCode: "33", State: "Tamil Nadu"}
	f.supplierURD = models.Party{ID: uuid.New(), UserID: f.userID, Name: "URD Supplier", PartyType: "vendor", StateCode: "33", State: "Tamil Nadu"}
	for _, p := range []*models.Party{&f.customer, &f.supplierReg, &f.supplierURD} {
		if err := db.Create(p).Error; err != nil {
			t.Fatal(err)
		}
	}

	f.productGST = models.Product{ID: uuid.New(), UserID: f.userID, Name: "Taxed Item", SKU: "GST-1", GstEnabled: true, ItemType: "product", TaxRate: 5}
	f.productNoGST = models.Product{ID: uuid.New(), UserID: f.userID, Name: "Non-GST Item", SKU: "NGST-1", GstEnabled: false, ItemType: "product"}
	if err := db.Create(&f.productGST).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&f.productNoGST).Error; err != nil {
		t.Fatal(err)
	}
	return f
}

func gstScopeFor(f *gstFixture) *gstReportScope {
	return &gstReportScope{
		Registration: f.reg,
		Start:        time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		End:          time.Date(2026, 9, 30, 23, 59, 59, 0, time.UTC),
		FY:           "2026-27", Quarter: 2, PeriodType: models.GSTPeriodTypeQuarterly,
	}
}

func mkInvoice(f *gstFixture, num string, date time.Time, status string, inter bool, items []models.InvoiceItem) models.Invoice {
	inv := models.Invoice{
		ID: uuid.New(), UserID: f.userID, PartyID: f.customer.ID,
		InvoiceNumber: num, Date: date, Status: status,
		IsInterState: inter, Items: items,
	}
	return inv
}

func gstItem(prod models.Product, qty, price, rate float64) models.InvoiceItem {
	return models.InvoiceItem{
		ID: uuid.New(), ProductID: &prod.ID, Product: prod,
		Quantity: qty, UnitPrice: price, TaxRate: rate,
		Total: qty * price * (1 + rate/100),
	}
}

func TestGSTReportPipeline(t *testing.T) {
	db := setupGSTTestDB(t)
	f := seedGSTFixture(t, db)
	d := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)

	// Intra-state invoice: 1000 taxable + 500 non-GST.
	inv1 := mkInvoice(f, "INV-1", d, "paid", false, []models.InvoiceItem{
		gstItem(f.productGST, 10, 100, 5),
		gstItem(f.productNoGST, 5, 100, 0),
	})
	inv1.SubTotal, inv1.TaxTotal, inv1.TotalAmount = 1500, 50, 1550
	// Inter-state invoice: 1000 taxable (flagged review).
	inv2 := mkInvoice(f, "INV-2", d, "sent", true, []models.InvoiceItem{gstItem(f.productGST, 10, 100, 5)})
	inv2.SubTotal, inv2.TaxTotal, inv2.TotalAmount, inv2.IGSTTotal = 1000, 50, 1050, 50
	// Cancelled + draft — excluded from totals but listed.
	inv3 := mkInvoice(f, "INV-3", d, "cancelled", false, []models.InvoiceItem{gstItem(f.productGST, 1, 500, 5)})
	inv3.SubTotal = 500
	inv4 := mkInvoice(f, "INV-4", d, "draft", false, []models.InvoiceItem{gstItem(f.productGST, 1, 300, 5)})
	inv4.SubTotal = 300
	for _, inv := range []models.Invoice{inv1, inv2, inv3, inv4} {
		if err := db.Create(&inv).Error; err != nil {
			t.Fatal(err)
		}
	}

	// Processed sales return: 200 taxable back.
	sr := models.SalesReturn{
		ID: uuid.New(), UserID: f.userID, PartyID: f.customer.ID, InvoiceID: inv1.ID,
		ReturnNumber: "SR-1", Date: d, Status: "processed", Amount: 210,
		Items: []models.SalesReturnItem{{ID: uuid.New(), Quantity: 2, UnitPrice: 100, TaxRate: 5, Total: 210}},
	}
	if err := db.Create(&sr).Error; err != nil {
		t.Fatal(err)
	}
	// Standalone issued credit note: 100 taxable back.
	cn := models.CreditNote{
		ID: uuid.New(), UserID: f.userID, PartyID: f.customer.ID, InvoiceID: inv1.ID,
		CreditNoteNumber: "CN-1", Date: d, Status: "issued", TotalAmount: 105,
		Items: []models.CreditNoteItem{{ID: uuid.New(), Quantity: 1, UnitPrice: 100, TaxRate: 5, Total: 105}},
	}
	if err := db.Create(&cn).Error; err != nil {
		t.Fatal(err)
	}

	// Purchases: registered supplier intra-state (GST 180), URD with RCM flag (600 @5 => 30), URD unassessed.
	bill1 := models.PurchaseBill{
		ID: uuid.New(), UserID: f.userID, PartyID: f.supplierReg.ID,
		BillNumber: "PB-1", BillDate: d, Status: "paid",
		SubTotal: 1000, TaxTotal: 180, TotalAmount: 1180,
		Items: []models.PurchaseBillItem{{ID: uuid.New(), Quantity: 5, UnitPrice: 200, TaxRate: 18, TaxAmount: 180, Total: 1180}},
	}
	bill2 := models.PurchaseBill{
		ID: uuid.New(), UserID: f.userID, PartyID: f.supplierURD.ID,
		BillNumber: "PB-2", BillDate: d, Status: "paid", ReverseCharge: true,
		SubTotal: 600, TaxTotal: 30, TotalAmount: 630,
		Items: []models.PurchaseBillItem{{ID: uuid.New(), Quantity: 2, UnitPrice: 300, TaxRate: 5, TaxAmount: 30, Total: 630}},
	}
	bill3 := models.PurchaseBill{
		ID: uuid.New(), UserID: f.userID, PartyID: f.supplierURD.ID,
		BillNumber: "PB-3", BillDate: d, Status: "paid",
		SubTotal: 400, TotalAmount: 400,
		Items: []models.PurchaseBillItem{{ID: uuid.New(), Quantity: 4, UnitPrice: 100, TaxRate: 0, TaxAmount: 0, Total: 400}},
	}
	for _, b := range []models.PurchaseBill{bill1, bill2, bill3} {
		if err := db.Create(&b).Error; err != nil {
			t.Fatal(err)
		}
	}
	// Processed purchase return against bill1: 100 back.
	pr := models.PurchaseReturn{
		ID: uuid.New(), UserID: f.userID, PartyID: f.supplierReg.ID, PurchaseBillID: bill1.ID,
		ReturnNumber: "PR-1", Date: d, Status: "processed", Amount: 118,
		Items: []models.PurchaseReturnItem{{ID: uuid.New(), Quantity: 1, UnitPrice: 100, TaxRate: 18, Total: 118}},
	}
	if err := db.Create(&pr).Error; err != nil {
		t.Fatal(err)
	}

	scope := gstScopeFor(f)
	sales, purchases, ss, ps, liab := computeGSTReport(f.userID, scope)

	// Sales: 2 valid invoices gross 2500; returns/credit notes -300 pre-tax.
	if ss.InvoiceCount != 2 || ss.CancelledInvoiceCount != 1 {
		t.Fatalf("invoice_count=%d cancelled=%d", ss.InvoiceCount, ss.CancelledInvoiceCount)
	}
	if ss.GrossSales != 2500 {
		t.Fatalf("gross=%v want 2500", ss.GrossSales)
	}
	if ss.SalesReturnAmount != 200 || ss.CreditNoteValue != 100 {
		t.Fatalf("returns=%v cn=%v", ss.SalesReturnAmount, ss.CreditNoteValue)
	}
	if ss.NetSales != 2200 {
		t.Fatalf("net=%v want 2200", ss.NetSales)
	}
	// Taxable: 1000+1000-200-100 = 1700; non-GST 500 stays out of the levy basis.
	if ss.TaxableSupplyValue != 1700 {
		t.Fatalf("taxable=%v want 1700", ss.TaxableSupplyValue)
	}
	if ss.NonGSTSupplyValue != 500 {
		t.Fatalf("non_gst=%v want 500", ss.NonGSTSupplyValue)
	}
	if ss.CompositionTurnoverBasis != 1700 {
		t.Fatalf("basis=%v want 1700", ss.CompositionTurnoverBasis)
	}
	if ss.InterStateOutwardValue != 1000 {
		t.Fatalf("interstate=%v want 1000", ss.InterStateOutwardValue)
	}

	// Purchases: 3 bills; returns reduce taxable.
	if ps.PurchaseInvoiceCount != 3 {
		t.Fatalf("bills=%d", ps.PurchaseInvoiceCount)
	}
	if ps.RegisteredSupplierPurchaseValue != 1000 {
		t.Fatalf("registered=%v", ps.RegisteredSupplierPurchaseValue)
	}
	if ps.UnregisteredSupplierPurchaseValue != 1000 {
		t.Fatalf("unregistered=%v want 1000 (600+400)", ps.UnregisteredSupplierPurchaseValue)
	}
	if ps.PurchaseReturnValue != 100 {
		t.Fatalf("purchase returns=%v", ps.PurchaseReturnValue)
	}
	if ps.RCMTaxableValue != 600 || ps.RCMTaxLiability != 30 {
		t.Fatalf("rcm=%v/%v want 600/30", ps.RCMTaxableValue, ps.RCMTaxLiability)
	}

	// Levy: 1% of 1700 = 17, split 8.5/8.5.
	if liab.Total != 17 || liab.CGST != 8.5 || liab.SGST != 8.5 {
		t.Fatalf("liab=%v/%v/%v want 17/8.5/8.5", liab.Total, liab.CGST, liab.SGST)
	}

	// Register audit fields.
	var cancelledRow, reviewRow *gstSalesRegisterRow
	for i := range sales {
		if sales[i].InvoiceStatus == "cancelled" {
			cancelledRow = &sales[i]
		}
		if sales[i].CompositionTurnover == "review" {
			reviewRow = &sales[i]
		}
	}
	if cancelledRow == nil || cancelledRow.IncludedInTotals {
		t.Fatal("cancelled invoice must appear flagged excluded")
	}
	if reviewRow == nil || !reviewRow.IsInterState {
		t.Fatal("inter-state invoice must be flagged for review")
	}
	if len(purchases) != 4 {
		t.Fatalf("purchase rows=%d want 4 (3 bills + 1 return)", len(purchases))
	}

	// Validation should flag inter-state sale + unassessed RCM (PB-3).
	issues := runGSTValidation(f.userID, scope, ss, ps, 1)
	codes := map[string]bool{}
	for _, i := range issues {
		codes[i.Code] = true
	}
	for _, want := range []string{"INTERSTATE_OUTWARD_SUPPLY", "UNASSESSED_RCM", "CANCELLED_INVOICE_EXCLUDED"} {
		if !codes[want] {
			t.Fatalf("expected issue %s; got %v", want, codes)
		}
	}
	if codes["MISSING_GST_REGISTRATION"] {
		t.Fatal("registration exists — must not flag missing")
	}
}

func TestGSTSnapshotAndWorkflow(t *testing.T) {
	db := setupGSTTestDB(t)
	f := seedGSTFixture(t, db)
	d := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)

	inv := mkInvoice(f, "INV-1", d, "paid", false, []models.InvoiceItem{gstItem(f.productGST, 10, 100, 5)})
	inv.SubTotal, inv.TaxTotal, inv.TotalAmount = 1000, 50, 1050
	if err := db.Create(&inv).Error; err != nil {
		t.Fatal(err)
	}

	period := models.GSTReturnPeriod{
		ID: uuid.New(), UserID: f.userID, GSTRegistrationID: f.reg.ID,
		FinancialYear: "2026-27", PeriodType: models.GSTPeriodTypeQuarterly, Quarter: 2,
		PeriodStart: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		PeriodEnd:   time.Date(2026, 9, 30, 23, 59, 59, 0, time.UTC),
		ReturnType:  models.GSTReturnTypeCMP08, Status: models.GSTPeriodStatusDraft,
	}
	if err := db.Create(&period).Error; err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user_id", f.userID)
	c.Set("actor_user_id", f.userID)

	snap, issues, err := generateGSTSnapshot(c, &period)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if snap.Version != 1 {
		t.Fatalf("version=%d want 1", snap.Version)
	}
	if snap.TurnoverBasis != 1000 || snap.TotalCompositionLiability != 10 {
		t.Fatalf("snapshot basis=%v liab=%v want 1000/10", snap.TurnoverBasis, snap.TotalCompositionLiability)
	}
	if snap.SourceDataVersion == "" {
		t.Fatal("source data fingerprint must be recorded")
	}
	var p models.GSTReturnPeriod
	db.First(&p, "id = ?", period.ID)
	if p.Status != models.GSTPeriodStatusValidated {
		t.Fatalf("status=%q want VALIDATED", p.Status)
	}

	// Persisted issues are retrievable.
	var stored []models.GSTValidationIssue
	db.Where("gst_return_period_id = ?", period.ID).Find(&stored)
	if len(stored) != len(issues) {
		t.Fatalf("stored issues=%d want %d", len(stored), len(issues))
	}

	// Regeneration bumps the version and refreshes open issues.
	snap2, _, err := generateGSTSnapshot(c, &period)
	if err != nil {
		t.Fatal(err)
	}
	if snap2.Version != 2 {
		t.Fatalf("version=%d want 2", snap2.Version)
	}
}
