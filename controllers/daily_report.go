package controllers

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"net/http"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/go-pdf/fpdf"
	"github.com/google/uuid"
)

// computeProductProfitForRange sums (taxable sale − purchase cost) per line item in [start, end],
// minus the same margin reversed on sales returns and credit notes.
func computeProductProfitForRange(userID uuid.UUID, startDate, endDate string) float64 {
	var salesProfit float64
	salesQuery := `
		SELECT COALESCE(SUM(
			(ii.quantity * ii.unit_price * (1.0 - ii.discount / 100.0))
			- (ii.quantity * COALESCE(p.purchase_price, 0))
		), 0) AS profit
		FROM invoice_items ii
		INNER JOIN invoices i ON i.id = ii.invoice_id
		LEFT JOIN products p ON p.id = ii.product_id
		WHERE i.user_id = ? AND DATE(i.date) >= ? AND DATE(i.date) <= ?
			AND i.status != 'cancelled' AND i.deleted_at IS NULL
	`
	utils.DB.Raw(salesQuery, userID, startDate, endDate).Scan(&salesProfit)

	var returnsProfit float64
	returnsQuery := `
		SELECT COALESCE(SUM(
			(sri.quantity * sri.unit_price)
			- (sri.quantity * COALESCE(p.purchase_price, 0))
		), 0) AS profit
		FROM sales_return_items sri
		INNER JOIN sales_returns sr ON sr.id = sri.return_id
		LEFT JOIN products p ON p.id = sri.product_id
		WHERE sr.user_id = ? AND DATE(sr.date) >= ? AND DATE(sr.date) <= ?
			AND sr.status != 'cancelled' AND sr.deleted_at IS NULL
	`
	utils.DB.Raw(returnsQuery, userID, startDate, endDate).Scan(&returnsProfit)

	var creditNoteProfit float64
	creditQuery := `
		SELECT COALESCE(SUM(
			(cni.quantity * cni.unit_price)
			- (cni.quantity * COALESCE(p.purchase_price, 0))
		), 0) AS profit
		FROM credit_note_items cni
		INNER JOIN credit_notes cn ON cn.id = cni.credit_note_id
		LEFT JOIN invoice_items ii ON ii.id = cni.invoice_item_id
		LEFT JOIN products p ON p.id = ii.product_id
		WHERE cn.user_id = ? AND DATE(cn.date) >= ? AND DATE(cn.date) <= ?
			AND cn.status != 'cancelled' AND cn.deleted_at IS NULL
	`
	utils.DB.Raw(creditQuery, userID, startDate, endDate).Scan(&creditNoteProfit)

	return salesProfit - returnsProfit - creditNoteProfit
}

func computeDailyProductProfit(userID uuid.UUID, date string) float64 {
	return computeProductProfitForRange(userID, date, date)
}

func parseReportDate(c *gin.Context) (string, bool) {
	date := c.DefaultQuery("date", time.Now().Format("2006-01-02"))
	if _, err := time.Parse("2006-01-02", date); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid date format. Use YYYY-MM-DD"})
		return "", false
	}
	return date, true
}

func parseISODate(value, field string) (time.Time, error) {
	t, err := time.Parse("2006-01-02", value)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid %s format. Use YYYY-MM-DD", field)
	}
	return t, nil
}

