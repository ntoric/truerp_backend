package controllers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupStockCheckTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.Product{}, &models.InventoryStock{}, &models.StockEntry{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	previousDB := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = previousDB })
	return db
}

func seedStockCheckProduct(t *testing.T, db *gorm.DB, userID uuid.UUID, name, batchNo string, available float64) models.Product {
	t.Helper()
	product := models.Product{ID: uuid.New(), UserID: userID, Name: name, SKU: uuid.NewString()}
	if err := db.Create(&product).Error; err != nil {
		t.Fatalf("create product: %v", err)
	}
	stock := models.InventoryStock{
		ID:           uuid.New(),
		UserID:       userID,
		ProductID:    product.ID,
		OutletID:     uuid.New(),
		BatchNo:      batchNo,
		Quantity:     available,
		AvailableQty: available,
	}
	if err := db.Create(&stock).Error; err != nil {
		t.Fatalf("create stock: %v", err)
	}
	return product
}

func postStockCheck(t *testing.T, userID uuid.UUID, payload string) (int, struct {
	Warnings []invoiceStockWarning `json:"warnings"`
}) {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/invoices/check-stock", bytes.NewBufferString(payload))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user_id", userID)
	CheckInvoiceStock(c)

	var body struct {
		Warnings []invoiceStockWarning `json:"warnings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rec.Body.String())
	}
	return rec.Code, body
}

func TestCheckInvoiceStockWarnsWhenStockWouldGoNegative(t *testing.T) {
	db := setupStockCheckTestDB(t)
	userID := uuid.New()
	product := seedStockCheckProduct(t, db, userID, "Badam", "", 3)

	code, body := postStockCheck(t, userID, fmt.Sprintf(
		`{"items":[{"product_id":%q,"quantity":5}]}`, product.ID))
	if code != 200 {
		t.Fatalf("status = %d, want 200", code)
	}
	if len(body.Warnings) != 1 {
		t.Fatalf("warnings = %d, want 1", len(body.Warnings))
	}
	w := body.Warnings[0]
	if w.ProductID != product.ID || w.ProductName != "Badam" {
		t.Fatalf("unexpected warning product: %+v", w)
	}
	if w.AvailableQty != 3 || w.RequestedQty != 5 || w.ResultingQty != -2 {
		t.Fatalf("unexpected quantities: %+v", w)
	}
}

func TestCheckInvoiceStockNoWarningWhenStockSufficient(t *testing.T) {
	db := setupStockCheckTestDB(t)
	userID := uuid.New()
	product := seedStockCheckProduct(t, db, userID, "Cashew", "", 10)

	_, body := postStockCheck(t, userID, fmt.Sprintf(
		`{"items":[{"product_id":%q,"quantity":10}]}`, product.ID))
	if len(body.Warnings) != 0 {
		t.Fatalf("warnings = %d, want 0", len(body.Warnings))
	}
}

func TestCheckInvoiceStockWarnsPerPinnedBatch(t *testing.T) {
	db := setupStockCheckTestDB(t)
	userID := uuid.New()
	product := models.Product{ID: uuid.New(), UserID: userID, Name: "Kismis", SKU: uuid.NewString()}
	if err := db.Create(&product).Error; err != nil {
		t.Fatalf("create product: %v", err)
	}
	for _, row := range []struct {
		batch string
		qty   float64
	}{{"B1", 2}, {"B2", 20}} {
		if err := db.Create(&models.InventoryStock{
			ID: uuid.New(), UserID: userID, ProductID: product.ID,
			OutletID: uuid.New(), BatchNo: row.batch,
			Quantity: row.qty, AvailableQty: row.qty,
		}).Error; err != nil {
			t.Fatalf("create stock: %v", err)
		}
	}

	// Product total (22) covers 5 units, but pinned batch B1 only has 2.
	_, body := postStockCheck(t, userID, fmt.Sprintf(
		`{"items":[{"product_id":%q,"quantity":5,"batch_no":"B1"}]}`, product.ID))
	if len(body.Warnings) != 1 {
		t.Fatalf("warnings = %d, want 1", len(body.Warnings))
	}
	w := body.Warnings[0]
	if w.BatchNo != "B1" || w.AvailableQty != 2 || w.ResultingQty != -3 {
		t.Fatalf("unexpected batch warning: %+v", w)
	}
}

func TestCheckInvoiceStockEditRestoresPreviouslyDeducted(t *testing.T) {
	db := setupStockCheckTestDB(t)
	userID := uuid.New()
	product := seedStockCheckProduct(t, db, userID, "Pista", "", 3)
	invoiceID := uuid.New()

	// The invoice being edited already consumed 5 units.
	if err := db.Create(&models.StockEntry{
		ID: uuid.New(), UserID: userID, ItemName: "Pista",
		ProductID: &product.ID, OutletID: uuid.New(), EntryType: "sale",
		Quantity: -5, ReferenceID: invoiceID, ReferenceType: "invoice",
		ApprovalStatus: "approved",
	}).Error; err != nil {
		t.Fatalf("create stock entry: %v", err)
	}

	// Editing the same invoice to qty 7: 3 available + 5 restored = 8 → no warning.
	payload := fmt.Sprintf(
		`{"invoice_id":%q,"items":[{"product_id":%q,"quantity":7}]}`, invoiceID, product.ID)
	_, body := postStockCheck(t, userID, payload)
	if len(body.Warnings) != 0 {
		t.Fatalf("warnings = %d, want 0 (restored stock must count)", len(body.Warnings))
	}

	// Qty 9 exceeds 8 → warning.
	payload = fmt.Sprintf(
		`{"invoice_id":%q,"items":[{"product_id":%q,"quantity":9}]}`, invoiceID, product.ID)
	_, body = postStockCheck(t, userID, payload)
	if len(body.Warnings) != 1 || body.Warnings[0].AvailableQty != 8 || body.Warnings[0].ResultingQty != -1 {
		t.Fatalf("unexpected warnings: %+v", body.Warnings)
	}
}

func TestCheckInvoiceStockSkipsNonProductLines(t *testing.T) {
	setupStockCheckTestDB(t)
	userID := uuid.New()

	_, body := postStockCheck(t, userID, `{"items":[{"description":"Custom item","quantity":5}]}`)
	if len(body.Warnings) != 0 {
		t.Fatalf("warnings = %d, want 0", len(body.Warnings))
	}
}
