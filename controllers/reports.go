package controllers

import (
	"net/http"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func parseReportDateRange(c *gin.Context) (time.Time, time.Time) {
	from := c.Query("from_date")
	to := c.Query("to_date")
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	end := now
	if from != "" {
		if t, err := time.Parse("2006-01-02", from); err == nil {
			start = t
		}
	}
	if to != "" {
		if t, err := time.Parse("2006-01-02", to); err == nil {
			end = t.Add(23*time.Hour + 59*time.Minute + 59*time.Second)
		}
	}
	return start, end
}

func GetReportWidgets(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	monthStart := time.Date(time.Now().Year(), time.Now().Month(), 1, 0, 0, 0, 0, time.Local)

	type widgets struct {
		TotalSales           float64 `json:"total_sales"`
		MonthRevenue         float64 `json:"month_revenue"`
		OutstandingAmount    float64 `json:"outstanding_amount"`
		OutstandingCount     int64   `json:"outstanding_count"`
		InventoryValue       float64 `json:"inventory_value"`
		LowStockCount        int64   `json:"low_stock_count"`
		MonthTax             float64 `json:"month_tax"`
		PaymentsInMonth      float64 `json:"payments_in_month"`
		PaymentsOutMonth     float64 `json:"payments_out_month"`
		PurchaseExpenseMonth float64 `json:"purchase_expense_month"`
		AccountsPayable      float64 `json:"accounts_payable"`
		AccountsPayableCount int64   `json:"accounts_payable_count"`
		MonthNetProfit       float64 `json:"month_net_profit"`
	}

	var w widgets

	utils.DB.Model(&models.Invoice{}).Where("user_id = ? AND status = ?", userID, "paid").
		Select("COALESCE(SUM(total_amount), 0)").Scan(&w.TotalSales)

	utils.DB.Model(&models.Invoice{}).Where("user_id = ? AND status = ? AND date >= ?", userID, "paid", monthStart).
		Select("COALESCE(SUM(total_amount), 0)").Scan(&w.MonthRevenue)

	utils.DB.Model(&models.Invoice{}).Where("user_id = ? AND status IN ? AND total_amount > amount_paid", userID, []string{"sent", "partial", "overdue"}).
		Select("COALESCE(SUM(total_amount - amount_paid), 0)").Scan(&w.OutstandingAmount)
	utils.DB.Model(&models.Invoice{}).Where("user_id = ? AND status IN ? AND total_amount > amount_paid", userID, []string{"sent", "partial", "overdue"}).
		Count(&w.OutstandingCount)

	utils.DB.Model(&models.InventoryStock{}).Where("user_id = ?", userID).
		Select("COALESCE(SUM(quantity * average_cost), 0)").Scan(&w.InventoryValue)

	lowStockQuery := `
		SELECT COUNT(DISTINCT p.id)
		FROM products p
		INNER JOIN inventory_stocks s ON p.id = s.product_id
		WHERE p.user_id = ? AND p.low_stock_alert = true AND s.quantity <= p.min_stock
	`
	utils.DB.Raw(lowStockQuery, userID).Scan(&w.LowStockCount)

	utils.DB.Model(&models.Invoice{}).Where("user_id = ? AND status IN ('paid', 'sent') AND date >= ?", userID, monthStart).
		Select("COALESCE(SUM(cgst_total + sgst_total + igst_total), 0)").Scan(&w.MonthTax)

	utils.DB.Model(&models.Payment{}).Where("user_id = ? AND date >= ?", userID, monthStart).
		Select("COALESCE(SUM(amount_received), 0)").Scan(&w.PaymentsInMonth)
	utils.DB.Model(&models.PaymentOut{}).Where("user_id = ? AND date >= ?", userID, monthStart).
		Select("COALESCE(SUM(amount_paid), 0)").Scan(&w.PaymentsOutMonth)

	// Purchase expense = full bill totals this month; AP = unpaid balances.
	utils.DB.Model(&models.PurchaseBill{}).Where("user_id = ? AND bill_date >= ?", userID, monthStart).
		Select("COALESCE(SUM(total_amount), 0)").Scan(&w.PurchaseExpenseMonth)
	utils.DB.Model(&models.PurchaseBill{}).Where("user_id = ? AND total_amount > paid_amount", userID).
		Select("COALESCE(SUM(total_amount - paid_amount), 0)").Scan(&w.AccountsPayable)
	utils.DB.Model(&models.PurchaseBill{}).Where("user_id = ? AND total_amount > paid_amount", userID).
		Count(&w.AccountsPayableCount)

	var income, expense float64
	utils.DB.Model(&models.Account{}).Where("user_id = ? AND is_active = ? AND account_type = ?", userID, true, "income").
		Select("COALESCE(SUM(balance), 0)").Scan(&income)
	utils.DB.Model(&models.Account{}).Where("user_id = ? AND is_active = ? AND account_type = ?", userID, true, "expense").
		Select("COALESCE(SUM(balance), 0)").Scan(&expense)
	w.MonthNetProfit = income - expense

	c.JSON(http.StatusOK, w)
}

func GetRevenueReport(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	period := c.DefaultQuery("period", "monthly")

	type RevenueItem struct {
		Period       string  `json:"period"`
		Gross        float64 `json:"gross"`
		Net          float64 `json:"net"`
		Tax          float64 `json:"tax"`
		InvoiceCount int64   `json:"invoice_count"`
		AvgInvoice   float64 `json:"avg_invoice"`
	}

	var results []RevenueItem
	var query string

	switch period {
	case "daily":
		query = `
			SELECT DATE(date) as period,
				COALESCE(SUM(total_amount), 0) as gross,
				COALESCE(SUM(sub_total - discount_total - invoice_discount), 0) as net,
				COALESCE(SUM(tax_total), 0) as tax,
				COUNT(*) as invoice_count,
				COALESCE(AVG(total_amount), 0) as avg_invoice
			FROM invoices WHERE user_id = ? AND status = 'paid'
			GROUP BY DATE(date) ORDER BY period DESC LIMIT 30`
	case "weekly":
		query = `
			SELECT strftime('%Y-W%W', date) as period,
				COALESCE(SUM(total_amount), 0) as gross,
				COALESCE(SUM(sub_total - discount_total - invoice_discount), 0) as net,
				COALESCE(SUM(tax_total), 0) as tax,
				COUNT(*) as invoice_count,
				COALESCE(AVG(total_amount), 0) as avg_invoice
			FROM invoices WHERE user_id = ? AND status = 'paid'
			GROUP BY strftime('%Y-W%W', date) ORDER BY period DESC LIMIT 12`
	case "yearly":
		query = `
			SELECT strftime('%Y', date) as period,
				COALESCE(SUM(total_amount), 0) as gross,
				COALESCE(SUM(sub_total - discount_total - invoice_discount), 0) as net,
				COALESCE(SUM(tax_total), 0) as tax,
				COUNT(*) as invoice_count,
				COALESCE(AVG(total_amount), 0) as avg_invoice
			FROM invoices WHERE user_id = ? AND status = 'paid'
			GROUP BY strftime('%Y', date) ORDER BY period DESC LIMIT 5`
	default:
		query = `
			SELECT strftime('%Y-%m', date) as period,
				COALESCE(SUM(total_amount), 0) as gross,
				COALESCE(SUM(sub_total - discount_total - invoice_discount), 0) as net,
				COALESCE(SUM(tax_total), 0) as tax,
				COUNT(*) as invoice_count,
				COALESCE(AVG(total_amount), 0) as avg_invoice
			FROM invoices WHERE user_id = ? AND status = 'paid'
			GROUP BY strftime('%Y-%m', date) ORDER BY period DESC LIMIT 12`
	}

	if err := utils.DB.Raw(query, userID).Scan(&results).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate revenue report"})
		return
	}

	var totalGross, totalNet, totalTax float64
	var totalInvoices int64
	for _, r := range results {
		totalGross += r.Gross
		totalNet += r.Net
		totalTax += r.Tax
		totalInvoices += r.InvoiceCount
	}
	avgInvoice := 0.0
	if totalInvoices > 0 {
		avgInvoice = totalGross / float64(totalInvoices)
	}

	c.JSON(http.StatusOK, gin.H{
		"period": period,
		"summary": gin.H{
			"total_gross":       totalGross,
			"total_net":         totalNet,
			"total_tax":         totalTax,
			"total_invoices":    totalInvoices,
			"avg_invoice_value": avgInvoice,
			"periods_in_report": len(results),
		},
		"periods": results,
	})
}