// resolvePeriodRange maps period + optional anchor/custom dates to an inclusive [start, end] range.
func resolvePeriodRange(period, anchorDate, startDate, endDate string) (start, end, label string, err error) {
	period = strings.ToLower(strings.TrimSpace(period))
	if period == "" {
		period = "daily"
	}

	now := time.Now()
	anchor := now
	if anchorDate != "" {
		anchor, err = parseISODate(anchorDate, "date")
		if err != nil {
			return "", "", "", err
		}
	}

	switch period {
	case "daily":
		d := anchor.Format("2006-01-02")
		return d, d, "Daily · " + anchor.Format("02 Jan 2006"), nil
	case "weekly":
		// ISO-style week: Monday–Sunday containing the anchor date.
		weekday := int(anchor.Weekday())
		if weekday == 0 {
			weekday = 7
		}
		monday := anchor.AddDate(0, 0, -(weekday - 1))
		sunday := monday.AddDate(0, 0, 6)
		return monday.Format("2006-01-02"), sunday.Format("2006-01-02"),
			fmt.Sprintf("Weekly · %s – %s", monday.Format("02 Jan"), sunday.Format("02 Jan 2006")), nil
	case "monthly":
		first := time.Date(anchor.Year(), anchor.Month(), 1, 0, 0, 0, 0, time.UTC)
		last := first.AddDate(0, 1, -1)
		return first.Format("2006-01-02"), last.Format("2006-01-02"),
			"Monthly · " + first.Format("Jan 2006"), nil
	case "yearly":
		first := time.Date(anchor.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
		last := time.Date(anchor.Year(), 12, 31, 0, 0, 0, 0, time.UTC)
		return first.Format("2006-01-02"), last.Format("2006-01-02"),
			"Yearly · " + first.Format("2006"), nil
	case "custom":
		if startDate == "" || endDate == "" {
			return "", "", "", fmt.Errorf("start_date and end_date are required for custom period")
		}
		startT, e1 := parseISODate(startDate, "start_date")
		if e1 != nil {
			return "", "", "", e1
		}
		endT, e2 := parseISODate(endDate, "end_date")
		if e2 != nil {
			return "", "", "", e2
		}
		if endT.Before(startT) {
			return "", "", "", fmt.Errorf("end_date must be on or after start_date")
		}
		// Cap custom ranges at 3 years to avoid accidental huge scans.
		if endT.Sub(startT) > 366*3*24*time.Hour {
			return "", "", "", fmt.Errorf("custom range cannot exceed 3 years")
		}
		return startT.Format("2006-01-02"), endT.Format("2006-01-02"),
			fmt.Sprintf("Custom · %s – %s", startT.Format("02 Jan 2006"), endT.Format("02 Jan 2006")), nil
	default:
		return "", "", "", fmt.Errorf("invalid period. Use daily, weekly, monthly, yearly, or custom")
	}
}

func loadReportForRange(userID uuid.UUID, startDate, endDate string) (models.DailyReport, error) {
	report := models.DailyReport{Date: startDate}
	if startDate == endDate {
		report.Date = startDate
	} else {
		report.Date = startDate + " to " + endDate
	}

	var business models.Business
	if err := utils.DB.Where("user_id = ?", userID).First(&business).Error; err == nil {
		report.BusinessName = business.Name
	}

	aggregate := func(model interface{}, dateColumn, amountColumn, extra string, dest *models.DailyReportMetric) {
		q := utils.DB.Model(model).
			Where("user_id = ? AND DATE("+dateColumn+") >= ? AND DATE("+dateColumn+") <= ?", userID, startDate, endDate)
		if extra != "" {
			q = q.Where(extra)
		}
		q.Select("COALESCE(SUM(" + amountColumn + "), 0) as total_amount, COUNT(*) as count").Scan(dest)
	}

	aggregate(&models.Invoice{}, "date", "total_amount", "status != 'cancelled'", &report.Sales)
	aggregate(&models.PurchaseBill{}, "bill_date", "total_amount", "", &report.Purchases)
	aggregate(&models.CreditNote{}, "date", "total_amount", "status != 'cancelled'", &report.CreditNotes)
	aggregate(&models.DebitNote{}, "date", "total_amount", "status != 'cancelled'", &report.DebitNotes)
	aggregate(&models.Expense{}, "date", "amount", "", &report.Expenses)
	aggregate(&models.Payment{}, "date", "amount_received", "", &report.PaymentsIn)
	aggregate(&models.PaymentOut{}, "date", "amount_paid", "", &report.PaymentsOut)
	aggregate(&models.SalesReturn{}, "date", "amount", "status != 'cancelled'", &report.SalesReturns)
	aggregate(&models.PurchaseReturn{}, "date", "amount", "status != 'cancelled'", &report.PurchaseReturns)

	utils.DB.Model(&models.PurchaseBill{}).
		Where("user_id = ? AND DATE(bill_date) >= ? AND DATE(bill_date) <= ? AND total_amount > paid_amount", userID, startDate, endDate).
		Select("COALESCE(SUM(total_amount - paid_amount), 0) as total_amount, COUNT(*) as count").
		Scan(&report.AccountsPayable)

	utils.DB.Model(&models.PurchaseBill{}).
		Where("user_id = ?", userID).
		Select("COALESCE(SUM(CASE WHEN total_amount > paid_amount THEN total_amount - paid_amount ELSE 0 END), 0)").
		Scan(&report.AccountsPayableTotal)

	utils.DB.Model(&models.Invoice{}).
		Where("user_id = ? AND DATE(date) >= ? AND DATE(date) <= ? AND status != ?", userID, startDate, endDate, "cancelled").
		Select("COALESCE(SUM(tax_total), 0)").Scan(&report.GSTCollected)

	report.NetCashFlow = report.PaymentsIn.TotalAmount - report.PaymentsOut.TotalAmount - report.Expenses.TotalAmount

	report.DailyProfit = report.Sales.TotalAmount -
		report.CreditNotes.TotalAmount -
		report.SalesReturns.TotalAmount -
		report.Purchases.TotalAmount +
		report.PurchaseReturns.TotalAmount +
		report.DebitNotes.TotalAmount -
		report.Expenses.TotalAmount

	report.ProductProfit = computeProductProfitForRange(userID, startDate, endDate)

	return report, nil
}

func loadDailyReport(userID uuid.UUID, date string) (models.DailyReport, error) {
	return loadReportForRange(userID, date, date)
}

func loadPeriodReport(userID uuid.UUID, period, anchorDate, startDate, endDate string) (models.PeriodReport, error) {
	start, end, label, err := resolvePeriodRange(period, anchorDate, startDate, endDate)
	if err != nil {
		return models.PeriodReport{}, err
	}
	base, err := loadReportForRange(userID, start, end)
	if err != nil {
		return models.PeriodReport{}, err
	}
	return models.PeriodReport{
		DailyReport: base,
		Period:      strings.ToLower(strings.TrimSpace(period)),
		StartDate:   start,
		EndDate:     end,
		Label:       label,
	}, nil
}

func GetDailyReport(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	date, ok := parseReportDate(c)
	if !ok {
		return
	}

	report, err := loadDailyReport(userID, date)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate daily report"})
		return
	}

	c.JSON(http.StatusOK, report)
}

