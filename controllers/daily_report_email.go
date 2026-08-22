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

// validReportEmailPeriods are the periods supported for scheduled report
// email delivery. The scheduler generates the corresponding periodic report
// PDF (today = current day, daily = previous day, weekly = last week, monthly = last month).
var validReportEmailPeriods = map[string]bool{
	"today":   true,
	"daily":   true,
	"weekly":  true,
	"monthly": true,
}

// parseRecipientEmails splits a comma/newline separated string into a clean
// list of trimmed, lowercased, non-empty email addresses with duplicates removed.
func parseRecipientEmails(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == ';' || r == ' ' || r == '\t' || r == '\r'
	})
	seen := make(map[string]bool, len(parts))
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		email := strings.ToLower(strings.TrimSpace(p))
		if email == "" || seen[email] {
			continue
		}
		if !strings.Contains(email, "@") || !strings.Contains(email, ".") {
			continue
		}
		seen[email] = true
		out = append(out, email)
	}
	return out
}

// GetOrCreateDailyReportEmailSettings returns the user's settings row,
// creating a disabled default row if one does not yet exist.
func GetOrCreateDailyReportEmailSettings(userID uuid.UUID) (models.DailyReportEmailSettings, error) {
	var settings models.DailyReportEmailSettings
	err := utils.DB.Where("user_id = ?", userID).First(&settings).Error
	if err == nil {
		return settings, nil
	}
	settings = models.DailyReportEmailSettings{
		ID:     uuid.New(),
		UserID: userID,
		Period: "daily",
		SendTime: "09:00",
	}
	if createErr := utils.DB.Create(&settings).Error; createErr != nil {
		return settings, createErr
	}
	return settings, nil
}

// GetDailyReportEmailSettingsHandler returns the current user's report email
// configuration (or a disabled default if none exists yet).
func GetDailyReportEmailSettingsHandler(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	settings, err := GetOrCreateDailyReportEmailSettings(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load report email settings"})
		return
	}
	c.JSON(http.StatusOK, settings)
}

// UpdateDailyReportEmailSettingsHandler enables/configures automatic daily
// report PDF email delivery. RecipientEmails is a comma/newline separated
// string; Period must be daily/weekly/monthly; SendTime is HH:MM (24h).
func UpdateDailyReportEmailSettingsHandler(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		IsEnabled       bool   `json:"is_enabled"`
		RecipientEmails string `json:"recipient_emails"`
		Period          string `json:"period"`
		SendTime        string `json:"send_time"`
		Subject         string `json:"subject"`
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
		c.JSON(http.StatusBadRequest, gin.H{"error": "period must be daily, weekly, or monthly"})
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

	recipients := parseRecipientEmails(input.RecipientEmails)
	if input.IsEnabled && len(recipients) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "At least one recipient email is required when enabled"})
		return
	}

	settings, err := GetOrCreateDailyReportEmailSettings(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load report email settings"})
		return
	}

	settings.IsEnabled = input.IsEnabled
	settings.RecipientEmails = strings.Join(recipients, ", ")
	settings.Period = period
	settings.SendTime = sendTime
	settings.Subject = strings.TrimSpace(input.Subject)

	if err := utils.DB.Save(&settings).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save report email settings"})
		return
	}

	c.JSON(http.StatusOK, settings)
}

