package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// APIKey is a store-scoped secret that lets an external platform call the
// public API (/api/public/v1) server-to-server. Only a SHA-256 hash of the
// secret is stored; the plaintext is shown once at creation.
type APIKey struct {
	ID      uuid.UUID `gorm:"type:uuid;primary_key;" json:"id"`
	StoreID uuid.UUID `gorm:"type:uuid;not null;index" json:"store_id"`
	Name    string    `gorm:"type:varchar(120);not null" json:"name"`
	// Prefix is the first characters of the key (e.g. "trp_live_AbC1"),
	// safe to display so admins can recognise a key.
	Prefix          string     `gorm:"type:varchar(32);not null;index" json:"prefix"`
	SecretHash      string     `gorm:"type:varchar(64);not null;uniqueIndex" json:"-"`
	Scopes          string     `gorm:"type:text;not null" json:"scopes"` // space-separated
	CreatedByUserID uuid.UUID  `gorm:"type:uuid;not null" json:"created_by_user_id"`
	LastUsedAt      *time.Time `json:"last_used_at,omitempty"`
	LastUsedIP      string     `gorm:"type:varchar(64)" json:"last_used_ip,omitempty"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	RevokedAt       *time.Time `gorm:"index" json:"revoked_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func (k *APIKey) BeforeCreate(tx *gorm.DB) error {
	if k.ID == uuid.Nil {
		k.ID = uuid.New()
	}
	return nil
}

// IntegrationPolicy is the per-store permission envelope a super admin sets
// for the public API. Credentials can never exceed AllowedScopes.
type IntegrationPolicy struct {
	ID                 uuid.UUID `gorm:"type:uuid;primary_key;" json:"id"`
	StoreID            uuid.UUID `gorm:"type:uuid;not null;uniqueIndex" json:"store_id"`
	APIEnabled         bool      `gorm:"default:true" json:"api_enabled"`
	AllowedScopes      string    `gorm:"type:text;not null" json:"allowed_scopes"` // space-separated
	RateLimitPerMinute int       `gorm:"default:120" json:"rate_limit_per_minute"`
	WebhooksEnabled    bool      `gorm:"default:true" json:"webhooks_enabled"`
	// AllowedIPs optionally restricts API-key calls to these comma-separated
	// IPs/CIDRs. Empty means any address.
	AllowedIPs string     `gorm:"type:text" json:"allowed_ips"`
	UpdatedBy  *uuid.UUID `gorm:"type:uuid" json:"updated_by,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

func (p *IntegrationPolicy) BeforeCreate(tx *gorm.DB) error {
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	return nil
}

// WebhookEndpoint receives signed event notifications for one store.
type WebhookEndpoint struct {
	ID          uuid.UUID `gorm:"type:uuid;primary_key;" json:"id"`
	StoreID     uuid.UUID `gorm:"type:uuid;not null;index" json:"store_id"`
	URL         string    `gorm:"type:text;not null" json:"url"`
	Description string    `gorm:"type:varchar(255)" json:"description"`
	// Events is a space-separated list of event types, or "*" for all.
	Events string `gorm:"type:text;not null" json:"events"`
	// EncryptedSecret holds the signing secret (encrypted at rest).
	EncryptedSecret string     `gorm:"type:text;not null" json:"-"`
	IsActive        bool       `gorm:"default:true;index" json:"is_active"`
	CreatedVia      string     `gorm:"type:varchar(32)" json:"created_via"` // admin | api
	FailureCount    int        `gorm:"default:0" json:"failure_count"`      // consecutive failed deliveries
	DisabledReason  string     `gorm:"type:varchar(255)" json:"disabled_reason,omitempty"`
	LastDeliveryAt  *time.Time `json:"last_delivery_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func (w *WebhookEndpoint) BeforeCreate(tx *gorm.DB) error {
	if w.ID == uuid.Nil {
		w.ID = uuid.New()
	}
	return nil
}

// IntegrationEvent is one entry in a store's event feed (outbox). Events are
// produced by the change scanner from rows touched by any client (web,
// mobile, POS, API) and fanned out to matching webhook endpoints.
type IntegrationEvent struct {
	ID         uuid.UUID `gorm:"type:uuid;primary_key;" json:"id"`
	StoreID    uuid.UUID `gorm:"type:uuid;not null;index:idx_integration_events_store_created,priority:1" json:"store_id"`
	Type       string    `gorm:"type:varchar(64);not null;index" json:"type"` // e.g. invoice.created
	ObjectType string    `gorm:"type:varchar(32);not null" json:"object_type"`
	ObjectID   uuid.UUID `gorm:"type:uuid;not null;index" json:"object_id"`
	Data       string    `gorm:"type:text" json:"-"` // JSON snapshot of the public representation
	CreatedAt  time.Time `gorm:"index:idx_integration_events_store_created,priority:2" json:"created_at"`
}

