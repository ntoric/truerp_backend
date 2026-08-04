package utils

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"truerp/models"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var DB *gorm.DB

func countSQLiteTableRows(dbPath, table string) int {
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return 0
	}
	sqlDB, err := db.DB()
	if err != nil {
		return 0
	}
	defer sqlDB.Close()

	var count int64
	if err := db.Table(table).Count(&count).Error; err != nil {
		return 0
	}
	return int(count)
}

func resolveDatabasePath() string {
	if custom := strings.TrimSpace(os.Getenv("DATABASE_PATH")); custom != "" {
		return custom
	}

	dataDir := "data"
	truerpPath := filepath.Join(dataDir, "truerp.db")
	legacyPath := filepath.Join(dataDir, "billbook.db")

	_, legacyErr := os.Stat(legacyPath)
	_, truerpErr := os.Stat(truerpPath)

	if legacyErr == nil && truerpErr != nil {
		return legacyPath
	}

	if legacyErr == nil && truerpErr == nil {
		legacyUsers := countSQLiteTableRows(legacyPath, "users")
		truerpUsers := countSQLiteTableRows(truerpPath, "users")
		if legacyUsers > 0 && truerpUsers == 0 {
			log.Printf("Using legacy database %s (%d users); %s has no users yet", legacyPath, legacyUsers, truerpPath)
			return legacyPath
		}
	}

	return truerpPath
}

func InitDatabase() *gorm.DB {
	dbPath := resolveDatabasePath()
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		log.Fatal("Failed to create data directory:", err)
	}

	log.Printf("Opening database: %s", dbPath)

	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Info),
	})
	if err != nil {
		log.Fatal("Failed to connect to database:", err)
	}

	// Auto-migrate models
	err = db.AutoMigrate(
		&models.User{},
		&models.Store{},
		&models.Business{},
		&models.Invoice{},
		&models.InvoiceItem{},
		&models.Payment{},
		&models.PaymentOut{},
		&models.Expense{},
		&models.ExpenseItem{},
		&models.Category{},
		&models.ExpenseCategory{},
		&models.POSSession{},
		&models.CashMovement{},
		&models.StockEntry{},
		&models.StockTransfer{},
		&models.StockTransferItem{},
		&models.PurchaseOrder{},
		&models.PurchaseOrderItem{},
		&models.PurchaseReceipt{},
		&models.PurchaseReceiptItem{},
		&models.PurchaseBill{},
		&models.PurchaseBillItem{},
		&models.Account{},
		&models.JournalEntry{},
		&models.JournalEntryLine{},
		&models.Ledger{},
		&models.BankReconciliation{},
		&models.CreditNote{},
		&models.CreditNoteItem{},
		&models.DebitNote{},
		&models.DebitNoteItem{},
		&models.DeliveryChallan{},
		&models.DeliveryChallanItem{},
		&models.Party{},
		&models.LoyaltySettings{},
		&models.LoyaltyTransaction{},
		&models.SalesReturn{},
		&models.SalesReturnItem{},
		&models.PurchaseReturn{},
		&models.PurchaseReturnItem{},
		&models.BankAccount{},
		&models.CashTransaction{},
		&models.PaymentMethodAccountMap{},
		&models.Staff{},
		&models.Attendance{},
		&models.Payroll{},
		&models.StaffDeduction{},
		&models.StaffAdvancePayment{},
		&models.InvoiceSettings{},
		&models.PrintSettings{},
		&models.WeighingScaleSettings{},
		&models.Reminder{},
		&models.Notification{},
		&models.NotificationTemplate{},
		&models.NotificationPreference{},
		&models.CAReportSharing{},
		&models.Product{},
		&models.Draft{},
		&models.OfflineQueue{},
		&models.POSDraft{},
		&models.MediaFile{},
		&models.DeveloperSettings{},
		&models.PageFeatureSettings{},
		&models.AuditLog{},
		&models.Role{},
		&models.Permission{},
		&models.UserRole{},
		&models.Warehouse{},
		&models.InventoryStock{},
		// GST Compliance Models
		&models.TaxPeriod{},
		&models.InputTaxCredit{},
		&models.GSTFilingStatus{},
		&models.GSTR1Data{},
		&models.GSTR3BData{},
		&models.CustomerStatement{},
		&models.StatementTransaction{},
		&models.CustomerPortalSettings{},
		&models.CustomerPortalAccess{},
		&models.SupportTicket{},
		&models.SavedInvoiceTemplate{},
		&models.InvoiceCustomFieldDefinition{},
		&models.InvoiceStatusHistory{},
	)
	if err != nil {
		log.Fatal("Failed to migrate database:", err)
	}

	// Fix legacy columns that may have NOT NULL constraints
	runRawMigrations(db)

	DB = db
	SeedPermissions(db)
	return db
}

