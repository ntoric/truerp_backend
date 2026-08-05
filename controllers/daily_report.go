package controllers

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"net/http"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/go-pdf/fpdf"
	"github.com/google/uuid"
)

// computeDailyProductProfit sums (taxable sale − purchase cost) per line item for the day,
// minus the same margin reversed on sales returns and credit notes.
func computeDailyProductProfit(userID uuid.UUID, date string) float64 {
	var salesProfit float64
	salesQuery := `
		SELECT COALESCE(SUM(
			(ii.quantity * ii.unit_price * (1.0 - ii.discount / 100.0))
			- (ii.quantity * COALESCE(p.purchase_price, 0))
		), 0) AS profit
		FROM invoice_items ii
		INNER JOIN invoices i ON i.id = ii.invoice_id
		LEFT JOIN products p ON p.id = ii.product_id
		WHERE i.user_id = ? AND DATE(i.date) = ? AND i.status != 'cancelled' AND i.deleted_at IS NULL
	`
	utils.DB.Raw(salesQuery, userID, date).Scan(&salesProfit)

	var returnsProfit float64
	returnsQuery := `
		SELECT COALESCE(SUM(
			(sri.quantity * sri.unit_price)
			- (sri.quantity * COALESCE(p.purchase_price, 0))
		), 0) AS profit
		FROM sales_return_items sri
		INNER JOIN sales_returns sr ON sr.id = sri.return_id
		LEFT JOIN products p ON p.id = sri.product_id
		WHERE sr.user_id = ? AND DATE(sr.date) = ? AND sr.status != 'cancelled' AND sr.deleted_at IS NULL
	`
	utils.DB.Raw(returnsQuery, userID, date).Scan(&returnsProfit)

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
		WHERE cn.user_id = ? AND DATE(cn.date) = ? AND cn.status != 'cancelled' AND cn.deleted_at IS NULL
	`
	utils.DB.Raw(creditQuery, userID, date).Scan(&creditNoteProfit)

	return salesProfit - returnsProfit - creditNoteProfit
}

func parseReportDate(c *gin.Context) (string, bool) {
	date := c.DefaultQuery("date", time.Now().Format("2006-01-02"))
	if _, err := time.Parse("2006-01-02", date); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid date format. Use YYYY-MM-DD"})
		return "", false
	}
	return date, true
}

func loadDailyReport(userID uuid.UUID, date string) (models.DailyReport, error) {
	report := models.DailyReport{Date: date}

	var business models.Business
	if err := utils.DB.Where("user_id = ?", userID).First(&business).Error; err == nil {
		report.BusinessName = business.Name
	}

	aggregate := func(model interface{}, dateColumn, amountColumn, extra string, dest *models.DailyReportMetric) {
		q := utils.DB.Model(model).
			Where("user_id = ? AND DATE("+dateColumn+") = ?", userID, date)
		if extra != "" {
			q = q.Where(extra)
		}
		q.Select("COALESCE(SUM(" + amountColumn + "), 0) as total_amount, COUNT(*) as count").Scan(dest)
	}

	aggregate(&models.Invoice{}, "date", "total_amount", "status != 'cancelled'", &report.Sales)
	// Purchase expense = full invoice amount for bills dated this day.
	aggregate(&models.PurchaseBill{}, "bill_date", "total_amount", "", &report.Purchases)
	aggregate(&models.CreditNote{}, "date", "total_amount", "status != 'cancelled'", &report.CreditNotes)
	aggregate(&models.DebitNote{}, "date", "total_amount", "status != 'cancelled'", &report.DebitNotes)
	aggregate(&models.Expense{}, "date", "amount", "", &report.Expenses)
	aggregate(&models.Payment{}, "date", "amount_received", "", &report.PaymentsIn)
	aggregate(&models.PaymentOut{}, "date", "amount_paid", "", &report.PaymentsOut)
	aggregate(&models.SalesReturn{}, "date", "amount", "status != 'cancelled'", &report.SalesReturns)
	aggregate(&models.PurchaseReturn{}, "date", "amount", "status != 'cancelled'", &report.PurchaseReturns)

	// AP from today's purchases: unpaid portion (expense − paid).
	utils.DB.Model(&models.PurchaseBill{}).
		Where("user_id = ? AND DATE(bill_date) = ? AND total_amount > paid_amount", userID, date).
		Select("COALESCE(SUM(total_amount - paid_amount), 0) as total_amount, COUNT(*) as count").
		Scan(&report.AccountsPayable)

	// Overall outstanding AP (all open purchase bills).
	utils.DB.Model(&models.PurchaseBill{}).
		Where("user_id = ?", userID).
		Select("COALESCE(SUM(CASE WHEN total_amount > paid_amount THEN total_amount - paid_amount ELSE 0 END), 0)").
		Scan(&report.AccountsPayableTotal)

	utils.DB.Model(&models.Invoice{}).
		Where("user_id = ? AND DATE(date) = ? AND status != ?", userID, date, "cancelled").
		Select("COALESCE(SUM(tax_total), 0)").Scan(&report.GSTCollected)

	report.NetCashFlow = report.PaymentsIn.TotalAmount - report.PaymentsOut.TotalAmount - report.Expenses.TotalAmount

	report.DailyProfit = report.Sales.TotalAmount -
		report.CreditNotes.TotalAmount -
		report.SalesReturns.TotalAmount -
		report.Purchases.TotalAmount +
		report.PurchaseReturns.TotalAmount +
		report.DebitNotes.TotalAmount -
		report.Expenses.TotalAmount

	report.ProductProfit = computeDailyProductProfit(userID, date)

	return report, nil
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

	filename := fmt.Sprintf("daily_report_%s.csv", date)
	c.Header("Content-Type", "text/csv")
	c.Header("Content-Disposition", "attachment; filename="+filename)

	writer := csv.NewWriter(c.Writer)
	defer writer.Flush()

	_ = writer.Write([]string{"Daily Business Report"})
	_ = writer.Write([]string{"Business", report.BusinessName})
	_ = writer.Write([]string{"Date", report.Date})
	_ = writer.Write([]string{""})
	_ = writer.Write([]string{"Section", "Count", "Amount (INR)"})

	rows := []struct {
		label string
		m     models.DailyReportMetric
	}{
		{"Sales (Invoices)", report.Sales},
		{"Purchase Expense", report.Purchases},
		{"Payment Out", report.PaymentsOut},
		{"Accounts Payable (today)", report.AccountsPayable},
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
	_ = writer.Write([]string{"Daily Profit (Sales − Purchases − Expenses ± returns/notes)", "", fmt.Sprintf("%.2f", report.DailyProfit)})
	_ = writer.Write([]string{"Product Profit (sale value − purchase cost on items sold)", "", fmt.Sprintf("%.2f", report.ProductProfit)})
	_ = writer.Write([]string{"Net Cash Flow (In − Out − Expenses)", "", fmt.Sprintf("%.2f", report.NetCashFlow)})
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

func buildDailyReportPDF(report models.DailyReport) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(14, 16, 14)
	pdf.SetAutoPageBreak(true, 16)
	pdf.AddPage()

	displayDate := report.Date
	if t, err := time.Parse("2006-01-02", report.Date); err == nil {
		displayDate = t.Format("02-01-2006")
	}

	pdf.SetFont("Arial", "B", 18)
	pdf.SetTextColor(37, 99, 235)
	pdf.CellFormat(0, 10, "DAILY BUSINESS REPORT", "", 1, "L", false, 0, "")

	pdf.SetFont("Arial", "", 11)
	pdf.SetTextColor(80, 80, 80)
	if report.BusinessName != "" {
		pdf.CellFormat(0, 6, sanitizePDFText(report.BusinessName), "", 1, "L", false, 0, "")
	}
	pdf.CellFormat(0, 6, "Date: "+displayDate, "", 1, "L", false, 0, "")
	pdf.Ln(6)

	// Summary cards
	pageW, _ := pdf.GetPageSize()
	leftM, _, rightM, _ := pdf.GetMargins()
	usable := pageW - leftM - rightM
	cardW := (usable - 12) / 5
	cardH := 22.0
	startY := pdf.GetY()

	// Purchase expense only — do not add payments_out (that is cash movement against AP).
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
		{"Daily profit", fmt.Sprintf("Rs. %.2f", report.DailyProfit), "Sales − purchases − expenses ± returns", profitR, profitG, profitB},
		{"Product profit", fmt.Sprintf("Rs. %.2f", report.ProductProfit), "Sale value − purchase cost on items", productR, productG, productB},
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

	// Detail table
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
		{"Accounts Payable (today)", report.AccountsPayable},
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

	pdf.CellFormat(colSection, 7, "Daily profit (Sales - Purchases - Expenses)", "1", 0, "L", true, 0, "")
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
