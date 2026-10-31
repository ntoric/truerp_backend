package controllers

import (
	"errors"
	"net/http"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func defaultSaleDiscountSettings() models.SaleDiscountSettings {
	return models.SaleDiscountSettings{
		Mode:         "manual",
		DiscountType: "percent",
	}
}

// GetSaleDiscountSettings returns the discount mode + auto-discount rule for
// the active store. Falls back to the global (NULL store) row, then to the
// manual defaults. Any authenticated user may read so the billing UI and POS
// can decide which discount widget to show.
func GetSaleDiscountSettings(c *gin.Context) {
	var settings models.SaleDiscountSettings
	found := false
	if storeID, ok := currentStoreID(c); ok {
		found = utils.DB.Where("store_id = ?", storeID).
			Order("created_at asc").First(&settings).Error == nil
	}
	if !found {
		found = utils.DB.Where("store_id IS NULL").
			Order("created_at asc").First(&settings).Error == nil
	}
	if !found {
		settings = defaultSaleDiscountSettings()
	}
	if settings.Mode != "auto" {
		settings.Mode = "manual"
	}
	c.JSON(http.StatusOK, settings)
}

// UpdateSaleDiscountSettings updates the discount mode + auto-discount rule
// for the active store. Super admin only. When no store context is present
// (no stores exist), the global fallback row is updated instead.
func UpdateSaleDiscountSettings(c *gin.Context) {
	var input struct {
		Mode          string  `json:"mode"`
		DiscountType  string  `json:"discount_type"`
		DiscountValue float64 `json:"discount_value"`
		MinSaleAmount float64 `json:"min_sale_amount"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if input.Mode != "auto" {
		input.Mode = "manual"
	}
	if input.DiscountType != "amount" {
		input.DiscountType = "percent"
	}
	input.DiscountValue = max(0, input.DiscountValue)
	input.MinSaleAmount = max(0, input.MinSaleAmount)

	var storeID *uuid.UUID
	query := utils.DB.Order("created_at asc")
	if id, ok := currentStoreID(c); ok {
		storeID = &id
		query = query.Where("store_id = ?", id)
	} else {
		query = query.Where("store_id IS NULL")
	}

	var settings models.SaleDiscountSettings
	err := query.First(&settings).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		settings = models.SaleDiscountSettings{
			ID:            uuid.New(),
			StoreID:       storeID,
			Mode:          input.Mode,
			DiscountType:  input.DiscountType,
			DiscountValue: input.DiscountValue,
			MinSaleAmount: input.MinSaleAmount,
		}
		if err := utils.DB.Create(&settings).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save sale discount settings"})
			return
		}
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load sale discount settings"})
		return
	} else {
		if err := utils.DB.Model(&settings).Updates(map[string]interface{}{
			"mode":            input.Mode,
			"discount_type":   input.DiscountType,
			"discount_value":  input.DiscountValue,
			"min_sale_amount": input.MinSaleAmount,
		}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update sale discount settings"})
			return
		}
	}

	c.JSON(http.StatusOK, settings)
}
