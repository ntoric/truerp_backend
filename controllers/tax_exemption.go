package controllers

import (
	"truerp/models"
	"truerp/utils"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func GetTaxExemptions(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var exemptions []models.TaxExemption
	query := utils.DB.Where("user_id = ?", userID)

	if exemptionType := c.Query("exemption_type"); exemptionType != "" {
		query = query.Where("exemption_type = ?", exemptionType)
	}

	if isActive := c.Query("is_active"); isActive != "" {
		query = query.Where("is_applicable = ?", isActive == "true")
	}

	if err := query.Order("created_at DESC").Find(&exemptions).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch tax exemptions"})
		return
	}

	c.JSON(http.StatusOK, exemptions)
}

func GetTaxExemption(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var exemption models.TaxExemption
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&exemption).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Tax exemption not found"})
		return
	}

	c.JSON(http.StatusOK, exemption)
}

func CreateTaxExemption(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		Name          string    `json:"name" binding:"required"`
		Code          string    `json:"code" binding:"required"`
		Description   string    `json:"description"`
		ExemptionType string    `json:"exemption_type" binding:"required"`
		MaxAmount     float64   `json:"max_amount"`
		ValidFrom     *time.Time `json:"valid_from"`
		ValidUntil    *time.Time `json:"valid_until"`
		Notes         string    `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	exemption := models.TaxExemption{
		ID:            uuid.New(),
		UserID:        userID,
		Name:          input.Name,
		Code:          input.Code,
		Description:   input.Description,
		ExemptionType: input.ExemptionType,
		MaxAmount:     input.MaxAmount,
		ValidFrom:     input.ValidFrom,
		ValidUntil:    input.ValidUntil,
		IsApplicable:  true,
		Notes:         input.Notes,
	}

	if err := utils.DB.Create(&exemption).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create tax exemption"})
		return
	}

	c.JSON(http.StatusCreated, exemption)
}

func UpdateTaxExemption(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var input struct {
		Name          string    `json:"name"`
		Code          string    `json:"code"`
		Description   string    `json:"description"`
		ExemptionType string    `json:"exemption_type"`
		MaxAmount     float64   `json:"max_amount"`
		ValidFrom     *time.Time `json:"valid_from"`
		ValidUntil    *time.Time `json:"valid_until"`
		IsApplicable  *bool     `json:"is_applicable"`
		Notes         string    `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var exemption models.TaxExemption
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&exemption).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Tax exemption not found"})
		return
	}

	updates := map[string]interface{}{
		"name":           input.Name,
		"code":           input.Code,
		"description":    input.Description,
		"exemption_type": input.ExemptionType,
		"max_amount":     input.MaxAmount,
		"valid_from":     input.ValidFrom,
		"valid_until":    input.ValidUntil,
		"notes":          input.Notes,
	}

	if input.IsApplicable != nil {
		updates["is_applicable"] = *input.IsApplicable
	}

	if err := utils.DB.Model(&exemption).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update tax exemption"})
		return
	}

	c.JSON(http.StatusOK, exemption)
}

func DeleteTaxExemption(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Delete(&models.TaxExemption{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete tax exemption"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Tax exemption deleted successfully"})
}
