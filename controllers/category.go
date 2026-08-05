package controllers

import (
	"truerp/models"
	"truerp/utils"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func GetCategories(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	parentID := c.Query("parent_id")
	active := c.Query("is_active")

	fmt.Printf("[DEBUG] GetCategories - UserID: %s, ParentID: %s, Active: %s\n", userID, parentID, active)

	_ = utils.EnsureDefaultCategories(utils.DB, userID)

	var categories []models.Category
	query := utils.DB.Where("user_id = ?", userID)

	if parentID != "" {
		query = query.Where("parent_id = ?", parentID)
	}

	if active != "" {
		query = query.Where("is_active = ?", active == "true")
	}

	if err := query.Order("name ASC").Find(&categories).Error; err != nil {
		fmt.Printf("[DEBUG] GetCategories - DB error: %v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch categories"})
		return
	}

	fmt.Printf("[DEBUG] GetCategories - Found %d categories\n", len(categories))
	c.JSON(http.StatusOK, categories)
}

func GetCategory(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	fmt.Printf("[DEBUG] GetCategory - UserID: %s, ID: %s\n", userID, id)

	var category models.Category
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Preload("Parent").First(&category).Error; err != nil {
		fmt.Printf("[DEBUG] GetCategory - Category not found: %v\n", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "Category not found"})
		return
	}

	c.JSON(http.StatusOK, category)
}

func CreateCategory(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		Name        string     `json:"name" binding:"required"`
		Description string     `json:"description"`
		ParentID    *uuid.UUID `json:"parent_id"`
		IsActive    bool       `json:"is_active"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		fmt.Printf("[DEBUG] CreateCategory - JSON bind error: %v\n", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	fmt.Printf("[DEBUG] CreateCategory - UserID: %s, Name: %s\n", userID, input.Name)

	category := models.Category{
		ID:          uuid.New(),
		UserID:      userID,
		Name:        input.Name,
		Description: input.Description,
		ParentID:    input.ParentID,
		IsActive:    input.IsActive,
	}

	if err := utils.DB.Create(&category).Error; err != nil {
		fmt.Printf("[DEBUG] CreateCategory - DB create error: %v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create category"})
		return
	}

	fmt.Printf("[DEBUG] CreateCategory - Category created successfully: %s\n", category.ID)
	c.JSON(http.StatusCreated, category)
}

func UpdateCategory(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	fmt.Printf("[DEBUG] UpdateCategory - UserID: %s, ID: %s\n", userID, id)

	var input struct {
		Name        string     `json:"name"`
		Description string     `json:"description"`
		ParentID    *uuid.UUID `json:"parent_id"`
		IsActive    bool       `json:"is_active"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		fmt.Printf("[DEBUG] UpdateCategory - JSON bind error: %v\n", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var category models.Category
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&category).Error; err != nil {
		fmt.Printf("[DEBUG] UpdateCategory - Category not found: %v\n", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "Category not found"})
		return
	}

	// Check if is_active is being changed
	statusChanged := category.IsActive != input.IsActive

	updates := map[string]interface{}{
		"name":         input.Name,
		"description":  input.Description,
		"parent_id":    input.ParentID,
		"is_active":    input.IsActive,
	}

	if err := utils.DB.Model(&category).Updates(updates).Error; err != nil {
		fmt.Printf("[DEBUG] UpdateCategory - DB update error: %v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update category"})
		return
	}

	// If category status changed, update all products in this category
	if statusChanged {
		if err := utils.DB.Model(&models.Product{}).
			Where("user_id = ? AND category = ?", userID, category.Name).
			Update("is_active", input.IsActive).Error; err != nil {
			fmt.Printf("[DEBUG] UpdateCategory - Failed to update products in category: %v\n", err)
		} else {
			fmt.Printf("[DEBUG] UpdateCategory - Updated products in category to is_active=%v\n", input.IsActive)
		}
	}

	fmt.Printf("[DEBUG] UpdateCategory - Category updated successfully: %s\n", id)
	c.JSON(http.StatusOK, category)
}

func DeleteCategory(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	fmt.Printf("[DEBUG] DeleteCategory - UserID: %s, ID: %s\n", userID, id)

	var category models.Category
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&category).Error; err != nil {
		fmt.Printf("[DEBUG] DeleteCategory - Category not found: %v\n", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "Category not found"})
		return
	}

	if err := utils.DB.Delete(&category).Error; err != nil {
		fmt.Printf("[DEBUG] DeleteCategory - DB delete error: %v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete category"})
		return
	}

	fmt.Printf("[DEBUG] DeleteCategory - Category deleted successfully: %s\n", id)
	c.JSON(http.StatusOK, gin.H{"message": "Category deleted successfully"})
}

func BulkDeleteCategories(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		IDs []uuid.UUID `json:"ids" binding:"required"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := utils.DB.Where("user_id = ? AND id IN ?", userID, input.IDs).Delete(&models.Category{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete categories"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Categories deleted successfully"})
}

func BulkUpdateCategoryStatus(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		IDs      []uuid.UUID `json:"ids" binding:"required"`
		IsActive bool        `json:"is_active"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var categories []models.Category
	if err := utils.DB.Where("user_id = ? AND id IN ?", userID, input.IDs).Find(&categories).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch categories"})
		return
	}

	if len(categories) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "No categories found"})
		return
	}

	if err := utils.DB.Model(&models.Category{}).
		Where("user_id = ? AND id IN ?", userID, input.IDs).
		Update("is_active", input.IsActive).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update categories"})
		return
	}

	names := make([]string, 0, len(categories))
	for _, category := range categories {
		names = append(names, category.Name)
	}

	if err := utils.DB.Model(&models.Product{}).
		Where("user_id = ? AND category IN ?", userID, names).
		Update("is_active", input.IsActive).Error; err != nil {
		fmt.Printf("[DEBUG] BulkUpdateCategoryStatus - Failed to update products: %v\n", err)
	}

	c.JSON(http.StatusOK, gin.H{"message": "Categories updated successfully"})
}
