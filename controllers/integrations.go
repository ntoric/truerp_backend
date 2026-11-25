package controllers

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Developer Settings → Integrations groups every third-party connection a
// store uses: email, WhatsApp, SMS and AI. WhatsApp has a single
// connection per store (WhatsAppInvoiceSettings credentials) shared by every
// WhatsApp feature; the old DeveloperSettings WhatsApp fields are retired
// and were merged in by utils.backfillWhatsAppGatewayFromDeveloperSettings. All routes are super-admin only and scoped to the active
// store: AuthRequired sets user_id to the owner of the X-Store-ID store, and
// each store has exactly one owner, so rows keyed by user_id are per store.

type integrationStatus struct {
	Configured bool   `json:"configured"`
	Enabled    bool   `json:"enabled"`
	Provider   string `json:"provider,omitempty"`
}

type integrationsOverview struct {
	StoreID   string            `json:"store_id,omitempty"`
	StoreName string            `json:"store_name,omitempty"`
	Email     integrationStatus `json:"email"`
	WhatsApp  integrationStatus `json:"whatsapp"`
	SMS       integrationStatus `json:"sms"`
	AI        integrationStatus `json:"ai"`
}

func emailConfigured(s models.DeveloperSettings) bool {
	switch s.EmailProvider {
	case "sendgrid":
		return s.EncryptedSendGridAPIKey != ""
	case "ses":
		return s.EncryptedSESAccessKey != "" && s.EncryptedSESSecretKey != ""
	case "mailgun":
		return s.EncryptedMailgunAPIKey != "" && s.MailgunDomain != ""
	}
	return s.SMTPHost != "" && s.SMTPUsername != "" && s.EncryptedSMTPPassword != ""
}

func smsConfigured(s models.DeveloperSettings) bool {
	switch s.SMSProvider {
	case "msg91":
		return s.EncryptedMsg91AuthKey != "" && s.Msg91SenderID != ""
	case "textlocal":
		return s.EncryptedTextLocalAPIKey != ""
	case "aws_sns":
		return s.AWSAccessKey != "" && s.EncryptedAWSSecretKey != "" && s.AWSRegion != ""
	case "sendgrid":
		return s.EncryptedSendGridSMSAPIKey != ""
	}
	return s.TwilioSMSAccountSID != "" && s.EncryptedTwilioSMSAuthToken != "" && s.TwilioSMSPhoneNumber != ""
}

// whatsAppGatewayError returns why the store's WhatsApp invoice gateway
// credentials are incomplete, or "" when they are enough to send. Template
// settings are checked separately by whatsAppInvoiceEnableError.
func whatsAppGatewayError(s models.WhatsAppInvoiceSettings) string {
	if s.EncryptedAPIKey == "" {
		return whatsAppCredentialName(s.Provider) + " is required"
	}
	if s.Provider == whatsAppProviderTwilio {
		if s.AccountSID == "" {
			return "Twilio Account SID is required"
		}
		if !strings.HasPrefix(s.AccountSID, "AC") {
			return "Twilio Account SID must start with AC"
		}
	}
	if s.PhoneNumberID == "" {
		return whatsAppSenderName(s.Provider) + " is required"
	}
	return ""
}

// GetIntegrationsOverview reports which integrations the active store has set up.
func GetIntegrationsOverview(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var out integrationsOverview

	if store, ok := requestStore(c); ok {
		out.StoreID = store.ID.String()
		out.StoreName = store.Name
	}

	var dev models.DeveloperSettings
	if err := utils.DB.Where("user_id = ?", userID).First(&dev).Error; err == nil {
		out.Email = integrationStatus{Provider: dev.EmailProvider, Configured: emailConfigured(dev)}
		out.SMS = integrationStatus{Provider: dev.SMSProvider, Configured: smsConfigured(dev)}
	} else {
		out.Email.Provider, out.SMS.Provider = "smtp", "twilio"
	}
	out.Email.Enabled = out.Email.Configured
	out.SMS.Enabled = out.SMS.Configured

	wa, _ := loadWhatsAppInvoiceSettings(userID)
	out.WhatsApp = integrationStatus{
		Provider:   wa.Provider,
		Configured: whatsAppGatewayError(wa) == "",
		Enabled:    wa.Enabled,
	}

	if business, err := ensureBusinessForUser(userID); err == nil {
		provider := normalizeAIProvider(business.AIProvider)
		cfg, _ := resolveAIConfig(business, provider)
		out.AI = integrationStatus{
			Provider:   provider,
			Configured: cfg.ready() == "",
			Enabled:    business.EnableAIHSNSearch || business.EnableAIBillParsing,
		}
	}

	c.JSON(http.StatusOK, out)
}

