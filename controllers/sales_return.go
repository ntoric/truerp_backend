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

	c.JSON(http.StatusOK, salesReturn)
}

func CreateSalesReturn(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		PartyID      uuid.UUID  `json:"party_id" binding:"required"`
		InvoiceID    uuid.UUID  `json:"invoice_id"`
		Date         time.Time  `json:"date" binding:"required"`
		Reason       string     `json:"reason"`
		RefundMode   string     `json:"refund_mode"`
		Notes        string     `json:"notes"`
		Items        []struct {
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
		taxAmount := item.UnitPrice * item.Quantity * (item.TaxRate / 100)
		total := item.UnitPrice*item.Quantity + taxAmount

		salesReturn.Items = append(salesReturn.Items, models.SalesReturnItem{
			ID:            uuid.New(),
			ReturnID:      salesReturn.ID,
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

	salesReturn.Amount = totalAmount

	if err := utils.DB.Create(&salesReturn).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create sales return"})
		return
	}

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
		Date       time.Time  `json:"date"`
		Reason     string     `json:"reason"`
		RefundMode string     `json:"refund_mode"`
		Notes      string     `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	updates := map[string]interface{}{
		"date":        input.Date,
		"reason":      input.Reason,
		"refund_mode": input.RefundMode,
		"notes":       input.Notes,
	}

	if err := utils.DB.Model(&salesReturn).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update sales return"})
		return
	}

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

	// Update stock entries for returned items
	for _, item := range salesReturn.Items {
		entry := models.StockEntry{
			ID:         uuid.New(),
			UserID:     userID,
			ItemName:   item.Description,
			EntryType:  "return",
			Quantity:   item.Quantity,
			BalanceQty: 0,
			CostPrice:  item.UnitPrice,
			EntryDate:  salesReturn.Date,
		}
		utils.DB.Create(&entry)
	}

	salesReturn.Status = "processed"
	utils.DB.Save(&salesReturn)

	c.JSON(http.StatusOK, salesReturn)
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