func runRawMigrations(db *gorm.DB) {
	// SQLite doesn't support DROP COLUMN directly; we handle legacy NOT NULL columns
	// by recreating tables if the old column causes issues.
	// Workaround: set vendor_id = party_id for existing purchase_bills rows
	db.Exec(`UPDATE purchase_bills SET vendor_id = party_id WHERE vendor_id IS NULL AND party_id IS NOT NULL`)

	migrateInvoicesDropLegacyCustomerID(db)
	migrateBarcodeColumnsToItemCode(db)
	backfillExpenseCategories(db)
}

// backfillExpenseCategories copies distinct expense.category strings into
// expense_categories so existing expenses keep working after the split from product categories.
func backfillExpenseCategories(db *gorm.DB) {
	type row struct {
		UserID   uuid.UUID
		Category string
	}
	var rows []row
	if err := db.Model(&models.Expense{}).
		Select("DISTINCT user_id, category").
		Where("category <> '' AND category IS NOT NULL").
		Scan(&rows).Error; err != nil {
		log.Printf("backfillExpenseCategories: failed to scan expenses: %v", err)
		return
	}

	for _, r := range rows {
		var existing models.ExpenseCategory
		err := db.Where("user_id = ? AND name = ?", r.UserID, r.Category).First(&existing).Error
		if err == nil {
			continue
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			log.Printf("backfillExpenseCategories: lookup failed for %s/%s: %v", r.UserID, r.Category, err)
			continue
		}
		cat := models.ExpenseCategory{
			ID:       uuid.New(),
			UserID:   r.UserID,
			Name:     r.Category,
			IsActive: true,
		}
		if err := db.Create(&cat).Error; err != nil {
			log.Printf("backfillExpenseCategories: create failed for %s/%s: %v", r.UserID, r.Category, err)
		}
	}
}

func migrateBarcodeColumnsToItemCode(db *gorm.DB) {
	renameColumnIfExists(db, "products", "barcode", "item_code")
	renameColumnIfExists(db, "stock_entries", "barcode", "item_code")
	renameColumnIfExists(db, "purchase_bill_items", "barcode", "item_code")
	db.Exec(`UPDATE weighing_scale_settings SET csv_item_match_field = 'item_code' WHERE csv_item_match_field = 'barcode'`)
}

func renameColumnIfExists(db *gorm.DB, table, fromCol, toCol string) {
	var legacyCol string
	if err := db.Raw(
		`SELECT name FROM pragma_table_info(?) WHERE name = ?`,
		table, fromCol,
	).Scan(&legacyCol).Error; err != nil || legacyCol == "" {
		return
	}
	var newCol string
	if err := db.Raw(
		`SELECT name FROM pragma_table_info(?) WHERE name = ?`,
		table, toCol,
	).Scan(&newCol).Error; err == nil && newCol != "" {
		return
	}
	log.Printf("Migrating %s: renaming column %s to %s", table, fromCol, toCol)
	if err := db.Exec(fmt.Sprintf(
		"ALTER TABLE %s RENAME COLUMN %s TO %s",
		table, fromCol, toCol,
	)).Error; err != nil {
		log.Printf("Warning: failed to rename %s.%s: %v", table, fromCol, err)
	}
}

