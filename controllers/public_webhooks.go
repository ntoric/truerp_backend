package controllers

// Event feed and webhooks for the public API.
//
// The change scanner reads rows touched since its per-store cursor in the
// parties, products, invoices, payments, purchase_bills and inventory_stocks
// tables, so changes from every client (web, mobile, POS, migrations, the
// public API itself) produce events without instrumenting each handler.
// Events are stored (the outbox / event feed) and fanned out to webhook
// endpoints as deliveries; the delivery worker signs and sends them with
// exponential-backoff retries.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// WebhookEventTypes lists every event type an endpoint can subscribe to.
var WebhookEventTypes = []struct {
	Type        string `json:"type"`
	Description string `json:"description"`
	Scope       string `json:"scope"`
}{
	{"party.created", "A customer or vendor was created", utils.ScopePartiesRead},
	{"party.updated", "A customer or vendor was updated (including balance changes)", utils.ScopePartiesRead},
	{"party.deleted", "A customer or vendor was deleted", utils.ScopePartiesRead},
	{"product.created", "A product or service was created", utils.ScopeProductsRead},
	{"product.updated", "A product or service was updated", utils.ScopeProductsRead},
	{"product.deleted", "A product or service was deleted", utils.ScopeProductsRead},
	{"stock.updated", "Stock quantity of a product changed", utils.ScopeStockRead},
	{"invoice.created", "A sales invoice was created (any channel, including POS)", utils.ScopeInvoicesRead},
	{"invoice.updated", "A sales invoice was edited or its payment status changed", utils.ScopeInvoicesRead},
	{"invoice.cancelled", "A sales invoice was cancelled", utils.ScopeInvoicesRead},
	{"invoice.deleted", "A sales invoice was deleted", utils.ScopeInvoicesRead},
	{"payment.created", "A payment was received", utils.ScopePaymentsRead},
	{"payment.updated", "A payment was updated", utils.ScopePaymentsRead},
	{"payment.cancelled", "A payment was cancelled", utils.ScopePaymentsRead},
	{"payment.deleted", "A payment was deleted", utils.ScopePaymentsRead},
	{"purchase_bill.created", "A purchase bill was created", utils.ScopePurchasesRead},
	{"purchase_bill.updated", "A purchase bill was updated", utils.ScopePurchasesRead},
	{"purchase_bill.deleted", "A purchase bill was deleted", utils.ScopePurchasesRead},
}

func eventTypeKnown(t string) bool {
	for _, e := range WebhookEventTypes {
		if e.Type == t {
			return true
		}
	}
	return false
}

// eventScope returns the read scope needed to receive an event type.
func eventScope(t string) string {
	for _, e := range WebhookEventTypes {
		if e.Type == t {
			return e.Scope
		}
	}
	return ""
}

func endpointWants(endpoint models.WebhookEndpoint, eventType string) bool {
	for _, e := range strings.Fields(endpoint.Events) {
		if e == "*" || e == eventType {
			return true
		}
		if strings.HasSuffix(e, ".*") && strings.HasPrefix(eventType, strings.TrimSuffix(e, "*")) {
			return true
		}
	}
	return false
}

// normalizeEventList validates and normalises a subscription list.
func normalizeEventList(events []string) (string, error) {
	if len(events) == 0 {
		return "", errors.New("events must list at least one event type, or \"*\"")
	}
	seen := map[string]bool{}
	out := []string{}
	for _, e := range events {
		e = strings.TrimSpace(e)
		if e == "" || seen[e] {
			continue
		}
		valid := e == "*" || eventTypeKnown(e)
		if strings.HasSuffix(e, ".*") {
			prefix := strings.TrimSuffix(e, "*")
			for _, t := range WebhookEventTypes {
				if strings.HasPrefix(t.Type, prefix) {
					valid = true
				}
			}
		}
		if !valid {
			return "", fmt.Errorf("unknown event type %q", e)
		}
		seen[e] = true
		out = append(out, e)
	}
	return strings.Join(out, " "), nil
}

// ============================================================
// SSRF-safe webhook URLs and HTTP client
// ============================================================