// ---------------------------------------------------------------------------
// AI providers (Gemini, OpenAI, Anthropic, OpenRouter, Ollama)
//
// Each store has one active provider (Business.AIProvider) and an
// AIProviderConfig row per provider it has set up (key, model, base URL).
// ---------------------------------------------------------------------------

type aiProviderConfigView struct {
	HasAPIKey  bool   `json:"has_api_key"`
	APIKeyHint string `json:"api_key_hint"`
	Model      string `json:"model"`
	BaseURL    string `json:"base_url"`
	Configured bool   `json:"configured"`
}

type aiIntegrationResponse struct {
	EnableAIHSNSearch   bool                            `json:"enable_ai_hsn_search"`
	EnableAIBillParsing bool                            `json:"enable_ai_bill_parsing"`
	Provider            string                          `json:"provider"`
	Providers           []aiProviderInfo                `json:"providers"`
	Configs             map[string]aiProviderConfigView `json:"configs"`
	Configured          bool                            `json:"configured"`
}

func secretHint(secret string) string {
	if len(secret) < 4 {
		if secret == "" {
			return ""
		}
		return "••••"
	}
	return "••••" + secret[len(secret)-4:]
}

func buildAIIntegrationResponse(b models.Business) aiIntegrationResponse {
	active := normalizeAIProvider(b.AIProvider)
	out := aiIntegrationResponse{
		EnableAIHSNSearch:   b.EnableAIHSNSearch,
		EnableAIBillParsing: b.EnableAIBillParsing,
		Provider:            active,
		Providers:           aiProviders,
		Configs:             map[string]aiProviderConfigView{},
	}
	for _, p := range aiProviders {
		cfg, _ := resolveAIConfig(b, p.Key)
		view := aiProviderConfigView{
			HasAPIKey:  cfg.APIKey != "",
			APIKeyHint: secretHint(cfg.APIKey),
			Model:      cfg.Model,
			BaseURL:    cfg.BaseURL,
			Configured: cfg.ready() == "",
		}
		out.Configs[p.Key] = view
		if p.Key == active {
			out.Configured = view.Configured
		}
	}
	return out
}

// aiIntegrationInput edits the store's AI toggles, picks the active provider
// and updates that provider's connection. A blank api_key keeps the saved one.
type aiIntegrationInput struct {
	EnableAIHSNSearch   bool   `json:"enable_ai_hsn_search"`
	EnableAIBillParsing bool   `json:"enable_ai_bill_parsing"`
	Provider            string `json:"provider"`
	Model               string `json:"model"`
	BaseURL             string `json:"base_url"`
	APIKey              string `json:"api_key"`
	ClearAPIKey         bool   `json:"clear_api_key"`
	// Legacy field from the Gemini-only form; treated as api_key for gemini.
	GeminiAPIKey      string `json:"gemini_api_key"`
	ClearGeminiAPIKey bool   `json:"clear_gemini_api_key"`
}

func (in *aiIntegrationInput) normalize() (string, error) {
	provider := strings.ToLower(strings.TrimSpace(in.Provider))
	if provider == "" {
		provider = aiProviderGemini
	}
	if _, ok := findAIProvider(provider); !ok {
		return "", fmt.Errorf("unsupported AI provider %q", in.Provider)
	}
	if provider == aiProviderGemini {
		if in.APIKey == "" {
			in.APIKey = in.GeminiAPIKey
		}
		in.ClearAPIKey = in.ClearAPIKey || in.ClearGeminiAPIKey
	}
	in.APIKey = strings.TrimSpace(in.APIKey)
	in.Model = strings.TrimSpace(in.Model)
	base, err := validateAIBaseURL(in.BaseURL)
	if err != nil {
		return "", err
	}
	in.BaseURL = base
	return provider, nil
}

// GetAIIntegration returns the active store's AI settings (keys masked).
func GetAIIntegration(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	business, err := ensureBusinessForUser(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load business"})
		return
	}
	c.JSON(http.StatusOK, buildAIIntegrationResponse(business))
}

