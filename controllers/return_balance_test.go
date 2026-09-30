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

func openReturnBalanceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Party{},
		&models.SalesReturn{},
		&models.SalesReturnItem{},
		&models.PurchaseReturn{},
		&models.PurchaseReturnItem{},
		&models.StockEntry{},
		&models.Warehouse{},
		&models.InventoryStock{},
		&models.InvoiceItem{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	previousDB := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = previousDB })
	return db
}

func processContext(path, id string) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", path, nil)
	c.Params = gin.Params{{Key: "id", Value: id}}
	return c, rec
}

func reloadPartyBalance(t *testing.T, db *gorm.DB, partyID uuid.UUID) float64 {
	t.Helper()
	var party models.Party
	if err := db.First(&party, "id = ?", partyID).Error; err != nil {
		t.Fatalf("reload party: %v", err)
	}
	return party.Balance
}

func TestProcessSalesReturnReducesCustomerBalance(t *testing.T) {
	db := openReturnBalanceTestDB(t)
	userID := uuid.New()

	party := models.Party{ID: uuid.New(), UserID: userID, Name: "Customer", PartyType: "customer", Balance: 1000}
	if err := db.Create(&party).Error; err != nil {
		t.Fatalf("create party: %v", err)
	}

	salesReturn := models.SalesReturn{
		ID:             uuid.New(),
		UserID:         userID,
		PartyID:        party.ID,
		ReturnNumber:   "SR-0001",
		Date:           time.Now(),
		Amount:         300,
		DeductionTotal: 50,
		Status:         "draft",
		Items: []models.SalesReturnItem{{
			ID:          uuid.New(),
			Description: "Widget",
			Quantity:    2,
			UnitPrice:   150,
			Total:       300,
		}},
	}
	if err := db.Create(&salesReturn).Error; err != nil {
		t.Fatalf("create sales return: %v", err)
	}

	c, rec := processContext("/sales-returns/"+salesReturn.ID.String()+"/process", salesReturn.ID.String())
	c.Set("user_id", userID)
	ProcessSalesReturn(c)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	// Net refund = 300 - 50 deductions = 250 credited back to the customer.
	if got := reloadPartyBalance(t, db, party.ID); got != 750 {
		t.Fatalf("party balance = %.2f, want 750", got)
	}
}

func TestProcessPurchaseReturnReducesVendorPayable(t *testing.T) {
	db := openReturnBalanceTestDB(t)
	userID := uuid.New()

	vendor := models.Party{ID: uuid.New(), UserID: userID, Name: "Vendor", PartyType: "vendor", Balance: -5000}
	if err := db.Create(&vendor).Error; err != nil {
		t.Fatalf("create vendor: %v", err)
	}

	purchaseReturn := models.PurchaseReturn{
		ID:           uuid.New(),
		UserID:       userID,
		PartyID:      vendor.ID,
		ReturnNumber: "PR-0001",
		Date:         time.Now(),
		Amount:       1200,
		Status:       "draft",
		Items: []models.PurchaseReturnItem{{
			ID:          uuid.New(),
			Description: "Raw material",
			Quantity:    10,
			UnitPrice:   120,
			Total:       1200,
		}},
	}
	if err := db.Create(&purchaseReturn).Error; err != nil {
		t.Fatalf("create purchase return: %v", err)
	}

	c, rec := processContext("/purchase-returns/"+purchaseReturn.ID.String()+"/process", purchaseReturn.ID.String())
	c.Set("user_id", userID)
	ProcessPurchaseReturn(c)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	// Vendor payable shrinks: -5000 + 1200 = -3800.
	if got := reloadPartyBalance(t, db, vendor.ID); got != -3800 {
		t.Fatalf("vendor balance = %.2f, want -3800", got)
	}
}

func TestDraftSalesReturnDeleteLeavesBalanceUntouched(t *testing.T) {
	db := openReturnBalanceTestDB(t)
	userID := uuid.New()

	party := models.Party{ID: uuid.New(), UserID: userID, Name: "Customer", PartyType: "customer", Balance: 1000}
	if err := db.Create(&party).Error; err != nil {
		t.Fatalf("create party: %v", err)
	}

	salesReturn := models.SalesReturn{
		ID:           uuid.New(),
		UserID:       userID,
		PartyID:      party.ID,
		ReturnNumber: "SR-0002",
		Date:         time.Now(),
		Amount:       300,
		Status:       "draft",
	}
	if err := db.Create(&salesReturn).Error; err != nil {
		t.Fatalf("create sales return: %v", err)
	}

	c, rec := deleteContext("/sales-returns/"+salesReturn.ID.String(), salesReturn.ID.String())
	c.Set("user_id", userID)
	DeleteSalesReturn(c)
	if rec.Code != 200 {
		t.Fatalf("delete status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := reloadPartyBalance(t, db, party.ID); got != 1000 {
		t.Fatalf("party balance after draft delete = %.2f, want 1000", got)
	}
}
