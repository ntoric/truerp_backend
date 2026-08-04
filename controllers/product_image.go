package controllers

import (
	"truerp/models"
	"truerp/utils"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func GetProductImages(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	productID := c.Param("id")

	var images []models.ProductImage
	if err := utils.DB.Where("user_id = ? AND product_id = ?", userID, productID).Order("sort_order ASC").Find(&images).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch product images"})
		return
	}

	c.JSON(http.StatusOK, images)
}

func CreateProductImage(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	productID := c.Param("id")

	var input struct {
		ImageURL string `json:"image_url" binding:"required"`
		AltText  string `json:"alt_text"`
		IsPrimary bool  `json:"is_primary"`
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

	// If setting as primary, remove primary from other images
	if input.IsPrimary {
		utils.DB.Model(&models.ProductImage{}).Where("user_id = ? AND product_id = ?", userID, productID).Update("is_primary", false)
	}

	// Get max sort order
	var maxSortOrder int
	utils.DB.Model(&models.ProductImage{}).Where("user_id = ? AND product_id = ?", userID, productID).Select("COALESCE(MAX(sort_order), 0)").Scan(&maxSortOrder)

	image := models.ProductImage{
		ID:        uuid.New(),
		ProductID: uuid.MustParse(productID),
		UserID:    userID,
		ImageURL:  input.ImageURL,
		AltText:   input.AltText,
		IsPrimary: input.IsPrimary,
		SortOrder: maxSortOrder + 1,
	}

	if err := utils.DB.Create(&image).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create product image"})
		return
	}

	c.JSON(http.StatusCreated, image)
}

func UpdateProductImage(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var input struct {
		ImageURL  string `json:"image_url"`
		AltText   string `json:"alt_text"`
		IsPrimary *bool  `json:"is_primary"`
		SortOrder *int   `json:"sort_order"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var image models.ProductImage
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&image).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Product image not found"})
		return
	}

	// If setting as primary, remove primary from other images
	if input.IsPrimary != nil && *input.IsPrimary {
		utils.DB.Model(&models.ProductImage{}).Where("user_id = ? AND product_id = ? AND id != ?", userID, image.ProductID, id).Update("is_primary", false)
	}

	updates := map[string]interface{}{
		"image_url": input.ImageURL,
		"alt_text":  input.AltText,
	}

	if input.IsPrimary != nil {
		updates["is_primary"] = *input.IsPrimary
	}
	if input.SortOrder != nil {
		updates["sort_order"] = *input.SortOrder
	}

	if err := utils.DB.Model(&image).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update product image"})
		return
	}

	c.JSON(http.StatusOK, image)
}

func DeleteProductImage(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Delete(&models.ProductImage{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete product image"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Product image deleted successfully"})
}

func ReorderProductImages(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	productID := c.Param("id")

	var input struct {
		ImageIDs []uuid.UUID `json:"image_ids" binding:"required"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	for i, imageID := range input.ImageIDs {
		utils.DB.Model(&models.ProductImage{}).
			Where("user_id = ? AND product_id = ? AND id = ?", userID, productID, imageID).
			Update("sort_order", i)
	}

	c.JSON(http.StatusOK, gin.H{"message": "Images reordered successfully"})
}
