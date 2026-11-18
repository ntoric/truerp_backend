package controllers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Automatic WhatsApp "purchase summary" message (with invoice PDF) sent to the
// customer when a sales invoice is created. Each store picks its gateway:
// Meta's WhatsApp Cloud API directly, Fast2SMS's Meta-format WhatsApp API, or
// Twilio. Meta and Fast2SMS take the same template payload and differ only in
// endpoint, auth header and error format; Twilio uses its own Messages API
// with Content templates (see callTwilioWhatsAppAPI). Configured and toggled
// per store (store-owner user_id scope).

const (
	whatsAppProviderCloud    = "whatsapp_cloud"
	whatsAppProviderFast2SMS = "fast2sms"
	whatsAppProviderTwilio   = "twilio"

	defaultWhatsAppCloudAPIBase  = "https://graph.facebook.com/v21.0"
	defaultFast2SMSWhatsAppBase  = "https://www.fast2sms.com/dev/whatsapp/v26.0"
	defaultTwilioAPIBase         = "https://api.twilio.com/2010-04-01"
	defaultWhatsAppInvoiceFields = "invoice_number,total_amount,invoice_date,points_earned,points_balance,points_redeemed,store_name"
	// Default mapping for the template used while the loyalty program is off.
	defaultWhatsAppNoLoyaltyFields = "invoice_number,total_amount,invoice_date,store_name"
	defaultPDFLinkTTLMinutes       = 60
	// WhatsApp caps a template body at 1024 characters; allow some slack.
	maxWhatsAppTemplateBodyLen = 4096
	minPDFLinkTTLMinutes       = 5
	maxPDFLinkTTLMinutes       = 7 * 24 * 60
	whatsAppInvoicePDFPurpose  = "wa-invoice-pdf"
	publicInvoicePDFPath       = "/api/v1/public/invoice-pdf/"
)

type whatsAppInvoiceField struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// whatsAppInvoiceFields lists the values a template variable can be mapped to.
var whatsAppInvoiceFields = []whatsAppInvoiceField{
	{"invoice_number", "Invoice number"},
	{"total_amount", "Invoice amount"},
	{"invoice_date", "Invoice date"},
	{"points_earned", "Loyalty points credited"},
	{"points_balance", "Loyalty points balance"},
	{"points_redeemed", "Loyalty points redeemed"},
	{"store_name", "Store / business name"},
	{"customer_name", "Customer name"},
	{"amount_paid", "Amount paid"},
	{"balance_due", "Balance due"},
	{"loyalty_discount", "Loyalty discount (₹)"},
	{"store_phone", "Store phone"},
	{"invoice_pdf_url", "Invoice PDF link (full URL)"},
	{"invoice_pdf_file", "Invoice PDF file (Twilio media URL variable)"},
}

// whatsAppPDFFields need a signed PDF link and therefore a public base URL.
func isWhatsAppPDFField(key string) bool {
	return key == "invoice_pdf_url" || key == "invoice_pdf_file"
}

func whatsAppMappingHas(mapping, key string) bool {
	for _, k := range strings.Split(mapping, ",") {
		if strings.TrimSpace(k) == key {
			return true
		}
	}
	return false
}

func whatsAppMappingNeedsPDF(mapping string) bool {
	for _, k := range strings.Split(mapping, ",") {
		if isWhatsAppPDFField(strings.TrimSpace(k)) {
			return true
		}
	}
	return false
}

func isKnownWhatsAppInvoiceField(key string) bool {
	for _, f := range whatsAppInvoiceFields {
		if f.Key == key {
			return true
		}
	}
	return false
}

type whatsAppProviderInfo struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

var whatsAppProviders = []whatsAppProviderInfo{
	{whatsAppProviderCloud, "WhatsApp Cloud API (Meta)"},
	{whatsAppProviderFast2SMS, "Fast2SMS"},
	{whatsAppProviderTwilio, "Twilio"},
}

func isKnownWhatsAppProvider(p string) bool {
	for _, info := range whatsAppProviders {
		if info.Key == p {
			return true
		}
	}
	return false
}

// normalizeWhatsAppProvider maps empty/legacy values to the Meta provider.
func normalizeWhatsAppProvider(p string) string {
	p = strings.ToLower(strings.TrimSpace(p))
	if isKnownWhatsAppProvider(p) {
		return p
	}
	return whatsAppProviderCloud
}

func whatsAppProviderLabel(p string) string {
	for _, info := range whatsAppProviders {
		if info.Key == p {
			return info.Label
		}
	}
	return p
}

// whatsAppAPIBase returns the gateway's base URL (overridable per provider
// via env, e.g. for staging or a newer API version).
func whatsAppAPIBase(provider string) string {
	envKey, def := "WHATSAPP_CLOUD_API_BASE", defaultWhatsAppCloudAPIBase
	switch provider {
	case whatsAppProviderFast2SMS:
		envKey, def = "FAST2SMS_WHATSAPP_API_BASE", defaultFast2SMSWhatsAppBase
	case whatsAppProviderTwilio:
		envKey, def = "TWILIO_API_BASE", defaultTwilioAPIBase
	}
	if u := strings.TrimSpace(os.Getenv(envKey)); u != "" {
		return u
	}
	return def
}

// whatsAppCredentialName is the user-facing name of the secret per provider.
func whatsAppCredentialName(provider string) string {
	switch provider {
	case whatsAppProviderFast2SMS:
		return "Fast2SMS API key"
	case whatsAppProviderTwilio:
		return "Twilio Auth Token"
	}
	return "WhatsApp Cloud API access token"
}

