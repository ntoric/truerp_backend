package controllers

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/go-pdf/fpdf"
	"github.com/google/uuid"
)

func GetPurchaseOrders(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var orders []models.PurchaseOrder
	query := utils.DB.Where("user_id = ?", userID).Preload("Party")

	if partyID := c.Query("party_id"); partyID != "" {
		query = query.Where("party_id = ?", partyID)
	}
	if status := c.Query("status"); status != "" {
		query = query.Where("status = ?", status)
	}
	if fromDate := c.Query("from_date"); fromDate != "" {
		query = query.Where("order_date >= ?", fromDate)
	}
	if toDate := c.Query("to_date"); toDate != "" {
		query = query.Where("order_date <= ?", toDate)
	}

	if err := query.Order("order_date DESC").Find(&orders).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch orders"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": orders})
}

func GetPurchaseOrder(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var order models.PurchaseOrder
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Preload("Party").Preload("Items").First(&order).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Order not found"})
		return
	}

	c.JSON(http.StatusOK, order)
}

func CreatePurchaseOrder(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		PartyID      uuid.UUID  `json:"party_id" binding:"required"`
		OrderDate    time.Time  `json:"order_date" binding:"required"`
		ExpectedDate *time.Time `json:"expected_date"`
		Notes        string     `json:"notes"`
		Terms        string     `json:"terms"`
		Items        []struct {
			Description string  `json:"description" binding:"required"`
			Quantity    float64 `json:"quantity" binding:"required,gt=0"`
			UnitPrice   float64 `json:"unit_price" binding:"required"`
			TaxRate     float64 `json:"tax_rate"`
		} `json:"items" binding:"required,min=1"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var count int64
	utils.DB.Model(&models.PurchaseOrder{}).Where("user_id = ?", userID).Count(&count)

	order := models.PurchaseOrder{
		ID:           uuid.New(),
		UserID:       userID,
		PartyID:      input.PartyID,
		OrderNumber:  fmt.Sprintf("PO-%04d", count+1),
		Status:       "draft",
		OrderDate:    input.OrderDate,
		ExpectedDate: input.ExpectedDate,
		Notes:        input.Notes,
		Terms:        input.Terms,
	}

	var subTotal, taxTotal float64
	for _, item := range input.Items {
		taxAmount := item.UnitPrice * item.Quantity * (item.TaxRate / 100)
		total := item.UnitPrice*item.Quantity + taxAmount

		order.Items = append(order.Items, models.PurchaseOrderItem{
			ID:          uuid.New(),
			OrderID:     order.ID,
			Description: item.Description,
			Quantity:    item.Quantity,
			UnitPrice:   item.UnitPrice,
			TaxRate:     item.TaxRate,
			TaxAmount:   taxAmount,
			Total:       total,
		})

		subTotal += item.UnitPrice * item.Quantity
		taxTotal += taxAmount
	}

	order.SubTotal = subTotal
	order.TaxTotal = taxTotal
	order.TotalAmount = subTotal + taxTotal

	if err := utils.DB.Create(&order).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create order"})
		return
	}

	c.JSON(http.StatusCreated, order)
}

func UpdatePurchaseOrder(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var order models.PurchaseOrder
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&order).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Order not found"})
		return
	}

	if order.Status != "draft" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot edit submitted order"})
		return
	}

	var input struct {
		ExpectedDate *time.Time `json:"expected_date"`
		Notes        string     `json:"notes"`
		Terms        string     `json:"terms"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	updates := map[string]interface{}{
		"expected_date": input.ExpectedDate,
		"notes":         input.Notes,
		"terms":         input.Terms,
	}

	if err := utils.DB.Model(&order).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update order"})
		return
	}

	c.JSON(http.StatusOK, order)
}

func SubmitPurchaseOrder(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var order models.PurchaseOrder
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&order).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Order not found"})
		return
	}

	if order.Status != "draft" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Order already submitted"})
		return
	}

	order.Status = "submitted"
	utils.DB.Save(&order)

	c.JSON(http.StatusOK, order)
}

func DeletePurchaseOrder(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var order models.PurchaseOrder
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&order).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Order not found"})
		return
	}

	if order.Status != "draft" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot delete submitted order"})
		return
	}

	utils.DB.Delete(&order)
	c.JSON(http.StatusOK, gin.H{"message": "Order deleted"})
}

func GetPurchaseReceipts(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var receipts []models.PurchaseReceipt
	query := utils.DB.Where("user_id = ?", userID).Preload("Party")

	if partyID := c.Query("party_id"); partyID != "" {
		query = query.Where("party_id = ?", partyID)
	}
	if status := c.Query("status"); status != "" {
		query = query.Where("status = ?", status)
	}

	if err := query.Order("receipt_date DESC").Find(&receipts).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch receipts"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": receipts})
}

func GetPurchaseReceipt(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var receipt models.PurchaseReceipt
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Preload("Party").Preload("Items").First(&receipt).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Receipt not found"})
		return
	}

	c.JSON(http.StatusOK, receipt)
}

