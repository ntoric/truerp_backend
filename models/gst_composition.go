package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// GST Composition reporting models.
//
// The billing documents (Invoice, PurchaseBill, SalesReturn, PurchaseReturn,
// CreditNote, DebitNote) remain the source of truth. These tables only hold
// configuration, filing-period workflow state, calculation snapshots, exports,
// validation results and reconciliation records so that finalized reports stay
// auditable even when the underlying documents change later.

// GSTRegistration holds one GST registration (GSTIN). Kept separate from
// Business and Store so multiple registrations can be supported later and so
// effective-dated registration changes are possible.
type GSTRegistration struct {
	ID          uuid.UUID `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID      uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index"`
	GSTIN       string    `json:"gstin" gorm:"not null;index"`
	LegalName   string    `json:"legal_name" gorm:"not null"`
	TradeName   string    `json:"trade_name"`
	StateCode   string    `json:"state_code" gorm:"not null"`
	State       string    `json:"state"`
	// RegistrationType is "composition" or "regular".
	RegistrationType string `json:"registration_type" gorm:"not null;default:'composition'"`
	// CompositionCategory drives the levy rate: trader, manufacturer,
	// restaurant, service_provider. Empty for regular registrations.
	CompositionCategory string `json:"composition_category"`
	// CompositionRate optionally overrides the statutory rate for the
	// category (percent, e.g. 1 for 1%). 0 means "use category default".
	CompositionRate float64    `json:"composition_rate" gorm:"default:0"`
	EffectiveFrom   time.Time  `json:"effective_from"`
	EffectiveTo     *time.Time `json:"effective_to,omitempty"`
	// FilingFrequency is informational (e.g. "quarterly" for CMP-08 +
	// annual GSTR-4).
	FilingFrequency string    `json:"filing_frequency" gorm:"default:'quarterly'"`
	Status          string    `json:"status" gorm:"default:'active'"` // active, inactive
	Notes           string    `json:"notes"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

// GST return / workflow statuses for GSTReturnPeriod.Status. These are
// application workflow statuses only — FILED may be set solely when the user
// supplies portal evidence (ARN / filing acknowledgement).
const (
	GSTPeriodStatusDraft                   = "DRAFT"
	GSTPeriodStatusGenerated               = "GENERATED"
	GSTPeriodStatusValidated               = "VALIDATED"
	GSTPeriodStatusFinalized               = "FINALIZED"
	GSTPeriodStatusExported                = "EXPORTED"
	GSTPeriodStatusFiled                   = "FILED"
	GSTPeriodStatusReconciliationRequired  = "RECONCILIATION_REQUIRED"
)

// GST return types supported by the composition module.
const (
	GSTReturnTypeCMP08 = "CMP-08"
	GSTReturnTypeGSTR4 = "GSTR-4"
)

// GST period types.
const (
	GSTPeriodTypeQuarterly = "quarterly"
	GSTPeriodTypeAnnual    = "annual"
)

// GSTReturnPeriod is a filing-period workspace: one row per (registration,
// financial year, period). Generating a report snapshots its calculation so a
// later invoice edit never silently changes a finalized report.
type GSTReturnPeriod struct {
	ID                uuid.UUID  `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID            uuid.UUID  `json:"user_id" gorm:"type:uuid;not null;index"`
	GSTRegistrationID uuid.UUID  `json:"gst_registration_id" gorm:"type:uuid;not null;index"`
	Registration      *GSTRegistration `json:"registration,omitempty" gorm:"foreignKey:GSTRegistrationID"`
	// FinancialYear in "YYYY-YY" form, e.g. "2026-27" (Apr 2026 - Mar 2027).
	FinancialYear string `json:"financial_year" gorm:"not null"`
	// PeriodType is "quarterly" (CMP-08) or "annual" (GSTR-4).
	PeriodType  string `json:"period_type" gorm:"not null"`
	// Quarter is 1-4 for quarterly periods, 0 for annual. Indian FY quarters:
	// Q1 Apr-Jun, Q2 Jul-Sep, Q3 Oct-Dec, Q4 Jan-Mar.
	Quarter     int        `json:"quarter" gorm:"default:0"`
	PeriodStart time.Time  `json:"period_start" gorm:"not null"`
	PeriodEnd   time.Time  `json:"period_end" gorm:"not null"`
	ReturnType  string     `json:"return_type" gorm:"not null"` // CMP-08, GSTR-4
	Status      string     `json:"status" gorm:"default:'DRAFT'"`
	GeneratedAt *time.Time `json:"generated_at,omitempty"`
	FinalizedAt *time.Time `json:"finalized_at,omitempty"`
	// FiledAt / PortalReference record claimed filing evidence. Status FILED
	// is only meaningful together with these.
	FiledAt         *time.Time `json:"filed_at,omitempty"`
	PortalReference string     `json:"portal_reference"` // ARN / acknowledgement number
	FilingNotes     string     `json:"filing_notes"`
	CreatedBy       uuid.UUID  `json:"created_by" gorm:"type:uuid"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

// GSTCalculationSnapshot freezes one report computation for a return period.
// Monetary values are stored rounded to 2 decimals (paise precision); the
// detailed breakdown is kept as JSON for audit.
type GSTCalculationSnapshot struct {
	ID                 uuid.UUID `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID             uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index"`
	GSTReturnPeriodID  uuid.UUID `json:"gst_return_period_id" gorm:"type:uuid;not null;index"`
	// Version increments each regeneration for the same period.
	Version           int    `json:"version" gorm:"not null;default:1"`
	CalculationRule   string `json:"calculation_rule"` // e.g. "composition-v1"
	// SourceDataVersion fingerprints the underlying documents (count +
	// max(updated_at)) so staleness is detectable.
	SourceDataVersion string  `json:"source_data_version"`
	TurnoverBasis     float64 `json:"turnover_basis" gorm:"default:0"`
	CompositionRate   float64 `json:"composition_rate" gorm:"default:0"`
	CGSTLiability     float64 `json:"cgst_liability" gorm:"default:0"`
	SGSTLiability     float64 `json:"sgst_liability" gorm:"default:0"`
	IGSTLiability     float64 `json:"igst_liability" gorm:"default:0"`
	TotalCompositionLiability float64 `json:"total_composition_liability" gorm:"default:0"`
	RCMTaxableValue   float64 `json:"rcm_taxable_value" gorm:"default:0"`
	RCMLiability      float64 `json:"rcm_liability" gorm:"default:0"`
	InterestAmount    float64 `json:"interest_amount" gorm:"default:0"`
	LateFeeAmount     float64 `json:"late_fee_amount" gorm:"default:0"`
	AdjustmentsTotal  float64 `json:"adjustments_total" gorm:"default:0"`
	TotalPayable      float64 `json:"total_payable" gorm:"default:0"`
	// CalculationDetails is a JSON document with the full line-level
	// breakdown (summaries, register counts, per-step math).
	CalculationDetails string    `json:"calculation_details" gorm:"type:text"`
	GeneratedAt        time.Time `json:"generated_at"`
	GeneratedBy        uuid.UUID `json:"generated_by" gorm:"type:uuid"`
	CreatedAt          time.Time `json:"created_at"`
}

