package controllers

// Stable public representations returned by /api/public/v1. Field names and
// meanings are part of the API contract documented in
// docs/public-api/openapi.yaml — change them only additively.

import (
	"encoding/json"
	"strings"
	"time"
	"truerp/models"

	"github.com/google/uuid"
)

const publicCurrency = "INR"

type PublicAddress struct {
	Line      string `json:"line"`
	City      string `json:"city"`
	State     string `json:"state"`
	StateCode string `json:"state_code"`
	Pincode   string `json:"pincode"`
}

type PublicParty struct {
	ID             uuid.UUID     `json:"id"`
	Object         string        `json:"object"`
	Type           string        `json:"type"`
	Name           string        `json:"name"`
	Phone          string        `json:"phone"`
	Email          string        `json:"email"`
	GSTIN          string        `json:"gstin"`
	PAN            string        `json:"pan"`
	BillingAddress PublicAddress `json:"billing_address"`
	Category       string        `json:"category"`
	CreditLimit    float64       `json:"credit_limit"`
	OpeningBalance float64       `json:"opening_balance"`
	Balance        float64       `json:"balance"`
	Currency       string        `json:"currency"`
	Notes          string        `json:"notes"`
	IsActive       bool          `json:"is_active"`
	CreatedAt      string        `json:"created_at"`
	UpdatedAt      string        `json:"updated_at"`
}

func toPublicParty(p models.Party) PublicParty {
	return PublicParty{
		ID:     p.ID,
		Object: "party",
		Type:   p.PartyType,
		Name:   p.Name,
		Phone:  p.Phone,
		Email:  p.Email,
		GSTIN:  p.GSTIN,
		PAN:    p.PAN,
		BillingAddress: PublicAddress{
			Line: p.Address, City: p.City, State: p.State, StateCode: p.StateCode, Pincode: p.Pincode,
		},
		Category:       p.Category,
		CreditLimit:    round2(p.CreditLimit),
		OpeningBalance: round2(p.OpeningBalance),
		Balance:        round2(p.Balance),
		Currency:       publicCurrency,
		Notes:          p.Notes,
		IsActive:       p.IsActive,
		CreatedAt:      publicTimestamp(p.CreatedAt),
		UpdatedAt:      publicTimestamp(p.UpdatedAt),
	}
}

type PublicProduct struct {
	ID                       uuid.UUID `json:"id"`
	Object                   string    `json:"object"`
	Type                     string    `json:"type"`
	Name                     string    `json:"name"`
	SKU                      string    `json:"sku"`
	ItemCode                 string    `json:"item_code"`
	Category                 string    `json:"category"`
	Unit                     string    `json:"unit"`
	HSNCode                  string    `json:"hsn_code"`
	SalePrice                float64   `json:"sale_price"`
	PurchasePrice            float64   `json:"purchase_price"`
	MRP                      float64   `json:"mrp"`
	TaxRate                  float64   `json:"tax_rate"`
	GSTEnabled               bool      `json:"gst_enabled"`
	SalePriceIncludesTax     bool      `json:"sale_price_includes_tax"`
	PurchasePriceIncludesTax bool      `json:"purchase_price_includes_tax"`
	MinStock                 float64   `json:"min_stock"`
	TrackBatches             bool      `json:"track_batches"`
	Description              string    `json:"description"`
	ImageURL                 string    `json:"image_url"`
	Currency                 string    `json:"currency"`
	IsActive                 bool      `json:"is_active"`
	CreatedAt                string    `json:"created_at"`
	UpdatedAt                string    `json:"updated_at"`
}

func toPublicProduct(p models.Product) PublicProduct {
	itemType := p.ItemType
	if itemType == "" {
		itemType = "product"
	}
	return PublicProduct{
		ID:                       p.ID,
		Object:                   "product",
		Type:                     itemType,
		Name:                     p.Name,
		SKU:                      p.SKU,
		ItemCode:                 p.ItemCode,
		Category:                 p.Category,
		Unit:                     p.Unit,
		HSNCode:                  p.HSNCode,
		SalePrice:                round2(p.SalePrice),
		PurchasePrice:            round2(p.PurchasePrice),
		MRP:                      round2(p.MRP),
		TaxRate:                  p.TaxRate,
		GSTEnabled:               p.GstEnabled,
		SalePriceIncludesTax:     p.SalePriceWithTax,
		PurchasePriceIncludesTax: p.PurchasePriceWithTax,
		MinStock:                 p.MinStock,
		TrackBatches:             p.EnableBatching,
		Description:              p.Description,
		ImageURL:                 p.ImageUrl,
		Currency:                 publicCurrency,
		IsActive:                 p.IsActive,
		CreatedAt:                publicTimestamp(p.CreatedAt),
		UpdatedAt:                publicTimestamp(p.UpdatedAt),
	}
}

