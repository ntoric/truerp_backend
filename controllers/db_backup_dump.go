package controllers

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"truerp/utils"
)

// createDBBackupFile writes a compressed database dump into the backup
// directory and returns its path. PostgreSQL produces a plain-SQL dump
// (.sql.gz); SQLite produces a consistent snapshot (.db.gz).
func createDBBackupFile() (string, error) {
	dir := dbBackupDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create backup directory: %w", err)
	}
	stamp := time.Now().Format("2006-01-02_15-04-05")

	switch {
	case utils.IsPostgres():
		outPath := filepath.Join(dir, fmt.Sprintf("truerp_%s.sql.gz", stamp))
		if err := dumpPostgres(outPath); err != nil {
			os.Remove(outPath)
			return "", err
		}
		return outPath, nil
	case utils.IsSQLite():
		outPath := filepath.Join(dir, fmt.Sprintf("truerp_%s.db.gz", stamp))
		if err := dumpSQLite(outPath, dir, stamp); err != nil {
			os.Remove(outPath)
			return "", err
		}
		return outPath, nil
	default:
		return "", fmt.Errorf("unsupported database dialect %q", utils.CurrentDialect())
	}
}

// ---------------------------------------------------------------------------
// SQLite — VACUUM INTO produces a consistent copy while the DB stays online.
// ---------------------------------------------------------------------------

func dumpSQLite(outPath, dir, stamp string) error {
	tmpPath := filepath.Join(dir, fmt.Sprintf("truerp_%s.db.tmp", stamp))
	defer os.Remove(tmpPath)

	abs, err := filepath.Abs(tmpPath)
	if err != nil {
		return fmt.Errorf("resolve temp path: %w", err)
	}
	escaped := strings.ReplaceAll(abs, "'", "''")
	if err := utils.DB.Exec(fmt.Sprintf("VACUUM INTO '%s'", escaped)).Error; err != nil {
		return fmt.Errorf("sqlite snapshot: %w", err)
	}
	if err := gzipCopyFile(tmpPath, outPath); err != nil {
		return fmt.Errorf("compress snapshot: %w", err)
	}
	return nil
}

// gzipCopyFile streams src into dst through a gzip writer.
func gzipCopyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}

	gz := gzip.NewWriter(out)
	_, copyErr := io.Copy(gz, in)
	closeErr := gz.Close()
	fileErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return fileErr
}

// ---------------------------------------------------------------------------
// PostgreSQL — prefer pg_dump when the binary is available (PG_DUMP_PATH or
// PATH); otherwise fall back to a pure-Go logical dump so backups still work
// in slim containers and the desktop app where pg_dump is not installed.
// ---------------------------------------------------------------------------

func dumpPostgres(outPath string) error {
	bin := strings.TrimSpace(os.Getenv("PG_DUMP_PATH"))
	if bin == "" {
		bin, _ = exec.LookPath("pg_dump")
	}
	dsn := utils.ResolvedDatabaseURL()
	if bin != "" && dsn != "" {
		return runPgDump(bin, dsn, outPath)
	}
	log.Printf("db backup: pg_dump unavailable (bin=%q, dsn configured=%v); using built-in logical dump",
		bin, dsn != "")
	return dumpPostgresLogical(outPath)
}

func runPgDump(bin, dsn, outPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()

	out, err := os.Create(outPath)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(out)

	cmd := exec.CommandContext(ctx, bin, "--no-owner", "--no-privileges", "--dbname", dsn)
	cmd.Stdout = gz
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Env = append(os.Environ(), "PGCONNECT_TIMEOUT=15")

	runErr := cmd.Run()
	gzErr := gz.Close()
	fileErr := out.Close()

	if runErr != nil {
		return fmt.Errorf("pg_dump failed: %v: %s", runErr, strings.TrimSpace(stderr.String()))
	}
	if gzErr != nil {
		return gzErr
	}
	return fileErr
}

// ---------------------------------------------------------------------------
// Pure-Go PostgreSQL logical dump
//
// Emits a plain-format .sql script: DROP + CREATE TABLE (columns, defaults,
// NOT NULL, primary keys), data as multi-row INSERTs with FK triggers
// disabled during load, secondary indexes, remaining constraints, and
// sequence setval calls. Covers everything GORM AutoMigrate creates (this app
// has no views/triggers/functions).
// ---------------------------------------------------------------------------

