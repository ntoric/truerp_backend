package controllers

import (
	"bytes"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/go-pdf/fpdf"
	"github.com/google/uuid"
	"github.com/tealeg/xlsx/v3"
)

// billwiseReturns is the sale value and purchase cost reversed against one
// invoice by linked sales returns and credit notes (any date).
type billwiseReturns struct {
	SaleValue float64
	Cost      float64
}

// loadBillwiseReturns maps invoice IDs to the sale value and item cost
// reversed by non-cancelled sales returns and credit notes linked to them.
func loadBillwiseReturns(userID uuid.UUID, invoiceIDs []uuid.UUID) map[uuid.UUID]billwiseReturns {
	result := make(map[uuid.UUID]billwiseReturns, len(invoiceIDs))
	if len(invoiceIDs) == 0 {
		return result
	}

	type row struct {
		InvoiceID uuid.UUID
		SaleValue float64
		Cost      float64
	}
	merge := func(rows []row) {
		for _, r := range rows {
			cur := result[r.InvoiceID]
			cur.SaleValue += r.SaleValue
			cur.Cost += r.Cost
			result[r.InvoiceID] = cur
		}
	}

	var returnRows []row
	utils.DB.Raw(`
		SELECT sr.invoice_id,
			COALESCE(SUM(sri.quantity * sri.unit_price), 0) AS sale_value,
			COALESCE(SUM(sri.quantity * COALESCE(p.purchase_price, 0)), 0) AS cost
		FROM sales_return_items sri
		INNER JOIN sales_returns sr ON sr.id = sri.return_id
		LEFT JOIN products p ON p.id = sri.product_id
		WHERE sr.user_id = ? AND sr.invoice_id IN ? AND sr.status != 'cancelled' AND sr.deleted_at IS NULL
		GROUP BY sr.invoice_id`, userID, invoiceIDs).Scan(&returnRows)
	merge(returnRows)

	var creditRows []row
	utils.DB.Raw(`
		SELECT cn.invoice_id,
			COALESCE(SUM(cni.quantity * cni.unit_price), 0) AS sale_value,
			COALESCE(SUM(cni.quantity * COALESCE(p.purchase_price, 0)), 0) AS cost
		FROM credit_note_items cni
		INNER JOIN credit_notes cn ON cn.id = cni.credit_note_id
		LEFT JOIN invoice_items ii ON ii.id = cni.invoice_item_id
		LEFT JOIN products p ON p.id = ii.product_id
		WHERE cn.user_id = ? AND cn.invoice_id IN ? AND cn.status != 'cancelled' AND cn.deleted_at IS NULL
		GROUP BY cn.invoice_id`, userID, invoiceIDs).Scan(&creditRows)
	merge(creditRows)

	return result
}

func sortBillwiseBills(bills []models.BillwiseProfitBill, sortKey string) {
	switch sortKey {
	case "oldest":
		sort.SliceStable(bills, func(i, j int) bool {
			if bills[i].Date != bills[j].Date {
				return bills[i].Date < bills[j].Date
			}
			return bills[i].InvoiceNumber < bills[j].InvoiceNumber
		})
	case "profit_asc":
		sort.SliceStable(bills, func(i, j int) bool { return bills[i].Profit < bills[j].Profit })
	case "profit_desc":
		sort.SliceStable(bills, func(i, j int) bool { return bills[i].Profit > bills[j].Profit })
	case "sale_desc":
		sort.SliceStable(bills, func(i, j int) bool { return bills[i].InvoiceTotal > bills[j].InvoiceTotal })
	default: // "newest"
		sort.SliceStable(bills, func(i, j int) bool {
			if bills[i].Date != bills[j].Date {
				return bills[i].Date > bills[j].Date
			}
			return bills[i].InvoiceNumber > bills[j].InvoiceNumber
		})
	}
}

