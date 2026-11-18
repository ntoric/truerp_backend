package controllers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"truerp/models"

	"github.com/google/uuid"
)

func TestInvoicePDFTokenRoundTrip(t *testing.T) {
	id := uuid.New()
	tok := signInvoicePDFToken(id, time.Now().Add(time.Hour))
	got, ok := verifyInvoicePDFToken(tok)
	if !ok || got != id {
		t.Fatalf("expected valid token for %s, got %s ok=%v", id, got, ok)
	}

	expired := signInvoicePDFToken(id, time.Now().Add(-time.Minute))
	if _, ok := verifyInvoicePDFToken(expired); ok {
		t.Fatal("expired token should be rejected")
	}

	tampered := []byte(tok)
	tampered[3] ^= 1
	if _, ok := verifyInvoicePDFToken(string(tampered)); ok {
		t.Fatal("tampered token should be rejected")
	}
}

func TestNormalizeWhatsAppNumber(t *testing.T) {
	cases := map[string]string{
		"98765 43210":     "919876543210",
		"+91 98765-43210": "919876543210",
		"09876543210":     "919876543210",
		"919876543210":    "919876543210",
		"0091 9876543210": "919876543210",
		"12345":           "",
		"":                "",
	}
	for in, want := range cases {
		if got := normalizeWhatsAppNumber(in, "91"); got != want {
			t.Errorf("normalizeWhatsAppNumber(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatINRAmount(t *testing.T) {
	cases := map[float64]string{
		0:          "0.00",
		999.5:      "999.50",
		1234:       "1,234.00",
		123456.75:  "1,23,456.75",
		12345678.9: "1,23,45,678.90",
	}
	for in, want := range cases {
		if got := formatINRAmount(in); got != want {
			t.Errorf("formatINRAmount(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestWhatsAppInvoiceValuesDefaultMapping(t *testing.T) {
	inv := &models.Invoice{
		InvoiceNumber:         "INV|001",
		TotalAmount:           1500,
		Date:                  time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC),
		LoyaltyPointsEarned:   15,
		LoyaltyPointsRedeemed: 0,
		Party:                 models.Party{Name: "", LoyaltyPoints: 120},
	}
	vals := whatsAppInvoiceValues(inv, &models.Business{Name: "My Store"}, models.WhatsAppInvoiceSettings{})
	var got []string
	for _, k := range strings.Split(defaultWhatsAppInvoiceFields, ",") {
		got = append(got, vals[k])
	}
	want := "INV|001|1,500.00|15-11-2026|15|120|0|My Store"
	if strings.Join(got, "|") != want {
		t.Fatalf("got %q, want %q", strings.Join(got, "|"), want)
	}
	if vals["customer_name"] != "Customer" {
		t.Fatalf("empty customer name should fall back, got %q", vals["customer_name"])
	}

	override := whatsAppInvoiceValues(inv, &models.Business{Name: "My Store"}, models.WhatsAppInvoiceSettings{StoreDisplayName: "Branch 2"})
	if override["store_name"] != "Branch 2" {
		t.Fatalf("store display name override ignored: %q", override["store_name"])
	}
}

func TestNormalizeWhatsAppVariableMapping(t *testing.T) {
	if got, err := normalizeWhatsAppVariableMapping(" invoice_number , store_name "); err != nil || got != "invoice_number,store_name" {
		t.Fatalf("got %q err=%v", got, err)
	}
	if _, err := normalizeWhatsAppVariableMapping("invoice_number,bogus"); err == nil {
		t.Fatal("expected error for unknown field")
	}
	if got, _ := normalizeWhatsAppVariableMapping(""); got != defaultWhatsAppInvoiceFields {
		t.Fatalf("empty mapping should use default, got %q", got)
	}
}

func TestCallWhatsAppAPICloud(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		if gotBody["to"] == "910000000000" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Invalid phone number","type":"OAuthException","code":131030}}`))
			return
		}
		_, _ = w.Write([]byte(`{"messaging_product":"whatsapp","contacts":[{"input":"919876543210","wa_id":"919876543210"}],"messages":[{"id":"wamid.abc123"}]}`))
	}))
	defer srv.Close()
	t.Setenv("WHATSAPP_CLOUD_API_BASE", srv.URL)

	payload := map[string]interface{}{
		"messaging_product": "whatsapp",
		"to":                "919876543210",
		"type":              "template",
		"template": map[string]interface{}{
			"name":     "purchase_summary",
			"language": map[string]interface{}{"code": "en"},
			"components": []map[string]interface{}{
				{"type": "body", "parameters": []map[string]interface{}{
					{"type": "text", "text": "a"},
					{"type": "text", "text": "b"},
				}},
			},
		},
	}
	msgID, _, err := callWhatsAppAPI(whatsAppProviderCloud, "12345", "TOKEN", payload)
	if err != nil || msgID != "wamid.abc123" {
		t.Fatalf("expected success, got id=%q err=%v", msgID, err)
	}
	if gotPath != "/12345/messages" {
		t.Fatalf("unexpected endpoint path: %s", gotPath)
	}
	if gotAuth != "Bearer TOKEN" {
		t.Fatalf("unexpected auth header: %s", gotAuth)
	}
	tmpl, _ := gotBody["template"].(map[string]interface{})
	if tmpl["name"] != "purchase_summary" {
		t.Fatalf("unexpected template payload: %v", tmpl)
	}

	payload["to"] = "910000000000"
	_, _, err = callWhatsAppAPI(whatsAppProviderCloud, "12345", "TOKEN", payload)
	if err == nil || !strings.Contains(err.Error(), "Invalid phone number") {
		t.Fatalf("expected gateway error, got %v", err)
	}
}

func TestCallWhatsAppAPIFast2SMS(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		switch gotBody["to"] {
		case "910000000000":
			// Fast2SMS-native error with HTTP 200.
			_, _ = w.Write([]byte(`{"return":false,"status_code":412,"message":"Invalid Authentication, Check Authorization Key"}`))
		case "910000000001":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"return":false,"message":["Template not approved"]}`))
		default:
			_, _ = w.Write([]byte(`{"messaging_product":"whatsapp","contacts":[{"input":"919876543210","wa_id":"919876543210"}],"messages":[{"id":"wamid.f2s","message_status":"accepted"}]}`))
		}
	}))
	defer srv.Close()
	t.Setenv("FAST2SMS_WHATSAPP_API_BASE", srv.URL+"/dev/whatsapp/v26.0")
	t.Setenv("WHATSAPP_CLOUD_API_BASE", "http://127.0.0.1:1") // must not be used

	payload := map[string]interface{}{
		"messaging_product": "whatsapp",
		"to":                "919876543210",
		"type":              "template",
		"template":          map[string]interface{}{"name": "purchase_summary", "language": map[string]interface{}{"code": "en"}},
	}
	msgID, _, err := callWhatsAppAPI(whatsAppProviderFast2SMS, "555", "F2SKEY", payload)
	if err != nil || msgID != "wamid.f2s" {
		t.Fatalf("expected success, got id=%q err=%v", msgID, err)
	}
	if gotPath != "/dev/whatsapp/v26.0/555/messages" {
		t.Fatalf("unexpected endpoint path: %s", gotPath)
	}
	if gotAuth != "F2SKEY" {
		t.Fatalf("Fast2SMS expects the bare API key, got auth header %q", gotAuth)
	}

	payload["to"] = "910000000000"
	if _, _, err = callWhatsAppAPI(whatsAppProviderFast2SMS, "555", "F2SKEY", payload); err == nil || !strings.Contains(err.Error(), "Invalid Authentication") {
		t.Fatalf("expected Fast2SMS return:false error, got %v", err)
	}
	payload["to"] = "910000000001"
	if _, _, err = callWhatsAppAPI(whatsAppProviderFast2SMS, "555", "F2SKEY", payload); err == nil || !strings.Contains(err.Error(), "Template not approved") {
		t.Fatalf("expected Fast2SMS array message error, got %v", err)
	}
}

func TestNormalizeWhatsAppProvider(t *testing.T) {
	cases := map[string]string{
		"":               whatsAppProviderCloud,
		"whatsapp_cloud": whatsAppProviderCloud,
		" Fast2SMS ":     whatsAppProviderFast2SMS,
		"Twilio":         whatsAppProviderTwilio,
		"unknown":        whatsAppProviderCloud,
	}
	for in, want := range cases {
		if got := normalizeWhatsAppProvider(in); got != want {
			t.Fatalf("normalizeWhatsAppProvider(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCallTwilioWhatsAppAPI(t *testing.T) {
	var gotPath, gotUser, gotPass string
	var gotForm map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotUser, gotPass, _ = r.BasicAuth()
		_ = r.ParseForm()
		gotForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		if r.PostForm.Get("To") == "whatsapp:+910000000000" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":21211,"message":"The 'To' number is not a valid phone number.","more_info":"https://www.twilio.com/docs/errors/21211","status":400}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"sid":"SM123","status":"queued","error_code":null,"error_message":null}`))
	}))
	defer srv.Close()
	t.Setenv("TWILIO_API_BASE", srv.URL+"/2010-04-01")

	sid, _, err := callTwilioWhatsAppAPI("AC1", "TOKEN", "+1 415 523 8886", "919876543210", "HXabc", []string{"INV-1", "abc.pdf"}, "91")
	if err != nil || sid != "SM123" {
		t.Fatalf("expected success, got sid=%q err=%v", sid, err)
	}
	if gotPath != "/2010-04-01/Accounts/AC1/Messages.json" {
		t.Fatalf("unexpected endpoint path: %s", gotPath)
	}
	if gotUser != "AC1" || gotPass != "TOKEN" {
		t.Fatalf("unexpected basic auth %q:%q", gotUser, gotPass)
	}
	if gotForm["From"][0] != "whatsapp:+14155238886" || gotForm["To"][0] != "whatsapp:+919876543210" {
		t.Fatalf("unexpected addresses: %v", gotForm)
	}
	if gotForm["ContentSid"][0] != "HXabc" {
		t.Fatalf("unexpected content sid: %v", gotForm["ContentSid"])
	}
	var cv map[string]string
	if err := json.Unmarshal([]byte(gotForm["ContentVariables"][0]), &cv); err != nil || cv["1"] != "INV-1" || cv["2"] != "abc.pdf" {
		t.Fatalf("unexpected content variables: %v err=%v", gotForm["ContentVariables"], err)
	}

	if _, _, err := callTwilioWhatsAppAPI("AC1", "TOKEN", "MGservice", "919876543210", "HXabc", nil, "91"); err != nil {
		t.Fatalf("messaging service send failed: %v", err)
	}
	if gotForm["MessagingServiceSid"][0] != "MGservice" || len(gotForm["From"]) != 0 {
		t.Fatalf("expected MessagingServiceSid and no From, got %v", gotForm)
	}

	_, _, err = callTwilioWhatsAppAPI("AC1", "TOKEN", "+14155238886", "910000000000", "HXabc", nil, "91")
	if err == nil || !strings.Contains(err.Error(), "not a valid phone number") || !strings.Contains(err.Error(), "21211") {
		t.Fatalf("expected Twilio error, got %v", err)
	}
	if strings.Contains(err.Error(), "TOKEN") {
		t.Fatal("auth token leaked into error")
	}
}

