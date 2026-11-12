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

// -----------------------------------------------------------------------------
// Party Balance Report — scheduled email delivery
//
// Mirrors the Daily Report email automation: one settings row per user, a
// background scheduler that checks every minute and sends the report PDF once
// per day when the user's configured-timezone clock reaches SendTime, plus a
// manual "Send now" test that does not count as the scheduled send.
// -----------------------------------------------------------------------------

var validPartyBalanceReportDays = map[string]bool{"today": true, "previous_day": true}

func getOrCreatePartyBalanceReportEmailSettings(userID uuid.UUID) (models.PartyBalanceReportEmailSettings, error) {
	var settings models.PartyBalanceReportEmailSettings
	if err := utils.DB.Where("user_id = ?", userID).First(&settings).Error; err == nil {
		return settings, nil
	}
	settings = models.PartyBalanceReportEmailSettings{
		ID:        uuid.New(),
		UserID:    userID,
		ReportDay: "today",
		PartyType: "all",
		SendTime:  "21:00",
	}
	err := utils.DB.Create(&settings).Error
	return settings, err
}

func GetPartyBalanceReportEmailSettingsHandler(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	settings, err := getOrCreatePartyBalanceReportEmailSettings(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load report email settings"})
		return
	}
	c.JSON(http.StatusOK, settings)
}

