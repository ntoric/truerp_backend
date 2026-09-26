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

func openPurchaseBillsTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:purchasebills?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Party{},
		&models.PurchaseBill{},
		&models.PurchaseBillItem{},
		&models.Warehouse{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestDocNumberKey(t *testing.T) {
	cases := map[string]string{
		"1":                  "1",
		"P-0001":             "1",
		"p-0001":             "1",
		"PINV-1780805860049": "1780805860049",
		"0007":               "7",
		"ABC":                "ABC",
		"  P-0042  ":         "42",
	}
	for in, want := range cases {
		if got := docNumberKey(in); got != want {
			t.Errorf("docNumberKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// The summary import must update the source link on an already-imported bill
// (matched prefix-agnostic) instead of re-creating it, and list unmatched rows
// without importing them until confirmed via importUnmatched.
func TestImportPurchaseBillsRows_MatchAndUnmatched(t *testing.T) {
	db := openPurchaseBillsTestDB(t)
	previousDB := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = previousDB })

	userID := uuid.New()

	vendor := models.Party{ID: uuid.New(), UserID: userID, Name: "Vendor Co", PartyType: "vendor", IsActive: true}
	if err := db.Create(&vendor).Error; err != nil {
		t.Fatalf("seed vendor: %v", err)
	}
	existing := models.PurchaseBill{
		ID:          uuid.New(),
		UserID:      userID,
		PartyID:     vendor.ID,
		BillNumber:  "P-0001",
		BillDate:    time.Now(),
		Status:      "unpaid",
		TotalAmount: 1500,
	}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatalf("seed bill: %v", err)
	}

	csv := "Purchase No,Original Invoice No,Purchase Date,Party Name,Purchase Amount,Purchase link,Notes\n" +
		"1,INV-1,01/09/2025,Vendor Co,1500,https://mybillbook.in/cpp/abc123,\n" +
		"2,INV-2,02/09/2025,Other Vendor,800,https://mybillbook.in/cpp/def456,\n"

	res, errs, err := importPurchaseBillsRows(userID, []byte(csv), false, "", false, nil)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("unexpected row errors: %v", errs)
	}
	if got := res["imported"].(int); got != 0 {
		t.Fatalf("imported = %d, want 0", got)
	}
	if got := res["updated"].(int); got != 1 {
		t.Fatalf("updated = %d, want 1", got)
	}
	unmatched, ok := res["unmatched"].([]unmatchedDocRow)
	if !ok || len(unmatched) != 1 || unmatched[0].Ref != "2" || unmatched[0].PartyName != "Other Vendor" {
		t.Fatalf("unmatched = %+v, want row for Purchase No 2", res["unmatched"])
	}

	var updatedBill models.PurchaseBill
	if err := db.Where("user_id = ? AND bill_number = ?", userID, "P-0001").First(&updatedBill).Error; err != nil {
		t.Fatalf("load bill: %v", err)
	}
	if updatedBill.SourceURL != "https://mybillbook.in/cpp/abc123" {
		t.Fatalf("source_url = %q, want purchase link", updatedBill.SourceURL)
	}

	// The unmatched row must not have been created yet.
	var count int64
	db.Model(&models.PurchaseBill{}).Where("user_id = ?", userID).Count(&count)
	if count != 1 {
		t.Fatalf("bill count = %d, want 1 (unmatched row must wait for confirmation)", count)
	}

	// Confirmed re-run imports the unmatched row as P-0002.
	res, errs, err = importPurchaseBillsRows(userID, []byte(csv), false, "", true, nil)
	if err != nil {
		t.Fatalf("confirmed import: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("unexpected row errors on confirm: %v", errs)
	}
	if got := res["imported"].(int); got != 1 {
		t.Fatalf("confirmed imported = %d, want 1", got)
	}
	if len(res["unmatched"].([]unmatchedDocRow)) != 0 {
		t.Fatalf("confirmed unmatched = %+v, want empty", res["unmatched"])
	}
	var created models.PurchaseBill
	if err := db.Where("user_id = ? AND bill_number = ?", userID, "P-0002").First(&created).Error; err != nil {
		t.Fatalf("expected P-0002 to be created: %v", err)
	}
	if created.SourceURL != "https://mybillbook.in/cpp/def456" {
		t.Fatalf("created source_url = %q", created.SourceURL)
	}
}
