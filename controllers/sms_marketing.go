package controllers

import (
	"truerp/models"
	"truerp/utils"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func GetSMSCampaigns(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var campaigns []models.SMSMarketing
	query := utils.DB.Where("user_id = ?", userID).Preload("Recipients")

	if status := c.Query("status"); status != "" {
		query = query.Where("status = ?", status)
	}

	if err := query.Order("created_at DESC").Find(&campaigns).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch SMS campaigns"})
		return
	}

	c.JSON(http.StatusOK, campaigns)
}

func GetSMSCampaign(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var campaign models.SMSMarketing
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Preload("Recipients").First(&campaign).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "SMS campaign not found"})
		return
	}

	c.JSON(http.StatusOK, campaign)
}

func CreateSMSCampaign(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		CampaignName   string    `json:"campaign_name" binding:"required"`
		Message        string    `json:"message" binding:"required"`
		TargetAudience string    `json:"target_audience" binding:"required"`
		ScheduledDate  *time.Time `json:"scheduled_date"`
		PartyIDs       []uuid.UUID `json:"party_ids"`
		PhoneNumbers   []string  `json:"phone_numbers"`
		Notes          string    `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	campaign := models.SMSMarketing{
		ID:             uuid.New(),
		UserID:         userID,
		CampaignName:   input.CampaignName,
		Message:        input.Message,
		TargetAudience: input.TargetAudience,
		ScheduledDate:  input.ScheduledDate,
		Status:         "draft",
		Notes:          input.Notes,
	}

	if err := utils.DB.Create(&campaign).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create SMS campaign"})
		return
	}

	// Add recipients based on target audience
	var recipients []models.SMSRecipient

	switch input.TargetAudience {
	case "all_customers":
		var parties []models.Party
		utils.DB.Where("user_id = ? AND party_type = ? AND phone != ''", userID, "customer").Find(&parties)
		for _, party := range parties {
			recipients = append(recipients, models.SMSRecipient{
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
			recipients = append(recipients, models.SMSRecipient{
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
			recipients = append(recipients, models.SMSRecipient{
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
			recipients = append(recipients, models.SMSRecipient{
				ID:          uuid.New(),
				CampaignID:  campaign.ID,
				PartyID:     &party.ID,
				PhoneNumber: party.Phone,
				Status:      "pending",
			})
		}
	case "custom_numbers":
		for _, phone := range input.PhoneNumbers {
			recipients = append(recipients, models.SMSRecipient{
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

func UpdateSMSCampaign(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var input struct {
		CampaignName   string     `json:"campaign_name"`
		Message        string     `json:"message"`
		TargetAudience string     `json:"target_audience"`
		ScheduledDate  *time.Time `json:"scheduled_date"`
		Notes          string     `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var campaign models.SMSMarketing
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&campaign).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "SMS campaign not found"})
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update SMS campaign"})
		return
	}

	utils.DB.Preload("Recipients").First(&campaign, campaign.ID)
	c.JSON(http.StatusOK, campaign)
}

func DeleteSMSCampaign(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var campaign models.SMSMarketing
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&campaign).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "SMS campaign not found"})
		return
	}

	// Only allow deletion if status is draft or scheduled
	if campaign.Status == "sent" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot delete sent campaigns"})
		return
	}

	if err := utils.DB.Delete(&campaign).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete SMS campaign"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "SMS campaign deleted successfully"})
}

