package controllers

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
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

const (
	oauthCodeTTL         = 10 * time.Minute
	oauthRefreshTokenTTL = 30 * 24 * time.Hour
)

// generateRandomSecret generates a URL-safe random string of n bytes
func generateRandomSecret(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
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

// SeedDefaultOAuthClients creates standard clients (e.g. for MCP server) if not already present
func SeedDefaultOAuthClients(db *gorm.DB) {
	if !db.Migrator().HasTable(&models.OAuthClient{}) {
		return
	}
	var count int64
	db.Model(&models.OAuthClient{}).Where("client_id = ?", "truerp-mcp-client").Count(&count)
	if count == 0 {
		client := models.OAuthClient{
			ClientID:     "truerp-mcp-client",
			ClientSecret: "truerp-mcp-secret",
			ClientName:   "TruERP MCP Server Client",
			RedirectURIs: "http://localhost:3000/callback,http://localhost:8089/callback,http://127.0.0.1:8089/callback,https://oauth.pstmn.io/v1/callback,http://localhost:8088/api/v1/oauth/callback",
			GrantTypes:   "authorization_code,client_credentials,refresh_token,password",
			Scopes:       "read write admin erp",
			IsPublic:     false,
		}
		if err := db.Create(&client).Error; err != nil {
			fmt.Printf("SeedDefaultOAuthClients error: %v\n", err)
		}
	}
}

// extractClientCredentials extracts client_id and client_secret from Basic Auth or form/JSON body
func extractClientCredentials(c *gin.Context) (clientID string, clientSecret string) {
	// 1. Check HTTP Basic Authentication Header
	if u, p, ok := c.Request.BasicAuth(); ok {
		return u, p
	}
	// 2. Check form/query/json
	clientID = c.PostForm("client_id")
	clientSecret = c.PostForm("client_secret")
	if clientID == "" {
		clientID = c.Query("client_id")
	}
	if clientSecret == "" {
		clientSecret = c.Query("client_secret")
	}
	return clientID, clientSecret
}

// OAuthDiscovery handles RFC 8414 OAuth 2.0 Authorization Server Metadata
func OAuthDiscovery(c *gin.Context) {
	scheme := "http"
	if c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	host := c.Request.Host
	baseURL := fmt.Sprintf("%s://%s", scheme, host)

	c.JSON(http.StatusOK, gin.H{
		"issuer":                                baseURL,
		"authorization_endpoint":                baseURL + "/api/v1/oauth/authorize",
		"token_endpoint":                        baseURL + "/api/v1/oauth/token",
		"userinfo_endpoint":                     baseURL + "/api/v1/oauth/userinfo",
		"revocation_endpoint":                   baseURL + "/api/v1/oauth/revoke",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "client_credentials", "refresh_token", "password"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post", "none"},
		"code_challenge_methods_supported":      []string{"S256", "plain"},
		"scopes_supported":                      []string{"read", "write", "admin", "erp"},
	})
}

// OAuthAuthorize handles GET and POST requests for OAuth 2.0 Authorization Code flow
func OAuthAuthorize(c *gin.Context) {
	clientID := c.Query("client_id")
	if clientID == "" {
		clientID = c.PostForm("client_id")
	}
	redirectURI := c.Query("redirect_uri")
	if redirectURI == "" {
		redirectURI = c.PostForm("redirect_uri")
	}
	responseType := c.Query("response_type")
	if responseType == "" {
		responseType = c.PostForm("response_type")
	}
	state := c.Query("state")
	if state == "" {
		state = c.PostForm("state")
	}
	scope := c.Query("scope")
	if scope == "" {
		scope = c.PostForm("scope")
	}
	codeChallenge := c.Query("code_challenge")
	if codeChallenge == "" {
		codeChallenge = c.PostForm("code_challenge")
	}
	codeChallengeMethod := c.Query("code_challenge_method")
	if codeChallengeMethod == "" {
		codeChallengeMethod = c.PostForm("code_challenge_method")
	}

	if clientID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "error_description": "client_id is required"})
		return
	}

	var client models.OAuthClient
	if err := utils.DB.Where("client_id = ?", clientID).First(&client).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unauthorized_client", "error_description": "Unknown client_id"})
		return
	}

	if redirectURI == "" {
		uris := strings.Split(client.RedirectURIs, ",")
		if len(uris) > 0 {
			redirectURI = strings.TrimSpace(uris[0])
		}
	}

	if responseType != "" && responseType != "code" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported_response_type", "error_description": "Only response_type=code is supported"})
		return
	}

	// GET: Render the interactive OAuth login and authorization consent form
	if c.Request.Method == http.MethodGet {
		c.Header("Content-Type", "text/html; charset=utf-8")
		tmpl := `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>TruERP — Authorize {{.ClientName}}</title>
  <style>
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body {
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
      background: linear-gradient(135deg, #0f172a 0%, #1e1b4b 100%);
      color: #f8fafc;
      min-height: 100vh;
      display: flex;
      align-items: center;
      justify-content: center;
      padding: 1.5rem;
    }
    .card {
      background: rgba(30, 41, 59, 0.85);
      backdrop-filter: blur(16px);
      border: 1px solid rgba(255, 255, 255, 0.1);
      border-radius: 1rem;
      width: 100%;
      max-width: 440px;
      padding: 2.5rem;
      box-shadow: 0 25px 50px -12px rgba(0, 0, 0, 0.5);
    }
    .brand {
      display: flex;
      align-items: center;
      gap: 0.75rem;
      margin-bottom: 1.5rem;
    }
    .brand-logo {
      width: 40px;
      height: 40px;
      border-radius: 8px;
      background: linear-gradient(135deg, #6366f1 0%, #a855f7 100%);
      display: flex;
      align-items: center;
      justify-content: center;
      font-weight: 700;
      font-size: 1.25rem;
      color: #fff;
    }
    .brand-title {
      font-size: 1.25rem;
      font-weight: 700;
      letter-spacing: -0.02em;
    }
    h2 {
      font-size: 1.15rem;
      font-weight: 600;
      margin-bottom: 0.5rem;
    }
    p.desc {
      font-size: 0.875rem;
      color: #94a3b8;
      margin-bottom: 1.5rem;
      line-height: 1.5;
    }
    .scopes-box {
      background: rgba(15, 23, 42, 0.6);
      border: 1px solid rgba(255, 255, 255, 0.05);
      border-radius: 0.5rem;
      padding: 0.75rem 1rem;
      margin-bottom: 1.5rem;
      font-size: 0.8125rem;
    }
    .scope-item {
      display: flex;
      align-items: center;
      gap: 0.5rem;
      color: #cbd5e1;
      margin: 0.35rem 0;
    }
    .scope-item svg {
      width: 16px;
      height: 16px;
      color: #10b981;
      flex-shrink: 0;
    }
    .form-group {
      margin-bottom: 1rem;
    }
    label {
      display: block;
      font-size: 0.8125rem;
      font-weight: 500;
      color: #cbd5e1;
      margin-bottom: 0.35rem;
    }
    input[type="email"], input[type="password"], input[type="text"] {
      width: 100%;
      background: #0f172a;
      border: 1px solid #334155;
      border-radius: 0.5rem;
      padding: 0.65rem 0.875rem;
      color: #fff;
      font-size: 0.9375rem;
      outline: none;
      transition: border-color 0.2s;
    }
    input:focus {
      border-color: #6366f1;
    }
    .actions {
      display: flex;
      gap: 0.75rem;
      margin-top: 1.5rem;
    }
    button {
      flex: 1;
      padding: 0.75rem 1rem;
      border-radius: 0.5rem;
      font-weight: 600;
      font-size: 0.875rem;
      cursor: pointer;
      border: none;
      transition: all 0.2s;
    }
    button.btn-primary {
      background: linear-gradient(135deg, #6366f1 0%, #4f46e5 100%);
      color: #ffffff;
    }
    button.btn-primary:hover {
      opacity: 0.95;
      transform: translateY(-1px);
    }
    button.btn-secondary {
      background: rgba(51, 65, 85, 0.5);
      color: #cbd5e1;
    }
    button.btn-secondary:hover {
      background: rgba(51, 65, 85, 0.8);
    }
  </style>
</head>
<body>
  <div class="card">
    <div class="brand">
      <div class="brand-logo">T</div>
      <div class="brand-title">TruERP Authorization</div>
    </div>
    <h2>Sign in & Grant Access</h2>
    <p class="desc"><strong style="color: #f1f5f9;">{{.ClientName}}</strong> is requesting authorization to connect with your TruERP account.</p>

    <div class="scopes-box">
      <div style="font-size: 0.75rem; text-transform: uppercase; color: #64748b; font-weight: 600; margin-bottom: 0.25rem;">Requested Permissions</div>
      <div class="scope-item">
        <svg fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" d="M5 13l4 4L19 7"></path></svg>
        <span>Full ERP & Billing API Access (Invoices, Parties, Stock, Ledgers)</span>
      </div>
      <div class="scope-item">
        <svg fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" d="M5 13l4 4L19 7"></path></svg>
        <span>AI MCP Server Integration</span>
      </div>
    </div>

    <form method="POST" action="/api/v1/oauth/authorize">
      <input type="hidden" name="client_id" value="{{.ClientID}}">
      <input type="hidden" name="redirect_uri" value="{{.RedirectURI}}">
      <input type="hidden" name="response_type" value="code">
      <input type="hidden" name="state" value="{{.State}}">
      <input type="hidden" name="scope" value="{{.Scope}}">
      <input type="hidden" name="code_challenge" value="{{.CodeChallenge}}">
      <input type="hidden" name="code_challenge_method" value="{{.CodeChallengeMethod}}">

      <div class="form-group">
        <label for="email">Account Email</label>
        <input type="email" id="email" name="email" required placeholder="admin@example.com" autocomplete="username">
      </div>
      <div class="form-group">
        <label for="password">Password</label>
        <input type="password" id="password" name="password" required placeholder="••••••••" autocomplete="current-password">
      </div>
      <div class="form-group">
        <label for="totp_code">2FA Code (Optional)</label>
        <input type="text" id="totp_code" name="totp_code" placeholder="6-digit code if enabled" autocomplete="one-time-code">
      </div>

      <div class="actions">
        <button type="button" class="btn-secondary" onclick="window.history.back()">Cancel</button>
        <button type="submit" class="btn-primary">Authorize Access</button>
      </div>
    </form>
  </div>
</body>
</html>`
		t, err := template.New("authorize").Parse(tmpl)
		if err != nil {
			c.String(http.StatusInternalServerError, "Template parse error")
			return
		}
		data := map[string]string{
			"ClientID":            client.ClientID,
			"ClientName":          client.ClientName,
			"RedirectURI":         redirectURI,
			"State":               state,
			"Scope":               scope,
			"CodeChallenge":       codeChallenge,
			"CodeChallengeMethod": codeChallengeMethod,
		}
		t.Execute(c.Writer, data)
		return
	}

	// POST: Authenticate user credentials and issue authorization code
	var email, password, totpCode string
	if c.ContentType() == "application/json" {
		var req struct {
			Email    string `json:"email"`
			Password string `json:"password"`
			TotpCode string `json:"totp_code"`
		}
		if err := c.ShouldBindJSON(&req); err == nil {
			email = req.Email
			password = req.Password
			totpCode = req.TotpCode
		}
	} else {
		email = c.PostForm("email")
		password = c.PostForm("password")
		totpCode = c.PostForm("totp_code")
	}

	if email == "" || password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "error_description": "email and password are required"})
		return
	}

	var user models.User
	if err := utils.DB.Where("LOWER(email) = LOWER(?)", strings.TrimSpace(email)).First(&user).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "access_denied", "error_description": "Invalid email or password"})
		return
	}

	if !user.IsActive {
		c.JSON(http.StatusForbidden, gin.H{"error": "access_denied", "error_description": "User account is inactive"})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "access_denied", "error_description": "Invalid email or password"})
		return
	}

	if user.TwoFactorEnabled {
		if totpCode == "" || !totp.Validate(totpCode, user.TotpSecret) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "access_denied", "error_description": "Invalid or missing two-factor authentication code"})
			return
		}
	}

	// Generate authorization code
	codeBytes := make([]byte, 32)
	if _, err := rand.Read(codeBytes); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error", "error_description": "Failed to generate authorization code"})
		return
	}
	authCode := hex.EncodeToString(codeBytes)

	codeRecord := models.OAuthCode{
		Code:                authCode,
		ClientID:            client.ClientID,
		UserID:              user.ID,
		StoreID:             user.StoreID,
		RedirectURI:         redirectURI,
		Scope:               scope,
		CodeChallenge:       codeChallenge,
		CodeChallengeMethod: codeChallengeMethod,
		ExpiresAt:           time.Now().Add(oauthCodeTTL),
		Used:                false,
	}

	if err := utils.DB.Create(&codeRecord).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error", "error_description": "Database error saving code"})
		return
	}

	// Build callback URL
	if redirectURI != "" {
		parsed, err := url.Parse(redirectURI)
		if err == nil {
			q := parsed.Query()
			q.Set("code", authCode)
			if state != "" {
				q.Set("state", state)
			}
			parsed.RawQuery = q.Encode()

			// If submitted via browser form, perform 302 redirect
			if !strings.Contains(c.GetHeader("Accept"), "application/json") && c.ContentType() != "application/json" {
				c.Redirect(http.StatusFound, parsed.String())
				return
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"code":         authCode,
		"state":        state,
		"redirect_uri": redirectURI,
	})
}

