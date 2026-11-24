package controllers

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ============================================================
// GET /me
// ============================================================

// PublicGetMe describes the calling credential, its store and its
// effective scopes — useful for "test connection" buttons.
func PublicGetMe(c *gin.Context) {
	storeID := publicStoreID(c)
	store, _ := utils.FindStoreByID(utils.DB, storeID)
	scopes := []string{}
	if v, ok := c.Get(utils.CtxPublicScopes); ok {
		for s, on := range v.(map[string]bool) {
			if on {
				scopes = append(scopes, s)
			}
		}
	}
	sort.Strings(scopes)
	policy, _ := c.Get(utils.CtxIntegrationPolicy)
	rate := 0
	if p, ok := policy.(models.IntegrationPolicy); ok {
		rate = p.RateLimitPerMinute
	}
	c.JSON(http.StatusOK, gin.H{
		"object": "credential",
		"credential": gin.H{
			"type": c.GetString(utils.CtxCredentialType),
			"id":   c.GetString(utils.CtxCredentialID),
			"name": c.GetString(utils.CtxCredentialName),
		},
		"store": gin.H{
			"id":         store.ID,
			"name":       store.Name,
			"code":       store.Code,
			"gstin":      store.GSTIN,
			"state_code": store.StateCode,
		},
		"scopes":                scopes,
		"rate_limit_per_minute": rate,
		"api_version":           "2026-11-01",
	})
}

// ============================================================
// Parties
// ============================================================

type publicPartyInput struct {
	Type           *string        `json:"type"`
	Name           *string        `json:"name"`
	Phone          *string        `json:"phone"`
	Email          *string        `json:"email"`
	GSTIN          *string        `json:"gstin"`
	PAN            *string        `json:"pan"`
	BillingAddress *PublicAddress `json:"billing_address"`
	Category       *string        `json:"category"`
	CreditLimit    *float64       `json:"credit_limit"`
	OpeningBalance *float64       `json:"opening_balance"`
	Notes          *string        `json:"notes"`
	IsActive       *bool          `json:"is_active"`
}

func (in publicPartyInput) applyTo(p *models.Party) {
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = strings.TrimSpace(*v)
		}
	}
	set(&p.PartyType, in.Type)
	set(&p.Name, in.Name)
	set(&p.Phone, in.Phone)
	set(&p.Email, in.Email)
	if in.GSTIN != nil {
		p.GSTIN = strings.ToUpper(strings.TrimSpace(*in.GSTIN))
	}
	if in.PAN != nil {
		p.PAN = strings.ToUpper(strings.TrimSpace(*in.PAN))
	}
	if a := in.BillingAddress; a != nil {
		p.Address, p.City, p.State, p.StateCode, p.Pincode = a.Line, a.City, a.State, a.StateCode, a.Pincode
	}
	set(&p.Category, in.Category)
	set(&p.Notes, in.Notes)
	if in.CreditLimit != nil {
		p.CreditLimit = *in.CreditLimit
	}
	if in.OpeningBalance != nil {
		p.OpeningBalance = *in.OpeningBalance
	}
	if in.IsActive != nil {
		p.IsActive = *in.IsActive
	}
}

func validatePartyFields(c *gin.Context, p models.Party) bool {
	if p.PartyType != "customer" && p.PartyType != "vendor" {
		paramError(c, "type", "type must be \"customer\" or \"vendor\"")
		return false
	}
	if strings.TrimSpace(p.Name) == "" {
		paramError(c, "name", "name is required")
		return false
	}
	if p.GSTIN != "" && len(p.GSTIN) != 15 {
		paramError(c, "gstin", "gstin must be 15 characters")
		return false
	}
	return true
}

func PublicListParties(c *gin.Context) {
	page, ok := readPageParams(c)
	if !ok {
		return
	}
	q := utils.DB.Model(&models.Party{}).Where("user_id = ?", scopeUserID(c))
	if t := c.Query("type"); t != "" {
		q = q.Where("party_type = ?", t)
	}
	if s := strings.TrimSpace(c.Query("q")); s != "" {
		like := "%" + strings.ToLower(s) + "%"
		q = q.Where("LOWER(name) LIKE ? OR phone LIKE ? OR LOWER(gstin) LIKE ? OR LOWER(email) LIKE ?", like, like, like, like)
	}
	if g := c.Query("gstin"); g != "" {
		q = q.Where("UPPER(gstin) = ?", strings.ToUpper(g))
	}
	if ph := c.Query("phone"); ph != "" {
		q = q.Where("phone = ?", ph)
	}
	var rows []models.Party
	if err := applyPage(q, "parties", page).Find(&rows).Error; err != nil {
		utils.PublicAPIError(c, http.StatusInternalServerError, "internal_error", "Failed to list parties")
		return
	}
	items := make([]interface{}, len(rows))
	for i, r := range rows {
		items[i] = toPublicParty(r)
	}
	writeList(c, items, page.limit, func(i int) (time.Time, uuid.UUID) { return rows[i].CreatedAt, rows[i].ID })
}

func loadParty(c *gin.Context, id uuid.UUID) (models.Party, bool) {
	var p models.Party
	if err := utils.DB.Where("user_id = ? AND id = ?", scopeUserID(c), id).First(&p).Error; err != nil {
		notFound(c, "party", id)
		return p, false
	}
	return p, true
}

func PublicGetParty(c *gin.Context) {
	id, ok := publicPathID(c, "id")
	if !ok {
		return
	}
	if p, ok := loadParty(c, id); ok {
		c.JSON(http.StatusOK, toPublicParty(p))
	}
}

func PublicCreateParty(c *gin.Context) {
	var in publicPartyInput
	if !bindPublicJSON(c, &in) {
		return
	}
	p := models.Party{IsActive: true}
	in.applyTo(&p)
	if !validatePartyFields(c, p) {
		return
	}
	r := callInternal(c, CreateParty, http.MethodPost, nil, p)
	if !r.ok() {
		relayInternalError(c, r)
		return
	}
	id, err := createdID(r)
	if err != nil {
		utils.PublicAPIError(c, http.StatusInternalServerError, "internal_error", "Party was not created")
		return
	}
	if created, ok := loadParty(c, id); ok {
		c.JSON(http.StatusCreated, toPublicParty(created))
	}
}

