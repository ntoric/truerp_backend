package controllers

import (
	"net/http"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ListOpeningStockOverrides returns all opening-stock overrides for the user.
// GET /api/v1/inventory/snapshots/opening-overrides
func ListOpeningStockOverrides(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var rows []models.StockOpeningOverride
	if err := utils.DB.Where("user_id = ?", userID).
		Order("effective_date DESC, created_at DESC").Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch opening overrides"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": rows})
}

// UpsertOpeningStockOverride creates or updates the overall opening-stock
// override for a date — superadmin only. Reports read overrides live, so no
// snapshot rebuild is needed.
// POST /api/v1/inventory/snapshots/opening-override
func UpsertOpeningStockOverride(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		Date     string  `json:"date" binding:"required"`
		Quantity float64 `json:"quantity"`
		Value    float64 `json:"value"`
		Notes    string  `json:"notes"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}
	date := strings.TrimSpace(input.Date)
	effectiveDate, err := time.Parse("2006-01-02", date)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid date (use YYYY-MM-DD)"})
		return
	}
	if input.Quantity < 0 || input.Value < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Quantity and value cannot be negative"})
		return
	}

	var existing models.StockOpeningOverride
	err = utils.DB.
		Where("user_id = ?", userID).
		Where(utils.SQLDateEquals("effective_date"), date).
		First(&existing).Error

	var row models.StockOpeningOverride
	if err == nil {
		existing.Quantity = input.Quantity
		existing.Value = input.Value
		existing.Notes = strings.TrimSpace(input.Notes)
		existing.CreatedBy = userID
		if err := utils.DB.Save(&existing).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update opening override"})
			return
		}
		row = existing
	} else if err == gorm.ErrRecordNotFound {
		row = models.StockOpeningOverride{
			ID:            uuid.New(),
			UserID:        userID,
			EffectiveDate: effectiveDate,
			Quantity:      input.Quantity,
			Value:         input.Value,
			Notes:         strings.TrimSpace(input.Notes),
			CreatedBy:     userID,
		}
		if err := utils.DB.Create(&row).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create opening override"})
			return
		}
	} else {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save opening override"})
		return
	}

	c.JSON(http.StatusOK, row)
}

// GetStockSnapshotPosition returns the computed overall closing position
// (quantity and value across all products and outlets) at the end of a date —
// the default an opening-stock override for the following day starts from.
// GET /api/v1/inventory/snapshots/position?date=
func GetStockSnapshotPosition(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	date := strings.TrimSpace(c.Query("date"))
	if _, err := time.Parse("2006-01-02", date); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid date (use YYYY-MM-DD)"})
		return
	}

	refreshStockSnapshots(userID, false)
	qty, value := overallPositionAsOf(userID, date)
	c.JSON(http.StatusOK, gin.H{"date": date, "quantity": qty, "value": value})
}

// DeleteOpeningStockOverride removes an override — reports immediately fall
// back to the computed position for that date.
// DELETE /api/v1/inventory/snapshots/opening-override/:id
func DeleteOpeningStockOverride(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid override id"})
		return
	}
	res := utils.DB.Where("user_id = ? AND id = ?", userID, id).Delete(&models.StockOpeningOverride{})
	if res.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete opening override"})
		return
	}
	if res.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Override not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Opening override removed"})
}