func UpdatePartyBalanceReportEmailSettingsHandler(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		IsEnabled       bool   `json:"is_enabled"`
		RecipientEmails string `json:"recipient_emails"`
		ReportDay       string `json:"report_day"`
		PartyType       string `json:"party_type"`
		SendTime        string `json:"send_time"`
		Subject         string `json:"subject"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	reportDay := strings.ToLower(strings.TrimSpace(input.ReportDay))
	if reportDay == "" {
		reportDay = "today"
	}
	if !validPartyBalanceReportDays[reportDay] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "report_day must be today or previous_day"})
		return
	}

	partyType := strings.ToLower(strings.TrimSpace(input.PartyType))
	if partyType != "customer" && partyType != "vendor" {
		partyType = "all"
	}

	sendTime := strings.TrimSpace(input.SendTime)
	if sendTime == "" {
		sendTime = "21:00"
	}
	if _, err := time.Parse("15:04", sendTime); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "send_time must be in HH:MM (24h) format, e.g. 21:00"})
		return
	}

	recipients := parseRecipientEmails(input.RecipientEmails)
	if input.IsEnabled && len(recipients) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "At least one recipient email is required when enabled"})
		return
	}

	settings, err := getOrCreatePartyBalanceReportEmailSettings(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load report email settings"})
		return
	}
	settings.IsEnabled = input.IsEnabled
	settings.RecipientEmails = strings.Join(recipients, ", ")
	settings.ReportDay = reportDay
	settings.PartyType = partyType
	settings.SendTime = sendTime
	settings.Subject = strings.TrimSpace(input.Subject)

	if err := utils.DB.Save(&settings).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save report email settings"})
		return
	}
	c.JSON(http.StatusOK, settings)
}

// partyBalanceEmailDate resolves the day the report covers for a send at now.
func partyBalanceEmailDate(settings models.PartyBalanceReportEmailSettings, now time.Time) string {
	if settings.ReportDay == "previous_day" {
		return now.AddDate(0, 0, -1).Format("2006-01-02")
	}
	return now.Format("2006-01-02")
}

// sendPartyBalanceReportEmail builds the PDF for date and sends it to every
// configured recipient, recording the outcome on the settings row.
func sendPartyBalanceReportEmail(settings models.PartyBalanceReportEmailSettings, date string, isScheduled bool) (int, error) {
	recipients := parseRecipientEmails(settings.RecipientEmails)
	if len(recipients) == 0 {
		return 0, fmt.Errorf("no recipient emails configured")
	}
	if !utils.EmailConfiguredForUser(settings.UserID) {
		return 0, fmt.Errorf("SMTP is not configured. Set up email in Developer Settings first.")
	}

	report, err := loadPartyBalanceReport(settings.UserID, date, settings.PartyType, "")
	if err != nil {
		return 0, fmt.Errorf("failed to load party balance report: %w", err)
	}
	pdfBytes, err := buildPartyBalanceReportPDF(report)
	if err != nil {
		return 0, fmt.Errorf("failed to build report PDF: %w", err)
	}

	dateLabel := date
	if t, err := time.Parse("2006-01-02", date); err == nil {
		dateLabel = t.Format("02 Jan 2006")
	}
	subject := strings.TrimSpace(settings.Subject)
	if subject == "" {
		if report.BusinessName != "" {
			subject = fmt.Sprintf("Party Balance Report — %s — %s", report.BusinessName, dateLabel)
		} else {
			subject = "Party Balance Report — " + dateLabel
		}
	}
	body := fmt.Sprintf(`<p>Hello,</p>
<p>Please find attached the party balance report for <strong>%s</strong>.</p>
<p>%d parties with an outstanding balance.<br>
Opening — receivable: %s, payable: %s<br>
Closing — receivable: %s, payable: %s</p>
<p>This report was generated and sent automatically by TruERP.</p>
<p>— TruERP</p>`,
		dateLabel, len(report.Lines),
		fmtINR(report.OpeningReceivable), fmtINR(report.OpeningPayable),
		fmtINR(report.ClosingReceivable), fmtINR(report.ClosingPayable))

	attachment := utils.PDFAttachment{Filename: fmt.Sprintf("party_balance_report_%s.pdf", date), Content: pdfBytes}

	sentCount := 0
	var firstErr error
	for _, to := range recipients {
		if err := utils.SendEmailWithPDFAttachmentForUser(settings.UserID, to, subject, body, []utils.PDFAttachment{attachment}); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("failed to send to %s: %w", to, err)
			}
			log.Printf("party balance report email: send to %s failed: %v", to, err)
			continue
		}
		sentCount++
	}

	now := time.Now()
	settings.LastSentAt = &now
	if isScheduled {
		settings.LastScheduledAt = &now
	}
	switch {
	case firstErr != nil && sentCount == 0:
		settings.LastSentStatus = "failed"
		settings.LastSentError = firstErr.Error()
	case firstErr != nil:
		settings.LastSentStatus = "partial"
		settings.LastSentError = firstErr.Error()
	default:
		settings.LastSentStatus = "success"
		settings.LastSentError = ""
	}
	utils.DB.Save(&settings)

	return sentCount, firstErr
}

// SendPartyBalanceReportEmailNowHandler sends the report immediately (for
// testing). ?date=YYYY-MM-DD overrides the configured report day.
func SendPartyBalanceReportEmailNowHandler(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	settings, err := getOrCreatePartyBalanceReportEmailSettings(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load report email settings"})
		return
	}
	recipients := parseRecipientEmails(settings.RecipientEmails)
	if len(recipients) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No recipient emails configured. Add recipients in the settings first."})
		return
	}

	loc, _ := ConfiguredLocationForUser(userID)
	date := partyBalanceEmailDate(settings, time.Now().In(loc))
	if d := strings.TrimSpace(c.Query("date")); d != "" {
		if _, err := time.Parse("2006-01-02", d); err == nil {
			date = d
		}
	}

	sentCount, sendErr := sendPartyBalanceReportEmail(settings, date, false)
	if sendErr != nil && sentCount == 0 {
		c.JSON(http.StatusInternalServerError, gin.H{"error": sendErr.Error()})
		return
	}
	utils.DB.Where("user_id = ?", userID).First(&settings)

	warningMsg := ""
	if sendErr != nil {
		warningMsg = sendErr.Error()
	}
	c.JSON(http.StatusOK, gin.H{
		"sent_count":  sentCount,
		"total":       len(recipients),
		"settings":    settings,
		"warning":     sendErr != nil,
		"warning_msg": warningMsg,
	})
}

// StartPartyBalanceReportEmailScheduler launches the background loop. Call
// once at startup; it checks immediately and then every 60 seconds.
func StartPartyBalanceReportEmailScheduler() {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("party balance report email scheduler: PANIC recovered: %v", r)
			}
		}()
		processDuePartyBalanceReportEmails()
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("party balance report email scheduler: PANIC in tick: %v", r)
					}
				}()
				processDuePartyBalanceReportEmails()
			}()
		}
	}()
}

// processDuePartyBalanceReportEmails sends each enabled user's report once per
// day after SendTime, evaluated in the user's configured timezone (falling back
// to server-local). Manual "Send now" tests don't suppress the scheduled send.
func processDuePartyBalanceReportEmails() {
	var all []models.PartyBalanceReportEmailSettings
	if err := utils.DB.Where("is_enabled = ?", true).Find(&all).Error; err != nil {
		log.Printf("party balance report email scheduler: query failed: %v", err)
		return
	}
	serverNow := time.Now()
	for i := range all {
		settings := all[i]
		loc, _ := ConfiguredLocationForUser(settings.UserID)
		now := serverNow.In(loc)

		sendTime := strings.TrimSpace(settings.SendTime)
		if sendTime == "" {
			sendTime = "21:00"
		}
		if sendTime > now.Format("15:04") {
			continue
		}
		if settings.LastScheduledAt != nil && settings.LastScheduledAt.In(loc).Format("2006-01-02") == now.Format("2006-01-02") {
			continue
		}
		if !utils.EmailConfiguredForUser(settings.UserID) {
			log.Printf("party balance report email scheduler: skipping user %s — SMTP not configured", settings.UserID)
			continue
		}

		date := partyBalanceEmailDate(settings, now)
		sentCount, err := sendPartyBalanceReportEmail(settings, date, true)
		if err != nil && sentCount == 0 {
			log.Printf("party balance report email scheduler: send for user %s failed: %v", settings.UserID, err)
		} else {
			log.Printf("party balance report email scheduler: sent %d email(s) for user %s (date=%s)", sentCount, settings.UserID, date)
		}
	}
}
