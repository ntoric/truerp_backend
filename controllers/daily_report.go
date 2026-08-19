package controllers

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"net/http"
	"sort"
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

	report.PaymentsByMethod = loadPaymentsByMethod(userID, startDate, endDate)
	report.ExpenseLines = loadExpenseLines(userID, startDate, endDate)

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

func paymentMethodLabel(method string) string {
	switch normalizePaymentMethod(method) {
	case "cash":
		return "Cash"
	case "upi":
		return "UPI"
	case "card":
		return "Card"
	case "bank_transfer":
		return "Bank Transfer"
	case "cheque", "check":
		return "Cheque"
	default:
		label := strings.ReplaceAll(normalizePaymentMethod(method), "_", " ")
		if label == "" {
			return "Cash"
		}
		return strings.ToUpper(label[:1]) + label[1:]
	}
}

func loadPaymentsByMethod(userID uuid.UUID, startDate, endDate string) []models.PaymentMethodTotal {
	type agg struct {
		Method      string
		TotalAmount float64
		Count       int64
	}

	methodExpr := `CASE
		WHEN LOWER(TRIM(COALESCE(mode, ''))) IN ('', 'cash') THEN 'cash'
		WHEN LOWER(TRIM(COALESCE(mode, ''))) IN ('cheque', 'check') THEN 'cheque'
		ELSE LOWER(TRIM(mode))
	END`
	dateWhere := utils.SQLDateGTE("date") + " AND " + utils.SQLDateLTE("date")

	var inRows, outRows []agg
	utils.DB.Raw(`
		SELECT `+methodExpr+` AS method,
			COALESCE(SUM(amount_received), 0) AS total_amount,
			COUNT(*) AS count
		FROM payments
		WHERE user_id = ? AND `+dateWhere+` AND deleted_at IS NULL
		GROUP BY `+methodExpr, userID, startDate, endDate).Scan(&inRows)
	utils.DB.Raw(`
		SELECT `+methodExpr+` AS method,
			COALESCE(SUM(amount_paid), 0) AS total_amount,
			COUNT(*) AS count
		FROM payment_outs
		WHERE user_id = ? AND `+dateWhere+` AND deleted_at IS NULL
		GROUP BY `+methodExpr, userID, startDate, endDate).Scan(&outRows)

	byMethod := make(map[string]*models.PaymentMethodTotal)
	ensure := func(method string) *models.PaymentMethodTotal {
		key := normalizePaymentMethod(method)
		if existing, ok := byMethod[key]; ok {
			return existing
		}
		row := &models.PaymentMethodTotal{
			Method: key,
			Label:  paymentMethodLabel(key),
		}
		byMethod[key] = row
		return row
	}

	for _, r := range inRows {
		row := ensure(r.Method)
		row.In.TotalAmount = r.TotalAmount
		row.In.Count = r.Count
	}
	for _, r := range outRows {
		row := ensure(r.Method)
		row.Out.TotalAmount = r.TotalAmount
		row.Out.Count = r.Count
	}

	standardOrder := make([]string, 0, len(standardPaymentMethods))
	for _, m := range standardPaymentMethods {
		standardOrder = append(standardOrder, m.Key)
	}

	result := make([]models.PaymentMethodTotal, 0, len(standardPaymentMethods)+len(byMethod))
	seen := make(map[string]bool, len(byMethod))
	for _, key := range standardOrder {
		row := ensure(key)
		result = append(result, *row)
		seen[key] = true
	}
	extra := make([]string, 0)
	for key := range byMethod {
		if !seen[key] {
			extra = append(extra, key)
		}
	}
	sort.Strings(extra)
	for _, key := range extra {
		result = append(result, *byMethod[key])
	}
	return result
}

func loadDailyReport(userID uuid.UUID, date string) (models.DailyReport, error) {
	return loadReportForRange(userID, date, date)
}