// loadBillwiseProfitReport builds the per-invoice profit report over an
// inclusive [start, end] range resolved from period/anchor/custom params.
// Per bill, profit = taxable sale value − invoice-level discount − purchase
// cost, net of sales returns and credit notes raised against the bill.
// Item cost uses the product's current purchase price — the same convention
// as the daily report's product-profit figure.
func loadBillwiseProfitReport(userID uuid.UUID, period, anchorDate, startDate, endDate, partyID, search, sortKey string) (models.BillwiseProfitReport, error) {
	start, end, label, err := resolvePeriodRange(period, anchorDate, startDate, endDate)
	if err != nil {
		return models.BillwiseProfitReport{}, err
	}

	report := models.BillwiseProfitReport{
		Period:    strings.ToLower(strings.TrimSpace(period)),
		StartDate: start,
		EndDate:   end,
		Label:     label,
		Bills:     []models.BillwiseProfitBill{},
	}

	var business models.Business
	if err := utils.DB.Where("user_id = ?", userID).First(&business).Error; err == nil {
		report.BusinessName = business.Name
	}

	q := utils.DB.Where("user_id = ? AND status != ? AND DATE(date) >= ? AND DATE(date) <= ?",
		userID, "cancelled", start, end).
		Preload("Items").Preload("Party")
	if partyID != "" {
		pid, err := uuid.Parse(partyID)
		if err != nil {
			return models.BillwiseProfitReport{}, fmt.Errorf("invalid party_id")
		}
		q = q.Where("party_id = ?", pid)
	}

	var invoices []models.Invoice
	if err := q.Find(&invoices).Error; err != nil {
		return models.BillwiseProfitReport{}, err
	}

	needle := strings.ToLower(strings.TrimSpace(search))
	if needle != "" {
		filtered := invoices[:0]
		for _, inv := range invoices {
			if strings.Contains(strings.ToLower(inv.InvoiceNumber), needle) ||
				strings.Contains(strings.ToLower(inv.Party.Name), needle) {
				filtered = append(filtered, inv)
			}
		}
		invoices = filtered
	}

	invoiceIDs := make([]uuid.UUID, 0, len(invoices))
	for _, inv := range invoices {
		invoiceIDs = append(invoiceIDs, inv.ID)
	}
	returns := loadBillwiseReturns(userID, invoiceIDs)

	var products []models.Product
	utils.DB.Where("user_id = ?", userID).Select("id", "purchase_price").Find(&products)
	costByProduct := make(map[uuid.UUID]float64, len(products))
	for _, p := range products {
		costByProduct[p.ID] = p.PurchasePrice
	}

	for _, inv := range invoices {
		bill := models.BillwiseProfitBill{
			InvoiceID:     inv.ID,
			InvoiceNumber: inv.InvoiceNumber,
			Date:          inv.Date.Format("2006-01-02"),
			PartyID:       inv.PartyID,
			PartyName:     inv.Party.Name,
			Status:        inv.Status,
			InvoiceTotal:  inv.TotalAmount,
			Discount:      inv.InvoiceDiscount + inv.LoyaltyDiscount,
			Items:         []models.BillwiseProfitItem{},
		}

		for _, item := range inv.Items {
			costPrice := float64(0)
			if item.ProductID != nil {
				costPrice = costByProduct[*item.ProductID]
			}
			sale := item.Quantity * item.UnitPrice * (1.0 - item.Discount/100.0)
			cost := item.Quantity * costPrice
			bill.Items = append(bill.Items, models.BillwiseProfitItem{
				Description: item.Description,
				Quantity:    item.Quantity,
				UnitPrice:   item.UnitPrice,
				DiscountPct: item.Discount,
				CostPrice:   costPrice,
				SaleAmount:  sale,
				CostAmount:  cost,
				Profit:      sale - cost,
			})
			bill.SaleAmount += sale
			bill.CostAmount += cost
		}

		ret := returns[inv.ID]
		bill.ReturnsAmount = ret.SaleValue
		netSale := bill.SaleAmount - bill.Discount - bill.ReturnsAmount
		bill.Profit = netSale - (bill.CostAmount - ret.Cost)
		if netSale > 0 {
			bill.MarginPct = bill.Profit / netSale * 100
		}

		report.Bills = append(report.Bills, bill)
		report.SaleAmount += bill.SaleAmount
		report.Discount += bill.Discount
		report.ReturnsAmount += bill.ReturnsAmount
		report.CostAmount += bill.CostAmount
		report.Profit += bill.Profit
	}

	report.BillCount = int64(len(report.Bills))
	if netSale := report.SaleAmount - report.Discount - report.ReturnsAmount; netSale > 0 {
		report.MarginPct = report.Profit / netSale * 100
	}

	sortBillwiseBills(report.Bills, sortKey)
	return report, nil
}