type PublicStockLocation struct {
	WarehouseID   uuid.UUID `json:"warehouse_id"`
	WarehouseName string    `json:"warehouse_name"`
	BatchNo       string    `json:"batch_no"`
	ExpiryDate    *string   `json:"expiry_date"`
	Quantity      float64   `json:"quantity"`
	Reserved      float64   `json:"reserved"`
	Available     float64   `json:"available"`
	AverageCost   float64   `json:"average_cost"`
}

type PublicStockLevel struct {
	Object      string                `json:"object"`
	ProductID   uuid.UUID             `json:"product_id"`
	ProductName string                `json:"product_name"`
	SKU         string                `json:"sku"`
	Unit        string                `json:"unit"`
	Quantity    float64               `json:"quantity"`
	Reserved    float64               `json:"reserved"`
	Available   float64               `json:"available"`
	Value       float64               `json:"value"`
	MinStock    float64               `json:"min_stock"`
	LowStock    bool                  `json:"low_stock"`
	Locations   []PublicStockLocation `json:"locations"`
	UpdatedAt   *string               `json:"updated_at"`
}

type PublicPartyRef struct {
	ID    uuid.UUID `json:"id"`
	Name  string    `json:"name"`
	GSTIN string    `json:"gstin"`
	Phone string    `json:"phone"`
}

func toPartyRef(p models.Party) *PublicPartyRef {
	if p.ID == uuid.Nil {
		return nil
	}
	return &PublicPartyRef{ID: p.ID, Name: p.Name, GSTIN: p.GSTIN, Phone: p.Phone}
}

type PublicCharge struct {
	Label  string  `json:"label"`
	Amount float64 `json:"amount"`
}

func toPublicCharges(items []models.AdditionalCharge) []PublicCharge {
	out := make([]PublicCharge, 0, len(items))
	for _, it := range items {
		out = append(out, PublicCharge{Label: it.Label, Amount: round2(it.Amount)})
	}
	return out
}

type PublicInvoiceLine struct {
	ID              uuid.UUID  `json:"id"`
	ProductID       *uuid.UUID `json:"product_id"`
	Description     string     `json:"description"`
	Quantity        float64    `json:"quantity"`
	Unit            string     `json:"unit"`
	UnitPrice       float64    `json:"unit_price"`
	DiscountPercent float64    `json:"discount_percent"`
	TaxRate         float64    `json:"tax_rate"`
	CGST            float64    `json:"cgst"`
	SGST            float64    `json:"sgst"`
	IGST            float64    `json:"igst"`
	CessRate        float64    `json:"cess_rate"`
	CessAmount      float64    `json:"cess_amount"`
	HSNCode         string     `json:"hsn_code"`
	BatchNo         string     `json:"batch_no"`
	ExpiryDate      *string    `json:"expiry_date"`
	Total           float64    `json:"total"`
}

type PublicInvoiceAmounts struct {
	Subtotal          float64 `json:"subtotal"`
	LineDiscount      float64 `json:"line_discount"`
	InvoiceDiscount   float64 `json:"invoice_discount"`
	AdditionalCharges float64 `json:"additional_charges"`
	Tax               float64 `json:"tax"`
	CGST              float64 `json:"cgst"`
	SGST              float64 `json:"sgst"`
	IGST              float64 `json:"igst"`
	Cess              float64 `json:"cess"`
	RoundOff          float64 `json:"round_off"`
	Total             float64 `json:"total"`
	Paid              float64 `json:"paid"`
	Due               float64 `json:"due"`
}

