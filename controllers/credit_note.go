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

func GetCreditNotes(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var creditNotes []models.CreditNote
	query := utils.DB.Where("user_id = ?", userID).Preload("Party").Preload("Invoice")

	if partyID := c.Query("party_id"); partyID != "" {
		query = query.Where("party_id = ?", partyID)
	}
	if invoiceID := c.Query("invoice_id"); invoiceID != "" {
		query = query.Where("invoice_id = ?", invoiceID)
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

	if err := query.Order("date DESC").Find(&creditNotes).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch credit notes"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": creditNotes})
}

func GetCreditNote(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var creditNote models.CreditNote
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Preload("Party").Preload("Invoice").Preload("Items").First(&creditNote).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Credit note not found"})
		return
	}

	c.JSON(http.StatusOK, creditNote)
}

func CreateCreditNote(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		InvoiceID   uuid.UUID  `json:"invoice_id" binding:"required"`
		Date        time.Time  `json:"date" binding:"required"`
		Reason      string     `json:"reason"`
		RefundMode  string     `json:"refund_mode"`
		Items       []struct {
			InvoiceItemID uuid.UUID `json:"invoice_item_id"`
			Description   string    `json:"description" binding:"required"`
			Quantity      float64   `json:"quantity" binding:"required,gt=0"`
			UnitPrice     float64   `json:"unit_price" binding:"required"`
			TaxRate       float64   `json:"tax_rate"`
			Reason        string    `json:"reason"`
		} `json:"items" binding:"required,min=1"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Validate that the invoice exists and belongs to the user
	var invoice models.Invoice
	if err := utils.DB.Where("id = ? AND user_id = ?", input.InvoiceID, userID).First(&invoice).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Sales invoice not found"})
		return
	}

	var count int64
	utils.DB.Model(&models.CreditNote{}).Where("user_id = ?", userID).Count(&count)

	creditNote := models.CreditNote{
		ID:               uuid.New(),
		UserID:           userID,
		InvoiceID:        input.InvoiceID,
		PartyID:          invoice.PartyID,
		CreditNoteNumber: fmt.Sprintf("CN-%04d", count+1),
		Date:             input.Date,
		Status:           "draft",
		Reason:           input.Reason,
		RefundMode:       input.RefundMode,
	}

	var totalAmount float64
	for _, item := range input.Items {
		taxAmount := item.UnitPrice * item.Quantity * (item.TaxRate / 100)
		total := item.UnitPrice*item.Quantity + taxAmount

		creditNote.Items = append(creditNote.Items, models.CreditNoteItem{
			ID:            uuid.New(),
			CreditNoteID:  creditNote.ID,
			InvoiceItemID: item.InvoiceItemID,
			Description:   item.Description,
			Quantity:      item.Quantity,
			UnitPrice:     item.UnitPrice,
			TaxRate:       item.TaxRate,
			Total:         total,
			Reason:        item.Reason,
		})

		totalAmount += total
	}

	creditNote.TotalAmount = totalAmount

	if err := utils.DB.Create(&creditNote).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create credit note"})
		return
	}

	c.JSON(http.StatusCreated, creditNote)
}

func UpdateCreditNote(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var creditNote models.CreditNote
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&creditNote).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Credit note not found"})
		return
	}

	if creditNote.Status != "draft" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot edit issued credit note"})
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

	if err := utils.DB.Model(&creditNote).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update credit note"})
		return
	}

	c.JSON(http.StatusOK, creditNote)
}

func IssueCreditNote(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var creditNote models.CreditNote
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Preload("Items").First(&creditNote).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Credit note not found"})
		return
	}

	if creditNote.Status != "draft" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Credit note already issued"})
		return
	}

	// Update the linked sales invoice - reduce amount paid
	var invoice models.Invoice
	if err := utils.DB.Where("id = ? AND user_id = ?", creditNote.InvoiceID, userID).First(&invoice).Error; err == nil {
		invoice.AmountPaid -= creditNote.TotalAmount
		utils.DB.Save(&invoice)
	}

	creditNote.Status = "issued"
	utils.DB.Save(&creditNote)

	c.JSON(http.StatusOK, creditNote)
}

func DeleteCreditNote(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var creditNote models.CreditNote
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&creditNote).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Credit note not found"})
		return
	}

	if creditNote.Status != "draft" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot delete issued credit note"})
		return
	}

	utils.DB.Delete(&creditNote)
	c.JSON(http.StatusOK, gin.H{"message": "Credit note deleted"})
}

func GetNextCreditNoteNumber(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var count int64
	utils.DB.Model(&models.CreditNote{}).Where("user_id = ?", userID).Count(&count)

	nextNum := fmt.Sprintf("CN-%04d", count+1)
	c.JSON(http.StatusOK, gin.H{"credit_note_number": nextNum})
}
