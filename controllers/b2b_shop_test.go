package controllers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type b2bFixture struct {
	db       *gorm.DB
	owner    uuid.UUID
	shop     models.B2BShopSettings
	listed   models.Product // listed, 10 in stock, wholesale price 80, MOQ 2
	unlisted models.Product
	buyer    models.Party // phone +91 98765 43210
}

func setupB2BShopTest(t *testing.T) *b2bFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Business{}, &models.Party{}, &models.Product{}, &models.ProductImage{},
		&models.InventoryStock{}, &models.Notification{},
		&models.Quotation{}, &models.QuotationItem{}, &models.QuotationVersion{},
		&models.B2BShopSettings{}, &models.B2BCatalogItem{}, &models.B2BOrder{}, &models.B2BOrderItem{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	prev := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = prev })

	f := &b2bFixture{db: db, owner: uuid.New()}
	f.shop = models.B2BShopSettings{ID: uuid.New(), UserID: f.owner, IsEnabled: true, Slug: "acme", ShowStock: true, AllowNewBuyers: true}
	f.listed = models.Product{ID: uuid.New(), UserID: f.owner, Name: "Rice 25kg", SKU: "R1", Unit: "BAG", SalePrice: 100, IsActive: true}
	f.unlisted = models.Product{ID: uuid.New(), UserID: f.owner, Name: "Secret item", SKU: "S1", Unit: "PCS", SalePrice: 50, IsActive: true}
	f.buyer = models.Party{ID: uuid.New(), UserID: f.owner, Name: "Sharma Traders", Phone: "+91 98765 43210", PartyType: "customer", IsActive: true}
	mustCreate(t, db, &f.shop, &f.listed, &f.unlisted, &f.buyer,
		&models.B2BCatalogItem{ID: uuid.New(), UserID: f.owner, ProductID: f.listed.ID, IsListed: true, B2BPrice: 80, MinOrderQty: 2},
		&models.InventoryStock{ID: uuid.New(), UserID: f.owner, ProductID: f.listed.ID, OutletID: uuid.New(), Quantity: 10, AvailableQty: 10},
	)
	return f
}

func mustCreate(t *testing.T, db *gorm.DB, rows ...interface{}) {
	t.Helper()
	for _, r := range rows {
		if err := db.Create(r).Error; err != nil {
			t.Fatalf("create %T: %v", r, err)
		}
	}
}

func b2bRequest(method, path string, body interface{}, params gin.Params) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	c.Request = httptest.NewRequest(method, path, &buf)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = params
	return c, rec
}

func (f *b2bFixture) placeOrder(t *testing.T, body map[string]interface{}) *httptest.ResponseRecorder {
	t.Helper()
	c, rec := b2bRequest("POST", "/shop/acme/orders", body, gin.Params{{Key: "slug", Value: "acme"}})
	PlacePublicB2BOrder(c)
	return rec
}

func (f *b2bFixture) items(qty float64) []map[string]interface{} {
	return []map[string]interface{}{{"product_id": f.listed.ID, "quantity": qty}}
}

