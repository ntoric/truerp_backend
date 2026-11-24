package controllers

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// OAuth 2.0 authorization server (RFC 6749) with PKCE (RFC 7636), metadata
// discovery (RFC 8414) and token revocation (RFC 7009).
//
// Security properties:
//   - redirect_uri must exactly match one registered for the client;
//   - client secrets and refresh tokens are stored as SHA-256 digests;
//   - each client is limited to its registered grant types and scopes;
//   - public clients must use PKCE with S256;
//   - authorization codes are single-use (atomic claim) and short-lived;
//   - refresh tokens rotate, and replaying a rotated token revokes the family;
//   - client_credentials only works for confidential clients bound to a
//     store, and acts as that store's owner — never as a super admin;
//   - access tokens carry their scopes and expire after one hour.

const (
	oauthCodeTTL         = 10 * time.Minute
	oauthRefreshTokenTTL = 30 * 24 * time.Hour

	grantAuthorizationCode = "authorization_code"
	grantRefreshToken      = "refresh_token"
	grantClientCredentials = "client_credentials"
	grantPassword          = "password"

	hashedSecretPrefix = "sha256:"
	legacyMCPSecret    = "truerp-mcp-secret"
	defaultMCPClientID = "truerp-mcp-client"
)

var supportedGrantTypes = []string{grantAuthorizationCode, grantRefreshToken, grantClientCredentials, grantPassword}

// generateRandomSecret generates a URL-safe random string of n bytes
func generateRandomSecret(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func hashClientSecret(secret string) string {
	return hashedSecretPrefix + sha256Hex(secret)
}

// verifyCodeChallenge implements PKCE validation (RFC 7636)
func verifyCodeChallenge(verifier, challenge, method string) bool {
	if challenge == "" {
		return true
	}
	switch strings.ToUpper(method) {
	case "S256":
		h := sha256.Sum256([]byte(verifier))
		computed := base64.RawURLEncoding.EncodeToString(h[:])
		return subtle.ConstantTimeCompare([]byte(computed), []byte(challenge)) == 1
	case "PLAIN", "":
		return subtle.ConstantTimeCompare([]byte(verifier), []byte(challenge)) == 1
	default:
		return false
	}
}

// verifyClientSecret checks a presented secret against the stored one,
// upgrading legacy plaintext secrets to the hashed form.
func verifyClientSecret(db *gorm.DB, client *models.OAuthClient, presented string) bool {
	if presented == "" || client.ClientSecret == "" {
		return false
	}
	if strings.HasPrefix(client.ClientSecret, hashedSecretPrefix) {
		return subtle.ConstantTimeCompare([]byte(hashClientSecret(presented)), []byte(client.ClientSecret)) == 1
	}
	if subtle.ConstantTimeCompare([]byte(presented), []byte(client.ClientSecret)) != 1 {
		return false
	}
	client.ClientSecret = hashClientSecret(presented)
	db.Model(&models.OAuthClient{}).Where("id = ?", client.ID).Update("client_secret", client.ClientSecret)
	return true
}

func splitList(raw string) []string {
	return utils.ParseScopes(raw) // same separators: space, comma, newline
}

func clientAllowsGrant(client *models.OAuthClient, grant string) bool {
	for _, g := range splitList(client.GrantTypes) {
		if g == grant {
			return true
		}
	}
	return false
}

func clientRedirectURIs(client *models.OAuthClient) []string {
	return splitList(client.RedirectURIs)
}

// resolveRedirectURI returns the redirect URI to use, which must exactly match
// a registered one. When none is supplied and exactly one is registered, that
// one is used.
func resolveRedirectURI(client *models.OAuthClient, requested string) (string, bool) {
	registered := clientRedirectURIs(client)
	if requested == "" {
		if len(registered) == 1 {
			return registered[0], true
		}
		return "", false
	}
	for _, r := range registered {
		if r == requested {
			return requested, true
		}
	}
	return "", false
}

// validateRedirectURIList enforces https (or http on loopback) and no fragments.
func validateRedirectURIList(raw string) error {
	uris := splitList(raw)
	if len(uris) == 0 {
		return errors.New("at least one redirect URI is required")
	}
	for _, u := range uris {
		parsed, err := url.Parse(u)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("invalid redirect URI %q", u)
		}
		if parsed.Fragment != "" {
			return fmt.Errorf("redirect URI %q must not contain a fragment", u)
		}
		host := parsed.Hostname()
		loopback := host == "localhost" || host == "127.0.0.1" || host == "::1"
		if parsed.Scheme != "https" && !(parsed.Scheme == "http" && loopback) {
			return fmt.Errorf("redirect URI %q must use https (http is allowed only for localhost)", u)
		}
	}
	return nil
}

