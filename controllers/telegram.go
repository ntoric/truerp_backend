package controllers

// Telegram integration, per store.
//
// Each store connects its own bot (created with @BotFather) in Settings →
// Telegram. The bot then sends:
//   - the invoice PDF + summary to the customer after each sale,
//   - party balance statements (a channel of the party statement feature),
//   - new-sale alerts to the store owner's chats,
//   - the scheduled daily/party-balance report PDFs to the owner's chats.
//
// A bot cannot message anyone by phone number, so recipients link once: a
// customer opens t.me/<bot>?start=<party code> and presses Start; the store
// owner does the same with the owner code. The link poller reads the bot's
// updates with getUpdates (no public URL or webhook needed) and stores the
// chat ID on the party, or adds it to the store's owner chats. Codes are
// HMAC-signed, so they cannot be guessed or forged for another party.

import (
	"bytes"
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/google/uuid"
)

const (
	telegramLinkPurpose  = "telegram-link"
	telegramPartyPrefix  = "p"
	telegramOwnerPrefix  = "o"
	telegramPollInterval = 5 * time.Second
	telegramCaptionLimit = 1024

	telegramKindInvoiceCustomer = "invoice_customer"
	telegramKindInvoiceOwner    = "invoice_owner"
	telegramKindDailyReport     = "daily_report"
	telegramKindPartyBalance    = "party_balance_report"
	telegramKindTest            = "test"
)

// Overridable in tests.
var (
	telegramAPIBase    = "https://api.telegram.org"
	telegramHTTPClient = &http.Client{Timeout: 60 * time.Second}
)

// ---------------------------------------------------------------------------
// Bot API client
// ---------------------------------------------------------------------------

type telegramResponse struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description"`
	Result      json.RawMessage `json:"result"`
}

func telegramDecode(resp *http.Response) (json.RawMessage, error) {
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	var parsed telegramResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("telegram: unexpected response (%d)", resp.StatusCode)
	}
	if !parsed.OK {
		desc := parsed.Description
		if desc == "" {
			desc = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("telegram: %s", desc)
	}
	return parsed.Result, nil
}

func telegramCall(token, method string, payload interface{}) (json.RawMessage, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	resp, err := telegramHTTPClient.Post(telegramAPIBase+"/bot"+token+"/"+method, "application/json", bytes.NewReader(body))
	if err != nil {
		// The URL holds the token; never surface it in errors or logs.
		return nil, errors.New("telegram: could not reach api.telegram.org")
	}
	return telegramDecode(resp)
}

func telegramSendMessage(token, chatID, text string) error {
	_, err := telegramCall(token, "sendMessage", map[string]interface{}{
		"chat_id":                  chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	})
	return err
}

func telegramSendDocument(token, chatID, filename string, content []byte, caption string) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("chat_id", chatID)
	if caption != "" {
		_ = w.WriteField("caption", truncateTelegramCaption(caption))
		_ = w.WriteField("parse_mode", "HTML")
	}
	part, err := w.CreateFormFile("document", filename)
	if err != nil {
		return err
	}
	if _, err := part.Write(content); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	resp, err := telegramHTTPClient.Post(telegramAPIBase+"/bot"+token+"/sendDocument", w.FormDataContentType(), &buf)
	if err != nil {
		return errors.New("telegram: could not reach api.telegram.org")
	}
	_, err = telegramDecode(resp)
	return err
}

type telegramBotInfo struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Name     string `json:"first_name"`
}

func telegramGetMe(token string) (telegramBotInfo, error) {
	var info telegramBotInfo
	raw, err := telegramCall(token, "getMe", map[string]interface{}{})
	if err != nil {
		return info, err
	}
	err = json.Unmarshal(raw, &info)
	return info, err
}

type telegramUpdate struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		Text string `json:"text"`
		Chat struct {
			ID        int64  `json:"id"`
			FirstName string `json:"first_name"`
			Username  string `json:"username"`
		} `json:"chat"`
	} `json:"message"`
}

func telegramGetUpdates(token string, offset int64) ([]telegramUpdate, error) {
	raw, err := telegramCall(token, "getUpdates", map[string]interface{}{
		"offset":          offset,
		"timeout":         0,
		"allowed_updates": []string{"message"},
	})
	if err != nil {
		return nil, err
	}
	var updates []telegramUpdate
	err = json.Unmarshal(raw, &updates)
	return updates, err
}

