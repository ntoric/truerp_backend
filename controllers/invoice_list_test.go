package controllers

import (
	"encoding/json"
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

func setupInvoiceListTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.Party{}, &models.Invoice{}, &models.Payment{}, &models.InvoiceStatusHistory{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	previousDB := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = previousDB })
	return db
}

func newInvoiceListContext(query string) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("GET", "/invoices"+query, nil)
	return c, rec
}

func seedInvoiceListRows(t *testing.T, db *gorm.DB, userID uuid.UUID) {
	t.Helper()
	party := models.Party{ID: uuid.New(), UserID: userID, Name: "Acme Corp", PartyType: "customer"}
	if err := db.Create(&party).Error; err != nil {
		t.Fatalf("create party: %v", err)
	}
	now := time.Now()
	for _, num := range []string{"INV-1", "INV-2", "INV-9", "INV-10", "INV-11"} {
		inv := models.Invoice{
			ID:            uuid.New(),
			UserID:        userID,
			InvoiceNumber: num,
			PartyID:       party.ID,
			Date:          now,
			Status:        "paid",
			TotalAmount:   100,
			AmountPaid:    100,
		}
		if err := db.Create(&inv).Error; err != nil {
			t.Fatalf("create invoice %s: %v", num, err)
		}
	}
}

func TestGetInvoicesPaginatedReturnsEnvelope(t *testing.T) {
	db := setupInvoiceListTestDB(t)
	userID := uuid.New()
	seedInvoiceListRows(t, db, userID)

	c, rec := newInvoiceListContext("?page=2&per_page=2")
	c.Set("user_id", userID)
	GetInvoices(c)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Invoices []models.Invoice `json:"invoices"`
		Total    int64            `json:"total"`
		Page     int              `json:"page"`
		PerPage  int              `json:"per_page"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 5 || body.Page != 2 || body.PerPage != 2 || len(body.Invoices) != 2 {
		t.Fatalf("unexpected envelope: total=%d page=%d per_page=%d rows=%d",
			body.Total, body.Page, body.PerPage, len(body.Invoices))
	}
}

func TestGetInvoicesNaturalSortOrder(t *testing.T) {
	db := setupInvoiceListTestDB(t)
	userID := uuid.New()
	seedInvoiceListRows(t, db, userID)

	c, rec := newInvoiceListContext("?page=1&per_page=0&sort=invoice_number&order=asc")
	c.Set("user_id", userID)
	GetInvoices(c)

	var body struct {
		Invoices []models.Invoice `json:"invoices"`
		Total    int64            `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := []string{"INV-1", "INV-2", "INV-9", "INV-10", "INV-11"}
	if len(body.Invoices) != len(want) {
		t.Fatalf("rows = %d, want %d", len(body.Invoices), len(want))
	}
	for i, inv := range body.Invoices {
		if inv.InvoiceNumber != want[i] {
			t.Fatalf("order[%d] = %s, want %s", i, inv.InvoiceNumber, want[i])
		}
	}
}

func TestGetInvoicesSearchFiltersByPartyName(t *testing.T) {
	db := setupInvoiceListTestDB(t)
	userID := uuid.New()
	seedInvoiceListRows(t, db, userID)

	c, rec := newInvoiceListContext("?page=1&per_page=10&search=acme")
	c.Set("user_id", userID)
	GetInvoices(c)

	var body struct {
		Invoices []models.Invoice `json:"invoices"`
		Total    int64            `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 5 || len(body.Invoices) != 5 {
		t.Fatalf("party search: total=%d rows=%d, want 5/5", body.Total, len(body.Invoices))
	}

	c, rec = newInvoiceListContext("?page=1&per_page=10&search=nomatch")
	c.Set("user_id", userID)
	GetInvoices(c)
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 0 || len(body.Invoices) != 0 {
		t.Fatalf("no-match search: total=%d rows=%d, want 0/0", body.Total, len(body.Invoices))
	}
}

func TestGetInvoicesLegacyArrayWithoutParams(t *testing.T) {
	db := setupInvoiceListTestDB(t)
	userID := uuid.New()
	seedInvoiceListRows(t, db, userID)

	c, rec := newInvoiceListContext("")
	c.Set("user_id", userID)
	GetInvoices(c)

	var rows []models.Invoice
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode legacy array: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("legacy rows = %d, want 5", len(rows))
	}
}
