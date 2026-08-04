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

// GetPOSDrafts - Get all POS drafts for a user
func GetPOSDrafts(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var drafts []models.POSDraft
	query := utils.DB.Where("user_id = ?", userID)

	if sessionID := c.Query("session_id"); sessionID != "" {
		query = query.Where("session_id = ?", sessionID)
	}

	if err := query.Order("created_at DESC").Find(&drafts).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch drafts"})
		return
	}

	c.JSON(http.StatusOK, drafts)
}

// GetPOSDraft - Get a specific POS draft
func GetPOSDraft(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var draft models.POSDraft
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&draft).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Draft not found"})
		return
	}

	c.JSON(http.StatusOK, draft)
}

// CreatePOSDraft - Create a new POS draft
func CreatePOSDraft(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		Title    string  `json:"title" binding:"required"`
		CartData string  `json:"cart_data" binding:"required"`
		PartyID  *string `json:"party_id"`
		Notes    string  `json:"notes"`
		SessionID *string `json:"session_id"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	draft := models.POSDraft{
		ID:       uuid.New(),
		UserID:   userID,
		Title:    input.Title,
		CartData: input.CartData,
		Notes:    input.Notes,
		IsActive: true,
	}

	if input.PartyID != nil {
		partyUUID := uuid.MustParse(*input.PartyID)
		draft.PartyID = &partyUUID
	}

	if input.SessionID != nil {
		sessionUUID := uuid.MustParse(*input.SessionID)
		draft.SessionID = &sessionUUID
	}

	if err := utils.DB.Create(&draft).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create draft"})
		return
	}

	c.JSON(http.StatusCreated, draft)
}

// UpdatePOSDraft - Update an existing POS draft
func UpdatePOSDraft(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var input struct {
		Title    *string `json:"title"`
		CartData *string `json:"cart_data"`
		PartyID  *string `json:"party_id"`
		Notes    *string `json:"notes"`
		IsActive *bool   `json:"is_active"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var draft models.POSDraft
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&draft).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Draft not found"})
		return
	}

	updates := make(map[string]interface{})
	if input.Title != nil {
		updates["title"] = *input.Title
	}
	if input.CartData != nil {
		updates["cart_data"] = *input.CartData
	}
	if input.PartyID != nil {
		partyUUID := uuid.MustParse(*input.PartyID)
		updates["party_id"] = partyUUID
	}
	if input.Notes != nil {
		updates["notes"] = *input.Notes
	}
	if input.IsActive != nil {
		updates["is_active"] = *input.IsActive
	}
	updates["updated_at"] = time.Now()

	if err := utils.DB.Model(&draft).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update draft"})
		return
	}

	c.JSON(http.StatusOK, draft)
}

// DeletePOSDraft - Delete a POS draft
func DeletePOSDraft(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var draft models.POSDraft
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&draft).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Draft not found"})
		return
	}

	if err := utils.DB.Delete(&draft).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete draft"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Draft deleted successfully"})
}

// ConvertDraftToInvoice - Convert a POS draft to an invoice
func ConvertDraftToInvoice(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var draft models.POSDraft
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&draft).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Draft not found"})
		return
	}

	// Parse cart data and create invoice
	var input struct {
		PaymentMode string `json:"payment_mode" binding:"required"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Create invoice from draft data
	invoice := models.Invoice{
		ID:         uuid.New(),
		UserID:     userID,
		PartyID:    *draft.PartyID,
		Date:       time.Now(),
		Status:     "paid",
		PaymentMode: input.PaymentMode,
		Notes:      draft.Notes,
	}

	// Generate invoice number
	var count int64
	utils.DB.Model(&models.Invoice{}).Where("user_id = ?", userID).Count(&count)
	invoice.InvoiceNumber = "INV-" + time.Now().Format("20060102") + "-" + fmt.Sprintf("%04d", count+1)

	// Parse cart data and create invoice items
	// This would need proper JSON parsing in a real implementation
	// For now, we'll assume the cart_data is properly formatted

	if err := utils.DB.Create(&invoice).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create invoice"})
		return
	}

	// Delete the draft after conversion
	utils.DB.Delete(&draft)

	c.JSON(http.StatusCreated, invoice)
}