// truncateTelegramCaption keeps a caption within Telegram's 1024-char limit.
func truncateTelegramCaption(s string) string {
	r := []rune(s)
	if len(r) <= telegramCaptionLimit {
		return s
	}
	return string(r[:telegramCaptionLimit-1]) + "…"
}

// ---------------------------------------------------------------------------
// Settings
// ---------------------------------------------------------------------------

func defaultTelegramSettings(userID uuid.UUID) models.TelegramSettings {
	return models.TelegramSettings{
		UserID:                userID,
		SendInvoiceToCustomer: true,
		SendForPOS:            true,
		SendForNonPOS:         true,
	}
}

func loadTelegramSettings(userID uuid.UUID) (models.TelegramSettings, bool) {
	var s models.TelegramSettings
	if err := utils.DB.Where("user_id = ?", userID).First(&s).Error; err != nil {
		return defaultTelegramSettings(userID), false
	}
	return s, true
}

// telegramBotToken returns the store's decrypted bot token, or an error
// explaining what is missing.
func telegramBotToken(s models.TelegramSettings) (string, error) {
	if s.EncryptedBotToken == "" {
		return "", errors.New("Telegram bot is not connected (Settings → Telegram)")
	}
	token, err := utils.Decrypt(s.EncryptedBotToken)
	if err != nil || token == "" {
		return "", errors.New("failed to decrypt the Telegram bot token; reconnect the bot")
	}
	return token, nil
}

// telegramReadyToken returns the token when the store's bot is connected and
// Telegram sending is switched on.
func telegramReadyToken(userID uuid.UUID) (models.TelegramSettings, string, error) {
	s, exists := loadTelegramSettings(userID)
	if !exists || s.EncryptedBotToken == "" {
		return s, "", errors.New("Telegram bot is not connected (Settings → Telegram)")
	}
	if !s.Enabled {
		return s, "", errors.New("Telegram messages are switched off (Settings → Telegram)")
	}
	token, err := telegramBotToken(s)
	return s, token, err
}

func splitTelegramChatIDs(raw string) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\n' || r == ' ' || r == ';' }) {
		f = strings.TrimSpace(f)
		if f == "" || seen[f] {
			continue
		}
		if _, err := strconv.ParseInt(f, 10, 64); err != nil {
			continue // chat IDs are integers (negative for groups)
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

// ---------------------------------------------------------------------------
// Link codes
// ---------------------------------------------------------------------------

// telegramLinkCode builds a /start payload: prefix + base64url(id | sig[:8]).
// It is 33 characters, inside Telegram's 64-char [A-Za-z0-9_-] limit.
func telegramLinkCode(prefix string, id uuid.UUID) string {
	sig := utils.SignHMAC(telegramLinkPurpose+"|"+prefix, id[:])[:8]
	return prefix + base64.RawURLEncoding.EncodeToString(append(append([]byte{}, id[:]...), sig...))
}

func parseTelegramLinkCode(code string) (prefix string, id uuid.UUID, ok bool) {
	if len(code) < 2 {
		return "", uuid.Nil, false
	}
	prefix = code[:1]
	if prefix != telegramPartyPrefix && prefix != telegramOwnerPrefix {
		return "", uuid.Nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(code[1:])
	if err != nil || len(raw) != 24 {
		return "", uuid.Nil, false
	}
	want := utils.SignHMAC(telegramLinkPurpose+"|"+prefix, raw[:16])[:8]
	if !hmac.Equal(raw[16:], want) {
		return "", uuid.Nil, false
	}
	id, err = uuid.FromBytes(raw[:16])
	return prefix, id, err == nil
}

func telegramBotLink(botUsername, code string) string {
	if botUsername == "" {
		return ""
	}
	return "https://t.me/" + botUsername + "?start=" + code
}

// ---------------------------------------------------------------------------
// Link poller
// ---------------------------------------------------------------------------

// StartTelegramLinkPoller polls every connected store bot for /start messages
// and links the sender's chat. Call once at startup.
func StartTelegramLinkPoller() {
	go func() {
		ticker := time.NewTicker(telegramPollInterval)
		defer ticker.Stop()
		for range ticker.C {
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("[telegram] poller panic: %v", r)
					}
				}()
				pollTelegramBots()
			}()
		}
	}()
}

