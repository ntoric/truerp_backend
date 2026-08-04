package controllers

import (
	"truerp/models"
	"truerp/utils"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func GetQuotations(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var quotations []models.Quotation
	query := utils.DB.Where("user_id = ?", userID).Preload("Party")

	// Filter by status
	if status := c.Query("status"); status != "" {
		query = query.Where("status = ?", status)
	}
	// Filter by party
	if partyID := c.Query("party_id"); partyID != "" {
		query = query.Where("party_id = ?", partyID)
	}
	// Date range
	if from := c.Query("from"); from != "" {
		query = query.Where("date >= ?", from)
	}
	if to := c.Query("to"); to != "" {
		query = query.Where("date <= ?", to)
	}

	if err := query.Order("date DESC, created_at DESC").Find(&quotations).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch quotations"})
		return
	}

	c.JSON(http.StatusOK, quotations)
}

func GetQuotation(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var quotation models.Quotation
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).
		Preload("Party").
		Preload("Items").
		Preload("Versions").
		First(&quotation).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Quotation not found"})
		return
	}

	c.JSON(http.StatusOK, quotation)
}

func CreateQuotation(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	userName := ""
	if name, exists := c.Get("user_name"); exists {
		userName = name.(string)
	}

	var input struct {
		QuotationNumber   string    `json:"quotation_number" binding:"required"`
		PartyID          uuid.UUID `json:"party_id" binding:"required"`
		Date             time.Time `json:"date" binding:"required"`
		ValidUntil       *time.Time `json:"valid_until"`
		PaymentTerms     int       `json:"payment_terms"`
		Notes            string    `json:"notes"`
		Terms            string    `json:"terms"`
		IsInterState     bool      `json:"is_inter_state"`
		PlaceOfSupply    string    `json:"place_of_supply"`
		ReverseCharge    bool      `json:"reverse_charge"`
		Signature       string    `json:"signature"`
		QuotationDiscount float64  `json:"quotation_discount"`
		AdditionalCharges float64 `json:"additional_charges"`
		Items            []struct {
			Description string  `json:"description"`
			Quantity    float64 `json:"quantity"`
			Unit        string  `json:"unit"`
			UnitPrice   float64 `json:"unit_price"`
			Discount    float64 `json:"discount"`
			TaxRate     float64 `json:"tax_rate"`
			HSNCode     string  `json:"hsn_code"`
			SACCode     string  `json:"sac_code"`
		} `json:"items" binding:"required,min=1"`
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

	quotation := models.Quotation{
		ID:                uuid.New(),
		UserID:            userID,
		QuotationNumber:   input.QuotationNumber,
		PartyID:           input.PartyID,
		Date:              input.Date,
		ValidUntil:        input.ValidUntil,
		PaymentTerms:      input.PaymentTerms,
		Status:            "draft",
		ApprovalStatus:    "pending",
		Notes:             input.Notes,
		Terms:             input.Terms,
		IsInterState:      input.IsInterState,
		PlaceOfSupply:     input.PlaceOfSupply,
		ReverseCharge:     input.ReverseCharge,
		Signature:         input.Signature,
		QuotationDiscount: input.QuotationDiscount,
		AdditionalCharges: input.AdditionalCharges,
		Version:           1,
	}

	// Calculate totals
	var subTotal, discountTotal, taxTotal, cgstTotal, sgstTotal, igstTotal float64
	for _, item := range input.Items {
		itemTotal := item.Quantity * item.UnitPrice
		itemDiscount := itemTotal * (item.Discount / 100)
		taxableAmount := itemTotal - itemDiscount
		itemTax := taxableAmount * (item.TaxRate / 100)

		var cgst, sgst, igst float64
		if input.IsInterState {
			igst = itemTax
		} else {
			cgst = itemTax / 2
			sgst = itemTax / 2
		}

		quotation.Items = append(quotation.Items, models.QuotationItem{
			ID:          uuid.New(),
			Description: item.Description,
			Quantity:    item.Quantity,
			Unit:        item.Unit,
			UnitPrice:   item.UnitPrice,
			Discount:    item.Discount,
			TaxRate:     item.TaxRate,
			CGST:        cgst,
			SGST:        sgst,
			IGST:        igst,
			Total:       taxableAmount + cgst + sgst + igst,
			HSNCode:     item.HSNCode,
			SACCode:     item.SACCode,
		})

		subTotal += itemTotal
		discountTotal += itemDiscount
		taxTotal += itemTax
		cgstTotal += cgst
		sgstTotal += sgst
		igstTotal += igst
	}

	total := subTotal - discountTotal + cgstTotal + sgstTotal + igstTotal - input.QuotationDiscount + input.AdditionalCharges
	roundedTotal := math.Round(total*100) / 100
	roundOff := roundedTotal - total

	quotation.SubTotal = subTotal
	quotation.DiscountTotal = discountTotal
	quotation.QuotationDiscount = input.QuotationDiscount
	quotation.AdditionalCharges = input.AdditionalCharges
	quotation.TaxTotal = taxTotal
	quotation.CGSTTotal = cgstTotal
	quotation.SGSTTotal = sgstTotal
	quotation.IGSTTotal = igstTotal
	quotation.RoundOff = roundOff
	quotation.TotalAmount = roundedTotal

	if err := utils.DB.Create(&quotation).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create quotation"})
		return
	}

	// Create initial version
	quotationData, _ := json.Marshal(quotation)
	version := models.QuotationVersion{
		ID:            uuid.New(),
		QuotationID:   quotation.ID,
		VersionNumber: 1,
		QuotationData: string(quotationData),
		ChangeReason:  "Initial version",
		CreatedBy:     userID,
	}
	utils.DB.Create(&version)

	// Log quotation creation
	CreateAuditLog(
		userID,
		userName,
		"create",
		"quotation",
		&quotation.ID,
		quotation.QuotationNumber,
		fmt.Sprintf("Created quotation: %s for %s - Amount: %.2f", quotation.QuotationNumber, party.Name, quotation.TotalAmount),
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		map[string]interface{}{
			"party_id":     input.PartyID,
			"party_name":   party.Name,
			"total_amount": quotation.TotalAmount,
			"status":       quotation.Status,
		},
		"success",
		"",
	)

	c.JSON(http.StatusCreated, quotation)
}