func CreatePurchaseReceipt(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		PurchaseOrderID *uuid.UUID `json:"purchase_order_id"`
		PartyID         uuid.UUID  `json:"party_id" binding:"required"`
		ReceiptDate     time.Time  `json:"receipt_date" binding:"required"`
		Notes           string     `json:"notes"`
		Items           []struct {
			Description string     `json:"description" binding:"required"`
			Quantity    float64    `json:"quantity" binding:"required,gt=0"`
			UnitPrice   float64    `json:"unit_price" binding:"required"`
			TaxRate     float64    `json:"tax_rate"`
			BatchNo     string               `json:"batch_no"`
			MfgDate     *models.FlexibleTime `json:"mfg_date"`
			ExpDate     *models.FlexibleTime `json:"exp_date"`
		} `json:"items" binding:"required,min=1"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var count int64
	utils.DB.Model(&models.PurchaseReceipt{}).Where("user_id = ?", userID).Count(&count)

	receipt := models.PurchaseReceipt{
		ID:              uuid.New(),
		UserID:          userID,
		PurchaseOrderID: input.PurchaseOrderID,
		PartyID:         input.PartyID,
		ReceiptNumber:   fmt.Sprintf("GRN-%04d", count+1),
		Status:          "draft",
		ReceiptDate:     input.ReceiptDate,
		Notes:           input.Notes,
	}

	var subTotal, taxTotal float64
	for _, item := range input.Items {
		taxAmount := item.UnitPrice * item.Quantity * (item.TaxRate / 100)
		total := item.UnitPrice*item.Quantity + taxAmount

		receipt.Items = append(receipt.Items, models.PurchaseReceiptItem{
			ID:          uuid.New(),
			ReceiptID:   receipt.ID,
			Description: item.Description,
			Quantity:    item.Quantity,
			UnitPrice:   item.UnitPrice,
			TaxRate:     item.TaxRate,
			TaxAmount:   taxAmount,
			Total:       total,
			BatchNo:     item.BatchNo,
			MfgDate:     item.MfgDate.Ptr(),
			ExpDate:     item.ExpDate.Ptr(),
		})

		subTotal += item.UnitPrice * item.Quantity
		taxTotal += taxAmount
	}

	receipt.SubTotal = subTotal
	receipt.TaxTotal = taxTotal
	receipt.TotalAmount = subTotal + taxTotal

	if err := utils.DB.Create(&receipt).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create receipt"})
		return
	}

	c.JSON(http.StatusCreated, receipt)
}

func SubmitPurchaseReceipt(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var receipt models.PurchaseReceipt
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Preload("Items").First(&receipt).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Receipt not found"})
		return
	}

	if receipt.Status != "draft" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Receipt already submitted"})
		return
	}

	for _, item := range receipt.Items {
		entry := models.StockEntry{
			ID:         uuid.New(),
			UserID:     userID,
			ItemName:   item.Description,
			EntryType:  "purchase",
			Quantity:   item.Quantity,
			BalanceQty: 0,
			CostPrice:  item.UnitPrice,
			BatchNo:    item.BatchNo,
			MfgDate:    item.MfgDate,
			ExpDate:    item.ExpDate,
			EntryDate:  receipt.ReceiptDate,
		}
		utils.DB.Create(&entry)
	}

	receipt.Status = "submitted"
	utils.DB.Save(&receipt)

	c.JSON(http.StatusOK, receipt)
}

func GetPurchaseBills(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var bills []models.PurchaseBill
	query := utils.DB.Where("user_id = ?", userID).Preload("Party")

	if partyID := c.Query("party_id"); partyID != "" {
		query = query.Where("party_id = ?", partyID)
	}
	if status := c.Query("status"); status != "" {
		query = query.Where("status = ?", status)
	}

	if err := query.Order("bill_date DESC").Find(&bills).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch bills"})
		return
	}

	c.JSON(http.StatusOK, bills)
}

func CreatePurchaseBill(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		PurchaseReceiptID *uuid.UUID `json:"purchase_receipt_id"`
		PartyID           uuid.UUID  `json:"party_id" binding:"required"`
		BillNumber        string     `json:"bill_number" binding:"required"`
		BillDate          time.Time  `json:"bill_date" binding:"required"`
		DueDate           *time.Time `json:"due_date"`
		WarehouseID       *uuid.UUID `json:"warehouse_id"`
		TotalAmount       float64    `json:"total_amount"` // 0 allowed (esp. drafts / zero-priced lines)
		PaidAmount        float64    `json:"paid_amount"`
		BalanceDue        float64    `json:"balance_due"`
		PaymentMode       string     `json:"payment_mode"`
		BankAccountID     *uuid.UUID `json:"bank_account_id"`
		Status            string     `json:"status"`
		Notes             string     `json:"notes"`
		TaxExempt         bool       `json:"tax_exempt"`
		Items             []struct {
			ProductID   *uuid.UUID           `json:"product_id"`
			ItemCode    string               `json:"item_code"`
			Description string               `json:"description" binding:"required"`
			Quantity    models.FlexibleFloat `json:"quantity" binding:"required"`
			Unit        string               `json:"unit"`
			UnitPrice   models.FlexibleFloat `json:"unit_price"`
			Discount    models.FlexibleFloat `json:"discount"`
			TaxRate     models.FlexibleFloat `json:"tax_rate"`
			MRP         models.FlexibleFloat `json:"mrp"`
			SalePrice   models.FlexibleFloat `json:"sale_price"`
			HSNCode     string               `json:"hsn_code"`
			BatchNo     string               `json:"batch_no"`
			MfgDate     *models.FlexibleTime `json:"mfg_date"`
			ExpDate     *models.FlexibleTime `json:"exp_date"`
		} `json:"items" binding:"required,min=1"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	status := input.Status
	if status == "" {
		status = "unpaid"
	}

	// Draft bills may omit batch numbers so line items can be autosaved while editing.
	if status != "draft" {
		for _, item := range input.Items {
			if err := validateBatchedProductRequiresBatch(userID, item.ProductID, item.BatchNo, item.Description); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
		}
	}

	if err := validateUserBankAccount(userID, input.BankAccountID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid bank account"})
		return
	}

	resolvedBankAccount, err := resolveBankAccountForPaymentMode(userID, input.PaymentMode, input.BankAccountID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid bank account for payment method"})
		return
	}

	warehouseID := input.WarehouseID
	if warehouseID == nil || *warehouseID == uuid.Nil {
		defaultWH := resolveDefaultWarehouseID(userID)
		if defaultWH != uuid.Nil {
			warehouseID = &defaultWH
		}
	} else {
		var warehouse models.Warehouse
		if err := utils.DB.Where("user_id = ? AND id = ?", userID, *warehouseID).First(&warehouse).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid warehouse"})
			return
		}
	}

	bill := models.PurchaseBill{
		ID:                uuid.New(),
		UserID:            userID,
		PurchaseReceiptID: input.PurchaseReceiptID,
		PartyID:           input.PartyID,
		VendorID:          &input.PartyID,
		BillNumber:        input.BillNumber,
		BillDate:          input.BillDate,
		DueDate:           input.DueDate,
		WarehouseID:       warehouseID,
		StockStatus:       "none",
		Status:            status,
		TaxExempt:         input.TaxExempt,
		TotalAmount: input.TotalAmount,
		PaidAmount:  input.PaidAmount,
		BalanceDue: func() float64 {
			due := input.TotalAmount - input.PaidAmount
			if due < 0 {
				return 0
			}
			return due
		}(),
		PaymentMode:   input.PaymentMode,
		BankAccountID: resolvedBankAccount,
		Notes:         input.Notes,
	}

	var subTotal, taxTotal float64
	for _, item := range input.Items {
		qty := item.Quantity.Float64()
		unitPrice := item.UnitPrice.Float64()
		discount := item.Discount.Float64()
		taxRate := item.TaxRate.Float64()
		if input.TaxExempt {
			taxRate = 0
		}
		if qty <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Item quantity must be greater than 0"})
			return
		}

		itemTotal := unitPrice * qty
		itemDiscount := itemTotal * (discount / 100)
		taxable := itemTotal - itemDiscount
		taxAmount := taxable * (taxRate / 100)
		total := taxable + taxAmount

		bill.Items = append(bill.Items, models.PurchaseBillItem{
			ID:          uuid.New(),
			BillID:      bill.ID,
			ProductID:   item.ProductID,
			ItemCode:    item.ItemCode,
			Description: item.Description,
			Quantity:    qty,
			Unit:        item.Unit,
			UnitPrice:   unitPrice,
			Discount:    discount,
			TaxRate:     taxRate,
			TaxAmount:   taxAmount,
			Total:       total,
			MRP:         item.MRP.Float64(),
			SalePrice:   item.SalePrice.Float64(),
			HSNCode:     item.HSNCode,
			BatchNo:     item.BatchNo,
			MfgDate:     item.MfgDate.Ptr(),
			ExpDate:     item.ExpDate.Ptr(),
		})

		subTotal += itemTotal
		taxTotal += taxAmount
	}

	bill.SubTotal = subTotal
	bill.TaxTotal = taxTotal

	if err := utils.DB.Create(&bill).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create bill"})
		return
	}

	if bill.Status != "draft" {
		if err := postPurchaseBillAccounting(utils.DB, userID, &bill); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Bill saved but failed to post to accounting"})
			return
		}

		// Full invoice amount is purchase expense (Dr Purchases / Cr AP).
		// Paid amount auto-creates Payment Out and reduces AP; unpaid remains Accounts Payable.
		if bill.PaidAmount > 0 {
			notes := fmt.Sprintf("Auto-created from purchase bill %s", bill.BillNumber)
			if err := createLinkedPurchasePaymentOut(utils.DB, userID, &bill, bill.PaidAmount, bill.BillDate, notes); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Bill saved but failed to create payment out"})
				return
			}
		}
	}

	if err := createPendingPurchaseStockEntries(userID, &bill); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Bill saved but failed to update inventory stock"})
		return
	}

	c.JSON(http.StatusCreated, bill)
}