func PublicUpdateParty(c *gin.Context) {
	id, ok := publicPathID(c, "id")
	if !ok {
		return
	}
	var in publicPartyInput
	if !bindPublicJSON(c, &in) {
		return
	}
	current, ok := loadParty(c, id)
	if !ok {
		return
	}
	in.applyTo(&current)
	if !validatePartyFields(c, current) {
		return
	}
	// The internal update replaces every field, so send the merged record.
	r := callInternal(c, UpdateParty, http.MethodPut, gin.Params{{Key: "id", Value: id.String()}}, current)
	if !r.ok() {
		relayInternalError(c, r)
		return
	}
	if updated, ok := loadParty(c, id); ok {
		c.JSON(http.StatusOK, toPublicParty(updated))
	}
}

// ============================================================
// Products
// ============================================================

type publicOpeningStock struct {
	Quantity    float64    `json:"quantity"`
	CostPrice   float64    `json:"cost_price"`
	WarehouseID *uuid.UUID `json:"warehouse_id"`
	BatchNo     string     `json:"batch_no"`
	ExpiryDate  string     `json:"expiry_date"`
}

type publicProductInput struct {
	Type                     *string             `json:"type"`
	Name                     *string             `json:"name"`
	ItemCode                 *string             `json:"item_code"`
	Category                 *string             `json:"category"`
	Unit                     *string             `json:"unit"`
	HSNCode                  *string             `json:"hsn_code"`
	SalePrice                *float64            `json:"sale_price"`
	PurchasePrice            *float64            `json:"purchase_price"`
	MRP                      *float64            `json:"mrp"`
	TaxRate                  *float64            `json:"tax_rate"`
	GSTEnabled               *bool               `json:"gst_enabled"`
	SalePriceIncludesTax     *bool               `json:"sale_price_includes_tax"`
	PurchasePriceIncludesTax *bool               `json:"purchase_price_includes_tax"`
	MinStock                 *float64            `json:"min_stock"`
	TrackBatches             *bool               `json:"track_batches"`
	Description              *string             `json:"description"`
	IsActive                 *bool               `json:"is_active"`
	OpeningStock             *publicOpeningStock `json:"opening_stock"`
}

func (in publicProductInput) applyTo(p *models.Product) {
	str := func(dst *string, v *string) {
		if v != nil {
			*dst = strings.TrimSpace(*v)
		}
	}
	f := func(dst *float64, v *float64) {
		if v != nil {
			*dst = *v
		}
	}
	b := func(dst *bool, v *bool) {
		if v != nil {
			*dst = *v
		}
	}
	str(&p.ItemType, in.Type)
	str(&p.Name, in.Name)
	str(&p.ItemCode, in.ItemCode)
	str(&p.Category, in.Category)
	str(&p.Unit, in.Unit)
	str(&p.HSNCode, in.HSNCode)
	str(&p.Description, in.Description)
	f(&p.SalePrice, in.SalePrice)
	f(&p.PurchasePrice, in.PurchasePrice)
	f(&p.MRP, in.MRP)
	f(&p.TaxRate, in.TaxRate)
	f(&p.MinStock, in.MinStock)
	b(&p.GstEnabled, in.GSTEnabled)
	b(&p.SalePriceWithTax, in.SalePriceIncludesTax)
	b(&p.PurchasePriceWithTax, in.PurchasePriceIncludesTax)
	b(&p.EnableBatching, in.TrackBatches)
	b(&p.IsActive, in.IsActive)
}

func validateProductFields(c *gin.Context, p models.Product) bool {
	if strings.TrimSpace(p.Name) == "" {
		paramError(c, "name", "name is required")
		return false
	}
	if p.ItemType != "product" && p.ItemType != "service" {
		paramError(c, "type", "type must be \"product\" or \"service\"")
		return false
	}
	if p.SalePrice < 0 || p.PurchasePrice < 0 || p.MRP < 0 {
		paramError(c, "sale_price", "prices cannot be negative")
		return false
	}
	if p.TaxRate < 0 || p.TaxRate > 100 {
		paramError(c, "tax_rate", "tax_rate must be between 0 and 100")
		return false
	}
	return true
}

func PublicListProducts(c *gin.Context) {
	page, ok := readPageParams(c)
	if !ok {
		return
	}
	q := utils.DB.Model(&models.Product{}).Where("user_id = ?", scopeUserID(c))
	if s := strings.TrimSpace(c.Query("q")); s != "" {
		like := "%" + strings.ToLower(s) + "%"
		q = q.Where("LOWER(name) LIKE ? OR LOWER(sku) LIKE ? OR LOWER(item_code) LIKE ?", like, like, like)
	}
	if v := c.Query("sku"); v != "" {
		q = q.Where("sku = ?", v)
	}
	if v := c.Query("item_code"); v != "" {
		q = q.Where("TRIM(item_code) = ?", strings.TrimSpace(v))
	}
	if v := c.Query("category"); v != "" {
		q = q.Where("category = ?", v)
	}
	if v := c.Query("type"); v != "" {
		q = q.Where("item_type = ?", v)
	}
	var rows []models.Product
	if err := applyPage(q, "products", page).Find(&rows).Error; err != nil {
		utils.PublicAPIError(c, http.StatusInternalServerError, "internal_error", "Failed to list products")
		return
	}
	items := make([]interface{}, len(rows))
	for i, r := range rows {
		items[i] = toPublicProduct(r)
	}
	writeList(c, items, page.limit, func(i int) (time.Time, uuid.UUID) { return rows[i].CreatedAt, rows[i].ID })
}

