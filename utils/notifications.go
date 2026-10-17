package utils

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"truerp/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// NotificationChannels lists every external channel supported by the
// notification module. Channel configs are stored in the
// notification_channel_configs table keyed by these names.
var NotificationChannels = []string{"smtp", "telegram", "whatsapp", "sms", "push"}

var notificationHTTPClient = &http.Client{Timeout: 15 * time.Second}

// ChannelSecrets decrypts the secrets JSON blob stored on a
// NotificationChannelConfig row. Returns an empty map when unset.
func ChannelSecrets(cfg models.NotificationChannelConfig) map[string]string {
	out := map[string]string{}
	if cfg.Secrets == "" {
		return out
	}
	plain, err := Decrypt(cfg.Secrets)
	if err != nil {
		log.Printf("notification channel %s: failed to decrypt secrets: %v", cfg.Channel, err)
		return out
	}
	if err := json.Unmarshal([]byte(plain), &out); err != nil {
		log.Printf("notification channel %s: malformed secrets JSON: %v", cfg.Channel, err)
		return out
	}
	return out
}

// ChannelConfig parses the non-secret config JSON blob.
func ChannelConfig(cfg models.NotificationChannelConfig) map[string]string {
	out := map[string]string{}
	if cfg.Config == "" {
		return out
	}
	if err := json.Unmarshal([]byte(cfg.Config), &out); err != nil {
		log.Printf("notification channel %s: malformed config JSON: %v", cfg.Channel, err)
	}
	return out
}

// EncryptChannelSecrets serialises and encrypts a secrets map for storage.
func EncryptChannelSecrets(secrets map[string]string) (string, error) {
	cleaned := map[string]string{}
	for k, v := range secrets {
		if strings.TrimSpace(v) != "" {
			cleaned[k] = v
		}
	}
	raw, err := json.Marshal(cleaned)
	if err != nil {
		return "", err
	}
	return Encrypt(string(raw))
}

// LoadNotificationChannel returns the config row for a channel, or an empty
// (disabled) row when the channel was never configured.
func LoadNotificationChannel(db *gorm.DB, channel string) (models.NotificationChannelConfig, error) {
	var cfg models.NotificationChannelConfig
	err := db.Where("channel = ?", channel).First(&cfg).Error
	if err == gorm.ErrRecordNotFound {
		return models.NotificationChannelConfig{Channel: channel, IsEnabled: false}, nil
	}
	return cfg, err
}

