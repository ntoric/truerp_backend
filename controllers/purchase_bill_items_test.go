package controllers

import (
	"strings"
	"sync"
	"testing"
	"time"
	"truerp/models"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openPurchaseBillItemTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:purchase_bill_items_" + uuid.NewString() + "?mode=memory&cache=shared&_busy_timeout=5000"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(5)
	if err := db.Exec("PRAGMA busy_timeout = 5000").Error; err != nil {
		t.Fatalf("busy_timeout: %v", err)
	}
	if err := db.AutoMigrate(&models.PurchaseBill{}, &models.PurchaseBillItem{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func seedPurchaseBill(t *testing.T, db *gorm.DB) models.PurchaseBill {
	t.Helper()
	bill := models.PurchaseBill{
		ID:          uuid.New(),
		UserID:      uuid.New(),
		PartyID:     uuid.New(),
		BillNumber:  "PINV-TEST",
		BillDate:    time.Now(),
		Status:      "draft",
		StockStatus: "none",
	}
	if err := db.Omit("Items").Create(&bill).Error; err != nil {
		t.Fatalf("seed bill: %v", err)
	}
	return bill
}

func twoLineItems(billID uuid.UUID) []models.PurchaseBillItem {
	return []models.PurchaseBillItem{
		{ID: uuid.New(), BillID: billID, Description: "Rice", Quantity: 1, Unit: "KG"},
		{ID: uuid.New(), BillID: billID, Description: "Oil", Quantity: 2, Unit: "LTR"},
	}
}

func TestReplacePurchaseBillItemsTxReplacesNotAppends(t *testing.T) {
	db := openPurchaseBillItemTestDB(t)
	bill := seedPurchaseBill(t, db)

	if err := replacePurchaseBillItemsTx(db, bill.ID, twoLineItems(bill.ID)); err != nil {
		t.Fatalf("first replace: %v", err)
	}
	if err := replacePurchaseBillItemsTx(db, bill.ID, twoLineItems(bill.ID)); err != nil {
		t.Fatalf("second replace: %v", err)
	}

	var count int64
	if err := db.Model(&models.PurchaseBillItem{}).Where("bill_id = ?", bill.ID).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Fatalf("item count = %d, want 2 (replace, not append)", count)
	}
}

func TestReplacePurchaseBillItemsTxConcurrentDoesNotDouble(t *testing.T) {
	db := openPurchaseBillItemTestDB(t)
	bill := seedPurchaseBill(t, db)
	userID := bill.UserID

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			for attempt := 0; attempt < 30; attempt++ {
				err = db.Transaction(func(tx *gorm.DB) error {
					if lockErr := lockPurchaseBillRow(tx, userID, bill.ID); lockErr != nil {
						return lockErr
					}
					return replacePurchaseBillItemsTx(tx, bill.ID, twoLineItems(bill.ID))
				})
				if err == nil || !strings.Contains(strings.ToLower(err.Error()), "locked") {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent replace: %v", err)
		}
	}

	var count int64
	if err := db.Model(&models.PurchaseBillItem{}).Where("bill_id = ?", bill.ID).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Fatalf("item count = %d, want 2 after overlapping updates", count)
	}
}
