package controllers

import (
	"encoding/json"
	"testing"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupInventoryListTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Product{},
		&models.Warehouse{},
		&models.StockEntry{},
		&models.InventoryStock{},
		&models.StockTransfer{},
		&models.StockTransferItem{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	previousDB := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = previousDB })
	return db
}

func seedStockEntries(t *testing.T, db *gorm.DB, userID uuid.UUID) models.Product {
	t.Helper()
	product := models.Product{ID: uuid.New(), UserID: userID, Name: "Widget A", SKU: "SKU-A"}
	if err := db.Create(&product).Error; err != nil {
		t.Fatalf("create product: %v", err)
	}
	outletID := uuid.New()
	base := time.Now().AddDate(0, 0, -5)
	statuses := []string{"pending", "approved", "approved", "pending", "approved"}
	for i, status := range statuses {
		entry := models.StockEntry{
			ID:             uuid.New(),
			UserID:         userID,
			ProductID:      &product.ID,
			ItemName:       "Widget A",
			OutletID:       outletID,
			EntryType:      "purchase",
			Quantity:       10,
			ApprovalStatus: status,
			EntryDate:      base.AddDate(0, 0, i),
		}
		if err := db.Create(&entry).Error; err != nil {
			t.Fatalf("create entry %d: %v", i, err)
		}
	}
	return product
}

func TestGetStockEntriesPaginatedReturnsEnvelope(t *testing.T) {
	db := setupInventoryListTestDB(t)
	userID := uuid.New()
	seedStockEntries(t, db, userID)

	c, rec := newListContext("/inventory/entries", "?page=2&per_page=2")
	c.Set("user_id", userID)
	GetStockEntries(c)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Data    []models.StockEntry `json:"data"`
		Total   int64               `json:"total"`
		Page    int                 `json:"page"`
		PerPage int                 `json:"per_page"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 5 || body.Page != 2 || body.PerPage != 2 || len(body.Data) != 2 {
		t.Fatalf("unexpected envelope: total=%d page=%d per_page=%d rows=%d",
			body.Total, body.Page, body.PerPage, len(body.Data))
	}
}

func TestGetStockEntriesFiltersAndSearch(t *testing.T) {
	db := setupInventoryListTestDB(t)
	userID := uuid.New()
	seedStockEntries(t, db, userID)

	// Pending-only filter (drives the approval badge count on the page).
	c, rec := newListContext("/inventory/entries", "?page=1&per_page=1&approval_status=pending")
	c.Set("user_id", userID)
	GetStockEntries(c)
	var body struct {
		Data  []models.StockEntry `json:"data"`
		Total int64               `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 2 || len(body.Data) != 1 {
		t.Fatalf("pending filter: total=%d rows=%d, want 2/1", body.Total, len(body.Data))
	}

	// Product-name search via the products table.
	c, rec = newListContext("/inventory/entries", "?page=1&per_page=10&search=widget")
	c.Set("user_id", userID)
	GetStockEntries(c)
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 5 || len(body.Data) != 5 {
		t.Fatalf("product search: total=%d rows=%d, want 5/5", body.Total, len(body.Data))
	}

	c, rec = newListContext("/inventory/entries", "?page=1&per_page=10&search=nomatch")
	c.Set("user_id", userID)
	GetStockEntries(c)
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 0 || len(body.Data) != 0 {
		t.Fatalf("no-match search: total=%d rows=%d, want 0/0", body.Total, len(body.Data))
	}

	// Day-inclusive to_date: the newest entry is dated today; filtering to
	// today must include it even though its timestamp is later in the day.
	today := time.Now().Format("2006-01-02")
	c, rec = newListContext("/inventory/entries", "?page=1&per_page=10&to_date="+today)
	c.Set("user_id", userID)
	GetStockEntries(c)
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 5 {
		t.Fatalf("to_date inclusive: total=%d, want 5", body.Total)
	}
}

