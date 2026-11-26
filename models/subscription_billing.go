package models

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SubscriptionBillingSettings is the single platform-wide profile used on
// subscription invoices and payment receipts: who issues them, how they look
// and how they are numbered. Edited by the super admin.
type SubscriptionBillingSettings struct {
	ID uuid.UUID `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`

	// Issuer ("Billed by").
	IssuerName string `json:"issuer_name"`
	Address    string `json:"address" gorm:"type:text"`
	City       string `json:"city"`
	State      string `json:"state"`
	StateCode  string `json:"state_code"` // GST state code; drives CGST+SGST vs IGST
	Pincode    string `json:"pincode"`
	GSTIN      string `json:"gstin"`
	PAN        string `json:"pan"`
	Email      string `json:"email"`
	Phone      string `json:"phone"`
	Website    string `json:"website"`

	// Branding. LogoData is a data: URL (resized client-side) so the logo
	// works with every storage backend and in unsaved previews.
	LogoData     string `json:"logo_data" gorm:"type:text"`
	AccentColor  string `json:"accent_color"`
	HighlightCol string `json:"highlight_color"`

	// Numbering: PREFIX-YYYY-00001, restarting each calendar year.
	InvoicePrefix string `json:"invoice_prefix"`
	ReceiptPrefix string `json:"receipt_prefix"`
	NumberPadding int    `json:"number_padding"`

	// Tax. Plan prices are tax-inclusive; the invoice breaks the tax out.
	TaxRate  float64 `json:"tax_rate"`
	TaxLabel string  `json:"tax_label"`
	SACCode  string  `json:"sac_code"`

	// Invoice content.
	InvoiceTitle       string `json:"invoice_title"` // blank = "Tax Invoice" with tax, else "Invoice"
	ShowAmountInWords  bool   `json:"show_amount_in_words"`
	ShowPaymentHistory bool   `json:"show_payment_history"`
	ShowBankDetails    bool   `json:"show_bank_details"`
	BankName           string `json:"bank_name"`
	AccountName        string `json:"account_name"`
	AccountNumber      string `json:"account_number"`
	IFSC               string `json:"ifsc"`
	UPIID              string `json:"upi_id"`
	InvoiceNotes       string `json:"invoice_notes" gorm:"type:text"`
	InvoiceTerms       string `json:"invoice_terms" gorm:"type:text"`

	// Receipt content.
	ReceiptTitle       string `json:"receipt_title"`
	ShowReceiptBalance bool   `json:"show_receipt_balance"`
	ReceiptNotes       string `json:"receipt_notes" gorm:"type:text"`

	// Shared.
	ShowSignature bool   `json:"show_signature"`
	SignatoryName string `json:"signatory_name"`
	FooterText    string `json:"footer_text"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// DefaultSubscriptionBillingSettings is used until the super admin saves a
// profile, and fills blanks in a saved one.
func DefaultSubscriptionBillingSettings() SubscriptionBillingSettings {
	return SubscriptionBillingSettings{
		IssuerName:         "TruERP",
		AccentColor:        "#1E1B4B",
		HighlightCol:       "#C9973F",
		InvoicePrefix:      "SINV",
		ReceiptPrefix:      "SRCPT",
		NumberPadding:      5,
		TaxLabel:           "GST",
		ReceiptTitle:       "Payment Receipt",
		ShowAmountInWords:  true,
		ShowPaymentHistory: true,
		ShowReceiptBalance: true,
		ShowSignature:      true,
		FooterText:         "Thank you for your business.",
	}
}

// LoadSubscriptionBillingSettings returns the saved profile, or the defaults.
// Blank required fields fall back to their defaults.
func LoadSubscriptionBillingSettings(tx *gorm.DB) SubscriptionBillingSettings {
	def := DefaultSubscriptionBillingSettings()
	var s SubscriptionBillingSettings
	if err := tx.Order("created_at ASC").First(&s).Error; err != nil {
		return def
	}
	if strings.TrimSpace(s.IssuerName) == "" {
		s.IssuerName = def.IssuerName
	}
	if strings.TrimSpace(s.InvoicePrefix) == "" {
		s.InvoicePrefix = def.InvoicePrefix
	}
	if strings.TrimSpace(s.ReceiptPrefix) == "" {
		s.ReceiptPrefix = def.ReceiptPrefix
	}
	if s.NumberPadding < 3 || s.NumberPadding > 8 {
		s.NumberPadding = def.NumberPadding
	}
	if strings.TrimSpace(s.AccentColor) == "" {
		s.AccentColor = def.AccentColor
	}
	if strings.TrimSpace(s.HighlightCol) == "" {
		s.HighlightCol = def.HighlightCol
	}
	if strings.TrimSpace(s.TaxLabel) == "" {
		s.TaxLabel = def.TaxLabel
	}
	if strings.TrimSpace(s.ReceiptTitle) == "" {
		s.ReceiptTitle = def.ReceiptTitle
	}
	return s
}

// SubscriptionDocCounter holds the last number issued per (kind, prefix, year).
type SubscriptionDocCounter struct {
	Key        string `gorm:"primaryKey"`
	LastNumber int    `gorm:"not null;default:0"`
}

// NextSubscriptionDocNumber issues the next invoice ("invoice") or receipt
// ("receipt") number for the year of date (YYYY-MM-DD). Must run inside the
// creating transaction so a rolled-back create does not burn a number.
func NextSubscriptionDocNumber(tx *gorm.DB, kind, date string) (string, error) {
	settings := LoadSubscriptionBillingSettings(tx)
	prefix := settings.InvoicePrefix
	if kind == "receipt" {
		prefix = settings.ReceiptPrefix
	}
	prefix = strings.ToUpper(strings.TrimSpace(prefix))
	year := time.Now().Format("2006")
	if len(date) >= 4 {
		year = date[:4]
	}
	key := kind + "|" + prefix + "|" + year
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).
		Create(&SubscriptionDocCounter{Key: key}).Error; err != nil {
		return "", err
	}
	if err := tx.Model(&SubscriptionDocCounter{}).Where("key = ?", key).
		UpdateColumn("last_number", gorm.Expr("last_number + 1")).Error; err != nil {
		return "", err
	}
	var c SubscriptionDocCounter
	if err := tx.First(&c, "key = ?", key).Error; err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%s-%0*d", prefix, year, settings.NumberPadding, c.LastNumber), nil
}

// BeforeCreate numbers the cycle's invoice.
func (c *SubscriptionCycle) BeforeCreate(tx *gorm.DB) error {
	if c.InvoiceDate == "" {
		c.InvoiceDate = c.PeriodStart
	}
	if c.InvoiceNumber != "" {
		return nil
	}
	n, err := NextSubscriptionDocNumber(tx, "invoice", c.InvoiceDate)
	c.InvoiceNumber = n
	return err
}

// BeforeCreate numbers the payment's receipt.
func (p *SubscriptionPayment) BeforeCreate(tx *gorm.DB) error {
	if p.ReceiptNumber != "" {
		return nil
	}
	n, err := NextSubscriptionDocNumber(tx, "receipt", p.PaymentDate)
	p.ReceiptNumber = n
	return err
}
