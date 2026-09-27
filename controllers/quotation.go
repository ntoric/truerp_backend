package controllers

import (
	"encoding/json"
	"fmt"
	"html"
	"math"
	"net/http"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

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
		QuotationNumber       string                    `json:"quotation_number"`
		PartyID               uuid.UUID                 `json:"party_id" binding:"required"`
		Date                  time.Time                 `json:"date" binding:"required"`
		ValidUntil            *time.Time                `json:"valid_until"`
		PaymentTerms          int                       `json:"payment_terms"`
		Notes                 string                    `json:"notes"`
		Terms                 string                    `json:"terms"`
		IsInterState          bool                      `json:"is_inter_state"`
		PlaceOfSupply         string                    `json:"place_of_supply"`
		ReverseCharge         bool                      `json:"reverse_charge"`
		Signature             string                    `json:"signature"`
		QuotationDiscount     float64                   `json:"quotation_discount"`
		AdditionalCharges     float64                   `json:"additional_charges"`
		AdditionalChargeItems []models.AdditionalCharge `json:"additional_charge_items"`
		Items                 []struct {
			ProductID   *uuid.UUID           `json:"product_id"`
			Description string               `json:"description"`
			Quantity    models.FlexibleFloat `json:"quantity"`
			Unit        string               `json:"unit"`
			UnitPrice   models.FlexibleFloat `json:"unit_price"`
			Discount    models.FlexibleFloat `json:"discount"`
			TaxRate     models.FlexibleFloat `json:"tax_rate"`
			HSNCode     string               `json:"hsn_code"`
			SACCode     string               `json:"sac_code"`
			BatchNo     string               `json:"batch_no"`
			ExpDate     *models.FlexibleTime `json:"exp_date"`
		} `json:"items" binding:"required,min=1"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	chargeItems, err := models.NormalizeAdditionalCharges(input.AdditionalChargeItems)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(chargeItems) > 0 {
		input.AdditionalCharges = models.SumAdditionalCharges(chargeItems)
	}

	// Validate party
	var party models.Party
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, input.PartyID).First(&party).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid party"})
		return
	}

	input.QuotationNumber = strings.TrimSpace(input.QuotationNumber)
	if input.QuotationNumber == "" {
		input.QuotationNumber = allocateUniqueQuotationNumber(userID)
	} else if quotationNumberInUse(userID, input.QuotationNumber, uuid.Nil) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Estimate number already in use"})
		return
	}

	quotation := models.Quotation{
		ID:                    uuid.New(),
		UserID:                userID,
		QuotationNumber:       input.QuotationNumber,
		PartyID:               input.PartyID,
		Date:                  input.Date,
		ValidUntil:            input.ValidUntil,
		PaymentTerms:          input.PaymentTerms,
		Status:                "draft",
		ApprovalStatus:        "pending",
		Notes:                 input.Notes,
		Terms:                 input.Terms,
		IsInterState:          input.IsInterState,
		PlaceOfSupply:         input.PlaceOfSupply,
		ReverseCharge:         input.ReverseCharge,
		Signature:             input.Signature,
		QuotationDiscount:     input.QuotationDiscount,
		AdditionalCharges:     input.AdditionalCharges,
		AdditionalChargeItems: chargeItems,
		Version:               1,
	}

	// Calculate totals
	var subTotal, discountTotal, taxTotal, cgstTotal, sgstTotal, igstTotal float64
	for _, item := range input.Items {
		qty := item.Quantity.Float64()
		unitPrice := item.UnitPrice.Float64()
		discount := item.Discount.Float64()
		taxRate := item.TaxRate.Float64()

		itemTotal := qty * unitPrice
		itemDiscount := itemTotal * (discount / 100)
		taxableAmount := itemTotal - itemDiscount
		itemTax := taxableAmount * (taxRate / 100)

		var cgst, sgst, igst float64
		if input.IsInterState {
			igst = itemTax
		} else {
			cgst = itemTax / 2
			sgst = itemTax / 2
		}

		quotation.Items = append(quotation.Items, models.QuotationItem{
			ID:          uuid.New(),
			ProductID:   item.ProductID,
			Description: item.Description,
			Quantity:    qty,
			Unit:        item.Unit,
			UnitPrice:   unitPrice,
			Discount:    discount,
			TaxRate:     taxRate,
			CGST:        cgst,
			SGST:        sgst,
			IGST:        igst,
			Total:       taxableAmount + cgst + sgst + igst,
			HSNCode:     item.HSNCode,
			SACCode:     item.SACCode,
			BatchNo:     strings.TrimSpace(item.BatchNo),
			ExpDate:     item.ExpDate.Ptr(),
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
		QuotationNumber       string                    `json:"quotation_number"`
		Date                  *time.Time                `json:"date"`
		ValidUntil            *time.Time                `json:"valid_until"`
		PaymentTerms          int                       `json:"payment_terms"`
		Notes                 string                    `json:"notes"`
		Terms                 string                    `json:"terms"`
		IsInterState          bool                      `json:"is_inter_state"`
		PlaceOfSupply         string                    `json:"place_of_supply"`
		ReverseCharge         bool                      `json:"reverse_charge"`
		Signature             string                    `json:"signature"`
		QuotationDiscount     float64                   `json:"quotation_discount"`
		AdditionalCharges     float64                   `json:"additional_charges"`
		AdditionalChargeItems []models.AdditionalCharge `json:"additional_charge_items"`
		Items                 []struct {
			ID          *uuid.UUID           `json:"id"`
			ProductID   *uuid.UUID           `json:"product_id"`
			Description string               `json:"description"`
			Quantity    models.FlexibleFloat `json:"quantity"`
			Unit        string               `json:"unit"`
			UnitPrice   models.FlexibleFloat `json:"unit_price"`
			Discount    models.FlexibleFloat `json:"discount"`
			TaxRate     models.FlexibleFloat `json:"tax_rate"`
			HSNCode     string               `json:"hsn_code"`
			SACCode     string               `json:"sac_code"`
			BatchNo     string               `json:"batch_no"`
			ExpDate     *models.FlexibleTime `json:"exp_date"`
		} `json:"items"`
		ChangeReason string `json:"change_reason"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	chargeItems, err := models.NormalizeAdditionalCharges(input.AdditionalChargeItems)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Update fields
	if num := strings.TrimSpace(input.QuotationNumber); num != "" && num != quotation.QuotationNumber {
		if quotationNumberInUse(userID, num, quotation.ID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Estimate number already in use"})
			return
		}
		quotation.QuotationNumber = num
	}
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
	if input.AdditionalChargeItems != nil {
		if len(chargeItems) == 0 {
			quotation.AdditionalChargeItems = nil
		} else {
			quotation.AdditionalChargeItems = chargeItems
		}
		quotation.AdditionalCharges = models.SumAdditionalCharges(chargeItems)
	} else {
		quotation.AdditionalCharges = input.AdditionalCharges
	}

	// Recalculate if items provided
	if len(input.Items) > 0 {
		// Delete existing items
		utils.DB.Where("quotation_id = ?", quotation.ID).Delete(&models.QuotationItem{})

		var subTotal, discountTotal, taxTotal, cgstTotal, sgstTotal, igstTotal float64
		for _, item := range input.Items {
			qty := item.Quantity.Float64()
			unitPrice := item.UnitPrice.Float64()
			discount := item.Discount.Float64()
			taxRate := item.TaxRate.Float64()

			itemTotal := qty * unitPrice
			itemDiscount := itemTotal * (discount / 100)
			taxableAmount := itemTotal - itemDiscount
			itemTax := taxableAmount * (taxRate / 100)

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
				ProductID:   item.ProductID,
				Description: item.Description,
				Quantity:    qty,
				Unit:        item.Unit,
				UnitPrice:   unitPrice,
				Discount:    discount,
				TaxRate:     taxRate,
				CGST:        cgst,
				SGST:        sgst,
				IGST:        igst,
				Total:       taxableAmount + cgst + sgst + igst,
				HSNCode:     item.HSNCode,
				SACCode:     item.SACCode,
				BatchNo:     strings.TrimSpace(item.BatchNo),
				ExpDate:     item.ExpDate.Ptr(),
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
			"version":       quotation.Version,
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

func quotationNumberInUse(userID uuid.UUID, number string, excludeID uuid.UUID) bool {
	var count int64
	query := utils.DB.Model(&models.Quotation{}).Where("user_id = ? AND quotation_number = ?", userID, number)
	if excludeID != uuid.Nil {
		query = query.Where("id != ?", excludeID)
	}
	query.Count(&count)
	return count > 0
}

// allocateUniqueQuotationNumber returns the next EST-XXXX estimate number that is not in use.
func allocateUniqueQuotationNumber(userID uuid.UUID) string {
	var numbers []string
	utils.DB.Model(&models.Quotation{}).
		Where("user_id = ?", userID).
		Pluck("quotation_number", &numbers)

	var maxSeq int64
	for _, number := range numbers {
		if seq := trailingNumberSequence(number); seq > maxSeq {
			maxSeq = seq
		}
	}
	for i := maxSeq + 1; i < maxSeq+10000; i++ {
		candidate := fmt.Sprintf("EST-%04d", i)
		if !quotationNumberInUse(userID, candidate, uuid.Nil) {
			return candidate
		}
	}
	return fmt.Sprintf("EST-%d", time.Now().Unix())
}

func trailingNumberSequence(number string) int64 {
	number = strings.TrimSpace(number)
	idx := len(number)
	for idx > 0 && number[idx-1] >= '0' && number[idx-1] <= '9' {
		idx--
	}
	if idx == len(number) {
		return 0
	}
	var seq int64
	fmt.Sscanf(number[idx:], "%d", &seq)
	return seq
}

func GetNextQuotationNumber(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	c.JSON(http.StatusOK, gin.H{"quotation_number": allocateUniqueQuotationNumber(userID)})
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
	userName := ""
	if name, exists := c.Get("user_name"); exists {
		userName = name.(string)
	}
	id := c.Param("id")

	// Optional payment details collected during conversion (e.g. "Mark as Sale").
	var input struct {
		PaymentMode   string                `json:"payment_mode"`
		AmountPaid    float64               `json:"amount_paid"`
		PaymentSplits []models.PaymentSplit `json:"payment_splits"`
		BankAccountID *uuid.UUID            `json:"bank_account_id"`
	}
	if c.Request.Body != nil && c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}

	var quotation models.Quotation
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).
		Preload("Party").
		Preload("Items").
		First(&quotation).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Estimate not found"})
		return
	}

	if quotation.Status == "converted" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Estimate already converted to invoice"})
		return
	}

	resolvedBankAccount, err := resolveBankAccountForPaymentMode(userID, input.PaymentMode, input.BankAccountID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid bank account for payment method"})
		return
	}

	invoiceNumber := allocateUniqueInvoiceNumber(userID, "")

	// Create invoice from quotation
	invoice := models.Invoice{
		ID:                    uuid.New(),
		UserID:                userID,
		InvoiceNumber:         invoiceNumber,
		InvoiceType:           "tax_invoice",
		PartyID:               quotation.PartyID,
		Date:                  time.Now(),
		PaymentTerms:          quotation.PaymentTerms,
		Status:                "sent",
		SubTotal:              quotation.SubTotal,
		DiscountTotal:         quotation.DiscountTotal,
		InvoiceDiscount:       quotation.QuotationDiscount,
		AdditionalCharges:     quotation.AdditionalCharges,
		AdditionalChargeItems: quotation.AdditionalChargeItems,
		TaxTotal:              quotation.TaxTotal,
		CGSTTotal:             quotation.CGSTTotal,
		SGSTTotal:             quotation.SGSTTotal,
		IGSTTotal:             quotation.IGSTTotal,
		RoundOff:              quotation.RoundOff,
		TotalAmount:           quotation.TotalAmount,
		PaymentMode:           input.PaymentMode,
		AmountPaid:            input.AmountPaid,
		BankAccountID:         resolvedBankAccount,
		Notes:                 quotation.Notes,
		Terms:                 quotation.Terms,
		IsInterState:          quotation.IsInterState,
		PlaceOfSupply:         quotation.PlaceOfSupply,
		ReverseCharge:         quotation.ReverseCharge,
		Signature:             quotation.Signature,
	}

	// Copy items
	for _, qItem := range quotation.Items {
		invoice.Items = append(invoice.Items, models.InvoiceItem{
			ID:          uuid.New(),
			ProductID:   qItem.ProductID,
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
			BatchNo:     qItem.BatchNo,
			ExpDate:     qItem.ExpDate,
		})
	}

	if err := finalizeInvoicePaymentSplits(userID, &invoice, input.PaymentSplits, input.PaymentMode, input.BankAccountID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid bank account for payment method"})
		return
	}
	normalizeInvoicePaymentStatus(&invoice)

	if err := utils.DB.Create(&invoice).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create invoice from estimate"})
		return
	}

	recordInvoiceStatusHistory(invoice.ID, userID, "", invoice.Status, fmt.Sprintf("Converted from estimate %s", quotation.QuotationNumber), userName)

	applyInvoiceSaleStock(userID, &invoice)

	if err := postInvoiceAccounting(utils.DB, userID, &invoice); err != nil {
		fmt.Printf("[DEBUG] ConvertToInvoice - accounting error: %v\n", err)
	}

	if invoice.AmountPaid > 0 {
		notes := fmt.Sprintf("Auto-created from estimate %s conversion", quotation.QuotationNumber)
		if err := createLinkedSalePaymentIn(utils.DB, userID, &invoice, invoice.AmountPaid, invoice.Date, notes); err != nil {
			fmt.Printf("[DEBUG] ConvertToInvoice - payment in error: %v\n", err)
		}
	}

	// Update party balance
	utils.DB.Model(&quotation.Party).Update("balance", quotation.Party.Balance+invoice.TotalAmount)

	// Update quotation status
	now := time.Now()
	quotation.Status = "converted"
	quotation.ConvertedToInvoiceID = &invoice.ID
	quotation.ConvertedAt = &now
	utils.DB.Save(&quotation)

	CreateAuditLog(
		userID,
		userName,
		"create",
		"invoice",
		&invoice.ID,
		invoice.InvoiceNumber,
		fmt.Sprintf("Converted estimate %s to invoice %s - Amount: %.2f", quotation.QuotationNumber, invoice.InvoiceNumber, invoice.TotalAmount),
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		map[string]interface{}{
			"quotation_id":   quotation.ID,
			"quotation_no":   quotation.QuotationNumber,
			"invoice_number": invoice.InvoiceNumber,
			"total_amount":   invoice.TotalAmount,
		},
		"success",
		"",
	)

	attachInvoicePaymentSplits(utils.DB, &invoice)
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

	// Load the party to resolve the recipient email address.
	var party models.Party
	if err := utils.DB.First(&party, "id = ?", quotation.PartyID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Quotation party not found"})
		return
	}

	if party.Email == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "This party does not have an email address"})
		return
	}

	if !utils.EmailConfiguredForUser(userID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "SMTP is not configured. Configure email settings in Developer Settings before sending quotations."})
		return
	}

	subject := fmt.Sprintf("Quotation %s from %s", quotation.QuotationNumber, party.Name)
	body := fmt.Sprintf(`<p>Hello %s,</p>
<p>Please find your quotation <strong>%s</strong> below.</p>
<p>Total amount: ₹%.2f</p>
<p>Notes: %s</p>
<p>Terms: %s</p>
<p>Thank you for your business.</p>`, party.Name, quotation.QuotationNumber, quotation.TotalAmount, quotation.Notes, quotation.Terms)

	if err := utils.SendEmailForUser(userID, party.Email, subject, body); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to send quotation email: %v", err)})
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

