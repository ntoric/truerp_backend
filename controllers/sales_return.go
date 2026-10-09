package controllers

import (
	"fmt"
	"net/http"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// normalizeDeductionItems trims labels, defaults blank labels, drops
// zero-amount rows and rejects negative amounts.
func normalizeDeductionItems(items []models.AdditionalCharge) ([]models.AdditionalCharge, error) {
	out := make([]models.AdditionalCharge, 0, len(items))
	for _, item := range items {
		label := strings.TrimSpace(item.Label)
		if item.Amount < 0 {
			return nil, fmt.Errorf("deduction amount cannot be negative")
		}
		if item.Amount == 0 {
			continue
		}
		if label == "" {
			label = "Deduction"
		}
		out = append(out, models.AdditionalCharge{Label: label, Amount: item.Amount})
	}
	return out, nil
}

// setSalesReturnRefund fills the computed (non-persisted) RefundAmount.
func setSalesReturnRefund(sr *models.SalesReturn) {
	sr.RefundAmount = sr.Amount - sr.DeductionTotal
	if sr.RefundAmount < 0 {
		sr.RefundAmount = 0
	}
}

func GetSalesReturns(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var returns []models.SalesReturn
	query := utils.DB.Where("user_id = ?", userID).Preload("Party").Preload("Invoice")

	if partyID := c.Query("party_id"); partyID != "" {
		query = query.Where("party_id = ?", partyID)
	}
	if status := c.Query("status"); status != "" {
		query = query.Where("status = ?", status)
	}
	if fromDate := c.Query("from_date"); fromDate != "" {
		query = query.Where("date >= ?", fromDate)
	}
	if toDate := c.Query("to_date"); toDate != "" {
		query = query.Where("date <= ?", toDate)
	}

	if err := query.Order("date DESC").Find(&returns).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch sales returns"})
		return
	}

	for i := range returns {
		setSalesReturnRefund(&returns[i])
	}
	c.JSON(http.StatusOK, gin.H{"data": returns})
}

func GetSalesReturn(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var salesReturn models.SalesReturn
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Preload("Party").Preload("Invoice").Preload("Items").First(&salesReturn).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Sales return not found"})
		return
	}

	setSalesReturnRefund(&salesReturn)
	c.JSON(http.StatusOK, salesReturn)
}

func CreateSalesReturn(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		PartyID        uuid.UUID                 `json:"party_id" binding:"required"`
		InvoiceID      uuid.UUID                 `json:"invoice_id"`
		Date           time.Time                 `json:"date" binding:"required"`
		Reason         string                    `json:"reason"`
		RefundMode     string                    `json:"refund_mode"`
		Notes          string                    `json:"notes"`
		DeductionItems []models.AdditionalCharge `json:"deduction_items"`
		Items          []struct {
			InvoiceItemID uuid.UUID  `json:"invoice_item_id"`
			ProductID     *uuid.UUID `json:"product_id"`
			Description   string     `json:"description"`
			Quantity      float64    `json:"quantity" binding:"required,gt=0"`
			UnitPrice     float64    `json:"unit_price" binding:"required"`
			TaxRate       float64    `json:"tax_rate"`
			Reason        string     `json:"reason"`
		} `json:"items" binding:"required,min=1"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var count int64
	utils.DB.Model(&models.SalesReturn{}).Where("user_id = ?", userID).Count(&count)

	salesReturn := models.SalesReturn{
		ID:           uuid.New(),
		UserID:       userID,
		PartyID:      input.PartyID,
		InvoiceID:    input.InvoiceID,
		ReturnNumber: fmt.Sprintf("SR-%04d", count+1),
		Date:         input.Date,
		Status:       "draft",
		Reason:       input.Reason,
		RefundMode:   input.RefundMode,
		Notes:        input.Notes,
	}

	var totalAmount float64
	for _, item := range input.Items {
		productID := item.ProductID
		description := item.Description

		if productID != nil && *productID != uuid.Nil {
			var product models.Product
			if err := utils.DB.Where("user_id = ? AND id = ?", userID, *productID).First(&product).Error; err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid product in return items"})
				return
			}
			if description == "" {
				description = product.Name
			}
		} else if item.InvoiceItemID != uuid.Nil {
			var invoiceItem models.InvoiceItem
			if err := utils.DB.Where("id = ?", item.InvoiceItemID).First(&invoiceItem).Error; err == nil {
				if invoiceItem.ProductID != nil {
					productID = invoiceItem.ProductID
				}
				if description == "" {
					description = invoiceItem.Description
				}
			}
		}

		if description == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Item description or product_id is required"})
			return
		}

		taxAmount := item.UnitPrice * item.Quantity * (item.TaxRate / 100)
		total := item.UnitPrice*item.Quantity + taxAmount

		salesReturn.Items = append(salesReturn.Items, models.SalesReturnItem{
			ID:            uuid.New(),
			ReturnID:      salesReturn.ID,
			InvoiceItemID: item.InvoiceItemID,
			ProductID:     productID,
			Description:   description,
			Quantity:      item.Quantity,
			UnitPrice:     item.UnitPrice,
			TaxRate:       item.TaxRate,
			Total:         total,
			Reason:        item.Reason,
		})

		totalAmount += total
	}

	salesReturn.Amount = totalAmount

	deductionItems, err := normalizeDeductionItems(input.DeductionItems)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	deductionTotal := models.SumAdditionalCharges(deductionItems)
	if deductionTotal > totalAmount {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Deductions cannot exceed the return amount"})
		return
	}
	salesReturn.DeductionItems = deductionItems
	salesReturn.DeductionTotal = deductionTotal

	if err := utils.DB.Create(&salesReturn).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create sales return"})
		return
	}

	setSalesReturnRefund(&salesReturn)
	c.JSON(http.StatusCreated, salesReturn)
}

func UpdateSalesReturn(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var salesReturn models.SalesReturn
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&salesReturn).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Sales return not found"})
		return
	}

	if salesReturn.Status != "draft" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot edit processed sales return"})
		return
	}

	var input struct {
		Date           time.Time                 `json:"date"`
		Reason         string                    `json:"reason"`
		RefundMode     string                    `json:"refund_mode"`
		Notes          string                    `json:"notes"`
		DeductionItems []models.AdditionalCharge `json:"deduction_items"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	deductionItems, err := normalizeDeductionItems(input.DeductionItems)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	deductionTotal := models.SumAdditionalCharges(deductionItems)
	if deductionTotal > salesReturn.Amount {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Deductions cannot exceed the return amount"})
		return
	}

	salesReturn.Date = input.Date
	salesReturn.Reason = input.Reason
	salesReturn.RefundMode = input.RefundMode
	salesReturn.Notes = input.Notes
	salesReturn.DeductionItems = deductionItems
	salesReturn.DeductionTotal = deductionTotal

	if err := utils.DB.Model(&salesReturn).
		Select("date", "reason", "refund_mode", "notes", "deduction_items", "deduction_total").
		Updates(&salesReturn).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update sales return"})
		return
	}

	setSalesReturnRefund(&salesReturn)
	c.JSON(http.StatusOK, salesReturn)
}

