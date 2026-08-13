package controllers

import (
	"truerp/models"
	"truerp/utils"
	"encoding/csv"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tealeg/xlsx/v3"
)

const itemCodeMaxLen = 14

func isWeightBasedUnit(unit string) bool {
	switch strings.ToUpper(strings.TrimSpace(unit)) {
	case "KG", "GM", "G", "GRAM", "KGS", "KILOGRAM", "KILOGRAMS":
		return true
	default:
		return false
	}
}

func normalizeProductGst(product *models.Product) {
	if !product.GstEnabled {
		product.TaxRate = 0
	}
}

func itemCodeFieldError(itemCode string) string {
	code := strings.TrimSpace(itemCode)
	if code == "" {
		return ""
	}
	if len(code) > itemCodeMaxLen {
		return fmt.Sprintf("Item code must be at most %d characters", itemCodeMaxLen)
	}
	return ""
}

func pluValidationError(userID uuid.UUID, plu string, excludeID *uuid.UUID, required bool) string {
	return utils.ValidateProductPLU(userID, plu, excludeID, required)
}

func pluConflictProduct(userID uuid.UUID, plu string, excludeID *uuid.UUID) (*models.Product, bool) {
	code := strings.TrimSpace(plu)
	if code == "" {
		return nil, false
	}
	query := utils.DB.Where("user_id = ? AND TRIM(plu) = ?", userID, code)
	if excludeID != nil {
		query = query.Where("id <> ?", *excludeID)
	}
	var existing models.Product
	if err := query.First(&existing).Error; err != nil {
		return nil, false
	}
	return &existing, true
}

func GetProducts(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var products []models.Product
	query := utils.DB.Where("user_id = ?", userID)

	if category := c.Query("category"); category != "" && category != "all" {
		query = query.Where("category = ?", category)
	}

	if search := c.Query("search"); search != "" {
		query = query.Where("name LIKE ? OR sku LIKE ? OR item_code LIKE ? OR plu LIKE ?", "%"+search+"%", "%"+search+"%", "%"+search+"%", "%"+search+"%")
	}

	if err := query.Order("created_at DESC").Find(&products).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch products"})
		return
	}

	stockByProduct := map[uuid.UUID]float64{}
	if len(products) > 0 {
		var stocks []models.InventoryStock
		if err := utils.DB.Where("user_id = ?", userID).Find(&stocks).Error; err == nil {
			for _, stock := range stocks {
				stockByProduct[stock.ProductID] += stock.AvailableQty
			}
		}
	}

	type productWithStock struct {
		models.Product
		StockQty float64 `json:"stock_qty"`
	}
	out := make([]productWithStock, 0, len(products))
	for _, p := range products {
		out = append(out, productWithStock{
			Product:  p,
			StockQty: stockByProduct[p.ID],
		})
	}

	c.JSON(http.StatusOK, out)
}

func GetProduct(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var product models.Product
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&product).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Product not found"})
		return
	}

	c.JSON(http.StatusOK, product)
}