func webhooksAllowPrivate() bool {
	return strings.EqualFold(os.Getenv("WEBHOOKS_ALLOW_PRIVATE_NETWORKS"), "true")
}

// validateWebhookURL requires https (http only when private networks are
// allowed for local development) and no credentials in the URL.
func validateWebhookURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return errors.New("url must be an absolute URL")
	}
	if u.User != nil {
		return errors.New("url must not contain credentials")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && webhooksAllowPrivate()) {
		return errors.New("url must use https")
	}
	if len(raw) > 2048 {
		return errors.New("url is too long")
	}
	if !webhooksAllowPrivate() {
		host := u.Hostname()
		if ip := net.ParseIP(host); ip != nil && isPrivateIP(ip) {
			return errors.New("url must not point to a private or loopback address")
		}
		if host == "localhost" || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
			return errors.New("url must be publicly reachable")
		}
	}
	return nil
}

func isPrivateIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() || ip.Equal(net.ParseIP("169.254.169.254")) ||
		(ip.To4() != nil && ip.To4()[0] == 100 && ip.To4()[1]&0xc0 == 64) // 100.64.0.0/10 CGNAT
}

// webhookHTTPClient checks the resolved address at connect time, so DNS
// rebinding cannot redirect deliveries to internal services.
var webhookHTTPClient = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: 5 * time.Second,
			Control: func(network, address string, _ syscall.RawConn) error {
				if webhooksAllowPrivate() {
					return nil
				}
				host, _, err := net.SplitHostPort(address)
				if err != nil {
					return err
				}
				if ip := net.ParseIP(host); ip == nil || isPrivateIP(ip) {
					return fmt.Errorf("webhook destination %s is not allowed", host)
				}
				return nil
			},
		}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		MaxIdleConns:          50,
		IdleConnTimeout:       60 * time.Second,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// SignWebhookPayload returns the TruERP-Signature header value:
// t=<unix seconds>,v1=<hex HMAC-SHA256(secret, "<t>.<body>")>.
func SignWebhookPayload(secret string, ts int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	return fmt.Sprintf("t=%d,v1=%s", ts, hex.EncodeToString(mac.Sum(nil)))
}

func newWebhookSecret() string {
	s, _ := generateRandomSecret(32)
	return "whsec_" + s
}

// ============================================================
// Public API: webhook endpoint management
// ============================================================

type publicWebhookInput struct {
	URL         *string   `json:"url"`
	Description *string   `json:"description"`
	Events      *[]string `json:"events"`
	IsActive    *bool     `json:"is_active"`
}

func webhooksAllowedForStore(c *gin.Context) bool {
	if v, ok := c.Get(utils.CtxIntegrationPolicy); ok {
		if p, ok := v.(models.IntegrationPolicy); ok && !p.WebhooksEnabled {
			utils.PublicAPIError(c, http.StatusForbidden, "webhooks_disabled", "Webhooks are disabled for this store by the administrator.")
			return false
		}
	}
	return true
}

func loadWebhook(c *gin.Context, id uuid.UUID) (models.WebhookEndpoint, bool) {
	var w models.WebhookEndpoint
	if err := utils.DB.Where("store_id = ? AND id = ?", publicStoreID(c), id).First(&w).Error; err != nil {
		notFound(c, "webhook endpoint", id)
		return w, false
	}
	return w, true
}

func PublicListWebhooks(c *gin.Context) {
	var rows []models.WebhookEndpoint
	utils.DB.Where("store_id = ?", publicStoreID(c)).Order("created_at DESC").Find(&rows)
	items := make([]interface{}, len(rows))
	for i, w := range rows {
		items[i] = toPublicWebhook(w)
	}
	c.JSON(http.StatusOK, listEnvelope{Object: "list", Data: items})
}

func PublicGetWebhook(c *gin.Context) {
	id, ok := publicPathID(c, "id")
	if !ok {
		return
	}
	if w, ok := loadWebhook(c, id); ok {
		c.JSON(http.StatusOK, toPublicWebhook(w))
	}
}

