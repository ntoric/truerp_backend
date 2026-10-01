package controllers

import (
	"bytes"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/go-pdf/fpdf"
	"github.com/google/uuid"
	"github.com/tealeg/xlsx/v3"
	"gorm.io/gorm"
)

// dailyProfitAgg accumulates one calendar day's raw money amounts before the
// derived stock/profit columns are computed.
type dailyProfitAgg struct {
	Sales          float64
	COGS           float64
	SalesReturn    float64
	ReturnCost     float64 // purchase cost reversed by sales returns / credit notes
	Purchase       float64
	PurchaseReturn float64
	Expenses       float64
}

type dailyProfitSumRow struct {
	Day   string
	Value float64
}

func normalizeReportDay(day string) string {
	if len(day) >= 10 {
		return day[:10]
	}
	return day
}

// scanDailyProfitSum runs a raw "SELECT <dayexpr> AS day, SUM(...) AS value ...
// GROUP BY <dayexpr>" query and returns amounts keyed by YYYY-MM-DD.
func scanDailyProfitSum(db *gorm.DB, query string, args ...interface{}) map[string]float64 {
	var rows []dailyProfitSumRow
	db.Raw(query, args...).Scan(&rows)
	out := make(map[string]float64, len(rows))
	for _, r := range rows {
		out[normalizeReportDay(r.Day)] += r.Value
	}
	return out
}

func mergeDailyProfitSum(dst, src map[string]float64) {
	for day, v := range src {
		dst[day] += v
	}
}

