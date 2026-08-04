package controllers

import (
	"truerp/models"
	"truerp/utils"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func GetNotifications(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var notifications []models.Notification
	query := utils.DB.Where("user_id = ?", userID)

	if status := c.Query("status"); status != "" {
		query = query.Where("status = ?", status)
	}
	if isRead := c.Query("is_read"); isRead != "" {
		query = query.Where("is_read = ?", isRead == "true")
	}
	if notificationType := c.Query("type"); notificationType != "" {
		query = query.Where("type = ?", notificationType)
	}

	if err := query.Order("created_at DESC").Find(&notifications).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch notifications"})
		return
	}

	c.JSON(http.StatusOK, notifications)
}

func GetNotification(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var notification models.Notification
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&notification).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Notification not found"})
		return
	}

	c.JSON(http.StatusOK, notification)
}

func MarkAsRead(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var notification models.Notification
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&notification).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Notification not found"})
		return
	}

	now := time.Now()
	notification.IsRead = true
	notification.ReadAt = &now
	utils.DB.Save(&notification)

	c.JSON(http.StatusOK, notification)
}

func MarkAllAsRead(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	now := time.Now()
	utils.DB.Model(&models.Notification{}).
		Where("user_id = ? AND is_read = ?", userID, false).
		Updates(map[string]interface{}{
			"is_read":  true,
			"read_at":  now,
		})

	c.JSON(http.StatusOK, gin.H{"message": "All notifications marked as read"})
}

func CreateNotification(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		Type         string     `json:"type" binding:"required"`
		Title        string     `json:"title" binding:"required"`
		Message      string     `json:"message" binding:"required"`
		Channels     string     `json:"channels" binding:"required"` // email,sms,whatsapp,internal
		RelatedID    *uuid.UUID `json:"related_id"`
		RelatedType  string     `json:"related_type"`
		Priority     string     `json:"priority"`
		ScheduledAt  *time.Time `json:"scheduled_at"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	notification := models.Notification{
		ID:          uuid.New(),
		UserID:      userID,
		Type:        input.Type,
		Title:       input.Title,
		Message:     input.Message,
		Channels:    input.Channels,
		RelatedID:   input.RelatedID,
		RelatedType: input.RelatedType,
		Priority:    input.Priority,
		Status:      "pending",
		ScheduledAt: input.ScheduledAt,
	}

	if input.ScheduledAt == nil {
		now := time.Now()
		notification.ScheduledAt = &now
	}

	if err := utils.DB.Create(&notification).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create notification"})
		return
	}

	c.JSON(http.StatusCreated, notification)
}

func SendNotification(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var notification models.Notification
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&notification).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Notification not found"})
		return
	}

	if notification.Status == "sent" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Notification already sent"})
		return
	}

	var sentChannels []string
	channels := strings.Split(notification.Channels, ",")

	for _, channel := range channels {
		channel = strings.TrimSpace(channel)
		success := false

		switch channel {
		case "email":
			// TODO: Implement email sending
			success = true
		case "sms":
			// TODO: Implement SMS sending
			success = true
		case "whatsapp":
			// TODO: Implement WhatsApp sending
			success = true
		case "internal":
			// Internal notifications are always successful
			success = true
		}

		if success {
			sentChannels = append(sentChannels, channel)
		}
	}

	now := time.Now()
	notification.SentChannels = strings.Join(sentChannels, ",")
	notification.Status = "sent"
	notification.SentAt = &now

	if err := utils.DB.Save(&notification).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update notification"})
		return
	}

	c.JSON(http.StatusOK, notification)
}

func DeleteNotification(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Delete(&models.Notification{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete notification"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Notification deleted"})
}

// Notification Templates
func GetNotificationTemplates(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var templates []models.NotificationTemplate
	if err := utils.DB.Where("user_id = ?", userID).Order("name ASC").Find(&templates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch templates"})
		return
	}

	c.JSON(http.StatusOK, templates)
}

func CreateNotificationTemplate(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		Name         string `json:"name" binding:"required"`
		Type         string `json:"type" binding:"required"`
		Subject      string `json:"subject"`
		Body         string `json:"body"`
		SMSBody      string `json:"sms_body"`
		WhatsAppBody string `json:"whatsapp_body"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	template := models.NotificationTemplate{
		ID:           uuid.New(),
		UserID:       userID,
		Name:         input.Name,
		Type:         input.Type,
		Subject:      input.Subject,
		Body:         input.Body,
		SMSBody:      input.SMSBody,
		WhatsAppBody: input.WhatsAppBody,
		IsActive:     true,
	}

	if err := utils.DB.Create(&template).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create template"})
		return
	}

	c.JSON(http.StatusCreated, template)
}

