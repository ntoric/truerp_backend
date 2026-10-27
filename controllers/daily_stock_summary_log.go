package controllers

import (
	"log"
	"sync"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/google/uuid"
	"gorm.io/gorm/clause"
)

// dailyStockSummaryRunTime is the fixed IST wall-clock time the daily stock
// summary cron fires at. It is deliberately not user-configurable.
const dailyStockSummaryRunTime = "01:00"

// dailyStockSummaryMu serializes summary passes so a scheduled run can never
// overlap with itself across ticks.
var dailyStockSummaryMu sync.Mutex

// StartDailyStockSummaryLogScheduler launches the daily stock summary cron.
// Every day at 01:00 IST it records the current stock quantity and value as
// the previous day's closing stock and the current day's opening stock in
// daily_stock_summary_log. An immediate check runs on startup so a run missed
// while the server was down is still honored (catch-up).
func StartDailyStockSummaryLogScheduler() {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("daily stock summary scheduler: PANIC recovered: %v", r)
			}
		}()

		loc := dbMaintenanceLocation()
		log.Printf("daily stock summary scheduler: started (run time: %s %s, IST now: %s)",
			dailyStockSummaryRunTime, loc.String(),
			time.Now().In(loc).Format("2006-01-02 15:04:05"))

		processDueDailyStockSummaryLogs()

		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("daily stock summary scheduler: PANIC in tick: %v", r)
					}
				}()
				processDueDailyStockSummaryLogs()
			}()
		}
	}()
}

// processDueDailyStockSummaryLogs runs the summary write once the 01:00 IST
// run time has been reached, for every user with stock data that does not yet
// have a row for today. The today's-row check makes the job idempotent — it
// runs once per day no matter how often the tick fires.
func processDueDailyStockSummaryLogs() {
	if !utils.DB.Migrator().HasTable(&models.DailyStockSummaryLog{}) ||
		!utils.DB.Migrator().HasTable(&models.StockDailySnapshot{}) {
		return
	}

	loc := dbMaintenanceLocation()
	now := time.Now().In(loc)
	if now.Format("15:04") < dailyStockSummaryRunTime {
		return
	}
	today := now.Format("2006-01-02")

	if !dailyStockSummaryMu.TryLock() {
		return // a pass is already in progress
	}
	defer dailyStockSummaryMu.Unlock()

	for _, userID := range dailyStockSummaryUsers() {
		var logged int64
		utils.DB.Model(&models.DailyStockSummaryLog{}).
			Where("user_id = ?", userID).
			Where(utils.SQLDateEquals("log_date"), today).
			Count(&logged)
		if logged > 0 {
			continue
		}
		if err := writeDailyStockSummaryLogs(userID, today); err != nil {
			log.Printf("daily stock summary scheduler: write failed for user %s: %v", userID, err)
		}
	}
}

// dailyStockSummaryUsers returns every user that owns stock data — the same
// tenant set the snapshot scheduler maintains — plus anyone who already has
// summary log rows.
func dailyStockSummaryUsers() []uuid.UUID {
	var userIDs []uuid.UUID
	if utils.DB.Migrator().HasTable(&models.InventoryStock{}) {
		utils.DB.Model(&models.InventoryStock{}).Distinct().Pluck("user_id", &userIDs)
	}
	var entryUsers []uuid.UUID
	utils.DB.Model(&models.StockEntry{}).Distinct().Pluck("user_id", &entryUsers)
	var metaUsers []uuid.UUID
	if utils.DB.Migrator().HasTable(&models.StockSnapshotMeta{}) {
		utils.DB.Model(&models.StockSnapshotMeta{}).Pluck("user_id", &metaUsers)
	}
	var loggedUsers []uuid.UUID
	utils.DB.Model(&models.DailyStockSummaryLog{}).Distinct().Pluck("user_id", &loggedUsers)

	seen := make(map[uuid.UUID]bool, len(userIDs)+len(entryUsers)+len(metaUsers)+len(loggedUsers))
	out := make([]uuid.UUID, 0, len(userIDs))
	for _, uid := range append(append(append(userIDs, entryUsers...), metaUsers...), loggedUsers...) {
		if seen[uid] {
			continue
		}
		seen[uid] = true
		out = append(out, uid)
	}
	return out
}

