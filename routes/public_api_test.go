package routes

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"truerp/apidocs"
	"truerp/controllers"
	"truerp/middleware"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type apiEnv struct {
	t          *testing.T
	r          *gin.Engine
	db         *gorm.DB
	store      models.Store
	owner      models.User
	adminToken string
}

func newAPIEnv(t *testing.T) *apiEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", uuid.NewString())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := utils.MigrateSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	prev := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = prev })

	owner := models.User{ID: uuid.New(), Name: "Store Owner", Email: "owner-" + uuid.NewString()[:8] + "@example.com", Password: "x", Role: "admin", IsActive: true, IsStoreOwner: true}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatalf("owner: %v", err)
	}
	store := models.Store{ID: uuid.New(), Name: "Test Store", Code: "T" + uuid.NewString()[:6], OwnerUserID: owner.ID, IsActive: true, StateCode: "29"}
	if err := db.Create(&store).Error; err != nil {
		t.Fatalf("store: %v", err)
	}
	db.Model(&owner).Update("store_id", store.ID)
	owner.StoreID = &store.ID
	db.Create(&models.Warehouse{ID: uuid.New(), UserID: owner.ID, Name: "Main", Code: "MAIN", IsActive: true, IsDefault: true})

	admin := models.User{ID: uuid.New(), Name: "Super Admin", Email: "sa-" + uuid.NewString()[:8] + "@example.com", Password: "x", Role: "super_admin", IsActive: true}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatalf("admin: %v", err)
	}
	token, err := utils.GenerateToken(admin.ID, admin.Name, admin.Email, admin.Role, &store.ID)
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	r := gin.New()
	r.GET("/api/v1/oauth/userinfo", middleware.AuthRequired(), controllers.OAuthUserInfo)
	r.GET("/api/v1/business", middleware.AuthRequired(), func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
	setupPublicAPIRoutes(r)
	return &apiEnv{t: t, r: r, db: db, store: store, owner: owner, adminToken: token}
}

type resp struct {
	Code   int
	Header http.Header
	Body   []byte
}

func (r resp) json(t *testing.T) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal(r.Body, &m); err != nil {
		t.Fatalf("invalid json (%d): %s", r.Code, r.Body)
	}
	return m
}

func (r resp) errCode(t *testing.T) string {
	e, _ := r.json(t)["error"].(map[string]interface{})
	code, _ := e["code"].(string)
	return code
}

func (e *apiEnv) do(method, path, token string, body interface{}, headers ...string) resp {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		switch b := body.(type) {
		case string:
			rd = strings.NewReader(b)
		default:
			raw, _ := json.Marshal(b)
			rd = bytes.NewReader(raw)
		}
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	e.r.ServeHTTP(w, req)
	return resp{Code: w.Code, Header: w.Header(), Body: w.Body.Bytes()}
}

func (e *apiEnv) createKey(scopes ...string) string {
	e.t.Helper()
	if len(scopes) == 0 {
		scopes = utils.AllPublicScopes()
	}
	res := e.do("POST", "/api/v1/admin/integrations/api-keys", e.adminToken, gin.H{
		"store_id": e.store.ID, "name": "Test key", "scopes": scopes,
	})
	if res.Code != http.StatusCreated {
		e.t.Fatalf("create key: %d %s", res.Code, res.Body)
	}
	secret, _ := res.json(e.t)["secret"].(string)
	if !strings.HasPrefix(secret, utils.APIKeyPrefix) {
		e.t.Fatalf("unexpected key format: %q", secret)
	}
	return secret
}

