package controllers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

var notificationEventKeyPattern = regexp.MustCompile(`[^a-z0-9.\-_]+`)

func normalizeNotificationEventKey(raw string) string {
	key := strings.ToLower(strings.TrimSpace(raw))
	key = strings.ReplaceAll(key, " ", ".")
	key = notificationEventKeyPattern.ReplaceAllString(key, "")
	key = strings.Trim(key, ".-_")
	if len(key) > 100 {
		key = key[:100]
	}
	return key
}

func validNotificationChannel(c *gin.Context) (string, bool) {
	channel := strings.ToLower(strings.TrimSpace(c.Param("channel")))
	for _, known := range utils.NotificationChannels {
		if channel == known {
			return channel, true
		}
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("unknown channel %q (supported: %s)", channel, strings.Join(utils.NotificationChannels, ", "))})
	return "", false
}

// --- Events ---------------------------------------------------------------

type notificationEventInput struct {
	Key                    string `json:"key"`
	Name                   string `json:"name" binding:"required"`
	Description            string `json:"description"`
	Category               string `json:"category"`
	IsActive               *bool  `json:"is_active"`
	EmailEnabled           *bool  `json:"email_enabled"`
	SMSEnabled             *bool  `json:"sms_enabled"`
	WhatsAppEnabled        *bool  `json:"whatsapp_enabled"`
	TelegramEnabled        *bool  `json:"telegram_enabled"`
	PushEnabled            *bool  `json:"push_enabled"`
	InternalEnabled        *bool  `json:"internal_enabled"`
	RecipientEmails        string `json:"recipient_emails"`
	RecipientPhones        string `json:"recipient_phones"`
	RecipientTelegramChats string `json:"recipient_telegram_chats"`
	PushDeviceTokens       string `json:"push_device_tokens"`
	NotifyStoreOwner       *bool  `json:"notify_store_owner"`
	SubjectTemplate        string `json:"subject_template"`
	BodyTemplate           string `json:"body_template"`
	Variables              string `json:"variables"`
}

func boolOr(input *bool, fallback bool) bool {
	if input == nil {
		return fallback
	}
	return *input
}

// AdminListNotificationEvents lists all platform notification events.
func AdminListNotificationEvents(c *gin.Context) {
	var events []models.NotificationEvent
	query := utils.DB.Order("category ASC, name ASC")
	if category := strings.TrimSpace(c.Query("category")); category != "" {
		query = query.Where("category = ?", category)
	}
	if active := strings.TrimSpace(c.Query("active")); active != "" {
		query = query.Where("is_active = ?", active == "true")
	}
	if q := strings.TrimSpace(c.Query("q")); q != "" {
		like := "%" + q + "%"
		query = query.Where("name LIKE ? OR key LIKE ? OR description LIKE ?", like, like, like)
	}
	if err := query.Find(&events).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch notification events"})
		return
	}
	c.JSON(http.StatusOK, events)
}

// AdminCreateNotificationEvent registers a new notification event.
func AdminCreateNotificationEvent(c *gin.Context) {
	var input notificationEventInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	key := normalizeNotificationEventKey(input.Key)
	if key == "" {
		key = normalizeNotificationEventKey(input.Name)
	}
	if key == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Event key is required"})
		return
	}

	var existing int64
	utils.DB.Model(&models.NotificationEvent{}).Where("key = ?", key).Count(&existing)
	if existing > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("An event with key %q already exists", key)})
		return
	}

	category := strings.TrimSpace(input.Category)
	if category == "" {
		category = "custom"
	}

	event := models.NotificationEvent{
		ID:                     uuid.New(),
		Key:                    key,
		Name:                   strings.TrimSpace(input.Name),
		Description:            input.Description,
		Category:               category,
		IsActive:               boolOr(input.IsActive, true),
		EmailEnabled:           boolOr(input.EmailEnabled, true),
		SMSEnabled:             boolOr(input.SMSEnabled, false),
		WhatsAppEnabled:        boolOr(input.WhatsAppEnabled, false),
		TelegramEnabled:        boolOr(input.TelegramEnabled, false),
		PushEnabled:            boolOr(input.PushEnabled, false),
		InternalEnabled:        boolOr(input.InternalEnabled, true),
		RecipientEmails:        input.RecipientEmails,
		RecipientPhones:        input.RecipientPhones,
		RecipientTelegramChats: input.RecipientTelegramChats,
		PushDeviceTokens:       input.PushDeviceTokens,
		NotifyStoreOwner:       boolOr(input.NotifyStoreOwner, false),
		SubjectTemplate:        input.SubjectTemplate,
		BodyTemplate:           input.BodyTemplate,
		Variables:              input.Variables,
	}

	if err := utils.DB.Create(&event).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create notification event"})
		return
	}
	c.JSON(http.StatusCreated, event)
}