// SplitRecipients parses a comma/semicolon/newline/space separated recipient
// list into trimmed entries.
func SplitRecipients(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == ' ' || r == '\t'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if v := strings.TrimSpace(f); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// RenderNotificationTemplate substitutes {{placeholder}} tokens with values
// from data. Unknown placeholders are left as-is so admins notice them.
func RenderNotificationTemplate(tpl string, data map[string]string) string {
	out := tpl
	for k, v := range data {
		out = strings.ReplaceAll(out, "{{"+k+"}}", v)
	}
	return out
}

// DeliveryResult summarises one dispatch for the API response.
type DeliveryResult struct {
	Channel   string `json:"channel"`
	Recipient string `json:"recipient"`
	Status    string `json:"status"` // sent, failed, skipped
	Error     string `json:"error,omitempty"`
}

func logDelivery(db *gorm.DB, eventID *uuid.UUID, eventKey, channel, recipient, subject, message string, err error) {
	delivery := models.NotificationDelivery{
		ID:        uuid.New(),
		EventID:   eventID,
		EventKey:  eventKey,
		Channel:   channel,
		Recipient: recipient,
		Subject:   subject,
		Message:   message,
		Status:    "sent",
	}
	if err != nil {
		delivery.Status = "failed"
		delivery.ErrorMessage = err.Error()
	}
	if dbErr := db.Create(&delivery).Error; dbErr != nil {
		log.Printf("notification delivery log failed: %v", dbErr)
	}
}

// SendChannelMessage delivers one message through one channel using the stored
// channel configuration. recipient semantics differ per channel: email address
// (smtp), phone number (sms/whatsapp), chat id (telegram), device token (push
// fcm). Push/webhook and broadcast callers may pass a comma-separated token
// list as a single recipient.
func SendChannelMessage(db *gorm.DB, channel, recipient, subject, message string, data map[string]string) error {
	cfg, err := LoadNotificationChannel(db, channel)
	if err != nil {
		return err
	}
	if !cfg.IsEnabled {
		return fmt.Errorf("%s channel is disabled", channel)
	}
	conf := ChannelConfig(cfg)
	secrets := ChannelSecrets(cfg)

	switch channel {
	case "smtp", "email":
		return sendSMTPEmail(conf, secrets, recipient, subject, message)
	case "telegram":
		return sendTelegram(conf, secrets, recipient, message)
	case "whatsapp":
		return sendWhatsApp(conf, secrets, recipient, message)
	case "sms":
		return sendSMS(conf, secrets, recipient, message)
	case "push":
		return sendPush(conf, secrets, recipient, subject, message, data)
	default:
		return fmt.Errorf("unsupported channel: %s", channel)
	}
}

// DispatchNotificationEvent fires a configured NotificationEvent: renders the
// templates and sends to every channel enabled on both the event and the
// channel config. It writes a NotificationDelivery row per attempt and returns
// the per-recipient results.
func DispatchNotificationEvent(db *gorm.DB, eventKey string, data map[string]string) ([]DeliveryResult, error) {
	var event models.NotificationEvent
	if err := db.Where("key = ?", eventKey).First(&event).Error; err != nil {
		return nil, fmt.Errorf("notification event %q not found", eventKey)
	}
	if !event.IsActive {
		return []DeliveryResult{{Channel: "-", Recipient: "-", Status: "skipped", Error: "event is disabled"}}, nil
	}
	if data == nil {
		data = map[string]string{}
	}

	subject := RenderNotificationTemplate(event.SubjectTemplate, data)
	if subject == "" {
		subject = event.Name
	}
	message := RenderNotificationTemplate(event.BodyTemplate, data)
	if message == "" {
		message = subject
	}

	var results []DeliveryResult
	attempt := func(channel, recipient string, send func() error) {
		var sendErr error
		status := "sent"
		errStr := ""
		if cfg, err := LoadNotificationChannel(db, channel); err == nil && !cfg.IsEnabled {
			status = "skipped"
			errStr = "channel disabled"
		} else {
			sendErr = send()
		}
		if sendErr != nil {
			status = "failed"
			errStr = sendErr.Error()
		}
		logDelivery(db, &event.ID, eventKey, channel, recipient, subject, message, sendErr)
		results = append(results, DeliveryResult{Channel: channel, Recipient: recipient, Status: status, Error: errStr})
	}

	// Resolve the triggering store owner (if any) for notify_store_owner.
	var owner models.User
	ownerLoaded := false
	if uid := strings.TrimSpace(data["user_id"]); uid != "" {
		if parsed, err := uuid.Parse(uid); err == nil {
			ownerLoaded = db.First(&owner, "id = ?", parsed).Error == nil
		}
	}

	if event.EmailEnabled {
		recipients := SplitRecipients(event.RecipientEmails)
		if extra := SplitRecipients(data["emails"]); len(extra) > 0 {
			recipients = append(recipients, extra...)
		}
		if event.NotifyStoreOwner && ownerLoaded && owner.Email != "" {
			recipients = append(recipients, owner.Email)
		}
		for _, to := range recipients {
			r := to
			attempt("smtp", r, func() error {
				return SendChannelMessage(db, "smtp", r, subject, message, data)
			})
		}
	}

	if event.TelegramEnabled {
		recipients := SplitRecipients(event.RecipientTelegramChats)
		if extra := SplitRecipients(data["telegram_chat_ids"]); len(extra) > 0 {
			recipients = append(recipients, extra...)
		}
		if extra := strings.TrimSpace(data["telegram_chat_id"]); extra != "" {
			recipients = append(recipients, extra)
		}
		for _, chat := range recipients {
			r := chat
			attempt("telegram", r, func() error {
				return SendChannelMessage(db, "telegram", r, subject, message, data)
			})
		}
	}

	if event.SMSEnabled || event.WhatsAppEnabled {
		phones := SplitRecipients(event.RecipientPhones)
		if extra := SplitRecipients(data["phones"]); len(extra) > 0 {
			phones = append(phones, extra...)
		}
		if event.NotifyStoreOwner && ownerLoaded && owner.Phone != "" {
			phones = append(phones, owner.Phone)
		}
		for _, phone := range phones {
			r := phone
			if event.SMSEnabled {
				attempt("sms", r, func() error {
					return SendChannelMessage(db, "sms", r, subject, message, data)
				})
			}
			if event.WhatsAppEnabled {
				attempt("whatsapp", r, func() error {
					return SendChannelMessage(db, "whatsapp", r, subject, message, data)
				})
			}
		}
	}

	if event.PushEnabled {
		tokens := SplitRecipients(event.PushDeviceTokens)
		if extra := SplitRecipients(data["device_tokens"]); len(extra) > 0 {
			tokens = append(tokens, extra...)
		}
		if len(tokens) > 0 {
			joined := strings.Join(tokens, ",")
			attempt("push", joined, func() error {
				return SendChannelMessage(db, "push", joined, subject, message, data)
			})
		}
	}

	if event.InternalEnabled {
		recipient := "-"
		var sendErr error
		if ownerLoaded {
			recipient = owner.Email
			sendErr = createInternalNotification(db, owner.ID, event, subject, message)
		} else if uid := strings.TrimSpace(data["user_id"]); uid != "" {
			if parsed, err := uuid.Parse(uid); err == nil {
				recipient = uid
				sendErr = createInternalNotification(db, parsed, event, subject, message)
			}
		} else {
			sendErr = fmt.Errorf("no user_id in event data for internal notification")
		}
		status := "sent"
		errStr := ""
		if sendErr != nil {
			status = "failed"
			errStr = sendErr.Error()
		}
		logDelivery(db, &event.ID, eventKey, "internal", recipient, subject, message, sendErr)
		results = append(results, DeliveryResult{Channel: "internal", Recipient: recipient, Status: status, Error: errStr})
	}

	return results, nil
}

func createInternalNotification(db *gorm.DB, userID uuid.UUID, event models.NotificationEvent, subject, message string) error {
	now := time.Now()
	notification := models.Notification{
		ID:          uuid.New(),
		UserID:      userID,
		Type:        event.Key,
		Title:       subject,
		Message:     message,
		Channels:    "internal",
		Priority:    "normal",
		Status:      "sent",
		ScheduledAt: &now,
		SentAt:      &now,
	}
	return db.Create(&notification).Error
}

// --- Channel senders -------------------------------------------------------

func sendSMTPEmail(conf, secrets map[string]string, to, subject, body string) error {
	port := 587
	if p := strings.TrimSpace(conf["port"]); p != "" {
		var parsed int
		if _, err := fmt.Sscanf(p, "%d", &parsed); err == nil && parsed > 0 {
			port = parsed
		}
	}
	return SendEmailWithConfig(EmailConfig{
		Host:     strings.TrimSpace(conf["host"]),
		Port:     port,
		Username: strings.TrimSpace(conf["username"]),
		Password: secrets["password"],
		From:     strings.TrimSpace(conf["from_email"]),
		FromName: strings.TrimSpace(conf["from_name"]),
	}, to, subject, body)
}

func sendTelegram(conf, secrets map[string]string, chatID, text string) error {
	token := secrets["bot_token"]
	if token == "" {
		return fmt.Errorf("telegram bot token not configured")
	}
	if strings.TrimSpace(chatID) == "" {
		return fmt.Errorf("telegram chat id is required")
	}
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	payload := map[string]interface{}{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "HTML",
	}
	return postJSON(apiURL, nil, payload)
}

func sendWhatsApp(conf, secrets map[string]string, to, body string) error {
	provider := conf["provider"]
	if provider == "" {
		provider = "meta"
	}
	to = strings.TrimSpace(to)
	if to == "" {
		return fmt.Errorf("whatsapp recipient phone is required")
	}
	switch provider {
	case "meta":
		phoneNumberID := strings.TrimSpace(conf["phone_number_id"])
		token := secrets["access_token"]
		if phoneNumberID == "" || token == "" {
			return fmt.Errorf("whatsapp phone_number_id and access token are required")
		}
		payload := map[string]interface{}{
			"messaging_product": "whatsapp",
			"to":                to,
			"type":              "text",
			"text":              map[string]string{"body": body},
		}
		return postJSON(fmt.Sprintf("https://graph.facebook.com/v19.0/%s/messages", phoneNumberID),
			map[string]string{"Authorization": "Bearer " + token}, payload)
	case "twilio":
		from := strings.TrimSpace(conf["twilio_from"])
		if from != "" && !strings.HasPrefix(from, "whatsapp:") {
			from = "whatsapp:" + from
		}
		toAddr := to
		if !strings.HasPrefix(toAddr, "whatsapp:") {
			toAddr = "whatsapp:" + toAddr
		}
		return sendTwilio(conf, secrets, from, toAddr, body)
	default:
		return fmt.Errorf("unsupported whatsapp provider: %s", provider)
	}
}

func sendSMS(conf, secrets map[string]string, to, message string) error {
	provider := conf["provider"]
	if provider == "" {
		provider = "twilio"
	}
	to = strings.TrimSpace(to)
	if to == "" {
		return fmt.Errorf("sms recipient phone is required")
	}
	switch provider {
	case "twilio":
		return sendTwilio(conf, secrets, strings.TrimSpace(conf["twilio_from"]), to, message)
	case "msg91":
		authKey := secrets["auth_key"]
		sender := strings.TrimSpace(conf["sender_id"])
		if authKey == "" || sender == "" {
			return fmt.Errorf("msg91 auth key and sender id are required")
		}
		country := conf["country"]
		if country == "" {
			country = "91"
		}
		form := url.Values{
			"authkey": {authKey},
			"mobiles": {strings.TrimPrefix(to, "+")},
			"message": {message},
			"sender":  {sender},
			"route":   {"4"},
			"country": {country},
		}
		return postForm("https://api.msg91.com/api/sendhttp.php", form)
	case "textlocal":
		apiKey := secrets["api_key"]
		if apiKey == "" {
			return fmt.Errorf("textlocal api key is required")
		}
		form := url.Values{
			"apikey":  {apiKey},
			"numbers": {to},
			"message": {message},
			"sender":  {strings.TrimSpace(conf["sender"])},
		}
		return postForm("https://api.textlocal.in/send/", form)
	case "webhook":
		hookURL := strings.TrimSpace(conf["webhook_url"])
		if hookURL == "" {
			return fmt.Errorf("sms webhook url is required")
		}
		headers := map[string]string{}
		if secret := secrets["webhook_secret"]; secret != "" {
			headers["Authorization"] = "Bearer " + secret
		}
		return postJSON(hookURL, headers, map[string]string{"to": to, "message": message})
	default:
		return fmt.Errorf("unsupported sms provider: %s", provider)
	}
}

func sendPush(conf, secrets map[string]string, tokensCSV, title, body string, data map[string]string) error {
	provider := conf["provider"]
	if provider == "" {
		provider = "fcm"
	}
	tokens := SplitRecipients(tokensCSV)
	switch provider {
	case "fcm":
		serverKey := secrets["server_key"]
		if serverKey == "" {
			return fmt.Errorf("fcm server key is required")
		}
		if len(tokens) == 0 {
			return fmt.Errorf("no push device tokens configured")
		}
		payload := map[string]interface{}{
			"registration_ids": tokens,
			"notification":     map[string]string{"title": title, "body": body},
			"data":             data,
		}
		return postJSON("https://fcm.googleapis.com/fcm/send",
			map[string]string{"Authorization": "key=" + serverKey}, payload)
	case "webhook":
		hookURL := strings.TrimSpace(conf["webhook_url"])
		if hookURL == "" {
			return fmt.Errorf("push webhook url is required")
		}
		headers := map[string]string{}
		if secret := secrets["webhook_secret"]; secret != "" {
			headers["Authorization"] = "Bearer " + secret
		}
		return postJSON(hookURL, headers, map[string]interface{}{
			"tokens": tokens,
			"title":  title,
			"body":   body,
			"data":   data,
		})
	default:
		return fmt.Errorf("unsupported push provider: %s", provider)
	}
}

func sendTwilio(conf, secrets map[string]string, from, to, body string) error {
	accountSID := strings.TrimSpace(conf["twilio_account_sid"])
	authToken := secrets["twilio_auth_token"]
	if accountSID == "" || authToken == "" {
		return fmt.Errorf("twilio account sid and auth token are required")
	}
	if from == "" {
		return fmt.Errorf("twilio sender number is required")
	}
	form := url.Values{
		"From": {from},
		"To":   {to},
		"Body": {body},
	}
	apiURL := fmt.Sprintf("https://api.twilio.com/2010-04-01/Accounts/%s/Messages.json", accountSID)
	req, err := http.NewRequest(http.MethodPost, apiURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.SetBasicAuth(accountSID, authToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := notificationHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("twilio request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("twilio returned %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	return nil
}

// --- HTTP helpers ----------------------------------------------------------

func postJSON(apiURL string, headers map[string]string, payload interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := notificationHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("request failed (%s): %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	return nil
}

func postForm(apiURL string, form url.Values) error {
	resp, err := notificationHTTPClient.PostForm(apiURL, form)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("request failed (%s): %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	return nil
}

// SecretKeysOf returns the sorted set of secret keys currently stored on a
// channel config — exposed to the admin UI as "configured" markers.
func SecretKeysOf(cfg models.NotificationChannelConfig) []string {
	secrets := ChannelSecrets(cfg)
	keys := make([]string, 0, len(secrets))
	for k := range secrets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// SeedDefaultNotificationEvents inserts a starter set of platform notification
// events when the table is empty. All channels start disabled so nothing sends
// until the super admin configures channels and opts events in.
func SeedDefaultNotificationEvents(db *gorm.DB) {
	if !db.Migrator().HasTable(&models.NotificationEvent{}) {
		return
	}
	var count int64
	db.Model(&models.NotificationEvent{}).Count(&count)
	if count > 0 {
		return
	}
	defaults := []models.NotificationEvent{
		{
			Key:             "invoice.created",
			Name:            "Invoice created",
			Description:     "Fired when a new sales invoice is created.",
			Category:        "sales",
			SubjectTemplate: "Invoice {{invoice_number}} created",
			BodyTemplate:    "Invoice {{invoice_number}} for {{party_name}} was created with total {{total_amount}}.",
			Variables:       "invoice_number,party_name,total_amount,due_date,user_id",
		},
		{
			Key:             "invoice.due",
			Name:            "Invoice due",
			Description:     "Fired when an invoice approaches its due date.",
			Category:        "sales",
			SubjectTemplate: "Invoice {{invoice_number}} due soon",
			BodyTemplate:    "Invoice {{invoice_number}} for {{party_name}} is due on {{due_date}}. Amount: {{total_amount}}.",
			Variables:       "invoice_number,party_name,total_amount,due_date,user_id",
		},
		{
			Key:             "payment.received",
			Name:            "Payment received",
			Description:     "Fired when a payment-in is recorded.",
			Category:        "payments",
			SubjectTemplate: "Payment {{payment_number}} received",
			BodyTemplate:    "Payment of {{amount}} received from {{party_name}} ({{payment_number}}).",
			Variables:       "payment_number,party_name,amount,user_id",
		},
		{
			Key:             "stock.low",
			Name:            "Low stock alert",
			Description:     "Fired when a product drops below its minimum stock level.",
			Category:        "inventory",
			SubjectTemplate: "Low stock: {{product_name}}",
			BodyTemplate:    "Product {{product_name}} ({{item_code}}) is below minimum stock. Remaining: {{quantity}}.",
			Variables:       "product_name,item_code,quantity,user_id",
		},
		{
			Key:             "support_request.created",
			Name:            "Support request created",
			Description:     "Fired when a store user raises a support request.",
			Category:        "system",
			SubjectTemplate: "New support request {{ticket_number}}",
			BodyTemplate:    "{{store_name}} opened support request {{ticket_number}}: {{subject}}.",
			Variables:       "ticket_number,store_name,subject,user_id",
		},
	}
	for _, e := range defaults {
		e.ID = uuid.New()
		e.IsActive = true
		e.InternalEnabled = true
		e.EmailEnabled = true
		if err := db.Create(&e).Error; err != nil {
			log.Printf("SeedDefaultNotificationEvents: %s failed: %v", e.Key, err)
		}
	}
}