func CreateProduct(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		models.Product
		Inventory *struct {
			OutletID  string  `json:"outlet_id"`
			Quantity  float64 `json:"quantity"`
			CostPrice float64 `json:"cost_price"`
			BatchNo   string  `json:"batch_no"`
			MfgDate   string  `json:"mfg_date"`
			ExpDate   string  `json:"exp_date"`
		} `json:"inventory"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		fmt.Printf("[DEBUG] CreateProduct - JSON bind error: %v\n", err)
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  "Invalid product data. Please check the form and try again.",
			"fields": mapBindingErrorToFields(err),
		})
		return
	}

	fmt.Printf("[DEBUG] CreateProduct - Input: %+v\n", input.Product)

	if strings.TrimSpace(input.Product.Name) == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  "Product name is required",
			"fields": gin.H{"name": "Product name is required"},
		})
		return
	}

	if msg := itemCodeFieldError(input.Product.ItemCode); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  msg,
			"fields": gin.H{"item_code": msg},
		})
		return
	}

	input.Product.PLU = strings.TrimSpace(input.Product.PLU)
	if err := utils.AssignProductPLU(userID, &input.Product); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to assign PLU. Please try again."})
		return
	}
	if msg := pluValidationError(userID, input.Product.PLU, nil, true); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  msg,
			"fields": gin.H{"plu": msg},
		})
		return
	}
	if existing, conflict := pluConflictProduct(userID, input.Product.PLU, nil); conflict {
		c.JSON(http.StatusConflict, gin.H{
			"error":  fmt.Sprintf("PLU already used by %s", existing.Name),
			"fields": gin.H{"plu": fmt.Sprintf("Already assigned to %s", existing.Name)},
		})
		return
	}

	itemCode := strings.TrimSpace(input.Product.ItemCode)
	if itemCode != "" {
		var existingProduct models.Product
		if err := utils.DB.Where("user_id = ? AND TRIM(item_code) = ?", userID, itemCode).First(&existingProduct).Error; err == nil {
			c.JSON(http.StatusConflict, gin.H{
				"error":  fmt.Sprintf("Item code already used by %s", existingProduct.Name),
				"fields": gin.H{"item_code": fmt.Sprintf("Already assigned to %s", existingProduct.Name)},
			})
			return
		}
	}

	input.Product.ID = uuid.New()
	input.Product.UserID = userID
	input.Product.Category = utils.ResolveCategoryName(input.Product.Category)
	_ = utils.EnsureDefaultCategories(utils.DB, userID)

	// Auto-populate tax rate from HSN code if not provided
	if input.Product.GstEnabled && input.Product.HSNCode != "" && input.Product.TaxRate == 0 {
		if taxRate := getTaxRateFromHSN(input.Product.HSNCode); taxRate > 0 {
			input.Product.TaxRate = taxRate
			fmt.Printf("[DEBUG] CreateProduct - Auto-populated tax rate from HSN: %s -> %.2f%%\n", input.Product.HSNCode, taxRate)
		}
	}

	normalizeProductGst(&input.Product)

	input.Product.SKU = strings.TrimSpace(input.Product.SKU)
	if input.Product.SKU == "" {
		input.Product.SKU = utils.GenerateUniqueProductSKU(input.Product.Name)
	} else {
		var existingProduct models.Product
		if err := utils.DB.Where("sku = ?", input.Product.SKU).First(&existingProduct).Error; err == nil {
			// Name-derived SKU already taken — append random digits instead of failing
			if input.Product.SKU == utils.SKUFromName(input.Product.Name) {
				input.Product.SKU = utils.GenerateUniqueProductSKU(input.Product.Name)
			} else {
				c.JSON(http.StatusConflict, gin.H{
					"error":  "A product with this SKU already exists",
					"fields": gin.H{"sku": "This SKU is already in use"},
				})
				return
			}
		}
	}

	// Validate inventory outlet before creating the product
	var inventoryOutletID uuid.UUID
	if input.Inventory != nil && input.Inventory.Quantity > 0 && strings.TrimSpace(input.Inventory.OutletID) != "" {
		parsed, err := uuid.Parse(input.Inventory.OutletID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":  "Please select a valid warehouse",
				"fields": gin.H{"inventory.outlet_id": "Please select a valid warehouse"},
			})
			return
		}
		inventoryOutletID = parsed
	}

	if input.Product.EnableBatching && input.Inventory != nil && input.Inventory.Quantity > 0 && strings.TrimSpace(input.Inventory.BatchNo) == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  "Batch number is required when batching is enabled",
			"fields": gin.H{"inventory.batch_no": "Batch number is required"},
		})
		return
	}

	if err := utils.DB.Create(&input.Product).Error; err != nil {
		fmt.Printf("[DEBUG] CreateProduct - DB create error: %v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create product. Please try again."})
		return
	}

	// Create inventory stock entry if inventory data is provided
	if input.Inventory != nil && input.Inventory.Quantity > 0 {
		outletID := inventoryOutletID
		if outletID == uuid.Nil {
			// Try to get default warehouse
			var defaultWarehouse models.Warehouse
			if err := utils.DB.Where("user_id = ? AND is_default = ?", userID, true).First(&defaultWarehouse).Error; err == nil {
				outletID = defaultWarehouse.ID
			} else {
				// Create default warehouse if none exists
				defaultWarehouse = models.Warehouse{
					ID:        uuid.New(),
					UserID:    userID,
					Name:      "Default Warehouse",
					Code:      "DEFAULT",
					IsDefault: true,
					IsActive:  true,
				}
				utils.DB.Create(&defaultWarehouse)
				outletID = defaultWarehouse.ID
			}
		}

		// Create stock entry
		var mfgDate, expDate *time.Time
		if input.Inventory.MfgDate != "" {
			if t, err := time.Parse("2006-01-02", input.Inventory.MfgDate); err == nil {
				mfgDate = &t
			}
		}
		if input.Inventory.ExpDate != "" {
			if t, err := time.Parse("2006-01-02", input.Inventory.ExpDate); err == nil {
				expDate = &t
			}
		}

		stockEntry := models.StockEntry{
			ID:         uuid.New(),
			UserID:     userID,
			ItemName:   input.Product.Name,
			ProductID:  &input.Product.ID,
			OutletID:   outletID,
			EntryType:  "opening",
			Quantity:   input.Inventory.Quantity,
			BalanceQty: 0,
			CostPrice:  input.Inventory.CostPrice,
			BatchNo:    input.Inventory.BatchNo,
			ItemCode:   strings.TrimSpace(input.Product.ItemCode),
			MfgDate:    mfgDate,
			ExpDate:    expDate,
			EntryDate:  time.Now(),
		}

		if err := utils.DB.Create(&stockEntry).Error; err != nil {
			fmt.Printf("[DEBUG] CreateProduct - Failed to create stock entry: %v\n", err)
		} else {
			fmt.Printf("[DEBUG] CreateProduct - Stock entry created: %s\n", stockEntry.ID)

			// Update inventory stock
			updateInventoryStock(userID, input.Product.ID, outletID, "opening", input.Inventory.Quantity, input.Inventory.CostPrice, input.Inventory.BatchNo, mfgDate, expDate)
		}
	}

	fmt.Printf("[DEBUG] CreateProduct - Product created successfully: %s\n", input.Product.ID)
	c.JSON(http.StatusCreated, input.Product)
}

func UpdateProduct(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	fmt.Printf("[DEBUG] UpdateProduct - ID: %s, UserID: %s\n", id, userID)

	var input struct {
		models.Product
		Inventory *struct {
			OutletID  string  `json:"outlet_id"`
			Quantity  float64 `json:"quantity"`
			CostPrice float64 `json:"cost_price"`
			BatchNo   string  `json:"batch_no"`
			MfgDate   string  `json:"mfg_date"`
			ExpDate   string  `json:"exp_date"`
		} `json:"inventory"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		fmt.Printf("[DEBUG] UpdateProduct - JSON bind error: %v\n", err)
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  "Invalid product data. Please check the form and try again.",
			"fields": mapBindingErrorToFields(err),
		})
		return
	}

	fmt.Printf("[DEBUG] UpdateProduct - Input: %+v\n", input.Product)

	var product models.Product
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&product).Error; err != nil {
		fmt.Printf("[DEBUG] UpdateProduct - Product not found: %v\n", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "Product not found"})
		return
	}

	itemCode := product.ItemCode
	if input.Product.ItemCode != "" {
		itemCode = input.Product.ItemCode
	}
	if msg := itemCodeFieldError(itemCode); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  msg,
			"fields": gin.H{"item_code": msg},
		})
		return
	}

	plu := strings.TrimSpace(product.PLU)
	if input.Product.PLU != "" {
		plu = strings.TrimSpace(input.Product.PLU)
	}
	if plu == "" {
		next, err := utils.NextProductPLU(userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to assign PLU. Please try again."})
			return
		}
		plu = next
	}
	if msg := pluValidationError(userID, plu, &product.ID, true); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  msg,
			"fields": gin.H{"plu": msg},
		})
		return
	}
	if existing, conflict := pluConflictProduct(userID, plu, &product.ID); conflict {
		c.JSON(http.StatusConflict, gin.H{
			"error":  fmt.Sprintf("PLU already used by %s", existing.Name),
			"fields": gin.H{"plu": fmt.Sprintf("Already assigned to %s", existing.Name)},
		})
		return
	}

	// Auto-populate tax rate from HSN code if HSN is being changed and tax rate is not provided
	hsnChanged := input.Product.HSNCode != "" && input.Product.HSNCode != product.HSNCode
	if input.Product.GstEnabled && hsnChanged && input.Product.TaxRate == 0 {
		if taxRate := getTaxRateFromHSN(input.Product.HSNCode); taxRate > 0 {
			input.Product.TaxRate = taxRate
			fmt.Printf("[DEBUG] UpdateProduct - Auto-populated tax rate from HSN: %s -> %.2f%%\n", input.Product.HSNCode, taxRate)
		}
	}

	normalizeProductGst(&input.Product)

	// Build updates map with only non-zero/non-empty fields
	updates := map[string]interface{}{}
	if input.Product.Name != "" {
		updates["name"] = input.Product.Name
	}
	if input.Product.SKU != "" {
		updates["sku"] = input.Product.SKU
	}
	if input.Product.ItemCode != "" {
		updates["item_code"] = input.Product.ItemCode
	}
	updates["plu"] = plu
	if input.Product.Category != "" {
		updates["category"] = input.Product.Category
	}
	if input.Product.PurchasePrice != 0 {
		updates["purchase_price"] = input.Product.PurchasePrice
	}
	if input.Product.SalePrice != 0 {
		updates["sale_price"] = input.Product.SalePrice
	}
	if input.Product.MRP != 0 {
		updates["mrp"] = input.Product.MRP
	}
	if input.Product.Unit != "" {
		updates["unit"] = input.Product.Unit
	}
	if input.Product.MinStock != 0 {
		updates["min_stock"] = input.Product.MinStock
	}
	// tax_rate can legitimately be 0 (exempt / zero-rated goods)
	updates["tax_rate"] = input.Product.TaxRate
	updates["gst_enabled"] = input.Product.GstEnabled
	if input.Product.ItemType != "" {
		updates["item_type"] = input.Product.ItemType
	}
	updates["low_stock_alert"] = input.Product.LowStockAlert
	if input.Product.HSNCode != "" {
		updates["hsn_code"] = input.Product.HSNCode
	}
	if input.Product.Description != "" {
		updates["description"] = input.Product.Description
	}
	if input.Product.Discount != "" {
		updates["discount"] = input.Product.Discount
	}
	updates["enable_batching"] = input.Product.EnableBatching
	updates["sale_price_with_tax"] = input.Product.SalePriceWithTax
	updates["purchase_price_with_tax"] = input.Product.PurchasePriceWithTax
	if input.Product.ImageUrl != "" {
		updates["image_url"] = input.Product.ImageUrl
	}
	updates["is_active"] = input.Product.IsActive

	if err := utils.DB.Model(&product).Updates(updates).Error; err != nil {
		fmt.Printf("[DEBUG] UpdateProduct - DB update error: %v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update product"})
		return
	}

	newItemCode := strings.TrimSpace(itemCode)
	oldItemCode := strings.TrimSpace(product.ItemCode)
	if newItemCode != "" && newItemCode != oldItemCode {
		sync := utils.DB.Model(&models.StockEntry{}).Where("user_id = ? AND product_id = ?", userID, product.ID)
		if oldItemCode == "" {
			sync = sync.Where("item_code = '' OR item_code IS NULL")
		} else {
			sync = sync.Where("TRIM(item_code) = ? OR item_code = '' OR item_code IS NULL", oldItemCode)
		}
		if err := sync.Update("item_code", newItemCode).Error; err != nil {
			fmt.Printf("[DEBUG] UpdateProduct - Failed to sync stock item codes: %v\n", err)
		}
	}

	// Update inventory stock entry if inventory data is provided
	if input.Inventory != nil && input.Inventory.Quantity > 0 {
		outletID := uuid.Nil
		if strings.TrimSpace(input.Inventory.OutletID) != "" {
			parsed, err := uuid.Parse(input.Inventory.OutletID)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{
					"error":  "Please select a valid warehouse",
					"fields": gin.H{"inventory.outlet_id": "Please select a valid warehouse"},
				})
				return
			}
			outletID = parsed
		}
		if outletID == uuid.Nil {
			// Try to get default warehouse
			var defaultWarehouse models.Warehouse
			if err := utils.DB.Where("user_id = ? AND is_default = ?", userID, true).First(&defaultWarehouse).Error; err == nil {
				outletID = defaultWarehouse.ID
			} else {
				// Create default warehouse if none exists
				defaultWarehouse = models.Warehouse{
					ID:        uuid.New(),
					UserID:    userID,
					Name:      "Default Warehouse",
					Code:      "DEFAULT",
					IsDefault: true,
					IsActive:  true,
				}
				utils.DB.Create(&defaultWarehouse)
				outletID = defaultWarehouse.ID
			}
		}

		// Create stock entry for adjustment
		var mfgDate, expDate *time.Time
		if input.Inventory.MfgDate != "" {
			if t, err := time.Parse("2006-01-02", input.Inventory.MfgDate); err == nil {
				mfgDate = &t
			}
		}
		if input.Inventory.ExpDate != "" {
			if t, err := time.Parse("2006-01-02", input.Inventory.ExpDate); err == nil {
				expDate = &t
			}
		}

		stockEntry := models.StockEntry{
			ID:         uuid.New(),
			UserID:     userID,
			ItemName:   input.Product.Name,
			ProductID:  &product.ID,
			OutletID:   outletID,
			EntryType:  "adjustment",
			Quantity:   input.Inventory.Quantity,
			BalanceQty: 0,
			CostPrice:  input.Inventory.CostPrice,
			BatchNo:    input.Inventory.BatchNo,
			ItemCode:   strings.TrimSpace(itemCode),
			MfgDate:    mfgDate,
			ExpDate:    expDate,
			EntryDate:  time.Now(),
		}

		if err := utils.DB.Create(&stockEntry).Error; err != nil {
			fmt.Printf("[DEBUG] UpdateProduct - Failed to create stock entry: %v\n", err)
		} else {
			fmt.Printf("[DEBUG] UpdateProduct - Stock entry created: %s\n", stockEntry.ID)

			// Update inventory stock
			updateInventoryStock(userID, product.ID, outletID, "adjustment", input.Inventory.Quantity, input.Inventory.CostPrice, input.Inventory.BatchNo, mfgDate, expDate)
		}
	}

	fmt.Printf("[DEBUG] UpdateProduct - Product updated successfully: %s\n", id)
	c.JSON(http.StatusOK, product)
}

