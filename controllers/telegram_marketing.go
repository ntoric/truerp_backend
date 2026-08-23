package controllers

import (
	"log"
	"net/http"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func GetTelegramCampaigns(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var campaigns []models.TelegramMarketing
	query := utils.DB.Where("user_id = ?", userID).Preload("Recipients")

	if status := c.Query("status"); status != "" {
		query = query.Where("status = ?", status)
	}

	if err := query.Order("created_at DESC").Find(&campaigns).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch Telegram campaigns"})
		return
	}

	c.JSON(http.StatusOK, campaigns)
}

func GetTelegramCampaign(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var campaign models.TelegramMarketing
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Preload("Recipients").First(&campaign).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Telegram campaign not found"})
		return
	}

	c.JSON(http.StatusOK, campaign)
}

// buildTelegramRecipients constructs the list of TelegramRecipient rows for a
// campaign based on the chosen target audience and the supplied party IDs /
// manual chat targets. Party-based audiences pull TelegramChatID from each
// Party; manual chats are taken verbatim.
func buildTelegramRecipients(userID uuid.UUID, campaignID uuid.UUID, targetAudience string, partyIDs []uuid.UUID, chatTargets []string) []models.TelegramRecipient {
	var recipients []models.TelegramRecipient

	addParty := func(party models.Party) {
		chat := strings.TrimSpace(party.TelegramChatID)
		if chat == "" {
			return
		}
		recipients = append(recipients, models.TelegramRecipient{
			ID:         uuid.New(),
			CampaignID: campaignID,
			PartyID:    &party.ID,
			ChatTarget: chat,
			Status:     "pending",
		})
	}

	switch targetAudience {
	case "all_customers":
		var parties []models.Party
		utils.DB.Where("user_id = ? AND party_type = ? AND telegram_chat_id != ''", userID, "customer").Find(&parties)
		for _, party := range parties {
			addParty(party)
		}
	case "all_vendors":
		var parties []models.Party
		utils.DB.Where("user_id = ? AND party_type = ? AND telegram_chat_id != ''", userID, "vendor").Find(&parties)
		for _, party := range parties {
			addParty(party)
		}
	case "specific_customers":
		var parties []models.Party
		utils.DB.Where("user_id = ? AND party_type = ? AND telegram_chat_id != '' AND id IN ?", userID, "customer", partyIDs).Find(&parties)
		for _, party := range parties {
			addParty(party)
		}
	case "specific_vendors":
		var parties []models.Party
		utils.DB.Where("user_id = ? AND party_type = ? AND telegram_chat_id != '' AND id IN ?", userID, "vendor", partyIDs).Find(&parties)
		for _, party := range parties {
			addParty(party)
		}
	case "custom_chats":
		for _, chat := range chatTargets {
			chat = strings.TrimSpace(chat)
			if chat == "" {
				continue
			}
			recipients = append(recipients, models.TelegramRecipient{
				ID:         uuid.New(),
				CampaignID: campaignID,
				ChatTarget: chat,
				Status:     "pending",
			})
		}
	}

	return recipients
}

