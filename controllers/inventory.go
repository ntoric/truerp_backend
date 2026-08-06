package controllers

import (
	"truerp/models"
	"truerp/utils"
	"encoding/csv"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tealeg/xlsx/v3"
)

type StockBalance struct {
	ProductID   uuid.UUID `json:"product_id"`
	ProductName string    `json:"product_name"`
	SKU         string    `json:"sku"`
	StockQty    float64   `json:"stock_qty"`
	CostPrice   float64   `json:"cost_price"`
	Value       float64   `json:"value"`
	OutletID    uuid.UUID `json:"outlet_id"`
	OutletName  string    `json:"outlet_name"`
}

func GetStockBalance(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	outletID := c.Query("outlet_id")

	fmt.Printf("[DEBUG] GetStockBalance - UserID: %s, OutletID: %s\n", userID, outletID)

	var stocks []models.InventoryStock
	query := utils.DB.Where("user_id = ?", userID).Preload("Product")

	if outletID != "" {
		query = query.Where("outlet_id = ?", outletID)
	}

	if err := query.Find(&stocks).Error; err != nil {
		fmt.Printf("[DEBUG] GetStockBalance - DB error: %v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch stock balance"})
		return
	}

	fmt.Printf("[DEBUG] GetStockBalance - Found %d stock rows (before consolidation)\n", len(stocks))

	// Fetch outlet names
	var outletIDs []uuid.UUID
	for _, stock := range stocks {
		outletIDs = append(outletIDs, stock.OutletID)
	}
	var warehouses []models.Warehouse
	if len(outletIDs) > 0 {
		utils.DB.Where("id IN ?", outletIDs).Find(&warehouses)
	}
	warehouseMap := make(map[uuid.UUID]string)
	for _, wh := range warehouses {
		warehouseMap[wh.ID] = wh.Name
	}

	// Consolidate batches into one row per product + outlet.
	type balanceKey struct {
		ProductID uuid.UUID
		OutletID  uuid.UUID
	}
	balanceMap := make(map[balanceKey]*StockBalance)
	for _, stock := range stocks {
		key := balanceKey{ProductID: stock.ProductID, OutletID: stock.OutletID}
		lineValue := stock.Quantity * stock.AverageCost
		if existing, ok := balanceMap[key]; ok {
			existing.StockQty += stock.Quantity
			existing.Value += lineValue
			if existing.StockQty > 0 {
				existing.CostPrice = existing.Value / existing.StockQty
			}
			continue
		}
		balanceMap[key] = &StockBalance{
			ProductID:   stock.ProductID,
			ProductName: stock.Product.Name,
			SKU:         stock.Product.SKU,
			StockQty:    stock.Quantity,
			CostPrice:   stock.AverageCost,
			Value:       lineValue,
			OutletID:    stock.OutletID,
			OutletName:  warehouseMap[stock.OutletID],
		}
	}

	balances := make([]StockBalance, 0, len(balanceMap))
	for _, balance := range balanceMap {
		balances = append(balances, *balance)
	}

	fmt.Printf("[DEBUG] GetStockBalance - Returning %d consolidated balances\n", len(balances))
	c.JSON(http.StatusOK, gin.H{"data": balances})
}

func GetStockEntries(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	productID := c.Query("product_id")
	entryType := c.Query("entry_type")
	outletID := c.Query("outlet_id")
	approvalStatus := c.Query("approval_status")
	fromDate := c.Query("from_date")
	toDate := c.Query("to_date")

	fmt.Printf("[DEBUG] GetStockEntries - UserID: %s, ProductID: %s, EntryType: %s, OutletID: %s\n", userID, productID, entryType, outletID)

	var entries []models.StockEntry
	query := utils.DB.Where("user_id = ?", userID).Preload("Product")

	if productID != "" {
		query = query.Where("product_id = ?", productID)
	}

	if entryType != "" {
		query = query.Where("entry_type = ?", entryType)
	}

	if outletID != "" {
		query = query.Where("outlet_id = ?", outletID)
	}

	if approvalStatus != "" {
		if approvalStatus == "approved" {
			query = query.Where("approval_status = ? OR approval_status = ? OR approval_status IS NULL", "approved", "")
		} else {
			query = query.Where("approval_status = ?", approvalStatus)
		}
	}

	if fromDate != "" {
		query = query.Where("entry_date >= ?", fromDate)
	}
	if toDate != "" {
		query = query.Where("entry_date <= ?", toDate)
	}

	if err := query.Order("entry_date DESC").Find(&entries).Error; err != nil {
		fmt.Printf("[DEBUG] GetStockEntries - DB error: %v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch entries"})
		return
	}

	fmt.Printf("[DEBUG] GetStockEntries - Found %d entries\n", len(entries))

	// Fetch outlet names
	var outletIDs []uuid.UUID
	for _, entry := range entries {
		outletIDs = append(outletIDs, entry.OutletID)
	}
	var warehouses []models.Warehouse
	if len(outletIDs) > 0 {
		utils.DB.Where("id IN ?", outletIDs).Find(&warehouses)
	}
	warehouseMap := make(map[uuid.UUID]string)
	for _, wh := range warehouses {
		warehouseMap[wh.ID] = wh.Name
	}

	// Add outlet names to entries
	type StockEntryWithDetails struct {
		models.StockEntry
		OutletName string `json:"outlet_name"`
	}
	entriesWithDetails := make([]StockEntryWithDetails, 0, len(entries))
	for _, entry := range entries {
		entriesWithDetails = append(entriesWithDetails, StockEntryWithDetails{
			StockEntry: entry,
			OutletName: warehouseMap[entry.OutletID],
		})
	}

	c.JSON(http.StatusOK, gin.H{"data": entriesWithDetails})
}