func UpdateQuotation(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	userName := ""
	if name, exists := c.Get("user_name"); exists {
		userName = name.(string)
	}
	id := c.Param("id")

	var quotation models.Quotation
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&quotation).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Quotation not found"})
		return
	}

	if quotation.Status == "accepted" || quotation.Status == "converted" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot edit accepted or converted quotation"})
		return
	}

	var input struct {
		Date             *time.Time `json:"date"`
		ValidUntil       *time.Time `json:"valid_until"`
		PaymentTerms     int        `json:"payment_terms"`
		Notes            string     `json:"notes"`
		Terms            string     `json:"terms"`
		IsInterState     bool       `json:"is_inter_state"`
		PlaceOfSupply    string     `json:"place_of_supply"`
		ReverseCharge    bool       `json:"reverse_charge"`
		Signature       string     `json:"signature"`
		QuotationDiscount float64   `json:"quotation_discount"`
		AdditionalCharges float64   `json:"additional_charges"`
		Items            []struct {
			ID          *uuid.UUID `json:"id"`
			Description string     `json:"description"`
			Quantity    float64    `json:"quantity"`
			Unit        string     `json:"unit"`
			UnitPrice   float64    `json:"unit_price"`
			Discount    float64    `json:"discount"`
			TaxRate     float64    `json:"tax_rate"`
			HSNCode     string     `json:"hsn_code"`
			SACCode     string     `json:"sac_code"`
		} `json:"items"`
		ChangeReason string `json:"change_reason"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Update fields
	if input.Date != nil {
		quotation.Date = *input.Date
	}
	quotation.ValidUntil = input.ValidUntil
	quotation.PaymentTerms = input.PaymentTerms
	quotation.Notes = input.Notes
	quotation.Terms = input.Terms
	quotation.IsInterState = input.IsInterState
	quotation.PlaceOfSupply = input.PlaceOfSupply
	quotation.ReverseCharge = input.ReverseCharge
	quotation.Signature = input.Signature
	quotation.QuotationDiscount = input.QuotationDiscount
	quotation.AdditionalCharges = input.AdditionalCharges

	// Recalculate if items provided
	if len(input.Items) > 0 {
		// Delete existing items
		utils.DB.Where("quotation_id = ?", quotation.ID).Delete(&models.QuotationItem{})

		var subTotal, discountTotal, taxTotal, cgstTotal, sgstTotal, igstTotal float64
		for _, item := range input.Items {
			itemTotal := item.Quantity * item.UnitPrice
			itemDiscount := itemTotal * (item.Discount / 100)
			taxableAmount := itemTotal - itemDiscount
			itemTax := taxableAmount * (item.TaxRate / 100)

			var cgst, sgst, igst float64
			if quotation.IsInterState {
				igst = itemTax
			} else {
				cgst = itemTax / 2
				sgst = itemTax / 2
			}

			quotation.Items = append(quotation.Items, models.QuotationItem{
				ID:          uuid.New(),
				QuotationID: quotation.ID,
				Description: item.Description,
				Quantity:    item.Quantity,
				Unit:        item.Unit,
				UnitPrice:   item.UnitPrice,
				Discount:    item.Discount,
				TaxRate:     item.TaxRate,
				CGST:        cgst,
				SGST:        sgst,
				IGST:        igst,
				Total:       taxableAmount + cgst + sgst + igst,
				HSNCode:     item.HSNCode,
				SACCode:     item.SACCode,
			})

			subTotal += itemTotal
			discountTotal += itemDiscount
			taxTotal += itemTax
			cgstTotal += cgst
			sgstTotal += sgst
			igstTotal += igst
		}

		total := subTotal - discountTotal + cgstTotal + sgstTotal + igstTotal - quotation.QuotationDiscount + quotation.AdditionalCharges
		roundedTotal := math.Round(total*100) / 100
		roundOff := roundedTotal - total

		quotation.SubTotal = subTotal
		quotation.DiscountTotal = discountTotal
		quotation.TaxTotal = taxTotal
		quotation.CGSTTotal = cgstTotal
		quotation.SGSTTotal = sgstTotal
		quotation.IGSTTotal = igstTotal
		quotation.RoundOff = roundOff
		quotation.TotalAmount = roundedTotal
	}

	quotation.Version += 1

	if err := utils.DB.Save(&quotation).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update quotation"})
		return
	}

	// Create new version
	quotationData, _ := json.Marshal(quotation)
	version := models.QuotationVersion{
		ID:            uuid.New(),
		QuotationID:   quotation.ID,
		VersionNumber: quotation.Version,
		QuotationData: string(quotationData),
		ChangeReason:  input.ChangeReason,
		CreatedBy:     userID,
	}
	utils.DB.Create(&version)

	// Log quotation update
	CreateAuditLog(
		userID,
		userName,
		"update",
		"quotation",
		&quotation.ID,
		quotation.QuotationNumber,
		fmt.Sprintf("Updated quotation: %s - Version: %d - Reason: %s", quotation.QuotationNumber, quotation.Version, input.ChangeReason),
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		map[string]interface{}{
			"version": quotation.Version,
			"change_reason": input.ChangeReason,
		},
		"success",
		"",
	)

	c.JSON(http.StatusOK, quotation)
}

func DeleteQuotation(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	userName := ""
	if name, exists := c.Get("user_name"); exists {
		userName = name.(string)
	}
	id := c.Param("id")

	var quotation models.Quotation
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&quotation).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Quotation not found"})
		return
	}

	if quotation.Status == "accepted" || quotation.Status == "converted" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot delete accepted or converted quotation"})
		return
	}

	if err := utils.DB.Delete(&quotation).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete quotation"})
		return
	}

	// Log quotation deletion
	CreateAuditLog(
		userID,
		userName,
		"delete",
		"quotation",
		&quotation.ID,
		quotation.QuotationNumber,
		fmt.Sprintf("Deleted quotation: %s - Amount: %.2f", quotation.QuotationNumber, quotation.TotalAmount),
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		map[string]interface{}{
			"total_amount": quotation.TotalAmount,
			"status":       quotation.Status,
		},
		"success",
		"",
	)

	c.JSON(http.StatusOK, gin.H{"message": "Quotation deleted successfully"})
}

func GetNextQuotationNumber(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var count int64
	utils.DB.Model(&models.Quotation{}).Where("user_id = ?", userID).Count(&count)

	nextNum := fmt.Sprintf("QUOT-%04d", count+1)
	c.JSON(http.StatusOK, gin.H{"quotation_number": nextNum})
}

func ApproveQuotation(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var input struct {
		Approved bool   `json:"approved"`
		Notes    string `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var quotation models.Quotation
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&quotation).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Quotation not found"})
		return
	}

	now := time.Now()
	approvalStatus := "approved"
	if !input.Approved {
		approvalStatus = "rejected"
	}

	quotation.ApprovalStatus = approvalStatus
	quotation.ApprovedBy = &userID
	quotation.ApprovedAt = &now

	if err := utils.DB.Save(&quotation).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update quotation approval status"})
		return
	}

	c.JSON(http.StatusOK, quotation)
}