func GetPurchaseBill(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var bill models.PurchaseBill
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Preload("Party").Preload("Items").First(&bill).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Bill not found"})
		return
	}

	c.JSON(http.StatusOK, bill)
}

func UpdatePurchaseBill(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var bill models.PurchaseBill
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&bill).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Bill not found"})
		return
	}

	var input struct {
		PartyID       uuid.UUID  `json:"party_id"`
		BillNumber    string     `json:"bill_number"`
		BillDate      time.Time  `json:"bill_date"`
		DueDate       *time.Time `json:"due_date"`
		WarehouseID   *uuid.UUID `json:"warehouse_id"`
		TotalAmount   float64    `json:"total_amount"`
		PaidAmount    float64    `json:"paid_amount"`
		BalanceDue    float64    `json:"balance_due"`
		PaymentMode   string     `json:"payment_mode"`
		BankAccountID *uuid.UUID `json:"bank_account_id"`
		Status        string     `json:"status"`
		Notes         string     `json:"notes"`
		TaxExempt     bool       `json:"tax_exempt"`
		Items         []struct {
			ProductID   *uuid.UUID           `json:"product_id"`
			ItemCode    string               `json:"item_code"`
			Description string               `json:"description"`
			Quantity    models.FlexibleFloat `json:"quantity"`
			Unit        string               `json:"unit"`
			UnitPrice   models.FlexibleFloat `json:"unit_price"`
			Discount    models.FlexibleFloat `json:"discount"`
			TaxRate     models.FlexibleFloat `json:"tax_rate"`
			MRP         models.FlexibleFloat `json:"mrp"`
			SalePrice   models.FlexibleFloat `json:"sale_price"`
			HSNCode     string               `json:"hsn_code"`
			BatchNo     string               `json:"batch_no"`
			MfgDate     *models.FlexibleTime `json:"mfg_date"`
			ExpDate     *models.FlexibleTime `json:"exp_date"`
		} `json:"items"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Draft bills may omit batch numbers so line items can be autosaved while editing.
	if input.Status != "draft" {
		for _, item := range input.Items {
			if err := validateBatchedProductRequiresBatch(userID, item.ProductID, item.BatchNo, item.Description); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
		}
	}

	// Status-only "mark as paid" from the list page (no items / totals in body).
	if input.Status == "paid" && len(input.Items) == 0 && input.TotalAmount == 0 && bill.TotalAmount > 0 {
		previousPaidAmount := bill.PaidAmount
		bill.Status = "paid"
		bill.PaidAmount = bill.TotalAmount
		bill.BalanceDue = 0
		if err := utils.DB.Model(&bill).Updates(map[string]interface{}{
			"status":      "paid",
			"paid_amount": bill.TotalAmount,
			"balance_due": 0,
		}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update bill"})
			return
		}
		if paymentDelta := bill.PaidAmount - previousPaidAmount; paymentDelta > 0 {
			notes := fmt.Sprintf("Auto-created from purchase bill %s (mark paid)", bill.BillNumber)
			if err := createLinkedPurchasePaymentOut(utils.DB, userID, &bill, paymentDelta, bill.BillDate, notes); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Bill updated but failed to create payment out"})
				return
			}
		}
		c.JSON(http.StatusOK, bill)
		return
	}

	if err := validateUserBankAccount(userID, input.BankAccountID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid bank account"})
		return
	}

	resolvedBankAccount, err := resolveBankAccountForPaymentMode(userID, input.PaymentMode, input.BankAccountID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid bank account for payment method"})
		return
	}

	previousPaidAmount := bill.PaidAmount

	warehouseID := input.WarehouseID
	if warehouseID == nil || *warehouseID == uuid.Nil {
		if bill.WarehouseID != nil {
			warehouseID = bill.WarehouseID
		} else {
			defaultWH := resolveDefaultWarehouseID(userID)
			if defaultWH != uuid.Nil {
				warehouseID = &defaultWH
			}
		}
	} else {
		var warehouse models.Warehouse
		if err := utils.DB.Where("user_id = ? AND id = ?", userID, *warehouseID).First(&warehouse).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid warehouse"})
			return
		}
	}

	// Update bill fields
	bill.PartyID = input.PartyID
	bill.VendorID = &input.PartyID
	bill.BillNumber = input.BillNumber
	bill.BillDate = input.BillDate
	bill.DueDate = input.DueDate
	bill.WarehouseID = warehouseID
	bill.TotalAmount = input.TotalAmount
	bill.PaidAmount = input.PaidAmount
	if due := input.TotalAmount - input.PaidAmount; due > 0 {
		bill.BalanceDue = due
	} else {
		bill.BalanceDue = 0
	}
	bill.PaymentMode = input.PaymentMode
	bill.BankAccountID = resolvedBankAccount
	bill.Status = input.Status
	bill.Notes = input.Notes
	bill.TaxExempt = input.TaxExempt

	// Delete old items and recreate
	utils.DB.Where("bill_id = ?", bill.ID).Delete(&models.PurchaseBillItem{})

	var subTotal, taxTotal float64
	bill.Items = nil
	for _, item := range input.Items {
		qty := item.Quantity.Float64()
		unitPrice := item.UnitPrice.Float64()
		discount := item.Discount.Float64()
		taxRate := item.TaxRate.Float64()
		if input.TaxExempt {
			taxRate = 0
		}
		if qty <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Item quantity must be greater than 0"})
			return
		}

		itemTotal := unitPrice * qty
		itemDiscount := itemTotal * (discount / 100)
		taxable := itemTotal - itemDiscount
		taxAmount := taxable * (taxRate / 100)
		total := taxable + taxAmount

		bill.Items = append(bill.Items, models.PurchaseBillItem{
			ID:          uuid.New(),
			BillID:      bill.ID,
			ProductID:   item.ProductID,
			ItemCode:    item.ItemCode,
			Description: item.Description,
			Quantity:    qty,
			Unit:        item.Unit,
			UnitPrice:   unitPrice,
			Discount:    discount,
			TaxRate:     taxRate,
			TaxAmount:   taxAmount,
			Total:       total,
			MRP:         item.MRP.Float64(),
			SalePrice:   item.SalePrice.Float64(),
			HSNCode:     item.HSNCode,
			BatchNo:     item.BatchNo,
			MfgDate:     item.MfgDate.Ptr(),
			ExpDate:     item.ExpDate.Ptr(),
		})

		subTotal += itemTotal
		taxTotal += taxAmount
	}

	bill.SubTotal = subTotal
	bill.TaxTotal = taxTotal

	// Reset linked stock entries so edits re-apply inventory immediately
	if err := removePurchaseStockEntries(userID, bill.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reset stock entries for bill"})
		return
	}

	if err := utils.DB.Save(&bill).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update bill"})
		return
	}

	if paymentDelta := bill.PaidAmount - previousPaidAmount; paymentDelta > 0 {
		notes := fmt.Sprintf("Auto-created from purchase bill %s (payment update)", bill.BillNumber)
		if err := createLinkedPurchasePaymentOut(utils.DB, userID, &bill, paymentDelta, bill.BillDate, notes); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Bill updated but failed to create payment out"})
			return
		}
	}

	if err := createPendingPurchaseStockEntries(userID, &bill); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Bill updated but failed to update inventory stock"})
		return
	}

	c.JSON(http.StatusOK, bill)
}

func DeletePurchaseBill(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var bill models.PurchaseBill
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&bill).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Bill not found"})
		return
	}

	if bill.Status == "paid" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot delete paid bill"})
		return
	}

	if err := removePurchaseStockEntries(userID, bill.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to remove linked stock entries"})
		return
	}

	utils.DB.Delete(&bill)
	c.JSON(http.StatusOK, gin.H{"message": "Bill deleted"})
}

func GetPurchaseBillStats(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var stats struct {
		TotalPurchase float64 `json:"total_purchase"`
		Paid          float64 `json:"paid"`
		Unpaid        float64 `json:"unpaid"`
	}

	// Fresh queries each time — reusing one GORM chain stacked status
	// filters and under-counted paid/unpaid (especially partial bills).
	// Exclude drafts from financial stats so incomplete invoices don't inflate totals.
	qTotal := utils.DB.Model(&models.PurchaseBill{}).Where("user_id = ? AND status <> ?", userID, "draft")
	qPaid := utils.DB.Model(&models.PurchaseBill{}).Where("user_id = ? AND status <> ?", userID, "draft")
	qUnpaid := utils.DB.Model(&models.PurchaseBill{}).Where("user_id = ? AND status <> ?", userID, "draft")

	if fromDate := c.Query("from_date"); fromDate != "" {
		qTotal = qTotal.Where("bill_date >= ?", fromDate)
		qPaid = qPaid.Where("bill_date >= ?", fromDate)
		qUnpaid = qUnpaid.Where("bill_date >= ?", fromDate)
	}
	if toDate := c.Query("to_date"); toDate != "" {
		qTotal = qTotal.Where("bill_date <= ?", toDate)
		qPaid = qPaid.Where("bill_date <= ?", toDate)
		qUnpaid = qUnpaid.Where("bill_date <= ?", toDate)
	}

	qTotal.Select("COALESCE(SUM(total_amount), 0)").Scan(&stats.TotalPurchase)
	// Include partial payments, not only fully paid bills.
	qPaid.Select("COALESCE(SUM(paid_amount), 0)").Scan(&stats.Paid)
	// Remaining balance from amounts (reliable even if balance_due is stale).
	qUnpaid.Select("COALESCE(SUM(CASE WHEN total_amount > paid_amount THEN total_amount - paid_amount ELSE 0 END), 0)").Scan(&stats.Unpaid)

	c.JSON(http.StatusOK, stats)
}

func GeneratePurchaseBillPDF(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var bill models.PurchaseBill
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).
		Preload("Party").
		Preload("Items").
		First(&bill).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Purchase bill not found"})
		return
	}

	html := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
	<meta charset="UTF-8">
	<title>Purchase Invoice - %s</title>
	<style>
		body { font-family: Arial, sans-serif; margin: 0; padding: 20px; color: #333; }
		.header { display: flex; justify-content: space-between; margin-bottom: 30px; }
		.title { font-size: 24px; font-weight: bold; color: #2563eb; }
		.invoice-number { font-size: 18px; color: #666; }
		.section { margin-bottom: 20px; }
		.section-title { font-size: 14px; font-weight: bold; color: #666; margin-bottom: 10px; }
		.info-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
		.info-item { margin-bottom: 5px; }
		.info-label { font-weight: bold; color: #666; }
		table { width: 100%%; border-collapse: collapse; margin-top: 10px; }
		th, td { border: 1px solid #ddd; padding: 10px; text-align: left; }
		th { background-color: #f3f4f6; font-weight: bold; }
		.totals { margin-top: 20px; text-align: right; }
		.total-row { display: flex; justify-content: flex-end; margin-bottom: 5px; }
		.total-label { width: 150px; font-weight: bold; }
		.total-value { width: 100px; }
		.grand-total { font-size: 18px; font-weight: bold; color: #2563eb; }
		.footer { margin-top: 40px; padding-top: 20px; border-top: 1px solid #ddd; }
		.status { display: inline-block; padding: 5px 10px; border-radius: 4px; font-size: 12px; font-weight: bold; text-transform: uppercase; }
		.status-unpaid { background-color: #fee2e2; color: #991b1b; }
		.status-paid { background-color: #d1fae5; color: #065f46; }
		.status-partial { background-color: #fef3c7; color: #92400e; }
		@media print { body { margin: 0; padding: 10px; } }
	</style>
</head>
<body>
	<div class="header">
		<div>
			<div class="title">PURCHASE INVOICE</div>
			<div class="invoice-number">%s</div>
		</div>
		<div>
			<span class="status status-%s">%s</span>
		</div>
	</div>
	<div class="section">
		<div class="section-title">Vendor</div>
		<div class="info-item">
			<strong>%s</strong><br>
			%s<br>
			%s<br>
			GSTIN: %s
		</div>
	</div>
	<div class="section">
		<div class="section-title">Invoice Details</div>
		<div class="info-grid">
			<div class="info-item"><span class="info-label">Bill Date:</span> %s</div>
			<div class="info-item"><span class="info-label">Due Date:</span> %s</div>
			<div class="info-item"><span class="info-label">Status:</span> %s</div>
			<div class="info-item"><span class="info-label">Balance Due:</span> ₹%.2f</div>
		</div>
	</div>
	<div class="section">
		<div class="section-title">Items</div>
		<table>
			<thead>
				<tr>
					<th>Description</th>
					<th>Quantity</th>
					<th>Unit</th>
					<th>Unit Price</th>
					<th>Tax %%</th>
					<th>Total</th>
				</tr>
			</thead>
			<tbody>%s</tbody>
		</table>
	</div>
	<div class="totals">
		<div class="total-row"><span class="total-label">Sub Total:</span><span class="total-value">₹%.2f</span></div>
		<div class="total-row"><span class="total-label">Tax Total:</span><span class="total-value">₹%.2f</span></div>
		<div class="total-row grand-total"><span class="total-label">Grand Total:</span><span class="total-value">₹%.2f</span></div>
		<div class="total-row"><span class="total-label">Paid Amount:</span><span class="total-value">₹%.2f</span></div>
	</div>
	<div class="footer">
		<div class="section-title">Notes</div>
		<div class="terms">%s</div>
	</div>
	<script>window.onload = function() { window.print(); };</script>
</body>
</html>`,
		bill.BillNumber,
		bill.BillNumber,
		bill.Status,
		strings.ToUpper(bill.Status),
		bill.Party.Name,
		bill.Party.Address,
		fmt.Sprintf("%s, %s - %s", bill.Party.City, bill.Party.State, bill.Party.Pincode),
		bill.Party.GSTIN,
		bill.BillDate.Format("02-01-2006"),
		func() string {
			if bill.DueDate != nil {
				return bill.DueDate.Format("02-01-2006")
			}
			return "N/A"
		}(),
		strings.ToUpper(bill.Status),
		bill.BalanceDue,
		func() string {
			var rows string
			for _, item := range bill.Items {
				rows += fmt.Sprintf(`<tr>
					<td>%s</td>
					<td>%.2f</td>
					<td>%s</td>
					<td>₹%.2f</td>
					<td>%.2f%%</td>
					<td>₹%.2f</td>
				</tr>`, item.Description, item.Quantity, item.Unit, item.UnitPrice, item.TaxRate, item.Total)
			}
			return rows
		}(),
		bill.SubTotal,
		bill.TaxTotal,
		bill.TotalAmount,
		bill.PaidAmount,
		bill.Notes,
	)

	c.Header("Content-Type", "text/html")
	c.String(http.StatusOK, html)
}

