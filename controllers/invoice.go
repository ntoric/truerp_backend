package controllers

import (
	"truerp/models"
	"truerp/utils"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func GetInvoices(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	status := c.Query("status")
	partyID := c.Query("party_id")

	fmt.Printf("[DEBUG] GetInvoices - UserID: %s, Status: %s, PartyID: %s\n", userID, status, partyID)

	syncOverdueInvoices(userID)

	var invoices []models.Invoice
	query := utils.DB.Where("user_id = ?", userID).Preload("Party")

	// Filter by status
	if status != "" {
		query = query.Where("status = ?", status)
	}
	// Filter by party
	if partyID != "" {
		query = query.Where("party_id = ?", partyID)
	}
	// Date range
	if from := c.Query("from"); from != "" {
		query = query.Where("date >= ?", from)
	}
	if to := c.Query("to"); to != "" {
		query = query.Where("date <= ?", to)
	}

	if err := query.Order("date DESC, created_at DESC").Find(&invoices).Error; err != nil {
		fmt.Printf("[DEBUG] GetInvoices - DB error: %v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch invoices"})
		return
	}

	fmt.Printf("[DEBUG] GetInvoices - Found %d invoices\n", len(invoices))
	c.JSON(http.StatusOK, invoices)
}

func GetInvoice(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	fmt.Printf("[DEBUG] GetInvoice - UserID: %s, ID: %s\n", userID, id)

	var invoice models.Invoice
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).
		Preload("Party").
		Preload("Items").
		First(&invoice).Error; err != nil {
		fmt.Printf("[DEBUG] GetInvoice - Invoice not found: %v\n", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "Invoice not found"})
		return
	}

	c.JSON(http.StatusOK, invoice)
}

