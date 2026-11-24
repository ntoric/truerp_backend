package utils

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"strings"
	"truerp/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Context keys set by the public API auth middleware.
const (
	CtxPublicScopes      = "public_scopes"    // map[string]bool of effective scopes
	CtxCredentialType    = "public_cred_type" // api_key | oauth | session
	CtxCredentialID      = "public_cred_id"   // API key ID, OAuth client_id or user ID
	CtxCredentialName    = "public_cred_name" // human label
	CtxIntegrationPolicy = "public_policy"    // models.IntegrationPolicy
)

// Public API error types (the "type" field of an error object).
const (
	APIErrInvalidRequest = "invalid_request_error"
	APIErrAuthentication = "authentication_error"
	APIErrPermission     = "permission_error"
	APIErrNotFound       = "not_found_error"
	APIErrConflict       = "conflict_error"
	APIErrRateLimit      = "rate_limit_error"
	APIErrIdempotency    = "idempotency_error"
	APIErrAPI            = "api_error"
)

// APIErrorTypeForStatus maps an HTTP status to a public error type.
func APIErrorTypeForStatus(status int) string {
	switch {
	case status == http.StatusUnauthorized:
		return APIErrAuthentication
	case status == http.StatusForbidden:
		return APIErrPermission
	case status == http.StatusNotFound:
		return APIErrNotFound
	case status == http.StatusConflict:
		return APIErrConflict
	case status == http.StatusTooManyRequests:
		return APIErrRateLimit
	case status >= 500:
		return APIErrAPI
	default:
		return APIErrInvalidRequest
	}
}

// PublicAPIError writes the standard public API error envelope and aborts:
//
//	{"error": {"type": "...", "code": "...", "message": "...", "param": "...", "request_id": "..."}}
func PublicAPIError(c *gin.Context, status int, code, message string, extra ...gin.H) {
	body := gin.H{
		"type":    APIErrorTypeForStatus(status),
		"code":    code,
		"message": message,
	}
	if rid, ok := c.Get("request_id"); ok {
		body["request_id"] = rid
	}
	for _, e := range extra {
		for k, v := range e {
			body[k] = v
		}
	}
	c.Set("public_error_code", code)
	c.AbortWithStatusJSON(status, gin.H{"error": body})
}

// APIKeyPrefix starts every public API key, so leaked keys are recognisable
// by secret scanners.
const APIKeyPrefix = "trp_live_"

// HashAPIKey returns the stored digest of an API key.
func HashAPIKey(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])
}

// IsAPIKey reports whether a bearer credential looks like an API key.
func IsAPIKey(token string) bool {
	return strings.HasPrefix(token, "trp_")
}

// DefaultIntegrationPolicy is used for stores a super admin has not
// configured: API enabled, every scope allowed, 120 requests/minute.
func DefaultIntegrationPolicy(storeID uuid.UUID) models.IntegrationPolicy {
	return models.IntegrationPolicy{
		StoreID:            storeID,
		APIEnabled:         true,
		AllowedScopes:      JoinScopes(AllPublicScopes()),
		RateLimitPerMinute: 120,
		WebhooksEnabled:    true,
	}
}

// LoadIntegrationPolicy returns the store's policy, or the default.
func LoadIntegrationPolicy(db *gorm.DB, storeID uuid.UUID) models.IntegrationPolicy {
	var p models.IntegrationPolicy
	if err := db.Where("store_id = ?", storeID).First(&p).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			// On a lookup failure fall back to the safest setting.
			d := DefaultIntegrationPolicy(storeID)
			d.APIEnabled = false
			return d
		}
		return DefaultIntegrationPolicy(storeID)
	}
	if p.RateLimitPerMinute <= 0 {
		p.RateLimitPerMinute = 120
	}
	return p
}

// IPAllowed reports whether ip matches the comma-separated allow list of IPs
// and CIDRs. An empty list allows every address.
func IPAllowed(allowList, ip string) bool {
	entries := ParseScopes(allowList)
	if len(entries) == 0 {
		return true
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, e := range entries {
		if strings.Contains(e, "/") {
			if _, cidr, err := net.ParseCIDR(e); err == nil && cidr.Contains(parsed) {
				return true
			}
			continue
		}
		if other := net.ParseIP(e); other != nil && other.Equal(parsed) {
			return true
		}
	}
	return false
}

// ValidateIPAllowList checks every entry is an IP or CIDR.
func ValidateIPAllowList(allowList string) error {
	for _, e := range ParseScopes(allowList) {
		if strings.Contains(e, "/") {
			if _, _, err := net.ParseCIDR(e); err != nil {
				return errors.New("invalid CIDR: " + e)
			}
			continue
		}
		if net.ParseIP(e) == nil {
			return errors.New("invalid IP address: " + e)
		}
	}
	return nil
}
