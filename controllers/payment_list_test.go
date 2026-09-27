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

func setupPaymentListTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.Party{}, &models.Invoice{}, &models.Payment{}, &models.PurchaseBill{}, &models.PaymentOut{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	previousDB := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = previousDB })
	return db
}

func newListContext(path, query string) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("GET", path+query, nil)
	return c, rec
}

func seedPayments(t *testing.T, db *gorm.DB, userID uuid.UUID) {
	t.Helper()
	party := models.Party{ID: uuid.New(), UserID: userID, Name: "Acme Corp", PartyType: "customer"}
	if err := db.Create(&party).Error; err != nil {
		t.Fatalf("create party: %v", err)
	}
	base := time.Now().AddDate(0, 0, -10)
	for i, num := range []string{"PIN-1", "PIN-2", "PIN-3", "PIN-4", "PIN-5"} {
		payment := models.Payment{
			ID:              uuid.New(),
			UserID:          userID,
			PartyID:         party.ID,
			PaymentInNumber: num,
			Mode:            "cash",
			AmountReceived:  100,
			Date:            base.AddDate(0, 0, i),
		}
		if err := db.Create(&payment).Error; err != nil {
			t.Fatalf("create payment %s: %v", num, err)
		}
	}
}

func TestGetPaymentsPaginatedReturnsEnvelope(t *testing.T) {
	db := setupPaymentListTestDB(t)
	userID := uuid.New()
	seedPayments(t, db, userID)

	c, rec := newListContext("/payments", "?page=2&per_page=2")
	c.Set("user_id", userID)
	GetPayments(c)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Payments []models.Payment `json:"payments"`
		Total    int64            `json:"total"`
		Page     int              `json:"page"`
		PerPage  int              `json:"per_page"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 5 || body.Page != 2 || body.PerPage != 2 || len(body.Payments) != 2 {
		t.Fatalf("unexpected envelope: total=%d page=%d per_page=%d rows=%d",
			body.Total, body.Page, body.PerPage, len(body.Payments))
	}
}

func TestGetPaymentsSearchAndFilters(t *testing.T) {
	db := setupPaymentListTestDB(t)
	userID := uuid.New()
	seedPayments(t, db, userID)

	// Party-name search matches all seeded rows.
	c, rec := newListContext("/payments", "?page=1&per_page=10&search=acme")
	c.Set("user_id", userID)
	GetPayments(c)

	var body struct {
		Payments []models.Payment `json:"payments"`
		Total    int64            `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 5 || len(body.Payments) != 5 {
		t.Fatalf("party search: total=%d rows=%d, want 5/5", body.Total, len(body.Payments))
	}

	// Payment-in-number search matches one row.
	c, rec = newListContext("/payments", "?page=1&per_page=10&search=pin-3")
	c.Set("user_id", userID)
	GetPayments(c)
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 1 || len(body.Payments) != 1 {
		t.Fatalf("number search: total=%d rows=%d, want 1/1", body.Total, len(body.Payments))
	}

	// Day-inclusive `to`: the most recent row is dated today-6; filtering to
	// that day must include it even though its timestamp is later in the day.
	to := time.Now().AddDate(0, 0, -6).Format("2006-01-02")
	c, rec = newListContext("/payments", "?page=1&per_page=10&to="+to)
	c.Set("user_id", userID)
	GetPayments(c)
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 5 || len(body.Payments) != 5 {
		t.Fatalf("to-date filter: total=%d rows=%d, want 5/5", body.Total, len(body.Payments))
	}

	// Mode filter excludes all seeded cash rows.
	c, rec = newListContext("/payments", "?page=1&per_page=10&mode=upi")
	c.Set("user_id", userID)
	GetPayments(c)
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 0 || len(body.Payments) != 0 {
		t.Fatalf("mode filter: total=%d rows=%d, want 0/0", body.Total, len(body.Payments))
	}
}