// resolveGrantedScopes validates requested scopes against the client's
// registered scopes. An empty request grants all of the client's scopes.
func resolveGrantedScopes(client *models.OAuthClient, requested string) (string, error) {
	clientScopes := splitList(client.Scopes)
	req := splitList(requested)
	if len(req) == 0 {
		return utils.JoinScopes(clientScopes), nil
	}
	allowedRaw := map[string]bool{}
	for _, s := range clientScopes {
		allowedRaw[s] = true
	}
	expanded := utils.ExpandScopes(clientScopes)
	for _, s := range req {
		if !utils.IsKnownScope(s) {
			return "", fmt.Errorf("unknown scope %q", s)
		}
		if allowedRaw[s] || expanded[s] {
			continue
		}
		return "", fmt.Errorf("scope %q is not allowed for this client", s)
	}
	return utils.JoinScopes(req), nil
}

func oauthError(c *gin.Context, status int, code, description string) {
	c.Header("Cache-Control", "no-store")
	c.JSON(status, gin.H{"error": code, "error_description": description})
}

// SeedDefaultOAuthClients provisions the MCP server's client from the
// TRUERP_MCP_CLIENT_SECRET environment variable. Without that variable no
// client is created, and an existing client still using the publicly
// documented legacy secret is deactivated.
func SeedDefaultOAuthClients(db *gorm.DB) {
	if !db.Migrator().HasTable(&models.OAuthClient{}) {
		return
	}
	secret := strings.TrimSpace(os.Getenv("TRUERP_MCP_CLIENT_SECRET"))

	var existing models.OAuthClient
	err := db.Where("client_id = ?", defaultMCPClientID).First(&existing).Error
	if err == nil {
		usesLegacy := existing.ClientSecret == legacyMCPSecret || existing.ClientSecret == hashClientSecret(legacyMCPSecret)
		switch {
		case secret != "" && existing.ClientSecret != hashClientSecret(secret):
			db.Model(&existing).Updates(map[string]interface{}{"client_secret": hashClientSecret(secret), "is_active": true})
		case secret == "" && usesLegacy && existing.IsActive:
			log.Printf("SECURITY: deactivating OAuth client %q because it uses the publicly documented default secret; set TRUERP_MCP_CLIENT_SECRET to re-enable it", defaultMCPClientID)
			db.Model(&existing).Update("is_active", false)
		}
		return
	}
	if secret == "" {
		return
	}
	client := models.OAuthClient{
		ClientID:     defaultMCPClientID,
		ClientSecret: hashClientSecret(secret),
		ClientName:   "TruERP MCP Server",
		RedirectURIs: "http://localhost:3000/callback,http://localhost:8089/callback,http://127.0.0.1:8089/callback,https://oauth.pstmn.io/v1/callback",
		GrantTypes:   "authorization_code,refresh_token",
		Scopes:       "read write admin erp",
		IsActive:     true,
	}
	if err := db.Create(&client).Error; err != nil {
		log.Printf("SeedDefaultOAuthClients error: %v", err)
	}
}

// extractClientCredentials extracts client_id and client_secret from HTTP
// Basic auth or the form/JSON body.
func extractClientCredentials(c *gin.Context, body map[string]string) (clientID string, clientSecret string) {
	if u, p, ok := c.Request.BasicAuth(); ok {
		uid, _ := url.QueryUnescape(u)
		pw, _ := url.QueryUnescape(p)
		return uid, pw
	}
	return body["client_id"], body["client_secret"]
}

// readOAuthParams merges form, query and JSON parameters.
func readOAuthParams(c *gin.Context) map[string]string {
	out := map[string]string{}
	if c.ContentType() == "application/json" {
		var req map[string]interface{}
		if err := c.ShouldBindJSON(&req); err == nil {
			for k, v := range req {
				if s, ok := v.(string); ok {
					out[k] = s
				}
			}
		}
	} else {
		_ = c.Request.ParseForm()
		for k, v := range c.Request.PostForm {
			if len(v) > 0 {
				out[k] = v[0]
			}
		}
	}
	for k, v := range c.Request.URL.Query() {
		if _, ok := out[k]; !ok && len(v) > 0 {
			out[k] = v[0]
		}
	}
	return out
}

