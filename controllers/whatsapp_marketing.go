package controllers

import (
	"truerp/models"
	"truerp/utils"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func GetWhatsAppCampaigns(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var campaigns []models.WhatsAppMarketing
	query := utils.DB.Where("user_id = ?", userID).Preload("Recipients")

	if status := c.Query("status"); status != "" {
		query = query.Where("status = ?", status)
	}

	if err := query.Order("created_at DESC").Find(&campaigns).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch WhatsApp campaigns"})
		return
	}

	c.JSON(http.StatusOK, campaigns)
}

func GetWhatsAppCampaign(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var campaign models.WhatsAppMarketing
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Preload("Recipients").First(&campaign).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "WhatsApp campaign not found"})
		return
	}

	c.JSON(http.StatusOK, campaign)
}

func CreateWhatsAppCampaign(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		CampaignName   string       `json:"campaign_name" binding:"required"`
		Message        string       `json:"message" binding:"required"`
		MediaURL       string       `json:"media_url"`
		TargetAudience string       `json:"target_audience" binding:"required"`
		ScheduledDate  *time.Time   `json:"scheduled_date"`
		PartyIDs       []uuid.UUID  `json:"party_ids"`
		PhoneNumbers   []string     `json:"phone_numbers"`
		Notes          string       `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	campaign := models.WhatsAppMarketing{
		ID:             uuid.New(),
		UserID:         userID,
		CampaignName:   input.CampaignName,
		Message:        input.Message,
		MediaURL:       input.MediaURL,
		TargetAudience: input.TargetAudience,
		ScheduledDate:  input.ScheduledDate,
		Status:         "draft",
		Notes:          input.Notes,
	}

	if err := utils.DB.Create(&campaign).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create WhatsApp campaign"})
		return
	}

	// Add recipients based on target audience
	var recipients []models.WhatsAppRecipient

	switch input.TargetAudience {
	case "all_customers":
		var parties []models.Party
		utils.DB.Where("user_id = ? AND party_type = ? AND phone != ''", userID, "customer").Find(&parties)
		for _, party := range parties {
			recipients = append(recipients, models.WhatsAppRecipient{
				ID:          uuid.New(),
				CampaignID:  campaign.ID,
				PartyID:     &party.ID,
				PhoneNumber: party.Phone,
				Status:      "pending",
			})
		}
	case "all_vendors":
		var parties []models.Party
		utils.DB.Where("user_id = ? AND party_type = ? AND phone != ''", userID, "vendor").Find(&parties)
		for _, party := range parties {
			recipients = append(recipients, models.WhatsAppRecipient{
				ID:          uuid.New(),
				CampaignID:  campaign.ID,
				PartyID:     &party.ID,
				PhoneNumber: party.Phone,
				Status:      "pending",
			})
		}
	case "specific_customers":
		var parties []models.Party
		utils.DB.Where("user_id = ? AND party_type = ? AND phone != '' AND id IN ?", userID, "customer", input.PartyIDs).Find(&parties)
		for _, party := range parties {
			recipients = append(recipients, models.WhatsAppRecipient{
				ID:          uuid.New(),
				CampaignID:  campaign.ID,
				PartyID:     &party.ID,
				PhoneNumber: party.Phone,
				Status:      "pending",
			})
		}
	case "specific_vendors":
		var parties []models.Party
		utils.DB.Where("user_id = ? AND party_type = ? AND phone != '' AND id IN ?", userID, "vendor", input.PartyIDs).Find(&parties)
		for _, party := range parties {
			recipients = append(recipients, models.WhatsAppRecipient{
				ID:          uuid.New(),
				CampaignID:  campaign.ID,
				PartyID:     &party.ID,
				PhoneNumber: party.Phone,
				Status:      "pending",
			})
		}
	case "custom_numbers":
		for _, phone := range input.PhoneNumbers {
			recipients = append(recipients, models.WhatsAppRecipient{
				ID:          uuid.New(),
				CampaignID:  campaign.ID,
				PhoneNumber: phone,
				Status:      "pending",
			})
		}
	}

	// Create recipients
	if len(recipients) > 0 {
		if err := utils.DB.Create(&recipients).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create recipients"})
			return
		}
	}

	// Update campaign with recipient count
	campaign.TotalRecipients = len(recipients)
	utils.DB.Save(&campaign)

	// Reload with recipients
	utils.DB.Preload("Recipients").First(&campaign, campaign.ID)

	c.JSON(http.StatusCreated, campaign)
}

func UpdateWhatsAppCampaign(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var input struct {
		CampaignName   string     `json:"campaign_name"`
		Message        string     `json:"message"`
		MediaURL       string     `json:"media_url"`
		TargetAudience string     `json:"target_audience"`
		ScheduledDate  *time.Time `json:"scheduled_date"`
		Notes          string     `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var campaign models.WhatsAppMarketing
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&campaign).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "WhatsApp campaign not found"})
		return
	}

	// Only allow updates if status is draft
	if campaign.Status != "draft" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Can only update draft campaigns"})
		return
	}

	updates := map[string]interface{}{}
	if input.CampaignName != "" {
		updates["campaign_name"] = input.CampaignName
	}
	if input.Message != "" {
		updates["message"] = input.Message
	}
	if input.MediaURL != "" {
		updates["media_url"] = input.MediaURL
	}
	if input.TargetAudience != "" {
		updates["target_audience"] = input.TargetAudience
	}
	if input.ScheduledDate != nil {
		updates["scheduled_date"] = input.ScheduledDate
	}
	if input.Notes != "" {
		updates["notes"] = input.Notes
	}

	if err := utils.DB.Model(&campaign).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update WhatsApp campaign"})
		return
	}

	utils.DB.Preload("Recipients").First(&campaign, campaign.ID)
	c.JSON(http.StatusOK, campaign)
}