func GetSalesReportDetailed(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	period := c.DefaultQuery("period", "monthly")

	type SalesItem struct {
		Period     string  `json:"period"`
		Sales      float64 `json:"sales"`
		Count      int64   `json:"count"`
		AvgInvoice float64 `json:"avg_invoice"`
	}

	var series []SalesItem
	var query string

	switch period {
	case "daily":
		query = `
			SELECT DATE(date) as period, COALESCE(SUM(total_amount), 0) as sales, COUNT(*) as count,
				COALESCE(AVG(total_amount), 0) as avg_invoice
			FROM invoices WHERE user_id = ? AND status = 'paid'
			GROUP BY DATE(date) ORDER BY period DESC LIMIT 30`
	case "weekly":
		query = `
			SELECT strftime('%Y-W%W', date) as period, COALESCE(SUM(total_amount), 0) as sales, COUNT(*) as count,
				COALESCE(AVG(total_amount), 0) as avg_invoice
			FROM invoices WHERE user_id = ? AND status = 'paid'
			GROUP BY strftime('%Y-W%W', date) ORDER BY period DESC LIMIT 12`
	case "yearly":
		query = `
			SELECT strftime('%Y', date) as period, COALESCE(SUM(total_amount), 0) as sales, COUNT(*) as count,
				COALESCE(AVG(total_amount), 0) as avg_invoice
			FROM invoices WHERE user_id = ? AND status = 'paid'
			GROUP BY strftime('%Y', date) ORDER BY period DESC LIMIT 5`
	default:
		query = `
			SELECT strftime('%Y-%m', date) as period, COALESCE(SUM(total_amount), 0) as sales, COUNT(*) as count,
				COALESCE(AVG(total_amount), 0) as avg_invoice
			FROM invoices WHERE user_id = ? AND status = 'paid'
			GROUP BY strftime('%Y-%m', date) ORDER BY period DESC LIMIT 12`
	}

	if err := utils.DB.Raw(query, userID).Scan(&series).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate sales report"})
		return
	}

	var totalSales float64
	var totalCount int64
	var bestPeriod string
	var bestSales float64
	for _, s := range series {
		totalSales += s.Sales
		totalCount += s.Count
		if s.Sales > bestSales {
			bestSales = s.Sales
			bestPeriod = s.Period
		}
	}
	avgInvoice := 0.0
	if totalCount > 0 {
		avgInvoice = totalSales / float64(totalCount)
	}

	var growthPct *float64
	if len(series) >= 2 {
		current := series[0].Sales
		previous := series[1].Sales
		if previous > 0 {
			g := ((current - previous) / previous) * 100
			growthPct = &g
		}
	}

	var statusBreakdown []struct {
		Status string  `json:"status"`
		Count  int64   `json:"count"`
		Amount float64 `json:"amount"`
	}
	utils.DB.Raw(`
		SELECT status, COUNT(*) as count, COALESCE(SUM(total_amount), 0) as amount
		FROM invoices WHERE user_id = ?
		GROUP BY status ORDER BY amount DESC`, userID).Scan(&statusBreakdown)

	c.JSON(http.StatusOK, gin.H{
		"period": period,
		"summary": gin.H{
			"total_sales":       totalSales,
			"total_invoices":    totalCount,
			"avg_invoice_value": avgInvoice,
			"best_period":       bestPeriod,
			"best_period_sales": bestSales,
			"growth_vs_prior":   growthPct,
		},
		"series":           series,
		"status_breakdown": statusBreakdown,
	})
}

