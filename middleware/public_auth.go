package middleware

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"truerp/models"
	"truerp/services/audit"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// publicRateLimiter holds one bucket per credential; the limit comes from
// the store's policy, so buckets are keyed by credential + limit.
var publicRateLimiter = struct {
	mu      sync.Mutex
	buckets map[string]*windowCounter
	sweep   time.Time
}{buckets: map[string]*windowCounter{}}

// allowPublicRequest applies a fixed one-minute window and returns whether
// the request may proceed, the remaining budget and the window reset time.
func allowPublicRequest(key string, limit int, now time.Time) (bool, int, time.Time) {
	publicRateLimiter.mu.Lock()
	defer publicRateLimiter.mu.Unlock()
	if now.Sub(publicRateLimiter.sweep) > time.Minute {
		for k, wc := range publicRateLimiter.buckets {
			if now.Sub(wc.start) >= time.Minute {
				delete(publicRateLimiter.buckets, k)
			}
		}
		publicRateLimiter.sweep = now
	}
	wc, ok := publicRateLimiter.buckets[key]
	if !ok || now.Sub(wc.start) >= time.Minute {
		wc = &windowCounter{start: now}
		publicRateLimiter.buckets[key] = wc
	}
	reset := wc.start.Add(time.Minute)
	if wc.count >= limit {
		return false, 0, reset
	}
	wc.count++
	return true, limit - wc.count, reset
}

type publicPrincipal struct {
	credType  string
	credID    string
	credName  string
	actor     models.User
	storeID   uuid.UUID
	scopeUser uuid.UUID
	scopes    []string
	apiKeyID  *uuid.UUID
}

// PublicAPIAuth authenticates calls to /api/public/v1 with an API key
// ("Authorization: Bearer trp_live_…" or "X-API-Key") or an OAuth access
// token, applies the store's integration policy and rate limit, binds the
// audit actor and logs the request.
func PublicAPIAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		requestID := strings.TrimSpace(c.GetHeader("X-Request-ID"))
		if requestID == "" || len(requestID) > 64 {
			requestID = uuid.NewString()
		}
		c.Set("request_id", requestID)
		c.Header("X-Request-ID", requestID)

		principal, status, code, msg := authenticatePublic(c)
		if status != 0 {
			utils.PublicAPIError(c, status, code, msg)
			logPublicRequest(c, nil, started)
			return
		}

		policy := utils.LoadIntegrationPolicy(utils.DB, principal.storeID)
		if !policy.APIEnabled {
			utils.PublicAPIError(c, http.StatusForbidden, "api_disabled", "API access is disabled for this store. Contact your TruERP administrator.")
			logPublicRequest(c, principal, started)
			return
		}
		if principal.credType == "api_key" && !utils.IPAllowed(policy.AllowedIPs, c.ClientIP()) {
			utils.PublicAPIError(c, http.StatusForbidden, "ip_not_allowed", "Requests from this IP address are not allowed for this store.")
			logPublicRequest(c, principal, started)
			return
		}

		effective := utils.IntersectScopes(principal.scopes, utils.ParseScopes(policy.AllowedScopes))
		scopeSet := map[string]bool{}
		for _, s := range effective {
			scopeSet[s] = true
		}

		allowed, remaining, reset := allowPublicRequest(principal.credType+":"+principal.credID+":"+strconv.Itoa(policy.RateLimitPerMinute), policy.RateLimitPerMinute, time.Now())
		c.Header("X-RateLimit-Limit", strconv.Itoa(policy.RateLimitPerMinute))
		c.Header("X-RateLimit-Remaining", strconv.Itoa(remaining))
		c.Header("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
		if !allowed {
			c.Header("Retry-After", strconv.Itoa(int(time.Until(reset).Seconds())+1))
			utils.PublicAPIError(c, http.StatusTooManyRequests, "rate_limited", fmt.Sprintf("Rate limit of %d requests per minute exceeded.", policy.RateLimitPerMinute))
			logPublicRequest(c, principal, started)
			return
		}

		// The same context keys AuthRequired sets, so internal handlers work
		// unchanged when the public API delegates to them.
		c.Set("actor_user_id", principal.actor.ID)
		c.Set("user_name", principal.credName)
		c.Set("email", principal.actor.Email)
		c.Set("role", principal.actor.Role)
		c.Set("user_id", principal.scopeUser)
		c.Set("store_id", principal.storeID)
		c.Set(utils.CtxPublicScopes, scopeSet)
		c.Set(utils.CtxCredentialType, principal.credType)
		c.Set(utils.CtxCredentialID, principal.credID)
		c.Set(utils.CtxCredentialName, principal.credName)
		c.Set(utils.CtxIntegrationPolicy, policy)

		if principal.apiKeyID != nil {
			touchAPIKey(*principal.apiKeyID, c.ClientIP())
		}

		unbind := audit.BindActor(&audit.Actor{
			UserID:    principal.actor.ID,
			UserName:  principal.credName,
			ScopeID:   principal.scopeUser,
			IPAddress: c.ClientIP(),
			UserAgent: c.GetHeader("User-Agent"),
			RequestID: requestID,
		})
		defer unbind()

		c.Next()
		logPublicRequest(c, principal, started)
	}
}