func ProcessSalesReturn(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var salesReturn models.SalesReturn
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Preload("Items").First(&salesReturn).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Sales return not found"})
		return
	}

	if salesReturn.Status != "draft" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Sales return already processed"})
		return
	}

	outletID := resolveDefaultWarehouseID(userID)
	now := time.Now()

	// Restore inventory for returned items
	for _, item := range salesReturn.Items {
		productID := item.ProductID
		batchNo := strings.TrimSpace(item.BatchNo)
		var expDate *time.Time
		if (productID == nil || *productID == uuid.Nil || batchNo == "") && item.InvoiceItemID != uuid.Nil {
			var invoiceItem models.InvoiceItem
			if err := utils.DB.Where("id = ?", item.InvoiceItemID).First(&invoiceItem).Error; err == nil {
				if productID == nil || *productID == uuid.Nil {
					productID = invoiceItem.ProductID
				}
				if batchNo == "" {
					batchNo = invoiceItem.BatchNo
				}
				expDate = invoiceItem.ExpDate
			}
		}

		restoreOutlet := outletID
		if productID != nil && *productID != uuid.Nil && batchNo != "" {
			var stock models.InventoryStock
			if err := utils.DB.Where(
				"user_id = ? AND product_id = ? AND batch_no = ?",
				userID, *productID, batchNo,
			).Order("available_qty DESC").First(&stock).Error; err == nil {
				restoreOutlet = stock.OutletID
			}
		}

		entry := models.StockEntry{
			ID:             uuid.New(),
			UserID:         userID,
			ItemName:       item.Description,
			ProductID:      productID,
			OutletID:       restoreOutlet,
			EntryType:      "return",
			Quantity:       item.Quantity,
			BalanceQty:     0,
			CostPrice:      item.UnitPrice,
			BatchNo:        batchNo,
			ExpDate:        expDate,
			ReferenceID:    salesReturn.ID,
			ReferenceType:  "sales_return",
			Notes:          fmt.Sprintf("Sales return %s", salesReturn.ReturnNumber),
			ApprovalStatus: "approved",
			ApprovedBy:     &userID,
			ApprovedAt:     &now,
			EntryDate:      salesReturn.Date,
		}
		if err := utils.DB.Create(&entry).Error; err != nil {
			fmt.Printf("[DEBUG] ProcessSalesReturn - Failed to create stock entry: %v\n", err)
			continue
		}

		if productID != nil && *productID != uuid.Nil && restoreOutlet != uuid.Nil {
			updateInventoryStock(userID, *productID, restoreOutlet, "return", item.Quantity, item.UnitPrice, batchNo, nil, expDate)
		}
	}

	salesReturn.Status = "processed"
	utils.DB.Save(&salesReturn)

	// The return settles one of two ways: money-settled refunds (cash, bank,
	// UPI, original payment) record the outgoing cash/bank row, while a
	// credit note reduces the customer's outstanding balance by the net
	// refund (return amount minus deductions) and is applied to the linked
	// invoice's outstanding amount.
	setSalesReturnRefund(&salesReturn)
	if salesReturn.RefundAmount > 0 {
		var linkedInvoice *models.Invoice
		if salesReturn.InvoiceID != uuid.Nil {
			var invoice models.Invoice
			if err := utils.DB.Where("user_id = ? AND id = ?", userID, salesReturn.InvoiceID).First(&invoice).Error; err == nil {
				linkedInvoice = &invoice
			}
		}
		if refundSettlesInMoney(salesReturn.RefundMode) {
			var linkedMode string
			var linkedAccountID *uuid.UUID
			if linkedInvoice != nil {
				linkedMode = linkedInvoice.PaymentMode
				linkedAccountID = linkedInvoice.BankAccountID
			}
			accountID := resolveRefundBankAccount(userID, salesReturn.RefundMode, linkedMode, linkedAccountID)
			desc := fmt.Sprintf("Sales return %s refund", salesReturn.ReturnNumber)
			if err := recordPaymentCashTxn(utils.DB, userID, accountID, "out", salesReturn.RefundAmount, salesReturn.Date, salesReturn.ReturnNumber, desc); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to record refund"})
				return
			}
		} else {
			adjustPartyBalance(utils.DB, userID, salesReturn.PartyID, -salesReturn.RefundAmount)
			settleInvoiceWithReturn(linkedInvoice, salesReturn.RefundAmount)
			if err := issueCreditNoteForReturn(userID, &salesReturn); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create credit note"})
				return
			}
		}
	}

	c.JSON(http.StatusOK, salesReturn)
}