func ConvertToInvoice(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var quotation models.Quotation
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).
		Preload("Party").
		Preload("Items").
		First(&quotation).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Quotation not found"})
		return
	}

	if quotation.Status != "accepted" && quotation.ApprovalStatus != "approved" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Quotation must be approved before converting to invoice"})
		return
	}

	if quotation.Status == "converted" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Quotation already converted to invoice"})
		return
	}

	// Get next invoice number
	var count int64
	utils.DB.Model(&models.Invoice{}).Where("user_id = ?", userID).Count(&count)
	invoiceNumber := fmt.Sprintf("INV-%04d", count+1)

	// Create invoice from quotation
	invoice := models.Invoice{
		ID:                uuid.New(),
		UserID:            userID,
		InvoiceNumber:     invoiceNumber,
		InvoiceType:       "tax_invoice",
		PartyID:           quotation.PartyID,
		Date:              time.Now(),
		PaymentTerms:      quotation.PaymentTerms,
		Status:            "draft",
		SubTotal:          quotation.SubTotal,
		DiscountTotal:     quotation.DiscountTotal,
		InvoiceDiscount:   quotation.QuotationDiscount,
		AdditionalCharges: quotation.AdditionalCharges,
		TaxTotal:          quotation.TaxTotal,
		CGSTTotal:         quotation.CGSTTotal,
		SGSTTotal:         quotation.SGSTTotal,
		IGSTTotal:         quotation.IGSTTotal,
		RoundOff:          quotation.RoundOff,
		TotalAmount:       quotation.TotalAmount,
		Notes:             quotation.Notes,
		Terms:             quotation.Terms,
		IsInterState:      quotation.IsInterState,
		PlaceOfSupply:     quotation.PlaceOfSupply,
		ReverseCharge:     quotation.ReverseCharge,
		Signature:         quotation.Signature,
	}

	// Copy items
	for _, qItem := range quotation.Items {
		invoice.Items = append(invoice.Items, models.InvoiceItem{
			ID:          uuid.New(),
			Description: qItem.Description,
			Quantity:    qItem.Quantity,
			Unit:        qItem.Unit,
			UnitPrice:   qItem.UnitPrice,
			Discount:    qItem.Discount,
			TaxRate:     qItem.TaxRate,
			CGST:        qItem.CGST,
			SGST:        qItem.SGST,
			IGST:        qItem.IGST,
			Total:       qItem.Total,
			HSNCode:     qItem.HSNCode,
			SACCode:     qItem.SACCode,
		})
	}

	if err := utils.DB.Create(&invoice).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create invoice from quotation"})
		return
	}

	// Update quotation status
	now := time.Now()
	quotation.Status = "converted"
	quotation.ConvertedToInvoiceID = &invoice.ID
	quotation.ConvertedAt = &now
	utils.DB.Save(&quotation)

	c.JSON(http.StatusCreated, invoice)
}

