package controllers

import (
	"fmt"
	"log"
	"math"
	"net/http"
	"sync"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// snapshotExcludedEntryTypes are ledger types that only move reserved_qty on
// inventory_stocks — they never change physical on-hand quantity, so they must
// not count toward stock positions or report movements.
var snapshotExcludedEntryTypes = []string{"reservation", "release"}

// snapshotCostedEntryTypes are inflow types whose cost_price is a real unit
// cost and may re-rate the running weighted average. 'sale' and 'return'
// entries store the selling price, so they are intentionally excluded.
var snapshotCostedEntryTypes = map[string]bool{
	"purchase":   true,
	"opening":    true,
	"adjustment": true,
}

var snapshotRebuildMu sync.Mutex

// dayStart truncates a timestamp to local calendar midnight.
func dayStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

type snapshotKey struct {
	ProductID uuid.UUID
	OutletID  uuid.UUID
}

type snapshotDayEvent struct {
	Net       float64
	In        float64
	Out       float64
	CostedAmt float64
	CostedQty float64
}

// stockSourceFingerprint summarizes the stock source tables. Reports compare
// it against the fingerprint stored at the last rebuild so snapshots are only
// rebuilt when entries or inventory stocks actually changed.
func stockSourceFingerprint(userID uuid.UUID) string {
	var entryCount, stockCount int64
	utils.DB.Unscoped().Model(&models.StockEntry{}).Where("user_id = ?", userID).Count(&entryCount)

	var entryUpdated, entryDeleted, stockUpdated []time.Time
	utils.DB.Unscoped().Model(&models.StockEntry{}).Where("user_id = ?", userID).
		Order("updated_at DESC").Limit(1).Pluck("updated_at", &entryUpdated)
	utils.DB.Unscoped().Model(&models.StockEntry{}).
		Where("user_id = ? AND deleted_at IS NOT NULL", userID).
		Order("deleted_at DESC").Limit(1).Pluck("deleted_at", &entryDeleted)

	if utils.DB.Migrator().HasTable(&models.InventoryStock{}) {
		utils.DB.Model(&models.InventoryStock{}).Where("user_id = ?", userID).Count(&stockCount)
		utils.DB.Model(&models.InventoryStock{}).Where("user_id = ?", userID).
			Order("updated_at DESC").Limit(1).Pluck("updated_at", &stockUpdated)
	}

	first := func(ts []time.Time) int64 {
		if len(ts) == 0 {
			return 0
		}
		return ts[0].UnixNano()
	}
	return fmt.Sprintf("%d|%d|%d|%d|%d",
		entryCount, first(entryUpdated), first(entryDeleted), stockCount, first(stockUpdated))
}

// ensureStockSnapshots rebuilds the daily snapshot table when the stock source
// data has changed since the last rebuild, or when the snapshots don't cover
// today yet (a new day rolls the coverage forward).
func ensureStockSnapshots(userID uuid.UUID) {
	refreshStockSnapshots(userID, false)
}

// refreshStockSnapshots rebuilds the user's daily snapshots when stale; pass
// force=true to recalculate unconditionally (manual "refresh closing stock").
func refreshStockSnapshots(userID uuid.UUID, force bool) {
	if !utils.DB.Migrator().HasTable(&models.StockDailySnapshot{}) ||
		!utils.DB.Migrator().HasTable(&models.StockSnapshotMeta{}) {
		return
	}
	snapshotRebuildMu.Lock()
	defer snapshotRebuildMu.Unlock()

	fingerprint := stockSourceFingerprint(userID)
	if !force {
		var meta models.StockSnapshotMeta
		if err := utils.DB.Where("user_id = ?", userID).First(&meta).Error; err == nil &&
			meta.Fingerprint == fingerprint && !meta.ThroughDate.Before(dayStart(time.Now())) {
			return
		}
	}
	if err := rebuildStockSnapshots(userID, fingerprint); err != nil {
		fmt.Printf("[DEBUG] rebuildStockSnapshots failed for user %s: %v\n", userID, err)
	}
}

// rebuildStockSnapshots recomputes every daily opening/closing snapshot for a
// user from scratch:
//
//   - Physical movements come from approved stock_entries (reservation/release
//     excluded — they only move reserved_qty).
//   - The part of each product+outlet's live inventory_stocks quantity that the
//     ledger cannot explain (e.g. stock imported via the stock-summary
//     migration, which writes inventory_stocks without ledger entries) is
//     injected as a baseline inflow on the day the stock row was first created.
//     This anchors historical positions to the current on-hand truth.
//   - Value is tracked with a running weighted average cost, re-rated by
//     costed inflows (purchase/opening/adjustment), falling back to the
//     product's purchase price when no cost information exists.
func rebuildStockSnapshots(userID uuid.UUID, fingerprint string) error {
	type stockAggregate struct {
		Qty     float64
		Value   float64
		FirstAt time.Time
	}
	stockAgg := make(map[snapshotKey]*stockAggregate)
	if utils.DB.Migrator().HasTable(&models.InventoryStock{}) {
		var stocks []models.InventoryStock
		if err := utils.DB.Where("user_id = ?", userID).
			Select("product_id", "outlet_id", "quantity", "average_cost", "created_at").
			Find(&stocks).Error; err != nil {
			return err
		}
		for _, s := range stocks {
			k := snapshotKey{ProductID: s.ProductID, OutletID: s.OutletID}
			agg := stockAgg[k]
			if agg == nil {
				agg = &stockAggregate{FirstAt: s.CreatedAt}
				stockAgg[k] = agg
			}
			agg.Qty += s.Quantity
			agg.Value += s.Quantity * s.AverageCost
			if s.CreatedAt.Before(agg.FirstAt) {
				agg.FirstAt = s.CreatedAt
			}
		}
	}

	var entries []models.StockEntry
	if err := utils.DB.Where("user_id = ? AND product_id IS NOT NULL", userID).
		Where("entry_type NOT IN ?", snapshotExcludedEntryTypes).
		Where("approval_status = 'approved' OR approval_status = '' OR approval_status IS NULL").
		Select("product_id", "outlet_id", "entry_type", "quantity", "cost_price", "entry_date").
		Find(&entries).Error; err != nil {
		return err
	}

	timelines := make(map[snapshotKey]map[string]*snapshotDayEvent)
	ledgerNet := make(map[snapshotKey]float64)
	eventAt := func(k snapshotKey, day string) *snapshotDayEvent {
		days := timelines[k]
		if days == nil {
			days = make(map[string]*snapshotDayEvent)
			timelines[k] = days
		}
		ev := days[day]
		if ev == nil {
			ev = &snapshotDayEvent{}
			days[day] = ev
		}
		return ev
	}

	for _, e := range entries {
		if e.ProductID == nil {
			continue
		}
		k := snapshotKey{ProductID: *e.ProductID, OutletID: e.OutletID}
		ev := eventAt(k, e.EntryDate.Format("2006-01-02"))
		ev.Net += e.Quantity
		if e.Quantity > 0 {
			ev.In += e.Quantity
		} else {
			ev.Out -= e.Quantity
		}
		if e.Quantity > 0 && e.CostPrice > 0 && snapshotCostedEntryTypes[e.EntryType] {
			ev.CostedAmt += e.Quantity * e.CostPrice
			ev.CostedQty += e.Quantity
		}
		ledgerNet[k] += e.Quantity
	}

	// Baseline: live stock not explained by the ledger (migrated stock, direct
	// writes). Anchored at the stock row's creation date so positions before
	// that day stay honest and positions after it match reality.
	for k, agg := range stockAgg {
		base := agg.Qty - ledgerNet[k]
		if math.Abs(base) < 1e-9 {
			continue
		}
		ev := eventAt(k, agg.FirstAt.Format("2006-01-02"))
		ev.Net += base
		if base > 0 {
			ev.In += base
			if agg.Qty > 0 {
				if cost := agg.Value / agg.Qty; cost > 0 {
					ev.CostedAmt += base * cost
					ev.CostedQty += base
				}
			}
		} else {
			ev.Out -= base
		}
	}

	var products []models.Product
	utils.DB.Where("user_id = ?", userID).Select("id", "purchase_price").Find(&products)
	purchasePrice := make(map[uuid.UUID]float64, len(products))
	for _, p := range products {
		purchasePrice[p.ID] = p.PurchasePrice
	}

	now := time.Now()
	today := dayStart(now)
	through := today
	snaps := make([]models.StockDailySnapshot, 0, len(timelines))
	for k, days := range timelines {
		firstDayStr, lastDayStr := "", ""
		for d := range days {
			if firstDayStr == "" || d < firstDayStr {
				firstDayStr = d
			}
			if d > lastDayStr {
				lastDayStr = d
			}
		}
		if firstDayStr == "" {
			continue
		}

		firstDay, _ := time.Parse("2006-01-02", firstDayStr)
		lastDay, _ := time.Parse("2006-01-02", lastDayStr)
		keyThrough := today
		if lastDay.After(keyThrough) {
			keyThrough = lastDay
		}
		if keyThrough.After(through) {
			through = keyThrough
		}

		// Emit one row per calendar day so the table always has an explicit
		// closing record for every date since the product's first activity.
		var qty, avg float64
		for d := firstDay; !d.After(keyThrough); d = d.AddDate(0, 0, 1) {
			dateStr := d.Format("2006-01-02")
			ev := days[dateStr]

			openQty := qty
			openValue := qty * avg

			// Re-rate the running average when costed inflow arrives.
			if ev != nil && ev.CostedQty > 0 {
				if qty > 0 && avg > 0 {
					avg = (avg*qty + ev.CostedAmt) / (qty + ev.CostedQty)
				} else {
					avg = ev.CostedAmt / ev.CostedQty
				}
			}
			in, out := 0.0, 0.0
			if ev != nil {
				qty += ev.Net
				in, out = ev.In, ev.Out
			}
			if qty != 0 && avg <= 0 {
				avg = purchasePrice[k.ProductID]
			}
			// Dead days after the position zeroed out add no information.
			if ev == nil && qty == 0 {
				continue
			}

			snaps = append(snaps, models.StockDailySnapshot{
				ID:           uuid.New(),
				UserID:       userID,
				ProductID:    k.ProductID,
				OutletID:     k.OutletID,
				SnapshotDate: d,
				OpeningQty:   openQty,
				OpeningValue: openValue,
				InQty:        in,
				OutQty:       out,
				ClosingQty:   qty,
				ClosingValue: qty * avg,
				AvgCost:      avg,
				CreatedAt:    now,
				UpdatedAt:    now,
			})
		}
	}

	return utils.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", userID).Delete(&models.StockDailySnapshot{}).Error; err != nil {
			return err
		}
		if len(snaps) > 0 {
			if err := tx.CreateInBatches(snaps, 200).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("user_id = ?", userID).Delete(&models.StockSnapshotMeta{}).Error; err != nil {
			return err
		}
		return tx.Create(&models.StockSnapshotMeta{
			UserID:      userID,
			Fingerprint: fingerprint,
			ThroughDate: through,
			RebuiltAt:   now,
		}).Error
	})
}

