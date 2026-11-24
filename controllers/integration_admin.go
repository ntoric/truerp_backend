package controllers

// Super-admin management of the public API: API keys, per-store permission
// policies, OAuth client applications, webhook endpoints and request logs.
// Mounted under /api/v1/admin/integrations (AuthRequired + SuperAdminRequired).

import (
	"net/http"
	"strconv"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// saveWithBools saves a record and then writes boolean columns explicitly:
// GORM's Create omits false for columns with a `default:true`, which would
// silently turn "disabled" into the database default.
func saveWithBools(record interface{}, bools map[string]interface{}) error {
	if err := utils.DB.Save(record).Error; err != nil {
		return err
	}
	return utils.DB.Model(record).Updates(bools).Error
}

func adminActorID(c *gin.Context) uuid.UUID {
	if v, ok := c.Get("actor_user_id"); ok {
		if id, ok := v.(uuid.UUID); ok {
			return id
		}
	}
	return uuid.Nil
}

func storeNameMap(ids []uuid.UUID) map[uuid.UUID]string {
	out := map[uuid.UUID]string{}
	if len(ids) == 0 {
		return out
	}
	var stores []models.Store
	utils.DB.Select("id", "name").Where("id IN ?", ids).Find(&stores)
	for _, s := range stores {
		out[s.ID] = s.Name
	}
	return out
}

// validateScopeList checks every scope is a fine-grained public scope.
func validateScopeList(scopes []string) (string, bool) {
	if len(scopes) == 0 {
		return "at least one scope is required", false
	}
	known := map[string]bool{}
	for _, s := range utils.AllPublicScopes() {
		known[s] = true
	}
	for _, s := range scopes {
		if !known[s] {
			return "unknown scope: " + s, false
		}
	}
	return "", true
}

// ---------------- catalog & overview ----------------

// AdminIntegrationCatalog returns the scope catalog and webhook event types.
func AdminIntegrationCatalog(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"scopes":      utils.PublicScopeCatalog,
		"event_types": WebhookEventTypes,
		"grant_types": supportedGrantTypes,
		"base_path":   "/api/public/v1",
		"docs_path":   "/api/public/docs",
		"spec_path":   "/api/public/v1/openapi.yaml",
	})
}

// AdminIntegrationOverview summarises usage over the last 24 hours.
func AdminIntegrationOverview(c *gin.Context) {
	since := time.Now().Add(-24 * time.Hour)
	var activeKeys, oauthClients, webhooks, failingWebhooks, pendingDeliveries int64
	utils.DB.Model(&models.APIKey{}).Where("revoked_at IS NULL").Count(&activeKeys)
	utils.DB.Model(&models.OAuthClient{}).Where("is_active = ?", true).Count(&oauthClients)
	utils.DB.Model(&models.WebhookEndpoint{}).Where("is_active = ?", true).Count(&webhooks)
	utils.DB.Model(&models.WebhookEndpoint{}).Where("failure_count > 0").Count(&failingWebhooks)
	utils.DB.Model(&models.WebhookDelivery{}).Where("status = ?", "pending").Count(&pendingDeliveries)

	var total, clientErrors, serverErrors int64
	utils.DB.Model(&models.APIRequestLog{}).Where("created_at >= ?", since).Count(&total)
	utils.DB.Model(&models.APIRequestLog{}).Where("created_at >= ? AND status_code >= 400 AND status_code < 500", since).Count(&clientErrors)
	utils.DB.Model(&models.APIRequestLog{}).Where("created_at >= ? AND status_code >= 500", since).Count(&serverErrors)

	type storeCount struct {
		StoreID *uuid.UUID `json:"store_id"`
		Count   int64      `json:"count"`
	}
	var perStore []storeCount
	utils.DB.Model(&models.APIRequestLog{}).Select("store_id, COUNT(*) AS count").
		Where("created_at >= ? AND store_id IS NOT NULL", since).Group("store_id").Order("count DESC").Limit(10).Scan(&perStore)
	ids := []uuid.UUID{}
	for _, s := range perStore {
		ids = append(ids, *s.StoreID)
	}
	names := storeNameMap(ids)
	top := []gin.H{}
	for _, s := range perStore {
		top = append(top, gin.H{"store_id": s.StoreID, "store_name": names[*s.StoreID], "requests": s.Count})
	}

	c.JSON(http.StatusOK, gin.H{
		"active_api_keys":      activeKeys,
		"active_oauth_clients": oauthClients,
		"active_webhooks":      webhooks,
		"failing_webhooks":     failingWebhooks,
		"pending_deliveries":   pendingDeliveries,
		"requests_24h":         total,
		"client_errors_24h":    clientErrors,
		"server_errors_24h":    serverErrors,
		"top_stores_24h":       top,
	})
}