const maxWebhooksPerStore = 20

func PublicCreateWebhook(c *gin.Context) {
	if !webhooksAllowedForStore(c) {
		return
	}
	var in publicWebhookInput
	if !bindPublicJSON(c, &in) {
		return
	}
	w, secret, status, code, msg := createWebhookEndpoint(publicStoreID(c), in, "api")
	if status != 0 {
		utils.PublicAPIError(c, status, code, msg)
		return
	}
	out := toPublicWebhook(w)
	out.Secret = secret
	c.JSON(http.StatusCreated, out)
}

func createWebhookEndpoint(storeID uuid.UUID, in publicWebhookInput, via string) (models.WebhookEndpoint, string, int, string, string) {
	var count int64
	utils.DB.Model(&models.WebhookEndpoint{}).Where("store_id = ?", storeID).Count(&count)
	if count >= maxWebhooksPerStore {
		return models.WebhookEndpoint{}, "", http.StatusBadRequest, "limit_reached", fmt.Sprintf("A store can have at most %d webhook endpoints", maxWebhooksPerStore)
	}
	if in.URL == nil {
		return models.WebhookEndpoint{}, "", http.StatusBadRequest, "parameter_invalid", "url is required"
	}
	if err := validateWebhookURL(*in.URL); err != nil {
		return models.WebhookEndpoint{}, "", http.StatusBadRequest, "parameter_invalid", err.Error()
	}
	events := []string{"*"}
	if in.Events != nil {
		events = *in.Events
	}
	eventList, err := normalizeEventList(events)
	if err != nil {
		return models.WebhookEndpoint{}, "", http.StatusBadRequest, "parameter_invalid", err.Error()
	}
	secret := newWebhookSecret()
	enc, err := utils.Encrypt(secret)
	if err != nil {
		return models.WebhookEndpoint{}, "", http.StatusInternalServerError, "internal_error", "Failed to secure the signing secret"
	}
	w := models.WebhookEndpoint{
		StoreID: storeID, URL: strings.TrimSpace(*in.URL), Events: eventList,
		EncryptedSecret: enc, IsActive: true, CreatedVia: via,
	}
	if in.Description != nil {
		w.Description = strings.TrimSpace(*in.Description)
	}
	if in.IsActive != nil {
		w.IsActive = *in.IsActive
	}
	if err := utils.DB.Create(&w).Error; err != nil {
		return models.WebhookEndpoint{}, "", http.StatusInternalServerError, "internal_error", "Failed to create webhook endpoint"
	}
	if !w.IsActive {
		// Create skips false for default:true columns; persist it explicitly.
		utils.DB.Model(&w).Update("is_active", false)
	}
	ensureScanCursor(storeID)
	return w, secret, 0, "", ""
}

func applyWebhookUpdate(w *models.WebhookEndpoint, in publicWebhookInput) error {
	if in.URL != nil {
		if err := validateWebhookURL(*in.URL); err != nil {
			return err
		}
		w.URL = strings.TrimSpace(*in.URL)
	}
	if in.Events != nil {
		list, err := normalizeEventList(*in.Events)
		if err != nil {
			return err
		}
		w.Events = list
	}
	if in.Description != nil {
		w.Description = strings.TrimSpace(*in.Description)
	}
	if in.IsActive != nil {
		w.IsActive = *in.IsActive
		if w.IsActive {
			w.FailureCount = 0
			w.DisabledReason = ""
		}
	}
	return nil
}

func PublicUpdateWebhook(c *gin.Context) {
	id, ok := publicPathID(c, "id")
	if !ok {
		return
	}
	var in publicWebhookInput
	if !bindPublicJSON(c, &in) {
		return
	}
	w, ok := loadWebhook(c, id)
	if !ok {
		return
	}
	if err := applyWebhookUpdate(&w, in); err != nil {
		paramError(c, "", err.Error())
		return
	}
	utils.DB.Save(&w)
	c.JSON(http.StatusOK, toPublicWebhook(w))
}