func findNotificationEvent(c *gin.Context) (models.NotificationEvent, bool) {
	var event models.NotificationEvent
	id := c.Param("id")
	if err := utils.DB.First(&event, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Notification event not found"})
		return event, false
	}
	return event, true
}

// AdminGetNotificationEvent returns one event definition.
func AdminGetNotificationEvent(c *gin.Context) {
	event, ok := findNotificationEvent(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, event)
}

// AdminUpdateNotificationEvent updates an event definition. The key is
// immutable once created.
func AdminUpdateNotificationEvent(c *gin.Context) {
	event, ok := findNotificationEvent(c)
	if !ok {
		return
	}

	var input notificationEventInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if strings.TrimSpace(input.Name) != "" {
		event.Name = strings.TrimSpace(input.Name)
	}
	event.Description = input.Description
	if strings.TrimSpace(input.Category) != "" {
		event.Category = strings.TrimSpace(input.Category)
	}
	event.IsActive = boolOr(input.IsActive, event.IsActive)
	event.EmailEnabled = boolOr(input.EmailEnabled, event.EmailEnabled)
	event.SMSEnabled = boolOr(input.SMSEnabled, event.SMSEnabled)
	event.WhatsAppEnabled = boolOr(input.WhatsAppEnabled, event.WhatsAppEnabled)
	event.TelegramEnabled = boolOr(input.TelegramEnabled, event.TelegramEnabled)
	event.PushEnabled = boolOr(input.PushEnabled, event.PushEnabled)
	event.InternalEnabled = boolOr(input.InternalEnabled, event.InternalEnabled)
	event.RecipientEmails = input.RecipientEmails
	event.RecipientPhones = input.RecipientPhones
	event.RecipientTelegramChats = input.RecipientTelegramChats
	event.PushDeviceTokens = input.PushDeviceTokens
	event.NotifyStoreOwner = boolOr(input.NotifyStoreOwner, event.NotifyStoreOwner)
	event.SubjectTemplate = input.SubjectTemplate
	event.BodyTemplate = input.BodyTemplate
	event.Variables = input.Variables

	if err := utils.DB.Save(&event).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update notification event"})
		return
	}
	c.JSON(http.StatusOK, event)
}

// AdminDeleteNotificationEvent removes an event definition. Delivery history
// is kept.
func AdminDeleteNotificationEvent(c *gin.Context) {
	event, ok := findNotificationEvent(c)
	if !ok {
		return
	}
	if err := utils.DB.Delete(&event).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete notification event"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Notification event deleted"})
}

// AdminTriggerNotificationEvent fires an event immediately with the supplied
// template data — used for testing configuration and for app code paths that
// forward events through this endpoint.
func AdminTriggerNotificationEvent(c *gin.Context) {
	event, ok := findNotificationEvent(c)
	if !ok {
		return
	}

	var input struct {
		Data map[string]string `json:"data"`
	}
	_ = c.ShouldBindJSON(&input)

	if input.Data == nil {
		input.Data = map[string]string{}
	}
	if _, hasUser := input.Data["user_id"]; !hasUser {
		if actor, exists := c.Get("user_id"); exists {
			if uid, isUUID := actor.(uuid.UUID); isUUID {
				input.Data["user_id"] = uid.String()
			}
		}
	}

	results, err := utils.DispatchNotificationEvent(utils.DB, event.Key, input.Data)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"event": event.Key, "results": results})
}