func TestPublicAPIAuthentication(t *testing.T) {
	e := newAPIEnv(t)
	key := e.createKey()

	if res := e.do("GET", "/api/public/v1/me", "", nil); res.Code != 401 || res.errCode(t) != "missing_credentials" {
		t.Fatalf("missing creds: %d %s", res.Code, res.Body)
	}
	if res := e.do("GET", "/api/public/v1/me", "trp_live_nope", nil); res.Code != 401 || res.errCode(t) != "invalid_api_key" {
		t.Fatalf("bad key: %d %s", res.Code, res.Body)
	}

	res := e.do("GET", "/api/public/v1/me", key, nil)
	if res.Code != 200 {
		t.Fatalf("me: %d %s", res.Code, res.Body)
	}
	me := res.json(t)
	if me["store"].(map[string]interface{})["id"] != e.store.ID.String() {
		t.Fatalf("wrong store: %v", me)
	}
	if res.Header.Get("X-Request-ID") == "" || res.Header.Get("X-RateLimit-Limit") != "120" {
		t.Fatalf("missing standard headers: %v", res.Header)
	}

	// X-API-Key header works too.
	if res := e.do("GET", "/api/public/v1/me", "", nil, "X-API-Key", key); res.Code != 200 {
		t.Fatalf("X-API-Key: %d", res.Code)
	}

	// Revoked keys stop working.
	var k models.APIKey
	e.db.Where("secret_hash = ?", utils.HashAPIKey(key)).First(&k)
	if r := e.do("POST", "/api/v1/admin/integrations/api-keys/"+k.ID.String()+"/revoke", e.adminToken, nil); r.Code != 200 {
		t.Fatalf("revoke: %d %s", r.Code, r.Body)
	}
	if res := e.do("GET", "/api/public/v1/me", key, nil); res.Code != 401 || res.errCode(t) != "api_key_revoked" {
		t.Fatalf("revoked: %d %s", res.Code, res.Body)
	}
}

func TestPublicAPIScopesAndPolicy(t *testing.T) {
	e := newAPIEnv(t)
	readOnly := e.createKey(utils.ScopePartiesRead)

	if res := e.do("GET", "/api/public/v1/parties", readOnly, nil); res.Code != 200 {
		t.Fatalf("list with read scope: %d %s", res.Code, res.Body)
	}
	res := e.do("POST", "/api/public/v1/parties", readOnly, gin.H{"type": "customer", "name": "X"})
	if res.Code != 403 || res.errCode(t) != "insufficient_scope" {
		t.Fatalf("write without scope: %d %s", res.Code, res.Body)
	}
	if res := e.do("GET", "/api/public/v1/invoices", readOnly, nil); res.Code != 403 {
		t.Fatalf("other resource: %d", res.Code)
	}

	full := e.createKey()
	policyPath := "/api/v1/admin/integrations/policies/" + e.store.ID.String()
	// The store policy caps every credential, even a full-scope key.
	if r := e.do("PUT", policyPath, e.adminToken, gin.H{"allowed_scopes": []string{utils.ScopePartiesRead}}); r.Code != 200 {
		t.Fatalf("policy: %d %s", r.Code, r.Body)
	}
	if res := e.do("POST", "/api/public/v1/parties", full, gin.H{"type": "customer", "name": "X"}); res.Code != 403 {
		t.Fatalf("policy should block write: %d %s", res.Code, res.Body)
	}
	if r := e.do("PUT", policyPath, e.adminToken, gin.H{"api_enabled": false}); r.Code != 200 {
		t.Fatalf("policy: %d", r.Code)
	}
	if res := e.do("GET", "/api/public/v1/me", full, nil); res.Code != 403 || res.errCode(t) != "api_disabled" {
		t.Fatalf("disabled: %d %s", res.Code, res.Body)
	}
	if r := e.do("PUT", policyPath, e.adminToken, gin.H{"api_enabled": true, "allowed_scopes": utils.AllPublicScopes(), "allowed_ips": "203.0.113.7"}); r.Code != 200 {
		t.Fatalf("policy: %d", r.Code)
	}
	if res := e.do("GET", "/api/public/v1/me", full, nil); res.Code != 403 || res.errCode(t) != "ip_not_allowed" {
		t.Fatalf("ip allow list: %d %s", res.Code, res.Body)
	}
	if r := e.do("PUT", policyPath, e.adminToken, gin.H{"allowed_ips": "", "rate_limit_per_minute": 2}); r.Code != 200 {
		t.Fatalf("policy: %d", r.Code)
	}
	e.do("GET", "/api/public/v1/me", full, nil)
	e.do("GET", "/api/public/v1/me", full, nil)
	res = e.do("GET", "/api/public/v1/me", full, nil)
	if res.Code != 429 || res.Header.Get("Retry-After") == "" {
		t.Fatalf("rate limit: %d %v", res.Code, res.Header)
	}

	// Non-admins cannot reach the management API.
	if r := e.do("GET", "/api/v1/admin/integrations/api-keys", full, nil); r.Code != 401 {
		t.Fatalf("api key on admin api: %d", r.Code)
	}
}