func DownloadPurchaseBillPDF(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var bill models.PurchaseBill
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).
		Preload("Party").
		Preload("Items").
		First(&bill).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Purchase bill not found"})
		return
	}

	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.AddPage()
	pdf.SetAutoPageBreak(true, 15)

	// Header
	pdf.SetFont("Arial", "B", 20)
	pdf.SetTextColor(37, 99, 235)
	pdf.Cell(0, 10, "PURCHASE INVOICE")
	pdf.Ln(8)
	pdf.SetFont("Arial", "", 12)
	pdf.SetTextColor(100, 100, 100)
	pdf.Cell(0, 6, sanitizePDFText(bill.BillNumber))
	pdf.Ln(12)

	// Status
	pdf.SetFont("Arial", "B", 10)
	statusColors := map[string][3]int{
		"paid":    {6, 95, 70},
		"unpaid":  {153, 27, 27},
		"partial": {146, 64, 14},
	}
	color := statusColors[bill.Status]
	if color[0] == 0 && color[1] == 0 && color[2] == 0 {
		color = statusColors["unpaid"]
	}
	pdf.SetTextColor(color[0], color[1], color[2])
	pdf.Cell(0, 6, strings.ToUpper(bill.Status))
	pdf.Ln(10)

	// Vendor Section
	pdf.SetDrawColor(200, 200, 200)
	pdf.SetFillColor(243, 244, 246)
	pdf.Rect(10, pdf.GetY(), 190, 35, "DF")
	pdf.SetXY(14, pdf.GetY()+4)
	pdf.SetFont("Arial", "B", 11)
	pdf.SetTextColor(80, 80, 80)
	pdf.Cell(0, 6, "Vendor")
	pdf.Ln(6)
	pdf.SetX(14)
	pdf.SetFont("Arial", "B", 11)
	pdf.SetTextColor(30, 30, 30)
	pdf.Cell(0, 6, sanitizePDFText(bill.Party.Name))
	pdf.Ln(5)
	pdf.SetX(14)
	pdf.SetFont("Arial", "", 10)
	pdf.SetTextColor(80, 80, 80)
	if bill.Party.Address != "" {
		pdf.Cell(0, 5, sanitizePDFText(bill.Party.Address))
		pdf.Ln(5)
		pdf.SetX(14)
	}
	cityState := fmt.Sprintf("%s, %s - %s", bill.Party.City, bill.Party.State, bill.Party.Pincode)
	pdf.Cell(0, 5, sanitizePDFText(cityState))
	pdf.Ln(5)
	pdf.SetX(14)
	pdf.Cell(0, 5, "GSTIN: "+sanitizePDFText(bill.Party.GSTIN))
	pdf.Ln(20)

	// Invoice Details
	pdf.SetFont("Arial", "B", 11)
	pdf.SetTextColor(80, 80, 80)
	pdf.Cell(0, 6, "Invoice Details")
	pdf.Ln(8)
	pdf.SetFont("Arial", "", 10)
	pdf.SetTextColor(60, 60, 60)
	pdf.Cell(60, 6, "Bill Date:")
	pdf.Cell(0, 6, bill.BillDate.Format("02-01-2006"))
	pdf.Ln(6)
	dueDate := "N/A"
	if bill.DueDate != nil {
		dueDate = bill.DueDate.Format("02-01-2006")
	}
	pdf.Cell(60, 6, "Due Date:")
	pdf.Cell(0, 6, dueDate)
	pdf.Ln(6)
	pdf.Cell(60, 6, "Status:")
	pdf.Cell(0, 6, strings.ToUpper(bill.Status))
	pdf.Ln(6)
	pdf.Cell(60, 6, "Balance Due:")
	pdf.Cell(0, 6, fmt.Sprintf("Rs. %.2f", bill.BalanceDue))
	pdf.Ln(12)

	// Items Table
	pdf.SetFont("Arial", "B", 11)
	pdf.SetTextColor(80, 80, 80)
	pdf.Cell(0, 6, "Items")
	pdf.Ln(8)

	// Table header
	pdf.SetFillColor(243, 244, 246)
	pdf.SetDrawColor(200, 200, 200)
	pdf.SetFont("Arial", "B", 9)
	pdf.SetTextColor(50, 50, 50)
	headerY := pdf.GetY()
	pdf.Rect(10, headerY, 190, 8, "DF")
	pdf.SetXY(10, headerY+2)
	pdf.Cell(70, 4, "Description")
	pdf.Cell(20, 4, "Qty")
	pdf.Cell(20, 4, "Unit")
	pdf.Cell(25, 4, "Unit Price")
	pdf.Cell(25, 4, "Tax %")
	pdf.Cell(30, 4, "Total")
	pdf.Ln(8)

	// Table rows
	pdf.SetFont("Arial", "", 9)
	pdf.SetTextColor(60, 60, 60)
	for _, item := range bill.Items {
		rowY := pdf.GetY()
		pdf.Rect(10, rowY, 190, 7, "D")
		pdf.SetXY(10, rowY+2)
		pdf.Cell(70, 3, sanitizePDFText(item.Description))
		pdf.Cell(20, 3, fmt.Sprintf("%.2f", item.Quantity))
		pdf.Cell(20, 3, sanitizePDFText(item.Unit))
		pdf.Cell(25, 3, fmt.Sprintf("Rs. %.2f", item.UnitPrice))
		pdf.Cell(25, 3, fmt.Sprintf("%.2f%%", item.TaxRate))
		pdf.Cell(30, 3, fmt.Sprintf("Rs. %.2f", item.Total))
		pdf.Ln(7)
	}
	pdf.Ln(5)

	// Totals
	pdf.SetFont("Arial", "", 10)
	pdf.SetTextColor(80, 80, 80)
	pdf.Cell(140, 6, "")
	pdf.Cell(30, 6, "Sub Total:")
	pdf.Cell(0, 6, fmt.Sprintf("Rs. %.2f", bill.SubTotal))
	pdf.Ln(6)
	pdf.Cell(140, 6, "")
	pdf.Cell(30, 6, "Tax Total:")
	pdf.Cell(0, 6, fmt.Sprintf("Rs. %.2f", bill.TaxTotal))
	pdf.Ln(6)
	pdf.SetFont("Arial", "B", 12)
	pdf.SetTextColor(37, 99, 235)
	pdf.Cell(140, 8, "")
	pdf.Cell(30, 8, "Grand Total:")
	pdf.Cell(0, 8, fmt.Sprintf("Rs. %.2f", bill.TotalAmount))
	pdf.Ln(8)
	pdf.SetFont("Arial", "", 10)
	pdf.SetTextColor(80, 80, 80)
	pdf.Cell(140, 6, "")
	pdf.Cell(30, 6, "Paid Amount:")
	pdf.Cell(0, 6, fmt.Sprintf("Rs. %.2f", bill.PaidAmount))
	pdf.Ln(12)

	// Notes
	if bill.Notes != "" {
		pdf.SetFont("Arial", "B", 10)
		pdf.SetTextColor(80, 80, 80)
		pdf.Cell(0, 6, "Notes")
		pdf.Ln(6)
		pdf.SetFont("Arial", "", 9)
		pdf.SetTextColor(100, 100, 100)
		pdf.MultiCell(190, 5, sanitizePDFText(bill.Notes), "", "L", false)
	}

	// Footer
	pdf.SetY(-20)
	pdf.SetFont("Arial", "I", 8)
	pdf.SetTextColor(150, 150, 150)
	pdf.Cell(0, 10, fmt.Sprintf("Generated on %s", time.Now().Format("02-01-2006 03:04 PM")))

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate purchase invoice PDF"})
		return
	}

	c.Header("Content-Type", "application/pdf")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"Purchase_Invoice_%s.pdf\"", bill.BillNumber))
	c.Data(http.StatusOK, "application/pdf", buf.Bytes())
}