// loadExpenseLines returns one row per expense item dated in [startDate, endDate],
// newest first, for the per-expense breakdown in the daily/periodic report.
// Expenses with no items emit a single row using the expense's own description
// and amount, so simple expenses still appear in the breakdown.
func loadExpenseLines(userID uuid.UUID, startDate, endDate string) []models.ExpenseLine {
	var expenses []models.Expense
	if err := utils.DB.
		Where("user_id = ? AND DATE(date) >= ? AND DATE(date) <= ?", userID, startDate, endDate).
		Preload("Items").
		Order("date DESC, created_at DESC").
		Find(&expenses).Error; err != nil {
		return nil
	}

	lines := make([]models.ExpenseLine, 0, len(expenses))
	for _, e := range expenses {
		base := models.ExpenseLine{
			ID:            e.ID,
			ExpenseNumber: e.ExpenseNumber,
			Category:      e.Category,
			Description:   e.Description,
			Vendor:        e.Vendor,
			PaymentMode:   e.PaymentMode,
			Date:          e.Date.Format("2006-01-02"),
			WithGST:       e.WithGST,
			TaxTotal:      e.TaxTotal,
			SubTotal:      e.SubTotal,
		}
		if len(e.Items) == 0 {
			row := base
			row.ItemDescription = e.Description
			row.Quantity = 1
			row.UnitPrice = e.Amount
			row.Amount = e.Amount
			lines = append(lines, row)
			continue
		}
		for _, item := range e.Items {
			row := base
			itemID := item.ID
			row.ItemID = &itemID
			row.ItemDescription = item.Description
			row.Quantity = item.Quantity
			row.UnitPrice = item.UnitPrice
			row.Amount = item.Total
			lines = append(lines, row)
		}
	}
	return lines
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

type reportTableRow struct {
	label string
	m     models.DailyReportMetric
}

func periodReportTableRows(report models.PeriodReport) []reportTableRow {
	return []reportTableRow{
		{label: "Sales (Invoices)", m: report.Sales},
		{label: "Purchase Expense", m: report.Purchases},
		{label: "Payment Out", m: report.PaymentsOut},
		{label: "Accounts Payable (period)", m: report.AccountsPayable},
		{label: "Credit Notes", m: report.CreditNotes},
		{label: "Debit Notes", m: report.DebitNotes},
		{label: "Expenses", m: report.Expenses},
		{label: "Payments Received", m: report.PaymentsIn},
		{label: "Sales Returns", m: report.SalesReturns},
		{label: "Purchase Returns", m: report.PurchaseReturns},
	}
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

	for _, row := range periodReportTableRows(report) {
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
	_ = writer.Write([]string{""})
	_ = writer.Write([]string{"Payments by method"})
	_ = writer.Write([]string{"Method", "Received count", "Received amount (INR)", "Paid count", "Paid amount (INR)"})
	for _, method := range report.PaymentsByMethod {
		_ = writer.Write([]string{
			method.Label,
			fmt.Sprintf("%d", method.In.Count),
			fmt.Sprintf("%.2f", method.In.TotalAmount),
			fmt.Sprintf("%d", method.Out.Count),
			fmt.Sprintf("%.2f", method.Out.TotalAmount),
		})
	}
	_ = writer.Write([]string{
		"Total",
		fmt.Sprintf("%d", report.PaymentsIn.Count),
		fmt.Sprintf("%.2f", report.PaymentsIn.TotalAmount),
		fmt.Sprintf("%d", report.PaymentsOut.Count),
		fmt.Sprintf("%.2f", report.PaymentsOut.TotalAmount),
	})

	if len(report.ExpenseLines) > 0 {
		_ = writer.Write([]string{""})
		_ = writer.Write([]string{"Expenses (one row per item)"})
		_ = writer.Write([]string{"Number", "Date", "Category", "Vendor", "Payment mode", "Item", "Qty", "Unit price (INR)", "Amount (INR)"})
		for _, line := range report.ExpenseLines {
			mode := line.PaymentMode
			if mode == "" {
				mode = "-"
			}
			vendor := line.Vendor
			if vendor == "" {
				vendor = "-"
			}
			category := line.Category
			if category == "" {
				category = "-"
			}
			itemDesc := line.ItemDescription
			if itemDesc == "" {
				itemDesc = line.Description
				if itemDesc == "" {
					itemDesc = "-"
				}
			}
			_ = writer.Write([]string{
				line.ExpenseNumber,
				line.Date,
				category,
				vendor,
				mode,
				itemDesc,
				fmt.Sprintf("%g", line.Quantity),
				fmt.Sprintf("%.2f", line.UnitPrice),
				fmt.Sprintf("%.2f", line.Amount),
			})
		}
		_ = writer.Write([]string{
			"Total",
			"",
			"",
			"",
			"",
			"",
			"",
			"",
			fmt.Sprintf("%.2f", report.Expenses.TotalAmount),
		})
	}
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

func writePaymentsByMethodPDF(pdf *fpdf.Fpdf, report models.PeriodReport) {
	methods := report.PaymentsByMethod
	if len(methods) == 0 {
		return
	}

	pdf.Ln(8)
	pdf.SetFont("Arial", "B", 11)
	pdf.SetTextColor(37, 99, 235)
	pdf.CellFormat(0, 7, "PAYMENTS BY METHOD", "", 1, "L", false, 0, "")
	pdf.SetFont("Arial", "", 8)
	pdf.SetTextColor(100, 100, 100)
	pdf.CellFormat(0, 5, "Totals for each payment method. Received = money in; Paid = money out.", "", 1, "L", false, 0, "")
	pdf.Ln(2)

	pageW, _ := pdf.GetPageSize()
	leftM, _, rightM, _ := pdf.GetMargins()
	usable := pageW - leftM - rightM
	colMethod := usable * 0.28
	colInCount := usable * 0.12
	colInAmt := usable * 0.24
	colOutCount := usable * 0.12
	colOutAmt := usable * 0.24

	pdf.SetFillColor(219, 234, 254)
	pdf.SetDrawColor(147, 197, 253)
	pdf.SetFont("Arial", "B", 8)
	pdf.SetTextColor(30, 64, 175)
	pdf.CellFormat(colMethod, 8, "Method", "1", 0, "L", true, 0, "")
	pdf.CellFormat(colInCount, 8, "In txn", "1", 0, "R", true, 0, "")
	pdf.CellFormat(colInAmt, 8, "Received (INR)", "1", 0, "R", true, 0, "")
	pdf.CellFormat(colOutCount, 8, "Out txn", "1", 0, "R", true, 0, "")
	pdf.CellFormat(colOutAmt, 8, "Paid (INR)", "1", 1, "R", true, 0, "")

	for i, method := range methods {
		if i%2 == 0 {
			pdf.SetFillColor(239, 246, 255)
		} else {
			pdf.SetFillColor(255, 255, 255)
		}
		pdf.SetFont("Arial", "B", 9)
		pdf.SetTextColor(30, 30, 30)
		pdf.CellFormat(colMethod, 7, sanitizePDFText(method.Label), "1", 0, "L", true, 0, "")
		pdf.SetFont("Arial", "", 9)
		pdf.SetTextColor(22, 101, 52)
		pdf.CellFormat(colInCount, 7, fmt.Sprintf("%d", method.In.Count), "1", 0, "R", true, 0, "")
		pdf.CellFormat(colInAmt, 7, fmt.Sprintf("%.2f", method.In.TotalAmount), "1", 0, "R", true, 0, "")
		pdf.SetTextColor(154, 52, 18)
		pdf.CellFormat(colOutCount, 7, fmt.Sprintf("%d", method.Out.Count), "1", 0, "R", true, 0, "")
		pdf.CellFormat(colOutAmt, 7, fmt.Sprintf("%.2f", method.Out.TotalAmount), "1", 1, "R", true, 0, "")
	}

	pdf.SetFillColor(219, 234, 254)
	pdf.SetFont("Arial", "B", 9)
	pdf.SetTextColor(30, 64, 175)
	pdf.CellFormat(colMethod, 7, "Total", "1", 0, "L", true, 0, "")
	pdf.CellFormat(colInCount, 7, fmt.Sprintf("%d", report.PaymentsIn.Count), "1", 0, "R", true, 0, "")
	pdf.CellFormat(colInAmt, 7, fmt.Sprintf("%.2f", report.PaymentsIn.TotalAmount), "1", 0, "R", true, 0, "")
	pdf.CellFormat(colOutCount, 7, fmt.Sprintf("%d", report.PaymentsOut.Count), "1", 0, "R", true, 0, "")
	pdf.CellFormat(colOutAmt, 7, fmt.Sprintf("%.2f", report.PaymentsOut.TotalAmount), "1", 1, "R", true, 0, "")
}

// writeExpenseLinesPDF renders a per-expense-item breakdown table when the
// report contains any expenses dated in the period. Each expense item is
// shown on its own row; expenses with no items show one row.
func writeExpenseLinesPDF(pdf *fpdf.Fpdf, report models.PeriodReport) {
	lines := report.ExpenseLines
	if len(lines) == 0 {
		return
	}

	pdf.Ln(8)
	pdf.SetFont("Arial", "B", 11)
	pdf.SetTextColor(37, 99, 235)
	pdf.CellFormat(0, 7, "EXPENSES", "", 1, "L", false, 0, "")
	pdf.SetFont("Arial", "", 8)
	pdf.SetTextColor(100, 100, 100)
	pdf.CellFormat(0, 5, "Each expense item recorded in this period.", "", 1, "L", false, 0, "")
	pdf.Ln(2)

	pageW, _ := pdf.GetPageSize()
	leftM, _, rightM, _ := pdf.GetMargins()
	usable := pageW - leftM - rightM
	colNum := usable * 0.14
	colDate := usable * 0.12
	colCategory := usable * 0.18
	colItem := usable * 0.42
	colAmount := usable * 0.14

	pdf.SetFillColor(254, 243, 199)
	pdf.SetDrawColor(253, 230, 138)
	pdf.SetFont("Arial", "B", 8)
	pdf.SetTextColor(120, 53, 15)
	pdf.CellFormat(colNum, 8, "No.", "1", 0, "L", true, 0, "")
	pdf.CellFormat(colDate, 8, "Date", "1", 0, "L", true, 0, "")
	pdf.CellFormat(colCategory, 8, "Category", "1", 0, "L", true, 0, "")
	pdf.CellFormat(colItem, 8, "Item", "1", 0, "L", true, 0, "")
	pdf.CellFormat(colAmount, 8, "Amount (INR)", "1", 1, "R", true, 0, "")

	// Group rows by expense so the expense number/category/vendor are shown
	// once as a header, then each item beneath it.
	expenseCount := 0
	seenExpense := make(map[string]bool, len(lines))
	for _, line := range lines {
		if !seenExpense[line.ExpenseNumber] {
			seenExpense[line.ExpenseNumber] = true
			expenseCount++
		}
	}

	for _, line := range lines {
		pdf.SetFillColor(254, 249, 235)
		pdf.SetFont("Arial", "", 8)
		pdf.SetTextColor(40, 40, 40)

		category := line.Category
		if category == "" {
			category = "-"
		}
		itemDesc := line.ItemDescription
		if itemDesc == "" {
			itemDesc = line.Description
			if itemDesc == "" {
				itemDesc = "-"
			}
		}

		pdf.CellFormat(colNum, 6.5, sanitizePDFText(line.ExpenseNumber), "1", 0, "L", true, 0, "")
		pdf.CellFormat(colDate, 6.5, line.Date, "1", 0, "L", true, 0, "")
		pdf.CellFormat(colCategory, 6.5, sanitizePDFText(truncatePDF(category, 24)), "1", 0, "L", true, 0, "")
		pdf.CellFormat(colItem, 6.5, sanitizePDFText(truncatePDF(itemDesc, 60)), "1", 0, "L", true, 0, "")
		pdf.SetFont("Arial", "B", 9)
		pdf.SetTextColor(154, 52, 18)
		pdf.CellFormat(colAmount, 6.5, fmt.Sprintf("%.2f", line.Amount), "1", 1, "R", true, 0, "")
	}

	pdf.SetFillColor(254, 243, 199)
	pdf.SetFont("Arial", "B", 9)
	pdf.SetTextColor(120, 53, 15)
	pdf.CellFormat(colNum+colDate+colCategory+colItem, 7,
		fmt.Sprintf("Total (%d expenses, %d items)", expenseCount, len(lines)), "1", 0, "L", true, 0, "")
	pdf.CellFormat(colAmount, 7, fmt.Sprintf("%.2f", report.Expenses.TotalAmount), "1", 1, "R", true, 0, "")
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
	innerW := cardW - 8
	padX := 4.0
	padTop := 3.0
	padBottom := 5.0
	labelLineH := 3.8
	valueLineH := 5.6
	subLineH := 4.0
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

	// Wrap against the inner text width, minus cell padding used when drawing.
	textW := innerW
	wrapW := innerW - 2.0
	if wrapW < 8 {
		wrapW = innerW
	}

	type cardLines struct {
		label []string
		value []string
		sub   []string
	}
	rendered := make([]cardLines, len(cards))
	maxLabelLines, maxValueLines, maxSubLines := 1, 1, 1
	for i, card := range cards {
		pdf.SetFont("Arial", "", 8)
		rendered[i].label = wrapPDFText(pdf, card.label, wrapW)
		pdf.SetFont("Arial", "B", 11)
		rendered[i].value = wrapPDFText(pdf, card.value, wrapW)
		pdf.SetFont("Arial", "", 7)
		rendered[i].sub = wrapPDFText(pdf, card.sub, wrapW)
		if n := len(rendered[i].label); n > maxLabelLines {
			maxLabelLines = n
		}
		if n := len(rendered[i].value); n > maxValueLines {
			maxValueLines = n
		}
		if n := len(rendered[i].sub); n > maxSubLines {
			maxSubLines = n
		}
	}
	cardH := padTop + float64(maxLabelLines)*labelLineH + float64(maxValueLines)*valueLineH + float64(maxSubLines)*subLineH + padBottom

	for i, card := range cards {
		x := leftM + float64(i)*(cardW+3)
		pdf.SetDrawColor(220, 220, 220)
		pdf.SetFillColor(248, 250, 252)
		pdf.Rect(x, startY, cardW, cardH, "DF")
		pdf.ClipRect(x+0.3, startY+0.3, cardW-0.6, cardH-0.6, false)

		textX := x + padX
		labelY := startY + padTop
		valueY := labelY + float64(maxLabelLines)*labelLineH
		subY := valueY + float64(maxValueLines)*valueLineH

		pdf.SetFont("Arial", "", 8)
		pdf.SetTextColor(card.r, card.g, card.b)
		writePDFWrappedLines(pdf, textX, labelY, textW, labelLineH, rendered[i].label)

		pdf.SetFont("Arial", "B", 11)
		writePDFWrappedLines(pdf, textX, valueY, textW, valueLineH, rendered[i].value)

		pdf.SetFont("Arial", "", 7)
		pdf.SetTextColor(100, 100, 100)
		writePDFWrappedLines(pdf, textX, subY, textW, subLineH, rendered[i].sub)

		pdf.ClipEnd()
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

	pdf.SetFont("Arial", "", 9)
	pdf.SetTextColor(50, 50, 50)
	for _, row := range periodReportTableRows(report) {
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

	writePaymentsByMethodPDF(pdf, report)

	writeExpenseLinesPDF(pdf, report)

	pdf.Ln(8)
	pdf.SetFont("Arial", "I", 8)
	pdf.SetTextColor(140, 140, 140)
	pdf.MultiCell(0, 4.5, sanitizePDFText("Purchase expense = full bill total; Payment out = paid; AP = unpaid. Payments by method lists Cash, UPI, Card, Bank Transfer and Cheque separately. Generated from TruERP."), "", "L", false)

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