// authenticateClient loads the client and verifies its credentials.
// Confidential clients must present their secret; public clients must not
// have one.
func authenticateClient(c *gin.Context, params map[string]string) (*models.OAuthClient, bool) {
	clientID, clientSecret := extractClientCredentials(c, params)
	if clientID == "" {
		oauthError(c, http.StatusUnauthorized, "invalid_client", "client authentication is required")
		return nil, false
	}
	var client models.OAuthClient
	if err := utils.DB.Where("client_id = ?", clientID).First(&client).Error; err != nil || !client.IsActive {
		oauthError(c, http.StatusUnauthorized, "invalid_client", "unknown or inactive client")
		return nil, false
	}
	if client.IsPublic {
		return &client, true
	}
	if !verifyClientSecret(utils.DB, &client, clientSecret) {
		oauthError(c, http.StatusUnauthorized, "invalid_client", "invalid client credentials")
		return nil, false
	}
	return &client, true
}

func requestBaseURL(c *gin.Context) string {
	if base := strings.TrimRight(strings.TrimSpace(os.Getenv("PUBLIC_API_BASE_URL")), "/"); base != "" {
		return base
	}
	scheme := "http"
	if c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s", scheme, c.Request.Host)
}

// OAuthDiscovery handles RFC 8414 OAuth 2.0 Authorization Server Metadata
func OAuthDiscovery(c *gin.Context) {
	baseURL := requestBaseURL(c)
	scopes := append([]string{}, utils.AllPublicScopes()...)
	scopes = append(scopes, utils.ScopeLegacyRead, utils.ScopeLegacyWrite, utils.ScopeInternalERP)
	c.JSON(http.StatusOK, gin.H{
		"issuer":                                baseURL,
		"authorization_endpoint":                baseURL + "/api/v1/oauth/authorize",
		"token_endpoint":                        baseURL + "/api/v1/oauth/token",
		"userinfo_endpoint":                     baseURL + "/api/v1/oauth/userinfo",
		"revocation_endpoint":                   baseURL + "/api/v1/oauth/revoke",
		"service_documentation":                 baseURL + "/api/public/docs",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 supportedGrantTypes,
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post", "none"},
		"code_challenge_methods_supported":      []string{"S256", "plain"},
		"scopes_supported":                      scopes,
	})
}

type authorizeRequest struct {
	client              *models.OAuthClient
	redirectURI         string
	state               string
	scope               string
	codeChallenge       string
	codeChallengeMethod string
}

// validateAuthorizeRequest checks every authorization parameter. Errors that
// occur before the redirect URI is trusted are shown to the user rather than
// redirected (RFC 6749 §4.1.2.1).
func validateAuthorizeRequest(c *gin.Context, p map[string]string) (*authorizeRequest, bool) {
	clientID := p["client_id"]
	if clientID == "" {
		renderAuthorizeError(c, "client_id is required")
		return nil, false
	}
	var client models.OAuthClient
	if err := utils.DB.Where("client_id = ?", clientID).First(&client).Error; err != nil || !client.IsActive {
		renderAuthorizeError(c, "Unknown or inactive client application.")
		return nil, false
	}
	redirectURI, ok := resolveRedirectURI(&client, p["redirect_uri"])
	if !ok {
		renderAuthorizeError(c, "The redirect_uri does not match any URI registered for this application.")
		return nil, false
	}
	req := &authorizeRequest{
		client:              &client,
		redirectURI:         redirectURI,
		state:               p["state"],
		codeChallenge:       p["code_challenge"],
		codeChallengeMethod: p["code_challenge_method"],
	}
	if rt := p["response_type"]; rt != "" && rt != "code" {
		redirectWithError(c, req, "unsupported_response_type", "only response_type=code is supported")
		return nil, false
	}
	if !clientAllowsGrant(&client, grantAuthorizationCode) {
		redirectWithError(c, req, "unauthorized_client", "this client may not use the authorization code grant")
		return nil, false
	}
	if req.codeChallenge != "" && req.codeChallengeMethod == "" {
		req.codeChallengeMethod = "plain"
	}
	if m := strings.ToUpper(req.codeChallengeMethod); m != "" && m != "S256" && m != "PLAIN" {
		redirectWithError(c, req, "invalid_request", "unsupported code_challenge_method")
		return nil, false
	}
	if client.IsPublic && (req.codeChallenge == "" || strings.ToUpper(req.codeChallengeMethod) != "S256") {
		redirectWithError(c, req, "invalid_request", "public clients must use PKCE with code_challenge_method=S256")
		return nil, false
	}
	scope, err := resolveGrantedScopes(&client, p["scope"])
	if err != nil {
		redirectWithError(c, req, "invalid_scope", err.Error())
		return nil, false
	}
	req.scope = scope
	return req, true
}

