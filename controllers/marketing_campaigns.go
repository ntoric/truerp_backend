package controllers

// Marketing → Campaigns: store-specific promotional campaigns (offers,
// festival sales, new arrivals, loyalty reminders, announcements) sent to the
// store's parties over WhatsApp, Telegram, email and SMS.
//
// Nothing here has its own credentials: every channel uses the connection the
// store already set up.
//   - WhatsApp: the store's WhatsApp gateway (Meta Cloud API, Fast2SMS or
//     Twilio, Developer Settings → Integrations). Business-initiated WhatsApp
//     messages must use an approved template, so a campaign names one and maps
//     its {{1}}, {{2}}… variables.
//   - Telegram: the store's bot (Settings → Customer Messaging → Telegram);
//     reaches customers who linked their chat.
//   - Email: the store's SMTP connection.
//   - SMS: the store's SMS provider (Twilio, MSG91, TextLocal).
//
// Rows are keyed by user_id, which AuthRequired resolves to the owner of the
// X-Store-ID store, so campaigns are per store.

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	mktChannelWhatsApp = "whatsapp"
	mktChannelTelegram = "telegram"
	mktChannelEmail    = "email"
	mktChannelSMS      = "sms"

	mktStatusDraft     = "draft"
	mktStatusScheduled = "scheduled"
	mktStatusSending   = "sending"
	mktStatusSent      = "sent"
	mktStatusPartial   = "partial"
	mktStatusFailed    = "failed"

	mktMaxWAVariables = 20

	// Page key in Developer Settings → Pages & Menus.
	marketingPageKey = "/marketing"
)

var marketingChannelOrder = []string{mktChannelWhatsApp, mktChannelTelegram, mktChannelEmail, mktChannelSMS}

var marketingChannelLabels = map[string]string{
	mktChannelWhatsApp: "WhatsApp",
	mktChannelTelegram: "Telegram",
	mktChannelEmail:    "Email",
	mktChannelSMS:      "SMS",
}

var marketingCampaignTypes = map[string]bool{
	"promotion": true, "offer": true, "festival": true, "new_arrival": true,
	"loyalty": true, "reengagement": true, "announcement": true,
}

var marketingAudienceTypes = map[string]bool{
	"all_customers": true, "all_vendors": true, "all_parties": true,
	"category": true, "loyalty_members": true, "specific": true,
}

// Senders are package variables so tests can stub the email and SMS
// transports (WhatsApp and Telegram are faked at the HTTP level).
var (
	marketingEmailSender = utils.SendEmailForUser
	marketingSMSSender   = utils.SendStoreSMS
)

// ---------------------------------------------------------------------------
// Placeholders
// ---------------------------------------------------------------------------

type marketingPlaceholder struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

var marketingPlaceholders = []marketingPlaceholder{
	{"customer_name", "Customer name"},
	{"first_name", "Customer first name"},
	{"store_name", "Store name"},
	{"store_phone", "Store phone"},
	{"offer_title", "Offer title"},
	{"offer_code", "Coupon / offer code"},
	{"discount", "Discount (e.g. 20% off)"},
	{"valid_until", "Offer valid until"},
	{"link", "Link (shop / catalogue URL)"},
	{"loyalty_points", "Customer loyalty points"},
	{"loyalty_cash", "Customer loyalty cashback (₹)"},
}

var marketingPlaceholderRe = regexp.MustCompile(`\{\{\s*([a-z_]+)\s*\}\}`)

// renderMarketingText fills {{key}} placeholders from values; escape (may be
// nil) is applied to each substituted value. Unknown keys are left as typed.
func renderMarketingText(tpl string, values map[string]string, escape func(string) string) string {
	return marketingPlaceholderRe.ReplaceAllStringFunc(tpl, func(m string) string {
		key := marketingPlaceholderRe.FindStringSubmatch(m)[1]
		v, ok := values[key]
		if !ok {
			return m
		}
		if escape != nil {
			return escape(v)
		}
		return v
	})
}

func formatMarketingDate(ymd string) string {
	ymd = strings.TrimSpace(ymd)
	if ymd == "" {
		return ""
	}
	if t, err := time.Parse("2006-01-02", ymd); err == nil {
		return t.Format("02 Jan 2006")
	}
	return ymd
}

// marketingValues builds the placeholder values for one party.
func marketingValues(camp *models.MarketingCampaign, party *models.Party, business *models.Business) map[string]string {
	name := strings.TrimSpace(party.Name)
	if name == "" {
		name = "Customer"
	}
	first := name
	if f := strings.Fields(name); len(f) > 0 {
		first = f[0]
	}
	return map[string]string{
		"customer_name":  name,
		"first_name":     first,
		"store_name":     strings.TrimSpace(business.Name),
		"store_phone":    strings.TrimSpace(business.Phone),
		"offer_title":    strings.TrimSpace(camp.OfferTitle),
		"offer_code":     strings.TrimSpace(camp.OfferCode),
		"discount":       strings.TrimSpace(camp.Discount),
		"valid_until":    formatMarketingDate(camp.ValidUntil),
		"link":           strings.TrimSpace(camp.Link),
		"loyalty_points": strconv.FormatInt(party.LoyaltyPoints, 10),
		"loyalty_cash":   formatINRAmount(party.LoyaltyCash),
	}
}

// ---------------------------------------------------------------------------
// Channel readiness (existing integrations)
// ---------------------------------------------------------------------------

type marketingChannelStatus struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Ready    bool   `json:"ready"`
	Provider string `json:"provider,omitempty"`
	Reason   string `json:"reason,omitempty"`
	SetupURL string `json:"setup_url"`
}

func marketingSMSProviderSupported(p string) bool {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "", "twilio", "msg91", "textlocal":
		return true
	}
	return false
}