func CreateStockEntry(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		ItemName      string     `json:"item_name" binding:"required"`
		ProductID     *uuid.UUID `json:"product_id"`
		OutletID      uuid.UUID  `json:"outlet_id" binding:"required"`
		EntryType     string     `json:"entry_type" binding:"required,oneof=purchase sale adjustment transfer opening"`
		Quantity      float64    `json:"quantity" binding:"required"`
		CostPrice     float64    `json:"cost_price"`
		BatchNo       string     `json:"batch_no"`
		ItemCode      string     `json:"item_code"`
		MfgDate       string     `json:"mfg_date"`
		ExpDate       string     `json:"exp_date"`
		Notes         string     `json:"notes"`
		ReferenceID   uuid.UUID  `json:"reference_id"`
		ReferenceType string     `json:"reference_type"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		fmt.Printf("[DEBUG] CreateStockEntry - JSON bind error: %v\n", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var mfgDate, expDate *time.Time
	if input.MfgDate != "" {
		if t, err := time.Parse("2006-01-02", input.MfgDate); err == nil {
			mfgDate = &t
		}
	}
	if input.ExpDate != "" {
		if t, err := time.Parse("2006-01-02", input.ExpDate); err == nil {
			expDate = &t
		}
	}

	fmt.Printf("[DEBUG] CreateStockEntry - UserID: %s, ItemName: %s, EntryType: %s, Quantity: %f\n", userID, input.ItemName, input.EntryType, input.Quantity)

	// Verify product exists if provided
	if input.ProductID != nil {
		var product models.Product
		if err := utils.DB.Where("user_id = ? AND id = ?", userID, *input.ProductID).First(&product).Error; err != nil {
			fmt.Printf("[DEBUG] CreateStockEntry - Product not found: %v\n", err)
			c.JSON(http.StatusNotFound, gin.H{"error": "Product not found"})
			return
		}
	}

	now := time.Now()
	entry := models.StockEntry{
		ID:             uuid.New(),
		UserID:         userID,
		ItemName:       input.ItemName,
		ProductID:      input.ProductID,
		OutletID:       input.OutletID,
		EntryType:      input.EntryType,
		Quantity:       input.Quantity,
		BalanceQty:     0,
		CostPrice:      input.CostPrice,
		BatchNo:        input.BatchNo,
		ItemCode:       input.ItemCode,
		MfgDate:        mfgDate,
		ExpDate:        expDate,
		ReferenceID:    input.ReferenceID,
		ReferenceType:  input.ReferenceType,
		Notes:          input.Notes,
		ApprovalStatus: "approved",
		ApprovedBy:     &userID,
		ApprovedAt:     &now,
		EntryDate:      now,
	}

	if err := utils.DB.Create(&entry).Error; err != nil {
		fmt.Printf("[DEBUG] CreateStockEntry - DB create error: %v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create entry"})
		return
	}

	fmt.Printf("[DEBUG] CreateStockEntry - Entry created successfully: %s\n", entry.ID)

	// Update inventory stock if product is linked
	if input.ProductID != nil {
		updateInventoryStock(userID, *input.ProductID, input.OutletID, input.EntryType, input.Quantity, input.CostPrice, input.BatchNo, mfgDate, expDate)
	}

	c.JSON(http.StatusCreated, entry)
}

func UpdateStockEntry(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	fmt.Printf("[DEBUG] UpdateStockEntry - ID: %s, UserID: %s\n", id, userID)

	var entry models.StockEntry
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&entry).Error; err != nil {
		fmt.Printf("[DEBUG] UpdateStockEntry - Entry not found: %v\n", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "Stock entry not found"})
		return
	}

	var input struct {
		Quantity  float64 `json:"quantity"`
		CostPrice float64 `json:"cost_price"`
		BatchNo   string  `json:"batch_no"`
		ItemCode  string  `json:"item_code"`
		MfgDate   string  `json:"mfg_date"`
		ExpDate   string  `json:"exp_date"`
		Notes     string  `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		fmt.Printf("[DEBUG] UpdateStockEntry - JSON bind error: %v\n", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Parse date strings to time.Time
	var mfgDate, expDate *time.Time
	if input.MfgDate != "" {
		if t, err := time.Parse("2006-01-02", input.MfgDate); err == nil {
			mfgDate = &t
		}
	}
	if input.ExpDate != "" {
		if t, err := time.Parse("2006-01-02", input.ExpDate); err == nil {
			expDate = &t
		}
	}

	oldBatch := strings.TrimSpace(entry.BatchNo)
	oldQty := entry.Quantity
	newBatch := strings.TrimSpace(input.BatchNo)
	if newBatch == "" {
		newBatch = oldBatch
	}

	// Update the entry
	updates := map[string]interface{}{
		"quantity":   input.Quantity,
		"cost_price": input.CostPrice,
		"batch_no":   newBatch,
		"item_code":  input.ItemCode,
		"notes":      input.Notes,
	}

	// Only include dates if they are provided
	if mfgDate != nil {
		updates["mfg_date"] = mfgDate
	}
	if expDate != nil {
		updates["exp_date"] = expDate
	}

	if err := utils.DB.Model(&entry).Updates(updates).Error; err != nil {
		fmt.Printf("[DEBUG] UpdateStockEntry - DB update error: %v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update entry"})
		return
	}

	// Only adjust available stock for already-approved entries
	if entry.ProductID != nil && stockEntryIsApproved(entry.ApprovalStatus) {
		updateInventoryStock(userID, *entry.ProductID, entry.OutletID, "adjustment", -oldQty, entry.CostPrice, oldBatch, nil, nil)
		updateInventoryStock(userID, *entry.ProductID, entry.OutletID, "adjustment", input.Quantity, input.CostPrice, newBatch, mfgDate, expDate)
	}

	fmt.Printf("[DEBUG] UpdateStockEntry - Entry updated successfully: %s\n", entry.ID)
	c.JSON(http.StatusOK, entry)
}

func stockEntryIsApproved(status string) bool {
	return status == "" || status == "approved"
}

func resolveDefaultWarehouseID(userID uuid.UUID) uuid.UUID {
	var defaultWarehouse models.Warehouse
	if err := utils.DB.Where("user_id = ? AND is_default = ?", userID, true).First(&defaultWarehouse).Error; err == nil {
		return defaultWarehouse.ID
	}

	defaultWarehouse = models.Warehouse{
		ID:        uuid.New(),
		UserID:    userID,
		Name:      "Default Warehouse",
		Code:      "DEFAULT",
		IsDefault: true,
		IsActive:  true,
	}
	if err := utils.DB.Create(&defaultWarehouse).Error; err != nil {
		var fallback models.Warehouse
		if err := utils.DB.Where("user_id = ?", userID).Order("created_at ASC").First(&fallback).Error; err == nil {
			return fallback.ID
		}
		return uuid.Nil
	}
	return defaultWarehouse.ID
}

// resolveSaleWarehouseID picks the warehouse that currently holds stock for the product.
// Falls back to the user's default warehouse when no stock row exists yet.
func resolveSaleWarehouseID(userID, productID uuid.UUID) uuid.UUID {
	var stock models.InventoryStock
	if err := utils.DB.Where("user_id = ? AND product_id = ? AND available_qty > 0", userID, productID).
		Order("available_qty DESC").First(&stock).Error; err == nil && stock.OutletID != uuid.Nil {
		return stock.OutletID
	}
	if err := utils.DB.Where("user_id = ? AND product_id = ?", userID, productID).
		Order("last_updated DESC").First(&stock).Error; err == nil && stock.OutletID != uuid.Nil {
		return stock.OutletID
	}
	return resolveDefaultWarehouseID(userID)
}

func createPendingPurchaseStockEntries(userID uuid.UUID, bill *models.PurchaseBill) error {
	if bill == nil || bill.Status == "draft" {
		bill.StockStatus = "none"
		return utils.DB.Model(bill).Update("stock_status", "none").Error
	}

	warehouseID := uuid.Nil
	if bill.WarehouseID != nil {
		warehouseID = *bill.WarehouseID
	}
	if warehouseID == uuid.Nil {
		warehouseID = resolveDefaultWarehouseID(userID)
		if warehouseID != uuid.Nil {
			bill.WarehouseID = &warehouseID
			utils.DB.Model(bill).Update("warehouse_id", warehouseID)
		}
	}
	if warehouseID == uuid.Nil {
		bill.StockStatus = "none"
		return utils.DB.Model(bill).Update("stock_status", "none").Error
	}

	created := 0
	for _, item := range bill.Items {
		if item.ProductID == nil || item.Quantity <= 0 {
			continue
		}

		entry := models.StockEntry{
			ID:             uuid.New(),
			UserID:         userID,
			ItemName:       item.Description,
			ProductID:      item.ProductID,
			OutletID:       warehouseID,
			EntryType:      "purchase",
			Quantity:       item.Quantity,
			CostPrice:      item.UnitPrice,
			BatchNo:        item.BatchNo,
			ItemCode:       item.ItemCode,
			MfgDate:        item.MfgDate,
			ExpDate:        item.ExpDate,
			ReferenceID:    bill.ID,
			ReferenceType:  "purchase_bill",
			Notes:          fmt.Sprintf("Pending approval from purchase bill %s", bill.BillNumber),
			ApprovalStatus: "pending",
			EntryDate:      bill.BillDate,
		}
		if err := utils.DB.Create(&entry).Error; err != nil {
			return err
		}
		created++
	}

	stockStatus := "none"
	if created > 0 {
		stockStatus = "pending"
	}
	bill.StockStatus = stockStatus
	return utils.DB.Model(bill).Update("stock_status", stockStatus).Error
}

func removePurchaseStockEntries(userID, billID uuid.UUID) error {
	var entries []models.StockEntry
	if err := utils.DB.Where("user_id = ? AND reference_id = ? AND reference_type = ?", userID, billID, "purchase_bill").Find(&entries).Error; err != nil {
		return err
	}

	for _, entry := range entries {
		if stockEntryIsApproved(entry.ApprovalStatus) && entry.ProductID != nil {
			// Reverse previously applied stock
			updateInventoryStock(userID, *entry.ProductID, entry.OutletID, "adjustment", -entry.Quantity, entry.CostPrice, entry.BatchNo, entry.MfgDate, entry.ExpDate)
		}
		if err := utils.DB.Delete(&entry).Error; err != nil {
			return err
		}
	}
	return nil
}