func bearerOrAPIKey(c *gin.Context) string {
	if key := strings.TrimSpace(c.GetHeader("X-API-Key")); key != "" {
		return key
	}
	parts := strings.SplitN(c.GetHeader("Authorization"), " ", 2)
	if len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
		return strings.TrimSpace(parts[1])
	}
	return ""
}

func authenticatePublic(c *gin.Context) (*publicPrincipal, int, string, string) {
	token := bearerOrAPIKey(c)
	if token == "" {
		return nil, http.StatusUnauthorized, "missing_credentials", "Provide an API key or OAuth access token as 'Authorization: Bearer <token>'."
	}
	if utils.IsAPIKey(token) {
		return authenticateAPIKey(token)
	}
	return authenticateBearerToken(c, token)
}

func authenticateAPIKey(token string) (*publicPrincipal, int, string, string) {
	var key models.APIKey
	if err := utils.DB.Where("secret_hash = ?", utils.HashAPIKey(token)).First(&key).Error; err != nil {
		return nil, http.StatusUnauthorized, "invalid_api_key", "The API key is invalid."
	}
	if key.RevokedAt != nil {
		return nil, http.StatusUnauthorized, "api_key_revoked", "The API key has been revoked."
	}
	if key.ExpiresAt != nil && time.Now().After(*key.ExpiresAt) {
		return nil, http.StatusUnauthorized, "api_key_expired", "The API key has expired."
	}
	store, err := utils.FindStoreByID(utils.DB, key.StoreID)
	if err != nil || !store.IsActive {
		return nil, http.StatusForbidden, "store_inactive", "The store for this API key is inactive."
	}
	var owner models.User
	if err := utils.DB.First(&owner, "id = ?", store.OwnerUserID).Error; err != nil || !owner.IsActive {
		return nil, http.StatusForbidden, "store_inactive", "The store owner account for this API key is inactive."
	}
	id := key.ID
	return &publicPrincipal{
		credType:  "api_key",
		credID:    key.ID.String(),
		credName:  "API key: " + key.Name,
		actor:     owner,
		storeID:   store.ID,
		scopeUser: store.OwnerUserID,
		scopes:    utils.ParseScopes(key.Scopes),
		apiKeyID:  &id,
	}, 0, "", ""
}