func GetPeriodicReport(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	period := c.DefaultQuery("period", "monthly")
	anchor := c.Query("date")
	if anchor == "" {
		anchor = time.Now().Format("2006-01-02")
	}
	startDate := c.Query("start_date")
	endDate := c.Query("end_date")

	report, err := loadPeriodReport(userID, period, anchor, startDate, endDate)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, report)
}

func writePeriodReportCSV(c *gin.Context, report models.PeriodReport, filename string) {
	c.Header("Content-Type", "text/csv")
	c.Header("Content-Disposition", "attachment; filename="+filename)

	writer := csv.NewWriter(c.Writer)
	defer writer.Flush()

	_ = writer.Write([]string{"Periodic Business Report"})
	_ = writer.Write([]string{"Business", report.BusinessName})
	_ = writer.Write([]string{"Period", report.Label})
	_ = writer.Write([]string{"Start Date", report.StartDate})
	_ = writer.Write([]string{"End Date", report.EndDate})
	_ = writer.Write([]string{""})
	_ = writer.Write([]string{"Section", "Count", "Amount (INR)"})

	rows := []struct {
		label string
		m     models.DailyReportMetric
	}{
		{"Sales (Invoices)", report.Sales},
		{"Purchase Expense", report.Purchases},
		{"Payment Out", report.PaymentsOut},
		{"Accounts Payable (period)", report.AccountsPayable},
		{"Credit Notes", report.CreditNotes},
		{"Debit Notes", report.DebitNotes},
		{"Expenses", report.Expenses},
		{"Payments Received", report.PaymentsIn},
		{"Sales Returns", report.SalesReturns},
		{"Purchase Returns", report.PurchaseReturns},
	}

	for _, row := range rows {
		_ = writer.Write([]string{
			row.label,
			fmt.Sprintf("%d", row.m.Count),
			fmt.Sprintf("%.2f", row.m.TotalAmount),
		})
	}

	_ = writer.Write([]string{""})
	_ = writer.Write([]string{"Accounts Payable (total outstanding)", "", fmt.Sprintf("%.2f", report.AccountsPayableTotal)})
	_ = writer.Write([]string{"GST Collected (Sales)", "", fmt.Sprintf("%.2f", report.GSTCollected)})
	_ = writer.Write([]string{"Period Profit (Sales − Purchases − Expenses ± returns/notes)", "", fmt.Sprintf("%.2f", report.DailyProfit)})
	_ = writer.Write([]string{"Product Profit (sale value − purchase cost on items sold)", "", fmt.Sprintf("%.2f", report.ProductProfit)})
	_ = writer.Write([]string{"Net Cash Flow (In − Out − Expenses)", "", fmt.Sprintf("%.2f", report.NetCashFlow)})
}