func syncPurchaseBillStockStatus(userID, billID uuid.UUID) {
	var entries []models.StockEntry
	if err := utils.DB.Where("user_id = ? AND reference_id = ? AND reference_type = ?", userID, billID, "purchase_bill").Find(&entries).Error; err != nil {
		return
	}

	status := "none"
	if len(entries) > 0 {
		pending, approved, rejected := 0, 0, 0
		for _, e := range entries {
			switch e.ApprovalStatus {
			case "pending":
				pending++
			case "rejected":
				rejected++
			default:
				approved++
			}
		}
		switch {
		case pending > 0 && (approved > 0 || rejected > 0):
			status = "partial"
		case pending > 0:
			status = "pending"
		case approved > 0 && rejected > 0:
			status = "partial"
		case approved > 0:
			status = "approved"
		case rejected > 0:
			status = "rejected"
		}
	}

	utils.DB.Model(&models.PurchaseBill{}).Where("user_id = ? AND id = ?", userID, billID).Update("stock_status", status)
}

func ApproveStockEntry(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var entry models.StockEntry
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&entry).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Stock entry not found"})
		return
	}

	if stockEntryIsApproved(entry.ApprovalStatus) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Stock entry is already approved"})
		return
	}
	if entry.ApprovalStatus == "rejected" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Rejected stock entry cannot be approved"})
		return
	}

	now := time.Now()
	updates := map[string]interface{}{
		"approval_status": "approved",
		"approved_by":     userID,
		"approved_at":     now,
	}
	if strings.HasPrefix(entry.Notes, "Pending approval from purchase bill ") {
		updates["notes"] = "From " + strings.TrimPrefix(entry.Notes, "Pending approval from ")
	}
	if err := utils.DB.Model(&entry).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to approve stock entry"})
		return
	}

	if entry.ProductID != nil {
		updateInventoryStock(userID, *entry.ProductID, entry.OutletID, entry.EntryType, entry.Quantity, entry.CostPrice, entry.BatchNo, entry.MfgDate, entry.ExpDate)
	}

	if entry.ReferenceType == "purchase_bill" && entry.ReferenceID != uuid.Nil {
		syncPurchaseBillStockStatus(userID, entry.ReferenceID)
	}

	entry.ApprovalStatus = "approved"
	entry.ApprovedBy = &userID
	entry.ApprovedAt = &now
	c.JSON(http.StatusOK, entry)
}

func RejectStockEntry(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var entry models.StockEntry
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&entry).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Stock entry not found"})
		return
	}

	if stockEntryIsApproved(entry.ApprovalStatus) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Approved stock entry cannot be rejected"})
		return
	}
	if entry.ApprovalStatus == "rejected" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Stock entry is already rejected"})
		return
	}

	var input struct {
		Reason string `json:"reason"`
	}
	_ = c.ShouldBindJSON(&input)

	notes := entry.Notes
	if input.Reason != "" {
		notes = "Rejected: " + input.Reason
	} else if !strings.HasPrefix(notes, "Rejected:") {
		notes = "Rejected"
	}

	if err := utils.DB.Model(&entry).Updates(map[string]interface{}{
		"approval_status": "rejected",
		"notes":           notes,
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reject stock entry"})
		return
	}

	if entry.ReferenceType == "purchase_bill" && entry.ReferenceID != uuid.Nil {
		syncPurchaseBillStockStatus(userID, entry.ReferenceID)
	}

	entry.ApprovalStatus = "rejected"
	entry.Notes = notes
	c.JSON(http.StatusOK, entry)
}

func ApproveAllPendingStockEntries(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		ReferenceID   string `json:"reference_id"`
		ReferenceType string `json:"reference_type"`
	}
	_ = c.ShouldBindJSON(&input)

	query := utils.DB.Where("user_id = ? AND approval_status = ?", userID, "pending")
	if input.ReferenceID != "" {
		query = query.Where("reference_id = ?", input.ReferenceID)
	}
	if input.ReferenceType != "" {
		query = query.Where("reference_type = ?", input.ReferenceType)
	}

	var entries []models.StockEntry
	if err := query.Find(&entries).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch pending entries"})
		return
	}

	now := time.Now()
	approvedCount := 0
	billIDs := map[uuid.UUID]bool{}

	for _, entry := range entries {
		if err := utils.DB.Model(&entry).Updates(map[string]interface{}{
			"approval_status": "approved",
			"approved_by":     userID,
			"approved_at":     now,
		}).Error; err != nil {
			continue
		}
		if entry.ProductID != nil {
			updateInventoryStock(userID, *entry.ProductID, entry.OutletID, entry.EntryType, entry.Quantity, entry.CostPrice, entry.BatchNo, entry.MfgDate, entry.ExpDate)
		}
		if entry.ReferenceType == "purchase_bill" && entry.ReferenceID != uuid.Nil {
			billIDs[entry.ReferenceID] = true
		}
		approvedCount++
	}

	for billID := range billIDs {
		syncPurchaseBillStockStatus(userID, billID)
	}

	c.JSON(http.StatusOK, gin.H{
		"message":        "Pending stock entries approved",
		"approved_count": approvedCount,
	})
}

func updateInventoryStock(userID, productID, outletID uuid.UUID, entryType string, quantity, costPrice float64, batchNo string, mfgDate, expDate *time.Time) {
	batchNo = strings.TrimSpace(batchNo)
	fmt.Printf("[DEBUG] updateInventoryStock - UserID: %s, ProductID: %s, OutletID: %s, BatchNo: %q, EntryType: %s, Quantity: %f\n", userID, productID, outletID, batchNo, entryType, quantity)

	var stock models.InventoryStock
	err := utils.DB.Where(
		"user_id = ? AND product_id = ? AND outlet_id = ? AND batch_no = ?",
		userID, productID, outletID, batchNo,
	).First(&stock).Error

	if err != nil {
		fmt.Printf("[DEBUG] updateInventoryStock - Creating new stock record\n")
		initialQty := 0.0
		if quantity > 0 {
			switch entryType {
			case "opening", "purchase", "adjustment", "return":
				initialQty = quantity
			}
		}
		stock = models.InventoryStock{
			ID:              uuid.New(),
			UserID:          userID,
			ProductID:       productID,
			OutletID:        outletID,
			BatchNo:         batchNo,
			MfgDate:         mfgDate,
			ExpDate:         expDate,
			Quantity:        0,
			InitialQuantity: initialQty,
			ReservedQty:     0,
			AvailableQty:    0,
			AverageCost:     costPrice,
			LastUpdated:     time.Now(),
		}
	} else {
		if mfgDate != nil {
			stock.MfgDate = mfgDate
		}
		if expDate != nil {
			stock.ExpDate = expDate
		}
	}

	// Update quantity based on entry type.
	// Sale/transfer always reduce by absolute quantity so callers can pass signed or unsigned qty.
	switch entryType {
	case "purchase", "opening", "adjustment", "return":
		stock.Quantity += quantity
		// Update weighted average cost (only when adding stock with a cost)
		if quantity > 0 && stock.Quantity > 0 && costPrice > 0 {
			prevQty := stock.Quantity - quantity
			if prevQty < 0 {
				prevQty = 0
			}
			stock.AverageCost = ((stock.AverageCost * prevQty) + (costPrice * quantity)) / stock.Quantity
		}
	case "sale", "transfer":
		stock.Quantity -= math.Abs(quantity)
	}

	stock.AvailableQty = stock.Quantity - stock.ReservedQty
	stock.LastUpdated = time.Now()

	if err := utils.DB.Save(&stock).Error; err != nil {
		fmt.Printf("[DEBUG] updateInventoryStock - DB save error: %v\n", err)
	} else {
		fmt.Printf("[DEBUG] updateInventoryStock - Stock updated successfully: Batch=%q Quantity=%f, Available=%f\n", batchNo, stock.Quantity, stock.AvailableQty)
	}
}

// pickFEFOBatch finds the earliest-expiring batch with enough available qty.
// Falls back to the first available batch (even if qty is partial) when none cover the full qty alone.
func pickFEFOBatch(userID, productID, outletID uuid.UUID, qty float64) (models.InventoryStock, bool) {
	var stocks []models.InventoryStock
	query := utils.DB.Where("user_id = ? AND product_id = ? AND available_qty > 0", userID, productID)
	if outletID != uuid.Nil {
		query = query.Where("outlet_id = ?", outletID)
	}
	query.Order("CASE WHEN exp_date IS NULL THEN 1 ELSE 0 END ASC, exp_date ASC, available_qty DESC").Find(&stocks)

	for _, s := range stocks {
		if s.AvailableQty+1e-9 >= qty {
			return s, true
		}
	}
	if len(stocks) > 0 {
		return stocks[0], true
	}
	return models.InventoryStock{}, false
}

