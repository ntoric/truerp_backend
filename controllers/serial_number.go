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

func GetSerialNumbers(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var serialNumbers []models.SerialNumber
	query := utils.DB.Where("user_id = ?", userID)

	if productID := c.Query("product_id"); productID != "" {
		query = query.Where("product_id = ?", productID)
	}

	if status := c.Query("status"); status != "" {
		query = query.Where("status = ?", status)
	}

	if warehouseID := c.Query("warehouse_id"); warehouseID != "" {
		query = query.Where("warehouse_id = ?", warehouseID)
	}

	if err := query.Order("created_at DESC").Find(&serialNumbers).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch serial numbers"})
		return
	}

	c.JSON(http.StatusOK, serialNumbers)
}

func GetSerialNumber(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var serialNumber models.SerialNumber
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&serialNumber).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Serial number not found"})
		return
	}

	c.JSON(http.StatusOK, serialNumber)
}

func CreateSerialNumber(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		ProductID   uuid.UUID  `json:"product_id" binding:"required"`
		SerialNumber string   `json:"serial_number" binding:"required"`
		WarehouseID *uuid.UUID `json:"warehouse_id"`
		BatchNo     string    `json:"batch_no"`
		MfgDate     *time.Time `json:"mfg_date"`
		ExpDate     *time.Time `json:"exp_date"`
		CostPrice   float64   `json:"cost_price"`
		Notes       string    `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Validate product
	var product models.Product
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, input.ProductID).First(&product).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid product"})
		return
	}

	serialNumber := models.SerialNumber{
		ID:           uuid.New(),
		UserID:       userID,
		ProductID:    input.ProductID,
		SerialNumber: input.SerialNumber,
		Status:       "in_stock",
		WarehouseID:  input.WarehouseID,
		BatchNo:      input.BatchNo,
		MfgDate:      input.MfgDate,
		ExpDate:      input.ExpDate,
		CostPrice:    input.CostPrice,
		Notes:        input.Notes,
	}

	if err := utils.DB.Create(&serialNumber).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create serial number"})
		return
	}

	c.JSON(http.StatusCreated, serialNumber)
}

func UpdateSerialNumber(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var input struct {
		Status      string    `json:"status"`
		WarehouseID *uuid.UUID `json:"warehouse_id"`
		InvoiceID   *uuid.UUID `json:"invoice_id"`
		Notes       string    `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var serialNumber models.SerialNumber
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&serialNumber).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Serial number not found"})
		return
	}

	updates := map[string]interface{}{
		"status":       input.Status,
		"warehouse_id": input.WarehouseID,
		"invoice_id":   input.InvoiceID,
		"notes":        input.Notes,
	}

	if input.Status == "sold" {
		now := time.Now()
		updates["sold_date"] = &now
	}

	if err := utils.DB.Model(&serialNumber).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update serial number"})
		return
	}

	c.JSON(http.StatusOK, serialNumber)
}

func DeleteSerialNumber(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Delete(&models.SerialNumber{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete serial number"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Serial number deleted successfully"})
}

func BulkCreateSerialNumbers(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		ProductID   uuid.UUID  `json:"product_id" binding:"required"`
		WarehouseID *uuid.UUID `json:"warehouse_id"`
		BatchNo     string    `json:"batch_no"`
		SerialNumbers []string `json:"serial_numbers" binding:"required,min=1"`
		CostPrice   float64   `json:"cost_price"`
		MfgDate     *time.Time `json:"mfg_date"`
		ExpDate     *time.Time `json:"exp_date"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Validate product
	var product models.Product
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, input.ProductID).First(&product).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid product"})
		return
	}

	var createdSerialNumbers []models.SerialNumber
	var errors []string

	for _, sn := range input.SerialNumbers {
		serialNumber := models.SerialNumber{
			ID:           uuid.New(),
			UserID:       userID,
			ProductID:    input.ProductID,
			SerialNumber: sn,
			Status:       "in_stock",
			WarehouseID:  input.WarehouseID,
			BatchNo:      input.BatchNo,
			MfgDate:      input.MfgDate,
			ExpDate:      input.ExpDate,
			CostPrice:    input.CostPrice,
		}

		if err := utils.DB.Create(&serialNumber).Error; err != nil {
			errors = append(errors, fmt.Sprintf("Failed to create serial number %s: %v", sn, err))
			continue
		}

		createdSerialNumbers = append(createdSerialNumbers, serialNumber)
	}

	c.JSON(http.StatusCreated, gin.H{
		"created": createdSerialNumbers,
		"errors":  errors,
	})
}