type PublicInvoice struct {
	ID                    uuid.UUID            `json:"id"`
	Object                string               `json:"object"`
	Number                string               `json:"number"`
	Type                  string               `json:"type"`
	Status                string               `json:"status"`
	PartyID               uuid.UUID            `json:"party_id"`
	Party                 *PublicPartyRef      `json:"party"`
	Date                  string               `json:"date"`
	DueDate               *string              `json:"due_date"`
	Currency              string               `json:"currency"`
	PlaceOfSupply         string               `json:"place_of_supply"`
	IsInterState          bool                 `json:"is_inter_state"`
	ReverseCharge         bool                 `json:"reverse_charge"`
	LineItems             []PublicInvoiceLine  `json:"line_items"`
	AdditionalChargeItems []PublicCharge       `json:"additional_charge_items"`
	Amounts               PublicInvoiceAmounts `json:"amounts"`
	PaymentMode           string               `json:"payment_mode"`
	Notes                 string               `json:"notes"`
	Terms                 string               `json:"terms"`
	ShippingAddress       string               `json:"shipping_address"`
	CustomFields          map[string]any       `json:"custom_fields"`
	IRN                   string               `json:"irn"`
	EInvoiceStatus        string               `json:"e_invoice_status"`
	EWayBillNumber        string               `json:"eway_bill_number"`
	Source                string               `json:"source"`
	CancelledAt           *string              `json:"cancelled_at"`
	CancelReason          string               `json:"cancel_reason"`
	CreatedAt             string               `json:"created_at"`
	UpdatedAt             string               `json:"updated_at"`
}

func toPublicInvoice(inv models.Invoice, loc *time.Location) PublicInvoice {
	lines := make([]PublicInvoiceLine, 0, len(inv.Items))
	for _, it := range inv.Items {
		lines = append(lines, PublicInvoiceLine{
			ID: it.ID, ProductID: it.ProductID, Description: it.Description,
			Quantity: it.Quantity, Unit: it.Unit, UnitPrice: round2(it.UnitPrice),
			DiscountPercent: it.Discount, TaxRate: it.TaxRate,
			CGST: round2(it.CGST), SGST: round2(it.SGST), IGST: round2(it.IGST),
			CessRate: it.CessRate, CessAmount: round2(it.CessAmount),
			HSNCode: it.HSNCode, BatchNo: it.BatchNo, ExpiryDate: publicDatePtr(it.ExpDate, loc),
			Total: round2(it.Total),
		})
	}
	invType := inv.InvoiceType
	if invType == "" {
		invType = "tax_invoice"
	}
	source := "backoffice"
	if inv.IsPOS {
		source = "pos"
	}
	due := inv.TotalAmount - inv.AmountPaid
	if due < 0 || inv.Status == "cancelled" {
		due = 0
	}
	custom := map[string]any{}
	if strings.TrimSpace(inv.CustomFields) != "" {
		_ = json.Unmarshal([]byte(inv.CustomFields), &custom)
	}
	return PublicInvoice{
		ID: inv.ID, Object: "invoice", Number: inv.InvoiceNumber, Type: invType, Status: inv.Status,
		PartyID: inv.PartyID, Party: toPartyRef(inv.Party),
		Date: publicDate(inv.Date, loc), DueDate: publicDatePtr(inv.DueDate, loc),
		Currency: publicCurrency, PlaceOfSupply: inv.PlaceOfSupply,
		IsInterState: inv.IsInterState, ReverseCharge: inv.ReverseCharge,
		LineItems: lines, AdditionalChargeItems: toPublicCharges(inv.AdditionalChargeItems),
		Amounts: PublicInvoiceAmounts{
			Subtotal: round2(inv.SubTotal), LineDiscount: round2(inv.DiscountTotal),
			InvoiceDiscount: round2(inv.InvoiceDiscount), AdditionalCharges: round2(inv.AdditionalCharges),
			Tax: round2(inv.TaxTotal), CGST: round2(inv.CGSTTotal), SGST: round2(inv.SGSTTotal),
			IGST: round2(inv.IGSTTotal), Cess: round2(inv.CessTotal), RoundOff: round2(inv.RoundOff),
			Total: round2(inv.TotalAmount), Paid: round2(inv.AmountPaid), Due: round2(due),
		},
		PaymentMode: inv.PaymentMode, Notes: inv.Notes, Terms: inv.Terms,
		ShippingAddress: inv.ShippingAddress, CustomFields: custom,
		IRN: inv.IRN, EInvoiceStatus: inv.EInvoiceStatus, EWayBillNumber: inv.EWayBillNumber,
		Source: source, CancelledAt: publicTimestampPtr(inv.CancelledAt), CancelReason: inv.CancelReason,
		CreatedAt: publicTimestamp(inv.CreatedAt), UpdatedAt: publicTimestamp(inv.UpdatedAt),
	}
}