type billwiseProfitParams struct {
	period    string
	anchor    string
	startDate string
	endDate   string
	partyID   string
	search    string
	sortKey   string
}

func parseBillwiseProfitParams(c *gin.Context) billwiseProfitParams {
	period, anchor, startDate, endDate := parseProfitLossParams(c)
	return billwiseProfitParams{
		period:    period,
		anchor:    anchor,
		startDate: startDate,
		endDate:   endDate,
		partyID:   c.Query("party_id"),
		search:    c.Query("search"),
		sortKey:   c.DefaultQuery("sort", "newest"),
	}
}

func GetBillwiseProfitReport(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	params := parseBillwiseProfitParams(c)

	report, err := loadBillwiseProfitReport(userID, params.period, params.anchor, params.startDate, params.endDate, params.partyID, params.search, params.sortKey)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, report)
}

func ExportBillwiseProfitReportExcel(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	params := parseBillwiseProfitParams(c)

	report, err := loadBillwiseProfitReport(userID, params.period, params.anchor, params.startDate, params.endDate, params.partyID, params.search, params.sortKey)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	file := xlsx.NewFile()
	sheet, err := file.AddSheet("Billwise Profit")
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

	writeRow("Billwise Profit Report")
	if report.BusinessName != "" {
		writeRow("Business", report.BusinessName)
	}
	writeRow("Period", report.Label)
	writeRow("Range", report.StartDate+" to "+report.EndDate)
	writeRow("")

	header := sheet.AddRow()
	for _, h := range []string{"Invoice No.", "Date", "Party", "Status", "Bill Total (INR)", "Sale Value (INR)", "Discount (INR)", "Returns (INR)", "Cost (INR)", "Profit (INR)", "Margin %"} {
		cell := header.AddCell()
		cell.SetValue(h)
		cell.GetStyle().Font.Bold = true
	}

	for _, bill := range report.Bills {
		row := sheet.AddRow()
		row.AddCell().SetValue(bill.InvoiceNumber)
		row.AddCell().SetValue(bill.Date)
		row.AddCell().SetValue(bill.PartyName)
		row.AddCell().SetValue(bill.Status)
		amounts := row.AddCell()
		amounts.SetFloat(bill.InvoiceTotal)
		amounts.SetFormat("#,##0.00")
		for _, v := range []float64{bill.SaleAmount, bill.Discount, bill.ReturnsAmount, bill.CostAmount, bill.Profit, bill.MarginPct} {
			cell := row.AddCell()
			cell.SetFloat(v)
			cell.SetFormat("#,##0.00")
		}
	}

	var totalBilled float64
	for _, bill := range report.Bills {
		totalBilled += bill.InvoiceTotal
	}
	totalRow := sheet.AddRow()
	totalLabel := totalRow.AddCell()
	totalLabel.SetValue("Total")
	totalLabel.GetStyle().Font.Bold = true
	totalRow.AddCell()
	totalRow.AddCell()
	totalRow.AddCell()
	for _, v := range []float64{totalBilled, report.SaleAmount, report.Discount, report.ReturnsAmount, report.CostAmount, report.Profit, report.MarginPct} {
		cell := totalRow.AddCell()
		cell.SetFloat(v)
		cell.SetFormat("#,##0.00")
		cell.GetStyle().Font.Bold = true
	}

	itemsSheet, err := file.AddSheet("Item Detail")
	if err == nil {
		row := itemsSheet.AddRow()
		for _, h := range []string{"Invoice No.", "Date", "Party", "Item", "Qty", "Rate (INR)", "Disc %", "Cost Price (INR)", "Sale Value (INR)", "Cost (INR)", "Profit (INR)"} {
			cell := row.AddCell()
			cell.SetValue(h)
			cell.GetStyle().Font.Bold = true
		}
		for _, bill := range report.Bills {
			for _, item := range bill.Items {
				r := itemsSheet.AddRow()
				r.AddCell().SetValue(bill.InvoiceNumber)
				r.AddCell().SetValue(bill.Date)
				r.AddCell().SetValue(bill.PartyName)
				r.AddCell().SetValue(item.Description)
				for _, v := range []float64{item.Quantity, item.UnitPrice, item.DiscountPct, item.CostPrice, item.SaleAmount, item.CostAmount, item.Profit} {
					cell := r.AddCell()
					cell.SetFloat(v)
					cell.SetFormat("#,##0.00")
				}
			}
		}
	}

	writeRow("")
	writeRow("Note", "Profit = taxable sale value - invoice-level discount - purchase cost, net of sales returns and credit notes on the bill.")
	writeRow("Note", "Item cost uses the product's current purchase price.")

	filename := fmt.Sprintf("billwise_profit_%s_%s_%s.xlsx", report.Period, report.StartDate, report.EndDate)
	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	if err := file.Write(c.Writer); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to write Excel file"})
		return
	}
}

