package utils

import (
	"database/sql"
	"fmt"
	"log"
	"sort"
	"strings"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// SQLiteToPostgresOptions configures a one-time SQLite → PostgreSQL data copy.
type SQLiteToPostgresOptions struct {
	SQLitePath string
	PostgresURL string
	BatchSize   int
	Truncate    bool
	DryRun      bool
}

// TableCount holds row counts for validation.
type TableCount struct {
	Table      string
	SQLiteRows int64
	PostgresRows int64
}

// FinancialTotals holds aggregate checks for post-migration validation.
type FinancialTotals struct {
	InvoiceTotal   sql.NullFloat64
	PaymentTotal   sql.NullFloat64
	ExpenseTotal   sql.NullFloat64
	PartyCount     sql.NullInt64
	UserCount      sql.NullInt64
}

// CopyReport summarizes a migration run.
type CopyReport struct {
	Tables      []TableCount
	Skipped     []string
	Errors      []string
	RowsCopied  int64
}

// Preferred table copy order (FK-safe when replication role is not available).
var preferredTableOrder = []string{
	"permissions",
	"users",
	"stores",
	"businesses",
	"parties",
	"categories",
	"expense_categories",
	"warehouses",
	"products",
	"product_images",
	"product_variants",
	"serial_numbers",
	"accounts",
	"bank_accounts",
	"staff",
	"roles",
	"role_permissions",
	"user_roles",
	"invoices",
	"invoice_items",
	"invoice_status_histories",
	"payments",
	"payment_outs",
	"expenses",
	"expense_items",
	"pos_sessions",
	"cash_movements",
	"stock_entries",
	"inventory_stocks",
	"stock_transfers",
	"stock_transfer_items",
	"purchase_orders",
	"purchase_order_items",
	"purchase_receipts",
	"purchase_receipt_items",
	"purchase_bills",
	"purchase_bill_items",
	"journal_entries",
	"journal_entry_lines",
	"ledgers",
	"bank_reconciliations",
	"credit_notes",
	"credit_note_items",
	"debit_notes",
	"debit_note_items",
	"delivery_challans",
	"delivery_challan_items",
	"loyalty_settings",
	"loyalty_transactions",
	"sales_returns",
	"sales_return_items",
	"purchase_returns",
	"purchase_return_items",
	"cash_transactions",
	"payment_method_account_maps",
	"attendances",
	"payrolls",
	"staff_deductions",
	"staff_advance_payments",
	"invoice_settings",
	"print_settings",
	"weighing_scale_settings",
	"reminders",
	"notifications",
	"notification_templates",
	"notification_preferences",
	"ca_report_sharings",
	"drafts",
	"offline_queues",
	"pos_drafts",
	"media_files",
	"developer_settings",
	"page_feature_settings",
	"audit_logs",
	"tax_periods",
	"input_tax_credits",
	"gst_filing_statuses",
	"gstr1_data",
	"gstr3b_data",
	"tax_exemptions",
	"tax_rules",
	"tax_rates",
	"quotations",
	"quotation_items",
	"quotation_versions",
	"customer_statements",
	"statement_transactions",
	"customer_portal_settings",
	"customer_portal_accesses",
	"support_tickets",
	"saved_invoice_templates",
	"invoice_custom_field_definitions",
	"sms_marketings",
	"sms_recipients",
	"email_marketings",
	"email_recipients",
	"whatsapp_marketings",
	"whatsapp_recipients",
	"ip_restrictions",
	"data_backups",
	"gdpr_requests",
}

// OpenSQLiteForMigration opens a read-only SQLite database for migration.
func OpenSQLiteForMigration(path string) (*gorm.DB, error) {
	dsn := fmt.Sprintf("file:%s?mode=ro", path)
	return gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
}

// OpenPostgresForMigration opens PostgreSQL for schema prep and data load.
func OpenPostgresForMigration(databaseURL string) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(databaseURL), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, err
	}
	if err := db.Exec(`CREATE EXTENSION IF NOT EXISTS "uuid-ossp"`).Error; err != nil {
		log.Printf("Warning: uuid-ossp extension setup failed: %v", err)
	}
	return db, nil
}

// PreparePostgresSchema creates all application tables on an empty PostgreSQL database.
func PreparePostgresSchema(db *gorm.DB) error {
	SetDialect(DialectPostgres)
	return MigrateSchema(db)
}

