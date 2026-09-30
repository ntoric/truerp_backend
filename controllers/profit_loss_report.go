package controllers

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/go-pdf/fpdf"
	"github.com/google/uuid"
	"github.com/tealeg/xlsx/v3"
)

// plSumMetric sums an amount column for a model within an inclusive date range.
func plSumMetric(model interface{}, userID uuid.UUID, dateColumn, amountColumn, extra, startDate, endDate string) models.DailyReportMetric {
	var m models.DailyReportMetric
	q := utils.DB.Model(model).
		Where("user_id = ? AND DATE("+dateColumn+") >= ? AND DATE("+dateColumn+") <= ?", userID, startDate, endDate)
	if extra != "" {
		q = q.Where(extra)
	}
	q.Select("COALESCE(SUM(" + amountColumn + "), 0) as total_amount, COUNT(*) as count").Scan(&m)
	return m
}

// stockPosition is a product's on-hand quantity and inventory value (at
// weighted average cost) at a point in time.
type stockPosition struct {
	Qty   float64
	Value float64
}

// stockPositionsAsOf reads the materialized stock_daily_snapshots table to get
// per-product inventory quantity and value at the end of the given date.
// Snapshots reconcile the stock_entries ledger against live inventory_stocks,
// so stock that entered without ledger entries (e.g. migrated opening stock)
// is included. Callers must run ensureStockSnapshots first.
func stockPositionsAsOf(userID uuid.UUID, date string) map[uuid.UUID]stockPosition {
	return snapshotPositionsAsOf(userID, date)
}

// computeStockValueAsOf returns inventory quantity and value at the end of the
// given date — the materialized stock snapshots adjusted by the latest overall
// opening-stock override.
func computeStockValueAsOf(userID uuid.UUID, date string) (qty, value float64) {
	return overallPositionAsOf(userID, date)
}

// loadPLLedgerLines aggregates posted ledger movements on income/expense GL
// accounts for the period, grouped by account. debitMinusCredit picks the
// sign convention (debit-heavy for expense accounts, credit-heavy for income).
func loadPLLedgerLines(userID uuid.UUID, accountType string, excludeCodes, excludeRefTypes []string, debitMinusCredit bool, startDate, endDate string) (models.DailyReportMetric, []models.ProfitLossLine) {
	sign := "l.credit - l.debit"
	if debitMinusCredit {
		sign = "l.debit - l.credit"
	}

	var rows []models.ProfitLossLine
	q := utils.DB.Table("ledgers AS l").
		Select("a.name AS name, COALESCE(SUM("+sign+"), 0) AS amount, COUNT(*) AS count").
		Joins("JOIN accounts AS a ON a.id = l.account_id").
		Where("l.user_id = ? AND a.account_type = ? AND a.deleted_at IS NULL", userID, accountType).
		Where("DATE(l.transaction_date) >= ? AND DATE(l.transaction_date) <= ?", startDate, endDate)
	if len(excludeCodes) > 0 {
		q = q.Where("a.code NOT IN ?", excludeCodes)
	}
	if len(excludeRefTypes) > 0 {
		q = q.Where("l.transaction_type NOT IN ?", excludeRefTypes)
	}
	q.Group("a.id, a.name").Order("a.name").Scan(&rows)

	var m models.DailyReportMetric
	lines := make([]models.ProfitLossLine, 0, len(rows))
	for _, r := range rows {
		if r.Amount == 0 {
			continue
		}
		m.TotalAmount += r.Amount
		m.Count += r.Count
		lines = append(lines, r)
	}
	return m, lines
}