// ---------------- API keys ----------------

type adminAPIKeyView struct {
	models.APIKey
	StoreName     string   `json:"store_name"`
	ScopeList     []string `json:"scope_list"`
	CreatedByName string   `json:"created_by_name"`
	Status        string   `json:"status"` // active | revoked | expired
}

func apiKeyStatus(k models.APIKey) string {
	switch {
	case k.RevokedAt != nil:
		return "revoked"
	case k.ExpiresAt != nil && time.Now().After(*k.ExpiresAt):
		return "expired"
	default:
		return "active"
	}
}

func AdminListAPIKeys(c *gin.Context) {
	q := utils.DB.Model(&models.APIKey{}).Order("created_at DESC")
	if s := c.Query("store_id"); s != "" {
		q = q.Where("store_id = ?", s)
	}
	var keys []models.APIKey
	if err := q.Find(&keys).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load API keys"})
		return
	}
	storeIDs, userIDs := []uuid.UUID{}, []uuid.UUID{}
	for _, k := range keys {
		storeIDs = append(storeIDs, k.StoreID)
		userIDs = append(userIDs, k.CreatedByUserID)
	}
	stores := storeNameMap(storeIDs)
	users := map[uuid.UUID]string{}
	if len(userIDs) > 0 {
		var us []models.User
		utils.DB.Select("id", "name").Where("id IN ?", userIDs).Find(&us)
		for _, u := range us {
			users[u.ID] = u.Name
		}
	}
	out := make([]adminAPIKeyView, 0, len(keys))
	for _, k := range keys {
		out = append(out, adminAPIKeyView{
			APIKey: k, StoreName: stores[k.StoreID], ScopeList: utils.ParseScopes(k.Scopes),
			CreatedByName: users[k.CreatedByUserID], Status: apiKeyStatus(k),
		})
	}
	c.JSON(http.StatusOK, out)
}

func AdminCreateAPIKey(c *gin.Context) {
	var in struct {
		StoreID   uuid.UUID  `json:"store_id" binding:"required"`
		Name      string     `json:"name" binding:"required"`
		Scopes    []string   `json:"scopes"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "store_id and name are required"})
		return
	}
	store, err := utils.FindStoreByID(utils.DB, in.StoreID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Store not found"})
		return
	}
	if msg, ok := validateScopeList(in.Scopes); !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": msg})
		return
	}
	if in.ExpiresAt != nil && in.ExpiresAt.Before(time.Now()) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Expiry must be in the future"})
		return
	}
	random, err := generateRandomSecret(32)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate key"})
		return
	}
	secret := utils.APIKeyPrefix + random
	key := models.APIKey{
		StoreID: store.ID, Name: strings.TrimSpace(in.Name), Prefix: secret[:len(utils.APIKeyPrefix)+6],
		SecretHash: utils.HashAPIKey(secret), Scopes: utils.JoinScopes(in.Scopes),
		CreatedByUserID: adminActorID(c), ExpiresAt: in.ExpiresAt,
	}
	if err := utils.DB.Create(&key).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create API key"})
		return
	}
	ensureScanCursor(store.ID)
	c.JSON(http.StatusCreated, gin.H{
		"api_key": adminAPIKeyView{APIKey: key, StoreName: store.Name, ScopeList: in.Scopes, Status: "active"},
		"secret":  secret,
		"notice":  "Copy this key now. It will not be shown again.",
	})
}

func AdminUpdateAPIKey(c *gin.Context) {
	var key models.APIKey
	if err := utils.DB.First(&key, "id = ?", c.Param("id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "API key not found"})
		return
	}
	var in struct {
		Name        *string    `json:"name"`
		Scopes      *[]string  `json:"scopes"`
		ExpiresAt   *time.Time `json:"expires_at"`
		ClearExpiry bool       `json:"clear_expiry"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}
	updates := map[string]interface{}{}
	if in.Name != nil && strings.TrimSpace(*in.Name) != "" {
		updates["name"] = strings.TrimSpace(*in.Name)
	}
	if in.Scopes != nil {
		if msg, ok := validateScopeList(*in.Scopes); !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": msg})
			return
		}
		updates["scopes"] = utils.JoinScopes(*in.Scopes)
	}
	if in.ExpiresAt != nil {
		updates["expires_at"] = in.ExpiresAt
	} else if in.ClearExpiry {
		updates["expires_at"] = nil
	}
	if len(updates) > 0 {
		utils.DB.Model(&key).Updates(updates)
	}
	utils.DB.First(&key, "id = ?", key.ID)
	c.JSON(http.StatusOK, adminAPIKeyView{APIKey: key, ScopeList: utils.ParseScopes(key.Scopes), Status: apiKeyStatus(key)})
}