func DeleteProduct(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	fmt.Printf("[DEBUG] DeleteProduct - ID: %s, UserID: %s\n", id, userID)

	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Delete(&models.Product{}).Error; err != nil {
		fmt.Printf("[DEBUG] DeleteProduct - DB delete error: %v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete product"})
		return
	}

	fmt.Printf("[DEBUG] DeleteProduct - Product deleted successfully: %s\n", id)
	c.JSON(http.StatusOK, gin.H{"message": "Product deleted successfully"})
}

func ExportProductsCSV(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var products []models.Product
	query := utils.DB.Where("user_id = ?", userID)

	if category := c.Query("category"); category != "" && category != "all" {
		query = query.Where("category = ?", category)
	}

	if search := c.Query("search"); search != "" {
		query = query.Where("name LIKE ? OR sku LIKE ? OR item_code LIKE ? OR plu LIKE ?", "%"+search+"%", "%"+search+"%", "%"+search+"%", "%"+search+"%")
	}

	if err := query.Order("created_at DESC").Find(&products).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch products"})
		return
	}

	c.Header("Content-Type", "text/csv")
	c.Header("Content-Disposition", "attachment; filename=products_"+time.Now().Format("2006-01-02")+".csv")

	writer := csv.NewWriter(c.Writer)
	defer writer.Flush()

	headers := []string{
		"Name", "SKU", "Item Code", "PLU", "Category", "Stock Qty", "Unit",
		"Purchase Price", "Sale Price", "MRP", "Tax Rate %", "Discount %",
		"HSN Code", "Description", "Min Stock", "Is Service", "Low Stock Alert",
		"Enable Batching", "Sale Price With Tax", "Purchase Price With Tax",
	}
	if err := writer.Write(headers); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to write CSV"})
		return
	}

	for _, p := range products {
		record := []string{
			p.Name,
			p.SKU,
			p.ItemCode,
			p.PLU,
			p.Category,
			"", // Stock Qty (live stock lives in inventory entries)
			p.Unit,
			fmt.Sprintf("%.2f", p.PurchasePrice),
			fmt.Sprintf("%.2f", p.SalePrice),
			fmt.Sprintf("%.2f", p.MRP),
			fmt.Sprintf("%.2f", p.TaxRate),
			p.Discount,
			p.HSNCode,
			p.Description,
			fmt.Sprintf("%.2f", p.MinStock),
			p.ItemType,
			strconv.FormatBool(p.LowStockAlert),
			strconv.FormatBool(p.EnableBatching),
			strconv.FormatBool(p.SalePriceWithTax),
			strconv.FormatBool(p.PurchasePriceWithTax),
		}
		if err := writer.Write(record); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to write CSV"})
			return
		}
	}
}