func loadProduct(c *gin.Context, id uuid.UUID) (models.Product, bool) {
	var p models.Product
	if err := utils.DB.Where("user_id = ? AND id = ?", scopeUserID(c), id).First(&p).Error; err != nil {
		notFound(c, "product", id)
		return p, false
	}
	return p, true
}

func PublicGetProduct(c *gin.Context) {
	id, ok := publicPathID(c, "id")
	if !ok {
		return
	}
	if p, ok := loadProduct(c, id); ok {
		c.JSON(http.StatusOK, toPublicProduct(p))
	}
}

type internalProductBody struct {
	models.Product
	Inventory map[string]interface{} `json:"inventory,omitempty"`
}

func PublicCreateProduct(c *gin.Context) {
	var in publicProductInput
	if !bindPublicJSON(c, &in) {
		return
	}
	p := models.Product{ItemType: "product", Unit: "PCS", IsActive: true, LowStockAlert: true, SalePriceWithTax: true, PurchasePriceWithTax: true}
	in.applyTo(&p)
	if !validateProductFields(c, p) {
		return
	}
	body := internalProductBody{Product: p}
	if os := in.OpeningStock; os != nil && os.Quantity != 0 {
		inv := map[string]interface{}{"quantity": os.Quantity, "cost_price": os.CostPrice, "batch_no": os.BatchNo}
		if os.WarehouseID != nil {
			inv["outlet_id"] = os.WarehouseID.String()
		}
		if os.ExpiryDate != "" {
			d, err := parsePublicDate(os.ExpiryDate)
			if err != nil {
				paramError(c, "opening_stock.expiry_date", err.Error())
				return
			}
			inv["exp_date"] = d.Format("2006-01-02")
		}
		body.Inventory = inv
	}
	r := callInternal(c, CreateProduct, http.MethodPost, nil, body)
	if !r.ok() {
		relayInternalError(c, r)
		return
	}
	id, err := createdID(r)
	if err != nil {
		utils.PublicAPIError(c, http.StatusInternalServerError, "internal_error", "Product was not created")
		return
	}
	if created, ok := loadProduct(c, id); ok {
		c.JSON(http.StatusCreated, toPublicProduct(created))
	}
}

func PublicUpdateProduct(c *gin.Context) {
	id, ok := publicPathID(c, "id")
	if !ok {
		return
	}
	var in publicProductInput
	if !bindPublicJSON(c, &in) {
		return
	}
	if in.OpeningStock != nil {
		paramError(c, "opening_stock", "opening_stock can only be set when creating a product")
		return
	}
	current, ok := loadProduct(c, id)
	if !ok {
		return
	}
	in.applyTo(&current)
	if !validateProductFields(c, current) {
		return
	}
	r := callInternal(c, UpdateProduct, http.MethodPut, gin.Params{{Key: "id", Value: id.String()}}, internalProductBody{Product: current})
	if !r.ok() {
		relayInternalError(c, r)
		return
	}
	if updated, ok := loadProduct(c, id); ok {
		c.JSON(http.StatusOK, toPublicProduct(updated))
	}
}

// ============================================================
// Stock
// ============================================================

func buildStockLevels(c *gin.Context, products []models.Product) []PublicStockLevel {
	loc := publicLocation(c)
	ids := make([]uuid.UUID, len(products))
	for i, p := range products {
		ids[i] = p.ID
	}
	var stocks []models.InventoryStock
	if len(ids) > 0 {
		utils.DB.Where("user_id = ? AND product_id IN ?", scopeUserID(c), ids).Find(&stocks)
	}
	outletIDs := []uuid.UUID{}
	for _, s := range stocks {
		outletIDs = append(outletIDs, s.OutletID)
	}
	names := fetchWarehouseNames(outletIDs)
	byProduct := map[uuid.UUID][]models.InventoryStock{}
	for _, s := range stocks {
		byProduct[s.ProductID] = append(byProduct[s.ProductID], s)
	}
	out := make([]PublicStockLevel, 0, len(products))
	for _, p := range products {
		lvl := PublicStockLevel{
			Object: "stock_level", ProductID: p.ID, ProductName: p.Name, SKU: p.SKU, Unit: p.Unit,
			MinStock: p.MinStock, Locations: []PublicStockLocation{},
		}
		var latest time.Time
		for _, s := range byProduct[p.ID] {
			available := s.Quantity - s.ReservedQty
			lvl.Quantity += s.Quantity
			lvl.Reserved += s.ReservedQty
			lvl.Value += s.Quantity * s.AverageCost
			lvl.Locations = append(lvl.Locations, PublicStockLocation{
				WarehouseID: s.OutletID, WarehouseName: names[s.OutletID], BatchNo: s.BatchNo,
				ExpiryDate: publicDatePtr(s.ExpDate, loc), Quantity: s.Quantity, Reserved: s.ReservedQty,
				Available: available, AverageCost: round2(s.AverageCost),
			})
			if s.UpdatedAt.After(latest) {
				latest = s.UpdatedAt
			}
		}
		lvl.Available = lvl.Quantity - lvl.Reserved
		lvl.Value = round2(lvl.Value)
		lvl.LowStock = p.MinStock > 0 && lvl.Quantity <= p.MinStock
		if !latest.IsZero() {
			ts := publicTimestamp(latest)
			lvl.UpdatedAt = &ts
		}
		out = append(out, lvl)
	}
	return out
}

