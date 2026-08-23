package controllers

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// parseTargetChats splits a comma/newline/space separated string into a clean
// list of trimmed, non-empty Telegram chat targets (numeric chat IDs or
// @channelusernames) with duplicates removed.
func parseTargetChats(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == ';' || r == ' ' || r == '\t' || r == '\r'
	})
	seen := make(map[string]bool, len(parts))
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		chat := strings.TrimSpace(p)
		if chat == "" || seen[chat] {
			continue
		}
		seen[chat] = true
		out = append(out, chat)
	}
	return out
}

// GetOrCreateDailyReportTelegramSettings returns the user's settings row,
// creating a disabled default row if one does not yet exist.
func GetOrCreateDailyReportTelegramSettings(userID uuid.UUID) (models.DailyReportTelegramSettings, error) {
	var settings models.DailyReportTelegramSettings
	err := utils.DB.Where("user_id = ?", userID).First(&settings).Error
	if err == nil {
		return settings, nil
	}
	settings = models.DailyReportTelegramSettings{
		ID:       uuid.New(),
		UserID:   userID,
		Period:   "daily",
		SendTime: "09:00",
	}
	if createErr := utils.DB.Create(&settings).Error; createErr != nil {
		return settings, createErr
	}
	return settings, nil
}

// GetDailyReportTelegramSettingsHandler returns the current user's report
// Telegram configuration (or a disabled default if none exists yet).
func GetDailyReportTelegramSettingsHandler(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	settings, err := GetOrCreateDailyReportTelegramSettings(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load report Telegram settings"})
		return
	}
	c.JSON(http.StatusOK, settings)
}