func TestPublicAPIPartiesProductsAndIdempotency(t *testing.T) {
	e := newAPIEnv(t)
	key := e.createKey()

	res := e.do("POST", "/api/public/v1/parties", key, `{"type":"customer","name":"Acme","nickname":"x"}`)
	if res.Code != 400 || res.errCode(t) != "invalid_body" {
		t.Fatalf("unknown field should fail: %d %s", res.Code, res.Body)
	}
	if res := e.do("POST", "/api/public/v1/parties", key, gin.H{"type": "friend", "name": "Acme"}); res.Code != 400 {
		t.Fatalf("bad type: %d", res.Code)
	}

	body := gin.H{"type": "customer", "name": "Acme Traders", "phone": "9876543210", "gstin": "29abcde1234f1z5",
		"billing_address": gin.H{"city": "Bengaluru", "state_code": "29"}, "credit_limit": 5000}
	first := e.do("POST", "/api/public/v1/parties", key, body, "Idempotency-Key", "party-1")
	if first.Code != 201 {
		t.Fatalf("create party: %d %s", first.Code, first.Body)
	}
	party := first.json(t)
	if party["object"] != "party" || party["gstin"] != "29ABCDE1234F1Z5" || party["currency"] != "INR" {
		t.Fatalf("representation: %v", party)
	}
	replay := e.do("POST", "/api/public/v1/parties", key, body, "Idempotency-Key", "party-1")
	if replay.Code != 201 || replay.Header.Get("Idempotent-Replayed") != "true" || replay.json(t)["id"] != party["id"] {
		t.Fatalf("replay: %d %v %s", replay.Code, replay.Header, replay.Body)
	}
	var count int64
	e.db.Model(&models.Party{}).Where("user_id = ?", e.owner.ID).Count(&count)
	if count != 1 {
		t.Fatalf("idempotent retry created a duplicate: %d parties", count)
	}
	if r := e.do("POST", "/api/public/v1/parties", key, gin.H{"type": "customer", "name": "Other"}, "Idempotency-Key", "party-1"); r.Code != 422 {
		t.Fatalf("key reuse with new body: %d", r.Code)
	}

	id := party["id"].(string)
	upd := e.do("PATCH", "/api/public/v1/parties/"+id, key, gin.H{"credit_limit": 9000})
	if upd.Code != 200 || upd.json(t)["credit_limit"].(float64) != 9000 || upd.json(t)["phone"] != "9876543210" {
		t.Fatalf("patch should keep other fields: %d %s", upd.Code, upd.Body)
	}
	if r := e.do("GET", "/api/public/v1/parties/"+uuid.NewString(), key, nil); r.Code != 404 || r.errCode(t) != "resource_missing" {
		t.Fatalf("404: %d %s", r.Code, r.Body)
	}

	// Pagination.
	for i := 0; i < 3; i++ {
		e.do("POST", "/api/public/v1/parties", key, gin.H{"type": "vendor", "name": fmt.Sprintf("Vendor %d", i)})
		time.Sleep(2 * time.Millisecond)
	}
	seen := map[string]bool{}
	cursor := ""
	pages := 0
	for {
		path := "/api/public/v1/parties?limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		page := e.do("GET", path, key, nil).json(t)
		for _, it := range page["data"].([]interface{}) {
			seen[it.(map[string]interface{})["id"].(string)] = true
		}
		pages++
		if page["has_more"] != true {
			break
		}
		cursor = page["next_cursor"].(string)
	}
	if len(seen) != 4 || pages != 2 {
		t.Fatalf("pagination saw %d parties in %d pages", len(seen), pages)
	}
	if r := e.do("GET", "/api/public/v1/parties?type=vendor", key, nil); len(r.json(t)["data"].([]interface{})) != 3 {
		t.Fatalf("type filter")
	}

	// Products + opening stock.
	prod := e.do("POST", "/api/public/v1/products", key, gin.H{
		"name": "Basmati Rice 5kg", "item_code": "8901234567890", "unit": "BAG", "sale_price": 650,
		"purchase_price": 540, "opening_stock": gin.H{"quantity": 40, "cost_price": 540},
	})
	if prod.Code != 201 {
		t.Fatalf("create product: %d %s", prod.Code, prod.Body)
	}
	pid := prod.json(t)["id"].(string)
	stock := e.do("GET", "/api/public/v1/products/"+pid+"/stock", key, nil)
	if stock.Code != 200 || stock.json(t)["quantity"].(float64) != 40 {
		t.Fatalf("stock: %d %s", stock.Code, stock.Body)
	}
	if r := e.do("PATCH", "/api/public/v1/products/"+pid, key, gin.H{"sale_price": 675}); r.Code != 200 || r.json(t)["sale_price"].(float64) != 675 || r.json(t)["item_code"] != "8901234567890" {
		t.Fatalf("patch product: %d %s", r.Code, r.Body)
	}
	dup := e.do("POST", "/api/public/v1/products", key, gin.H{"name": "Dup", "item_code": "8901234567890"})
	if dup.Code != 409 {
		t.Fatalf("duplicate item code: %d %s", dup.Code, dup.Body)
	}
}