// RefreshStockClosing recalculates the current closing stock and materializes
// a snapshot row under today's date (updating it if it already exists).
// POST /api/v1/inventory/snapshots/refresh-closing
func RefreshStockClosing(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	if !utils.DB.Migrator().HasTable(&models.StockDailySnapshot{}) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Stock snapshots not available"})
		return
	}
	refreshStockSnapshots(userID, true)

	today := dayStart(time.Now()).Format("2006-01-02")
	var rowCount int64
	utils.DB.Model(&models.StockDailySnapshot{}).
		Where("user_id = ?", userID).
		Where(utils.SQLDateEquals("snapshot_date"), today).
		Count(&rowCount)

	qty, value := overallPositionAsOf(userID, today)
	c.JSON(http.StatusOK, gin.H{
		"date":          today,
		"closing_qty":   qty,
		"closing_value": value,
		"rows":          rowCount,
	})
}

// ---------------------------------------------------------------------------
// Daily closing scheduler
// ---------------------------------------------------------------------------

// StartStockSnapshotScheduler launches a background goroutine that keeps every
// user's daily closing snapshots rolled forward. An immediate pass runs on
// startup so days missed while the server was down are still recorded.
func StartStockSnapshotScheduler() {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("stock snapshot scheduler: PANIC recovered: %v", r)
			}
		}()

		processDueStockSnapshots()

		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()

		for range ticker.C {
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("stock snapshot scheduler: PANIC in tick: %v", r)
					}
				}()
				processDueStockSnapshots()
			}()
		}
	}()
}

