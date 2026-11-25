package controllers

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestPartyStatementTokenRoundTrip(t *testing.T) {
	id := uuid.New()
	tok := signPartyStatementToken(id, "2026-10-01", "2026-10-31", time.Now().Add(time.Hour))
	gotID, from, to, ok := verifyPartyStatementToken(tok)
	if !ok || gotID != id || from != "2026-10-01" || to != "2026-10-31" {
		t.Fatalf("round trip = %v %s %s %v", gotID, from, to, ok)
	}

	// Tampering with the range must break the signature.
	raw := []byte(tok)
	raw[30] ^= 1
	if _, _, _, ok := verifyPartyStatementToken(string(raw)); ok {
		t.Fatal("tampered token verified")
	}
	expired := signPartyStatementToken(id, "2026-10-01", "2026-10-31", time.Now().Add(-time.Second))
	if _, _, _, ok := verifyPartyStatementToken(expired); ok {
		t.Fatal("expired token verified")
	}
	// An invoice token must not pass as a statement token.
	if _, _, _, ok := verifyPartyStatementToken(signInvoicePDFToken(id, time.Now().Add(time.Hour))); ok {
		t.Fatal("invoice token accepted as statement token")
	}
}

func TestFormatStatementBalance(t *testing.T) {
	cases := map[float64]string{
		125000.5: "1,25,000.50",
		0:        "0.00",
		-0.001:   "0.00",
		-500:     "500.00 Cr",
	}
	for in, want := range cases {
		if got := formatStatementBalance(in); got != want {
			t.Errorf("formatStatementBalance(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizePartyStatementMapping(t *testing.T) {
	if got, _ := normalizePartyStatementMapping(""); got != defaultPartyStatementMapping {
		t.Fatalf("empty mapping = %q", got)
	}
	if _, err := normalizePartyStatementMapping("customer_name,invoice_number"); err == nil {
		t.Fatal("invoice field accepted in statement mapping")
	}
}

// seedStatementFixture creates a customer with an opening balance, sales and
// payments before and inside October 2026, and a connected WhatsApp gateway
// pointing at srvURL.
func seedStatementFixture(t *testing.T, srvURL string) (uuid.UUID, models.Party) {
	t.Helper()
	db := openPartyLedgerTestDB(t)
	if err := db.AutoMigrate(&models.WhatsAppInvoiceSettings{}, &models.PartyStatementSettings{},
		&models.PartyStatementLog{}, &models.Business{}, &models.Store{}, &models.DeveloperSettings{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	userID := uuid.New()
	at := func(m time.Month, d, h int) time.Time { return time.Date(2026, m, d, h, 0, 0, 0, time.UTC) }
	party := models.Party{ID: uuid.New(), UserID: userID, Name: "Rahul Traders", Phone: "98765 43210",
		PartyType: "customer", OpeningBalance: 1000}
	mk := func(v interface{}) {
		t.Helper()
		if err := db.Create(v).Error; err != nil {
			t.Fatalf("create %T: %v", v, err)
		}
	}
	mk(&party)
	mk(&models.Business{ID: uuid.New(), UserID: userID, Name: "Your Store", Phone: "9000000000"})
	// Before the period: +2000 sale, -500 payment → previous balance 2500.
	mk(&models.Invoice{ID: uuid.New(), UserID: userID, PartyID: party.ID, InvoiceNumber: "INV-1",
		Date: at(9, 20, 0), CreatedAt: at(9, 20, 11), Status: "unpaid", TotalAmount: 2000})
	mk(&models.Payment{ID: uuid.New(), UserID: userID, PartyID: party.ID, PaymentInNumber: "PI-1",
		Date: at(9, 25, 0), CreatedAt: at(9, 25, 9), Mode: "cash", AmountReceived: 500})
	// In the period, same day: payment recorded after the sale.
	mk(&models.Payment{ID: uuid.New(), UserID: userID, PartyID: party.ID, PaymentInNumber: "PI-2",
		Date: at(10, 5, 0), CreatedAt: at(10, 5, 16), Mode: "upi", AmountReceived: 1500})
	mk(&models.Invoice{ID: uuid.New(), UserID: userID, PartyID: party.ID, InvoiceNumber: "INV-2",
		Date: at(10, 5, 0), CreatedAt: at(10, 5, 10), Status: "unpaid", TotalAmount: 3000})

	key, err := utils.Encrypt("test-token")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	mk(&models.WhatsAppInvoiceSettings{ID: uuid.New(), UserID: userID, Provider: whatsAppProviderCloud,
		PhoneNumberID: "111", EncryptedAPIKey: key, LanguageCode: "en", CountryCode: "91",
		PublicBaseURL: "https://api.example.test", PDFLinkTTLMinutes: 60})
	mk(&models.PartyStatementSettings{ID: uuid.New(), UserID: userID, DefaultPeriod: "previous_month",
		WhatsAppMessageID: "balance_reminder", WhatsAppVariableMapping: defaultPartyStatementMapping,
		WhatsAppAttachPDF: true})
	t.Setenv("WHATSAPP_CLOUD_API_BASE", srvURL)
	return userID, party
}

func TestSendPartyStatementWhatsAppEndToEnd(t *testing.T) {
	var got map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/111/messages" || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("unexpected request %s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		_, _ = w.Write([]byte(`{"messages":[{"id":"wamid.X"}]}`))
	}))
	defer srv.Close()

	userID, party := seedStatementFixture(t, srv.URL)
	entry, err := sendPartyStatement(userID, party.ID, partyStatementChannelWhatsApp, "2026-10-01", "2026-10-31", "manual", "", false)
	if err != nil {
		t.Fatalf("send: %v (log %+v)", err, entry)
	}
	if entry.Status != "sent" || entry.RequestID != "wamid.X" || entry.Recipient != "919876543210" {
		t.Fatalf("log = %+v", entry)
	}
	if entry.OpeningBalance != 2500 || entry.ClosingBalance != 4000 {
		t.Fatalf("balances = %v → %v, want 2500 → 4000", entry.OpeningBalance, entry.ClosingBalance)
	}

	tpl := got["template"].(map[string]interface{})
	if tpl["name"] != "balance_reminder" {
		t.Fatalf("template name = %v", tpl["name"])
	}
	comps := tpl["components"].([]interface{})
	header := comps[0].(map[string]interface{})["parameters"].([]interface{})[0].(map[string]interface{})
	link := header["document"].(map[string]interface{})["link"].(string)
	if !strings.HasPrefix(link, "https://api.example.test"+publicPartyStatementPDFPath) {
		t.Fatalf("document link = %s", link)
	}
	var params []string
	for _, p := range comps[1].(map[string]interface{})["parameters"].([]interface{}) {
		params = append(params, p.(map[string]interface{})["text"].(string))
	}
	want := []string{"Rahul Traders", "Your Store", "01-10-2026", "31-10-2026", "2,500.00", "4,000.00"}
	if strings.Join(params, "|") != strings.Join(want, "|") {
		t.Fatalf("body params = %v, want %v", params, want)
	}

	// The signed link serves the statement PDF without auth.
	token := strings.TrimPrefix(link, "https://api.example.test"+publicPartyStatementPDFPath)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("GET", "/x", nil)
	c.Params = gin.Params{{Key: "token", Value: token}}
	PublicPartyStatementPDF(c)
	if rec.Code != http.StatusOK || !bytes.HasPrefix(rec.Body.Bytes(), []byte("%PDF")) {
		t.Fatalf("public pdf: status %d, body %.60q", rec.Code, rec.Body.String())
	}

	var count int64
	utils.DB.Model(&models.PartyStatementLog{}).Where("party_id = ? AND status = ?", party.ID, "sent").Count(&count)
	if count != 1 {
		t.Fatalf("sent log rows = %d", count)
	}
}

func TestSendPartyStatementSkipsZeroBalanceInBulk(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("gateway must not be called for a zero balance")
	}))
	defer srv.Close()
	userID, _ := seedStatementFixture(t, srv.URL)
	settled := models.Party{ID: uuid.New(), UserID: userID, Name: "Settled", Phone: "9876500000", PartyType: "customer"}
	if err := utils.DB.Create(&settled).Error; err != nil {
		t.Fatal(err)
	}
	entry, err := sendPartyStatement(userID, settled.ID, partyStatementChannelWhatsApp, "2026-10-01", "2026-10-31", "bulk", "", true)
	if entry != nil || err != nil {
		t.Fatalf("zero balance: entry=%+v err=%v", entry, err)
	}
}