func AdminRevokeAPIKey(c *gin.Context) {
	now := time.Now()
	res := utils.DB.Model(&models.APIKey{}).Where("id = ? AND revoked_at IS NULL", c.Param("id")).Update("revoked_at", now)
	if res.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "API key not found or already revoked"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "API key revoked"})
}

func AdminDeleteAPIKey(c *gin.Context) {
	res := utils.DB.Where("id = ?", c.Param("id")).Delete(&models.APIKey{})
	if res.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "API key not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "API key deleted"})
}

// ---------------- per-store permission policies ----------------

type adminPolicyView struct {
	models.IntegrationPolicy
	StoreName     string   `json:"store_name"`
	StoreCode     string   `json:"store_code"`
	StoreActive   bool     `json:"store_active"`
	ScopeList     []string `json:"scope_list"`
	IsDefault     bool     `json:"is_default"` // no explicit policy saved yet
	ActiveAPIKeys int64    `json:"active_api_keys"`
	Webhooks      int64    `json:"webhooks"`
	OAuthClients  int64    `json:"oauth_clients"`
}

func buildPolicyView(store models.Store) adminPolicyView {
	var p models.IntegrationPolicy
	isDefault := false
	if err := utils.DB.Where("store_id = ?", store.ID).First(&p).Error; err != nil {
		p = utils.DefaultIntegrationPolicy(store.ID)
		isDefault = true
	}
	v := adminPolicyView{
		IntegrationPolicy: p, StoreName: store.Name, StoreCode: store.Code, StoreActive: store.IsActive,
		ScopeList: utils.ParseScopes(p.AllowedScopes), IsDefault: isDefault,
	}
	utils.DB.Model(&models.APIKey{}).Where("store_id = ? AND revoked_at IS NULL", store.ID).Count(&v.ActiveAPIKeys)
	utils.DB.Model(&models.WebhookEndpoint{}).Where("store_id = ?", store.ID).Count(&v.Webhooks)
	utils.DB.Model(&models.OAuthClient{}).Where("store_id = ?", store.ID).Count(&v.OAuthClients)
	return v
}

func AdminListIntegrationPolicies(c *gin.Context) {
	var stores []models.Store
	utils.DB.Order("name ASC").Find(&stores)
	out := make([]adminPolicyView, 0, len(stores))
	for _, s := range stores {
		out = append(out, buildPolicyView(s))
	}
	c.JSON(http.StatusOK, out)
}