func UpdateNotificationTemplate(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var input struct {
		Name         string `json:"name"`
		Subject      string `json:"subject"`
		Body         string `json:"body"`
		SMSBody      string `json:"sms_body"`
		WhatsAppBody string `json:"whatsapp_body"`
		IsActive      bool   `json:"is_active"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var template models.NotificationTemplate
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&template).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Template not found"})
		return
	}

	updates := map[string]interface{}{
		"name":           input.Name,
		"subject":        input.Subject,
		"body":           input.Body,
		"sms_body":       input.SMSBody,
		"whatsapp_body":  input.WhatsAppBody,
		"is_active":      input.IsActive,
	}

	if err := utils.DB.Model(&template).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update template"})
		return
	}

	c.JSON(http.StatusOK, template)
}

func DeleteNotificationTemplate(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Delete(&models.NotificationTemplate{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete template"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Template deleted"})
}

// Notification Preferences
func GetNotificationPreferences(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var preferences []models.NotificationPreference
	if err := utils.DB.Where("user_id = ?", userID).Find(&preferences).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch preferences"})
		return
	}

	c.JSON(http.StatusOK, preferences)
}

func UpdateNotificationPreference(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	notificationType := c.Param("type")

	var input struct {
		IsEnabled       bool `json:"is_enabled"`
		EmailEnabled    bool `json:"email_enabled"`
		SMSEnabled      bool `json:"sms_enabled"`
		WhatsAppEnabled bool `json:"whatsapp_enabled"`
		InternalEnabled bool `json:"internal_enabled"`
		LeadDays        int  `json:"lead_days"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var preference models.NotificationPreference
	if err := utils.DB.Where("user_id = ? AND notification_type = ?", userID, notificationType).First(&preference).Error; err != nil {
		// Create if not exists
		preference = models.NotificationPreference{
			ID:               uuid.New(),
			UserID:           userID,
			NotificationType: notificationType,
		}
	}

	preference.IsEnabled = input.IsEnabled
	preference.EmailEnabled = input.EmailEnabled
	preference.SMSEnabled = input.SMSEnabled
	preference.WhatsAppEnabled = input.WhatsAppEnabled
	preference.InternalEnabled = input.InternalEnabled
	preference.LeadDays = input.LeadDays

	if err := utils.DB.Save(&preference).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update preference"})
		return
	}

	c.JSON(http.StatusOK, preference)
}

// Automation Functions
func SendInvoiceDueReminders(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	// Get preference for invoice_due notifications
	var preference models.NotificationPreference
	utils.DB.Where("user_id = ? AND notification_type = ?", userID, "invoice_due").First(&preference)

	if !preference.IsEnabled || (!preference.EmailEnabled && !preference.SMSEnabled && !preference.WhatsAppEnabled && !preference.InternalEnabled) {
		c.JSON(http.StatusOK, gin.H{"message": "Invoice due reminders are disabled or no channels enabled"})
		return
	}

	// Find invoices due within lead_days
	dueDate := time.Now().AddDate(0, 0, preference.LeadDays)
	var invoices []models.Invoice
	utils.DB.Where("user_id = ? AND status != 'paid' AND status != 'cancelled' AND due_date = ?", userID, dueDate).
		Preload("Party").
		Find(&invoices)

	var sentCount int
	for _, invoice := range invoices {
		channels := []string{}
		if preference.EmailEnabled {
			channels = append(channels, "email")
		}
		if preference.SMSEnabled {
			channels = append(channels, "sms")
		}
		if preference.WhatsAppEnabled {
			channels = append(channels, "whatsapp")
		}
		if preference.InternalEnabled {
			channels = append(channels, "internal")
		}

		notification := models.Notification{
			ID:          uuid.New(),
			UserID:      userID,
			Type:        "invoice_due",
			Title:       fmt.Sprintf("Invoice %s Due Soon", invoice.InvoiceNumber),
			Message:     fmt.Sprintf("Invoice %s for %s is due on %s. Amount: ₹%.2f", invoice.InvoiceNumber, invoice.Party.Name, invoice.DueDate.Format("02-01-2006"), invoice.TotalAmount),
			Channels:    strings.Join(channels, ","),
			RelatedID:   &invoice.ID,
			RelatedType: "invoice",
			Priority:    "normal",
			Status:      "pending",
		}

		now := time.Now()
		notification.ScheduledAt = &now
		utils.DB.Create(&notification)
		sentCount++
	}

	c.JSON(http.StatusOK, gin.H{
		"message":    fmt.Sprintf("Sent %d invoice due reminders", sentCount),
		"sent_count": sentCount,
	})
}