func TestGetStockEntriesLegacyShapeWithoutParams(t *testing.T) {
	db := setupInventoryListTestDB(t)
	userID := uuid.New()
	seedStockEntries(t, db, userID)

	c, rec := newListContext("/inventory/entries", "")
	c.Set("user_id", userID)
	GetStockEntries(c)

	var body struct {
		Data []models.StockEntry `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode legacy shape: %v", err)
	}
	if len(body.Data) != 5 {
		t.Fatalf("legacy rows = %d, want 5", len(body.Data))
	}
}

func seedInventoryStocks(t *testing.T, db *gorm.DB, userID uuid.UUID) {
	t.Helper()
	product := models.Product{ID: uuid.New(), UserID: userID, Name: "Widget B", SKU: "SKU-B"}
	if err := db.Create(&product).Error; err != nil {
		t.Fatalf("create product: %v", err)
	}
	outletID := uuid.New()
	for i := 0; i < 5; i++ {
		stock := models.InventoryStock{
			ID:        uuid.New(),
			UserID:    userID,
			ProductID: product.ID,
			OutletID:  outletID,
			BatchNo:   "BATCH-" + uuid.NewString()[:6],
			Quantity:  10,
		}
		if err := db.Create(&stock).Error; err != nil {
			t.Fatalf("create stock %d: %v", i, err)
		}
	}
}

func TestGetInventoryStocksPaginatedReturnsEnvelope(t *testing.T) {
	db := setupInventoryListTestDB(t)
	userID := uuid.New()
	seedInventoryStocks(t, db, userID)

	c, rec := newListContext("/inventory/stocks", "?page=1&per_page=3")
	c.Set("user_id", userID)
	GetInventoryStocks(c)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Data    []models.InventoryStock `json:"data"`
		Total   int64                   `json:"total"`
		Page    int                     `json:"page"`
		PerPage int                     `json:"per_page"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 5 || body.Page != 1 || body.PerPage != 3 || len(body.Data) != 3 {
		t.Fatalf("unexpected envelope: total=%d page=%d per_page=%d rows=%d",
			body.Total, body.Page, body.PerPage, len(body.Data))
	}
}

func TestGetInventoryStocksSearchAndLegacy(t *testing.T) {
	db := setupInventoryListTestDB(t)
	userID := uuid.New()
	seedInventoryStocks(t, db, userID)

	c, rec := newListContext("/inventory/stocks", "?page=1&per_page=10&search=widget%20b")
	c.Set("user_id", userID)
	GetInventoryStocks(c)
	var body struct {
		Data  []models.InventoryStock `json:"data"`
		Total int64                   `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 5 || len(body.Data) != 5 {
		t.Fatalf("product search: total=%d rows=%d, want 5/5", body.Total, len(body.Data))
	}

	// Legacy callers (e.g. POS catalog) still get a plain array.
	c, rec = newListContext("/inventory/stocks", "")
	c.Set("user_id", userID)
	GetInventoryStocks(c)
	var rows []models.InventoryStock
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode legacy array: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("legacy rows = %d, want 5", len(rows))
	}
}

func seedStockTransfers(t *testing.T, db *gorm.DB, userID uuid.UUID) {
	t.Helper()
	base := time.Now().AddDate(0, 0, -5)
	for i := 0; i < 5; i++ {
		transfer := models.StockTransfer{
			ID:           uuid.New(),
			UserID:       userID,
			FromOutletID: uuid.New(),
			ToOutletID:   uuid.New(),
			Status:       "draft",
			TotalItems:   1,
			CreatedAt:    base.AddDate(0, 0, i),
		}
		if err := db.Create(&transfer).Error; err != nil {
			t.Fatalf("create transfer %d: %v", i, err)
		}
	}
}

func TestGetStockTransfersPaginatedReturnsEnvelope(t *testing.T) {
	db := setupInventoryListTestDB(t)
	userID := uuid.New()
	seedStockTransfers(t, db, userID)

	c, rec := newListContext("/inventory/transfers", "?page=2&per_page=2")
	c.Set("user_id", userID)
	GetStockTransfers(c)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Data    []models.StockTransfer `json:"data"`
		Total   int64                  `json:"total"`
		Page    int                    `json:"page"`
		PerPage int                    `json:"per_page"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 5 || body.Page != 2 || body.PerPage != 2 || len(body.Data) != 2 {
		t.Fatalf("unexpected envelope: total=%d page=%d per_page=%d rows=%d",
			body.Total, body.Page, body.PerPage, len(body.Data))
	}
}

func TestGetStockTransfersDateRangeAndLegacy(t *testing.T) {
	db := setupInventoryListTestDB(t)
	userID := uuid.New()
	seedStockTransfers(t, db, userID)

	// Rows are dated today-5 .. today-1; narrow to the middle two days.
	from := time.Now().AddDate(0, 0, -4).Format("2006-01-02")
	to := time.Now().AddDate(0, 0, -3).Format("2006-01-02")

	c, rec := newListContext("/inventory/transfers", "?page=1&per_page=10&from_date="+from+"&to_date="+to)
	c.Set("user_id", userID)
	GetStockTransfers(c)
	var body struct {
		Data  []models.StockTransfer `json:"data"`
		Total int64                  `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 2 || len(body.Data) != 2 {
		t.Fatalf("date range: total=%d rows=%d, want 2/2", body.Total, len(body.Data))
	}

	// Legacy callers still get a plain array.
	c, rec = newListContext("/inventory/transfers", "")
	c.Set("user_id", userID)
	GetStockTransfers(c)
	var rows []models.StockTransfer
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode legacy array: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("legacy rows = %d, want 5", len(rows))
	}
}