type LabelConfig struct {
	PaperSize     string  `json:"paper_size"` // a4, letter, a5, 1inch, 1.5inch, 2inch, 3inch
	SheetPreset   string  `json:"sheet_preset"`
	LabelWidth    float64 `json:"label_width"` // mm
	LabelHeight   float64 `json:"label_height"`
	Cols          int     `json:"cols"`
	Rows          int     `json:"rows"`
	Margin        float64 `json:"margin"` // mm (even on all sides)
	MarginTop     float64 `json:"margin_top"`
	MarginLeft    float64 `json:"margin_left"`
	GapH          float64 `json:"gap_h"`
	GapV          float64 `json:"gap_v"`
	StartPosition int     `json:"start_position"`
}

func isThermalLabelPaperSize(size string) bool {
	switch size {
	case "1inch", "1.5inch", "2inch", "3inch":
		return true
	default:
		return false
	}
}

type LabelRequest struct {
	BillID         string             `json:"bill_id" binding:"required"`
	ItemQuantities map[string]float64 `json:"item_quantities"` // item_id -> quantity (0 skips item; default to invoice quantity if omitted)
	Config         LabelConfig        `json:"config"`
	// Format: "html" (default) or "json" for silent desktop ESC/POS printing.
	Format string `json:"format"`
	// Preview: return on-screen HTML preview (A4 sheet layout) without printing.
	Preview bool `json:"preview"`
}

