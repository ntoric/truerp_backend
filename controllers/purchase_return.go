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
		PartyID         uuid.UUID  `json:"party_id" binding:"required"`
		PurchaseBillID uuid.UUID  `json:"purchase_bill_id"`
		Date           string     `json:"date" binding:"required"`
		Reason         string     `json:"reason"`
		RefundMode     string     `json:"refund_mode"`
		Notes          string     `json:"notes"`
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
			ID:                uuid.New(),
			ReturnID:          purchaseReturn.ID,
			PurchaseBillItemID: item.PurchaseBillItemID,
			Description:       item.Description,
			Quantity:          item.Quantity,
			UnitPrice:         item.UnitPrice,
			TaxRate:           item.TaxRate,
			Total:             total,
			Reason:            item.Reason,
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
		Date       string     `json:"date"`
		Reason     string     `json:"reason"`
		RefundMode string     `json:"refund_mode"`
		Notes      string     `json:"notes"`
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

	c.JSON(http.StatusOK, purchaseReturn)
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
