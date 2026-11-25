package controllers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// fakeTelegram is an in-process stand-in for api.telegram.org.
type fakeTelegram struct {
	mu        sync.Mutex
	updates   []map[string]interface{}
	messages  []map[string]interface{} // sendMessage payloads
	documents []map[string]string      // sendDocument form fields (+ "filename")
	server    *httptest.Server
}

func newFakeTelegram(t *testing.T) *fakeTelegram {
	t.Helper()
	f := &fakeTelegram{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		if !strings.HasPrefix(r.URL.Path, "/botGOODTOKEN/") {
			w.Write([]byte(`{"ok":false,"description":"Unauthorized"}`))
			return
		}
		switch method {
		case "getMe":
			w.Write([]byte(`{"ok":true,"result":{"id":1,"username":"shop_bot","first_name":"Shop"}}`))
		case "getUpdates":
			body, _ := json.Marshal(map[string]interface{}{"ok": true, "result": f.updates})
			f.updates = nil
			w.Write(body)
		case "sendMessage":
			var p map[string]interface{}
			json.NewDecoder(r.Body).Decode(&p)
			f.messages = append(f.messages, p)
			w.Write([]byte(`{"ok":true,"result":{}}`))
		case "sendDocument":
			if err := r.ParseMultipartForm(10 << 20); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			fields := map[string]string{}
			for k, v := range r.MultipartForm.Value {
				fields[k] = v[0]
			}
			if fh := r.MultipartForm.File["document"]; len(fh) > 0 {
				fields["filename"] = fh[0].Filename
				file, _ := fh[0].Open()
				data, _ := io.ReadAll(file)
				fields["size"] = string(rune('0' + min(len(data), 9)))
			}
			f.documents = append(f.documents, fields)
			w.Write([]byte(`{"ok":true,"result":{}}`))
		default:
			w.Write([]byte(`{"ok":false,"description":"unknown method"}`))
		}
	}))
	prevBase := telegramAPIBase
	telegramAPIBase = f.server.URL
	t.Cleanup(func() {
		telegramAPIBase = prevBase
		f.server.Close()
	})
	return f
}

func (f *fakeTelegram) queueStart(updateID, chatID int64, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, map[string]interface{}{
		"update_id": updateID,
		"message":   map[string]interface{}{"text": text, "chat": map[string]interface{}{"id": chatID}},
	})
}

func setupTelegramTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Business{}, &models.Party{}, &models.Invoice{}, &models.InvoiceItem{}, &models.Product{},
		&models.InvoiceSettings{}, &models.PrintSettings{}, &models.Payment{}, &models.PaymentOut{},
		&models.SalesReturn{}, &models.PurchaseBill{}, &models.PurchaseReturn{}, &models.CreditNote{},
		&models.DebitNote{}, &models.TelegramSettings{}, &models.TelegramMessageLog{},
		&models.PartyStatementLog{}, &models.Store{}, &models.User{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	prev := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = prev })
	return db
}

func connectTestBot(t *testing.T, db *gorm.DB, userID uuid.UUID, enabled bool, ownerChats string) models.TelegramSettings {
	t.Helper()
	enc, err := utils.Encrypt("GOODTOKEN")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	s := defaultTelegramSettings(userID)
	s.ID = uuid.New()
	s.EncryptedBotToken = enc
	s.BotUsername = "shop_bot"
	if err := db.Create(&s).Error; err != nil {
		t.Fatalf("settings: %v", err)
	}
	db.Model(&s).Updates(map[string]interface{}{"enabled": enabled, "owner_chat_ids": ownerChats})
	s, _ = loadTelegramSettings(userID)
	return s
}

func TestTelegramLinkCodeRoundTrip(t *testing.T) {
	id := uuid.New()
	code := telegramLinkCode(telegramPartyPrefix, id)
	if len(code) > 64 || strings.ContainsAny(code, "+/=") {
		t.Fatalf("code %q is not a valid Telegram start parameter", code)
	}
	prefix, got, ok := parseTelegramLinkCode(code)
	if !ok || prefix != telegramPartyPrefix || got != id {
		t.Fatalf("round trip failed: %q %v %v", prefix, got, ok)
	}
	// A party code must not be accepted as an owner code, and tampering fails.
	if _, _, ok := parseTelegramLinkCode(telegramOwnerPrefix + code[1:]); ok {
		t.Fatalf("party code accepted with owner prefix")
	}
	tampered := code[:len(code)-1] + map[bool]string{true: "A", false: "B"}[code[len(code)-1] != 'A']
	if _, _, ok := parseTelegramLinkCode(tampered); ok {
		t.Fatalf("tampered code accepted")
	}
}