// loadDailyProfitAggregates returns per-day source amounts for the inclusive
// [start, end] range, keyed by YYYY-MM-DD. Cancelled documents are excluded.
// All amounts are tax-exclusive so profit figures match the billwise report.
func loadDailyProfitAggregates(db *gorm.DB, userID uuid.UUID, start, end string) map[string]*dailyProfitAgg {
	agg := make(map[string]*dailyProfitAgg)
	at := func(day string) *dailyProfitAgg {
		a := agg[day]
		if a == nil {
			a = &dailyProfitAgg{}
			agg[day] = a
		}
		return a
	}
	dayRange := func(col string) string {
		return utils.SQLDateGTE(col) + " AND " + utils.SQLDateLTE(col)
	}

	for day, v := range scanDailyProfitSum(db,
		`SELECT `+utils.SQLDateExpr("date")+` AS day, COALESCE(SUM(total_amount - tax_total), 0) AS value
		 FROM invoices
		 WHERE user_id = ? AND status != 'cancelled' AND deleted_at IS NULL
		 AND `+dayRange("date")+`
		 GROUP BY `+utils.SQLDateExpr("date"), userID, start, end) {
		at(day).Sales = v
	}

	for day, v := range scanDailyProfitSum(db,
		`SELECT `+utils.SQLDateExpr("i.date")+` AS day,
			COALESCE(SUM(ii.quantity * COALESCE(p.purchase_price, 0)), 0) AS value
		 FROM invoice_items ii
		 INNER JOIN invoices i ON i.id = ii.invoice_id
		 LEFT JOIN products p ON p.id = ii.product_id
		 WHERE i.user_id = ? AND i.status != 'cancelled' AND i.deleted_at IS NULL
		 AND `+dayRange("i.date")+`
		 GROUP BY `+utils.SQLDateExpr("i.date"), userID, start, end) {
		at(day).COGS = v
	}

	salesReturn := scanDailyProfitSum(db,
		`SELECT `+utils.SQLDateExpr("sr.date")+` AS day,
			COALESCE(SUM(sri.quantity * sri.unit_price), 0) AS value
		 FROM sales_return_items sri
		 INNER JOIN sales_returns sr ON sr.id = sri.return_id
		 WHERE sr.user_id = ? AND sr.status != 'cancelled' AND sr.deleted_at IS NULL
		 AND `+dayRange("sr.date")+`
		 GROUP BY `+utils.SQLDateExpr("sr.date"), userID, start, end)
	mergeDailyProfitSum(salesReturn, scanDailyProfitSum(db,
		`SELECT `+utils.SQLDateExpr("cn.date")+` AS day,
			COALESCE(SUM(cni.quantity * cni.unit_price), 0) AS value
		 FROM credit_note_items cni
		 INNER JOIN credit_notes cn ON cn.id = cni.credit_note_id
		 WHERE cn.user_id = ? AND cn.status != 'cancelled' AND cn.deleted_at IS NULL
		 AND `+dayRange("cn.date")+`
		 GROUP BY `+utils.SQLDateExpr("cn.date"), userID, start, end))
	for day, v := range salesReturn {
		at(day).SalesReturn = v
	}

	returnCost := scanDailyProfitSum(db,
		`SELECT `+utils.SQLDateExpr("sr.date")+` AS day,
			COALESCE(SUM(sri.quantity * COALESCE(p.purchase_price, 0)), 0) AS value
		 FROM sales_return_items sri
		 INNER JOIN sales_returns sr ON sr.id = sri.return_id
		 LEFT JOIN products p ON p.id = sri.product_id
		 WHERE sr.user_id = ? AND sr.status != 'cancelled' AND sr.deleted_at IS NULL
		 AND `+dayRange("sr.date")+`
		 GROUP BY `+utils.SQLDateExpr("sr.date"), userID, start, end)
	mergeDailyProfitSum(returnCost, scanDailyProfitSum(db,
		`SELECT `+utils.SQLDateExpr("cn.date")+` AS day,
			COALESCE(SUM(cni.quantity * COALESCE(p.purchase_price, 0)), 0) AS value
		 FROM credit_note_items cni
		 INNER JOIN credit_notes cn ON cn.id = cni.credit_note_id
		 LEFT JOIN invoice_items ii ON ii.id = cni.invoice_item_id
		 LEFT JOIN products p ON p.id = ii.product_id
		 WHERE cn.user_id = ? AND cn.status != 'cancelled' AND cn.deleted_at IS NULL
		 AND `+dayRange("cn.date")+`
		 GROUP BY `+utils.SQLDateExpr("cn.date"), userID, start, end))
	for day, v := range returnCost {
		at(day).ReturnCost = v
	}

	for day, v := range scanDailyProfitSum(db,
		`SELECT `+utils.SQLDateExpr("bill_date")+` AS day, COALESCE(SUM(total_amount - tax_total), 0) AS value
		 FROM purchase_bills
		 WHERE user_id = ? AND deleted_at IS NULL
		 AND `+dayRange("bill_date")+`
		 GROUP BY `+utils.SQLDateExpr("bill_date"), userID, start, end) {
		at(day).Purchase = v
	}

	purchaseReturn := scanDailyProfitSum(db,
		`SELECT `+utils.SQLDateExpr("pr.date")+` AS day,
			COALESCE(SUM(pri.quantity * pri.unit_price), 0) AS value
		 FROM purchase_return_items pri
		 INNER JOIN purchase_returns pr ON pr.id = pri.return_id
		 WHERE pr.user_id = ? AND pr.status != 'cancelled' AND pr.deleted_at IS NULL
		 AND `+dayRange("pr.date")+`
		 GROUP BY `+utils.SQLDateExpr("pr.date"), userID, start, end)
	mergeDailyProfitSum(purchaseReturn, scanDailyProfitSum(db,
		`SELECT `+utils.SQLDateExpr("dn.date")+` AS day,
			COALESCE(SUM(dni.quantity * dni.unit_price), 0) AS value
		 FROM debit_note_items dni
		 INNER JOIN debit_notes dn ON dn.id = dni.debit_note_id
		 WHERE dn.user_id = ? AND dn.status != 'cancelled' AND dn.deleted_at IS NULL
		 AND `+dayRange("dn.date")+`
		 GROUP BY `+utils.SQLDateExpr("dn.date"), userID, start, end))
	for day, v := range purchaseReturn {
		at(day).PurchaseReturn = v
	}

	for day, v := range scanDailyProfitSum(db,
		`SELECT `+utils.SQLDateExpr("date")+` AS day, COALESCE(SUM(amount), 0) AS value
		 FROM expenses
		 WHERE user_id = ? AND deleted_at IS NULL
		 AND `+dayRange("date")+`
		 GROUP BY `+utils.SQLDateExpr("date"), userID, start, end) {
		at(day).Expenses = v
	}

	return agg
}