func TestPartyStatementPDFOrderAndTimes(t *testing.T) {
	userID, party := seedStatementFixture(t, "http://127.0.0.1:1")
	ledger, err := loadPartyLedger(userID, party.ID, "2026-10-01", "2026-10-31")
	if err != nil {
		t.Fatal(err)
	}
	// opening, INV-2 (10:00), PI-2 (16:00), closing — same date, time order.
	var refs []string
	for _, e := range ledger.Entries[1 : len(ledger.Entries)-1] {
		refs = append(refs, e.RefNumber)
	}
	if strings.Join(refs, ",") != "INV-2,PI-2" {
		t.Fatalf("order = %v", refs)
	}
	pdfBytes, _, err := renderPartyStatementPDF(userID, party.ID, "2026-10-01", "2026-10-31")
	if err != nil || !bytes.HasPrefix(pdfBytes, []byte("%PDF")) {
		t.Fatalf("render: %v", err)
	}
}

func TestPartyStatementExclusions(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Write([]byte(`{"messages":[{"id":"wamid.1"}]}`))
	}))
	defer srv.Close()
	userID, party := seedStatementFixture(t, srv.URL)
	if err := utils.DB.AutoMigrate(&models.PartyStatementExclusion{}); err != nil {
		t.Fatal(err)
	}
	other := models.Party{ID: uuid.New(), UserID: userID, Name: "Other", Phone: "9876500001", PartyType: "customer", OpeningBalance: 50}
	utils.DB.Create(&other)

	call := func(h gin.HandlerFunc, method, body string, params gin.Params) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(method, "/x", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Params = params
		c.Set("user_id", userID)
		h(c)
		return rec
	}
	// Excluding twice is harmless.
	for i := 0; i < 2; i++ {
		if rec := call(AddPartyStatementExclusions, "POST", `{"party_ids":["`+party.ID.String()+`"]}`, nil); rec.Code != http.StatusOK {
			t.Fatalf("add: %d %s", rec.Code, rec.Body.String())
		}
	}
	rec := call(ListPartyStatementExclusions, "GET", "", nil)
	var list []partyStatementExclusionView
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 || list[0].Name != "Rahul Traders" {
		t.Fatalf("list = %s", rec.Body.String())
	}

	res := runPartyStatementBatch(userID, []uuid.UUID{party.ID, other.ID}, partyStatementChannelWhatsApp,
		"2026-10-01", "2026-10-31", "scheduled", true)
	if res.Excluded != 1 || res.Sent != 1 || atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("batch = %+v calls=%d", res, calls)
	}
	if !strings.Contains(res.summary(), "1 excluded") {
		t.Errorf("summary = %q", res.summary())
	}

	if rec := call(RemovePartyStatementExclusion, "DELETE", "", gin.Params{{Key: "pid", Value: party.ID.String()}}); rec.Code != http.StatusOK {
		t.Fatalf("remove: %d", rec.Code)
	}
	if ids, n := withoutExcludedStatementParties(userID, []uuid.UUID{party.ID}); n != 0 || len(ids) != 1 {
		t.Errorf("after remove: %v %d", ids, n)
	}
}