func TestPublicAPITransactions(t *testing.T) {
	e := newAPIEnv(t)
	key := e.createKey()
	cust := e.do("POST", "/api/public/v1/parties", key, gin.H{"type": "customer", "name": "Buyer"}).json(t)
	vendor := e.do("POST", "/api/public/v1/parties", key, gin.H{"type": "vendor", "name": "Supplier"}).json(t)
	prod := e.do("POST", "/api/public/v1/products", key, gin.H{"name": "Widget", "sale_price": 100, "opening_stock": gin.H{"quantity": 10, "cost_price": 60}}).json(t)

	inv := e.do("POST", "/api/public/v1/invoices", key, gin.H{
		"party_id": cust["id"], "date": "2026-11-20",
		"line_items": []gin.H{
			{"product_id": prod["id"], "quantity": 2},
			{"description": "Delivery", "quantity": 1, "unit_price": 50},
		},
		"amount_paid": 100, "payment_mode": "cash",
	}, "Idempotency-Key", "order-1")
	if inv.Code != 201 {
		t.Fatalf("create invoice: %d %s", inv.Code, inv.Body)
	}
	invoice := inv.json(t)
	amounts := invoice["amounts"].(map[string]interface{})
	if invoice["date"] != "2026-11-20" || amounts["subtotal"].(float64) != 250 || len(invoice["line_items"].([]interface{})) != 2 {
		t.Fatalf("invoice representation: %s", inv.Body)
	}
	if r := e.do("POST", "/api/public/v1/invoices", key, gin.H{"party_id": cust["id"], "line_items": []gin.H{{"product_id": prod["id"], "quantity": 2}}, "amount_paid": 100, "payment_mode": "cash", "date": "2026-11-20"}, "Idempotency-Key", "order-1"); r.Code != 422 {
		t.Fatalf("idempotency mismatch on invoice: %d", r.Code)
	}
	stock := e.do("GET", "/api/public/v1/products/"+prod["id"].(string)+"/stock", key, nil).json(t)
	if stock["quantity"].(float64) != 8 {
		t.Fatalf("invoice should deduct stock: %v", stock["quantity"])
	}
	list := e.do("GET", "/api/public/v1/invoices?date_from=2026-11-20&date_to=2026-11-20", key, nil).json(t)
	if len(list["data"].([]interface{})) != 1 {
		t.Fatalf("date filter: %v", list)
	}
	if r := e.do("GET", "/api/public/v1/invoices?date_from=2026-11-21", key, nil).json(t); len(r["data"].([]interface{})) != 0 {
		t.Fatalf("date_from should exclude")
	}

	if r := e.do("POST", "/api/public/v1/invoices", key, gin.H{"party_id": cust["id"], "line_items": []gin.H{{"product_id": prod["id"], "quantity": 0}}}); r.Code != 400 || !strings.Contains(string(r.Body), "line_items[0].quantity") {
		t.Fatalf("line validation: %d %s", r.Code, r.Body)
	}

	pay := e.do("POST", "/api/public/v1/payments", key, gin.H{"party_id": cust["id"], "invoice_id": invoice["id"], "amount": 150, "mode": "upi", "reference": "UTR123"})
	if pay.Code != 201 || pay.json(t)["net_amount"].(float64) != 150 {
		t.Fatalf("payment: %d %s", pay.Code, pay.Body)
	}
	if r := e.do("POST", "/api/public/v1/payments", key, gin.H{"party_id": vendor["id"], "invoice_id": invoice["id"], "amount": 1}); r.Code != 400 {
		t.Fatalf("invoice/party mismatch should fail: %d", r.Code)
	}

	cancel := e.do("POST", "/api/public/v1/invoices/"+invoice["id"].(string)+"/cancel", key, gin.H{"reason": "Customer cancelled"})
	if cancel.Code != 200 || cancel.json(t)["status"] != "cancelled" {
		t.Fatalf("cancel: %d %s", cancel.Code, cancel.Body)
	}

	bill := e.do("POST", "/api/public/v1/purchase-bills", key, gin.H{
		"party_id": vendor["id"], "number": "SUP-001", "date": "2026-11-22",
		"line_items": []gin.H{{"product_id": prod["id"], "quantity": 5, "unit_price": 60, "tax_rate": 5}},
	})
	if bill.Code != 201 {
		t.Fatalf("purchase bill: %d %s", bill.Code, bill.Body)
	}
	if total := bill.json(t)["amounts"].(map[string]interface{})["total"].(float64); total != 315 {
		t.Fatalf("bill total = %v, want 315", total)
	}
}

