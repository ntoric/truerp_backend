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

func GetPurchaseReturns(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var returns []models.PurchaseReturn
	query := utils.DB.Where("user_id = ?", userID).Preload("Party").Preload("PurchaseBill")

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
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch purchase returns"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": returns})
}

func GetPurchaseReturn(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var purchaseReturn models.PurchaseReturn
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Preload("Party").Preload("PurchaseBill").Preload("Items").First(&purchaseReturn).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Purchase return not found"})
		return
	}

	c.JSON(http.StatusOK, purchaseReturn)
}

func CreatePurchaseReturn(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		PartyID        uuid.UUID `json:"party_id" binding:"required"`
		PurchaseBillID uuid.UUID `json:"purchase_bill_id"`
		Date           string    `json:"date" binding:"required"`
		Reason         string    `json:"reason"`
		RefundMode     string    `json:"refund_mode"`
		Notes          string    `json:"notes"`
		Items          []struct {
			PurchaseBillItemID uuid.UUID `json:"purchase_bill_item_id"`
			Description        string    `json:"description" binding:"required"`
			Quantity           float64   `json:"quantity" binding:"required,gt=0"`
			UnitPrice          float64   `json:"unit_price" binding:"required"`
			TaxRate            float64   `json:"tax_rate"`
			Reason             string    `json:"reason"`
		} `json:"items" binding:"required,min=1"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	parsedDate, err := time.Parse("2006-01-02", input.Date)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid date format, expected YYYY-MM-DD"})
		return
	}

	var count int64
	utils.DB.Model(&models.PurchaseReturn{}).Where("user_id = ?", userID).Count(&count)

	purchaseReturn := models.PurchaseReturn{
		ID:             uuid.New(),
		UserID:         userID,
		PartyID:        input.PartyID,
		PurchaseBillID: input.PurchaseBillID,
		ReturnNumber:   fmt.Sprintf("PR-%04d", count+1),
		Date:           parsedDate,
		Status:         "draft",
		Reason:         input.Reason,
		RefundMode:     input.RefundMode,
		Notes:          input.Notes,
	}

	var totalAmount float64
	for _, item := range input.Items {
		taxAmount := item.UnitPrice * item.Quantity * (item.TaxRate / 100)
		total := item.UnitPrice*item.Quantity + taxAmount

		purchaseReturn.Items = append(purchaseReturn.Items, models.PurchaseReturnItem{
			ID:                 uuid.New(),
			ReturnID:           purchaseReturn.ID,
			PurchaseBillItemID: item.PurchaseBillItemID,
			Description:        item.Description,
			Quantity:           item.Quantity,
			UnitPrice:          item.UnitPrice,
			TaxRate:            item.TaxRate,
			Total:              total,
			Reason:             item.Reason,
		})

		totalAmount += total
	}

	purchaseReturn.Amount = totalAmount

	if err := utils.DB.Create(&purchaseReturn).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create purchase return"})
		return
	}

	c.JSON(http.StatusCreated, purchaseReturn)
}

func UpdatePurchaseReturn(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var purchaseReturn models.PurchaseReturn
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&purchaseReturn).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Purchase return not found"})
		return
	}

	if purchaseReturn.Status != "draft" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot edit processed purchase return"})
		return
	}

	var input struct {
		Date       string `json:"date"`
		Reason     string `json:"reason"`
		RefundMode string `json:"refund_mode"`
		Notes      string `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	updates := map[string]interface{}{
		"reason":      input.Reason,
		"refund_mode": input.RefundMode,
		"notes":       input.Notes,
	}

	if input.Date != "" {
		parsedDate, err := time.Parse("2006-01-02", input.Date)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid date format, expected YYYY-MM-DD"})
			return
		}
		updates["date"] = parsedDate
	}

	if err := utils.DB.Model(&purchaseReturn).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update purchase return"})
		return
	}

	c.JSON(http.StatusOK, purchaseReturn)
}