func CreateTelegramCampaign(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		CampaignName   string       `json:"campaign_name" binding:"required"`
		Message        string       `json:"message" binding:"required"`
		MediaURL       string       `json:"media_url"`
		TargetAudience string       `json:"target_audience" binding:"required"`
		ScheduledDate  *time.Time   `json:"scheduled_date"`
		PartyIDs       []uuid.UUID  `json:"party_ids"`
		ChatTargets    []string     `json:"chat_targets"`
		Notes          string       `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	status := "draft"
	if input.ScheduledDate != nil {
		if input.ScheduledDate.Before(time.Now()) {
			status = "sent"
		} else {
			status = "scheduled"
		}
	}

	campaign := models.TelegramMarketing{
		ID:             uuid.New(),
		UserID:         userID,
		CampaignName:   input.CampaignName,
		Message:        input.Message,
		MediaURL:       input.MediaURL,
		TargetAudience: input.TargetAudience,
		ScheduledDate:  input.ScheduledDate,
		Status:         status,
		Notes:          input.Notes,
	}

	if err := utils.DB.Create(&campaign).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create Telegram campaign"})
		return
	}

	recipients := buildTelegramRecipients(userID, campaign.ID, input.TargetAudience, input.PartyIDs, input.ChatTargets)
	if len(recipients) > 0 {
		if err := utils.DB.Create(&recipients).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create recipients"})
			return
		}
	}

	campaign.TotalRecipients = len(recipients)
	utils.DB.Save(&campaign)

	// Send immediately if scheduled in the past and the bot is configured.
	if status == "sent" {
		if utils.TelegramConfiguredForUser(userID) {
			executeTelegramCampaignSend(&campaign)
		} else {
			campaign.Status = "failed"
			utils.DB.Save(&campaign)
		}
	}

	utils.DB.Preload("Recipients").First(&campaign, campaign.ID)
	c.JSON(http.StatusCreated, campaign)
}

func UpdateTelegramCampaign(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var input struct {
		CampaignName   string     `json:"campaign_name"`
		Message        string     `json:"message"`
		MediaURL       string     `json:"media_url"`
		TargetAudience string     `json:"target_audience"`
		ScheduledDate  *time.Time `json:"scheduled_date"`
		PartyIDs       []uuid.UUID `json:"party_ids"`
		ChatTargets    []string    `json:"chat_targets"`
		Notes          string     `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var campaign models.TelegramMarketing
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&campaign).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Telegram campaign not found"})
		return
	}

	if campaign.Status != "draft" && campaign.Status != "scheduled" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Can only update draft or scheduled campaigns"})
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update Telegram campaign"})
		return
	}

	utils.DB.First(&campaign, campaign.ID)

	// Rebuild recipients when audience or chat selection changes.
	rebuild := input.TargetAudience != "" || len(input.PartyIDs) > 0 || len(input.ChatTargets) > 0
	if rebuild {
		targetAudience := campaign.TargetAudience
		if input.TargetAudience != "" {
			targetAudience = input.TargetAudience
		}
		utils.DB.Where("campaign_id = ?", campaign.ID).Delete(&models.TelegramRecipient{})
		recipients := buildTelegramRecipients(userID, campaign.ID, targetAudience, input.PartyIDs, input.ChatTargets)
		if len(recipients) > 0 {
			if err := utils.DB.Create(&recipients).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update recipients"})
				return
			}
		}
		campaign.TotalRecipients = len(recipients)
		utils.DB.Save(&campaign)
	}

	// Recompute status from the (possibly updated) scheduled date.
	if input.ScheduledDate != nil {
		if input.ScheduledDate.Before(time.Now()) {
			if utils.TelegramConfiguredForUser(userID) {
				executeTelegramCampaignSend(&campaign)
			} else {
				campaign.Status = "failed"
				utils.DB.Save(&campaign)
			}
		} else {
			campaign.Status = "scheduled"
			utils.DB.Save(&campaign)
		}
	}

	utils.DB.Preload("Recipients").First(&campaign, campaign.ID)
	c.JSON(http.StatusOK, campaign)
}

func DeleteTelegramCampaign(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var campaign models.TelegramMarketing
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&campaign).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Telegram campaign not found"})
		return
	}

	if campaign.Status == "sent" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot delete sent campaigns"})
		return
	}

	if err := utils.DB.Delete(&campaign).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete Telegram campaign"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Telegram campaign deleted successfully"})
}

// executeTelegramCampaignSend performs the actual Bot API send for every
// recipient of the given campaign and updates the campaign + recipient rows.
func executeTelegramCampaignSend(campaign *models.TelegramMarketing) {
	now := time.Now()
	campaign.Status = "sent"
	campaign.SentDate = &now
	utils.DB.Save(campaign)

	cfg, err := utils.GetTelegramConfigForUser(campaign.UserID)
	if err != nil {
		log.Printf("telegram campaign: load bot config for user %s failed: %v", campaign.UserID, err)
		campaign.Status = "failed"
		utils.DB.Save(campaign)
		return
	}

	var recipients []models.TelegramRecipient
	utils.DB.Where("campaign_id = ?", campaign.ID).Find(&recipients)

	sentCount := 0
	failedCount := 0

	for i := range recipients {
		recipient := &recipients[i]
		if err := utils.SendTelegramMessage(cfg.BotToken, recipient.ChatTarget, campaign.Message); err != nil {
			recipient.Status = "failed"
			recipient.ErrorMessage = err.Error()
			utils.DB.Save(recipient)
			failedCount++
			continue
		}
		recipient.Status = "sent"
		recipient.SentAt = &now
		utils.DB.Save(recipient)
		sentCount++
	}

	campaign.SentCount = sentCount
	campaign.FailedCount = failedCount
	utils.DB.Save(campaign)
}