func ExportProductsExcel(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var products []models.Product
	query := utils.DB.Where("user_id = ?", userID)

	if category := c.Query("category"); category != "" && category != "all" {
		query = query.Where("category = ?", category)
	}

	if search := c.Query("search"); search != "" {
		query = query.Where("name LIKE ? OR sku LIKE ? OR item_code LIKE ? OR plu LIKE ?", "%"+search+"%", "%"+search+"%", "%"+search+"%", "%"+search+"%")
	}

	if err := query.Order("created_at DESC").Find(&products).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch products"})
		return
	}

	file := xlsx.NewFile()
	sheet, err := file.AddSheet("Products")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create Excel sheet"})
		return
	}

	headers := []string{
		"Name", "SKU", "Item Code", "PLU", "Category", "Stock Qty", "Unit",
		"Purchase Price", "Sale Price", "MRP", "Tax Rate %", "Discount %",
		"HSN Code", "Description", "Min Stock", "Is Service", "Low Stock Alert",
		"Enable Batching", "Sale Price With Tax", "Purchase Price With Tax",
	}

	headerRow := sheet.AddRow()
	for _, header := range headers {
		cell := headerRow.AddCell()
		cell.Value = header
		cell.GetStyle().Font.Bold = true
	}

	for _, p := range products {
		row := sheet.AddRow()
		row.AddCell().SetValue(p.Name)
		row.AddCell().SetValue(p.SKU)
		row.AddCell().SetValue(p.ItemCode)
		row.AddCell().SetValue(p.PLU)
		row.AddCell().SetValue(p.Category)
		row.AddCell().SetValue("") // Stock Qty (live stock lives in inventory entries)
		row.AddCell().SetValue(p.Unit)
		row.AddCell().SetValue(p.PurchasePrice)
		row.AddCell().SetValue(p.SalePrice)
		row.AddCell().SetValue(p.MRP)
		row.AddCell().SetValue(p.TaxRate)
		row.AddCell().SetValue(p.Discount)
		row.AddCell().SetValue(p.HSNCode)
		row.AddCell().SetValue(p.Description)
		row.AddCell().SetValue(p.MinStock)
		row.AddCell().SetValue(p.ItemType)
		row.AddCell().SetValue(p.LowStockAlert)
		row.AddCell().SetValue(p.EnableBatching)
		row.AddCell().SetValue(p.SalePriceWithTax)
		row.AddCell().SetValue(p.PurchasePriceWithTax)
	}

	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Header("Content-Disposition", "attachment; filename=products_"+time.Now().Format("2006-01-02")+".xlsx")

	if err := file.Write(c.Writer); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to write Excel file"})
		return
	}
}