func ExportDailyReportCSV(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	date, ok := parseReportDate(c)
	if !ok {
		return
	}

	report, err := loadDailyReport(userID, date)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate daily report"})
		return
	}

	period := models.PeriodReport{
		DailyReport: report,
		Period:      "daily",
		StartDate:   date,
		EndDate:     date,
		Label:       "Daily · " + date,
	}
	writePeriodReportCSV(c, period, fmt.Sprintf("daily_report_%s.csv", date))
}

func ExportPeriodicReportCSV(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	period := c.DefaultQuery("period", "monthly")
	anchor := c.Query("date")
	if anchor == "" {
		anchor = time.Now().Format("2006-01-02")
	}
	report, err := loadPeriodReport(userID, period, anchor, c.Query("start_date"), c.Query("end_date"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	filename := fmt.Sprintf("periodic_report_%s_%s_%s.csv", report.Period, report.StartDate, report.EndDate)
	writePeriodReportCSV(c, report, filename)
}

func ExportDailyReportPDF(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	date, ok := parseReportDate(c)
	if !ok {
		return
	}

	report, err := loadDailyReport(userID, date)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate daily report"})
		return
	}

	pdfBytes, err := buildDailyReportPDF(report)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate PDF"})
		return
	}

	filename := fmt.Sprintf("daily_report_%s.pdf", date)
	c.Header("Content-Type", "application/pdf")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	c.Data(http.StatusOK, "application/pdf", pdfBytes)
}

