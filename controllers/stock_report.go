package controllers

import (
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
	startT, _ := time.Parse("2006-01-02", start)
	openingDate := startT.AddDate(0, 0, -1).Format("2006-01-02")
	opening := stockPositionsAsOf(userID, openingDate)
	closing := stockPositionsAsOf(userID, end)

	// Units moved into/out of stock within the period.
	type movementRow struct {
		ProductID uuid.UUID
		InQty     float64
		OutQty    float64
	}
	var movements []movementRow
	utils.DB.Raw(`
		SELECT product_id,
			COALESCE(SUM(CASE WHEN quantity > 0 THEN quantity ELSE 0 END), 0) AS in_qty,
			COALESCE(SUM(CASE WHEN quantity < 0 THEN -quantity ELSE 0 END), 0) AS out_qty
		FROM stock_entries
		WHERE user_id = ? AND product_id IS NOT NULL AND deleted_at IS NULL
			AND (approval_status = 'approved' OR approval_status = '' OR approval_status IS NULL)
			AND DATE(entry_date) >= ? AND DATE(entry_date) <= ?
		GROUP BY product_id`, userID, start, end).Scan(&movements)

	inOut := make(map[uuid.UUID]movementRow, len(movements))
	for _, m := range movements {
		inOut[m.ProductID] = m
		report.InQty += m.InQty
		report.OutQty += m.OutQty
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
			OpeningValue: o.Qty * o.Cost,
			InQty:        m.InQty,
			OutQty:       m.OutQty,
			ClosingQty:   cl.Qty,
			ClosingValue: cl.Qty * cl.Cost,
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