func PrintProductLabel(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var product models.Product
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&product).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Product not found"})
		return
	}

	// Fetch business settings for label printing
	var business models.Business
	if err := utils.DB.Where("user_id = ?", userID).First(&business).Error; err != nil {
		// Use defaults if business not found
		business = models.Business{
			LabelPaperSize: "A4",
			LabelWidthMM:   50,
			LabelHeightMM:  30,
			LabelColumns:   3,
			LabelRows:      8,
			LabelMarginMM:  10,
		}
	}

	barcodeMode := "a4"
	labelSizeKey := "2inch"
	var printSettings models.PrintSettings
	if err := utils.DB.Where("user_id = ?", userID).First(&printSettings).Error; err == nil {
		if printSettings.BarcodePrintMode == "label" {
			barcodeMode = "label"
		}
		labelSizeKey = normalizeBarcodeLabelSize(printSettings.BarcodeLabelSize)
	}

	// Parse request body for quantity / optional size override
	var input struct {
		Quantity      int    `json:"quantity"`
		LabelSize     string `json:"label_size"`
		Format        string `json:"format"` // html (default) or json for silent ESC/POS
		StartPosition int    `json:"start_position"`
		Preview       bool   `json:"preview"`
	}
	if c.Request.Method == "POST" {
		if err := c.ShouldBindJSON(&input); err != nil {
			input.Quantity = 1
		}
	} else {
		input.Quantity = 1
		if q := c.Query("label_size"); q != "" {
			input.LabelSize = q
		}
		if q := c.Query("quantity"); q != "" {
			if n, err := strconv.Atoi(q); err == nil {
				input.Quantity = n
			}
		}
	}

	if input.Quantity < 1 {
		input.Quantity = 1
	}
	if input.Quantity > 500 {
		input.Quantity = 500
	}
	if input.Preview && input.Quantity > 8 {
		input.Quantity = 8
	}
	if input.LabelSize != "" {
		labelSizeKey = normalizeBarcodeLabelSize(input.LabelSize)
		// Explicit size from the print dialog always targets thermal label rolls
		barcodeMode = "label"
	}

	labelSize := getBarcodeLabelSize(labelSizeKey)
	compact := labelSizeKey == "1inch" || labelSizeKey == "1.5inch"

	labelData := productLabelData{
		Name:      product.Name,
		SKU:       product.SKU,
		ItemCode:  product.ItemCode,
		Category:  product.Category,
		SalePrice: product.SalePrice,
		MRP:       product.MRP,
	}
	format := strings.ToLower(strings.TrimSpace(input.Format))
	if format == "" {
		format = strings.ToLower(strings.TrimSpace(c.Query("format")))
	}
	if barcodeMode == "label" && (format == "json" || wantsJSONResponse(c)) {
		code := barcodeValueForProduct(labelData)
		entry := BarcodeLabelItemJSON{
			Name:    labelData.Name,
			Barcode: code,
			SKU:     labelData.SKU,
			Price:   labelData.SalePrice,
		}
		if labelData.MRP > 0 {
			entry.MRP = labelData.MRP
		}
		labels := make([]BarcodeLabelItemJSON, 0, input.Quantity)
		for i := 0; i < input.Quantity; i++ {
			labels = append(labels, entry)
		}
		c.JSON(http.StatusOK, BarcodeLabelsResponse{
			Title:    "Product Label",
			Size:     labelSize.Key,
			WidthMM:  labelSize.WidthMM,
			HeightMM: labelSize.HeightMM,
			Compact:  compact,
			Labels:   labels,
		})
		return
	}

	singleLabel := buildProductLabelHTML(labelData, labelSize, compact)

	labelsHTML := ""
	for i := 0; i < input.Quantity; i++ {
		labelsHTML += singleLabel
	}

	if barcodeMode == "label" {
		html := wrapBarcodeLabelDocument("Product Label", barcodeLabelPageCSS(labelSize), labelsHTML)
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusOK, html)
		return
	}

	sheetLayout := a4LabelSheetLayoutFromBusiness(business)
	a4Size := barcodeLabelSizeForA4Layout(sheetLayout)
	labelHTMLs := make([]string, 0, input.Quantity)
	singleA4Label := buildProductLabelHTML(labelData, a4Size, false)
	for i := 0; i < input.Quantity; i++ {
		labelHTMLs = append(labelHTMLs, singleA4Label)
	}

	html := buildA4LabelsSheetDocument("Product Labels", labelHTMLs, sheetLayout, input.StartPosition, input.Preview)
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, html)
}