// --- Channels ---------------------------------------------------------------

// channelResponse masks secrets — the API only reports which secret keys are
// configured, never their values.
func channelResponse(cfg models.NotificationChannelConfig) gin.H {
	return gin.H{
		"channel":     cfg.Channel,
		"is_enabled":  cfg.IsEnabled,
		"config":      utils.ChannelConfig(cfg),
		"secrets_set": utils.SecretKeysOf(cfg),
		"updated_at":  cfg.UpdatedAt,
	}
}

// AdminListNotificationChannels returns the configuration state of every
// supported external channel.
func AdminListNotificationChannels(c *gin.Context) {
	out := make([]gin.H, 0, len(utils.NotificationChannels))
	for _, channel := range utils.NotificationChannels {
		cfg, err := utils.LoadNotificationChannel(utils.DB, channel)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load channel configuration"})
			return
		}
		out = append(out, channelResponse(cfg))
	}
	c.JSON(http.StatusOK, out)
}

// AdminUpdateNotificationChannel upserts one channel's enable flag, non-secret
// config, and secrets. Empty secret values keep the stored secret; send a
// secret with value "__clear__" to remove it.
func AdminUpdateNotificationChannel(c *gin.Context) {
	channel, ok := validNotificationChannel(c)
	if !ok {
		return
	}

	var input struct {
		IsEnabled *bool             `json:"is_enabled"`
		Config    map[string]string `json:"config"`
		Secrets   map[string]string `json:"secrets"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	cfg, err := utils.LoadNotificationChannel(utils.DB, channel)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load channel configuration"})
		return
	}
	isNew := cfg.ID == uuid.Nil
	if isNew {
		cfg.ID = uuid.New()
		cfg.Channel = channel
	}

	if input.IsEnabled != nil {
		cfg.IsEnabled = *input.IsEnabled
	}
	if input.Config != nil {
		raw, err := json.Marshal(input.Config)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid config object"})
			return
		}
		cfg.Config = string(raw)
	}
	if input.Secrets != nil {
		existing := utils.ChannelSecrets(cfg)
		for k, v := range input.Secrets {
			if v == "__clear__" {
				delete(existing, k)
			} else if strings.TrimSpace(v) != "" {
				existing[k] = v
			}
		}
		encrypted, err := utils.EncryptChannelSecrets(existing)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to encrypt channel secrets"})
			return
		}
		cfg.Secrets = encrypted
	}

	if isNew {
		err = utils.DB.Create(&cfg).Error
	} else {
		err = utils.DB.Save(&cfg).Error
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save channel configuration"})
		return
	}
	c.JSON(http.StatusOK, channelResponse(cfg))
}

// AdminTestNotificationChannel sends a test message through a channel to a
// single recipient.
func AdminTestNotificationChannel(c *gin.Context) {
	channel, ok := validNotificationChannel(c)
	if !ok {
		return
	}

	var input struct {
		Recipient string `json:"recipient" binding:"required"`
		Message   string `json:"message"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	message := input.Message
	if message == "" {
		message = "This is a TruERP test notification sent from the backoffice."
	}
	subject := "TruERP test notification"

	err := utils.SendChannelMessage(utils.DB, channel, strings.TrimSpace(input.Recipient), subject, message, map[string]string{})
	status := "sent"
	errStr := ""
	if err != nil {
		status = "failed"
		errStr = err.Error()
	}
	utils.DB.Create(&models.NotificationDelivery{
		ID:           uuid.New(),
		EventKey:     "manual.test",
		Channel:      channel,
		Recipient:    input.Recipient,
		Subject:      subject,
		Message:      message,
		Status:       status,
		ErrorMessage: errStr,
	})

	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("Test %s notification sent to %s", channel, input.Recipient)})
}