func CreateInvoice(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	userName := ""
	if name, exists := c.Get("user_name"); exists {
		userName = name.(string)
	}

	var input struct {
		InvoiceNumber     string     `json:"invoice_number"`
		InvoiceType       string     `json:"invoice_type"`
		PartyID           uuid.UUID  `json:"party_id"`
		CustomerID        uuid.UUID  `json:"customer_id"`
		Date              time.Time  `json:"date" binding:"required"`
		DueDate           *time.Time `json:"due_date"`
		PaymentTerms      int        `json:"payment_terms"`
		Status            string     `json:"status"`
		PaymentMode       string     `json:"payment_mode"`
		AmountPaid        float64    `json:"amount_paid"`
		ReceivedAmount    float64    `json:"received_amount"`
		BankAccountID     *uuid.UUID `json:"bank_account_id"`
		Notes            string     `json:"notes"`
		Terms            string    `json:"terms"`
		IsInterState     bool      `json:"is_inter_state"`
		EWayBillRequired bool      `json:"eway_bill_required"`
		InvoiceDiscount  float64   `json:"invoice_discount"`
		AdditionalCharges float64  `json:"additional_charges"`
		LoyaltyPointsRedeemed int64 `json:"loyalty_points_redeemed"`
		Signature        string                 `json:"signature"`
		PDFTemplate      string                 `json:"pdf_template"`
		CustomFields     map[string]interface{} `json:"custom_fields"`
		Items            []struct {
			Description string               `json:"description"`
			Quantity    models.FlexibleFloat `json:"quantity"`
			UnitPrice   models.FlexibleFloat `json:"unit_price"`
			Discount    models.FlexibleFloat `json:"discount"`
			TaxRate     models.FlexibleFloat `json:"tax_rate"`
			Unit        string               `json:"unit"`
		} `json:"items" binding:"required,min=1"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		fmt.Printf("[DEBUG] CreateInvoice - JSON bind error: %v\n", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if input.PartyID == uuid.Nil {
		input.PartyID = input.CustomerID
	}
	if input.PartyID == uuid.Nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "party_id is required"})
		return
	}

	if err := validateCustomFields(userID, input.CustomFields); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if input.InvoiceNumber == "" {
		var count int64
		utils.DB.Model(&models.Invoice{}).Where("user_id = ?", userID).Count(&count)
		input.InvoiceNumber = fmt.Sprintf("INV-%04d", count+1)
	}

	if input.AmountPaid == 0 && input.ReceivedAmount > 0 {
		input.AmountPaid = input.ReceivedAmount
	}

	resolvedBankAccount, err := resolveBankAccountForPaymentMode(userID, input.PaymentMode, input.BankAccountID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid bank account for payment method"})
		return
	}

	fmt.Printf("[DEBUG] CreateInvoice - UserID: %s, InvoiceNumber: %s, PartyID: %s, Items: %d\n", userID, input.InvoiceNumber, input.PartyID, len(input.Items))

	// Validate party
	var party models.Party
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, input.PartyID).First(&party).Error; err != nil {
		fmt.Printf("[DEBUG] CreateInvoice - Party not found: %v\n", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid party"})
		return
	}

	invoice := models.Invoice{
		ID:                uuid.New(),
		UserID:            userID,
		InvoiceNumber:     input.InvoiceNumber,
		InvoiceType:       input.InvoiceType,
		PartyID:           input.PartyID,
		Date:              input.Date,
		DueDate:           input.DueDate,
		PaymentTerms:      input.PaymentTerms,
		Status:            input.Status,
		PaymentMode:       input.PaymentMode,
		AmountPaid:        input.AmountPaid,
		BankAccountID:     resolvedBankAccount,
		Notes:             input.Notes,
		Terms:             input.Terms,
		IsInterState:      input.IsInterState,
		EWayBillRequired:  input.EWayBillRequired,
		InvoiceDiscount:   input.InvoiceDiscount,
		AdditionalCharges: input.AdditionalCharges,
		Signature:         input.Signature,
		PDFTemplate:       input.PDFTemplate,
		CustomFields:      encodeCustomFieldsMap(input.CustomFields),
	}

	if invoice.Status == "" {
		invoice.Status = "draft"
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

		invoice.Items = append(invoice.Items, models.InvoiceItem{
			ID:          uuid.New(),
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
		})

		subTotal += itemTotal
		discountTotal += itemDiscount
		taxTotal += itemTax
		cgstTotal += cgst
		sgstTotal += sgst
		igstTotal += igst

		// Update stock entry
		entry := models.StockEntry{
			ID:         uuid.New(),
			UserID:     userID,
			ItemName:   item.Description,
			EntryType:  "sale",
			Quantity:   -qty,
			BalanceQty: 0,
			CostPrice:  unitPrice,
			EntryDate:  input.Date,
		}
		utils.DB.Create(&entry)
	}

	total := subTotal - discountTotal + cgstTotal + sgstTotal + igstTotal - input.InvoiceDiscount + input.AdditionalCharges
	preLoyaltyTotal := total

	loyaltySettings, _ := GetOrCreateLoyaltySettings(userID)
	var loyaltyDiscount float64
	if input.LoyaltyPointsRedeemed > 0 {
		var redeemErr error
		loyaltyDiscount, redeemErr = ComputeLoyaltyRedemption(loyaltySettings, &party, total, input.LoyaltyPointsRedeemed)
		if redeemErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": redeemErr.Error()})
			return
		}
		total -= loyaltyDiscount
	}

	roundedTotal := math.Round(total*100) / 100
	roundOff := roundedTotal - total

	invoice.SubTotal = subTotal
	invoice.DiscountTotal = discountTotal
	invoice.InvoiceDiscount = input.InvoiceDiscount
	invoice.AdditionalCharges = input.AdditionalCharges
	invoice.LoyaltyPointsRedeemed = input.LoyaltyPointsRedeemed
	invoice.LoyaltyDiscount = loyaltyDiscount
	invoice.TaxTotal = taxTotal
	invoice.CGSTTotal = cgstTotal
	invoice.SGSTTotal = sgstTotal
	invoice.IGSTTotal = igstTotal
	invoice.RoundOff = roundOff
	invoice.TotalAmount = roundedTotal

	if invoice.Status == "paid" {
		invoice.AmountPaid = invoice.TotalAmount
	} else if loyaltyDiscount > 0 && input.AmountPaid > 0 {
		// Cash received was often entered against the pre-loyalty total — reduce by loyalty discount.
		if input.AmountPaid+0.01 >= preLoyaltyTotal {
			invoice.AmountPaid = input.AmountPaid - loyaltyDiscount
		}
		if invoice.AmountPaid > invoice.TotalAmount {
			invoice.AmountPaid = invoice.TotalAmount
		}
		if invoice.AmountPaid < 0 {
			invoice.AmountPaid = 0
		}
	} else if invoice.AmountPaid == 0 && input.ReceivedAmount > 0 {
		invoice.AmountPaid = input.ReceivedAmount
		if invoice.AmountPaid > invoice.TotalAmount {
			invoice.AmountPaid = invoice.TotalAmount
		}
	} else if invoice.AmountPaid > invoice.TotalAmount {
		invoice.AmountPaid = invoice.TotalAmount
	}

	normalizeInvoicePaymentStatus(&invoice)

	if err := utils.DB.Create(&invoice).Error; err != nil {
		fmt.Printf("[DEBUG] CreateInvoice - DB create error: %v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create invoice"})
		return
	}

	recordInvoiceStatusHistory(invoice.ID, userID, "", invoice.Status, "Invoice created", userName)

	if err := utils.DB.Transaction(func(tx *gorm.DB) error {
		return applyLoyaltyForInvoice(tx, userID, &party, &invoice, loyaltySettings, input.LoyaltyPointsRedeemed)
	}); err != nil {
		utils.DB.Delete(&invoice)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := postInvoiceAccounting(utils.DB, userID, &invoice); err != nil {
		fmt.Printf("[DEBUG] CreateInvoice - accounting error: %v\n", err)
	}

	if invoice.AmountPaid > 0 {
		desc := fmt.Sprintf("Sales invoice %s", invoice.InvoiceNumber)
		if err := recordSalePaymentIn(utils.DB, userID, invoice.BankAccountID, invoice.AmountPaid, invoice.Date, invoice.InvoiceNumber, desc); err != nil {
			fmt.Printf("[DEBUG] CreateInvoice - cash ledger error: %v\n", err)
		}
	}

	fmt.Printf("[DEBUG] CreateInvoice - Invoice created successfully: %s\n", invoice.ID)

	// Update party balance
	utils.DB.Model(&party).Update("balance", party.Balance+invoice.TotalAmount)

	// Log invoice creation
	CreateAuditLog(
		userID,
		userName,
		"create",
		"invoice",
		&invoice.ID,
		invoice.InvoiceNumber,
		fmt.Sprintf("Created invoice: %s for %s - Amount: %.2f", invoice.InvoiceNumber, party.Name, invoice.TotalAmount),
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		map[string]interface{}{
			"party_id":     input.PartyID,
			"party_name":   party.Name,
			"total_amount": invoice.TotalAmount,
			"status":       invoice.Status,
		},
		"success",
		"",
	)

	c.JSON(http.StatusCreated, invoice)
}

func UpdateInvoice(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	userName := ""
	if name, exists := c.Get("user_name"); exists {
		userName = name.(string)
	}
	id := c.Param("id")

	fmt.Printf("[DEBUG] UpdateInvoice - UserID: %s, ID: %s\n", userID, id)

	var invoice models.Invoice
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&invoice).Error; err != nil {
		fmt.Printf("[DEBUG] UpdateInvoice - Invoice not found: %v\n", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "Invoice not found"})
		return
	}

	var input struct {
		InvoiceNumber     string          `json:"invoice_number"`
		PartyID           uuid.UUID       `json:"party_id"`
		CustomerID        uuid.UUID       `json:"customer_id"`
		Date              time.Time       `json:"date"`
		DueDate           *time.Time      `json:"due_date"`
		PaymentTerms      int             `json:"payment_terms"`
		Status            string          `json:"status"`
		IsInterState      bool            `json:"is_inter_state"`
		PaymentMode       string          `json:"payment_mode"`
		AmountPaid        float64         `json:"amount_paid"`
		BankAccountID     *uuid.UUID      `json:"bank_account_id"`
		Notes             string          `json:"notes"`
		Terms             string          `json:"terms"`
		InvoiceDiscount   float64         `json:"invoice_discount"`
		AdditionalCharges float64         `json:"additional_charges"`
		Signature         string          `json:"signature"`
		PDFTemplate       string          `json:"pdf_template"`
		CustomFields      map[string]interface{} `json:"custom_fields"`
		Items             []struct {
			ProductID   *uuid.UUID           `json:"product_id"`
			Description string               `json:"description"`
			Quantity    models.FlexibleFloat `json:"quantity"`
			UnitPrice   models.FlexibleFloat `json:"unit_price"`
			Discount    models.FlexibleFloat `json:"discount"`
			TaxRate     models.FlexibleFloat `json:"tax_rate"`
			Unit        string               `json:"unit"`
			HSNCode     string               `json:"hsn_code"`
		} `json:"items"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		fmt.Printf("[DEBUG] UpdateInvoice - JSON bind error: %v\n", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if input.PartyID == uuid.Nil && input.CustomerID != uuid.Nil {
		input.PartyID = input.CustomerID
	}

	if err := validateUserBankAccount(userID, input.BankAccountID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid bank account"})
		return
	}

	resolvedBankAccount, err := resolveBankAccountForPaymentMode(userID, input.PaymentMode, input.BankAccountID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid bank account for payment method"})
		return
	}

	previousAmountPaid := invoice.AmountPaid
	previousStatus := invoice.Status

	if input.CustomFields != nil {
		if err := validateCustomFields(userID, input.CustomFields); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		invoice.CustomFields = encodeCustomFieldsMap(input.CustomFields)
	}
	if input.PDFTemplate != "" {
		invoice.PDFTemplate = input.PDFTemplate
	}

	// Update invoice fields
	invoice.InvoiceNumber = input.InvoiceNumber
	invoice.PartyID = input.PartyID
	invoice.Date = input.Date
	invoice.DueDate = input.DueDate
	invoice.PaymentTerms = input.PaymentTerms
	invoice.Status = input.Status
	invoice.IsInterState = input.IsInterState
	invoice.PaymentMode = input.PaymentMode
	invoice.AmountPaid = input.AmountPaid
	invoice.BankAccountID = resolvedBankAccount
	invoice.Notes = input.Notes
	invoice.Terms = input.Terms
	invoice.InvoiceDiscount = input.InvoiceDiscount
	invoice.AdditionalCharges = input.AdditionalCharges
	invoice.Signature = input.Signature

	// Delete old items and recreate
	utils.DB.Where("invoice_id = ?", invoice.ID).Delete(&models.InvoiceItem{})

	var subTotal, discountTotal, cgstTotal, sgstTotal, igstTotal float64
	invoice.Items = nil
	for _, item := range input.Items {
		qty := item.Quantity.Float64()
		price := item.UnitPrice.Float64()
		disc := item.Discount.Float64()
		tax := item.TaxRate.Float64()

		itemTotal := qty * price
		itemDiscount := itemTotal * (disc / 100)
		taxable := itemTotal - itemDiscount
		itemTax := taxable * (tax / 100)

		var cgst, sgst, igst float64
		if invoice.IsInterState {
			igst = itemTax
		} else {
			cgst = itemTax / 2
			sgst = itemTax / 2
		}

		invoice.Items = append(invoice.Items, models.InvoiceItem{
			ID:          uuid.New(),
			InvoiceID:   invoice.ID,
			Description: item.Description,
			Quantity:    qty,
			Unit:        item.Unit,
			UnitPrice:   price,
			Discount:    disc,
			TaxRate:     tax,
			CGST:        cgst,
			SGST:        sgst,
			IGST:        igst,
			Total:       taxable + cgst + sgst + igst,
			HSNCode:     item.HSNCode,
		})

		subTotal += itemTotal
		discountTotal += itemDiscount
		cgstTotal += cgst
		sgstTotal += sgst
		igstTotal += igst
	}

	invoice.SubTotal = subTotal
	invoice.DiscountTotal = discountTotal
	invoice.CGSTTotal = cgstTotal
	invoice.SGSTTotal = sgstTotal
	invoice.IGSTTotal = igstTotal
	invoice.TaxTotal = cgstTotal + sgstTotal + igstTotal

	totalBeforeRound := subTotal - discountTotal + invoice.TaxTotal - invoice.InvoiceDiscount + invoice.AdditionalCharges
	roundOff := math.Round(totalBeforeRound) - totalBeforeRound
	invoice.RoundOff = roundOff
	invoice.TotalAmount = math.Round(totalBeforeRound)

	if invoice.Status == "paid" {
		invoice.AmountPaid = invoice.TotalAmount
	}
	normalizeInvoicePaymentStatus(&invoice)

	if err := utils.DB.Save(&invoice).Error; err != nil {
		fmt.Printf("[DEBUG] UpdateInvoice - DB update error: %v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update invoice"})
		return
	}

	if paymentDelta := invoice.AmountPaid - previousAmountPaid; paymentDelta > 0 {
		desc := fmt.Sprintf("Sales invoice %s (payment update)", invoice.InvoiceNumber)
		if err := recordSalePaymentIn(utils.DB, userID, invoice.BankAccountID, paymentDelta, invoice.Date, invoice.InvoiceNumber, desc); err != nil {
			fmt.Printf("[DEBUG] UpdateInvoice - cash ledger error: %v\n", err)
		}
	}

	fmt.Printf("[DEBUG] UpdateInvoice - Invoice updated successfully: %s\n", id)

	if previousStatus != invoice.Status {
		recordInvoiceStatusHistory(invoice.ID, userID, previousStatus, invoice.Status, "Invoice updated", userName)
	}

	// Log invoice update
	CreateAuditLog(
		userID,
		userName,
		"update",
		"invoice",
		&invoice.ID,
		invoice.InvoiceNumber,
		fmt.Sprintf("Updated invoice: %s - Status changed to %s", invoice.InvoiceNumber, input.Status),
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		map[string]interface{}{
			"status":       input.Status,
			"amount_paid":  input.AmountPaid,
			"payment_mode": input.PaymentMode,
		},
		"success",
		"",
	)

	c.JSON(http.StatusOK, invoice)
}

func DeleteInvoice(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	userName := ""
	if name, exists := c.Get("user_name"); exists {
		userName = name.(string)
	}
	id := c.Param("id")

	fmt.Printf("[DEBUG] DeleteInvoice - UserID: %s, ID: %s\n", userID, id)

	var invoice models.Invoice
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&invoice).Error; err != nil {
		fmt.Printf("[DEBUG] DeleteInvoice - Invoice not found: %v\n", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "Invoice not found"})
		return
	}

	if invoice.Status == "paid" {
		fmt.Printf("[DEBUG] DeleteInvoice - Cannot delete paid invoice\n")
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot delete a paid invoice"})
		return
	}

	if err := utils.DB.Delete(&invoice).Error; err != nil {
		fmt.Printf("[DEBUG] DeleteInvoice - DB delete error: %v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete invoice"})
		return
	}

	fmt.Printf("[DEBUG] DeleteInvoice - Invoice deleted successfully: %s\n", id)

	// Log invoice deletion
	CreateAuditLog(
		userID,
		userName,
		"delete",
		"invoice",
		&invoice.ID,
		invoice.InvoiceNumber,
		fmt.Sprintf("Deleted invoice: %s - Amount: %.2f", invoice.InvoiceNumber, invoice.TotalAmount),
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		map[string]interface{}{
			"total_amount": invoice.TotalAmount,
			"status":       invoice.Status,
		},
		"success",
		"",
	)

	c.JSON(http.StatusOK, gin.H{"message": "Invoice deleted successfully"})
}

