package controllers

import (
	"truerp/models"
	"truerp/utils"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type AgingBucket struct {
	Current   float64 `json:"current"`
	Days1_30  float64 `json:"days_1_30"`
	Days31_60 float64 `json:"days_31_60"`
	Days61_90 float64 `json:"days_61_90"`
	Days90Plus float64 `json:"days_90_plus"`
}

func GenerateCustomerStatement(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		PartyID   uuid.UUID `json:"party_id" binding:"required"`
		FromDate  time.Time `json:"from_date" binding:"required"`
		ToDate    time.Time `json:"to_date" binding:"required"`
		Notes     string    `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Validate party
	var party models.Party
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, input.PartyID).First(&party).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid party"})
		return
	}

	// Calculate opening balance (transactions before from_date)
	var openingBalance float64
	utils.DB.Raw(`
		SELECT COALESCE(SUM(total_amount), 0) - COALESCE(SUM(amount_paid), 0) as balance
		FROM invoices
		WHERE user_id = ? AND party_id = ? AND date < ? AND status != 'cancelled'
	`, userID, input.PartyID, input.FromDate).Scan(&openingBalance)

	// Get next statement number
	var count int64
	utils.DB.Model(&models.CustomerStatement{}).Where("user_id = ?", userID).Count(&count)
	statementNumber := fmt.Sprintf("STMT-%04d", count+1)

	// Create statement
	statement := models.CustomerStatement{
		ID:              uuid.New(),
		UserID:          userID,
		PartyID:         input.PartyID,
		StatementNumber: statementNumber,
		FromDate:        input.FromDate,
		ToDate:          input.ToDate,
		OpeningBalance:  openingBalance,
		Notes:           input.Notes,
		GeneratedAt:     time.Now(),
	}

	// Fetch transactions within date range
	var transactions []models.StatementTransaction
	var runningBalance = openingBalance

	// Get invoices
	var invoices []models.Invoice
	utils.DB.Where("user_id = ? AND party_id = ? AND date >= ? AND date <= ? AND status != 'cancelled'",
		userID, input.PartyID, input.FromDate, input.ToDate).
		Order("date ASC, invoice_number ASC").
		Find(&invoices)

	for _, inv := range invoices {
		runningBalance += inv.TotalAmount
		transactions = append(transactions, models.StatementTransaction{
			ID:              uuid.New(),
			TransactionType: "invoice",
			ReferenceID:     inv.ID,
			ReferenceNumber: inv.InvoiceNumber,
			Date:            inv.Date,
			Description:     fmt.Sprintf("Invoice %s", inv.InvoiceNumber),
			Debit:           inv.TotalAmount,
			Credit:          0,
			Balance:         runningBalance,
			DueDate:         inv.DueDate,
			IsOverdue:       inv.DueDate != nil && time.Now().After(*inv.DueDate),
		})
		statement.TotalInvoices += inv.TotalAmount
	}

	// Get payments
	var payments []models.Payment
	utils.DB.Where("user_id = ? AND party_id = ? AND date >= ? AND date <= ?",
		userID, input.PartyID, input.FromDate, input.ToDate).
		Order("date ASC, payment_in_number ASC").
		Find(&payments)

	for _, pay := range payments {
		runningBalance -= pay.AmountReceived
		transactions = append(transactions, models.StatementTransaction{
			ID:              uuid.New(),
			TransactionType: "payment",
			ReferenceID:     pay.ID,
			ReferenceNumber: pay.PaymentInNumber,
			Date:            pay.Date,
			Description:     fmt.Sprintf("Payment %s", pay.PaymentInNumber),
			Debit:           0,
			Credit:          pay.AmountReceived,
			Balance:         runningBalance,
		})
		statement.TotalPayments += pay.AmountReceived
	}

	// Get credit notes (sales returns)
	var salesReturns []models.SalesReturn
	utils.DB.Where("user_id = ? AND party_id = ? AND date >= ? AND date <= ? AND status != 'cancelled'",
		userID, input.PartyID, input.FromDate, input.ToDate).
		Order("date ASC, return_number ASC").
		Find(&salesReturns)

	for _, sr := range salesReturns {
		runningBalance -= sr.Amount
		transactions = append(transactions, models.StatementTransaction{
			ID:              uuid.New(),
			TransactionType: "credit_note",
			ReferenceID:     sr.ID,
			ReferenceNumber: sr.ReturnNumber,
			Date:            sr.Date,
			Description:     fmt.Sprintf("Credit Note %s", sr.ReturnNumber),
			Debit:           0,
			Credit:          sr.Amount,
			Balance:         runningBalance,
		})
		statement.TotalCredits += sr.Amount
	}

	statement.Transactions = transactions
	statement.TotalDebits = statement.TotalInvoices
	statement.ClosingBalance = runningBalance

	if err := utils.DB.Create(&statement).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create statement"})
		return
	}

	// Create transactions
	for i := range transactions {
		transactions[i].StatementID = statement.ID
		utils.DB.Create(&transactions[i])
	}

	c.JSON(http.StatusCreated, statement)
}

func GetCustomerStatements(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var statements []models.CustomerStatement
	query := utils.DB.Where("user_id = ?", userID).Preload("Party")

	if partyID := c.Query("party_id"); partyID != "" {
		query = query.Where("party_id = ?", partyID)
	}

	if err := query.Order("generated_at DESC").Find(&statements).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch statements"})
		return
	}

	c.JSON(http.StatusOK, statements)
}

func GetCustomerStatement(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var statement models.CustomerStatement
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).
		Preload("Party").
		Preload("Transactions").
		First(&statement).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Statement not found"})
		return
	}

	c.JSON(http.StatusOK, statement)
}

func GetAgingReport(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	partyID := c.Query("party_id")

	var invoices []models.Invoice
	query := utils.DB.Where("user_id = ? AND status != 'cancelled' AND status != 'paid'", userID).
		Preload("Party")

	if partyID != "" {
		query = query.Where("party_id = ?", partyID)
	}

	if err := query.Find(&invoices).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch invoices"})
		return
	}

	now := time.Now()
	type AgingSummary struct {
		PartyID       uuid.UUID  `json:"party_id"`
		PartyName     string     `json:"party_name"`
		TotalOutstanding float64 `json:"total_outstanding"`
		Aging         AgingBucket `json:"aging"`
	}

	var summaries []AgingSummary
	partyMap := make(map[uuid.UUID]*AgingSummary)

	for _, inv := range invoices {
		daysOverdue := 0
		if inv.DueDate != nil && now.After(*inv.DueDate) {
			daysOverdue = int(now.Sub(*inv.DueDate).Hours() / 24)
		}

		outstanding := inv.TotalAmount - inv.AmountPaid

		if _, exists := partyMap[inv.PartyID]; !exists {
			partyMap[inv.PartyID] = &AgingSummary{
				PartyID:   inv.PartyID,
				PartyName: inv.Party.Name,
				Aging:     AgingBucket{},
			}
		}

		summary := partyMap[inv.PartyID]
		summary.TotalOutstanding += outstanding

		if daysOverdue <= 0 {
			summary.Aging.Current += outstanding
		} else if daysOverdue <= 30 {
			summary.Aging.Days1_30 += outstanding
		} else if daysOverdue <= 60 {
			summary.Aging.Days31_60 += outstanding
		} else if daysOverdue <= 90 {
			summary.Aging.Days61_90 += outstanding
		} else {
			summary.Aging.Days90Plus += outstanding
		}
	}

	for _, summary := range partyMap {
		summaries = append(summaries, *summary)
	}

	c.JSON(http.StatusOK, gin.H{
		"generated_at": now,
		"aging_report": summaries,
	})
}

func GetPartyAging(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	partyID := c.Param("id")

	var party models.Party
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, partyID).First(&party).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Party not found"})
		return
	}

	var invoices []models.Invoice
	utils.DB.Where("user_id = ? AND party_id = ? AND status != 'cancelled'", userID, partyID).
		Find(&invoices)

	now := time.Now()
	aging := AgingBucket{}
	totalOutstanding := 0.0

	for _, inv := range invoices {
		outstanding := inv.TotalAmount - inv.AmountPaid
		if outstanding <= 0 {
			continue
		}

		totalOutstanding += outstanding

		daysOverdue := 0
		if inv.DueDate != nil && now.After(*inv.DueDate) {
			daysOverdue = int(now.Sub(*inv.DueDate).Hours() / 24)
		}

		if daysOverdue <= 0 {
			aging.Current += outstanding
		} else if daysOverdue <= 30 {
			aging.Days1_30 += outstanding
		} else if daysOverdue <= 60 {
			aging.Days31_60 += outstanding
		} else if daysOverdue <= 90 {
			aging.Days61_90 += outstanding
		} else {
			aging.Days90Plus += outstanding
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"party_id":          partyID,
		"party_name":        party.Name,
		"total_outstanding": totalOutstanding,
		"aging":             aging,
	})
}

func GenerateStatementPDF(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var statement models.CustomerStatement
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).
		Preload("Party").
		Preload("Transactions").
		First(&statement).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Statement not found"})
		return
	}

	// Generate HTML for PDF
	html := StatementPDFHTML(statement)
	c.Header("Content-Type", "text/html")
	c.String(http.StatusOK, html)
}

func StatementPDFHTML(statement models.CustomerStatement) string {
	return fmt.Sprintf(`
<!DOCTYPE html>
<html>
<head>
	<meta charset="UTF-8">
	<title>Customer Statement - %s</title>
	<style>
		body {
			font-family: Arial, sans-serif;
			margin: 0;
			padding: 20px;
			color: #333;
		}
		.header {
			display: flex;
			justify-content: space-between;
			margin-bottom: 30px;
		}
		.title {
			font-size: 24px;
			font-weight: bold;
			color: #2563eb;
		}
		.statement-number {
			font-size: 18px;
			color: #666;
		}
		.section {
			margin-bottom: 20px;
		}
		.section-title {
			font-size: 14px;
			font-weight: bold;
			color: #666;
			margin-bottom: 10px;
		}
		.info-grid {
			display: grid;
			grid-template-columns: 1fr 1fr;
			gap: 10px;
		}
		.info-item {
			margin-bottom: 5px;
		}
		.info-label {
			font-weight: bold;
			color: #666;
		}
		table {
			width: 100%%;
			border-collapse: collapse;
			margin-top: 10px;
		}
		th, td {
			border: 1px solid #ddd;
			padding: 10px;
			text-align: left;
		}
		th {
			background-color: #f3f4f6;
			font-weight: bold;
		}
		.totals {
			margin-top: 20px;
			text-align: right;
		}
		.total-row {
			display: flex;
			justify-content: flex-end;
			margin-bottom: 5px;
		}
		.total-label {
			width: 150px;
			font-weight: bold;
		}
		.total-value {
			width: 100px;
		}
		.grand-total {
			font-size: 18px;
			font-weight: bold;
			color: #2563eb;
		}
		.overdue {
			color: #dc2626;
			font-weight: bold;
		}
		.debit {
			color: #dc2626;
		}
		.credit {
			color: #16a34a;
		}
		@media print {
			body { margin: 0; padding: 10px; }
		}
	</style>
</head>
<body>
	<div class="header">
		<div>
			<div class="title">CUSTOMER STATEMENT</div>
			<div class="statement-number">%s</div>
		</div>
		<div>
			<div>Generated: %s</div>
		</div>
	</div>

	<div class="section">
		<div class="section-title">Customer Details</div>
		<div class="info-item">
			<strong>%s</strong><br>
			%s<br>
			%s<br>
			GSTIN: %s
		</div>
	</div>

	<div class="section">
		<div class="section-title">Statement Period</div>
		<div class="info-grid">
			<div class="info-item">
				<span class="info-label">From:</span> %s
			</div>
			<div class="info-item">
				<span class="info-label">To:</span> %s
			</div>
		</div>
	</div>

	<div class="section">
		<div class="section-title">Transactions</div>
		<table>
			<thead>
				<tr>
					<th>Date</th>
					<th>Type</th>
					<th>Reference</th>
					<th>Description</th>
					<th>Debit</th>
					<th>Credit</th>
					<th>Balance</th>
				</tr>
			</thead>
			<tbody>
				%s
			</tbody>
		</table>
	</div>

	<div class="totals">
		<div class="total-row">
			<span class="total-label">Opening Balance:</span>
			<span class="total-value">₹%.2f</span>
		</div>
		<div class="total-row">
			<span class="total-label">Total Invoices:</span>
			<span class="total-value">₹%.2f</span>
		</div>
		<div class="total-row">
			<span class="total-label">Total Payments:</span>
			<span class="total-value">₹%.2f</span>
		</div>
		<div class="total-row">
			<span class="total-label">Total Credits:</span>
			<span class="total-value">₹%.2f</span>
		</div>
		<div class="total-row grand-total">
			<span class="total-label">Closing Balance:</span>
			<span class="total-value">₹%.2f</span>
		</div>
	</div>

	%s

	<script>
		window.onload = function() {
			window.print();
		};
	</script>
</body>
</html>`,
		statement.StatementNumber,
		statement.StatementNumber,
		statement.GeneratedAt.Format("02-01-2006 15:04"),
		statement.Party.Name,
		statement.Party.Address,
		fmt.Sprintf("%s, %s - %s", statement.Party.City, statement.Party.State, statement.Party.Pincode),
		statement.Party.GSTIN,
		statement.FromDate.Format("02-01-2006"),
		statement.ToDate.Format("02-01-2006"),
		func() string {
			var rows string
			for _, tx := range statement.Transactions {
				overdueClass := ""
				if tx.IsOverdue {
					overdueClass = " class='overdue'"
				}
				rows += fmt.Sprintf(`<tr%s>
					<td>%s</td>
					<td>%s</td>
					<td>%s</td>
					<td>%s</td>
					<td class="debit">%s</td>
					<td class="credit">%s</td>
					<td>₹%.2f</td>
				</tr>`,
					overdueClass,
					tx.Date.Format("02-01-2006"),
					strings.ToUpper(tx.TransactionType),
					tx.ReferenceNumber,
					tx.Description,
					func() string {
						if tx.Debit > 0 {
							return fmt.Sprintf("₹%.2f", tx.Debit)
						}
						return "-"
					}(),
					func() string {
						if tx.Credit > 0 {
							return fmt.Sprintf("₹%.2f", tx.Credit)
						}
						return "-"
					}(),
					tx.Balance,
				)
			}
			return rows
		}(),
		statement.OpeningBalance,
		statement.TotalInvoices,
		statement.TotalPayments,
		statement.TotalCredits,
		statement.ClosingBalance,
		func() string {
			if statement.Notes != "" {
				return fmt.Sprintf(`<div class="section" style="margin-top: 40px;">
				<div class="section-title">Notes</div>
				<div style="font-size: 12px; color: #666;">%s</div>
			</div>`, statement.Notes)
			}
			return ""
		}(),
	)
}

func DeleteCustomerStatement(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Delete(&models.CustomerStatement{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete statement"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Statement deleted successfully"})
}