// whatsAppSenderName / whatsAppTemplateName are the user-facing names of the
// sender and template fields per provider.
func whatsAppSenderName(provider string) string {
	if provider == whatsAppProviderTwilio {
		return "WhatsApp sender (number or Messaging Service SID)"
	}
	return "Phone Number ID"
}

func whatsAppTemplateName(provider string) string {
	if provider == whatsAppProviderTwilio {
		return "Content SID"
	}
	return "Template name"
}

func loadWhatsAppInvoiceSettings(userID uuid.UUID) (models.WhatsAppInvoiceSettings, bool) {
	var s models.WhatsAppInvoiceSettings
	if err := utils.DB.Where("user_id = ?", userID).First(&s).Error; err != nil {
		return models.WhatsAppInvoiceSettings{
			UserID:                   userID,
			Provider:                 whatsAppProviderCloud,
			VariableMapping:          defaultWhatsAppInvoiceFields,
			NoLoyaltyVariableMapping: defaultWhatsAppNoLoyaltyFields,
			AttachPDF:                true,
			CountryCode:              "91",
			LanguageCode:             "en",
			SendForPOS:               true,
			SendForNonPOS:            true,
			PDFLinkTTLMinutes:        defaultPDFLinkTTLMinutes,
		}, false
	}
	s.Provider = normalizeWhatsAppProvider(s.Provider)
	if s.PDFLinkTTLMinutes <= 0 {
		s.PDFLinkTTLMinutes = defaultPDFLinkTTLMinutes
	}
	if strings.TrimSpace(s.VariableMapping) == "" {
		s.VariableMapping = defaultWhatsAppInvoiceFields
	}
	if strings.TrimSpace(s.NoLoyaltyVariableMapping) == "" {
		s.NoLoyaltyVariableMapping = defaultWhatsAppNoLoyaltyFields
	}
	if strings.TrimSpace(s.CountryCode) == "" {
		s.CountryCode = "91"
	}
	if strings.TrimSpace(s.LanguageCode) == "" {
		s.LanguageCode = "en"
	}
	return s, true
}

func whatsAppInvoicePublicBaseURL(s models.WhatsAppInvoiceSettings) string {
	base := strings.TrimSpace(s.PublicBaseURL)
	if base == "" {
		base = strings.TrimSpace(os.Getenv("PUBLIC_API_BASE_URL"))
	}
	return strings.TrimRight(base, "/")
}

// storeLoyaltyEnabled reports whether the store's loyalty program is on
// (without creating a settings row as a side effect).
func storeLoyaltyEnabled(userID uuid.UUID) bool {
	var ls models.LoyaltySettings
	if err := utils.DB.Select("is_enabled").Where("user_id = ?", userID).First(&ls).Error; err != nil {
		return false
	}
	return ls.IsEnabled
}

// whatsAppActiveTemplate returns the template and variable mapping to send:
// the "without loyalty" template while the loyalty program is off and one is
// configured, otherwise the main template.
func whatsAppActiveTemplate(s models.WhatsAppInvoiceSettings, loyaltyEnabled bool) (messageID, mapping string, noLoyalty bool) {
	if !loyaltyEnabled && strings.TrimSpace(s.NoLoyaltyMessageID) != "" {
		return s.NoLoyaltyMessageID, s.NoLoyaltyVariableMapping, true
	}
	return s.MessageID, s.VariableMapping, false
}

type whatsAppInvoiceSettingsResponse struct {
	models.WhatsAppInvoiceSettings
	HasAPIKey  bool   `json:"has_api_key"`
	APIKeyHint string `json:"api_key_hint"`
	// Phone Number ID is masked like the API key; only super admins get the
	// full value back (in phone_number_id) so they can view it.
	HasPhoneNumberID  bool                   `json:"has_phone_number_id"`
	PhoneNumberIDHint string                 `json:"phone_number_id_hint"`
	CanViewPhoneID    bool                   `json:"can_view_phone_number_id"`
	AvailableFields   []whatsAppInvoiceField `json:"available_fields"`
	Providers         []whatsAppProviderInfo `json:"providers"`
	DefaultMapping    string                 `json:"default_mapping"`
	DefaultNoLoyalty  string                 `json:"default_no_loyalty_mapping"`
	LoyaltyEnabled    bool                   `json:"loyalty_enabled"`
	EnvPublicURL      string                 `json:"env_public_base_url"`
	CanEditLinkTTL    bool                   `json:"can_edit_link_ttl"`
	MinLinkTTL        int                    `json:"min_link_ttl_minutes"`
	MaxLinkTTL        int                    `json:"max_link_ttl_minutes"`
}

func isSuperAdminRequest(c *gin.Context) bool {
	role, _ := c.Get("role")
	roleStr, _ := role.(string)
	return utils.IsSuperAdminRole(roleStr)
}

