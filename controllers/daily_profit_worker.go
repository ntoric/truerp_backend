package controllers

import (
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// dailyProfitSweepInterval is how often the background cron recalculates the
// materialized daily profit rows for users who enabled asynchronous updates.
const dailyProfitSweepInterval = 5 * time.Minute

// dailyProfitJobWait bounds how long HTTP handlers wait for a queued
// recompute to finish before answering with whatever is materialized.
const dailyProfitJobWait = 30 * time.Second

// dailyProfitDefaultLocation is the fallback timezone for the daily-profit
// cron when the user hasn't configured one in Developer Settings: IST
// (Asia/Kolkata, UTC+5:30 — no DST).
var dailyProfitDefaultLocation = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		return time.UTC
	}
	return loc
}()

// dailyProfitUserLocation resolves the timezone the daily-profit cron uses to
// decide what "today" is: the user's configured Developer Settings timezone,
// or Asia/Kolkata (UTC+5:30) when unset.
func dailyProfitUserLocation(userID uuid.UUID) *time.Location {
	if loc, ok := ConfiguredLocationForUser(userID); ok {
		return loc
	}
	return dailyProfitDefaultLocation
}

// dailyProfitToday is the current calendar date in the user's cron timezone.
func dailyProfitToday(userID uuid.UUID) string {
	return time.Now().In(dailyProfitUserLocation(userID)).Format("2006-01-02")
}

// dailyProfitTicket tracks one queued recompute so concurrent requests for the
// same (user, range) coalesce onto a single worker job.
type dailyProfitTicket struct {
	done chan struct{}
	err  error
}

type dailyProfitJob struct {
	userID uuid.UUID
	start  string
	end    string
	ticket *dailyProfitTicket
}

var (
	dailyProfitJobCh = make(chan dailyProfitJob, 128)
	dailyProfitMu    sync.Mutex
	dailyProfitPend  = make(map[string]*dailyProfitTicket)
)

// dailyProfitAsyncEnabled reports whether the user turned on asynchronous
// daily-profit updates in Developer Settings.
func dailyProfitAsyncEnabled(userID uuid.UUID) bool {
	if !utils.DB.Migrator().HasTable(&models.DeveloperSettings{}) {
		return false
	}
	var enabled bool
	utils.DB.Model(&models.DeveloperSettings{}).
		Where("user_id = ?", userID).
		Select("async_daily_profit").
		Limit(1).
		Scan(&enabled)
	return enabled
}

// scheduleDailyProfitRecompute queues a priority recompute of [start, end] for
// the worker. Identical pending requests share one job. If the queue is full
// the recompute still runs, spawned directly, so callers never get stuck.
func scheduleDailyProfitRecompute(userID uuid.UUID, start, end string) *dailyProfitTicket {
	key := userID.String() + "|" + start + "|" + end

	dailyProfitMu.Lock()
	if t, ok := dailyProfitPend[key]; ok {
		dailyProfitMu.Unlock()
		return t
	}
	t := &dailyProfitTicket{done: make(chan struct{})}
	dailyProfitPend[key] = t
	dailyProfitMu.Unlock()

	job := dailyProfitJob{userID: userID, start: start, end: end, ticket: t}
	select {
	case dailyProfitJobCh <- job:
	default:
		go func() { finishDailyProfitJob(job) }()
	}
	return t
}

// finishDailyProfitJob runs the recompute transaction and wakes every waiter
// on the shared ticket. Cleanup lives in the defer so waiters are released
// and the pending entry is cleared even when the recompute panics.
func finishDailyProfitJob(job dailyProfitJob) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("daily profit worker: PANIC recomputing %s %s..%s: %v",
				job.userID, job.start, job.end, r)
			job.ticket.err = errDailyProfitPanic
		}
		dailyProfitMu.Lock()
		delete(dailyProfitPend, job.userID.String()+"|"+job.start+"|"+job.end)
		dailyProfitMu.Unlock()
		close(job.ticket.done)
	}()
	job.ticket.err = recomputeDailyProfitRange(job.userID, job.start, job.end)
}

var errDailyProfitPanic = errDailyProfit("recompute panicked")

type errDailyProfit string

func (e errDailyProfit) Error() string { return string(e) }

// waitDailyProfitTicket blocks until the job finishes or the wait budget
// expires; it returns the job error (nil on success or timeout expiry).
func waitDailyProfitTicket(t *dailyProfitTicket, budget time.Duration) error {
	select {
	case <-t.done:
		return t.err
	case <-time.After(budget):
		return nil
	}
}