func dumpPostgresLogical(outPath string) error {
	out, err := os.Create(outPath)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(out)
	w := bufio.NewWriterSize(gz, 256*1024)

	writeErr := func() error {
		if err := w.Flush(); err != nil {
			return err
		}
		if err := gz.Close(); err != nil {
			return err
		}
		return out.Close()
	}
	fail := func(err error) error {
		w.Flush()
		gz.Close()
		out.Close()
		return err
	}

	write := func(format string, args ...interface{}) bool {
		if _, err := fmt.Fprintf(w, format, args...); err != nil {
			return false
		}
		return true
	}

	if !write("-- TruERP database backup (logical dump)\n-- Generated: %s\n-- Dialect: PostgreSQL\n\nSET statement_timeout = 0;\nSET client_encoding = 'UTF8';\n\n",
		time.Now().UTC().Format(time.RFC3339)) {
		return fail(fmt.Errorf("write dump header"))
	}

	var tables []string
	if err := utils.DB.Raw(`
		SELECT table_name FROM information_schema.tables
		WHERE table_schema = 'public' AND table_type = 'BASE TABLE'
		ORDER BY table_name
	`).Scan(&tables).Error; err != nil {
		return fail(fmt.Errorf("list tables: %w", err))
	}

	type constraint struct {
		Name string `gorm:"column:conname"`
		Def  string `gorm:"column:def"`
	}
	var pendingConstraints []string // emitted after all data loads

	for _, table := range tables {
		qt := quotePGIdent(table)
		if !write("\n--\n-- Table: %s\n--\n\nDROP TABLE IF EXISTS %s CASCADE;\n\n", table, qt) {
			return fail(fmt.Errorf("write header for %s", table))
		}

		// Column definitions: name, formatted type, nullability, default expr.
		type col struct {
			Name    string  `gorm:"column:attname"`
			Type    string  `gorm:"column:typ"`
			NotNull bool    `gorm:"column:attnotnull"`
			Default *string `gorm:"column:def"`
		}
		var cols []col
		if err := utils.DB.Raw(`
			SELECT a.attname,
			       pg_catalog.format_type(a.atttypid, a.atttypmod) AS typ,
			       a.attnotnull,
			       pg_get_expr(d.adbin, d.adrelid) AS def
			FROM pg_attribute a
			LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
			WHERE a.attrelid = ?::regclass AND a.attnum > 0 AND NOT a.attisdropped
			ORDER BY a.attnum
		`, qt).Scan(&cols).Error; err != nil {
			return fail(fmt.Errorf("columns for %s: %w", table, err))
		}

		var colDefs []string
		for _, c := range cols {
			def := quotePGIdent(c.Name) + " " + c.Type
			if c.Default != nil {
				def += " DEFAULT " + *c.Default
			}
			if c.NotNull {
				def += " NOT NULL"
			}
			colDefs = append(colDefs, def)
		}

		// Inline the primary key constraint.
		var pk constraint
		err := utils.DB.Raw(`
			SELECT conname, pg_get_constraintdef(oid) AS def
			FROM pg_constraint WHERE conrelid = ?::regclass AND contype = 'p'
		`, qt).Scan(&pk).Error
		if err == nil && pk.Name != "" {
			colDefs = append(colDefs, fmt.Sprintf("CONSTRAINT %s %s", quotePGIdent(pk.Name), pk.Def))
		}

		if !write("CREATE TABLE %s (\n    %s\n);\n\n", qt, strings.Join(colDefs, ",\n    ")) {
			return fail(fmt.Errorf("write CREATE TABLE for %s", table))
		}

		// Data — multi-row INSERTs with FK/enforcement triggers disabled.
		if !write("ALTER TABLE %s DISABLE TRIGGER ALL;\n\n", qt) {
			return fail(fmt.Errorf("write disable triggers for %s", table))
		}
		if err := dumpTableData(w, table); err != nil {
			return fail(fmt.Errorf("dump data for %s: %w", table, err))
		}
		if !write("\nALTER TABLE %s ENABLE TRIGGER ALL;\n", qt) {
			return fail(fmt.Errorf("write enable triggers for %s", table))
		}

		// Secondary indexes (constraint-backed indexes are recreated by the
		// constraint ALTER statements below, so they are excluded here).
		var indexes []struct {
			Name string `gorm:"column:indexname"`
			Def  string `gorm:"column:indexdef"`
		}
		if err := utils.DB.Raw(`
			SELECT indexname, indexdef FROM pg_indexes
			WHERE schemaname = 'public' AND tablename = ?
			  AND indexname NOT IN (
			    SELECT conname FROM pg_constraint WHERE conrelid = ?::regclass
			  )
			ORDER BY indexname
		`, table, qt).Scan(&indexes).Error; err != nil {
			return fail(fmt.Errorf("indexes for %s: %w", table, err))
		}
		for _, idx := range indexes {
			def := idx.Def
			if !strings.Contains(strings.ToUpper(def), " IF NOT EXISTS ") {
				def = strings.Replace(def, "INDEX ", "INDEX IF NOT EXISTS ", 1)
			}
			if !write("%s;\n", def) {
				return fail(fmt.Errorf("write index for %s", table))
			}
		}

		// FK / unique / check constraints are deferred to the end so they can
		// reference tables created later in the dump.
		var cons []constraint
		if err := utils.DB.Raw(`
			SELECT conname, pg_get_constraintdef(oid) AS def
			FROM pg_constraint WHERE conrelid = ?::regclass AND contype IN ('f','u','c')
			ORDER BY conname
		`, qt).Scan(&cons).Error; err != nil {
			return fail(fmt.Errorf("constraints for %s: %w", table, err))
		}
		for _, cn := range cons {
			pendingConstraints = append(pendingConstraints,
				fmt.Sprintf("ALTER TABLE ONLY %s ADD CONSTRAINT %s %s;", qt, quotePGIdent(cn.Name), cn.Def))
		}
	}

	if len(pendingConstraints) > 0 {
		if !write("\n--\n-- Constraints\n--\n\n%s\n", strings.Join(pendingConstraints, "\n")) {
			return fail(fmt.Errorf("write constraints"))
		}
	}

	// Sequence positions so autoincrement/serial values continue correctly.
	var seqs []string
	if err := utils.DB.Raw(`
		SELECT sequence_name FROM information_schema.sequences
		WHERE sequence_schema = 'public' ORDER BY sequence_name
	`).Scan(&seqs).Error; err == nil && len(seqs) > 0 {
		write("\n--\n-- Sequences\n--\n\n")
		for _, seq := range seqs {
			var last int64
			var isCalled bool
			row := utils.DB.Raw(fmt.Sprintf(
				`SELECT last_value, is_called FROM %s`, quotePGIdent(seq))).Row()
			if err := row.Scan(&last, &isCalled); err != nil {
				continue
			}
			write("SELECT pg_catalog.setval('%s', %d, %v);\n", seq, last, isCalled)
		}
	}

	write("\n-- End of dump\n")
	return writeErr()
}