// dailyStockPosition is the business-wide stock value at the start and end of
// a calendar day, read from the materialized snapshot table.
type dailyStockPosition struct {
	Opening float64
	Closing float64
}

// loadDailyStockPositions returns opening/closing stock value per day over
// [start, end], carrying positions forward across days with no snapshot rows
// and applying opening-stock overrides the same way overallPositionAsOf does.
// Callers must run ensureStockSnapshots first.
func loadDailyStockPositions(db *gorm.DB, userID uuid.UUID, start, end string) map[string]dailyStockPosition {
	positions := make(map[string]dailyStockPosition)
	if !db.Migrator().HasTable(&models.StockDailySnapshot{}) {
		return positions
	}

	type stockDayRow struct {
		Day     string
		Opening float64
		Closing float64
	}
	var stockRows []stockDayRow
	db.Raw(
		`SELECT `+utils.SQLDateExpr("snapshot_date")+` AS day,
			COALESCE(SUM(opening_value), 0) AS opening,
			COALESCE(SUM(closing_value), 0) AS closing
		 FROM stock_daily_snapshots
		 WHERE user_id = ?
		 AND `+utils.SQLDateGTE("snapshot_date")+` AND `+utils.SQLDateLTE("snapshot_date")+`
		 GROUP BY `+utils.SQLDateExpr("snapshot_date"), userID, start, end).Scan(&stockRows)

	raw := make(map[string]dailyStockPosition, len(stockRows))
	for _, r := range stockRows {
		raw[normalizeReportDay(r.Day)] = dailyStockPosition{Opening: r.Opening, Closing: r.Closing}
	}

	// Baseline: computed stock value at close of the day before the range.
	startT, err := time.Parse("2006-01-02", start)
	if err != nil {
		return positions
	}
	endT, err := time.Parse("2006-01-02", end)
	if err != nil {
		return positions
	}
	prevDay := startT.AddDate(0, 0, -1).Format("2006-01-02")
	var prevClosing float64
	for _, p := range snapshotPositionsAsOfDB(db, userID, prevDay) {
		prevClosing += p.Value
	}

	// Opening-stock overrides shift every day's computed position by a
	// constant once their base day (effective_date − 1) is reached.
	type overrideDelta struct {
		base  string
		delta float64
	}
	var deltas []overrideDelta
	if db.Migrator().HasTable(&models.StockOpeningOverride{}) {
		var overrides []models.StockOpeningOverride
		db.Unscoped().Where("user_id = ?", userID).Order("effective_date").Find(&overrides)
		for _, o := range overrides {
			base := o.EffectiveDate.AddDate(0, 0, -1).Format("2006-01-02")
			var baseValue float64
			for _, p := range snapshotPositionsAsOfDB(db, userID, base) {
				baseValue += p.Value
			}
			deltas = append(deltas, overrideDelta{base: base, delta: o.Value - baseValue})
		}
	}

	di := 0
	var deltaPrev float64
	for d := startT; !d.After(endT); d = d.AddDate(0, 0, 1) {
		day := d.Format("2006-01-02")
		delta := deltaPrev
		for di < len(deltas) && deltas[di].base <= day {
			delta = deltas[di].delta
			di++
		}
		pos := raw[day]
		if pos.Closing == 0 && pos.Opening == 0 {
			// No snapshot rows this day: position carries forward unchanged.
			pos.Closing = prevClosing
		}
		positions[day] = dailyStockPosition{
			Opening: prevClosing + deltaPrev,
			Closing: pos.Closing + delta,
		}
		prevClosing = pos.Closing
		deltaPrev = delta
	}
	return positions
}