func TestWhatsAppInvoiceEnableErrorTwilio(t *testing.T) {
	s := models.WhatsAppInvoiceSettings{
		Provider:        whatsAppProviderTwilio,
		EncryptedAPIKey: "enc",
		PhoneNumberID:   "+14155238886",
		MessageID:       "HXabc",
		VariableMapping: "invoice_number,invoice_pdf_file",
		AttachPDF:       true,
		PublicBaseURL:   "https://api.example.com",
	}
	if msg := whatsAppInvoiceEnableError(s, false); !strings.Contains(msg, "Account SID") {
		t.Fatalf("expected Account SID error, got %q", msg)
	}
	s.AccountSID = "AC123"
	if msg := whatsAppInvoiceEnableError(s, false); msg != "" {
		t.Fatalf("expected valid settings, got %q", msg)
	}
	s.VariableMapping = "invoice_number"
	if msg := whatsAppInvoiceEnableError(s, false); !strings.Contains(msg, "Invoice PDF file") {
		t.Fatalf("expected PDF mapping error, got %q", msg)
	}
	s.AttachPDF = false
	s.MessageID = "purchase_summary"
	if msg := whatsAppInvoiceEnableError(s, false); !strings.Contains(msg, "HX") {
		t.Fatalf("expected Content SID error, got %q", msg)
	}
}

