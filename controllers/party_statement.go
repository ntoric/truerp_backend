package controllers

import (
	"crypto/hmac"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"html"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Party balance statement ("balance reminder"): a short message with the
// party's previous and current balance for a period, plus a detailed periodic
// statement PDF (sales and payments in date-and-time order). Built on the
// party ledger (loadPartyLedger) so the figures match the Party Statement
// page exactly.
//
// Channels: WhatsApp now — it reuses the store's WhatsApp gateway (provider,
// credentials, sender, public URL) from the WhatsApp Invoice settings and adds
// only its own approved template. Telegram and email plug in as further
// channels via sendPartyStatement.

const (
	partyStatementChannelWhatsApp = "whatsapp"
	partyStatementChannelTelegram = "telegram"

	partyStatementPDFPurpose     = "party-statement-pdf"
	publicPartyStatementPDFPath  = "/api/v1/public/party-statement-pdf/"
	defaultPartyStatementPeriod  = "previous_month"
	defaultPartyStatementMapping = "customer_name,store_name,from_date,to_date,opening_balance,closing_balance"
	// Upper bound on one bulk run, to keep a mis-click from messaging an
	// entire large customer base.
	maxPartyStatementBulk = 2000
	// Pause between bulk sends so gateways don't rate-limit the run.
	partyStatementBulkDelay = 300 * time.Millisecond
)

var partyStatementChannels = []string{partyStatementChannelWhatsApp, partyStatementChannelTelegram}

// partyStatementPeriods are the period presets the send dialogs offer; the
// frontend resolves them to dates. Kept here to validate the default period.
var partyStatementPeriods = []whatsAppInvoiceField{
	{"this_week", "This week"},
	{"previous_week", "Previous week"},
	{"this_month", "This month"},
	{"previous_month", "Previous month"},
	{"last_n_days", "Last N days"},
	{"this_quarter", "This quarter"},
	{"previous_quarter", "Previous quarter"},
	{"this_fy", "This financial year"},
	{"previous_fy", "Previous financial year"},
	{"custom", "Custom range"},
}

// partyStatementFields lists the values a statement template variable can be
// mapped to.
var partyStatementFields = []whatsAppInvoiceField{
	{"customer_name", "Party name"},
	{"store_name", "Store / business name"},
	{"from_date", "Period start date"},
	{"to_date", "Period end date"},
	{"opening_balance", "Previous balance (start of period)"},
	{"closing_balance", "Current balance (end of period)"},
	{"total_sales", "Sales in period"},
	{"total_received", "Payments received in period"},
	{"store_phone", "Store phone"},
	{"statement_pdf_url", "Statement PDF link (full URL)"},
	{"statement_pdf_file", "Statement PDF file (Twilio media URL variable)"},
}

func isKnownPartyStatementField(key string) bool {
	for _, f := range partyStatementFields {
		if f.Key == key {
			return true
		}
	}
	return false
}

func isPartyStatementPDFField(key string) bool {
	return key == "statement_pdf_url" || key == "statement_pdf_file"
}

func partyStatementMappingNeedsPDF(mapping string) bool {
	for _, k := range strings.Split(mapping, ",") {
		if isPartyStatementPDFField(strings.TrimSpace(k)) {
			return true
		}
	}
	return false
}

func isKnownPartyStatementPeriod(p string) bool {
	if p == "last_30_days" { // legacy preset, now last_n_days with 30
		return true
	}
	for _, f := range partyStatementPeriods {
		if f.Key == p {
			return true
		}
	}
	return false
}

func normalizePartyStatementMapping(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultPartyStatementMapping, nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for i, p := range parts {
		key := strings.TrimSpace(p)
		if !isKnownPartyStatementField(key) {
			return "", fmt.Errorf("unknown field %q for variable {{%d}}", key, i+1)
		}
		out = append(out, key)
	}
	return strings.Join(out, ","), nil
}

func loadPartyStatementSettings(userID uuid.UUID) (models.PartyStatementSettings, bool) {
	var s models.PartyStatementSettings
	if err := utils.DB.Where("user_id = ?", userID).First(&s).Error; err != nil {
		return models.PartyStatementSettings{
			UserID:                  userID,
			DefaultPeriod:           defaultPartyStatementPeriod,
			DefaultPeriodDays:       defaultStatementPeriodDays,
			WhatsAppVariableMapping: defaultPartyStatementMapping,
			WhatsAppAttachPDF:       true,
		}, false
	}
	if s.DefaultPeriod == "last_30_days" {
		s.DefaultPeriod, s.DefaultPeriodDays = "last_n_days", 30
	}
	if !isKnownPartyStatementPeriod(s.DefaultPeriod) {
		s.DefaultPeriod = defaultPartyStatementPeriod
	}
	if s.DefaultPeriodDays <= 0 {
		s.DefaultPeriodDays = defaultStatementPeriodDays
	}
	if strings.TrimSpace(s.WhatsAppVariableMapping) == "" {
		s.WhatsAppVariableMapping = defaultPartyStatementMapping
	}
	return s, true
}

// whatsAppGatewayReady reports whether the store's WhatsApp connection (from
// the WhatsApp Invoice settings) has what any template send needs.
func whatsAppGatewayReady(wa models.WhatsAppInvoiceSettings, exists bool) bool {
	if !exists || wa.EncryptedAPIKey == "" || wa.PhoneNumberID == "" {
		return false
	}
	return wa.Provider != whatsAppProviderTwilio || wa.AccountSID != ""
}

type partyStatementSettingsResponse struct {
	models.PartyStatementSettings
	Channels        []string               `json:"channels"`
	Periods         []whatsAppInvoiceField `json:"periods"`
	AvailableFields []whatsAppInvoiceField `json:"available_fields"`
	DefaultMapping  string                 `json:"default_mapping"`
	WhatsAppReady   bool                   `json:"whatsapp_ready"`
	// TelegramReady: the store's bot is connected and Telegram is switched on.
	TelegramReady    bool   `json:"telegram_ready"`
	WhatsAppProvider string `json:"whatsapp_provider"`
	// The invoice template's language, used when no statement language is set.
	WhatsAppInvoiceLanguage string `json:"whatsapp_invoice_language"`
	HasPublicBaseURL        bool   `json:"has_public_base_url"`
	// StoreName is what the store_name variable fills, for previews.
	StoreName string `json:"store_name"`
	// DefaultTelegramMessage is used when TelegramMessage is empty.
	DefaultTelegramMessage string                 `json:"default_telegram_message"`
	TelegramFields         []whatsAppInvoiceField `json:"telegram_fields"`
}

func buildPartyStatementSettingsResponse(s models.PartyStatementSettings) partyStatementSettingsResponse {
	wa, waExists := loadWhatsAppInvoiceSettings(s.UserID)
	_, _, tgErr := telegramReadyToken(s.UserID)
	return partyStatementSettingsResponse{
		TelegramReady:           tgErr == nil,
		PartyStatementSettings:  s,
		Channels:                partyStatementChannels,
		Periods:                 partyStatementPeriods,
		AvailableFields:         partyStatementFields,
		DefaultMapping:          defaultPartyStatementMapping,
		WhatsAppReady:           whatsAppGatewayReady(wa, waExists),
		WhatsAppProvider:        wa.Provider,
		WhatsAppInvoiceLanguage: wa.LanguageCode,
		HasPublicBaseURL:        whatsAppInvoicePublicBaseURL(wa) != "",
		StoreName:               partyStatementStoreName(s.UserID, wa.StoreDisplayName),
		DefaultTelegramMessage:  defaultTelegramStatementMessage,
		TelegramFields:          telegramStatementFields(),
	}
}

// GetPartyStatementSettings returns the active store's statement settings.
func GetPartyStatementSettings(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	s, _ := loadPartyStatementSettings(userID)
	c.JSON(http.StatusOK, buildPartyStatementSettingsResponse(s))
}

// UpdatePartyStatementSettings saves the active store's statement settings.
func UpdatePartyStatementSettings(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var input struct {
		DefaultPeriod           string  `json:"default_period"`
		DefaultPeriodDays       int     `json:"default_period_days"`
		WhatsAppMessageID       string  `json:"whatsapp_message_id"`
		WhatsAppLanguageCode    string  `json:"whatsapp_language_code"`
		WhatsAppVariableMapping string  `json:"whatsapp_variable_mapping"`
		WhatsAppTemplateBody    *string `json:"whatsapp_template_body"` // nil = keep
		WhatsAppAttachPDF       bool    `json:"whatsapp_attach_pdf"`
		TelegramMessage         *string `json:"telegram_message"` // nil = keep
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	period := strings.TrimSpace(input.DefaultPeriod)
	if period == "" {
		period = defaultPartyStatementPeriod
	}
	if !isKnownPartyStatementPeriod(period) {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Unknown statement period %q", input.DefaultPeriod)})
		return
	}
	days := input.DefaultPeriodDays
	if days == 0 {
		days = defaultStatementPeriodDays
	}
	if err := validateStatementPeriodDays(days); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	mapping, err := normalizePartyStatementMapping(input.WhatsAppVariableMapping)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	s, exists := loadPartyStatementSettings(userID)
	s.DefaultPeriod = period
	s.DefaultPeriodDays = days
	s.WhatsAppMessageID = strings.TrimSpace(input.WhatsAppMessageID)
	s.WhatsAppLanguageCode = strings.TrimSpace(input.WhatsAppLanguageCode)
	s.WhatsAppVariableMapping = mapping
	s.WhatsAppAttachPDF = input.WhatsAppAttachPDF
	if input.WhatsAppTemplateBody != nil {
		body := strings.TrimSpace(strings.ReplaceAll(*input.WhatsAppTemplateBody, "\r\n", "\n"))
		if len(body) > maxWhatsAppTemplateBodyLen {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Template text must be at most %d characters", maxWhatsAppTemplateBodyLen)})
			return
		}
		s.WhatsAppTemplateBody = body
	}
	if input.TelegramMessage != nil {
		msg := strings.TrimSpace(strings.ReplaceAll(*input.TelegramMessage, "\r\n", "\n"))
		if msg == strings.TrimSpace(defaultTelegramStatementMessage) {
			msg = ""
		}
		if n := len([]rune(msg)); n > maxTelegramStatementMessageLen {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Telegram message must be at most %d characters", maxTelegramStatementMessageLen)})
			return
		}
		s.TelegramMessage = msg
	}

	wa, _ := loadWhatsAppInvoiceSettings(userID)
	if s.WhatsAppMessageID != "" && wa.Provider == whatsAppProviderTwilio {
		if !strings.HasPrefix(s.WhatsAppMessageID, "HX") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Twilio Content SID must start with HX"})
			return
		}
		if s.WhatsAppAttachPDF && !whatsAppMappingHas(s.WhatsAppVariableMapping, "statement_pdf_file") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "To attach the statement with Twilio, map your media template's URL variable to \"Statement PDF file\""})
			return
		}
	}

	if exists {
		err = utils.DB.Save(&s).Error
	} else {
		s.ID = uuid.New()
		// Select("*") so a false attach flag isn't replaced by the column default.
		err = utils.DB.Select("*").Create(&s).Error
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save statement settings"})
		return
	}
	c.JSON(http.StatusOK, buildPartyStatementSettingsResponse(s))
}