func marketingChannelStatuses(userID uuid.UUID) map[string]marketingChannelStatus {
	out := map[string]marketingChannelStatus{}

	wa, waExists := loadWhatsAppInvoiceSettings(userID)
	waSt := marketingChannelStatus{Key: mktChannelWhatsApp, Label: "WhatsApp", SetupURL: "/developer-settings?tab=integrations&section=whatsapp"}
	if waExists {
		waSt.Provider = whatsAppProviderLabel(wa.Provider)
	}
	if !waExists {
		waSt.Reason = "WhatsApp gateway is not connected"
	} else if msg := whatsAppGatewayError(wa); msg != "" {
		waSt.Reason = msg
	} else {
		waSt.Ready = true
	}
	out[mktChannelWhatsApp] = waSt

	tg, tgExists := loadTelegramSettings(userID)
	tgSt := marketingChannelStatus{Key: mktChannelTelegram, Label: "Telegram", SetupURL: "/settings/messaging?tab=telegram"}
	switch {
	case !tgExists || tg.EncryptedBotToken == "":
		tgSt.Reason = "Telegram bot is not connected"
	case !tg.Enabled:
		tgSt.Reason = "Telegram messages are switched off"
	default:
		tgSt.Ready = true
		if tg.BotUsername != "" {
			tgSt.Provider = "@" + tg.BotUsername
		}
	}
	out[mktChannelTelegram] = tgSt

	var dev models.DeveloperSettings
	devExists := utils.DB.Where("user_id = ?", userID).First(&dev).Error == nil

	emSt := marketingChannelStatus{Key: mktChannelEmail, Label: "Email", SetupURL: "/developer-settings?tab=integrations&section=email"}
	if devExists {
		emSt.Provider = strings.ToUpper(dev.EmailProvider)
	}
	if utils.EmailConfiguredForUser(userID) {
		emSt.Ready = true
	} else {
		emSt.Reason = "SMTP email is not configured"
	}
	out[mktChannelEmail] = emSt

	smsSt := marketingChannelStatus{Key: mktChannelSMS, Label: "SMS", SetupURL: "/developer-settings?tab=integrations&section=sms"}
	switch {
	case !devExists || !smsConfigured(dev):
		smsSt.Reason = "SMS provider is not configured"
	case !marketingSMSProviderSupported(dev.SMSProvider):
		smsSt.Provider = dev.SMSProvider
		smsSt.Reason = "Campaigns support Twilio, MSG91 and TextLocal SMS"
	default:
		smsSt.Ready = true
		smsSt.Provider = dev.SMSProvider
		if smsSt.Provider == "" {
			smsSt.Provider = "twilio"
		}
	}
	out[mktChannelSMS] = smsSt
	return out
}

// GetMarketingChannels reports which channels the active store can send on,
// plus the placeholder catalogue for the editor.
func GetMarketingChannels(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	statuses := marketingChannelStatuses(userID)
	list := make([]marketingChannelStatus, 0, len(marketingChannelOrder))
	for _, k := range marketingChannelOrder {
		list = append(list, statuses[k])
	}
	resp := gin.H{"channels": list, "placeholders": marketingPlaceholders}
	if store, ok := requestStore(c); ok {
		resp["store_id"] = store.ID
		resp["store_name"] = store.Name
	}
	c.JSON(http.StatusOK, resp)
}

// ---------------------------------------------------------------------------
// Input / validation
// ---------------------------------------------------------------------------

type marketingCampaignInput struct {
	Name             string      `json:"name"`
	Type             string      `json:"type"`
	Channels         []string    `json:"channels"`
	OfferTitle       string      `json:"offer_title"`
	OfferCode        string      `json:"offer_code"`
	Discount         string      `json:"discount"`
	ValidUntil       string      `json:"valid_until"`
	Link             string      `json:"link"`
	ImageURL         string      `json:"image_url"`
	Message          string      `json:"message"`
	SMSMessage       string      `json:"sms_message"`
	EmailSubject     string      `json:"email_subject"`
	EmailBody        string      `json:"email_body"`
	WATemplateName   string      `json:"wa_template_name"`
	WALanguage       string      `json:"wa_language"`
	WAVariables      []string    `json:"wa_variables"`
	AudienceType     string      `json:"audience_type"`
	AudienceCategory string      `json:"audience_category"`
	AudiencePartyIDs []uuid.UUID `json:"audience_party_ids"`
	ScheduledAt      *time.Time  `json:"scheduled_at"`
}