// migrateInvoicesDropLegacyCustomerID removes the pre-parties customer_id column from invoices.
// Older schemas required customer_id (FK to customers) while the app now uses party_id only.
func migrateInvoicesDropLegacyCustomerID(db *gorm.DB) {
	var legacyCol string
	if err := db.Raw(`SELECT name FROM pragma_table_info('invoices') WHERE name = 'customer_id'`).Scan(&legacyCol).Error; err != nil || legacyCol == "" {
		return
	}

	log.Println("Migrating invoices table: dropping legacy customer_id column")

	db.Exec(`PRAGMA foreign_keys = OFF`)
	defer db.Exec(`PRAGMA foreign_keys = ON`)

	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`
			CREATE TABLE IF NOT EXISTS invoices_new (
				id uuid DEFAULT (uuid_generate_v4()),
				user_id uuid NOT NULL,
				invoice_number text NOT NULL,
				invoice_type text DEFAULT 'tax_invoice',
				party_id uuid NOT NULL,
				date datetime NOT NULL,
				due_date datetime,
				payment_terms integer DEFAULT 0,
				status text DEFAULT 'draft',
				sub_total real DEFAULT 0,
				discount_total real DEFAULT 0,
				invoice_discount real DEFAULT 0,
				additional_charges real DEFAULT 0,
				tax_total real DEFAULT 0,
				cgst_total real DEFAULT 0,
				sgst_total real DEFAULT 0,
				igst_total real DEFAULT 0,
				round_off real DEFAULT 0,
				total_amount real DEFAULT 0,
				amount_paid real DEFAULT 0,
				payment_mode text,
				bank_account_id uuid,
				notes text,
				terms text,
				is_inter_state numeric DEFAULT false,
				e_way_bill_required numeric DEFAULT false,
				e_way_bill_number text,
				e_way_bill_status text DEFAULT 'pending',
				e_way_bill_valid_until datetime,
				signature text,
				place_of_supply text,
				reverse_charge numeric DEFAULT false,
				irn text,
				e_invoice_status text DEFAULT 'pending',
				e_invoice_generated_at datetime,
				created_at datetime,
				updated_at datetime,
				deleted_at datetime,
				PRIMARY KEY (id),
				CONSTRAINT fk_invoices_party FOREIGN KEY (party_id) REFERENCES parties(id)
			)
		`).Error; err != nil {
			return err
		}

		if err := tx.Exec(`
			INSERT INTO invoices_new (
				id, user_id, invoice_number, invoice_type, party_id, date, due_date, payment_terms,
				status, sub_total, discount_total, invoice_discount, additional_charges, tax_total,
				cgst_total, sgst_total, igst_total, round_off, total_amount, amount_paid, payment_mode,
				bank_account_id, notes, terms, is_inter_state, e_way_bill_required, e_way_bill_number,
				e_way_bill_status, e_way_bill_valid_until, signature, place_of_supply, reverse_charge,
				irn, e_invoice_status, e_invoice_generated_at, created_at, updated_at, deleted_at
			)
			SELECT
				id, user_id, invoice_number, invoice_type,
				COALESCE(party_id, customer_id), date, due_date, payment_terms,
				status, sub_total, discount_total, invoice_discount, additional_charges, tax_total,
				cgst_total, sgst_total, igst_total, round_off, total_amount, amount_paid, payment_mode,
				bank_account_id, notes, terms, is_inter_state, e_way_bill_required, e_way_bill_number,
				e_way_bill_status, e_way_bill_valid_until, signature, place_of_supply, reverse_charge,
				irn, e_invoice_status, e_invoice_generated_at, created_at, updated_at, deleted_at
			FROM invoices
		`).Error; err != nil {
			return err
		}

		if err := tx.Exec(`DROP TABLE invoices`).Error; err != nil {
			return err
		}
		if err := tx.Exec(`ALTER TABLE invoices_new RENAME TO invoices`).Error; err != nil {
			return err
		}

		tx.Exec(`CREATE INDEX IF NOT EXISTS idx_invoices_user_id ON invoices(user_id)`)
		tx.Exec(`CREATE INDEX IF NOT EXISTS idx_invoices_invoice_number ON invoices(invoice_number)`)
		tx.Exec(`CREATE INDEX IF NOT EXISTS idx_invoices_deleted_at ON invoices(deleted_at)`)
		tx.Exec(`CREATE INDEX IF NOT EXISTS idx_invoices_bank_account_id ON invoices(bank_account_id)`)

		return nil
	})

	if err != nil {
		log.Printf("Warning: invoices customer_id migration failed: %v", err)
	}
}