func SendTelegramCampaign(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var campaign models.TelegramMarketing
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&campaign).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Telegram campaign not found"})
		return
	}

	if campaign.Status != "draft" && campaign.Status != "scheduled" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Campaign has already been sent"})
		return
	}

	if !utils.TelegramConfiguredForUser(userID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Telegram bot is not configured. Set a bot token in Developer Settings before sending campaigns."})
		return
	}

	executeTelegramCampaignSend(&campaign)

	utils.DB.Preload("Recipients").First(&campaign, campaign.ID)
	c.JSON(http.StatusOK, campaign)
}

func ScheduleTelegramCampaign(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var input struct {
		ScheduledDate time.Time `json:"scheduled_date" binding:"required"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var campaign models.TelegramMarketing
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&campaign).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Telegram campaign not found"})
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

func GetTelegramStats(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var totalCampaigns int64
	var sentCampaigns int64
	var scheduledCampaigns int64
	var totalSent int64
	var totalFailed int64

	utils.DB.Model(&models.TelegramMarketing{}).Where("user_id = ?", userID).Count(&totalCampaigns)
	utils.DB.Model(&models.TelegramMarketing{}).Where("user_id = ? AND status = ?", userID, "sent").Count(&sentCampaigns)
	utils.DB.Model(&models.TelegramMarketing{}).Where("user_id = ? AND status = ?", userID, "scheduled").Count(&scheduledCampaigns)
	utils.DB.Model(&models.TelegramMarketing{}).Where("user_id = ?", userID).Select("COALESCE(SUM(sent_count), 0)").Scan(&totalSent)
	utils.DB.Model(&models.TelegramMarketing{}).Where("user_id = ?", userID).Select("COALESCE(SUM(failed_count), 0)").Scan(&totalFailed)

	c.JSON(http.StatusOK, gin.H{
		"total_campaigns":     totalCampaigns,
		"sent_campaigns":      sentCampaigns,
		"scheduled_campaigns": scheduledCampaigns,
		"total_sent":          totalSent,
		"total_failed":        totalFailed,
	})
}

// StartTelegramCampaignScheduler launches a background goroutine that
// periodically picks up scheduled Telegram campaigns whose ScheduledDate has
// arrived and sends them. It should be called once at application startup.
func StartTelegramCampaignScheduler() {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("telegram campaign scheduler: PANIC recovered: %v", r)
			}
		}()

		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("telegram campaign scheduler: PANIC in tick: %v", r)
					}
				}()
				processDueTelegramCampaigns()
			}()
		}
	}()
}

// processDueTelegramCampaigns finds scheduled campaigns whose time has come
// and sends them. Campaigns whose owner has not configured a Telegram bot are
// skipped so they can be retried once settings are in place.
func processDueTelegramCampaigns() {
	now := time.Now()

	var due []models.TelegramMarketing
	if err := utils.DB.Where("status = ? AND scheduled_date IS NOT NULL AND scheduled_date <= ?", "scheduled", now).Find(&due).Error; err != nil {
		log.Printf("telegram campaign scheduler: failed to query due campaigns: %v", err)
		return
	}

	for i := range due {
		campaign := &due[i]

		if !utils.TelegramConfiguredForUser(campaign.UserID) {
			// Skip until the user configures a bot; retried next tick.
			continue
		}

		executeTelegramCampaignSend(campaign)
		log.Printf("telegram campaign scheduler: sent campaign %s (%s)", campaign.ID, campaign.CampaignName)
	}
}