// issueCreditNoteForReturn writes the CreditNote document for a
// credit-settled sales return so it appears on the credit notes list. The
// note is created already issued because the balance and invoice effects
// are applied by the caller; each withheld deduction is added as a
// negative line so the items reconcile with the net credited amount.
func issueCreditNoteForReturn(userID uuid.UUID, salesReturn *models.SalesReturn) error {
	var count int64
	if err := utils.DB.Model(&models.CreditNote{}).Where("user_id = ?", userID).Count(&count).Error; err != nil {
		return err
	}

	reason := fmt.Sprintf("Sales return %s", salesReturn.ReturnNumber)
	if salesReturn.Reason != "" {
		reason += " - " + salesReturn.Reason
	}

	creditNote := models.CreditNote{
		ID:               uuid.New(),
		UserID:           userID,
		InvoiceID:        salesReturn.InvoiceID,
		PartyID:          salesReturn.PartyID,
		CreditNoteNumber: fmt.Sprintf("CN-%04d", count+1),
		Date:             salesReturn.Date,
		Status:           "issued",
		Reason:           reason,
		RefundMode:       "credit_note",
		TotalAmount:      salesReturn.RefundAmount,
	}
	for _, item := range salesReturn.Items {
		creditNote.Items = append(creditNote.Items, models.CreditNoteItem{
			ID:            uuid.New(),
			CreditNoteID:  creditNote.ID,
			InvoiceItemID: item.InvoiceItemID,
			Description:   item.Description,
			Quantity:      item.Quantity,
			UnitPrice:     item.UnitPrice,
			TaxRate:       item.TaxRate,
			Total:         item.Total,
			Reason:        item.Reason,
		})
	}
	for _, deduction := range salesReturn.DeductionItems {
		creditNote.Items = append(creditNote.Items, models.CreditNoteItem{
			ID:           uuid.New(),
			CreditNoteID: creditNote.ID,
			Description:  deduction.Label,
			Quantity:     1,
			UnitPrice:    -deduction.Amount,
			Total:        -deduction.Amount,
			Reason:       "Deduction",
		})
	}

	// Omit InvoiceID when the return is not linked to an invoice so NULL is
	// written — a zero UUID would violate the credit_notes invoice FK.
	if salesReturn.InvoiceID == uuid.Nil {
		return utils.DB.Omit("InvoiceID", "Invoice").Create(&creditNote).Error
	}
	return utils.DB.Create(&creditNote).Error
}

// settleInvoiceWithReturn applies a credit-settled return against the linked
// invoice's outstanding amount. The covered share lands on amount_paid since
// outstanding is derived as total - amount_paid everywhere else.
func settleInvoiceWithReturn(invoice *models.Invoice, amount float64) {
	if invoice == nil || amount <= 0 {
		return
	}
	outstanding := invoice.TotalAmount - invoice.AmountPaid
	if outstanding <= 0 {
		return
	}
	covered := amount
	if covered > outstanding {
		covered = outstanding
	}
	newPaid := invoice.AmountPaid + covered
	utils.DB.Model(invoice).Updates(map[string]interface{}{
		"amount_paid": newPaid,
		"status":      invoiceStatusForPaidAmount(*invoice, newPaid),
	})
}

func DeleteSalesReturn(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var salesReturn models.SalesReturn
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&salesReturn).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Sales return not found"})
		return
	}

	if salesReturn.Status != "draft" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot delete processed sales return"})
		return
	}

	utils.DB.Delete(&salesReturn)
	c.JSON(http.StatusOK, gin.H{"message": "Sales return deleted"})
}