func PublicListStock(c *gin.Context) {
	page, ok := readPageParams(c)
	if !ok {
		return
	}
	q := utils.DB.Model(&models.Product{}).Where("user_id = ? AND (item_type IS NULL OR item_type <> ?)", scopeUserID(c), "service")
	if v := c.Query("product_id"); v != "" {
		pid, err := uuid.Parse(v)
		if err != nil {
			paramError(c, "product_id", "product_id must be a UUID")
			return
		}
		q = q.Where("id = ?", pid)
	}
	if v := c.Query("warehouse_id"); v != "" {
		wid, err := uuid.Parse(v)
		if err != nil {
			paramError(c, "warehouse_id", "warehouse_id must be a UUID")
			return
		}
		q = q.Where("id IN (SELECT product_id FROM inventory_stocks WHERE user_id = ? AND outlet_id = ?)", scopeUserID(c), wid)
	}
	if c.Query("low_stock") == "true" {
		q = q.Where("min_stock > 0 AND min_stock >= COALESCE((SELECT SUM(quantity) FROM inventory_stocks s WHERE s.product_id = products.id AND s.user_id = products.user_id), 0)")
	}
	// Stock changes do not touch products.updated_at, so updated_since
	// filters on the latest inventory change instead.
	if page.updatedSince != nil {
		q = q.Where("id IN (SELECT product_id FROM inventory_stocks WHERE user_id = ? AND updated_at >= ?)", scopeUserID(c), *page.updatedSince)
		page.updatedSince = nil
	}
	var rows []models.Product
	if err := applyPage(q, "products", page).Find(&rows).Error; err != nil {
		utils.PublicAPIError(c, http.StatusInternalServerError, "internal_error", "Failed to list stock")
		return
	}
	levels := buildStockLevels(c, rows)
	items := make([]interface{}, len(levels))
	for i, l := range levels {
		items[i] = l
	}
	writeList(c, items, page.limit, func(i int) (time.Time, uuid.UUID) { return rows[i].CreatedAt, rows[i].ID })
}

func PublicGetProductStock(c *gin.Context) {
	id, ok := publicPathID(c, "id")
	if !ok {
		return
	}
	p, ok := loadProduct(c, id)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, buildStockLevels(c, []models.Product{p})[0])
}

// ============================================================
// Invoices
// ============================================================

type publicInvoiceLineInput struct {
	ProductID       *uuid.UUID `json:"product_id"`
	Description     string     `json:"description"`
	Quantity        float64    `json:"quantity"`
	Unit            string     `json:"unit"`
	UnitPrice       *float64   `json:"unit_price"`
	DiscountPercent float64    `json:"discount_percent"`
	TaxRate         *float64   `json:"tax_rate"`
	CessRate        float64    `json:"cess_rate"`
	HSNCode         string     `json:"hsn_code"`
	BatchNo         string     `json:"batch_no"`
	ExpiryDate      string     `json:"expiry_date"`
}

type publicInvoiceInput struct {
	PartyID               uuid.UUID                `json:"party_id"`
	Number                string                   `json:"number"`
	Type                  string                   `json:"type"`
	Status                string                   `json:"status"`
	Date                  string                   `json:"date"`
	DueDate               string                   `json:"due_date"`
	PaymentTermsDays      int                      `json:"payment_terms_days"`
	LineItems             []publicInvoiceLineInput `json:"line_items"`
	InvoiceDiscount       float64                  `json:"invoice_discount"`
	AdditionalChargeItems []PublicCharge           `json:"additional_charge_items"`
	AmountPaid            float64                  `json:"amount_paid"`
	PaymentMode           string                   `json:"payment_mode"`
	BankAccountID         *uuid.UUID               `json:"bank_account_id"`
	PlaceOfSupply         string                   `json:"place_of_supply"`
	IsInterState          bool                     `json:"is_inter_state"`
	ReverseCharge         bool                     `json:"reverse_charge"`
	ShippingAddress       string                   `json:"shipping_address"`
	Notes                 string                   `json:"notes"`
	Terms                 string                   `json:"terms"`
	CustomFields          map[string]interface{}   `json:"custom_fields"`
}

var publicInvoiceCreateStatuses = map[string]bool{"draft": true, "sent": true, "paid": true, "partial": true}

func loadInvoice(c *gin.Context, id uuid.UUID) (models.Invoice, bool) {
	var inv models.Invoice
	if err := utils.DB.Where("user_id = ? AND id = ?", scopeUserID(c), id).
		Preload("Party").Preload("Items").First(&inv).Error; err != nil {
		notFound(c, "invoice", id)
		return inv, false
	}
	return inv, true
}

func PublicListInvoices(c *gin.Context) {
	page, ok := readPageParams(c)
	if !ok {
		return
	}
	q := utils.DB.Model(&models.Invoice{}).Where("user_id = ?", scopeUserID(c))
	if v := c.Query("party_id"); v != "" {
		pid, err := uuid.Parse(v)
		if err != nil {
			paramError(c, "party_id", "party_id must be a UUID")
			return
		}
		q = q.Where("party_id = ?", pid)
	}
	if v := c.Query("status"); v != "" {
		q = q.Where("status IN ?", strings.Split(v, ","))
	}
	if v := c.Query("number"); v != "" {
		q = q.Where("invoice_number = ?", v)
	}
	if !applyDateRange(c, &q, "date") {
		return
	}
	var rows []models.Invoice
	if err := applyPage(q, "invoices", page).Preload("Party").Preload("Items").Find(&rows).Error; err != nil {
		utils.PublicAPIError(c, http.StatusInternalServerError, "internal_error", "Failed to list invoices")
		return
	}
	loc := publicLocation(c)
	items := make([]interface{}, len(rows))
	for i, r := range rows {
		items[i] = toPublicInvoice(r, loc)
	}
	writeList(c, items, page.limit, func(i int) (time.Time, uuid.UUID) { return rows[i].CreatedAt, rows[i].ID })
}

// applyDateRange applies date_from / date_to as inclusive calendar days in
// the store's timezone.
func applyDateRange(c *gin.Context, q **gorm.DB, column string) bool {
	loc := publicLocation(c)
	// Boundaries are expressed in UTC so they compare correctly with the
	// UTC-midnight document dates the apps store, on Postgres and SQLite.
	dayStart := func(d time.Time) time.Time {
		return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc).UTC()
	}
	if v := c.Query("date_from"); v != "" {
		d, err := parsePublicDate(v)
		if err != nil {
			paramError(c, "date_from", err.Error())
			return false
		}
		*q = (*q).Where(column+" >= ?", dayStart(d))
	}
	if v := c.Query("date_to"); v != "" {
		d, err := parsePublicDate(v)
		if err != nil {
			paramError(c, "date_to", err.Error())
			return false
		}
		*q = (*q).Where(column+" < ?", dayStart(d).AddDate(0, 0, 1))
	}
	return true
}