// UpdateAIIntegration saves the active store's AI settings.
func UpdateAIIntegration(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var input aiIntegrationInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	provider, err := input.normalize()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	business, err := ensureBusinessForUser(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load business"})
		return
	}

	row, exists := loadAIProviderConfig(userID, provider)
	row.Model = input.Model
	row.BaseURL = input.BaseURL
	clearLegacyGemini := false
	if input.APIKey != "" {
		enc, err := utils.Encrypt(input.APIKey)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to encrypt API key"})
			return
		}
		row.EncryptedAPIKey = enc
		clearLegacyGemini = provider == aiProviderGemini
	} else if input.ClearAPIKey {
		row.EncryptedAPIKey = ""
		clearLegacyGemini = provider == aiProviderGemini
	} else if provider == aiProviderGemini && row.EncryptedAPIKey == "" && business.GeminiAPIKey != "" {
		// Move the legacy plaintext key into encrypted storage.
		enc, err := utils.Encrypt(business.GeminiAPIKey)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to encrypt API key"})
			return
		}
		row.EncryptedAPIKey = enc
		clearLegacyGemini = true
	}

	// Validate before writing: features can only be on with a usable provider.
	check := business
	check.AIProvider = provider
	if clearLegacyGemini {
		check.GeminiAPIKey = ""
	}
	cfg := aiConfig{Provider: provider, Model: row.Model, BaseURL: row.BaseURL}
	if row.EncryptedAPIKey != "" {
		cfg.APIKey = "set"
	} else if provider == aiProviderGemini {
		cfg.APIKey = check.GeminiAPIKey
	}
	if (input.EnableAIHSNSearch || input.EnableAIBillParsing) && cfg.ready() != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": cfg.ready() + ". Add it to turn on AI features."})
		return
	}

	err = utils.DB.Transaction(func(tx *gorm.DB) error {
		if exists {
			if err := tx.Save(&row).Error; err != nil {
				return err
			}
		} else {
			row.ID = uuid.New()
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		updates := map[string]interface{}{
			"enable_aihsn_search":    input.EnableAIHSNSearch,
			"enable_ai_bill_parsing": input.EnableAIBillParsing,
			"ai_provider":            provider,
		}
		if clearLegacyGemini {
			updates["gemini_api_key"] = ""
		}
		return tx.Model(&business).Updates(updates).Error
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save AI settings"})
		return
	}

	business.EnableAIHSNSearch = input.EnableAIHSNSearch
	business.EnableAIBillParsing = input.EnableAIBillParsing
	business.AIProvider = provider
	if clearLegacyGemini {
		business.GeminiAPIKey = ""
	}
	c.JSON(http.StatusOK, buildAIIntegrationResponse(business))
}

// TestAIIntegration sends a tiny prompt to a provider. Unsaved form values
// are used when given; a blank key falls back to the saved one.
func TestAIIntegration(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var input aiIntegrationInput
	_ = c.ShouldBindJSON(&input)
	provider, err := input.normalize()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	business, err := ensureBusinessForUser(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load business"})
		return
	}
	cfg, err := resolveAIConfig(business, provider)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	cfg.Model, cfg.BaseURL = input.Model, input.BaseURL
	if input.APIKey != "" {
		cfg.APIKey = input.APIKey
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Second)
	defer cancel()
	reply, err := aiGenerate(ctx, cfg, `Reply with the single word OK.`, nil)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message": fmt.Sprintf("%s responded using %s", cfg.label(), cfg.model()),
		"reply":   truncateWALog(reply, 200),
	})
}

// ---------------------------------------------------------------------------
// WhatsApp connection (Meta Cloud API / Fast2SMS / Twilio)
//
// The store's one WhatsApp connection. Credentials live on
// WhatsAppInvoiceSettings and are shared by every WhatsApp feature (invoice
// and party statement messages today). Templates, mapping and send rules
// stay with each feature (Settings → WhatsApp Invoice, Party Statement).
// ---------------------------------------------------------------------------

type whatsAppGatewayResponse struct {
	Provider        string                 `json:"provider"`
	Providers       []whatsAppProviderInfo `json:"providers"`
	HasAPIKey       bool                   `json:"has_api_key"`
	APIKeyHint      string                 `json:"api_key_hint"`
	PhoneNumberID   string                 `json:"phone_number_id"`
	AccountSID      string                 `json:"account_sid"`
	Configured      bool                   `json:"configured"`
	MissingReason   string                 `json:"missing_reason,omitempty"`
	InvoicesEnabled bool                   `json:"invoices_enabled"`
	// InvoicesAutoDisabled is set when a provider switch turned WhatsApp
	// invoices off (templates are provider-specific and must be redone).
	InvoicesAutoDisabled bool `json:"invoices_auto_disabled,omitempty"`
}