func redirectWithError(c *gin.Context, req *authorizeRequest, code, description string) {
	parsed, err := url.Parse(req.redirectURI)
	if err != nil {
		renderAuthorizeError(c, description)
		return
	}
	q := parsed.Query()
	q.Set("error", code)
	q.Set("error_description", description)
	if req.state != "" {
		q.Set("state", req.state)
	}
	parsed.RawQuery = q.Encode()
	c.Redirect(http.StatusFound, parsed.String())
}

func setConsentPageHeaders(c *gin.Context) {
	c.Header("X-Frame-Options", "DENY")
	c.Header("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self' *; frame-ancestors 'none'")
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
}

func renderAuthorizeError(c *gin.Context, message string) {
	setConsentPageHeaders(c)
	if strings.Contains(c.GetHeader("Accept"), "application/json") || c.ContentType() == "application/json" {
		oauthError(c, http.StatusBadRequest, "invalid_request", message)
		return
	}
	c.Status(http.StatusBadRequest)
	c.Header("Content-Type", "text/html; charset=utf-8")
	_ = consentTemplate.Execute(c.Writer, map[string]interface{}{"Error": message})
}

type scopeLine struct{ Scope, Description string }

func describeScopes(scope string) []scopeLine {
	lines := []scopeLine{}
	for _, s := range splitList(scope) {
		desc := s
		switch s {
		case utils.ScopeInternalERP, utils.ScopeAdmin:
			desc = "Full access to your TruERP account, the same as signing in"
		case utils.ScopeLegacyRead:
			desc = "Read all of your business data"
		case utils.ScopeLegacyWrite:
			desc = "Read, create and update your business data"
		default:
			for _, info := range utils.PublicScopeCatalog {
				if info.Scope == s {
					desc = info.Description
				}
			}
		}
		lines = append(lines, scopeLine{Scope: s, Description: desc})
	}
	return lines
}

// OAuthAuthorize renders the consent page (GET) and issues an authorization
// code after the user signs in and approves (POST).
func OAuthAuthorize(c *gin.Context) {
	params := readOAuthParams(c)
	req, ok := validateAuthorizeRequest(c, params)
	if !ok {
		return
	}

	if c.Request.Method == http.MethodGet {
		setConsentPageHeaders(c)
		c.Header("Content-Type", "text/html; charset=utf-8")
		_ = consentTemplate.Execute(c.Writer, map[string]interface{}{
			"ClientID":            req.client.ClientID,
			"ClientName":          req.client.ClientName,
			"RedirectHost":        hostOf(req.redirectURI),
			"RedirectURI":         req.redirectURI,
			"State":               req.state,
			"Scope":               req.scope,
			"Scopes":              describeScopes(req.scope),
			"CodeChallenge":       req.codeChallenge,
			"CodeChallengeMethod": req.codeChallengeMethod,
		})
		return
	}

	if params["action"] == "deny" {
		redirectWithError(c, req, "access_denied", "the user denied the request")
		return
	}

	email, password, totpCode := params["email"], params["password"], params["totp_code"]
	if email == "" || password == "" {
		renderAuthorizeError(c, "Email and password are required.")
		return
	}
	var user models.User
	if err := utils.DB.Where("LOWER(email) = LOWER(?)", strings.TrimSpace(email)).First(&user).Error; err != nil ||
		bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)) != nil {
		renderAuthorizeError(c, "Invalid email or password.")
		return
	}
	if !user.IsActive {
		renderAuthorizeError(c, "This account is deactivated.")
		return
	}
	if user.TwoFactorEnabled && (totpCode == "" || !totp.Validate(totpCode, user.TotpSecret)) {
		renderAuthorizeError(c, "Invalid or missing two-factor authentication code.")
		return
	}
	// A client bound to a store may only be authorized by that store's users.
	if req.client.StoreID != nil && !userBelongsToStore(user, *req.client.StoreID) {
		redirectWithError(c, req, "access_denied", "this application is restricted to another store")
		return
	}

	codeBytes := make([]byte, 32)
	if _, err := rand.Read(codeBytes); err != nil {
		oauthError(c, http.StatusInternalServerError, "server_error", "failed to generate authorization code")
		return
	}
	authCode := hex.EncodeToString(codeBytes)
	storeID := user.StoreID
	if req.client.StoreID != nil {
		storeID = req.client.StoreID
	}
	codeRecord := models.OAuthCode{
		Code:                sha256Hex(authCode),
		ClientID:            req.client.ClientID,
		UserID:              user.ID,
		StoreID:             storeID,
		RedirectURI:         req.redirectURI,
		Scope:               req.scope,
		CodeChallenge:       req.codeChallenge,
		CodeChallengeMethod: req.codeChallengeMethod,
		ExpiresAt:           time.Now().Add(oauthCodeTTL),
	}
	if err := utils.DB.Create(&codeRecord).Error; err != nil {
		oauthError(c, http.StatusInternalServerError, "server_error", "failed to save authorization code")
		return
	}

	parsed, _ := url.Parse(req.redirectURI)
	q := parsed.Query()
	q.Set("code", authCode)
	if req.state != "" {
		q.Set("state", req.state)
	}
	parsed.RawQuery = q.Encode()

	if c.ContentType() == "application/json" || strings.Contains(c.GetHeader("Accept"), "application/json") {
		c.JSON(http.StatusOK, gin.H{"code": authCode, "state": req.state, "redirect_uri": parsed.String()})
		return
	}
	c.Redirect(http.StatusFound, parsed.String())
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Host
	}
	return raw
}

