package controllers

import (
	"testing"
	"truerp/models"
	"truerp/utils"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openStockSummaryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Category{},
		&models.Product{},
		&models.Warehouse{},
		&models.InventoryStock{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// A product is a duplicate only when name, item code, purchase price, selling
// price and category all match; rows that differ become separate products and
// separate stock entries. Re-importing the same file must not create
// duplicates.
func TestImportStockSummaryRowsDedupAndIdempotency(t *testing.T) {
	db := openStockSummaryTestDB(t)
	previousDB := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = previousDB })

	userID := uuid.New()
	content := []byte(`Name,Batch No.,Item Code,Purchase Price,Selling Price,Stock Quantity,Stock Value,Item Category Name,MRP
Widget A,,IC1,10.0,20.0,5.0 PCS,50.0,CAT1,25.0
Widget A,,IC1,10.0,20.0,5.0 PCS,50.0,CAT1,25.0
Widget A,,IC1,12.0,20.0,3.0 PCS,36.0,CAT1,25.0
Widget A,B2,IC1,10.0,20.0,7.0 PCS,70.0,CAT1,25.0
Widget B,,IC2,1.0,2.0,1.0 PCS,1.0,CAT2,2.0
`)

	result, errs, err := importStockSummaryRows(userID, content, map[string]string{}, nil)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("import errors = %v, want none", errs)
	}
	if got := result["products_created"].(int); got != 3 {
		t.Fatalf("products_created = %d, want 3 (price variant + batch row share products)", got)
	}
	if got := result["stock_updated"].(int); got != 5 {
		t.Fatalf("stock_updated = %d, want 5", got)
	}

	var productCount, stockCount int64
	if err := db.Model(&models.Product{}).Where("user_id = ?", userID).Count(&productCount).Error; err != nil {
		t.Fatalf("count products: %v", err)
	}
	if productCount != 3 {
		t.Fatalf("products = %d, want 3", productCount)
	}
	if err := db.Model(&models.InventoryStock{}).Where("user_id = ?", userID).Count(&stockCount).Error; err != nil {
		t.Fatalf("count stocks: %v", err)
	}
	if stockCount != 4 {
		t.Fatalf("stock rows = %d, want 4 (exact dup row merged)", stockCount)
	}

	// Re-import the same file: nothing new should be created.
	result, errs, err = importStockSummaryRows(userID, content, map[string]string{}, nil)
	if err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("re-import errors = %v, want none", errs)
	}
	if got := result["products_created"].(int); got != 0 {
		t.Fatalf("re-import products_created = %d, want 0", got)
	}
	if err := db.Model(&models.Product{}).Where("user_id = ?", userID).Count(&productCount).Error; err != nil {
		t.Fatalf("count products after re-import: %v", err)
	}
	if productCount != 3 {
		t.Fatalf("products after re-import = %d, want 3", productCount)
	}
	if err := db.Model(&models.InventoryStock{}).Where("user_id = ?", userID).Count(&stockCount).Error; err != nil {
		t.Fatalf("count stocks after re-import: %v", err)
	}
	if stockCount != 4 {
		t.Fatalf("stock rows after re-import = %d, want 4", stockCount)
	}
}