func GetTaxReport(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var months []models.GSTReport
	query := `
		SELECT 
			strftime('%Y-%m', date) as month,
			COALESCE(SUM(cgst_total), 0) as cgst,
			COALESCE(SUM(sgst_total), 0) as sgst,
			COALESCE(SUM(igst_total), 0) as igst,
			COALESCE(SUM(cgst_total + sgst_total + igst_total), 0) as total_tax,
			COALESCE(SUM(total_amount), 0) as total_value
		FROM invoices 
		WHERE user_id = ? AND status IN ('paid', 'sent')
		GROUP BY strftime('%Y-%m', date)
		ORDER BY month DESC
		LIMIT 12
	`
	if err := utils.DB.Raw(query, userID).Scan(&months).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate tax report"})
		return
	}

	var totalCGST, totalSGST, totalIGST, totalTax, totalValue float64
	for _, m := range months {
		totalCGST += m.CGST
		totalSGST += m.SGST
		totalIGST += m.IGST
		totalTax += m.TotalTax
		totalValue += m.TotalValue
	}
	effectiveRate := 0.0
	if totalValue > 0 {
		effectiveRate = (totalTax / totalValue) * 100
	}

	c.JSON(http.StatusOK, gin.H{
		"summary": gin.H{
			"total_cgst":         totalCGST,
			"total_sgst":         totalSGST,
			"total_igst":         totalIGST,
			"total_tax":          totalTax,
			"taxable_turnover":   totalValue,
			"effective_tax_rate": effectiveRate,
			"months_in_report":   len(months),
		},
		"months": months,
	})
}

