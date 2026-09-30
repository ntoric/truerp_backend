package controllers

import (
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// loadStockReport builds the opening/closing stock analysis for an inclusive
// [start, end] range resolved from period/anchor/custom params — the same
// window semantics the daily/periodic and profit & loss reports use.
func loadStockReport(userID uuid.UUID, period, anchorDate, startDate, endDate string) (models.StockReport, error) {
	start, end, label, err := resolvePeriodRange(period, anchorDate, startDate, endDate)
	if err != nil {
		return models.StockReport{}, err
	}

	report := models.StockReport{
		Period:    strings.ToLower(strings.TrimSpace(period)),
		StartDate: start,
		EndDate:   end,
		Label:     label,
	}

	var business models.Business
	if err := utils.DB.Where("user_id = ?", userID).First(&business).Error; err == nil {
		report.BusinessName = business.Name
	}

	// Opening positions are taken at the close of the day before the period
	// starts; closing positions at the period end — same as the P&L report.
	// Positions and movements come from the materialized daily snapshot table,
	// refreshed if the stock source data changed since the last rebuild.
	ensureStockSnapshots(userID)
	startT, _ := time.Parse("2006-01-02", start)
	openingDate := startT.AddDate(0, 0, -1).Format("2006-01-02")
	opening := stockPositionsAsOf(userID, openingDate)
	closing := stockPositionsAsOf(userID, end)
	// Report totals honor the latest overall opening-stock override — the
	// aggregate position can't be decomposed per product, so any residual vs
	// the per-product lines is surfaced as its own line below.
	openingQtyTotal, openingValTotal := overallPositionAsOf(userID, openingDate)
	closingQtyTotal, closingValTotal := overallPositionAsOf(userID, end)

	// Units moved into/out of stock within the period.
	inOut := snapshotMovements(userID, start, end)
	for _, m := range inOut {
		report.InQty += m[0]
		report.OutQty += m[1]
	}

	// Every product that has an opening position, closing position, or
	// in-period movement gets a line.
	seen := make(map[uuid.UUID]bool)
	for id := range opening {
		seen[id] = true
	}
	for id := range closing {
		seen[id] = true
	}
	for id := range inOut {
		seen[id] = true
	}
	productIDs := make([]uuid.UUID, 0, len(seen))
	for id := range seen {
		productIDs = append(productIDs, id)
	}

	var products []models.Product
	if len(productIDs) > 0 {
		utils.DB.Where("user_id = ? AND id IN ?", userID, productIDs).
			Select("id", "name", "sku", "category", "unit").Find(&products)
	}
	meta := make(map[uuid.UUID]models.Product, len(products))
	for _, p := range products {
		meta[p.ID] = p
	}

	lines := make([]models.StockReportLine, 0, len(seen))
	for id := range seen {
		o := opening[id]
		cl := closing[id]
		m := inOut[id]
		p := meta[id]
		name := p.Name
		if name == "" {
			name = "(Deleted product)"
		}
		line := models.StockReportLine{
			ProductID:    id,
			ProductName:  name,
			SKU:          p.SKU,
			Category:     p.Category,
			Unit:         p.Unit,
			OpeningQty:   o.Qty,
			OpeningValue: o.Value,
			InQty:        m[0],
			OutQty:       m[1],
			ClosingQty:   cl.Qty,
			ClosingValue: cl.Value,
		}
		line.ChangeQty = line.ClosingQty - line.OpeningQty
		line.ChangeValue = line.ClosingValue - line.OpeningValue
		lines = append(lines, line)

		report.OpeningStockQty += o.Qty
		report.OpeningStock += line.OpeningValue
		report.ClosingStockQty += cl.Qty
		report.ClosingStock += line.ClosingValue
	}
	sort.Slice(lines, func(i, j int) bool {
		return strings.ToLower(lines[i].ProductName) < strings.ToLower(lines[j].ProductName)
	})

	// An overall opening-stock override shifts the aggregate position without a
	// per-product breakdown — surface the residual as its own line so the
	// report's line items still reconcile with the totals.
	dOpenQty := openingQtyTotal - report.OpeningStockQty
	dOpenVal := openingValTotal - report.OpeningStock
	dCloseQty := closingQtyTotal - report.ClosingStockQty
	dCloseVal := closingValTotal - report.ClosingStock
	if math.Abs(dOpenQty)+math.Abs(dOpenVal)+math.Abs(dCloseQty)+math.Abs(dCloseVal) > 1e-6 {
		lines = append(lines, models.StockReportLine{
			ProductName:  "(Opening stock override)",
			OpeningQty:   dOpenQty,
			OpeningValue: dOpenVal,
			ClosingQty:   dCloseQty,
			ClosingValue: dCloseVal,
			ChangeQty:    dCloseQty - dOpenQty,
			ChangeValue:  dCloseVal - dOpenVal,
		})
	}

	report.OpeningStockQty = openingQtyTotal
	report.OpeningStock = openingValTotal
	report.ClosingStockQty = closingQtyTotal
	report.ClosingStock = closingValTotal
	report.StockChangeQty = report.ClosingStockQty - report.OpeningStockQty
	report.StockChange = report.ClosingStock - report.OpeningStock
	report.Lines = lines
	return report, nil
}

func GetStockReport(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	period, anchor, startDate, endDate := parseProfitLossParams(c)

	report, err := loadStockReport(userID, period, anchor, startDate, endDate)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, report)
}
