package controllers

import (
	"truerp/models"
	"truerp/utils"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func GetDebitNotes(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var debitNotes []models.DebitNote
	query := utils.DB.Where("user_id = ?", userID).Preload("Party").Preload("PurchaseBill")

	if partyID := c.Query("party_id"); partyID != "" {
		query = query.Where("party_id = ?", partyID)
	}
	if purchaseBillID := c.Query("purchase_bill_id"); purchaseBillID != "" {
		query = query.Where("purchase_bill_id = ?", purchaseBillID)
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

	if err := query.Order("date DESC").Find(&debitNotes).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch debit notes"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": debitNotes})
}

func GetDebitNote(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var debitNote models.DebitNote
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Preload("Party").Preload("PurchaseBill").Preload("Items").First(&debitNote).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Debit note not found"})
		return
	}

	c.JSON(http.StatusOK, debitNote)
}

func CreateDebitNote(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		PurchaseBillID uuid.UUID  `json:"purchase_bill_id" binding:"required"`
		Date           time.Time  `json:"date" binding:"required"`
		Reason         string     `json:"reason"`
		RefundMode     string     `json:"refund_mode"`
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

	// Validate that the purchase bill exists and belongs to the user
	var purchaseBill models.PurchaseBill
	if err := utils.DB.Where("id = ? AND user_id = ?", input.PurchaseBillID, userID).First(&purchaseBill).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Purchase bill not found"})
		return
	}

	var count int64
	utils.DB.Model(&models.DebitNote{}).Where("user_id = ?", userID).Count(&count)

	debitNote := models.DebitNote{
		ID:              uuid.New(),
		UserID:          userID,
		PurchaseBillID:  input.PurchaseBillID,
		PartyID:         purchaseBill.PartyID,
		DebitNoteNumber: fmt.Sprintf("DN-%04d", count+1),
		Date:            input.Date,
		Status:          "draft",
		Reason:          input.Reason,
		RefundMode:      input.RefundMode,
	}

	var totalAmount float64
	for _, item := range input.Items {
		taxAmount := item.UnitPrice * item.Quantity * (item.TaxRate / 100)
		total := item.UnitPrice*item.Quantity + taxAmount

		debitNote.Items = append(debitNote.Items, models.DebitNoteItem{
			ID:                 uuid.New(),
			DebitNoteID:        debitNote.ID,
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

	debitNote.TotalAmount = totalAmount

	if err := utils.DB.Create(&debitNote).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create debit note"})
		return
	}

	c.JSON(http.StatusCreated, debitNote)
}

func UpdateDebitNote(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var debitNote models.DebitNote
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&debitNote).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Debit note not found"})
		return
	}

	if debitNote.Status != "draft" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot edit issued debit note"})
		return
	}

	var input struct {
		Date       time.Time `json:"date"`
		Reason     string    `json:"reason"`
		RefundMode string    `json:"refund_mode"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	updates := map[string]interface{}{
		"date":        input.Date,
		"reason":      input.Reason,
		"refund_mode": input.RefundMode,
	}

	if err := utils.DB.Model(&debitNote).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update debit note"})
		return
	}

	c.JSON(http.StatusOK, debitNote)
}

func IssueDebitNote(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var debitNote models.DebitNote
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Preload("Items").First(&debitNote).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Debit note not found"})
		return
	}

	if debitNote.Status != "draft" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Debit note already issued"})
		return
	}

	// Update the linked purchase bill - reduce paid amount
	var purchaseBill models.PurchaseBill
	if err := utils.DB.Where("id = ? AND user_id = ?", debitNote.PurchaseBillID, userID).First(&purchaseBill).Error; err == nil {
		purchaseBill.PaidAmount -= debitNote.TotalAmount
		purchaseBill.BalanceDue += debitNote.TotalAmount
		utils.DB.Save(&purchaseBill)
	}

	debitNote.Status = "issued"
	utils.DB.Save(&debitNote)

	c.JSON(http.StatusOK, debitNote)
}

func DeleteDebitNote(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var debitNote models.DebitNote
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&debitNote).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Debit note not found"})
		return
	}

	if debitNote.Status != "draft" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot delete issued debit note"})
		return
	}

	utils.DB.Delete(&debitNote)
	c.JSON(http.StatusOK, gin.H{"message": "Debit note deleted"})
}

func GetNextDebitNoteNumber(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var count int64
	utils.DB.Model(&models.DebitNote{}).Where("user_id = ?", userID).Count(&count)

	nextNum := fmt.Sprintf("DN-%04d", count+1)
	c.JSON(http.StatusOK, gin.H{"debit_note_number": nextNum})
}
