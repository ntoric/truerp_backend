package audit

import (
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"truerp/utils"

	"gorm.io/gorm"
)

// auditLogBaseTable is the legacy single audit table; monthly shards are
// named <base>_YYYY_MM and inherit its schema.
const auditLogBaseTable = "audit_logs"

var monthTablePattern = regexp.MustCompile(`^audit_logs_\d{4}_\d{2}$`)

// MonthTableName returns the shard table name for the month containing t.
func MonthTableName(t time.Time) string {
	return fmt.Sprintf("%s_%04d_%02d", auditLogBaseTable, t.Year(), int(t.Month()))
}

var ensuredTables sync.Map // table name -> struct{}

// EnsureMonthTable creates the audit shard for t's month if it does not exist.
// Postgres shards are cloned with LIKE ... INCLUDING ALL so they inherit the
// base table's columns, defaults and indexes; SQLite shards copy the base
// table's DDL and add equivalent indexes.
func EnsureMonthTable(db *gorm.DB, t time.Time) error {
	name := MonthTableName(t)
	if _, ok := ensuredTables.Load(name); ok {
		return nil
	}
	if err := createMonthTable(db, name); err != nil {
		return err
	}
	ensuredTables.Store(name, struct{}{})
	return nil
}

func createMonthTable(db *gorm.DB, name string) error {
	if utils.IsPostgres() {
		stmt := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (LIKE %s INCLUDING ALL)`, name, auditLogBaseTable)
		return db.Exec(stmt).Error
	}

	// SQLite: reuse the base table's DDL so columns/types/defaults stay in sync.
	var ddl string
	if err := db.Raw(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, auditLogBaseTable).
		Scan(&ddl).Error; err != nil {
		return err
	}
	if ddl == "" {
		return fmt.Errorf("base table %s not found", auditLogBaseTable)
	}
	ddl = strings.Replace(ddl, "CREATE TABLE", "CREATE TABLE IF NOT EXISTS", 1)
	ddl = strings.Replace(ddl, auditLogBaseTable, name, 1)
	if err := db.Exec(ddl).Error; err != nil {
		return err
	}
	for _, col := range []string{"user_id", "entity_type", "entity_id", "action", "created_at", "changed_by_user_id", "request_id"} {
		db.Exec(fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_%s_%s ON %s(%s)`, name, col, name, col))
	}
	return nil
}

// auditTableCache caches the list of existing audit shard tables.
var (
	auditTableCacheMu sync.RWMutex
	auditTableCache   []string
	auditTableCached  time.Time
)

// AuditTables returns all audit log tables (legacy base + monthly shards),
// oldest first. The result is cached briefly since it hits the catalog.
func AuditTables(db *gorm.DB) []string {
	auditTableCacheMu.RLock()
	if time.Since(auditTableCached) < 30*time.Second && auditTableCache != nil {
		defer auditTableCacheMu.RUnlock()
		return auditTableCache
	}
	auditTableCacheMu.RUnlock()

	tables := listAuditTables(db)

	auditTableCacheMu.Lock()
	auditTableCache = tables
	auditTableCached = time.Now()
	auditTableCacheMu.Unlock()
	return tables
}

func listAuditTables(db *gorm.DB) []string {
	var names []string
	if utils.IsPostgres() {
		db.Raw(`SELECT table_name FROM information_schema.tables
			WHERE table_schema = CURRENT_SCHEMA()
			  AND (table_name = ? OR table_name ~ '^audit_logs_[0-9]{4}_[0-9]{2}$')
			ORDER BY table_name`, auditLogBaseTable).Scan(&names)
	} else {
		db.Raw(`SELECT name FROM sqlite_master
			WHERE type = 'table'
			  AND (name = ? OR name GLOB 'audit_logs_[0-9][0-9][0-9][0-9]_[0-9][0-9]')
			ORDER BY name`, auditLogBaseTable).Scan(&names)
	}
	// Ensure the base table is always included even on a fresh database.
	found := false
	for _, n := range names {
		if n == auditLogBaseTable {
			found = true
			break
		}
	}
	if !found {
		names = append([]string{auditLogBaseTable}, names...)
	}
	return names
}

// ParseMonthTable returns the month start encoded in a shard table name.
func ParseMonthTable(name string) (time.Time, bool) {
	if !monthTablePattern.MatchString(name) {
		return time.Time{}, false
	}
	y, err1 := strconv.Atoi(name[len(auditLogBaseTable)+1 : len(auditLogBaseTable)+5])
	m, err2 := strconv.Atoi(name[len(auditLogBaseTable)+6:])
	if err1 != nil || err2 != nil || m < 1 || m > 12 {
		return time.Time{}, false
	}
	return time.Date(y, time.Month(m), 1, 0, 0, 0, 0, time.UTC), true
}

// InvalidateTableCache forces the next AuditTables call to re-read the catalog.
func InvalidateTableCache() {
	auditTableCacheMu.Lock()
	auditTableCache = nil
	auditTableCacheMu.Unlock()
}

// MonthTablesInRange filters shard tables overlapping [from,to] (inclusive,
// nil bound = open). The base table is always included.
func MonthTablesInRange(tables []string, from, to *time.Time) []string {
	if from == nil && to == nil {
		return tables
	}
	out := make([]string, 0, len(tables))
	for _, t := range tables {
		monthStart, isShard := ParseMonthTable(t)
		if !isShard {
			out = append(out, t)
			continue
		}
		monthEnd := monthStart.AddDate(0, 1, 0)
		if from != nil && monthEnd.Before(*from) {
			continue
		}
		if to != nil && monthStart.After(*to) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// auditSelectColumns is the explicit column list shared by every audit table.
const auditSelectColumns = `id, user_id, user_name, changed_by_user_id, changed_by_name, action, entity_type, entity_id, entity_name, description, ip_address, user_agent, request_id, previous_data, new_data, changes, status, error_message, created_at`

// UnionSelect returns a UNION ALL subquery over the given audit tables.
func UnionSelect(tables []string) string {
	parts := make([]string, 0, len(tables))
	for _, t := range tables {
		parts = append(parts, fmt.Sprintf("SELECT %s FROM %s", auditSelectColumns, t))
	}
	return strings.Join(parts, " UNION ALL ")
}

// StartMonthScheduler pre-creates the current and next month's shard every
// hour so inserts never stall on DDL at month rollover.
func StartMonthScheduler(db *gorm.DB, stop <-chan struct{}) {
	ensure := func() {
		now := time.Now()
		for _, t := range []time.Time{now, now.AddDate(0, 1, 0)} {
			if err := EnsureMonthTable(db, t); err != nil {
				log.Printf("audit: ensure month table %s failed: %v", MonthTableName(t), err)
			}
		}
	}
	ensure()
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ensure()
			case <-stop:
				return
			}
		}
	}()
}