// GenerateQuotationPDF renders the estimate print view. Per requirements the
// print carries no business details — only the "Estimate" title and date at top.
func GenerateQuotationPDF(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var quotation models.Quotation
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).
		Preload("Party").
		Preload("Items").
		First(&quotation).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Estimate not found"})
		return
	}

	esc := html.EscapeString

	// Generate HTML for PDF
	htmlDoc := fmt.Sprintf(`
<!DOCTYPE html>
<html>
<head>
	<meta charset="UTF-8">
	<title>Estimate - %s</title>
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
			align-items: baseline;
			margin-bottom: 30px;
			border-bottom: 2px solid #333;
			padding-bottom: 12px;
		}
		.title {
			font-size: 26px;
			font-weight: bold;
			letter-spacing: 1px;
		}
		.header-date {
			font-size: 14px;
			color: #333;
		}
		.estimate-number {
			font-size: 14px;
			color: #666;
			margin-bottom: 20px;
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
		.info-item {
			margin-bottom: 5px;
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
		@media print {
			body { margin: 0; padding: 10px; }
		}
	</style>
</head>
<body>
	<div class="header">
		<div class="title">ESTIMATE</div>
		<div class="header-date">Date: %s</div>
	</div>

	<div class="estimate-number">Estimate No: %s</div>

	<div class="section">
		<div class="section-title">Bill To</div>
		<div class="info-item">
			<strong>%s</strong><br>
			%s<br>
			%s
		</div>
	</div>

	<div class="section">
		<div class="section-title">Items</div>
		<table>
			<thead>
				<tr>
					<th>Description</th>
					<th>Qty</th>
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
		%s
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

	%s
	<script>
		window.onload = function() {
			window.print();
		};
	</script>
</body>
</html>`,
		esc(quotation.QuotationNumber),
		quotation.Date.Format("02-01-2006"),
		esc(quotation.QuotationNumber),
		esc(quotation.Party.Name),
		esc(quotation.Party.Address),
		esc(fmt.Sprintf("%s, %s - %s", quotation.Party.City, quotation.Party.State, quotation.Party.Pincode)),
		func() string {
			var rows string
			for _, item := range quotation.Items {
				rows += fmt.Sprintf(`<tr>
					<td>%s</td>
					<td>%.2f %s</td>
					<td>₹%.2f</td>
					<td>%.2f%%</td>
					<td>%.2f%%</td>
					<td>₹%.2f</td>
				</tr>`, esc(item.Description), item.Quantity, esc(item.Unit), item.UnitPrice, item.Discount, item.TaxRate, item.Total)
			}
			return rows
		}(),
		quotation.SubTotal,
		quotation.DiscountTotal+quotation.QuotationDiscount,
		quotation.TaxTotal,
		additionalChargeRowsHTML(quotation.AdditionalChargeItems, quotation.AdditionalCharges),
		quotation.RoundOff,
		quotation.TotalAmount,
		esc(quotation.Terms),
		func() string {
			if quotation.Notes != "" {
				return fmt.Sprintf(`<div class="section-title" style="margin-top: 20px;">Notes</div>
				<div class="terms">%s</div>`, esc(quotation.Notes))
			}
			return ""
		}(),
		signatureBlockHTML(quotation.Signature),
	)

	c.Header("Content-Type", "text/html")
	c.String(http.StatusOK, htmlDoc)
}