func TestTelegramPollerLinksPartyAndOwner(t *testing.T) {
	db := setupTelegramTestDB(t)
	fake := newFakeTelegram(t)
	userID, otherUser := uuid.New(), uuid.New()
	partyID, foreignParty := uuid.New(), uuid.New()
	db.Create(&models.Party{ID: partyID, UserID: userID, Name: "Asha", PartyType: "customer"})
	db.Create(&models.Party{ID: foreignParty, UserID: otherUser, Name: "Other store's", PartyType: "customer"})
	s := connectTestBot(t, db, userID, false, "")

	fake.queueStart(10, 111, "/start "+telegramLinkCode(telegramPartyPrefix, partyID))
	fake.queueStart(11, 222, "/start "+telegramLinkCode(telegramOwnerPrefix, userID))
	fake.queueStart(12, 333, "/start "+telegramLinkCode(telegramPartyPrefix, foreignParty)) // another store's party
	fake.queueStart(13, 444, "/start garbage")
	pollTelegramBot(s)

	var p models.Party
	db.First(&p, "id = ?", partyID)
	if p.TelegramChatID != "111" || p.TelegramLinkedAt == nil {
		t.Fatalf("party not linked: %+v", p)
	}
	var foreign models.Party
	if err := db.First(&foreign, "id = ?", foreignParty).Error; err != nil {
		t.Fatalf("load foreign party: %v", err)
	}
	if foreign.TelegramChatID != "" {
		t.Fatalf("another store's party was linked through this bot")
	}
	s, _ = loadTelegramSettings(userID)
	if s.OwnerChatIDs != "222" || s.LastUpdateID != 13 {
		t.Fatalf("owner chats %q last update %d", s.OwnerChatIDs, s.LastUpdateID)
	}
	if len(fake.messages) != 4 {
		t.Fatalf("expected a reply to every /start, got %d", len(fake.messages))
	}
	if txt, _ := fake.messages[2]["text"].(string); !strings.Contains(txt, "not valid") {
		t.Fatalf("foreign party link should be rejected, reply: %q", txt)
	}
}

func TestSendInvoiceTelegram(t *testing.T) {
	db := setupTelegramTestDB(t)
	fake := newFakeTelegram(t)
	userID, partyID := uuid.New(), uuid.New()
	db.Create(&models.Party{ID: partyID, UserID: userID, Name: "Asha", PartyType: "customer"})
	inv := models.Invoice{ID: uuid.New(), UserID: userID, PartyID: partyID, InvoiceNumber: "INV-7",
		Date: time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC), Status: "sent", TotalAmount: 1500, AmountPaid: 500}
	db.Create(&inv)

	// Not connected → failed with a clear reason.
	if _, err := sendInvoiceTelegram(userID, inv.ID, "manual", "", true); err == nil {
		t.Fatalf("expected error without a bot")
	}
	connectTestBot(t, db, userID, true, "999")

	// Connected but customer not linked → skipped.
	entry, err := sendInvoiceTelegram(userID, inv.ID, "manual", "", true)
	if err == nil || entry.Status != "skipped" {
		t.Fatalf("expected skipped for unlinked customer, got %+v %v", entry, err)
	}

	db.Model(&models.Party{}).Where("id = ?", partyID).Update("telegram_chat_id", "111")
	entry, err = sendInvoiceTelegram(userID, inv.ID, "manual", "", true)
	if err != nil || entry.Status != "sent" || entry.ChatID != "111" {
		t.Fatalf("send failed: %+v %v", entry, err)
	}
	doc := fake.documents[len(fake.documents)-1]
	if doc["chat_id"] != "111" || !strings.HasSuffix(doc["filename"], ".pdf") ||
		!strings.Contains(doc["caption"], "INV-7") || !strings.Contains(doc["caption"], "Balance due") {
		t.Fatalf("unexpected document: %+v", doc)
	}

	if n := sendInvoiceTelegramOwnerAlert(userID, inv.ID); n != 1 {
		t.Fatalf("owner alert sent to %d chats, want 1", n)
	}
	if doc := fake.documents[len(fake.documents)-1]; doc["chat_id"] != "999" || !strings.Contains(doc["caption"], "New sale") {
		t.Fatalf("unexpected owner alert: %+v", doc)
	}
}

