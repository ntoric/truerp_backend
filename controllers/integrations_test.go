package controllers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func setupIntegrationsTestDB(t *testing.T) {
	t.Helper()
	db := openPartyLedgerTestDB(t)
	if err := db.AutoMigrate(&models.WhatsAppInvoiceSettings{}, &models.Business{}, &models.Store{},
		&models.DeveloperSettings{}, &models.LoyaltySettings{}, &models.AIProviderConfig{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
}

// callIntegration runs handler as the given store-owner scope and role.
func callIntegration(t *testing.T, handler gin.HandlerFunc, method string, scope uuid.UUID, role string, body interface{}) (int, map[string]interface{}) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	c.Request = httptest.NewRequest(method, "/", &buf)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user_id", scope)
	c.Set("role", role)
	handler(c)
	var out map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestWhatsAppGatewayIsStoreSpecific(t *testing.T) {
	setupIntegrationsTestDB(t)
	storeA, storeB := uuid.New(), uuid.New()

	code, out := callIntegration(t, UpdateWhatsAppGatewayIntegration, http.MethodPut, storeA, "super_admin", map[string]string{
		"provider": "whatsapp_cloud", "api_key": "token-abcd", "phone_number_id": "12345",
	})
	if code != http.StatusOK || out["configured"] != true || out["api_key_hint"] != "••••abcd" {
		t.Fatalf("save store A = %d %v", code, out)
	}

	_, outB := callIntegration(t, GetWhatsAppGatewayIntegration, http.MethodGet, storeB, "super_admin", nil)
	if outB["has_api_key"] != false || outB["configured"] != false || outB["phone_number_id"] != "" {
		t.Fatalf("store B leaked store A gateway: %v", outB)
	}

	// Blank api_key keeps the saved secret.
	_, out = callIntegration(t, UpdateWhatsAppGatewayIntegration, http.MethodPut, storeA, "super_admin", map[string]string{
		"provider": "whatsapp_cloud", "phone_number_id": "999",
	})
	if out["has_api_key"] != true || out["phone_number_id"] != "999" {
		t.Fatalf("blank key should keep secret: %v", out)
	}
}

func TestWhatsAppGatewayProviderSwitchDisablesInvoices(t *testing.T) {
	setupIntegrationsTestDB(t)
	scope := uuid.New()
	key, _ := utils.Encrypt("meta-token")
	if err := utils.DB.Create(&models.WhatsAppInvoiceSettings{ID: uuid.New(), UserID: scope, Enabled: true,
		Provider: whatsAppProviderCloud, EncryptedAPIKey: key, PhoneNumberID: "111", MessageID: "purchase_summary"}).Error; err != nil {
		t.Fatal(err)
	}

	// Removing the sender while invoices are on is refused.
	code, _ := callIntegration(t, UpdateWhatsAppGatewayIntegration, http.MethodPut, scope, "super_admin", map[string]string{
		"provider": "whatsapp_cloud", "phone_number_id": "",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("clearing sender while enabled = %d, want 400", code)
	}

	// Switching provider drops the old secret and turns invoices off.
	code, out := callIntegration(t, UpdateWhatsAppGatewayIntegration, http.MethodPut, scope, "super_admin", map[string]string{
		"provider": "fast2sms", "phone_number_id": "111",
	})
	if code != http.StatusOK || out["has_api_key"] != false || out["invoices_enabled"] != false || out["invoices_auto_disabled"] != true {
		t.Fatalf("provider switch = %d %v", code, out)
	}
}

func TestWhatsAppInvoiceSettingsIgnoresGatewayFields(t *testing.T) {
	setupIntegrationsTestDB(t)
	scope := uuid.New()
	key, _ := utils.Encrypt("meta-token")
	if err := utils.DB.Create(&models.WhatsAppInvoiceSettings{ID: uuid.New(), UserID: scope,
		Provider: whatsAppProviderCloud, EncryptedAPIKey: key, PhoneNumberID: "111"}).Error; err != nil {
		t.Fatal(err)
	}

	code, out := callIntegration(t, UpdateWhatsAppInvoiceSettings, http.MethodPut, scope, "admin", map[string]interface{}{
		"provider": "twilio", "api_key": "attacker", "phone_number_id": "222", "account_sid": "ACx",
		"message_id": "summary", "country_code": "91",
	})
	if code != http.StatusOK {
		t.Fatalf("save = %d %v", code, out)
	}
	s, _ := loadWhatsAppInvoiceSettings(scope)
	if s.Provider != whatsAppProviderCloud || s.EncryptedAPIKey != key || s.PhoneNumberID != "111" || s.AccountSID != "" {
		t.Fatalf("gateway changed through invoice settings: %+v", s)
	}
	if s.MessageID != "summary" {
		t.Fatalf("template not saved: %q", s.MessageID)
	}
	if out["api_key_hint"] != "" || out["phone_number_id_hint"] != "" {
		t.Fatalf("non-super-admin got credential hints: %v", out)
	}
}

func TestAIIntegrationSettings(t *testing.T) {
	setupIntegrationsTestDB(t)
	scope := uuid.New()

	code, _ := callIntegration(t, UpdateAIIntegration, http.MethodPut, scope, "super_admin", map[string]interface{}{
		"provider": "openai", "enable_ai_hsn_search": true,
	})
	if code != http.StatusBadRequest {
		t.Fatalf("enable without key = %d, want 400", code)
	}

	code, out := callIntegration(t, UpdateAIIntegration, http.MethodPut, scope, "super_admin", map[string]interface{}{
		"provider": "openai", "enable_ai_hsn_search": true, "api_key": "sk-test-wxyz", "model": "gpt-4o",
	})
	if code != http.StatusOK || out["provider"] != "openai" || out["configured"] != true {
		t.Fatalf("save = %d %v", code, out)
	}
	openai := out["configs"].(map[string]interface{})["openai"].(map[string]interface{})
	if openai["api_key_hint"] != "••••wxyz" || openai["model"] != "gpt-4o" {
		t.Fatalf("openai config = %v", openai)
	}

	// Switching to Claude keeps the OpenAI key; Ollama needs no key.
	_, out = callIntegration(t, UpdateAIIntegration, http.MethodPut, scope, "super_admin", map[string]interface{}{
		"provider": "ollama", "enable_ai_bill_parsing": true, "base_url": "http://10.0.0.5:11434/",
	})
	configs := out["configs"].(map[string]interface{})
	if out["provider"] != "ollama" || configs["openai"].(map[string]interface{})["has_api_key"] != true ||
		configs["ollama"].(map[string]interface{})["base_url"] != "http://10.0.0.5:11434" {
		t.Fatalf("after switching to ollama: %v", out)
	}

	code, _ = callIntegration(t, UpdateAIIntegration, http.MethodPut, scope, "super_admin", map[string]interface{}{
		"provider": "ollama", "base_url": "ftp://nope",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("bad base URL = %d, want 400", code)
	}

	_, other := callIntegration(t, GetAIIntegration, http.MethodGet, uuid.New(), "super_admin", nil)
	if other["configured"] != false || other["provider"] != "gemini" {
		t.Fatalf("other store sees this store's AI config: %v", other)
	}
}

func TestAIIntegrationMigratesLegacyGeminiKey(t *testing.T) {
	setupIntegrationsTestDB(t)
	scope := uuid.New()
	if err := utils.DB.Create(&models.Business{ID: uuid.New(), UserID: scope, Name: "Shop", GeminiAPIKey: "legacy-1234"}).Error; err != nil {
		t.Fatal(err)
	}
	_, out := callIntegration(t, GetAIIntegration, http.MethodGet, scope, "super_admin", nil)
	if out["configured"] != true {
		t.Fatalf("legacy key not honoured: %v", out)
	}
	callIntegration(t, UpdateAIIntegration, http.MethodPut, scope, "super_admin", map[string]interface{}{
		"provider": "gemini", "enable_ai_hsn_search": true,
	})
	var b models.Business
	utils.DB.Where("user_id = ?", scope).First(&b)
	row, _ := loadAIProviderConfig(scope, aiProviderGemini)
	if b.GeminiAPIKey != "" || row.EncryptedAPIKey == "" {
		t.Fatalf("legacy key not moved: business=%q row=%q", b.GeminiAPIKey, row.EncryptedAPIKey)
	}
	cfg, _ := activeAIConfig(b)
	if cfg.APIKey != "legacy-1234" {
		t.Fatalf("decrypted key = %q", cfg.APIKey)
	}
}