func userBelongsToStore(user models.User, storeID uuid.UUID) bool {
	if utils.IsSuperAdminRole(user.Role) {
		return true
	}
	for _, id := range utils.AssignedStoreIDs(utils.DB, user.ID, user.StoreID) {
		if id == storeID {
			return true
		}
	}
	return false
}

type issuedTokens struct {
	Access  string
	Refresh string
}

func issueOAuthTokens(user models.User, storeID *uuid.UUID, clientID, scope string, withRefresh bool) (*issuedTokens, error) {
	access, err := utils.GenerateOAuthAccessToken(user.ID, user.Name, user.Email, user.Role, storeID, clientID, scope)
	if err != nil {
		return nil, err
	}
	out := &issuedTokens{Access: access}
	if withRefresh {
		refresh, err := generateRandomSecret(32)
		if err != nil {
			return nil, err
		}
		rec := models.OAuthRefreshToken{
			Token:     sha256Hex(refresh),
			ClientID:  clientID,
			UserID:    user.ID,
			StoreID:   storeID,
			Scope:     scope,
			ExpiresAt: time.Now().Add(oauthRefreshTokenTTL),
		}
		if err := utils.DB.Create(&rec).Error; err != nil {
			return nil, err
		}
		out.Refresh = refresh
	}
	return out, nil
}

func writeTokenResponse(c *gin.Context, t *issuedTokens, scope string) {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	resp := gin.H{
		"access_token": t.Access,
		"token_type":   "Bearer",
		"expires_in":   int(utils.OAuthAccessTokenTTL.Seconds()),
		"scope":        scope,
	}
	if t.Refresh != "" {
		resp["refresh_token"] = t.Refresh
	}
	c.JSON(http.StatusOK, resp)
}

// OAuthToken handles POST /api/v1/oauth/token for all supported grant types.
func OAuthToken(c *gin.Context) {
	p := readOAuthParams(c)
	grantType := p["grant_type"]
	if grantType == "" {
		oauthError(c, http.StatusBadRequest, "invalid_request", "grant_type is required")
		return
	}
	client, ok := authenticateClient(c, p)
	if !ok {
		return
	}
	if !clientAllowsGrant(client, grantType) {
		oauthError(c, http.StatusBadRequest, "unauthorized_client", fmt.Sprintf("this client may not use the %s grant", grantType))
		return
	}

	switch grantType {
	case grantAuthorizationCode:
		tokenFromAuthorizationCode(c, client, p)
	case grantRefreshToken:
		tokenFromRefreshToken(c, client, p)
	case grantClientCredentials:
		tokenFromClientCredentials(c, client, p)
	case grantPassword:
		tokenFromPassword(c, client, p)
	default:
		oauthError(c, http.StatusBadRequest, "unsupported_grant_type", fmt.Sprintf("grant type %s is not supported", grantType))
	}
}