func GetOutstandingInvoicesReport(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	type OutstandingRow struct {
		ID            uuid.UUID  `json:"id"`
		InvoiceNumber string     `json:"invoice_number"`
		PartyID       uuid.UUID  `json:"party_id"`
		PartyName     string     `json:"party_name"`
		Date          time.Time  `json:"date"`
		DueDate       *time.Time `json:"due_date,omitempty"`
		Status        string     `json:"status"`
		TotalAmount   float64    `json:"total_amount"`
		AmountPaid    float64    `json:"amount_paid"`
		Outstanding   float64    `json:"outstanding"`
		DaysOverdue   int        `json:"days_overdue"`
		AgingBucket   string     `json:"aging_bucket"`
	}

	type PartyOutstanding struct {
		PartyID      uuid.UUID `json:"party_id"`
		PartyName    string    `json:"party_name"`
		InvoiceCount int       `json:"invoice_count"`
		Outstanding  float64   `json:"outstanding"`
	}

	type AgingBucket struct {
		Current    float64 `json:"current"`
		Days1_30   float64 `json:"days_1_30"`
		Days31_60  float64 `json:"days_31_60"`
		Days61_90  float64 `json:"days_61_90"`
		Days90Plus float64 `json:"days_90_plus"`
	}

	var rows []OutstandingRow
	query := `
		SELECT i.id, i.invoice_number, i.party_id, p.name as party_name, i.date, i.due_date, i.status,
			i.total_amount, i.amount_paid,
			(i.total_amount - i.amount_paid) as outstanding
		FROM invoices i
		INNER JOIN parties p ON p.id = i.party_id
		WHERE i.user_id = ? AND i.status IN ('sent', 'partial', 'overdue')
			AND (i.total_amount - i.amount_paid) > 0
			AND i.deleted_at IS NULL
		ORDER BY i.due_date ASC, i.date DESC
	`
	if err := utils.DB.Raw(query, userID).Scan(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch outstanding invoices"})
		return
	}

	now := time.Now()
	var totalOutstanding float64
	var overdueCount int
	var totalDaysOverdue int
	aging := AgingBucket{}
	partyMap := make(map[uuid.UUID]*PartyOutstanding)

	for i := range rows {
		totalOutstanding += rows[i].Outstanding
		if rows[i].DueDate != nil && now.After(*rows[i].DueDate) {
			rows[i].DaysOverdue = int(now.Sub(*rows[i].DueDate).Hours() / 24)
			overdueCount++
			totalDaysOverdue += rows[i].DaysOverdue
		}

		switch {
		case rows[i].DaysOverdue <= 0:
			rows[i].AgingBucket = "current"
			aging.Current += rows[i].Outstanding
		case rows[i].DaysOverdue <= 30:
			rows[i].AgingBucket = "1-30"
			aging.Days1_30 += rows[i].Outstanding
		case rows[i].DaysOverdue <= 60:
			rows[i].AgingBucket = "31-60"
			aging.Days31_60 += rows[i].Outstanding
		case rows[i].DaysOverdue <= 90:
			rows[i].AgingBucket = "61-90"
			aging.Days61_90 += rows[i].Outstanding
		default:
			rows[i].AgingBucket = "90+"
			aging.Days90Plus += rows[i].Outstanding
		}

		if po, ok := partyMap[rows[i].PartyID]; ok {
			po.InvoiceCount++
			po.Outstanding += rows[i].Outstanding
		} else {
			partyMap[rows[i].PartyID] = &PartyOutstanding{
				PartyID:      rows[i].PartyID,
				PartyName:    rows[i].PartyName,
				InvoiceCount: 1,
				Outstanding:  rows[i].Outstanding,
			}
		}
	}

	var byParty []PartyOutstanding
	for _, p := range partyMap {
		byParty = append(byParty, *p)
	}
	for i := 0; i < len(byParty); i++ {
		for j := i + 1; j < len(byParty); j++ {
			if byParty[j].Outstanding > byParty[i].Outstanding {
				byParty[i], byParty[j] = byParty[j], byParty[i]
			}
		}
	}
	if len(byParty) > 10 {
		byParty = byParty[:10]
	}

	avgDaysOverdue := 0.0
	if overdueCount > 0 {
		avgDaysOverdue = float64(totalDaysOverdue) / float64(overdueCount)
	}

	c.JSON(http.StatusOK, gin.H{
		"summary": gin.H{
			"total_outstanding": totalOutstanding,
			"invoice_count":     len(rows),
			"overdue_count":     overdueCount,
			"avg_days_overdue":  avgDaysOverdue,
		},
		"aging":    aging,
		"by_party": byParty,
		"invoices": rows,
	})
}

