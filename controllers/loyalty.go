package controllers

import (
	"truerp/models"
	"truerp/utils"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func GetLoyaltySettings(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	settings, err := GetOrCreateLoyaltySettings(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load loyalty settings"})
		return
	}
	c.JSON(http.StatusOK, settings)
}

func UpdateLoyaltySettings(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	settings, err := GetOrCreateLoyaltySettings(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load loyalty settings"})
		return
	}

	var input models.LoyaltySettings
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	settings.IsEnabled = input.IsEnabled
	if input.SpendAmount > 0 {
		settings.SpendAmount = input.SpendAmount
	}
	if input.PointsPerSpend > 0 {
		settings.PointsPerSpend = input.PointsPerSpend
	}
	if input.PointValue > 0 {
		settings.PointValue = input.PointValue
	}
	if input.MinRedeemPoints >= 0 {
		settings.MinRedeemPoints = input.MinRedeemPoints
	}
	if input.MaxRedeemPercent >= 0 && input.MaxRedeemPercent <= 100 {
		settings.MaxRedeemPercent = input.MaxRedeemPercent
	}

	if err := utils.DB.Save(settings).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update loyalty settings"})
		return
	}
	c.JSON(http.StatusOK, settings)
}

func GetLoyaltyStats(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var stats models.LoyaltyStats

	utils.DB.Model(&models.Party{}).
		Where("user_id = ? AND party_type = ? AND loyalty_points > 0", userID, "customer").
		Count(&stats.TotalMembers)

	utils.DB.Model(&models.Party{}).
		Where("user_id = ? AND party_type = ?", userID, "customer").
		Select("COALESCE(SUM(loyalty_points), 0)").
		Scan(&stats.TotalPointsOutstanding)

	startOfMonth := time.Date(time.Now().Year(), time.Now().Month(), 1, 0, 0, 0, 0, time.Now().Location())
	utils.DB.Model(&models.LoyaltyTransaction{}).
		Where("user_id = ? AND transaction_type = ? AND created_at >= ?", userID, "earn", startOfMonth).
		Select("COALESCE(SUM(points), 0)").
		Scan(&stats.PointsEarnedThisMonth)

	var redeemed int64
	utils.DB.Model(&models.LoyaltyTransaction{}).
		Where("user_id = ? AND transaction_type = ? AND created_at >= ?", userID, "redeem", startOfMonth).
		Select("COALESCE(SUM(ABS(points)), 0)").
		Scan(&redeemed)
	stats.PointsRedeemedThisMonth = redeemed

	c.JSON(http.StatusOK, stats)
}

func GetLoyaltyTransactions(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	partyID := c.Query("party_id")

	query := utils.DB.Where("user_id = ?", userID).Preload("Party")
	if partyID != "" {
		query = query.Where("party_id = ?", partyID)
	}

	var transactions []models.LoyaltyTransaction
	if err := query.Order("created_at DESC").Limit(200).Find(&transactions).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch transactions"})
		return
	}
	c.JSON(http.StatusOK, transactions)
}

func GetLoyaltyCustomers(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	search := c.Query("search")

	query := utils.DB.Where("user_id = ? AND party_type = ?", userID, "customer")
	if search != "" {
		query = query.Where("name LIKE ? OR phone LIKE ?", "%"+search+"%", "%"+search+"%")
	}

	var parties []models.Party
	if err := query.Order("loyalty_points DESC, name ASC").Find(&parties).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch customers"})
		return
	}
	c.JSON(http.StatusOK, parties)
}

func GetPartyLoyaltyBalance(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	partyID, err := uuid.Parse(c.Param("party_id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid party id"})
		return
	}

	var party models.Party
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, partyID).First(&party).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Party not found"})
		return
	}

	var transactions []models.LoyaltyTransaction
	utils.DB.Where("user_id = ? AND party_id = ?", userID, partyID).
		Order("created_at DESC").
		Limit(50).
		Find(&transactions)

	settings, _ := GetOrCreateLoyaltySettings(userID)

	c.JSON(http.StatusOK, gin.H{
		"party_id":       party.ID,
		"party_name":     party.Name,
		"loyalty_points": party.LoyaltyPoints,
		"settings":       settings,
		"transactions":   transactions,
	})
}

func AdjustPartyLoyalty(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	partyID, err := uuid.Parse(c.Param("party_id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid party id"})
		return
	}

	var input struct {
		Points int64  `json:"points" binding:"required"`
		Notes  string `json:"notes"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if input.Points == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Points adjustment cannot be zero"})
		return
	}

	var party models.Party
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, partyID).First(&party).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Party not found"})
		return
	}

	if err := utils.DB.Transaction(func(tx *gorm.DB) error {
		return AdjustPartyLoyaltyPoints(tx, userID, &party, input.Points, input.Notes)
	}); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	utils.DB.Where("user_id = ? AND id = ?", userID, partyID).First(&party)
	c.JSON(http.StatusOK, party)
}

func CalculateLoyaltyRedemption(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var input struct {
		PartyID   uuid.UUID `json:"party_id" binding:"required"`
		Points    int64     `json:"points" binding:"required"`
		BillTotal float64   `json:"bill_total" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	settings, err := GetOrCreateLoyaltySettings(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load settings"})
		return
	}

	var party models.Party
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, input.PartyID).First(&party).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Party not found"})
		return
	}

	discount, err := ComputeLoyaltyRedemption(settings, &party, input.BillTotal, input.Points)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"discount":       discount,
		"points":         input.Points,
		"available_points": party.LoyaltyPoints,
	})
}