// validateBatchedProductRequiresBatch returns an error when the product has batching enabled but batch is empty.
func validateBatchedProductRequiresBatch(userID uuid.UUID, productID *uuid.UUID, batchNo, productLabel string) error {
	if productID == nil || *productID == uuid.Nil {
		return nil
	}
	var product models.Product
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, *productID).First(&product).Error; err != nil {
		return nil
	}
	if product.EnableBatching && strings.TrimSpace(batchNo) == "" {
		name := product.Name
		if productLabel != "" {
			name = productLabel
		}
		return fmt.Errorf("batch number is required for %s (batching enabled)", name)
	}
	return nil
}

// applyInvoiceSaleStock reduces available inventory for invoice line items linked to products.
func applyInvoiceSaleStock(userID uuid.UUID, invoice *models.Invoice) {
	if invoice == nil || invoice.Status == "cancelled" || invoice.Status == "draft" {
		return
	}

	now := time.Now()
	for i := range invoice.Items {
		item := &invoice.Items[i]
		if item.ProductID == nil || *item.ProductID == uuid.Nil || item.Quantity == 0 {
			continue
		}

		batchNo := strings.TrimSpace(item.BatchNo)
		outletID := resolveSaleWarehouseID(userID, *item.ProductID)
		if outletID == uuid.Nil {
			fmt.Printf("[DEBUG] applyInvoiceSaleStock - No warehouse for user %s product %s\n", userID, *item.ProductID)
			continue
		}

		var product models.Product
		_ = utils.DB.Where("user_id = ? AND id = ?", userID, *item.ProductID).First(&product).Error

		if product.EnableBatching && batchNo == "" {
			if stock, ok := pickFEFOBatch(userID, *item.ProductID, outletID, math.Abs(item.Quantity)); ok {
				batchNo = stock.BatchNo
				outletID = stock.OutletID
				item.BatchNo = batchNo
				if stock.ExpDate != nil {
					item.ExpDate = stock.ExpDate
				}
				utils.DB.Model(item).Updates(map[string]interface{}{
					"batch_no": batchNo,
					"exp_date": item.ExpDate,
				})
			}
		} else if batchNo != "" {
			var stock models.InventoryStock
			if err := utils.DB.Where(
				"user_id = ? AND product_id = ? AND batch_no = ? AND available_qty > 0",
				userID, *item.ProductID, batchNo,
			).Order("available_qty DESC").First(&stock).Error; err == nil {
				outletID = stock.OutletID
				if item.ExpDate == nil && stock.ExpDate != nil {
					item.ExpDate = stock.ExpDate
					utils.DB.Model(item).Update("exp_date", item.ExpDate)
				}
			}
		}

		qty := math.Abs(item.Quantity)
		entry := models.StockEntry{
			ID:             uuid.New(),
			UserID:         userID,
			ItemName:       item.Description,
			ProductID:      item.ProductID,
			OutletID:       outletID,
			EntryType:      "sale",
			Quantity:       -qty,
			BalanceQty:     0,
			CostPrice:      item.UnitPrice,
			BatchNo:        batchNo,
			ExpDate:        item.ExpDate,
			ReferenceID:    invoice.ID,
			ReferenceType:  "invoice",
			Notes:          fmt.Sprintf("Sale via invoice %s", invoice.InvoiceNumber),
			ApprovalStatus: "approved",
			ApprovedBy:     &userID,
			ApprovedAt:     &now,
			EntryDate:      invoice.Date,
		}
		if err := utils.DB.Create(&entry).Error; err != nil {
			fmt.Printf("[DEBUG] applyInvoiceSaleStock - Failed to create stock entry: %v\n", err)
			continue
		}
		updateInventoryStock(userID, *item.ProductID, outletID, "sale", qty, 0, batchNo, nil, item.ExpDate)
	}
}

// reverseInvoiceSaleStock restores inventory for stock entries previously posted for an invoice.
func reverseInvoiceSaleStock(userID, invoiceID uuid.UUID) {
	var entries []models.StockEntry
	if err := utils.DB.Where(
		"user_id = ? AND reference_id = ? AND reference_type = ?",
		userID, invoiceID, "invoice",
	).Find(&entries).Error; err != nil {
		fmt.Printf("[DEBUG] reverseInvoiceSaleStock - Failed to load entries: %v\n", err)
		return
	}

	for _, entry := range entries {
		if entry.ProductID != nil && stockEntryIsApproved(entry.ApprovalStatus) {
			// Sale entries store negative quantity; -entry.Quantity restores stock.
			updateInventoryStock(userID, *entry.ProductID, entry.OutletID, "adjustment", -entry.Quantity, entry.CostPrice, entry.BatchNo, entry.MfgDate, entry.ExpDate)
		}
		if err := utils.DB.Delete(&entry).Error; err != nil {
			fmt.Printf("[DEBUG] reverseInvoiceSaleStock - Failed to delete entry %s: %v\n", entry.ID, err)
		}
	}
}

func GetStockTransfers(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	status := c.Query("status")

	fmt.Printf("[DEBUG] GetStockTransfers - UserID: %s, Status: %s\n", userID, status)

	var transfers []models.StockTransfer
	query := utils.DB.Where("user_id = ?", userID).Preload("Items")

	if status != "" {
		query = query.Where("status = ?", status)
	}

	if err := query.Order("created_at DESC").Find(&transfers).Error; err != nil {
		fmt.Printf("[DEBUG] GetStockTransfers - DB error: %v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch transfers"})
		return
	}

	fmt.Printf("[DEBUG] GetStockTransfers - Found %d transfers\n", len(transfers))
	c.JSON(http.StatusOK, transfers)
}

func GetStockTransfer(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	fmt.Printf("[DEBUG] GetStockTransfer - UserID: %s, ID: %s\n", userID, id)

	var transfer models.StockTransfer
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Preload("Items").First(&transfer).Error; err != nil {
		fmt.Printf("[DEBUG] GetStockTransfer - Transfer not found: %v\n", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "Transfer not found"})
		return
	}

	c.JSON(http.StatusOK, transfer)
}