// loadDailyProfitRows computes the report rows on the global DB handle —
// this is the synchronous ("event"/read-time) path used when asynchronous
// daily-profit updates are disabled in Developer Settings.
func loadDailyProfitRows(userID uuid.UUID, start, end string) []models.DailyProfitRow {
	ensureStockSnapshots(userID)
	return computeDailyProfitRows(utils.DB, userID, start, end)
}

// computeDailyProfitRows builds one row per calendar day in [start, end],
// ascending. Sales profit = net sales − net COGS (item cost at current
// purchase price, same convention as the billwise/daily reports). Gross
// profit uses the trading-account formula — net sales − net purchases +
// closing stock − opening stock — so purchases and stock adjustments flow
// through the day's stock columns. It runs on the given DB handle — passing
// a transaction handle makes the calculation consistent with the rest of an
// enclosing transaction. Callers must run ensureStockSnapshots first.
func computeDailyProfitRows(db *gorm.DB, userID uuid.UUID, start, end string) []models.DailyProfitRow {
	aggs := loadDailyProfitAggregates(db, userID, start, end)
	stock := loadDailyStockPositions(db, userID, start, end)

	startT, err := time.Parse("2006-01-02", start)
	if err != nil {
		return nil
	}
	endT, err := time.Parse("2006-01-02", end)
	if err != nil {
		return nil
	}

	rows := make([]models.DailyProfitRow, 0, int(endT.Sub(startT).Hours()/24)+1)
	for d := startT; !d.After(endT); d = d.AddDate(0, 0, 1) {
		day := d.Format("2006-01-02")
		a := aggs[day]
		if a == nil {
			a = &dailyProfitAgg{}
		}
		pos := stock[day]

		netSales := a.Sales - a.SalesReturn
		row := models.DailyProfitRow{
			Date:           day,
			OpeningStock:   pos.Opening,
			Sales:          a.Sales,
			COGS:           a.COGS,
			SalesReturn:    a.SalesReturn,
			SalesProfit:    netSales - (a.COGS - a.ReturnCost),
			Purchase:       a.Purchase,
			PurchaseReturn: a.PurchaseReturn,
			ClosingStock:   pos.Closing,
			Expenses:       a.Expenses,
		}
		row.GrossProfit = netSales - (a.Purchase - a.PurchaseReturn) + row.ClosingStock - row.OpeningStock
		row.NetProfit = row.GrossProfit - row.Expenses
		rows = append(rows, row)
	}
	return rows
}

func dailyProfitTotals(rows []models.DailyProfitRow) models.DailyProfitRow {
	var t models.DailyProfitRow
	for i, r := range rows {
		if i == 0 {
			t.OpeningStock = r.OpeningStock
		}
		t.ClosingStock = r.ClosingStock
		t.Sales += r.Sales
		t.COGS += r.COGS
		t.SalesReturn += r.SalesReturn
		t.SalesProfit += r.SalesProfit
		t.Purchase += r.Purchase
		t.PurchaseReturn += r.PurchaseReturn
		t.GrossProfit += r.GrossProfit
		t.Expenses += r.Expenses
		t.NetProfit += r.NetProfit
	}
	return t
}

type dailyProfitParams struct {
	period    string
	anchor    string
	startDate string
	endDate   string
	sortKey   string
	page      int
	perPage   int
}

func parseDailyProfitParams(c *gin.Context, userID uuid.UUID) dailyProfitParams {
	period, anchor, startDate, endDate := parseProfitLossParams(c)
	if c.Query("date") == "" {
		// Default "today" in the user's configured timezone (IST when unset),
		// matching the cron's notion of the current day.
		anchor = dailyProfitToday(userID)
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	perPage, _ := strconv.Atoi(c.DefaultQuery("per_page", "25"))
	if perPage < 1 {
		perPage = 25
	}
	if perPage > 500 {
		perPage = 500
	}
	return dailyProfitParams{
		period:    period,
		anchor:    anchor,
		startDate: startDate,
		endDate:   endDate,
		sortKey:   c.DefaultQuery("sort", "desc"),
		page:      page,
		perPage:   perPage,
	}
}

func sortDailyProfitRows(rows []models.DailyProfitRow, sortKey string) {
	if sortKey == "asc" {
		return // rows are already ascending
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Date > rows[j].Date })
}

