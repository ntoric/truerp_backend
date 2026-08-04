package models

// InvoiceTemplateCustomization holds display toggles for GST invoice PDFs and previews.
type InvoiceTemplateCustomization struct {
	ThemeStyleTags []string                  `json:"theme_style_tags"`
	UseCustomTheme bool                      `json:"use_custom_theme"`
	ThemeSettings  InvoiceThemeSettings      `json:"theme_settings"`
	InvoiceDetails InvoiceDetailVisibility   `json:"invoice_details"`
	PartyDetails   PartyDetailVisibility     `json:"party_details"`
	ItemColumns    ItemTableColumnVisibility `json:"item_columns"`
	Miscellaneous  InvoiceMiscVisibility     `json:"miscellaneous"`
}

type InvoiceThemeSettings struct {
	ShowPartyBalance               bool `json:"show_party_balance"`
	EnableFreeItemQuantity         bool `json:"enable_free_item_quantity"`
	ShowItemDescription            bool `json:"show_item_description"`
	ShowAlternateUnit              bool `json:"show_alternate_unit"`
	ShowPhoneOnInvoice             bool `json:"show_phone_on_invoice"`
	ShowTimeOnInvoice              bool `json:"show_time_on_invoice"`
	PriceHistory                   bool `json:"price_history"`
	AutoApplyLuxuryThemeForSharing bool `json:"auto_apply_luxury_theme_for_sharing"`
}

type InvoiceDetailVisibility struct {
	ShowInvoiceNumber      bool `json:"show_invoice_number"`
	ShowInvoiceDate        bool `json:"show_invoice_date"`
	ShowDueDate            bool `json:"show_due_date"`
	ShowPlaceOfSupply      bool `json:"show_place_of_supply"`
	ShowPaymentTerms       bool `json:"show_payment_terms"`
	ShowNotes              bool `json:"show_notes"`
	ShowTermsAndConditions bool `json:"show_terms_and_conditions"`
	ShowAmountInWords      bool `json:"show_amount_in_words"`
	ShowReceivedAmount     bool `json:"show_received_amount"`
	ShowBalanceDue         bool `json:"show_balance_due"`
}

type PartyDetailVisibility struct {
	ShowPartyName      bool `json:"show_party_name"`
	ShowPartyAddress   bool `json:"show_party_address"`
	ShowPartyPhone     bool `json:"show_party_phone"`
	ShowPartyGSTIN     bool `json:"show_party_gstin"`
	ShowShippingAddress bool `json:"show_shipping_address"`
}

type ItemTableColumnVisibility struct {
	Items  bool `json:"items"`
	HSN    bool `json:"hsn"`
	Qty    bool `json:"qty"`
	Rate   bool `json:"rate"`
	Disc   bool `json:"disc"`
	Tax    bool `json:"tax"`
	Amount bool `json:"amount"`
	Batch  bool `json:"batch"`
	MRP    bool `json:"mrp"`
}

type InvoiceMiscVisibility struct {
	ShowBankDetails bool `json:"show_bank_details"`
	ShowSignature   bool `json:"show_signature"`
	ShowQRCode      bool `json:"show_qr_code"`
	ShowEwayBill    bool `json:"show_eway_bill"`
}