func CreateStockTransfer(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		FromOutletID uuid.UUID `json:"from_outlet_id"`
		ToOutletID   uuid.UUID `json:"to_outlet_id" binding:"required"`
		Notes        string    `json:"notes"`
		Items        []struct {
			ProductID uuid.UUID `json:"product_id" binding:"required"`
			Quantity  float64   `json:"quantity" binding:"required,gt=0"`
		} `json:"items" binding:"required,min=1"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		fmt.Printf("[DEBUG] CreateStockTransfer - JSON bind error: %v\n", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	fmt.Printf("[DEBUG] CreateStockTransfer - UserID: %s, ToOutletID: %s, Items: %d\n", userID, input.ToOutletID, len(input.Items))

	transfer := models.StockTransfer{
		ID:            uuid.New(),
		UserID:        userID,
		FromOutletID:  input.FromOutletID,
		ToOutletID:    input.ToOutletID,
		Status:        "draft",
		TotalItems:    len(input.Items),
		Notes:         input.Notes,
	}

	for _, item := range input.Items {
		// Verify product exists
		var product models.Product
		if err := utils.DB.Where("user_id = ? AND id = ?", userID, item.ProductID).First(&product).Error; err == nil {
			transfer.Items = append(transfer.Items, models.StockTransferItem{
				ID:        uuid.New(),
				TransferID: transfer.ID,
				ProductID: item.ProductID,
				Quantity:  item.Quantity,
			})
			transfer.TotalQuantity += item.Quantity
		}
	}

	if err := utils.DB.Create(&transfer).Error; err != nil {
		fmt.Printf("[DEBUG] CreateStockTransfer - DB create error: %v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create transfer"})
		return
	}

	fmt.Printf("[DEBUG] CreateStockTransfer - Transfer created successfully: %s\n", transfer.ID)
	c.JSON(http.StatusCreated, transfer)
}

func SubmitStockTransfer(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	fmt.Printf("[DEBUG] SubmitStockTransfer - UserID: %s, ID: %s\n", userID, id)

	var transfer models.StockTransfer
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Preload("Items").First(&transfer).Error; err != nil {
		fmt.Printf("[DEBUG] SubmitStockTransfer - Transfer not found: %v\n", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "Transfer not found"})
		return
	}

	if transfer.Status != "draft" {
		fmt.Printf("[DEBUG] SubmitStockTransfer - Transfer already submitted, status: %s\n", transfer.Status)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Transfer already submitted"})
		return
	}

	now := time.Now()
	transfer.Status = "submitted"
	transfer.SentDate = &now

	for _, item := range transfer.Items {
		item.SentQuantity = item.Quantity
		utils.DB.Save(&item)
	}

	utils.DB.Save(&transfer)
	fmt.Printf("[DEBUG] SubmitStockTransfer - Transfer submitted successfully: %s\n", id)
	c.JSON(http.StatusOK, transfer)
}

func ReceiveStockTransfer(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	fmt.Printf("[DEBUG] ReceiveStockTransfer - UserID: %s, ID: %s\n", userID, id)

	var transfer models.StockTransfer
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Preload("Items").First(&transfer).Error; err != nil {
		fmt.Printf("[DEBUG] ReceiveStockTransfer - Transfer not found: %v\n", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "Transfer not found"})
		return
	}

	if transfer.Status != "submitted" {
		fmt.Printf("[DEBUG] ReceiveStockTransfer - Transfer not ready for receipt, status: %s\n", transfer.Status)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Transfer not ready for receipt"})
		return
	}

	now := time.Now()
	transfer.Status = "received"
	transfer.ReceivedDate = &now

	for _, item := range transfer.Items {
		item.ReceivedQuantity = item.SentQuantity
		utils.DB.Save(&item)
	}

	utils.DB.Save(&transfer)
	fmt.Printf("[DEBUG] ReceiveStockTransfer - Transfer received successfully: %s\n", id)
	c.JSON(http.StatusOK, transfer)
}

func GetInventoryValuation(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	outletID := c.Query("outlet_id")

	fmt.Printf("[DEBUG] GetInventoryValuation - UserID: %s, OutletID: %s\n", userID, outletID)

	var stocks []models.InventoryStock
	query := utils.DB.Where("user_id = ?", userID).Preload("Product")

	if outletID != "" {
		query = query.Where("outlet_id = ?", outletID)
	}

	if err := query.Find(&stocks).Error; err != nil {
		fmt.Printf("[DEBUG] GetInventoryValuation - DB error: %v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch valuation"})
		return
	}

	type ValuationResult struct {
		ProductID   uuid.UUID `json:"product_id"`
		ProductName string    `json:"product_name"`
		SKU         string    `json:"sku"`
		StockQty    float64   `json:"stock_qty"`
		CostPrice   float64   `json:"cost_price"`
		TotalValue  float64   `json:"total_value"`
		OutletID    uuid.UUID `json:"outlet_id"`
		OutletName  string    `json:"outlet_name"`
	}

	var results []ValuationResult
	var totalValue float64

	// Fetch outlet names
	var outletIDs []uuid.UUID
	for _, stock := range stocks {
		outletIDs = append(outletIDs, stock.OutletID)
	}
	var warehouses []models.Warehouse
	if len(outletIDs) > 0 {
		utils.DB.Where("id IN ?", outletIDs).Find(&warehouses)
	}
	warehouseMap := make(map[uuid.UUID]string)
	for _, wh := range warehouses {
		warehouseMap[wh.ID] = wh.Name
	}

	for _, stock := range stocks {
		itemValue := stock.Quantity * stock.AverageCost
		totalValue += itemValue
		results = append(results, ValuationResult{
			ProductID:   stock.ProductID,
			ProductName: stock.Product.Name,
			SKU:         stock.Product.SKU,
			StockQty:    stock.Quantity,
			CostPrice:   stock.AverageCost,
			TotalValue:  itemValue,
			OutletID:    stock.OutletID,
			OutletName:  warehouseMap[stock.OutletID],
		})
	}

	fmt.Printf("[DEBUG] GetInventoryValuation - Total value: %f, Items: %d\n", totalValue, len(results))
	c.JSON(http.StatusOK, gin.H{
		"items":       results,
		"total_value": totalValue,
	})
}

// Inventory Stock Management
func GetInventoryStocks(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	productID := c.Query("product_id")
	outletID := c.Query("outlet_id")
	batchNo := c.Query("batch_no")
	availableOnly := c.Query("available_only") == "true" || c.Query("available_only") == "1"
	expiringWithinDays := c.Query("expiring_within_days")

	fmt.Printf("[DEBUG] GetInventoryStocks - UserID: %s, ProductID: %s, OutletID: %s\n", userID, productID, outletID)

	var stocks []models.InventoryStock
	query := utils.DB.Where("user_id = ?", userID).Preload("Product")

	if productID != "" {
		query = query.Where("product_id = ?", productID)
	}
	if outletID != "" {
		query = query.Where("outlet_id = ?", outletID)
	}
	if batchNo != "" {
		query = query.Where("batch_no = ?", batchNo)
	}
	if availableOnly {
		query = query.Where("available_qty > 0")
	}
	if expiringWithinDays != "" {
		var days int
		if _, err := fmt.Sscanf(expiringWithinDays, "%d", &days); err == nil && days >= 0 {
			cutoff := time.Now().AddDate(0, 0, days)
			query = query.Where("exp_date IS NOT NULL AND exp_date <= ?", cutoff)
		}
	}

	query = query.Order("CASE WHEN exp_date IS NULL THEN 1 ELSE 0 END ASC, exp_date ASC, batch_no ASC")

	if err := query.Find(&stocks).Error; err != nil {
		fmt.Printf("[DEBUG] GetInventoryStocks - DB error: %v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch inventory stocks"})
		return
	}

	fmt.Printf("[DEBUG] GetInventoryStocks - Found %d stocks\n", len(stocks))

	// Backfill initial quantity from opening stock entries for legacy rows.
	type openingKey struct {
		ProductID uuid.UUID
		OutletID  uuid.UUID
		BatchNo   string
	}
	openingQtyMap := make(map[openingKey]float64)
	if len(stocks) > 0 {
		var openingRows []struct {
			ProductID uuid.UUID
			OutletID  uuid.UUID
			BatchNo   string
			TotalQty  float64
		}
		utils.DB.Model(&models.StockEntry{}).
			Select("product_id, outlet_id, batch_no, COALESCE(SUM(quantity), 0) as total_qty").
			Where("user_id = ? AND entry_type = ? AND product_id IS NOT NULL", userID, "opening").
			Group("product_id, outlet_id, batch_no").
			Scan(&openingRows)
		for _, row := range openingRows {
			openingQtyMap[openingKey{
				ProductID: row.ProductID,
				OutletID:  row.OutletID,
				BatchNo:   strings.TrimSpace(row.BatchNo),
			}] = row.TotalQty
		}
	}

	// Fetch outlet names
	var outletIDs []uuid.UUID
	for _, stock := range stocks {
		outletIDs = append(outletIDs, stock.OutletID)
	}
	var warehouses []models.Warehouse
	if len(outletIDs) > 0 {
		utils.DB.Where("id IN ?", outletIDs).Find(&warehouses)
	}
	warehouseMap := make(map[uuid.UUID]string)
	for _, wh := range warehouses {
		warehouseMap[wh.ID] = wh.Name
	}

	// Add outlet names to stocks
	type InventoryStockWithOutlet struct {
		models.InventoryStock
		OutletName string `json:"outlet_name"`
	}
	stocksWithOutlet := make([]InventoryStockWithOutlet, 0, len(stocks))
	for _, stock := range stocks {
		if stock.InitialQuantity == 0 {
			if qty, ok := openingQtyMap[openingKey{
				ProductID: stock.ProductID,
				OutletID:  stock.OutletID,
				BatchNo:   strings.TrimSpace(stock.BatchNo),
			}]; ok {
				stock.InitialQuantity = qty
			}
		}
		stocksWithOutlet = append(stocksWithOutlet, InventoryStockWithOutlet{
			InventoryStock: stock,
			OutletName:     warehouseMap[stock.OutletID],
		})
	}

	c.JSON(http.StatusOK, stocksWithOutlet)
}

func GetProductStock(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	productID := c.Param("id")

	fmt.Printf("[DEBUG] GetProductStock - UserID: %s, ProductID: %s\n", userID, productID)

	var stocks []models.InventoryStock
	if err := utils.DB.Where("user_id = ? AND product_id = ?", userID, productID).Find(&stocks).Error; err != nil {
		fmt.Printf("[DEBUG] GetProductStock - DB error: %v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch product stock"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": stocks})
}

func SearchByItemCode(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	itemCode := strings.TrimSpace(c.Query("item_code"))
	if itemCode == "" {
		itemCode = strings.TrimSpace(c.Query("barcode"))
	}
	outletID := c.Query("outlet_id")

	fmt.Printf("[DEBUG] SearchByItemCode - UserID: %s, ItemCode: %s, OutletID: %s\n", userID, itemCode, outletID)

	if itemCode == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Item code is required"})
		return
	}

	var entries []models.StockEntry
	query := utils.DB.Where("user_id = ? AND TRIM(item_code) = ?", userID, itemCode).Preload("Product")

	if outletID != "" {
		query = query.Where("outlet_id = ?", outletID)
	}

	if err := query.Find(&entries).Error; err != nil {
		fmt.Printf("[DEBUG] SearchByItemCode - DB error: %v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to search by item code"})
		return
	}

	fmt.Printf("[DEBUG] SearchByItemCode - Found %d entries\n", len(entries))

	if len(entries) == 0 {
		var products []models.Product
		if err := utils.DB.Where(
			"user_id = ? AND (TRIM(item_code) = ? OR TRIM(sku) = ?)",
			userID, itemCode, itemCode,
		).Find(&products).Error; err != nil {
			fmt.Printf("[DEBUG] SearchByItemCode - Product fallback error: %v\n", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to search by item code"})
			return
		}
		if len(products) == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "No products found with this item code"})
			return
		}
		results := make([]map[string]interface{}, 0, len(products))
		for _, product := range products {
			results = append(results, map[string]interface{}{
				"product_id":     product.ID,
				"product_name":   product.Name,
				"sku":            product.SKU,
				"item_code":      product.ItemCode,
				"unit":           product.Unit,
				"purchase_price": product.PurchasePrice,
				"sale_price":     product.SalePrice,
				"tax_rate":       product.TaxRate,
				"hsn_code":       product.HSNCode,
			})
		}
		c.JSON(http.StatusOK, gin.H{"data": results})
		return
	}

	// Transform to product list with entry information
	results := make([]map[string]interface{}, 0)
	for _, entry := range entries {
		result := map[string]interface{}{
			"product_id":   entry.ProductID,
			"product_name": entry.Product.Name,
			"sku":          entry.Product.SKU,
			"item_code":    entry.ItemCode,
			"outlet_id":    entry.OutletID,
			"quantity":     entry.Quantity,
			"cost_price":   entry.CostPrice,
			"batch_no":     entry.BatchNo,
			"unit":         entry.Product.Unit,
			"purchase_price": entry.Product.PurchasePrice,
			"sale_price":   entry.Product.SalePrice,
			"tax_rate":     entry.Product.TaxRate,
			"hsn_code":     entry.Product.HSNCode,
		}
		results = append(results, result)
	}

	c.JSON(http.StatusOK, gin.H{"data": results})
}

func AdjustStock(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		ItemName  string     `json:"item_name" binding:"required"`
		ProductID *uuid.UUID `json:"product_id"`
		OutletID  uuid.UUID  `json:"outlet_id" binding:"required"`
		Quantity  float64    `json:"quantity" binding:"required"`
		BatchNo   string     `json:"batch_no"`
		Reason    string     `json:"reason"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		fmt.Printf("[DEBUG] AdjustStock - JSON bind error: %v\n", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	input.Reason = strings.TrimSpace(input.Reason)
	if input.Reason == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Reason is required for stock adjustment"})
		return
	}

	fmt.Printf("[DEBUG] AdjustStock - UserID: %s, ItemName: %s, Quantity: %f\n", userID, input.ItemName, input.Quantity)

	// Verify product exists if provided
	if input.ProductID != nil {
		var product models.Product
		if err := utils.DB.Where("user_id = ? AND id = ?", userID, *input.ProductID).First(&product).Error; err != nil {
			fmt.Printf("[DEBUG] AdjustStock - Product not found: %v\n", err)
			c.JSON(http.StatusNotFound, gin.H{"error": "Product not found"})
			return
		}
	}

	batchNo := strings.TrimSpace(input.BatchNo)

	// Create stock entry
	entry := models.StockEntry{
		ID:         uuid.New(),
		UserID:     userID,
		ItemName:   input.ItemName,
		ProductID:  input.ProductID,
		OutletID:   input.OutletID,
		EntryType:  "adjustment",
		Quantity:   input.Quantity,
		BalanceQty: 0,
		BatchNo:    batchNo,
		Notes:      input.Reason,
		EntryDate:  time.Now(),
	}

	if err := utils.DB.Create(&entry).Error; err != nil {
		fmt.Printf("[DEBUG] AdjustStock - DB create error: %v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create adjustment entry"})
		return
	}

	fmt.Printf("[DEBUG] AdjustStock - Entry created successfully: %s\n", entry.ID)

	// Update inventory stock if product is linked
	if input.ProductID != nil {
		updateInventoryStock(userID, *input.ProductID, input.OutletID, "adjustment", input.Quantity, 0, batchNo, nil, nil)
	}

	c.JSON(http.StatusCreated, entry)
}