func ExportPeriodicReportPDF(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	period := c.DefaultQuery("period", "monthly")
	anchor := c.Query("date")
	if anchor == "" {
		anchor = time.Now().Format("2006-01-02")
	}
	report, err := loadPeriodReport(userID, period, anchor, c.Query("start_date"), c.Query("end_date"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	pdfBytes, err := buildPeriodReportPDF(report)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate PDF"})
		return
	}

	filename := fmt.Sprintf("periodic_report_%s_%s_%s.pdf", report.Period, report.StartDate, report.EndDate)
	c.Header("Content-Type", "application/pdf")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	c.Data(http.StatusOK, "application/pdf", pdfBytes)
}

func buildDailyReportPDF(report models.DailyReport) ([]byte, error) {
	return buildPeriodReportPDF(models.PeriodReport{
		DailyReport: report,
		Period:      "daily",
		StartDate:   report.Date,
		EndDate:     report.Date,
		Label:       "Daily · " + report.Date,
	})
}

func buildPeriodReportPDF(report models.PeriodReport) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(14, 16, 14)
	pdf.SetAutoPageBreak(true, 16)
	pdf.AddPage()

	title := "PERIODIC BUSINESS REPORT"
	if report.Period == "daily" || (report.StartDate != "" && report.StartDate == report.EndDate) {
		title = "DAILY BUSINESS REPORT"
	}

	pdf.SetFont("Arial", "B", 18)
	pdf.SetTextColor(37, 99, 235)
	pdf.CellFormat(0, 10, title, "", 1, "L", false, 0, "")

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
	cardW := (usable - 12) / 5
	cardH := 22.0
	startY := pdf.GetY()

	purchaseExpense := report.Purchases.TotalAmount + report.Expenses.TotalAmount

	type summaryCard struct {
		label   string
		value   string
		sub     string
		r, g, b int
	}
	profitR, profitG, profitB := 22, 101, 52
	if report.DailyProfit < 0 {
		profitR, profitG, profitB = 153, 27, 27
	}
	productR, productG, productB := 22, 101, 52
	if report.ProductProfit < 0 {
		productR, productG, productB = 153, 27, 27
	}

	cards := []summaryCard{
		{"Sales", fmt.Sprintf("Rs. %.2f", report.Sales.TotalAmount), fmt.Sprintf("%d invoices", report.Sales.Count), 22, 101, 52},
		{"Purchase expense", fmt.Sprintf("Rs. %.2f", purchaseExpense), fmt.Sprintf("Purchases %d · Ops expenses %d · AP %.2f", report.Purchases.Count, report.Expenses.Count, report.AccountsPayable.TotalAmount), 154, 52, 18},
		{"Period profit", fmt.Sprintf("Rs. %.2f", report.DailyProfit), "Sales - purchases - expenses +/- returns", profitR, profitG, profitB},
		{"Product profit", fmt.Sprintf("Rs. %.2f", report.ProductProfit), "Sale value - purchase cost on items", productR, productG, productB},
		{"Net cash flow", fmt.Sprintf("Rs. %.2f", report.NetCashFlow), "Payments in - out - expenses", 30, 64, 175},
	}
	if report.NetCashFlow < 0 {
		cards[4].r, cards[4].g, cards[4].b = 153, 27, 27
	}

	for i, card := range cards {
		x := leftM + float64(i)*(cardW+3)
		pdf.SetXY(x, startY)
		pdf.SetDrawColor(220, 220, 220)
		pdf.SetFillColor(248, 250, 252)
		pdf.Rect(x, startY, cardW, cardH, "DF")
		pdf.SetXY(x+3, startY+3)
		pdf.SetFont("Arial", "", 8)
		pdf.SetTextColor(card.r, card.g, card.b)
		pdf.CellFormat(cardW-6, 4, card.label, "", 1, "L", false, 0, "")
		pdf.SetX(x + 3)
		pdf.SetFont("Arial", "B", 11)
		pdf.CellFormat(cardW-6, 6, card.value, "", 1, "L", false, 0, "")
		pdf.SetX(x + 3)
		pdf.SetFont("Arial", "", 7)
		pdf.SetTextColor(100, 100, 100)
		pdf.CellFormat(cardW-6, 4, sanitizePDFText(card.sub), "", 1, "L", false, 0, "")
	}
	pdf.SetY(startY + cardH + 8)

	colSection := usable * 0.5
	colCount := usable * 0.2
	colAmount := usable * 0.3

	pdf.SetFillColor(243, 244, 246)
	pdf.SetDrawColor(200, 200, 200)
	pdf.SetFont("Arial", "B", 9)
	pdf.SetTextColor(40, 40, 40)
	pdf.CellFormat(colSection, 8, "Section", "1", 0, "L", true, 0, "")
	pdf.CellFormat(colCount, 8, "Transactions", "1", 0, "R", true, 0, "")
	pdf.CellFormat(colAmount, 8, "Amount (INR)", "1", 1, "R", true, 0, "")

	rows := []struct {
		label string
		m     models.DailyReportMetric
	}{
		{"Sales (Invoices)", report.Sales},
		{"Purchase Expense", report.Purchases},
		{"Payment Out", report.PaymentsOut},
		{"Accounts Payable (period)", report.AccountsPayable},
		{"Credit Notes", report.CreditNotes},
		{"Debit Notes", report.DebitNotes},
		{"Expenses", report.Expenses},
		{"Payments Received", report.PaymentsIn},
		{"Sales Returns", report.SalesReturns},
		{"Purchase Returns", report.PurchaseReturns},
	}

	pdf.SetFont("Arial", "", 9)
	pdf.SetTextColor(50, 50, 50)
	for _, row := range rows {
		pdf.CellFormat(colSection, 7, row.label, "1", 0, "L", false, 0, "")
		pdf.CellFormat(colCount, 7, fmt.Sprintf("%d", row.m.Count), "1", 0, "R", false, 0, "")
		pdf.CellFormat(colAmount, 7, fmt.Sprintf("%.2f", row.m.TotalAmount), "1", 1, "R", false, 0, "")
	}

	pdf.SetFillColor(243, 244, 246)
	pdf.SetFont("Arial", "B", 9)
	pdf.CellFormat(colSection, 7, "Accounts Payable (total outstanding)", "1", 0, "L", true, 0, "")
	pdf.CellFormat(colCount, 7, "-", "1", 0, "R", true, 0, "")
	pdf.CellFormat(colAmount, 7, fmt.Sprintf("%.2f", report.AccountsPayableTotal), "1", 1, "R", true, 0, "")

	pdf.CellFormat(colSection, 7, "GST collected (sales)", "1", 0, "L", true, 0, "")
	pdf.CellFormat(colCount, 7, "-", "1", 0, "R", true, 0, "")
	pdf.CellFormat(colAmount, 7, fmt.Sprintf("%.2f", report.GSTCollected), "1", 1, "R", true, 0, "")

	pdf.CellFormat(colSection, 7, "Period profit (Sales - Purchases - Expenses)", "1", 0, "L", true, 0, "")
	pdf.CellFormat(colCount, 7, "-", "1", 0, "R", true, 0, "")
	pdf.CellFormat(colAmount, 7, fmt.Sprintf("%.2f", report.DailyProfit), "1", 1, "R", true, 0, "")

	productProfitR, productProfitG, productProfitB := 22, 101, 52
	if report.ProductProfit < 0 {
		productProfitR, productProfitG, productProfitB = 153, 27, 27
	}
	pdf.SetTextColor(productProfitR, productProfitG, productProfitB)
	pdf.CellFormat(colSection, 7, "Product profit (sale - purchase cost)", "1", 0, "L", true, 0, "")
	pdf.CellFormat(colCount, 7, "-", "1", 0, "R", true, 0, "")
	pdf.CellFormat(colAmount, 7, fmt.Sprintf("%.2f", report.ProductProfit), "1", 1, "R", true, 0, "")
	pdf.SetTextColor(40, 40, 40)

	pdf.CellFormat(colSection, 7, "Net cash flow (In - Out - Expenses)", "1", 0, "L", true, 0, "")
	pdf.CellFormat(colCount, 7, "-", "1", 0, "R", true, 0, "")
	pdf.CellFormat(colAmount, 7, fmt.Sprintf("%.2f", report.NetCashFlow), "1", 1, "R", true, 0, "")

	pdf.Ln(8)
	pdf.SetFont("Arial", "I", 8)
	pdf.SetTextColor(140, 140, 140)
	pdf.CellFormat(0, 5, "Purchase expense = full bill total; Payment out = paid; AP = unpaid. Generated from TruERP.", "", 1, "L", false, 0, "")

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
