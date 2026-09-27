package controllers

import (
	"encoding/json"
	"fmt"
	"testing"
	"truerp/models"
	"truerp/utils"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupProductListTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Product{},
		&models.InventoryStock{},
		&models.Category{},
		&models.ExpenseCategory{},
		&models.Account{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	previousDB := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = previousDB })
	return db
}

func seedProducts(t *testing.T, db *gorm.DB, userID uuid.UUID) {
	t.Helper()
	for i := 1; i <= 5; i++ {
		product := models.Product{
			ID:       uuid.New(),
			UserID:   userID,
			Name:     fmt.Sprintf("Widget %d", i),
			SKU:      fmt.Sprintf("SKU-%d", i),
			Category: "Hardware",
		}
		if err := db.Create(&product).Error; err != nil {
			t.Fatalf("create product %d: %v", i, err)
		}
	}
}

func TestGetProductsPaginatedReturnsEnvelope(t *testing.T) {
	db := setupProductListTestDB(t)
	userID := uuid.New()
	seedProducts(t, db, userID)

	c, rec := newListContext("/products", "?page=2&per_page=2")
	c.Set("user_id", userID)
	GetProducts(c)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Products []models.Product `json:"products"`
		Total    int64            `json:"total"`
		Page     int              `json:"page"`
		PerPage  int              `json:"per_page"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 5 || body.Page != 2 || body.PerPage != 2 || len(body.Products) != 2 {
		t.Fatalf("unexpected envelope: total=%d page=%d per_page=%d rows=%d",
			body.Total, body.Page, body.PerPage, len(body.Products))
	}
}

func TestGetProductsPaginatedFiltersAndLegacy(t *testing.T) {
	db := setupProductListTestDB(t)
	userID := uuid.New()
	seedProducts(t, db, userID)

	// Search narrows the counted total, not just the returned rows.
	c, rec := newListContext("/products", "?page=1&per_page=10&search=widget%201")
	c.Set("user_id", userID)
	GetProducts(c)
	var body struct {
		Products []models.Product `json:"products"`
		Total    int64            `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 1 || len(body.Products) != 1 {
		t.Fatalf("search: total=%d rows=%d, want 1/1", body.Total, len(body.Products))
	}

	// Category filter applies before pagination.
	c, rec = newListContext("/products", "?page=1&per_page=10&category=Other")
	c.Set("user_id", userID)
	GetProducts(c)
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 0 || len(body.Products) != 0 {
		t.Fatalf("category filter: total=%d rows=%d, want 0/0", body.Total, len(body.Products))
	}

	// Legacy callers (e.g. POS catalog) still get a plain array.
	c, rec = newListContext("/products", "")
	c.Set("user_id", userID)
	GetProducts(c)
	var rows []models.Product
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode legacy array: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("legacy rows = %d, want 5", len(rows))
	}
}

func seedCategories(t *testing.T, db *gorm.DB, userID uuid.UUID) {
	t.Helper()
	for i := 1; i <= 5; i++ {
		category := models.Category{
			ID:       uuid.New(),
			UserID:   userID,
			Name:     fmt.Sprintf("Category %d", i),
			IsActive: true,
		}
		if err := db.Create(&category).Error; err != nil {
			t.Fatalf("create category %d: %v", i, err)
		}
		// is_active has gorm default:true, so false must be set via Update.
		if i%2 != 0 {
			if err := db.Model(&models.Category{}).Where("id = ?", category.ID).Update("is_active", false).Error; err != nil {
				t.Fatalf("deactivate category %d: %v", i, err)
			}
		}
	}
}

func TestGetCategoriesPaginatedReturnsEnvelope(t *testing.T) {
	db := setupProductListTestDB(t)
	userID := uuid.New()
	seedCategories(t, db, userID)

	c, rec := newListContext("/categories", "?page=2&per_page=2")
	c.Set("user_id", userID)
	GetCategories(c)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Categories []models.Category `json:"categories"`
		Total      int64             `json:"total"`
		Page       int               `json:"page"`
		PerPage    int               `json:"per_page"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// GetCategories seeds the default "General" category on first call.
	if body.Total != 6 || body.Page != 2 || body.PerPage != 2 || len(body.Categories) != 2 {
		t.Fatalf("unexpected envelope: total=%d page=%d per_page=%d rows=%d",
			body.Total, body.Page, body.PerPage, len(body.Categories))
	}
}

func TestGetCategoriesPaginatedFilterAndLegacy(t *testing.T) {
	db := setupProductListTestDB(t)
	userID := uuid.New()
	seedCategories(t, db, userID)

	// is_active filter applies before pagination (3 of 6 rows: 2 seeded
	// actives plus the seeded "General" default).
	c, rec := newListContext("/categories", "?page=1&per_page=10&is_active=true")
	c.Set("user_id", userID)
	GetCategories(c)
	var body struct {
		Categories []models.Category `json:"categories"`
		Total      int64             `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 3 || len(body.Categories) != 3 {
		t.Fatalf("is_active filter: total=%d rows=%d, want 3/3", body.Total, len(body.Categories))
	}

	// Legacy callers still get a plain array.
	c, rec = newListContext("/categories", "")
	c.Set("user_id", userID)
	GetCategories(c)
	var rows []models.Category
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode legacy array: %v", err)
	}
	if len(rows) != 6 {
		t.Fatalf("legacy rows = %d, want 6", len(rows))
	}
}