type PublicPayment struct {
	ID            uuid.UUID       `json:"id"`
	Object        string          `json:"object"`
	Number        string          `json:"number"`
	PartyID       uuid.UUID       `json:"party_id"`
	Party         *PublicPartyRef `json:"party"`
	InvoiceID     *uuid.UUID      `json:"invoice_id"`
	Amount        float64         `json:"amount"`
	Discount      float64         `json:"discount"`
	NetAmount     float64         `json:"net_amount"`
	Currency      string          `json:"currency"`
	Mode          string          `json:"mode"`
	BankAccountID *uuid.UUID      `json:"bank_account_id"`
	Date          string          `json:"date"`
	Reference     string          `json:"reference"`
	Notes         string          `json:"notes"`
	Status        string          `json:"status"`
	CancelledAt   *string         `json:"cancelled_at"`
	CancelReason  string          `json:"cancel_reason"`
	CreatedAt     string          `json:"created_at"`
	UpdatedAt     string          `json:"updated_at"`
}

func toPublicPayment(p models.Payment, loc *time.Location) PublicPayment {
	status := p.Status
	if status == "" {
		status = "active"
	}
	return PublicPayment{
		ID: p.ID, Object: "payment", Number: p.PaymentInNumber, PartyID: p.PartyID, Party: toPartyRef(p.Party),
		InvoiceID: p.InvoiceID, Amount: round2(p.AmountReceived), Discount: round2(p.PaymentInDiscount),
		NetAmount: round2(p.AmountReceived - p.PaymentInDiscount), Currency: publicCurrency,
		Mode: p.Mode, BankAccountID: p.BankAccountID, Date: publicDate(p.Date, loc),
		Reference: p.Reference, Notes: p.Notes, Status: status,
		CancelledAt: publicTimestampPtr(p.CancelledAt), CancelReason: p.CancelReason,
		CreatedAt: publicTimestamp(p.CreatedAt), UpdatedAt: publicTimestamp(p.UpdatedAt),
	}
}

type PublicPurchaseLine struct {
	ID              uuid.UUID  `json:"id"`
	ProductID       *uuid.UUID `json:"product_id"`
	ItemCode        string     `json:"item_code"`
	Description     string     `json:"description"`
	Quantity        float64    `json:"quantity"`
	Unit            string     `json:"unit"`
	UnitPrice       float64    `json:"unit_price"`
	DiscountPercent float64    `json:"discount_percent"`
	TaxRate         float64    `json:"tax_rate"`
	TaxAmount       float64    `json:"tax_amount"`
	MRP             float64    `json:"mrp"`
	SalePrice       float64    `json:"sale_price"`
	HSNCode         string     `json:"hsn_code"`
	BatchNo         string     `json:"batch_no"`
	MfgDate         *string    `json:"mfg_date"`
	ExpiryDate      *string    `json:"expiry_date"`
	Total           float64    `json:"total"`
}

type PublicPurchaseAmounts struct {
	Subtotal          float64 `json:"subtotal"`
	Tax               float64 `json:"tax"`
	InvoiceDiscount   float64 `json:"invoice_discount"`
	AdditionalCharges float64 `json:"additional_charges"`
	Total             float64 `json:"total"`
	Paid              float64 `json:"paid"`
	Due               float64 `json:"due"`
}

type PublicPurchaseBill struct {
	ID                    uuid.UUID             `json:"id"`
	Object                string                `json:"object"`
	Number                string                `json:"number"`
	Status                string                `json:"status"`
	PartyID               uuid.UUID             `json:"party_id"`
	Party                 *PublicPartyRef       `json:"party"`
	Date                  string                `json:"date"`
	DueDate               *string               `json:"due_date"`
	WarehouseID           *uuid.UUID            `json:"warehouse_id"`
	StockStatus           string                `json:"stock_status"`
	TaxExempt             bool                  `json:"tax_exempt"`
	Currency              string                `json:"currency"`
	LineItems             []PublicPurchaseLine  `json:"line_items"`
	AdditionalChargeItems []PublicCharge        `json:"additional_charge_items"`
	Amounts               PublicPurchaseAmounts `json:"amounts"`
	PaymentMode           string                `json:"payment_mode"`
	Notes                 string                `json:"notes"`
	CreatedAt             string                `json:"created_at"`
	UpdatedAt             string                `json:"updated_at"`
}

