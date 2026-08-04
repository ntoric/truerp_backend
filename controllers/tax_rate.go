package controllers

import (
	"truerp/models"
	"truerp/utils"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func GetTaxRates(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var rates []models.TaxRate
	query := utils.DB.Where("user_id = ?", userID)

	if isActive := c.Query("is_active"); isActive != "" {
		query = query.Where("is_active = ?", isActive == "true")
	}

	if err := query.Order("is_default DESC, total_rate ASC").Find(&rates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch tax rates"})
		return
	}

	c.JSON(http.StatusOK, rates)
}

func GetTaxRate(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var rate models.TaxRate
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&rate).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Tax rate not found"})
		return
	}

	c.JSON(http.StatusOK, rate)
}

func CreateTaxRate(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		Name        string  `json:"name" binding:"required"`
		CGSTRate    float64 `json:"cgst_rate"`
		SGSTRate    float64 `json:"sgst_rate"`
		IGSTRate    float64 `json:"igst_rate"`
		CESSRate    float64 `json:"cess_rate"`
		TotalRate   float64 `json:"total_rate" binding:"required"`
		IsDefault   bool    `json:"is_default"`
		Description string  `json:"description"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// If setting as default, remove default from other rates
	if input.IsDefault {
		utils.DB.Model(&models.TaxRate{}).Where("user_id = ?", userID).Update("is_default", false)
	}

	rate := models.TaxRate{
		ID:          uuid.New(),
		UserID:      userID,
		Name:        input.Name,
		CGSTRate:    input.CGSTRate,
		SGSTRate:    input.SGSTRate,
		IGSTRate:    input.IGSTRate,
		CESSRate:    input.CESSRate,
		TotalRate:   input.TotalRate,
		IsDefault:   input.IsDefault,
		Description: input.Description,
		IsActive:    true,
	}

	if err := utils.DB.Create(&rate).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create tax rate"})
		return
	}

	c.JSON(http.StatusCreated, rate)
}

func UpdateTaxRate(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var input struct {
		Name        string  `json:"name"`
		CGSTRate    float64 `json:"cgst_rate"`
		SGSTRate    float64 `json:"sgst_rate"`
		IGSTRate    float64 `json:"igst_rate"`
		CESSRate    float64 `json:"cess_rate"`
		TotalRate   float64 `json:"total_rate"`
		IsDefault   *bool   `json:"is_default"`
		Description string  `json:"description"`
		IsActive    *bool   `json:"is_active"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var rate models.TaxRate
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&rate).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Tax rate not found"})
		return
	}

	// If setting as default, remove default from other rates
	if input.IsDefault != nil && *input.IsDefault {
		utils.DB.Model(&models.TaxRate{}).Where("user_id = ? AND id != ?", userID, id).Update("is_default", false)
	}

	updates := map[string]interface{}{
		"name":        input.Name,
		"cgst_rate":   input.CGSTRate,
		"sgst_rate":   input.SGSTRate,
		"igst_rate":   input.IGSTRate,
		"cess_rate":   input.CESSRate,
		"total_rate":  input.TotalRate,
		"description": input.Description,
	}

	if input.IsDefault != nil {
		updates["is_default"] = *input.IsDefault
	}
	if input.IsActive != nil {
		updates["is_active"] = *input.IsActive
	}

	if err := utils.DB.Model(&rate).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update tax rate"})
		return
	}

	c.JSON(http.StatusOK, rate)
}

func DeleteTaxRate(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var rate models.TaxRate
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&rate).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Tax rate not found"})
		return
	}

	if rate.IsDefault {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot delete default tax rate"})
		return
	}

	if err := utils.DB.Delete(&rate).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete tax rate"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Tax rate deleted successfully"})
}

func GetDefaultTaxRate(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var rate models.TaxRate
	if err := utils.DB.Where("user_id = ? AND is_default = ? AND is_active = ?", userID, true, true).First(&rate).Error; err != nil {
		// If no default, return the first active rate
		if err := utils.DB.Where("user_id = ? AND is_active = ?", userID, true).First(&rate).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "No tax rate found"})
			return
		}
	}

	c.JSON(http.StatusOK, rate)
}