// loadProfitLossReport builds the trading & P&L statement for an inclusive
// [start, end] range resolved from period/anchor/custom params — the same
// window semantics the daily/periodic report uses.
func loadProfitLossReport(userID uuid.UUID, period, anchorDate, startDate, endDate string) (models.ProfitLossReport, error) {
	start, end, label, err := resolvePeriodRange(period, anchorDate, startDate, endDate)
	if err != nil {
		return models.ProfitLossReport{}, err
	}

	report := models.ProfitLossReport{
		Period:    strings.ToLower(strings.TrimSpace(period)),
		StartDate: start,
		EndDate:   end,
		Label:     label,
	}

	var business models.Business
	if err := utils.DB.Where("user_id = ?", userID).First(&business).Error; err == nil {
		report.BusinessName = business.Name
	}

	report.Sales = plSumMetric(&models.Invoice{}, userID, "date", "total_amount", "status != 'cancelled'", start, end)
	report.Purchases = plSumMetric(&models.PurchaseBill{}, userID, "bill_date", "total_amount", "", start, end)
	report.Expenses = plSumMetric(&models.Expense{}, userID, "date", "amount", "", start, end)

	salesReturns := plSumMetric(&models.SalesReturn{}, userID, "date", "amount", "status != 'cancelled'", start, end)
	creditNotes := plSumMetric(&models.CreditNote{}, userID, "date", "total_amount", "status != 'cancelled'", start, end)
	report.SalesReturns = models.DailyReportMetric{
		TotalAmount: salesReturns.TotalAmount + creditNotes.TotalAmount,
		Count:       salesReturns.Count + creditNotes.Count,
	}

	purchaseReturns := plSumMetric(&models.PurchaseReturn{}, userID, "date", "amount", "status != 'cancelled'", start, end)
	debitNotes := plSumMetric(&models.DebitNote{}, userID, "date", "total_amount", "status != 'cancelled'", start, end)
	report.PurchaseReturns = models.DailyReportMetric{
		TotalAmount: purchaseReturns.TotalAmount + debitNotes.TotalAmount,
		Count:       purchaseReturns.Count + debitNotes.Count,
	}

	// Opening stock is the inventory value at the close of the day before the
	// period starts; closing stock is the value at the period end. Positions
	// come from the materialized daily snapshot table, refreshed if the stock
	// source data changed since the last rebuild.
	ensureStockSnapshots(userID)
	startT, _ := time.Parse("2006-01-02", start)
	openingDate := startT.AddDate(0, 0, -1).Format("2006-01-02")
	report.OpeningStockQty, report.OpeningStock = computeStockValueAsOf(userID, openingDate)
	report.ClosingStockQty, report.ClosingStock = computeStockValueAsOf(userID, end)

	netSales := report.Sales.TotalAmount - report.SalesReturns.TotalAmount
	netPurchases := report.Purchases.TotalAmount - report.PurchaseReturns.TotalAmount
	report.GrossProfit = netSales - netPurchases + report.ClosingStock - report.OpeningStock

	report.OtherIncome, report.OtherIncomeLines = loadPLLedgerLines(
		userID, "income", []string{acCodeSales}, nil, false, start, end)
	// Expense-module and payroll postings are excluded because the Expenses
	// section below already counts them from the expenses table.
	report.IndirectExpenses, report.IndirectExpenseLines = loadPLLedgerLines(
		userID, "expense", []string{acCodePurchase}, []string{"expense", "payroll"}, true, start, end)

	report.NetProfit = report.GrossProfit +
		report.OtherIncome.TotalAmount -
		report.Expenses.TotalAmount

	report.ExpenseLines = loadExpenseLines(userID, start, end)

	return report, nil
}

func parseProfitLossParams(c *gin.Context) (period, anchor, startDate, endDate string) {
	period = c.DefaultQuery("period", "monthly")
	anchor = c.Query("date")
	if anchor == "" {
		anchor = time.Now().Format("2006-01-02")
	}
	startDate = c.Query("start_date")
	endDate = c.Query("end_date")
	return
}

func GetProfitLossReport(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	period, anchor, startDate, endDate := parseProfitLossParams(c)

	report, err := loadProfitLossReport(userID, period, anchor, startDate, endDate)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, report)
}

// plStatementRow is one rendered line in the P&L statement output (web table,
// Excel, PDF all share the same row list).
type plStatementRow struct {
	Label      string
	Amount     float64
	Count      int64
	Kind       string // "item" | "section" | "subtotal" | "total" | "detail"
	Negative   bool   // amount is presented as a deduction
	CountLabel string // appended to Count, e.g. "invoices"
	Details    string // fixed details text; takes precedence over Count+CountLabel
}

func (r plStatementRow) detailsText() string {
	if r.Details != "" {
		return r.Details
	}
	if r.CountLabel != "" {
		return fmt.Sprintf("%d %s", r.Count, r.CountLabel)
	}
	return ""
}

