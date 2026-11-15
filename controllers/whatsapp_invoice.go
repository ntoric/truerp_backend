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
// customer through Meta's WhatsApp Cloud API when a sales invoice is
// created. Configured and toggled per store (store-owner user_id scope).

const (
	defaultWhatsAppCloudAPIBase  = "https://graph.facebook.com/v21.0"
	defaultWhatsAppInvoiceFields = "invoice_number,total_amount,invoice_date,points_earned,points_balance,points_redeemed,store_name"
	defaultPDFLinkTTLMinutes     = 60
	minPDFLinkTTLMinutes         = 5
	maxPDFLinkTTLMinutes         = 7 * 24 * 60
	whatsAppInvoicePDFPurpose    = "wa-invoice-pdf"
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
}

func isKnownWhatsAppInvoiceField(key string) bool {
	for _, f := range whatsAppInvoiceFields {
		if f.Key == key {
			return true
		}
	}
	return false
}

func whatsAppCloudAPIBase() string {
	if u := strings.TrimSpace(os.Getenv("WHATSAPP_CLOUD_API_BASE")); u != "" {
		return u
	}
	return defaultWhatsAppCloudAPIBase
}

func loadWhatsAppInvoiceSettings(userID uuid.UUID) (models.WhatsAppInvoiceSettings, bool) {
	var s models.WhatsAppInvoiceSettings
	if err := utils.DB.Where("user_id = ?", userID).First(&s).Error; err != nil {
		return models.WhatsAppInvoiceSettings{
			UserID:            userID,
			Provider:          "whatsapp_cloud",
			VariableMapping:   defaultWhatsAppInvoiceFields,
			AttachPDF:         true,
			CountryCode:       "91",
			LanguageCode:      "en",
			SendForPOS:        true,
			SendForNonPOS:     true,
			PDFLinkTTLMinutes: defaultPDFLinkTTLMinutes,
		}, false
	}
	if s.PDFLinkTTLMinutes <= 0 {
		s.PDFLinkTTLMinutes = defaultPDFLinkTTLMinutes
	}
	if strings.TrimSpace(s.VariableMapping) == "" {
		s.VariableMapping = defaultWhatsAppInvoiceFields
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

type whatsAppInvoiceSettingsResponse struct {
	models.WhatsAppInvoiceSettings
	HasAPIKey       bool                   `json:"has_api_key"`
	APIKeyHint      string                 `json:"api_key_hint"`
	AvailableFields []whatsAppInvoiceField `json:"available_fields"`
	DefaultMapping  string                 `json:"default_mapping"`
	EnvPublicURL    string                 `json:"env_public_base_url"`
	CanEditLinkTTL  bool                   `json:"can_edit_link_ttl"`
	MinLinkTTL      int                    `json:"min_link_ttl_minutes"`
	MaxLinkTTL      int                    `json:"max_link_ttl_minutes"`
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
	return whatsAppInvoiceSettingsResponse{
		WhatsAppInvoiceSettings: s,
		HasAPIKey:               s.EncryptedAPIKey != "",
		APIKeyHint:              hint,
		AvailableFields:         whatsAppInvoiceFields,
		DefaultMapping:          defaultWhatsAppInvoiceFields,
		EnvPublicURL:            strings.TrimSpace(os.Getenv("PUBLIC_API_BASE_URL")),
		CanEditLinkTTL:          isSuperAdminRequest(c),
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
		APIKey           *string `json:"api_key"` // nil/"" = keep existing
		ClearAPIKey      bool    `json:"clear_api_key"`
		MessageID        string  `json:"message_id"`
		PhoneNumberID    string  `json:"phone_number_id"`
		LanguageCode     string  `json:"language_code"`
		VariableMapping  string  `json:"variable_mapping"`
		StoreDisplayName string  `json:"store_display_name"`
		AttachPDF        bool    `json:"attach_pdf"`
		PublicBaseURL    string  `json:"public_base_url"`
		CountryCode      string  `json:"country_code"`
		SendForPOS       bool    `json:"send_for_pos"`
		SendForNonPOS    bool    `json:"send_for_non_pos"`
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

	s, exists := loadWhatsAppInvoiceSettings(userID)
	s.Enabled = input.Enabled
	s.Provider = "whatsapp_cloud"
	s.MessageID = strings.TrimSpace(input.MessageID)
	s.PhoneNumberID = strings.TrimSpace(input.PhoneNumberID)
	s.LanguageCode = languageCode
	s.VariableMapping = mapping
	s.StoreDisplayName = strings.TrimSpace(input.StoreDisplayName)
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

	if input.ClearAPIKey {
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
		if s.EncryptedAPIKey == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "WhatsApp Cloud API access token is required to enable WhatsApp invoices"})
			return
		}
		if s.PhoneNumberID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Phone Number ID is required to enable WhatsApp invoices"})
			return
		}
		if s.MessageID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Template name is required to enable WhatsApp invoices"})
			return
		}
		if s.AttachPDF && whatsAppInvoicePublicBaseURL(s) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Public base URL is required to attach the invoice PDF"})
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
	if s.EncryptedAPIKey == "" || s.MessageID == "" || s.PhoneNumberID == "" {
		return finish("failed", errors.New("WhatsApp Cloud API access token, template name and phone number ID are required"))
	}
	accessToken, err := utils.Decrypt(s.EncryptedAPIKey)
	if err != nil {
		return finish("failed", errors.New("failed to decrypt WhatsApp Cloud API access token"))
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

	business := resolveSellerBusiness(userID, nil)
	values := whatsAppInvoiceValues(&invoice, &business, s)
	vars := make([]string, 0, 8)
	for _, key := range strings.Split(s.VariableMapping, ",") {
		vars = append(vars, values[strings.TrimSpace(key)])
	}

	template := map[string]interface{}{
		"name":     s.MessageID,
		"language": map[string]interface{}{"code": s.LanguageCode},
	}
	var components []map[string]interface{}
	if s.AttachPDF {
		base := whatsAppInvoicePublicBaseURL(s)
		if base == "" {
			return finish("failed", errors.New("public base URL is not configured; cannot attach invoice PDF"))
		}
		pdfURL := base + "/api/v1/public/invoice-pdf/" + signInvoicePDFToken(invoice.ID, time.Now().Add(time.Duration(s.PDFLinkTTLMinutes)*time.Minute)) + ".pdf"
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

	requestID, body, err := callWhatsAppCloud(s.PhoneNumberID, accessToken, payload)
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

// callWhatsAppCloud sends a template message via the WhatsApp Cloud API
// (Meta Graph API). Returns the sent message's wamid and the raw body.
// The access token travels only in the Authorization header; it is never
// surfaced in errors.
func callWhatsAppCloud(phoneNumberID, accessToken string, payload map[string]interface{}) (string, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	endpoint := strings.TrimRight(whatsAppCloudAPIBase(), "/") + "/" + url.PathEscape(phoneNumberID) + "/messages"
	body, err := json.Marshal(payload)
	if err != nil {
		return "", "", errors.New("failed to encode WhatsApp message payload")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", "", errors.New("invalid WhatsApp Cloud API endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := whatsAppCloudHTTPClient.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return "", "", fmt.Errorf("WhatsApp Cloud API request failed: %v", err)
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
	}
	_ = json.Unmarshal(raw, &parsed)

	messageID := ""
	if len(parsed.Messages) > 0 {
		messageID = parsed.Messages[0].ID
	}
	if resp.StatusCode >= 300 || parsed.Error != nil {
		msg := ""
		if parsed.Error != nil {
			msg = parsed.Error.Message
		}
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return messageID, bodyText, fmt.Errorf("WhatsApp Cloud API rejected the message: %s", msg)
	}
	return messageID, bodyText, nil
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