// OAuthToken handles POST /api/v1/oauth/token for all standard grant types
func OAuthToken(c *gin.Context) {
	grantType := c.PostForm("grant_type")
	code := c.PostForm("code")
	redirectURI := c.PostForm("redirect_uri")
	codeVerifier := c.PostForm("code_verifier")
	refreshToken := c.PostForm("refresh_token")
	username := c.PostForm("username")
	if username == "" {
		username = c.PostForm("email")
	}
	password := c.PostForm("password")
	totpCode := c.PostForm("totp_code")
	scope := c.PostForm("scope")

	// Fallback to JSON payload if form is empty
	if grantType == "" && c.ContentType() == "application/json" {
		var req map[string]string
		if err := c.ShouldBindJSON(&req); err == nil {
			grantType = req["grant_type"]
			code = req["code"]
			redirectURI = req["redirect_uri"]
			codeVerifier = req["code_verifier"]
			refreshToken = req["refresh_token"]
			username = req["username"]
			if username == "" {
				username = req["email"]
			}
			password = req["password"]
			totpCode = req["totp_code"]
			scope = req["scope"]
		}
	}

	clientID, clientSecret := extractClientCredentials(c)

	if grantType == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "error_description": "grant_type is required"})
		return
	}

	// -------------------------------------------------------------
	// 1. Grant Type: authorization_code
	// -------------------------------------------------------------
	if grantType == "authorization_code" {
		if code == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "error_description": "code is required"})
			return
		}

		var codeRecord models.OAuthCode
		if err := utils.DB.Where("code = ?", code).First(&codeRecord).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_grant", "error_description": "Invalid or expired authorization code"})
			return
		}

		if codeRecord.Used {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_grant", "error_description": "Authorization code has already been used"})
			return
		}

		if time.Now().After(codeRecord.ExpiresAt) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_grant", "error_description": "Authorization code expired"})
			return
		}

		// Verify client_id matches
		if clientID != "" && codeRecord.ClientID != clientID {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_grant", "error_description": "Client ID mismatch"})
			return
		}

		// Verify redirect_uri matches if provided
		if redirectURI != "" && codeRecord.RedirectURI != "" && codeRecord.RedirectURI != redirectURI {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_grant", "error_description": "redirect_uri mismatch"})
			return
		}

		// Verify PKCE code_verifier if challenge was set
		if codeRecord.CodeChallenge != "" {
			if codeVerifier == "" || !verifyCodeChallenge(codeVerifier, codeRecord.CodeChallenge, codeRecord.CodeChallengeMethod) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_grant", "error_description": "PKCE verification failed"})
				return
			}
		}

		// Mark code as used
		codeRecord.Used = true
		utils.DB.Save(&codeRecord)

		var user models.User
		if err := utils.DB.First(&user, "id = ?", codeRecord.UserID).Error; err != nil || !user.IsActive {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid_grant", "error_description": "Associated user account is unavailable"})
			return
		}

		accessToken, err := utils.GenerateToken(user.ID, user.Name, user.Email, user.Role, user.StoreID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error", "error_description": "Failed to create access token"})
			return
		}

		newRefreshToken, _ := generateRandomSecret(32)
		rfRecord := models.OAuthRefreshToken{
			Token:     newRefreshToken,
			ClientID:  codeRecord.ClientID,
			UserID:    user.ID,
			StoreID:   user.StoreID,
			Scope:     codeRecord.Scope,
			ExpiresAt: time.Now().Add(oauthRefreshTokenTTL),
			Revoked:   false,
		}
		utils.DB.Create(&rfRecord)

		c.JSON(http.StatusOK, gin.H{
			"access_token":  accessToken,
			"token_type":    "Bearer",
			"expires_in":    86400,
			"refresh_token": newRefreshToken,
			"scope":         codeRecord.Scope,
		})
		return
	}

	// -------------------------------------------------------------
	// 2. Grant Type: refresh_token
	// -------------------------------------------------------------
	if grantType == "refresh_token" {
		if refreshToken == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "error_description": "refresh_token is required"})
			return
		}

		var rf models.OAuthRefreshToken
		if err := utils.DB.Where("token = ? AND revoked = ?", refreshToken, false).First(&rf).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_grant", "error_description": "Invalid or revoked refresh token"})
			return
		}

		if time.Now().After(rf.ExpiresAt) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_grant", "error_description": "Refresh token expired"})
			return
		}

		var user models.User
		if err := utils.DB.First(&user, "id = ?", rf.UserID).Error; err != nil || !user.IsActive {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid_grant", "error_description": "User is inactive or not found"})
			return
		}

		// Rotate refresh token
		rf.Revoked = true
		utils.DB.Save(&rf)

		newRefreshToken, _ := generateRandomSecret(32)
		nextRf := models.OAuthRefreshToken{
			Token:     newRefreshToken,
			ClientID:  rf.ClientID,
			UserID:    user.ID,
			StoreID:   user.StoreID,
			Scope:     rf.Scope,
			ExpiresAt: time.Now().Add(oauthRefreshTokenTTL),
			Revoked:   false,
		}
		utils.DB.Create(&nextRf)

		accessToken, err := utils.GenerateToken(user.ID, user.Name, user.Email, user.Role, user.StoreID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error", "error_description": "Failed to create access token"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"access_token":  accessToken,
			"token_type":    "Bearer",
			"expires_in":    86400,
			"refresh_token": newRefreshToken,
			"scope":         rf.Scope,
		})
		return
	}

	// -------------------------------------------------------------
	// 3. Grant Type: client_credentials
	// -------------------------------------------------------------
	if grantType == "client_credentials" {
		if clientID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_client", "error_description": "client_id is required"})
			return
		}

		var client models.OAuthClient
		if err := utils.DB.Where("client_id = ?", clientID).First(&client).Error; err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid_client", "error_description": "Unknown client_id"})
			return
		}

		if client.ClientSecret != "" && client.ClientSecret != clientSecret {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid_client", "error_description": "Invalid client_secret"})
			return
		}

		// Find associated user for machine-to-machine requests
		var user models.User
		if client.UserID != nil {
			utils.DB.First(&user, "id = ?", *client.UserID)
		}
		if user.ID == uuid.Nil {
			// Find primary owner or super_admin
			utils.DB.Where("role IN ('super_admin', 'owner') AND is_active = ?", true).Order("created_at ASC").First(&user)
		}

		if user.ID == uuid.Nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error", "error_description": "No active administrative user available for client credentials"})
			return
		}

		accessToken, err := utils.GenerateToken(user.ID, user.Name, user.Email, user.Role, user.StoreID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error", "error_description": "Failed to create access token"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"access_token": accessToken,
			"token_type":   "Bearer",
			"expires_in":   86400,
			"scope":        client.Scopes,
		})
		return
	}

	// -------------------------------------------------------------
	// 4. Grant Type: password
	// -------------------------------------------------------------
	if grantType == "password" {
		if username == "" || password == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "error_description": "username and password are required"})
			return
		}

		var user models.User
		if err := utils.DB.Where("LOWER(email) = LOWER(?)", strings.TrimSpace(username)).First(&user).Error; err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid_grant", "error_description": "Invalid credentials"})
			return
		}

		if !user.IsActive {
			c.JSON(http.StatusForbidden, gin.H{"error": "invalid_grant", "error_description": "Account is deactivated"})
			return
		}

		if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)); err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid_grant", "error_description": "Invalid credentials"})
			return
		}

		if user.TwoFactorEnabled {
			if totpCode == "" || !totp.Validate(totpCode, user.TotpSecret) {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid_grant", "error_description": "Invalid or missing two-factor code"})
				return
			}
		}

		accessToken, err := utils.GenerateToken(user.ID, user.Name, user.Email, user.Role, user.StoreID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error", "error_description": "Failed to create access token"})
			return
		}

		newRefreshToken, _ := generateRandomSecret(32)
		rfRecord := models.OAuthRefreshToken{
			Token:     newRefreshToken,
			ClientID:  clientID,
			UserID:    user.ID,
			StoreID:   user.StoreID,
			Scope:     scope,
			ExpiresAt: time.Now().Add(oauthRefreshTokenTTL),
			Revoked:   false,
		}
		utils.DB.Create(&rfRecord)

		c.JSON(http.StatusOK, gin.H{
			"access_token":  accessToken,
			"token_type":    "Bearer",
			"expires_in":    86400,
			"refresh_token": newRefreshToken,
			"scope":         scope,
		})
		return
	}

	c.JSON(http.StatusBadRequest, gin.H{
		"error":             "unsupported_grant_type",
		"error_description": fmt.Sprintf("Grant type %s is not supported", grantType),
	})
}