// UpdateDailyReportTelegramSettingsHandler enables/configures automatic
// daily/periodic report PDF delivery to a list of Telegram chats via the
// configured bot.
func UpdateDailyReportTelegramSettingsHandler(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		IsEnabled   bool   `json:"is_enabled"`
		TargetChats string `json:"target_chats"`
		Period      string `json:"period"`
		SendTime    string `json:"send_time"`
		Caption     string `json:"caption"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	period := strings.ToLower(strings.TrimSpace(input.Period))
	if period == "" {
		period = "daily"
	}
	if !validReportEmailPeriods[period] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "period must be today, daily, weekly, or monthly"})
		return
	}

	sendTime := strings.TrimSpace(input.SendTime)
	if sendTime == "" {
		sendTime = "09:00"
	}
	if _, err := time.Parse("15:04", sendTime); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "send_time must be in HH:MM (24h) format, e.g. 09:00"})
		return
	}

	chats := parseTargetChats(input.TargetChats)
	if input.IsEnabled && len(chats) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "At least one Telegram chat target is required when enabled"})
		return
	}

	settings, err := GetOrCreateDailyReportTelegramSettings(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load report Telegram settings"})
		return
	}

	settings.IsEnabled = input.IsEnabled
	settings.TargetChats = strings.Join(chats, ", ")
	settings.Period = period
	settings.SendTime = sendTime
	settings.Caption = strings.TrimSpace(input.Caption)

	if err := utils.DB.Save(&settings).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save report Telegram settings"})
		return
	}

	c.JSON(http.StatusOK, settings)
}

// reportTelegramCaption builds the caption for a scheduled report send,
// using the user's custom caption if provided, otherwise a sensible default.
func reportTelegramCaption(settings models.DailyReportTelegramSettings, label string) string {
	if c := strings.TrimSpace(settings.Caption); c != "" {
		return c
	}
	var business models.Business
	name := ""
	if err := utils.DB.Where("user_id = ?", settings.UserID).First(&business).Error; err == nil {
		name = business.Name
	}
	periodLabel := strings.ToUpper(settings.Period[:1]) + settings.Period[1:]
	if name != "" {
		return fmt.Sprintf("%s Report — %s — %s", periodLabel, name, label)
	}
	return fmt.Sprintf("%s Business Report — %s", periodLabel, label)
}

// sendReportTelegramNow generates and sends the report PDF for the given date
// to all configured Telegram chats. Returns the count of successful sends and
// the first error encountered (if any). Updates LastSent* fields on settings.
// When isScheduled is true, LastScheduledAt is also updated so the scheduler
// can track its own sends independently of manual "Send now" tests.
func sendReportTelegramNow(settings models.DailyReportTelegramSettings, anchor time.Time, isScheduled bool) (int, error) {
	chats := parseTargetChats(settings.TargetChats)
	if len(chats) == 0 {
		return 0, fmt.Errorf("no Telegram chat targets configured")
	}
	if !utils.TelegramConfiguredForUser(settings.UserID) {
		return 0, fmt.Errorf("Telegram bot is not configured. Set a bot token in Developer Settings first.")
	}

	cfg, err := utils.GetTelegramConfigForUser(settings.UserID)
	if err != nil {
		return 0, fmt.Errorf("failed to load Telegram bot config: %w", err)
	}

	// Reuse the email PDF builder (it only depends on settings.UserID, period,
	// and the anchor date) so the Telegram PDF matches the email PDF exactly.
	emailSettings := models.DailyReportEmailSettings{
		UserID: settings.UserID,
		Period: settings.Period,
	}
	filename, label, pdfBytes, err := buildReportEmailPDF(emailSettings, anchor)
	if err != nil {
		return 0, fmt.Errorf("failed to build report PDF: %w", err)
	}

	caption := reportTelegramCaption(settings, label)

	sentCount := 0
	var firstErr error
	for _, chat := range chats {
		if err := utils.SendTelegramDocument(cfg.BotToken, chat, filename, pdfBytes, caption); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("failed to send to %s: %w", chat, err)
			}
			log.Printf("daily report telegram: send to %s failed: %v", chat, err)
			continue
		}
		sentCount++
	}

	now := time.Now()
	settings.LastSentAt = &now
	if isScheduled {
		settings.LastScheduledAt = &now
	}
	if firstErr != nil && sentCount == 0 {
		settings.LastSentStatus = "failed"
		settings.LastSentError = firstErr.Error()
	} else if firstErr != nil {
		settings.LastSentStatus = "partial"
		settings.LastSentError = firstErr.Error()
	} else {
		settings.LastSentStatus = "success"
		settings.LastSentError = ""
	}
	utils.DB.Save(&settings)

	return sentCount, firstErr
}

// SendDailyReportTelegramNowHandler manually triggers a report Telegram send
// for the given date. Useful for testing the configuration without waiting
// for the scheduler.
func SendDailyReportTelegramNowHandler(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	settings, err := GetOrCreateDailyReportTelegramSettings(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load report Telegram settings"})
		return
	}

	chats := parseTargetChats(settings.TargetChats)
	if len(chats) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No Telegram chat targets configured. Add chats in the settings first."})
		return
	}

	anchor := time.Now().AddDate(0, 0, -1)
	if strings.EqualFold(settings.Period, "today") {
		anchor = time.Now()
	}
	if dateStr := strings.TrimSpace(c.Query("date")); dateStr != "" {
		if parsed, err := time.Parse("2006-01-02", dateStr); err == nil {
			anchor = parsed
		}
	}

	sentCount, sendErr := sendReportTelegramNow(settings, anchor, false)
	if sendErr != nil && sentCount == 0 {
		c.JSON(http.StatusInternalServerError, gin.H{"error": sendErr.Error()})
		return
	}

	utils.DB.Where("user_id = ?", userID).First(&settings)

	c.JSON(http.StatusOK, gin.H{
		"sent_count":  sentCount,
		"total":       len(chats),
		"settings":    settings,
		"warning":     sendErr != nil,
		"warning_msg": sendErr,
	})
}

// StartDailyReportTelegramScheduler launches a background goroutine that
// periodically checks each enabled DailyReportTelegramSettings row and sends
// the configured report PDF when the configured SendTime is reached.
func StartDailyReportTelegramScheduler() {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("daily report telegram scheduler: PANIC recovered: %v", r)
			}
		}()

		log.Printf("daily report telegram scheduler: started (server time: %s, timezone: %s)",
			time.Now().Format("2006-01-02 15:04:05"), time.Local.String())

		// Run once immediately on startup so we catch up if the send time
		// already passed today while the server was down.
		processDueReportTelegrams()

		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("daily report telegram scheduler: PANIC in tick: %v", r)
					}
				}()
				processDueReportTelegrams()
			}()
		}
	}()
}

// processDueReportTelegrams finds enabled settings whose SendTime has been
// reached today and which have not already been sent today by the scheduler,
// then sends the report PDF for the previous day/week/month to the configured
// Telegram chats. SendTime and "today" are interpreted in each user's
// configured timezone, falling back to the server's local timezone.
func processDueReportTelegrams() {
	serverNow := time.Now()

	var allSettings []models.DailyReportTelegramSettings
	if err := utils.DB.Where("is_enabled = ?", true).Find(&allSettings).Error; err != nil {
		log.Printf("daily report telegram scheduler: query failed: %v", err)
		return
	}

	if len(allSettings) == 0 {
		return
	}

	for i := range allSettings {
		settings := &allSettings[i]

		loc, configured := ConfiguredLocationForUser(settings.UserID)
		now := serverNow.In(loc)
		nowHHMM := now.Format("15:04")
		today := now.Format("2006-01-02")

		tzLabel := loc.String()
		if !configured {
			tzLabel = loc.String() + " (server-default)"
		}

		sendTime := strings.TrimSpace(settings.SendTime)
		if sendTime == "" {
			sendTime = "09:00"
		}

		if sendTime > nowHHMM {
			log.Printf("daily report telegram scheduler: user %s — not yet time (tz=%s, send_time=%s, now=%s)",
				settings.UserID, tzLabel, sendTime, nowHHMM)
			continue
		}

		if settings.LastScheduledAt != nil && settings.LastScheduledAt.In(loc).Format("2006-01-02") == today {
			log.Printf("daily report telegram scheduler: user %s — already scheduled-sent today (tz=%s)", settings.UserID, tzLabel)
			continue
		}

		if !utils.TelegramConfiguredForUser(settings.UserID) {
			log.Printf("daily report telegram scheduler: skipping user %s — Telegram bot not configured", settings.UserID)
			continue
		}

		anchor := now.AddDate(0, 0, -1)
		if strings.EqualFold(settings.Period, "today") {
			anchor = now
		}

		log.Printf("daily report telegram scheduler: sending report for user %s (tz=%s, period=%s, send_time=%s, anchor=%s)",
			settings.UserID, tzLabel, settings.Period, sendTime, anchor.Format("2006-01-02"))

		sentCount, err := sendReportTelegramNow(*settings, anchor, true)
		if err != nil && sentCount == 0 {
			log.Printf("daily report telegram scheduler: send for user %s failed: %v", settings.UserID, err)
		} else {
			log.Printf("daily report telegram scheduler: sent %d report telegram(s) for user %s", sentCount, settings.UserID)
		}
	}
}
