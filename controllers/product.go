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

func GetProducts(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var products []models.Product
	query := utils.DB.Where("user_id = ?", userID)

	if category := c.Query("category"); category != "" && category != "all" {
		query = query.Where("category = ?", category)
	}

	if search := c.Query("search"); search != "" {
		query = query.Where("name LIKE ? OR sku LIKE ? OR item_code LIKE ?", "%"+search+"%", "%"+search+"%", "%"+search+"%")
	}

	if err := query.Order("created_at DESC").Find(&products).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch products"})
		return
	}

	c.JSON(http.StatusOK, products)
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

	input.Product.ID = uuid.New()
	input.Product.UserID = userID

	// Auto-populate tax rate from HSN code if not provided
	if input.Product.HSNCode != "" && input.Product.TaxRate == 0 {
		if taxRate := getTaxRateFromHSN(input.Product.HSNCode); taxRate > 0 {
			input.Product.TaxRate = taxRate
			fmt.Printf("[DEBUG] CreateProduct - Auto-populated tax rate from HSN: %s -> %.2f%%\n", input.Product.HSNCode, taxRate)
		}
	}

	// Check if SKU already exists for this user
	if input.Product.SKU != "" {
		var existingProduct models.Product
		if err := utils.DB.Where("user_id = ? AND sku = ?", userID, input.Product.SKU).First(&existingProduct).Error; err == nil {
			c.JSON(http.StatusConflict, gin.H{
				"error":  "A product with this SKU already exists",
				"fields": gin.H{"sku": "This SKU is already in use"},
			})
			return
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
			MfgDate:    mfgDate,
			ExpDate:    expDate,
			EntryDate:  time.Now(),
		}

		if err := utils.DB.Create(&stockEntry).Error; err != nil {
			fmt.Printf("[DEBUG] CreateProduct - Failed to create stock entry: %v\n", err)
		} else {
			fmt.Printf("[DEBUG] CreateProduct - Stock entry created: %s\n", stockEntry.ID)

			// Update inventory stock
			updateInventoryStock(userID, input.Product.ID, outletID, "opening", input.Inventory.Quantity, input.Inventory.CostPrice)
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

	// Auto-populate tax rate from HSN code if HSN is being changed and tax rate is not provided
	hsnChanged := input.Product.HSNCode != "" && input.Product.HSNCode != product.HSNCode
	if hsnChanged && input.Product.TaxRate == 0 {
		if taxRate := getTaxRateFromHSN(input.Product.HSNCode); taxRate > 0 {
			input.Product.TaxRate = taxRate
			fmt.Printf("[DEBUG] UpdateProduct - Auto-populated tax rate from HSN: %s -> %.2f%%\n", input.Product.HSNCode, taxRate)
		}
	}

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
	if input.Product.TaxRate != 0 {
		updates["tax_rate"] = input.Product.TaxRate
	}
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
			MfgDate:    mfgDate,
			ExpDate:    expDate,
			EntryDate:  time.Now(),
		}

		if err := utils.DB.Create(&stockEntry).Error; err != nil {
			fmt.Printf("[DEBUG] UpdateProduct - Failed to create stock entry: %v\n", err)
		} else {
			fmt.Printf("[DEBUG] UpdateProduct - Stock entry created: %s\n", stockEntry.ID)

			// Update inventory stock
			updateInventoryStock(userID, product.ID, outletID, "adjustment", input.Inventory.Quantity, input.Inventory.CostPrice)
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
		query = query.Where("name LIKE ? OR sku LIKE ? OR item_code LIKE ?", "%"+search+"%", "%"+search+"%", "%"+search+"%")
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
		"Name", "SKU", "Item Code", "Category", "Stock Qty", "Unit",
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
			p.Category,
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
		query = query.Where("name LIKE ? OR sku LIKE ? OR item_code LIKE ?", "%"+search+"%", "%"+search+"%", "%"+search+"%")
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
		"Name", "SKU", "Item Code", "Category", "Stock Qty", "Unit",
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
		row.AddCell().SetValue(p.Category)
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
		Quantity  int    `json:"quantity"`
		LabelSize string `json:"label_size"`
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

	// Use business settings for A4 sheet columns
	columns := business.LabelColumns
	if columns < 1 || columns > 5 {
		columns = 3
	}
	colWidth := 100.0 / float64(columns)
	a4Width := business.LabelWidthMM
	a4Height := business.LabelHeightMM
	if a4Width < 10 {
		a4Width = 50
	}
	if a4Height < 10 {
		a4Height = 30
	}

	// For A4 grid, derive barcode bar metrics from cell size
	a4Size := BarcodeLabelSize{
		Key:         "a4",
		WidthMM:     a4Width,
		HeightMM:    a4Height,
		NameFontPx:  12,
		SkuFontPx:   10,
		PriceFontPx: 14,
		MetaFontPx:  9,
		BarcodeH:    32,
		BarcodeW:    1.3,
		PaddingMM:   3,
	}
	singleLabel = buildProductLabelHTML(labelData, a4Size, false)
	labelsHTML = ""
	for i := 0; i < input.Quantity; i++ {
		labelsHTML += singleLabel
	}

	css := fmt.Sprintf(`
@page {
	size: %s;
	margin: %.2fmm;
}
body {
	font-family: Arial, Helvetica, sans-serif;
	margin: 0;
	padding: 0;
}
.labels-grid {
	display: grid;
	grid-template-columns: repeat(%d, %s);
	gap: 5mm;
}
.label {
	border: 1px solid #000;
	padding: %.2fmm;
	text-align: center;
	page-break-inside: avoid;
	box-sizing: border-box;
	width: %.2fmm;
	height: %.2fmm;
	display: flex;
	flex-direction: column;
	align-items: center;
	justify-content: center;
	overflow: hidden;
}
.product-name {
	font-size: %.1fpx;
	font-weight: bold;
	margin-bottom: 2px;
	max-width: 100%%;
	overflow: hidden;
	text-overflow: ellipsis;
	white-space: nowrap;
}
.product-sku { font-size: %.1fpx; margin-bottom: 2px; }
.product-barcode { width: 100%%; line-height: 0; margin: 2px 0; }
.product-barcode svg { max-width: 100%%; height: auto; }
.product-price { font-size: %.1fpx; font-weight: bold; margin-top: 2px; }
.product-mrp, .product-category { font-size: %.1fpx; color: #666; }
`, business.LabelPaperSize, business.LabelMarginMM, columns, fmt.Sprintf("%.2f%%", colWidth),
		a4Size.PaddingMM, a4Width, a4Height,
		a4Size.NameFontPx, a4Size.SkuFontPx, a4Size.PriceFontPx, a4Size.MetaFontPx)

	html := wrapBarcodeLabelDocument("Product Labels", css, `<div class="labels-grid">`+labelsHTML+`</div>`)
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, html)
}

func ImportProductsCSV(c *gin.Context) {
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
			Category: getCSVValue(record, headers, "Category"),
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
			Category: getExcelValue(row, headers, "Category"),
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