func PublicDeleteWebhook(c *gin.Context) {
	id, ok := publicPathID(c, "id")
	if !ok {
		return
	}
	w, ok := loadWebhook(c, id)
	if !ok {
		return
	}
	utils.DB.Where("endpoint_id = ?", w.ID).Delete(&models.WebhookDelivery{})
	utils.DB.Delete(&w)
	c.JSON(http.StatusOK, gin.H{"id": w.ID, "object": "webhook_endpoint", "deleted": true})
}

func PublicRotateWebhookSecret(c *gin.Context) {
	id, ok := publicPathID(c, "id")
	if !ok {
		return
	}
	w, ok := loadWebhook(c, id)
	if !ok {
		return
	}
	secret := newWebhookSecret()
	enc, err := utils.Encrypt(secret)
	if err != nil {
		utils.PublicAPIError(c, http.StatusInternalServerError, "internal_error", "Failed to rotate secret")
		return
	}
	utils.DB.Model(&w).Update("encrypted_secret", enc)
	out := toPublicWebhook(w)
	out.Secret = secret
	c.JSON(http.StatusOK, out)
}

// PublicTestWebhook sends a synchronous "ping" event and reports the result.
func PublicTestWebhook(c *gin.Context) {
	id, ok := publicPathID(c, "id")
	if !ok {
		return
	}
	w, ok := loadWebhook(c, id)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, sendTestWebhook(w))
}

func sendTestWebhook(w models.WebhookEndpoint) gin.H {
	evt := models.IntegrationEvent{
		ID: uuid.New(), StoreID: w.StoreID, Type: "ping", ObjectType: "webhook_endpoint",
		ObjectID: w.ID, Data: `{"message":"Webhook test from TruERP"}`, CreatedAt: time.Now(),
	}
	status, body, err := deliverWebhook(w, evt)
	result := gin.H{"object": "webhook_test", "endpoint_id": w.ID, "response_status": status, "response_body": body, "succeeded": err == nil}
	if err != nil {
		result["error"] = err.Error()
	}
	return result
}

func PublicListWebhookDeliveries(c *gin.Context) {
	id, ok := publicPathID(c, "id")
	if !ok {
		return
	}
	if _, ok := loadWebhook(c, id); !ok {
		return
	}
	page, ok := readPageParams(c)
	if !ok {
		return
	}
	q := utils.DB.Model(&models.WebhookDelivery{}).Where("endpoint_id = ?", id)
	if s := c.Query("status"); s != "" {
		q = q.Where("status = ?", s)
	}
	var rows []models.WebhookDelivery
	applyPage(q, "webhook_deliveries", page).Find(&rows)
	items := make([]interface{}, len(rows))
	for i, d := range rows {
		items[i] = publicDelivery(d)
	}
	writeList(c, items, page.limit, func(i int) (time.Time, uuid.UUID) { return rows[i].CreatedAt, rows[i].ID })
}

func publicDelivery(d models.WebhookDelivery) gin.H {
	return gin.H{
		"id": d.ID, "object": "webhook_delivery", "endpoint_id": d.EndpointID, "event_id": d.EventID,
		"event_type": d.EventType, "status": d.Status, "attempts": d.Attempts,
		"next_attempt_at": publicTimestampPtr(d.NextAttemptAt), "response_status": d.ResponseStatus,
		"last_error": d.LastError, "delivered_at": publicTimestampPtr(d.DeliveredAt),
		"created_at": publicTimestamp(d.CreatedAt),
	}
}

// PublicListEventTypes lists subscribable event types.
func PublicListEventTypes(c *gin.Context) {
	items := make([]interface{}, len(WebhookEventTypes))
	for i, e := range WebhookEventTypes {
		items[i] = gin.H{"object": "event_type", "type": e.Type, "description": e.Description, "required_scope": e.Scope}
	}
	c.JSON(http.StatusOK, listEnvelope{Object: "list", Data: items})
}

// ============================================================
// Public API: event feed (polling alternative to webhooks)
// ============================================================

