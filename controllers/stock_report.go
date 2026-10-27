package controllers

import (
	"bytes"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/go-pdf/fpdf"
	"github.com/google/uuid"
	"github.com/tealeg/xlsx/v3"
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

func ExportStockReportExcel(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	period, anchor, startDate, endDate := parseProfitLossParams(c)

	report, err := loadStockReport(userID, period, anchor, startDate, endDate)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	file := xlsx.NewFile()
	sheet, err := file.AddSheet("Stock Report")
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
	writeNum := func(row *xlsx.Row, v float64) *xlsx.Cell {
		cell := row.AddCell()
		cell.SetFloat(v)
		cell.SetFormat("#,##0.00")
		return cell
	}

	writeRow("Stock Report")
	if report.BusinessName != "" {
		writeRow("Business", report.BusinessName)
	}
	writeRow("Period", report.Label)
	writeRow("Range", report.StartDate+" to "+report.EndDate)
	writeRow("")

	for _, s := range []struct {
		label    string
		qty      float64
		value    float64
		hasValue bool
	}{
		{"Opening stock", report.OpeningStockQty, report.OpeningStock, true},
		{"Stock in", report.InQty, 0, false},
		{"Stock out", report.OutQty, 0, false},
		{"Closing stock", report.ClosingStockQty, report.ClosingStock, true},
		{"Net change", report.StockChangeQty, report.StockChange, true},
	} {
		row := sheet.AddRow()
		row.AddCell().SetValue(s.label)
		writeNum(row, s.qty)
		if s.hasValue {
			writeNum(row, s.value)
		}
	}
	writeRow("")

	header := sheet.AddRow()
	for _, h := range []string{"Product", "SKU", "Category", "Unit", "Opening Qty", "Opening Value (INR)", "In", "Out", "Closing Qty", "Closing Value (INR)", "Change Qty", "Change (INR)"} {
		cell := header.AddCell()
		cell.SetValue(h)
		cell.GetStyle().Font.Bold = true
	}

	for _, line := range report.Lines {
		row := sheet.AddRow()
		row.AddCell().SetValue(line.ProductName)
		row.AddCell().SetValue(line.SKU)
		row.AddCell().SetValue(line.Category)
		row.AddCell().SetValue(line.Unit)
		for _, v := range []float64{line.OpeningQty, line.OpeningValue, line.InQty, line.OutQty, line.ClosingQty, line.ClosingValue, line.ChangeQty, line.ChangeValue} {
			writeNum(row, v)
		}
	}

	totalRow := sheet.AddRow()
	totalLabel := totalRow.AddCell()
	totalLabel.SetValue(fmt.Sprintf("Total (%d products)", len(report.Lines)))
	totalLabel.GetStyle().Font.Bold = true
	totalRow.AddCell()
	totalRow.AddCell()
	totalRow.AddCell()
	for _, v := range []float64{report.OpeningStockQty, report.OpeningStock, report.InQty, report.OutQty, report.ClosingStockQty, report.ClosingStock, report.StockChangeQty, report.StockChange} {
		writeNum(totalRow, v).GetStyle().Font.Bold = true
	}

	writeRow("")
	writeRow("Note", "Opening stock is valued at the close of the day before the period starts; closing stock at the period end.")
	writeRow("Note", "Quantities and values are replayed from approved stock ledger entries at weighted average cost.")

	filename := fmt.Sprintf("stock_report_%s_%s_%s.xlsx", report.Period, report.StartDate, report.EndDate)
	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	if err := file.Write(c.Writer); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to write Excel file"})
		return
	}
}

