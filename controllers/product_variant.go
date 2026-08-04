package controllers

import (
	"truerp/models"
	"truerp/utils"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func GetProductVariants(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	productID := c.Param("id")

	var variants []models.ProductVariant
	query := utils.DB.Where("user_id = ? AND product_id = ?", userID, productID)

	if isActive := c.Query("is_active"); isActive != "" {
		query = query.Where("is_active = ?", isActive == "true")
	}

	if err := query.Order("created_at DESC").Find(&variants).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch product variants"})
		return
	}

	c.JSON(http.StatusOK, variants)
}

func GetProductVariant(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var variant models.ProductVariant
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&variant).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Product variant not found"})
		return
	}

	c.JSON(http.StatusOK, variant)
}

func CreateProductVariant(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	productID := c.Param("id")

	var input struct {
		VariantName    string  `json:"variant_name" binding:"required"`
		VariantSKU     string  `json:"variant_sku" binding:"required"`
		VariantBarcode string  `json:"variant_barcode"`
		Attributes     string  `json:"attributes"`
		PurchasePrice  float64 `json:"purchase_price"`
		SalePrice      float64 `json:"sale_price"`
		MRP            float64 `json:"mrp"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Validate product
	var product models.Product
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, productID).First(&product).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid product"})
		return
	}

	variant := models.ProductVariant{
		ID:             uuid.New(),
		ProductID:      uuid.MustParse(productID),
		UserID:         userID,
		VariantName:    input.VariantName,
		VariantSKU:     input.VariantSKU,
		VariantBarcode: input.VariantBarcode,
		Attributes:     input.Attributes,
		PurchasePrice:  input.PurchasePrice,
		SalePrice:      input.SalePrice,
		MRP:            input.MRP,
		IsActive:       true,
	}

	if err := utils.DB.Create(&variant).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create product variant"})
		return
	}

	c.JSON(http.StatusCreated, variant)
}

func UpdateProductVariant(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var input struct {
		VariantName    string  `json:"variant_name"`
		VariantSKU     string  `json:"variant_sku"`
		VariantBarcode string  `json:"variant_barcode"`
		Attributes     string  `json:"attributes"`
		PurchasePrice  float64 `json:"purchase_price"`
		SalePrice      float64 `json:"sale_price"`
		MRP            float64 `json:"mrp"`
		IsActive       *bool   `json:"is_active"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var variant models.ProductVariant
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&variant).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Product variant not found"})
		return
	}

	updates := map[string]interface{}{
		"variant_name":    input.VariantName,
		"variant_sku":     input.VariantSKU,
		"variant_barcode": input.VariantBarcode,
		"attributes":      input.Attributes,
		"purchase_price":  input.PurchasePrice,
		"sale_price":      input.SalePrice,
		"mrp":             input.MRP,
	}

	if input.IsActive != nil {
		updates["is_active"] = *input.IsActive
	}

	if err := utils.DB.Model(&variant).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update product variant"})
		return
	}

	c.JSON(http.StatusOK, variant)
}

func DeleteProductVariant(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Delete(&models.ProductVariant{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete product variant"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Product variant deleted successfully"})
}