func ReserveStock(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		ProductID uuid.UUID `json:"product_id" binding:"required"`
		OutletID  uuid.UUID `json:"outlet_id" binding:"required"`
		Quantity  float64   `json:"quantity" binding:"required"`
		BatchNo   string    `json:"batch_no"`
		Reason    string    `json:"reason"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		fmt.Printf("[DEBUG] ReserveStock - JSON bind error: %v\n", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	input.Reason = strings.TrimSpace(input.Reason)
	if input.Reason == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Reason is required for stock reservation"})
		return
	}

	fmt.Printf("[DEBUG] ReserveStock - UserID: %s, ProductID: %s, OutletID: %s, Quantity: %f\n", userID, input.ProductID, input.OutletID, input.Quantity)

	// Verify product exists
	var product models.Product
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, input.ProductID).First(&product).Error; err != nil {
		fmt.Printf("[DEBUG] ReserveStock - Product not found: %v\n", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "Product not found"})
		return
	}

	batchNo := strings.TrimSpace(input.BatchNo)
	var stock models.InventoryStock
	var err error
	if batchNo != "" {
		err = utils.DB.Where(
			"user_id = ? AND product_id = ? AND outlet_id = ? AND batch_no = ?",
			userID, input.ProductID, input.OutletID, batchNo,
		).First(&stock).Error
	} else if product.EnableBatching {
		var ok bool
		stock, ok = pickFEFOBatch(userID, input.ProductID, input.OutletID, input.Quantity)
		if !ok {
			err = fmt.Errorf("no batch stock")
		}
	} else {
		err = utils.DB.Where(
			"user_id = ? AND product_id = ? AND outlet_id = ? AND batch_no = ?",
			userID, input.ProductID, input.OutletID, "",
		).First(&stock).Error
		if err != nil {
			// Fallback: any stock row for product+outlet (legacy unbatched rows)
			err = utils.DB.Where(
				"user_id = ? AND product_id = ? AND outlet_id = ?",
				userID, input.ProductID, input.OutletID,
			).Order("available_qty DESC").First(&stock).Error
		}
	}
	if err != nil {
		fmt.Printf("[DEBUG] ReserveStock - Stock not found: %v\n", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "Stock not found for this product and outlet"})
		return
	}

	// Check if enough available stock
	if stock.AvailableQty < input.Quantity {
		fmt.Printf("[DEBUG] ReserveStock - Insufficient stock: Available=%f, Requested=%f\n", stock.AvailableQty, input.Quantity)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Insufficient available stock"})
		return
	}

	// Update reserved quantity and recompute available
	stock.ReservedQty += input.Quantity
	stock.AvailableQty = stock.Quantity - stock.ReservedQty
	stock.LastUpdated = time.Now()
	if err := utils.DB.Save(&stock).Error; err != nil {
		fmt.Printf("[DEBUG] ReserveStock - DB update error: %v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reserve stock"})
		return
	}

	// Create stock entry for reservation
	entry := models.StockEntry{
		ID:         uuid.New(),
		UserID:     userID,
		ItemName:   product.Name,
		ProductID:  &input.ProductID,
		OutletID:   input.OutletID,
		EntryType:  "reservation",
		Quantity:   -input.Quantity,
		BalanceQty: 0,
		CostPrice:  stock.AverageCost,
		Notes:      input.Reason,
		EntryDate:  time.Now(),
	}

	if err := utils.DB.Create(&entry).Error; err != nil {
		fmt.Printf("[DEBUG] ReserveStock - Failed to create entry: %v\n", err)
	}

	fmt.Printf("[DEBUG] ReserveStock - Stock reserved successfully\n")
	c.JSON(http.StatusOK, gin.H{"message": "Stock reserved successfully", "reserved_qty": stock.ReservedQty, "available_qty": stock.AvailableQty})
}

func ReleaseStock(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		ProductID uuid.UUID `json:"product_id" binding:"required"`
		OutletID  uuid.UUID `json:"outlet_id" binding:"required"`
		Quantity  float64   `json:"quantity" binding:"required"`
		BatchNo   string    `json:"batch_no"`
		Reason    string    `json:"reason"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		fmt.Printf("[DEBUG] ReleaseStock - JSON bind error: %v\n", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	input.Reason = strings.TrimSpace(input.Reason)
	if input.Reason == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Reason is required for stock release"})
		return
	}

	fmt.Printf("[DEBUG] ReleaseStock - UserID: %s, ProductID: %s, OutletID: %s, Quantity: %f\n", userID, input.ProductID, input.OutletID, input.Quantity)

	// Verify product exists
	var product models.Product
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, input.ProductID).First(&product).Error; err != nil {
		fmt.Printf("[DEBUG] ReleaseStock - Product not found: %v\n", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "Product not found"})
		return
	}

	batchNo := strings.TrimSpace(input.BatchNo)
	var stock models.InventoryStock
	var err error
	if batchNo != "" {
		err = utils.DB.Where(
			"user_id = ? AND product_id = ? AND outlet_id = ? AND batch_no = ?",
			userID, input.ProductID, input.OutletID, batchNo,
		).First(&stock).Error
	} else {
		err = utils.DB.Where(
			"user_id = ? AND product_id = ? AND outlet_id = ? AND reserved_qty > 0",
			userID, input.ProductID, input.OutletID,
		).Order("reserved_qty DESC").First(&stock).Error
	}
	if err != nil {
		fmt.Printf("[DEBUG] ReleaseStock - Stock not found: %v\n", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "Stock not found for this product and outlet"})
		return
	}

	// Check if enough reserved stock
	if stock.ReservedQty < input.Quantity {
		fmt.Printf("[DEBUG] ReleaseStock - Insufficient reserved stock: Reserved=%f, Requested=%f\n", stock.ReservedQty, input.Quantity)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Insufficient reserved stock"})
		return
	}

	// Update reserved quantity and recompute available
	stock.ReservedQty -= input.Quantity
	if stock.ReservedQty < 0 {
		stock.ReservedQty = 0
	}
	stock.AvailableQty = stock.Quantity - stock.ReservedQty
	stock.LastUpdated = time.Now()
	if err := utils.DB.Save(&stock).Error; err != nil {
		fmt.Printf("[DEBUG] ReleaseStock - DB update error: %v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to release stock"})
		return
	}

	// Create stock entry for release
	entry := models.StockEntry{
		ID:         uuid.New(),
		UserID:     userID,
		ItemName:   product.Name,
		ProductID:  &input.ProductID,
		OutletID:   input.OutletID,
		EntryType:  "release",
		Quantity:   input.Quantity,
		BalanceQty: 0,
		CostPrice:  stock.AverageCost,
		BatchNo:    stock.BatchNo,
		Notes:      input.Reason,
		EntryDate:  time.Now(),
	}

	if err := utils.DB.Create(&entry).Error; err != nil {
		fmt.Printf("[DEBUG] ReleaseStock - Failed to create entry: %v\n", err)
	}

	fmt.Printf("[DEBUG] ReleaseStock - Stock released successfully\n")
	c.JSON(http.StatusOK, gin.H{"message": "Stock released successfully", "reserved_qty": stock.ReservedQty, "available_qty": stock.AvailableQty})
}

