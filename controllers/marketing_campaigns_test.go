package controllers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupMarketingTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Business{}, &models.Store{}, &models.User{}, &models.Party{},
		&models.WhatsAppInvoiceSettings{}, &models.TelegramSettings{}, &models.TelegramMessageLog{},
		&models.DeveloperSettings{}, &models.MarketingCampaign{}, &models.MarketingCampaignDelivery{},
		&models.PageFeatureSettings{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	prev := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = prev })
	return db
}

// fakeWhatsApp stands in for the Meta Cloud API and records template payloads.
type fakeWhatsApp struct {
	mu       sync.Mutex
	payloads []map[string]interface{}
	failTo   string // recipient number that gets rejected
}

func newFakeWhatsApp(t *testing.T) *fakeWhatsApp {
	t.Helper()
	f := &fakeWhatsApp{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&p)
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer wa-token" {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":{"message":"bad token"}}`))
			return
		}
		if p["to"] == f.failTo {
			w.WriteHeader(400)
			w.Write([]byte(`{"error":{"message":"Template name does not exist"}}`))
			return
		}
		f.payloads = append(f.payloads, p)
		w.Write([]byte(`{"messages":[{"id":"wamid.1"}]}`))
	}))
	t.Setenv("WHATSAPP_CLOUD_API_BASE", srv.URL)
	t.Cleanup(srv.Close)
	return f
}

type sentRecord struct{ To, Subject, Body string }

// stubMarketingTransports replaces the email and SMS senders.
func stubMarketingTransports(t *testing.T) (emails, sms *[]sentRecord) {
	t.Helper()
	var mu sync.Mutex
	e, s := []sentRecord{}, []sentRecord{}
	prevE, prevS := marketingEmailSender, marketingSMSSender
	marketingEmailSender = func(_ uuid.UUID, to, subject, body string) error {
		mu.Lock()
		defer mu.Unlock()
		e = append(e, sentRecord{to, subject, body})
		return nil
	}
	marketingSMSSender = func(_ models.DeveloperSettings, to, msg string) error {
		mu.Lock()
		defer mu.Unlock()
		s = append(s, sentRecord{To: to, Body: msg})
		return nil
	}
	t.Cleanup(func() { marketingEmailSender, marketingSMSSender = prevE, prevS })
	return &e, &s
}

// connectAllMarketingChannels sets up every integration for the store.
func connectAllMarketingChannels(t *testing.T, db *gorm.DB, userID uuid.UUID) {
	t.Helper()
	waKey, _ := utils.Encrypt("wa-token")
	if err := db.Create(&models.WhatsAppInvoiceSettings{ID: uuid.New(), UserID: userID, Provider: whatsAppProviderCloud,
		EncryptedAPIKey: waKey, PhoneNumberID: "111", CountryCode: "91"}).Error; err != nil {
		t.Fatal(err)
	}
	connectTestBot(t, db, userID, true, "9001")
	smtpPass, _ := utils.Encrypt("pw")
	smsTok, _ := utils.Encrypt("tok")
	if err := db.Create(&models.DeveloperSettings{ID: uuid.New(), UserID: userID,
		EmailProvider: "smtp", SMTPHost: "smtp.test", SMTPUsername: "u", EncryptedSMTPPassword: smtpPass, FromEmail: "shop@test.in",
		SMSProvider: "twilio", TwilioSMSAccountSID: "AC1", EncryptedTwilioSMSAuthToken: smsTok, TwilioSMSPhoneNumber: "+15550001",
	}).Error; err != nil {
		t.Fatal(err)
	}
}

func callMarketing(t *testing.T, handler gin.HandlerFunc, method string, userID uuid.UUID, id string, body interface{}) (int, map[string]interface{}) {
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
	c.Set("user_id", userID)
	if id != "" {
		c.Params = gin.Params{{Key: "id", Value: id}}
	}
	handler(c)
	var out map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestRenderMarketingText(t *testing.T) {
	values := map[string]string{"customer_name": "Asha <Rao>", "offer_code": "DIWALI20"}
	got := renderMarketingText("Hi {{customer_name}}, use {{ offer_code }} {{unknown}}", values, nil)
	if got != "Hi Asha <Rao>, use DIWALI20 {{unknown}}" {
		t.Fatalf("render = %q", got)
	}
	esc := renderMarketingText("Hi {{customer_name}}", values, func(s string) string { return strings.ReplaceAll(s, "<", "&lt;") })
	if esc != "Hi Asha &lt;Rao>" {
		t.Fatalf("escaped render = %q", esc)
	}
	if formatMarketingDate("2026-12-31") != "31 Dec 2026" {
		t.Fatalf("date = %q", formatMarketingDate("2026-12-31"))
	}
}

func TestAddWhatsAppImageHeaderGoesFirst(t *testing.T) {
	p := buildWhatsAppTemplatePayload("919800000000", "diwali_offer", "en", "", "", []string{"Asha"})
	addWhatsAppImageHeader(p, "https://cdn.test/banner.jpg")
	comps := p["template"].(map[string]interface{})["components"].([]map[string]interface{})
	if len(comps) != 2 || comps[0]["type"] != "header" || comps[1]["type"] != "body" {
		t.Fatalf("components = %v", comps)
	}
}

func TestMarketingCampaignSendsOnEveryChannel(t *testing.T) {
	db := setupMarketingTestDB(t)
	wa := newFakeWhatsApp(t)
	tg := newFakeTelegram(t)
	emails, sms := stubMarketingTransports(t)
	store := uuid.New()
	other := uuid.New()
	connectAllMarketingChannels(t, db, store)
	db.Create(&models.Business{ID: uuid.New(), UserID: store, Name: "Sharma Stores", Phone: "9800011111"})

	parties := []models.Party{
		{ID: uuid.New(), UserID: store, Name: "Asha Rao", PartyType: "customer", Phone: "98765 43210", Email: "asha@test.in", TelegramChatID: "5001", IsActive: true},
		{ID: uuid.New(), UserID: store, Name: "Ravi", PartyType: "customer", Phone: "+91 9876543210", IsActive: true}, // same number as Asha
		{ID: uuid.New(), UserID: store, Name: "No Contact", PartyType: "customer", IsActive: true},
		{ID: uuid.New(), UserID: store, Name: "Vendor Co", PartyType: "vendor", Phone: "9000000001", IsActive: true},
		{ID: uuid.New(), UserID: other, Name: "Other Store Customer", PartyType: "customer", Phone: "9111111111", IsActive: true},
	}
	for i := range parties {
		if err := db.Create(&parties[i]).Error; err != nil {
			t.Fatal(err)
		}
	}

	input := map[string]interface{}{
		"name": "Diwali Sale", "type": "festival",
		"channels":   []string{"whatsapp", "telegram", "email", "sms"},
		"offer_code": "DIWALI20", "discount": "20% off", "valid_until": "2026-11-30",
		"message":          "Hi {{first_name}}, {{discount}} at {{store_name}}! Code {{offer_code}}",
		"sms_message":      "{{store_name}}: {{discount}} code {{offer_code}}",
		"email_subject":    "{{discount}} for you, {{first_name}}",
		"wa_template_name": "festival_offer", "wa_language": "en",
		"wa_variables":  []string{"{{customer_name}}", "{{discount}}", "{{offer_code}}", "{{valid_until}}"},
		"audience_type": "all_customers",
	}
	code, out := callMarketing(t, CreateMarketingCampaign, http.MethodPost, store, "", input)
	if code != http.StatusCreated {
		t.Fatalf("create = %d %v", code, out)
	}
	id := out["id"].(string)

	// Audience preview: 3 customers; Asha+Ravi share one number.
	code, prev := callMarketing(t, PreviewMarketingAudience, http.MethodPost, store, "", input)
	reach := prev["reachable"].(map[string]interface{})
	if code != 200 || prev["total"].(float64) != 3 || reach["whatsapp"].(float64) != 1 || reach["email"].(float64) != 1 || reach["telegram"].(float64) != 1 {
		t.Fatalf("preview = %d %v", code, prev)
	}

	campID := uuid.MustParse(id)
	if !claimMarketingCampaign(campID, mktStatusDraft) {
		t.Fatal("claim failed")
	}
	if claimMarketingCampaign(campID, mktStatusDraft) {
		t.Fatal("second claim must fail (double send)")
	}
	runMarketingCampaign(campID)

	var camp models.MarketingCampaign
	db.First(&camp, "id = ?", campID)
	// Asha: 4 channels sent. Ravi: WA+SMS duplicate, email+telegram skipped. No Contact: 4 skipped.
	if camp.Status != mktStatusSent || camp.SentCount != 4 || camp.FailedCount != 0 || camp.SkippedCount != 8 || camp.TotalRecipients != 3 {
		t.Fatalf("campaign = status %s sent %d failed %d skipped %d total %d (%s)", camp.Status, camp.SentCount, camp.FailedCount, camp.SkippedCount, camp.TotalRecipients, camp.LastError)
	}

	if len(wa.payloads) != 1 {
		t.Fatalf("whatsapp payloads = %d", len(wa.payloads))
	}
	p := wa.payloads[0]
	tmpl := p["template"].(map[string]interface{})
	params := tmpl["components"].([]interface{})[0].(map[string]interface{})["parameters"].([]interface{})
	var got []string
	for _, x := range params {
		got = append(got, x.(map[string]interface{})["text"].(string))
	}
	if p["to"] != "919876543210" || tmpl["name"] != "festival_offer" || strings.Join(got, "|") != "Asha Rao|20% off|DIWALI20|30 Nov 2026" {
		t.Fatalf("whatsapp payload = %v / %v", p, got)
	}
	if len(tg.messages) != 1 || tg.messages[0]["chat_id"] != "5001" || tg.messages[0]["text"] != "Hi Asha, 20% off at Sharma Stores! Code DIWALI20" {
		t.Fatalf("telegram = %v", tg.messages)
	}
	if len(*emails) != 1 || (*emails)[0].To != "asha@test.in" || (*emails)[0].Subject != "20% off for you, Asha" || !strings.Contains((*emails)[0].Body, "DIWALI20") {
		t.Fatalf("emails = %v", *emails)
	}
	if len(*sms) != 1 || (*sms)[0].To != "919876543210" || (*sms)[0].Body != "Sharma Stores: 20% off code DIWALI20" {
		t.Fatalf("sms = %v", *sms)
	}

	// Sent campaigns can't be edited or re-sent.
	if code, _ := callMarketing(t, SendMarketingCampaign, http.MethodPost, store, id, nil); code != http.StatusBadRequest {
		t.Fatalf("resend = %d", code)
	}
	if code, _ := callMarketing(t, UpdateMarketingCampaign, http.MethodPut, store, id, input); code != http.StatusBadRequest {
		t.Fatalf("edit sent = %d", code)
	}
	// Other store can't see it.
	if code, _ := callMarketing(t, GetMarketingCampaign, http.MethodGet, other, id, nil); code != http.StatusNotFound {
		t.Fatalf("cross-store get = %d", code)
	}
}

func TestMarketingCampaignPartialOnProviderError(t *testing.T) {
	db := setupMarketingTestDB(t)
	wa := newFakeWhatsApp(t)
	wa.failTo = "919000000002"
	stubMarketingTransports(t)
	store := uuid.New()
	connectAllMarketingChannels(t, db, store)
	db.Create(&models.Party{ID: uuid.New(), UserID: store, Name: "A", PartyType: "customer", Phone: "9000000001", Category: "VIP", IsActive: true})
	db.Create(&models.Party{ID: uuid.New(), UserID: store, Name: "B", PartyType: "customer", Phone: "9000000002", Category: "VIP", IsActive: true})
	db.Create(&models.Party{ID: uuid.New(), UserID: store, Name: "C", PartyType: "customer", Phone: "9000000003", Category: "Retail", IsActive: true})

	code, out := callMarketing(t, CreateMarketingCampaign, http.MethodPost, store, "", map[string]interface{}{
		"name": "VIP", "channels": []string{"whatsapp"}, "wa_template_name": "vip_offer",
		"audience_type": "category", "audience_category": "VIP",
	})
	if code != http.StatusCreated {
		t.Fatalf("create = %d %v", code, out)
	}
	id := uuid.MustParse(out["id"].(string))
	claimMarketingCampaign(id, mktStatusDraft)
	runMarketingCampaign(id)

	var camp models.MarketingCampaign
	db.First(&camp, "id = ?", id)
	if camp.Status != mktStatusPartial || camp.SentCount != 1 || camp.FailedCount != 1 || !strings.Contains(camp.LastError, "Template name does not exist") {
		t.Fatalf("campaign = %s %d/%d %q", camp.Status, camp.SentCount, camp.FailedCount, camp.LastError)
	}
	var failed models.MarketingCampaignDelivery
	db.Where("campaign_id = ? AND status = ?", id, "failed").First(&failed)
	if failed.PartyName != "B" || failed.Recipient != "919000000002" {
		t.Fatalf("failed delivery = %+v", failed)
	}
}

func TestMarketingSendRefusedWhenChannelNotConnected(t *testing.T) {
	setupMarketingTestDB(t)
	store := uuid.New()
	code, out := callMarketing(t, CreateMarketingCampaign, http.MethodPost, store, "", map[string]interface{}{
		"name": "Offer", "channels": []string{"telegram", "sms"}, "message": "Hello",
	})
	if code != http.StatusCreated {
		t.Fatalf("create draft without integrations should work: %d %v", code, out)
	}
	code, out = callMarketing(t, SendMarketingCampaign, http.MethodPost, store, out["id"].(string), nil)
	errs, _ := out["errors"].([]interface{})
	if code != http.StatusBadRequest || len(errs) != 2 {
		t.Fatalf("send = %d %v", code, out)
	}

	// Scheduling also checks readiness.
	at := time.Now().Add(time.Hour)
	code, _ = callMarketing(t, CreateMarketingCampaign, http.MethodPost, store, "", map[string]interface{}{
		"name": "Later", "channels": []string{"sms"}, "message": "Hi", "scheduled_at": at,
	})
	if code != http.StatusBadRequest {
		t.Fatalf("schedule unready = %d", code)
	}
}

func TestMarketingSchedulerSendsDueCampaigns(t *testing.T) {
	db := setupMarketingTestDB(t)
	_, sms := stubMarketingTransports(t)
	store := uuid.New()
	connectAllMarketingChannels(t, db, store)
	db.Create(&models.Party{ID: uuid.New(), UserID: store, Name: "A", PartyType: "customer", Phone: "9000000001", IsActive: true})

	at := time.Now().Add(2 * time.Minute)
	code, out := callMarketing(t, CreateMarketingCampaign, http.MethodPost, store, "", map[string]interface{}{
		"name": "Weekend", "channels": []string{"sms"}, "message": "Weekend sale", "scheduled_at": at,
	})
	if code != http.StatusCreated || out["status"] != mktStatusScheduled {
		t.Fatalf("schedule = %d %v", code, out)
	}
	processDueMarketingCampaigns()
	if len(*sms) != 0 {
		t.Fatal("sent before due")
	}
	db.Model(&models.MarketingCampaign{}).Where("id = ?", out["id"]).Update("scheduled_at", time.Now().Add(-time.Second))
	processDueMarketingCampaigns()
	if len(*sms) != 1 || (*sms)[0].Body != "Weekend sale" {
		t.Fatalf("sms = %v", *sms)
	}
	var camp models.MarketingCampaign
	db.First(&camp, "id = ?", out["id"])
	if camp.Status != mktStatusSent {
		t.Fatalf("status = %s", camp.Status)
	}
}

func TestMarketingInputValidation(t *testing.T) {
	cases := []struct {
		in   marketingCampaignInput
		want string
	}{
		{marketingCampaignInput{Channels: []string{"sms"}}, "name"},
		{marketingCampaignInput{Name: "x"}, "channel"},
		{marketingCampaignInput{Name: "x", Channels: []string{"fax"}}, "unknown channel"},
		{marketingCampaignInput{Name: "x", Channels: []string{"sms"}, AudienceType: "category"}, "category"},
		{marketingCampaignInput{Name: "x", Channels: []string{"sms"}, AudienceType: "specific"}, "party"},
		{marketingCampaignInput{Name: "x", Channels: []string{"sms"}, ImageURL: "http://x"}, "https"},
		{marketingCampaignInput{Name: "x", Channels: []string{"sms"}, ValidUntil: "31/12/2026"}, "date"},
	}
	for _, tc := range cases {
		var camp models.MarketingCampaign
		err := tc.in.apply(&camp)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), tc.want) {
			t.Errorf("apply(%+v) = %v, want error containing %q", tc.in, err, tc.want)
		}
	}
}

func TestMarketingEditCancelScheduleAndDuplicate(t *testing.T) {
	db := setupMarketingTestDB(t)
	stubMarketingTransports(t)
	store := uuid.New()
	connectAllMarketingChannels(t, db, store)

	at := time.Now().Add(time.Hour)
	code, out := callMarketing(t, CreateMarketingCampaign, http.MethodPost, store, "", map[string]interface{}{
		"name": "Offer", "channels": []string{"sms"}, "message": "Hi", "scheduled_at": at,
	})
	if code != http.StatusCreated {
		t.Fatalf("create = %d %v", code, out)
	}
	id := out["id"].(string)

	// Editing without a schedule turns it back into a draft with the new content.
	code, out = callMarketing(t, UpdateMarketingCampaign, http.MethodPut, store, id, map[string]interface{}{
		"name": "Offer v2", "channels": []string{"sms", "email"}, "message": "Hello", "email_subject": "Sale",
		"audience_type": "category", "audience_category": "VIP",
	})
	if code != http.StatusOK || out["name"] != "Offer v2" || out["status"] != mktStatusDraft || out["scheduled_at"] != nil ||
		out["channels"] != "sms,email" || out["audience_category"] != "VIP" {
		t.Fatalf("update = %d %v", code, out)
	}

	// Reschedule, then cancel.
	code, out = callMarketing(t, UpdateMarketingCampaign, http.MethodPut, store, id, map[string]interface{}{
		"name": "Offer v2", "channels": []string{"sms"}, "message": "Hello", "scheduled_at": at,
	})
	if code != http.StatusOK || out["status"] != mktStatusScheduled {
		t.Fatalf("reschedule = %d %v", code, out)
	}
	code, out = callMarketing(t, CancelMarketingSchedule, http.MethodPost, store, id, nil)
	if code != http.StatusOK || out["status"] != mktStatusDraft {
		t.Fatalf("cancel = %d %v", code, out)
	}
	if code, _ := callMarketing(t, CancelMarketingSchedule, http.MethodPost, store, id, nil); code != http.StatusBadRequest {
		t.Fatalf("cancel draft = %d", code)
	}

	// Past schedule times are refused.
	code, _ = callMarketing(t, UpdateMarketingCampaign, http.MethodPut, store, id, map[string]interface{}{
		"name": "Offer v2", "channels": []string{"sms"}, "message": "Hello", "scheduled_at": time.Now().Add(-time.Hour),
	})
	if code != http.StatusBadRequest {
		t.Fatalf("past schedule = %d", code)
	}

	// Duplicate a sent campaign into a fresh draft.
	db.Model(&models.MarketingCampaign{}).Where("id = ?", id).Updates(map[string]interface{}{"status": mktStatusSent, "sent_count": 5})
	code, out = callMarketing(t, DuplicateMarketingCampaign, http.MethodPost, store, id, nil)
	if code != http.StatusCreated || out["id"] == id || out["name"] != "Offer v2 (copy)" || out["status"] != mktStatusDraft || out["sent_count"].(float64) != 0 {
		t.Fatalf("duplicate = %d %v", code, out)
	}
}

func disableMarketingPage(t *testing.T, db *gorm.DB, storeID uuid.UUID) {
	t.Helper()
	pages := defaultPageFeatures()
	pages[marketingPageKey] = false
	raw, _ := json.Marshal(pages)
	if err := db.Create(&models.PageFeatureSettings{ID: uuid.New(), StoreID: &storeID, PagesJSON: string(raw)}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestMarketingPageToggleBlocksAPI(t *testing.T) {
	db := setupMarketingTestDB(t)
	storeOn, storeOff := uuid.New(), uuid.New()
	disableMarketingPage(t, db, storeOff)

	run := func(storeID uuid.UUID) (int, bool) {
		gin.SetMode(gin.TestMode)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		c.Set("store_id", storeID)
		reached := false
		MarketingPageRequired()(c)
		if !c.IsAborted() {
			reached = true
		}
		return w.Code, reached
	}
	if code, reached := run(storeOff); reached || code != http.StatusForbidden {
		t.Fatalf("disabled store: code %d reached %v", code, reached)
	}
	if _, reached := run(storeOn); !reached {
		t.Fatal("enabled store was blocked")
	}
	// Sub-tab toggles don't block the API.
	if !pageFeatureEnabledForStore(&storeOff, "/marketing/channels") {
		t.Fatal("unrelated key should stay enabled")
	}
}

func TestMarketingSchedulerSkipsDisabledStore(t *testing.T) {
	db := setupMarketingTestDB(t)
	_, sms := stubMarketingTransports(t)
	owner, storeID := uuid.New(), uuid.New()
	connectAllMarketingChannels(t, db, owner)
	db.Create(&models.Party{ID: uuid.New(), UserID: owner, Name: "A", PartyType: "customer", Phone: "9000000001", IsActive: true})
	past := time.Now().Add(-time.Minute)
	camp := models.MarketingCampaign{ID: uuid.New(), UserID: owner, StoreID: &storeID, Name: "Sale", Channels: "sms",
		Message: "Hi", AudienceType: "all_customers", Status: mktStatusScheduled, ScheduledAt: &past}
	db.Create(&camp)
	disableMarketingPage(t, db, storeID)

	processDueMarketingCampaigns()
	db.First(&camp, "id = ?", camp.ID)
	if len(*sms) != 0 || camp.Status != mktStatusDraft || !strings.Contains(camp.LastError, "disabled") {
		t.Fatalf("sent %d, status %s, err %q", len(*sms), camp.Status, camp.LastError)
	}
}
