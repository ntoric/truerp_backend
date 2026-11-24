package controllers

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
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
	"gorm.io/gorm/logger"
)

const testRedirect = "https://app.example.com/callback"

type oauthFixture struct {
	db     *gorm.DB
	r      *gin.Engine
	user   models.User
	store  models.Store
	client models.OAuthClient
	secret string
}

func setupOAuthTest(t *testing.T) *oauthFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", uuid.NewString())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.User{}, &models.Store{}, &models.UserStore{}, &models.OAuthClient{}, &models.OAuthCode{}, &models.OAuthRefreshToken{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	prev := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = prev })

	hashed, _ := bcrypt.GenerateFromPassword([]byte("testpassword123"), bcrypt.MinCost)
	user := models.User{ID: uuid.New(), Name: "Owner", Email: "owner@example.com", Password: string(hashed), Role: "admin", IsActive: true}
	db.Create(&user)
	store := models.Store{ID: uuid.New(), Name: "Store", Code: "S1", OwnerUserID: user.ID, IsActive: true}
	db.Create(&store)
	db.Model(&user).Update("store_id", store.ID)
	user.StoreID = &store.ID

	secret := "trp_secret_test"
	client := models.OAuthClient{
		ClientID: "app1", ClientSecret: hashClientSecret(secret), ClientName: "App One",
		RedirectURIs: testRedirect, GrantTypes: "authorization_code,refresh_token,client_credentials",
		Scopes: "parties:read invoices:read", StoreID: &store.ID, IsActive: true,
	}
	db.Create(&client)

	r := gin.New()
	r.GET("/.well-known/oauth-authorization-server", OAuthDiscovery)
	r.GET("/api/v1/oauth/authorize", OAuthAuthorize)
	r.POST("/api/v1/oauth/authorize", OAuthAuthorize)
	r.POST("/api/v1/oauth/token", OAuthToken)
	r.POST("/api/v1/oauth/revoke", OAuthRevoke)
	return &oauthFixture{db: db, r: r, user: user, store: store, client: client, secret: secret}
}

func (f *oauthFixture) post(path string, form url.Values, basicUser, basicPass string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if basicUser != "" {
		req.SetBasicAuth(basicUser, basicPass)
	}
	w := httptest.NewRecorder()
	f.r.ServeHTTP(w, req)
	return w
}

func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("invalid json (%d): %s", w.Code, w.Body.String())
	}
	return m
}

func pkcePair() (verifier, challenge string) {
	verifier = "verifier-" + uuid.NewString() + uuid.NewString()
	h := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(h[:])
}

// authorize signs in on the consent form and returns the issued code.
func (f *oauthFixture) authorize(t *testing.T, challenge, scope string) string {
	t.Helper()
	form := url.Values{
		"client_id": {"app1"}, "redirect_uri": {testRedirect}, "response_type": {"code"}, "state": {"xyz"},
		"scope": {scope}, "code_challenge": {challenge}, "code_challenge_method": {"S256"},
		"email": {f.user.Email}, "password": {"testpassword123"}, "action": {"allow"},
	}
	w := f.post("/api/v1/oauth/authorize", form, "", "")
	if w.Code != http.StatusFound {
		t.Fatalf("authorize: %d %s", w.Code, w.Body.String())
	}
	loc, _ := url.Parse(w.Header().Get("Location"))
	if !strings.HasPrefix(loc.String(), testRedirect) || loc.Query().Get("state") != "xyz" {
		t.Fatalf("bad redirect: %s", loc)
	}
	return loc.Query().Get("code")
}

func TestOAuthDiscovery(t *testing.T) {
	f := setupOAuthTest(t)
	w := httptest.NewRecorder()
	f.r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server", nil))
	meta := decode(t, w)
	if meta["token_endpoint"] == nil || meta["authorization_endpoint"] == nil {
		t.Fatalf("missing endpoints: %v", meta)
	}
}