func PublicGetInvoice(c *gin.Context) {
	id, ok := publicPathID(c, "id")
	if !ok {
		return
	}
	if inv, ok := loadInvoice(c, id); ok {
		c.JSON(http.StatusOK, toPublicInvoice(inv, publicLocation(c)))
	}
}

func PublicCreateInvoice(c *gin.Context) {
	var in publicInvoiceInput
	if !bindPublicJSON(c, &in) {
		return
	}
	if in.PartyID == uuid.Nil {
		paramError(c, "party_id", "party_id is required")
		return
	}
	var party models.Party
	if err := utils.DB.Where("user_id = ? AND id = ?", scopeUserID(c), in.PartyID).First(&party).Error; err != nil {
		paramError(c, "party_id", "party_id does not refer to a party in this store")
		return
	}
	if len(in.LineItems) == 0 {
		paramError(c, "line_items", "at least one line item is required")
		return
	}
	if len(in.LineItems) > 500 {
		paramError(c, "line_items", "at most 500 line items are allowed")
		return
	}
	if in.Status == "" {
		in.Status = "sent"
	}
	if !publicInvoiceCreateStatuses[in.Status] {
		paramError(c, "status", "status must be one of draft, sent, partial, paid")
		return
	}
	date := time.Now().In(publicLocation(c))
	date = time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
	if in.Date != "" {
		d, err := parsePublicDate(in.Date)
		if err != nil {
			paramError(c, "date", err.Error())
			return
		}
		date = d
	}
	var dueDate *time.Time
	if in.DueDate != "" {
		d, err := parsePublicDate(in.DueDate)
		if err != nil {
			paramError(c, "due_date", err.Error())
			return
		}
		dueDate = &d
	} else if in.PaymentTermsDays > 0 {
		d := date.AddDate(0, 0, in.PaymentTermsDays)
		dueDate = &d
	}

	productIDs := []uuid.UUID{}
	for _, l := range in.LineItems {
		if l.ProductID != nil {
			productIDs = append(productIDs, *l.ProductID)
		}
	}
	products := map[uuid.UUID]models.Product{}
	if len(productIDs) > 0 {
		var rows []models.Product
		utils.DB.Where("user_id = ? AND id IN ?", scopeUserID(c), productIDs).Find(&rows)
		for _, p := range rows {
			products[p.ID] = p
		}
	}

	items := make([]map[string]interface{}, 0, len(in.LineItems))
	for i, l := range in.LineItems {
		param := fmt.Sprintf("line_items[%d]", i)
		if l.Quantity <= 0 {
			paramError(c, param+".quantity", "quantity must be greater than 0")
			return
		}
		if l.DiscountPercent < 0 || l.DiscountPercent > 100 {
			paramError(c, param+".discount_percent", "discount_percent must be between 0 and 100")
			return
		}
		item := map[string]interface{}{
			"quantity": l.Quantity, "discount": l.DiscountPercent, "cess_rate": l.CessRate,
			"description": l.Description, "unit": l.Unit, "hsn_code": l.HSNCode, "batch_no": l.BatchNo,
		}
		if l.ProductID != nil {
			p, ok := products[*l.ProductID]
			if !ok {
				paramError(c, param+".product_id", "product_id does not refer to a product in this store")
				return
			}
			item["product_id"] = p.ID
			if l.Description == "" {
				item["description"] = p.Name
			}
			if l.Unit == "" {
				item["unit"] = p.Unit
			}
			if l.HSNCode == "" {
				item["hsn_code"] = p.HSNCode
			}
			if l.UnitPrice == nil {
				item["unit_price"] = p.SalePrice
			}
			if l.TaxRate == nil {
				item["tax_rate"] = p.TaxRate
			}
		} else if strings.TrimSpace(l.Description) == "" {
			paramError(c, param+".description", "description is required when product_id is not given")
			return
		}
		if l.UnitPrice != nil {
			if *l.UnitPrice < 0 {
				paramError(c, param+".unit_price", "unit_price cannot be negative")
				return
			}
			item["unit_price"] = *l.UnitPrice
		} else if l.ProductID == nil {
			paramError(c, param+".unit_price", "unit_price is required when product_id is not given")
			return
		}
		if l.TaxRate != nil {
			item["tax_rate"] = *l.TaxRate
		}
		if l.ExpiryDate != "" {
			d, err := parsePublicDate(l.ExpiryDate)
			if err != nil {
				paramError(c, param+".expiry_date", err.Error())
				return
			}
			item["exp_date"] = d
		}
		items = append(items, item)
	}

	charges := make([]models.AdditionalCharge, 0, len(in.AdditionalChargeItems))
	for _, ch := range in.AdditionalChargeItems {
		charges = append(charges, models.AdditionalCharge{Label: ch.Label, Amount: ch.Amount})
	}
	body := map[string]interface{}{
		"invoice_number":          strings.TrimSpace(in.Number),
		"invoice_type":            in.Type,
		"party_id":                in.PartyID,
		"date":                    date,
		"due_date":                dueDate,
		"payment_terms":           in.PaymentTermsDays,
		"status":                  in.Status,
		"payment_mode":            in.PaymentMode,
		"amount_paid":             in.AmountPaid,
		"bank_account_id":         in.BankAccountID,
		"notes":                   in.Notes,
		"terms":                   in.Terms,
		"is_inter_state":          in.IsInterState,
		"place_of_supply":         in.PlaceOfSupply,
		"reverse_charge":          in.ReverseCharge,
		"shipping_address":        in.ShippingAddress,
		"invoice_discount":        in.InvoiceDiscount,
		"additional_charge_items": charges,
		"custom_fields":           in.CustomFields,
		"items":                   items,
	}
	if v, ok := c.Get("public_idempotency_uuid"); ok {
		body["client_sale_id"] = v
	}
	r := callInternal(c, CreateInvoice, http.MethodPost, nil, body)
	if !r.ok() {
		relayInternalError(c, r)
		return
	}
	id, err := createdID(r)
	if err != nil {
		utils.PublicAPIError(c, http.StatusInternalServerError, "internal_error", "Invoice was not created")
		return
	}
	if inv, ok := loadInvoice(c, id); ok {
		c.JSON(http.StatusCreated, toPublicInvoice(inv, publicLocation(c)))
	}
}

