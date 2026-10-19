package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// OAuthClient represents a registered OAuth 2.0 client application
// capable of authenticating via Authorization Code (with PKCE), Client Credentials, etc.
type OAuthClient struct {
	ID           uuid.UUID  `gorm:"type:uuid;primary_key;" json:"id"`
	ClientID     string     `gorm:"type:varchar(128);uniqueIndex;not null" json:"client_id"`
	ClientSecret string     `gorm:"type:varchar(255);not null" json:"client_secret,omitempty"`
	ClientName   string     `gorm:"type:varchar(255);not null" json:"client_name"`
	RedirectURIs string     `gorm:"type:text;not null" json:"redirect_uris"` // comma-separated or space-separated list
	GrantTypes   string     `gorm:"type:varchar(255);default:'authorization_code,client_credentials,refresh_token,password'" json:"grant_types"`
	Scopes       string     `gorm:"type:varchar(255);default:'read write admin erp'" json:"scopes"`
	IsPublic     bool       `gorm:"default:false" json:"is_public"` // e.g. SPA or native app using PKCE without secret
	UserID       *uuid.UUID `gorm:"type:uuid;index" json:"user_id,omitempty"`
	StoreID      *uuid.UUID `gorm:"type:uuid;index" json:"store_id,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

func (c *OAuthClient) BeforeCreate(tx *gorm.DB) error {
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	return nil
}

// OAuthCode represents an issued authorization code for OAuth 2.0 Authorization Code flow with PKCE
type OAuthCode struct {
	ID                  uuid.UUID  `gorm:"type:uuid;primary_key;" json:"id"`
	Code                string     `gorm:"type:varchar(128);uniqueIndex;not null" json:"code"`
	ClientID            string     `gorm:"type:varchar(128);not null;index" json:"client_id"`
	UserID              uuid.UUID  `gorm:"type:uuid;not null;index" json:"user_id"`
	StoreID             *uuid.UUID `gorm:"type:uuid;index" json:"store_id,omitempty"`
	RedirectURI         string     `gorm:"type:text;not null" json:"redirect_uri"`
	Scope               string     `gorm:"type:varchar(255)" json:"scope"`
	CodeChallenge       string     `gorm:"type:varchar(128)" json:"code_challenge"`
	CodeChallengeMethod string     `gorm:"type:varchar(32)" json:"code_challenge_method"` // "S256" or "plain"
	ExpiresAt           time.Time  `gorm:"not null;index" json:"expires_at"`
	Used                bool       `gorm:"default:false;index" json:"used"`
	CreatedAt           time.Time  `json:"created_at"`
}

func (c *OAuthCode) BeforeCreate(tx *gorm.DB) error {
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	return nil
}

// OAuthRefreshToken represents a stored refresh token for issuing renewed access tokens
type OAuthRefreshToken struct {
	ID        uuid.UUID  `gorm:"type:uuid;primary_key;" json:"id"`
	Token     string     `gorm:"type:varchar(128);uniqueIndex;not null" json:"token"`
	ClientID  string     `gorm:"type:varchar(128);not null;index" json:"client_id"`
	UserID    uuid.UUID  `gorm:"type:uuid;not null;index" json:"user_id"`
	StoreID   *uuid.UUID `gorm:"type:uuid;index" json:"store_id,omitempty"`
	Scope     string     `gorm:"type:varchar(255)" json:"scope"`
	ExpiresAt time.Time  `gorm:"not null;index" json:"expires_at"`
	Revoked   bool       `gorm:"default:false;index" json:"revoked"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

func (r *OAuthRefreshToken) BeforeCreate(tx *gorm.DB) error {
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	return nil
}