func buildWhatsAppInvoiceSettingsResponse(c *gin.Context, s models.WhatsAppInvoiceSettings) whatsAppInvoiceSettingsResponse {
	hint := ""
	if s.EncryptedAPIKey != "" {
		if key, err := utils.Decrypt(s.EncryptedAPIKey); err == nil && len(key) >= 4 {
			hint = "••••" + key[len(key)-4:]
		}
	}
	phoneID := strings.TrimSpace(s.PhoneNumberID)
	phoneHint := ""
	if len(phoneID) >= 4 {
		phoneHint = "••••" + phoneID[len(phoneID)-4:]
	} else if phoneID != "" {
		phoneHint = "••••"
	}
	superAdmin := isSuperAdminRequest(c)
	if !superAdmin {
		s.PhoneNumberID = ""
	}
	return whatsAppInvoiceSettingsResponse{
		WhatsAppInvoiceSettings: s,
		HasAPIKey:               s.EncryptedAPIKey != "",
		APIKeyHint:              hint,
		HasPhoneNumberID:        phoneID != "",
		PhoneNumberIDHint:       phoneHint,
		CanViewPhoneID:          superAdmin,
		AvailableFields:         whatsAppInvoiceFields,
		Providers:               whatsAppProviders,
		DefaultMapping:          defaultWhatsAppInvoiceFields,
		DefaultNoLoyalty:        defaultWhatsAppNoLoyaltyFields,
		LoyaltyEnabled:          storeLoyaltyEnabled(s.UserID),
		EnvPublicURL:            strings.TrimSpace(os.Getenv("PUBLIC_API_BASE_URL")),
		CanEditLinkTTL:          superAdmin,
		MinLinkTTL:              minPDFLinkTTLMinutes,
		MaxLinkTTL:              maxPDFLinkTTLMinutes,
	}
}

// GetWhatsAppInvoiceSettings returns the active store's settings.
func GetWhatsAppInvoiceSettings(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	s, _ := loadWhatsAppInvoiceSettings(userID)
	c.JSON(http.StatusOK, buildWhatsAppInvoiceSettingsResponse(c, s))
}