func AdminUpdateIntegrationPolicy(c *gin.Context) {
	storeID, err := uuid.Parse(c.Param("store_id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid store id"})
		return
	}
	store, err := utils.FindStoreByID(utils.DB, storeID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Store not found"})
		return
	}
	var in struct {
		APIEnabled         *bool     `json:"api_enabled"`
		AllowedScopes      *[]string `json:"allowed_scopes"`
		RateLimitPerMinute *int      `json:"rate_limit_per_minute"`
		WebhooksEnabled    *bool     `json:"webhooks_enabled"`
		AllowedIPs         *string   `json:"allowed_ips"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}
	var p models.IntegrationPolicy
	if err := utils.DB.Where("store_id = ?", storeID).First(&p).Error; err != nil {
		p = utils.DefaultIntegrationPolicy(storeID)
	}
	if in.APIEnabled != nil {
		p.APIEnabled = *in.APIEnabled
	}
	if in.WebhooksEnabled != nil {
		p.WebhooksEnabled = *in.WebhooksEnabled
	}
	if in.AllowedScopes != nil {
		if len(*in.AllowedScopes) > 0 {
			if msg, ok := validateScopeList(*in.AllowedScopes); !ok {
				c.JSON(http.StatusBadRequest, gin.H{"error": msg})
				return
			}
		}
		p.AllowedScopes = utils.JoinScopes(*in.AllowedScopes)
	}
	if in.RateLimitPerMinute != nil {
		if *in.RateLimitPerMinute < 1 || *in.RateLimitPerMinute > 6000 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Rate limit must be between 1 and 6000 requests per minute"})
			return
		}
		p.RateLimitPerMinute = *in.RateLimitPerMinute
	}
	if in.AllowedIPs != nil {
		if err := utils.ValidateIPAllowList(*in.AllowedIPs); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		p.AllowedIPs = strings.Join(utils.ParseScopes(*in.AllowedIPs), ", ")
	}
	actor := adminActorID(c)
	p.UpdatedBy = &actor
	if err := saveWithBools(&p, map[string]interface{}{"api_enabled": p.APIEnabled, "webhooks_enabled": p.WebhooksEnabled}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save policy"})
		return
	}
	c.JSON(http.StatusOK, buildPolicyView(store))
}

// ---------------- OAuth clients ----------------

type adminOAuthClientView struct {
	models.OAuthClient
	StoreName       string   `json:"store_name"`
	GrantTypeList   []string `json:"grant_type_list"`
	ScopeList       []string `json:"scope_list"`
	RedirectURIList []string `json:"redirect_uri_list"`
	ActiveTokens    int64    `json:"active_tokens"`
}

func buildOAuthClientView(cl models.OAuthClient, stores map[uuid.UUID]string) adminOAuthClientView {
	v := adminOAuthClientView{
		OAuthClient: cl, GrantTypeList: splitList(cl.GrantTypes), ScopeList: splitList(cl.Scopes),
		RedirectURIList: clientRedirectURIs(&cl),
	}
	if cl.StoreID != nil {
		v.StoreName = stores[*cl.StoreID]
	}
	utils.DB.Model(&models.OAuthRefreshToken{}).
		Where("client_id = ? AND revoked = ? AND expires_at > ?", cl.ClientID, false, time.Now()).Count(&v.ActiveTokens)
	return v
}

func ListOAuthClients(c *gin.Context) {
	var clients []models.OAuthClient
	if err := utils.DB.Order("created_at DESC").Find(&clients).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	ids := []uuid.UUID{}
	for _, cl := range clients {
		if cl.StoreID != nil {
			ids = append(ids, *cl.StoreID)
		}
	}
	stores := storeNameMap(ids)
	out := make([]adminOAuthClientView, 0, len(clients))
	for _, cl := range clients {
		out = append(out, buildOAuthClientView(cl, stores))
	}
	c.JSON(http.StatusOK, out)
}

type oauthClientInput struct {
	ClientName   *string   `json:"client_name"`
	Description  *string   `json:"description"`
	RedirectURIs *string   `json:"redirect_uris"`
	GrantTypes   *[]string `json:"grant_types"`
	Scopes       *[]string `json:"scopes"`
	IsPublic     *bool     `json:"is_public"`
	IsActive     *bool     `json:"is_active"`
	StoreID      *string   `json:"store_id"` // "" clears the binding
}

func applyOAuthClientInput(cl *models.OAuthClient, in oauthClientInput) string {
	if in.ClientName != nil {
		if strings.TrimSpace(*in.ClientName) == "" {
			return "Application name is required"
		}
		cl.ClientName = strings.TrimSpace(*in.ClientName)
	}
	if in.Description != nil {
		cl.Description = strings.TrimSpace(*in.Description)
	}
	if in.IsPublic != nil {
		cl.IsPublic = *in.IsPublic
	}
	if in.IsActive != nil {
		cl.IsActive = *in.IsActive
	}
	if in.StoreID != nil {
		if *in.StoreID == "" {
			cl.StoreID = nil
		} else {
			id, err := uuid.Parse(*in.StoreID)
			if err != nil {
				return "Invalid store"
			}
			if _, err := utils.FindStoreByID(utils.DB, id); err != nil {
				return "Store not found"
			}
			cl.StoreID = &id
		}
	}
	if in.GrantTypes != nil {
		allowed := map[string]bool{}
		for _, g := range supportedGrantTypes {
			allowed[g] = true
		}
		if len(*in.GrantTypes) == 0 {
			return "Select at least one grant type"
		}
		for _, g := range *in.GrantTypes {
			if !allowed[g] {
				return "Unsupported grant type: " + g
			}
		}
		cl.GrantTypes = strings.Join(*in.GrantTypes, ",")
	}
	if in.Scopes != nil {
		if len(*in.Scopes) == 0 {
			return "Select at least one scope"
		}
		for _, s := range *in.Scopes {
			if !utils.IsKnownScope(s) {
				return "Unknown scope: " + s
			}
		}
		cl.Scopes = utils.JoinScopes(*in.Scopes)
	}
	if in.RedirectURIs != nil {
		cl.RedirectURIs = strings.Join(splitList(*in.RedirectURIs), ",")
	}
	// Cross-field rules.
	if clientAllowsGrant(cl, grantAuthorizationCode) {
		if err := validateRedirectURIList(cl.RedirectURIs); err != nil {
			return err.Error()
		}
	}
	if cl.IsPublic && (clientAllowsGrant(cl, grantClientCredentials) || clientAllowsGrant(cl, grantPassword)) {
		return "Public clients can only use authorization_code and refresh_token"
	}
	if clientAllowsGrant(cl, grantClientCredentials) && cl.StoreID == nil {
		return "client_credentials requires binding the application to a store"
	}
	return ""
}

func CreateOAuthClient(c *gin.Context) {
	var in oauthClientInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}
	if in.ClientName == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Application name is required"})
		return
	}
	if in.GrantTypes == nil {
		g := []string{grantAuthorizationCode, grantRefreshToken}
		in.GrantTypes = &g
	}
	if in.Scopes == nil {
		s := []string{utils.ScopeLegacyRead}
		in.Scopes = &s
	}
	idPart, _ := generateRandomSecret(12)
	cl := models.OAuthClient{ClientID: "trp_client_" + idPart, IsActive: true, RedirectURIs: ""}
	if msg := applyOAuthClientInput(&cl, in); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msg})
		return
	}
	secret := ""
	if !cl.IsPublic {
		s, _ := generateRandomSecret(32)
		secret = "trp_secret_" + s
		cl.ClientSecret = hashClientSecret(secret)
	}
	if err := saveWithBools(&cl, map[string]interface{}{"is_active": cl.IsActive, "is_public": cl.IsPublic}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create application"})
		return
	}
	if cl.StoreID != nil {
		ensureScanCursor(*cl.StoreID)
	}
	resp := gin.H{"client": buildOAuthClientView(cl, storeNameMap(storeIDList(cl.StoreID)))}
	if secret != "" {
		resp["client_secret"] = secret
		resp["notice"] = "Copy the client secret now. It will not be shown again."
	}
	c.JSON(http.StatusCreated, resp)
}

func storeIDList(id *uuid.UUID) []uuid.UUID {
	if id == nil {
		return nil
	}
	return []uuid.UUID{*id}
}

func loadOAuthClientParam(c *gin.Context) (models.OAuthClient, bool) {
	var cl models.OAuthClient
	id := c.Param("id")
	q := utils.DB.Where("client_id = ?", id)
	if parsed, err := uuid.Parse(id); err == nil {
		q = utils.DB.Where("id = ?", parsed)
	}
	if err := q.First(&cl).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Application not found"})
		return cl, false
	}
	return cl, true
}

func UpdateOAuthClient(c *gin.Context) {
	cl, ok := loadOAuthClientParam(c)
	if !ok {
		return
	}
	var in oauthClientInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}
	wasPublic := cl.IsPublic
	if msg := applyOAuthClientInput(&cl, in); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msg})
		return
	}
	resp := gin.H{}
	if wasPublic && !cl.IsPublic {
		s, _ := generateRandomSecret(32)
		secret := "trp_secret_" + s
		cl.ClientSecret = hashClientSecret(secret)
		resp["client_secret"] = secret
		resp["notice"] = "The application is now confidential. Copy the new client secret now."
	}
	if cl.IsPublic {
		cl.ClientSecret = ""
	}
	if err := utils.DB.Save(&cl).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update application"})
		return
	}
	if !cl.IsActive {
		utils.DB.Model(&models.OAuthRefreshToken{}).Where("client_id = ?", cl.ClientID).Update("revoked", true)
	}
	resp["client"] = buildOAuthClientView(cl, storeNameMap(storeIDList(cl.StoreID)))
	c.JSON(http.StatusOK, resp)
}

func RotateOAuthClientSecret(c *gin.Context) {
	cl, ok := loadOAuthClientParam(c)
	if !ok {
		return
	}
	if cl.IsPublic {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Public applications have no secret"})
		return
	}
	s, _ := generateRandomSecret(32)
	secret := "trp_secret_" + s
	utils.DB.Model(&cl).Update("client_secret", hashClientSecret(secret))
	c.JSON(http.StatusOK, gin.H{"client_secret": secret, "notice": "Copy the client secret now. The previous secret no longer works."})
}

func RevokeOAuthClientTokens(c *gin.Context) {
	cl, ok := loadOAuthClientParam(c)
	if !ok {
		return
	}
	res := utils.DB.Model(&models.OAuthRefreshToken{}).Where("client_id = ? AND revoked = ?", cl.ClientID, false).Update("revoked", true)
	c.JSON(http.StatusOK, gin.H{"revoked": res.RowsAffected, "notice": "Refresh tokens revoked. Issued access tokens expire within one hour."})
}

func DeleteOAuthClient(c *gin.Context) {
	cl, ok := loadOAuthClientParam(c)
	if !ok {
		return
	}
	utils.DB.Model(&models.OAuthRefreshToken{}).Where("client_id = ?", cl.ClientID).Update("revoked", true)
	utils.DB.Where("client_id = ?", cl.ClientID).Delete(&models.OAuthCode{})
	utils.DB.Delete(&cl)
	c.JSON(http.StatusOK, gin.H{"message": "Application deleted"})
}

// ---------------- webhooks (admin) ----------------

func AdminListWebhooks(c *gin.Context) {
	q := utils.DB.Model(&models.WebhookEndpoint{}).Order("created_at DESC")
	if s := c.Query("store_id"); s != "" {
		q = q.Where("store_id = ?", s)
	}
	var rows []models.WebhookEndpoint
	q.Find(&rows)
	ids := []uuid.UUID{}
	for _, w := range rows {
		ids = append(ids, w.StoreID)
	}
	stores := storeNameMap(ids)
	out := []gin.H{}
	for _, w := range rows {
		pw := toPublicWebhook(w)
		var pending int64
		utils.DB.Model(&models.WebhookDelivery{}).Where("endpoint_id = ? AND status = ?", w.ID, "pending").Count(&pending)
		out = append(out, gin.H{
			"id": pw.ID, "store_id": w.StoreID, "store_name": stores[w.StoreID], "url": pw.URL,
			"description": pw.Description, "events": pw.Events, "is_active": pw.IsActive,
			"disabled_reason": pw.DisabledReason, "failure_count": pw.FailureCount,
			"last_delivery_at": pw.LastDeliveryAt, "created_via": w.CreatedVia,
			"pending_deliveries": pending, "created_at": pw.CreatedAt,
		})
	}
	c.JSON(http.StatusOK, out)
}

func AdminCreateWebhook(c *gin.Context) {
	var in struct {
		StoreID     uuid.UUID `json:"store_id"`
		URL         string    `json:"url"`
		Description string    `json:"description"`
		Events      []string  `json:"events"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || in.StoreID == uuid.Nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "store_id and url are required"})
		return
	}
	if _, err := utils.FindStoreByID(utils.DB, in.StoreID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Store not found"})
		return
	}
	events := in.Events
	w, secret, status, _, msg := createWebhookEndpoint(in.StoreID, publicWebhookInput{URL: &in.URL, Description: &in.Description, Events: &events}, "admin")
	if status != 0 {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"webhook": toPublicWebhook(w), "secret": secret, "notice": "Copy the signing secret now. It will not be shown again."})
}