func TestPublicAPIEventsAndWebhooks(t *testing.T) {
	t.Setenv("WEBHOOKS_ALLOW_PRIVATE_NETWORKS", "true")
	e := newAPIEnv(t)
	key := e.createKey()

	var mu sync.Mutex
	var received []map[string]interface{}
	var sigOK = true
	var secret string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		parts := map[string]string{}
		for _, p := range strings.Split(r.Header.Get("TruERP-Signature"), ",") {
			kv := strings.SplitN(p, "=", 2)
			if len(kv) == 2 {
				parts[kv[0]] = kv[1]
			}
		}
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(parts["t"] + "."))
		mac.Write(raw)
		if !hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(parts["v1"])) {
			sigOK = false
		}
		var m map[string]interface{}
		_ = json.Unmarshal(raw, &m)
		received = append(received, m)
		w.WriteHeader(204)
	}))
	defer srv.Close()

	wh := e.do("POST", "/api/public/v1/webhook-endpoints", key, gin.H{"url": srv.URL + "/hook", "events": []string{"party.*", "invoice.created"}})
	if wh.Code != 201 {
		t.Fatalf("create webhook: %d %s", wh.Code, wh.Body)
	}
	secret = wh.json(t)["secret"].(string)
	whID := wh.json(t)["id"].(string)
	if r := e.do("POST", "/api/public/v1/webhook-endpoints", key, gin.H{"url": srv.URL, "events": []string{"nope.created"}}); r.Code != 400 {
		t.Fatalf("unknown event type should fail: %d", r.Code)
	}

	test := e.do("POST", "/api/public/v1/webhook-endpoints/"+whID+"/test", key, nil)
	if test.Code != 200 || test.json(t)["succeeded"] != true {
		t.Fatalf("test webhook: %d %s", test.Code, test.Body)
	}

	// Rewind the scan cursor so the scanner covers the changes below.
	e.db.Model(&models.IntegrationScanCursor{}).Where("store_id = ?", e.store.ID).Update("scanned_to", time.Now().Add(-time.Hour))
	party := e.do("POST", "/api/public/v1/parties", key, gin.H{"type": "customer", "name": "Evented"}).json(t)
	// Rows must be older than the scanner's settle delay.
	past := time.Now().Add(-30 * time.Second)
	e.db.Model(&models.Party{}).Where("id = ?", party["id"]).Updates(map[string]interface{}{"created_at": past, "updated_at": past})

	controllers.RunIntegrationScanNow()

	mu.Lock()
	n := len(received)
	mu.Unlock()
	if n != 2 { // ping + party.created
		t.Fatalf("expected 2 deliveries, got %d: %v", n, received)
	}
	if !sigOK {
		t.Fatalf("webhook signature did not verify")
	}
	if received[1]["type"] != "party.created" {
		t.Fatalf("event type: %v", received[1])
	}
	obj := received[1]["data"].(map[string]interface{})["object"].(map[string]interface{})
	if obj["id"] != party["id"] || obj["object"] != "party" {
		t.Fatalf("event payload: %v", obj)
	}

	// Event feed.
	feed := e.do("GET", "/api/public/v1/events", key, nil)
	if feed.Code != 200 {
		t.Fatalf("events: %d %s", feed.Code, feed.Body)
	}
	data := feed.json(t)["data"].([]interface{})
	if len(data) != 1 || feed.json(t)["next_cursor"] == nil {
		t.Fatalf("event feed: %s", feed.Body)
	}
	cursor := feed.json(t)["next_cursor"].(string)
	if again := e.do("GET", "/api/public/v1/events?cursor="+cursor, key, nil).json(t); len(again["data"].([]interface{})) != 0 || again["next_cursor"] != cursor {
		t.Fatalf("resuming from cursor: %v", again)
	}

	// A key without parties:read cannot see party events.
	limited := e.createKey(utils.ScopeEventsRead, utils.ScopeInvoicesRead)
	if r := e.do("GET", "/api/public/v1/events", limited, nil).json(t); len(r["data"].([]interface{})) != 0 {
		t.Fatalf("event feed leaked events beyond scope: %v", r)
	}

	deliveries := e.do("GET", "/api/public/v1/webhook-endpoints/"+whID+"/deliveries", key, nil).json(t)
	if len(deliveries["data"].([]interface{})) != 1 {
		t.Fatalf("deliveries: %v", deliveries)
	}
}