// writeDailyStockSummaryLogs upserts one summary row per day for the user:
// every day from the first unlogged day through yesterday gets both opening
// and closing positions plus that day's sales, COGS, purchase, and expense
// totals; today's row gets its opening from yesterday's closing and its
// closing from the current live position. Positions come from the
// materialized snapshots (reconciled against live inventory and adjusted by
// opening-stock overrides), so the log matches the stock and P&L reports.
func writeDailyStockSummaryLogs(userID uuid.UUID, today string) error {
	todayT, err := time.Parse("2006-01-02", today)
	if err != nil {
		return err
	}
	yesterdayT := todayT.AddDate(0, 0, -1)

	// Entries are store-specific: operational data is scoped to the store's
	// owner user, so resolve the store via stores.owner_user_id.
	var storeID *uuid.UUID
	var store models.Store
	if err := utils.DB.Select("id").Where("owner_user_id = ?", userID).
		Limit(1).First(&store).Error; err == nil {
		storeID = &store.ID
	}

	// Roll snapshot coverage forward so today's position is materialized.
	refreshStockSnapshots(userID, false)

	// The range that still needs rows starts the day after the latest logged
	// date — but never after yesterday, so the previous day's closing is
	// always refreshed with the current position. On the first run it starts
	// at the earliest snapshot day, seeding the log with the full history.
	start := yesterdayT
	var last models.DailyStockSummaryLog
	if err := utils.DB.Where("user_id = ?", userID).
		Order("log_date DESC").Limit(1).First(&last).Error; err == nil {
		start = dayStart(last.LogDate).AddDate(0, 0, 1)
		if start.After(yesterdayT) {
			start = yesterdayT
		}
	} else {
		var first models.StockDailySnapshot
		if err := utils.DB.Where("user_id = ?", userID).
			Order("snapshot_date ASC").Limit(1).First(&first).Error; err == nil &&
			dayStart(first.SnapshotDate).Before(start) {
			start = dayStart(first.SnapshotDate)
		}
	}
	if start.After(todayT) {
		return nil
	}

	startStr := start.Format("2006-01-02")
	positions := loadDailyStockPositions(utils.DB, userID, startStr, today)
	// Day aggregates (sales, COGS, purchases, expenses) come from the same
	// tax-exclusive queries the daily profit report uses.
	aggs := loadDailyProfitAggregates(utils.DB, userID, startStr, today)

	rows := make([]models.DailyStockSummaryLog, 0, int(todayT.Sub(start).Hours()/24)+1)
	for d := start; !d.After(todayT); d = d.AddDate(0, 0, 1) {
		day := d.Format("2006-01-02")
		pos := positions[day]
		a := aggs[day]
		if a == nil {
			a = &dailyProfitAgg{}
		}
		rows = append(rows, models.DailyStockSummaryLog{
			ID:           uuid.New(),
			UserID:       userID,
			StoreID:      storeID,
			LogDate:      d,
			OpeningQty:   pos.OpeningQty,
			OpeningValue: pos.Opening,
			ClosingQty:   pos.ClosingQty,
			ClosingValue: pos.Closing,
			Sales:        a.Sales,
			COGS:         a.COGS,
			Purchase:     a.Purchase,
			Expenses:     a.Expenses,
		})
	}
	return utils.DB.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}, {Name: "log_date"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"store_id", "opening_qty", "opening_value", "closing_qty", "closing_value",
			"sales", "cogs", "purchase", "expenses", "updated_at",
		}),
	}).Create(&rows).Error
}