func pollTelegramBots() {
	if utils.DB == nil {
		return
	}
	var rows []models.TelegramSettings
	if err := utils.DB.Where("bot_token IS NOT NULL AND bot_token <> ''").Find(&rows).Error; err != nil {
		return
	}
	for _, s := range rows {
		pollTelegramBot(s)
	}
}

func pollTelegramBot(s models.TelegramSettings) {
	token, err := telegramBotToken(s)
	if err != nil {
		return
	}
	updates, err := telegramGetUpdates(token, s.LastUpdateID+1)
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "webhook") {
			msg = "This bot has a webhook set, so its messages can't be read. Remove the webhook (or use a new bot) to link customers."
		}
		if msg != s.PollError {
			utils.DB.Model(&models.TelegramSettings{}).Where("id = ?", s.ID).Update("poll_error", msg)
		}
		return
	}
	last := s.LastUpdateID
	for _, u := range updates {
		if u.UpdateID > last {
			last = u.UpdateID
		}
		if u.Message == nil {
			continue
		}
		reply := handleTelegramStart(s, strconv.FormatInt(u.Message.Chat.ID, 10), u.Message.Text)
		if reply != "" {
			if err := telegramSendMessage(token, strconv.FormatInt(u.Message.Chat.ID, 10), reply); err != nil {
				log.Printf("[telegram] reply to chat failed for store %s: %v", s.UserID, err)
			}
		}
	}
	updates2 := map[string]interface{}{}
	if last != s.LastUpdateID {
		updates2["last_update_id"] = last
	}
	if s.PollError != "" {
		updates2["poll_error"] = ""
	}
	if len(updates2) > 0 {
		utils.DB.Model(&models.TelegramSettings{}).Where("id = ?", s.ID).Updates(updates2)
	}
}

// handleTelegramStart processes one incoming message and returns the reply.
func handleTelegramStart(s models.TelegramSettings, chatID, text string) string {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/start") {
		return ""
	}
	storeName := html.EscapeString(partyStatementStoreName(s.UserID, ""))
	if storeName == "" {
		storeName = "the store"
	}
	code := strings.TrimSpace(strings.TrimPrefix(text, "/start"))
	if code == "" {
		return fmt.Sprintf("Hello! This is the official bot of <b>%s</b>.\nTo receive your invoices here, open the personal link the store shared with you.", storeName)
	}
	prefix, id, ok := parseTelegramLinkCode(code)
	if !ok {
		return "This link is not valid. Please ask the store for a new one."
	}
	now := time.Now()
	switch prefix {
	case telegramPartyPrefix:
		res := utils.DB.Model(&models.Party{}).
			Where("user_id = ? AND id = ?", s.UserID, id).
			Updates(map[string]interface{}{"telegram_chat_id": chatID, "telegram_linked_at": now})
		if res.Error != nil || res.RowsAffected == 0 {
			return "This link is not valid. Please ask the store for a new one."
		}
		return fmt.Sprintf("✅ You're connected to <b>%s</b>.\nYour invoices and account statements will be sent here.", storeName)
	case telegramOwnerPrefix:
		if id != s.UserID {
			return "This link is not valid. Please ask the store for a new one."
		}
		chats := splitTelegramChatIDs(s.OwnerChatIDs)
		for _, c := range chats {
			if c == chatID {
				return fmt.Sprintf("This chat already receives alerts and reports for <b>%s</b>.", storeName)
			}
		}
		chats = append(chats, chatID)
		utils.DB.Model(&models.TelegramSettings{}).Where("id = ?", s.ID).
			Update("owner_chat_ids", strings.Join(chats, ", "))
		return fmt.Sprintf("✅ This chat will now receive sale alerts and reports for <b>%s</b>.", storeName)
	}
	return ""
}

// ---------------------------------------------------------------------------
// Logging
// ---------------------------------------------------------------------------

func writeTelegramLog(entry *models.TelegramMessageLog, status string, err error) (*models.TelegramMessageLog, error) {
	entry.Status = status
	if err != nil {
		entry.Error = err.Error()
	}
	if entry.ID == uuid.Nil {
		entry.ID = uuid.New()
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now()
	}
	if dbErr := utils.DB.Create(entry).Error; dbErr != nil {
		log.Printf("[telegram] failed to write log: %v", dbErr)
	}
	return entry, err
}

// ---------------------------------------------------------------------------
// Invoices
// ---------------------------------------------------------------------------