func SendPaymentReminders(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	// Get preference for payment_due notifications
	var preference models.NotificationPreference
	utils.DB.Where("user_id = ? AND notification_type = ?", userID, "payment_due").First(&preference)

	if !preference.IsEnabled || (!preference.EmailEnabled && !preference.SMSEnabled && !preference.WhatsAppEnabled && !preference.InternalEnabled) {
		c.JSON(http.StatusOK, gin.H{"message": "Payment reminders are disabled or no channels enabled"})
		return
	}

	// Find overdue invoices
	var invoices []models.Invoice
	utils.DB.Where("user_id = ? AND status != 'paid' AND status != 'cancelled' AND due_date < ?", userID, time.Now()).
		Preload("Party").
		Find(&invoices)

	var sentCount int
	for _, invoice := range invoices {
		channels := []string{}
		if preference.EmailEnabled {
			channels = append(channels, "email")
		}
		if preference.SMSEnabled {
			channels = append(channels, "sms")
		}
		if preference.WhatsAppEnabled {
			channels = append(channels, "whatsapp")
		}
		if preference.InternalEnabled {
			channels = append(channels, "internal")
		}

		daysOverdue := int(time.Now().Sub(*invoice.DueDate).Hours() / 24)
		notification := models.Notification{
			ID:          uuid.New(),
			UserID:      userID,
			Type:        "overdue",
			Title:       fmt.Sprintf("Invoice %s Overdue", invoice.InvoiceNumber),
			Message:     fmt.Sprintf("Invoice %s for %s is overdue by %d days. Amount: ₹%.2f", invoice.InvoiceNumber, invoice.Party.Name, daysOverdue, invoice.TotalAmount),
			Channels:    strings.Join(channels, ","),
			RelatedID:   &invoice.ID,
			RelatedType: "invoice",
			Priority:    "high",
			Status:      "pending",
		}

		now := time.Now()
		notification.ScheduledAt = &now
		utils.DB.Create(&notification)
		sentCount++
	}

	c.JSON(http.StatusOK, gin.H{
		"message":    fmt.Sprintf("Sent %d payment reminders", sentCount),
		"sent_count": sentCount,
	})
}

func SendOverdueNotifications(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	// Get preference for overdue notifications
	var preference models.NotificationPreference
	utils.DB.Where("user_id = ? AND notification_type = ?", userID, "overdue").First(&preference)

	if !preference.IsEnabled || (!preference.EmailEnabled && !preference.SMSEnabled && !preference.WhatsAppEnabled && !preference.InternalEnabled) {
		c.JSON(http.StatusOK, gin.H{"message": "Overdue notifications are disabled or no channels enabled"})
		return
	}

	// Find invoices overdue by more than 30 days
	thirtyDaysAgo := time.Now().AddDate(0, 0, -30)
	var invoices []models.Invoice
	utils.DB.Where("user_id = ? AND status != 'paid' AND status != 'cancelled' AND due_date < ?", userID, thirtyDaysAgo).
		Preload("Party").
		Find(&invoices)

	var sentCount int
	for _, invoice := range invoices {
		channels := []string{}
		if preference.EmailEnabled {
			channels = append(channels, "email")
		}
		if preference.SMSEnabled {
			channels = append(channels, "sms")
		}
		if preference.WhatsAppEnabled {
			channels = append(channels, "whatsapp")
		}
		if preference.InternalEnabled {
			channels = append(channels, "internal")
		}

		daysOverdue := int(time.Now().Sub(*invoice.DueDate).Hours() / 24)
		notification := models.Notification{
			ID:          uuid.New(),
			UserID:      userID,
			Type:        "overdue",
			Title:       fmt.Sprintf("Invoice %s Seriously Overdue", invoice.InvoiceNumber),
			Message:     fmt.Sprintf("Invoice %s for %s is seriously overdue by %d days. Amount: ₹%.2f. Immediate action required.", invoice.InvoiceNumber, invoice.Party.Name, daysOverdue, invoice.TotalAmount),
			Channels:    strings.Join(channels, ","),
			RelatedID:   &invoice.ID,
			RelatedType: "invoice",
			Priority:    "urgent",
			Status:      "pending",
		}

		now := time.Now()
		notification.ScheduledAt = &now
		utils.DB.Create(&notification)
		sentCount++
	}

	c.JSON(http.StatusOK, gin.H{
		"message":    fmt.Sprintf("Sent %d overdue notifications", sentCount),
		"sent_count": sentCount,
	})
}