func GetNextInvoiceNumber(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var count int64
	utils.DB.Model(&models.Invoice{}).Where("user_id = ?", userID).Count(&count)

	nextNum := fmt.Sprintf("INV-%04d", count+1)
	c.JSON(http.StatusOK, gin.H{"invoice_number": nextNum})
}

func GetInvoiceStats(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var stats struct {
		TotalSales float64 `json:"total_sales"`
		Paid       float64 `json:"paid"`
		Unpaid     float64 `json:"unpaid"`
		Cancelled  float64 `json:"cancelled"`
	}

	query := utils.DB.Model(&models.Invoice{}).Where("user_id = ?", userID)

	// Apply date range filter if provided
	if from := c.Query("from"); from != "" {
		query = query.Where("date >= ?", from)
	}
	if to := c.Query("to"); to != "" {
		query = query.Where("date <= ?", to)
	}

	// Total Sales (excluding cancelled)
	utils.DB.Model(&models.Invoice{}).Where("user_id = ? AND status != ?", userID, "cancelled").
		Scopes(func(db *gorm.DB) *gorm.DB {
			if from := c.Query("from"); from != "" {
				db = db.Where("date >= ?", from)
			}
			if to := c.Query("to"); to != "" {
				db = db.Where("date <= ?", to)
			}
			return db
		}).
		Select("COALESCE(SUM(total_amount), 0)").Scan(&stats.TotalSales)

	// Paid
	utils.DB.Model(&models.Invoice{}).Where("user_id = ? AND status = ?", userID, "paid").
		Scopes(func(db *gorm.DB) *gorm.DB {
			if from := c.Query("from"); from != "" {
				db = db.Where("date >= ?", from)
			}
			if to := c.Query("to"); to != "" {
				db = db.Where("date <= ?", to)
			}
			return db
		}).
		Select("COALESCE(SUM(total_amount), 0)").Scan(&stats.Paid)

	// Unpaid (draft, sent, overdue)
	utils.DB.Model(&models.Invoice{}).Where("user_id = ? AND status IN ?", userID, []string{"draft", "sent", "overdue"}).
		Scopes(func(db *gorm.DB) *gorm.DB {
			if from := c.Query("from"); from != "" {
				db = db.Where("date >= ?", from)
			}
			if to := c.Query("to"); to != "" {
				db = db.Where("date <= ?", to)
			}
			return db
		}).
		Select("COALESCE(SUM(total_amount), 0)").Scan(&stats.Unpaid)

	// Cancelled
	utils.DB.Model(&models.Invoice{}).Where("user_id = ? AND status = ?", userID, "cancelled").
		Scopes(func(db *gorm.DB) *gorm.DB {
			if from := c.Query("from"); from != "" {
				db = db.Where("date >= ?", from)
			}
			if to := c.Query("to"); to != "" {
				db = db.Where("date <= ?", to)
			}
			return db
		}).
		Select("COALESCE(SUM(total_amount), 0)").Scan(&stats.Cancelled)

	c.JSON(http.StatusOK, stats)
}

func GenerateInvoicePDF(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var invoice models.Invoice
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).
		Preload("Party").
		Preload("Items").
		First(&invoice).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Invoice not found"})
		return
	}

	settings := loadPrintSettings(userID)
	var business models.Business
	_ = utils.DB.Where("user_id = ?", userID).First(&business)

	pdfBytes, err := buildInvoiceDocumentPDF(invoice, &business, settings)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate invoice PDF"})
		return
	}

	c.Header("Content-Type", "application/pdf")
	c.Header("Content-Disposition", fmt.Sprintf("inline; filename=\"Invoice_%s.pdf\"", invoice.InvoiceNumber))
	c.Data(http.StatusOK, "application/pdf", pdfBytes)
}