// countConsolidatedLowStockProducts counts distinct products where consolidated stock
// (summed across batches per outlet, matching stock balance) is at or below min_stock.
func countConsolidatedLowStockProducts(userID uuid.UUID, excludeDeleted bool) int64 {
	var count int64
	deletedFilter := ""
	if excludeDeleted {
		deletedFilter = " AND p.deleted_at IS NULL"
	}
	query := `
		SELECT COUNT(DISTINCT product_id) FROM (
			SELECT p.id AS product_id
			FROM products p
			INNER JOIN inventory_stocks s ON p.id = s.product_id
			WHERE p.user_id = ? AND p.low_stock_alert = true` + deletedFilter + `
			GROUP BY p.id, p.min_stock, s.outlet_id
			HAVING SUM(s.quantity) <= p.min_stock
		) consolidated
	`
	utils.DB.Raw(query, userID).Scan(&count)
	return count
}

func GetLowStockAlerts(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	fmt.Printf("[DEBUG] GetLowStockAlerts - UserID: %s\n", userID)

	type LowStockItem struct {
		ProductID    uuid.UUID `json:"product_id"`
		ProductName  string    `json:"product_name"`
		SKU          string    `json:"sku"`
		CurrentStock float64   `json:"current_stock"`
		MinStock     float64   `json:"min_stock"`
		OutletID     uuid.UUID `json:"outlet_id"`
		OutletName   string    `json:"outlet_name"`
	}

	results := make([]LowStockItem, 0)
	// Consolidate batches per product + outlet (same as GetStockBalance).
	query := `
		SELECT p.id as product_id, p.name as product_name, p.sku, p.min_stock, s.outlet_id,
			SUM(s.quantity) as current_stock, w.name as outlet_name
		FROM products p
		INNER JOIN inventory_stocks s ON p.id = s.product_id
		LEFT JOIN warehouses w ON s.outlet_id = w.id
		WHERE p.user_id = ? AND p.deleted_at IS NULL AND p.low_stock_alert = true
		GROUP BY p.id, p.name, p.sku, p.min_stock, s.outlet_id, w.name
		HAVING SUM(s.quantity) <= p.min_stock
	`

	if err := utils.DB.Raw(query, userID).Scan(&results).Error; err != nil {
		fmt.Printf("[DEBUG] GetLowStockAlerts - DB error: %v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch low stock alerts"})
		return
	}

	fmt.Printf("[DEBUG] GetLowStockAlerts - Found %d low stock alerts\n", len(results))
	c.JSON(http.StatusOK, results)
}

// GetInventoryItems returns all items (products and standalone stock entries) for dropdown selection
func GetInventoryItems(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	fmt.Printf("[DEBUG] GetInventoryItems - UserID: %s\n", userID)

	type InventoryItem struct {
		ID              uuid.UUID `json:"id"`
		Name            string    `json:"name"`
		SKU             string    `json:"sku"`
		Type            string    `json:"type"` // "product" or "standalone"
		IsActive        bool      `json:"is_active"`
		EnableBatching  bool      `json:"enable_batching"`
	}

	items := make([]InventoryItem, 0)

	// Get all active products
	var products []models.Product
	if err := utils.DB.Where("user_id = ? AND is_active = ?", userID, true).Find(&products).Error; err != nil {
		fmt.Printf("[DEBUG] GetInventoryItems - Failed to fetch products: %v\n", err)
	} else {
		for _, p := range products {
			items = append(items, InventoryItem{
				ID:             p.ID,
				Name:           p.Name,
				SKU:            p.SKU,
				Type:           "product",
				IsActive:       p.IsActive,
				EnableBatching: p.EnableBatching,
			})
		}
	}

	// Get standalone stock entries (entries without product_id)
	var standaloneEntries []models.StockEntry
	if err := utils.DB.Where("user_id = ? AND product_id IS NULL", userID).Group("item_name").Find(&standaloneEntries).Error; err != nil {
		fmt.Printf("[DEBUG] GetInventoryItems - Failed to fetch standalone entries: %v\n", err)
	} else {
		for _, entry := range standaloneEntries {
			items = append(items, InventoryItem{
				ID:       entry.ID,
				Name:     entry.ItemName,
				SKU:      "",
				Type:     "standalone",
				IsActive: true,
			})
		}
	}

	fmt.Printf("[DEBUG] GetInventoryItems - Found %d items\n", len(items))
	c.JSON(http.StatusOK, items)
}

