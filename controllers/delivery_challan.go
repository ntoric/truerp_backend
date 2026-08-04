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

func GetDeliveryChallans(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var challans []models.DeliveryChallan
	query := utils.DB.Where("user_id = ?", userID).Preload("Party").Preload("Invoice")

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

	if err := query.Order("date DESC, created_at DESC").Find(&challans).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch delivery challans"})
		return
	}

	c.JSON(http.StatusOK, challans)
}

func GetDeliveryChallan(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var challan models.DeliveryChallan
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).
		Preload("Party").
		Preload("Invoice").
		Preload("Items").
		First(&challan).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Delivery challan not found"})
		return
	}

	c.JSON(http.StatusOK, challan)
}

func CreateDeliveryChallan(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		ChallanNumber string       `json:"challan_number" binding:"required"`
		PartyID       uuid.UUID    `json:"party_id" binding:"required"`
		InvoiceID     *uuid.UUID   `json:"invoice_id"`
		Date          time.Time    `json:"date" binding:"required"`
		DueDate       *time.Time   `json:"due_date"`
		Status        string       `json:"status"`
		Notes         string       `json:"notes"`
		Terms         string       `json:"terms"`
		Signature     string       `json:"signature"`
		VehicleNumber string       `json:"vehicle_number"`
		TransportMode string       `json:"transport_mode"`
		Items         []struct {
			Description string               `json:"description"`
			Quantity    models.FlexibleFloat `json:"quantity"`
			Unit        string               `json:"unit"`
			UnitPrice   models.FlexibleFloat `json:"unit_price"`
			BatchNo     string               `json:"batch_no"`
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

	// Validate invoice if provided
	if input.InvoiceID != nil {
		var invoice models.Invoice
		if err := utils.DB.Where("user_id = ? AND id = ?", userID, input.InvoiceID).First(&invoice).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid invoice"})
			return
		}
	}

	challan := models.DeliveryChallan{
		ID:            uuid.New(),
		UserID:        userID,
		ChallanNumber: input.ChallanNumber,
		PartyID:       input.PartyID,
		InvoiceID:     input.InvoiceID,
		Date:          input.Date,
		DueDate:       input.DueDate,
		Status:        input.Status,
		Notes:         input.Notes,
		Terms:         input.Terms,
		Signature:     input.Signature,
		VehicleNumber: input.VehicleNumber,
		TransportMode: input.TransportMode,
	}

	// Calculate totals
	var subTotal, totalQuantity float64
	for _, item := range input.Items {
		qty := item.Quantity.Float64()
		unitPrice := item.UnitPrice.Float64()
		itemTotal := qty * unitPrice

		challan.Items = append(challan.Items, models.DeliveryChallanItem{
			ID:          uuid.New(),
			Description: item.Description,
			Quantity:    qty,
			Unit:        item.Unit,
			UnitPrice:   unitPrice,
			Total:       itemTotal,
			BatchNo:     item.BatchNo,
		})

		subTotal += itemTotal
		totalQuantity += qty
	}

	challan.SubTotal = subTotal
	challan.TotalQuantity = totalQuantity

	if err := utils.DB.Create(&challan).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create delivery challan"})
		return
	}

	c.JSON(http.StatusCreated, challan)
}

func UpdateDeliveryChallan(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var challan models.DeliveryChallan
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&challan).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Delivery challan not found"})
		return
	}

	if challan.Status == "delivered" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot edit a delivered challan"})
		return
	}

	var input struct {
		Status        string     `json:"status"`
		Notes         string     `json:"notes"`
		Terms         string     `json:"terms"`
		Signature     string     `json:"signature"`
		VehicleNumber string     `json:"vehicle_number"`
		TransportMode string     `json:"transport_mode"`
		DueDate       *time.Time `json:"due_date"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	updates := map[string]interface{}{
		"status":        input.Status,
		"notes":         input.Notes,
		"terms":         input.Terms,
		"signature":     input.Signature,
		"vehicle_number": input.VehicleNumber,
		"transport_mode": input.TransportMode,
		"due_date":      input.DueDate,
	}

	if err := utils.DB.Model(&challan).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update delivery challan"})
		return
	}

	c.JSON(http.StatusOK, challan)
}

func DeleteDeliveryChallan(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var challan models.DeliveryChallan
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&challan).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Delivery challan not found"})
		return
	}

	if challan.Status == "delivered" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot delete a delivered challan"})
		return
	}

	if err := utils.DB.Delete(&challan).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete delivery challan"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Delivery challan deleted successfully"})
}

func GetNextChallanNumber(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var count int64
	utils.DB.Model(&models.DeliveryChallan{}).Where("user_id = ?", userID).Count(&count)

	nextNum := fmt.Sprintf("DC-%04d", count+1)
	c.JSON(http.StatusOK, gin.H{"challan_number": nextNum})
}
