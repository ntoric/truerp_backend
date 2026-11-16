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
		"unknown":        whatsAppProviderCloud,
	}
	for in, want := range cases {
		if got := normalizeWhatsAppProvider(in); got != want {
			t.Fatalf("normalizeWhatsAppProvider(%q) = %q, want %q", in, got, want)
		}
	}
}