func loadInvoiceForTelegram(userID, invoiceID uuid.UUID) (models.Invoice, []byte, error) {
	var invoice models.Invoice
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, invoiceID).
		Preload("Party").Preload("Items").Preload("Items.Product").
		First(&invoice).Error; err != nil {
		return invoice, nil, errors.New("invoice not found")
	}
	attachInvoicePaymentSplits(utils.DB, &invoice)
	business := resolveSellerBusiness(userID, nil)
	pdfBytes, err := buildInvoiceDocumentPDF(invoice, &business, loadPrintSettings(userID))
	if err != nil {
		return invoice, nil, errors.New("failed to generate invoice PDF")
	}
	return invoice, pdfBytes, nil
}

func telegramInvoiceCaption(userID uuid.UUID, inv models.Invoice) string {
	store := html.EscapeString(partyStatementStoreName(userID, ""))
	var b strings.Builder
	if store != "" {
		fmt.Fprintf(&b, "<b>%s</b>\n", store)
	}
	fmt.Fprintf(&b, "Invoice <b>%s</b> · %s\n", html.EscapeString(inv.InvoiceNumber), inv.Date.Format("02 Jan 2006"))
	fmt.Fprintf(&b, "Amount: ₹%s\n", formatINRAmount(inv.TotalAmount))
	if inv.AmountPaid > 0 {
		fmt.Fprintf(&b, "Paid: ₹%s\n", formatINRAmount(inv.AmountPaid))
	}
	if due := inv.TotalAmount - inv.AmountPaid; due > 0.005 {
		fmt.Fprintf(&b, "Balance due: ₹%s\n", formatINRAmount(due))
	}
	b.WriteString("\nThank you for your purchase!")
	return b.String()
}

func telegramOwnerInvoiceCaption(inv models.Invoice) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🧾 <b>New sale</b> %s\n", html.EscapeString(inv.InvoiceNumber))
	if name := strings.TrimSpace(inv.Party.Name); name != "" {
		fmt.Fprintf(&b, "Customer: %s\n", html.EscapeString(name))
	}
	fmt.Fprintf(&b, "Amount: ₹%s", formatINRAmount(inv.TotalAmount))
	if inv.AmountPaid > 0 {
		fmt.Fprintf(&b, " · Paid: ₹%s", formatINRAmount(inv.AmountPaid))
	}
	if inv.IsPOS {
		b.WriteString("\nPOS sale")
	}
	return b.String()
}

// sendInvoiceTelegram sends the invoice PDF to the customer's linked chat (or
// overrideChat). force skips the automatic-send switches (manual sends).
func sendInvoiceTelegram(userID, invoiceID uuid.UUID, trigger, overrideChat string, force bool) (*models.TelegramMessageLog, error) {
	refID := invoiceID
	entry := &models.TelegramMessageLog{UserID: userID, Kind: telegramKindInvoiceCustomer, RefID: &refID, Trigger: trigger}

	s, token, err := telegramReadyToken(userID)
	if err != nil {
		return writeTelegramLog(entry, "failed", err)
	}
	if !force && !s.SendInvoiceToCustomer {
		return writeTelegramLog(entry, "skipped", errors.New("sending invoices to customers on Telegram is off"))
	}
	invoice, pdfBytes, err := loadInvoiceForTelegram(userID, invoiceID)
	entry.Reference = invoice.InvoiceNumber
	entry.PartyName = invoice.Party.Name
	if err != nil {
		return writeTelegramLog(entry, "failed", err)
	}
	chatID := strings.TrimSpace(overrideChat)
	if chatID == "" {
		chatID = strings.TrimSpace(invoice.Party.TelegramChatID)
	}
	entry.ChatID = chatID
	if chatID == "" {
		return writeTelegramLog(entry, "skipped", errors.New("customer has not linked Telegram yet — share their Telegram link from the party page"))
	}
	if err := telegramSendDocument(token, chatID, invoicePDFFilename(invoice.InvoiceNumber), pdfBytes, telegramInvoiceCaption(userID, invoice)); err != nil {
		return writeTelegramLog(entry, "failed", err)
	}
	return writeTelegramLog(entry, "sent", nil)
}