func findProductForBulkStock(userID uuid.UUID, sku, productName, itemCode string) (*models.Product, error) {
	sku = strings.TrimSpace(sku)
	productName = strings.TrimSpace(productName)
	itemCode = strings.TrimSpace(itemCode)

	if sku != "" {
		var product models.Product
		if err := utils.DB.Where("user_id = ? AND sku = ?", userID, sku).First(&product).Error; err == nil {
			return &product, nil
		}
	}

	if itemCode != "" {
		var product models.Product
		if err := utils.DB.Where("user_id = ? AND item_code = ?", userID, itemCode).First(&product).Error; err == nil {
			return &product, nil
		}
	}

	if productName != "" {
		var product models.Product
		if err := utils.DB.Where("user_id = ? AND name = ?", userID, productName).First(&product).Error; err == nil {
			return &product, nil
		}
	}

	return nil, fmt.Errorf("product not found")
}

func findWarehouseForBulkStock(userID uuid.UUID, warehouseRef string) (*models.Warehouse, error) {
	warehouseRef = strings.TrimSpace(warehouseRef)
	if warehouseRef == "" {
		return nil, fmt.Errorf("warehouse is required")
	}

	var warehouse models.Warehouse
	if err := utils.DB.Where(
		"user_id = ? AND is_active = ? AND (code = ? OR LOWER(name) = LOWER(?))",
		userID, true, warehouseRef, warehouseRef,
	).First(&warehouse).Error; err != nil {
		return nil, fmt.Errorf("warehouse not found")
	}

	return &warehouse, nil
}

func processBulkStockRow(
	userID uuid.UUID,
	sku, productName, itemCode, warehouseRef, updateMode string,
	quantity, costPrice float64,
	batchNo, notes string,
) error {
	if sku == "" && productName == "" && itemCode == "" {
		return fmt.Errorf("SKU, product name, or item code is required")
	}

	product, err := findProductForBulkStock(userID, sku, productName, itemCode)
	if err != nil {
		return err
	}

	warehouse, err := findWarehouseForBulkStock(userID, warehouseRef)
	if err != nil {
		return err
	}

	mode := strings.ToLower(strings.TrimSpace(updateMode))
	if mode == "" {
		mode = "adjust"
	}

	adjQty := quantity
	switch mode {
	case "set":
		currentQty := 0.0
		batchKey := strings.TrimSpace(batchNo)
		var stock models.InventoryStock
		if err := utils.DB.Where(
			"user_id = ? AND product_id = ? AND outlet_id = ? AND batch_no = ?",
			userID, product.ID, warehouse.ID, batchKey,
		).First(&stock).Error; err == nil {
			currentQty = stock.Quantity
		}
		adjQty = quantity - currentQty
	case "adjust":
		if quantity == 0 {
			return fmt.Errorf("quantity cannot be zero for adjust mode")
		}
	default:
		return fmt.Errorf("invalid update mode %q (use adjust or set)", updateMode)
	}

	if adjQty == 0 {
		return nil
	}

	productID := product.ID
	entry := models.StockEntry{
		ID:        uuid.New(),
		UserID:    userID,
		ItemName:  product.Name,
		ProductID: &productID,
		OutletID:  warehouse.ID,
		EntryType: "adjustment",
		Quantity:  adjQty,
		CostPrice: costPrice,
		BatchNo:   batchNo,
		Notes:     notes,
		EntryDate: time.Now(),
	}

	if err := utils.DB.Create(&entry).Error; err != nil {
		return err
	}

	updateInventoryStock(userID, product.ID, warehouse.ID, "adjustment", adjQty, costPrice, batchNo, nil, nil)
	return nil
}

func BulkUpdateStockCSV(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No file uploaded"})
		return
	}

	src, err := file.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to open file"})
		return
	}
	defer src.Close()

	reader := csv.NewReader(src)
	records, err := reader.ReadAll()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to read CSV"})
		return
	}

	if len(records) < 2 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "CSV file is empty or has no data"})
		return
	}

	headers := records[0]
	var updatedCount int
	var errors []string

	for i, record := range records[1:] {
		if len(record) != len(headers) {
			errors = append(errors, fmt.Sprintf("Row %d: Column count mismatch", i+2))
			continue
		}

		sku := getCSVValue(record, headers, "SKU")
		productName := firstCSVValue(record, headers, "Product Name", "Name")
		itemCode := firstCSVValue(record, headers, "Item Code", "Itemcode", "Barcode")
		warehouseRef := firstCSVValue(record, headers, "Warehouse", "Outlet", "Warehouse Name", "Warehouse Code")
		updateMode := getCSVValue(record, headers, "Update Mode")
		quantity := parseFloat(getCSVValue(record, headers, "Quantity"))
		costPrice := parseFloat(firstCSVValue(record, headers, "Cost Price", "Cost"))
		batchNo := getCSVValue(record, headers, "Batch No")
		notes := firstCSVValue(record, headers, "Notes", "Reason")

		if err := processBulkStockRow(userID, sku, productName, itemCode, warehouseRef, updateMode, quantity, costPrice, batchNo, notes); err != nil {
			errors = append(errors, fmt.Sprintf("Row %d: %v", i+2, err))
			continue
		}

		updatedCount++
	}

	c.JSON(http.StatusOK, gin.H{
		"updated": updatedCount,
		"errors":  errors,
	})
}

func BulkUpdateStockExcel(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No file uploaded"})
		return
	}

	src, err := file.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to open file"})
		return
	}
	defer src.Close()

	xlsxFile, err := xlsx.OpenReaderAt(src, file.Size)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to read Excel file"})
		return
	}

	sheet := xlsxFile.Sheets[0]
	if sheet == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Excel file has no sheets"})
		return
	}

	if sheet.MaxRow < 2 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Excel file is empty or has no data"})
		return
	}

	headers := make(map[int]string)
	headerRow, err := sheet.Row(0)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to read header row"})
		return
	}
	for i := 0; ; i++ {
		cell := headerRow.GetCell(i)
		if cell == nil {
			break
		}
		headers[i] = cell.String()
	}

	var updatedCount int
	var errors []string

	for i := 1; i <= sheet.MaxRow; i++ {
		row, err := sheet.Row(i)
		if err != nil {
			continue
		}

		sku := getExcelValue(row, headers, "SKU")
		productName := firstExcelValue(row, headers, "Product Name", "Name")
		itemCode := firstExcelValue(row, headers, "Item Code", "Itemcode", "Barcode")
		warehouseRef := firstExcelValue(row, headers, "Warehouse", "Outlet", "Warehouse Name", "Warehouse Code")
		updateMode := getExcelValue(row, headers, "Update Mode")
		quantity := parseFloat(getExcelValue(row, headers, "Quantity"))
		costPrice := parseFloat(firstExcelValue(row, headers, "Cost Price", "Cost"))
		batchNo := getExcelValue(row, headers, "Batch No")
		notes := firstExcelValue(row, headers, "Notes", "Reason")

		if err := processBulkStockRow(userID, sku, productName, itemCode, warehouseRef, updateMode, quantity, costPrice, batchNo, notes); err != nil {
			errors = append(errors, fmt.Sprintf("Row %d: %v", i+1, err))
			continue
		}

		updatedCount++
	}

	c.JSON(http.StatusOK, gin.H{
		"updated": updatedCount,
		"errors":  errors,
	})
}