// apply validates the input's structure and copies it onto camp.
func (in marketingCampaignInput) apply(camp *models.MarketingCampaign) error {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return errors.New("Campaign name is required")
	}
	typ := strings.TrimSpace(in.Type)
	if typ == "" {
		typ = "promotion"
	}
	if !marketingCampaignTypes[typ] {
		return fmt.Errorf("unknown campaign type %q", typ)
	}

	seen := map[string]bool{}
	var channels []string
	for _, ch := range in.Channels {
		ch = strings.ToLower(strings.TrimSpace(ch))
		if _, ok := marketingChannelLabels[ch]; !ok {
			return fmt.Errorf("unknown channel %q", ch)
		}
		if !seen[ch] {
			seen[ch] = true
			channels = append(channels, ch)
		}
	}
	if len(channels) == 0 {
		return errors.New("Pick at least one channel")
	}

	audience := strings.TrimSpace(in.AudienceType)
	if audience == "" {
		audience = "all_customers"
	}
	if !marketingAudienceTypes[audience] {
		return fmt.Errorf("unknown audience %q", audience)
	}
	if audience == "category" && strings.TrimSpace(in.AudienceCategory) == "" {
		return errors.New("Pick a party category for the audience")
	}
	if audience == "specific" && len(in.AudiencePartyIDs) == 0 {
		return errors.New("Pick at least one party for the audience")
	}

	if v := strings.TrimSpace(in.ValidUntil); v != "" {
		if _, err := time.Parse("2006-01-02", v); err != nil {
			return errors.New("Valid until must be a date (YYYY-MM-DD)")
		}
	}
	img := strings.TrimSpace(in.ImageURL)
	if img != "" && !strings.HasPrefix(img, "https://") {
		return errors.New("Image URL must be a public https:// link")
	}
	if len(in.WAVariables) > mktMaxWAVariables {
		return fmt.Errorf("A WhatsApp template can map at most %d variables", mktMaxWAVariables)
	}
	vars := make([]string, len(in.WAVariables))
	for i, v := range in.WAVariables {
		vars[i] = strings.TrimSpace(v)
	}
	varsJSON, _ := json.Marshal(vars)
	idsJSON := []byte("[]")
	if audience == "specific" {
		idsJSON, _ = json.Marshal(in.AudiencePartyIDs)
	}
	lang := strings.TrimSpace(in.WALanguage)
	if lang == "" {
		lang = "en"
	}

	camp.Name = name
	camp.Type = typ
	camp.Channels = strings.Join(channels, ",")
	camp.OfferTitle = strings.TrimSpace(in.OfferTitle)
	camp.OfferCode = strings.TrimSpace(in.OfferCode)
	camp.Discount = strings.TrimSpace(in.Discount)
	camp.ValidUntil = strings.TrimSpace(in.ValidUntil)
	camp.Link = strings.TrimSpace(in.Link)
	camp.ImageURL = img
	camp.Message = strings.TrimSpace(in.Message)
	camp.SMSMessage = strings.TrimSpace(in.SMSMessage)
	camp.EmailSubject = strings.TrimSpace(in.EmailSubject)
	camp.EmailBody = strings.TrimSpace(in.EmailBody)
	camp.WATemplateName = strings.TrimSpace(in.WATemplateName)
	camp.WALanguage = lang
	camp.WAVariables = string(varsJSON)
	camp.AudienceType = audience
	camp.AudienceCategory = strings.TrimSpace(in.AudienceCategory)
	camp.AudiencePartyIDs = string(idsJSON)
	return nil
}

func campaignChannels(camp *models.MarketingCampaign) []string {
	var out []string
	for _, ch := range strings.Split(camp.Channels, ",") {
		if ch = strings.TrimSpace(ch); ch != "" {
			out = append(out, ch)
		}
	}
	return out
}

func campaignWAVariables(camp *models.MarketingCampaign) []string {
	var vars []string
	if strings.TrimSpace(camp.WAVariables) != "" {
		_ = json.Unmarshal([]byte(camp.WAVariables), &vars)
	}
	return vars
}

// marketingContentError returns why a channel's content is incomplete, or "".
func marketingContentError(camp *models.MarketingCampaign, channel string) string {
	switch channel {
	case mktChannelWhatsApp:
		if camp.WATemplateName == "" {
			return "WhatsApp: approved template name (or Twilio Content SID) is required"
		}
	case mktChannelTelegram:
		if camp.Message == "" {
			return "Telegram: message is required"
		}
	case mktChannelEmail:
		if camp.EmailSubject == "" {
			return "Email: subject is required"
		}
		if camp.EmailBody == "" && camp.Message == "" {
			return "Email: body (or the common message) is required"
		}
	case mktChannelSMS:
		if camp.SMSMessage == "" && camp.Message == "" {
			return "SMS: message is required"
		}
	}
	return ""
}

// marketingReadyErrors lists every reason the campaign can't be sent now:
// missing content or channels whose integration isn't connected.
func marketingReadyErrors(camp *models.MarketingCampaign) []string {
	statuses := marketingChannelStatuses(camp.UserID)
	var errs []string
	for _, ch := range campaignChannels(camp) {
		if msg := marketingContentError(camp, ch); msg != "" {
			errs = append(errs, msg)
		}
		if st := statuses[ch]; !st.Ready {
			errs = append(errs, fmt.Sprintf("%s: %s", st.Label, st.Reason))
		}
	}
	return errs
}

// ---------------------------------------------------------------------------
// Audience
// ---------------------------------------------------------------------------

func marketingAudience(camp *models.MarketingCampaign) ([]models.Party, error) {
	q := utils.DB.Model(&models.Party{}).
		Where("user_id = ?", camp.UserID).
		Where("is_active = ? OR is_active IS NULL", true)
	switch camp.AudienceType {
	case "all_customers":
		q = q.Where("party_type = ?", "customer")
	case "all_vendors":
		q = q.Where("party_type = ?", "vendor")
	case "all_parties":
	case "category":
		q = q.Where("category = ?", camp.AudienceCategory)
	case "loyalty_members":
		q = q.Where("party_type = ? AND (loyalty_points > 0 OR loyalty_cash > 0)", "customer").
			Where("loyalty_excluded = ? OR loyalty_excluded IS NULL", false)
	case "specific":
		var ids []uuid.UUID
		_ = json.Unmarshal([]byte(camp.AudiencePartyIDs), &ids)
		if len(ids) == 0 {
			return nil, nil
		}
		q = q.Where("id IN ?", ids)
	default:
		return nil, fmt.Errorf("unknown audience %q", camp.AudienceType)
	}
	var parties []models.Party
	err := q.Order("name ASC").Find(&parties).Error
	return parties, err
}

func validEmail(s string) bool {
	s = strings.TrimSpace(s)
	at := strings.Index(s, "@")
	return at > 0 && at < len(s)-1 && strings.Contains(s[at:], ".") && !strings.ContainsAny(s, " ,;")
}

// marketingRecipient is the address of a party on a channel, or "" with the
// reason it can't be reached.
func marketingRecipient(channel string, p *models.Party, countryCode string) (string, string) {
	switch channel {
	case mktChannelWhatsApp, mktChannelSMS:
		if n := normalizeWhatsAppNumber(p.Phone, countryCode); n != "" {
			return n, ""
		}
		return "", "no valid mobile number"
	case mktChannelEmail:
		if validEmail(p.Email) {
			return strings.TrimSpace(p.Email), ""
		}
		return "", "no email address"
	case mktChannelTelegram:
		if id := strings.TrimSpace(p.TelegramChatID); id != "" {
			return id, ""
		}
		return "", "not linked on Telegram"
	}
	return "", "unknown channel"
}