func tokenFromAuthorizationCode(c *gin.Context, client *models.OAuthClient, p map[string]string) {
	code := p["code"]
	if code == "" {
		oauthError(c, http.StatusBadRequest, "invalid_request", "code is required")
		return
	}
	codeHash := sha256Hex(code)
	// Claim the code atomically so concurrent exchanges cannot both succeed.
	res := utils.DB.Model(&models.OAuthCode{}).Where("code = ? AND used = ?", codeHash, false).Update("used", true)
	if res.Error != nil || res.RowsAffected != 1 {
		oauthError(c, http.StatusBadRequest, "invalid_grant", "invalid, expired or already used authorization code")
		return
	}
	var rec models.OAuthCode
	if err := utils.DB.Where("code = ?", codeHash).First(&rec).Error; err != nil {
		oauthError(c, http.StatusBadRequest, "invalid_grant", "invalid authorization code")
		return
	}
	if time.Now().After(rec.ExpiresAt) {
		oauthError(c, http.StatusBadRequest, "invalid_grant", "authorization code expired")
		return
	}
	if rec.ClientID != client.ClientID {
		oauthError(c, http.StatusBadRequest, "invalid_grant", "authorization code was issued to another client")
		return
	}
	if p["redirect_uri"] != rec.RedirectURI && !(p["redirect_uri"] == "" && len(clientRedirectURIs(client)) == 1) {
		oauthError(c, http.StatusBadRequest, "invalid_grant", "redirect_uri mismatch")
		return
	}
	if rec.CodeChallenge != "" {
		if p["code_verifier"] == "" || !verifyCodeChallenge(p["code_verifier"], rec.CodeChallenge, rec.CodeChallengeMethod) {
			oauthError(c, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
			return
		}
	} else if client.IsPublic {
		oauthError(c, http.StatusBadRequest, "invalid_grant", "PKCE is required for public clients")
		return
	}

	var user models.User
	if err := utils.DB.First(&user, "id = ?", rec.UserID).Error; err != nil || !user.IsActive {
		oauthError(c, http.StatusBadRequest, "invalid_grant", "the authorizing user is unavailable")
		return
	}
	tokens, err := issueOAuthTokens(user, rec.StoreID, client.ClientID, rec.Scope, clientAllowsGrant(client, grantRefreshToken))
	if err != nil {
		oauthError(c, http.StatusInternalServerError, "server_error", "failed to issue tokens")
		return
	}
	writeTokenResponse(c, tokens, rec.Scope)
}

func tokenFromRefreshToken(c *gin.Context, client *models.OAuthClient, p map[string]string) {
	presented := p["refresh_token"]
	if presented == "" {
		oauthError(c, http.StatusBadRequest, "invalid_request", "refresh_token is required")
		return
	}
	var rf models.OAuthRefreshToken
	if err := utils.DB.Where("token = ?", sha256Hex(presented)).First(&rf).Error; err != nil {
		oauthError(c, http.StatusBadRequest, "invalid_grant", "invalid refresh token")
		return
	}
	if rf.ClientID != client.ClientID {
		oauthError(c, http.StatusBadRequest, "invalid_grant", "refresh token was issued to another client")
		return
	}
	if rf.Revoked {
		// Replay of a rotated token: assume theft and revoke the whole family.
		utils.DB.Model(&models.OAuthRefreshToken{}).
			Where("client_id = ? AND user_id = ? AND revoked = ?", rf.ClientID, rf.UserID, false).
			Update("revoked", true)
		oauthError(c, http.StatusBadRequest, "invalid_grant", "refresh token has been revoked")
		return
	}
	if time.Now().After(rf.ExpiresAt) {
		oauthError(c, http.StatusBadRequest, "invalid_grant", "refresh token expired")
		return
	}
	scope := rf.Scope
	if requested := p["scope"]; requested != "" {
		// A refresh may narrow, never widen, the original grant.
		granted := map[string]bool{}
		for s := range utils.ExpandScopes(splitList(rf.Scope)) {
			granted[s] = true
		}
		for _, s := range splitList(rf.Scope) {
			granted[s] = true
		}
		for _, s := range splitList(requested) {
			if !granted[s] {
				oauthError(c, http.StatusBadRequest, "invalid_scope", fmt.Sprintf("scope %q exceeds the original grant", s))
				return
			}
		}
		scope = utils.JoinScopes(splitList(requested))
	}

	res := utils.DB.Model(&models.OAuthRefreshToken{}).Where("id = ? AND revoked = ?", rf.ID, false).Update("revoked", true)
	if res.Error != nil || res.RowsAffected != 1 {
		oauthError(c, http.StatusBadRequest, "invalid_grant", "refresh token has been revoked")
		return
	}
	var user models.User
	if err := utils.DB.First(&user, "id = ?", rf.UserID).Error; err != nil || !user.IsActive {
		oauthError(c, http.StatusBadRequest, "invalid_grant", "user is inactive or not found")
		return
	}
	tokens, err := issueOAuthTokens(user, rf.StoreID, client.ClientID, scope, true)
	if err != nil {
		oauthError(c, http.StatusInternalServerError, "server_error", "failed to issue tokens")
		return
	}
	writeTokenResponse(c, tokens, scope)
}

func tokenFromClientCredentials(c *gin.Context, client *models.OAuthClient, p map[string]string) {
	if client.IsPublic {
		oauthError(c, http.StatusBadRequest, "unauthorized_client", "public clients cannot use client_credentials")
		return
	}
	if client.StoreID == nil {
		oauthError(c, http.StatusBadRequest, "unauthorized_client", "client_credentials requires a client bound to a store")
		return
	}
	store, err := utils.FindStoreByID(utils.DB, *client.StoreID)
	if err != nil || !store.IsActive {
		oauthError(c, http.StatusBadRequest, "invalid_client", "the client's store is unavailable")
		return
	}
	var owner models.User
	if err := utils.DB.First(&owner, "id = ?", store.OwnerUserID).Error; err != nil || !owner.IsActive {
		oauthError(c, http.StatusBadRequest, "invalid_client", "the client's store owner is unavailable")
		return
	}
	scope, err := resolveGrantedScopes(client, p["scope"])
	if err != nil {
		oauthError(c, http.StatusBadRequest, "invalid_scope", err.Error())
		return
	}
	// Machine tokens never get internal-API access.
	scopes := []string{}
	for _, s := range splitList(scope) {
		if s != utils.ScopeInternalERP && s != utils.ScopeAdmin {
			scopes = append(scopes, s)
		}
	}
	scope = utils.JoinScopes(scopes)
	storeID := store.ID
	tokens, err := issueOAuthTokens(owner, &storeID, client.ClientID, scope, false)
	if err != nil {
		oauthError(c, http.StatusInternalServerError, "server_error", "failed to issue token")
		return
	}
	writeTokenResponse(c, tokens, scope)
}

func tokenFromPassword(c *gin.Context, client *models.OAuthClient, p map[string]string) {
	if client.IsPublic {
		oauthError(c, http.StatusBadRequest, "unauthorized_client", "public clients cannot use the password grant")
		return
	}
	username := p["username"]
	if username == "" {
		username = p["email"]
	}
	password := p["password"]
	if username == "" || password == "" {
		oauthError(c, http.StatusBadRequest, "invalid_request", "username and password are required")
		return
	}
	var user models.User
	if err := utils.DB.Where("LOWER(email) = LOWER(?)", strings.TrimSpace(username)).First(&user).Error; err != nil ||
		bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)) != nil {
		oauthError(c, http.StatusBadRequest, "invalid_grant", "invalid credentials")
		return
	}
	if !user.IsActive {
		oauthError(c, http.StatusBadRequest, "invalid_grant", "account is deactivated")
		return
	}
	if user.TwoFactorEnabled && (p["totp_code"] == "" || !totp.Validate(p["totp_code"], user.TotpSecret)) {
		oauthError(c, http.StatusBadRequest, "invalid_grant", "invalid or missing two-factor code")
		return
	}
	if client.StoreID != nil && !userBelongsToStore(user, *client.StoreID) {
		oauthError(c, http.StatusBadRequest, "invalid_grant", "this client is restricted to another store")
		return
	}
	scope, err := resolveGrantedScopes(client, p["scope"])
	if err != nil {
		oauthError(c, http.StatusBadRequest, "invalid_scope", err.Error())
		return
	}
	storeID := user.StoreID
	if client.StoreID != nil {
		storeID = client.StoreID
	}
	tokens, err := issueOAuthTokens(user, storeID, client.ClientID, scope, clientAllowsGrant(client, grantRefreshToken))
	if err != nil {
		oauthError(c, http.StatusInternalServerError, "server_error", "failed to issue tokens")
		return
	}
	writeTokenResponse(c, tokens, scope)
}