func PrintPurchaseBillLabels(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var req LabelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var bill models.PurchaseBill
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, req.BillID).Preload("Items").First(&bill).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Bill not found"})
		return
	}

	// Use the live product item code so generated/updated barcodes print and scan.
	for i := range bill.Items {
		bill.Items[i].ItemCode = resolvePurchaseLabelBarcode(bill.Items[i], userID)
		enrichPurchaseItemLabelPrices(&bill.Items[i], userID)
	}

	// Set default config if not provided
	config := req.Config
	if config.PaperSize == "" {
		config.PaperSize = "a4"
	}
	if isThermalLabelPaperSize(config.PaperSize) {
		size := getBarcodeLabelSize(config.PaperSize)
		config.LabelWidth = size.WidthMM
		config.LabelHeight = size.HeightMM
		config.Cols = 1
		config.Rows = 1
		config.Margin = 0
		config.MarginTop = 0
		config.MarginLeft = 0
	} else {
		if preset, ok := a4LabelSheetPresetByKey(config.SheetPreset); ok {
			config.PaperSize = strings.ToLower(preset.PaperSize)
			config.LabelWidth = preset.LabelWidthMM
			config.LabelHeight = preset.LabelHeightMM
			config.Cols = preset.Columns
			config.Rows = preset.Rows
			config.MarginTop = preset.MarginTopMM
			config.MarginLeft = preset.MarginLeftMM
			config.Margin = preset.MarginLeftMM
			config.GapH = preset.GapHMM
			config.GapV = preset.GapVMM
		}
		if config.LabelWidth == 0 {
			config.LabelWidth = 48.5
		}
		if config.LabelHeight == 0 {
			config.LabelHeight = 25.4
		}
		if config.Cols == 0 {
			config.Cols = 4
		}
		if config.Rows == 0 {
			config.Rows = 11
		}
		if config.Margin > 0 {
			if config.MarginTop == 0 {
				config.MarginTop = config.Margin
			}
			if config.MarginLeft == 0 {
				config.MarginLeft = config.Margin
			}
		}
		if config.MarginTop == 0 {
			config.MarginTop = 8.8
		}
		if config.MarginLeft == 0 {
			config.MarginLeft = 5
		}
		if config.SheetPreset == "" && config.GapH == 0 {
			config.GapH = 2
		}
	}

	items := collectPurchaseLabelItems(bill, req.ItemQuantities)
	if len(items) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No items available to print labels"})
		return
	}

	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format == "" {
		format = strings.ToLower(strings.TrimSpace(c.Query("format")))
	}
	if req.Preview {
		html := generateLabelsHTML(bill, req.ItemQuantities, config, true)
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusOK, html)
		return
	}
	if format == "json" || (isThermalLabelPaperSize(config.PaperSize) && wantsJSONResponse(c)) {
		size := getBarcodeLabelSize(config.PaperSize)
		compact := config.PaperSize == "1inch" || config.PaperSize == "1.5inch"
		payload := BarcodeLabelsResponse{
			Title:    "Labels - " + bill.BillNumber,
			Size:     size.Key,
			WidthMM:  size.WidthMM,
			HeightMM: size.HeightMM,
			Compact:  compact,
			Labels:   purchaseItemsToBarcodeLabels(items, compact),
		}
		c.JSON(http.StatusOK, payload)
		return
	}

	// Generate labels HTML
	html := generateLabelsHTML(bill, req.ItemQuantities, config, false)

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, html)
}

func wantsJSONResponse(c *gin.Context) bool {
	accept := strings.ToLower(c.GetHeader("Accept"))
	return strings.Contains(accept, "application/json")
}

func resolvePurchaseLabelBarcode(item models.PurchaseBillItem, userID uuid.UUID) string {
	if item.ProductID != nil {
		var product models.Product
		if err := utils.DB.Select("item_code", "sku").
			Where("user_id = ? AND id = ?", userID, *item.ProductID).
			First(&product).Error; err == nil {
			if code := strings.TrimSpace(product.ItemCode); code != "" {
				return code
			}
			if code := strings.TrimSpace(product.SKU); code != "" && strings.TrimSpace(item.ItemCode) == "" {
				return code
			}
		}
	}
	if code := strings.TrimSpace(item.ItemCode); code != "" {
		return code
	}

	var stock models.StockEntry
	var err error
	if item.ProductID != nil {
		err = utils.DB.Where("product_id = ? AND user_id = ? AND item_code != ''", item.ProductID, userID).
			Order("created_at DESC").First(&stock).Error
	} else {
		err = fmt.Errorf("no product_id")
	}
	if err != nil && item.Description != "" {
		err = utils.DB.Where("item_name = ? AND user_id = ? AND item_code != ''", item.Description, userID).
			Order("created_at DESC").First(&stock).Error
	}
	if err != nil && item.BatchNo != "" {
		err = utils.DB.Where("batch_no = ? AND user_id = ? AND item_code != ''", item.BatchNo, userID).
			Order("created_at DESC").First(&stock).Error
	}
	if err == nil {
		if code := strings.TrimSpace(stock.ItemCode); code != "" {
			return code
		}
	}

	if item.ProductID != nil {
		var product models.Product
		if err := utils.DB.Select("sku").
			Where("user_id = ? AND id = ?", userID, *item.ProductID).
			First(&product).Error; err == nil {
			if code := strings.TrimSpace(product.SKU); code != "" {
				return code
			}
		}
	}
	return "0000000000"
}