func buildWhatsAppGatewayResponse(s models.WhatsAppInvoiceSettings) whatsAppGatewayResponse {
	hint := ""
	if s.EncryptedAPIKey != "" {
		if key, err := utils.Decrypt(s.EncryptedAPIKey); err == nil {
			hint = secretHint(key)
		}
	}
	missing := whatsAppGatewayError(s)
	return whatsAppGatewayResponse{
		Provider:        s.Provider,
		Providers:       whatsAppProviders,
		HasAPIKey:       s.EncryptedAPIKey != "",
		APIKeyHint:      hint,
		PhoneNumberID:   s.PhoneNumberID,
		AccountSID:      s.AccountSID,
		Configured:      missing == "",
		MissingReason:   missing,
		InvoicesEnabled: s.Enabled,
	}
}

// GetWhatsAppGatewayIntegration returns the active store's WhatsApp gateway.
func GetWhatsAppGatewayIntegration(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	s, _ := loadWhatsAppInvoiceSettings(userID)
	c.JSON(http.StatusOK, buildWhatsAppGatewayResponse(s))
}

// UpdateWhatsAppGatewayIntegration saves the active store's WhatsApp gateway
// credentials. A blank api_key keeps the saved secret, except when the
// provider changes: a Meta token is useless for Fast2SMS and vice versa, so
// the old secret is dropped.
func UpdateWhatsAppGatewayIntegration(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var input struct {
		Provider      string `json:"provider"`
		APIKey        string `json:"api_key"`
		ClearAPIKey   bool   `json:"clear_api_key"`
		PhoneNumberID string `json:"phone_number_id"`
		AccountSID    string `json:"account_sid"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	provider := strings.ToLower(strings.TrimSpace(input.Provider))
	if provider == "" {
		provider = whatsAppProviderCloud
	}
	if !isKnownWhatsAppProvider(provider) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Unsupported WhatsApp provider " + input.Provider})
		return
	}

	s, exists := loadWhatsAppInvoiceSettings(userID)
	providerChanged := s.Provider != provider
	s.Provider = provider
	s.PhoneNumberID = strings.TrimSpace(input.PhoneNumberID)
	s.AccountSID = strings.TrimSpace(input.AccountSID)

	newKey := strings.TrimSpace(input.APIKey)
	if newKey != "" {
		enc, err := utils.Encrypt(newKey)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to encrypt API key"})
			return
		}
		s.EncryptedAPIKey = enc
	} else if input.ClearAPIKey || providerChanged {
		s.EncryptedAPIKey = ""
	}

	autoDisabled := false
	if s.Enabled {
		if providerChanged {
			// Template names / Content SIDs belong to the old provider.
			s.Enabled = false
			autoDisabled = true
		} else if msg := whatsAppGatewayError(s); msg != "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "WhatsApp invoices are on for this store: " + msg + ". Turn them off first to remove the connection."})
			return
		}
	}

	var err error
	if exists {
		err = utils.DB.Save(&s).Error
	} else {
		s.ID = uuid.New()
		// Select("*") so false booleans aren't replaced by column defaults.
		err = utils.DB.Select("*").Create(&s).Error
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save WhatsApp gateway"})
		return
	}

	resp := buildWhatsAppGatewayResponse(s)
	resp.InvoicesAutoDisabled = autoDisabled
	c.JSON(http.StatusOK, resp)
}

// ListAIModels returns the models a provider offers. Unsaved form values
// (key, base URL) are used when given; a blank key falls back to the saved one.
func ListAIModels(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var input aiIntegrationInput
	_ = c.ShouldBindJSON(&input)
	provider, err := input.normalize()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	business, err := ensureBusinessForUser(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load business"})
		return
	}
	cfg, err := resolveAIConfig(business, provider)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	cfg.BaseURL = input.BaseURL
	if input.APIKey != "" {
		cfg.APIKey = input.APIKey
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	models, err := aiListModels(ctx, cfg)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"models": models, "default_model": cfg.info().DefaultModel})
}