// processDueStockSnapshots refreshes snapshots for every user that has stock
// data — refreshStockSnapshots skips users whose snapshots already cover today
// with a matching source fingerprint.
func processDueStockSnapshots() {
	if !utils.DB.Migrator().HasTable(&models.StockDailySnapshot{}) {
		return
	}
	var userIDs []uuid.UUID
	utils.DB.Model(&models.InventoryStock{}).Distinct().Pluck("user_id", &userIDs)
	var entryUsers []uuid.UUID
	utils.DB.Model(&models.StockEntry{}).Distinct().Pluck("user_id", &entryUsers)
	var metaUsers []uuid.UUID
	utils.DB.Model(&models.StockSnapshotMeta{}).Pluck("user_id", &metaUsers)

	seen := make(map[uuid.UUID]bool, len(userIDs)+len(entryUsers)+len(metaUsers))
	for _, uid := range append(append(userIDs, entryUsers...), metaUsers...) {
		if seen[uid] {
			continue
		}
		seen[uid] = true
		refreshStockSnapshots(uid, false)
	}
}

// snapshotPositionsAsOf returns the closing position of the latest snapshot on
// or before the given date for every product, aggregated across outlets.
func snapshotPositionsAsOf(userID uuid.UUID, date string) map[uuid.UUID]stockPosition {
	positions := make(map[uuid.UUID]stockPosition)
	if !utils.DB.Migrator().HasTable(&models.StockDailySnapshot{}) {
		return positions
	}

	var snaps []models.StockDailySnapshot
	if err := utils.DB.
		Where("user_id = ?", userID).
		Where(utils.SQLDateLTE("snapshot_date"), date).
		Select("product_id", "outlet_id", "snapshot_date", "closing_qty", "closing_value", "avg_cost").
		Find(&snaps).Error; err != nil {
		return positions
	}

	latest := make(map[snapshotKey]models.StockDailySnapshot, len(snaps))
	for _, s := range snaps {
		k := snapshotKey{ProductID: s.ProductID, OutletID: s.OutletID}
		if cur, ok := latest[k]; !ok || s.SnapshotDate.After(cur.SnapshotDate) {
			latest[k] = s
		}
	}
	for k, s := range latest {
		pos := positions[k.ProductID]
		pos.Qty += s.ClosingQty
		pos.Value += s.ClosingValue
		positions[k.ProductID] = pos
	}
	return positions
}