func GetCustomerWiseReport(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	limit := c.DefaultQuery("limit", "25")

	type CustomerRow struct {
		PartyID          uuid.UUID  `json:"party_id"`
		Name             string     `json:"name"`
		Phone            string     `json:"phone"`
		Email            string     `json:"email"`
		GSTIN            string     `json:"gstin"`
		TotalSales       float64    `json:"total_sales"`
		TotalOutstanding float64    `json:"total_outstanding"`
		InvoiceCount     int64      `json:"invoice_count"`
		PaidCount        int64      `json:"paid_count"`
		AvgInvoiceValue  float64    `json:"avg_invoice_value"`
		LastInvoiceDate  *time.Time `json:"last_invoice_date,omitempty"`
	}

	var results []CustomerRow
	query := `
		SELECT
			p.id as party_id,
			p.name,
			p.phone,
			p.email,
			p.gstin,
			COALESCE(SUM(CASE WHEN i.status = 'paid' THEN i.total_amount ELSE 0 END), 0) as total_sales,
			COALESCE(SUM(CASE WHEN i.status NOT IN ('cancelled', 'paid') THEN i.total_amount - i.amount_paid ELSE 0 END), 0) as total_outstanding,
			COUNT(i.id) as invoice_count,
			SUM(CASE WHEN i.status = 'paid' THEN 1 ELSE 0 END) as paid_count,
			COALESCE(AVG(CASE WHEN i.status = 'paid' THEN i.total_amount END), 0) as avg_invoice_value,
			MAX(i.date) as last_invoice_date
		FROM parties p
		LEFT JOIN invoices i ON p.id = i.party_id AND i.user_id = ?
		WHERE p.user_id = ? AND p.party_type = 'customer'
		GROUP BY p.id, p.name, p.phone, p.email, p.gstin
		HAVING invoice_count > 0 OR total_outstanding > 0
		ORDER BY total_sales DESC
		LIMIT ?
	`

	if err := utils.DB.Raw(query, userID, userID, limit).Scan(&results).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate customer report"})
		return
	}

	var totalSales, totalOutstanding float64
	var totalCustomers int
	for _, r := range results {
		totalSales += r.TotalSales
		totalOutstanding += r.TotalOutstanding
		totalCustomers++
	}

	c.JSON(http.StatusOK, gin.H{
		"summary": gin.H{
			"customer_count":    totalCustomers,
			"total_paid_sales":  totalSales,
			"total_outstanding": totalOutstanding,
			"avg_sales_per_party": func() float64 {
				if totalCustomers == 0 {
					return 0
				}
				return totalSales / float64(totalCustomers)
			}(),
		},
		"customers": results,
	})
}