func PublicCancelInvoice(c *gin.Context) {
	id, ok := publicPathID(c, "id")
	if !ok {
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if !bindPublicJSON(c, &in) {
		return
	}
	if strings.TrimSpace(in.Reason) == "" {
		paramError(c, "reason", "reason is required")
		return
	}
	inv, ok := loadInvoice(c, id)
	if !ok {
		return
	}
	if inv.Status == "cancelled" {
		c.JSON(http.StatusOK, toPublicInvoice(inv, publicLocation(c)))
		return
	}
	r := callInternal(c, UpdateInvoiceStatus, http.MethodPatch, gin.Params{{Key: "id", Value: id.String()}},
		gin.H{"status": "cancelled", "note": strings.TrimSpace(in.Reason)})
	if !r.ok() {
		relayInternalError(c, r)
		return
	}
	if inv, ok := loadInvoice(c, id); ok {
		c.JSON(http.StatusOK, toPublicInvoice(inv, publicLocation(c)))
	}
}

// PublicGetInvoicePDF streams the invoice PDF rendered by the internal
// generator.
func PublicGetInvoicePDF(c *gin.Context) {
	id, ok := publicPathID(c, "id")
	if !ok {
		return
	}
	if _, ok := loadInvoice(c, id); !ok {
		return
	}
	r := callInternal(c, GenerateInvoicePDF, http.MethodGet, gin.Params{{Key: "id", Value: id.String()}}, nil)
	if !r.ok() {
		relayInternalError(c, r)
		return
	}
	c.Data(http.StatusOK, "application/pdf", r.Body)
}

// ============================================================
// Payments (payment in)
// ============================================================

type publicPaymentInput struct {
	PartyID       uuid.UUID  `json:"party_id"`
	InvoiceID     *uuid.UUID `json:"invoice_id"`
	Amount        float64    `json:"amount"`
	Discount      float64    `json:"discount"`
	Mode          string     `json:"mode"`
	BankAccountID *uuid.UUID `json:"bank_account_id"`
	Date          string     `json:"date"`
	Number        string     `json:"number"`
	Reference     string     `json:"reference"`
	Notes         string     `json:"notes"`
}

var publicPaymentModes = map[string]bool{"cash": true, "upi": true, "bank_transfer": true, "cheque": true, "card": true}

func loadPayment(c *gin.Context, id uuid.UUID) (models.Payment, bool) {
	var p models.Payment
	if err := utils.DB.Where("user_id = ? AND id = ?", scopeUserID(c), id).Preload("Party").First(&p).Error; err != nil {
		notFound(c, "payment", id)
		return p, false
	}
	return p, true
}

func PublicListPayments(c *gin.Context) {
	page, ok := readPageParams(c)
	if !ok {
		return
	}
	q := utils.DB.Model(&models.Payment{}).Where("user_id = ?", scopeUserID(c))
	if v := c.Query("party_id"); v != "" {
		pid, err := uuid.Parse(v)
		if err != nil {
			paramError(c, "party_id", "party_id must be a UUID")
			return
		}
		q = q.Where("party_id = ?", pid)
	}
	if v := c.Query("invoice_id"); v != "" {
		iid, err := uuid.Parse(v)
		if err != nil {
			paramError(c, "invoice_id", "invoice_id must be a UUID")
			return
		}
		q = q.Where("invoice_id = ?", iid)
	}
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	if !applyDateRange(c, &q, "date") {
		return
	}
	var rows []models.Payment
	if err := applyPage(q, "payments", page).Preload("Party").Find(&rows).Error; err != nil {
		utils.PublicAPIError(c, http.StatusInternalServerError, "internal_error", "Failed to list payments")
		return
	}
	loc := publicLocation(c)
	items := make([]interface{}, len(rows))
	for i, r := range rows {
		items[i] = toPublicPayment(r, loc)
	}
	writeList(c, items, page.limit, func(i int) (time.Time, uuid.UUID) { return rows[i].CreatedAt, rows[i].ID })
}

func PublicGetPayment(c *gin.Context) {
	id, ok := publicPathID(c, "id")
	if !ok {
		return
	}
	if p, ok := loadPayment(c, id); ok {
		c.JSON(http.StatusOK, toPublicPayment(p, publicLocation(c)))
	}
}

func PublicCreatePayment(c *gin.Context) {
	var in publicPaymentInput
	if !bindPublicJSON(c, &in) {
		return
	}
	if in.PartyID == uuid.Nil {
		paramError(c, "party_id", "party_id is required")
		return
	}
	if in.Amount <= 0 {
		paramError(c, "amount", "amount must be greater than 0")
		return
	}
	if in.Discount < 0 || in.Discount >= in.Amount {
		paramError(c, "discount", "discount must be at least 0 and less than amount")
		return
	}
	if in.Mode == "" {
		in.Mode = "cash"
	}
	if !publicPaymentModes[in.Mode] {
		paramError(c, "mode", "mode must be one of cash, upi, bank_transfer, cheque, card")
		return
	}
	if in.InvoiceID != nil {
		var inv models.Invoice
		if err := utils.DB.Select("id", "party_id").Where("user_id = ? AND id = ?", scopeUserID(c), *in.InvoiceID).First(&inv).Error; err != nil {
			paramError(c, "invoice_id", "invoice_id does not refer to an invoice in this store")
			return
		}
		if inv.PartyID != in.PartyID {
			paramError(c, "invoice_id", "the invoice belongs to a different party")
			return
		}
	}
	date := time.Now().In(publicLocation(c))
	date = time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
	if in.Date != "" {
		d, err := parsePublicDate(in.Date)
		if err != nil {
			paramError(c, "date", err.Error())
			return
		}
		date = d
	}
	body := gin.H{
		"invoice_id": in.InvoiceID, "party_id": in.PartyID, "amount_received": in.Amount,
		"payment_in_discount": in.Discount, "payment_in_number": strings.TrimSpace(in.Number),
		"mode": in.Mode, "bank_account_id": in.BankAccountID, "date": date,
		"reference": in.Reference, "notes": in.Notes,
	}
	r := callInternal(c, CreatePayment, http.MethodPost, nil, body)
	if !r.ok() {
		relayInternalError(c, r)
		return
	}
	id, err := createdID(r)
	if err != nil {
		utils.PublicAPIError(c, http.StatusInternalServerError, "internal_error", "Payment was not recorded")
		return
	}
	if p, ok := loadPayment(c, id); ok {
		c.JSON(http.StatusCreated, toPublicPayment(p, publicLocation(c)))
	}
}

// ============================================================
// Purchase bills
// ============================================================

type publicPurchaseLineInput struct {
	ProductID       *uuid.UUID `json:"product_id"`
	ItemCode        string     `json:"item_code"`
	Description     string     `json:"description"`
	Quantity        float64    `json:"quantity"`
	Unit            string     `json:"unit"`
	UnitPrice       float64    `json:"unit_price"`
	DiscountPercent float64    `json:"discount_percent"`
	TaxRate         float64    `json:"tax_rate"`
	MRP             float64    `json:"mrp"`
	SalePrice       float64    `json:"sale_price"`
	HSNCode         string     `json:"hsn_code"`
	BatchNo         string     `json:"batch_no"`
	MfgDate         string     `json:"mfg_date"`
	ExpiryDate      string     `json:"expiry_date"`
}

type publicPurchaseInput struct {
	PartyID               uuid.UUID                 `json:"party_id"`
	Number                string                    `json:"number"`
	Date                  string                    `json:"date"`
	DueDate               string                    `json:"due_date"`
	WarehouseID           *uuid.UUID                `json:"warehouse_id"`
	TaxExempt             bool                      `json:"tax_exempt"`
	LineItems             []publicPurchaseLineInput `json:"line_items"`
	InvoiceDiscount       float64                   `json:"invoice_discount"`
	AdditionalChargeItems []PublicCharge            `json:"additional_charge_items"`
	AmountPaid            float64                   `json:"amount_paid"`
	PaymentMode           string                    `json:"payment_mode"`
	BankAccountID         *uuid.UUID                `json:"bank_account_id"`
	Notes                 string                    `json:"notes"`
}

func loadPurchaseBill(c *gin.Context, id uuid.UUID) (models.PurchaseBill, bool) {
	var b models.PurchaseBill
	if err := utils.DB.Where("user_id = ? AND id = ?", scopeUserID(c), id).Preload("Party").Preload("Items").First(&b).Error; err != nil {
		notFound(c, "purchase bill", id)
		return b, false
	}
	return b, true
}

func PublicListPurchaseBills(c *gin.Context) {
	page, ok := readPageParams(c)
	if !ok {
		return
	}
	q := utils.DB.Model(&models.PurchaseBill{}).Where("user_id = ?", scopeUserID(c))
	if v := c.Query("party_id"); v != "" {
		pid, err := uuid.Parse(v)
		if err != nil {
			paramError(c, "party_id", "party_id must be a UUID")
			return
		}
		q = q.Where("party_id = ?", pid)
	}
	if v := c.Query("status"); v != "" {
		q = q.Where("status IN ?", strings.Split(v, ","))
	}
	if !applyDateRange(c, &q, "bill_date") {
		return
	}
	var rows []models.PurchaseBill
	if err := applyPage(q, "purchase_bills", page).Preload("Party").Preload("Items").Find(&rows).Error; err != nil {
		utils.PublicAPIError(c, http.StatusInternalServerError, "internal_error", "Failed to list purchase bills")
		return
	}
	loc := publicLocation(c)
	items := make([]interface{}, len(rows))
	for i, r := range rows {
		items[i] = toPublicPurchaseBill(r, loc)
	}
	writeList(c, items, page.limit, func(i int) (time.Time, uuid.UUID) { return rows[i].CreatedAt, rows[i].ID })
}

func PublicGetPurchaseBill(c *gin.Context) {
	id, ok := publicPathID(c, "id")
	if !ok {
		return
	}
	if b, ok := loadPurchaseBill(c, id); ok {
		c.JSON(http.StatusOK, toPublicPurchaseBill(b, publicLocation(c)))
	}
}

func PublicCreatePurchaseBill(c *gin.Context) {
	var in publicPurchaseInput
	if !bindPublicJSON(c, &in) {
		return
	}
	if in.PartyID == uuid.Nil {
		paramError(c, "party_id", "party_id is required")
		return
	}
	var party models.Party
	if err := utils.DB.Where("user_id = ? AND id = ?", scopeUserID(c), in.PartyID).First(&party).Error; err != nil {
		paramError(c, "party_id", "party_id does not refer to a party in this store")
		return
	}
	if strings.TrimSpace(in.Number) == "" {
		paramError(c, "number", "number (the supplier's bill number) is required")
		return
	}
	if len(in.LineItems) == 0 || len(in.LineItems) > 500 {
		paramError(c, "line_items", "between 1 and 500 line items are required")
		return
	}
	date := time.Now().In(publicLocation(c))
	date = time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
	if in.Date != "" {
		d, err := parsePublicDate(in.Date)
		if err != nil {
			paramError(c, "date", err.Error())
			return
		}
		date = d
	}
	var dueDate *time.Time
	if in.DueDate != "" {
		d, err := parsePublicDate(in.DueDate)
		if err != nil {
			paramError(c, "due_date", err.Error())
			return
		}
		dueDate = &d
	}

	productIDs := []uuid.UUID{}
	for _, l := range in.LineItems {
		if l.ProductID != nil {
			productIDs = append(productIDs, *l.ProductID)
		}
	}
	known := map[uuid.UUID]models.Product{}
	if len(productIDs) > 0 {
		var rows []models.Product
		utils.DB.Where("user_id = ? AND id IN ?", scopeUserID(c), productIDs).Find(&rows)
		for _, p := range rows {
			known[p.ID] = p
		}
	}

	var lineSum float64
	items := make([]map[string]interface{}, 0, len(in.LineItems))
	for i, l := range in.LineItems {
		param := fmt.Sprintf("line_items[%d]", i)
		if l.Quantity <= 0 {
			paramError(c, param+".quantity", "quantity must be greater than 0")
			return
		}
		if l.UnitPrice < 0 {
			paramError(c, param+".unit_price", "unit_price cannot be negative")
			return
		}
		desc := strings.TrimSpace(l.Description)
		if l.ProductID != nil {
			p, ok := known[*l.ProductID]
			if !ok {
				paramError(c, param+".product_id", "product_id does not refer to a product in this store")
				return
			}
			if desc == "" {
				desc = p.Name
			}
			if l.Unit == "" {
				l.Unit = p.Unit
			}
			if l.HSNCode == "" {
				l.HSNCode = p.HSNCode
			}
		}
		if desc == "" {
			paramError(c, param+".description", "description is required when product_id is not given")
			return
		}
		item := map[string]interface{}{
			"product_id": l.ProductID, "item_code": l.ItemCode, "description": desc,
			"quantity": l.Quantity, "unit": l.Unit, "unit_price": l.UnitPrice, "discount": l.DiscountPercent,
			"tax_rate": l.TaxRate, "mrp": l.MRP, "sale_price": l.SalePrice, "hsn_code": l.HSNCode,
			"batch_no": l.BatchNo, "is_new_item": l.ProductID == nil,
		}
		if l.ProductID == nil {
			ref := uuid.NewSHA1(in.PartyID, []byte(fmt.Sprintf("%s|%d|%s", in.Number, i, desc))).String()
			item["client_item_ref"] = ref
		}
		for name, raw := range map[string]string{"mfg_date": l.MfgDate, "exp_date": l.ExpiryDate} {
			if raw == "" {
				continue
			}
			d, err := parsePublicDate(raw)
			if err != nil {
				paramError(c, param+"."+name, err.Error())
				return
			}
			item[name] = d
		}
		// Same arithmetic the internal handler uses for each line.
		rate := l.TaxRate
		if in.TaxExempt {
			rate = 0
		}
		gross := l.UnitPrice * l.Quantity
		taxable := gross - gross*(l.DiscountPercent/100)
		lineSum += taxable + taxable*(rate/100)
		items = append(items, item)
	}
	charges := make([]models.AdditionalCharge, 0, len(in.AdditionalChargeItems))
	var chargeSum float64
	for _, ch := range in.AdditionalChargeItems {
		charges = append(charges, models.AdditionalCharge{Label: ch.Label, Amount: ch.Amount})
		chargeSum += ch.Amount
	}
	total := round2(lineSum - in.InvoiceDiscount + chargeSum)
	if total < 0 {
		paramError(c, "invoice_discount", "invoice_discount exceeds the bill total")
		return
	}
	if in.AmountPaid < 0 || in.AmountPaid > total {
		paramError(c, "amount_paid", "amount_paid must be between 0 and the bill total")
		return
	}
	status := "unpaid"
	switch {
	case in.AmountPaid >= total && total > 0:
		status = "paid"
	case in.AmountPaid > 0:
		status = "partial"
	}
	body := gin.H{
		"party_id": in.PartyID, "bill_number": strings.TrimSpace(in.Number), "bill_date": date,
		"due_date": dueDate, "warehouse_id": in.WarehouseID, "total_amount": total,
		"paid_amount": in.AmountPaid, "balance_due": round2(total - in.AmountPaid),
		"payment_mode": in.PaymentMode, "bank_account_id": in.BankAccountID, "status": status,
		"notes": in.Notes, "tax_exempt": in.TaxExempt, "invoice_discount": in.InvoiceDiscount,
		"additional_charge_items": charges, "items": items,
	}
	if v, ok := c.Get("public_idempotency_uuid"); ok {
		body["client_bill_id"] = v
	}
	r := callInternal(c, CreatePurchaseBill, http.MethodPost, nil, body)
	if !r.ok() {
		relayInternalError(c, r)
		return
	}
	id, err := createdID(r)
	if err != nil {
		utils.PublicAPIError(c, http.StatusInternalServerError, "internal_error", "Purchase bill was not created")
		return
	}
	if b, ok := loadPurchaseBill(c, id); ok {
		c.JSON(http.StatusCreated, toPublicPurchaseBill(b, publicLocation(c)))
	}
}

// ============================================================
// Warehouses (read-only reference data for stock & purchases)
// ============================================================

func PublicListWarehouses(c *gin.Context) {
	var rows []models.Warehouse
	if err := utils.DB.Where("user_id = ?", scopeUserID(c)).Order("is_default DESC, name ASC").Find(&rows).Error; err != nil {
		utils.PublicAPIError(c, http.StatusInternalServerError, "internal_error", "Failed to list warehouses")
		return
	}
	items := make([]interface{}, len(rows))
	for i, w := range rows {
		items[i] = gin.H{
			"id": w.ID, "object": "warehouse", "name": w.Name, "code": w.Code, "city": w.City,
			"state": w.State, "is_default": w.IsDefault, "is_active": w.IsActive,
		}
	}
	c.JSON(http.StatusOK, listEnvelope{Object: "list", Data: items})
}