func ImportProductsCSV(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	_ = utils.EnsureDefaultCategories(utils.DB, userID)

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
	var importedCount int
	var errors []string

	for i, record := range records[1:] {
		if len(record) != len(headers) {
			errors = append(errors, fmt.Sprintf("Row %d: Column count mismatch", i+2))
			continue
		}

		product := models.Product{
			ID:       uuid.New(),
			UserID:   userID,
			Name:     getCSVValue(record, headers, "Name"),
			SKU:      getCSVValue(record, headers, "SKU"),
			ItemCode: firstCSVValue(record, headers, "Item Code", "Itemcode", "Barcode"),
			PLU:      strings.TrimSpace(getCSVValue(record, headers, "PLU")),
			Category: utils.ResolveCategoryName(getCSVValue(record, headers, "Category")),
			Unit:     getCSVValue(record, headers, "Unit"),
			HSNCode:  getCSVValue(record, headers, "HSN Code"),
		}

		product.PurchasePrice = parseFloat(getCSVValue(record, headers, "Purchase Price"))
		product.SalePrice = parseFloat(getCSVValue(record, headers, "Sale Price"))
		product.MRP = parseFloat(getCSVValue(record, headers, "MRP"))
		product.TaxRate = parseFloat(getCSVValue(record, headers, "Tax Rate %"))
		product.Discount = getCSVValue(record, headers, "Discount")
		product.MinStock = parseFloat(getCSVValue(record, headers, "Min Stock"))
		product.ItemType = getCSVValue(record, headers, "Item Type")
		product.LowStockAlert = parseBool(getCSVValue(record, headers, "Low Stock Alert"))
		product.EnableBatching = parseBool(getCSVValue(record, headers, "Enable Batching"))
		product.SalePriceWithTax = parseBool(getCSVValue(record, headers, "Sale Price With Tax"))
		product.PurchasePriceWithTax = parseBool(getCSVValue(record, headers, "Purchase Price With Tax"))

		if product.Name == "" {
			errors = append(errors, fmt.Sprintf("Row %d: Name is required", i+2))
			continue
		}

		product.SKU = strings.TrimSpace(product.SKU)
		if product.SKU == "" {
			product.SKU = utils.GenerateUniqueProductSKU(product.Name)
		}

		if err := utils.AssignProductPLU(userID, &product); err != nil {
			errors = append(errors, fmt.Sprintf("Row %d: %v", i+2, err))
			continue
		}
		if msg := pluValidationError(userID, product.PLU, nil, true); msg != "" {
			errors = append(errors, fmt.Sprintf("Row %d: %s", i+2, msg))
			continue
		}

		if err := utils.DB.Create(&product).Error; err != nil {
			errors = append(errors, fmt.Sprintf("Row %d: %v", i+2, err))
			continue
		}

		importedCount++
	}

	c.JSON(http.StatusOK, gin.H{
		"imported": importedCount,
		"errors":   errors,
	})
}