func loadWebhookAdmin(c *gin.Context) (models.WebhookEndpoint, bool) {
	var w models.WebhookEndpoint
	if err := utils.DB.First(&w, "id = ?", c.Param("id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Webhook not found"})
		return w, false
	}
	return w, true
}

func AdminUpdateWebhook(c *gin.Context) {
	w, ok := loadWebhookAdmin(c)
	if !ok {
		return
	}
	var in publicWebhookInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}
	if err := applyWebhookUpdate(&w, in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	utils.DB.Save(&w)
	c.JSON(http.StatusOK, toPublicWebhook(w))
}

func AdminDeleteWebhook(c *gin.Context) {
	w, ok := loadWebhookAdmin(c)
	if !ok {
		return
	}
	utils.DB.Where("endpoint_id = ?", w.ID).Delete(&models.WebhookDelivery{})
	utils.DB.Delete(&w)
	c.JSON(http.StatusOK, gin.H{"message": "Webhook deleted"})
}

func AdminTestWebhook(c *gin.Context) {
	w, ok := loadWebhookAdmin(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, sendTestWebhook(w))
}

func AdminListWebhookDeliveries(c *gin.Context) {
	w, ok := loadWebhookAdmin(c)
	if !ok {
		return
	}
	var rows []models.WebhookDelivery
	q := utils.DB.Where("endpoint_id = ?", w.ID).Order("created_at DESC").Limit(100)
	if s := c.Query("status"); s != "" {
		q = q.Where("status = ?", s)
	}
	q.Find(&rows)
	out := make([]gin.H, 0, len(rows))
	for _, d := range rows {
		out = append(out, publicDelivery(d))
	}
	c.JSON(http.StatusOK, out)
}