// CopySQLiteToPostgres copies all shared tables from SQLite into PostgreSQL.
func CopySQLiteToPostgres(opts SQLiteToPostgresOptions) (*CopyReport, error) {
	if strings.TrimSpace(opts.SQLitePath) == "" {
		return nil, fmt.Errorf("sqlite path is required")
	}
	if strings.TrimSpace(opts.PostgresURL) == "" {
		return nil, fmt.Errorf("postgres url is required")
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 500
	}

	src, err := OpenSQLiteForMigration(opts.SQLitePath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	srcSQL, err := src.DB()
	if err != nil {
		return nil, err
	}
	defer srcSQL.Close()

	dst, err := OpenPostgresForMigration(opts.PostgresURL)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	dstSQL, err := dst.DB()
	if err != nil {
		return nil, err
	}
	defer dstSQL.Close()

	SetDialect(DialectPostgres)

	report := &CopyReport{}

	if opts.Truncate {
		if opts.DryRun {
			log.Println("[dry-run] would truncate all postgres tables")
		} else if err := truncatePostgresTables(dst); err != nil {
			return report, fmt.Errorf("truncate postgres: %w", err)
		}
	}

	tables, err := orderTablesForCopy(src, dst)
	if err != nil {
		return report, err
	}

	if opts.DryRun {
		for _, table := range tables {
			count, err := countRows(src, table)
			if err != nil {
				report.Errors = append(report.Errors, fmt.Sprintf("%s: count failed: %v", table, err))
				continue
			}
			report.Tables = append(report.Tables, TableCount{Table: table, SQLiteRows: count})
			log.Printf("[dry-run] would copy %s (%d rows)", table, count)
		}
		return report, nil
	}

	if err := setPostgresReplicationRole(dst, true); err != nil {
		return report, fmt.Errorf("disable postgres FK checks: %w", err)
	}
	defer setPostgresReplicationRole(dst, false)

	for _, table := range tables {
		copied, err := copyTable(src, dst, table, opts.BatchSize)
		if err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("%s: %v", table, err))
			continue
		}
		report.RowsCopied += copied
		log.Printf("Copied %s: %d rows", table, copied)
	}

	return report, nil
}

// ValidateSQLitePostgres compares row counts and financial totals between databases.
func ValidateSQLitePostgres(sqlitePath, postgresURL string) ([]TableCount, FinancialTotals, []string, error) {
	src, err := OpenSQLiteForMigration(sqlitePath)
	if err != nil {
		return nil, FinancialTotals{}, nil, err
	}
	srcSQL, _ := src.DB()
	defer srcSQL.Close()

	dst, err := OpenPostgresForMigration(postgresURL)
	if err != nil {
		return nil, FinancialTotals{}, nil, err
	}
	dstSQL, _ := dst.DB()
	defer dstSQL.Close()

	srcTables, err := listSQLiteTables(src)
	if err != nil {
		return nil, FinancialTotals{}, nil, err
	}
	dstSet, err := postgresTableSet(dst)
	if err != nil {
		return nil, FinancialTotals{}, nil, err
	}

	var mismatches []string
	var counts []TableCount
	for _, table := range srcTables {
		if !dstSet[table] {
			continue
		}
		srcCount, err := countRows(src, table)
		if err != nil {
			mismatches = append(mismatches, fmt.Sprintf("%s: sqlite count failed: %v", table, err))
			continue
		}
		dstCount, err := countRows(dst, table)
		if err != nil {
			mismatches = append(mismatches, fmt.Sprintf("%s: postgres count failed: %v", table, err))
			continue
		}
		counts = append(counts, TableCount{
			Table:        table,
			SQLiteRows:   srcCount,
			PostgresRows: dstCount,
		})
		if srcCount != dstCount {
			mismatches = append(mismatches, fmt.Sprintf("%s: sqlite=%d postgres=%d", table, srcCount, dstCount))
		}
	}

	srcTotals := queryFinancialTotals(src)
	dstTotals := queryFinancialTotals(dst)
	if srcTotals.InvoiceTotal.Valid && dstTotals.InvoiceTotal.Valid &&
		srcTotals.InvoiceTotal.Float64 != dstTotals.InvoiceTotal.Float64 {
		mismatches = append(mismatches, fmt.Sprintf(
			"invoice totals differ: sqlite=%.2f postgres=%.2f",
			srcTotals.InvoiceTotal.Float64, dstTotals.InvoiceTotal.Float64,
		))
	}
	if srcTotals.PaymentTotal.Valid && dstTotals.PaymentTotal.Valid &&
		srcTotals.PaymentTotal.Float64 != dstTotals.PaymentTotal.Float64 {
		mismatches = append(mismatches, fmt.Sprintf(
			"payment totals differ: sqlite=%.2f postgres=%.2f",
			srcTotals.PaymentTotal.Float64, dstTotals.PaymentTotal.Float64,
		))
	}
	if srcTotals.ExpenseTotal.Valid && dstTotals.ExpenseTotal.Valid &&
		srcTotals.ExpenseTotal.Float64 != dstTotals.ExpenseTotal.Float64 {
		mismatches = append(mismatches, fmt.Sprintf(
			"expense totals differ: sqlite=%.2f postgres=%.2f",
			srcTotals.ExpenseTotal.Float64, dstTotals.ExpenseTotal.Float64,
		))
	}

	return counts, srcTotals, mismatches, nil
}