// UpdateWhatsAppInvoiceSettings saves the active store's settings.
func UpdateWhatsAppInvoiceSettings(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		Enabled          bool    `json:"enabled"`
		Provider         string  `json:"provider"`
		APIKey           *string `json:"api_key"` // nil/"" = keep existing
		ClearAPIKey      bool    `json:"clear_api_key"`
		MessageID        string  `json:"message_id"`
		PhoneNumberID    *string `json:"phone_number_id"` // nil/"" = keep existing
		AccountSID       *string `json:"account_sid"`     // nil = keep existing
		LanguageCode     string  `json:"language_code"`
		VariableMapping  string  `json:"variable_mapping"`
		StoreDisplayName string  `json:"store_display_name"`
		TemplateBody     *string `json:"template_body"` // nil = keep existing
		// Template used while the loyalty program is off; nil = keep existing.
		NoLoyaltyMessageID       *string `json:"no_loyalty_message_id"`
		NoLoyaltyVariableMapping *string `json:"no_loyalty_variable_mapping"`
		NoLoyaltyTemplateBody    *string `json:"no_loyalty_template_body"`
		AttachPDF                bool    `json:"attach_pdf"`
		PublicBaseURL            string  `json:"public_base_url"`
		CountryCode              string  `json:"country_code"`
		SendForPOS               bool    `json:"send_for_pos"`
		SendForNonPOS            bool    `json:"send_for_non_pos"`
		// Super admin only; ignored for other roles.
		PDFLinkTTLMinutes *int `json:"pdf_link_ttl_minutes"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	mapping, err := normalizeWhatsAppVariableMapping(input.VariableMapping)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	publicURL := strings.TrimRight(strings.TrimSpace(input.PublicBaseURL), "/")
	if publicURL != "" {
		if u, err := url.Parse(publicURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Public base URL must be a full http(s) URL, e.g. https://api.example.com"})
			return
		}
	}

	countryCode := onlyDigits(input.CountryCode)
	if countryCode == "" {
		countryCode = "91"
	}

	languageCode := strings.TrimSpace(input.LanguageCode)
	if languageCode == "" {
		languageCode = "en"
	}

	provider := strings.ToLower(strings.TrimSpace(input.Provider))
	if provider == "" {
		provider = whatsAppProviderCloud
	}
	if !isKnownWhatsAppProvider(provider) {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Unsupported WhatsApp provider %q", input.Provider)})
		return
	}

	s, exists := loadWhatsAppInvoiceSettings(userID)
	providerChanged := exists && s.Provider != provider
	s.Enabled = input.Enabled
	s.Provider = provider
	s.MessageID = strings.TrimSpace(input.MessageID)
	if input.PhoneNumberID != nil && strings.TrimSpace(*input.PhoneNumberID) != "" {
		s.PhoneNumberID = strings.TrimSpace(*input.PhoneNumberID)
	}
	if input.AccountSID != nil {
		s.AccountSID = strings.TrimSpace(*input.AccountSID)
	}
	s.LanguageCode = languageCode
	s.VariableMapping = mapping
	s.StoreDisplayName = strings.TrimSpace(input.StoreDisplayName)
	if input.TemplateBody != nil {
		body := strings.TrimSpace(strings.ReplaceAll(*input.TemplateBody, "\r\n", "\n"))
		if len(body) > maxWhatsAppTemplateBodyLen {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Template text must be at most %d characters", maxWhatsAppTemplateBodyLen)})
			return
		}
		s.TemplateBody = body
	}
	if input.NoLoyaltyMessageID != nil {
		s.NoLoyaltyMessageID = strings.TrimSpace(*input.NoLoyaltyMessageID)
	}
	if input.NoLoyaltyVariableMapping != nil {
		m := strings.TrimSpace(*input.NoLoyaltyVariableMapping)
		if m == "" {
			m = defaultWhatsAppNoLoyaltyFields
		}
		m, err := normalizeWhatsAppVariableMapping(m)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Template without loyalty: " + err.Error()})
			return
		}
		s.NoLoyaltyVariableMapping = m
	}
	if input.NoLoyaltyTemplateBody != nil {
		body := strings.TrimSpace(strings.ReplaceAll(*input.NoLoyaltyTemplateBody, "\r\n", "\n"))
		if len(body) > maxWhatsAppTemplateBodyLen {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Template text must be at most %d characters", maxWhatsAppTemplateBodyLen)})
			return
		}
		s.NoLoyaltyTemplateBody = body
	}
	s.AttachPDF = input.AttachPDF
	s.PublicBaseURL = publicURL
	s.CountryCode = countryCode
	s.SendForPOS = input.SendForPOS
	s.SendForNonPOS = input.SendForNonPOS

	if input.PDFLinkTTLMinutes != nil && *input.PDFLinkTTLMinutes != s.PDFLinkTTLMinutes {
		if !isSuperAdminRequest(c) {
			c.JSON(http.StatusForbidden, gin.H{"error": "Only a super admin can change the PDF link expiry"})
			return
		}
		ttl := *input.PDFLinkTTLMinutes
		if ttl < minPDFLinkTTLMinutes || ttl > maxPDFLinkTTLMinutes {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("PDF link expiry must be between %d minutes and %d days", minPDFLinkTTLMinutes, maxPDFLinkTTLMinutes/(24*60))})
			return
		}
		s.PDFLinkTTLMinutes = ttl
	}

	// A Meta token is useless for Fast2SMS and vice versa, so switching
	// provider without supplying a new secret drops the old one.
	newKey := input.APIKey != nil && strings.TrimSpace(*input.APIKey) != ""
	if input.ClearAPIKey || (providerChanged && !newKey) {
		s.EncryptedAPIKey = ""
	} else if input.APIKey != nil && strings.TrimSpace(*input.APIKey) != "" {
		enc, err := utils.Encrypt(strings.TrimSpace(*input.APIKey))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to encrypt API key"})
			return
		}
		s.EncryptedAPIKey = enc
	}

	if s.Enabled {
		if msg := whatsAppInvoiceEnableError(s, providerChanged); msg != "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": msg})
			return
		}
	}

	if exists {
		err = utils.DB.Save(&s).Error
	} else {
		s.ID = uuid.New()
		// Select("*") so false booleans aren't replaced by column defaults.
		err = utils.DB.Select("*").Create(&s).Error
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save WhatsApp invoice settings"})
		return
	}
	c.JSON(http.StatusOK, buildWhatsAppInvoiceSettingsResponse(c, s))
}

// whatsAppInvoiceEnableError returns why the settings can't be enabled, or ""
// when they're complete enough to send.
func whatsAppInvoiceEnableError(s models.WhatsAppInvoiceSettings, providerChanged bool) string {
	if s.EncryptedAPIKey == "" {
		if providerChanged {
			return "You switched to " + whatsAppProviderLabel(s.Provider) + ": enter its " + whatsAppCredentialName(s.Provider) + " to enable WhatsApp invoices"
		}
		return whatsAppCredentialName(s.Provider) + " is required to enable WhatsApp invoices"
	}
	if s.Provider == whatsAppProviderTwilio && s.AccountSID == "" {
		return "Twilio Account SID is required to enable WhatsApp invoices"
	}
	if s.PhoneNumberID == "" {
		return whatsAppSenderName(s.Provider) + " is required to enable WhatsApp invoices"
	}
	if s.MessageID == "" {
		return whatsAppTemplateName(s.Provider) + " is required to enable WhatsApp invoices"
	}
	if s.Provider == whatsAppProviderTwilio {
		if !strings.HasPrefix(s.AccountSID, "AC") {
			return "Twilio Account SID must start with AC"
		}
		if !strings.HasPrefix(s.MessageID, "HX") {
			return "Twilio Content SID must start with HX (Twilio Console → Messaging → Content Template Builder)"
		}
		if s.AttachPDF && !whatsAppMappingHas(s.VariableMapping, "invoice_pdf_file") {
			return "To attach the PDF with Twilio, map your media template's URL variable to \"Invoice PDF file\""
		}
	}
	if (s.AttachPDF || whatsAppMappingNeedsPDF(s.VariableMapping)) && whatsAppInvoicePublicBaseURL(s) == "" {
		return "Public base URL is required to attach or link the invoice PDF"
	}
	if s.NoLoyaltyMessageID != "" {
		if s.Provider == whatsAppProviderTwilio {
			if !strings.HasPrefix(s.NoLoyaltyMessageID, "HX") {
				return "Twilio Content SID of the template without loyalty must start with HX"
			}
			if s.AttachPDF && !whatsAppMappingHas(s.NoLoyaltyVariableMapping, "invoice_pdf_file") {
				return "To attach the PDF with Twilio, map the media URL variable of the template without loyalty to \"Invoice PDF file\""
			}
		}
		if whatsAppMappingNeedsPDF(s.NoLoyaltyVariableMapping) && whatsAppInvoicePublicBaseURL(s) == "" {
			return "Public base URL is required to link the invoice PDF in the template without loyalty"
		}
	}
	return ""
}

// SetWhatsAppInvoiceEnabled turns automatic WhatsApp invoice messages on or
// off without touching the rest of the settings.
func SetWhatsAppInvoiceEnabled(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var input struct {
		Enabled *bool `json:"enabled" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil || input.Enabled == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "enabled is required"})
		return
	}

	s, exists := loadWhatsAppInvoiceSettings(userID)
	if *input.Enabled {
		if !exists {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Configure WhatsApp invoice settings before enabling"})
			return
		}
		if msg := whatsAppInvoiceEnableError(s, false); msg != "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": msg + ". Open Configure WhatsApp to finish setup."})
			return
		}
	}
	if !exists {
		// Nothing saved and asked to disable: already off.
		c.JSON(http.StatusOK, buildWhatsAppInvoiceSettingsResponse(c, s))
		return
	}

	if err := utils.DB.Model(&models.WhatsAppInvoiceSettings{}).Where("id = ?", s.ID).
		Update("enabled", *input.Enabled).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update WhatsApp invoice setting"})
		return
	}
	s.Enabled = *input.Enabled
	c.JSON(http.StatusOK, buildWhatsAppInvoiceSettingsResponse(c, s))
}

