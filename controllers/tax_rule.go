package controllers

import (
	"truerp/models"
	"truerp/utils"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func GetTaxRules(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var rules []models.TaxRule
	query := utils.DB.Where("user_id = ?", userID)

	if country := c.Query("country"); country != "" {
		query = query.Where("country = ?", country)
	}

	if taxType := c.Query("tax_type"); taxType != "" {
		query = query.Where("tax_type = ?", taxType)
	}

	if isActive := c.Query("is_active"); isActive != "" {
		query = query.Where("is_active = ?", isActive == "true")
	}

	if err := query.Order("country, state, tax_type").Find(&rules).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch tax rules"})
		return
	}

	c.JSON(http.StatusOK, rules)
}

func GetTaxRule(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var rule models.TaxRule
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&rule).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Tax rule not found"})
		return
	}

	c.JSON(http.StatusOK, rule)
}

func CreateTaxRule(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		Country         string    `json:"country" binding:"required"`
		CountryCode     string    `json:"country_code" binding:"required"`
		State           string    `json:"state"`
		StateCode       string    `json:"state_code"`
		TaxType         string    `json:"tax_type" binding:"required"`
		TaxName         string    `json:"tax_name" binding:"required"`
		Rate            float64   `json:"rate" binding:"required"`
		IsCompound      bool      `json:"is_compound"`
		ThresholdAmount float64   `json:"threshold_amount"`
		HSNCode         string    `json:"hsn_code"`
		Category        string    `json:"category"`
		EffectiveFrom   *time.Time `json:"effective_from"`
		EffectiveUntil  *time.Time `json:"effective_until"`
		Notes           string    `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	rule := models.TaxRule{
		ID:              uuid.New(),
		UserID:          userID,
		Country:         input.Country,
		CountryCode:     input.CountryCode,
		State:           input.State,
		StateCode:       input.StateCode,
		TaxType:         input.TaxType,
		TaxName:         input.TaxName,
		Rate:            input.Rate,
		IsCompound:      input.IsCompound,
		ThresholdAmount:  input.ThresholdAmount,
		HSNCode:         input.HSNCode,
		Category:        input.Category,
		EffectiveFrom:  input.EffectiveFrom,
		EffectiveUntil: input.EffectiveUntil,
		IsActive:        true,
		Notes:           input.Notes,
	}

	if err := utils.DB.Create(&rule).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create tax rule"})
		return
	}

	c.JSON(http.StatusCreated, rule)
}

func UpdateTaxRule(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var input struct {
		Country         string    `json:"country"`
		CountryCode     string    `json:"country_code"`
		State           string    `json:"state"`
		StateCode       string    `json:"state_code"`
		TaxType         string    `json:"tax_type"`
		TaxName         string    `json:"tax_name"`
		Rate            float64   `json:"rate"`
		IsCompound      bool      `json:"is_compound"`
		ThresholdAmount float64   `json:"threshold_amount"`
		HSNCode         string    `json:"hsn_code"`
		Category        string    `json:"category"`
		EffectiveFrom   *time.Time `json:"effective_from"`
		EffectiveUntil  *time.Time `json:"effective_until"`
		IsActive        *bool     `json:"is_active"`
		Notes           string    `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var rule models.TaxRule
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&rule).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Tax rule not found"})
		return
	}

	updates := map[string]interface{}{
		"country":          input.Country,
		"country_code":     input.CountryCode,
		"state":            input.State,
		"state_code":       input.StateCode,
		"tax_type":         input.TaxType,
		"tax_name":         input.TaxName,
		"rate":             input.Rate,
		"is_compound":      input.IsCompound,
		"threshold_amount": input.ThresholdAmount,
		"hsn_code":         input.HSNCode,
		"category":         input.Category,
		"effective_from":   input.EffectiveFrom,
		"effective_until":  input.EffectiveUntil,
		"notes":            input.Notes,
	}

	if input.IsActive != nil {
		updates["is_active"] = *input.IsActive
	}

	if err := utils.DB.Model(&rule).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update tax rule"})
		return
	}

	c.JSON(http.StatusOK, rule)
}

func DeleteTaxRule(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Delete(&models.TaxRule{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete tax rule"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Tax rule deleted successfully"})
}

func GetApplicableTaxRule(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	country := c.Query("country")
	state := c.Query("state")
	hsnCode := c.Query("hsn_code")
	category := c.Query("category")
	amountStr := c.Query("amount")
	amount := 0.0
	if amountStr != "" {
		if val, err := strconv.ParseFloat(amountStr, 64); err == nil {
			amount = val
		}
	}

	var rules []models.TaxRule
	query := utils.DB.Where("user_id = ? AND is_active = ?", userID, true)

	if country != "" {
		query = query.Where("country = ?", country)
	}
	if state != "" {
		query = query.Where("state = ?", state)
	}
	if hsnCode != "" {
		query = query.Where("(hsn_code = ? OR hsn_code = '')", hsnCode)
	}
	if category != "" {
		query = query.Where("(category = ? OR category = '')", category)
	}

	now := time.Now()
	query = query.Where("(effective_from IS NULL OR effective_from <= ?)", now)
	query = query.Where("(effective_until IS NULL OR effective_until >= ?)", now)

	if err := query.Order("is_compound DESC, rate DESC").Find(&rules).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch applicable tax rules"})
		return
	}

	// Filter by threshold amount
	var applicableRules []models.TaxRule
	for _, rule := range rules {
		if rule.ThresholdAmount == 0 || amount >= rule.ThresholdAmount {
			applicableRules = append(applicableRules, rule)
		}
	}

	c.JSON(http.StatusOK, applicableRules)
}