func SendSMSCampaign(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var campaign models.SMSMarketing
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&campaign).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "SMS campaign not found"})
		return
	}

	if campaign.Status != "draft" && campaign.Status != "scheduled" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Campaign has already been sent"})
		return
	}

	// Fetch developer settings to get SMS provider configuration
	var devSettings models.DeveloperSettings
	if err := utils.DB.Where("user_id = ?", userID).First(&devSettings).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "SMS provider not configured. Please configure SMS provider in Developer Settings."})
		return
	}

	// Update campaign status
	now := time.Now()
	campaign.Status = "sent"
	campaign.SentDate = &now
	utils.DB.Save(&campaign)

	// Get recipients
	var recipients []models.SMSRecipient
	utils.DB.Where("campaign_id = ?", campaign.ID).Find(&recipients)

	sentCount := 0
	failedCount := 0

	for _, recipient := range recipients {
		// Send SMS based on provider
		var err error
		
		switch devSettings.SMSProvider {
		case "twilio":
			err = sendViaTwilio(devSettings, recipient.PhoneNumber, campaign.Message)
		case "msg91":
			err = sendViaMsg91(devSettings, recipient.PhoneNumber, campaign.Message)
		case "textlocal":
			err = sendViaTextLocal(devSettings, recipient.PhoneNumber, campaign.Message)
		case "aws_sns":
			err = sendViaAWSSNS(devSettings, recipient.PhoneNumber, campaign.Message)
		case "sendgrid":
			err = sendViaSendGridSMS(devSettings, recipient.PhoneNumber, campaign.Message)
		default:
			err = fmt.Errorf("unsupported SMS provider: %s", devSettings.SMSProvider)
		}

		if err != nil {
			recipient.Status = "failed"
			recipient.ErrorMessage = err.Error()
			failedCount++
		} else {
			recipient.Status = "sent"
			recipient.SentAt = &now
			sentCount++
		}
		utils.DB.Save(&recipient)
	}

	// Update campaign stats
	campaign.SentCount = sentCount
	campaign.FailedCount = failedCount
	utils.DB.Save(&campaign)

	utils.DB.Preload("Recipients").First(&campaign, campaign.ID)
	c.JSON(http.StatusOK, campaign)
}

// Placeholder functions for SMS providers - to be implemented with actual SDK integrations
func sendViaTwilio(settings models.DeveloperSettings, phone, message string) error {
	// TODO: Implement actual Twilio SMS sending using Twilio Go SDK
	// Decrypt auth token and use Twilio API
	return nil
}

func sendViaMsg91(settings models.DeveloperSettings, phone, message string) error {
	// TODO: Implement actual Msg91 SMS sending
	// Decrypt auth key and use Msg91 API
	return nil
}

func sendViaTextLocal(settings models.DeveloperSettings, phone, message string) error {
	// TODO: Implement actual TextLocal SMS sending
	// Decrypt API key and use TextLocal API
	return nil
}

func sendViaAWSSNS(settings models.DeveloperSettings, phone, message string) error {
	// TODO: Implement actual AWS SNS SMS sending using AWS SDK for Go
	// Decrypt secret key and use AWS SNS API
	return nil
}

func sendViaSendGridSMS(settings models.DeveloperSettings, phone, message string) error {
	// TODO: Implement actual SendGrid SMS sending using SendGrid Go SDK
	// Decrypt API key and use SendGrid SMS API
	return nil
}

func ScheduleSMSCampaign(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var input struct {
		ScheduledDate time.Time `json:"scheduled_date" binding:"required"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var campaign models.SMSMarketing
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&campaign).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "SMS campaign not found"})
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

func GetSMSStats(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var totalCampaigns int64
	var sentCampaigns int64
	var scheduledCampaigns int64
	var totalSent int64
	var totalFailed int64

	utils.DB.Model(&models.SMSMarketing{}).Where("user_id = ?", userID).Count(&totalCampaigns)
	utils.DB.Model(&models.SMSMarketing{}).Where("user_id = ? AND status = ?", userID, "sent").Count(&sentCampaigns)
	utils.DB.Model(&models.SMSMarketing{}).Where("user_id = ? AND status = ?", userID, "scheduled").Count(&scheduledCampaigns)
	
	utils.DB.Model(&models.SMSMarketing{}).Where("user_id = ?", userID).Select("COALESCE(SUM(sent_count), 0)").Scan(&totalSent)
	utils.DB.Model(&models.SMSMarketing{}).Where("user_id = ?", userID).Select("COALESCE(SUM(failed_count), 0)").Scan(&totalFailed)

	c.JSON(http.StatusOK, gin.H{
		"total_campaigns":      totalCampaigns,
		"sent_campaigns":       sentCampaigns,
		"scheduled_campaigns":  scheduledCampaigns,
		"total_sent":           totalSent,
		"total_failed":         totalFailed,
	})
}