func buildBillwiseProfitReportPDF(report models.BillwiseProfitReport) ([]byte, error) {
	pdf := fpdf.New("L", "mm", "A4", "")
	pdf.SetMargins(12, 16, 12)
	pdf.SetAutoPageBreak(true, 16)
	pdf.AddPage()

	pdf.SetFont("Arial", "B", 18)
	pdf.SetTextColor(37, 99, 235)
	pdf.CellFormat(0, 10, "BILLWISE PROFIT REPORT", "", 1, "L", false, 0, "")

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
	colInv := usable * 0.11
	colDate := usable * 0.09
	colParty := usable * 0.20
	colBill := usable * 0.10
	colSale := usable * 0.11
	colRet := usable * 0.09
	colCost := usable * 0.11
	colProfit := usable * 0.11
	colMargin := usable * 0.08

	pdf.SetFillColor(243, 244, 246)
	pdf.SetDrawColor(200, 200, 200)
	pdf.SetFont("Arial", "B", 8)
	pdf.SetTextColor(40, 40, 40)
	pdf.CellFormat(colInv, 8, "Invoice No.", "1", 0, "L", true, 0, "")
	pdf.CellFormat(colDate, 8, "Date", "1", 0, "L", true, 0, "")
	pdf.CellFormat(colParty, 8, "Party", "1", 0, "L", true, 0, "")
	pdf.CellFormat(colBill, 8, "Bill Total", "1", 0, "R", true, 0, "")
	pdf.CellFormat(colSale, 8, "Sale Value", "1", 0, "R", true, 0, "")
	pdf.CellFormat(colRet, 8, "Returns", "1", 0, "R", true, 0, "")
	pdf.CellFormat(colCost, 8, "Cost", "1", 0, "R", true, 0, "")
	pdf.CellFormat(colProfit, 8, "Profit", "1", 0, "R", true, 0, "")
	pdf.CellFormat(colMargin, 8, "Margin %", "1", 1, "R", true, 0, "")

	for _, bill := range report.Bills {
		pdf.SetFillColor(255, 255, 255)
		pdf.SetFont("Arial", "", 8)
		pdf.SetTextColor(40, 40, 40)
		pdf.CellFormat(colInv, 6.5, sanitizePDFText(truncatePDF(bill.InvoiceNumber, 20)), "1", 0, "L", true, 0, "")
		pdf.CellFormat(colDate, 6.5, bill.Date, "1", 0, "L", true, 0, "")
		pdf.CellFormat(colParty, 6.5, sanitizePDFText(truncatePDF(bill.PartyName, 34)), "1", 0, "L", true, 0, "")
		pdf.CellFormat(colBill, 6.5, fmt.Sprintf("%.2f", bill.InvoiceTotal), "1", 0, "R", true, 0, "")
		pdf.CellFormat(colSale, 6.5, fmt.Sprintf("%.2f", bill.SaleAmount), "1", 0, "R", true, 0, "")
		pdf.CellFormat(colRet, 6.5, fmt.Sprintf("%.2f", bill.ReturnsAmount), "1", 0, "R", true, 0, "")
		pdf.CellFormat(colCost, 6.5, fmt.Sprintf("%.2f", bill.CostAmount), "1", 0, "R", true, 0, "")
		if bill.Profit < 0 {
			pdf.SetTextColor(153, 27, 27)
		} else {
			pdf.SetTextColor(22, 101, 52)
		}
		pdf.CellFormat(colProfit, 6.5, fmt.Sprintf("%.2f", bill.Profit), "1", 0, "R", true, 0, "")
		pdf.SetTextColor(40, 40, 40)
		pdf.CellFormat(colMargin, 6.5, fmt.Sprintf("%.1f", bill.MarginPct), "1", 1, "R", true, 0, "")
	}

	pdf.SetFillColor(243, 244, 246)
	pdf.SetFont("Arial", "B", 8)
	pdf.SetTextColor(40, 40, 40)
	pdf.CellFormat(colInv+colDate+colParty, 7, fmt.Sprintf("Total (%d bills)", report.BillCount), "1", 0, "L", true, 0, "")
	pdf.CellFormat(colBill, 7, "", "1", 0, "R", true, 0, "")
	pdf.CellFormat(colSale, 7, fmt.Sprintf("%.2f", report.SaleAmount), "1", 0, "R", true, 0, "")
	pdf.CellFormat(colRet, 7, fmt.Sprintf("%.2f", report.ReturnsAmount), "1", 0, "R", true, 0, "")
	pdf.CellFormat(colCost, 7, fmt.Sprintf("%.2f", report.CostAmount), "1", 0, "R", true, 0, "")
	if report.Profit < 0 {
		pdf.SetTextColor(153, 27, 27)
	} else {
		pdf.SetTextColor(22, 101, 52)
	}
	pdf.CellFormat(colProfit, 7, fmt.Sprintf("%.2f", report.Profit), "1", 0, "R", true, 0, "")
	pdf.SetTextColor(40, 40, 40)
	pdf.CellFormat(colMargin, 7, fmt.Sprintf("%.1f", report.MarginPct), "1", 1, "R", true, 0, "")

	pdf.Ln(8)
	pdf.SetFont("Arial", "I", 8)
	pdf.SetTextColor(140, 140, 140)
	pdf.MultiCell(0, 4.5, sanitizePDFText("Profit = taxable sale value - invoice-level discount - purchase cost, net of sales returns and credit notes raised against the bill. Item cost uses the product's current purchase price. Cancelled documents are excluded. Generated from TruERP."), "", "L", false)

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func ExportBillwiseProfitReportPDF(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	params := parseBillwiseProfitParams(c)

	report, err := loadBillwiseProfitReport(userID, params.period, params.anchor, params.startDate, params.endDate, params.partyID, params.search, params.sortKey)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	pdfBytes, err := buildBillwiseProfitReportPDF(report)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate PDF"})
		return
	}

	filename := fmt.Sprintf("billwise_profit_%s_%s_%s.pdf", report.Period, report.StartDate, report.EndDate)
	c.Header("Content-Type", "application/pdf")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	c.Data(http.StatusOK, "application/pdf", pdfBytes)
}