// parseStatementRange validates a required, inclusive YYYY-MM-DD range.
func parseStatementRange(from, to string) (string, string, error) {
	from, to = strings.TrimSpace(from), strings.TrimSpace(to)
	if from == "" || to == "" {
		return "", "", errors.New("from_date and to_date are required")
	}
	for _, d := range []string{from, to} {
		if _, err := time.Parse("2006-01-02", d); err != nil {
			return "", "", fmt.Errorf("invalid date %q, use YYYY-MM-DD", d)
		}
	}
	if to < from {
		return "", "", errors.New("to_date must be on or after from_date")
	}
	return from, to, nil
}

func isKnownPartyStatementChannel(ch string) bool {
	for _, c := range partyStatementChannels {
		if c == ch {
			return true
		}
	}
	return false
}

// SendPartyStatement sends one party's balance statement on a channel, to the
// party's own contact or an override recipient.
func SendPartyStatement(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	partyID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid party id"})
		return
	}
	var input struct {
		Channel   string `json:"channel"`
		FromDate  string `json:"from_date"`
		ToDate    string `json:"to_date"`
		Recipient string `json:"recipient"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	channel := strings.ToLower(strings.TrimSpace(input.Channel))
	if channel == "" {
		channel = partyStatementChannelWhatsApp
	}
	if !isKnownPartyStatementChannel(channel) {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Sending statements by %q is not available yet", input.Channel)})
		return
	}
	from, to, err := parseStatementRange(input.FromDate, input.ToDate)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	entry, err := sendPartyStatement(userID, partyID, channel, from, to, "manual", input.Recipient, false)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error(), "log": entry})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Statement sent", "log": entry})
}

// TestPartyStatement sends a statement for a party (or the most recently
// updated customer) to a test number, using saved settings.
func TestPartyStatement(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var input struct {
		Channel  string `json:"channel"`
		Phone    string `json:"phone"` // WhatsApp number, or Telegram chat ID
		PartyID  string `json:"party_id"`
		FromDate string `json:"from_date"`
		ToDate   string `json:"to_date"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	channel := strings.ToLower(strings.TrimSpace(input.Channel))
	if channel == "" {
		channel = partyStatementChannelWhatsApp
	}
	if !isKnownPartyStatementChannel(channel) {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Sending statements by %q is not available yet", input.Channel)})
		return
	}
	if strings.TrimSpace(input.Phone) == "" {
		msg := "phone is required"
		if channel == partyStatementChannelTelegram {
			msg = "Telegram chat ID is required"
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": msg})
		return
	}
	from, to, err := parseStatementRange(input.FromDate, input.ToDate)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var partyID uuid.UUID
	if input.PartyID != "" {
		if partyID, err = uuid.Parse(input.PartyID); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid party_id"})
			return
		}
	} else {
		var p models.Party
		if err := utils.DB.Select("id").Where("user_id = ? AND party_type = ?", userID, "customer").
			Order("updated_at DESC").First(&p).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Create at least one customer before sending a test statement"})
			return
		}
		partyID = p.ID
	}

	entry, err := sendPartyStatement(userID, partyID, channel, from, to, "test", input.Phone, false)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error(), "log": entry})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Test statement sent", "log": entry})
}

