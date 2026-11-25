package controllers

import (
	"errors"
	"html"
	"net/http"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type telegramSettingsResponse struct {
	models.TelegramSettings
	HasBotToken     bool     `json:"has_bot_token"`
	OwnerLink       string   `json:"owner_link"`
	OwnerChats      []string `json:"owner_chats"`
	LinkedCustomers int64    `json:"linked_customers"`
}

func buildTelegramSettingsResponse(s models.TelegramSettings) telegramSettingsResponse {
	resp := telegramSettingsResponse{
		TelegramSettings: s,
		HasBotToken:      s.EncryptedBotToken != "",
		OwnerChats:       splitTelegramChatIDs(s.OwnerChatIDs),
	}
	if resp.OwnerChats == nil {
		resp.OwnerChats = []string{}
	}
	if resp.HasBotToken {
		resp.OwnerLink = telegramBotLink(s.BotUsername, telegramLinkCode(telegramOwnerPrefix, s.UserID))
	}
	utils.DB.Model(&models.Party{}).
		Where("user_id = ? AND telegram_chat_id IS NOT NULL AND telegram_chat_id <> ''", s.UserID).
		Count(&resp.LinkedCustomers)
	return resp
}

// GetTelegramSettings returns the active store's Telegram settings.
func GetTelegramSettings(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	s, _ := loadTelegramSettings(userID)
	c.JSON(http.StatusOK, buildTelegramSettingsResponse(s))
}

// UpdateTelegramSettings saves the store's Telegram settings. A new bot token
// is verified with getMe before it is stored; an empty token keeps the
// current one, and disconnect=true removes it.
func UpdateTelegramSettings(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var input struct {
		Enabled               bool   `json:"enabled"`
		BotToken              string `json:"bot_token"`
		Disconnect            bool   `json:"disconnect"`
		SendInvoiceToCustomer bool   `json:"send_invoice_to_customer"`
		SendForPOS            bool   `json:"send_for_pos"`
		SendForNonPOS         bool   `json:"send_for_non_pos"`
		NotifyOwnerOnInvoice  bool   `json:"notify_owner_on_invoice"`
		OwnerChatIDs          string `json:"owner_chat_ids"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	s, exists := loadTelegramSettings(userID)
	if !exists {
		s.ID = uuid.New()
		if err := utils.DB.Create(&s).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save Telegram settings"})
			return
		}
	}

	updates := map[string]interface{}{
		"send_invoice_to_customer": input.SendInvoiceToCustomer,
		"send_for_pos":             input.SendForPOS,
		"send_for_non_pos":         input.SendForNonPOS,
		"notify_owner_on_invoice":  input.NotifyOwnerOnInvoice,
		"owner_chat_ids":           strings.Join(splitTelegramChatIDs(input.OwnerChatIDs), ", "),
	}
	hasToken := s.EncryptedBotToken != ""
	if token := strings.TrimSpace(input.BotToken); token != "" {
		info, err := telegramGetMe(token)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Telegram rejected this bot token: " + strings.TrimPrefix(err.Error(), "telegram: "),
				"fields": gin.H{"bot_token": "Invalid bot token"}})
			return
		}
		encrypted, err := utils.Encrypt(token)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to store the bot token"})
			return
		}
		updates["bot_token"] = encrypted
		updates["bot_username"] = info.Username
		updates["bot_name"] = info.Name
		updates["last_update_id"] = 0
		updates["poll_error"] = ""
		hasToken = true
	} else if input.Disconnect {
		updates["bot_token"] = ""
		updates["bot_username"] = ""
		updates["bot_name"] = ""
		updates["last_update_id"] = 0
		updates["poll_error"] = ""
		hasToken = false
	}
	if input.Enabled && !hasToken {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Connect a Telegram bot before switching Telegram on",
			"fields": gin.H{"bot_token": "Bot token is required"}})
		return
	}
	updates["enabled"] = input.Enabled && hasToken

	if err := utils.DB.Model(&models.TelegramSettings{}).Where("id = ?", s.ID).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save Telegram settings"})
		return
	}
	s, _ = loadTelegramSettings(userID)
	c.JSON(http.StatusOK, buildTelegramSettingsResponse(s))
}

// TestTelegram sends a test message to one chat, or to every owner chat.
func TestTelegram(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var input struct {
		ChatID string `json:"chat_id"`
	}
	_ = c.ShouldBindJSON(&input)

	s, exists := loadTelegramSettings(userID)
	if !exists {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Connect a Telegram bot first"})
		return
	}
	token, err := telegramBotToken(s)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	chats := splitTelegramChatIDs(input.ChatID)
	if len(chats) == 0 {
		chats = splitTelegramChatIDs(s.OwnerChatIDs)
	}
	if len(chats) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No chat to test with. Open the owner link in Telegram first, or enter a chat ID."})
		return
	}
	store := html.EscapeString(partyStatementStoreName(userID, ""))
	text := "✅ Test message from <b>" + store + "</b>. Telegram is connected."
	var logs []*models.TelegramMessageLog
	var firstErr error
	for _, chat := range chats {
		entry := &models.TelegramMessageLog{UserID: userID, Kind: telegramKindTest, Reference: "Test message", ChatID: chat, Trigger: "test"}
		sendErr := telegramSendMessage(token, chat, text)
		status := "sent"
		if sendErr != nil {
			status = "failed"
			if firstErr == nil {
				firstErr = sendErr
			}
		}
		e, _ := writeTelegramLog(entry, status, sendErr)
		logs = append(logs, e)
	}
	if firstErr != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": firstErr.Error(), "logs": logs})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Test message sent", "logs": logs})
}

// GetTelegramLogs lists the most recent Telegram send attempts.
func GetTelegramLogs(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var logs []models.TelegramMessageLog
	if err := utils.DB.Where("user_id = ?", userID).Order("created_at desc").Limit(100).Find(&logs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load Telegram logs"})
		return
	}
	c.JSON(http.StatusOK, logs)
}

// GetPartyTelegramLink returns the party's personal bot link and link status.
func GetPartyTelegramLink(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	party, err := loadPartyForTelegram(c, userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	s, _ := loadTelegramSettings(userID)
	link := ""
	if s.EncryptedBotToken != "" {
		link = telegramBotLink(s.BotUsername, telegramLinkCode(telegramPartyPrefix, party.ID))
	}
	c.JSON(http.StatusOK, gin.H{
		"bot_connected": s.EncryptedBotToken != "",
		"enabled":       s.Enabled,
		"bot_username":  s.BotUsername,
		"link":          link,
		"linked":        party.TelegramChatID != "",
		"chat_id":       party.TelegramChatID,
		"linked_at":     party.TelegramLinkedAt,
	})
}

// UnlinkPartyTelegram forgets the party's Telegram chat.
func UnlinkPartyTelegram(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	party, err := loadPartyForTelegram(c, userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	if err := utils.DB.Model(&models.Party{}).Where("user_id = ? AND id = ?", userID, party.ID).
		Updates(map[string]interface{}{"telegram_chat_id": "", "telegram_linked_at": (*time.Time)(nil)}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to unlink Telegram"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Telegram unlinked"})
}

func loadPartyForTelegram(c *gin.Context, userID uuid.UUID) (models.Party, error) {
	var party models.Party
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return party, errors.New("Invalid party id")
	}
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&party).Error; err != nil {
		return party, errors.New("Party not found")
	}
	return party, nil
}

// SendInvoiceTelegram manually (re)sends an invoice to the customer's linked
// Telegram chat, or to chat_id from the body.
func SendInvoiceTelegram(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	invoiceID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid invoice id"})
		return
	}
	var input struct {
		ChatID string `json:"chat_id"`
	}
	_ = c.ShouldBindJSON(&input)
	entry, err := sendInvoiceTelegram(userID, invoiceID, "manual", input.ChatID, true)
	if err != nil {
		status := http.StatusBadGateway
		if entry != nil && entry.Status == "skipped" {
			status = http.StatusUnprocessableEntity
		}
		c.JSON(status, gin.H{"error": err.Error(), "log": entry})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Invoice sent on Telegram", "log": entry})
}
