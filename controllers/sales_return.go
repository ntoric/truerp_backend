package controllers

import (
	"fmt"
	"net/http"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// normalizeDeductionItems trims labels, defaults blank labels, drops
// zero-amount rows and rejects negative amounts.
func normalizeDeductionItems(items []models.AdditionalCharge) ([]models.AdditionalCharge, error) {
	out := make([]models.AdditionalCharge, 0, len(items))
	for _, item := range items {
		label := strings.TrimSpace(item.Label)
		if item.Amount < 0 {
			return nil, fmt.Errorf("deduction amount cannot be negative")
		}
		if item.Amount == 0 {
			continue
		}
		if label == "" {
			label = "Deduction"
		}
		out = append(out, models.AdditionalCharge{Label: label, Amount: item.Amount})
	}
	return out, nil
}

// setSalesReturnRefund fills the computed (non-persisted) RefundAmount.
func setSalesReturnRefund(sr *models.SalesReturn) {
	sr.RefundAmount = sr.Amount - sr.DeductionTotal
	if sr.RefundAmount < 0 {
		sr.RefundAmount = 0
	}
}

func GetSalesReturns(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var returns []models.SalesReturn
	query := utils.DB.Where("user_id = ?", userID).Preload("Party").Preload("Invoice")

	if partyID := c.Query("party_id"); partyID != "" {
		query = query.Where("party_id = ?", partyID)
	}
	if status := c.Query("status"); status != "" {
		query = query.Where("status = ?", status)
	}
	if fromDate := c.Query("from_date"); fromDate != "" {
		query = query.Where("date >= ?", fromDate)
	}
	if toDate := c.Query("to_date"); toDate != "" {
		query = query.Where("date <= ?", toDate)
	}

	if err := query.Order("date DESC").Find(&returns).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch sales returns"})
		return
	}

	for i := range returns {
		setSalesReturnRefund(&returns[i])
	}
	c.JSON(http.StatusOK, gin.H{"data": returns})
}

func GetSalesReturn(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var salesReturn models.SalesReturn
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Preload("Party").Preload("Invoice").Preload("Items").First(&salesReturn).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Sales return not found"})
		return
	}

	setSalesReturnRefund(&salesReturn)
	c.JSON(http.StatusOK, salesReturn)
}

func CreateSalesReturn(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		PartyID        uuid.UUID                 `json:"party_id" binding:"required"`
		InvoiceID      uuid.UUID                 `json:"invoice_id"`
		Date           time.Time                 `json:"date" binding:"required"`
		Reason         string                    `json:"reason"`
		RefundMode     string                    `json:"refund_mode"`
		Notes          string                    `json:"notes"`
		DeductionItems []models.AdditionalCharge `json:"deduction_items"`
		Items          []struct {
			InvoiceItemID uuid.UUID  `json:"invoice_item_id"`
			ProductID     *uuid.UUID `json:"product_id"`
			Description   string     `json:"description"`
			Quantity      float64    `json:"quantity" binding:"required,gt=0"`
			UnitPrice     float64    `json:"unit_price" binding:"required"`
			TaxRate       float64    `json:"tax_rate"`
			Reason        string     `json:"reason"`
		} `json:"items" binding:"required,min=1"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var count int64
	utils.DB.Model(&models.SalesReturn{}).Where("user_id = ?", userID).Count(&count)

	salesReturn := models.SalesReturn{
		ID:           uuid.New(),
		UserID:       userID,
		PartyID:      input.PartyID,
		InvoiceID:    input.InvoiceID,
		ReturnNumber: fmt.Sprintf("SR-%04d", count+1),
		Date:         input.Date,
		Status:       "draft",
		Reason:       input.Reason,
		RefundMode:   input.RefundMode,
		Notes:        input.Notes,
	}

	var totalAmount float64
	for _, item := range input.Items {
		productID := item.ProductID
		description := item.Description

		if productID != nil && *productID != uuid.Nil {
			var product models.Product
			if err := utils.DB.Where("user_id = ? AND id = ?", userID, *productID).First(&product).Error; err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid product in return items"})
				return
			}
			if description == "" {
				description = product.Name
			}
		} else if item.InvoiceItemID != uuid.Nil {
			var invoiceItem models.InvoiceItem
			if err := utils.DB.Where("id = ?", item.InvoiceItemID).First(&invoiceItem).Error; err == nil {
				if invoiceItem.ProductID != nil {
					productID = invoiceItem.ProductID
				}
				if description == "" {
					description = invoiceItem.Description
				}
			}
		}

		if description == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Item description or product_id is required"})
			return
		}

		taxAmount := item.UnitPrice * item.Quantity * (item.TaxRate / 100)
		total := item.UnitPrice*item.Quantity + taxAmount

		salesReturn.Items = append(salesReturn.Items, models.SalesReturnItem{
			ID:            uuid.New(),
			ReturnID:      salesReturn.ID,
			InvoiceItemID: item.InvoiceItemID,
			ProductID:     productID,
			Description:   description,
			Quantity:      item.Quantity,
			UnitPrice:     item.UnitPrice,
			TaxRate:       item.TaxRate,
			Total:         total,
			Reason:        item.Reason,
		})

		totalAmount += total
	}

	salesReturn.Amount = totalAmount

	deductionItems, err := normalizeDeductionItems(input.DeductionItems)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	deductionTotal := models.SumAdditionalCharges(deductionItems)
	if deductionTotal > totalAmount {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Deductions cannot exceed the return amount"})
		return
	}
	salesReturn.DeductionItems = deductionItems
	salesReturn.DeductionTotal = deductionTotal

	if err := utils.DB.Create(&salesReturn).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create sales return"})
		return
	}

	setSalesReturnRefund(&salesReturn)
	c.JSON(http.StatusCreated, salesReturn)
}

func UpdateSalesReturn(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var salesReturn models.SalesReturn
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&salesReturn).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Sales return not found"})
		return
	}

	if salesReturn.Status != "draft" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot edit processed sales return"})
		return
	}

	var input struct {
		Date           time.Time                 `json:"date"`
		Reason         string                    `json:"reason"`
		RefundMode     string                    `json:"refund_mode"`
		Notes          string                    `json:"notes"`
		DeductionItems []models.AdditionalCharge `json:"deduction_items"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	deductionItems, err := normalizeDeductionItems(input.DeductionItems)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	deductionTotal := models.SumAdditionalCharges(deductionItems)
	if deductionTotal > salesReturn.Amount {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Deductions cannot exceed the return amount"})
		return
	}

	salesReturn.Date = input.Date
	salesReturn.Reason = input.Reason
	salesReturn.RefundMode = input.RefundMode
	salesReturn.Notes = input.Notes
	salesReturn.DeductionItems = deductionItems
	salesReturn.DeductionTotal = deductionTotal

	if err := utils.DB.Model(&salesReturn).
		Select("date", "reason", "refund_mode", "notes", "deduction_items", "deduction_total").
		Updates(&salesReturn).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update sales return"})
		return
	}

	setSalesReturnRefund(&salesReturn)
	c.JSON(http.StatusOK, salesReturn)
}