// sendInvoiceTelegramOwnerAlert sends a new-sale alert with the PDF to every
// owner chat. Returns how many chats received it.
func sendInvoiceTelegramOwnerAlert(userID, invoiceID uuid.UUID) int {
	s, token, err := telegramReadyToken(userID)
	if err != nil {
		return 0
	}
	chats := splitTelegramChatIDs(s.OwnerChatIDs)
	if len(chats) == 0 {
		return 0
	}
	invoice, pdfBytes, err := loadInvoiceForTelegram(userID, invoiceID)
	refID := invoiceID
	sent := 0
	for _, chat := range chats {
		entry := &models.TelegramMessageLog{UserID: userID, Kind: telegramKindInvoiceOwner, RefID: &refID,
			Reference: invoice.InvoiceNumber, PartyName: invoice.Party.Name, ChatID: chat, Trigger: "auto"}
		if err != nil {
			writeTelegramLog(entry, "failed", err)
			continue
		}
		if sendErr := telegramSendDocument(token, chat, invoicePDFFilename(invoice.InvoiceNumber), pdfBytes, telegramOwnerInvoiceCaption(invoice)); sendErr != nil {
			writeTelegramLog(entry, "failed", sendErr)
			continue
		}
		writeTelegramLog(entry, "sent", nil)
		sent++
	}
	return sent
}

// queueInvoiceTelegram fires the automatic customer send and owner alert in
// the background so invoice creation never waits on Telegram.
func queueInvoiceTelegram(userID uuid.UUID, invoice *models.Invoice) {
	if invoice == nil {
		return
	}
	s, exists := loadTelegramSettings(userID)
	if !exists || !s.Enabled || s.EncryptedBotToken == "" {
		return
	}
	toCustomer := s.SendInvoiceToCustomer &&
		((invoice.IsPOS && s.SendForPOS) || (!invoice.IsPOS && s.SendForNonPOS))
	toOwner := s.NotifyOwnerOnInvoice && len(splitTelegramChatIDs(s.OwnerChatIDs)) > 0
	if !toCustomer && !toOwner {
		return
	}
	invoiceID, partyID := invoice.ID, invoice.PartyID
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[telegram] panic sending invoice %s: %v", invoiceID, r)
			}
		}()
		if toCustomer && partyHasTelegram(userID, partyID) {
			if _, err := sendInvoiceTelegram(userID, invoiceID, "auto", "", false); err != nil {
				log.Printf("[telegram] invoice %s: %v", invoiceID, err)
			}
		}
		if toOwner {
			sendInvoiceTelegramOwnerAlert(userID, invoiceID)
		}
	}()
}

// partyHasTelegram reports whether the party linked a chat. Automatic sends
// to unlinked customers are skipped silently (most customers never link), so
// they don't flood the log.
func partyHasTelegram(userID, partyID uuid.UUID) bool {
	var count int64
	utils.DB.Model(&models.Party{}).
		Where("user_id = ? AND id = ? AND telegram_chat_id IS NOT NULL AND telegram_chat_id <> ''", userID, partyID).
		Count(&count)
	return count > 0
}

// ---------------------------------------------------------------------------
// Owner documents (scheduled reports)
// ---------------------------------------------------------------------------

// sendTelegramOwnerDocument delivers a PDF to every owner chat of the store.
// Returns the number of chats reached and the first error.
func sendTelegramOwnerDocument(userID uuid.UUID, kind, reference, trigger, filename string, pdfBytes []byte, caption string) (int, error) {
	s, token, err := telegramReadyToken(userID)
	if err != nil {
		return 0, err
	}
	chats := splitTelegramChatIDs(s.OwnerChatIDs)
	if len(chats) == 0 {
		return 0, errors.New("no Telegram owner chats linked (Settings → Telegram → Owner chats)")
	}
	sent := 0
	var firstErr error
	for _, chat := range chats {
		entry := &models.TelegramMessageLog{UserID: userID, Kind: kind, Reference: reference, ChatID: chat, Trigger: trigger}
		if sendErr := telegramSendDocument(token, chat, filename, pdfBytes, caption); sendErr != nil {
			writeTelegramLog(entry, "failed", sendErr)
			if firstErr == nil {
				firstErr = fmt.Errorf("Telegram chat %s: %w", chat, sendErr)
			}
			continue
		}
		writeTelegramLog(entry, "sent", nil)
		sent++
	}
	return sent, firstErr
}

// reportTelegramChatCount is how many owner chats a report send targets, for
// the "sent x of y" summary.
func reportTelegramChatCount(userID uuid.UUID, sendTelegram bool) int {
	if !sendTelegram {
		return 0
	}
	s, _ := loadTelegramSettings(userID)
	return len(splitTelegramChatIDs(s.OwnerChatIDs))
}
