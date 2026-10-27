package controllers

import (
	"math"
	"net/http"
	"sort"
	"strings"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// invoiceStockWarning describes a product (optionally a specific batch) whose
// available stock would drop below zero if the invoice were saved as-is.
type invoiceStockWarning struct {
	ProductID    uuid.UUID `json:"product_id"`
	ProductName  string    `json:"product_name"`
	BatchNo      string    `json:"batch_no,omitempty"`
	AvailableQty float64   `json:"available_qty"`
	RequestedQty float64   `json:"requested_qty"`
	ResultingQty float64   `json:"resulting_qty"`
}

// CheckInvoiceStock reports which invoice lines would push stock negative.
// The sale form calls this before saving so the user can confirm overselling.
// Availability mirrors what applyInvoiceSaleStock will see: current available
// stock plus — when editing — the quantities the invoice already deducted,
// since UpdateInvoice reverses the old sale entries before re-applying.
func CheckInvoiceStock(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		InvoiceID *uuid.UUID `json:"invoice_id"`
		Items     []struct {
			ProductID *uuid.UUID           `json:"product_id"`
			Quantity  models.FlexibleFloat `json:"quantity"`
			BatchNo   string               `json:"batch_no"`
		} `json:"items"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	type batchKey struct {
		productID uuid.UUID
		batchNo   string
	}
	requestedByProduct := map[uuid.UUID]float64{}
	requestedByBatch := map[batchKey]float64{}
	for _, item := range input.Items {
		if item.ProductID == nil || *item.ProductID == uuid.Nil {
			continue
		}
		qty := math.Abs(item.Quantity.Float64())
		if qty == 0 {
			continue
		}
		requestedByProduct[*item.ProductID] += qty
		if batch := strings.TrimSpace(item.BatchNo); batch != "" {
			requestedByBatch[batchKey{*item.ProductID, batch}] += qty
		}
	}
	if len(requestedByProduct) == 0 {
		c.JSON(http.StatusOK, gin.H{"warnings": []invoiceStockWarning{}})
		return
	}

	productIDs := make([]uuid.UUID, 0, len(requestedByProduct))
	for id := range requestedByProduct {
		productIDs = append(productIDs, id)
	}

	type availabilityRow struct {
		ProductID uuid.UUID
		BatchNo   string
		Available float64
	}
	availableByProduct := map[uuid.UUID]float64{}
	availableByBatch := map[batchKey]float64{}
	var stockRows []availabilityRow
	if err := utils.DB.Model(&models.InventoryStock{}).
		Select("product_id, batch_no, COALESCE(SUM(available_qty), 0) AS available").
		Where("user_id = ? AND product_id IN ?", userID, productIDs).
		Group("product_id, batch_no").
		Scan(&stockRows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check stock"})
		return
	}
	for _, row := range stockRows {
		availableByProduct[row.ProductID] += row.Available
		availableByBatch[batchKey{row.ProductID, row.BatchNo}] += row.Available
	}

	if input.InvoiceID != nil && *input.InvoiceID != uuid.Nil {
		var restoredRows []availabilityRow
		if err := utils.DB.Model(&models.StockEntry{}).
			Select("product_id, batch_no, COALESCE(SUM(-quantity), 0) AS available").
			Where(
				"user_id = ? AND reference_id = ? AND reference_type = ? AND entry_type = ? AND product_id IS NOT NULL AND (approval_status = ? OR approval_status = ?)",
				userID, *input.InvoiceID, "invoice", "sale", "", "approved",
			).
			Group("product_id, batch_no").
			Scan(&restoredRows).Error; err == nil {
			for _, row := range restoredRows {
				availableByProduct[row.ProductID] += row.Available
				availableByBatch[batchKey{row.ProductID, row.BatchNo}] += row.Available
			}
		}
	}

	productNames := map[uuid.UUID]string{}
	var products []models.Product
	if err := utils.DB.Unscoped().
		Select("id, name").
		Where("user_id = ? AND id IN ?", userID, productIDs).
		Find(&products).Error; err == nil {
		for _, p := range products {
			productNames[p.ID] = p.Name
		}
	}

	warnings := make([]invoiceStockWarning, 0)
	for productID, qty := range requestedByProduct {
		available := availableByProduct[productID]
		if resulting := available - qty; resulting < -1e-9 {
			warnings = append(warnings, invoiceStockWarning{
				ProductID:    productID,
				ProductName:  productNames[productID],
				AvailableQty: available,
				RequestedQty: qty,
				ResultingQty: resulting,
			})
		}
	}
	for key, qty := range requestedByBatch {
		available := availableByBatch[key]
		if resulting := available - qty; resulting < -1e-9 {
			warnings = append(warnings, invoiceStockWarning{
				ProductID:    key.productID,
				ProductName:  productNames[key.productID],
				BatchNo:      key.batchNo,
				AvailableQty: available,
				RequestedQty: qty,
				ResultingQty: resulting,
			})
		}
	}
	sort.Slice(warnings, func(i, j int) bool {
		if warnings[i].ProductName != warnings[j].ProductName {
			return warnings[i].ProductName < warnings[j].ProductName
		}
		return warnings[i].BatchNo < warnings[j].BatchNo
	})

	c.JSON(http.StatusOK, gin.H{"warnings": warnings})
}
