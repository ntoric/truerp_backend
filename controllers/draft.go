package controllers

import (
	"truerp/models"
	"truerp/utils"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func GetDrafts(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	entityType := c.Query("entity_type")

	var drafts []models.Draft
	query := utils.DB.Where("user_id = ?", userID)

	if entityType != "" {
		query = query.Where("entity_type = ?", entityType)
	}

	if err := query.Order("updated_at DESC").Find(&drafts).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch drafts"})
		return
	}

	c.JSON(http.StatusOK, drafts)
}

func GetDraft(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var draft models.Draft
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&draft).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Draft not found"})
		return
	}

	c.JSON(http.StatusOK, draft)
}

func CreateDraft(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		EntityType string `json:"entity_type" binding:"required"`
		Title      string `json:"title" binding:"required"`
		Data       string `json:"data" binding:"required"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	draft := models.Draft{
		ID:         uuid.New(),
		UserID:     userID,
		EntityType: input.EntityType,
		Title:      input.Title,
		Data:       input.Data,
	}

	if err := utils.DB.Create(&draft).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create draft"})
		return
	}

	c.JSON(http.StatusCreated, draft)
}

func UpdateDraft(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var input struct {
		Title string `json:"title"`
		Data  string `json:"data"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var draft models.Draft
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&draft).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Draft not found"})
		return
	}

	updates := map[string]interface{}{}
	if input.Title != "" {
		updates["title"] = input.Title
	}
	if input.Data != "" {
		updates["data"] = input.Data
	}

	if err := utils.DB.Model(&draft).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update draft"})
		return
	}

	c.JSON(http.StatusOK, draft)
}

func DeleteDraft(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Delete(&models.Draft{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete draft"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Draft deleted successfully"})
}
