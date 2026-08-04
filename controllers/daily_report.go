package controllers

import (
	"truerp/models"
	"truerp/utils"
	"encoding/csv"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

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
	aggregate(&models.PurchaseBill{}, "bill_date", "total_amount", "", &report.Purchases)
	aggregate(&models.CreditNote{}, "date", "total_amount", "status != 'cancelled'", &report.CreditNotes)
	aggregate(&models.DebitNote{}, "date", "total_amount", "status != 'cancelled'", &report.DebitNotes)
	aggregate(&models.Expense{}, "date", "amount", "", &report.Expenses)
	aggregate(&models.Payment{}, "date", "amount_received", "", &report.PaymentsIn)
	aggregate(&models.PaymentOut{}, "date", "amount_paid", "", &report.PaymentsOut)
	aggregate(&models.SalesReturn{}, "date", "amount", "status != 'cancelled'", &report.SalesReturns)
	aggregate(&models.PurchaseReturn{}, "date", "amount", "status != 'cancelled'", &report.PurchaseReturns)

	utils.DB.Model(&models.Invoice{}).
		Where("user_id = ? AND DATE(date) = ? AND status != ?", userID, date, "cancelled").
		Select("COALESCE(SUM(tax_total), 0)").Scan(&report.GSTCollected)

	report.NetCashFlow = report.PaymentsIn.TotalAmount - report.PaymentsOut.TotalAmount - report.Expenses.TotalAmount

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
		{"Purchases", report.Purchases},
		{"Credit Notes", report.CreditNotes},
		{"Debit Notes", report.DebitNotes},
		{"Expenses", report.Expenses},
		{"Payments Received", report.PaymentsIn},
		{"Payments Made", report.PaymentsOut},
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
	_ = writer.Write([]string{"GST Collected (Sales)", "", fmt.Sprintf("%.2f", report.GSTCollected)})
	_ = writer.Write([]string{"Net Cash Flow (In − Out − Expenses)", "", fmt.Sprintf("%.2f", report.NetCashFlow)})
}