// OAuthUserInfo returns user claims for authenticated OAuth Bearer token
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

	c.JSON(http.StatusOK, gin.H{
		"sub":      userID,
		"name":     userName,
		"email":    email,
		"role":     role,
		"store_id": storeID,
	})
}

// OAuthRevoke revokes an issued refresh token
func OAuthRevoke(c *gin.Context) {
	token := c.PostForm("token")
	if token == "" {
		var req struct {
			Token string `json:"token"`
		}
		c.ShouldBindJSON(&req)
		token = req.Token
	}

	if token != "" {
		utils.DB.Model(&models.OAuthRefreshToken{}).Where("token = ?", token).Update("revoked", true)
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// ListOAuthClients lists all registered OAuth clients (super admin)
func ListOAuthClients(c *gin.Context) {
	var clients []models.OAuthClient
	if err := utils.DB.Order("created_at DESC").Find(&clients).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, clients)
}

// CreateOAuthClient registers a new OAuth client application (super admin)
func CreateOAuthClient(c *gin.Context) {
	var input struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		ClientName   string `json:"client_name" binding:"required"`
		RedirectURIs string `json:"redirect_uris" binding:"required"`
		GrantTypes   string `json:"grant_types"`
		Scopes       string `json:"scopes"`
		IsPublic     bool   `json:"is_public"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	clientID := input.ClientID
	if clientID == "" {
		idHex, _ := generateRandomSecret(16)
		clientID = "client_" + idHex
	}

	clientSecret := input.ClientSecret
	if clientSecret == "" && !input.IsPublic {
		clientSecret, _ = generateRandomSecret(32)
	}

	grantTypes := input.GrantTypes
	if grantTypes == "" {
		grantTypes = "authorization_code,client_credentials,refresh_token,password"
	}

	scopes := input.Scopes
	if scopes == "" {
		scopes = "read write admin erp"
	}

	client := models.OAuthClient{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		ClientName:   input.ClientName,
		RedirectURIs: input.RedirectURIs,
		GrantTypes:   grantTypes,
		Scopes:       scopes,
		IsPublic:     input.IsPublic,
	}

	if err := utils.DB.Create(&client).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to create client or client_id already exists"})
		return
	}

	c.JSON(http.StatusCreated, client)
}

// DeleteOAuthClient removes an OAuth client (super admin)
func DeleteOAuthClient(c *gin.Context) {
	id := c.Param("id")
	if err := utils.DB.Where("id = ? OR client_id = ?", id, id).Delete(&models.OAuthClient{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "OAuth client deleted"})
}
