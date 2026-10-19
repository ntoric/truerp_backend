package controllers

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupOAuthTestDB(t *testing.T) (*gorm.DB, *models.User) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite test db: %v", err)
	}

	err = db.AutoMigrate(
		&models.User{},
		&models.Store{},
		&models.OAuthClient{},
		&models.OAuthCode{},
		&models.OAuthRefreshToken{},
	)
	if err != nil {
		t.Fatalf("failed to automigrate oauth test models: %v", err)
	}

	utils.DB = db

	// Create test user
	hashed, _ := bcrypt.GenerateFromPassword([]byte("testpassword123"), bcrypt.DefaultCost)
	testUser := models.User{
		ID:       uuid.New(),
		Name:     "Test Admin",
		Email:    "oauth_admin@example.com",
		Password: string(hashed),
		Role:     "super_admin",
		IsActive: true,
	}
	if err := db.Create(&testUser).Error; err != nil {
		t.Fatalf("failed to create test user: %v", err)
	}

	SeedDefaultOAuthClients(db)
	return db, &testUser
}

func TestOAuthDiscovery(t *testing.T) {
	r := gin.New()
	r.GET("/.well-known/oauth-authorization-server", OAuthDiscovery)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var meta map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &meta); err != nil {
		t.Fatalf("invalid json: %v", err)
	}

	if meta["token_endpoint"] == nil || meta["authorization_endpoint"] == nil {
		t.Fatalf("missing required oauth metadata endpoints: %v", meta)
	}
}

func TestOAuthGrants(t *testing.T) {
	_, user := setupOAuthTestDB(t)

	r := gin.New()
	r.POST("/api/v1/oauth/authorize", OAuthAuthorize)
	r.POST("/api/v1/oauth/token", OAuthToken)

	// 1. Test Password Grant
	form := url.Values{}
	form.Set("grant_type", "password")
	form.Set("client_id", "truerp-mcp-client")
	form.Set("client_secret", "truerp-mcp-secret")
	form.Set("username", user.Email)
	form.Set("password", "testpassword123")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("password grant failed: status %d, body: %s", w.Code, w.Body.String())
	}

	var tokenResp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &tokenResp)
	accessToken, ok := tokenResp["access_token"].(string)
	if !ok || accessToken == "" {
		t.Fatalf("expected access_token in response: %v", tokenResp)
	}
	refreshToken, ok := tokenResp["refresh_token"].(string)
	if !ok || refreshToken == "" {
		t.Fatalf("expected refresh_token in response: %v", tokenResp)
	}

	// 2. Test Refresh Token Grant
	rfForm := url.Values{}
	rfForm.Set("grant_type", "refresh_token")
	rfForm.Set("client_id", "truerp-mcp-client")
	rfForm.Set("refresh_token", refreshToken)

	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest(http.MethodPost, "/api/v1/oauth/token", strings.NewReader(rfForm.Encode()))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("refresh_token grant failed: status %d, body: %s", w2.Code, w2.Body.String())
	}

	var rfResp map[string]interface{}
	json.Unmarshal(w2.Body.Bytes(), &rfResp)
	if rfResp["access_token"] == nil || rfResp["refresh_token"] == nil {
		t.Fatalf("expected new tokens: %v", rfResp)
	}

	// 3. Test Client Credentials Grant
	ccForm := url.Values{}
	ccForm.Set("grant_type", "client_credentials")
	ccForm.Set("client_id", "truerp-mcp-client")
	ccForm.Set("client_secret", "truerp-mcp-secret")

	w3 := httptest.NewRecorder()
	req3, _ := http.NewRequest(http.MethodPost, "/api/v1/oauth/token", strings.NewReader(ccForm.Encode()))
	req3.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.ServeHTTP(w3, req3)

	if w3.Code != http.StatusOK {
		t.Fatalf("client_credentials grant failed: status %d, body: %s", w3.Code, w3.Body.String())
	}

	var ccResp map[string]interface{}
	json.Unmarshal(w3.Body.Bytes(), &ccResp)
	if ccResp["access_token"] == nil {
		t.Fatalf("expected access_token: %v", ccResp)
	}

	// 4. Test Authorization Code Grant with PKCE (S256)
	codeVerifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	h := sha256.Sum256([]byte(codeVerifier))
	codeChallenge := base64.RawURLEncoding.EncodeToString(h[:])

	authForm := url.Values{}
	authForm.Set("client_id", "truerp-mcp-client")
	authForm.Set("redirect_uri", "http://localhost:3000/callback")
	authForm.Set("email", user.Email)
	authForm.Set("password", "testpassword123")
	authForm.Set("code_challenge", codeChallenge)
	authForm.Set("code_challenge_method", "S256")
	authForm.Set("state", "xyzState123")

	wAuth := httptest.NewRecorder()
	reqAuth, _ := http.NewRequest(http.MethodPost, "/api/v1/oauth/authorize", strings.NewReader(authForm.Encode()))
	reqAuth.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqAuth.Header.Set("Accept", "application/json")
	r.ServeHTTP(wAuth, reqAuth)

	if wAuth.Code != http.StatusOK {
		t.Fatalf("oauth authorize failed: status %d, body: %s", wAuth.Code, wAuth.Body.String())
	}

	var authResp map[string]interface{}
	json.Unmarshal(wAuth.Body.Bytes(), &authResp)
	code, ok := authResp["code"].(string)
	if !ok || code == "" {
		t.Fatalf("expected authorization code: %v", authResp)
	}

	// Exchange Code with PKCE verifier
	tokenCodeForm := url.Values{}
	tokenCodeForm.Set("grant_type", "authorization_code")
	tokenCodeForm.Set("client_id", "truerp-mcp-client")
	tokenCodeForm.Set("client_secret", "truerp-mcp-secret")
	tokenCodeForm.Set("code", code)
	tokenCodeForm.Set("code_verifier", codeVerifier)
	tokenCodeForm.Set("redirect_uri", "http://localhost:3000/callback")

	wToken := httptest.NewRecorder()
	reqToken, _ := http.NewRequest(http.MethodPost, "/api/v1/oauth/token", strings.NewReader(tokenCodeForm.Encode()))
	reqToken.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.ServeHTTP(wToken, reqToken)

	if wToken.Code != http.StatusOK {
		t.Fatalf("code exchange failed: status %d, body: %s", wToken.Code, wToken.Body.String())
	}

	var tokenExResp map[string]interface{}
	json.Unmarshal(wToken.Body.Bytes(), &tokenExResp)
	if tokenExResp["access_token"] == nil {
		t.Fatalf("expected access_token after code exchange: %v", tokenExResp)
	}
}