func dailyProfitBusinessName(userID uuid.UUID) string {
	var business models.Business
	if err := utils.DB.Where("user_id = ?", userID).First(&business).Error; err == nil {
		return business.Name
	}
	return ""
}

// GetDailyProfitReport serves the per-day profit table. Rows are server-side
// paginated with page/per_page; Totals always covers the whole range. With
// asynchronous updates enabled (Developer Settings) rows come from the
// materialized daily_profit_entries table kept fresh by the background cron;
// otherwise they are computed live from transactions on every read.
// GET /api/v1/dashboard/daily-profit-report
func GetDailyProfitReport(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	params := parseDailyProfitParams(c, userID)

	start, end, label, err := resolvePeriodRange(params.period, params.anchor, params.startDate, params.endDate)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	report := models.DailyProfitReport{
		BusinessName: dailyProfitBusinessName(userID),
		Period:       strings.ToLower(strings.TrimSpace(params.period)),
		StartDate:    start,
		EndDate:      end,
		Label:        label,
		Page:         params.page,
		PerPage:      params.perPage,
		AsyncMode:    dailyProfitAsyncEnabled(userID),
	}

	if report.AsyncMode {
		ensureDailyProfitCoverage(userID, start, end)
		rows, totals, total := loadDailyProfitEntryPage(
			userID, start, end, params.sortKey, params.page, params.perPage)
		report.Rows = rows
		report.Totals = totals
		report.TotalDays = total
	} else {
		rows := loadDailyProfitRows(userID, start, end)
		sortDailyProfitRows(rows, params.sortKey)

		total := len(rows)
		from := (params.page - 1) * params.perPage
		if from > total {
			from = total
		}
		to := from + params.perPage
		if to > total {
			to = total
		}
		report.TotalDays = int64(total)
		report.Rows = rows[from:to]
		report.Totals = dailyProfitTotals(rows)
	}

	c.JSON(http.StatusOK, report)
}

// dailyProfitDayCount returns the inclusive number of calendar days between
// two YYYY-MM-DD dates (0 on parse error).
func dailyProfitDayCount(start, end string) int {
	s, err1 := time.Parse("2006-01-02", start)
	e, err2 := time.Parse("2006-01-02", end)
	if err1 != nil || err2 != nil {
		return 0
	}
	return int(e.Sub(s).Hours()/24) + 1
}

// ensureDailyProfitCoverage queues a recompute for the requested range when
// materialized rows are missing for any day, and waits briefly so the caller
// can serve the fresh rows. Fully covered ranges return immediately — the
// cron sweep keeps them current.
func ensureDailyProfitCoverage(userID uuid.UUID, start, end string) {
	if !utils.DB.Migrator().HasTable(&models.DailyProfitEntry{}) {
		return
	}
	var covered int64
	utils.DB.Model(&models.DailyProfitEntry{}).
		Where("user_id = ? AND date >= ? AND date <= ?", userID, start, end).
		Distinct("date").
		Count(&covered)
	if covered >= int64(dailyProfitDayCount(start, end)) {
		return
	}
	waitDailyProfitTicket(scheduleDailyProfitRecompute(userID, start, end), dailyProfitJobWait)
}

// dailyProfitRowsForRange returns all report rows for a range, choosing the
// materialized table in async mode and live computation otherwise. Used by the
// Excel/PDF exports so they always cover the full period.
func dailyProfitRowsForRange(userID uuid.UUID, start, end, sortKey string) []models.DailyProfitRow {
	if dailyProfitAsyncEnabled(userID) {
		ensureDailyProfitCoverage(userID, start, end)
		return loadAllDailyProfitEntries(userID, start, end, sortKey)
	}
	rows := loadDailyProfitRows(userID, start, end)
	sortDailyProfitRows(rows, sortKey)
	return rows
}