func TestPublicB2BProductsOnlyListedWithWholesalePrice(t *testing.T) {
	f := setupB2BShopTest(t)
	c, rec := b2bRequest("GET", "/shop/acme/products", nil, gin.Params{{Key: "slug", Value: "acme"}})
	ListPublicB2BProducts(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Products []publicB2BProduct `json:"products"`
		Total    int64              `json:"total"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Total != 1 || len(body.Products) != 1 {
		t.Fatalf("want only the listed product, got total=%d %+v", body.Total, body.Products)
	}
	p := body.Products[0]
	if p.ID != f.listed.ID || p.Price != 80 || p.MinOrderQty != 2 || !p.InStock {
		t.Fatalf("unexpected product %+v", p)
	}
	if p.StockQty == nil || *p.StockQty != 10 {
		t.Fatalf("stock_qty = %v, want 10", p.StockQty)
	}
}

func TestPublicB2BShopDisabledIs404(t *testing.T) {
	f := setupB2BShopTest(t)
	f.db.Model(&f.shop).Update("is_enabled", false)
	c, rec := b2bRequest("GET", "/shop/acme", nil, gin.Params{{Key: "slug", Value: "acme"}})
	GetPublicB2BShop(c)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestPlaceB2BOrderExistingBuyerRequiresMatchingPhone(t *testing.T) {
	f := setupB2BShopTest(t)
	base := map[string]interface{}{"party_id": f.buyer.ID, "shipping_address": "12 Market Rd", "items": f.items(3)}

	base["phone"] = "9999999999"
	if rec := f.placeOrder(t, base); rec.Code != http.StatusForbidden {
		t.Fatalf("wrong phone: status = %d, want 403", rec.Code)
	}

	base["phone"] = "098765-43210" // formatting differs, last 10 digits match
	rec := f.placeOrder(t, base)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var order models.B2BOrder
	if err := f.db.Preload("Items").First(&order).Error; err != nil {
		t.Fatalf("load order: %v", err)
	}
	if order.OrderNumber != "B2B-0001" || order.TotalAmount != 240 || !order.BuyerVerified || order.IsNewBuyer {
		t.Fatalf("unexpected order %+v", order)
	}
	if len(order.Items) != 1 || order.Items[0].UnitPrice != 80 {
		t.Fatalf("unexpected items %+v", order.Items)
	}
	var notes int64
	f.db.Model(&models.Notification{}).Where("user_id = ? AND type = ?", f.owner, "b2b_order").Count(&notes)
	if notes != 1 {
		t.Fatalf("notifications = %d, want 1", notes)
	}
}

func TestPlaceB2BOrderEnforcesStockMOQAndListing(t *testing.T) {
	f := setupB2BShopTest(t)
	order := func(items []map[string]interface{}) int {
		return f.placeOrder(t, map[string]interface{}{
			"party_id": f.buyer.ID, "phone": "9876543210", "shipping_address": "x", "items": items,
		}).Code
	}
	if code := order(f.items(11)); code != http.StatusConflict {
		t.Fatalf("over stock: status = %d, want 409", code)
	}
	if code := order(f.items(1)); code != http.StatusBadRequest {
		t.Fatalf("below MOQ: status = %d, want 400", code)
	}
	if code := order([]map[string]interface{}{{"product_id": f.unlisted.ID, "quantity": 5}}); code != http.StatusConflict {
		t.Fatalf("unlisted product: status = %d, want 409", code)
	}

	f.db.Model(&f.shop).Update("allow_backorders", true)
	if code := order(f.items(11)); code != http.StatusCreated {
		t.Fatalf("backorders allowed: status = %d, want 201", code)
	}
}

func TestPlaceB2BOrderMinOrderValue(t *testing.T) {
	f := setupB2BShopTest(t)
	f.db.Model(&f.shop).Update("min_order_value", 500)
	rec := f.placeOrder(t, map[string]interface{}{
		"party_id": f.buyer.ID, "phone": "9876543210", "shipping_address": "x", "items": f.items(2),
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestPlaceB2BOrderNewBuyerCreatesThenReusesParty(t *testing.T) {
	f := setupB2BShopTest(t)
	body := func() map[string]interface{} {
		return map[string]interface{}{
			"business": map[string]interface{}{
				"name": "Fresh Mart", "phone": "91234 56789", "gstin": "29abcde1234f1z5", "address": "5 Lake View", "city": "Pune",
			},
			"items": f.items(2),
		}
	}
	if rec := f.placeOrder(t, body()); rec.Code != http.StatusCreated {
		t.Fatalf("first order: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.placeOrder(t, body()); rec.Code != http.StatusCreated {
		t.Fatalf("second order: %d %s", rec.Code, rec.Body.String())
	}
	var parties []models.Party
	f.db.Where("name = ?", "Fresh Mart").Find(&parties)
	if len(parties) != 1 || parties[0].GSTIN != "29ABCDE1234F1Z5" || parties[0].PartyType != "customer" {
		t.Fatalf("want one reused customer party, got %+v", parties)
	}
	var orders []models.B2BOrder
	f.db.Order("order_number").Find(&orders)
	if len(orders) != 2 || !orders[0].IsNewBuyer || orders[1].IsNewBuyer || orders[1].OrderNumber != "B2B-0002" {
		t.Fatalf("unexpected orders %+v", orders)
	}
	if orders[0].ShippingAddress != "5 Lake View" {
		t.Fatalf("shipping address should default to business address, got %q", orders[0].ShippingAddress)
	}

	f.db.Model(&f.shop).Update("allow_new_buyers", false)
	b := body()
	b["business"].(map[string]interface{})["phone"] = "9000000001"
	if rec := f.placeOrder(t, b); rec.Code != http.StatusForbidden {
		t.Fatalf("new buyers disabled: status = %d, want 403", rec.Code)
	}
}

func (f *b2bFixture) seedOrder(t *testing.T, status string) models.B2BOrder {
	t.Helper()
	order := models.B2BOrder{
		ID: uuid.New(), UserID: f.owner, OrderNumber: "B2B-" + uuid.NewString()[:6], PartyID: f.buyer.ID,
		Status: status, BuyerName: f.buyer.Name,
		Items: []models.B2BOrderItem{{ID: uuid.New(), UserID: f.owner, ProductID: &f.listed.ID, Name: "Rice", Quantity: 4, UnitPrice: 80, Total: 320}},
	}
	recalcB2BOrderTotals(&order)
	mustCreate(t, f.db, &order)
	return order
}

func (f *b2bFixture) adminCall(handler gin.HandlerFunc, method string, orderID uuid.UUID, body interface{}) *httptest.ResponseRecorder {
	c, rec := b2bRequest(method, "/b2b-shop/orders/"+orderID.String(), body, gin.Params{{Key: "id", Value: orderID.String()}})
	c.Set("user_id", f.owner)
	c.Set("user_name", "Asha")
	handler(c)
	return rec
}

func TestB2BOrderStatusTransitions(t *testing.T) {
	f := setupB2BShopTest(t)
	order := f.seedOrder(t, models.B2BOrderPending)
	step := func(status string) int {
		return f.adminCall(UpdateB2BOrderStatus, "PATCH", order.ID, map[string]string{"status": status}).Code
	}
	if code := step("dispatched"); code != http.StatusOK {
		t.Fatalf("skip forward: %d", code)
	}
	if code := step("confirmed"); code != http.StatusBadRequest {
		t.Fatalf("move backwards: %d, want 400", code)
	}
	if code := step("cancelled"); code != http.StatusOK {
		t.Fatalf("cancel: %d", code)
	}
	if code := step("delivered"); code != http.StatusBadRequest {
		t.Fatalf("cancelled → delivered: %d, want 400", code)
	}
	if code := step("pending"); code != http.StatusOK {
		t.Fatalf("reopen: %d", code)
	}
	var saved models.B2BOrder
	f.db.First(&saved, "id = ?", order.ID)
	if len(saved.Timeline) != 3 || saved.Timeline[0].By != "Asha" {
		t.Fatalf("timeline = %+v", saved.Timeline)
	}
}

func TestB2BOrderEditItemsAndEstimate(t *testing.T) {
	f := setupB2BShopTest(t)
	order := f.seedOrder(t, models.B2BOrderPending)

	rec := f.adminCall(UpdateB2BOrder, "PUT", order.ID, map[string]interface{}{
		"items": []map[string]interface{}{{"id": order.Items[0].ID, "quantity": 5, "unit_price": 75}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("edit items: %d %s", rec.Code, rec.Body.String())
	}

	rec = f.adminCall(CreateB2BOrderEstimate, "POST", order.ID, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("estimate: %d %s", rec.Code, rec.Body.String())
	}
	var q models.Quotation
	if err := f.db.Preload("Items").First(&q).Error; err != nil {
		t.Fatalf("load quotation: %v", err)
	}
	if q.TotalAmount != 375 || len(q.Items) != 1 || q.Items[0].Quantity != 5 || q.PartyID != f.buyer.ID {
		t.Fatalf("unexpected quotation %+v", q)
	}
	var saved models.B2BOrder
	f.db.First(&saved, "id = ?", order.ID)
	if saved.QuotationID == nil || *saved.QuotationID != q.ID || saved.Status != models.B2BOrderConfirmed {
		t.Fatalf("order not linked/confirmed: %+v", saved)
	}

	// Idempotent, and items are locked once an estimate exists.
	if rec := f.adminCall(CreateB2BOrderEstimate, "POST", order.ID, nil); rec.Code != http.StatusOK {
		t.Fatalf("repeat estimate: %d", rec.Code)
	}
	if rec := f.adminCall(UpdateB2BOrder, "PUT", order.ID, map[string]interface{}{
		"items": []map[string]interface{}{{"id": order.Items[0].ID, "quantity": 1, "unit_price": 75}},
	}); rec.Code != http.StatusBadRequest {
		t.Fatalf("edit after estimate: %d, want 400", rec.Code)
	}

	// Converting the estimate elsewhere is picked up on the next read.
	invoiceID := uuid.New()
	now := time.Now()
	f.db.Model(&q).Updates(map[string]interface{}{"converted_to_invoice_id": invoiceID, "converted_at": now, "status": "converted"})
	orders := []models.B2BOrder{saved}
	syncB2BOrderInvoiceLinks(f.owner, orders)
	if orders[0].InvoiceID == nil || *orders[0].InvoiceID != invoiceID {
		t.Fatalf("invoice link not synced")
	}
}

func TestBulkListInStockProducts(t *testing.T) {
	f := setupB2BShopTest(t)
	extra := models.Product{ID: uuid.New(), UserID: f.owner, Name: "Oil", SKU: "O1", Unit: "LTR", SalePrice: 10, IsActive: true}
	mustCreate(t, f.db, &extra,
		&models.InventoryStock{ID: uuid.New(), UserID: f.owner, ProductID: extra.ID, OutletID: uuid.New(), Quantity: 3, AvailableQty: 3})

	c, rec := b2bRequest("POST", "/b2b-shop/catalog/bulk", map[string]string{"action": "list_in_stock"}, nil)
	c.Set("user_id", f.owner)
	BulkUpdateB2BCatalog(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
	}
	var listed []uuid.UUID
	f.db.Model(&models.B2BCatalogItem{}).Where("user_id = ? AND is_listed = ?", f.owner, true).Pluck("product_id", &listed)
	if len(listed) != 2 {
		t.Fatalf("listed = %v, want rice + oil (unlisted has no stock)", listed)
	}
}

func TestListB2BCatalogShowsListingStateAndSummary(t *testing.T) {
	f := setupB2BShopTest(t)
	c, rec := b2bRequest("GET", "/b2b-shop/catalog?filter=all", nil, nil)
	c.Set("user_id", f.owner)
	ListB2BCatalog(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items   []b2bAdminCatalogRow `json:"items"`
		Total   int64                `json:"total"`
		Summary map[string]int64     `json:"summary"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Total != 2 || len(body.Items) != 2 {
		t.Fatalf("want both products, got %+v", body)
	}
	first := body.Items[0] // listed products sort first
	if first.ProductID != f.listed.ID || !first.IsListed || first.B2BPrice != 80 || first.StockQty != 10 {
		t.Fatalf("unexpected listed row %+v", first)
	}
	if body.Items[1].IsListed || body.Items[1].MinOrderQty != 1 {
		t.Fatalf("unlisted row should default to not listed / MOQ 1: %+v", body.Items[1])
	}
	if body.Summary["listed"] != 1 || body.Summary["in_stock"] != 1 || body.Summary["total_products"] != 2 {
		t.Fatalf("summary = %+v", body.Summary)
	}

	c, rec = b2bRequest("GET", "/b2b-shop/catalog?filter=unlisted", nil, nil)
	c.Set("user_id", f.owner)
	ListB2BCatalog(c)
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Total != 1 || body.Items[0].ProductID != f.unlisted.ID {
		t.Fatalf("unlisted filter: %+v", body.Items)
	}
}

func TestUpsertB2BCatalogItemDoesNotListImplicitly(t *testing.T) {
	f := setupB2BShopTest(t)
	put := func(body map[string]interface{}) {
		t.Helper()
		c, rec := b2bRequest("PUT", "/b2b-shop/catalog/"+f.unlisted.ID.String(), body, gin.Params{{Key: "product_id", Value: f.unlisted.ID.String()}})
		c.Set("user_id", f.owner)
		UpsertB2BCatalogItem(c)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
		}
	}
	load := func() models.B2BCatalogItem {
		var item models.B2BCatalogItem
		f.db.Where("product_id = ?", f.unlisted.ID).First(&item)
		return item
	}

	put(map[string]interface{}{"b2b_price": 45})
	if item := load(); item.IsListed || item.B2BPrice != 45 {
		t.Fatalf("editing price must not list the product: %+v", item)
	}
	put(map[string]interface{}{"is_listed": true})
	if item := load(); !item.IsListed || item.B2BPrice != 45 {
		t.Fatalf("explicit listing should keep price: %+v", item)
	}
	put(map[string]interface{}{"is_listed": false})
	if item := load(); item.IsListed {
		t.Fatalf("unlisting should persist false: %+v", item)
	}
}

