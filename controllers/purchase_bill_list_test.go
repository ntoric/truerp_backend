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

func setupPurchaseBillListTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.Party{}, &models.PurchaseBill{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	previousDB := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = previousDB })
	return db
}

func newPurchaseBillListContext(query string) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("GET", "/purchase/bills"+query, nil)
	return c, rec
}

func seedPurchaseBillListRows(t *testing.T, db *gorm.DB, userID uuid.UUID) {
	t.Helper()
	party := models.Party{ID: uuid.New(), UserID: userID, Name: "Vendor One", PartyType: "vendor"}
	if err := db.Create(&party).Error; err != nil {
		t.Fatalf("create party: %v", err)
	}
	base := time.Now().AddDate(0, 0, -10)
	for i, num := range []string{"BILL-1", "BILL-2", "BILL-9", "BILL-10", "BILL-11"} {
		bill := models.PurchaseBill{
			ID:          uuid.New(),
			UserID:      userID,
			BillNumber:  num,
			PartyID:     party.ID,
			BillDate:    base.AddDate(0, 0, i),
			Status:      "paid",
			TotalAmount: 100,
			PaidAmount:  100,
		}
		if err := db.Create(&bill).Error; err != nil {
			t.Fatalf("create bill %s: %v", num, err)
		}
	}
}

func TestGetPurchaseBillsPaginatedReturnsEnvelope(t *testing.T) {
	db := setupPurchaseBillListTestDB(t)
	userID := uuid.New()
	seedPurchaseBillListRows(t, db, userID)

	c, rec := newPurchaseBillListContext("?page=2&per_page=2")
	c.Set("user_id", userID)
	GetPurchaseBills(c)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Bills   []models.PurchaseBill `json:"bills"`
		Total   int64                 `json:"total"`
		Page    int                   `json:"page"`
		PerPage int                   `json:"per_page"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 5 || body.Page != 2 || body.PerPage != 2 || len(body.Bills) != 2 {
		t.Fatalf("unexpected envelope: total=%d page=%d per_page=%d rows=%d",
			body.Total, body.Page, body.PerPage, len(body.Bills))
	}
}

func TestGetPurchaseBillsSearchFiltersByPartyName(t *testing.T) {
	db := setupPurchaseBillListTestDB(t)
	userID := uuid.New()
	seedPurchaseBillListRows(t, db, userID)

	c, rec := newPurchaseBillListContext("?page=1&per_page=10&search=vendor%20one")
	c.Set("user_id", userID)
	GetPurchaseBills(c)

	var body struct {
		Bills []models.PurchaseBill `json:"bills"`
		Total int64                 `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 5 || len(body.Bills) != 5 {
		t.Fatalf("party search: total=%d rows=%d, want 5/5", body.Total, len(body.Bills))
	}

	c, rec = newPurchaseBillListContext("?page=1&per_page=10&search=nomatch")
	c.Set("user_id", userID)
	GetPurchaseBills(c)
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 0 || len(body.Bills) != 0 {
		t.Fatalf("no-match search: total=%d rows=%d, want 0/0", body.Total, len(body.Bills))
	}
}

func TestGetPurchaseBillsDateRangeFilter(t *testing.T) {
	db := setupPurchaseBillListTestDB(t)
	userID := uuid.New()
	seedPurchaseBillListRows(t, db, userID)

	// Rows are dated today-10 .. today-6; narrow to the middle two days.
	from := time.Now().AddDate(0, 0, -8).Format("2006-01-02")
	to := time.Now().AddDate(0, 0, -7).Format("2006-01-02")

	c, rec := newPurchaseBillListContext("?page=1&per_page=10&from_date=" + from + "&to_date=" + to)
	c.Set("user_id", userID)
	GetPurchaseBills(c)

	var body struct {
		Bills []models.PurchaseBill `json:"bills"`
		Total int64                 `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 2 || len(body.Bills) != 2 {
		t.Fatalf("date range: total=%d rows=%d, want 2/2", body.Total, len(body.Bills))
	}
}

func TestGetPurchaseBillsLegacyArrayWithoutParams(t *testing.T) {
	db := setupPurchaseBillListTestDB(t)
	userID := uuid.New()
	seedPurchaseBillListRows(t, db, userID)

	c, rec := newPurchaseBillListContext("")
	c.Set("user_id", userID)
	GetPurchaseBills(c)

	var rows []models.PurchaseBill
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode legacy array: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("legacy rows = %d, want 5", len(rows))
	}
}