func AdminRetryWebhookDelivery(c *gin.Context) {
	now := time.Now()
	res := utils.DB.Model(&models.WebhookDelivery{}).Where("id = ?", c.Param("delivery_id")).
		Updates(map[string]interface{}{"status": "pending", "next_attempt_at": now})
	if res.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Delivery not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Delivery queued for retry"})
}

// ---------------- request logs ----------------

func AdminListAPIRequestLogs(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if limit < 1 || limit > 200 {
		limit = 50
	}
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if offset < 0 {
		offset = 0
	}
	q := utils.DB.Model(&models.APIRequestLog{})
	if s := c.Query("store_id"); s != "" {
		q = q.Where("store_id = ?", s)
	}
	if s := c.Query("credential_id"); s != "" {
		q = q.Where("credential_id = ?", s)
	}
	switch c.Query("status") {
	case "success":
		q = q.Where("status_code < 400")
	case "client_error":
		q = q.Where("status_code >= 400 AND status_code < 500")
	case "server_error":
		q = q.Where("status_code >= 500")
	}
	var total int64
	q.Count(&total)
	var rows []models.APIRequestLog
	q.Order("created_at DESC").Limit(limit).Offset(offset).Find(&rows)
	ids := []uuid.UUID{}
	for _, r := range rows {
		if r.StoreID != nil {
			ids = append(ids, *r.StoreID)
		}
	}
	stores := storeNameMap(ids)
	out := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		name := ""
		if r.StoreID != nil {
			name = stores[*r.StoreID]
		}
		out = append(out, gin.H{
			"id": r.ID, "store_id": r.StoreID, "store_name": name, "credential_type": r.CredentialType,
			"credential_id": r.CredentialID, "credential_name": r.CredentialName, "method": r.Method,
			"path": r.Path, "status_code": r.StatusCode, "duration_ms": r.DurationMs, "ip_address": r.IPAddress,
			"request_id": r.RequestID, "error_code": r.ErrorCode, "created_at": r.CreatedAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"logs": out, "total": total, "limit": limit, "offset": offset})
}