func GetProductWiseReport(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	limit := c.DefaultQuery("limit", "25")

	type ProductRow struct {
		ProductID    uuid.UUID `json:"product_id"`
		Name         string    `json:"name"`
		SKU          string    `json:"sku"`
		Category     string    `json:"category"`
		Unit         string    `json:"unit"`
		SalePrice    float64   `json:"sale_price"`
		QuantitySold float64   `json:"quantity_sold"`
		Revenue      float64   `json:"revenue"`
		SharePercent float64   `json:"share_percent"`
	}

	source := "stock_entries"
	var byStock []ProductRow
	stockQuery := `
		SELECT
			p.id as product_id,
			p.name,
			p.sku,
			p.category,
			p.unit,
			p.sale_price,
			COALESCE(SUM(ABS(se.quantity)), 0) as quantity_sold,
			COALESCE(SUM(ABS(se.quantity) * p.sale_price), 0) as revenue,
			0 as share_percent
		FROM stock_entries se
		INNER JOIN products p ON p.id = se.product_id
		WHERE se.user_id = ? AND se.entry_type = 'sale' AND se.product_id IS NOT NULL
		GROUP BY p.id, p.name, p.sku, p.category, p.unit, p.sale_price
		ORDER BY quantity_sold DESC
		LIMIT ?
	`
	if err := utils.DB.Raw(stockQuery, userID, limit).Scan(&byStock).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate product report"})
		return
	}

	products := byStock
	if len(products) == 0 {
		source = "invoice_line_items"
		var byLineItem []ProductRow
		lineQuery := `
			SELECT
				'00000000-0000-0000-0000-000000000000' as product_id,
				ii.description as name,
				'' as sku,
				'' as category,
				COALESCE(ii.unit, '') as unit,
				COALESCE(AVG(ii.unit_price), 0) as sale_price,
				COALESCE(SUM(ii.quantity), 0) as quantity_sold,
				COALESCE(SUM(ii.total), 0) as revenue,
				0 as share_percent
			FROM invoice_items ii
			INNER JOIN invoices i ON i.id = ii.invoice_id
			WHERE i.user_id = ? AND i.status = 'paid'
			GROUP BY ii.description, ii.unit
			ORDER BY revenue DESC
			LIMIT ?
		`
		if err := utils.DB.Raw(lineQuery, userID, limit).Scan(&byLineItem).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate product report"})
			return
		}
		products = byLineItem
	}

	var totalRevenue, totalQty float64
	for _, p := range products {
		totalRevenue += p.Revenue
		totalQty += p.QuantitySold
	}
	for i := range products {
		if totalRevenue > 0 {
			products[i].SharePercent = (products[i].Revenue / totalRevenue) * 100
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"source": source,
		"summary": gin.H{
			"product_count":  len(products),
			"total_revenue":  totalRevenue,
			"total_qty_sold": totalQty,
			"avg_unit_revenue": func() float64 {
				if totalQty == 0 {
					return 0
				}
				return totalRevenue / totalQty
			}(),
		},
		"products": products,
	})
}

func GetPaymentsReport(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	period := c.DefaultQuery("period", "monthly")

	type PeriodRow struct {
		Period    string  `json:"period"`
		AmountIn  float64 `json:"amount_in"`
		AmountOut float64 `json:"amount_out"`
		CountIn   int64   `json:"count_in"`
		CountOut  int64   `json:"count_out"`
	}

	type ModeRow struct {
		Mode      string  `json:"mode"`
		Direction string  `json:"direction"`
		Total     float64 `json:"total"`
		Count     int64   `json:"count"`
	}

	var periodExpr string
	var limit string
	switch period {
	case "daily":
		periodExpr = "DATE(date)"
		limit = "30"
	case "weekly":
		periodExpr = "strftime('%Y-W%W', date)"
		limit = "12"
	case "yearly":
		periodExpr = "strftime('%Y', date)"
		limit = "5"
	default:
		periodExpr = "strftime('%Y-%m', date)"
		limit = "12"
	}

	var timeline []PeriodRow
	inQuery := `
		SELECT ` + periodExpr + ` as period,
			COALESCE(SUM(amount_received), 0) as amount_in,
			0 as amount_out,
			COUNT(*) as count_in,
			0 as count_out
		FROM payments WHERE user_id = ? AND deleted_at IS NULL
		GROUP BY period ORDER BY period DESC LIMIT ` + limit
	utils.DB.Raw(inQuery, userID).Scan(&timeline)

	outRows := make(map[string]*PeriodRow)
	for i := range timeline {
		outRows[timeline[i].Period] = &timeline[i]
	}

	type outOnly struct {
		Period    string
		AmountOut float64
		CountOut  int64
	}
	var outs []outOnly
	outQuery := `
		SELECT ` + periodExpr + ` as period,
			COALESCE(SUM(amount_paid), 0) as amount_out,
			COUNT(*) as count_out
		FROM payment_outs WHERE user_id = ? AND deleted_at IS NULL
		GROUP BY period ORDER BY period DESC LIMIT ` + limit
	utils.DB.Raw(outQuery, userID).Scan(&outs)

	for _, o := range outs {
		if row, ok := outRows[o.Period]; ok {
			row.AmountOut = o.AmountOut
			row.CountOut = o.CountOut
		} else {
			timeline = append(timeline, PeriodRow{
				Period:    o.Period,
				AmountOut: o.AmountOut,
				CountOut:  o.CountOut,
			})
		}
	}

	var byMode []ModeRow
	modeInQuery := `
		SELECT mode, 'in' as direction, COALESCE(SUM(amount_received), 0) as total, COUNT(*) as count
		FROM payments WHERE user_id = ? AND deleted_at IS NULL GROUP BY mode`
	utils.DB.Raw(modeInQuery, userID).Scan(&byMode)
	var modeOut []ModeRow
	modeOutQuery := `
		SELECT mode, 'out' as direction, COALESCE(SUM(amount_paid), 0) as total, COUNT(*) as count
		FROM payment_outs WHERE user_id = ? AND deleted_at IS NULL GROUP BY mode`
	utils.DB.Raw(modeOutQuery, userID).Scan(&modeOut)
	byMode = append(byMode, modeOut...)

	var totalIn, totalOut float64
	var countIn, countOut int64
	for _, t := range timeline {
		totalIn += t.AmountIn
		totalOut += t.AmountOut
		countIn += t.CountIn
		countOut += t.CountOut
	}

	if timeline == nil {
		timeline = []PeriodRow{}
	}
	if byMode == nil {
		byMode = []ModeRow{}
	}

	c.JSON(http.StatusOK, gin.H{
		"period": period,
		"summary": gin.H{
			"total_in":        totalIn,
			"total_out":       totalOut,
			"net_flow":        totalIn - totalOut,
			"transaction_in":  countIn,
			"transaction_out": countOut,
		},
		"timeline": timeline,
		"by_mode":  byMode,
	})
}