// dumpTableData streams a table's rows as multi-row INSERT statements.
func dumpTableData(w *bufio.Writer, table string) error {
	rows, err := utils.DB.Raw(fmt.Sprintf("SELECT * FROM %s", quotePGIdent(table))).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()

	colNames, err := rows.Columns()
	if err != nil {
		return err
	}
	colTypes, err := rows.ColumnTypes()
	if err != nil {
		return err
	}

	quoted := make([]string, len(colNames))
	for i, n := range colNames {
		quoted[i] = quotePGIdent(n)
	}
	prefix := fmt.Sprintf("INSERT INTO %s (%s) VALUES\n", quotePGIdent(table), strings.Join(quoted, ", "))

	const batchSize = 200
	batch := make([]string, 0, batchSize)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if _, err := fmt.Fprintf(w, "%s%s;\n\n", prefix, strings.Join(batch, ",\n")); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}

	vals := make([]interface{}, len(colNames))
	ptrs := make([]interface{}, len(colNames))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		lits := make([]string, len(vals))
		for i, v := range vals {
			lits[i] = pgSQLLiteral(v, colTypes[i].DatabaseTypeName())
		}
		batch = append(batch, "("+strings.Join(lits, ", ")+")")
		if len(batch) >= batchSize {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return flush()
}

// pgSQLLiteral renders a scanned driver value as a SQL literal. Non-text
// types are quoted as text anyway — PostgreSQL coerces quoted literals to the
// target column type on INSERT — with the exception of bytea which needs the
// hex \x format.
func pgSQLLiteral(v interface{}, dbType string) string {
	switch t := v.(type) {
	case nil:
		return "NULL"
	case bool:
		if t {
			return "TRUE"
		}
		return "FALSE"
	case int64, int32, int, float64, float32:
		return fmt.Sprintf("%v", t)
	case []byte:
		if strings.EqualFold(dbType, "BYTEA") {
			return `'\x` + hex.EncodeToString(t) + `'`
		}
		return pgSQLQuote(string(t))
	case string:
		return pgSQLQuote(t)
	case time.Time:
		return pgSQLQuote(t.Format("2006-01-02 15:04:05.999999999-07:00"))
	default:
		return pgSQLQuote(fmt.Sprint(t))
	}
}

func pgSQLQuote(s string) string {
	if !strings.ContainsAny(s, "'\\") {
		return "'" + s + "'"
	}
	var b strings.Builder
	b.WriteString("'")
	for _, r := range s {
		if r == '\'' {
			b.WriteString("''")
		} else {
			b.WriteRune(r)
		}
	}
	b.WriteString("'")
	return b.String()
}
