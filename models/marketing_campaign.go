package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// MarketingCampaign is a store's multi-channel promotional campaign (offers,
// festival sales, announcements…) sent to its parties over WhatsApp,
// Telegram, email and SMS using the store's existing integrations.
//
// Rows are keyed by UserID, the store owner resolved from X-Store-ID, so a
// campaign belongs to exactly one store. StoreID is kept for reference.
type MarketingCampaign struct {
	ID      uuid.UUID  `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID  uuid.UUID  `json:"user_id" gorm:"type:uuid;not null;index"`
	StoreID *uuid.UUID `json:"store_id,omitempty" gorm:"type:uuid;index"`

	Name string `json:"name" gorm:"not null"`
	// Type: promotion, offer, festival, new_arrival, loyalty, reengagement, announcement.
	Type string `json:"type" gorm:"default:'promotion'"`
	// Channels is a comma-separated subset of whatsapp,telegram,email,sms.
	Channels string `json:"channels"`

	// Offer details, available to every message as {{offer_title}},
	// {{offer_code}}, {{discount}}, {{valid_until}} and {{link}}.
	OfferTitle string `json:"offer_title"`
	OfferCode  string `json:"offer_code"`
	Discount   string `json:"discount"`
	ValidUntil string `json:"valid_until"` // YYYY-MM-DD (date only, no timezone shift)
	Link       string `json:"link"`

	// Message is the free text sent on Telegram and SMS (placeholders allowed).
	Message string `json:"message" gorm:"type:text"`
	// SMSMessage overrides Message for SMS when set (SMS is kept short).
	SMSMessage   string `json:"sms_message" gorm:"type:text"`
	EmailSubject string `json:"email_subject"`
	// EmailBody is HTML; when empty Message is sent with line breaks kept.
	EmailBody string `json:"email_body" gorm:"type:text"`

	// WhatsApp business-initiated messages must use an approved template.
	// WATemplateName is the template name (Meta/Fast2SMS) or Content SID (Twilio).
	WATemplateName string `json:"wa_template_name"`
	WALanguage     string `json:"wa_language" gorm:"default:'en'"`
	// WAVariables is a JSON array; entry i fills body variable {{i+1}} and may
	// mix placeholders and literal text, e.g. ["{{customer_name}}", "20% off"].
	WAVariables string `json:"wa_variables" gorm:"type:text"`
	// ImageURL (public https) is the WhatsApp template's Image header, the
	// Telegram photo and the banner at the top of the email. Optional.
	ImageURL string `json:"image_url"`

	// Audience: all_customers, all_vendors, all_parties, category,
	// loyalty_members, specific.
	AudienceType     string `json:"audience_type" gorm:"default:'all_customers'"`
	AudienceCategory string `json:"audience_category"`
	// AudiencePartyIDs is a JSON array of party UUIDs (audience_type=specific).
	AudiencePartyIDs string `json:"audience_party_ids" gorm:"type:text"`

	// Status: draft, scheduled, sending, sent, partial, failed.
	Status      string     `json:"status" gorm:"default:'draft';index"`
	ScheduledAt *time.Time `json:"scheduled_at" gorm:"index"`
	SentAt      *time.Time `json:"sent_at"`
	LastError   string     `json:"last_error" gorm:"type:text"`

	TotalRecipients int `json:"total_recipients" gorm:"default:0"`
	SentCount       int `json:"sent_count" gorm:"default:0"`
	FailedCount     int `json:"failed_count" gorm:"default:0"`
	SkippedCount    int `json:"skipped_count" gorm:"default:0"`

	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

// MarketingCampaignDelivery records one send attempt of a campaign to one
// party on one channel.
type MarketingCampaignDelivery struct {
	ID         uuid.UUID  `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	CampaignID uuid.UUID  `json:"campaign_id" gorm:"type:uuid;not null;index"`
	UserID     uuid.UUID  `json:"user_id" gorm:"type:uuid;not null;index"`
	PartyID    *uuid.UUID `json:"party_id,omitempty" gorm:"type:uuid"`
	PartyName  string     `json:"party_name"`
	Channel    string     `json:"channel" gorm:"index"`
	Recipient  string     `json:"recipient"`
	// Status: sent, failed, skipped.
	Status    string    `json:"status" gorm:"index"`
	Error     string    `json:"error" gorm:"type:text"`
	MessageID string    `json:"message_id"`
	CreatedAt time.Time `json:"created_at"`
}