func TestWhatsAppActiveTemplate(t *testing.T) {
	s := models.WhatsAppInvoiceSettings{
		MessageID:                "purchase_summary",
		VariableMapping:          defaultWhatsAppInvoiceFields,
		NoLoyaltyVariableMapping: defaultWhatsAppNoLoyaltyFields,
	}
	// No alternate template configured: always the main one.
	if id, m, alt := whatsAppActiveTemplate(s, false); id != "purchase_summary" || m != defaultWhatsAppInvoiceFields || alt {
		t.Fatalf("got %q %q %v, want main template", id, m, alt)
	}
	s.NoLoyaltyMessageID = "purchase_summary_basic"
	if id, m, alt := whatsAppActiveTemplate(s, false); id != "purchase_summary_basic" || m != defaultWhatsAppNoLoyaltyFields || !alt {
		t.Fatalf("got %q %q %v, want no-loyalty template", id, m, alt)
	}
	if id, _, alt := whatsAppActiveTemplate(s, true); id != "purchase_summary" || alt {
		t.Fatalf("loyalty on: got %q %v, want main template", id, alt)
	}
}

func TestWhatsAppInvoiceEnableErrorNoLoyaltyTwilio(t *testing.T) {
	s := models.WhatsAppInvoiceSettings{
		Provider:                 whatsAppProviderTwilio,
		EncryptedAPIKey:          "x",
		AccountSID:               "AC123",
		PhoneNumberID:            "+14155238886",
		MessageID:                "HX123",
		VariableMapping:          defaultWhatsAppInvoiceFields,
		NoLoyaltyMessageID:       "purchase_basic",
		NoLoyaltyVariableMapping: defaultWhatsAppNoLoyaltyFields,
	}
	if msg := whatsAppInvoiceEnableError(s, false); !strings.Contains(msg, "without loyalty") {
		t.Fatalf("expected no-loyalty HX error, got %q", msg)
	}
	s.NoLoyaltyMessageID = "HX456"
	if msg := whatsAppInvoiceEnableError(s, false); msg != "" {
		t.Fatalf("expected valid settings, got %q", msg)
	}
}