var dailyProfitExportHeaders = []string{
	"Date", "Opening Stock", "Sales", "COGS", "Sales Return", "Sales Profit",
	"Purchase", "Purchase Return", "Closing Stock", "Gross Profit", "Expenses", "Net Profit",
}

func dailyProfitRowAmounts(r models.DailyProfitRow) []float64 {
	return []float64{
		r.OpeningStock, r.Sales, r.COGS, r.SalesReturn, r.SalesProfit,
		r.Purchase, r.PurchaseReturn, r.ClosingStock, r.GrossProfit,
		r.Expenses, r.NetProfit,
	}
}

func ExportDailyProfitReportExcel(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	params := parseDailyProfitParams(c, userID)

	start, end, label, err := resolvePeriodRange(params.period, params.anchor, params.startDate, params.endDate)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	rows := dailyProfitRowsForRange(userID, start, end, params.sortKey)
	businessName := dailyProfitBusinessName(userID)

	file := xlsx.NewFile()
	sheet, err := file.AddSheet("Daily Profit")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create Excel sheet"})
		return
	}

	writeRow := func(cols ...string) {
		row := sheet.AddRow()
		for _, col := range cols {
			row.AddCell().SetValue(col)
		}
	}

	writeRow("Daily Profit Report")
	if businessName != "" {
		writeRow("Business", businessName)
	}
	writeRow("Period", label)
	writeRow("Range", start+" to "+end)
	writeRow("")

	header := sheet.AddRow()
	for _, h := range dailyProfitExportHeaders {
		cell := header.AddCell()
		cell.SetValue(h)
		cell.GetStyle().Font.Bold = true
	}

	for _, r := range rows {
		row := sheet.AddRow()
		row.AddCell().SetValue(r.Date)
		for _, v := range dailyProfitRowAmounts(r) {
			cell := row.AddCell()
			cell.SetFloat(v)
			cell.SetFormat("#,##0.00")
		}
	}

	totals := dailyProfitTotals(rows)
	totalRow := sheet.AddRow()
	totalLabel := totalRow.AddCell()
	totalLabel.SetValue("Total")
	totalLabel.GetStyle().Font.Bold = true
	for _, v := range dailyProfitRowAmounts(totals) {
		cell := totalRow.AddCell()
		cell.SetFloat(v)
		cell.SetFormat("#,##0.00")
		cell.GetStyle().Font.Bold = true
	}

	writeRow("")
	writeRow("Note", "Sales profit = (sales - sales return) - cost of items sold at current purchase price.")
	writeRow("Note", "Gross profit = (sales - sales return) - (purchase - purchase return) + closing stock - opening stock.")
	writeRow("Note", "Net profit = gross profit - expenses. Stock values come from the daily stock snapshot ledger.")
	writeRow("Note", "All amounts are tax-exclusive.")

	period := strings.ToLower(strings.TrimSpace(params.period))
	filename := fmt.Sprintf("daily_profit_%s_%s_%s.xlsx", period, start, end)
	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	if err := file.Write(c.Writer); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to write Excel file"})
		return
	}
}

