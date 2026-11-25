package utils

import (
	"log"
	"strings"
	"truerp/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// backfillWhatsAppGatewayFromDeveloperSettings merges the old "WhatsApp
// Messaging" credentials (DeveloperSettings: Meta token + Phone Number ID, or
// Twilio SID + token + number) into the store's single WhatsApp connection
// (WhatsAppInvoiceSettings), which is what actually sends messages. A store
// that already has a connection key is left alone. Secrets are copied as
// ciphertext: both sides use utils.Encrypt. Runs once (data_repairs marker).
func backfillWhatsAppGatewayFromDeveloperSettings(db *gorm.DB) {
	if !db.Migrator().HasTable(&models.DeveloperSettings{}) || !db.Migrator().HasTable(&models.WhatsAppInvoiceSettings{}) {
		return
	}
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS data_repairs (
		name VARCHAR(200) PRIMARY KEY,
		applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`).Error; err != nil {
		log.Printf("backfillWhatsAppGateway: create marker table failed: %v", err)
		return
	}
	const marker = "whatsapp_messaging_into_gateway_v1"
	var applied int64
	if err := db.Raw(`SELECT COUNT(*) FROM data_repairs WHERE name = ?`, marker).Scan(&applied).Error; err != nil || applied > 0 {
		return
	}

	var all []models.DeveloperSettings
	if err := db.Find(&all).Error; err != nil {
		log.Printf("backfillWhatsAppGateway: load developer settings failed: %v", err)
		return
	}
	merged := 0
	for _, dev := range all {
		provider, secret, sender, sid := "", "", "", ""
		if strings.EqualFold(dev.WhatsAppProvider, "twilio") {
			provider, secret, sender, sid = "twilio", dev.EncryptedTwilioAuthToken, strings.TrimSpace(dev.TwilioPhoneNumber), strings.TrimSpace(dev.TwilioAccountSID)
			if sid == "" {
				continue
			}
		} else {
			provider, secret, sender = "whatsapp_cloud", dev.EncryptedWhatsAppAPIKey, strings.TrimSpace(dev.WhatsAppPhoneNumberID)
		}
		if secret == "" || sender == "" {
			continue
		}

		var gw models.WhatsAppInvoiceSettings
		err := db.Where("user_id = ?", dev.UserID).First(&gw).Error
		if err == nil {
			if gw.EncryptedAPIKey != "" {
				continue // store already has a working connection
			}
			if err := db.Model(&gw).Updates(map[string]interface{}{
				"provider": provider, "api_key": secret, "phone_number_id": sender, "account_sid": sid,
			}).Error; err != nil {
				log.Printf("backfillWhatsAppGateway: update %s failed: %v", dev.UserID, err)
				continue
			}
		} else {
			// Plain Create so column defaults (country code, PDF, send rules) apply.
			gw = models.WhatsAppInvoiceSettings{ID: uuid.New(), UserID: dev.UserID, Provider: provider,
				EncryptedAPIKey: secret, PhoneNumberID: sender, AccountSID: sid}
			if err := db.Create(&gw).Error; err != nil {
				log.Printf("backfillWhatsAppGateway: create %s failed: %v", dev.UserID, err)
				continue
			}
		}
		merged++
	}
	if merged > 0 {
		log.Printf("backfillWhatsAppGateway: merged WhatsApp messaging credentials into %d store connection(s)", merged)
	}
	db.Exec(`INSERT INTO data_repairs (name) VALUES (?)`, marker)
}