func (e *IntegrationEvent) BeforeCreate(tx *gorm.DB) error {
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	return nil
}

// IntegrationScanCursor tracks how far the change scanner has read each
// store's tables.
type IntegrationScanCursor struct {
	StoreID   uuid.UUID `gorm:"type:uuid;primary_key" json:"store_id"`
	ScannedTo time.Time `json:"scanned_to"`
	UpdatedAt time.Time `json:"updated_at"`
}

// WebhookDelivery is one attempt series of sending an event to an endpoint.
type WebhookDelivery struct {
	ID             uuid.UUID  `gorm:"type:uuid;primary_key;" json:"id"`
	EndpointID     uuid.UUID  `gorm:"type:uuid;not null;index" json:"endpoint_id"`
	EventID        uuid.UUID  `gorm:"type:uuid;not null;index" json:"event_id"`
	EventType      string     `gorm:"type:varchar(64)" json:"event_type"`
	Status         string     `gorm:"type:varchar(16);not null;index" json:"status"` // pending | succeeded | failed
	Attempts       int        `gorm:"default:0" json:"attempts"`
	NextAttemptAt  *time.Time `gorm:"index" json:"next_attempt_at,omitempty"`
	ResponseStatus int        `json:"response_status"`
	ResponseBody   string     `gorm:"type:text" json:"response_body,omitempty"` // truncated
	LastError      string     `gorm:"type:text" json:"last_error,omitempty"`
	DeliveredAt    *time.Time `json:"delivered_at,omitempty"`
	CreatedAt      time.Time  `gorm:"index" json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

func (d *WebhookDelivery) BeforeCreate(tx *gorm.DB) error {
	if d.ID == uuid.Nil {
		d.ID = uuid.New()
	}
	return nil
}

// APIIdempotencyRecord caches the response of a public API write so a retry
// carrying the same Idempotency-Key replays it instead of acting twice.
type APIIdempotencyRecord struct {
	ID           uuid.UUID `gorm:"type:uuid;primary_key;" json:"id"`
	StoreID      uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_api_idem_store_key,priority:1" json:"store_id"`
	Key          string    `gorm:"type:varchar(255);not null;uniqueIndex:idx_api_idem_store_key,priority:2" json:"key"`
	RequestHash  string    `gorm:"type:varchar(64);not null" json:"request_hash"`
	StatusCode   int       `json:"status_code"`
	ResponseBody string    `gorm:"type:text" json:"-"`
	CreatedAt    time.Time `gorm:"index" json:"created_at"`
}

func (r *APIIdempotencyRecord) BeforeCreate(tx *gorm.DB) error {
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	return nil
}

// APIRequestLog records each public API call for the admin usage view.
type APIRequestLog struct {
	ID             uuid.UUID  `gorm:"type:uuid;primary_key;" json:"id"`
	StoreID        *uuid.UUID `gorm:"type:uuid;index:idx_api_request_logs_store_created,priority:1" json:"store_id,omitempty"`
	CredentialType string     `gorm:"type:varchar(16)" json:"credential_type"` // api_key | oauth | session
	CredentialID   string     `gorm:"type:varchar(128);index" json:"credential_id"`
	CredentialName string     `gorm:"type:varchar(255)" json:"credential_name"`
	Method         string     `gorm:"type:varchar(8)" json:"method"`
	Path           string     `gorm:"type:text" json:"path"`
	StatusCode     int        `gorm:"index" json:"status_code"`
	DurationMs     int64      `json:"duration_ms"`
	IPAddress      string     `gorm:"type:varchar(64)" json:"ip_address"`
	RequestID      string     `gorm:"type:varchar(64)" json:"request_id"`
	ErrorCode      string     `gorm:"type:varchar(64)" json:"error_code,omitempty"`
	CreatedAt      time.Time  `gorm:"index:idx_api_request_logs_store_created,priority:2;index" json:"created_at"`
}

func (l *APIRequestLog) BeforeCreate(tx *gorm.DB) error {
	if l.ID == uuid.Nil {
		l.ID = uuid.New()
	}
	return nil
}