func marketingCountryCode(userID uuid.UUID) string {
	wa, _ := loadWhatsAppInvoiceSettings(userID)
	if cc := onlyDigits(wa.CountryCode); cc != "" {
		return cc
	}
	return "91"
}

// PreviewMarketingAudience counts how many parties the audience selects and
// how many of them each channel can reach.
func PreviewMarketingAudience(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var in marketingCampaignInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}
	if in.Name == "" {
		in.Name = "preview"
	}
	if len(in.Channels) == 0 {
		in.Channels = marketingChannelOrder
	}
	camp := models.MarketingCampaign{UserID: userID}
	if err := in.apply(&camp); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	parties, err := marketingAudience(&camp)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load audience"})
		return
	}
	cc := marketingCountryCode(userID)
	reach := map[string]int{}
	for _, ch := range marketingChannelOrder {
		seen := map[string]bool{}
		for i := range parties {
			if r, _ := marketingRecipient(ch, &parties[i], cc); r != "" && !seen[r] {
				seen[r] = true
				reach[ch]++
			}
		}
	}
	sample := make([]gin.H, 0, 5)
	for i := 0; i < len(parties) && i < 5; i++ {
		sample = append(sample, gin.H{"id": parties[i].ID, "name": parties[i].Name})
	}
	c.JSON(http.StatusOK, gin.H{"total": len(parties), "reachable": reach, "sample": sample})
}

// ---------------------------------------------------------------------------
// Delivery (one party, one channel)
// ---------------------------------------------------------------------------

// marketingSender holds the per-run connection state so credentials are
// loaded and decrypted once per campaign run, not per message.
type marketingSender struct {
	userID   uuid.UUID
	camp     *models.MarketingCampaign
	business models.Business
	cc       string

	wa       models.WhatsAppInvoiceSettings
	waSecret string
	tgToken  string
	dev      models.DeveloperSettings
	// channelErr records a channel whose connection failed to load; every
	// delivery on it fails with this error instead of being attempted.
	channelErr map[string]error
}

func newMarketingSender(camp *models.MarketingCampaign) *marketingSender {
	s := &marketingSender{
		userID:     camp.UserID,
		camp:       camp,
		business:   resolveSellerBusiness(camp.UserID, camp.StoreID),
		cc:         marketingCountryCode(camp.UserID),
		channelErr: map[string]error{},
	}
	statuses := marketingChannelStatuses(camp.UserID)
	for _, ch := range campaignChannels(camp) {
		if st := statuses[ch]; !st.Ready {
			s.channelErr[ch] = errors.New(st.Reason)
			continue
		}
		switch ch {
		case mktChannelWhatsApp:
			s.wa, _ = loadWhatsAppInvoiceSettings(camp.UserID)
			secret, err := utils.Decrypt(s.wa.EncryptedAPIKey)
			if err != nil || secret == "" {
				s.channelErr[ch] = fmt.Errorf("failed to decrypt %s", whatsAppCredentialName(s.wa.Provider))
			}
			s.waSecret = secret
		case mktChannelTelegram:
			_, token, err := telegramReadyToken(camp.UserID)
			if err != nil {
				s.channelErr[ch] = err
			}
			s.tgToken = token
		case mktChannelSMS:
			utils.DB.Where("user_id = ?", camp.UserID).First(&s.dev)
		}
	}
	return s
}

func sanitizeWhatsAppParam(v string) string {
	m := map[string]string{"v": v}
	sanitizeWhatsAppValues(m)
	return m["v"]
}

// send delivers the campaign to one address on one channel. Returns the
// provider message ID when it has one.
func (s *marketingSender) send(channel, to string, values map[string]string) (string, error) {
	if err := s.channelErr[channel]; err != nil {
		return "", err
	}
	camp := s.camp
	switch channel {
	case mktChannelWhatsApp:
		templateVars := campaignWAVariables(camp)
		vars := make([]string, len(templateVars))
		for i, v := range templateVars {
			vars[i] = sanitizeWhatsAppParam(renderMarketingText(v, values, nil))
		}
		if s.wa.Provider == whatsAppProviderTwilio {
			// A Twilio Content template carries its own media; only body
			// variables are sent.
			sid, _, err := callTwilioWhatsAppAPI(s.wa.AccountSID, s.waSecret, s.wa.PhoneNumberID, to, camp.WATemplateName, vars, s.wa.CountryCode)
			return sid, err
		}
		payload := buildWhatsAppTemplatePayload(to, camp.WATemplateName, camp.WALanguage, "", "", vars)
		if camp.ImageURL != "" {
			addWhatsAppImageHeader(payload, camp.ImageURL)
		}
		id, _, err := callWhatsAppAPI(s.wa.Provider, s.wa.PhoneNumberID, s.waSecret, payload)
		return id, err

	case mktChannelTelegram:
		text := renderMarketingText(html.EscapeString(camp.Message), values, html.EscapeString)
		if camp.ImageURL != "" {
			if len([]rune(text)) <= telegramCaptionLimit {
				_, err := telegramCall(s.tgToken, "sendPhoto", map[string]interface{}{
					"chat_id": to, "photo": camp.ImageURL, "caption": text, "parse_mode": "HTML",
				})
				return "", err
			}
			if _, err := telegramCall(s.tgToken, "sendPhoto", map[string]interface{}{"chat_id": to, "photo": camp.ImageURL}); err != nil {
				return "", err
			}
		}
		return "", telegramSendMessage(s.tgToken, to, text)

	case mktChannelEmail:
		subject := renderMarketingText(camp.EmailSubject, values, nil)
		return "", marketingEmailSender(s.userID, to, subject, marketingEmailHTML(camp, values))

	case mktChannelSMS:
		msg := camp.SMSMessage
		if msg == "" {
			msg = camp.Message
		}
		return "", marketingSMSSender(s.dev, to, renderMarketingText(msg, values, nil))
	}
	return "", fmt.Errorf("unknown channel %q", channel)
}