func PublicListEvents(c *gin.Context) {
	page, ok := readPageParams(c)
	if !ok {
		return
	}
	storeID := publicStoreID(c)
	ensureScanCursor(storeID)
	q := utils.DB.Model(&models.IntegrationEvent{}).Where("store_id = ?", storeID)
	if t := c.Query("type"); t != "" {
		q = q.Where("type IN ?", strings.Split(t, ","))
	}
	// Only return events the credential could read directly.
	allowedTypes := []string{}
	for _, e := range WebhookEventTypes {
		if hasScope(c, e.Scope) {
			allowedTypes = append(allowedTypes, e.Type)
		}
	}
	q = q.Where("type IN ?", allowedTypes)
	// The event feed is chronological: events are listed oldest-first after
	// the cursor so consumers can process them in order.
	if page.createdSince != nil {
		q = q.Where("created_at >= ?", *page.createdSince)
	}
	if page.hasCursor {
		q = q.Where("(created_at > ?) OR (created_at = ? AND id > ?)", page.cursorTime, page.cursorTime, page.cursorID)
	}
	var rows []models.IntegrationEvent
	q.Order("created_at ASC").Order("id ASC").Limit(page.limit + 1).Find(&rows)
	items := make([]interface{}, len(rows))
	for i, e := range rows {
		items[i] = toPublicEvent(e)
	}
	env := listEnvelope{Object: "list", Data: items}
	if len(rows) > page.limit {
		env.HasMore = true
		env.Data = items[:page.limit]
	}
	// next_cursor is always returned for the feed so pollers can resume
	// from the last event they saw even when has_more is false.
	n := len(rows)
	if n > page.limit {
		n = page.limit
	}
	if n > 0 {
		cur := encodeCursor(rows[n-1].CreatedAt, rows[n-1].ID)
		env.NextCursor = &cur
	} else if page.hasCursor {
		cur := c.Query("cursor")
		env.NextCursor = &cur
	}
	if env.Data == nil {
		env.Data = []interface{}{}
	}
	c.JSON(http.StatusOK, env)
}

func PublicGetEvent(c *gin.Context) {
	id, ok := publicPathID(c, "id")
	if !ok {
		return
	}
	var e models.IntegrationEvent
	if err := utils.DB.Where("store_id = ? AND id = ?", publicStoreID(c), id).First(&e).Error; err != nil || !hasScope(c, eventScope(e.Type)) {
		notFound(c, "event", id)
		return
	}
	c.JSON(http.StatusOK, toPublicEvent(e))
}

func hasScope(c *gin.Context, scope string) bool {
	v, ok := c.Get(utils.CtxPublicScopes)
	if !ok {
		return false
	}
	set, _ := v.(map[string]bool)
	return set[scope]
}

// ============================================================
// Change scanner (outbox producer)
// ============================================================

const (
	scanInterval      = 15 * time.Second
	scanSettleDelay   = 3 * time.Second // ignore rows newer than this (in-flight commits)
	scanMaxRowsPerTab = 2000
	eventRetention    = 30 * 24 * time.Hour
)

// ensureScanCursor starts change capture for a store from "now".
func ensureScanCursor(storeID uuid.UUID) {
	var cur models.IntegrationScanCursor
	if err := utils.DB.Where("store_id = ?", storeID).First(&cur).Error; err == nil {
		return
	}
	utils.DB.Create(&models.IntegrationScanCursor{StoreID: storeID, ScannedTo: time.Now().Add(-scanSettleDelay)})
}

var integrationSchedulerOnce sync.Once

// StartIntegrationScheduler launches the change scanner, the webhook
// delivery worker and daily retention cleanup.
func StartIntegrationScheduler() {
	integrationSchedulerOnce.Do(func() {
		go func() {
			t := time.NewTicker(scanInterval)
			defer t.Stop()
			for range t.C {
				safeRun("integration scan", scanAllStores)
			}
		}()
		go func() {
			t := time.NewTicker(5 * time.Second)
			defer t.Stop()
			for range t.C {
				safeRun("webhook delivery", deliverDueWebhooks)
			}
		}()
		go func() {
			t := time.NewTicker(6 * time.Hour)
			defer t.Stop()
			for range t.C {
				safeRun("integration cleanup", cleanupIntegrationData)
			}
		}()
	})
}

