package controllers

import (
	"truerp/models"
	"truerp/utils"
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// QueueOfflineOperation - Add an operation to the offline queue
func QueueOfflineOperation(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		Operation  string `json:"operation" binding:"required"`
		EntityType string `json:"entity_type" binding:"required"`
		EntityData string `json:"entity_data" binding:"required"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	queueItem := models.OfflineQueue{
		ID:         uuid.New(),
		UserID:     userID,
		Operation:  input.Operation,
		EntityType: input.EntityType,
		EntityData: input.EntityData,
		Status:     "pending",
	}

	if err := utils.DB.Create(&queueItem).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to queue operation"})
		return
	}

	c.JSON(http.StatusCreated, queueItem)
}

// GetPendingSyncs - Get all pending sync operations for a user
func GetPendingSyncs(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var pendingItems []models.OfflineQueue
	if err := utils.DB.Where("user_id = ? AND status = ?", userID, "pending").
		Order("created_at ASC").
		Find(&pendingItems).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch pending syncs"})
		return
	}

	c.JSON(http.StatusOK, pendingItems)
}

// SyncOfflineData - Process and sync offline data to server
func SyncOfflineData(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		Operations []struct {
			Operation  string `json:"operation" binding:"required"`
			EntityType string `json:"entity_type" binding:"required"`
			EntityData string `json:"entity_data" binding:"required"`
		} `json:"operations" binding:"required"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	results := make([]gin.H, 0, len(input.Operations))

	for _, op := range input.Operations {
		result := gin.H{
			"operation":  op.Operation,
			"entity_type": op.EntityType,
			"success":     false,
		}

		switch op.EntityType {
		case "invoice":
			if err := processInvoiceSync(userID, op.Operation, op.EntityData); err != nil {
				result["error"] = err.Error()
			} else {
				result["success"] = true
			}
		case "payment":
			if err := processPaymentSync(userID, op.Operation, op.EntityData); err != nil {
				result["error"] = err.Error()
			} else {
				result["success"] = true
			}
		case "stock":
			if err := processStockSync(userID, op.Operation, op.EntityData); err != nil {
				result["error"] = err.Error()
			} else {
				result["success"] = true
			}
		default:
			result["error"] = "Unknown entity type"
		}

		results = append(results, result)
	}

	c.JSON(http.StatusOK, gin.H{
		"results": results,
		"total":   len(results),
		"success": countSuccessful(results),
	})
}

// processInvoiceSync - Handle invoice synchronization
func processInvoiceSync(userID uuid.UUID, operation string, entityData string) error {
	switch operation {
	case "create":
		var invoice models.Invoice
		if err := json.Unmarshal([]byte(entityData), &invoice); err != nil {
			return err
		}
		invoice.UserID = userID
		if err := utils.DB.Create(&invoice).Error; err != nil {
			return err
		}
		// Create invoice items
		for _, item := range invoice.Items {
			item.InvoiceID = invoice.ID
			utils.DB.Create(&item)
		}
	case "update":
		var invoice models.Invoice
		if err := json.Unmarshal([]byte(entityData), &invoice); err != nil {
			return err
		}
		if err := utils.DB.Where("user_id = ? AND id = ?", userID, invoice.ID).Updates(&invoice).Error; err != nil {
			return err
		}
	}
	return nil
}

// processPaymentSync - Handle payment synchronization
func processPaymentSync(userID uuid.UUID, operation string, entityData string) error {
	switch operation {
	case "create":
		var payment models.Payment
		if err := json.Unmarshal([]byte(entityData), &payment); err != nil {
			return err
		}
		payment.UserID = userID
		if err := utils.DB.Create(&payment).Error; err != nil {
			return err
		}
	}
	return nil
}

// processStockSync - Handle stock synchronization
func processStockSync(userID uuid.UUID, operation string, entityData string) error {
	switch operation {
	case "update":
		var stockEntry models.StockEntry
		if err := json.Unmarshal([]byte(entityData), &stockEntry); err != nil {
			return err
		}
		stockEntry.UserID = userID
		if err := utils.DB.Create(&stockEntry).Error; err != nil {
			return err
		}
	}
	return nil
}

// countSuccessful - Count successful sync operations
func countSuccessful(results []gin.H) int {
	count := 0
	for _, r := range results {
		if r["success"] == true {
			count++
		}
	}
	return count
}

// ClearSyncedItems - Clear successfully synced items from queue
func ClearSyncedItems(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		IDs []uuid.UUID `json:"ids" binding:"required"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := utils.DB.Where("user_id = ? AND id IN ?", userID, input.IDs).Delete(&models.OfflineQueue{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to clear synced items"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Synced items cleared successfully"})
}

// GetSyncStatus - Get sync status for a user
func GetSyncStatus(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var pendingCount int64
	utils.DB.Model(&models.OfflineQueue{}).Where("user_id = ? AND status = ?", userID, "pending").Count(&pendingCount)

	var failedCount int64
	utils.DB.Model(&models.OfflineQueue{}).Where("user_id = ? AND status = ?", userID, "failed").Count(&failedCount)

	c.JSON(http.StatusOK, gin.H{
		"pending_count": pendingCount,
		"failed_count":  failedCount,
		"sync_status":   pendingCount == 0,
	})
}