// --- Broadcast --------------------------------------------------------------

// AdminBroadcastNotification sends an ad-hoc notification composed by the
// super admin over the selected channels to the supplied recipients.
func AdminBroadcastNotification(c *gin.Context) {
	var input struct {
		Title           string   `json:"title" binding:"required"`
		Message         string   `json:"message" binding:"required"`
		Channels        []string `json:"channels" binding:"required"`
		Emails          string   `json:"emails"`
		Phones          string   `json:"phones"`
		TelegramChatIDs string   `json:"telegram_chat_ids"`
		DeviceTokens    string   `json:"device_tokens"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	recipientsFor := func(channel string) []string {
		switch channel {
		case "smtp":
			return utils.SplitRecipients(input.Emails)
		case "sms", "whatsapp":
			return utils.SplitRecipients(input.Phones)
		case "telegram":
			return utils.SplitRecipients(input.TelegramChatIDs)
		case "push":
			if tokens := utils.SplitRecipients(input.DeviceTokens); len(tokens) > 0 {
				return []string{strings.Join(tokens, ",")}
			}
			return nil
		}
		return nil
	}

	var results []utils.DeliveryResult
	for _, channel := range input.Channels {
		channel = strings.ToLower(strings.TrimSpace(channel))
		recipients := recipientsFor(channel)
		if len(recipients) == 0 {
			results = append(results, utils.DeliveryResult{Channel: channel, Status: "skipped", Error: "no recipients"})
			continue
		}
		for _, recipient := range recipients {
			err := utils.SendChannelMessage(utils.DB, channel, recipient, input.Title, input.Message, map[string]string{})
			status := "sent"
			errStr := ""
			if err != nil {
				status = "failed"
				errStr = err.Error()
			}
			utils.DB.Create(&models.NotificationDelivery{
				ID:           uuid.New(),
				EventKey:     "manual.broadcast",
				Channel:      channel,
				Recipient:    recipient,
				Subject:      input.Title,
				Message:      input.Message,
				Status:       status,
				ErrorMessage: errStr,
			})
			results = append(results, utils.DeliveryResult{Channel: channel, Recipient: recipient, Status: status, Error: errStr})
		}
	}

	c.JSON(http.StatusOK, gin.H{"results": results})
}

// --- Delivery log -----------------------------------------------------------

// AdminListNotificationDeliveries returns delivery log rows, newest first.
// Supports channel, status, event_key, limit, offset filters.
func AdminListNotificationDeliveries(c *gin.Context) {
	query := utils.DB.Model(&models.NotificationDelivery{}).Order("created_at DESC")

	if channel := strings.TrimSpace(c.Query("channel")); channel != "" {
		query = query.Where("channel = ?", channel)
	}
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		query = query.Where("status = ?", status)
	}
	if eventKey := strings.TrimSpace(c.Query("event_key")); eventKey != "" {
		query = query.Where("event_key = ?", eventKey)
	}

	var total int64
	query.Count(&total)

	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if offset < 0 {
		offset = 0
	}

	var deliveries []models.NotificationDelivery
	if err := query.Limit(limit).Offset(offset).Find(&deliveries).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch deliveries"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"total": total, "deliveries": deliveries})
}

// AdminClearNotificationDeliveries deletes delivery log rows older than
// ?days=N (default 30). Pass days=0 to clear everything.
func AdminClearNotificationDeliveries(c *gin.Context) {
	days, _ := strconv.Atoi(c.DefaultQuery("days", "30"))
	query := utils.DB.Where("1 = 1")
	if days > 0 {
		cutoff := time.Now().AddDate(0, 0, -days)
		query = query.Where("created_at < ?", cutoff)
	}
	res := query.Delete(&models.NotificationDelivery{})
	if res.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to clear deliveries"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": res.RowsAffected})
}