func toPublicPurchaseBill(b models.PurchaseBill, loc *time.Location) PublicPurchaseBill {
	lines := make([]PublicPurchaseLine, 0, len(b.Items))
	for _, it := range b.Items {
		lines = append(lines, PublicPurchaseLine{
			ID: it.ID, ProductID: it.ProductID, ItemCode: it.ItemCode, Description: it.Description,
			Quantity: it.Quantity, Unit: it.Unit, UnitPrice: round2(it.UnitPrice), DiscountPercent: it.Discount,
			TaxRate: it.TaxRate, TaxAmount: round2(it.TaxAmount), MRP: round2(it.MRP), SalePrice: round2(it.SalePrice),
			HSNCode: it.HSNCode, BatchNo: it.BatchNo, MfgDate: publicDatePtr(it.MfgDate, loc),
			ExpiryDate: publicDatePtr(it.ExpDate, loc), Total: round2(it.Total),
		})
	}
	return PublicPurchaseBill{
		ID: b.ID, Object: "purchase_bill", Number: b.BillNumber, Status: b.Status,
		PartyID: b.PartyID, Party: toPartyRef(b.Party),
		Date: publicDate(b.BillDate, loc), DueDate: publicDatePtr(b.DueDate, loc),
		WarehouseID: b.WarehouseID, StockStatus: b.StockStatus, TaxExempt: b.TaxExempt,
		Currency: publicCurrency, LineItems: lines, AdditionalChargeItems: toPublicCharges(b.AdditionalChargeItems),
		Amounts: PublicPurchaseAmounts{
			Subtotal: round2(b.SubTotal), Tax: round2(b.TaxTotal), InvoiceDiscount: round2(b.InvoiceDiscount),
			AdditionalCharges: round2(b.AdditionalCharges), Total: round2(b.TotalAmount),
			Paid: round2(b.PaidAmount), Due: round2(b.BalanceDue),
		},
		PaymentMode: b.PaymentMode, Notes: b.Notes,
		CreatedAt: publicTimestamp(b.CreatedAt), UpdatedAt: publicTimestamp(b.UpdatedAt),
	}
}

type PublicWebhookEndpoint struct {
	ID             uuid.UUID `json:"id"`
	Object         string    `json:"object"`
	URL            string    `json:"url"`
	Description    string    `json:"description"`
	Events         []string  `json:"events"`
	IsActive       bool      `json:"is_active"`
	DisabledReason string    `json:"disabled_reason"`
	FailureCount   int       `json:"failure_count"`
	Secret         string    `json:"secret,omitempty"` // only on create / rotate
	LastDeliveryAt *string   `json:"last_delivery_at"`
	CreatedAt      string    `json:"created_at"`
	UpdatedAt      string    `json:"updated_at"`
}

func toPublicWebhook(w models.WebhookEndpoint) PublicWebhookEndpoint {
	events := strings.Fields(w.Events)
	if len(events) == 0 {
		events = []string{"*"}
	}
	return PublicWebhookEndpoint{
		ID: w.ID, Object: "webhook_endpoint", URL: w.URL, Description: w.Description, Events: events,
		IsActive: w.IsActive, DisabledReason: w.DisabledReason, FailureCount: w.FailureCount,
		LastDeliveryAt: publicTimestampPtr(w.LastDeliveryAt),
		CreatedAt:      publicTimestamp(w.CreatedAt), UpdatedAt: publicTimestamp(w.UpdatedAt),
	}
}

type PublicEvent struct {
	ID        uuid.UUID       `json:"id"`
	Object    string          `json:"object"`
	Type      string          `json:"type"`
	CreatedAt string          `json:"created_at"`
	Data      json.RawMessage `json:"data"`
}

func toPublicEvent(e models.IntegrationEvent) PublicEvent {
	data := json.RawMessage(e.Data)
	if len(data) == 0 {
		data = json.RawMessage(`{}`)
	}
	return PublicEvent{
		ID: e.ID, Object: "event", Type: e.Type, CreatedAt: publicTimestamp(e.CreatedAt),
		Data: json.RawMessage(`{"object":` + string(data) + `}`),
	}
}
