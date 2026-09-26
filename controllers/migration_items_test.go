package controllers

import (
	"strings"
	"testing"

	"truerp/models"
	"truerp/utils"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openItemsTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:items?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Invoice{},
		&models.InvoiceItem{},
		&models.PurchaseBill{},
		&models.PurchaseBillItem{},
		&models.Product{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// Uploading the myBillBook "Sales" summary export to the sales-items importer
// must fail fast with a pointer to the correct importer, not one
// "invoice not found" error per row.
func TestImportSalesItemsRows_RejectsSalesSummaryExport(t *testing.T) {
	csv := "Invoice No,Invoice Date,Contact Name,Amount,Remaining Amount,Invoice Status,Due Date,Invoice Link,Payment Type,Party Category,Created by\n" +
		"19,13/10/2025,GOPINADH,149.0,0.0,Paid,12/11/2025,https://mybillbook.in/cpp/31kcgcjdnb,credit,,Admin\n" +
		"20,13/10/2025,poornima,230.0,0.0,Paid,12/11/2025,https://mybillbook.in/cpp/wmo4rd7gj0,cash,,Admin\n"

	_, _, err := importSalesItemsRows(uuid.New(), []byte(csv), nil)
	if err == nil || !strings.Contains(err.Error(), "Sales (invoices)") {
		t.Fatalf("expected redirect to Sales (invoices) importer, got: %v", err)
	}
}

// A file with no item column at all gets the generic missing-column error.
func TestImportSalesItemsRows_MissingItemColumn(t *testing.T) {
	csv := "invoice number,quantity,rate\nINV-1,2,50\n"

	_, _, err := importSalesItemsRows(uuid.New(), []byte(csv), nil)
	if err == nil || !strings.Contains(err.Error(), "item name") {
		t.Fatalf("expected missing item name error, got: %v", err)
	}
}

// A well-formed items file against an empty DB reports a per-row error that
// hints at running the invoice import first.
func TestImportSalesItemsRows_InvoiceNotFoundHint(t *testing.T) {
	db := openItemsTestDB(t)
	previousDB := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = previousDB })

	csv := "invoice number,item name,quantity,rate,amount\nINV-1,Widget,2,50,100\n"

	_, errs, err := importSalesItemsRows(uuid.New(), []byte(csv), nil)
	if err != nil {
		t.Fatalf("unexpected fatal error: %v", err)
	}
	if len(errs) != 1 || !strings.Contains(errs[0], "Sales (invoices)") {
		t.Fatalf("expected invoice-not-found hint, got: %v", errs)
	}
}

// Same fail-fast guard for purchase bills: the myBillBook purchase summary
// export must be rejected with a pointer to "Purchases (bills)".
func TestImportPurchaseItemsRows_RejectsPurchaseSummaryExport(t *testing.T) {
	csv := "Purchase No,Original Invoice No,Purchase Date,Party Name,Purchase Amount,Purchase link,Notes\n" +
		"1,INV-001,01/09/2025,Global Supplies,15000,,Monthly stock\n"

	_, _, err := importPurchaseItemsRows(uuid.New(), []byte(csv), nil)
	if err == nil || !strings.Contains(err.Error(), "Purchases (bills)") {
		t.Fatalf("expected redirect to Purchases (bills) importer, got: %v", err)
	}
}