// latestOpeningOverride returns the most recent superadmin opening-stock
// override whose baseline day (effective_date − 1) is on or before `date` —
// i.e. an override effective on date+1 sets the closing position of `date`.
func latestOpeningOverride(userID uuid.UUID, date string) *models.StockOpeningOverride {
	if !utils.DB.Migrator().HasTable(&models.StockOpeningOverride{}) {
		return nil
	}
	end, err := time.Parse("2006-01-02", date)
	if err != nil {
		return nil
	}
	limit := end.AddDate(0, 0, 1).Format("2006-01-02")
	var o models.StockOpeningOverride
	if err := utils.DB.Where("user_id = ?", userID).
		Where(utils.SQLDateLTE("effective_date"), limit).
		Order("effective_date DESC").Limit(1).First(&o).Error; err != nil {
		return nil
	}
	return &o
}

// overallPositionAsOf returns the aggregate stock position (quantity and value
// across all products and outlets) at the end of `date`, adjusted by the
// latest overall opening-stock override: the override declares the position at
// the close of the day before its effective date, and computed movements since
// that baseline apply on top. Callers must run ensureStockSnapshots first.
func overallPositionAsOf(userID uuid.UUID, date string) (qty, value float64) {
	computed := func(d string) (float64, float64) {
		var q, v float64
		for _, p := range snapshotPositionsAsOf(userID, d) {
			q += p.Qty
			v += p.Value
		}
		return q, v
	}
	cQty, cVal := computed(date)
	o := latestOpeningOverride(userID, date)
	if o == nil {
		return cQty, cVal
	}
	base := o.EffectiveDate.AddDate(0, 0, -1).Format("2006-01-02")
	bQty, bVal := computed(base)
	return o.Quantity + cQty - bQty, o.Value + cVal - bVal
}

// snapshotMovements returns per-product units moved into and out of stock
// within the inclusive [start, end] range, from the daily snapshot table.
func snapshotMovements(userID uuid.UUID, start, end string) map[uuid.UUID][2]float64 {
	movements := make(map[uuid.UUID][2]float64)
	if !utils.DB.Migrator().HasTable(&models.StockDailySnapshot{}) {
		return movements
	}

	var snaps []models.StockDailySnapshot
	if err := utils.DB.
		Where("user_id = ?", userID).
		Where(utils.SQLDateGTE("snapshot_date"), start).
		Where(utils.SQLDateLTE("snapshot_date"), end).
		Select("product_id", "in_qty", "out_qty").
		Find(&snaps).Error; err != nil {
		return movements
	}
	for _, s := range snaps {
		m := movements[s.ProductID]
		m[0] += s.InQty
		m[1] += s.OutQty
		movements[s.ProductID] = m
	}
	return movements
}