// RunIntegrationScanNow runs one change-scan pass and one delivery pass
// synchronously (used by tests and admin tooling).
func RunIntegrationScanNow() {
	scanAllStores()
	deliverDueWebhooks()
}

func safeRun(name string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("%s panic: %v", name, r)
		}
	}()
	fn()
}

func scanAllStores() {
	var cursors []models.IntegrationScanCursor
	if err := utils.DB.Find(&cursors).Error; err != nil {
		return
	}
	for _, cur := range cursors {
		scanStore(cur)
	}
}

type scanRow struct {
	ID        uuid.UUID
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time
	Status    string
	Cancelled *time.Time
}

func scanStore(cur models.IntegrationScanCursor) {
	store, err := utils.FindStoreByID(utils.DB, cur.StoreID)
	if err != nil {
		return
	}
	from := cur.ScannedTo
	to := time.Now().Add(-scanSettleDelay)
	if !to.After(from) {
		return
	}
	owner := store.OwnerUserID
	loc, _ := ConfiguredLocationForUser(owner)
	if loc == nil {
		loc = time.Local
	}

	type pending struct {
		typ, objType string
		id           uuid.UUID
		data         interface{}
		at           time.Time
	}
	var events []pending

	collect := func(table, objType string, cancelCol string) []scanRow {
		cols := "id, created_at, updated_at, deleted_at"
		if cancelCol != "" {
			cols += ", status, " + cancelCol + " AS cancelled"
		}
		var rows []scanRow
		utils.DB.Table(table).Select(cols).
			Where("user_id = ?", owner).
			Where("(updated_at > ? AND updated_at <= ?) OR (deleted_at > ? AND deleted_at <= ?)", from, to, from, to).
			Order("updated_at ASC").Limit(scanMaxRowsPerTab).Scan(&rows)
		if len(rows) == scanMaxRowsPerTab {
			log.Printf("integration scan: store %s table %s hit the %d row cap", store.ID, table, scanMaxRowsPerTab)
		}
		return rows
	}
	classify := func(prefix string, r scanRow) string {
		switch {
		case r.DeletedAt != nil && r.DeletedAt.After(from):
			return prefix + ".deleted"
		case r.CreatedAt.After(from):
			return prefix + ".created"
		case r.Status == "cancelled" && r.Cancelled != nil && r.Cancelled.After(from):
			return prefix + ".cancelled"
		default:
			return prefix + ".updated"
		}
	}

	for _, r := range collect("parties", "party", "") {
		var p models.Party
		if utils.DB.Unscoped().First(&p, "id = ?", r.ID).Error == nil {
			events = append(events, pending{classify("party", r), "party", r.ID, toPublicParty(p), r.UpdatedAt})
		}
	}
	for _, r := range collect("products", "product", "") {
		var p models.Product
		if utils.DB.Unscoped().First(&p, "id = ?", r.ID).Error == nil {
			events = append(events, pending{classify("product", r), "product", r.ID, toPublicProduct(p), r.UpdatedAt})
		}
	}
	for _, r := range collect("invoices", "invoice", "cancelled_at") {
		var inv models.Invoice
		if utils.DB.Unscoped().Preload("Party", func(db *gorm.DB) *gorm.DB { return db.Unscoped() }).Preload("Items").First(&inv, "id = ?", r.ID).Error == nil {
			events = append(events, pending{classify("invoice", r), "invoice", r.ID, toPublicInvoice(inv, loc), r.UpdatedAt})
		}
	}
	for _, r := range collect("payments", "payment", "cancelled_at") {
		var p models.Payment
		if utils.DB.Unscoped().Preload("Party", func(db *gorm.DB) *gorm.DB { return db.Unscoped() }).First(&p, "id = ?", r.ID).Error == nil {
			events = append(events, pending{classify("payment", r), "payment", r.ID, toPublicPayment(p, loc), r.UpdatedAt})
		}
	}
	for _, r := range collect("purchase_bills", "purchase_bill", "") {
		var b models.PurchaseBill
		if utils.DB.Unscoped().Preload("Party", func(db *gorm.DB) *gorm.DB { return db.Unscoped() }).Preload("Items").First(&b, "id = ?", r.ID).Error == nil {
			events = append(events, pending{classify("purchase_bill", r), "purchase_bill", r.ID, toPublicPurchaseBill(b, loc), r.UpdatedAt})
		}
	}
	// Stock: one stock.updated per product whose inventory rows changed.
	var stockProducts []uuid.UUID
	utils.DB.Model(&models.InventoryStock{}).Where("user_id = ? AND updated_at > ? AND updated_at <= ?", owner, from, to).
		Distinct("product_id").Pluck("product_id", &stockProducts)
	if len(stockProducts) > 0 {
		var products []models.Product
		utils.DB.Where("user_id = ? AND id IN ?", owner, stockProducts).Find(&products)
		ctx := &gin.Context{}
		ctx.Set("user_id", owner)
		for _, lvl := range buildStockLevels(ctx, products) {
			events = append(events, pending{"stock.updated", "stock_level", lvl.ProductID, lvl, to})
		}
	}

	var endpoints []models.WebhookEndpoint
	policy := utils.LoadIntegrationPolicy(utils.DB, store.ID)
	if policy.APIEnabled && policy.WebhooksEnabled {
		utils.DB.Where("store_id = ? AND is_active = ?", store.ID, true).Find(&endpoints)
	}
	allowedScopes := utils.ExpandScopes(utils.ParseScopes(policy.AllowedScopes))

	// Advance the cursor and write events + deliveries atomically; the
	// conditional update makes concurrent scanners (multiple instances)
	// skip a window another instance already claimed.
	_ = utils.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.IntegrationScanCursor{}).
			Where("store_id = ? AND scanned_to = ?", cur.StoreID, cur.ScannedTo).
			Updates(map[string]interface{}{"scanned_to": to, "updated_at": time.Now()})
		if res.Error != nil || res.RowsAffected != 1 {
			return errors.New("cursor moved")
		}
		now := time.Now()
		for _, e := range events {
			b, _ := json.Marshal(e.data)
			evt := models.IntegrationEvent{
				ID: uuid.New(), StoreID: store.ID, Type: e.typ, ObjectType: e.objType,
				ObjectID: e.id, Data: string(b), CreatedAt: e.at,
			}
			if err := tx.Create(&evt).Error; err != nil {
				return err
			}
			if !allowedScopes[eventScope(e.typ)] {
				continue
			}
			for _, ep := range endpoints {
				if !endpointWants(ep, e.typ) {
					continue
				}
				d := models.WebhookDelivery{
					EndpointID: ep.ID, EventID: evt.ID, EventType: evt.Type,
					Status: "pending", NextAttemptAt: &now,
				}
				if err := tx.Create(&d).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// ============================================================
// Delivery worker
// ============================================================

var webhookBackoff = []time.Duration{
	time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour,
}

const webhookDisableAfterFailures = 50

func deliverDueWebhooks() {
	now := time.Now()
	var due []models.WebhookDelivery
	utils.DB.Where("status = ? AND next_attempt_at <= ?", "pending", now).
		Order("next_attempt_at ASC").Limit(50).Find(&due)
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, d := range due {
		// Claim the delivery so another worker does not send it concurrently.
		lease := now.Add(2 * time.Minute)
		res := utils.DB.Model(&models.WebhookDelivery{}).
			Where("id = ? AND status = ? AND next_attempt_at = ?", d.ID, "pending", d.NextAttemptAt).
			Update("next_attempt_at", lease)
		if res.RowsAffected != 1 {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(d models.WebhookDelivery) {
			defer wg.Done()
			defer func() { <-sem }()
			attemptDelivery(d)
		}(d)
	}
	wg.Wait()
}

func attemptDelivery(d models.WebhookDelivery) {
	var ep models.WebhookEndpoint
	if err := utils.DB.First(&ep, "id = ?", d.EndpointID).Error; err != nil || !ep.IsActive {
		utils.DB.Model(&d).Updates(map[string]interface{}{"status": "failed", "last_error": "endpoint disabled or deleted", "next_attempt_at": nil})
		return
	}
	var evt models.IntegrationEvent
	if err := utils.DB.First(&evt, "id = ?", d.EventID).Error; err != nil {
		utils.DB.Model(&d).Updates(map[string]interface{}{"status": "failed", "last_error": "event expired", "next_attempt_at": nil})
		return
	}
	status, body, err := deliverWebhook(ep, evt)
	now := time.Now()
	updates := map[string]interface{}{"attempts": d.Attempts + 1, "response_status": status, "response_body": body}
	if err == nil {
		updates["status"] = "succeeded"
		updates["delivered_at"] = now
		updates["next_attempt_at"] = nil
		updates["last_error"] = ""
		utils.DB.Model(&d).Updates(updates)
		utils.DB.Model(&ep).Updates(map[string]interface{}{"failure_count": 0, "last_delivery_at": now})
		return
	}
	updates["last_error"] = err.Error()
	if d.Attempts < len(webhookBackoff) {
		next := now.Add(webhookBackoff[d.Attempts])
		updates["next_attempt_at"] = next
	} else {
		updates["status"] = "failed"
		updates["next_attempt_at"] = nil
	}
	utils.DB.Model(&d).Updates(updates)
	failures := ep.FailureCount + 1
	epUpdates := map[string]interface{}{"failure_count": failures, "last_delivery_at": now}
	if failures >= webhookDisableAfterFailures {
		epUpdates["is_active"] = false
		epUpdates["disabled_reason"] = fmt.Sprintf("Disabled automatically after %d consecutive failed deliveries", failures)
	}
	utils.DB.Model(&ep).Updates(epUpdates)
}

// webhookEnvelope is the JSON body POSTed to endpoints.
type webhookEnvelope struct {
	ID         uuid.UUID       `json:"id"`
	Object     string          `json:"object"`
	Type       string          `json:"type"`
	APIVersion string          `json:"api_version"`
	StoreID    uuid.UUID       `json:"store_id"`
	CreatedAt  string          `json:"created_at"`
	Data       json.RawMessage `json:"data"`
}

func deliverWebhook(ep models.WebhookEndpoint, evt models.IntegrationEvent) (int, string, error) {
	secret, err := utils.Decrypt(ep.EncryptedSecret)
	if err != nil {
		return 0, "", errors.New("cannot decrypt signing secret")
	}
	if err := validateWebhookURL(ep.URL); err != nil {
		return 0, "", err
	}
	pe := toPublicEvent(evt)
	body, _ := json.Marshal(webhookEnvelope{
		ID: evt.ID, Object: "event", Type: evt.Type, APIVersion: "2026-11-01",
		StoreID: evt.StoreID, CreatedAt: pe.CreatedAt, Data: pe.Data,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.URL, bytes.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	ts := time.Now().Unix()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "TruERP-Webhooks/1.0")
	req.Header.Set("TruERP-Event-Id", evt.ID.String())
	req.Header.Set("TruERP-Event-Type", evt.Type)
	req.Header.Set("TruERP-Signature", SignWebhookPayload(secret, ts, body))
	resp, err := webhookHTTPClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, string(snippet), fmt.Errorf("endpoint responded with HTTP %d", resp.StatusCode)
	}
	return resp.StatusCode, string(snippet), nil
}

func cleanupIntegrationData() {
	cutoff := time.Now().Add(-eventRetention)
	utils.DB.Where("created_at < ? AND status <> ?", cutoff, "pending").Delete(&models.WebhookDelivery{})
	utils.DB.Where("created_at < ?", cutoff).Delete(&models.IntegrationEvent{})
	utils.DB.Where("created_at < ?", cutoff).Delete(&models.APIRequestLog{})
	utils.DB.Where("created_at < ?", time.Now().Add(-idempotencyTTL)).Delete(&models.APIIdempotencyRecord{})
	utils.DB.Where("expires_at < ?", time.Now().Add(-24*time.Hour)).Delete(&models.OAuthCode{})
}
