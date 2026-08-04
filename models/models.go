package models

import (
	"database/sql/driver"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// FlexibleTime is a custom time type that can parse both date-only (YYYY-MM-DD) and full datetime (ISO 8601) formats
type FlexibleTime struct {
	time.Time
}

// UnmarshalJSON implements custom JSON unmarshaling for FlexibleTime
func (ft *FlexibleTime) UnmarshalJSON(data []byte) error {
	s := string(data)
	if s == "null" || s == `""` {
		return nil
	}
	s = s[1 : len(s)-1] // Remove quotes

	// Try parsing as date-only format (YYYY-MM-DD)
	if t, err := time.Parse("2006-01-02", s); err == nil {
		ft.Time = t
		return nil
	}

	// Try parsing as full datetime format (ISO 8601)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		ft.Time = t
		return nil
	}

	return fmt.Errorf("cannot parse time: %s", s)
}

// MarshalJSON implements custom JSON marshaling for FlexibleTime
func (ft FlexibleTime) MarshalJSON() ([]byte, error) {
	if ft.Time.IsZero() {
		return []byte("null"), nil
	}
	return []byte(fmt.Sprintf("\"%s\"", ft.Time.Format("2006-01-02"))), nil
}

// Scan implements the sql.Scanner interface for database scanning
func (ft *FlexibleTime) Scan(value interface{}) error {
	if value == nil {
		ft.Time = time.Time{}
		return nil
	}
	if t, ok := value.(time.Time); ok {
		ft.Time = t
		return nil
	}
	return fmt.Errorf("cannot scan %T into FlexibleTime", value)
}

// Value implements the driver.Valuer interface for database storage
func (ft FlexibleTime) Value() (driver.Value, error) {
	if ft.Time.IsZero() {
		return nil, nil
	}
	return ft.Time, nil
}

