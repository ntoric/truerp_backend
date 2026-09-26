package controllers

import (
	"testing"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openBatchedItemsTestDB(t *testing.T) *gorm.DB {
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
		&models.InventoryStock{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// The batched items report is an audit tool: it updates expiry/mfg dates on
// matching inventory batches and reports stock quantity mismatches without
// changing quantities. Unknown products and unmatched batches are errors;
// duplicate product+batch rows flag a warning.
func TestImportBatchedItemsRows(t *testing.T) {
	db := openBatchedItemsTestDB(t)
	previousDB := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = previousDB })

	userID := uuid.New()

	// Seed two same-named products (price variants) and one lone product.
	p1 := models.Product{ID: uuid.New(), UserID: userID, Name: "Widget A", SKU: "W-A1", PurchasePrice: 10, SalePrice: 20, Unit: "PCS", ItemType: "product", IsActive: true}
	p2 := models.Product{ID: uuid.New(), UserID: userID, Name: "Widget A", SKU: "W-A2", PurchasePrice: 12, SalePrice: 25, Unit: "PCS", ItemType: "product", IsActive: true}
	p3 := models.Product{ID: uuid.New(), UserID: userID, Name: "Widget B", SKU: "W-B", PurchasePrice: 5, SalePrice: 9, Unit: "PCS", ItemType: "product", IsActive: true}
	for _, p := range []models.Product{p1, p2, p3} {
		if err := db.Create(&p).Error; err != nil {
			t.Fatalf("seed product: %v", err)
		}
	}

	// resolveDefaultWarehouseID creates the default warehouse on first use.
	whID := resolveDefaultWarehouseID(userID)
	if whID == uuid.Nil {
		t.Fatal("no default warehouse")
	}

	// Seed stock rows: p1/"Batch #1" (qty 3 → will mismatch the CSV's 7/9)
	// and p2/"batch#2" (lowercase — exercises normalized batch matching).
	stocks := []models.InventoryStock{
		{ID: uuid.New(), UserID: userID, ProductID: p1.ID, OutletID: whID, BatchNo: "Batch #1", Quantity: 3, AvailableQty: 3, AverageCost: 10, LastUpdated: time.Now()},
		{ID: uuid.New(), UserID: userID, ProductID: p2.ID, OutletID: whID, BatchNo: "batch#2", Quantity: 4, AvailableQty: 4, AverageCost: 12, LastUpdated: time.Now()},
	}
	for _, s := range stocks {
		if err := db.Create(&s).Error; err != nil {
			t.Fatalf("seed stock: %v", err)
		}
	}

	content := []byte(`Item Name,Batch Number,Expiry Date,MFG Date,MRP,Purchase Price,Selling Price,Current Stock
Widget A,Batch #1,31/12/2026,01/01/2025,25.0,10.0,20.0,7.0 PCS
Widget A,BATCH#2,15/06/2027,"",30.0,12.0,25.0,4.0 PCS
Widget B,Batch #1,"","",10.0,5.0,9.0,-2.0 PCS
Ghost Item,Batch #1,01/01/2030,"",10.0,1.0,2.0,1.0 PCS
Widget A,Batch #1,31/12/2026,01/01/2025,25.0,10.0,20.0,9.0 PCS
`)

	result, errs, err := importBatchedItemsRows(userID, content, nil, nil)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if got := result["imported"].(int); got != 3 {
		t.Fatalf("imported = %d, want 3", got)
	}
	if got := result["stock_updated"].(int); got != 3 {
		t.Fatalf("stock_updated = %d, want 3", got)
	}
	// Expect: unmatched batch (Widget B), unknown product (Ghost Item),
	// duplicate row warning.
	if len(errs) != 3 {
		t.Fatalf("errors = %v, want 3", errs)
	}

	// Rows 1 and 5 report stock mismatches (CSV 7/9 vs inventory 3).
	mismatches := result["mismatches"].([]batchStockMismatch)
	if len(mismatches) != 2 {
		t.Fatalf("mismatches = %v, want 2", mismatches)
	}
	if mismatches[0].Row != 1 || mismatches[0].CSVStock != 7 || mismatches[0].InventoryStock != 3 {
		t.Fatalf("mismatch[0] = %+v, want row 1 csv 7 inv 3", mismatches[0])
	}
	if mismatches[1].Row != 5 || mismatches[1].CSVStock != 9 || mismatches[1].InventoryStock != 3 {
		t.Fatalf("mismatch[1] = %+v, want row 5 csv 9 inv 3", mismatches[1])
	}

	// Report-only: the quantity is left unchanged, dates are updated.
	var s1 models.InventoryStock
	if err := db.Where("product_id = ? AND batch_no = ?", p1.ID, "Batch #1").First(&s1).Error; err != nil {
		t.Fatalf("load stock: %v", err)
	}
	if s1.Quantity != 3 || s1.AvailableQty != 3 {
		t.Fatalf("qty = %v avail = %v, want 3/3 (unchanged)", s1.Quantity, s1.AvailableQty)
	}
	if s1.ExpDate == nil || s1.ExpDate.Format("02/01/2006") != "31/12/2026" {
		t.Fatalf("exp date = %v, want 31/12/2026", s1.ExpDate)
	}
	if s1.MfgDate == nil || s1.MfgDate.Format("02/01/2006") != "01/01/2025" {
		t.Fatalf("mfg date = %v, want 01/01/2025", s1.MfgDate)
	}

	// Price disambiguation + normalized batch match: BATCH#2 landed on the
	// 12/25 variant (p2) via its lowercase "batch#2" row; equal qty → no
	// mismatch, expiry date updated.
	var s2 models.InventoryStock
	if err := db.Where("product_id = ? AND batch_no = ?", p2.ID, "batch#2").First(&s2).Error; err != nil {
		t.Fatalf("load stock p2: %v", err)
	}
	if s2.Quantity != 4 {
		t.Fatalf("p2 qty = %v, want 4", s2.Quantity)
	}
	if s2.ExpDate == nil || s2.ExpDate.Format("02/01/2006") != "15/06/2027" {
		t.Fatalf("p2 exp date = %v, want 15/06/2027", s2.ExpDate)
	}
	// Batching is enabled since the row carried a batch number.
	var p2After models.Product
	if err := db.First(&p2After, p2.ID).Error; err != nil {
		t.Fatalf("reload p2: %v", err)
	}
	if !p2After.EnableBatching {
		t.Fatal("p2 enable_batching = false, want true")
	}

	// Re-import is idempotent: no new products or stock rows are created.
	result, errs, err = importBatchedItemsRows(userID, content, nil, nil)
	if err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if got := len(result["mismatches"].([]batchStockMismatch)); got != 2 {
		t.Fatalf("re-import mismatches = %d, want 2", got)
	}
	var productCount, stockCount int64
	db.Model(&models.Product{}).Where("user_id = ?", userID).Count(&productCount)
	db.Model(&models.InventoryStock{}).Where("user_id = ?", userID).Count(&stockCount)
	if productCount != 3 {
		t.Fatalf("products = %d, want 3", productCount)
	}
	if stockCount != 2 {
		t.Fatalf("stock rows = %d, want 2", stockCount)
	}
}