func profitLossStatementRows(r models.ProfitLossReport) []plStatementRow {
	netSales := r.Sales.TotalAmount - r.SalesReturns.TotalAmount
	netPurchases := r.Purchases.TotalAmount - r.PurchaseReturns.TotalAmount

	rows := []plStatementRow{
		{Label: "Trading Account", Kind: "section"},
		{Label: "Sales", Amount: r.Sales.TotalAmount, Count: r.Sales.Count, Kind: "item", CountLabel: "invoices"},
		{Label: "Less: Sales Return / Credit Notes", Amount: r.SalesReturns.TotalAmount, Count: r.SalesReturns.Count, Kind: "item", Negative: true, CountLabel: "notes/returns"},
		{Label: "Net Sales", Amount: netSales, Kind: "subtotal"},
		{Label: "Opening Stock", Amount: r.OpeningStock, Kind: "item", Details: fmt.Sprintf("%.2f units", r.OpeningStockQty)},
		{Label: "Purchases", Amount: r.Purchases.TotalAmount, Count: r.Purchases.Count, Kind: "item", CountLabel: "bills"},
		{Label: "Less: Purchase Return / Debit Notes", Amount: r.PurchaseReturns.TotalAmount, Count: r.PurchaseReturns.Count, Kind: "item", Negative: true, CountLabel: "notes/returns"},
		{Label: "Net Purchases", Amount: netPurchases, Kind: "subtotal"},
		{Label: "Closing Stock", Amount: r.ClosingStock, Kind: "item", Details: fmt.Sprintf("%.2f units", r.ClosingStockQty)},
		{Label: "Gross Profit", Amount: r.GrossProfit, Kind: "total"},
		{Label: "Profit & Loss", Kind: "section"},
		{Label: "Other Income", Amount: r.OtherIncome.TotalAmount, Count: r.OtherIncome.Count, Kind: "item", CountLabel: "postings"},
	}
	for _, line := range r.OtherIncomeLines {
		rows = append(rows, plStatementRow{
			Label: line.Name, Amount: line.Amount, Count: line.Count,
			Kind: "detail", CountLabel: "postings",
		})
	}
	rows = append(rows,
		plStatementRow{Label: "Expenses", Amount: r.Expenses.TotalAmount, Count: r.Expenses.Count, Kind: "item", Negative: true, CountLabel: "expenses"},
		plStatementRow{Label: "Net Profit", Amount: r.NetProfit, Kind: "total"},
	)
	return rows
}

func ExportProfitLossReportExcel(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	period, anchor, startDate, endDate := parseProfitLossParams(c)

	report, err := loadProfitLossReport(userID, period, anchor, startDate, endDate)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	file := xlsx.NewFile()
	sheet, err := file.AddSheet("Profit & Loss")
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
	writeAmountRow := func(label string, amount float64, countText string, bold bool) {
		row := sheet.AddRow()
		labelCell := row.AddCell()
		labelCell.SetValue(label)
		amountCell := row.AddCell()
		amountCell.SetFloat(amount)
		amountCell.SetFormat("#,##0.00")
		row.AddCell().SetValue(countText)
		if bold {
			labelCell.GetStyle().Font.Bold = true
			amountCell.GetStyle().Font.Bold = true
		}
	}

	writeRow("Profit & Loss Report")
	if report.BusinessName != "" {
		writeRow("Business", report.BusinessName)
	}
	writeRow("Period", report.Label)
	writeRow("Range", report.StartDate+" to "+report.EndDate)
	writeRow("")

	header := sheet.AddRow()
	for _, h := range []string{"Particulars", "Amount (INR)", "Details"} {
		cell := header.AddCell()
		cell.SetValue(h)
		cell.GetStyle().Font.Bold = true
	}

	for _, r := range profitLossStatementRows(report) {
		amount := r.Amount
		if r.Negative {
			amount = -amount
		}
		switch r.Kind {
		case "section":
			row := sheet.AddRow()
			cell := row.AddCell()
			cell.SetValue(r.Label)
			cell.GetStyle().Font.Bold = true
		case "detail":
			writeAmountRow("    "+r.Label, amount, r.detailsText(), false)
		default:
			label := r.Label
			if r.Negative {
				label = "(-) " + r.Label
			}
			writeAmountRow(label, amount, r.detailsText(), r.Kind == "subtotal" || r.Kind == "total")
		}
	}

	if len(report.ExpenseLines) > 0 {
		writeRow("")
		expHeader := sheet.AddRow()
		for _, h := range []string{"Expenses (per item)"} {
			cell := expHeader.AddCell()
			cell.SetValue(h)
			cell.GetStyle().Font.Bold = true
		}
		writeRow("Number", "Date", "Category", "Item", "Amount (INR)")
		for _, line := range report.ExpenseLines {
			itemDesc := line.ItemDescription
			if itemDesc == "" {
				itemDesc = line.Description
			}
			row := sheet.AddRow()
			row.AddCell().SetValue(line.ExpenseNumber)
			row.AddCell().SetValue(line.Date)
			row.AddCell().SetValue(line.Category)
			row.AddCell().SetValue(itemDesc)
			amt := row.AddCell()
			amt.SetFloat(line.Amount)
			amt.SetFormat("#,##0.00")
		}
	}

	writeRow("")
	writeRow("Note", "Gross profit = net sales − net purchases + closing stock − opening stock.")
	writeRow("Note", "Net profit = gross profit + other income − expenses.")

	filename := fmt.Sprintf("profit_loss_%s_%s_%s.xlsx", report.Period, report.StartDate, report.EndDate)
	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	if err := file.Write(c.Writer); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to write Excel file"})
		return
	}
}

