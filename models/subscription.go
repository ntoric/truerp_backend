package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// SubscriptionPlan is a platform-level plan the super admin sells to stores
// (not store-scoped). Features is a JSON array of strings shown on the plan
// card; plan-based menu access will key off it in future.
type SubscriptionPlan struct {
	ID          uuid.UUID `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	Name        string    `json:"name" gorm:"not null"`
	Description string    `json:"description" gorm:"type:text"`
	// BillingCycle: monthly or annual.
	BillingCycle string  `json:"billing_cycle" gorm:"not null;default:'monthly'"`
	Price        float64 `json:"price" gorm:"not null;default:0"`
	Currency     string  `json:"currency" gorm:"not null;default:'INR'"`
	Features     string  `json:"features" gorm:"type:text"` // JSON array of strings
	// Highlight marks the plan as recommended (premium accent on cards).
	Highlight bool           `json:"highlight" gorm:"default:false"`
	IsActive  bool           `json:"is_active"`
	SortOrder int            `json:"sort_order" gorm:"default:0"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

// StoreSubscription assigns a plan to a store. Plan name, cycle, price and
// features are snapshotted at assignment so later plan edits don't rewrite a
// store's history.
//
// Dates are calendar dates (YYYY-MM-DD) in the store owner's timezone. A
// period covers [CurrentPeriodStart, CurrentPeriodEnd); CurrentPeriodEnd is
// the next renewal date.
type StoreSubscription struct {
	ID      uuid.UUID  `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	StoreID uuid.UUID  `json:"store_id" gorm:"type:uuid;not null;index"`
	PlanID  *uuid.UUID `json:"plan_id,omitempty" gorm:"type:uuid;index"`

	PlanName     string  `json:"plan_name" gorm:"not null"`
	BillingCycle string  `json:"billing_cycle" gorm:"not null"`
	Price        float64 `json:"price" gorm:"not null;default:0"`
	Currency     string  `json:"currency" gorm:"not null;default:'INR'"`
	Features     string  `json:"features" gorm:"type:text"`

	// Status: active or cancelled.
	Status    string `json:"status" gorm:"not null;default:'active';index"`
	AutoRenew bool   `json:"auto_renew"`

	// IsComplimentary gives the plan free of charge: cycles up to
	// ComplimentaryUntilCycle (0 = every cycle) are created at no cost. Later
	// cycles bill at Price, the regular price.
	IsComplimentary         bool   `json:"is_complimentary" gorm:"index"`
	ComplimentaryUntilCycle int    `json:"complimentary_until_cycle"`
	ComplimentaryReason     string `json:"complimentary_reason"`

	// AnchorDate is the first day of the first cycle; cycle N runs from
	// anchor + (N-1) periods, clamped to month end (Jan 31 → Feb 28 → Mar 31).
	AnchorDate         string `json:"anchor_date" gorm:"not null"`
	CurrentCycle       int    `json:"current_cycle" gorm:"not null;default:1"`
	CurrentPeriodStart string `json:"current_period_start" gorm:"not null"`
	CurrentPeriodEnd   string `json:"current_period_end" gorm:"not null;index"`

	CancelledAt        *time.Time `json:"cancelled_at,omitempty"`
	CancellationReason string     `json:"cancellation_reason"`
	Notes              string     `json:"notes" gorm:"type:text"`
	CreatedByID        uuid.UUID  `json:"created_by_id" gorm:"type:uuid"`

	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

// SubscriptionCycle is one billing period of a subscription. A cycle can be
// created ahead of time by recording a payment for the next period; the
// renewal job then rolls into it even when auto-renew is off.
type SubscriptionCycle struct {
	ID             uuid.UUID `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	SubscriptionID uuid.UUID `json:"subscription_id" gorm:"type:uuid;not null;index"`
	StoreID        uuid.UUID `json:"store_id" gorm:"type:uuid;not null;index"`
	CycleNumber    int       `json:"cycle_number" gorm:"not null"`
	PeriodStart    string    `json:"period_start" gorm:"not null"`
	PeriodEnd      string    `json:"period_end" gorm:"not null"`
	Amount         float64   `json:"amount" gorm:"not null;default:0"`
	AmountPaid     float64   `json:"amount_paid" gorm:"not null;default:0"`
	// PaymentStatus: unpaid, partial or paid.
	PaymentStatus string     `json:"payment_status" gorm:"not null;default:'unpaid'"`
	PaidAt        *time.Time `json:"paid_at,omitempty"`
	// Source: initial, auto_renewal, prepaid or reactivation.
	Source string `json:"source" gorm:"default:'initial'"`
	// Complimentary cycles are free; WaivedAmount is the regular price that
	// was not charged (shown as the value given away).
	Complimentary bool    `json:"complimentary"`
	WaivedAmount  float64 `json:"waived_amount"`
	// Every cycle is invoiced: InvoiceNumber is assigned on create
	// (PREFIX-YYYY-00001) and InvoiceDate is the billing date (YYYY-MM-DD).
	InvoiceNumber string    `json:"invoice_number" gorm:"index"`
	InvoiceDate   string    `json:"invoice_date"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// SubscriptionPayment is a manually recorded payment against a cycle.
// Gateway capture will add rows here in future.
type SubscriptionPayment struct {
	ID             uuid.UUID `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	SubscriptionID uuid.UUID `json:"subscription_id" gorm:"type:uuid;not null;index"`
	CycleID        uuid.UUID `json:"cycle_id" gorm:"type:uuid;not null;index"`
	StoreID        uuid.UUID `json:"store_id" gorm:"type:uuid;not null;index"`
	Amount         float64   `json:"amount" gorm:"not null"`
	PaymentDate    string    `json:"payment_date" gorm:"not null"` // YYYY-MM-DD
	// Method: cash, upi, bank_transfer, card, cheque, other.
	Method    string `json:"method" gorm:"default:'bank_transfer'"`
	Reference string `json:"reference"`
	// ReceiptNumber is assigned on create (PREFIX-YYYY-00001).
	ReceiptNumber string    `json:"receipt_number" gorm:"index"`
	Notes         string    `json:"notes" gorm:"type:text"`
	CreatedByID   uuid.UUID `json:"created_by_id" gorm:"type:uuid"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}