func normalizeWhatsAppVariableMapping(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultWhatsAppInvoiceFields, nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for i, p := range parts {
		key := strings.TrimSpace(p)
		if !isKnownWhatsAppInvoiceField(key) {
			return "", fmt.Errorf("unknown field %q for variable {{%d}}", key, i+1)
		}
		out = append(out, key)
	}
	return strings.Join(out, ","), nil
}

// GetWhatsAppInvoiceLogs lists recent send attempts for the active store.
func GetWhatsAppInvoiceLogs(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := utils.DB.Where("user_id = ?", userID)
	if invID := c.Query("invoice_id"); invID != "" {
		q = q.Where("invoice_id = ?", invID)
	}
	var logs []models.WhatsAppInvoiceLog
	if err := q.Order("created_at DESC").Limit(limit).Find(&logs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load logs"})
		return
	}
	c.JSON(http.StatusOK, logs)
}

// TestWhatsAppInvoice sends the message for the latest (or a given) invoice to
// a test number using the saved settings, ignoring the enabled toggle.
func TestWhatsAppInvoice(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var input struct {
		Phone     string `json:"phone" binding:"required"`
		InvoiceID string `json:"invoice_id"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "phone is required"})
		return
	}

	var invoiceID uuid.UUID
	if input.InvoiceID != "" {
		id, err := uuid.Parse(input.InvoiceID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid invoice_id"})
			return
		}
		invoiceID = id
	} else {
		var latest models.Invoice
		if err := utils.DB.Select("id").Where("user_id = ?", userID).Order("created_at DESC").First(&latest).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Create at least one invoice before sending a test message"})
			return
		}
		invoiceID = latest.ID
	}

	entry, err := sendInvoiceWhatsApp(userID, invoiceID, "test", input.Phone, true)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error(), "log": entry})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Test message sent", "log": entry})
}

// SendInvoiceWhatsApp manually (re)sends an invoice's WhatsApp message to the
// party's phone, or an override phone from the body.
func SendInvoiceWhatsApp(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	invoiceID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid invoice id"})
		return
	}
	var input struct {
		Phone string `json:"phone"`
	}
	_ = c.ShouldBindJSON(&input)

	entry, err := sendInvoiceWhatsApp(userID, invoiceID, "manual", input.Phone, true)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error(), "log": entry})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "WhatsApp message sent", "log": entry})
}

// queueInvoiceWhatsApp fires the automatic send in the background so invoice
// creation never waits on (or fails because of) the WhatsApp gateway.
func queueInvoiceWhatsApp(userID uuid.UUID, invoice *models.Invoice) {
	if invoice == nil {
		return
	}
	s, exists := loadWhatsAppInvoiceSettings(userID)
	if !exists || !s.Enabled {
		return
	}
	if invoice.IsPOS && !s.SendForPOS {
		return
	}
	if !invoice.IsPOS && !s.SendForNonPOS {
		return
	}
	invoiceID := invoice.ID
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[whatsapp-invoice] panic sending invoice %s: %v", invoiceID, r)
			}
		}()
		if _, err := sendInvoiceWhatsApp(userID, invoiceID, "auto", "", false); err != nil {
			log.Printf("[whatsapp-invoice] invoice %s: %v", invoiceID, err)
		}
	}()
}

// sendInvoiceWhatsApp builds and sends the template message and records a log
// row. force skips the enabled check (manual/test sends).
func sendInvoiceWhatsApp(userID, invoiceID uuid.UUID, trigger, overridePhone string, force bool) (*models.WhatsAppInvoiceLog, error) {
	entry := &models.WhatsAppInvoiceLog{
		ID:        uuid.New(),
		UserID:    userID,
		InvoiceID: invoiceID,
		Trigger:   trigger,
		CreatedAt: time.Now(),
	}
	finish := func(status string, err error) (*models.WhatsAppInvoiceLog, error) {
		entry.Status = status
		if err != nil {
			entry.Error = err.Error()
		}
		if dbErr := utils.DB.Create(entry).Error; dbErr != nil {
			log.Printf("[whatsapp-invoice] failed to write log: %v", dbErr)
		}
		return entry, err
	}

	s, exists := loadWhatsAppInvoiceSettings(userID)
	if !exists {
		return finish("failed", errors.New("WhatsApp invoice settings are not configured"))
	}
	if !force && !s.Enabled {
		return finish("skipped", errors.New("WhatsApp invoice messages are disabled"))
	}
	if s.EncryptedAPIKey == "" || s.MessageID == "" || s.PhoneNumberID == "" ||
		(s.Provider == whatsAppProviderTwilio && s.AccountSID == "") {
		if s.Provider == whatsAppProviderTwilio {
			return finish("failed", errors.New("Twilio Account SID, Auth Token, Content SID and WhatsApp sender are required"))
		}
		return finish("failed", fmt.Errorf("%s, template name and phone number ID are required", whatsAppCredentialName(s.Provider)))
	}
	accessToken, err := utils.Decrypt(s.EncryptedAPIKey)
	if err != nil {
		return finish("failed", fmt.Errorf("failed to decrypt %s", whatsAppCredentialName(s.Provider)))
	}

	var invoice models.Invoice
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, invoiceID).
		Preload("Party").Preload("Items").Preload("Items.Product").
		First(&invoice).Error; err != nil {
		return finish("failed", errors.New("invoice not found"))
	}
	entry.InvoiceNumber = invoice.InvoiceNumber
	entry.PartyName = invoice.Party.Name

	rawPhone := overridePhone
	if strings.TrimSpace(rawPhone) == "" {
		rawPhone = invoice.Party.Phone
	}
	phone := normalizeWhatsAppNumber(rawPhone, s.CountryCode)
	entry.Phone = phone
	if phone == "" {
		return finish("skipped", errors.New("customer has no valid mobile number"))
	}

	// Pick the template for the store's current loyalty state; the rest of the
	// send works on this local copy.
	s.MessageID, s.VariableMapping, _ = whatsAppActiveTemplate(s, storeLoyaltyEnabled(userID))

	business := resolveSellerBusiness(userID, nil)
	values := whatsAppInvoiceValues(&invoice, &business, s)

	// Signed, expiring PDF link: Meta's document header, Twilio's media URL
	// variable (invoice_pdf_file) and the invoice_pdf_url text field.
	pdfFile, pdfURL := "", ""
	if s.AttachPDF || whatsAppMappingNeedsPDF(s.VariableMapping) {
		base := whatsAppInvoicePublicBaseURL(s)
		if base == "" {
			return finish("failed", errors.New("public base URL is not configured; cannot attach invoice PDF"))
		}
		pdfFile = signInvoicePDFToken(invoice.ID, time.Now().Add(time.Duration(s.PDFLinkTTLMinutes)*time.Minute)) + ".pdf"
		pdfURL = base + publicInvoicePDFPath + pdfFile
		values["invoice_pdf_file"] = pdfFile
		values["invoice_pdf_url"] = pdfURL
	}

	vars := make([]string, 0, 8)
	for _, key := range strings.Split(s.VariableMapping, ",") {
		vars = append(vars, values[strings.TrimSpace(key)])
	}

	if s.Provider == whatsAppProviderTwilio {
		if s.AttachPDF && !whatsAppMappingHas(s.VariableMapping, "invoice_pdf_file") {
			return finish("failed", errors.New("attach PDF is on but no template variable is mapped to \"Invoice PDF file\""))
		}
		sid, body, err := callTwilioWhatsAppAPI(s.AccountSID, accessToken, s.PhoneNumberID, phone, s.MessageID, vars, s.CountryCode)
		entry.RequestID = sid
		entry.Response = truncateWALog(body, 2000)
		if err != nil {
			return finish("failed", err)
		}
		return finish("sent", nil)
	}

	template := map[string]interface{}{
		"name":     s.MessageID,
		"language": map[string]interface{}{"code": s.LanguageCode},
	}
	var components []map[string]interface{}
	if s.AttachPDF {
		components = append(components, map[string]interface{}{
			"type": "header",
			"parameters": []map[string]interface{}{
				{"type": "document", "document": map[string]interface{}{"link": pdfURL, "filename": invoicePDFFilename(invoice.InvoiceNumber)}},
			},
		})
	}
	if len(vars) > 0 {
		params := make([]map[string]interface{}, 0, len(vars))
		for _, v := range vars {
			params = append(params, map[string]interface{}{"type": "text", "text": v})
		}
		components = append(components, map[string]interface{}{"type": "body", "parameters": params})
	}
	if len(components) > 0 {
		template["components"] = components
	}
	payload := map[string]interface{}{
		"messaging_product": "whatsapp",
		"recipient_type":    "individual",
		"to":                phone,
		"type":              "template",
		"template":          template,
	}

	requestID, body, err := callWhatsAppAPI(s.Provider, s.PhoneNumberID, accessToken, payload)
	entry.RequestID = requestID
	entry.Response = truncateWALog(body, 2000)
	if err != nil {
		return finish("failed", err)
	}
	return finish("sent", nil)
}

func whatsAppInvoiceValues(inv *models.Invoice, business *models.Business, s models.WhatsAppInvoiceSettings) map[string]string {
	storeName := s.StoreDisplayName
	if storeName == "" {
		storeName = business.Name
	}
	customer := strings.TrimSpace(inv.Party.Name)
	if customer == "" {
		customer = "Customer"
	}
	balanceDue := inv.TotalAmount - inv.AmountPaid
	if balanceDue < 0 {
		balanceDue = 0
	}
	values := map[string]string{
		"invoice_number":   inv.InvoiceNumber,
		"total_amount":     formatINRAmount(inv.TotalAmount),
		"invoice_date":     inv.Date.Format("02-01-2006"),
		"points_earned":    strconv.FormatInt(inv.LoyaltyPointsEarned, 10),
		"points_balance":   strconv.FormatInt(inv.Party.LoyaltyPoints, 10),
		"points_redeemed":  strconv.FormatInt(inv.LoyaltyPointsRedeemed, 10),
		"store_name":       storeName,
		"customer_name":    customer,
		"amount_paid":      formatINRAmount(inv.AmountPaid),
		"balance_due":      formatINRAmount(balanceDue),
		"loyalty_discount": formatINRAmount(inv.LoyaltyDiscount),
		"store_phone":      business.Phone,
		// Filled by sendInvoiceWhatsApp once the signed link is built.
		"invoice_pdf_url":  "",
		"invoice_pdf_file": "",
	}
	for k, v := range values {
		// WhatsApp rejects empty template parameters and body text containing
		// newlines or tabs.
		v = strings.NewReplacer("\n", " ", "\r", " ", "\t", " ").Replace(strings.TrimSpace(v))
		if v == "" {
			v = "-"
		}
		values[k] = v
	}
	return values
}

// formatINRAmount renders 123456.5 as "1,23,456.50" (Indian grouping).
func formatINRAmount(v float64) string {
	neg := v < 0
	if neg {
		v = -v
	}
	s := strconv.FormatFloat(v, 'f', 2, 64)
	intPart, frac := s[:len(s)-3], s[len(s)-3:]
	if len(intPart) > 3 {
		head, tail := intPart[:len(intPart)-3], intPart[len(intPart)-3:]
		var groups []string
		for len(head) > 2 {
			groups = append([]string{head[len(head)-2:]}, groups...)
			head = head[:len(head)-2]
		}
		if head != "" {
			groups = append([]string{head}, groups...)
		}
		intPart = strings.Join(groups, ",") + "," + tail
	}
	if neg {
		return "-" + intPart + frac
	}
	return intPart + frac
}

func onlyDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// normalizeWhatsAppNumber returns the number with country code (digits only),
// or "" when it can't be a valid mobile number.
func normalizeWhatsAppNumber(raw, countryCode string) string {
	d := onlyDigits(raw)
	d = strings.TrimLeft(d, "0") // strips trunk "0" and "00" international prefix
	cc := onlyDigits(countryCode)
	if cc == "" {
		cc = "91"
	}
	switch {
	case len(d) == 10:
		return cc + d
	case len(d) >= 11 && len(d) <= 15:
		return d
	default:
		return ""
	}
}

func invoicePDFFilename(invoiceNumber string) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, invoiceNumber)
	return "Invoice_" + name + ".pdf"
}

func truncateWALog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

var whatsAppCloudHTTPClient = &http.Client{Timeout: 30 * time.Second}

// callWhatsAppAPI sends a template message through the store's provider.
// Meta and Fast2SMS share the payload and success response
// ({"messages":[{"id":"wamid..."}]}); they differ in base URL, auth header
// (Meta: "Bearer <token>", Fast2SMS: the bare API key) and error body
// (Meta: {"error":{"message":..}}, Fast2SMS: {"return":false,"message":..}).
// Returns the sent message's wamid and the raw body. The secret travels only
// in the Authorization header; it is never surfaced in errors.
func callWhatsAppAPI(provider, phoneNumberID, secret string, payload map[string]interface{}) (string, string, error) {
	provider = normalizeWhatsAppProvider(provider)
	label := whatsAppProviderLabel(provider)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	endpoint := strings.TrimRight(whatsAppAPIBase(provider), "/") + "/" + url.PathEscape(phoneNumberID) + "/messages"
	body, err := json.Marshal(payload)
	if err != nil {
		return "", "", errors.New("failed to encode WhatsApp message payload")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", "", fmt.Errorf("invalid %s endpoint", label)
	}
	if provider == whatsAppProviderFast2SMS {
		req.Header.Set("Authorization", secret)
	} else {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := whatsAppCloudHTTPClient.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return "", "", fmt.Errorf("%s request failed: %v", label, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	bodyText := string(bytes.TrimSpace(raw))

	var parsed struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
		Error *struct {
			Message string `json:"message"`
			Code    int    `json:"code"`
		} `json:"error"`
		// Fast2SMS-native fields.
		Return  *bool           `json:"return"`
		Message json.RawMessage `json:"message"`
	}
	_ = json.Unmarshal(raw, &parsed)

	messageID := ""
	if len(parsed.Messages) > 0 {
		messageID = parsed.Messages[0].ID
	}
	failed := resp.StatusCode >= 300 || parsed.Error != nil || (parsed.Return != nil && !*parsed.Return)
	if failed {
		msg := ""
		if parsed.Error != nil {
			msg = parsed.Error.Message
		}
		if msg == "" {
			msg = fast2SMSMessageText(parsed.Message)
		}
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return messageID, bodyText, fmt.Errorf("%s rejected the message: %s", label, msg)
	}
	return messageID, bodyText, nil
}

// fast2SMSMessageText extracts Fast2SMS's "message" field, which is either a
// string or an array of strings.
func fast2SMSMessageText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return strings.TrimSpace(str)
	}
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return strings.TrimSpace(strings.Join(list, "; "))
	}
	return ""
}

// twilioWhatsAppAddress turns a number into Twilio's "whatsapp:+<digits>"
// address form; "" when it isn't a valid number.
func twilioWhatsAppAddress(raw, countryCode string) string {
	raw = strings.TrimPrefix(strings.TrimSpace(raw), "whatsapp:")
	n := normalizeWhatsAppNumber(raw, countryCode)
	if n == "" {
		return ""
	}
	return "whatsapp:+" + n
}

// callTwilioWhatsAppAPI sends an approved Content template through Twilio's
// Messages API. sender is a WhatsApp-enabled Twilio number or a Messaging
// Service SID (MG...); vars fill the template's {{1}}, {{2}}… in order. A
// media template's document comes from its media URL, whose variable is one of
// vars. Auth is HTTP Basic (Account SID : Auth Token); the token is never
// surfaced in errors. Returns the message SID (SM...) and the raw body.
//
// Twilio accepts the message (HTTP 201, status "queued") before WhatsApp
// delivers it, so a later delivery failure (e.g. 63016, template mismatch)
// shows only in the Twilio console, not here.
func callTwilioWhatsAppAPI(accountSID, authToken, sender, to, contentSID string, vars []string, countryCode string) (string, string, error) {
	const label = "Twilio"
	toAddr := twilioWhatsAppAddress(to, countryCode)
	if toAddr == "" {
		return "", "", errors.New("customer has no valid mobile number")
	}

	form := url.Values{}
	form.Set("To", toAddr)
	form.Set("ContentSid", contentSID)
	if strings.HasPrefix(sender, "MG") {
		form.Set("MessagingServiceSid", sender)
	} else {
		from := twilioWhatsAppAddress(sender, countryCode)
		if from == "" {
			return "", "", errors.New("Twilio WhatsApp sender must be a phone number with country code or a Messaging Service SID (MG...)")
		}
		form.Set("From", from)
	}
	if len(vars) > 0 {
		cv := make(map[string]string, len(vars))
		for i, v := range vars {
			cv[strconv.Itoa(i+1)] = v
		}
		raw, err := json.Marshal(cv)
		if err != nil {
			return "", "", errors.New("failed to encode Twilio content variables")
		}
		form.Set("ContentVariables", string(raw))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	endpoint := strings.TrimRight(whatsAppAPIBase(whatsAppProviderTwilio), "/") +
		"/Accounts/" + url.PathEscape(accountSID) + "/Messages.json"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", fmt.Errorf("invalid %s endpoint", label)
	}
	req.SetBasicAuth(accountSID, authToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := whatsAppCloudHTTPClient.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return "", "", fmt.Errorf("%s request failed: %v", label, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	bodyText := string(bytes.TrimSpace(raw))

	var parsed struct {
		SID       string `json:"sid"`
		Status    string `json:"status"`
		Code      int    `json:"code"`
		Message   string `json:"message"`
		ErrorCode *int   `json:"error_code"`
		ErrorMsg  string `json:"error_message"`
	}
	_ = json.Unmarshal(raw, &parsed)

	if resp.StatusCode >= 300 || parsed.Status == "failed" || parsed.Status == "undelivered" {
		msg := strings.TrimSpace(parsed.Message)
		if msg == "" {
			msg = strings.TrimSpace(parsed.ErrorMsg)
		}
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		if parsed.Code != 0 {
			msg = fmt.Sprintf("%s (code %d)", msg, parsed.Code)
		} else if parsed.ErrorCode != nil {
			msg = fmt.Sprintf("%s (code %d)", msg, *parsed.ErrorCode)
		}
		return parsed.SID, bodyText, fmt.Errorf("%s rejected the message: %s", label, msg)
	}
	return parsed.SID, bodyText, nil
}

// ---- Public, signed invoice PDF link (fetched by WhatsApp's media fetcher) ----

func signInvoicePDFToken(invoiceID uuid.UUID, expires time.Time) string {
	payload := make([]byte, 24)
	copy(payload[:16], invoiceID[:])
	binary.BigEndian.PutUint64(payload[16:], uint64(expires.Unix()))
	sig := utils.SignHMAC(whatsAppInvoicePDFPurpose, payload)[:16]
	return base64.RawURLEncoding.EncodeToString(append(payload, sig...))
}

func verifyInvoicePDFToken(token string) (uuid.UUID, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 40 {
		return uuid.Nil, false
	}
	payload, sig := raw[:24], raw[24:]
	if !hmac.Equal(sig, utils.SignHMAC(whatsAppInvoicePDFPurpose, payload)[:16]) {
		return uuid.Nil, false
	}
	if time.Now().Unix() > int64(binary.BigEndian.Uint64(payload[16:])) {
		return uuid.Nil, false
	}
	id, err := uuid.FromBytes(payload[:16])
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

// PublicInvoicePDF serves an invoice PDF for a signed, expiring token. No auth:
// WhatsApp's media fetcher downloads the attachment from this URL.
func PublicInvoicePDF(c *gin.Context) {
	token := strings.TrimSuffix(c.Param("token"), ".pdf")
	invoiceID, ok := verifyInvoicePDFToken(token)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "Link is invalid or has expired"})
		return
	}

	var invoice models.Invoice
	if err := utils.DB.Where("id = ?", invoiceID).
		Preload("Party").Preload("Items").Preload("Items.Product").
		First(&invoice).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Invoice not found"})
		return
	}
	attachInvoicePaymentSplits(utils.DB, &invoice)

	business := resolveSellerBusiness(invoice.UserID, nil)
	pdfBytes, err := buildInvoiceDocumentPDF(invoice, &business, loadPrintSettings(invoice.UserID))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate invoice PDF"})
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf("inline; filename=%q", invoicePDFFilename(invoice.InvoiceNumber)))
	c.Header("Cache-Control", "private, max-age=3600")
	c.Data(http.StatusOK, "application/pdf", pdfBytes)
}
