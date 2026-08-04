package controllers

import (
	"truerp/models"
	"truerp/utils"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func GetParties(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	search := c.Query("search")
	category := c.Query("category")
	partyType := c.Query("party_type")

	fmt.Printf("[DEBUG] GetParties - UserID: %s, Search: %s, Category: %s, PartyType: %s\n", userID, search, category, partyType)

	var parties []models.Party
	query := utils.DB.Where("user_id = ?", userID)

	if search != "" {
		query = query.Where("name LIKE ? OR phone LIKE ? OR email LIKE ? OR category LIKE ?", "%"+search+"%", "%"+search+"%", "%"+search+"%", "%"+search+"%")
	}

	if category != "" {
		query = query.Where("category = ?", category)
	}

	if partyType != "" {
		query = query.Where("party_type = ?", partyType)
	}

	if err := query.Order("name ASC").Find(&parties).Error; err != nil {
		fmt.Printf("[DEBUG] GetParties - DB error: %v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch parties"})
		return
	}

	fmt.Printf("[DEBUG] GetParties - Found %d parties\n", len(parties))
	c.JSON(http.StatusOK, parties)
}

func GetPartyStats(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	fmt.Printf("[DEBUG] GetPartyStats - UserID: %s\n", userID)

	var stats models.PartyStats

	var totalParties int64
	if err := utils.DB.Model(&models.Party{}).Where("user_id = ?", userID).Count(&totalParties).Error; err != nil {
		fmt.Printf("[DEBUG] GetPartyStats - DB error: %v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch party stats"})
		return
	}
	stats.TotalParties = totalParties

	var toCollect float64
	if err := utils.DB.Model(&models.Party{}).
		Where("user_id = ? AND party_type = ? AND balance > 0", userID, "customer").
		Select("COALESCE(SUM(balance), 0)").
		Scan(&toCollect).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch to collect amount"})
		return
	}
	stats.ToCollect = toCollect

	var toPay float64
	if err := utils.DB.Model(&models.Party{}).
		Where("user_id = ? AND party_type = ? AND balance < 0", userID, "vendor").
		Select("COALESCE(ABS(SUM(balance)), 0)").
		Scan(&toPay).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch to pay amount"})
		return
	}
	stats.ToPay = toPay

	c.JSON(http.StatusOK, stats)
}

func GetParty(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	fmt.Printf("[DEBUG] GetParty - UserID: %s, ID: %s\n", userID, id)

	var party models.Party
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&party).Error; err != nil {
		fmt.Printf("[DEBUG] GetParty - Party not found: %v\n", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "Party not found"})
		return
	}

	c.JSON(http.StatusOK, party)
}