func GetQuotationVersions(c *gin.Context) {
	quotationID := c.Param("id")

	var versions []models.QuotationVersion
	if err := utils.DB.Where("quotation_id = ?", quotationID).Order("version_number DESC").Find(&versions).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch quotation versions"})
		return
	}

	c.JSON(http.StatusOK, versions)
}

func SendQuotation(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var quotation models.Quotation
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&quotation).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Quotation not found"})
		return
	}

	quotation.Status = "sent"
	if err := utils.DB.Save(&quotation).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update quotation status"})
		return
	}

	c.JSON(http.StatusOK, quotation)
}

func AcceptQuotation(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var quotation models.Quotation
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&quotation).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Quotation not found"})
		return
	}

	quotation.Status = "accepted"
	if err := utils.DB.Save(&quotation).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update quotation status"})
		return
	}

	c.JSON(http.StatusOK, quotation)
}

func RejectQuotation(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var input struct {
		Reason string `json:"reason"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var quotation models.Quotation
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&quotation).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Quotation not found"})
		return
	}

	quotation.Status = "rejected"
	quotation.Notes = input.Reason
	if err := utils.DB.Save(&quotation).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update quotation status"})
		return
	}

	c.JSON(http.StatusOK, quotation)
}

func GenerateQuotationPDF(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var quotation models.Quotation
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).
		Preload("Party").
		Preload("Items").
		First(&quotation).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Quotation not found"})
		return
	}

	// Generate HTML for PDF
	html := fmt.Sprintf(`
<!DOCTYPE html>
<html>
<head>
	<meta charset="UTF-8">
	<title>Quotation - %s</title>
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
		.quotation-number {
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
		.footer {
			margin-top: 40px;
			padding-top: 20px;
			border-top: 1px solid #ddd;
		}
		.terms {
			font-size: 12px;
			color: #666;
		}
		.status {
			display: inline-block;
			padding: 5px 10px;
			border-radius: 4px;
			font-size: 12px;
			font-weight: bold;
			text-transform: uppercase;
		}
		.status-draft { background-color: #f3f4f6; color: #666; }
		.status-sent { background-color: #dbeafe; color: #1e40af; }
		.status-accepted { background-color: #d1fae5; color: #065f46; }
		.status-rejected { background-color: #fee2e2; color: #991b1b; }
		.status-converted { background-color: #fef3c7; color: #92400e; }
		.expiry {
			color: #dc2626;
			font-weight: bold;
		}
		@media print {
			body { margin: 0; padding: 10px; }
		}
	</style>
</head>
<body>
	<div class="header">
		<div>
			<div class="title">QUOTATION</div>
			<div class="quotation-number">%s</div>
		</div>
		<div>
			<span class="status status-%s">%s</span>
		</div>
	</div>

	<div class="section">
		<div class="section-title">Bill To</div>
		<div class="info-item">
			<strong>%s</strong><br>
			%s<br>
			%s<br>
			GSTIN: %s
		</div>
	</div>

	<div class="section">
		<div class="section-title">Quotation Details</div>
		<div class="info-grid">
			<div class="info-item">
				<span class="info-label">Date:</span> %s
			</div>
			<div class="info-item">
				<span class="info-label">Valid Until:</span> <span class="expiry">%s</span>
			</div>
			<div class="info-item">
				<span class="info-label">Payment Terms:</span> %d days
			</div>
			<div class="info-item">
				<span class="info-label">Place of Supply:</span> %s
			</div>
		</div>
	</div>

	<div class="section">
		<div class="section-title">Items</div>
		<table>
			<thead>
				<tr>
					<th>Description</th>
					<th>Quantity</th>
					<th>Unit</th>
					<th>Unit Price</th>
					<th>Discount %%</th>
					<th>Tax %%</th>
					<th>Total</th>
				</tr>
			</thead>
			<tbody>
				%s
			</tbody>
		</table>
	</div>

	<div class="totals">
		<div class="total-row">
			<span class="total-label">Sub Total:</span>
			<span class="total-value">₹%.2f</span>
		</div>
		<div class="total-row">
			<span class="total-label">Discount:</span>
			<span class="total-value">-₹%.2f</span>
		</div>
		<div class="total-row">
			<span class="total-label">Tax Total:</span>
			<span class="total-value">₹%.2f</span>
		</div>
		<div class="total-row">
			<span class="total-label">Additional Charges:</span>
			<span class="total-value">₹%.2f</span>
		</div>
		<div class="total-row">
			<span class="total-label">Round Off:</span>
			<span class="total-value">₹%.2f</span>
		</div>
		<div class="total-row grand-total">
			<span class="total-label">Grand Total:</span>
			<span class="total-value">₹%.2f</span>
		</div>
	</div>

	<div class="footer">
		<div class="section-title">Terms & Conditions</div>
		<div class="terms">%s</div>
		%s
	</div>

	<script>
		window.onload = function() {
			window.print();
		};
	</script>
</body>
</html>`,
		quotation.QuotationNumber,
		quotation.QuotationNumber,
		quotation.Status,
		strings.ToUpper(quotation.Status),
		quotation.Party.Name,
		quotation.Party.Address,
		fmt.Sprintf("%s, %s - %s", quotation.Party.City, quotation.Party.State, quotation.Party.Pincode),
		quotation.Party.GSTIN,
		quotation.Date.Format("02-01-2006"),
		func() string {
			if quotation.ValidUntil != nil {
				return quotation.ValidUntil.Format("02-01-2006")
			}
			return "N/A"
		}(),
		quotation.PaymentTerms,
		quotation.PlaceOfSupply,
		func() string {
			var rows string
			for _, item := range quotation.Items {
				rows += fmt.Sprintf(`<tr>
					<td>%s</td>
					<td>%.2f</td>
					<td>%s</td>
					<td>₹%.2f</td>
					<td>%.2f%%</td>
					<td>%.2f%%</td>
					<td>₹%.2f</td>
				</tr>`, item.Description, item.Quantity, item.Unit, item.UnitPrice, item.Discount, item.TaxRate, item.Total)
			}
			return rows
		}(),
		quotation.SubTotal,
		quotation.DiscountTotal + quotation.QuotationDiscount,
		quotation.TaxTotal,
		quotation.AdditionalCharges,
		quotation.RoundOff,
		quotation.TotalAmount,
		quotation.Terms,
		func() string {
			if quotation.Notes != "" {
				return fmt.Sprintf(`<div class="section-title" style="margin-top: 20px;">Notes</div>
				<div class="terms">%s</div>`, quotation.Notes)
			}
			return ""
		}(),
	)

	c.Header("Content-Type", "text/html")
	c.String(http.StatusOK, html)
}