// recomputeDailyProfitRange recalculates every calendar day in [start, end]
// and upserts the materialized rows — the calculation and the write run inside
// one DB transaction so a day is never half-updated.
func recomputeDailyProfitRange(userID uuid.UUID, start, end string) error {
	if _, err := time.Parse("2006-01-02", start); err != nil {
		return err
	}
	if _, err := time.Parse("2006-01-02", end); err != nil {
		return err
	}
	// Snapshot rebuild manages its own mutex/table and runs before the tx.
	ensureStockSnapshots(userID)
	return utils.DB.Transaction(func(tx *gorm.DB) error {
		rows := computeDailyProfitRows(tx, userID, start, end)
		return upsertDailyProfitEntries(tx, userID, rows)
	})
}

// upsertDailyProfitEntries writes one row per day, keyed by (user_id, date).
func upsertDailyProfitEntries(tx *gorm.DB, userID uuid.UUID, rows []models.DailyProfitRow) error {
	if len(rows) == 0 {
		return nil
	}
	entries := make([]models.DailyProfitEntry, 0, len(rows))
	for _, r := range rows {
		entries = append(entries, models.DailyProfitEntry{
			UserID:         userID,
			Date:           r.Date,
			OpeningStock:   r.OpeningStock,
			Sales:          r.Sales,
			COGS:           r.COGS,
			SalesReturn:    r.SalesReturn,
			SalesProfit:    r.SalesProfit,
			Purchase:       r.Purchase,
			PurchaseReturn: r.PurchaseReturn,
			ClosingStock:   r.ClosingStock,
			GrossProfit:    r.GrossProfit,
			Expenses:       r.Expenses,
			NetProfit:      r.NetProfit,
		})
	}
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}, {Name: "date"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"opening_stock", "sales", "cogs", "sales_return", "sales_profit",
			"purchase", "purchase_return", "closing_stock", "gross_profit",
			"expenses", "net_profit", "updated_at",
		}),
	}).Create(&entries).Error
}

// StartDailyProfitScheduler launches the background cron. Priority jobs
// (manual refresh, gap fills requested by the report endpoint) are always
// drained before the periodic sweep runs.
func StartDailyProfitScheduler() {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("daily profit scheduler: PANIC recovered: %v", r)
			}
		}()

		ticker := time.NewTicker(dailyProfitSweepInterval)
		defer ticker.Stop()

		for {
			// Drain any queued priority jobs before considering the sweep.
			select {
			case job := <-dailyProfitJobCh:
				finishDailyProfitJob(job)
				continue
			default:
			}
			select {
			case job := <-dailyProfitJobCh:
				finishDailyProfitJob(job)
			case <-ticker.C:
				func() {
					defer func() {
						if r := recover(); r != nil {
							log.Printf("daily profit scheduler: PANIC in sweep: %v", r)
						}
					}()
					sweepDailyProfitEntries()
				}()
			}
		}
	}()
}

// sweepDailyProfitEntries recalculates materialized rows for every user who
// enabled async mode. Each user's tracked window starts at their earliest
// entry and runs through today, so newly viewed ranges join the sweep once
// their first entries exist; users with no entries yet get today's row seeded.
func sweepDailyProfitEntries() {
	if !utils.DB.Migrator().HasTable(&models.DailyProfitEntry{}) ||
		!utils.DB.Migrator().HasTable(&models.DeveloperSettings{}) {
		return
	}
	var userIDs []uuid.UUID
	utils.DB.Model(&models.DeveloperSettings{}).
		Where("async_daily_profit = ?", true).
		Pluck("user_id", &userIDs)

	for _, userID := range userIDs {
		today := dailyProfitToday(userID)
		start := today
		var first models.DailyProfitEntry
		if err := utils.DB.Where("user_id = ?", userID).
			Select("date").Order("date ASC").Limit(1).
			First(&first).Error; err == nil && first.Date != "" {
			start = first.Date
		}
		if err := recomputeDailyProfitRange(userID, start, today); err != nil {
			log.Printf("daily profit scheduler: recompute failed for user %s: %v", userID, err)
		}
	}
}