func TestTelegramStatementChannelAndOwnerDocument(t *testing.T) {
	db := setupTelegramTestDB(t)
	fake := newFakeTelegram(t)
	userID := uuid.New()

	if msg := partyStatementChannelReadyError(userID, partyStatementChannelTelegram); msg == "" {
		t.Fatalf("telegram statements should not be ready without a bot")
	}
	if _, err := sendTelegramOwnerDocument(userID, telegramKindDailyReport, "Daily", "manual", "r.pdf", []byte("%PDF"), "cap"); err == nil {
		t.Fatalf("expected error without a bot")
	}
	connectTestBot(t, db, userID, true, "5, 6")
	if msg := partyStatementChannelReadyError(userID, partyStatementChannelTelegram); msg != "" {
		t.Fatalf("telegram statements not ready: %s", msg)
	}
	n, err := sendTelegramOwnerDocument(userID, telegramKindDailyReport, "Daily", "scheduled", "r.pdf", []byte("%PDF"), "cap")
	if err != nil || n != 2 || len(fake.documents) != 2 {
		t.Fatalf("owner document: n=%d err=%v docs=%d", n, err, len(fake.documents))
	}
}

func TestPartyStatementTelegramMessage(t *testing.T) {
	db := setupTelegramTestDB(t)
	if err := db.AutoMigrate(&models.PartyStatementSettings{}, &models.WhatsAppInvoiceSettings{}, &models.DeveloperSettings{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	fake := newFakeTelegram(t)
	userID := uuid.New()
	connectTestBot(t, db, userID, true, "")
	db.Create(&models.Business{ID: uuid.New(), UserID: userID, Name: "Your Store"})
	linked := models.Party{ID: uuid.New(), UserID: userID, Name: "Asha", PartyType: "customer",
		OpeningBalance: 1250, TelegramChatID: "777"}
	unlinked := models.Party{ID: uuid.New(), UserID: userID, Name: "Ravi", PartyType: "customer", OpeningBalance: 10}
	db.Create(&linked)
	db.Create(&unlinked)
	db.Create(&models.PartyStatementSettings{ID: uuid.New(), UserID: userID, DefaultPeriod: "previous_month",
		TelegramMessage: "Hi {customer_name} <friend>,\n**Due ₹{closing_balance}** from {store_name}. {unknown}"})

	entry, err := sendPartyStatement(userID, linked.ID, partyStatementChannelTelegram, "2026-10-01", "2026-10-31", "manual", "", false)
	if err != nil || entry.Status != "sent" || entry.Recipient != "777" {
		t.Fatalf("send: entry=%+v err=%v", entry, err)
	}
	if len(fake.documents) != 1 {
		t.Fatalf("documents = %d", len(fake.documents))
	}
	doc := fake.documents[0]
	want := "Hi Asha &lt;friend&gt;,\n<b>Due ₹1,250.00</b> from Your Store. {unknown}"
	if doc["caption"] != want || doc["parse_mode"] != "HTML" || doc["chat_id"] != "777" {
		t.Errorf("caption = %q (parse_mode %q)\nwant      %q", doc["caption"], doc["parse_mode"], want)
	}

	// Unlinked parties are skipped, not failed.
	if entry, _ := sendPartyStatement(userID, unlinked.ID, partyStatementChannelTelegram, "2026-10-01", "2026-10-31", "bulk", "", false); entry == nil || entry.Status != "skipped" {
		t.Errorf("unlinked party: %+v", entry)
	}

	// Longer than a caption: text message first, then the PDF without caption.
	db.Model(&models.PartyStatementSettings{}).Where("user_id = ?", userID).
		Update("telegram_message", strings.Repeat("x", telegramCaptionLimit+10))
	fake.documents, fake.messages = nil, nil
	if entry, err := sendPartyStatement(userID, linked.ID, partyStatementChannelTelegram, "2026-10-01", "2026-10-31", "manual", "", false); err != nil || entry.Status != "sent" {
		t.Fatalf("long send: %+v %v", entry, err)
	}
	if len(fake.messages) != 1 || len(fake.documents) != 1 || fake.documents[0]["caption"] != "" {
		t.Errorf("long message: messages=%d documents=%d caption=%q", len(fake.messages), len(fake.documents), fake.documents[0]["caption"])
	}
}

func TestRenderTelegramStatementMessageDefault(t *testing.T) {
	got := renderTelegramStatementMessage("", map[string]string{"customer_name": "A", "store_name": "S",
		"from_date": "01-10-2026", "to_date": "31-10-2026", "opening_balance": "1.00", "closing_balance": "2.00"})
	if !strings.HasPrefix(got, "Hi A,\nHere is a reminder from S.") || !strings.Contains(got, "Current Balance : ₹2.00") {
		t.Errorf("default render = %q", got)
	}
	if got := renderTelegramStatementMessage("a ** b", nil); got != "a ** b" {
		t.Errorf("unpaired bold = %q", got)
	}
}