type User struct {
	ID                       uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	Name                     string         `json:"name" gorm:"not null"`
	Email                    string         `json:"email" gorm:"unique;not null"`
	Password                 string         `json:"-" gorm:"not null"`
	Phone                    string         `json:"phone"`
	Role                     string         `json:"role" gorm:"default:'owner'"`
	StoreID                  *uuid.UUID     `json:"store_id,omitempty" gorm:"type:uuid;index"`
	Store                    *Store         `json:"store,omitempty" gorm:"foreignKey:StoreID"`
	IsStoreOwner             bool           `json:"is_store_owner" gorm:"default:false"`
	TwoFactorEnabled         bool           `json:"two_factor_enabled" gorm:"default:false"`
	TotpSecret               string         `json:"-" gorm:"column:totp_secret"`
	PasswordResetTokenHash   string         `json:"-" gorm:"index"`
	PasswordResetExpiresAt   *time.Time     `json:"-"`
	IsActive                 bool           `json:"is_active" gorm:"default:true"`
	Business                 Business       `json:"business,omitempty" gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE;"`
	CreatedAt                time.Time      `json:"created_at"`
	UpdatedAt                time.Time      `json:"updated_at"`
	DeletedAt                gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

// Store is a multi-tenant business unit. Operational data is scoped to OwnerUserID.
type Store struct {
	ID          uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	Name        string         `json:"name" gorm:"not null"`
	Code        string         `json:"code" gorm:"uniqueIndex;not null"`
	Description string         `json:"description"`
	Address     string         `json:"address"`
	City        string         `json:"city"`
	State       string         `json:"state"`
	Pincode     string         `json:"pincode"`
	Phone       string         `json:"phone"`
	Email       string         `json:"email"`
	OwnerUserID uuid.UUID      `json:"owner_user_id" gorm:"type:uuid;not null;uniqueIndex"`
	IsActive    bool           `json:"is_active" gorm:"default:true"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type Business struct {
	ID            uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID        uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;uniqueIndex"`
	Name          string         `json:"name" gorm:"not null"`
	GSTIN         string         `json:"gstin"`
	Address       string         `json:"address"`
	City          string         `json:"city"`
	State         string         `json:"state"`
	Pincode       string         `json:"pincode"`
	Phone         string         `json:"phone"`
	Email         string         `json:"email"`
	LogoURL       string         `json:"logo_url"`
	SignatureURL  string         `json:"signature_url"`
	StateCode     string         `json:"state_code"`
	BankName      string         `json:"bank_name"`
	AccountNumber string         `json:"account_number"`
	IFSCCode      string         `json:"ifsc_code"`
	UPIID         string         `json:"upi_id"`
	// Label printing settings
	LabelPaperSize      string  `json:"label_paper_size" gorm:"default:'A4'"`
	LabelWidthMM        float64 `json:"label_width_mm" gorm:"default:50"`
	LabelHeightMM       float64 `json:"label_height_mm" gorm:"default:30"`
	LabelColumns        int     `json:"label_columns" gorm:"default:3"`
	LabelRows           int     `json:"label_rows" gorm:"default:8"`
	LabelMarginMM       float64 `json:"label_margin_mm" gorm:"default:10"`
	// AI HSN search settings
	EnableAIHSNSearch   bool    `json:"enable_ai_hsn_search" gorm:"column:enable_aihsn_search;default:false"`
	EnableAIBillParsing bool    `json:"enable_ai_bill_parsing" gorm:"default:false"`
	GeminiAPIKey        string  `json:"gemini_api_key"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	DeletedAt     gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type Invoice struct {
	ID              uuid.UUID       `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID       `json:"user_id" gorm:"type:uuid;not null;index"`
	InvoiceNumber   string          `json:"invoice_number" gorm:"not null;index"`
	InvoiceType     string          `json:"invoice_type" gorm:"default:'tax_invoice'"` // tax_invoice, bill_of_supply, export
	PartyID         uuid.UUID       `json:"party_id" gorm:"type:uuid;not null"`
	Date            time.Time       `json:"date" gorm:"not null"`
	DueDate         *time.Time      `json:"due_date,omitempty"`
	PaymentTerms    int             `json:"payment_terms" gorm:"default:0"` // Payment term in days
	Status          string          `json:"status" gorm:"default:'draft'"` // draft, sent, paid, overdue, cancelled
	SubTotal        float64         `json:"sub_total" gorm:"default:0"`
	DiscountTotal   float64         `json:"discount_total" gorm:"default:0"`
	InvoiceDiscount float64         `json:"invoice_discount" gorm:"default:0"` // Additional invoice-level discount
	AdditionalCharges float64       `json:"additional_charges" gorm:"default:0"`
	TaxTotal        float64         `json:"tax_total" gorm:"default:0"`
	CGSTTotal       float64         `json:"cgst_total" gorm:"default:0"`
	SGSTTotal       float64         `json:"sgst_total" gorm:"default:0"`
	IGSTTotal       float64         `json:"igst_total" gorm:"default:0"`
	RoundOff        float64         `json:"round_off" gorm:"default:0"`
	TotalAmount            float64         `json:"total_amount" gorm:"default:0"`
	AmountPaid             float64         `json:"amount_paid" gorm:"default:0"`
	LoyaltyPointsRedeemed  int64           `json:"loyalty_points_redeemed" gorm:"default:0"`
	LoyaltyPointsEarned    int64           `json:"loyalty_points_earned" gorm:"default:0"`
	LoyaltyDiscount        float64         `json:"loyalty_discount" gorm:"default:0"`
	PaymentMode            string          `json:"payment_mode"`
	BankAccountID   *uuid.UUID      `json:"bank_account_id,omitempty" gorm:"type:uuid;index"`
	Notes           string          `json:"notes"`
	Terms           string          `json:"terms"`
	IsInterState    bool            `json:"is_inter_state" gorm:"default:false"`
	EWayBillRequired bool           `json:"eway_bill_required" gorm:"default:false"`
	EWayBillNumber  string          `json:"eway_bill_number"`
	EWayBillStatus  string          `json:"eway_bill_status" gorm:"default:'pending'"` // pending, generated, cancelled
	EWayBillValidUntil *time.Time   `json:"eway_bill_valid_until,omitempty"`
	Signature       string          `json:"signature"` // Base64 encoded signature
	CustomFields    string          `json:"custom_fields" gorm:"type:text"` // JSON map of custom field key -> value
	PDFTemplate     string          `json:"pdf_template"` // optional per-invoice layout: classic, modern, minimal
	PlaceOfSupply   string          `json:"place_of_supply"` // State code for GST compliance
	ReverseCharge   bool            `json:"reverse_charge" gorm:"default:false"` // Reverse charge mechanism
	IRN             string          `json:"irn"` // Invoice Reference Number for e-invoicing
	EInvoiceStatus  string          `json:"e_invoice_status" gorm:"default:'pending'"` // pending, generated, cancelled
	EInvoiceGeneratedAt *time.Time  `json:"e_invoice_generated_at,omitempty"`
	Party           Party           `json:"party,omitempty" gorm:"foreignKey:PartyID"`
	Items           []InvoiceItem   `json:"items" gorm:"foreignKey:InvoiceID;constraint:OnDelete:CASCADE;"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	DeletedAt       gorm.DeletedAt  `json:"deleted_at,omitempty" gorm:"index"`
}

type InvoiceItem struct {
	ID          uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	InvoiceID   uuid.UUID      `json:"invoice_id" gorm:"type:uuid;not null;index"`
	Description string         `json:"description"`
	Quantity    float64        `json:"quantity" gorm:"default:1"`
	Unit        string         `json:"unit"`
	UnitPrice   float64        `json:"unit_price" gorm:"default:0"`
	Discount    float64        `json:"discount" gorm:"default:0"`
	TaxRate     float64        `json:"tax_rate" gorm:"default:18"`
	CGST        float64        `json:"cgst" gorm:"default:0"`
	SGST        float64        `json:"sgst" gorm:"default:0"`
	IGST        float64        `json:"igst" gorm:"default:0"`
	Total       float64        `json:"total" gorm:"default:0"`
	HSNCode     string         `json:"hsn_code"`
	SACCode     string         `json:"sac_code"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

type Payment struct {
	ID                 uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID             uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	InvoiceID          *uuid.UUID     `json:"invoice_id,omitempty" gorm:"type:uuid"`
	PartyID            uuid.UUID      `json:"party_id" gorm:"type:uuid;not null"`
	Party              Party          `json:"party,omitempty" gorm:"foreignKey:PartyID"`
	AmountReceived     float64        `json:"amount_received" gorm:"default:0"`
	PaymentInDiscount  float64        `json:"payment_in_discount" gorm:"default:0"`
	PaymentInNumber    string         `json:"payment_in_number"`
	Mode               string         `json:"mode"` // cash, upi, bank_transfer, cheque, card
	Date               time.Time      `json:"date"`
	Reference          string         `json:"reference"`
	Notes              string         `json:"notes"`
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
	DeletedAt          gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type PaymentOut struct {
	ID                   uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID               uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	PurchaseBillID       *uuid.UUID     `json:"purchase_bill_id,omitempty" gorm:"type:uuid"`
	PurchaseBill         *PurchaseBill  `json:"purchase_bill,omitempty" gorm:"foreignKey:PurchaseBillID"`
	PartyID              uuid.UUID      `json:"party_id" gorm:"type:uuid;not null"`
	Party                Party          `json:"party,omitempty" gorm:"foreignKey:PartyID"`
	AmountPaid           float64        `json:"amount_paid" gorm:"default:0"`
	PaymentOutDiscount   float64        `json:"payment_out_discount" gorm:"default:0"`
	PaymentOutNumber     string         `json:"payment_out_number"`
	Mode                 string         `json:"mode"` // cash, upi, bank_transfer, cheque, card
	Date                 time.Time      `json:"date"`
	Reference            string         `json:"reference"`
	Notes                string         `json:"notes"`
	CreatedAt            time.Time      `json:"created_at"`
	UpdatedAt            time.Time      `json:"updated_at"`
	DeletedAt            gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type Expense struct {
	ID                 uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID             uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	ExpenseNumber      string         `json:"expense_number" gorm:"not null;index"`
	OriginalInvoiceNum string         `json:"original_invoice_num"`
	Category           string         `json:"category" gorm:"not null"`
	Description         string         `json:"description"`
	Amount             float64        `json:"amount" gorm:"default:0"`
	SubTotal           float64        `json:"sub_total" gorm:"default:0"`
	TaxTotal           float64        `json:"tax_total" gorm:"default:0"`
	WithGST            bool           `json:"with_gst" gorm:"default:false"`
	TaxRate            float64        `json:"tax_rate" gorm:"default:18"`
	Date               time.Time      `json:"date"`
	Vendor             string         `json:"vendor"`
	PaymentMode        string         `json:"payment_mode"`
	Notes              string         `json:"notes"`
	ReceiptURL         string         `json:"receipt_url"`
	Items              []ExpenseItem  `json:"items" gorm:"foreignKey:ExpenseID;constraint:OnDelete:CASCADE;"`
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
	DeletedAt          gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type ExpenseItem struct {
	ID          uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	ExpenseID   uuid.UUID      `json:"expense_id" gorm:"type:uuid;not null;index"`
	Description string         `json:"description" gorm:"not null"`
	Quantity    float64        `json:"quantity" gorm:"default:1"`
	UnitPrice   float64        `json:"unit_price" gorm:"default:0"`
	TaxRate     float64        `json:"tax_rate" gorm:"default:18"`
	TaxAmount   float64        `json:"tax_amount" gorm:"default:0"`
	Total       float64        `json:"total" gorm:"default:0"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

type DashboardStats struct {
	TotalSales        float64 `json:"total_sales"`
	TotalInvoices     int64   `json:"total_invoices"`
	TotalParties      int64   `json:"total_parties"`
	PendingAmount     float64 `json:"pending_amount"`
	TodaySales        float64 `json:"today_sales"`
	TodayInvoices     int64   `json:"today_invoices"`
	OverdueInvoices   int64   `json:"overdue_invoices"`
}

type DailyReportMetric struct {
	TotalAmount float64 `json:"total_amount"`
	Count       int64   `json:"count"`
}

type DailyReport struct {
	Date            string            `json:"date"`
	BusinessName    string            `json:"business_name"`
	Sales           DailyReportMetric `json:"sales"`
	Purchases       DailyReportMetric `json:"purchases"`
	CreditNotes     DailyReportMetric `json:"credit_notes"`
	DebitNotes      DailyReportMetric `json:"debit_notes"`
	Expenses        DailyReportMetric `json:"expenses"`
	PaymentsIn      DailyReportMetric `json:"payments_in"`
	PaymentsOut     DailyReportMetric `json:"payments_out"`
	SalesReturns    DailyReportMetric `json:"sales_returns"`
	PurchaseReturns DailyReportMetric `json:"purchase_returns"`
	GSTCollected    float64           `json:"gst_collected"`
	NetCashFlow     float64           `json:"net_cash_flow"`
}

type GSTReport struct {
	Month       string  `json:"month"`
	CGST        float64 `json:"cgst"`
	SGST        float64 `json:"sgst"`
	IGST        float64 `json:"igst"`
	TotalTax    float64 `json:"total_tax"`
	TotalValue  float64 `json:"total_value"`
}

type Category struct {
	ID          uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID      uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	Name        string         `json:"name" gorm:"not null"`
	Description string         `json:"description"`
	ParentID    *uuid.UUID     `json:"parent_id" gorm:"type:uuid"`
	Parent      *Category      `json:"parent,omitempty" gorm:"foreignKey:ParentID"`
	IsActive    bool           `json:"is_active" gorm:"default:true"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

// ExpenseCategory is separate from product Category — used only for expenses.
type ExpenseCategory struct {
	ID          uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID      uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	Name        string         `json:"name" gorm:"not null"`
	Description string         `json:"description"`
	IsActive    bool           `json:"is_active" gorm:"default:true"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type POSSession struct {
	ID            uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID        uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	CashierID     uuid.UUID      `json:"cashier_id" gorm:"type:uuid;not null"`
	OutletID      uuid.UUID      `json:"outlet_id" gorm:"type:uuid"`
	Status        string         `json:"status" gorm:"default:'open'"` // open, closed
	OpeningCash   float64        `json:"opening_cash" gorm:"default:0"`
	ClosingCash   float64        `json:"closing_cash" gorm:"default:0"`
	CashInTotal   float64        `json:"cash_in_total" gorm:"default:0"`
	CashOutTotal  float64        `json:"cash_out_total" gorm:"default:0"`
	TotalSales    float64        `json:"total_sales" gorm:"default:0"`
	TotalReturns  float64        `json:"total_returns" gorm:"default:0"`
	TotalInvoices int            `json:"total_invoices" gorm:"default:0"`
	Notes         string         `json:"notes"`
	OpenedAt      time.Time      `json:"opened_at"`
	ClosedAt      *time.Time     `json:"closed_at,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	DeletedAt     gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type CashMovement struct {
	ID          uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	SessionID   uuid.UUID      `json:"session_id" gorm:"type:uuid;not null;index"`
	Type        string         `json:"type" gorm:"not null"` // cash_in, cash_out
	Amount      float64        `json:"amount" gorm:"default:0"`
	Reason      string         `json:"reason"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

type StockEntry struct {
	ID          uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID      uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	ItemName    string         `json:"item_name" gorm:"not null"`
	ProductID   *uuid.UUID     `json:"product_id" gorm:"type:uuid;index"` // Optional link to product
	Product     Product       `json:"product,omitempty" gorm:"foreignKey:ProductID"`
	OutletID    uuid.UUID      `json:"outlet_id" gorm:"type:uuid"`
	EntryType   string         `json:"entry_type" gorm:"not null"` // purchase, sale, adjustment, transfer, opening
	Quantity    float64        `json:"quantity" gorm:"default:0"`
	BalanceQty  float64        `json:"balance_qty" gorm:"default:0"`
	CostPrice   float64        `json:"cost_price" gorm:"default:0"`
	BatchNo     string         `json:"batch_no"`
	ItemCode    string         `json:"item_code"` // Item code specific to this stock entry/batch
	MfgDate     *time.Time     `json:"mfg_date,omitempty"`
	ExpDate     *time.Time     `json:"exp_date,omitempty"`
	ReferenceID uuid.UUID      `json:"reference_id" gorm:"type:uuid"` // invoice_id, po_id, etc.
	ReferenceType string        `json:"reference_type"` // invoice, purchase_order, etc.
	Notes       string         `json:"notes"`
	EntryDate   time.Time      `json:"entry_date"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type InventoryStock struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	ProductID       uuid.UUID      `json:"product_id" gorm:"type:uuid;not null;index"`
	Product         Product       `json:"product,omitempty" gorm:"foreignKey:ProductID"`
	OutletID        uuid.UUID      `json:"outlet_id" gorm:"type:uuid;not null;index"`
	Quantity        float64        `json:"quantity" gorm:"default:0"`
	ReservedQty     float64        `json:"reserved_qty" gorm:"default:0"` // Reserved for orders
	AvailableQty    float64        `json:"available_qty" gorm:"default:0"` // Quantity - Reserved
	AverageCost     float64        `json:"average_cost" gorm:"default:0"` // Weighted average cost
	LastUpdated     time.Time      `json:"last_updated"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

type StockTransfer struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	FromOutletID    uuid.UUID      `json:"from_outlet_id" gorm:"type:uuid"`
	ToOutletID      uuid.UUID      `json:"to_outlet_id" gorm:"type:uuid"`
	Status          string         `json:"status" gorm:"default:'draft'"` // draft, submitted, received, cancelled
	TotalItems      int            `json:"total_items" gorm:"default:0"`
	TotalQuantity   float64        `json:"total_quantity" gorm:"default:0"`
	Notes           string         `json:"notes"`
	SentDate        *time.Time     `json:"sent_date,omitempty"`
	ReceivedDate    *time.Time     `json:"received_date,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
	Items           []StockTransferItem `json:"items" gorm:"foreignKey:TransferID;constraint:OnDelete:CASCADE;"`
}

type StockTransferItem struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	TransferID      uuid.UUID      `json:"transfer_id" gorm:"type:uuid;not null;index"`
	ProductID       uuid.UUID      `json:"product_id" gorm:"type:uuid;not null"`
	Product         Product       `json:"product,omitempty" gorm:"foreignKey:ProductID"`
	Quantity        float64        `json:"quantity" gorm:"default:0"`
	SentQuantity    float64        `json:"sent_quantity" gorm:"default:0"`
	ReceivedQuantity float64       `json:"received_quantity" gorm:"default:0"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

type PurchaseOrder struct {
	ID              uuid.UUID           `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID           `json:"user_id" gorm:"type:uuid;not null;index"`
	PartyID         uuid.UUID           `json:"party_id" gorm:"type:uuid;not null"`
	Party           Party               `json:"party,omitempty" gorm:"foreignKey:PartyID"`
	OrderNumber     string              `json:"order_number" gorm:"not null;index"`
	Status          string              `json:"status" gorm:"default:'draft'"` // draft, submitted, received, cancelled
	OrderDate       time.Time           `json:"order_date" gorm:"not null"`
	ExpectedDate    *time.Time          `json:"expected_date,omitempty"`
	SubTotal        float64             `json:"sub_total" gorm:"default:0"`
	TaxTotal        float64             `json:"tax_total" gorm:"default:0"`
	TotalAmount     float64             `json:"total_amount" gorm:"default:0"`
	Notes           string              `json:"notes"`
	Terms           string              `json:"terms"`
	Items           []PurchaseOrderItem `json:"items" gorm:"foreignKey:OrderID;constraint:OnDelete:CASCADE;"`
	CreatedAt       time.Time           `json:"created_at"`
	UpdatedAt       time.Time           `json:"updated_at"`
	DeletedAt       gorm.DeletedAt      `json:"deleted_at,omitempty" gorm:"index"`
}

type PurchaseOrderItem struct {
	ID            uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	OrderID       uuid.UUID      `json:"order_id" gorm:"type:uuid;not null;index"`
	Description   string         `json:"description"`
	Quantity      float64        `json:"quantity" gorm:"default:1"`
	ReceivedQty   float64        `json:"received_qty" gorm:"default:0"`
	UnitPrice     float64        `json:"unit_price" gorm:"default:0"`
	TaxRate       float64        `json:"tax_rate" gorm:"default:0"`
	TaxAmount     float64        `json:"tax_amount" gorm:"default:0"`
	Total         float64        `json:"total" gorm:"default:0"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

type PurchaseReceipt struct {
	ID            uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID        uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	PurchaseOrderID *uuid.UUID   `json:"purchase_order_id" gorm:"type:uuid"`
	PartyID       uuid.UUID      `json:"party_id" gorm:"type:uuid;not null"`
	Party         Party          `json:"party,omitempty" gorm:"foreignKey:PartyID"`
	ReceiptNumber string         `json:"receipt_number" gorm:"not null;index"`
	Status        string         `json:"status" gorm:"default:'draft'"` // draft, submitted, cancelled
	ReceiptDate   time.Time      `json:"receipt_date" gorm:"not null"`
	SubTotal      float64        `json:"sub_total" gorm:"default:0"`
	TaxTotal      float64        `json:"tax_total" gorm:"default:0"`
	TotalAmount   float64        `json:"total_amount" gorm:"default:0"`
	Notes         string         `json:"notes"`
	Items         []PurchaseReceiptItem `json:"items" gorm:"foreignKey:ReceiptID;constraint:OnDelete:CASCADE;"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	DeletedAt     gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type PurchaseReceiptItem struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	ReceiptID       uuid.UUID      `json:"receipt_id" gorm:"type:uuid;not null;index"`
	Description     string         `json:"description"`
	Quantity        float64        `json:"quantity" gorm:"default:1"`
	UnitPrice       float64        `json:"unit_price" gorm:"default:0"`
	TaxRate         float64        `json:"tax_rate" gorm:"default:0"`
	TaxAmount       float64        `json:"tax_amount" gorm:"default:0"`
	Total           float64        `json:"total" gorm:"default:0"`
	BatchNo         string         `json:"batch_no"`
	MfgDate         *time.Time     `json:"mfg_date,omitempty"`
	ExpDate         *time.Time     `json:"exp_date,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

type PurchaseBill struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	PurchaseReceiptID *uuid.UUID  `json:"purchase_receipt_id" gorm:"type:uuid"`
	PartyID         uuid.UUID      `json:"party_id" gorm:"type:uuid;not null"`
	VendorID        *uuid.UUID     `json:"vendor_id,omitempty" gorm:"type:uuid"` // legacy alias for party_id
	Party           Party          `json:"party,omitempty" gorm:"foreignKey:PartyID"`
	BillNumber      string         `json:"bill_number" gorm:"not null;index"`
	BillDate        time.Time      `json:"bill_date" gorm:"not null"`
	DueDate         *time.Time     `json:"due_date,omitempty"`
	Status          string         `json:"status" gorm:"default:'unpaid'"` // unpaid, paid, partial
	SubTotal        float64        `json:"sub_total" gorm:"default:0"`
	TaxTotal        float64        `json:"tax_total" gorm:"default:0"`
	TotalAmount     float64        `json:"total_amount" gorm:"default:0"`
	PaidAmount      float64        `json:"paid_amount" gorm:"default:0"`
	BalanceDue      float64        `json:"balance_due" gorm:"default:0"`
	PaymentMode     string         `json:"payment_mode"`
	BankAccountID   *uuid.UUID     `json:"bank_account_id,omitempty" gorm:"type:uuid;index"`
	Notes           string         `json:"notes"`
	Items           []PurchaseBillItem `json:"items" gorm:"foreignKey:BillID;constraint:OnDelete:CASCADE;"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type PurchaseBillItem struct {
	ID          uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	BillID      uuid.UUID      `json:"bill_id" gorm:"type:uuid;not null;index"`
	ProductID   *uuid.UUID     `json:"product_id,omitempty" gorm:"type:uuid;index"`
	ItemCode    string         `json:"item_code"`
	Description string         `json:"description"`
	Quantity    float64        `json:"quantity" gorm:"default:1"`
	Unit        string         `json:"unit"`
	UnitPrice   float64        `json:"unit_price" gorm:"default:0"`
	Discount    float64        `json:"discount" gorm:"default:0"`
	TaxRate     float64        `json:"tax_rate" gorm:"default:18"`
	TaxAmount   float64        `json:"tax_amount" gorm:"default:0"`
	Total       float64        `json:"total" gorm:"default:0"`
	MRP         float64        `json:"mrp" gorm:"default:0"`
	SalePrice   float64        `json:"sale_price" gorm:"default:0"`
	HSNCode     string         `json:"hsn_code"`
	BatchNo     string         `json:"batch_no"`
	MfgDate     *time.Time     `json:"mfg_date,omitempty"`
	ExpDate     *time.Time     `json:"exp_date,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

type Account struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	Code            string         `json:"code" gorm:"not null;index"` // Account code (e.g., 1000, 1100)
	Name            string         `json:"name" gorm:"not null"`
	AccountType     string         `json:"account_type" gorm:"not null"` // asset, liability, equity, income, expense
	SubType         string         `json:"sub_type"` // current_asset, fixed_asset, current_liability, long_term_liability, etc.
	ParentID        *uuid.UUID     `json:"parent_id" gorm:"type:uuid"`
	Parent          *Account       `json:"parent,omitempty" gorm:"foreignKey:ParentID"`
	OpeningBalance  float64        `json:"opening_balance" gorm:"default:0"`
	Balance         float64        `json:"balance" gorm:"default:0"`
	IsDefault       bool           `json:"is_default" gorm:"default:false"`
	IsActive        bool           `json:"is_active" gorm:"default:true"`
	Description     string         `json:"description"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type JournalEntry struct {
	ID            uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID        uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	EntryNumber   string         `json:"entry_number" gorm:"not null;index"`
	EntryDate     time.Time      `json:"entry_date" gorm:"not null"`
	Description   string         `json:"description"`
	TotalDebit    float64        `json:"total_debit" gorm:"default:0"`
	TotalCredit   float64        `json:"total_credit" gorm:"default:0"`
	Status        string         `json:"status" gorm:"default:'draft'"` // draft, posted
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	DeletedAt     gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
	Lines         []JournalEntryLine `json:"lines" gorm:"foreignKey:EntryID;constraint:OnDelete:CASCADE;"`
}

type JournalEntryLine struct {
	ID          uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	EntryID     uuid.UUID      `json:"entry_id" gorm:"type:uuid;not null;index"`
	AccountID   uuid.UUID      `json:"account_id" gorm:"type:uuid;not null"`
	Account     Account        `json:"account,omitempty" gorm:"foreignKey:AccountID"`
	Debit       float64        `json:"debit" gorm:"default:0"`
	Credit      float64        `json:"credit" gorm:"default:0"`
	Description string         `json:"description"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

type Ledger struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	AccountID       uuid.UUID      `json:"account_id" gorm:"type:uuid;not null;index"`
	Account         Account        `json:"account,omitempty" gorm:"foreignKey:AccountID"`
	TransactionDate time.Time      `json:"transaction_date" gorm:"not null;index"`
	TransactionType string         `json:"transaction_type"` // journal_entry, invoice, payment, etc.
	ReferenceID     uuid.UUID      `json:"reference_id" gorm:"type:uuid"` // JournalEntryID, InvoiceID, etc.
	ReferenceNumber string         `json:"reference_number"`
	Description     string         `json:"description"`
	Debit           float64        `json:"debit" gorm:"default:0"`
	Credit          float64        `json:"credit" gorm:"default:0"`
	Balance         float64        `json:"balance" gorm:"default:0"`
	CreatedAt       time.Time      `json:"created_at"`
}

type BankReconciliation struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	BankAccountID   uuid.UUID      `json:"bank_account_id" gorm:"type:uuid;not null;index"`
	StatementDate   time.Time      `json:"statement_date" gorm:"not null"`
	StatementBalance float64      `json:"statement_balance" gorm:"default:0"`
	BookBalance     float64        `json:"book_balance" gorm:"default:0"`
	Difference      float64        `json:"difference" gorm:"default:0"`
	ReconciledItems string         `json:"reconciled_items" gorm:"type:text"` // JSON array of reconciled item IDs
	UnreconciledItems string       `json:"unreconciled_items" gorm:"type:text"` // JSON array of unreconciled item IDs
	Notes           string         `json:"notes"`
	Status          string         `json:"status" gorm:"default:'draft'"` // draft, reconciled
	ReconciledAt    *time.Time     `json:"reconciled_at,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

type CreditNote struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	InvoiceID       uuid.UUID      `json:"invoice_id" gorm:"type:uuid"`
	Invoice         *Invoice       `json:"invoice,omitempty" gorm:"foreignKey:InvoiceID"`
	PartyID         uuid.UUID      `json:"party_id" gorm:"type:uuid;not null"`
	Party           Party          `json:"party,omitempty" gorm:"foreignKey:PartyID"`
	CreditNoteNumber string        `json:"credit_note_number" gorm:"not null;index"`
	Reason          string         `json:"reason"`
	TotalAmount     float64        `json:"total_amount" gorm:"default:0"`
	RefundMode      string         `json:"refund_mode"` // cash, original_payment, credit_note
	Status          string         `json:"status" gorm:"default:'draft'"` // draft, issued
	Date            time.Time      `json:"date" gorm:"not null"`
	Items           []CreditNoteItem `json:"items" gorm:"foreignKey:CreditNoteID;constraint:OnDelete:CASCADE;"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type CreditNoteItem struct {
	ID            uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	CreditNoteID  uuid.UUID      `json:"credit_note_id" gorm:"type:uuid;not null;index"`
	InvoiceItemID uuid.UUID      `json:"invoice_item_id" gorm:"type:uuid"`
	Description   string         `json:"description"`
	Quantity      float64        `json:"quantity" gorm:"default:1"`
	UnitPrice     float64        `json:"unit_price" gorm:"default:0"`
	TaxRate       float64        `json:"tax_rate" gorm:"default:0"`
	Total         float64        `json:"total" gorm:"default:0"`
	Reason        string         `json:"reason"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

type DebitNote struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	PurchaseBillID  uuid.UUID      `json:"purchase_bill_id" gorm:"type:uuid"`
	PurchaseBill    *PurchaseBill  `json:"purchase_bill,omitempty" gorm:"foreignKey:PurchaseBillID"`
	PartyID         uuid.UUID      `json:"party_id" gorm:"type:uuid;not null"`
	Party           Party          `json:"party,omitempty" gorm:"foreignKey:PartyID"`
	DebitNoteNumber string         `json:"debit_note_number" gorm:"not null;index"`
	Reason          string         `json:"reason"`
	TotalAmount     float64        `json:"total_amount" gorm:"default:0"`
	RefundMode      string         `json:"refund_mode"` // cash, original_payment, debit_note
	Status          string         `json:"status" gorm:"default:'draft'"` // draft, issued
	Date            time.Time      `json:"date" gorm:"not null"`
	Items           []DebitNoteItem `json:"items" gorm:"foreignKey:DebitNoteID;constraint:OnDelete:CASCADE;"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type DebitNoteItem struct {
	ID                 uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	DebitNoteID        uuid.UUID      `json:"debit_note_id" gorm:"type:uuid;not null;index"`
	PurchaseBillItemID uuid.UUID      `json:"purchase_bill_item_id" gorm:"type:uuid"`
	Description        string         `json:"description"`
	Quantity           float64        `json:"quantity" gorm:"default:1"`
	UnitPrice          float64        `json:"unit_price" gorm:"default:0"`
	TaxRate            float64        `json:"tax_rate" gorm:"default:0"`
	Total              float64        `json:"total" gorm:"default:0"`
	Reason             string         `json:"reason"`
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
}

type Party struct {
	ID             uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID         uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	Name           string         `json:"name" gorm:"not null"`
	Phone          string         `json:"phone"`
	Email          string         `json:"email"`
	GSTIN          string         `json:"gstin"`
	Address        string         `json:"address"`
	City           string         `json:"city"`
	State          string         `json:"state"`
	Pincode        string         `json:"pincode"`
	StateCode      string         `json:"state_code"`
	Category       string         `json:"category"`
	PartyType      string         `json:"party_type" gorm:"not null"` // customer, vendor
	OpeningBalance float64        `json:"opening_balance" gorm:"default:0"`
	Balance        float64        `json:"balance" gorm:"default:0"`
	CreditLimit    float64        `json:"credit_limit" gorm:"default:0"`
	TAN            string         `json:"tan"`
	PAN            string         `json:"pan"`
	Notes          string         `json:"notes"`
	LoyaltyPoints  int64          `json:"loyalty_points" gorm:"default:0"`
	IsActive       bool           `json:"is_active" gorm:"default:true"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	DeletedAt      gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type LoyaltySettings struct {
	ID               uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID           uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;uniqueIndex"`
	IsEnabled        bool           `json:"is_enabled" gorm:"default:false"`
	SpendAmount      float64        `json:"spend_amount" gorm:"default:100"`       // ₹ spent to earn points
	PointsPerSpend   int64          `json:"points_per_spend" gorm:"default:1"`     // points earned per SpendAmount
	PointValue       float64        `json:"point_value" gorm:"default:1"`          // ₹ discount per point redeemed
	MinRedeemPoints  int64          `json:"min_redeem_points" gorm:"default:50"`
	MaxRedeemPercent float64        `json:"max_redeem_percent" gorm:"default:25"` // max % of bill payable with points
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
	DeletedAt        gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type LoyaltyTransaction struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	PartyID         uuid.UUID      `json:"party_id" gorm:"type:uuid;not null;index"`
	Party           Party          `json:"party,omitempty" gorm:"foreignKey:PartyID"`
	TransactionType string         `json:"transaction_type" gorm:"not null"` // earn, redeem, adjust, expire
	Points          int64          `json:"points" gorm:"not null"`           // positive = credit, negative = debit
	BalanceAfter    int64          `json:"balance_after" gorm:"not null"`
	ReferenceType   string         `json:"reference_type"` // invoice, manual
	ReferenceID     *uuid.UUID     `json:"reference_id,omitempty" gorm:"type:uuid"`
	ReferenceNumber string         `json:"reference_number"`
	Notes           string         `json:"notes"`
	CreatedAt       time.Time      `json:"created_at"`
	DeletedAt       gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type LoyaltyStats struct {
	TotalMembers           int64 `json:"total_members"`
	TotalPointsOutstanding int64 `json:"total_points_outstanding"`
	PointsEarnedThisMonth  int64 `json:"points_earned_this_month"`
	PointsRedeemedThisMonth int64 `json:"points_redeemed_this_month"`
}

type PartyStats struct {
	TotalParties  int64   `json:"total_parties"`
	ToCollect     float64 `json:"to_collect"`
	ToPay         float64 `json:"to_pay"`
}

type SalesReturn struct {
	ID                uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID            uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	PartyID           uuid.UUID      `json:"party_id" gorm:"type:uuid;not null"`
	Party             Party          `json:"party,omitempty" gorm:"foreignKey:PartyID"`
	InvoiceID         uuid.UUID      `json:"invoice_id" gorm:"type:uuid"`
	Invoice           Invoice        `json:"invoice,omitempty" gorm:"foreignKey:InvoiceID"`
	ReturnNumber      string         `json:"return_number" gorm:"not null;index"`
	Date              time.Time      `json:"date" gorm:"not null"`
	Amount            float64        `json:"amount" gorm:"default:0"`
	Status            string         `json:"status" gorm:"default:'draft'"` // draft, processed, cancelled
	Reason            string         `json:"reason"`
	RefundMode        string         `json:"refund_mode"` // cash, original_payment, credit_note
	Notes             string         `json:"notes"`
	Items             []SalesReturnItem `json:"items" gorm:"foreignKey:ReturnID;constraint:OnDelete:CASCADE;"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
	DeletedAt         gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type SalesReturnItem struct {
	ID            uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	ReturnID      uuid.UUID      `json:"return_id" gorm:"type:uuid;not null;index"`
	InvoiceItemID uuid.UUID      `json:"invoice_item_id" gorm:"type:uuid"`
	Description   string         `json:"description"`
	Quantity      float64        `json:"quantity" gorm:"default:1"`
	UnitPrice     float64        `json:"unit_price" gorm:"default:0"`
	TaxRate       float64        `json:"tax_rate" gorm:"default:0"`
	Total         float64        `json:"total" gorm:"default:0"`
	Reason        string         `json:"reason"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

type PurchaseReturn struct {
	ID                uuid.UUID           `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID            uuid.UUID           `json:"user_id" gorm:"type:uuid;not null;index"`
	PartyID           uuid.UUID           `json:"party_id" gorm:"type:uuid;not null"`
	Party             Party               `json:"party,omitempty" gorm:"foreignKey:PartyID"`
	PurchaseBillID    uuid.UUID           `json:"purchase_bill_id" gorm:"type:uuid"`
	PurchaseBill      PurchaseBill        `json:"purchase_bill,omitempty" gorm:"foreignKey:PurchaseBillID"`
	ReturnNumber      string              `json:"return_number" gorm:"not null;index"`
	Date              time.Time           `json:"date" gorm:"not null"`
	Amount            float64             `json:"amount" gorm:"default:0"`
	Status            string              `json:"status" gorm:"default:'draft'"` // draft, processed, cancelled
	Reason            string              `json:"reason"`
	RefundMode        string              `json:"refund_mode"` // cash, original_payment, credit_note
	Notes             string              `json:"notes"`
	Items             []PurchaseReturnItem `json:"items" gorm:"foreignKey:ReturnID;constraint:OnDelete:CASCADE;"`
	CreatedAt         time.Time           `json:"created_at"`
	UpdatedAt         time.Time           `json:"updated_at"`
	DeletedAt         gorm.DeletedAt      `json:"deleted_at,omitempty" gorm:"index"`
}

type PurchaseReturnItem struct {
	ID                uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	ReturnID          uuid.UUID      `json:"return_id" gorm:"type:uuid;not null;index"`
	PurchaseBillItemID uuid.UUID     `json:"purchase_bill_item_id" gorm:"type:uuid"`
	Description       string         `json:"description"`
	Quantity          float64        `json:"quantity" gorm:"default:1"`
	UnitPrice         float64        `json:"unit_price" gorm:"default:0"`
	TaxRate           float64        `json:"tax_rate" gorm:"default:0"`
	Total             float64        `json:"total" gorm:"default:0"`
	Reason            string         `json:"reason"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
}

type BankAccount struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	AccountName     string         `json:"account_name" gorm:"not null"`
	AccountNumber   string         `json:"account_number" gorm:"not null"`
	BankName        string         `json:"bank_name" gorm:"not null"`
	IFSCCode        string         `json:"ifsc_code"`
	AccountType     string         `json:"account_type" gorm:"default:'savings'"` // savings, current
	OpeningBalance float64        `json:"opening_balance" gorm:"default:0"`
	Balance         float64        `json:"balance" gorm:"default:0"`
	IsActive        bool           `json:"is_active" gorm:"default:true"`
	IsPrimary       bool           `json:"is_primary" gorm:"default:false"`
	Notes           string         `json:"notes"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type CashTransaction struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	AccountID       *uuid.UUID     `json:"account_id,omitempty" gorm:"type:uuid"`
	Account         *BankAccount   `json:"account,omitempty" gorm:"foreignKey:AccountID"`
	TransactionType string         `json:"transaction_type" gorm:"not null"` // add, reduce, transfer_in, transfer_out
	Amount          float64        `json:"amount" gorm:"not null"`
	Date            time.Time      `json:"date" gorm:"not null"`
	Description     string         `json:"description"`
	Reference       string         `json:"reference"`
	FromAccountID   *uuid.UUID     `json:"from_account_id,omitempty" gorm:"type:uuid"` // For transfers
	ToAccountID     *uuid.UUID     `json:"to_account_id,omitempty" gorm:"type:uuid"`     // For transfers
	IsLinked        bool           `json:"is_linked" gorm:"default:false"` // Whether linked to invoice/payment
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type CashBankSummary struct {
	TotalBalance      float64              `json:"total_balance"`
	CashInHand        float64              `json:"cash_in_hand"`
	BankAccounts      []BankAccount        `json:"bank_accounts"`
	UnlinkedCount     int64                `json:"unlinked_count"`
	UnlinkedAmount    float64              `json:"unlinked_amount"`
}

type PaymentMethodAccountMap struct {
	ID            uuid.UUID  `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID        uuid.UUID  `json:"user_id" gorm:"type:uuid;not null;uniqueIndex:idx_payment_method_map"`
	PaymentMethod string     `json:"payment_method" gorm:"not null;uniqueIndex:idx_payment_method_map"`
	BankAccountID *uuid.UUID `json:"bank_account_id,omitempty" gorm:"type:uuid"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type Staff struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	Name            string         `json:"name" gorm:"not null"`
	Phone           string         `json:"phone"`
	Email           string         `json:"email"`
	Address         string         `json:"address"`
	Designation     string         `json:"designation"`
	Department      string         `json:"department"`
	JoiningDate     *time.Time     `json:"joining_date,omitempty"`
	Salary          float64        `json:"salary" gorm:"default:0"`
	SalaryType      string         `json:"salary_type" gorm:"default:'monthly'"` // monthly, daily, hourly
	BankName        string         `json:"bank_name"`
	AccountNumber   string         `json:"account_number"`
	IFSCCode        string         `json:"ifsc_code"`
	AadharNumber    string         `json:"aadhar_number"`
	PANNumber       string         `json:"pan_number"`
	IsActive        bool           `json:"is_active" gorm:"default:true"`
	Notes           string         `json:"notes"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type Attendance struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	StaffID         uuid.UUID      `json:"staff_id" gorm:"type:uuid;not null;index"`
	Staff           Staff          `json:"staff,omitempty" gorm:"foreignKey:StaffID"`
	Date            time.Time      `json:"date" gorm:"not null"`
	Status          string         `json:"status" gorm:"not null"` // present, absent, half_day, paid_leave, weekly_off
	CheckInTime     *time.Time     `json:"check_in_time,omitempty"`
	CheckOutTime    *time.Time     `json:"check_out_time,omitempty"`
	WorkHours       float64        `json:"work_hours" gorm:"default:0"`
	Notes           string         `json:"notes"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type AttendanceStats struct {
	TotalStaff      int64   `json:"total_staff"`
	Present         int64   `json:"present"`
	Absent          int64   `json:"absent"`
	HalfDay         int64   `json:"half_day"`
	PaidLeave       int64   `json:"paid_leave"`
	WeeklyOff       int64   `json:"weekly_off"`
	Date            string  `json:"date"`
}

type Payroll struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	StaffID         uuid.UUID      `json:"staff_id" gorm:"type:uuid;not null;index"`
	Staff           Staff          `json:"staff,omitempty" gorm:"foreignKey:StaffID"`
	PaymentNumber   string         `json:"payment_number" gorm:"not null;index"`
	PaymentDate     time.Time      `json:"payment_date" gorm:"not null"`
	StartDate       time.Time      `json:"start_date" gorm:"not null"`
	EndDate         time.Time      `json:"end_date" gorm:"not null"`
	BasicSalary     float64        `json:"basic_salary" gorm:"default:0"`
	WorkingDays     int            `json:"working_days" gorm:"default:0"`
	PresentDays     int            `json:"present_days" gorm:"default:0"`
	AbsentDays      int            `json:"absent_days" gorm:"default:0"`
	HalfDays        int            `json:"half_days" gorm:"default:0"`
	PaidLeaveDays   int            `json:"paid_leave_days" gorm:"default:0"`
	WeeklyOffDays   int            `json:"weekly_off_days" gorm:"default:0"`
	Deductions      float64        `json:"deductions" gorm:"default:0"`
	Bonus           float64        `json:"bonus" gorm:"default:0"`
	NetSalary       float64        `json:"net_salary" gorm:"default:0"`
	PaymentMode     string         `json:"payment_mode"` // cash, bank_transfer, upi, cheque
	BankAccountID   *uuid.UUID     `json:"bank_account_id,omitempty" gorm:"type:uuid;index"` // nil = cash in-hand
	BankAccount     *BankAccount   `json:"bank_account,omitempty" gorm:"foreignKey:BankAccountID"`
	ExpenseID       *uuid.UUID     `json:"expense_id,omitempty" gorm:"type:uuid;index"`
	Reference       string         `json:"reference"`
	Notes           string         `json:"notes"`
	Status          string         `json:"status" gorm:"default:'paid'"` // paid, pending
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type StaffDeduction struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	StaffID         uuid.UUID      `json:"staff_id" gorm:"type:uuid;not null;index"`
	Staff           Staff          `json:"staff,omitempty" gorm:"foreignKey:StaffID"`
	DeductionNumber string         `json:"deduction_number" gorm:"not null;index"`
	DeductionType   string         `json:"deduction_type" gorm:"not null"` // loan, penalty, advance_recovery, tax, insurance, other
	Amount          float64        `json:"amount" gorm:"default:0"`
	Description     string         `json:"description"`
	DeductionDate   time.Time      `json:"deduction_date" gorm:"not null"`
	IsRecurring     bool           `json:"is_recurring" gorm:"default:false"`
	RecurringPeriod string         `json:"recurring_period"` // monthly, weekly, biweekly
	TotalInstallments int           `json:"total_installments" gorm:"default:0"`
	InstallmentsPaid int           `json:"installments_paid" gorm:"default:0"`
	Status          string         `json:"status" gorm:"default:'active'"` // active, completed, cancelled
	Notes           string         `json:"notes"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type StaffAdvancePayment struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	StaffID         uuid.UUID      `json:"staff_id" gorm:"type:uuid;not null;index"`
	Staff           Staff          `json:"staff,omitempty" gorm:"foreignKey:StaffID"`
	AdvanceNumber   string         `json:"advance_number" gorm:"not null;index"`
	Amount          float64        `json:"amount" gorm:"default:0"`
	Reason          string         `json:"reason"`
	AdvanceDate     time.Time      `json:"advance_date" gorm:"not null"`
	ExpectedRecoveryDate *time.Time `json:"expected_recovery_date,omitempty"`
	IsRecovered     bool           `json:"is_recovered" gorm:"default:false"`
	RecoveredAmount float64        `json:"recovered_amount" gorm:"default:0"`
	PendingAmount   float64        `json:"pending_amount" gorm:"default:0"`
	PaymentMode     string         `json:"payment_mode"` // cash, bank_transfer, upi, cheque
	Reference       string         `json:"reference"`
	Notes           string         `json:"notes"`
	Status          string         `json:"status" gorm:"default:'pending'"` // pending, partial, recovered
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type InvoiceSettings struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	Template        string         `json:"template" gorm:"default:'classic'"` // classic, modern, minimal
	PrimaryColor    string         `json:"primary_color" gorm:"default:'#2563eb'"`
	SecondaryColor  string         `json:"secondary_color" gorm:"default:'#1e40af'"`
	Theme           string         `json:"theme" gorm:"default:'light'"` // light, dark
	ShowLogo        bool           `json:"show_logo" gorm:"default:true"`
	ShowSignature   bool           `json:"show_signature" gorm:"default:false"`
	ShowBankDetails bool           `json:"show_bank_details" gorm:"default:true"`
	ShowTerms       bool           `json:"show_terms" gorm:"default:true"`
	DefaultTerms    string         `json:"default_terms"`
	InvoicePrefix   string         `json:"invoice_prefix" gorm:"default:'INV'"`
	StartingNumber  int            `json:"starting_number" gorm:"default:1"`
	Customization   string         `json:"customization" gorm:"type:text"` // JSON: InvoiceTemplateCustomization
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type PrintSettings struct {
	ID                   uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID               uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	InvoicePrintMode     string         `json:"invoice_print_mode" gorm:"default:'a4'"` // a4, thermal
	PaperSize            string         `json:"paper_size" gorm:"default:'a4'"`         // a4, letter, legal
	Orientation          string         `json:"orientation" gorm:"default:'portrait'"`  // portrait, landscape
	MarginTop            float64        `json:"margin_top" gorm:"default:0.5"`
	MarginBottom         float64        `json:"margin_bottom" gorm:"default:0.5"`
	MarginLeft           float64        `json:"margin_left" gorm:"default:0.5"`
	MarginRight          float64        `json:"margin_right" gorm:"default:0.5"`
	FontSize             int            `json:"font_size" gorm:"default:12"`
	PrintHeader          bool           `json:"print_header" gorm:"default:true"`
	PrintFooter          bool           `json:"print_footer" gorm:"default:true"`
	ThermalPrintSize     string         `json:"thermal_print_size" gorm:"default:'2inch'"` // 1inch, 1.5inch, 2inch, 3inch
	BarcodePrintMode     string         `json:"barcode_print_mode" gorm:"default:'a4'"`    // label, a4
	BarcodeLabelSize     string         `json:"barcode_label_size" gorm:"default:'2inch'"` // 1inch, 1.5inch, 2inch, 3inch (thermal label rolls)
	ThermalPrinterName   string         `json:"thermal_printer_name"`                      // OS printer name (desktop)
	DocumentPrinterName  string         `json:"document_printer_name"`                     // OS printer name for A4/PDF (desktop)
	AutoPrintOnPOS       bool           `json:"auto_print_on_pos" gorm:"default:true"`
	CreatedAt            time.Time      `json:"created_at"`
	UpdatedAt            time.Time      `json:"updated_at"`
	DeletedAt            gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type WeighingScaleSettings struct {
	ID                       uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID                   uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	Enabled                  bool           `json:"enabled" gorm:"default:false"`
	Connection               string         `json:"connection" gorm:"default:'serial'"` // serial, keyboard
	Protocol                 string         `json:"protocol" gorm:"default:'generic'"`  // generic, cas, toledo, legacy_fixed
	BaudRate                 int            `json:"baud_rate" gorm:"default:9600"`
	DataBits                 int            `json:"data_bits" gorm:"default:8"`
	StopBits                 int            `json:"stop_bits" gorm:"default:1"`
	Parity                   string         `json:"parity" gorm:"default:'none'"` // none, even, odd
	ScaleWeightUnit          string         `json:"scale_weight_unit" gorm:"default:'kg'"` // kg, g
	DecimalPlaces            int            `json:"decimal_places" gorm:"default:3"`
	MinWeight                float64        `json:"min_weight" gorm:"default:0.001"`
	TareWeight               float64        `json:"tare_weight" gorm:"default:0"`
	StableReadingsRequired   int            `json:"stable_readings_required" gorm:"default:3"`
	RequireStableWeight      bool           `json:"require_stable_weight" gorm:"default:true"`
	AutoApplyOnPos           bool           `json:"auto_apply_on_pos" gorm:"default:true"`
	AutoApplyOnInvoice       bool           `json:"auto_apply_on_invoice" gorm:"default:true"`
	CsvImportEnabled         bool           `json:"csv_import_enabled" gorm:"default:true"`
	CsvHasHeader             bool           `json:"csv_has_header" gorm:"default:true"`
	CsvDelimiter             string         `json:"csv_delimiter" gorm:"default:','"` // comma, semicolon, tab
	CsvItemMatchField        string         `json:"csv_item_match_field" gorm:"default:'auto'"` // auto, sku, item_code
	CsvItemCodeColumn        string         `json:"csv_item_code_column"`
	CsvNameColumn            string         `json:"csv_name_column"`
	CsvUnitColumn            string         `json:"csv_unit_column"`
	CsvPriceColumn           string         `json:"csv_price_column"`
	CsvExportWeightItemsOnly bool           `json:"csv_export_weight_items_only" gorm:"default:true"`
	CsvWeightColumn          string         `json:"csv_weight_column,omitempty"` // legacy DB column; use csv_price_column
	BarcodeScanEnabled       bool           `json:"barcode_scan_enabled" gorm:"default:true"`
	BarcodePrefix            string         `json:"barcode_prefix" gorm:"default:'w'"` // leading char(s), e.g. "w" → w0000112500
	BarcodePrefixStart       int            `json:"barcode_prefix_start" gorm:"default:20"` // legacy EAN range (optional fallback)
	BarcodePrefixEnd         int            `json:"barcode_prefix_end" gorm:"default:29"`
	BarcodePluDigits         int            `json:"barcode_plu_digits" gorm:"default:5"`     // item code digits after prefix
	BarcodePayloadDigits     int            `json:"barcode_payload_digits" gorm:"default:5"` // weight/price digits
	BarcodePayloadType       string         `json:"barcode_payload_type" gorm:"default:'weight_grams'"` // weight_grams, weight_kg_thousandths, price_paise
	CreatedAt                time.Time      `json:"created_at"`
	UpdatedAt                time.Time      `json:"updated_at"`
	DeletedAt                gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type Reminder struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	Title           string         `json:"title" gorm:"not null"`
	Description     string         `json:"description"`
	ReminderDate    time.Time      `json:"reminder_date" gorm:"not null"`
	ReminderType    string         `json:"reminder_type" gorm:"not null"` // payment_due, invoice_overdue, tax_filing, custom
	RelatedID       *uuid.UUID     `json:"related_id" gorm:"type:uuid"` // invoice_id, party_id, etc.
	IsCompleted     bool           `json:"is_completed" gorm:"default:false"`
	Repeat          string         `json:"repeat" gorm:"default:'once'"` // once, daily, weekly, monthly, yearly
	NextReminderDate *time.Time    `json:"next_reminder_date,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type Notification struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	Type            string         `json:"type" gorm:"not null"` // invoice_due, payment_received, overdue, custom
	Title           string         `json:"title" gorm:"not null"`
	Message         string         `json:"message" gorm:"not null"`
	Channels        string         `json:"channels" gorm:"not null"` // email, sms, whatsapp, internal (comma-separated)
	SentChannels    string         `json:"sent_channels"` // Comma-separated list of channels successfully sent
	RelatedID       *uuid.UUID     `json:"related_id" gorm:"type:uuid"` // invoice_id, party_id, etc.
	RelatedType     string         `json:"related_type"` // invoice, payment, party
	Priority        string         `json:"priority" gorm:"default:'normal'"` // low, normal, high, urgent
	Status          string         `json:"status" gorm:"default:'pending'"` // pending, sent, failed
	ScheduledAt     *time.Time     `json:"scheduled_at,omitempty"`
	SentAt          *time.Time     `json:"sent_at,omitempty"`
	ReadAt          *time.Time     `json:"read_at,omitempty"`
	IsRead          bool           `json:"is_read" gorm:"default:false"`
	ErrorMessage    string         `json:"error_message"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type NotificationTemplate struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	Name            string         `json:"name" gorm:"not null"`
	Type            string         `json:"type" gorm:"not null"` // invoice_due, payment_received, overdue, welcome
	Subject         string         `json:"subject"`
	Body            string         `json:"body" gorm:"type:text"`
	SMSBody         string         `json:"sms_body"`
	WhatsAppBody    string         `json:"whatsapp_body"`
	IsActive        bool           `json:"is_active" gorm:"default:true"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type NotificationPreference struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	NotificationType string       `json:"notification_type" gorm:"not null"` // invoice_due, payment_received, overdue, etc.
	IsEnabled       bool           `json:"is_enabled" gorm:"default:true"`
	EmailEnabled    bool           `json:"email_enabled" gorm:"default:true"`
	SMSEnabled      bool           `json:"sms_enabled" gorm:"default:false"`
	WhatsAppEnabled bool           `json:"whatsapp_enabled" gorm:"default:false"`
	InternalEnabled bool           `json:"internal_enabled" gorm:"default:true"`
	LeadDays        int            `json:"lead_days" gorm:"default:0"` // Days before due date to send
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

type CAReportSharing struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	CAEmail         string         `json:"ca_email" gorm:"not null"`
	CAName          string         `json:"ca_name"`
	AccessLevel     string         `json:"access_level" gorm:"default:'read_only'"` // read_only, full_access
	IsActive        bool           `json:"is_active" gorm:"default:true"`
	LastSharedAt    *time.Time     `json:"last_shared_at,omitempty"`
	Notes           string         `json:"notes"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type Product struct {
	ID                    uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID                uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	Name                  string         `json:"name" gorm:"not null"`
	SKU                   string         `json:"sku" gorm:"uniqueIndex"`
	ItemCode              string         `json:"item_code"`
	Category              string         `json:"category"`
	PurchasePrice         float64        `json:"purchase_price" gorm:"default:0"`
	SalePrice             float64        `json:"sale_price" gorm:"default:0"`
	MRP                   float64        `json:"mrp"`
	Unit                  string         `json:"unit" gorm:"default:'PCS'"`
	MinStock              float64        `json:"min_stock" gorm:"default:0"`
	TaxRate               float64        `json:"tax_rate" gorm:"default:18"`
	ItemType              string         `json:"item_type" gorm:"default:'product'"`
	LowStockAlert         bool           `json:"low_stock_alert" gorm:"default:true"`
	HSNCode               string         `json:"hsn_code"`
	Description           string         `json:"description"`
	Discount              string         `json:"discount" gorm:"default:''"`
	EnableBatching        bool           `json:"enable_batching" gorm:"default:false"`
	SalePriceWithTax      bool           `json:"sale_price_with_tax" gorm:"default:true"`
	PurchasePriceWithTax  bool           `json:"purchase_price_with_tax" gorm:"default:true"`
	ImageUrl              string         `json:"image_url"`
	IsActive              bool           `json:"is_active" gorm:"default:true"`
	Images                []ProductImage `json:"images,omitempty" gorm:"foreignKey:ProductID;constraint:OnDelete:CASCADE;"`
	Variants              []ProductVariant `json:"variants,omitempty" gorm:"foreignKey:ProductID;constraint:OnDelete:CASCADE;"`
	CreatedAt             time.Time      `json:"created_at"`
	UpdatedAt             time.Time      `json:"updated_at"`
	DeletedAt             gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type ProductImage struct {
	ID          uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	ProductID   uuid.UUID      `json:"product_id" gorm:"type:uuid;not null;index"`
	UserID      uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	ImageURL    string         `json:"image_url" gorm:"not null"`
	AltText     string         `json:"alt_text"`
	IsPrimary   bool           `json:"is_primary" gorm:"default:false"`
	SortOrder   int            `json:"sort_order" gorm:"default:0"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type ProductVariant struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	ProductID       uuid.UUID      `json:"product_id" gorm:"type:uuid;not null;index"`
	UserID          uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	VariantName     string         `json:"variant_name" gorm:"not null"`
	VariantSKU      string         `json:"variant_sku" gorm:"uniqueIndex"`
	VariantBarcode  string         `json:"variant_barcode"`
	Attributes      string         `json:"attributes" gorm:"type:text"` // JSON string for variant attributes (e.g., {"color": "red", "size": "M"})
	StockQty        float64        `json:"stock_qty" gorm:"default:0"`
	PurchasePrice   float64        `json:"purchase_price" gorm:"default:0"`
	SalePrice       float64        `json:"sale_price" gorm:"default:0"`
	MRP             float64        `json:"mrp"`
	IsActive        bool           `json:"is_active" gorm:"default:true"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type SerialNumber struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	ProductID       uuid.UUID      `json:"product_id" gorm:"type:uuid;not null;index"`
	SerialNumber    string         `json:"serial_number" gorm:"not null;uniqueIndex"`
	Status          string         `json:"status" gorm:"default:'in_stock'"` // in_stock, sold, returned, damaged
	WarehouseID     *uuid.UUID     `json:"warehouse_id" gorm:"type:uuid"`
	BatchNo         string         `json:"batch_no"`
	MfgDate         *time.Time     `json:"mfg_date,omitempty"`
	ExpDate         *time.Time     `json:"exp_date,omitempty"`
	CostPrice       float64        `json:"cost_price" gorm:"default:0"`
	SoldDate        *time.Time     `json:"sold_date,omitempty"`
	InvoiceID       *uuid.UUID     `json:"invoice_id" gorm:"type:uuid"`
	Notes           string         `json:"notes"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type Warehouse struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	Name            string         `json:"name" gorm:"not null"`
	Code            string         `json:"code" gorm:"uniqueIndex"`
	Address         string         `json:"address"`
	City            string         `json:"city"`
	State           string         `json:"state"`
	Pincode         string         `json:"pincode"`
	ContactPerson   string         `json:"contact_person"`
	ContactPhone    string         `json:"contact_phone"`
	ContactEmail    string         `json:"contact_email"`
	IsActive        bool           `json:"is_active" gorm:"default:true"`
	IsDefault       bool           `json:"is_default" gorm:"default:false"`
	Notes           string         `json:"notes"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type Draft struct {
	ID          uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID      uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	EntityType  string         `json:"entity_type" gorm:"not null;index"` // product, category, invoice, purchase, sales_return, purchase_return, payment_in, payment_out
	Title       string         `json:"title" gorm:"not null"`
	Data        string         `json:"data" gorm:"type:text;not null"` // JSON string of the form data
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type OfflineQueue struct {
	ID          uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID      uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	Operation   string         `json:"operation" gorm:"not null"` // create_invoice, create_payment, update_stock, etc.
	EntityType  string         `json:"entity_type" gorm:"not null"` // invoice, payment, stock, etc.
	EntityData  string         `json:"entity_data" gorm:"type:text;not null"` // JSON string of the entity data
	Status      string         `json:"status" gorm:"default:'pending'"` // pending, synced, failed
	ErrorMessage string        `json:"error_message"`
	RetryCount  int            `json:"retry_count" gorm:"default:0"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type POSDraft struct {
	ID          uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID      uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	SessionID   *uuid.UUID     `json:"session_id" gorm:"type:uuid;index"`
	Title       string         `json:"title" gorm:"not null"`
	CartData    string         `json:"cart_data" gorm:"type:text;not null"` // JSON string of cart items
	PartyID     *uuid.UUID     `json:"party_id" gorm:"type:uuid"`
	Notes       string         `json:"notes"`
	IsActive    bool           `json:"is_active" gorm:"default:true"` // For tab management
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type MediaFile struct {
	ID          uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID      uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	FileName    string         `json:"file_name" gorm:"not null"`
	OriginalName string         `json:"original_name" gorm:"not null"`
	FilePath    string         `json:"file_path" gorm:"not null;index"`
	FileSize    int64          `json:"file_size" gorm:"not null"`
	MimeType    string         `json:"mime_type" gorm:"not null"`
	StorageType string         `json:"storage_type" gorm:"default:'local'"` // local, s3
	PublicURL   string         `json:"public_url" gorm:"not null"`
	EntityType  string         `json:"entity_type"` // logo, signature, invoice_attachment, etc.
	EntityID    uuid.UUID      `json:"entity_id" gorm:"type:uuid;index"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type DeliveryChallan struct {
	ID              uuid.UUID           `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID           `json:"user_id" gorm:"type:uuid;not null;index"`
	ChallanNumber   string              `json:"challan_number" gorm:"not null;index"`
	PartyID         uuid.UUID           `json:"party_id" gorm:"type:uuid;not null"`
	Party           Party               `json:"party,omitempty" gorm:"foreignKey:PartyID"`
	InvoiceID       *uuid.UUID          `json:"invoice_id,omitempty" gorm:"type:uuid"`
	Invoice         *Invoice            `json:"invoice,omitempty" gorm:"foreignKey:InvoiceID"`
	Date            time.Time           `json:"date" gorm:"not null"`
	DueDate         *time.Time          `json:"due_date,omitempty"`
	Status          string              `json:"status" gorm:"default:'draft'"` // draft, delivered, cancelled
	SubTotal        float64             `json:"sub_total" gorm:"default:0"`
	TotalQuantity   float64             `json:"total_quantity" gorm:"default:0"`
	Notes           string              `json:"notes"`
	Terms           string              `json:"terms"`
	Signature       string              `json:"signature"` // Base64 encoded signature
	VehicleNumber   string              `json:"vehicle_number"`
	TransportMode   string              `json:"transport_mode"` // road, rail, air, sea
	Items           []DeliveryChallanItem `json:"items" gorm:"foreignKey:ChallanID;constraint:OnDelete:CASCADE;"`
	CreatedAt       time.Time           `json:"created_at"`
	UpdatedAt       time.Time           `json:"updated_at"`
	DeletedAt       gorm.DeletedAt      `json:"deleted_at,omitempty" gorm:"index"`
}

type DeliveryChallanItem struct {
	ID          uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	ChallanID   uuid.UUID      `json:"challan_id" gorm:"type:uuid;not null;index"`
	Description string         `json:"description"`
	Quantity    float64        `json:"quantity" gorm:"default:1"`
	Unit        string         `json:"unit"`
	UnitPrice   float64        `json:"unit_price" gorm:"default:0"`
	Total       float64        `json:"total" gorm:"default:0"`
	BatchNo     string         `json:"batch_no"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

type SMSMarketing struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	CampaignName    string         `json:"campaign_name" gorm:"not null"`
	Message         string         `json:"message" gorm:"not null"`
	TargetAudience  string         `json:"target_audience" gorm:"not null"` // all_customers, specific_customers, all_vendors, specific_vendors
	ScheduledDate   *time.Time     `json:"scheduled_date,omitempty"`
	SentDate        *time.Time     `json:"sent_date,omitempty"`
	Status          string         `json:"status" gorm:"default:'draft'"` // draft, scheduled, sent, failed
	TotalRecipients int            `json:"total_recipients" gorm:"default:0"`
	SentCount       int            `json:"sent_count" gorm:"default:0"`
	FailedCount     int            `json:"failed_count" gorm:"default:0"`
	Notes           string         `json:"notes"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type SMSRecipient struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	CampaignID      uuid.UUID      `json:"campaign_id" gorm:"type:uuid;not null;index"`
	Campaign        SMSMarketing   `json:"campaign,omitempty" gorm:"foreignKey:CampaignID"`
	PartyID         *uuid.UUID     `json:"party_id,omitempty" gorm:"type:uuid"`
	Party           *Party         `json:"party,omitempty" gorm:"foreignKey:PartyID"`
	PhoneNumber     string         `json:"phone_number" gorm:"not null"`
	Status          string         `json:"status" gorm:"default:'pending'"` // pending, sent, failed
	ErrorMessage    string         `json:"error_message"`
	SentAt          *time.Time     `json:"sent_at,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

type EmailMarketing struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	CampaignName    string         `json:"campaign_name" gorm:"not null"`
	Subject         string         `json:"subject" gorm:"not null"`
	Body            string         `json:"body" gorm:"not null"`
	TargetAudience  string         `json:"target_audience" gorm:"not null"` // all_customers, specific_customers, all_vendors, specific_vendors
	ScheduledDate   *time.Time     `json:"scheduled_date,omitempty"`
	SentDate        *time.Time     `json:"sent_date,omitempty"`
	Status          string         `json:"status" gorm:"default:'draft'"` // draft, scheduled, sent, failed
	TotalRecipients int            `json:"total_recipients" gorm:"default:0"`
	SentCount       int            `json:"sent_count" gorm:"default:0"`
	FailedCount     int            `json:"failed_count" gorm:"default:0"`
	OpenedCount     int            `json:"opened_count" gorm:"default:0"`
	ClickedCount    int            `json:"clicked_count" gorm:"default:0"`
	Notes           string         `json:"notes"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type EmailRecipient struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	CampaignID      uuid.UUID      `json:"campaign_id" gorm:"type:uuid;not null;index"`
	Campaign        EmailMarketing `json:"campaign,omitempty" gorm:"foreignKey:CampaignID"`
	PartyID         *uuid.UUID     `json:"party_id,omitempty" gorm:"type:uuid"`
	Party           *Party         `json:"party,omitempty" gorm:"foreignKey:PartyID"`
	EmailAddress    string         `json:"email_address" gorm:"not null"`
	Status          string         `json:"status" gorm:"default:'pending'"` // pending, sent, failed, opened, clicked
	ErrorMessage    string         `json:"error_message"`
	SentAt          *time.Time     `json:"sent_at,omitempty"`
	OpenedAt        *time.Time     `json:"opened_at,omitempty"`
	ClickedAt       *time.Time     `json:"clicked_at,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

type WhatsAppMarketing struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	CampaignName    string         `json:"campaign_name" gorm:"not null"`
	Message         string         `json:"message" gorm:"not null"`
	MediaURL        string         `json:"media_url"` // For image/video messages
	TargetAudience  string         `json:"target_audience" gorm:"not null"` // all_customers, specific_customers, all_vendors, specific_vendors
	ScheduledDate   *time.Time     `json:"scheduled_date,omitempty"`
	SentDate        *time.Time     `json:"sent_date,omitempty"`
	Status          string         `json:"status" gorm:"default:'draft'"` // draft, scheduled, sent, failed
	TotalRecipients int            `json:"total_recipients" gorm:"default:0"`
	SentCount       int            `json:"sent_count" gorm:"default:0"`
	FailedCount     int            `json:"failed_count" gorm:"default:0"`
	DeliveredCount  int            `json:"delivered_count" gorm:"default:0"`
	ReadCount       int            `json:"read_count" gorm:"default:0"`
	Notes           string         `json:"notes"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type WhatsAppRecipient struct {
	ID              uuid.UUID        `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	CampaignID      uuid.UUID        `json:"campaign_id" gorm:"type:uuid;not null;index"`
	Campaign        WhatsAppMarketing `json:"campaign,omitempty" gorm:"foreignKey:CampaignID"`
	PartyID         *uuid.UUID       `json:"party_id,omitempty" gorm:"type:uuid"`
	Party           *Party           `json:"party,omitempty" gorm:"foreignKey:PartyID"`
	PhoneNumber     string           `json:"phone_number" gorm:"not null"`
	Status          string           `json:"status" gorm:"default:'pending'"` // pending, sent, delivered, read, failed
	ErrorMessage    string           `json:"error_message"`
	SentAt          *time.Time       `json:"sent_at,omitempty"`
	DeliveredAt     *time.Time       `json:"delivered_at,omitempty"`
	ReadAt          *time.Time       `json:"read_at,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

type AuditLog struct {
	ID          uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID      uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	UserName    string         `json:"user_name"`
	Action      string         `json:"action" gorm:"not null;index"` // create, update, delete, view, login, logout, export, etc.
	EntityType  string         `json:"entity_type" gorm:"not null;index"` // invoice, payment, expense, party, etc.
	EntityID    *uuid.UUID     `json:"entity_id" gorm:"type:uuid;index"`
	EntityName  string         `json:"entity_name"` // Human-readable name of the entity
	Description string         `json:"description" gorm:"type:text"` // Detailed description of the action
	IPAddress   string         `json:"ip_address"`
	UserAgent   string         `json:"user_agent"`
	Changes     string         `json:"changes" gorm:"type:text"` // JSON string of changes made
	Status      string         `json:"status" gorm:"default:'success'"` // success, failed
	ErrorMessage string        `json:"error_message"`
	CreatedAt   time.Time      `json:"created_at" gorm:"index"`
}

type AuditLogStats struct {
	TotalLogs      int64   `json:"total_logs"`
	TodayLogs      int64   `json:"today_logs"`
	SuccessCount   int64   `json:"success_count"`
	FailedCount    int64   `json:"failed_count"`
	TopActions     []ActionCount `json:"top_actions"`
	TopUsers       []UserActionCount `json:"top_users"`
}

type ActionCount struct {
	Action string `json:"action"`
	Count  int64  `json:"count"`
}

type UserActionCount struct {
	UserName string `json:"user_name"`
	Count    int64  `json:"count"`
}

type Role struct {
	ID          uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID      uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	Name        string         `json:"name" gorm:"not null"`
	Description string         `json:"description"`
	IsDefault   bool           `json:"is_default" gorm:"default:false"`
	IsActive    bool           `json:"is_active" gorm:"default:true"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
	Permissions []Permission   `json:"permissions,omitempty" gorm:"many2many:role_permissions;"`
}

type Permission struct {
	ID          uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	Name        string         `json:"name" gorm:"not null;uniqueIndex"`
	Resource    string         `json:"resource" gorm:"not null"` // invoices, payments, parties, etc.
	Action      string         `json:"action" gorm:"not null"` // create, read, update, delete, export
	Description string         `json:"description"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

type UserRole struct {
	ID        uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID    uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	RoleID    uuid.UUID      `json:"role_id" gorm:"type:uuid;not null;index"`
	Role      Role          `json:"role,omitempty" gorm:"foreignKey:RoleID"`
	CreatedAt time.Time      `json:"created_at"`
}

type IPRestriction struct {
	ID          uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID      uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	IPAddress   string         `json:"ip_address" gorm:"not null;index"`
	Description string         `json:"description"`
	IsAllowed   bool           `json:"is_allowed" gorm:"default:true"` // true for whitelist, false for blacklist
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type DataBackup struct {
	ID          uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID      uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	FileName    string         `json:"file_name" gorm:"not null"`
	FilePath    string         `json:"file_path" gorm:"not null"`
	FileSize    int64          `json:"file_size"`
	Type        string         `json:"type" gorm:"not null"` // full, incremental
	Status      string         `json:"status" gorm:"default:'completed'"` // pending, completed, failed
	Description string         `json:"description"`
	CreatedAt   time.Time      `json:"created_at"`
}

type GDPRRequest struct {
	ID          uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID      uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	RequestType string         `json:"request_type" gorm:"not null"` // data_export, data_deletion
	Status      string         `json:"status" gorm:"default:'pending'"` // pending, processing, completed, rejected
	Reason      string         `json:"reason"`
	ProcessedAt *time.Time     `json:"processed_at,omitempty"`
	FilePath    string         `json:"file_path"` // For data export
	Notes       string         `json:"notes"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

// GST Compliance Models
type TaxPeriod struct {
	ID          uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID      uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	Period      string         `json:"period" gorm:"not null;index"` // Format: YYYY-MM (e.g., 2024-01)
	StartDate   time.Time      `json:"start_date" gorm:"not null"`
	EndDate     time.Time      `json:"end_date" gorm:"not null"`
	Status      string         `json:"status" gorm:"default:'open'"` // open, closed, filed
	GSTR1Status string         `json:"gstr1_status" gorm:"default:'not_filed'"` // not_filed, filed, late_filed
	GSTR3BStatus string         `json:"gstr3b_status" gorm:"default:'not_filed'"` // not_filed, filed, late_filed
	GSTR1FiledAt *time.Time     `json:"gstr1_filed_at,omitempty"`
	GSTR3BFiledAt *time.Time    `json:"gstr3b_filed_at,omitempty"`
	Notes       string         `json:"notes"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type InputTaxCredit struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	TaxPeriod       string         `json:"tax_period" gorm:"not null;index"` // Format: YYYY-MM
	SourceID        uuid.UUID      `json:"source_id" gorm:"type:uuid;not null"` // PurchaseReceiptID, PurchaseBillID
	SourceType      string         `json:"source_type" gorm:"not null"` // purchase_receipt, purchase_bill
	CGSTAvailable   float64        `json:"cgst_available" gorm:"default:0"`
	SGSTAvailable   float64        `json:"sgst_available" gorm:"default:0"`
	IGSTAvailable   float64        `json:"igst_available" gorm:"default:0"`
	CGSTUtilized    float64        `json:"cgst_utilized" gorm:"default:0"`
	SGSTUtilized    float64        `json:"sgst_utilized" gorm:"default:0"`
	IGSTUtilized    float64        `json:"igst_utilized" gorm:"default:0"`
	CGSTExpired     float64        `json:"cgst_expired" gorm:"default:0"`
	SGSTExpired     float64        `json:"sgst_expired" gorm:"default:0"`
	IGSTExpired     float64        `json:"igst_expired" gorm:"default:0"`
	EligibleDate    time.Time      `json:"eligible_date"` // Date when ITC becomes eligible
	ExpiryDate      *time.Time     `json:"expiry_date,omitempty"` // Date when ITC expires
	Status          string         `json:"status" gorm:"default:'available'"` // available, utilized, expired, blocked
	Notes           string         `json:"notes"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type GSTFilingStatus struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	TaxPeriod       string         `json:"tax_period" gorm:"not null;index"`
	ReturnType      string         `json:"return_type" gorm:"not null"` // GSTR1, GSTR3B, GSTR5
	Status          string         `json:"status" gorm:"default:'not_filed'"` // not_filed, ready_to_file, filed, rejected, late_filed
	FilingDate      *time.Time     `json:"filing_date,omitempty"`
	ARN             string         `json:"arn"` // Acknowledgement Reference Number
	ReferenceNumber string         `json:"reference_number"`
	TotalTaxLiability float64      `json:"total_tax_liability" gorm:"default:0"`
	TaxPaid         float64        `json:"tax_paid" gorm:"default:0"`
	Interest        float64        `json:"interest" gorm:"default:0"`
	Penalty         float64        `json:"penalty" gorm:"default:0"`
	TotalAmountPaid float64        `json:"total_amount_paid" gorm:"default:0"`
	ErrorMessage    string         `json:"error_message"`
	Notes           string         `json:"notes"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type GSTR1Data struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	TaxPeriod       string         `json:"tax_period" gorm:"not null;index"`
	Section         string         `json:"section" gorm:"not null"` // B2B, B2C, B2CL, EXP, AT, etc.
	InvoiceID       uuid.UUID      `json:"invoice_id" gorm:"type:uuid"`
	InvoiceNumber   string         `json:"invoice_number"`
	InvoiceDate     time.Time      `json:"invoice_date"`
	PartyGSTIN      string         `json:"party_gstin"`
	PartyName       string         `json:"party_name"`
	PlaceOfSupply   string         `json:"place_of_supply"`
	ReverseCharge   bool           `json:"reverse_charge"`
	InvoiceType     string         `json:"invoice_type"` // Regular, SEZ, Deemed Export
	TaxableValue    float64        `json:"taxable_value"`
	CGST            float64        `json:"cgst"`
	SGST            float64        `json:"sgst"`
	IGST            float64        `json:"igst"`
	CESS            float64        `json:"cess"`
	TotalInvoiceValue float64      `json:"total_invoice_value"`
	HSNCode         string         `json:"hsn_code"`
	Description     string         `json:"description"`
	ExportType      string         `json:"export_type"` // WPAY, WOPAY
	ShippingBillNumber string      `json:"shipping_bill_number"`
	ShippingBillDate time.Time     `json:"shipping_bill_date"`
	PortCode        string         `json:"port_code"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type GSTR3BData struct {
	ID                    uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID                uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	TaxPeriod             string         `json:"tax_period" gorm:"not null;index"`
	
	// Outward Supplies
	SuppliesTaxable       float64        `json:"supplies_taxable"`
	SuppliesNilRated      float64        `json:"supplies_nil_rated"`
	SuppliesExempted      float64        `json:"supplies_exempted"`
	SuppliesNonGST        float64        `json:"supplies_non_gst"`
	TotalOutwardSupplies  float64        `json:"total_outward_supplies"`
	
	// Inward Supplies (Reverse Charge)
	InwardTaxable         float64        `json:"inward_taxable"`
	InwardNilRated        float64        `json:"inward_nil_rated"`
	InwardExempted        float64        `json:"inward_exempted"`
	InwardNonGST          float64        `json:"inward_non_gst"`
	TotalInwardSupplies   float64        `json:"total_inward_supplies"`
	
	// Tax Liability
	IGSTLiability         float64        `json:"igst_liability"`
	CGSTLiability         float64        `json:"cgst_liability"`
	SGSTLiability         float64        `json:"sgst_liability"`
	CESSLiability         float64        `json:"cess_liability"`
	TotalTaxLiability     float64        `json:"total_tax_liability"`
	
	// ITC Available
	IGSTAvailable         float64        `json:"igst_available"`
	CGSTAvailable         float64        `json:"cgst_available"`
	SGSTAvailable         float64        `json:"sgst_available"`
	CESSAvailable         float64        `json:"cess_available"`
	TotalITCAvailable     float64        `json:"total_itc_available"`
	
	// ITC Reversal
	IGSTReversal          float64        `json:"igst_reversal"`
	CGSTReversal          float64        `json:"cgst_reversal"`
	SGSTReversal          float64        `json:"sgst_reversal"`
	CESSReversal          float64        `json:"cess_reversal"`
	TotalITCReversal      float64        `json:"total_itc_reversal"`
	
	// Net ITC
	NetIGST               float64        `json:"net_igst"`
	NetCGST               float64        `json:"net_cgst"`
	NetSGST               float64        `json:"net_sgst"`
	NetCESS               float64        `json:"net_cess"`
	TotalNetITC           float64        `json:"total_net_itc"`
	
	// Tax Payable
	IGSTPayable           float64        `json:"igst_payable"`
	CGSTPayable           float64        `json:"cgst_payable"`
	SGSTPayable           float64        `json:"sgst_payable"`
	CESSPayable           float64        `json:"cess_payable"`
	TotalTaxPayable       float64        `json:"total_tax_payable"`
	
	// Cash Ledger
	OpeningBalance        float64        `json:"opening_balance"`
	TaxDeposited          float64        `json:"tax_deposited"`
	TaxPaid               float64        `json:"tax_paid"`
	ClosingBalance        float64        `json:"closing_balance"`
	
	// Interest and Late Fee
	Interest              float64        `json:"interest"`
	LateFee               float64        `json:"late_fee"`
	Penalty               float64        `json:"penalty"`
	
	// Payment Details
	PaymentMode           string         `json:"payment_mode"`
}

type TaxExemption struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	Name            string         `json:"name" gorm:"not null"`
	Code            string         `json:"code" gorm:"uniqueIndex"`
	Description     string         `json:"description"`
	ExemptionType   string         `json:"exemption_type" gorm:"not null"` // nil_rated, exempted, non_gst
	MaxAmount       float64        `json:"max_amount"` // Maximum amount for exemption (0 = unlimited)
	ValidFrom       *time.Time     `json:"valid_from,omitempty"`
	ValidUntil      *time.Time     `json:"valid_until,omitempty"`
	IsApplicable    bool           `json:"is_applicable" gorm:"default:true"`
	Notes           string         `json:"notes"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type TaxRule struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	Country         string         `json:"country" gorm:"not null"`
	CountryCode     string         `json:"country_code" gorm:"not null"` // ISO 3166-1 alpha-2
	State           string         `json:"state"`
	StateCode       string         `json:"state_code"`
	TaxType         string         `json:"tax_type" gorm:"not null"` // gst, vat, sales_tax, service_tax
	TaxName         string         `json:"tax_name" gorm:"not null"`
	Rate            float64        `json:"rate" gorm:"not null"` // Tax rate percentage
	IsCompound      bool           `json:"is_compound" gorm:"default:false"` // If true, applies on top of other taxes
	ThresholdAmount float64        `json:"threshold_amount"` // Minimum amount for tax to apply
	HSNCode         string         `json:"hsn_code"` // Applicable to specific HSN codes
	Category        string         `json:"category"` // Applicable to specific categories
	EffectiveFrom   *time.Time     `json:"effective_from,omitempty"`
	EffectiveUntil  *time.Time     `json:"effective_until,omitempty"`
	IsActive        bool           `json:"is_active" gorm:"default:true"`
	Notes           string         `json:"notes"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type TaxRate struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	Name            string         `json:"name" gorm:"not null"`
	CGSTRate        float64        `json:"cgst_rate" gorm:"default:0"`
	SGSTRate        float64        `json:"sgst_rate" gorm:"default:0"`
	IGSTRate        float64        `json:"igst_rate" gorm:"default:0"`
	CESSRate        float64        `json:"cess_rate" gorm:"default:0"`
	TotalRate       float64        `json:"total_rate" gorm:"not null"`
	IsDefault       bool           `json:"is_default" gorm:"default:false"`
	Description     string         `json:"description"`
	IsActive        bool           `json:"is_active" gorm:"default:true"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type Quotation struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	QuotationNumber string         `json:"quotation_number" gorm:"not null;index"`
	PartyID         uuid.UUID      `json:"party_id" gorm:"type:uuid;not null"`
	Date            time.Time      `json:"date" gorm:"not null"`
	ValidUntil      *time.Time     `json:"valid_until,omitempty"`
	PaymentTerms    int            `json:"payment_terms" gorm:"default:0"`
	Status          string         `json:"status" gorm:"default:'draft'"` // draft, sent, accepted, rejected, expired, converted
	ApprovalStatus  string         `json:"approval_status" gorm:"default:'pending'"` // pending, approved, rejected
	ApprovedBy      *uuid.UUID     `json:"approved_by" gorm:"type:uuid"`
	ApprovedAt      *time.Time     `json:"approved_at,omitempty"`
	SubTotal        float64        `json:"sub_total" gorm:"default:0"`
	DiscountTotal   float64        `json:"discount_total" gorm:"default:0"`
	QuotationDiscount float64      `json:"quotation_discount" gorm:"default:0"`
	AdditionalCharges float64      `json:"additional_charges" gorm:"default:0"`
	TaxTotal        float64        `json:"tax_total" gorm:"default:0"`
	CGSTTotal       float64        `json:"cgst_total" gorm:"default:0"`
	SGSTTotal       float64        `json:"sgst_total" gorm:"default:0"`
	IGSTTotal       float64        `json:"igst_total" gorm:"default:0"`
	RoundOff        float64        `json:"round_off" gorm:"default:0"`
	TotalAmount     float64        `json:"total_amount" gorm:"default:0"`
	Notes           string         `json:"notes"`
	Terms           string         `json:"terms"`
	IsInterState    bool           `json:"is_inter_state" gorm:"default:false"`
	PlaceOfSupply   string         `json:"place_of_supply"`
	ReverseCharge   bool           `json:"reverse_charge" gorm:"default:false"`
	Signature       string         `json:"signature"`
	ConvertedToInvoiceID *uuid.UUID `json:"converted_to_invoice_id" gorm:"type:uuid"`
	ConvertedAt     *time.Time     `json:"converted_at,omitempty"`
	Version         int            `json:"version" gorm:"default:1"`
	Party           Party          `json:"party,omitempty" gorm:"foreignKey:PartyID"`
	Items           []QuotationItem `json:"items" gorm:"foreignKey:QuotationID;constraint:OnDelete:CASCADE;"`
	Versions        []QuotationVersion `json:"versions,omitempty" gorm:"foreignKey:QuotationID;constraint:OnDelete:CASCADE;"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type QuotationItem struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	QuotationID     uuid.UUID      `json:"quotation_id" gorm:"type:uuid;not null;index"`
	Description     string         `json:"description"`
	Quantity        float64        `json:"quantity" gorm:"default:1"`
	Unit            string         `json:"unit"`
	UnitPrice       float64        `json:"unit_price" gorm:"default:0"`
	Discount        float64        `json:"discount" gorm:"default:0"`
	TaxRate         float64        `json:"tax_rate" gorm:"default:18"`
	CGST            float64        `json:"cgst" gorm:"default:0"`
	SGST            float64        `json:"sgst" gorm:"default:0"`
	IGST            float64        `json:"igst" gorm:"default:0"`
	Total           float64        `json:"total" gorm:"default:0"`
	HSNCode         string         `json:"hsn_code"`
	SACCode         string         `json:"sac_code"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

type QuotationVersion struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	QuotationID     uuid.UUID      `json:"quotation_id" gorm:"type:uuid;not null;index"`
	VersionNumber   int            `json:"version_number" gorm:"not null"`
	QuotationData   string         `json:"quotation_data" gorm:"type:text"` // JSON string of quotation data
	ChangeReason    string         `json:"change_reason"`
	CreatedBy       uuid.UUID      `json:"created_by" gorm:"type:uuid"`
	CreatedAt       time.Time      `json:"created_at"`
}

type CustomerStatement struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	PartyID         uuid.UUID      `json:"party_id" gorm:"type:uuid;not null;index"`
	StatementNumber string         `json:"statement_number" gorm:"not null;index"`
	FromDate        time.Time      `json:"from_date" gorm:"not null"`
	ToDate          time.Time      `json:"to_date" gorm:"not null"`
	OpeningBalance  float64        `json:"opening_balance" gorm:"default:0"`
	TotalInvoices   float64        `json:"total_invoices" gorm:"default:0"`
	TotalPayments   float64        `json:"total_payments" gorm:"default:0"`
	TotalCredits    float64        `json:"total_credits" gorm:"default:0"`
	TotalDebits     float64        `json:"total_debits" gorm:"default:0"`
	ClosingBalance  float64        `json:"closing_balance" gorm:"default:0"`
	Notes           string         `json:"notes"`
	GeneratedAt     time.Time      `json:"generated_at"`
	Party           Party          `json:"party,omitempty" gorm:"foreignKey:PartyID"`
	Transactions    []StatementTransaction `json:"transactions,omitempty" gorm:"foreignKey:StatementID;constraint:OnDelete:CASCADE;"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type StatementTransaction struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	StatementID     uuid.UUID      `json:"statement_id" gorm:"type:uuid;not null;index"`
	TransactionType string         `json:"transaction_type" gorm:"not null"` // invoice, payment, credit_note, debit_note
	ReferenceID     uuid.UUID      `json:"reference_id" gorm:"type:uuid"` // InvoiceID, PaymentID, etc.
	ReferenceNumber string         `json:"reference_number"`
	Date            time.Time      `json:"date" gorm:"not null"`
	Description     string         `json:"description"`
	Debit           float64        `json:"debit" gorm:"default:0"`
	Credit          float64        `json:"credit" gorm:"default:0"`
	Balance         float64        `json:"balance" gorm:"default:0"`
	DueDate         *time.Time     `json:"due_date,omitempty"`
	IsOverdue       bool           `json:"is_overdue" gorm:"default:false"`
	CreatedAt       time.Time      `json:"created_at"`
}

type DeveloperSettings struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID          uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`

	// Email Service Configuration
	EmailProvider   string         `json:"email_provider" gorm:"default:'smtp'"` // smtp, sendgrid, ses, mailgun
	SMTPHost        string         `json:"smtp_host"`
	SMTPPort        int            `json:"smtp_port"`
	SMTPUsername    string         `json:"smtp_username"`
	SMTPPassword    string         `json:"-"` // Decrypted value for API responses
	FromEmail       string         `json:"from_email"`
	FromName        string         `json:"from_name"`
	SendGridAPIKey  string         `json:"-"` // Decrypted value for API responses
	SESAccessKey   string         `json:"-"` // Decrypted value for API responses
	SESSecretKey   string         `json:"-"` // Decrypted value for API responses
	MailgunAPIKey   string         `json:"-"` // Decrypted value for API responses
	MailgunDomain   string         `json:"mailgun_domain"`

	// WhatsApp Service Configuration
	WhatsAppProvider string         `json:"whatsapp_provider" gorm:"default:'meta'"` // meta, twilio
	WhatsAppAPIKey   string         `json:"-"` // Decrypted value for API responses
	WhatsAppPhoneNumberID string    `json:"whatsapp_phone_number_id"`
	WhatsAppBusinessAccountID string `json:"whatsapp_business_account_id"`

	// Twilio Configuration
	TwilioAccountSID string         `json:"twilio_account_sid"`
	TwilioAuthToken  string         `json:"-"` // Decrypted value for API responses
	TwilioPhoneNumber string        `json:"twilio_phone_number"`

	// SMS Service Configuration
	SMSProvider      string         `json:"sms_provider" gorm:"default:'twilio'"` // twilio, msg91, textlocal, aws_sns, sendgrid
	TwilioSMSAccountSID string       `json:"twilio_sms_account_sid"`
	TwilioSMSAuthToken string        `json:"-"` // Decrypted value for API responses
	TwilioSMSPhoneNumber string       `json:"twilio_sms_phone_number"`
	Msg91SenderID    string         `json:"msg91_sender_id"`
	Msg91AuthKey     string         `json:"-"` // Decrypted value for API responses
	TextLocalSenderID string         `json:"textlocal_sender_id"`
	TextLocalAPIKey  string         `json:"-"` // Decrypted value for API responses
	
	// AWS SNS Configuration
	AWSAccessKey     string         `json:"aws_access_key"`
	AWSSecretKey     string         `json:"-"` // Decrypted value for API responses
	AWSRegion        string         `json:"aws_region"`
	
	// SendGrid SMS Configuration
	SendGridSMSAPIKey string         `json:"-"` // Decrypted value for API responses

	// Encrypted fields (database storage only)
	EncryptedSMTPPassword    string `json:"-" gorm:"column:smtp_password"`
	EncryptedSendGridAPIKey  string `json:"-" gorm:"column:sendgrid_api_key"`
	EncryptedSESAccessKey    string `json:"-" gorm:"column:ses_access_key"`
	EncryptedSESSecretKey    string `json:"-" gorm:"column:ses_secret_key"`
	EncryptedMailgunAPIKey   string `json:"-" gorm:"column:mailgun_api_key"`
	EncryptedWhatsAppAPIKey  string `json:"-" gorm:"column:whatsapp_api_key"`
	EncryptedTwilioAccountSID string `json:"-" gorm:"column:twilio_account_sid"`
	EncryptedTwilioAuthToken string `json:"-" gorm:"column:twilio_auth_token"`
	EncryptedTwilioSMSAccountSID string `json:"-" gorm:"column:twilio_sms_account_sid"`
	EncryptedTwilioSMSAuthToken string `json:"-" gorm:"column:twilio_sms_auth_token"`
	EncryptedMsg91AuthKey     string `json:"-" gorm:"column:msg91_auth_key"`
	EncryptedTextLocalAPIKey  string `json:"-" gorm:"column:textlocal_api_key"`
	EncryptedAWSSecretKey     string `json:"-" gorm:"column:aws_secret_key"`
	EncryptedSendGridSMSAPIKey string `json:"-" gorm:"column:sendgrid_sms_api_key"`

	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type CustomerPortalSettings struct {
	ID                  uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID              uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;uniqueIndex"`
	IsEnabled           bool           `json:"is_enabled" gorm:"default:false"`
	Slug                string         `json:"slug" gorm:"not null;index"`
	WelcomeMessage      string         `json:"welcome_message"`
	AllowSupportTickets bool           `json:"allow_support_tickets" gorm:"default:true"`
	CreatedAt           time.Time      `json:"created_at"`
	UpdatedAt           time.Time      `json:"updated_at"`
	DeletedAt           gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type CustomerPortalAccess struct {
	ID          uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID      uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	PartyID     uuid.UUID      `json:"party_id" gorm:"type:uuid;not null;uniqueIndex:idx_portal_party"`
	Party       Party          `json:"party,omitempty" gorm:"foreignKey:PartyID"`
	PINHash     string         `json:"-" gorm:"not null"`
	IsEnabled   bool           `json:"is_enabled" gorm:"default:true"`
	LastLoginAt *time.Time     `json:"last_login_at,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

type SupportTicket struct {
	ID           uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID       uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	PartyID      uuid.UUID      `json:"party_id" gorm:"type:uuid;not null;index"`
	Party        Party          `json:"party,omitempty" gorm:"foreignKey:PartyID"`
	TicketNumber string         `json:"ticket_number" gorm:"not null;index"`
	Subject      string         `json:"subject" gorm:"not null"`
	Description  string         `json:"description" gorm:"type:text"`
	Status       string         `json:"status" gorm:"default:'open'"` // open, in_progress, resolved, closed
	AdminNotes   string         `json:"admin_notes" gorm:"type:text"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	DeletedAt    gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

// SavedInvoiceTemplate stores reusable invoice line-item / terms presets.
type SavedInvoiceTemplate struct {
	ID          uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID      uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	Name        string         `json:"name" gorm:"not null"`
	Description string         `json:"description"`
	Payload     string         `json:"payload" gorm:"type:text;not null"` // JSON: items, terms, notes, payment_terms, etc.
	IsDefault   bool           `json:"is_default" gorm:"default:false"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

// InvoiceCustomFieldDefinition defines extra fields shown on invoice forms.
type InvoiceCustomFieldDefinition struct {
	ID         uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID     uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	Label      string         `json:"label" gorm:"not null"`
	FieldKey   string         `json:"field_key" gorm:"not null;index"`
	FieldType  string         `json:"field_type" gorm:"default:'text'"` // text, number, date, boolean
	IsRequired bool           `json:"is_required" gorm:"default:false"`
	ShowOnPDF  bool           `json:"show_on_pdf" gorm:"default:true"`
	SortOrder  int            `json:"sort_order" gorm:"default:0"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
	DeletedAt  gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

// InvoiceStatusHistory tracks invoice status changes over time.
type InvoiceStatusHistory struct {
	ID         uuid.UUID `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	InvoiceID  uuid.UUID `json:"invoice_id" gorm:"type:uuid;not null;index"`
	UserID     uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index"`
	FromStatus string    `json:"from_status"`
	ToStatus   string    `json:"to_status" gorm:"not null"`
	Note       string    `json:"note"`
	ChangedBy  string    `json:"changed_by"`
	CreatedAt  time.Time `json:"created_at"`
}

// PageFeatureSettings stores which app pages/menus are enabled (system-wide singleton).
// PagesJSON is a JSON object of route key -> enabled bool, e.g. {"/pos":true,"/loyalty":false}.
type PageFeatureSettings struct {
	ID        uuid.UUID      `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	PagesJSON string         `json:"-" gorm:"column:pages_json;type:text"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}