func orderTablesForCopy(src, dst *gorm.DB) ([]string, error) {
	srcTables, err := listSQLiteTables(src)
	if err != nil {
		return nil, err
	}
	dstSet, err := postgresTableSet(dst)
	if err != nil {
		return nil, err
	}

	ordered := make([]string, 0, len(srcTables))
	seen := map[string]bool{}

	for _, table := range preferredTableOrder {
		if !containsString(srcTables, table) || !dstSet[table] {
			continue
		}
		ordered = append(ordered, table)
		seen[table] = true
	}

	rest := make([]string, 0)
	for _, table := range srcTables {
		if seen[table] || !dstSet[table] {
			continue
		}
		rest = append(rest, table)
	}
	sort.Strings(rest)
	ordered = append(ordered, rest...)
	return ordered, nil
}

func listSQLiteTables(db *gorm.DB) ([]string, error) {
	var tables []string
	err := db.Raw(`
		SELECT name FROM sqlite_master
		WHERE type = 'table'
		  AND name NOT LIKE 'sqlite_%'
		ORDER BY name
	`).Scan(&tables).Error
	return tables, err
}

func postgresTableSet(db *gorm.DB) (map[string]bool, error) {
	var tables []string
	err := db.Raw(`
		SELECT tablename FROM pg_tables
		WHERE schemaname = CURRENT_SCHEMA()
		ORDER BY tablename
	`).Scan(&tables).Error
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(tables))
	for _, t := range tables {
		set[t] = true
	}
	return set, nil
}

func truncatePostgresTables(db *gorm.DB) error {
	tables, err := listPostgresTables(db)
	if err != nil {
		return err
	}
	if len(tables) == 0 {
		return nil
	}
	quoted := make([]string, 0, len(tables))
	for _, t := range tables {
		quoted = append(quoted, quoteIdent(t))
	}
	sql := "TRUNCATE TABLE " + strings.Join(quoted, ", ") + " RESTART IDENTITY CASCADE"
	return db.Exec(sql).Error
}

func listPostgresTables(db *gorm.DB) ([]string, error) {
	var tables []string
	err := db.Raw(`
		SELECT tablename FROM pg_tables
		WHERE schemaname = CURRENT_SCHEMA()
		ORDER BY tablename
	`).Scan(&tables).Error
	return tables, err
}

func setPostgresReplicationRole(db *gorm.DB, disableFK bool) error {
	if disableFK {
		return db.Exec("SET session_replication_role = replica").Error
	}
	return db.Exec("SET session_replication_role = DEFAULT").Error
}

func countRows(db *gorm.DB, table string) (int64, error) {
	var count int64
	err := db.Table(table).Count(&count).Error
	return count, err
}

