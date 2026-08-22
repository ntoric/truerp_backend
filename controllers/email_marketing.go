package controllers

import (
	"log"
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

// buildEmailRecipients constructs the list of EmailRecipient rows for a campaign
// based on the chosen target audience and the supplied party IDs / email addresses.
func buildEmailRecipients(userID uuid.UUID, campaignID uuid.UUID, targetAudience string, partyIDs []uuid.UUID, emailAddresses []string) []models.EmailRecipient {
	var recipients []models.EmailRecipient

	switch targetAudience {
	case "all_customers":
		var parties []models.Party
		utils.DB.Where("user_id = ? AND party_type = ? AND email != ''", userID, "customer").Find(&parties)
		for _, party := range parties {
			recipients = append(recipients, models.EmailRecipient{
				ID:           uuid.New(),
				CampaignID:   campaignID,
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
				CampaignID:   campaignID,
				PartyID:      &party.ID,
				EmailAddress: party.Email,
				Status:       "pending",
			})
		}
	case "specific_customers":
		var parties []models.Party
		utils.DB.Where("user_id = ? AND party_type = ? AND email != '' AND id IN ?", userID, "customer", partyIDs).Find(&parties)
		for _, party := range parties {
			recipients = append(recipients, models.EmailRecipient{
				ID:           uuid.New(),
				CampaignID:   campaignID,
				PartyID:      &party.ID,
				EmailAddress: party.Email,
				Status:       "pending",
			})
		}
	case "specific_vendors":
		var parties []models.Party
		utils.DB.Where("user_id = ? AND party_type = ? AND email != '' AND id IN ?", userID, "vendor", partyIDs).Find(&parties)
		for _, party := range parties {
			recipients = append(recipients, models.EmailRecipient{
				ID:           uuid.New(),
				CampaignID:   campaignID,
				PartyID:      &party.ID,
				EmailAddress: party.Email,
				Status:       "pending",
			})
		}
	case "custom_emails":
		for _, email := range emailAddresses {
			recipients = append(recipients, models.EmailRecipient{
				ID:           uuid.New(),
				CampaignID:   campaignID,
				EmailAddress: email,
				Status:       "pending",
			})
		}
	}

	return recipients
}

// executeEmailCampaignSend performs the actual SMTP send for every recipient
// of the given campaign and updates the campaign + recipient status rows.
// It does not perform any auth/SMTP-config checks; callers should ensure SMTP
// is configured before invoking (the scheduler skips campaigns whose user has
// no SMTP configured so they can be retried later).
func executeEmailCampaignSend(campaign *models.EmailMarketing) {
	now := time.Now()
	campaign.Status = "sent"
	campaign.SentDate = &now
	utils.DB.Save(campaign)

	var recipients []models.EmailRecipient
	utils.DB.Where("campaign_id = ?", campaign.ID).Find(&recipients)

	sentCount := 0
	failedCount := 0

	for _, recipient := range recipients {
		if recipient.EmailAddress == "" {
			recipient.Status = "failed"
			utils.DB.Save(&recipient)
			failedCount++
			continue
		}
		if err := utils.SendEmailForUser(campaign.UserID, recipient.EmailAddress, campaign.Subject, campaign.Body); err != nil {
			recipient.Status = "failed"
			utils.DB.Save(&recipient)
			failedCount++
			continue
		}
		recipient.Status = "sent"
		recipient.SentAt = &now
		utils.DB.Save(&recipient)
		sentCount++
	}

	campaign.SentCount = sentCount
	campaign.FailedCount = failedCount
	utils.DB.Save(campaign)
}

// resetCampaignRecipientsAndStats resets every recipient of the campaign to
// "pending" and zeroes the aggregate send/open/click counters so a fresh send
// (re-send or recurring occurrence) starts from a clean slate.
func resetCampaignRecipientsAndStats(campaign *models.EmailMarketing) {
	utils.DB.Model(&models.EmailRecipient{}).
		Where("campaign_id = ?", campaign.ID).
		Updates(map[string]interface{}{
			"status":        "pending",
			"error_message": "",
			"sent_at":       nil,
			"opened_at":     nil,
			"clicked_at":    nil,
		})

	campaign.SentCount = 0
	campaign.FailedCount = 0
	campaign.OpenedCount = 0
	campaign.ClickedCount = 0
	utils.DB.Save(campaign)
}

// advanceRecurrence computes the next send time for a recurring campaign by
// advancing the given base time by RecurrenceInterval units of
// RecurrenceFrequency. Returns the next time, or nil if the recurrence has
// passed its end date (in which case the campaign should be marked completed).
func advanceRecurrence(campaign *models.EmailMarketing, base time.Time) *time.Time {
	interval := campaign.RecurrenceInterval
	if interval <= 0 {
		interval = 1
	}

	var next time.Time
	switch campaign.RecurrenceFrequency {
	case "daily":
		next = base.AddDate(0, 0, interval)
	case "weekly":
		next = base.AddDate(0, 0, 7*interval)
	case "monthly":
		next = base.AddDate(0, interval, 0)
	default:
		return nil
	}

	if campaign.RecurrenceEndDate != nil && next.After(*campaign.RecurrenceEndDate) {
		return nil
	}
	return &next
}

// scheduleNextRecurrence advances a recurring campaign to its next occurrence.
// If there is no further occurrence (end date passed), the campaign is marked
// "completed". Recipients and stats are reset so the next send is clean.
func scheduleNextRecurrence(campaign *models.EmailMarketing) {
	base := time.Now()
	if campaign.ScheduledDate != nil {
		base = *campaign.ScheduledDate
	}

	next := advanceRecurrence(campaign, base)
	if next == nil {
		campaign.Status = "completed"
		campaign.ScheduledDate = nil
		utils.DB.Save(campaign)
		return
	}

	resetCampaignRecipientsAndStats(campaign)
	campaign.Status = "scheduled"
	campaign.ScheduledDate = next
	utils.DB.Save(campaign)
}

// StartEmailCampaignScheduler launches a background goroutine that periodically
// picks up scheduled email campaigns whose ScheduledDate has arrived and sends
// them. It should be called once at application startup.
func StartEmailCampaignScheduler() {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			processDueEmailCampaigns()
		}
	}()
}