func TestOAuthAuthorizationCodeWithPKCE(t *testing.T) {
	f := setupOAuthTest(t)
	verifier, challenge := pkcePair()
	code := f.authorize(t, challenge, "parties:read")

	// Wrong verifier fails, and burns the code.
	bad := f.post("/api/v1/oauth/token", url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {testRedirect}, "code_verifier": {"wrong"}}, "app1", f.secret)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("wrong verifier accepted: %d", bad.Code)
	}

	code = f.authorize(t, challenge, "parties:read")
	ok := f.post("/api/v1/oauth/token", url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {testRedirect}, "code_verifier": {verifier}}, "app1", f.secret)
	if ok.Code != http.StatusOK {
		t.Fatalf("code exchange: %d %s", ok.Code, ok.Body.String())
	}
	tok := decode(t, ok)
	if tok["scope"] != "parties:read" || tok["refresh_token"] == nil || tok["expires_in"].(float64) != 3600 {
		t.Fatalf("token response: %v", tok)
	}
	claims, err := utils.ParseToken(tok["access_token"].(string))
	if err != nil || claims.Scope != "parties:read" || claims.ClientID != "app1" {
		t.Fatalf("access token claims: %+v %v", claims, err)
	}

	// Codes are single use.
	again := f.post("/api/v1/oauth/token", url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {testRedirect}, "code_verifier": {verifier}}, "app1", f.secret)
	if again.Code != http.StatusBadRequest {
		t.Fatalf("code reuse: %d", again.Code)
	}

	// Refresh rotates; replaying the old token revokes the family.
	rt := tok["refresh_token"].(string)
	r1 := f.post("/api/v1/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rt}}, "app1", f.secret)
	if r1.Code != http.StatusOK {
		t.Fatalf("refresh: %d %s", r1.Code, r1.Body.String())
	}
	newRT := decode(t, r1)["refresh_token"].(string)
	if replay := f.post("/api/v1/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rt}}, "app1", f.secret); replay.Code != http.StatusBadRequest {
		t.Fatalf("replayed refresh token accepted: %d", replay.Code)
	}
	if r2 := f.post("/api/v1/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {newRT}}, "app1", f.secret); r2.Code != http.StatusBadRequest {
		t.Fatalf("family should be revoked after replay: %d", r2.Code)
	}

	// Refresh tokens are stored hashed.
	var stored models.OAuthRefreshToken
	if f.db.Where("token = ?", newRT).First(&stored).Error == nil {
		t.Fatalf("refresh token stored in plaintext")
	}
}

func TestOAuthAuthorizeRejectsUnregisteredRedirect(t *testing.T) {
	f := setupOAuthTest(t)
	_, challenge := pkcePair()
	w := httptest.NewRecorder()
	q := url.Values{"client_id": {"app1"}, "redirect_uri": {"https://evil.example/steal"}, "response_type": {"code"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}}
	f.r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/oauth/authorize?"+q.Encode(), nil))
	if w.Code != http.StatusBadRequest || w.Header().Get("Location") != "" {
		t.Fatalf("unregistered redirect must not redirect: %d %s", w.Code, w.Header().Get("Location"))
	}
	// A scope beyond the client's registration is refused (via redirect, as the URI is trusted).
	w2 := httptest.NewRecorder()
	q.Set("redirect_uri", testRedirect)
	q.Set("scope", "invoices:write")
	f.r.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/api/v1/oauth/authorize?"+q.Encode(), nil))
	if loc := w2.Header().Get("Location"); !strings.Contains(loc, "error=invalid_scope") {
		t.Fatalf("expected invalid_scope redirect, got %d %s", w2.Code, loc)
	}
	// The consent page cannot be framed.
	w3 := httptest.NewRecorder()
	q.Set("scope", "parties:read")
	f.r.ServeHTTP(w3, httptest.NewRequest(http.MethodGet, "/api/v1/oauth/authorize?"+q.Encode(), nil))
	if w3.Code != http.StatusOK || w3.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("consent page: %d %v", w3.Code, w3.Header())
	}
}