// enrichPurchaseItemLabelPrices fills missing MRP / sale price from the product catalog.
// UnitPrice is purchase cost and must never be used as MRP on labels.
func enrichPurchaseItemLabelPrices(item *models.PurchaseBillItem, userID uuid.UUID) {
	if item == nil {
		return
	}
	if item.MRP > 0 && item.SalePrice > 0 {
		return
	}

	var product models.Product
	var err error
	if item.ProductID != nil {
		err = utils.DB.Select("id", "mrp", "sale_price").
			Where("user_id = ? AND id = ?", userID, *item.ProductID).
			First(&product).Error
	}
	if err != nil && strings.TrimSpace(item.ItemCode) != "" {
		err = utils.DB.Select("id", "mrp", "sale_price").
			Where("user_id = ? AND (item_code = ? OR sku = ?)", userID, item.ItemCode, item.ItemCode).
			First(&product).Error
	}
	if err != nil && strings.TrimSpace(item.Description) != "" {
		err = utils.DB.Select("id", "mrp", "sale_price").
			Where("user_id = ? AND name = ?", userID, item.Description).
			First(&product).Error
	}
	if err != nil {
		return
	}
	if item.MRP <= 0 && product.MRP > 0 {
		item.MRP = product.MRP
	}
	if item.SalePrice <= 0 && product.SalePrice > 0 {
		item.SalePrice = product.SalePrice
	}
}

func purchaseItemsToBarcodeLabels(items []models.PurchaseBillItem, _ bool) []BarcodeLabelItemJSON {
	out := make([]BarcodeLabelItemJSON, 0, len(items))
	for _, item := range items {
		barcodeVal := strings.TrimSpace(item.ItemCode)
		if barcodeVal == "" {
			barcodeVal = "0000000000"
		}
		entry := BarcodeLabelItemJSON{
			Name:    item.Description,
			Barcode: barcodeVal,
			Price:   item.SalePrice,
		}
		if item.MRP > 0 {
			entry.MRP = item.MRP
		}
		out = append(out, entry)
	}
	return out
}

func collectPurchaseLabelItems(bill models.PurchaseBill, itemQuantities map[string]float64) []models.PurchaseBillItem {
	var items []models.PurchaseBillItem
	for _, item := range bill.Items {
		qtyFloat, ok := itemQuantities[item.ID.String()]
		if !ok {
			qtyFloat = item.Quantity
		}
		quantity := int(qtyFloat + 0.5) // round
		if quantity < 0 {
			quantity = 0
		}
		if quantity > 500 {
			quantity = 500
		}
		if quantity == 0 {
			continue
		}
		for i := 0; i < quantity; i++ {
			items = append(items, item)
		}
	}
	return items
}

func labelConfigToA4Layout(config LabelConfig) A4LabelSheetLayout {
	paper := strings.ToUpper(strings.TrimSpace(config.PaperSize))
	if paper == "" {
		paper = "A4"
	}
	return A4LabelSheetLayout{
		PaperSize:      paper,
		LabelWidthMM:   config.LabelWidth,
		LabelHeightMM:  config.LabelHeight,
		Columns:        config.Cols,
		Rows:           config.Rows,
		MarginTopMM:    config.MarginTop,
		MarginBottomMM: config.MarginTop,
		MarginLeftMM:   config.MarginLeft,
		MarginRightMM:  config.MarginLeft,
		GapHMM:         config.GapH,
		GapVMM:         config.GapV,
	}
}

func generateLabelsHTML(bill models.PurchaseBill, itemQuantities map[string]float64, config LabelConfig, screenPreview bool) string {
	items := collectPurchaseLabelItems(bill, itemQuantities)
	if isThermalLabelPaperSize(config.PaperSize) {
		return generateThermalPurchaseLabelsHTML(bill.BillNumber, items, config.PaperSize)
	}

	layout := labelConfigToA4Layout(config)
	a4Size := barcodeLabelSizeForA4Layout(layout)
	labelHTMLs := make([]string, 0, len(items))
	for _, item := range items {
		barcodeVal := strings.TrimSpace(item.ItemCode)
		if barcodeVal == "" {
			barcodeVal = "0000000000"
		}
		labelHTMLs = append(labelHTMLs, buildProductLabelHTML(productLabelData{
			Name:      item.Description,
			SKU:       item.ItemCode,
			ItemCode:  barcodeVal,
			SalePrice: item.SalePrice,
			MRP:       item.MRP,
		}, a4Size, false))
	}

	return buildA4LabelsSheetDocument("Labels - "+bill.BillNumber, labelHTMLs, layout, config.StartPosition, screenPreview)
}

func generateThermalPurchaseLabelsHTML(billNumber string, items []models.PurchaseBillItem, paperSize string) string {
	size := getBarcodeLabelSize(paperSize)
	compact := paperSize == "1inch" || paperSize == "1.5inch"

	var labelsHTML strings.Builder
	for _, item := range items {
		labelsHTML.WriteString(generateThermalPurchaseLabel(item, size, compact))
	}

	return wrapBarcodeLabelDocument("Labels - "+billNumber, barcodeLabelPageCSS(size), labelsHTML.String())
}

func generateThermalPurchaseLabel(item models.PurchaseBillItem, size BarcodeLabelSize, compact bool) string {
	_ = compact
	barcodeVal := strings.TrimSpace(item.ItemCode)
	if barcodeVal == "" {
		barcodeVal = "0000000000"
	}

	name := html.EscapeString(item.Description)
	mrpCell := ""
	if item.MRP > 0 {
		mrpCell = fmt.Sprintf(`<span class="product-mrp">MRP: ₹%.2f</span>`, item.MRP)
	}

	return fmt.Sprintf(`<div class="label">
	<div class="product-name">%s</div>
	<div class="product-barcode">
		%s
	</div>
	<div class="price-row">
		%s
		<span class="product-price">₹%.2f</span>
	</div>
</div>`, name,
		barcodeImageHTML(barcodeVal, size.BarcodeW, size.BarcodeH, size.MetaFontPx),
		mrpCell, item.SalePrice)
}

