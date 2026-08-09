package controllers

import (
	"fmt"
	"net/http"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func parseDashboardPeriod(c *gin.Context) (start time.Time, end time.Time, filterDates bool) {
	now := time.Now()
	loc := now.Location()
	end = time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 999999999, loc)

	from := c.Query("from_date")
	to := c.Query("to_date")
	if from != "" || to != "" {
		start = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
		if from != "" {
			if t, err := time.ParseInLocation("2006-01-02", from, loc); err == nil {
				start = t
			}
		}
		if to != "" {
			if t, err := time.ParseInLocation("2006-01-02", to, loc); err == nil {
				end = t.Add(23*time.Hour + 59*time.Minute + 59*time.Second)
			}
		}
		return start, end, true
	}

	switch c.DefaultQuery("period", "month") {
	case "today":
		start = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	case "week":
		weekday := int(now.Weekday())
		if weekday == 0 {
			weekday = 7
		}
		start = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -(weekday - 1))
	case "year":
		start = time.Date(now.Year(), 1, 1, 0, 0, 0, 0, loc)
	case "all":
		return start, end, false
	default: // month
		start = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
	}
	return start, end, true
}

func GetDashboardStats(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	start, end, filterDates := parseDashboardPeriod(c)

	// Keep overdue status in sync before snapshot metrics
	syncOverdueInvoices(userID)

	var stats models.DashboardStats
	unpaidStatuses := []string{"sent", "partial", "overdue"}

	salesQuery := utils.DB.Model(&models.Invoice{}).Where("user_id = ? AND status = ?", userID, "paid")
	invoiceQuery := utils.DB.Model(&models.Invoice{}).Where("user_id = ?", userID)
	if filterDates {
		salesQuery = salesQuery.Where("date >= ? AND date <= ?", start, end)
		invoiceQuery = invoiceQuery.Where("date >= ? AND date <= ?", start, end)
	}
	salesQuery.Select("COALESCE(SUM(total_amount), 0)").Scan(&stats.TotalSales)
	invoiceQuery.Count(&stats.TotalInvoices)

	utils.DB.Model(&models.Party{}).Where("user_id = ?", userID).Count(&stats.TotalParties)
	utils.DB.Model(&models.Party{}).Where("user_id = ? AND party_type = ?", userID, "customer").Count(&stats.TotalCustomers)

	// Catalog snapshot — same scope as GET /products (user_id, GORM soft-delete)
	utils.DB.Model(&models.Product{}).Where("user_id = ?", userID).Count(&stats.TotalProducts)

	stats.LowStockProducts = countConsolidatedLowStockProducts(userID, true)

	// Pending receivables — issued invoices with remaining balance (excludes drafts/cancelled/paid)
	utils.DB.Model(&models.Invoice{}).
		Where("user_id = ? AND status IN ? AND total_amount > amount_paid", userID, unpaidStatuses).
		Select("COALESCE(SUM(total_amount - amount_paid), 0)").
		Scan(&stats.PendingAmount)

	// Period paid sales and invoice count (mirrors filtered totals for dashboard cards)
	stats.TodaySales = stats.TotalSales
	stats.TodayInvoices = stats.TotalInvoices

	// Overdue — unpaid issued invoices past due (includes status=overdue and past-due partial/sent)
	utils.DB.Model(&models.Invoice{}).
		Where("user_id = ? AND status IN ? AND due_date IS NOT NULL AND due_date < ? AND total_amount > amount_paid",
			userID, unpaidStatuses, time.Now()).
		Count(&stats.OverdueInvoices)

	c.JSON(http.StatusOK, stats)
}

func GetRecentInvoices(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	start, end, filterDates := parseDashboardPeriod(c)

	var invoices []models.Invoice
	query := utils.DB.Where("user_id = ?", userID)
	if filterDates {
		query = query.Where("date >= ? AND date <= ?", start, end)
	}
	if err := query.Preload("Party").Order("created_at DESC").Limit(5).Find(&invoices).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch recent invoices"})
		return
	}

	c.JSON(http.StatusOK, invoices)
}

func GetRecentPayments(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var payments []models.Payment
	if err := utils.DB.Where("user_id = ?", userID).Order("created_at DESC").Limit(5).Find(&payments).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch recent payments"})
		return
	}

	c.JSON(http.StatusOK, payments)
}

func GetSalesReport(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	period := c.DefaultQuery("period", "monthly") // daily, weekly, monthly, yearly

	type ReportItem struct {
		Period string  `json:"period"`
		Sales  float64 `json:"sales"`
		Count  int64   `json:"count"`
	}

	var results []ReportItem
	periodExpr := utils.SQLPeriodExpr("date", period)
	limit := utils.SQLPeriodLimit(period)
	query := fmt.Sprintf(
		"SELECT %s as period, COALESCE(SUM(total_amount), 0) as sales, COUNT(*) as count FROM invoices WHERE user_id = ? AND status = 'paid' GROUP BY %s ORDER BY period DESC LIMIT %s",
		periodExpr, periodExpr, limit,
	)

	if err := utils.DB.Raw(query, userID).Scan(&results).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate sales report"})
		return
	}

	c.JSON(http.StatusOK, results)
}

func GetGSTReport(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var reports []models.GSTReport
	periodExpr := utils.SQLPeriodExpr("date", "monthly")
	query := fmt.Sprintf(`
		SELECT 
			%s as month,
			COALESCE(SUM(cgst_total), 0) as cgst,
			COALESCE(SUM(sgst_total), 0) as sgst,
			COALESCE(SUM(igst_total), 0) as igst,
			COALESCE(SUM(cgst_total + sgst_total + igst_total), 0) as total_tax,
			COALESCE(SUM(total_amount), 0) as total_value
		FROM invoices 
		WHERE user_id = ? AND status IN ('paid', 'sent')
		GROUP BY %s
		ORDER BY month DESC
		LIMIT 12
	`, periodExpr, periodExpr)

	if err := utils.DB.Raw(query, userID).Scan(&reports).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate GST report"})
		return
	}

	c.JSON(http.StatusOK, reports)
}

func GetTopParties(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	type TopParty struct {
		PartyID     uuid.UUID `json:"party_id"`
		Name        string    `json:"name"`
		TotalSales  float64   `json:"total_sales"`
		InvoiceCount int64   `json:"invoice_count"`
	}

	var results []TopParty
	query := `
		SELECT 
			p.id as party_id,
			p.name,
			COALESCE(SUM(i.total_amount), 0) as total_sales,
			COUNT(i.id) as invoice_count
		FROM parties p
		LEFT JOIN invoices i ON p.id = i.party_id AND i.status = 'paid'
		WHERE p.user_id = ?
		GROUP BY p.id, p.name
		ORDER BY total_sales DESC
		LIMIT 5
	`

	if err := utils.DB.Raw(query, userID).Scan(&results).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch top parties"})
		return
	}

	c.JSON(http.StatusOK, results)
}
