package utils

import (
	"truerp/models"
	"encoding/json"
)

func DefaultInvoiceTemplateCustomization() models.InvoiceTemplateCustomization {
	return models.InvoiceTemplateCustomization{
		ThemeStyleTags: []string{},
		UseCustomTheme: false,
		ThemeSettings: models.InvoiceThemeSettings{
			ShowPartyBalance:               true,
			ShowPhoneOnInvoice:             true,
			ShowItemDescription:            false,
			ShowAlternateUnit:              false,
			EnableFreeItemQuantity:         false,
			ShowTimeOnInvoice:              false,
			PriceHistory:                   false,
			AutoApplyLuxuryThemeForSharing: false,
		},
		InvoiceDetails: models.InvoiceDetailVisibility{
			ShowInvoiceNumber:      true,
			ShowInvoiceDate:        true,
			ShowDueDate:            true,
			ShowPlaceOfSupply:      true,
			ShowPaymentTerms:       true,
			ShowNotes:              true,
			ShowTermsAndConditions: true,
			ShowAmountInWords:      true,
			ShowReceivedAmount:     true,
			ShowBalanceDue:         true,
		},
		PartyDetails: models.PartyDetailVisibility{
			ShowPartyName:       true,
			ShowPartyAddress:    true,
			ShowPartyPhone:      true,
			ShowPartyGSTIN:      true,
			ShowShippingAddress: false,
		},
		ItemColumns: models.ItemTableColumnVisibility{
			Items: true, HSN: true, Qty: true, Rate: true, Disc: true, Tax: true, Amount: true,
			Batch: false, MRP: false,
		},
		Miscellaneous: models.InvoiceMiscVisibility{
			ShowBankDetails: true,
			ShowSignature:   false,
			ShowQRCode:      false,
			ShowEwayBill:    false,
		},
	}
}

func ParseInvoiceTemplateCustomization(raw string) models.InvoiceTemplateCustomization {
	out := DefaultInvoiceTemplateCustomization()
	if raw == "" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	if out.ThemeStyleTags == nil {
		out.ThemeStyleTags = []string{}
	}
	return out
}

func EncodeInvoiceTemplateCustomization(c models.InvoiceTemplateCustomization) string {
	if c.ThemeStyleTags == nil {
		c.ThemeStyleTags = []string{}
	}
	b, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	return string(b)
}