// reportEmailSubject builds the email subject for a scheduled report send,
// using the user's custom subject if provided, otherwise a sensible default
// that includes the business name and report date/label.
func reportEmailSubject(settings models.DailyReportEmailSettings, reportDate, label string) string {
	if s := strings.TrimSpace(settings.Subject); s != "" {
		return s
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

// buildReportEmailPDF generates the PDF bytes for the configured period using
// the given anchor date (the day the report covers — for daily this is the
// previous day, for weekly the start of last week, etc.).
func buildReportEmailPDF(settings models.DailyReportEmailSettings, anchor time.Time) (filename, label string, pdfBytes []byte, err error) {
	period := settings.Period
	if period == "" {
		period = "daily"
	}

	var start, end time.Time
	switch period {
	case "today":
		start = anchor
		end = anchor
	case "daily":
		start = anchor
		end = anchor
	case "weekly":
		weekday := int(anchor.Weekday())
		if weekday == 0 {
			weekday = 7
		}
		monday := anchor.AddDate(0, 0, -(weekday - 1))
		start = monday
		end = monday.AddDate(0, 0, 6)
	case "monthly":
		start = time.Date(anchor.Year(), anchor.Month(), 1, 0, 0, 0, 0, anchor.Location())
		end = start.AddDate(0, 1, -1)
	default:
		return "", "", nil, fmt.Errorf("unsupported period %q", period)
	}

	startStr := start.Format("2006-01-02")
	endStr := end.Format("2006-01-02")

	report, err := loadReportForRange(settings.UserID, startStr, endStr)
	if err != nil {
		return "", "", nil, fmt.Errorf("load report: %w", err)
	}

	periodReport := models.PeriodReport{
		DailyReport: report,
		Period:      period,
		StartDate:   startStr,
		EndDate:     endStr,
		Label:       labelForPeriod(period, start, end),
	}

	pdfBytes, err = buildPeriodReportPDF(periodReport)
	if err != nil {
		return "", "", nil, fmt.Errorf("build pdf: %w", err)
	}

	filename = fmt.Sprintf("report_%s_%s.pdf", period, startStr)
	return filename, periodReport.Label, pdfBytes, nil
}

func labelForPeriod(period string, start, end time.Time) string {
	switch period {
	case "today":
		return "Today · " + start.Format("02 Jan 2006")
	case "daily":
		return "Daily · " + start.Format("02 Jan 2006")
	case "weekly":
		return fmt.Sprintf("Weekly · %s – %s", start.Format("02 Jan"), end.Format("02 Jan 2006"))
	case "monthly":
		return "Monthly · " + start.Format("Jan 2006")
	default:
		return fmt.Sprintf("%s – %s", start.Format("02 Jan 2006"), end.Format("02 Jan 2006"))
	}
}

// sendReportEmailNow generates and sends the report PDF for the given date to
// all configured recipients. Returns the count of successful sends and the
// first error encountered (if any). Updates LastSent* fields on the settings.
// When isScheduled is true, LastScheduledAt is also updated so the scheduler
// can track its own sends independently of manual "Send now" tests.
func sendReportEmailNow(settings models.DailyReportEmailSettings, anchor time.Time, isScheduled bool) (int, error) {
	recipients := parseRecipientEmails(settings.RecipientEmails)
	if len(recipients) == 0 {
		return 0, fmt.Errorf("no recipient emails configured")
	}
	if !utils.EmailConfiguredForUser(settings.UserID) {
		return 0, fmt.Errorf("SMTP is not configured. Set up email in Developer Settings first.")
	}

	filename, label, pdfBytes, err := buildReportEmailPDF(settings, anchor)
	if err != nil {
		return 0, fmt.Errorf("failed to build report PDF: %w", err)
	}

	subject := reportEmailSubject(settings, anchor.Format("2006-01-02"), label)
	body := fmt.Sprintf(`<p>Hello,</p>
<p>Please find attached the %s business report for <strong>%s</strong>.</p>
<p>Report period: %s</p>
<p>This report was generated and sent automatically by TruERP.</p>
<p>— TruERP</p>`,
		settings.Period, label, label)

	attachment := utils.PDFAttachment{Filename: filename, Content: pdfBytes}

	sentCount := 0
	var firstErr error
	for _, to := range recipients {
		if err := utils.SendEmailWithPDFAttachmentForUser(settings.UserID, to, subject, body, []utils.PDFAttachment{attachment}); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("failed to send to %s: %w", to, err)
			}
			log.Printf("daily report email: send to %s failed: %v", to, err)
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

// SendDailyReportEmailNowHandler manually triggers a report email send for
// the given date (defaults to yesterday for daily, last week/month otherwise).
// Useful for testing the configuration without waiting for the scheduler.
func SendDailyReportEmailNowHandler(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	settings, err := GetOrCreateDailyReportEmailSettings(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load report email settings"})
		return
	}

	recipients := parseRecipientEmails(settings.RecipientEmails)
	if len(recipients) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No recipient emails configured. Add recipients in the settings first."})
		return
	}

	// Default anchor: today for the "today" period, yesterday for daily/weekly/monthly.
	// Allow override via ?date=YYYY-MM-DD for testing a specific day.
	anchor := time.Now().AddDate(0, 0, -1)
	if strings.EqualFold(settings.Period, "today") {
		anchor = time.Now()
	}
	if dateStr := strings.TrimSpace(c.Query("date")); dateStr != "" {
		if parsed, err := time.Parse("2006-01-02", dateStr); err == nil {
			anchor = parsed
		}
	}

	sentCount, sendErr := sendReportEmailNow(settings, anchor, false)
	if sendErr != nil && sentCount == 0 {
		c.JSON(http.StatusInternalServerError, gin.H{"error": sendErr.Error()})
		return
	}

	// Reload to return updated last_sent_* fields.
	utils.DB.Where("user_id = ?", userID).First(&settings)

	c.JSON(http.StatusOK, gin.H{
		"sent_count":   sentCount,
		"total":        len(recipients),
		"settings":     settings,
		"warning":      sendErr != nil,
		"warning_msg":  sendErr,
	})
}

// StartDailyReportEmailScheduler launches a background goroutine that
// periodically checks each enabled DailyReportEmailSettings row and sends the
// configured report PDF when the local clock reaches the configured SendTime.
// It should be called once at application startup. An immediate check runs on
// startup so a send time that already passed today is not missed.
func StartDailyReportEmailScheduler() {
	go func() {
		// Recover from panics so the scheduler goroutine never silently dies.
		defer func() {
			if r := recover(); r != nil {
				log.Printf("daily report email scheduler: PANIC recovered: %v", r)
			}
		}()

		log.Printf("daily report email scheduler: started (server time: %s, timezone: %s)",
			time.Now().Format("2006-01-02 15:04:05"), time.Local.String())

		// Run once immediately on startup so we catch up if the send time
		// already passed today while the server was down.
		processDueReportEmails()

		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("daily report email scheduler: PANIC in tick: %v", r)
					}
				}()
				processDueReportEmails()
			}()
		}
	}()
}

