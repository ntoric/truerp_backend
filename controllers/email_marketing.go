package controllers

import (
	"truerp/models"
	"truerp/utils"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func GetEmailCampaigns(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var campaigns []models.EmailMarketing
	query := utils.DB.Where("user_id = ?", userID).Preload("Recipients")

	if status := c.Query("status"); status != "" {
		query = query.Where("status = ?", status)
	}

	if err := query.Order("created_at DESC").Find(&campaigns).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch email campaigns"})
		return
	}

	c.JSON(http.StatusOK, campaigns)
}

func GetEmailCampaign(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var campaign models.EmailMarketing
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Preload("Recipients").First(&campaign).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Email campaign not found"})
		return
	}

	c.JSON(http.StatusOK, campaign)
}

func CreateEmailCampaign(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		CampaignName   string       `json:"campaign_name" binding:"required"`
		Subject        string       `json:"subject" binding:"required"`
		Body           string       `json:"body" binding:"required"`
		TargetAudience string       `json:"target_audience" binding:"required"`
		ScheduledDate  *time.Time   `json:"scheduled_date"`
		PartyIDs       []uuid.UUID  `json:"party_ids"`
		EmailAddresses []string     `json:"email_addresses"`
		Notes          string       `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	campaign := models.EmailMarketing{
		ID:             uuid.New(),
		UserID:         userID,
		CampaignName:   input.CampaignName,
		Subject:        input.Subject,
		Body:           input.Body,
		TargetAudience: input.TargetAudience,
		ScheduledDate:  input.ScheduledDate,
		Status:         "draft",
		Notes:          input.Notes,
	}

	if err := utils.DB.Create(&campaign).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create email campaign"})
		return
	}

	// Add recipients based on target audience
	var recipients []models.EmailRecipient

	switch input.TargetAudience {
	case "all_customers":
		var parties []models.Party
		utils.DB.Where("user_id = ? AND party_type = ? AND email != ''", userID, "customer").Find(&parties)
		for _, party := range parties {
			recipients = append(recipients, models.EmailRecipient{
				ID:           uuid.New(),
				CampaignID:   campaign.ID,
				PartyID:      &party.ID,
				EmailAddress: party.Email,
				Status:       "pending",
			})
		}
	case "all_vendors":
		var parties []models.Party
		utils.DB.Where("user_id = ? AND party_type = ? AND email != ''", userID, "vendor").Find(&parties)
		for _, party := range parties {
			recipients = append(recipients, models.EmailRecipient{
				ID:           uuid.New(),
				CampaignID:   campaign.ID,
				PartyID:      &party.ID,
				EmailAddress: party.Email,
				Status:       "pending",
			})
		}
	case "specific_customers":
		var parties []models.Party
		utils.DB.Where("user_id = ? AND party_type = ? AND email != '' AND id IN ?", userID, "customer", input.PartyIDs).Find(&parties)
		for _, party := range parties {
			recipients = append(recipients, models.EmailRecipient{
				ID:           uuid.New(),
				CampaignID:   campaign.ID,
				PartyID:      &party.ID,
				EmailAddress: party.Email,
				Status:       "pending",
			})
		}
	case "specific_vendors":
		var parties []models.Party
		utils.DB.Where("user_id = ? AND party_type = ? AND email != '' AND id IN ?", userID, "vendor", input.PartyIDs).Find(&parties)
		for _, party := range parties {
			recipients = append(recipients, models.EmailRecipient{
				ID:           uuid.New(),
				CampaignID:   campaign.ID,
				PartyID:      &party.ID,
				EmailAddress: party.Email,
				Status:       "pending",
			})
		}
	case "custom_emails":
		for _, email := range input.EmailAddresses {
			recipients = append(recipients, models.EmailRecipient{
				ID:           uuid.New(),
				CampaignID:   campaign.ID,
				EmailAddress: email,
				Status:       "pending",
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

func UpdateEmailCampaign(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var input struct {
		CampaignName   string     `json:"campaign_name"`
		Subject        string     `json:"subject"`
		Body           string     `json:"body"`
		TargetAudience string     `json:"target_audience"`
		ScheduledDate  *time.Time `json:"scheduled_date"`
		Notes          string     `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var campaign models.EmailMarketing
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&campaign).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Email campaign not found"})
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
	if input.Subject != "" {
		updates["subject"] = input.Subject
	}
	if input.Body != "" {
		updates["body"] = input.Body
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update email campaign"})
		return
	}

	utils.DB.Preload("Recipients").First(&campaign, campaign.ID)
	c.JSON(http.StatusOK, campaign)
}

func DeleteEmailCampaign(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var campaign models.EmailMarketing
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&campaign).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Email campaign not found"})
		return
	}

	// Only allow deletion if status is draft or scheduled
	if campaign.Status == "sent" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot delete sent campaigns"})
		return
	}

	if err := utils.DB.Delete(&campaign).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete email campaign"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Email campaign deleted successfully"})
}

func SendEmailCampaign(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var campaign models.EmailMarketing
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&campaign).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Email campaign not found"})
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

	// In a real implementation, you would integrate with an email service here
	// For now, we'll mark all recipients as sent
	var recipients []models.EmailRecipient
	utils.DB.Where("campaign_id = ?", campaign.ID).Find(&recipients)

	sentCount := 0
	failedCount := 0

	for _, recipient := range recipients {
		// Simulate email sending
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

func ScheduleEmailCampaign(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var input struct {
		ScheduledDate time.Time `json:"scheduled_date" binding:"required"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var campaign models.EmailMarketing
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&campaign).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Email campaign not found"})
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

func GetEmailStats(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var totalCampaigns int64
	var sentCampaigns int64
	var scheduledCampaigns int64
	var totalSent int64
	var totalFailed int64
	var totalOpened int64
	var totalClicked int64

	utils.DB.Model(&models.EmailMarketing{}).Where("user_id = ?", userID).Count(&totalCampaigns)
	utils.DB.Model(&models.EmailMarketing{}).Where("user_id = ? AND status = ?", userID, "sent").Count(&sentCampaigns)
	utils.DB.Model(&models.EmailMarketing{}).Where("user_id = ? AND status = ?", userID, "scheduled").Count(&scheduledCampaigns)
	
	utils.DB.Model(&models.EmailMarketing{}).Where("user_id = ?", userID).Select("COALESCE(SUM(sent_count), 0)").Scan(&totalSent)
	utils.DB.Model(&models.EmailMarketing{}).Where("user_id = ?", userID).Select("COALESCE(SUM(failed_count), 0)").Scan(&totalFailed)
	utils.DB.Model(&models.EmailMarketing{}).Where("user_id = ?", userID).Select("COALESCE(SUM(opened_count), 0)").Scan(&totalOpened)
	utils.DB.Model(&models.EmailMarketing{}).Where("user_id = ?", userID).Select("COALESCE(SUM(clicked_count), 0)").Scan(&totalClicked)

	c.JSON(http.StatusOK, gin.H{
		"total_campaigns":      totalCampaigns,
		"sent_campaigns":       sentCampaigns,
		"scheduled_campaigns":  scheduledCampaigns,
		"total_sent":           totalSent,
		"total_failed":         totalFailed,
		"total_opened":         totalOpened,
		"total_clicked":        totalClicked,
	})
}