// addWhatsAppImageHeader puts an Image header component first in a Meta-format
// template payload (for templates created with an Image header).
func addWhatsAppImageHeader(payload map[string]interface{}, imageURL string) {
	tmpl, ok := payload["template"].(map[string]interface{})
	if !ok {
		return
	}
	header := map[string]interface{}{
		"type":       "header",
		"parameters": []map[string]interface{}{{"type": "image", "image": map[string]interface{}{"link": imageURL}}},
	}
	comps, _ := tmpl["components"].([]map[string]interface{})
	tmpl["components"] = append([]map[string]interface{}{header}, comps...)
}

// marketingEmailHTML renders the email: the HTML body (or the common message
// with line breaks kept), an optional banner image and a store footer.
func marketingEmailHTML(camp *models.MarketingCampaign, values map[string]string) string {
	var body string
	if camp.EmailBody != "" {
		body = renderMarketingText(camp.EmailBody, values, html.EscapeString)
	} else {
		text := renderMarketingText(html.EscapeString(camp.Message), values, html.EscapeString)
		body = "<p>" + strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "<br>") + "</p>"
	}
	var b strings.Builder
	b.WriteString(`<div style="font-family:Arial,Helvetica,sans-serif;max-width:600px;margin:0 auto;color:#1f2937;line-height:1.5">`)
	if camp.ImageURL != "" {
		fmt.Fprintf(&b, `<img src="%s" alt="" style="width:100%%;max-width:600px;border-radius:8px;display:block;margin-bottom:16px">`, html.EscapeString(camp.ImageURL))
	}
	b.WriteString(body)
	if code := values["offer_code"]; code != "" && !strings.Contains(camp.EmailBody+camp.Message, "offer_code") {
		fmt.Fprintf(&b, `<p style="margin:20px 0;padding:12px 16px;border:2px dashed #ca8a04;border-radius:8px;text-align:center;font-size:18px;letter-spacing:2px"><b>%s</b></p>`, html.EscapeString(code))
	}
	if link := values["link"]; link != "" && strings.HasPrefix(link, "http") && !strings.Contains(camp.EmailBody, "{{link}}") {
		fmt.Fprintf(&b, `<p style="text-align:center;margin:24px 0"><a href="%s" style="background:#ca8a04;color:#fff;padding:10px 22px;border-radius:6px;text-decoration:none;font-weight:bold">Shop now</a></p>`, html.EscapeString(link))
	}
	if store := values["store_name"]; store != "" {
		fmt.Fprintf(&b, `<hr style="border:none;border-top:1px solid #e5e7eb;margin:24px 0 12px"><p style="font-size:12px;color:#6b7280">You are receiving this because you are a customer of %s.`, html.EscapeString(store))
		if phone := values["store_phone"]; phone != "" {
			fmt.Fprintf(&b, ` Contact: %s`, html.EscapeString(phone))
		}
		b.WriteString(`</p>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// ---------------------------------------------------------------------------
// Campaign run
// ---------------------------------------------------------------------------

// claimMarketingCampaign atomically moves a campaign from one of the given
// states to "sending"; false when another request or the scheduler got there
// first (prevents double sends).
func claimMarketingCampaign(id uuid.UUID, from ...string) bool {
	res := utils.DB.Model(&models.MarketingCampaign{}).
		Where("id = ? AND status IN ?", id, from).
		Updates(map[string]interface{}{"status": mktStatusSending, "last_error": ""})
	return res.Error == nil && res.RowsAffected == 1
}

// runMarketingCampaign sends a claimed ("sending") campaign to its whole
// audience on every selected channel and records one delivery row per party
// and channel. Duplicate addresses on a channel are sent once.
func runMarketingCampaign(campaignID uuid.UUID) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[marketing] panic running campaign %s: %v", campaignID, r)
			utils.DB.Model(&models.MarketingCampaign{}).Where("id = ?", campaignID).
				Updates(map[string]interface{}{"status": mktStatusFailed, "last_error": fmt.Sprintf("internal error: %v", r)})
		}
	}()

	var camp models.MarketingCampaign
	if err := utils.DB.First(&camp, "id = ?", campaignID).Error; err != nil {
		return
	}
	parties, err := marketingAudience(&camp)
	if err != nil {
		utils.DB.Model(&camp).Updates(map[string]interface{}{"status": mktStatusFailed, "last_error": "failed to load audience"})
		return
	}

	sender := newMarketingSender(&camp)
	channels := campaignChannels(&camp)
	utils.DB.Model(&camp).Update("total_recipients", len(parties))

	sent, failed, skipped := 0, 0, 0
	seen := map[string]map[string]bool{}
	for _, ch := range channels {
		seen[ch] = map[string]bool{}
	}
	channelErrs := map[string]string{}

	for i := range parties {
		p := &parties[i]
		values := marketingValues(&camp, p, &sender.business)
		for _, ch := range channels {
			d := models.MarketingCampaignDelivery{
				ID: uuid.New(), CampaignID: camp.ID, UserID: camp.UserID,
				PartyID: &p.ID, PartyName: p.Name, Channel: ch, CreatedAt: time.Now(),
			}
			to, why := marketingRecipient(ch, p, sender.cc)
			d.Recipient = to
			switch {
			case to == "":
				d.Status, d.Error = "skipped", why
				skipped++
			case seen[ch][to]:
				d.Status, d.Error = "skipped", "duplicate address (already sent in this campaign)"
				skipped++
			default:
				seen[ch][to] = true
				msgID, sendErr := sender.send(ch, to, values)
				d.MessageID = msgID
				if sendErr != nil {
					d.Status, d.Error = "failed", truncateWALog(sendErr.Error(), 1000)
					failed++
					if _, ok := channelErrs[ch]; !ok {
						channelErrs[ch] = sendErr.Error()
					}
				} else {
					d.Status = "sent"
					sent++
				}
			}
			if err := utils.DB.Create(&d).Error; err != nil {
				log.Printf("[marketing] campaign %s: failed to log delivery: %v", camp.ID, err)
			}
		}
		// Keep live counts so the UI can show progress while sending.
		if (i+1)%20 == 0 {
			utils.DB.Model(&camp).Updates(map[string]interface{}{"sent_count": sent, "failed_count": failed, "skipped_count": skipped})
		}
	}

	status := mktStatusSent
	var lastErr []string
	for _, ch := range channels {
		if e, ok := channelErrs[ch]; ok {
			lastErr = append(lastErr, marketingChannelLabels[ch]+": "+e)
		}
	}
	switch {
	case sent == 0 && failed == 0:
		status = mktStatusFailed
		lastErr = append(lastErr, "No party in the audience could be reached on the selected channels")
	case sent == 0:
		status = mktStatusFailed
	case failed > 0:
		status = mktStatusPartial
	}
	now := time.Now()
	utils.DB.Model(&camp).Updates(map[string]interface{}{
		"status": status, "sent_at": &now, "last_error": truncateWALog(strings.Join(lastErr, "\n"), 2000),
		"sent_count": sent, "failed_count": failed, "skipped_count": skipped,
	})
	log.Printf("[marketing] campaign %s (%s): %d sent, %d failed, %d skipped", camp.ID, camp.Name, sent, failed, skipped)
}

// ---------------------------------------------------------------------------
// Scheduler
// ---------------------------------------------------------------------------

// StartMarketingCampaignScheduler sends scheduled campaigns when due. Runs
// once at startup; campaigns left "sending" by a restart are closed out with
// the counts already logged.
func StartMarketingCampaignScheduler() {
	recoverInterruptedMarketingCampaigns()
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			processDueMarketingCampaigns()
		}
	}()
}

func recoverInterruptedMarketingCampaigns() {
	var stuck []models.MarketingCampaign
	if err := utils.DB.Where("status = ?", mktStatusSending).Find(&stuck).Error; err != nil {
		return
	}
	for _, camp := range stuck {
		counts := marketingDeliveryCounts(camp.ID)
		utils.DB.Model(&camp).Updates(map[string]interface{}{
			"status":     mktStatusPartial,
			"last_error": "Sending was interrupted by a server restart; parties not in the delivery log were not messaged",
			"sent_count": counts["sent"], "failed_count": counts["failed"], "skipped_count": counts["skipped"],
		})
	}
}

func processDueMarketingCampaigns() {
	var due []models.MarketingCampaign
	if err := utils.DB.Select("id", "user_id", "store_id", "name").
		Where("status = ? AND scheduled_at IS NOT NULL AND scheduled_at <= ?", mktStatusScheduled, time.Now()).
		Find(&due).Error; err != nil {
		log.Printf("[marketing] scheduler: %v", err)
		return
	}
	for _, d := range due {
		// Campaigns page switched off for the store: don't send, and don't
		// leave it to fire late once re-enabled; return it to draft instead.
		if !pageFeatureEnabledForStore(marketingStoreID(&d), marketingPageKey) {
			utils.DB.Model(&models.MarketingCampaign{}).
				Where("id = ? AND status = ?", d.ID, mktStatusScheduled).
				Updates(map[string]interface{}{
					"status":     mktStatusDraft,
					"last_error": "Not sent: Campaigns was disabled for this store (Pages & Menus) at the scheduled time. Reschedule or send it now.",
				})
			log.Printf("[marketing] scheduler: campaign %s (%s) skipped, Campaigns page disabled", d.ID, d.Name)
			continue
		}
		if claimMarketingCampaign(d.ID, mktStatusScheduled) {
			runMarketingCampaign(d.ID)
		}
	}
}

func marketingDeliveryCounts(campaignID uuid.UUID) map[string]int {
	type row struct {
		Status string
		N      int
	}
	var rows []row
	utils.DB.Model(&models.MarketingCampaignDelivery{}).
		Select("status, COUNT(*) AS n").Where("campaign_id = ?", campaignID).
		Group("status").Scan(&rows)
	out := map[string]int{"sent": 0, "failed": 0, "skipped": 0}
	for _, r := range rows {
		out[r.Status] = r.N
	}
	return out
}

// ---------------------------------------------------------------------------
// HTTP handlers
// ---------------------------------------------------------------------------

// MarketingPageRequired blocks the campaign API for a store whose Campaigns
// page is switched off in Developer Settings → Pages & Menus.
func MarketingPageRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		var storeID *uuid.UUID
		if id, ok := currentStoreID(c); ok {
			storeID = &id
		}
		if !pageFeatureEnabledForStore(storeID, marketingPageKey) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": "Campaigns are disabled for this store (Developer Settings → Pages & Menus)",
			})
			return
		}
		c.Next()
	}
}

// marketingStoreID is the campaign's store, falling back to the owner's store
// for rows saved without one.
func marketingStoreID(camp *models.MarketingCampaign) *uuid.UUID {
	if camp.StoreID != nil && *camp.StoreID != uuid.Nil {
		return camp.StoreID
	}
	if s, err := utils.FindStoreByOwnerUserID(utils.DB, camp.UserID); err == nil {
		return &s.ID
	}
	return nil
}

func loadMarketingCampaign(c *gin.Context) (models.MarketingCampaign, bool) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var camp models.MarketingCampaign
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, c.Param("id")).First(&camp).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Campaign not found"})
		return camp, false
	}
	return camp, true
}

func ListMarketingCampaigns(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	q := utils.DB.Where("user_id = ?", userID)
	if st := c.Query("status"); st != "" {
		q = q.Where("status = ?", st)
	}
	if ch := c.Query("channel"); ch != "" {
		q = q.Where("channels LIKE ?", "%"+ch+"%")
	}
	if s := strings.TrimSpace(c.Query("search")); s != "" {
		q = q.Where("LOWER(name) LIKE ?", "%"+strings.ToLower(s)+"%")
	}
	campaigns := make([]models.MarketingCampaign, 0)
	if err := q.Order("created_at DESC").Find(&campaigns).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load campaigns"})
		return
	}
	c.JSON(http.StatusOK, campaigns)
}

func GetMarketingStats(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var total, scheduled, completed int64
	base := func() *gorm.DB { return utils.DB.Model(&models.MarketingCampaign{}).Where("user_id = ?", userID) }
	base().Count(&total)
	base().Where("status = ?", mktStatusScheduled).Count(&scheduled)
	base().Where("status IN ?", []string{mktStatusSent, mktStatusPartial}).Count(&completed)

	type row struct {
		Channel string
		Status  string
		N       int
	}
	var rows []row
	utils.DB.Model(&models.MarketingCampaignDelivery{}).
		Select("channel, status, COUNT(*) AS n").Where("user_id = ?", userID).
		Group("channel, status").Scan(&rows)
	byChannel := map[string]map[string]int{}
	totals := map[string]int{"sent": 0, "failed": 0, "skipped": 0}
	for _, ch := range marketingChannelOrder {
		byChannel[ch] = map[string]int{"sent": 0, "failed": 0, "skipped": 0}
	}
	for _, r := range rows {
		if byChannel[r.Channel] == nil {
			byChannel[r.Channel] = map[string]int{}
		}
		byChannel[r.Channel][r.Status] = r.N
		totals[r.Status] += r.N
	}
	c.JSON(http.StatusOK, gin.H{
		"total_campaigns": total, "scheduled_campaigns": scheduled, "completed_campaigns": completed,
		"messages": totals, "by_channel": byChannel,
	})
}

func GetMarketingCampaign(c *gin.Context) {
	camp, ok := loadMarketingCampaign(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, camp)
}

// scheduleFromInput validates a requested schedule time (must be in the future).
func scheduleFromInput(at *time.Time) (*time.Time, error) {
	if at == nil {
		return nil, nil
	}
	if at.Before(time.Now().Add(-time.Minute)) {
		return nil, errors.New("Schedule time must be in the future")
	}
	// Stored in server-local time, like the time.Now() the scheduler compares
	// against: SQLite (desktop) compares timestamps as text.
	t := at.In(time.Local)
	return &t, nil
}

func CreateMarketingCampaign(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var in marketingCampaignInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request: " + err.Error()})
		return
	}
	camp := models.MarketingCampaign{ID: uuid.New(), UserID: userID, Status: mktStatusDraft}
	if id, ok := currentStoreID(c); ok {
		camp.StoreID = &id
	}
	if err := in.apply(&camp); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	at, err := scheduleFromInput(in.ScheduledAt)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if at != nil {
		if errs := marketingReadyErrors(&camp); len(errs) > 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": strings.Join(errs, "; "), "errors": errs})
			return
		}
		camp.ScheduledAt, camp.Status = at, mktStatusScheduled
	}
	if err := utils.DB.Create(&camp).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save campaign"})
		return
	}
	c.JSON(http.StatusCreated, camp)
}

func UpdateMarketingCampaign(c *gin.Context) {
	camp, ok := loadMarketingCampaign(c)
	if !ok {
		return
	}
	if camp.Status != mktStatusDraft && camp.Status != mktStatusScheduled {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Only draft or scheduled campaigns can be edited; duplicate it instead"})
		return
	}
	var in marketingCampaignInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request: " + err.Error()})
		return
	}
	if err := in.apply(&camp); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	at, err := scheduleFromInput(in.ScheduledAt)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if at != nil {
		if errs := marketingReadyErrors(&camp); len(errs) > 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": strings.Join(errs, "; "), "errors": errs})
			return
		}
		camp.ScheduledAt, camp.Status = at, mktStatusScheduled
	} else {
		camp.ScheduledAt, camp.Status = nil, mktStatusDraft
	}
	// Guard against the scheduler claiming it mid-edit.
	res := utils.DB.Model(&models.MarketingCampaign{}).
		Where("id = ? AND status IN ?", camp.ID, []string{mktStatusDraft, mktStatusScheduled}).
		Select("*").Omit("id", "user_id", "store_id", "created_at").Updates(&camp)
	if res.Error != nil || res.RowsAffected == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "Campaign started sending; refresh to see its status"})
		return
	}
	utils.DB.First(&camp, "id = ?", camp.ID)
	c.JSON(http.StatusOK, camp)
}

func DeleteMarketingCampaign(c *gin.Context) {
	camp, ok := loadMarketingCampaign(c)
	if !ok {
		return
	}
	if camp.Status == mktStatusSending {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Campaign is sending; wait for it to finish"})
		return
	}
	utils.DB.Where("campaign_id = ?", camp.ID).Delete(&models.MarketingCampaignDelivery{})
	if err := utils.DB.Delete(&camp).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete campaign"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Campaign deleted"})
}

// SendMarketingCampaign sends a draft or scheduled campaign now, in the
// background; poll the campaign for progress.
func SendMarketingCampaign(c *gin.Context) {
	camp, ok := loadMarketingCampaign(c)
	if !ok {
		return
	}
	if camp.Status != mktStatusDraft && camp.Status != mktStatusScheduled {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Campaign was already sent; duplicate it to send again"})
		return
	}
	if errs := marketingReadyErrors(&camp); len(errs) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": strings.Join(errs, "; "), "errors": errs})
		return
	}
	if !claimMarketingCampaign(camp.ID, mktStatusDraft, mktStatusScheduled) {
		c.JSON(http.StatusConflict, gin.H{"error": "Campaign is already sending"})
		return
	}
	go runMarketingCampaign(camp.ID)
	camp.Status = mktStatusSending
	c.JSON(http.StatusAccepted, camp)
}

// CancelMarketingSchedule moves a scheduled campaign back to draft.
func CancelMarketingSchedule(c *gin.Context) {
	camp, ok := loadMarketingCampaign(c)
	if !ok {
		return
	}
	res := utils.DB.Model(&models.MarketingCampaign{}).
		Where("id = ? AND status = ?", camp.ID, mktStatusScheduled).
		Updates(map[string]interface{}{"status": mktStatusDraft, "scheduled_at": nil})
	if res.RowsAffected == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Campaign is not scheduled"})
		return
	}
	utils.DB.First(&camp, "id = ?", camp.ID)
	c.JSON(http.StatusOK, camp)
}

// DuplicateMarketingCampaign copies a campaign's content into a new draft.
func DuplicateMarketingCampaign(c *gin.Context) {
	camp, ok := loadMarketingCampaign(c)
	if !ok {
		return
	}
	cp := camp
	cp.ID = uuid.New()
	cp.Name = camp.Name + " (copy)"
	cp.Status = mktStatusDraft
	cp.ScheduledAt, cp.SentAt = nil, nil
	cp.LastError = ""
	cp.TotalRecipients, cp.SentCount, cp.FailedCount, cp.SkippedCount = 0, 0, 0, 0
	cp.CreatedAt, cp.UpdatedAt = time.Time{}, time.Time{}
	cp.DeletedAt = gorm.DeletedAt{}
	if id, ok := currentStoreID(c); ok {
		cp.StoreID = &id
	}
	if err := utils.DB.Create(&cp).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to duplicate campaign"})
		return
	}
	c.JSON(http.StatusCreated, cp)
}

// GetMarketingDeliveries pages through a campaign's delivery log.
func GetMarketingDeliveries(c *gin.Context) {
	camp, ok := loadMarketingCampaign(c)
	if !ok {
		return
	}
	q := utils.DB.Model(&models.MarketingCampaignDelivery{}).Where("campaign_id = ?", camp.ID)
	if st := c.Query("status"); st != "" {
		q = q.Where("status = ?", st)
	}
	if ch := c.Query("channel"); ch != "" {
		q = q.Where("channel = ?", ch)
	}
	if s := strings.TrimSpace(c.Query("search")); s != "" {
		like := "%" + strings.ToLower(s) + "%"
		q = q.Where("LOWER(party_name) LIKE ? OR LOWER(recipient) LIKE ?", like, like)
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	perPage, _ := strconv.Atoi(c.DefaultQuery("per_page", "50"))
	if perPage < 1 || perPage > 500 {
		perPage = 50
	}
	var total int64
	q.Session(&gorm.Session{}).Count(&total)
	items := make([]models.MarketingCampaignDelivery, 0)
	q.Order("created_at ASC").Limit(perPage).Offset((page - 1) * perPage).Find(&items)

	type row struct {
		Channel string
		Status  string
		N       int
	}
	var rows []row
	utils.DB.Model(&models.MarketingCampaignDelivery{}).
		Select("channel, status, COUNT(*) AS n").Where("campaign_id = ?", camp.ID).
		Group("channel, status").Scan(&rows)
	summary := map[string]map[string]int{}
	for _, r := range rows {
		if summary[r.Channel] == nil {
			summary[r.Channel] = map[string]int{}
		}
		summary[r.Channel][r.Status] = r.N
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total, "page": page, "per_page": perPage, "summary": summary})
}

// TestMarketingMessage sends the (unsaved) campaign content on one channel to
// a single address, filled with sample values, so the store can check the
// template and formatting before sending to everyone.
func TestMarketingMessage(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var in struct {
		marketingCampaignInput
		Channel string `json:"channel"`
		To      string `json:"to"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}
	channel := strings.ToLower(strings.TrimSpace(in.Channel))
	if _, ok := marketingChannelLabels[channel]; !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Pick a channel to test"})
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		in.Name = "Test"
	}
	in.Channels = []string{channel}
	camp := models.MarketingCampaign{UserID: userID}
	if id, ok := currentStoreID(c); ok {
		camp.StoreID = &id
	}
	if err := in.marketingCampaignInput.apply(&camp); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if errs := marketingReadyErrors(&camp); len(errs) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": strings.Join(errs, "; "), "errors": errs})
		return
	}

	sender := newMarketingSender(&camp)
	raw := strings.TrimSpace(in.To)
	var to string
	switch channel {
	case mktChannelWhatsApp, mktChannelSMS:
		to = normalizeWhatsAppNumber(raw, sender.cc)
		if to == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Enter a valid mobile number"})
			return
		}
	case mktChannelEmail:
		if !validEmail(raw) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Enter a valid email address"})
			return
		}
		to = raw
	case mktChannelTelegram:
		to = raw
		if to == "" {
			// Default to the store owner's linked chats.
			tg, _ := loadTelegramSettings(userID)
			if chats := splitTelegramChatIDs(tg.OwnerChatIDs); len(chats) > 0 {
				to = chats[0]
			}
		}
		if to == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Enter a Telegram chat ID or link an owner chat in Settings → Telegram"})
			return
		}
	}

	sample := models.Party{Name: "Test Customer", LoyaltyPoints: 120, LoyaltyCash: 50}
	values := marketingValues(&camp, &sample, &sender.business)
	if _, err := sender.send(channel, to, values); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("Test %s message sent to %s", marketingChannelLabels[channel], to)})
}