func GetInventoryReport(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	type StockRow struct {
		ProductID    uuid.UUID `json:"product_id"`
		ProductName  string    `json:"product_name"`
		SKU          string    `json:"sku"`
		Category     string    `json:"category"`
		StockQty     float64   `json:"stock_qty"`
		ReservedQty  float64   `json:"reserved_qty"`
		AvailableQty float64   `json:"available_qty"`
		MinStock     float64   `json:"min_stock"`
		CostPrice    float64   `json:"cost_price"`
		SalePrice    float64   `json:"sale_price"`
		TotalValue   float64   `json:"total_value"`
		RetailValue  float64   `json:"retail_value"`
		OutletName   string    `json:"outlet_name"`
		IsLowStock   bool      `json:"is_low_stock"`
		IsOutOfStock bool      `json:"is_out_of_stock"`
	}

	var stocks []models.InventoryStock
	if err := utils.DB.Where("user_id = ?", userID).Preload("Product").Find(&stocks).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch inventory report"})
		return
	}

	var outletIDs []uuid.UUID
	for _, s := range stocks {
		outletIDs = append(outletIDs, s.OutletID)
	}
	warehouseMap := make(map[uuid.UUID]string)
	if len(outletIDs) > 0 {
		var warehouses []models.Warehouse
		utils.DB.Where("id IN ?", outletIDs).Find(&warehouses)
		for _, wh := range warehouses {
			warehouseMap[wh.ID] = wh.Name
		}
	}

	var items []StockRow
	var totalValue, totalRetail, totalQty float64
	var lowStockCount, outOfStockCount int64
	categoryValue := make(map[string]float64)

	for _, stock := range stocks {
		value := stock.Quantity * stock.AverageCost
		retail := stock.Quantity * stock.Product.SalePrice
		totalValue += value
		totalRetail += retail
		totalQty += stock.Quantity
		isLow := stock.Product.LowStockAlert && stock.Quantity <= stock.Product.MinStock
		isOut := stock.Quantity <= 0
		if isLow {
			lowStockCount++
		}
		if isOut {
			outOfStockCount++
		}
		cat := stock.Product.Category
		if cat == "" {
			cat = "Uncategorized"
		}
		categoryValue[cat] += value

		items = append(items, StockRow{
			ProductID:    stock.ProductID,
			ProductName:  stock.Product.Name,
			SKU:          stock.Product.SKU,
			Category:     cat,
			StockQty:     stock.Quantity,
			ReservedQty:  stock.ReservedQty,
			AvailableQty: stock.AvailableQty,
			MinStock:     stock.Product.MinStock,
			CostPrice:    stock.AverageCost,
			SalePrice:    stock.Product.SalePrice,
			TotalValue:   value,
			RetailValue:  retail,
			OutletName:   warehouseMap[stock.OutletID],
			IsLowStock:   isLow,
			IsOutOfStock: isOut,
		})
	}

	type CategoryBreakdown struct {
		Category string  `json:"category"`
		Value    float64 `json:"value"`
	}
	var categories []CategoryBreakdown
	for cat, val := range categoryValue {
		categories = append(categories, CategoryBreakdown{Category: cat, Value: val})
	}

	c.JSON(http.StatusOK, gin.H{
		"summary": gin.H{
			"total_value":        totalValue,
			"total_retail_value": totalRetail,
			"total_quantity":     totalQty,
			"sku_locations":      len(items),
			"low_stock_count":    lowStockCount,
			"out_of_stock_count": outOfStockCount,
		},
		"categories": categories,
		"items":      items,
	})
}