// ParseBillWithAI uses Gemini to parse purchase bill/invoice from image
func ParseBillWithAI(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	fmt.Println("[ParseBillWithAI] Received request from user:", userID)

	// Get business settings to check if AI is enabled and get API key
	var business models.Business
	if err := utils.DB.Where("user_id = ?", userID).First(&business).Error; err != nil {
		fmt.Println("[ParseBillWithAI] ERROR: Business settings not found:", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Business settings not found"})
		return
	}
	fmt.Println("[ParseBillWithAI] Business found. EnableAIBillParsing:", business.EnableAIBillParsing, "APIKey present:", business.GeminiAPIKey != "")

	if !business.EnableAIBillParsing {
		fmt.Println("[ParseBillWithAI] ERROR: AI bill parsing not enabled")
		c.JSON(http.StatusBadRequest, gin.H{"error": "AI bill parsing is not enabled. Please enable it in business settings."})
		return
	}

	if business.GeminiAPIKey == "" {
		fmt.Println("[ParseBillWithAI] ERROR: Gemini API key not configured")
		c.JSON(http.StatusBadRequest, gin.H{"error": "Gemini API key not configured. Please add it in business settings."})
		return
	}

	apiKey := business.GeminiAPIKey

	// Get uploaded file
	file, err := c.FormFile("image")
	if err != nil {
		fmt.Println("[ParseBillWithAI] ERROR: No image file provided:", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "No image file provided"})
		return
	}
	fmt.Println("[ParseBillWithAI] File received:", file.Filename, "size:", file.Size, "header:", file.Header)

	// Open the file
	fileContent, err := file.Open()
	if err != nil {
		fmt.Println("[ParseBillWithAI] ERROR: Failed to open file:", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to open file"})
		return
	}
	defer fileContent.Close()

	// Read file content
	imageBytes, err := io.ReadAll(fileContent)
	if err != nil {
		fmt.Println("[ParseBillWithAI] ERROR: Failed to read file:", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read file"})
		return
	}
	fmt.Println("[ParseBillWithAI] File read successfully. Bytes:", len(imageBytes))

	// Convert to base64
	base64Image := base64.StdEncoding.EncodeToString(imageBytes)

	// Determine MIME type
	mimeType := "image/jpeg"
	if strings.HasSuffix(strings.ToLower(file.Filename), ".png") {
		mimeType = "image/png"
	} else if strings.HasSuffix(strings.ToLower(file.Filename), ".webp") {
		mimeType = "image/webp"
	} else if strings.HasSuffix(strings.ToLower(file.Filename), ".pdf") {
		mimeType = "application/pdf"
	}

	// Call Gemini API with the image
	prompt := `You are an expert at extracting data from purchase bills and invoices. Analyze this document and extract all the information.

IMPORTANT: Return ONLY a valid JSON object. No markdown, no explanations, no extra text.

Extract the following fields:
- vendor_name: Name of the vendor/supplier
- vendor_gstin: GSTIN of the vendor (if present)
- bill_number: Invoice/bill number
- bill_date: Date of the bill (format: YYYY-MM-DD)
- due_date: Due date (format: YYYY-MM-DD, if present)
- items: Array of items with:
  - description: Item description
  - quantity: Quantity
  - unit: Unit (e.g., pcs, kg, liters)
  - unit_price: Unit price
  - hsn_code: HSN code (if present)
  - tax_rate: Tax rate percentage
- subtotal: Subtotal amount
- tax_total: Total tax amount
- total_amount: Grand total
- notes: Any notes or terms (if present)

Return this exact JSON format:
{
  "status": "success",
  "data": {
    "vendor_name": "",
    "vendor_gstin": "",
    "bill_number": "",
    "bill_date": "",
    "due_date": "",
    "items": [
      {
        "description": "",
        "quantity": 0,
        "unit": "",
        "unit_price": 0,
        "hsn_code": "",
        "tax_rate": 0
      }
    ],
    "subtotal": 0,
    "tax_total": 0,
    "total_amount": 0,
    "notes": ""
  }
}

If you cannot extract the data or there's an error, return:
{
  "status": "error",
  "error": "Error message describing why you couldn't extract the data"
}

Return ONLY the JSON, nothing else.`

	requestBody := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"parts": []map[string]interface{}{
					{
						"text": prompt,
					},
					{
						"inline_data": map[string]interface{}{
							"mime_type": mimeType,
							"data":      base64Image,
						},
					},
				},
			},
		},
	}

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create request"})
		return
	}

	req, err := http.NewRequest("POST", "https://generativelanguage.googleapis.com/v1/models/gemini-2.5-flash:generateContent?key="+apiKey, bytes.NewBuffer(jsonBody))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create request"})
		return
	}

	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	fmt.Println("[ParseBillWithAI] Calling Gemini API...")
	resp, err := client.Do(req)
	if err != nil {
		fmt.Println("[ParseBillWithAI] ERROR: Failed to call Gemini API:", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to call Gemini API"})
		return
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	fmt.Println("[ParseBillWithAI] Gemini API status:", resp.StatusCode, "response:", string(bodyBytes))

	if resp.StatusCode != http.StatusOK {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Gemini API error: %s", string(bodyBytes))})
		return
	}

	// Re-create reader for decoder since we already consumed the body
	resp.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

	var geminiResponse struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&geminiResponse); err != nil {
		fmt.Println("[ParseBillWithAI] ERROR: Failed to decode Gemini response:", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse Gemini response"})
		return
	}
	fmt.Println("[ParseBillWithAI] Gemini candidates:", len(geminiResponse.Candidates))

	if len(geminiResponse.Candidates) == 0 || len(geminiResponse.Candidates[0].Content.Parts) == 0 {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "No response from Gemini"})
		return
	}

	// Extract text from response
	text := strings.TrimSpace(geminiResponse.Candidates[0].Content.Parts[0].Text)
	fmt.Println("[ParseBillWithAI] Raw Gemini text length:", len(text))

	// Try to extract JSON from the text (in case AI added markdown)
	text = extractJSON(text)
	fmt.Println("[ParseBillWithAI] Extracted JSON length:", len(text))

	// Parse the JSON response from Gemini
	var aiResponse struct {
		Status string `json:"status"`
		Data   struct {
			VendorName  string `json:"vendor_name"`
			VendorGSTIN string `json:"vendor_gstin"`
			BillNumber  string `json:"bill_number"`
			BillDate    string `json:"bill_date"`
			DueDate     string `json:"due_date"`
			Items       []struct {
				Description string  `json:"description"`
				Quantity    float64 `json:"quantity"`
				Unit        string  `json:"unit"`
				UnitPrice   float64 `json:"unit_price"`
				HSNCode     string  `json:"hsn_code"`
				TaxRate     float64 `json:"tax_rate"`
			} `json:"items"`
			SubTotal    float64 `json:"subtotal"`
			TaxTotal    float64 `json:"tax_total"`
			TotalAmount float64 `json:"total_amount"`
			Notes       string  `json:"notes"`
		} `json:"data"`
		Error string `json:"error"`
	}

	if err := json.Unmarshal([]byte(text), &aiResponse); err != nil {
		fmt.Println("[ParseBillWithAI] ERROR: JSON unmarshal failed:", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse Gemini result as JSON", "raw_response": text})
		return
	}
	fmt.Println("[ParseBillWithAI] AI response status:", aiResponse.Status)

	// Check if AI returned an error
	if aiResponse.Status == "error" {
		c.JSON(http.StatusBadRequest, gin.H{"error": aiResponse.Error})
		return
	}

	// Return the parsed data for preview
	c.JSON(http.StatusOK, gin.H{
		"status": "success",
		"data":   aiResponse.Data,
	})
}
