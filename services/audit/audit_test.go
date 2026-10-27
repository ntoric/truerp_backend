package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"truerp/models"
	"truerp/utils"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupTestDB(t *testing.T) *gorm.DB {
	t.Setenv("AUDIT_SPOOL_PATH", filepath.Join(t.TempDir(), "spool.jsonl"))
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE parties (
		id TEXT PRIMARY KEY, user_id TEXT, name TEXT, party_type TEXT,
		opening_balance REAL, balance REAL, password TEXT,
		created_at DATETIME, updated_at DATETIME,
		deleted_at DATETIME)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.AuditLog{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func waitForRows(t *testing.T, db *gorm.DB, table string, want int) {
	t.Helper()
	for i := 0; i < 200; i++ {
		var n int64
		db.Table(table).Count(&n)
		if n >= int64(want) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	var n int64
	db.Table(table).Count(&n)
	t.Fatalf("expected %d rows in %s, got %d", want, table, n)
}

func TestQueueWritesToMonthlyShard(t *testing.T) {
	db := setupTestDB(t)
	utils.SetDialect(utils.DialectSQLite)
	defer utils.SetDialect("")
	Start(db)
	defer Shutdown()

	Enqueue(models.AuditLog{
		UserID: uuid.New(), Action: "create", EntityType: "party",
		EntityName: "Acme", Description: "test", CreatedAt: time.Now(),
	})

	table := MonthTableName(time.Now())
	waitForRows(t, db, table, 1)

	var row models.AuditLog
	if err := db.Table(table).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.EntityName != "Acme" || row.Action != "create" {
		t.Fatalf("unexpected row: %+v", row)
	}
}

func TestHooksCaptureCRUD(t *testing.T) {
	db := setupTestDB(t)
	utils.SetDialect(utils.DialectSQLite)
	defer utils.SetDialect("")
	Start(db)
	defer Shutdown()

	unbind := BindActor(&Actor{
		UserID:    uuid.New(),
		UserName:  "tester",
		ScopeID:   uuid.New(),
		IPAddress: "10.0.0.1",
		RequestID: "req-1",
	})
	defer unbind()

	type Party struct {
		ID       string `gorm:"primaryKey"`
		UserID   string
		Name     string
		Balance  float64
		Password string
	}
	db.Table("parties")

	// Create
	p := &Party{ID: uuid.New().String(), UserID: uuid.New().String(), Name: "Acme", Balance: 10, Password: "secret-pw"}
	if err := db.Table("parties").Create(p).Error; err != nil {
		t.Fatal(err)
	}
	table := MonthTableName(time.Now())
	waitForRows(t, db, table, 1)

	// Update via map
	if err := db.Table("parties").Where("id = ?", p.ID).Update("balance", 42).Error; err != nil {
		t.Fatal(err)
	}
	waitForRows(t, db, table, 2)

	// Delete
	if err := db.Table("parties").Where("id = ?", p.ID).Delete(&Party{}).Error; err != nil {
		t.Fatal(err)
	}
	waitForRows(t, db, table, 3)

	var rows []models.AuditLog
	if err := db.Table(table).Order("created_at").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	for i, r := range rows {
		t.Logf("row %d: action=%s entity=%s prev=%dB new=%dB changes=%s", i, r.Action, r.EntityType, len(r.PreviousData), len(r.NewData), r.Changes)
	}
	if len(rows) != 3 {
		t.Fatalf("want 3 entries, got %d", len(rows))
	}
	if rows[0].Action != "create" || rows[0].NewData == "" {
		t.Fatalf("create entry missing new_data: %+v", rows[0])
	}
	if rows[0].ChangedByName != "tester" || rows[0].IPAddress != "10.0.0.1" || rows[0].RequestID != "req-1" {
		t.Fatalf("actor not propagated: %+v", rows[0])
	}
	if rows[1].Action != "update" || rows[1].PreviousData == "" || rows[1].NewData == "" {
		t.Fatalf("update entry missing snapshots: %+v", rows[1])
	}
	if rows[2].Action != "delete" || rows[2].PreviousData == "" {
		t.Fatalf("delete entry missing snapshot: %+v", rows[2])
	}
}

func TestSpoolReplay(t *testing.T) {
	spoolDir := t.TempDir()
	spoolPath := filepath.Join(spoolDir, "spool.jsonl")

	// Simulate a previous run that died with 2 uncommitted entries.
	f, err := os.Create(spoolPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"lost-1", "lost-2"} {
		e := models.AuditLog{
			ID: uuid.New(), UserID: uuid.New(), Action: "create",
			EntityType: "party", EntityName: name, CreatedAt: time.Now(),
		}
		b, _ := json.Marshal(e)
		f.Write(append(b, '\n'))
	}
	f.Close()

	db := setupTestDB(t)
	t.Setenv("AUDIT_SPOOL_PATH", spoolPath)
	utils.SetDialect(utils.DialectSQLite)
	defer utils.SetDialect("")
	Start(db)
	defer Shutdown()

	table := MonthTableName(time.Now())
	waitForRows(t, db, table, 2)

	var rows []models.AuditLog
	db.Table(table).Order("entity_name").Find(&rows)
	if len(rows) != 2 || rows[0].EntityName != "lost-1" || rows[1].EntityName != "lost-2" {
		t.Fatalf("replayed rows wrong: %+v", rows)
	}
}

func TestMonthTableName(t *testing.T) {
	got := MonthTableName(time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC))
	if got != "audit_logs_2026_10" {
		t.Fatalf("got %s", got)
	}
	if _, ok := ParseMonthTable(got); !ok {
		t.Fatal("ParseMonthTable failed")
	}
	fmt.Println("ok")
}