func TestGetPaymentsLegacyArrayWithoutParams(t *testing.T) {
	db := setupPaymentListTestDB(t)
	userID := uuid.New()
	seedPayments(t, db, userID)

	c, rec := newListContext("/payments", "")
	c.Set("user_id", userID)
	GetPayments(c)

	var rows []models.Payment
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode legacy array: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("legacy rows = %d, want 5", len(rows))
	}
}

func seedPaymentOuts(t *testing.T, db *gorm.DB, userID uuid.UUID) {
	t.Helper()
	vendor := models.Party{ID: uuid.New(), UserID: userID, Name: "Vendor One", PartyType: "vendor"}
	if err := db.Create(&vendor).Error; err != nil {
		t.Fatalf("create vendor: %v", err)
	}
	bill := models.PurchaseBill{
		ID:         uuid.New(),
		UserID:     userID,
		BillNumber: "BILL-42",
		PartyID:    vendor.ID,
		BillDate:   time.Now(),
		Status:     "unpaid",
	}
	if err := db.Create(&bill).Error; err != nil {
		t.Fatalf("create bill: %v", err)
	}
	for i, num := range []string{"POUT-1", "POUT-2", "POUT-3", "POUT-4", "POUT-5"} {
		paymentOut := models.PaymentOut{
			ID:               uuid.New(),
			UserID:           userID,
			PartyID:          vendor.ID,
			PurchaseBillID:   &bill.ID,
			PaymentOutNumber: num,
			Mode:             "upi",
			AmountPaid:       50,
			Date:             time.Now().AddDate(0, 0, -i),
		}
		if err := db.Create(&paymentOut).Error; err != nil {
			t.Fatalf("create payment out %s: %v", num, err)
		}
	}
}

func TestGetPaymentOutsPaginatedReturnsEnvelope(t *testing.T) {
	db := setupPaymentListTestDB(t)
	userID := uuid.New()
	seedPaymentOuts(t, db, userID)

	c, rec := newListContext("/payment-outs", "?page=2&per_page=2")
	c.Set("user_id", userID)
	GetPaymentOuts(c)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		PaymentOuts []models.PaymentOut `json:"payment_outs"`
		Total       int64               `json:"total"`
		Page        int                 `json:"page"`
		PerPage     int                 `json:"per_page"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 5 || body.Page != 2 || body.PerPage != 2 || len(body.PaymentOuts) != 2 {
		t.Fatalf("unexpected envelope: total=%d page=%d per_page=%d rows=%d",
			body.Total, body.Page, body.PerPage, len(body.PaymentOuts))
	}
}

func TestGetPaymentOutsSearchByBillNumber(t *testing.T) {
	db := setupPaymentListTestDB(t)
	userID := uuid.New()
	seedPaymentOuts(t, db, userID)

	c, rec := newListContext("/payment-outs", "?page=1&per_page=10&search=bill-42")
	c.Set("user_id", userID)
	GetPaymentOuts(c)

	var body struct {
		PaymentOuts []models.PaymentOut `json:"payment_outs"`
		Total       int64               `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 5 || len(body.PaymentOuts) != 5 {
		t.Fatalf("bill search: total=%d rows=%d, want 5/5", body.Total, len(body.PaymentOuts))
	}

	c, rec = newListContext("/payment-outs", "?page=1&per_page=10&search=nomatch")
	c.Set("user_id", userID)
	GetPaymentOuts(c)
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 0 || len(body.PaymentOuts) != 0 {
		t.Fatalf("no-match search: total=%d rows=%d, want 0/0", body.Total, len(body.PaymentOuts))
	}
}

func TestGetPaymentOutsLegacyArrayWithoutParams(t *testing.T) {
	db := setupPaymentListTestDB(t)
	userID := uuid.New()
	seedPaymentOuts(t, db, userID)

	c, rec := newListContext("/payment-outs", "")
	c.Set("user_id", userID)
	GetPaymentOuts(c)

	var rows []models.PaymentOut
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode legacy array: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("legacy rows = %d, want 5", len(rows))
	}
}