func ProcessSalesReturn(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var salesReturn models.SalesReturn
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Preload("Items").First(&salesReturn).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Sales return not found"})
		return
	}

	if salesReturn.Status != "draft" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Sales return already processed"})
		return
	}

	outletID := resolveDefaultWarehouseID(userID)
	now := time.Now()

	// Restore inventory for returned items
	for _, item := range salesReturn.Items {
		productID := item.ProductID
		batchNo := strings.TrimSpace(item.BatchNo)
		var expDate *time.Time
		if (productID == nil || *productID == uuid.Nil || batchNo == "") && item.InvoiceItemID != uuid.Nil {
			var invoiceItem models.InvoiceItem
			if err := utils.DB.Where("id = ?", item.InvoiceItemID).First(&invoiceItem).Error; err == nil {
				if productID == nil || *productID == uuid.Nil {
					productID = invoiceItem.ProductID
				}
				if batchNo == "" {
					batchNo = invoiceItem.BatchNo
				}
				expDate = invoiceItem.ExpDate
			}
		}

		restoreOutlet := outletID
		if productID != nil && *productID != uuid.Nil && batchNo != "" {
			var stock models.InventoryStock
			if err := utils.DB.Where(
				"user_id = ? AND product_id = ? AND batch_no = ?",
				userID, *productID, batchNo,
			).Order("available_qty DESC").First(&stock).Error; err == nil {
				restoreOutlet = stock.OutletID
			}
		}

		entry := models.StockEntry{
			ID:             uuid.New(),
			UserID:         userID,
			ItemName:       item.Description,
			ProductID:      productID,
			OutletID:       restoreOutlet,
			EntryType:      "return",
			Quantity:       item.Quantity,
			BalanceQty:     0,
			CostPrice:      item.UnitPrice,
			BatchNo:        batchNo,
			ExpDate:        expDate,
			ReferenceID:    salesReturn.ID,
			ReferenceType:  "sales_return",
			Notes:          fmt.Sprintf("Sales return %s", salesReturn.ReturnNumber),
			ApprovalStatus: "approved",
			ApprovedBy:     &userID,
			ApprovedAt:     &now,
			EntryDate:      salesReturn.Date,
		}
		if err := utils.DB.Create(&entry).Error; err != nil {
			fmt.Printf("[DEBUG] ProcessSalesReturn - Failed to create stock entry: %v\n", err)
			continue
		}

		if productID != nil && *productID != uuid.Nil && restoreOutlet != uuid.Nil {
			updateInventoryStock(userID, *productID, restoreOutlet, "return", item.Quantity, item.UnitPrice, batchNo, nil, expDate)
		}
	}

	salesReturn.Status = "processed"
	utils.DB.Save(&salesReturn)

	setSalesReturnRefund(&salesReturn)
	c.JSON(http.StatusOK, salesReturn)
}

func DeleteSalesReturn(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var salesReturn models.SalesReturn
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&salesReturn).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Sales return not found"})
		return
	}

	if salesReturn.Status != "draft" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot delete processed sales return"})
		return
	}

	utils.DB.Delete(&salesReturn)
	c.JSON(http.StatusOK, gin.H{"message": "Sales return deleted"})
}