func TestGetPublicB2BProductDetail(t *testing.T) {
	f := setupB2BShopTest(t)
	f.db.Model(&f.listed).Updates(map[string]interface{}{"category": "Grains", "image_url": "/uploads/rice.jpg", "item_code": "RC-25"})
	sibling := models.Product{ID: uuid.New(), UserID: f.owner, Name: "Wheat 25kg", SKU: "W1", Unit: "BAG", Category: "Grains", SalePrice: 90, IsActive: true}
	mustCreate(t, f.db, &sibling,
		&models.B2BCatalogItem{ID: uuid.New(), UserID: f.owner, ProductID: sibling.ID, IsListed: true, MinOrderQty: 1},
		&models.ProductImage{ID: uuid.New(), UserID: f.owner, ProductID: f.listed.ID, ImageURL: "/uploads/rice-2.jpg", SortOrder: 1},
		&models.ProductImage{ID: uuid.New(), UserID: f.owner, ProductID: f.listed.ID, ImageURL: "/uploads/rice.jpg", IsPrimary: true},
	)

	get := func(id uuid.UUID) *httptest.ResponseRecorder {
		c, rec := b2bRequest("GET", "/shop/acme/products/"+id.String(), nil, gin.Params{{Key: "slug", Value: "acme"}, {Key: "id", Value: id.String()}})
		GetPublicB2BProduct(c)
		return rec
	}

	rec := get(f.listed.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Product  publicB2BProduct   `json:"product"`
		Images   []string           `json:"images"`
		ItemCode string             `json:"item_code"`
		Related  []publicB2BProduct `json:"related"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Product.Price != 80 || body.ItemCode != "RC-25" {
		t.Fatalf("unexpected product %+v code=%q", body.Product, body.ItemCode)
	}
	if len(body.Images) != 2 || body.Images[0] != "/uploads/rice.jpg" || body.Images[1] != "/uploads/rice-2.jpg" {
		t.Fatalf("gallery should dedupe and keep the main image first: %v", body.Images)
	}
	if len(body.Related) != 1 || body.Related[0].ID != sibling.ID {
		t.Fatalf("related should hold the listed sibling only: %+v", body.Related)
	}

	if rec := get(f.unlisted.ID); rec.Code != http.StatusNotFound {
		t.Fatalf("unlisted product: status = %d, want 404", rec.Code)
	}
}

func (f *b2bFixture) searchBuyers(t *testing.T, q string) []map[string]interface{} {
	t.Helper()
	c, rec := b2bRequest("GET", "/shop/acme/buyers?q="+q, nil, gin.Params{{Key: "slug", Value: "acme"}})
	c.Request.URL.RawQuery = "q=" + q
	SearchPublicB2BBuyers(c)
	var out []map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

func TestSearchB2BBuyersCoversAllPartiesNamePhoneGSTIN(t *testing.T) {
	f := setupB2BShopTest(t)
	vendor := models.Party{ID: uuid.New(), UserID: f.owner, Name: "Gupta Agro Supplies", Phone: "9811122233", GSTIN: "07AAAAA1111A1Z1", PartyType: "vendor", IsActive: true}
	mustCreate(t, f.db, &vendor)

	if got := f.searchBuyers(t, "guptaagro"); len(got) != 1 || got[0]["name"] != vendor.Name {
		t.Fatalf("vendor should match whitespace-insensitive name search: %v", got)
	}
	if got := f.searchBuyers(t, "07aaaaa1111a1z1"); len(got) != 1 || got[0]["name"] != vendor.Name {
		t.Fatalf("GSTIN search: %v", got)
	}
	if got := f.searchBuyers(t, "9876543210"); len(got) != 1 || got[0]["name"] != f.buyer.Name {
		t.Fatalf("phone search: %v", got)
	}
	for _, r := range f.searchBuyers(t, "sharma") {
		if _, leaked := r["phone"]; leaked {
			t.Fatalf("search must not expose phone numbers: %v", r)
		}
	}

	// A vendor can place an order as itself.
	rec := f.placeOrder(t, map[string]interface{}{
		"party_id": vendor.ID, "phone": "98111 22233", "shipping_address": "Yard 4", "items": f.items(2),
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("vendor order: %d %s", rec.Code, rec.Body.String())
	}
}

func signup(f *b2bFixture, business map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{"business": business, "items": f.items(2)}
}

func (f *b2bFixture) lastOrder(t *testing.T) models.B2BOrder {
	t.Helper()
	var o models.B2BOrder
	if err := f.db.Order("order_number DESC").First(&o).Error; err != nil {
		t.Fatalf("load order: %v", err)
	}
	return o
}

func TestB2BSignupMatchesExistingParties(t *testing.T) {
	f := setupB2BShopTest(t)
	gstParty := models.Party{ID: uuid.New(), UserID: f.owner, Name: "Om Stores", Phone: "9000000001", GSTIN: "27ABCDE1234F1Z5", Address: "Old Address", PartyType: "customer", IsActive: true}
	mustCreate(t, f.db, &gstParty)
	var before int64
	f.db.Model(&models.Party{}).Count(&before)

	// Phone match: verified, fills blanks only.
	rec := f.placeOrder(t, signup(f, map[string]interface{}{
		"name": "Sharma Traders Pvt", "phone": "+91-98765-43210", "email": "buy@sharma.test",
		"gstin": "08SHARM1234A1Z9", "address": "New Godown", "city": "Jaipur",
	}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("phone match: %d %s", rec.Code, rec.Body.String())
	}
	o := f.lastOrder(t)
	var sharma models.Party
	f.db.First(&sharma, "id = ?", f.buyer.ID)
	if o.PartyID != f.buyer.ID || o.BuyerMatch != "phone" || !o.BuyerVerified || o.IsNewBuyer {
		t.Fatalf("phone match order: %+v", o)
	}
	if sharma.Email != "buy@sharma.test" || sharma.GSTIN != "08SHARM1234A1Z9" || sharma.StateCode != "08" || sharma.Name != "Sharma Traders" {
		t.Fatalf("blanks should be filled and name kept: %+v", sharma)
	}

	// GSTIN match with a different phone: linked, unverified, party untouched,
	// and the buyer can track with the phone they used.
	rec = f.placeOrder(t, signup(f, map[string]interface{}{
		"name": "Om Stores Branch", "phone": "9333344444", "gstin": "27abcde1234f1z5", "address": "Branch Road",
	}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("gstin match: %d %s", rec.Code, rec.Body.String())
	}
	o = f.lastOrder(t)
	if o.PartyID != gstParty.ID || o.BuyerMatch != "gstin" || o.BuyerVerified || o.BuyerPhone != "9333344444" || o.ShippingAddress != "Branch Road" {
		t.Fatalf("gstin match order: %+v", o)
	}
	var om models.Party
	f.db.First(&om, "id = ?", gstParty.ID)
	if om.Phone != "9000000001" || om.Address != "Old Address" {
		t.Fatalf("unverified match must not modify the party: %+v", om)
	}
	c, trackRec := b2bRequest("GET", "/shop/acme/orders/"+o.OrderNumber+"?phone=9333344444", nil,
		gin.Params{{Key: "slug", Value: "acme"}, {Key: "number", Value: o.OrderNumber}})
	TrackPublicB2BOrder(c)
	if trackRec.Code != http.StatusOK {
		t.Fatalf("tracking with the sign-up phone: %d", trackRec.Code)
	}

	// Exact name match (case/spacing-insensitive).
	rec = f.placeOrder(t, signup(f, map[string]interface{}{"name": "om  STORES", "phone": "9555566666", "address": "Shop 9"}))
	if o = f.lastOrder(t); rec.Code != http.StatusCreated || o.PartyID != gstParty.ID || o.BuyerMatch != "name" {
		t.Fatalf("name match: %d %+v", rec.Code, o)
	}

	var after int64
	f.db.Model(&models.Party{}).Count(&after)
	if after != before {
		t.Fatalf("matched sign-ups must not create parties: before=%d after=%d", before, after)
	}
}

func TestB2BSignupIndividualCreatesRetailCustomer(t *testing.T) {
	f := setupB2BShopTest(t)
	rec := f.placeOrder(t, signup(f, map[string]interface{}{
		"buyer_type": "individual", "name": "Priya Nair", "phone": "9445566778", "address": "Flat 3B",
	}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
	}
	var p models.Party
	f.db.First(&p, "name = ?", "Priya Nair")
	o := f.lastOrder(t)
	if p.PartyType != "customer" || p.Category != "Retail" || o.BuyerMatch != "created" || !o.IsNewBuyer {
		t.Fatalf("party=%+v order=%+v", p, o)
	}
}

func TestPlaceB2BOrderDeliveryAddressSetting(t *testing.T) {
	f := setupB2BShopTest(t)
	// The buyer has no address on file and none is given.
	body := map[string]interface{}{"party_id": f.buyer.ID, "phone": "9876543210", "items": f.items(2)}
	if rec := f.placeOrder(t, body); rec.Code != http.StatusBadRequest {
		t.Fatalf("address required by default: status = %d, want 400", rec.Code)
	}
	f.db.Model(&f.shop).Update("require_delivery_address", false)
	if rec := f.placeOrder(t, body); rec.Code != http.StatusCreated {
		t.Fatalf("address optional: status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
}

func TestListB2BPendingItemsBreakdownByOrder(t *testing.T) {
	f := setupB2BShopTest(t)
	for _, qty := range []float64{2, 5} {
		rec := f.placeOrder(t, map[string]interface{}{
			"party_id": f.buyer.ID, "phone": "9876543210", "shipping_address": "x", "items": f.items(qty),
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("place order: %d %s", rec.Code, rec.Body.String())
		}
	}

	c, rec := b2bRequest("GET", "/b2b-shop/orders/pending-items", nil, nil)
	c.Set("user_id", f.owner)
	ListB2BPendingItems(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Items []struct {
			Quantity float64 `json:"quantity"`
			Orders   []struct {
				OrderNumber string  `json:"order_number"`
				BuyerName   string  `json:"buyer_name"`
				Quantity    float64 `json:"quantity"`
			} `json:"orders"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) != 1 || resp.Items[0].Quantity != 7 {
		t.Fatalf("items = %+v, want one item with qty 7", resp.Items)
	}
	orders := resp.Items[0].Orders
	if len(orders) != 2 || orders[0].Quantity+orders[1].Quantity != 7 || orders[0].OrderNumber == "" || orders[0].BuyerName != "Sharma Traders" {
		t.Fatalf("orders breakdown = %+v", orders)
	}
}