// GSTReportExport records every generated export file for audit.
type GSTReportExport struct {
	ID                uuid.UUID `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID            uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index"`
	GSTReturnPeriodID *uuid.UUID `json:"gst_return_period_id,omitempty" gorm:"type:uuid;index"`
	// ExportType: filing_summary, sales_register, purchase_register, cmp08,
	// gstr4. FileFormat: xlsx, csv, pdf.
	ExportType   string    `json:"export_type" gorm:"not null"`
	FileFormat   string    `json:"file_format" gorm:"not null"`
	FileName     string    `json:"file_name"`
	FileReference string   `json:"file_reference"`
	ReportVersion int      `json:"report_version" gorm:"default:0"` // snapshot version used
	RecordCount  int       `json:"record_count" gorm:"default:0"`
	// ValidationStatus is the issue state at export time:
	// clean, warnings, errors, not_validated.
	ValidationStatus string    `json:"validation_status"`
	GeneratedAt      time.Time `json:"generated_at"`
	GeneratedBy      uuid.UUID `json:"generated_by" gorm:"type:uuid"`
	CreatedAt        time.Time `json:"created_at"`
}

// GSTValidationIssue is one finding from the validation engine. Issues are
// stored per return period + snapshot version and carry the source document
// so users can navigate to the affected transaction.
type GSTValidationIssue struct {
	ID                uuid.UUID `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID            uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index"`
	GSTReturnPeriodID uuid.UUID `json:"gst_return_period_id" gorm:"type:uuid;not null;index"`
	ReportVersion     int       `json:"report_version" gorm:"default:0"`
	// Severity: ERROR, WARNING, INFO.
	Severity string `json:"severity" gorm:"not null;index"`
	// Code is a stable machine identifier, e.g. MISSING_GST_REGISTRATION.
	Code    string `json:"code" gorm:"not null"`
	Message string `json:"message" gorm:"not null"`
	// SourceTransactionType / ID point at the affected document:
	// invoice, purchase_bill, sales_return, purchase_return, credit_note,
	// debit_note, period, registration.
	SourceTransactionType string     `json:"source_transaction_type"`
	SourceTransactionID   *uuid.UUID `json:"source_transaction_id,omitempty" gorm:"type:uuid"`
	SourceReference       string     `json:"source_reference"` // human ref e.g. invoice number
	// ResolutionStatus: open, resolved.
	ResolutionStatus string     `json:"resolution_status" gorm:"default:'open';index"`
	ResolutionNote   string     `json:"resolution_note"`
	ResolvedBy       *uuid.UUID `json:"resolved_by,omitempty" gorm:"type:uuid"`
	ResolvedAt       *time.Time `json:"resolved_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
}

// GSTTaxPayment records a tax payment the user says they made (challan).
// PortalConfirmed stays false until verified against portal records; merely
// creating a row does not mean tax was actually paid.
type GSTTaxPayment struct {
	ID                uuid.UUID `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID            uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index"`
	GSTReturnPeriodID uuid.UUID `json:"gst_return_period_id" gorm:"type:uuid;not null;index"`
	PaymentDate       time.Time `json:"payment_date" gorm:"not null"`
	Amount            float64   `json:"amount" gorm:"not null"`
	// Component: composition_tax, reverse_charge, interest, late_fee, other.
	Component        string `json:"component" gorm:"default:'composition_tax'"`
	ChallanReference string `json:"challan_reference"` // CPIN/CIN
	// PortalConfirmed is only set via explicit reconciliation against portal
	// evidence (ARN/challan verification), never automatically.
	PortalConfirmed bool   `json:"portal_confirmed" gorm:"default:false"`
	PortalReference string `json:"portal_reference"`
	Notes           string `json:"notes"`
	CreatedBy       uuid.UUID `json:"created_by" gorm:"type:uuid"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

// GSTReconciliationRecord compares a system-calculated value with the value
// recorded on a filed return and (optionally) a portal-confirmed value.
type GSTReconciliationRecord struct {
	ID                uuid.UUID `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID            uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index"`
	GSTReturnPeriodID uuid.UUID `json:"gst_return_period_id" gorm:"type:uuid;not null;index"`
	// ReconciliationType: cmp08_turnover, cmp08_liability, cmp08_tax_paid,
	// gstr4_turnover, gstr4_liability, annual_vs_quarters.
	ReconciliationType  string   `json:"reconciliation_type" gorm:"not null"`
	SystemCalculatedValue float64 `json:"system_calculated_value" gorm:"default:0"`
	RecordedReturnValue   float64 `json:"recorded_return_value" gorm:"default:0"`
	PortalConfirmedValue  *float64 `json:"portal_confirmed_value,omitempty"`
	Difference            float64 `json:"difference" gorm:"default:0"`
	// ResolutionStatus: open, explained, resolved.
	ResolutionStatus string     `json:"resolution_status" gorm:"default:'open';index"`
	ResolutionNote   string     `json:"resolution_note"`
	ResolvedBy       *uuid.UUID `json:"resolved_by,omitempty" gorm:"type:uuid"`
	ResolvedAt       *time.Time `json:"resolved_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}