func CreateParty(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	userName := ""
	if name, exists := c.Get("user_name"); exists {
		userName = name.(string)
	}

	var input models.Party
	if err := c.ShouldBindJSON(&input); err != nil {
		fmt.Printf("[DEBUG] CreateParty - JSON bind error: %v\n", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	fmt.Printf("[DEBUG] CreateParty - UserID: %s, Name: %s, PartyType: %s\n", userID, input.Name, input.PartyType)

	party := models.Party{
		ID:             uuid.New(),
		UserID:         userID,
		Name:           input.Name,
		Phone:          input.Phone,
		Email:          input.Email,
		GSTIN:          input.GSTIN,
		Address:        input.Address,
		City:           input.City,
		State:          input.State,
		Pincode:        input.Pincode,
		StateCode:      input.StateCode,
		Category:       input.Category,
		PartyType:      input.PartyType,
		OpeningBalance: input.OpeningBalance,
		Balance:        input.OpeningBalance,
		CreditLimit:    input.CreditLimit,
		TAN:            input.TAN,
		PAN:            input.PAN,
		Notes:          input.Notes,
		IsActive:       true,
	}

	if err := utils.DB.Create(&party).Error; err != nil {
		fmt.Printf("[DEBUG] CreateParty - DB create error: %v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create party"})
		return
	}

	fmt.Printf("[DEBUG] CreateParty - Party created successfully: %s\n", party.ID)

	// Log party creation
	CreateAuditLog(
		userID,
		userName,
		"create",
		"party",
		&party.ID,
		party.Name,
		fmt.Sprintf("Created %s: %s", party.PartyType, party.Name),
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		map[string]interface{}{
			"party_type":      party.PartyType,
			"category":        party.Category,
			"opening_balance": party.OpeningBalance,
		},
		"success",
		"",
	)

	c.JSON(http.StatusCreated, party)
}

func UpdateParty(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	userName := ""
	if name, exists := c.Get("user_name"); exists {
		userName = name.(string)
	}
	id := c.Param("id")

	fmt.Printf("[DEBUG] UpdateParty - UserID: %s, ID: %s\n", userID, id)

	var input models.Party
	if err := c.ShouldBindJSON(&input); err != nil {
		fmt.Printf("[DEBUG] UpdateParty - JSON bind error: %v\n", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var party models.Party
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&party).Error; err != nil {
		fmt.Printf("[DEBUG] UpdateParty - Party not found: %v\n", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "Party not found"})
		return
	}

	updates := map[string]interface{}{
		"name":           input.Name,
		"phone":          input.Phone,
		"email":          input.Email,
		"gstin":          input.GSTIN,
		"address":        input.Address,
		"city":           input.City,
		"state":          input.State,
		"pincode":        input.Pincode,
		"state_code":     input.StateCode,
		"category":       input.Category,
		"party_type":     input.PartyType,
		"credit_limit":   input.CreditLimit,
		"tan":            input.TAN,
		"pan":            input.PAN,
		"notes":          input.Notes,
		"is_active":      input.IsActive,
	}

	if err := utils.DB.Model(&party).Updates(updates).Error; err != nil {
		fmt.Printf("[DEBUG] UpdateParty - DB update error: %v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update party"})
		return
	}

	fmt.Printf("[DEBUG] UpdateParty - Party updated successfully: %s\n", id)

	// Log party update
	CreateAuditLog(
		userID,
		userName,
		"update",
		"party",
		&party.ID,
		party.Name,
		fmt.Sprintf("Updated party: %s", party.Name),
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		map[string]interface{}{
			"party_type": input.PartyType,
			"category":   input.Category,
		},
		"success",
		"",
	)

	c.JSON(http.StatusOK, party)
}

func DeleteParty(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	userName := ""
	if name, exists := c.Get("user_name"); exists {
		userName = name.(string)
	}
	id := c.Param("id")

	fmt.Printf("[DEBUG] DeleteParty - UserID: %s, ID: %s\n", userID, id)

	var party models.Party
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&party).Error; err != nil {
		fmt.Printf("[DEBUG] DeleteParty - Party not found: %v\n", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "Party not found"})
		return
	}

	if err := utils.DB.Delete(&party).Error; err != nil {
		fmt.Printf("[DEBUG] DeleteParty - DB delete error: %v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete party"})
		return
	}

	fmt.Printf("[DEBUG] DeleteParty - Party deleted successfully: %s\n", id)

	// Log party deletion
	CreateAuditLog(
		userID,
		userName,
		"delete",
		"party",
		&party.ID,
		party.Name,
		fmt.Sprintf("Deleted %s: %s", party.PartyType, party.Name),
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		map[string]interface{}{
			"party_type": party.PartyType,
			"balance":    party.Balance,
		},
		"success",
		"",
	)

	c.JSON(http.StatusOK, gin.H{"message": "Party deleted successfully"})
}

func BulkDeleteParties(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		IDs []uuid.UUID `json:"ids" binding:"required"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := utils.DB.Where("user_id = ? AND id IN ?", userID, input.IDs).Delete(&models.Party{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete parties"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Parties deleted successfully"})
}

func BulkUpdatePartyCategory(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		IDs      []uuid.UUID `json:"ids" binding:"required"`
		Category string      `json:"category" binding:"required"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := utils.DB.Model(&models.Party{}).
		Where("user_id = ? AND id IN ?", userID, input.IDs).
		Update("category", input.Category).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update party categories"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Party categories updated successfully"})
}