func ImportProductsExcel(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	_ = utils.EnsureDefaultCategories(utils.DB, userID)

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
	row, err := sheet.Row(0)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to read header row"})
		return
	}
	for i := 0; ; i++ {
		cell := row.GetCell(i)
		if cell == nil {
			break
		}
		headers[i] = cell.String()
	}

	var importedCount int
	var errors []string

	for i := 1; i <= sheet.MaxRow; i++ {
		row, err := sheet.Row(i)
		if err != nil {
			continue
		}
		product := models.Product{
			ID:       uuid.New(),
			UserID:   userID,
			Name:     getExcelValue(row, headers, "Name"),
			SKU:      getExcelValue(row, headers, "SKU"),
			ItemCode: firstExcelValue(row, headers, "Item Code", "Itemcode", "Barcode"),
			PLU:      strings.TrimSpace(getExcelValue(row, headers, "PLU")),
			Category: utils.ResolveCategoryName(getExcelValue(row, headers, "Category")),
			Unit:     getExcelValue(row, headers, "Unit"),
			HSNCode:  getExcelValue(row, headers, "HSN Code"),
		}

		product.PurchasePrice = parseFloat(getExcelValue(row, headers, "Purchase Price"))
		product.SalePrice = parseFloat(getExcelValue(row, headers, "Sale Price"))
		product.MRP = parseFloat(getExcelValue(row, headers, "MRP"))
		product.TaxRate = parseFloat(getExcelValue(row, headers, "Tax Rate %"))
		product.Discount = getExcelValue(row, headers, "Discount")
		product.MinStock = parseFloat(getExcelValue(row, headers, "Min Stock"))
		product.ItemType = getExcelValue(row, headers, "Item Type")
		product.LowStockAlert = parseBool(getExcelValue(row, headers, "Low Stock Alert"))
		product.EnableBatching = parseBool(getExcelValue(row, headers, "Enable Batching"))
		product.SalePriceWithTax = parseBool(getExcelValue(row, headers, "Sale Price With Tax"))
		product.PurchasePriceWithTax = parseBool(getExcelValue(row, headers, "Purchase Price With Tax"))

		if product.Name == "" {
			errors = append(errors, fmt.Sprintf("Row %d: Name is required", i+1))
			continue
		}

		product.SKU = strings.TrimSpace(product.SKU)
		if product.SKU == "" {
			product.SKU = utils.GenerateUniqueProductSKU(product.Name)
		}

		if err := utils.AssignProductPLU(userID, &product); err != nil {
			errors = append(errors, fmt.Sprintf("Row %d: %v", i+1, err))
			continue
		}
		if msg := pluValidationError(userID, product.PLU, nil, true); msg != "" {
			errors = append(errors, fmt.Sprintf("Row %d: %s", i+1, msg))
			continue
		}

		if err := utils.DB.Create(&product).Error; err != nil {
			errors = append(errors, fmt.Sprintf("Row %d: %v", i+1, err))
			continue
		}

		importedCount++
	}

	c.JSON(http.StatusOK, gin.H{
		"imported": importedCount,
		"errors":   errors,
	})
}

func getCSVValue(record []string, headers []string, key string) string {
	for i, h := range headers {
		if h == key && i < len(record) {
			return record[i]
		}
	}
	return ""
}

