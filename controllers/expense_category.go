package controllers

import (
	"net/http"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func GetExpenseCategories(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	active := c.Query("is_active")

	var categories []models.ExpenseCategory
	query := utils.DB.Where("user_id = ?", userID)

	if active != "" {
		query = query.Where("is_active = ?", active == "true")
	}

	if err := query.Order("name ASC").Find(&categories).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch expense categories"})
		return
	}

	c.JSON(http.StatusOK, categories)
}

func GetExpenseCategory(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var category models.ExpenseCategory
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&category).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Expense category not found"})
		return
	}

	c.JSON(http.StatusOK, category)
}

func CreateExpenseCategory(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		Name        string `json:"name" binding:"required"`
		Description string `json:"description"`
		IsActive    *bool  `json:"is_active"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	isActive := true
	if input.IsActive != nil {
		isActive = *input.IsActive
	}

	var existing models.ExpenseCategory
	if err := utils.DB.Where("user_id = ? AND name = ?", userID, input.Name).First(&existing).Error; err == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "Expense category with this name already exists"})
		return
	}

	category := models.ExpenseCategory{
		ID:          uuid.New(),
		UserID:      userID,
		Name:        input.Name,
		Description: input.Description,
		IsActive:    isActive,
	}

	if err := utils.DB.Create(&category).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create expense category"})
		return
	}

	c.JSON(http.StatusCreated, category)
}

func UpdateExpenseCategory(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var input struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		IsActive    *bool  `json:"is_active"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var category models.ExpenseCategory
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&category).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Expense category not found"})
		return
	}

	oldName := category.Name
	updates := map[string]interface{}{}
	if input.Name != "" {
		updates["name"] = input.Name
	}
	if input.Description != "" || input.Name != "" {
		updates["description"] = input.Description
	}
	if input.IsActive != nil {
		updates["is_active"] = *input.IsActive
	}

	if len(updates) == 0 {
		c.JSON(http.StatusOK, category)
		return
	}

	if err := utils.DB.Model(&category).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update expense category"})
		return
	}

	// Keep expense.category strings in sync when renamed
	if newName, ok := updates["name"].(string); ok && newName != oldName {
		_ = utils.DB.Model(&models.Expense{}).
			Where("user_id = ? AND category = ?", userID, oldName).
			Update("category", newName).Error
	}

	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&category).Error; err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "Expense category updated"})
		return
	}

	c.JSON(http.StatusOK, category)
}

func DeleteExpenseCategory(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var category models.ExpenseCategory
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&category).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Expense category not found"})
		return
	}

	if err := utils.DB.Delete(&category).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete expense category"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Expense category deleted successfully"})
}