// processDueEmailCampaigns finds scheduled campaigns whose time has come and
// sends them. Campaigns whose owner has not configured SMTP are skipped so
// they can be retried once settings are in place.
func processDueEmailCampaigns() {
	now := time.Now()

	var due []models.EmailMarketing
	if err := utils.DB.Where("status = ? AND scheduled_date IS NOT NULL AND scheduled_date <= ?", "scheduled", now).Find(&due).Error; err != nil {
		log.Printf("email scheduler: failed to query due campaigns: %v", err)
		return
	}

	for i := range due {
		campaign := &due[i]

		if !utils.EmailConfiguredForUser(campaign.UserID) {
			// Skip until the user configures SMTP; it will be retried next tick.
			continue
		}

		now := time.Now()
		campaign.LastSentAt = &now
		utils.DB.Save(campaign)

		executeEmailCampaignSend(campaign)
		log.Printf("email scheduler: sent campaign %s (%s)", campaign.ID, campaign.CampaignName)

		// For recurring campaigns, schedule the next occurrence (or mark
		// completed if the end date has passed).
		if campaign.IsRecurring {
			scheduleNextRecurrence(campaign)
		}
	}
}

func CreateEmailCampaign(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		CampaignName        string      `json:"campaign_name" binding:"required"`
		Subject             string      `json:"subject" binding:"required"`
		Body                string      `json:"body" binding:"required"`
		TargetAudience      string      `json:"target_audience" binding:"required"`
		ScheduledDate       *time.Time  `json:"scheduled_date"`
		PartyIDs            []uuid.UUID `json:"party_ids"`
		EmailAddresses      []string    `json:"email_addresses"`
		Notes               string      `json:"notes"`
		IsRecurring         bool        `json:"is_recurring"`
		RecurrenceFrequency string      `json:"recurrence_frequency"`
		RecurrenceInterval  int         `json:"recurrence_interval"`
		RecurrenceEndDate   *time.Time  `json:"recurrence_end_date"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Recurring campaigns require a schedule (the first send time) and a
	// valid frequency.
	if input.IsRecurring {
		if input.ScheduledDate == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Recurring campaigns require a scheduled start date"})
			return
		}
		if input.RecurrenceFrequency != "daily" && input.RecurrenceFrequency != "weekly" && input.RecurrenceFrequency != "monthly" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Recurrence frequency must be daily, weekly, or monthly"})
			return
		}
		if input.RecurrenceInterval <= 0 {
			input.RecurrenceInterval = 1
		}
	}

	// If a scheduled date is provided, the campaign is treated as scheduled
	// (or sent immediately if the date is in the past). Only campaigns without
	// a scheduled date remain drafts.
	status := "draft"
	if input.ScheduledDate != nil {
		if input.ScheduledDate.Before(time.Now()) {
			status = "sent"
		} else {
			status = "scheduled"
		}
	}

	campaign := models.EmailMarketing{
		ID:                  uuid.New(),
		UserID:              userID,
		CampaignName:        input.CampaignName,
		Subject:             input.Subject,
		Body:                input.Body,
		TargetAudience:      input.TargetAudience,
		ScheduledDate:       input.ScheduledDate,
		Status:              status,
		Notes:               input.Notes,
		IsRecurring:         input.IsRecurring,
		RecurrenceFrequency: input.RecurrenceFrequency,
		RecurrenceInterval:  input.RecurrenceInterval,
		RecurrenceEndDate:   input.RecurrenceEndDate,
	}

	if err := utils.DB.Create(&campaign).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create email campaign"})
		return
	}

	// Add recipients based on target audience
	recipients := buildEmailRecipients(userID, campaign.ID, input.TargetAudience, input.PartyIDs, input.EmailAddresses)

	if len(recipients) > 0 {
		if err := utils.DB.Create(&recipients).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create recipients"})
			return
		}
	}

	// Update campaign with recipient count
	campaign.TotalRecipients = len(recipients)
	utils.DB.Save(&campaign)

	// If the scheduled date is in the past, send right away. SMTP must be
	// configured; otherwise the campaign stays in "sent" state with failed
	// recipients so the user can see what happened.
	if status == "sent" {
		if utils.EmailConfiguredForUser(userID) {
			now := time.Now()
			campaign.LastSentAt = &now
			utils.DB.Save(&campaign)
			executeEmailCampaignSend(&campaign)
			// For recurring campaigns, schedule the next occurrence.
			if campaign.IsRecurring {
				scheduleNextRecurrence(&campaign)
			}
		} else {
			// Mark as failed so the user knows SMTP is not set up.
			campaign.Status = "failed"
			utils.DB.Save(&campaign)
		}
	}

	// Reload with recipients
	utils.DB.Preload("Recipients").First(&campaign, campaign.ID)

	c.JSON(http.StatusCreated, campaign)
}

func UpdateEmailCampaign(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var input struct {
		CampaignName        string      `json:"campaign_name"`
		Subject             string      `json:"subject"`
		Body                string      `json:"body"`
		TargetAudience      string      `json:"target_audience"`
		ScheduledDate       *time.Time  `json:"scheduled_date"`
		PartyIDs            []uuid.UUID `json:"party_ids"`
		EmailAddresses      []string    `json:"email_addresses"`
		Notes               string      `json:"notes"`
		IsRecurring         *bool       `json:"is_recurring"`
		RecurrenceFrequency string      `json:"recurrence_frequency"`
		RecurrenceInterval  *int        `json:"recurrence_interval"`
		RecurrenceEndDate   *time.Time  `json:"recurrence_end_date"`
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

	// Allow editing until the campaign has actually been sent (one-off).
	// Recurring campaigns in "scheduled" or "completed" state can still be
	// edited so the user can tweak the next occurrence or recurrence rules.
	if campaign.Status == "sent" && !campaign.IsRecurring {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot edit a sent campaign"})
		return
	}

	// Validate recurrence settings if enabling recurring mode.
	willRecur := campaign.IsRecurring
	if input.IsRecurring != nil {
		willRecur = *input.IsRecurring
	}
	if willRecur {
		freq := campaign.RecurrenceFrequency
		if input.RecurrenceFrequency != "" {
			freq = input.RecurrenceFrequency
		}
		if freq != "daily" && freq != "weekly" && freq != "monthly" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Recurrence frequency must be daily, weekly, or monthly"})
			return
		}
		sched := campaign.ScheduledDate
		if input.ScheduledDate != nil {
			sched = input.ScheduledDate
		}
		if sched == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Recurring campaigns require a scheduled date"})
			return
		}
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
	if input.Notes != "" {
		updates["notes"] = input.Notes
	}
	if input.IsRecurring != nil {
		updates["is_recurring"] = *input.IsRecurring
	}
	if input.RecurrenceFrequency != "" {
		updates["recurrence_frequency"] = input.RecurrenceFrequency
	}
	if input.RecurrenceInterval != nil {
		updates["recurrence_interval"] = *input.RecurrenceInterval
	}
	if input.RecurrenceEndDate != nil {
		updates["recurrence_end_date"] = input.RecurrenceEndDate
	}

	// Scheduled date handling: a nil value means "don't change". A future
	// date keeps it scheduled. A past date marks it for immediate send.
	if input.ScheduledDate != nil {
		updates["scheduled_date"] = input.ScheduledDate
	}

	if err := utils.DB.Model(&campaign).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update email campaign"})
		return
	}

	// Reload the campaign to pick up the updated fields.
	utils.DB.First(&campaign, campaign.ID)

	// Rebuild recipients when the target audience or recipient selection
	// changes. We treat presence of party_ids/email_addresses or a changed
	// target_audience as a signal to rebuild.
	rebuildRecipients := input.TargetAudience != "" || len(input.PartyIDs) > 0 || len(input.EmailAddresses) > 0
	if rebuildRecipients {
		targetAudience := campaign.TargetAudience
		if input.TargetAudience != "" {
			targetAudience = input.TargetAudience
		}

		// Remove existing recipients.
		utils.DB.Where("campaign_id = ?", campaign.ID).Delete(&models.EmailRecipient{})

		recipients := buildEmailRecipients(userID, campaign.ID, targetAudience, input.PartyIDs, input.EmailAddresses)
		if len(recipients) > 0 {
			if err := utils.DB.Create(&recipients).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update recipients"})
				return
			}
		}

		campaign.TotalRecipients = len(recipients)
		utils.DB.Save(&campaign)
	}

	// Recompute status based on the (possibly updated) scheduled date.
	if input.ScheduledDate != nil {
		if input.ScheduledDate.Before(time.Now()) {
			// Send immediately if SMTP is configured, otherwise mark failed.
			if utils.EmailConfiguredForUser(userID) {
				now := time.Now()
				campaign.LastSentAt = &now
				utils.DB.Save(&campaign)
				executeEmailCampaignSend(&campaign)
				if campaign.IsRecurring {
					scheduleNextRecurrence(&campaign)
				}
			} else {
				campaign.Status = "failed"
				utils.DB.Save(&campaign)
			}
		} else {
			campaign.Status = "scheduled"
			utils.DB.Save(&campaign)
		}
	} else if campaign.IsRecurring && campaign.Status == "completed" {
		// Re-activating a completed recurring campaign: recompute the next
		// occurrence from now so it can resume.
		next := advanceRecurrence(&campaign, time.Now())
		if next == nil {
			campaign.Status = "completed"
		} else {
			campaign.Status = "scheduled"
			campaign.ScheduledDate = next
		}
		utils.DB.Save(&campaign)
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

	// Allow deletion for draft, scheduled, failed, and completed (recurring)
	// campaigns. Sent one-off campaigns are locked.
	if campaign.Status == "sent" && !campaign.IsRecurring {
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

	// Verify SMTP is configured for this user (store) before attempting to send.
	if !utils.EmailConfiguredForUser(userID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "SMTP is not configured. Configure email settings in Developer Settings before sending campaigns."})
		return
	}

	executeEmailCampaignSend(&campaign)

	utils.DB.Preload("Recipients").First(&campaign, campaign.ID)
	c.JSON(http.StatusOK, campaign)
}

// ResendEmailCampaign re-sends an already sent (or failed) campaign to all of
// its existing recipients. Recipient statuses are reset to "pending" and the
// campaign's send stats are recomputed from the new send attempt.
func ResendEmailCampaign(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var campaign models.EmailMarketing
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&campaign).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Email campaign not found"})
		return
	}

	// Only allow re-sending campaigns that have been sent (or failed) before.
	if campaign.Status != "sent" && campaign.Status != "failed" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Only sent or failed campaigns can be re-sent"})
		return
	}

	// Verify SMTP is configured for this user (store) before attempting to send.
	if !utils.EmailConfiguredForUser(userID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "SMTP is not configured. Configure email settings in Developer Settings before sending campaigns."})
		return
	}

	// Reset recipients so the new send attempt is reflected per-recipient.
	utils.DB.Model(&models.EmailRecipient{}).
		Where("campaign_id = ?", campaign.ID).
		Updates(map[string]interface{}{
			"status":        "pending",
			"error_message": "",
			"sent_at":       nil,
			"opened_at":     nil,
			"clicked_at":    nil,
		})

	// Reset aggregate stats so the new send attempt's counts are accurate.
	campaign.SentCount = 0
	campaign.FailedCount = 0
	campaign.OpenedCount = 0
	campaign.ClickedCount = 0
	utils.DB.Save(&campaign)

	executeEmailCampaignSend(&campaign)

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
		"total_campaigns":     totalCampaigns,
		"sent_campaigns":      sentCampaigns,
		"scheduled_campaigns": scheduledCampaigns,
		"total_sent":          totalSent,
		"total_failed":        totalFailed,
		"total_opened":        totalOpened,
		"total_clicked":       totalClicked,
	})
}