// Regression: GORM's Create drops false for default:true columns, so the very
// first policy write disabling the API must still persist "disabled".
func TestFirstPolicyWriteCanDisable(t *testing.T) {
	e := newAPIEnv(t)
	key := e.createKey()
	if r := e.do("PUT", "/api/v1/admin/integrations/policies/"+e.store.ID.String(), e.adminToken, gin.H{"api_enabled": false, "webhooks_enabled": false}); r.Code != 200 || r.json(t)["api_enabled"] != false || r.json(t)["webhooks_enabled"] != false {
		t.Fatalf("policy response: %d %s", r.Code, r.Body)
	}
	if res := e.do("GET", "/api/public/v1/me", key, nil); res.Code != 403 {
		t.Fatalf("API should be disabled: %d", res.Code)
	}
	r := e.do("POST", "/api/v1/admin/integrations/oauth-clients", e.adminToken, gin.H{"client_name": "Off", "redirect_uris": "https://x.example/cb", "is_active": false})
	if r.Code != 201 || r.json(t)["client"].(map[string]interface{})["is_active"] != false {
		t.Fatalf("inactive OAuth app: %d %s", r.Code, r.Body)
	}
}

func TestWebhookURLsMustBePublic(t *testing.T) {
	e := newAPIEnv(t)
	key := e.createKey()
	for _, u := range []string{"http://example.com/h", "https://127.0.0.1/h", "https://10.0.0.5/h", "https://localhost/h", "https://169.254.169.254/latest"} {
		if r := e.do("POST", "/api/public/v1/webhook-endpoints", key, gin.H{"url": u}); r.Code != 400 {
			t.Fatalf("%s should be rejected, got %d", u, r.Code)
		}
	}
}