func authenticateBearerToken(c *gin.Context, token string) (*publicPrincipal, int, string, string) {
	claims, err := utils.ParseToken(token)
	if err != nil || claims.Role == "portal" {
		return nil, http.StatusUnauthorized, "invalid_token", "The access token is invalid or expired."
	}
	var user models.User
	if err := utils.DB.First(&user, "id = ?", claims.UserID).Error; err != nil {
		return nil, http.StatusUnauthorized, "invalid_token", "The access token's user no longer exists."
	}
	if !user.IsActive {
		return nil, http.StatusForbidden, "account_inactive", "The account is deactivated."
	}
	scopeUser, storeID, status, msg := ResolveStoreScope(c, claims)
	if status != 0 {
		return nil, status, "store_unavailable", msg
	}
	if storeID == nil {
		return nil, http.StatusForbidden, "store_unavailable", "No active store is available for this token."
	}
	p := &publicPrincipal{actor: user, storeID: *storeID, scopeUser: scopeUser}
	if claims.Scope != "" {
		p.credType = "oauth"
		p.credID = claims.ClientID
		p.credName = fmt.Sprintf("%s via %s", user.Name, claims.ClientID)
		p.scopes = utils.ParseScopes(claims.Scope)
	} else {
		// A first-party session token (TruERP web/mobile sign-in).
		p.credType = "session"
		p.credID = user.ID.String()
		p.credName = user.Name
		p.scopes = utils.AllPublicScopes()
	}
	return p, 0, "", ""
}

var apiKeyTouch = struct {
	mu   sync.Mutex
	last map[uuid.UUID]time.Time
}{last: map[uuid.UUID]time.Time{}}

// touchAPIKey records last use at most once a minute per key.
func touchAPIKey(id uuid.UUID, ip string) {
	now := time.Now()
	apiKeyTouch.mu.Lock()
	if now.Sub(apiKeyTouch.last[id]) < time.Minute {
		apiKeyTouch.mu.Unlock()
		return
	}
	apiKeyTouch.last[id] = now
	apiKeyTouch.mu.Unlock()
	go utils.DB.Model(&models.APIKey{}).Where("id = ?", id).Updates(map[string]interface{}{"last_used_at": now, "last_used_ip": ip})
}

// RequireScope rejects requests whose credential lacks scope.
func RequireScope(scope string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !HasPublicScope(c, scope) {
			utils.PublicAPIError(c, http.StatusForbidden, "insufficient_scope",
				fmt.Sprintf("This credential is missing the %q scope (or the store's policy does not allow it).", scope),
				gin.H{"required_scope": scope})
			return
		}
		c.Next()
	}
}

// HasPublicScope reports whether the current credential has scope.
func HasPublicScope(c *gin.Context, scope string) bool {
	v, ok := c.Get(utils.CtxPublicScopes)
	if !ok {
		return false
	}
	set, _ := v.(map[string]bool)
	return set[scope]
}

// ---- request log (asynchronous, batched) ----

var (
	requestLogOnce sync.Once
	requestLogCh   chan models.APIRequestLog
)

func startRequestLogWriter() {
	requestLogCh = make(chan models.APIRequestLog, 2048)
	go func() {
		batch := make([]models.APIRequestLog, 0, 100)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		flush := func() {
			if len(batch) == 0 || utils.DB == nil {
				batch = batch[:0]
				return
			}
			if err := utils.DB.CreateInBatches(&batch, 100).Error; err != nil {
				log.Printf("public api request log: %v", err)
			}
			batch = batch[:0]
		}
		for {
			select {
			case entry := <-requestLogCh:
				batch = append(batch, entry)
				if len(batch) >= 100 {
					flush()
				}
			case <-ticker.C:
				flush()
			}
		}
	}()
}

func logPublicRequest(c *gin.Context, p *publicPrincipal, started time.Time) {
	requestLogOnce.Do(startRequestLogWriter)
	entry := models.APIRequestLog{
		ID:         uuid.New(),
		Method:     c.Request.Method,
		Path:       c.Request.URL.Path,
		StatusCode: c.Writer.Status(),
		DurationMs: time.Since(started).Milliseconds(),
		IPAddress:  c.ClientIP(),
		RequestID:  c.GetString("request_id"),
		ErrorCode:  c.GetString("public_error_code"),
		CreatedAt:  time.Now(),
	}
	if p != nil {
		sid := p.storeID
		entry.StoreID = &sid
		entry.CredentialType = p.credType
		entry.CredentialID = p.credID
		entry.CredentialName = p.credName
	}
	select {
	case requestLogCh <- entry:
	default: // never block a request on logging
	}
}
