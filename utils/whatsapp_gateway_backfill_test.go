package utils

import (
	"testing"
	"truerp/models"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestBackfillWhatsAppGatewayFromDeveloperSettings(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:wa_backfill?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.DeveloperSettings{}, &models.WhatsAppInvoiceSettings{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	metaStore, twilioStore, connectedStore, emptyStore := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, d := range []models.DeveloperSettings{
		{ID: uuid.New(), UserID: metaStore, WhatsAppProvider: "meta", EncryptedWhatsAppAPIKey: "enc-meta", WhatsAppPhoneNumberID: "111"},
		{ID: uuid.New(), UserID: twilioStore, WhatsAppProvider: "twilio", TwilioAccountSID: "AC1", EncryptedTwilioAuthToken: "enc-tw", TwilioPhoneNumber: "+14155238886"},
		{ID: uuid.New(), UserID: connectedStore, WhatsAppProvider: "meta", EncryptedWhatsAppAPIKey: "enc-old", WhatsAppPhoneNumberID: "222"},
		{ID: uuid.New(), UserID: emptyStore, WhatsAppProvider: "meta"},
	} {
		if err := db.Create(&d).Error; err != nil {
			t.Fatal(err)
		}
	}
	// This store already has a working connection: it must not be overwritten.
	if err := db.Create(&models.WhatsAppInvoiceSettings{ID: uuid.New(), UserID: connectedStore,
		Provider: "fast2sms", EncryptedAPIKey: "enc-f2s", PhoneNumberID: "333"}).Error; err != nil {
		t.Fatal(err)
	}

	backfillWhatsAppGatewayFromDeveloperSettings(db)

	get := func(id uuid.UUID) (models.WhatsAppInvoiceSettings, bool) {
		var s models.WhatsAppInvoiceSettings
		return s, db.Where("user_id = ?", id).First(&s).Error == nil
	}
	if s, ok := get(metaStore); !ok || s.Provider != "whatsapp_cloud" || s.EncryptedAPIKey != "enc-meta" || s.PhoneNumberID != "111" || !s.AttachPDF || s.CountryCode != "91" {
		t.Fatalf("meta store = %+v (found %v)", s, ok)
	}
	if s, ok := get(twilioStore); !ok || s.Provider != "twilio" || s.EncryptedAPIKey != "enc-tw" || s.AccountSID != "AC1" || s.PhoneNumberID != "+14155238886" {
		t.Fatalf("twilio store = %+v (found %v)", s, ok)
	}
	if s, _ := get(connectedStore); s.Provider != "fast2sms" || s.EncryptedAPIKey != "enc-f2s" {
		t.Fatalf("existing connection overwritten: %+v", s)
	}
	if _, ok := get(emptyStore); ok {
		t.Fatal("created a connection for a store with no WhatsApp credentials")
	}

	// Runs once: a later change to developer settings is not re-copied.
	db.Model(&models.WhatsAppInvoiceSettings{}).Where("user_id = ?", metaStore).Update("api_key", "")
	backfillWhatsAppGatewayFromDeveloperSettings(db)
	if s, _ := get(metaStore); s.EncryptedAPIKey != "" {
		t.Fatal("backfill ran twice")
	}
}