func TestOAuthClientCredentials(t *testing.T) {
	f := setupOAuthTest(t)
	if w := f.post("/api/v1/oauth/token", url.Values{"grant_type": {"client_credentials"}}, "app1", "wrong"); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong secret: %d", w.Code)
	}
	w := f.post("/api/v1/oauth/token", url.Values{"grant_type": {"client_credentials"}}, "app1", f.secret)
	if w.Code != http.StatusOK {
		t.Fatalf("client credentials: %d %s", w.Code, w.Body.String())
	}
	claims, _ := utils.ParseToken(decode(t, w)["access_token"].(string))
	if claims.UserID != f.user.ID || claims.StoreID == nil || *claims.StoreID != f.store.ID {
		t.Fatalf("token must act as the bound store's owner: %+v", claims)
	}
	if decode(t, w)["refresh_token"] != nil {
		t.Fatalf("client credentials must not issue a refresh token")
	}

	// Grants not registered for the client are refused.
	if w := f.post("/api/v1/oauth/token", url.Values{"grant_type": {"password"}, "username": {f.user.Email}, "password": {"testpassword123"}}, "app1", f.secret); w.Code != http.StatusBadRequest {
		t.Fatalf("unregistered password grant: %d", w.Code)
	}

	// An unbound client can never mint tokens via client_credentials.
	unbound := models.OAuthClient{ClientID: "app2", ClientSecret: hashClientSecret("s2"), ClientName: "App Two", RedirectURIs: testRedirect, GrantTypes: "client_credentials", Scopes: "read", IsActive: true}
	f.db.Create(&unbound)
	if w := f.post("/api/v1/oauth/token", url.Values{"grant_type": {"client_credentials"}}, "app2", "s2"); w.Code != http.StatusBadRequest {
		t.Fatalf("unbound client_credentials: %d %s", w.Code, w.Body.String())
	}
}

func TestLegacyDefaultClientIsDeactivated(t *testing.T) {
	f := setupOAuthTest(t)
	legacy := models.OAuthClient{ClientID: defaultMCPClientID, ClientSecret: legacyMCPSecret, ClientName: "MCP", RedirectURIs: testRedirect, GrantTypes: "client_credentials", Scopes: "read write admin erp", IsActive: true}
	f.db.Create(&legacy)
	t.Setenv("TRUERP_MCP_CLIENT_SECRET", "")
	SeedDefaultOAuthClients(f.db)
	f.db.First(&legacy, "id = ?", legacy.ID)
	if legacy.IsActive {
		t.Fatalf("client with the published default secret must be deactivated")
	}
	if w := f.post("/api/v1/oauth/token", url.Values{"grant_type": {"client_credentials"}}, defaultMCPClientID, legacyMCPSecret); w.Code != http.StatusUnauthorized {
		t.Fatalf("legacy client still usable: %d", w.Code)
	}

	t.Setenv("TRUERP_MCP_CLIENT_SECRET", "a-strong-secret")
	SeedDefaultOAuthClients(f.db)
	f.db.First(&legacy, "id = ?", legacy.ID)
	if !legacy.IsActive || legacy.ClientSecret != hashClientSecret("a-strong-secret") {
		t.Fatalf("configured secret should re-enable the client: %+v", legacy)
	}
}

func TestLegacyPlaintextSecretIsUpgraded(t *testing.T) {
	f := setupOAuthTest(t)
	f.db.Model(&models.OAuthClient{}).Where("client_id = ?", "app1").Update("client_secret", "plain-secret")
	if w := f.post("/api/v1/oauth/token", url.Values{"grant_type": {"client_credentials"}}, "app1", "plain-secret"); w.Code != http.StatusOK {
		t.Fatalf("legacy plaintext secret: %d %s", w.Code, w.Body.String())
	}
	var cl models.OAuthClient
	f.db.First(&cl, "client_id = ?", "app1")
	if cl.ClientSecret != hashClientSecret("plain-secret") {
		t.Fatalf("secret not upgraded to hash: %s", cl.ClientSecret)
	}
}