func GetCustomReport(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	metric := c.DefaultQuery("metric", "sales")
	start, end := parseReportDateRange(c)

	type CustomRow struct {
		Label  string  `json:"label"`
		Amount float64 `json:"amount"`
		Count  int64   `json:"count"`
	}

	var rows []CustomRow

	switch metric {
	case "payments_in":
		query := `
			SELECT DATE(date) as label, COALESCE(SUM(amount_received), 0) as amount, COUNT(*) as count
			FROM payments WHERE user_id = ? AND deleted_at IS NULL AND date BETWEEN ? AND ?
			GROUP BY DATE(date) ORDER BY label DESC`
		if err := utils.DB.Raw(query, userID, start, end).Scan(&rows).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to run custom report"})
			return
		}
	case "payments_out":
		query := `
			SELECT DATE(date) as label, COALESCE(SUM(amount_paid), 0) as amount, COUNT(*) as count
			FROM payment_outs WHERE user_id = ? AND deleted_at IS NULL AND date BETWEEN ? AND ?
			GROUP BY DATE(date) ORDER BY label DESC`
		if err := utils.DB.Raw(query, userID, start, end).Scan(&rows).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to run custom report"})
			return
		}
	case "expenses":
		query := `
			SELECT category as label, COALESCE(SUM(amount), 0) as amount, COUNT(*) as count
			FROM expenses WHERE user_id = ? AND deleted_at IS NULL AND date BETWEEN ? AND ?
			GROUP BY category ORDER BY amount DESC`
		if err := utils.DB.Raw(query, userID, start, end).Scan(&rows).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to run custom report"})
			return
		}
	case "purchases":
		query := `
			SELECT DATE(bill_date) as label, COALESCE(SUM(total_amount), 0) as amount, COUNT(*) as count
			FROM purchase_bills WHERE user_id = ? AND deleted_at IS NULL AND bill_date BETWEEN ? AND ?
			GROUP BY DATE(bill_date) ORDER BY label DESC`
		if err := utils.DB.Raw(query, userID, start, end).Scan(&rows).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to run custom report"})
			return
		}
	case "accounts_payable":
		query := `
			SELECT DATE(bill_date) as label,
				COALESCE(SUM(CASE WHEN total_amount > paid_amount THEN total_amount - paid_amount ELSE 0 END), 0) as amount,
				COUNT(*) as count
			FROM purchase_bills WHERE user_id = ? AND deleted_at IS NULL AND bill_date BETWEEN ? AND ?
				AND total_amount > paid_amount
			GROUP BY DATE(bill_date) ORDER BY label DESC`
		if err := utils.DB.Raw(query, userID, start, end).Scan(&rows).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to run custom report"})
			return
		}
	default:
		query := `
			SELECT DATE(date) as label, COALESCE(SUM(total_amount), 0) as amount, COUNT(*) as count
			FROM invoices WHERE user_id = ? AND status = 'paid' AND date BETWEEN ? AND ?
			GROUP BY DATE(date) ORDER BY label DESC`
		if err := utils.DB.Raw(query, userID, start, end).Scan(&rows).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to run custom report"})
			return
		}
	}

	var totalAmount float64
	var totalCount int64
	for _, r := range rows {
		totalAmount += r.Amount
		totalCount += r.Count
	}
	avgAmount := 0.0
	if totalCount > 0 {
		avgAmount = totalAmount / float64(totalCount)
	}

	c.JSON(http.StatusOK, gin.H{
		"metric":       metric,
		"from_date":    start.Format("2006-01-02"),
		"to_date":      end.Format("2006-01-02"),
		"total_amount": totalAmount,
		"total_count":  totalCount,
		"avg_amount":   avgAmount,
		"rows":         rows,
	})
}