func firstCSVValue(record []string, headers []string, keys ...string) string {
	for _, key := range keys {
		if v := getCSVValue(record, headers, key); v != "" {
			return v
		}
	}
	return ""
}

func getExcelValue(row *xlsx.Row, headers map[int]string, key string) string {
	for i, h := range headers {
		if h == key {
			cell := row.GetCell(i)
			if cell != nil {
				return cell.String()
			}
		}
	}
	return ""
}

func firstExcelValue(row *xlsx.Row, headers map[int]string, keys ...string) string {
	for _, key := range keys {
		if v := getExcelValue(row, headers, key); v != "" {
			return v
		}
	}
	return ""
}

func parseFloat(s string) float64 {
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return 0
}

func parseBool(s string) bool {
	return s == "true" || s == "1" || s == "yes"
}

// mapBindingErrorToFields converts Go JSON binding errors into user-facing field errors.
func mapBindingErrorToFields(err error) gin.H {
	fields := gin.H{}
	if err == nil {
		return fields
	}
	msg := err.Error()
	if strings.Contains(msg, "outlet_id") {
		fields["inventory.outlet_id"] = "Please select a valid warehouse, or leave it empty"
	}
	if strings.Contains(msg, "uuid.UUID") {
		if _, ok := fields["inventory.outlet_id"]; !ok {
			fields["_form"] = "One or more fields have an invalid value"
		}
	}
	if len(fields) == 0 {
		fields["_form"] = "Please check the highlighted fields and try again"
	}
	return fields
}

// getTaxRateFromHSN fetches the tax rate for a given HSN code from the CSV dataset
func getTaxRateFromHSN(hsnCode string) float64 {
	file, err := os.Open("HSN_DATASET.csv")
	if err != nil {
		fmt.Printf("[DEBUG] getTaxRateFromHSN - Failed to open HSN dataset: %v\n", err)
		return 0
	}
	defer file.Close()

	reader := csv.NewReader(file)
	records, err := reader.ReadAll()
	if err != nil {
		fmt.Printf("[DEBUG] getTaxRateFromHSN - Failed to read HSN dataset: %v\n", err)
		return 0
	}

	// Search for HSN code in the dataset (skip header row)
	for i := 1; i < len(records); i++ {
		if len(records[i]) >= 2 && strings.EqualFold(records[i][0], hsnCode) {
			// Default tax rate is 18% (9% CGST + 9% SGST)
			// In a real implementation, you might have tax rates in the CSV
			fmt.Printf("[DEBUG] getTaxRateFromHSN - Found HSN code: %s\n", hsnCode)
			return 18.0
		}
	}

	fmt.Printf("[DEBUG] getTaxRateFromHSN - HSN code not found: %s\n", hsnCode)
	return 0
}

func GenerateProductItemCode(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	unit := c.DefaultQuery("unit", "PCS")

	code, err := utils.GenerateUniqueProductItemCode(userID, isWeightBasedUnit(unit))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate item code. Please try again."})
		return
	}

	c.JSON(http.StatusOK, gin.H{"item_code": code})
}

func NextProductPLU(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	plu, err := utils.NextProductPLU(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate PLU. Please try again."})
		return
	}

	c.JSON(http.StatusOK, gin.H{"plu": plu})
}

func CheckProductItemCode(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	itemCode := strings.TrimSpace(c.Query("item_code"))
	if itemCode == "" {
		c.JSON(http.StatusOK, gin.H{"exists": false, "products": []gin.H{}})
		return
	}

	var products []models.Product
	if err := utils.DB.Where("user_id = ? AND TRIM(item_code) = ?", userID, itemCode).
		Order("name ASC").
		Find(&products).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check item code"})
		return
	}

	if len(products) == 0 {
		c.JSON(http.StatusOK, gin.H{"exists": false, "products": []gin.H{}})
		return
	}

	out := make([]gin.H, 0, len(products))
	for _, product := range products {
		out = append(out, gin.H{
			"id":        product.ID,
			"name":      product.Name,
			"sku":       product.SKU,
			"item_code": product.ItemCode,
		})
	}

	c.JSON(http.StatusOK, gin.H{"exists": true, "products": out})
}

func CheckProductPLU(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	plu := strings.TrimSpace(c.Query("plu"))
	if plu == "" {
		c.JSON(http.StatusOK, gin.H{"exists": false, "products": []gin.H{}})
		return
	}

	var products []models.Product
	if err := utils.DB.Where("user_id = ? AND TRIM(plu) = ?", userID, plu).
		Order("name ASC").
		Find(&products).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check PLU"})
		return
	}

	if len(products) == 0 {
		c.JSON(http.StatusOK, gin.H{"exists": false, "products": []gin.H{}})
		return
	}

	out := make([]gin.H, 0, len(products))
	for _, product := range products {
		out = append(out, gin.H{
			"id":        product.ID,
			"name":      product.Name,
			"sku":       product.SKU,
			"item_code": product.ItemCode,
			"plu":       product.PLU,
		})
	}

	c.JSON(http.StatusOK, gin.H{"exists": true, "products": out})
}