func buildStockReportPDF(report models.StockReport) ([]byte, error) {
	pdf := fpdf.New("L", "mm", "A4", "")
	pdf.SetMargins(12, 16, 12)
	pdf.SetAutoPageBreak(true, 16)
	pdf.AddPage()

	pdf.SetFont("Arial", "B", 18)
	pdf.SetTextColor(37, 99, 235)
	pdf.CellFormat(0, 10, "STOCK REPORT", "", 1, "L", false, 0, "")

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
	pdf.CellFormat(0, 6, fmt.Sprintf(
		"Opening: %.2f units (%.2f)  |  In: %.2f  |  Out: %.2f  |  Closing: %.2f units (%.2f)  |  Net change: %.2f",
		report.OpeningStockQty, report.OpeningStock, report.InQty, report.OutQty,
		report.ClosingStockQty, report.ClosingStock, report.StockChange,
	), "", 1, "L", false, 0, "")
	pdf.Ln(4)

	pageW, _ := pdf.GetPageSize()
	leftM, _, rightM, _ := pdf.GetMargins()
	usable := pageW - leftM - rightM
	colProduct := usable * 0.22
	colSKU := usable * 0.10
	colOpenQty := usable * 0.09
	colOpenVal := usable * 0.11
	colIn := usable * 0.08
	colOut := usable * 0.08
	colCloseQty := usable * 0.09
	colCloseVal := usable * 0.11
	colChange := usable * 0.12

	pdf.SetFillColor(243, 244, 246)
	pdf.SetDrawColor(200, 200, 200)
	pdf.SetFont("Arial", "B", 8)
	pdf.SetTextColor(40, 40, 40)
	pdf.CellFormat(colProduct, 8, "Product", "1", 0, "L", true, 0, "")
	pdf.CellFormat(colSKU, 8, "SKU", "1", 0, "L", true, 0, "")
	pdf.CellFormat(colOpenQty, 8, "Open Qty", "1", 0, "R", true, 0, "")
	pdf.CellFormat(colOpenVal, 8, "Open Value", "1", 0, "R", true, 0, "")
	pdf.CellFormat(colIn, 8, "In", "1", 0, "R", true, 0, "")
	pdf.CellFormat(colOut, 8, "Out", "1", 0, "R", true, 0, "")
	pdf.CellFormat(colCloseQty, 8, "Close Qty", "1", 0, "R", true, 0, "")
	pdf.CellFormat(colCloseVal, 8, "Close Value", "1", 0, "R", true, 0, "")
	pdf.CellFormat(colChange, 8, "Change", "1", 1, "R", true, 0, "")

	for _, line := range report.Lines {
		pdf.SetFillColor(255, 255, 255)
		pdf.SetFont("Arial", "", 8)
		pdf.SetTextColor(40, 40, 40)
		pdf.CellFormat(colProduct, 6.5, sanitizePDFText(truncatePDF(line.ProductName, 40)), "1", 0, "L", true, 0, "")
		pdf.CellFormat(colSKU, 6.5, sanitizePDFText(truncatePDF(line.SKU, 14)), "1", 0, "L", true, 0, "")
		pdf.CellFormat(colOpenQty, 6.5, fmt.Sprintf("%.2f", line.OpeningQty), "1", 0, "R", true, 0, "")
		pdf.CellFormat(colOpenVal, 6.5, fmt.Sprintf("%.2f", line.OpeningValue), "1", 0, "R", true, 0, "")
		pdf.CellFormat(colIn, 6.5, fmt.Sprintf("%.2f", line.InQty), "1", 0, "R", true, 0, "")
		pdf.CellFormat(colOut, 6.5, fmt.Sprintf("%.2f", line.OutQty), "1", 0, "R", true, 0, "")
		pdf.CellFormat(colCloseQty, 6.5, fmt.Sprintf("%.2f", line.ClosingQty), "1", 0, "R", true, 0, "")
		pdf.CellFormat(colCloseVal, 6.5, fmt.Sprintf("%.2f", line.ClosingValue), "1", 0, "R", true, 0, "")
		if line.ChangeValue < 0 {
			pdf.SetTextColor(153, 27, 27)
		} else {
			pdf.SetTextColor(22, 101, 52)
		}
		pdf.CellFormat(colChange, 6.5, fmt.Sprintf("%.2f", line.ChangeValue), "1", 1, "R", true, 0, "")
	}

	pdf.SetFillColor(243, 244, 246)
	pdf.SetFont("Arial", "B", 8)
	pdf.SetTextColor(40, 40, 40)
	pdf.CellFormat(colProduct+colSKU, 7, fmt.Sprintf("Total (%d products)", len(report.Lines)), "1", 0, "L", true, 0, "")
	pdf.CellFormat(colOpenQty, 7, fmt.Sprintf("%.2f", report.OpeningStockQty), "1", 0, "R", true, 0, "")
	pdf.CellFormat(colOpenVal, 7, fmt.Sprintf("%.2f", report.OpeningStock), "1", 0, "R", true, 0, "")
	pdf.CellFormat(colIn, 7, fmt.Sprintf("%.2f", report.InQty), "1", 0, "R", true, 0, "")
	pdf.CellFormat(colOut, 7, fmt.Sprintf("%.2f", report.OutQty), "1", 0, "R", true, 0, "")
	pdf.CellFormat(colCloseQty, 7, fmt.Sprintf("%.2f", report.ClosingStockQty), "1", 0, "R", true, 0, "")
	pdf.CellFormat(colCloseVal, 7, fmt.Sprintf("%.2f", report.ClosingStock), "1", 0, "R", true, 0, "")
	if report.StockChange < 0 {
		pdf.SetTextColor(153, 27, 27)
	} else {
		pdf.SetTextColor(22, 101, 52)
	}
	pdf.CellFormat(colChange, 7, fmt.Sprintf("%.2f", report.StockChange), "1", 1, "R", true, 0, "")
	pdf.SetTextColor(40, 40, 40)

	pdf.Ln(8)
	pdf.SetFont("Arial", "I", 8)
	pdf.SetTextColor(140, 140, 140)
	pdf.MultiCell(0, 4.5, sanitizePDFText("Opening stock is valued at the close of the day before the period starts; closing stock at the period end. Quantities and values are replayed from approved stock ledger entries at weighted average cost (purchase, opening and adjustment entries); products without costed inflows fall back to the product purchase price. Generated from TruERP."), "", "L", false)

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func ExportStockReportPDF(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	period, anchor, startDate, endDate := parseProfitLossParams(c)

	report, err := loadStockReport(userID, period, anchor, startDate, endDate)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	pdfBytes, err := buildStockReportPDF(report)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate PDF"})
		return
	}

	filename := fmt.Sprintf("stock_report_%s_%s_%s.pdf", report.Period, report.StartDate, report.EndDate)
	c.Header("Content-Type", "application/pdf")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	c.Data(http.StatusOK, "application/pdf", pdfBytes)
}