// partyStatementBulkRuns guards against overlapping bulk runs per store.
var partyStatementBulkRuns sync.Map // userID -> struct{}

// SendPartyStatementsBulk queues statements for the selected parties (or all
// parties matching the list filters) and sends them one by one in the
// background. Parties with no balance at the end of the period are skipped
// unless include_zero_balance is set. Results land in the statement log.
func SendPartyStatementsBulk(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var input struct {
		Channel            string           `json:"channel"`
		FromDate           string           `json:"from_date"`
		ToDate             string           `json:"to_date"`
		IDs                []uuid.UUID      `json:"ids"`
		AllMatching        bool             `json:"all_matching"`
		Filters            PartyBulkFilters `json:"filters"`
		IncludeZeroBalance bool             `json:"include_zero_balance"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	channel := strings.ToLower(strings.TrimSpace(input.Channel))
	if channel == "" {
		channel = partyStatementChannelWhatsApp
	}
	if !isKnownPartyStatementChannel(channel) {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Sending statements by %q is not available yet", input.Channel)})
		return
	}
	from, to, err := parseStatementRange(input.FromDate, input.ToDate)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if msg := partyStatementChannelReadyError(userID, channel); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msg})
		return
	}

	var query *gorm.DB
	if input.AllMatching {
		f := input.Filters
		query = partyFilterQuery(userID, f.Search, f.Category, f.PartyType, f.Balance)
	} else {
		if len(input.IDs) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Select at least one party"})
			return
		}
		query = utils.DB.Model(&models.Party{}).Where("user_id = ? AND id IN ?", userID, input.IDs)
	}
	var ids []uuid.UUID
	if err := query.Order("name ASC").Limit(maxPartyStatementBulk+1).Pluck("id", &ids).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load parties"})
		return
	}
	if len(ids) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No parties match the selection"})
		return
	}
	if len(ids) > maxPartyStatementBulk {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Select at most %d parties per run", maxPartyStatementBulk)})
		return
	}
	ids, excluded := withoutExcludedStatementParties(userID, ids)
	if len(ids) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "All the selected parties are excluded from statements"})
		return
	}

	if _, running := partyStatementBulkRuns.LoadOrStore(userID, struct{}{}); running {
		c.JSON(http.StatusConflict, gin.H{"error": "A bulk statement run is already in progress for this store. Try again when it finishes."})
		return
	}
	skipZero := !input.IncludeZeroBalance
	go func() {
		defer partyStatementBulkRuns.Delete(userID)
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[party-statement] bulk run panic: %v", r)
			}
		}()
		res := runPartyStatementBatch(userID, ids, channel, from, to, "bulk", skipZero)
		log.Printf("[party-statement] bulk run for %s: %s", userID, res.summary())
	}()

	msg := fmt.Sprintf("Sending statements to %d parties in the background", len(ids))
	if excluded > 0 {
		msg += fmt.Sprintf(" (%d excluded)", excluded)
	}
	c.JSON(http.StatusAccepted, gin.H{"message": msg, "queued": len(ids), "excluded": excluded})
}

type partyStatementBatchResult struct {
	Total, Sent, Failed, Skipped, ZeroBalance, Excluded int
}

func (r partyStatementBatchResult) summary() string {
	s := fmt.Sprintf("%d sent", r.Sent)
	if r.Failed > 0 {
		s += fmt.Sprintf(", %d failed", r.Failed)
	}
	if r.Skipped > 0 {
		s += fmt.Sprintf(", %d skipped (no mobile)", r.Skipped)
	}
	if r.ZeroBalance > 0 {
		s += fmt.Sprintf(", %d with zero balance", r.ZeroBalance)
	}
	if r.Excluded > 0 {
		s += fmt.Sprintf(", %d excluded", r.Excluded)
	}
	return s + fmt.Sprintf(" of %d parties", r.Total)
}

// runPartyStatementBatch sends statements to each party in turn, pausing
// between sends so the gateway doesn't rate-limit the run.
func runPartyStatementBatch(userID uuid.UUID, ids []uuid.UUID, channel, from, to, trigger string, skipZero bool) partyStatementBatchResult {
	res := partyStatementBatchResult{Total: len(ids)}
	ids, res.Excluded = withoutExcludedStatementParties(userID, ids)
	for i, id := range ids {
		if i > 0 {
			time.Sleep(partyStatementBulkDelay)
		}
		entry, err := sendPartyStatement(userID, id, channel, from, to, trigger, "", skipZero)
		switch {
		case entry == nil:
			res.ZeroBalance++
		case entry.Status == "skipped":
			res.Skipped++
		case err != nil:
			res.Failed++
		default:
			res.Sent++
		}
	}
	return res
}

// GetPartyStatementLogs lists recent statement sends (optionally one party's).
func GetPartyStatementLogs(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	q := utils.DB.Where("user_id = ?", userID)
	if pid := c.Query("party_id"); pid != "" {
		q = q.Where("party_id = ?", pid)
	}
	if ch := c.Query("channel"); ch != "" {
		q = q.Where("channel = ?", ch)
	}
	logs := make([]models.PartyStatementLog, 0)
	if err := q.Order("created_at DESC").Limit(limit).Find(&logs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load statement log"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"logs": logs, "bulk_running": partyStatementBulkRunning(userID)})
}

func partyStatementBulkRunning(userID uuid.UUID) bool {
	_, ok := partyStatementBulkRuns.Load(userID)
	return ok
}

// GetPartyStatementPDF downloads the detailed periodic statement PDF, the
// same document attached to the message.
func GetPartyStatementPDF(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	partyID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid party id"})
		return
	}
	from, to, err := parseStatementRange(c.Query("from_date"), c.Query("to_date"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	pdfBytes, ledger, err := renderPartyStatementPDF(userID, partyID, from, to)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", partyStatementPDFFilename(ledger.Party.Name, from, to)))
	c.Data(http.StatusOK, "application/pdf", pdfBytes)
}

func renderPartyStatementPDF(userID, partyID uuid.UUID, from, to string) ([]byte, *PartyLedger, error) {
	ledger, err := loadPartyLedger(userID, partyID, from, to)
	if err != nil {
		return nil, nil, err
	}
	business := resolveSellerBusiness(userID, nil)
	loc, _ := ConfiguredLocationForUser(userID)
	pdfBytes, err := buildPartyStatementPDF(ledger, &business, loc)
	if err != nil {
		return nil, nil, errors.New("failed to generate statement PDF")
	}
	return pdfBytes, ledger, nil
}

// partyStatementChannelReadyError returns why the channel can't send yet, or "".
func partyStatementChannelReadyError(userID uuid.UUID, channel string) string {
	switch channel {
	case partyStatementChannelWhatsApp:
		wa, exists := loadWhatsAppInvoiceSettings(userID)
		if !whatsAppGatewayReady(wa, exists) {
			return "Connect WhatsApp first (Settings → WhatsApp Invoice → WhatsApp connection)"
		}
		st, _ := loadPartyStatementSettings(userID)
		if st.WhatsAppMessageID == "" {
			return "Set the statement WhatsApp template in Settings → WhatsApp Invoice → Party Statement Messages"
		}
		if (st.WhatsAppAttachPDF || partyStatementMappingNeedsPDF(st.WhatsAppVariableMapping)) && whatsAppInvoicePublicBaseURL(wa) == "" {
			return "Public API URL is required to attach the statement PDF (Settings → WhatsApp Invoice)"
		}
		return ""
	case partyStatementChannelTelegram:
		if _, _, err := telegramReadyToken(userID); err != nil {
			return err.Error()
		}
		return ""
	}
	return fmt.Sprintf("Sending statements by %q is not available yet", channel)
}

// sendPartyStatement sends the statement on the channel and records a log
// row. With skipZero, a party whose balance at the end of the period is zero
// is skipped and (nil, nil) returned without logging.
func sendPartyStatement(userID, partyID uuid.UUID, channel, from, to, trigger, recipient string, skipZero bool) (*models.PartyStatementLog, error) {
	entry := &models.PartyStatementLog{
		ID:        uuid.New(),
		UserID:    userID,
		PartyID:   partyID,
		Channel:   channel,
		FromDate:  from,
		ToDate:    to,
		Trigger:   trigger,
		CreatedAt: time.Now(),
	}
	finish := func(status string, err error) (*models.PartyStatementLog, error) {
		entry.Status = status
		if err != nil {
			entry.Error = err.Error()
		}
		if dbErr := utils.DB.Create(entry).Error; dbErr != nil {
			log.Printf("[party-statement] failed to write log: %v", dbErr)
		}
		return entry, err
	}

	ledger, err := loadPartyLedger(userID, partyID, from, to)
	if err != nil {
		return finish("failed", errors.New("party not found"))
	}
	entry.PartyName = ledger.Party.Name
	entry.OpeningBalance = roundMoney(ledger.OpeningBalance)
	entry.ClosingBalance = roundMoney(ledger.ClosingBalance)
	if skipZero && math.Abs(ledger.ClosingBalance) < 0.005 {
		return nil, nil
	}

	switch channel {
	case partyStatementChannelWhatsApp:
		return sendPartyStatementWhatsApp(entry, ledger, recipient, finish)
	case partyStatementChannelTelegram:
		return sendPartyStatementTelegram(entry, ledger, recipient, finish)
	}
	return finish("failed", fmt.Errorf("sending statements by %q is not available yet", channel))
}

func sendPartyStatementWhatsApp(
	entry *models.PartyStatementLog,
	ledger *PartyLedger,
	recipient string,
	finish func(string, error) (*models.PartyStatementLog, error),
) (*models.PartyStatementLog, error) {
	userID := entry.UserID
	if msg := partyStatementChannelReadyError(userID, partyStatementChannelWhatsApp); msg != "" {
		return finish("failed", errors.New(msg))
	}
	wa, _ := loadWhatsAppInvoiceSettings(userID)
	st, _ := loadPartyStatementSettings(userID)

	secret, err := utils.Decrypt(wa.EncryptedAPIKey)
	if err != nil {
		return finish("failed", fmt.Errorf("failed to decrypt %s", whatsAppCredentialName(wa.Provider)))
	}

	rawPhone := recipient
	if strings.TrimSpace(rawPhone) == "" {
		rawPhone = ledger.Party.Phone
	}
	phone := normalizeWhatsAppNumber(rawPhone, wa.CountryCode)
	entry.Recipient = phone
	if phone == "" {
		return finish("skipped", errors.New("party has no valid mobile number"))
	}

	business := resolveSellerBusiness(userID, nil)
	values := partyStatementValues(ledger, &business, wa.StoreDisplayName)

	pdfURL := ""
	if st.WhatsAppAttachPDF || partyStatementMappingNeedsPDF(st.WhatsAppVariableMapping) {
		base := whatsAppInvoicePublicBaseURL(wa)
		ttl := time.Duration(wa.PDFLinkTTLMinutes) * time.Minute
		file := signPartyStatementToken(ledger.Party.ID, ledger.FromDate, ledger.ToDate, time.Now().Add(ttl)) + ".pdf"
		pdfURL = base + publicPartyStatementPDFPath + file
		values["statement_pdf_file"] = file
		values["statement_pdf_url"] = pdfURL
	}

	vars := make([]string, 0, 8)
	for _, key := range strings.Split(st.WhatsAppVariableMapping, ",") {
		vars = append(vars, values[strings.TrimSpace(key)])
	}

	if wa.Provider == whatsAppProviderTwilio {
		sid, body, err := callTwilioWhatsAppAPI(wa.AccountSID, secret, wa.PhoneNumberID, phone, st.WhatsAppMessageID, vars, wa.CountryCode)
		entry.RequestID = sid
		entry.Response = truncateWALog(body, 2000)
		if err != nil {
			return finish("failed", err)
		}
		return finish("sent", nil)
	}

	lang := st.WhatsAppLanguageCode
	if lang == "" {
		lang = wa.LanguageCode
	}
	docLink := ""
	if st.WhatsAppAttachPDF {
		docLink = pdfURL
	}
	payload := buildWhatsAppTemplatePayload(phone, st.WhatsAppMessageID, lang, docLink,
		partyStatementPDFFilename(ledger.Party.Name, ledger.FromDate, ledger.ToDate), vars)
	requestID, body, err := callWhatsAppAPI(wa.Provider, wa.PhoneNumberID, secret, payload)
	entry.RequestID = requestID
	entry.Response = truncateWALog(body, 2000)
	if err != nil {
		return finish("failed", err)
	}
	return finish("sent", nil)
}

// sendPartyStatementTelegram sends the statement PDF with a balance summary to
// the party's linked Telegram chat (or the recipient chat ID override).
func sendPartyStatementTelegram(
	entry *models.PartyStatementLog,
	ledger *PartyLedger,
	recipient string,
	finish func(string, error) (*models.PartyStatementLog, error),
) (*models.PartyStatementLog, error) {
	userID := entry.UserID
	_, token, err := telegramReadyToken(userID)
	if err != nil {
		return finish("failed", err)
	}
	chatID := strings.TrimSpace(recipient)
	if chatID == "" {
		chatID = strings.TrimSpace(ledger.Party.TelegramChatID)
	}
	entry.Recipient = chatID
	if chatID == "" {
		return finish("skipped", errors.New("party has not linked Telegram yet"))
	}

	business := resolveSellerBusiness(userID, nil)
	loc, _ := ConfiguredLocationForUser(userID)
	pdfBytes, err := buildPartyStatementPDF(ledger, &business, loc)
	if err != nil {
		return finish("failed", errors.New("failed to generate statement PDF"))
	}
	st, _ := loadPartyStatementSettings(userID)
	wa, _ := loadWhatsAppInvoiceSettings(userID)
	text := renderTelegramStatementMessage(st.TelegramMessage, partyStatementValues(ledger, &business, wa.StoreDisplayName))
	filename := partyStatementPDFFilename(ledger.Party.Name, ledger.FromDate, ledger.ToDate)
	// A document caption holds 1024 characters; a longer message goes first
	// as its own text message, then the PDF without a caption.
	caption := text
	if len([]rune(text)) > telegramCaptionLimit {
		if err := telegramSendMessage(token, chatID, text); err != nil {
			return finish("failed", err)
		}
		caption = ""
	}
	if err := telegramSendDocument(token, chatID, filename, pdfBytes, caption); err != nil {
		return finish("failed", err)
	}
	return finish("sent", nil)
}

const maxTelegramStatementMessageLen = 3500

// defaultTelegramStatementMessage mirrors the WhatsApp balance reminder.
const defaultTelegramStatementMessage = `Hi {customer_name},
Here is a reminder from {store_name}.
Your balance due for the period {from_date} to {to_date} is as follows:
• Previous Balance : ₹{opening_balance}
• Current Balance : ₹{closing_balance}
Please find the detailed statement attached.
Thank you.`

// telegramStatementFields are the placeholders a Telegram message can use;
// the PDF is always attached, so the PDF link fields don't apply.
func telegramStatementFields() []whatsAppInvoiceField {
	out := make([]whatsAppInvoiceField, 0, len(partyStatementFields))
	for _, f := range partyStatementFields {
		if !isPartyStatementPDFField(f.Key) {
			out = append(out, f)
		}
	}
	return out
}

// renderTelegramStatementMessage fills {field} placeholders in the store's
// message (or the default) and returns Telegram HTML: the text is escaped,
// and **bold** marks become <b>…</b>. Unknown placeholders are left as typed.
func renderTelegramStatementMessage(tmpl string, values map[string]string) string {
	if strings.TrimSpace(tmpl) == "" {
		tmpl = defaultTelegramStatementMessage
	}
	out := html.EscapeString(tmpl)
	for _, f := range telegramStatementFields() {
		out = strings.ReplaceAll(out, "{"+f.Key+"}", html.EscapeString(values[f.Key]))
	}
	// **bold**: pair up markers; an unpaired one stays literal.
	parts := strings.Split(out, "**")
	if len(parts) >= 3 {
		var b strings.Builder
		for i, p := range parts {
			if i > 0 {
				switch {
				case i%2 == 1 && i < len(parts)-1:
					b.WriteString("<b>")
				case i%2 == 0:
					b.WriteString("</b>")
				default:
					b.WriteString("**")
				}
			}
			b.WriteString(p)
		}
		out = b.String()
	}
	return strings.TrimSpace(out)
}

// formatStatementBalance renders a ledger balance for a message: receivables
// as plain amounts ("1,250.00"), advances / payables with a Cr suffix
// ("500.00 Cr") so the text never reads "₹-500.00".
func formatStatementBalance(v float64) string {
	if v < -0.005 {
		return formatINRAmount(-v) + " Cr"
	}
	return formatINRAmount(math.Abs(v))
}

func statementDisplayDate(iso string) string {
	t, err := time.Parse("2006-01-02", iso)
	if err != nil {
		return iso
	}
	return t.Format("02-01-2006")
}

func partyStatementStoreName(userID uuid.UUID, displayName string) string {
	if n := strings.TrimSpace(displayName); n != "" {
		return n
	}
	return resolveSellerBusiness(userID, nil).Name
}

func partyStatementValues(ledger *PartyLedger, business *models.Business, storeDisplayName string) map[string]string {
	storeName := strings.TrimSpace(storeDisplayName)
	if storeName == "" {
		storeName = business.Name
	}
	name := strings.TrimSpace(ledger.Party.Name)
	if name == "" {
		name = "Customer"
	}
	received := ledger.TotalReceived
	sales := ledger.TotalSales
	if ledger.Party.PartyType == "vendor" {
		sales, received = ledger.TotalPurchases, ledger.TotalPaid
	}
	values := map[string]string{
		"customer_name":      name,
		"store_name":         storeName,
		"from_date":          statementDisplayDate(ledger.FromDate),
		"to_date":            statementDisplayDate(ledger.ToDate),
		"opening_balance":    formatStatementBalance(ledger.OpeningBalance),
		"closing_balance":    formatStatementBalance(ledger.ClosingBalance),
		"total_sales":        formatINRAmount(sales),
		"total_received":     formatINRAmount(received),
		"store_phone":        business.Phone,
		"statement_pdf_url":  "",
		"statement_pdf_file": "",
	}
	sanitizeWhatsAppValues(values)
	return values
}

func partyStatementPDFFilename(partyName, from, to string) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, strings.TrimSpace(partyName))
	if name == "" {
		name = "Party"
	}
	return fmt.Sprintf("Statement_%s_%s_to_%s.pdf", name, from, to)
}

// ---- Public, signed statement PDF link (fetched by the WhatsApp gateway) ----
//
// Token = party ID (16) | from day (4) | to day (4) | expiry unix (8) | HMAC (16),
// base64url. Days count from the Unix epoch so the range is exact.

func statementDayNumber(iso string) uint32 {
	t, _ := time.Parse("2006-01-02", iso)
	return uint32(t.Unix() / 86400)
}

func statementDayISO(n uint32) string {
	return time.Unix(int64(n)*86400, 0).UTC().Format("2006-01-02")
}

func signPartyStatementToken(partyID uuid.UUID, from, to string, expires time.Time) string {
	payload := make([]byte, 32)
	copy(payload[:16], partyID[:])
	binary.BigEndian.PutUint32(payload[16:20], statementDayNumber(from))
	binary.BigEndian.PutUint32(payload[20:24], statementDayNumber(to))
	binary.BigEndian.PutUint64(payload[24:], uint64(expires.Unix()))
	sig := utils.SignHMAC(partyStatementPDFPurpose, payload)[:16]
	return base64.RawURLEncoding.EncodeToString(append(payload, sig...))
}

func verifyPartyStatementToken(token string) (partyID uuid.UUID, from, to string, ok bool) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 48 {
		return uuid.Nil, "", "", false
	}
	payload, sig := raw[:32], raw[32:]
	if !hmac.Equal(sig, utils.SignHMAC(partyStatementPDFPurpose, payload)[:16]) {
		return uuid.Nil, "", "", false
	}
	if time.Now().Unix() > int64(binary.BigEndian.Uint64(payload[24:])) {
		return uuid.Nil, "", "", false
	}
	id, err := uuid.FromBytes(payload[:16])
	if err != nil {
		return uuid.Nil, "", "", false
	}
	return id, statementDayISO(binary.BigEndian.Uint32(payload[16:20])),
		statementDayISO(binary.BigEndian.Uint32(payload[20:24])), true
}

// PublicPartyStatementPDF serves a statement PDF for a signed, expiring
// token. No auth: the WhatsApp gateway downloads the attachment from here.
func PublicPartyStatementPDF(c *gin.Context) {
	token := strings.TrimSuffix(c.Param("token"), ".pdf")
	partyID, from, to, ok := verifyPartyStatementToken(token)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "Link is invalid or has expired"})
		return
	}
	var party models.Party
	if err := utils.DB.Select("id", "user_id").Where("id = ?", partyID).First(&party).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Statement not found"})
		return
	}
	pdfBytes, ledger, err := renderPartyStatementPDF(party.UserID, partyID, from, to)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate statement PDF"})
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf("inline; filename=%q", partyStatementPDFFilename(ledger.Party.Name, from, to)))
	c.Header("Cache-Control", "private, max-age=3600")
	c.Data(http.StatusOK, "application/pdf", pdfBytes)
}