// dailyProfitRowFromEntry converts a materialized row to the API shape.
func dailyProfitRowFromEntry(e models.DailyProfitEntry) models.DailyProfitRow {
	return models.DailyProfitRow{
		Date:           e.Date,
		OpeningStock:   e.OpeningStock,
		Sales:          e.Sales,
		COGS:           e.COGS,
		SalesReturn:    e.SalesReturn,
		SalesProfit:    e.SalesProfit,
		Purchase:       e.Purchase,
		PurchaseReturn: e.PurchaseReturn,
		ClosingStock:   e.ClosingStock,
		GrossProfit:    e.GrossProfit,
		Expenses:       e.Expenses,
		NetProfit:      e.NetProfit,
	}
}

// loadDailyProfitEntryPage reads a paginated slice of the materialized table
// plus the range-wide totals (OpeningStock = earliest day's opening,
// ClosingStock = latest day's closing).
func loadDailyProfitEntryPage(userID uuid.UUID, start, end, sortKey string, page, perPage int) ([]models.DailyProfitRow, models.DailyProfitRow, int64) {
	scope := func() *gorm.DB {
		return utils.DB.Model(&models.DailyProfitEntry{}).
			Where("user_id = ? AND date >= ? AND date <= ?", userID, start, end)
	}

	var total int64
	scope().Count(&total)

	order := "date DESC"
	if sortKey == "asc" {
		order = "date ASC"
	}
	var entries []models.DailyProfitEntry
	scope().Order(order).Offset((page - 1) * perPage).Limit(perPage).Find(&entries)

	rows := make([]models.DailyProfitRow, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, dailyProfitRowFromEntry(e))
	}

	var sums models.DailyProfitEntry
	scope().Select(
		"COALESCE(SUM(sales),0) AS sales, COALESCE(SUM(cogs),0) AS cogs, " +
			"COALESCE(SUM(sales_return),0) AS sales_return, COALESCE(SUM(sales_profit),0) AS sales_profit, " +
			"COALESCE(SUM(purchase),0) AS purchase, COALESCE(SUM(purchase_return),0) AS purchase_return, " +
			"COALESCE(SUM(gross_profit),0) AS gross_profit, COALESCE(SUM(expenses),0) AS expenses, " +
			"COALESCE(SUM(net_profit),0) AS net_profit").
		Scan(&sums)
	totals := dailyProfitRowFromEntry(sums)

	var edge models.DailyProfitEntry
	if err := scope().Order("date ASC").Select("opening_stock").Limit(1).First(&edge).Error; err == nil {
		totals.OpeningStock = edge.OpeningStock
	}
	if err := scope().Order("date DESC").Select("closing_stock").Limit(1).First(&edge).Error; err == nil {
		totals.ClosingStock = edge.ClosingStock
	}

	return rows, totals, total
}

// loadAllDailyProfitEntries reads every materialized row in the range (for
// exports), ordered by the requested sort.
func loadAllDailyProfitEntries(userID uuid.UUID, start, end, sortKey string) []models.DailyProfitRow {
	order := "date DESC"
	if sortKey == "asc" {
		order = "date ASC"
	}
	var entries []models.DailyProfitEntry
	utils.DB.Where("user_id = ? AND date >= ? AND date <= ?", userID, start, end).
		Order(order).Find(&entries)
	rows := make([]models.DailyProfitRow, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, dailyProfitRowFromEntry(e))
	}
	return rows
}

// RefreshDailyProfitReport schedules a priority recompute of one day (or an
// explicit start/end range) and waits for it, returning the fresh rows.
// POST /api/v1/dashboard/daily-profit-report/refresh
func RefreshDailyProfitReport(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		Date      string `json:"date"`
		StartDate string `json:"start_date"`
		EndDate   string `json:"end_date"`
	}
	_ = c.ShouldBindJSON(&input)

	start, end := input.StartDate, input.EndDate
	if start == "" {
		start = input.Date
	}
	if end == "" {
		end = input.Date
	}
	if start == "" && end == "" {
		start = dailyProfitToday(userID)
		end = start
	} else if start == "" {
		start = end
	} else if end == "" {
		end = start
	}
	if _, err := time.Parse("2006-01-02", start); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid start date"})
		return
	}
	if _, err := time.Parse("2006-01-02", end); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid end date"})
		return
	}
	if strings.Compare(start, end) > 0 {
		start, end = end, start
	}

	ticket := scheduleDailyProfitRecompute(userID, start, end)
	if err := waitDailyProfitTicket(ticket, dailyProfitJobWait); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Daily profit refresh failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Daily profit recalculated",
		"rows":    loadAllDailyProfitEntries(userID, start, end, "asc"),
	})
}