func DeleteWhatsAppCampaign(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var campaign models.WhatsAppMarketing
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&campaign).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "WhatsApp campaign not found"})
		return
	}

	// Only allow deletion if status is draft or scheduled
	if campaign.Status == "sent" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot delete sent campaigns"})
		return
	}

	if err := utils.DB.Delete(&campaign).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete WhatsApp campaign"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "WhatsApp campaign deleted successfully"})
}

func SendWhatsAppCampaign(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var campaign models.WhatsAppMarketing
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&campaign).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "WhatsApp campaign not found"})
		return
	}

	if campaign.Status != "draft" && campaign.Status != "scheduled" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Campaign has already been sent"})
		return
	}

	// Update campaign status
	now := time.Now()
	campaign.Status = "sent"
	campaign.SentDate = &now
	utils.DB.Save(&campaign)

	// In a real implementation, you would integrate with WhatsApp Business API here
	// For now, we'll mark all recipients as sent
	var recipients []models.WhatsAppRecipient
	utils.DB.Where("campaign_id = ?", campaign.ID).Find(&recipients)

	sentCount := 0
	failedCount := 0

	for _, recipient := range recipients {
		// Simulate WhatsApp sending
		recipient.Status = "sent"
		recipient.SentAt = &now
		utils.DB.Save(&recipient)
		sentCount++
	}

	// Update campaign stats
	campaign.SentCount = sentCount
	campaign.FailedCount = failedCount
	utils.DB.Save(&campaign)

	utils.DB.Preload("Recipients").First(&campaign, campaign.ID)
	c.JSON(http.StatusOK, campaign)
}

func ScheduleWhatsAppCampaign(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var input struct {
		ScheduledDate time.Time `json:"scheduled_date" binding:"required"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var campaign models.WhatsAppMarketing
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&campaign).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "WhatsApp campaign not found"})
		return
	}

	if campaign.Status != "draft" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Can only schedule draft campaigns"})
		return
	}

	campaign.Status = "scheduled"
	campaign.ScheduledDate = &input.ScheduledDate
	utils.DB.Save(&campaign)

	utils.DB.Preload("Recipients").First(&campaign, campaign.ID)
	c.JSON(http.StatusOK, campaign)
}

func GetWhatsAppStats(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var totalCampaigns int64
	var sentCampaigns int64
	var scheduledCampaigns int64
	var totalSent int64
	var totalFailed int64
	var totalDelivered int64
	var totalRead int64

	utils.DB.Model(&models.WhatsAppMarketing{}).Where("user_id = ?", userID).Count(&totalCampaigns)
	utils.DB.Model(&models.WhatsAppMarketing{}).Where("user_id = ? AND status = ?", userID, "sent").Count(&sentCampaigns)
	utils.DB.Model(&models.WhatsAppMarketing{}).Where("user_id = ? AND status = ?", userID, "scheduled").Count(&scheduledCampaigns)
	
	utils.DB.Model(&models.WhatsAppMarketing{}).Where("user_id = ?", userID).Select("COALESCE(SUM(sent_count), 0)").Scan(&totalSent)
	utils.DB.Model(&models.WhatsAppMarketing{}).Where("user_id = ?", userID).Select("COALESCE(SUM(failed_count), 0)").Scan(&totalFailed)
	utils.DB.Model(&models.WhatsAppMarketing{}).Where("user_id = ?", userID).Select("COALESCE(SUM(delivered_count), 0)").Scan(&totalDelivered)
	utils.DB.Model(&models.WhatsAppMarketing{}).Where("user_id = ?", userID).Select("COALESCE(SUM(read_count), 0)").Scan(&totalRead)

	c.JSON(http.StatusOK, gin.H{
		"total_campaigns":      totalCampaigns,
		"sent_campaigns":       sentCampaigns,
		"scheduled_campaigns":  scheduledCampaigns,
		"total_sent":           totalSent,
		"total_failed":         totalFailed,
		"total_delivered":      totalDelivered,
		"total_read":           totalRead,
	})
}