func buildDailyProfitReportPDF(report models.DailyProfitReport, rows []models.DailyProfitRow) ([]byte, error) {
	pdf := fpdf.New("L", "mm", "A4", "")
	pdf.SetMargins(10, 14, 10)
	pdf.SetAutoPageBreak(true, 14)
	pdf.AddPage()

	pdf.SetFont("Arial", "B", 16)
	pdf.SetTextColor(37, 99, 235)
	pdf.CellFormat(0, 9, "DAILY PROFIT REPORT", "", 1, "L", false, 0, "")

	pdf.SetFont("Arial", "", 10)
	pdf.SetTextColor(80, 80, 80)
	if report.BusinessName != "" {
		pdf.CellFormat(0, 5.5, sanitizePDFText(report.BusinessName), "", 1, "L", false, 0, "")
	}
	if report.Label != "" {
		pdf.CellFormat(0, 5.5, sanitizePDFText(report.Label), "", 1, "L", false, 0, "")
	}
	pdf.CellFormat(0, 5.5, "Range: "+report.StartDate+" to "+report.EndDate, "", 1, "L", false, 0, "")
	pdf.Ln(5)

	pageW, _ := pdf.GetPageSize()
	leftM, _, rightM, _ := pdf.GetMargins()
	usable := pageW - leftM - rightM
	colDate := usable * 0.08
	colNum := (usable - colDate) / 11

	writeHeader := func() {
		pdf.SetFillColor(243, 244, 246)
		pdf.SetDrawColor(200, 200, 200)
		pdf.SetFont("Arial", "B", 7)
		pdf.SetTextColor(40, 40, 40)
		pdf.CellFormat(colDate, 7, "Date", "1", 0, "L", true, 0, "")
		for _, h := range dailyProfitExportHeaders[1:] {
			pdf.CellFormat(colNum, 7, h, "1", 0, "R", true, 0, "")
		}
		pdf.Ln(7)
	}
	writeHeader()

	for _, r := range rows {
		if pdf.GetY() > 190 {
			pdf.AddPage()
			writeHeader()
		}
		pdf.SetFillColor(255, 255, 255)
		pdf.SetFont("Arial", "", 7)
		pdf.SetTextColor(40, 40, 40)
		pdf.CellFormat(colDate, 6, r.Date, "1", 0, "L", true, 0, "")
		amounts := dailyProfitRowAmounts(r)
		for i, v := range amounts {
			// Colour profit columns red/green.
			if i == 4 || i == 8 || i == 10 {
				if v < 0 {
					pdf.SetTextColor(153, 27, 27)
				} else {
					pdf.SetTextColor(22, 101, 52)
				}
			} else {
				pdf.SetTextColor(40, 40, 40)
			}
			pdf.CellFormat(colNum, 6, fmt.Sprintf("%.2f", v), "1", 0, "R", true, 0, "")
		}
		pdf.Ln(6)
	}

	totals := report.Totals
	pdf.SetFillColor(243, 244, 246)
	pdf.SetFont("Arial", "B", 7)
	pdf.SetTextColor(40, 40, 40)
	pdf.CellFormat(colDate, 6.5, "Total", "1", 0, "L", true, 0, "")
	for _, v := range dailyProfitRowAmounts(totals) {
		pdf.CellFormat(colNum, 6.5, fmt.Sprintf("%.2f", v), "1", 0, "R", true, 0, "")
	}
	pdf.Ln(8)

	pdf.SetFont("Arial", "I", 7.5)
	pdf.SetTextColor(140, 140, 140)
	pdf.MultiCell(0, 4, sanitizePDFText("Sales profit = (sales - sales return) - cost of items sold at current purchase price. Gross profit = (sales - sales return) - (purchase - purchase return) + closing stock - opening stock. Net profit = gross profit - expenses. All amounts are tax-exclusive. Stock values come from the daily stock snapshot ledger; cancelled documents are excluded. Generated from TruERP."), "", "L", false)

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func ExportDailyProfitReportPDF(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	params := parseDailyProfitParams(c, userID)

	start, end, label, err := resolvePeriodRange(params.period, params.anchor, params.startDate, params.endDate)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	rows := dailyProfitRowsForRange(userID, start, end, params.sortKey)

	report := models.DailyProfitReport{
		BusinessName: dailyProfitBusinessName(userID),
		StartDate:    start,
		EndDate:      end,
		Label:        label,
		TotalDays:    int64(len(rows)),
		Totals:       dailyProfitTotals(rows),
	}

	pdfBytes, err := buildDailyProfitReportPDF(report, rows)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate PDF"})
		return
	}

	period := strings.ToLower(strings.TrimSpace(params.period))
	filename := fmt.Sprintf("daily_profit_%s_%s_%s.pdf", period, start, end)
	c.Header("Content-Type", "application/pdf")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	c.Data(http.StatusOK, "application/pdf", pdfBytes)
}