func ProcessPurchaseReturn(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var purchaseReturn models.PurchaseReturn
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Preload("Items").First(&purchaseReturn).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Purchase return not found"})
		return
	}

	if purchaseReturn.Status != "draft" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Purchase return already processed"})
		return
	}

	// Update stock entries for returned items (reduce stock since we're returning to vendor)
	for _, item := range purchaseReturn.Items {
		entry := models.StockEntry{
			ID:         uuid.New(),
			UserID:     userID,
			ItemName:   item.Description,
			EntryType:  "return",
			Quantity:   -item.Quantity,
			BalanceQty: 0,
			CostPrice:  item.UnitPrice,
			EntryDate:  purchaseReturn.Date,
		}
		utils.DB.Create(&entry)
	}

	purchaseReturn.Status = "processed"
	utils.DB.Save(&purchaseReturn)

	// Returning stock to the vendor settles one of two ways: money-settled
	// refunds (cash, original payment) record the incoming cash/bank row,
	// while a credit note reduces what we owe them (vendor balances are
	// negative while payable) and is applied to the linked bill's
	// outstanding amount.
	if purchaseReturn.Amount > 0 {
		var linkedBill *models.PurchaseBill
		if purchaseReturn.PurchaseBillID != uuid.Nil {
			var bill models.PurchaseBill
			if err := utils.DB.Where("user_id = ? AND id = ?", userID, purchaseReturn.PurchaseBillID).First(&bill).Error; err == nil {
				linkedBill = &bill
			}
		}
		if refundSettlesInMoney(purchaseReturn.RefundMode) {
			var linkedMode string
			var linkedAccountID *uuid.UUID
			if linkedBill != nil {
				linkedMode = linkedBill.PaymentMode
				linkedAccountID = linkedBill.BankAccountID
			}
			accountID := resolveRefundBankAccount(userID, purchaseReturn.RefundMode, linkedMode, linkedAccountID)
			desc := fmt.Sprintf("Purchase return %s refund", purchaseReturn.ReturnNumber)
			if err := recordPaymentCashTxn(utils.DB, userID, accountID, "in", purchaseReturn.Amount, purchaseReturn.Date, purchaseReturn.ReturnNumber, desc); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to record refund"})
				return
			}
		} else {
			adjustPartyBalance(utils.DB, userID, purchaseReturn.PartyID, purchaseReturn.Amount)
			settlePurchaseBillWithReturn(linkedBill, purchaseReturn.Amount)
			if err := issueDebitNoteForReturn(userID, &purchaseReturn); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create debit note"})
				return
			}
		}
	}

	c.JSON(http.StatusOK, purchaseReturn)
}

// issueDebitNoteForReturn writes the DebitNote document for a
// credit-settled purchase return so it appears on the debit notes list.
// The note is created already issued because the balance and bill effects
// are applied by the caller.
func issueDebitNoteForReturn(userID uuid.UUID, purchaseReturn *models.PurchaseReturn) error {
	var count int64
	if err := utils.DB.Model(&models.DebitNote{}).Where("user_id = ?", userID).Count(&count).Error; err != nil {
		return err
	}

	reason := fmt.Sprintf("Purchase return %s", purchaseReturn.ReturnNumber)
	if purchaseReturn.Reason != "" {
		reason += " - " + purchaseReturn.Reason
	}

	debitNote := models.DebitNote{
		ID:              uuid.New(),
		UserID:          userID,
		PurchaseBillID:  purchaseReturn.PurchaseBillID,
		PartyID:         purchaseReturn.PartyID,
		DebitNoteNumber: fmt.Sprintf("DN-%04d", count+1),
		Date:            purchaseReturn.Date,
		Status:          "issued",
		Reason:          reason,
		RefundMode:      "debit_note",
		TotalAmount:     purchaseReturn.Amount,
	}
	for _, item := range purchaseReturn.Items {
		debitNote.Items = append(debitNote.Items, models.DebitNoteItem{
			ID:                 uuid.New(),
			DebitNoteID:        debitNote.ID,
			PurchaseBillItemID: item.PurchaseBillItemID,
			Description:        item.Description,
			Quantity:           item.Quantity,
			UnitPrice:          item.UnitPrice,
			TaxRate:            item.TaxRate,
			Total:              item.Total,
			Reason:             item.Reason,
		})
	}

	// Omit PurchaseBillID when the return is not linked to a bill so NULL is
	// written — a zero UUID would violate the debit_notes purchase_bill FK.
	if purchaseReturn.PurchaseBillID == uuid.Nil {
		return utils.DB.Omit("PurchaseBillID", "PurchaseBill").Create(&debitNote).Error
	}
	return utils.DB.Create(&debitNote).Error
}

// settlePurchaseBillWithReturn applies a credit-settled return against the
// linked bill's outstanding amount. The covered share lands on paid_amount
// because balance_due is always derived as total - paid across the codebase.
func settlePurchaseBillWithReturn(bill *models.PurchaseBill, amount float64) {
	if bill == nil || amount <= 0 || bill.BalanceDue <= 0 {
		return
	}
	covered := amount
	if covered > bill.BalanceDue {
		covered = bill.BalanceDue
	}
	newPaid := bill.PaidAmount + covered
	status := "unpaid"
	if bill.TotalAmount > 0 && newPaid+0.01 >= bill.TotalAmount {
		status = "paid"
	} else if newPaid > 0 {
		status = "partial"
	}
	balanceDue := bill.TotalAmount - newPaid
	if balanceDue < 0 {
		balanceDue = 0
	}
	utils.DB.Model(bill).Updates(map[string]interface{}{
		"paid_amount": newPaid,
		"balance_due": balanceDue,
		"status":      status,
	})
}

func DeletePurchaseReturn(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var purchaseReturn models.PurchaseReturn
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&purchaseReturn).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Purchase return not found"})
		return
	}

	if purchaseReturn.Status != "draft" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot delete processed purchase return"})
		return
	}

	utils.DB.Delete(&purchaseReturn)
	c.JSON(http.StatusOK, gin.H{"message": "Purchase return deleted"})
}