// GetServerTimeHandler returns the server's current local time and timezone
// name, plus the user's configured timezone and the current time expressed in
// that timezone. The daily report email scheduler interprets send_time in the
// configured timezone (falling back to server-local when none is set), so the
// frontend shows the configured clock the scheduler actually uses.
func GetServerTimeHandler(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	now := time.Now()
	zone, offset := now.Zone()

	loc, configured := ConfiguredLocationForUser(userID)
	configuredNow := now.In(loc)
	cZone, cOffset := configuredNow.Zone()

	configuredName := ""
	if configured {
		configuredName = loc.String()
	}

	c.JSON(http.StatusOK, gin.H{
		// Server-local clock (the OS timezone).
		"server_time":      now.Format("2006-01-02 15:04:05"),
		"server_date":      now.Format("2006-01-02"),
		"server_time_hhmm": now.Format("15:04"),
		"timezone":         zone,
		"utc_offset_hours": float64(offset) / 3600.0,
		"timezone_name":    time.Local.String(),

		// Configured timezone clock — this is what the scheduler uses.
		"configured_time":      configuredNow.Format("2006-01-02 15:04:05"),
		"configured_date":      configuredNow.Format("2006-01-02"),
		"configured_time_hhmm": configuredNow.Format("15:04"),
		"configured_timezone":  cZone,
		"configured_timezone_name": configuredName,
		"configured_utc_offset_hours": float64(cOffset) / 3600.0,
		"has_configured_timezone":    configured,

		// Common IANA timezone identifiers the frontend can offer in a dropdown.
		"common_timezones": CommonTimezones,
	})
}

// processDueReportEmails finds enabled settings whose SendTime has been
// reached today and which have not already been sent today by the scheduler,
// then sends the report PDF for the previous day/week/month to the configured
// recipients. Manual "Send now" tests do NOT count as a scheduled send, so
// testing the configuration won't suppress the daily scheduled send.
//
// SendTime and "today" are interpreted in each user's configured timezone
// (DeveloperSettings.Timezone), falling back to the server's local timezone
// when none is configured. This keeps the scheduled send time stable even when
// the server runs in UTC.
func processDueReportEmails() {
	serverNow := time.Now()

	var allSettings []models.DailyReportEmailSettings
	if err := utils.DB.Where("is_enabled = ?", true).Find(&allSettings).Error; err != nil {
		log.Printf("daily report email scheduler: query failed: %v", err)
		return
	}

	if len(allSettings) == 0 {
		return
	}

	log.Printf("daily report email scheduler: checking %d enabled setting(s) at %s (server tz=%s)",
		len(allSettings), serverNow.Format("2006-01-02 15:04:05"), time.Local.String())

	for i := range allSettings {
		settings := &allSettings[i]

		// Resolve the user's configured timezone; fall back to server-local.
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

		// Only send once the configured time has been reached (compare HH:MM
		// in the user's configured timezone).
		if sendTime > nowHHMM {
			log.Printf("daily report email scheduler: user %s — not yet time (tz=%s, send_time=%s, now=%s)",
				settings.UserID, tzLabel, sendTime, nowHHMM)
			continue
		}

		// Skip if the SCHEDULER already sent today (manual "Send now" tests
		// don't count — they only update LastSentAt, not LastScheduledAt).
		// Compare dates in the user's configured timezone so a send just after
		// local midnight isn't double-counted across calendar days.
		if settings.LastScheduledAt != nil && settings.LastScheduledAt.In(loc).Format("2006-01-02") == today {
			log.Printf("daily report email scheduler: user %s — already scheduled-sent today (tz=%s)", settings.UserID, tzLabel)
			continue
		}

		if !utils.EmailConfiguredForUser(settings.UserID) {
			log.Printf("daily report email scheduler: skipping user %s — SMTP not configured", settings.UserID)
			continue
		}

		// Anchor = the day the report should cover, in the user's configured
		// timezone. "today" uses the current day; all other periods use the
		// most recent completed day/week/month (yesterday), so a morning email
		// reflects the prior day's activity.
		anchor := now.AddDate(0, 0, -1)
		if strings.EqualFold(settings.Period, "today") {
			anchor = now
		}

		log.Printf("daily report email scheduler: sending report for user %s (tz=%s, period=%s, send_time=%s, anchor=%s)",
			settings.UserID, tzLabel, settings.Period, sendTime, anchor.Format("2006-01-02"))

		sentCount, err := sendReportEmailNow(*settings, anchor, true)
		if err != nil && sentCount == 0 {
			log.Printf("daily report email scheduler: send for user %s failed: %v", settings.UserID, err)
		} else {
			log.Printf("daily report email scheduler: sent %d report email(s) for user %s", sentCount, settings.UserID)
		}
	}
}