func buildProfitLossReportPDF(report models.ProfitLossReport) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(14, 16, 14)
	pdf.SetAutoPageBreak(true, 16)
	pdf.AddPage()

	pdf.SetFont("Arial", "B", 18)
	pdf.SetTextColor(37, 99, 235)
	pdf.CellFormat(0, 10, "PROFIT & LOSS REPORT", "", 1, "L", false, 0, "")

	pdf.SetFont("Arial", "", 11)
	pdf.SetTextColor(80, 80, 80)
	if report.BusinessName != "" {
		pdf.CellFormat(0, 6, sanitizePDFText(report.BusinessName), "", 1, "L", false, 0, "")
	}
	if report.Label != "" {
		pdf.CellFormat(0, 6, sanitizePDFText(report.Label), "", 1, "L", false, 0, "")
	}
	if report.StartDate != "" && report.EndDate != "" {
		pdf.CellFormat(0, 6, "Range: "+report.StartDate+" to "+report.EndDate, "", 1, "L", false, 0, "")
	}
	pdf.Ln(6)

	pageW, _ := pdf.GetPageSize()
	leftM, _, rightM, _ := pdf.GetMargins()
	usable := pageW - leftM - rightM
	colLabel := usable * 0.55
	colDetails := usable * 0.2
	colAmount := usable * 0.25

	writeHeaderRow := func() {
		pdf.SetFillColor(243, 244, 246)
		pdf.SetDrawColor(200, 200, 200)
		pdf.SetFont("Arial", "B", 9)
		pdf.SetTextColor(40, 40, 40)
		pdf.CellFormat(colLabel, 8, "Particulars", "1", 0, "L", true, 0, "")
		pdf.CellFormat(colDetails, 8, "Details", "1", 0, "R", true, 0, "")
		pdf.CellFormat(colAmount, 8, "Amount (INR)", "1", 1, "R", true, 0, "")
	}
	writeHeaderRow()

	for _, row := range profitLossStatementRows(report) {
		switch row.Kind {
		case "section":
			pdf.SetFillColor(219, 234, 254)
			pdf.SetFont("Arial", "B", 9)
			pdf.SetTextColor(30, 64, 175)
			pdf.CellFormat(colLabel+colDetails+colAmount, 7, sanitizePDFText(row.Label), "1", 1, "L", true, 0, "")
		case "detail":
			pdf.SetFillColor(255, 255, 255)
			pdf.SetFont("Arial", "", 8)
			pdf.SetTextColor(90, 90, 90)
			pdf.CellFormat(colLabel, 6.5, "    "+sanitizePDFText(truncatePDF(row.Label, 60)), "1", 0, "L", true, 0, "")
			pdf.CellFormat(colDetails, 6.5, fmt.Sprintf("%d", row.Count), "1", 0, "R", true, 0, "")
			amount := row.Amount
			if row.Negative {
				amount = -amount
			}
			pdf.CellFormat(colAmount, 6.5, fmt.Sprintf("%.2f", amount), "1", 1, "R", true, 0, "")
		default:
			bold := row.Kind == "subtotal" || row.Kind == "total"
			if bold {
				pdf.SetFillColor(243, 244, 246)
				pdf.SetFont("Arial", "B", 9)
			} else {
				pdf.SetFillColor(255, 255, 255)
				pdf.SetFont("Arial", "", 9)
			}
			pdf.SetTextColor(40, 40, 40)
			pdf.CellFormat(colLabel, 7, sanitizePDFText(row.Label), "1", 0, "L", true, 0, "")
			pdf.CellFormat(colDetails, 7, sanitizePDFText(row.detailsText()), "1", 0, "R", true, 0, "")

			amount := row.Amount
			if row.Negative {
				amount = -amount
			}
			if row.Kind == "total" {
				if amount < 0 {
					pdf.SetTextColor(153, 27, 27)
				} else {
					pdf.SetTextColor(22, 101, 52)
				}
			}
			pdf.CellFormat(colAmount, 7, fmt.Sprintf("%.2f", amount), "1", 1, "R", true, 0, "")
		}
	}

	// Per-expense breakdown, same rows as the daily report table.
	if len(report.ExpenseLines) > 0 {
		pdf.Ln(8)
		pdf.SetFont("Arial", "B", 11)
		pdf.SetTextColor(37, 99, 235)
		pdf.CellFormat(0, 7, "EXPENSES", "", 1, "L", false, 0, "")
		pdf.SetFont("Arial", "", 8)
		pdf.SetTextColor(100, 100, 100)
		pdf.CellFormat(0, 5, "Each expense item recorded in this period.", "", 1, "L", false, 0, "")
		pdf.Ln(2)

		colNum := usable * 0.16
		colDate := usable * 0.14
		colCategory := usable * 0.2
		colItem := usable * 0.34
		colAmount2 := usable * 0.16

		pdf.SetFillColor(254, 243, 199)
		pdf.SetDrawColor(253, 230, 138)
		pdf.SetFont("Arial", "B", 8)
		pdf.SetTextColor(120, 53, 15)
		pdf.CellFormat(colNum, 8, "No.", "1", 0, "L", true, 0, "")
		pdf.CellFormat(colDate, 8, "Date", "1", 0, "L", true, 0, "")
		pdf.CellFormat(colCategory, 8, "Category", "1", 0, "L", true, 0, "")
		pdf.CellFormat(colItem, 8, "Item", "1", 0, "L", true, 0, "")
		pdf.CellFormat(colAmount2, 8, "Amount (INR)", "1", 1, "R", true, 0, "")

		for _, line := range report.ExpenseLines {
			pdf.SetFillColor(254, 249, 235)
			pdf.SetFont("Arial", "", 8)
			pdf.SetTextColor(40, 40, 40)
			itemDesc := line.ItemDescription
			if itemDesc == "" {
				itemDesc = line.Description
			}
			pdf.CellFormat(colNum, 6.5, sanitizePDFText(line.ExpenseNumber), "1", 0, "L", true, 0, "")
			pdf.CellFormat(colDate, 6.5, line.Date, "1", 0, "L", true, 0, "")
			pdf.CellFormat(colCategory, 6.5, sanitizePDFText(truncatePDF(line.Category, 24)), "1", 0, "L", true, 0, "")
			pdf.CellFormat(colItem, 6.5, sanitizePDFText(truncatePDF(itemDesc, 48)), "1", 0, "L", true, 0, "")
			pdf.SetFont("Arial", "B", 9)
			pdf.SetTextColor(154, 52, 18)
			pdf.CellFormat(colAmount2, 6.5, fmt.Sprintf("%.2f", line.Amount), "1", 1, "R", true, 0, "")
		}

		pdf.SetFillColor(254, 243, 199)
		pdf.SetFont("Arial", "B", 9)
		pdf.SetTextColor(120, 53, 15)
		pdf.CellFormat(colNum+colDate+colCategory+colItem, 7, "Total", "1", 0, "L", true, 0, "")
		pdf.CellFormat(colAmount2, 7, fmt.Sprintf("%.2f", report.Expenses.TotalAmount), "1", 1, "R", true, 0, "")
	}

	pdf.Ln(8)
	pdf.SetFont("Arial", "I", 8)
	pdf.SetTextColor(140, 140, 140)
	pdf.MultiCell(0, 4.5, sanitizePDFText("Gross profit = net sales - net purchases + closing stock - opening stock. Net profit = gross profit + other income - expenses. Stock values use weighted average cost from the stock ledger. Generated from TruERP."), "", "L", false)

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func ExportProfitLossReportPDF(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	period, anchor, startDate, endDate := parseProfitLossParams(c)

	report, err := loadProfitLossReport(userID, period, anchor, startDate, endDate)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	pdfBytes, err := buildProfitLossReportPDF(report)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate PDF"})
		return
	}

	filename := fmt.Sprintf("profit_loss_%s_%s_%s.pdf", report.Period, report.StartDate, report.EndDate)
	c.Header("Content-Type", "application/pdf")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	c.Data(http.StatusOK, "application/pdf", pdfBytes)
}