func TestListB2BPendingItemsPaginated(t *testing.T) {
	f := setupB2BShopTest(t)
	f.db.Create(&models.B2BCatalogItem{ID: uuid.New(), UserID: f.owner, ProductID: f.unlisted.ID, IsListed: true, B2BPrice: 40, MinOrderQty: 1})
	f.db.Create(&models.InventoryStock{ID: uuid.New(), UserID: f.owner, ProductID: f.unlisted.ID, OutletID: uuid.New(), Quantity: 5, AvailableQty: 5})
	rec := f.placeOrder(t, map[string]interface{}{
		"party_id": f.buyer.ID, "phone": "9876543210", "shipping_address": "x",
		"items": []map[string]interface{}{
			{"product_id": f.listed.ID, "quantity": 8},
			{"product_id": f.unlisted.ID, "quantity": 1},
		},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("place order: %d %s", rec.Code, rec.Body.String())
	}
	// Stock drops below what the pending order needs for both items.
	f.db.Model(&models.InventoryStock{}).Where("user_id = ?", f.owner).Update("available_qty", 0)

	type pageResp struct {
		Items []struct {
			Name   string            `json:"name"`
			Amount float64           `json:"amount"`
			Orders []json.RawMessage `json:"orders"`
		} `json:"items"`
		Total  int     `json:"total"`
		Short  int     `json:"short"`
		Amount float64 `json:"amount"`
	}
	get := func(page string) pageResp {
		c, rec := b2bRequest("GET", "/b2b-shop/orders/pending-items?per_page=1&page="+page, nil, nil)
		c.Set("user_id", f.owner)
		ListB2BPendingItems(c)
		if rec.Code != http.StatusOK {
			t.Fatalf("page %s: status = %d: %s", page, rec.Code, rec.Body.String())
		}
		var r pageResp
		if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		return r
	}

	p1, p2, p3 := get("1"), get("2"), get("3")
	if len(p1.Items) != 1 || p1.Items[0].Name != "Rice 25kg" || len(p1.Items[0].Orders) != 1 {
		t.Fatalf("page 1 = %+v, want Rice 25kg with its order", p1.Items)
	}
	if len(p2.Items) != 1 || p2.Items[0].Name != "Secret item" || len(p2.Items[0].Orders) != 1 {
		t.Fatalf("page 2 = %+v, want Secret item with its order", p2.Items)
	}
	if len(p3.Items) != 0 {
		t.Fatalf("page 3 = %+v, want empty", p3.Items)
	}
	for _, r := range []pageResp{p1, p2, p3} {
		// Totals cover every item, not just the page: both are short on stock.
		if r.Total != 2 || r.Short != 2 || r.Amount != p1.Items[0].Amount+p2.Items[0].Amount {
			t.Fatalf("totals = total %d short %d amount %v", r.Total, r.Short, r.Amount)
		}
	}
}

func TestExportB2BOrdersFormats(t *testing.T) {
	f := setupB2BShopTest(t)
	rec := f.placeOrder(t, map[string]interface{}{
		"party_id": f.buyer.ID, "phone": "9876543210", "shipping_address": "x", "items": f.items(3),
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("place order: %d %s", rec.Code, rec.Body.String())
	}
	cases := []struct{ query, contentType, magic, contains string }{
		{"format=csv&status=pending", "text/csv", "\ufeffOrder #", "Sharma Traders"},
		{"format=csv&status=delivered", "text/csv", "\ufeffOrder #", "Total,0 orders"},
		{"format=csv&view=items", "text/csv", "\ufeffItem / Order", "Rice 25kg"},
		{"format=excel&status=all", "spreadsheetml", "PK", ""},
		{"format=pdf&view=items", "application/pdf", "%PDF", ""},
		{"format=pdf&status=pending", "application/pdf", "%PDF", ""},
	}
	for _, tc := range cases {
		c, rec := b2bRequest("GET", "/b2b-shop/orders/export?"+tc.query, nil, nil)
		c.Set("user_id", f.owner)
		ExportB2BOrders(c)
		body := rec.Body.String()
		if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), tc.contentType) || !strings.HasPrefix(body, tc.magic) {
			t.Fatalf("%s: status %d, type %q, body %.40q", tc.query, rec.Code, rec.Header().Get("Content-Type"), body)
		}
		if tc.contains != "" && !strings.Contains(body, tc.contains) {
			t.Fatalf("%s: body missing %q:\n%s", tc.query, tc.contains, body)
		}
	}
	c, rec2 := b2bRequest("GET", "/b2b-shop/orders/export?format=doc", nil, nil)
	c.Set("user_id", f.owner)
	ExportB2BOrders(c)
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("bad format: status %d", rec2.Code)
	}
}
