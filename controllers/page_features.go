package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Default toggleable page keys. Missing keys are treated as enabled.
var defaultPageFeatureKeys = []string{
	"/parties",
	"/categories",
	"/products",
	"/inventory",
	"/warehouses",
	"/purchase-invoices",
	"/purchase-returns",
	"/debit-notes",
	"/payment-outs",
	"/invoices",
	"/estimates",
	"/delivery-challans",
	"/sales-returns",
	"/credit-notes",
	"/payments",
	"/expenses",
	"/expense-categories",
	"/cash-bank",
	"/profit-distribution",
	"/accounting",
	"/reports/index",
	"/reports/daily",
	"/reports/daily-profit",
	"/reports/profit-loss",
	"/reports/billwise-profit",
	"/reports/stock",
	"/reports",
	"/gst",
	"/e-invoicing",
	"/staff",
	"/attendance",
	"/payroll",
	"/pos",
	"/pos/sessions",
	"/sms-marketing",
	"/email-marketing",
	"/whatsapp-marketing",
	"/loyalty",
	"/stores",
	"/user-management",
	"/audit",
	"/notifications",
	"/customer-portal",
	"/settings/reminders",
	"/settings/ca-share",
}

func defaultPageFeatures() map[string]bool {
	pages := make(map[string]bool, len(defaultPageFeatureKeys))
	for _, key := range defaultPageFeatureKeys {
		pages[key] = true
	}
	return pages
}

func mergePageFeatures(stored map[string]bool) map[string]bool {
	pages := defaultPageFeatures()
	for key, enabled := range stored {
		if _, ok := pages[key]; ok {
			pages[key] = enabled
		}
	}
	return pages
}

func parsePagesJSON(raw string) map[string]bool {
	stored := map[string]bool{}
	if raw == "" {
		return mergePageFeatures(stored)
	}
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return defaultPageFeatures()
	}
	return mergePageFeatures(stored)
}

// GetPageFeatures returns which pages/menus are enabled for the active store.
// Falls back to the global (NULL store) row, then to all-enabled defaults.
// Any authenticated user may read.
func GetPageFeatures(c *gin.Context) {
	var settings models.PageFeatureSettings
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
		c.JSON(http.StatusOK, gin.H{"pages": defaultPageFeatures()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"pages":    parsePagesJSON(settings.PagesJSON),
		"store_id": settings.StoreID,
	})
}

// UpdatePageFeatures updates page/menu enablement for the active store.
// Super admin only. When no store context is present (no stores exist), the
// global fallback row is updated instead.
func UpdatePageFeatures(c *gin.Context) {
	var input struct {
		Pages map[string]bool `json:"pages" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	pages := mergePageFeatures(input.Pages)
	payload, err := json.Marshal(pages)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to encode page features"})
		return
	}

	var storeID *uuid.UUID
	query := utils.DB.Order("created_at asc")
	if id, ok := currentStoreID(c); ok {
		storeID = &id
		query = query.Where("store_id = ?", id)
	} else {
		query = query.Where("store_id IS NULL")
	}

	var settings models.PageFeatureSettings
	err = query.First(&settings).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		settings = models.PageFeatureSettings{
			ID:        uuid.New(),
			StoreID:   storeID,
			PagesJSON: string(payload),
		}
		if err := utils.DB.Create(&settings).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save page features"})
			return
		}
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load page features"})
		return
	} else {
		if err := utils.DB.Model(&settings).Update("pages_json", string(payload)).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update page features"})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"message":  "Page features updated successfully",
		"pages":    pages,
		"store_id": settings.StoreID,
	})
}