// OAuthUserInfo returns user claims for an authenticated OAuth Bearer token
func OAuthUserInfo(c *gin.Context) {
	userID, ok := c.Get("actor_user_id")
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	userName, _ := c.Get("user_name")
	email, _ := c.Get("email")
	role, _ := c.Get("role")
	storeID, _ := currentStoreID(c)
	scope, _ := c.Get("token_scope")
	c.JSON(http.StatusOK, gin.H{
		"sub":      userID,
		"name":     userName,
		"email":    email,
		"role":     role,
		"store_id": storeID,
		"scope":    scope,
	})
}

// OAuthRevoke revokes a refresh token (RFC 7009). It always answers 200 so
// callers cannot probe for valid tokens.
func OAuthRevoke(c *gin.Context) {
	p := readOAuthParams(c)
	client, ok := authenticateClient(c, p)
	if !ok {
		return
	}
	if token := p["token"]; token != "" {
		utils.DB.Model(&models.OAuthRefreshToken{}).
			Where("token = ? AND client_id = ?", sha256Hex(token), client.ClientID).
			Update("revoked", true)
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

var consentTemplate = template.Must(template.New("authorize").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>TruERP — Authorize{{if .ClientName}} {{.ClientName}}{{end}}</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Helvetica,Arial,sans-serif;background:#f1f5f9;color:#0f172a;min-height:100vh;display:flex;align-items:center;justify-content:center;padding:1.5rem}
.card{background:#fff;border:1px solid #e2e8f0;border-radius:14px;width:100%;max-width:440px;padding:2rem;box-shadow:0 10px 30px -12px rgba(15,23,42,.18)}
.brand{display:flex;align-items:center;gap:.6rem;margin-bottom:1.25rem;font-weight:700}
.logo{width:34px;height:34px;border-radius:8px;background:#4f46e5;color:#fff;display:flex;align-items:center;justify-content:center}
h1{font-size:1.1rem;margin-bottom:.35rem}
p{font-size:.875rem;color:#475569;line-height:1.5}
.scopes{margin:1rem 0 1.25rem;border:1px solid #e2e8f0;border-radius:10px;padding:.75rem 1rem;background:#f8fafc}
.scopes h2{font-size:.7rem;text-transform:uppercase;letter-spacing:.04em;color:#64748b;margin-bottom:.4rem}
.scopes li{list-style:none;font-size:.85rem;padding:.2rem 0;color:#334155}
.scopes code{font-size:.75rem;color:#4f46e5}
label{display:block;font-size:.8rem;font-weight:600;color:#334155;margin:.75rem 0 .3rem}
input{width:100%;border:1px solid #cbd5e1;border-radius:8px;padding:.6rem .75rem;font-size:.9rem}
input:focus{outline:2px solid #c7d2fe;border-color:#4f46e5}
.actions{display:flex;gap:.6rem;margin-top:1.25rem}
button{flex:1;padding:.7rem;border-radius:8px;font-weight:600;font-size:.875rem;cursor:pointer;border:1px solid transparent}
.primary{background:#4f46e5;color:#fff}
.secondary{background:#fff;border-color:#cbd5e1;color:#334155}
.error{background:#fef2f2;border:1px solid #fecaca;color:#991b1b;border-radius:8px;padding:.75rem;font-size:.875rem}
.note{font-size:.75rem;color:#64748b;margin-top:1rem}
</style>
</head>
<body>
<div class="card">
<div class="brand"><div class="logo">T</div>TruERP</div>
{{if .Error}}
<h1>Authorization failed</h1>
<div class="error">{{.Error}}</div>
{{else}}
<h1>Allow {{.ClientName}} to access your account?</h1>
<p>After you approve, you will be returned to <strong>{{.RedirectHost}}</strong>.</p>
<div class="scopes"><h2>This application will be able to</h2><ul>
{{range .Scopes}}<li>{{.Description}} <code>{{.Scope}}</code></li>{{end}}
</ul></div>
<form method="POST" action="/api/v1/oauth/authorize" autocomplete="on">
<input type="hidden" name="client_id" value="{{.ClientID}}">
<input type="hidden" name="redirect_uri" value="{{.RedirectURI}}">
<input type="hidden" name="response_type" value="code">
<input type="hidden" name="state" value="{{.State}}">
<input type="hidden" name="scope" value="{{.Scope}}">
<input type="hidden" name="code_challenge" value="{{.CodeChallenge}}">
<input type="hidden" name="code_challenge_method" value="{{.CodeChallengeMethod}}">
<label for="email">Email</label>
<input type="email" id="email" name="email" autocomplete="username">
<label for="password">Password</label>
<input type="password" id="password" name="password" autocomplete="current-password">
<label for="totp_code">Two-factor code (if enabled)</label>
<input type="text" id="totp_code" name="totp_code" inputmode="numeric" autocomplete="one-time-code">
<div class="actions">
<button type="submit" name="action" value="deny" class="secondary" formnovalidate>Deny</button>
<button type="submit" name="action" value="allow" class="primary">Allow access</button>
</div>
</form>
<p class="note">Only approve applications you trust. You can revoke access at any time.</p>
{{end}}
</div>
</body>
</html>`))