func copyTable(src, dst *gorm.DB, table string, batchSize int) (int64, error) {
	columns, err := sharedColumns(src, dst, table)
	if err != nil {
		return 0, err
	}
	if len(columns) == 0 {
		return 0, fmt.Errorf("no shared columns with postgres")
	}

	pgTypes, err := postgresColumnTypes(dst, table)
	if err != nil {
		return 0, err
	}

	srcSQL, err := src.DB()
	if err != nil {
		return 0, err
	}
	dstSQL, err := dst.DB()
	if err != nil {
		return 0, err
	}

	selectCols := strings.Join(quoteColumns(columns), ", ")
	query := fmt.Sprintf("SELECT %s FROM %s", selectCols, quoteIdent(table))

	rows, err := srcSQL.Query(query)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var copied int64
	batch := make([][]interface{}, 0, batchSize)

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := insertBatch(dstSQL, table, columns, batch); err != nil {
			return err
		}
		copied += int64(len(batch))
		batch = batch[:0]
		return nil
	}

	for rows.Next() {
		raw := make([]interface{}, len(columns))
		ptrs := make([]interface{}, len(columns))
		for i := range raw {
			ptrs[i] = &raw[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return copied, err
		}
		values := make([]interface{}, len(columns))
		for i, col := range columns {
			values[i] = convertForPostgres(pgTypes[strings.ToLower(col)], normalizeSQLiteValue(raw[i]))
		}
		batch = append(batch, values)
		if len(batch) >= batchSize {
			if err := flush(); err != nil {
				return copied, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return copied, err
	}
	if err := flush(); err != nil {
		return copied, err
	}
	return copied, nil
}

func sharedColumns(src, dst *gorm.DB, table string) ([]string, error) {
	srcCols, err := sqliteColumns(src, table)
	if err != nil {
		return nil, err
	}
	dstCols, err := postgresColumns(dst, table)
	if err != nil {
		return nil, err
	}
	dstSet := make(map[string]bool, len(dstCols))
	for _, c := range dstCols {
		dstSet[strings.ToLower(c)] = true
	}

	shared := make([]string, 0, len(srcCols))
	for _, c := range srcCols {
		if dstSet[strings.ToLower(c)] {
			shared = append(shared, c)
		}
	}
	return shared, nil
}

func sqliteColumns(db *gorm.DB, table string) ([]string, error) {
	var cols []string
	rows, err := db.Raw(`SELECT name FROM pragma_table_info(?) ORDER BY cid`, table).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		cols = append(cols, name)
	}
	return cols, rows.Err()
}

func postgresColumns(db *gorm.DB, table string) ([]string, error) {
	var cols []string
	err := db.Raw(`
		SELECT column_name FROM information_schema.columns
		WHERE table_schema = CURRENT_SCHEMA() AND table_name = ?
		ORDER BY ordinal_position
	`, strings.ToLower(table)).Scan(&cols).Error
	return cols, err
}

func postgresColumnTypes(db *gorm.DB, table string) (map[string]string, error) {
	type row struct {
		ColumnName string
		DataType   string
	}
	var rows []row
	err := db.Raw(`
		SELECT column_name, data_type
		FROM information_schema.columns
		WHERE table_schema = CURRENT_SCHEMA() AND table_name = ?
	`, strings.ToLower(table)).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[strings.ToLower(r.ColumnName)] = strings.ToLower(r.DataType)
	}
	return out, nil
}

func normalizeSQLiteValue(v interface{}) interface{} {
	if v == nil {
		return nil
	}
	switch x := v.(type) {
	case []byte:
		return string(x)
	case bool:
		return x
	case int64:
		return x
	case float64:
		return x
	case string:
		return x
	default:
		return fmt.Sprint(x)
	}
}

func convertForPostgres(pgType string, v interface{}) interface{} {
	if v == nil {
		return nil
	}
	if pgType == "boolean" {
		switch x := v.(type) {
		case bool:
			return x
		case int64:
			return x != 0
		case float64:
			return x != 0
		case string:
			return x == "1" || strings.EqualFold(x, "true")
		}
	}
	return v
}

func insertBatch(db *sql.DB, table string, columns []string, rows [][]interface{}) error {
	if len(rows) == 0 {
		return nil
	}

	colList := strings.Join(quoteColumns(columns), ", ")
	var sb strings.Builder
	sb.WriteString("INSERT INTO ")
	sb.WriteString(quoteIdent(table))
	sb.WriteString(" (")
	sb.WriteString(colList)
	sb.WriteString(") VALUES ")

	args := make([]interface{}, 0, len(rows)*len(columns))
	argNum := 1
	for i, row := range rows {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString("(")
		for j := range columns {
			if j > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(fmt.Sprintf("$%d", argNum))
			argNum++
			args = append(args, row[j])
		}
		sb.WriteString(")")
	}

	_, err := db.Exec(sb.String(), args...)
	return err
}

func queryFinancialTotals(db *gorm.DB) FinancialTotals {
	var totals FinancialTotals
	db.Raw(`SELECT COALESCE(SUM(total_amount), 0) FROM invoices WHERE deleted_at IS NULL`).Scan(&totals.InvoiceTotal)
	db.Raw(`SELECT COALESCE(SUM(amount), 0) FROM payments`).Scan(&totals.PaymentTotal)
	db.Raw(`SELECT COALESCE(SUM(amount), 0) FROM expenses`).Scan(&totals.ExpenseTotal)
	db.Raw(`SELECT COUNT(*) FROM parties`).Scan(&totals.PartyCount)
	db.Raw(`SELECT COUNT(*) FROM users`).Scan(&totals.UserCount)
	return totals
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func quoteColumns(columns []string) []string {
	out := make([]string, len(columns))
	for i, c := range columns {
		out[i] = quoteIdent(c)
	}
	return out
}

func containsString(list []string, target string) bool {
	for _, s := range list {
		if s == target {
			return true
		}
	}
	return false
}