func TestOAuthScopedTokensCannotUseInternalAPI(t *testing.T) {
	e := newAPIEnv(t)
	scoped, _ := utils.GenerateOAuthAccessToken(e.owner.ID, e.owner.Name, e.owner.Email, e.owner.Role, e.owner.StoreID, "client_x", "parties:read")
	if r := e.do("GET", "/api/v1/business", scoped, nil); r.Code != 403 {
		t.Fatalf("public-scoped token on internal API: %d %s", r.Code, r.Body)
	}
	if r := e.do("GET", "/api/v1/oauth/userinfo", scoped, nil); r.Code != 200 {
		t.Fatalf("userinfo: %d %s", r.Code, r.Body)
	}
	erp, _ := utils.GenerateOAuthAccessToken(e.owner.ID, e.owner.Name, e.owner.Email, e.owner.Role, e.owner.StoreID, "mcp", "read write erp")
	if r := e.do("GET", "/api/v1/business", erp, nil); r.Code != 200 {
		t.Fatalf("erp-scoped token on internal API: %d", r.Code)
	}
	// The same scoped token works on the public API, limited to its scope.
	if r := e.do("GET", "/api/public/v1/parties", scoped, nil); r.Code != 200 {
		t.Fatalf("scoped token on public API: %d %s", r.Code, r.Body)
	}
	if r := e.do("GET", "/api/public/v1/invoices", scoped, nil); r.Code != 403 {
		t.Fatalf("scoped token beyond scope: %d", r.Code)
	}
	portal, _ := utils.GeneratePortalToken(e.owner.ID, uuid.New(), "Customer", "999")
	if r := e.do("GET", "/api/v1/business", portal, nil); r.Code != 401 {
		t.Fatalf("portal token on internal API: %d", r.Code)
	}
}

func TestOpenAPISpecIsValidAndCoversRoutes(t *testing.T) {
	js, err := apidocs.SpecJSON()
	if err != nil {
		t.Fatalf("spec does not parse: %v", err)
	}
	var spec struct {
		OpenAPI string                            `json:"openapi"`
		Paths   map[string]map[string]interface{} `json:"paths"`
	}
	if err := json.Unmarshal([]byte(js), &spec); err != nil || spec.OpenAPI != "3.1.0" {
		t.Fatalf("spec json: %v %s", err, spec.OpenAPI)
	}
	// Every public route must be documented.
	e := newAPIEnv(t)
	for _, rt := range e.r.Routes() {
		if !strings.HasPrefix(rt.Path, "/api/public/v1/") || strings.HasPrefix(rt.Path, "/api/public/v1/openapi") {
			continue
		}
		p := strings.TrimPrefix(rt.Path, "/api/public/v1")
		p = strings.ReplaceAll(p, ":id", "{id}")
		ops, ok := spec.Paths[p]
		if !ok {
			t.Errorf("route %s %s is not documented", rt.Method, rt.Path)
			continue
		}
		if _, ok := ops[strings.ToLower(rt.Method)]; !ok {
			t.Errorf("method %s %s is not documented", rt.Method, rt.Path)
		}
	}
	// And every documented path must exist.
	routes := map[string]bool{}
	for _, rt := range e.r.Routes() {
		routes[strings.ToLower(rt.Method)+" "+strings.ReplaceAll(rt.Path, ":id", "{id}")] = true
	}
	for p, ops := range spec.Paths {
		for m := range ops {
			if m == "parameters" {
				continue
			}
			if !routes[m+" /api/public/v1"+p] {
				t.Errorf("documented %s %s has no route", m, p)
			}
		}
	}
}
