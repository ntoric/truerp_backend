package controllers

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"truerp/models"
	"truerp/services/audit"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

var gstinPattern = regexp.MustCompile(`^[0-9]{2}[A-Z0-9]{13}$`)

const (
	b2bMaxOrderLines   = 200
	b2bMaxLineQuantity = 1_000_000
)

// loadPublicB2BShop resolves an enabled shop by slug, writing a 404 when the
// shop does not exist or is switched off.
func loadPublicB2BShop(c *gin.Context) (*models.B2BShopSettings, bool) {
	slug := strings.ToLower(strings.TrimSpace(c.Param("slug")))
	var settings models.B2BShopSettings
	if slug == "" || utils.DB.Where("slug = ? AND is_enabled = ?", slug, true).First(&settings).Error != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "This shop is not available"})
		return nil, false
	}
	return &settings, true
}

// GetPublicB2BShop returns storefront branding and settings.
func GetPublicB2BShop(c *gin.Context) {
	shop, ok := loadPublicB2BShop(c)
	if !ok {
		return
	}
	var business models.Business
	utils.DB.Where("user_id = ?", shop.UserID).First(&business)

	name := strings.TrimSpace(shop.ShopName)
	if name == "" {
		name = business.Name
	}

	var categories []string
	browsableB2BProductsQuery(shop).
		Where("p.category <> ''").
		Distinct("p.category").
		Order("p.category").
		Pluck("p.category", &categories)

	c.JSON(http.StatusOK, gin.H{
		"slug":                     shop.Slug,
		"name":                     name,
		"tagline":                  shop.Tagline,
		"announcement":             shop.Announcement,
		"logo_url":                 business.LogoURL,
		"city":                     business.City,
		"state":                    business.State,
		"contact_phone":            firstNonEmpty(shop.ContactPhone, business.Phone),
		"contact_email":            firstNonEmpty(shop.ContactEmail, business.Email),
		"min_order_value":          shop.MinOrderValue,
		"show_stock":               shop.ShowStock,
		"show_price":               shop.ShowPrice,
		"allow_backorders":         shop.AllowBackorders,
		"allow_new_buyers":         shop.AllowNewBuyers,
		"require_delivery_address": shop.RequireDeliveryAddress,
		"terms":                    shop.Terms,
		"categories":               categories,
	})
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

type publicB2BProduct struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Category    string    `json:"category"`
	Unit        string    `json:"unit"`
	Price       float64   `json:"price"`
	MRP         float64   `json:"mrp"`
	MinOrderQty float64   `json:"min_order_qty"`
	IsFeatured  bool      `json:"is_featured"`
	Description string    `json:"description"`
	ImageURL    string    `json:"image_url"`
	HSNCode     string    `json:"hsn_code"`
	InStock     bool      `json:"in_stock"`
	// StockQty is only populated when the shop shows stock.
	StockQty *float64 `json:"stock_qty,omitempty"`
	// MaxQty caps orderable quantity when backorders are off.
	MaxQty *float64 `json:"max_qty,omitempty"`
}

type b2bCatalogRow struct {
	models.Product
	CatalogItemID   uuid.UUID `gorm:"column:catalog_item_id"`
	CatalogB2BPrice float64   `gorm:"column:catalog_b2b_price"`
	CatalogMinQty   float64   `gorm:"column:catalog_min_qty"`
	CatalogFeatured bool      `gorm:"column:catalog_featured"`
	CatalogDesc     string    `gorm:"column:catalog_desc"`
}

const b2bCatalogRowSelect = `p.*, c.id AS catalog_item_id, c.b2b_price AS catalog_b2b_price,
	c.min_order_qty AS catalog_min_qty, c.is_featured AS catalog_featured,
	c.description AS catalog_desc`

// listedB2BProducts loads listed, active products for a shop. productIDs
// optionally restricts the result to specific products.
func listedB2BProducts(userID uuid.UUID, productIDs []uuid.UUID) ([]b2bCatalogRow, error) {
	q := listedB2BProductsQuery(userID)
	if productIDs != nil {
		q = q.Where("p.id IN ?", productIDs)
	}
	var rows []b2bCatalogRow
	err := q.Select(b2bCatalogRowSelect).Scan(&rows).Error
	return rows, err
}

// listedB2BProductsQuery is the join/filter base without a SELECT list, so
// it can be used for both Count and row fetches.
// browsableB2BProductsQuery is listedB2BProductsQuery narrowed to what the
// storefront should browse: with HideOutOfStock on, goods whose available
// stock can't cover their minimum order quantity are left out (services have
// no stock and always stay).
func browsableB2BProductsQuery(shop *models.B2BShopSettings) *gorm.DB {
	q := listedB2BProductsQuery(shop.UserID)
	if shop.HideOutOfStock {
		q = q.Where("(LOWER(COALESCE(p.item_type, '')) = 'service' OR " + b2bStockSubquery + " >= CASE WHEN c.min_order_qty > 0 THEN c.min_order_qty ELSE 1 END)")
	}
	return q
}

func listedB2BProductsQuery(userID uuid.UUID) *gorm.DB {
	return utils.DB.Table("b2b_catalog_items AS c").
		Joins("JOIN products AS p ON p.id = c.product_id AND p.user_id = c.user_id").
		Where("c.user_id = ? AND c.is_listed = ? AND p.deleted_at IS NULL AND p.is_active = ?", userID, true, true)
}

func (r b2bCatalogRow) catalogItem() models.B2BCatalogItem {
	return models.B2BCatalogItem{
		ID:          r.CatalogItemID,
		ProductID:   r.Product.ID,
		IsListed:    true,
		B2BPrice:    r.CatalogB2BPrice,
		MinOrderQty: r.CatalogMinQty,
		IsFeatured:  r.CatalogFeatured,
		Description: r.CatalogDesc,
	}
}

// ListPublicB2BProducts lists the shop's catalogue, paginated.
func ListPublicB2BProducts(c *gin.Context) {
	shop, ok := loadPublicB2BShop(c)
	if !ok {
		return
	}

	q := browsableB2BProductsQuery(shop)
	if category := strings.TrimSpace(c.Query("category")); category != "" && category != "all" {
		q = q.Where("p.category = ?", category)
	}
	if search := strings.TrimSpace(c.Query("search")); search != "" {
		like := "%" + strings.ToLower(search) + "%"
		q = q.Where("(LOWER(p.name) LIKE ? OR LOWER(p.category) LIKE ? OR LOWER(p.item_code) LIKE ? OR LOWER(p.sku) LIKE ?)", like, like, like, like)
	}
	if c.Query("featured") == "true" {
		q = q.Where("c.is_featured = ?", true)
	}

	var total int64
	if err := q.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load products"})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	perPage, _ := strconv.Atoi(c.DefaultQuery("per_page", "48"))
	if perPage < 1 || perPage > 100 {
		perPage = 48
	}

	order := "c.is_featured DESC, LOWER(p.name) ASC"
	switch c.Query("sort") {
	case "name":
		order = "LOWER(p.name) ASC"
	case "newest":
		order = "p.created_at DESC"
	}

	var rows []b2bCatalogRow
	if err := q.Select(b2bCatalogRowSelect).Order(order).Limit(perPage).Offset((page - 1) * perPage).Scan(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load products"})
		return
	}

	out := toPublicB2BProducts(shop, rows)

	c.JSON(http.StatusOK, gin.H{
		"products": out,
		"total":    total,
		"page":     page,
		"per_page": perPage,
	})
}

// toPublicB2BProducts maps catalogue rows to the buyer-facing shape, filling
// in stock and fallback images in two batched queries.
func toPublicB2BProducts(shop *models.B2BShopSettings, rows []b2bCatalogRow) []publicB2BProduct {
	ids := make([]uuid.UUID, 0, len(rows))
	var missingImage []uuid.UUID
	for _, r := range rows {
		ids = append(ids, r.Product.ID)
		if r.Product.ImageUrl == "" {
			missingImage = append(missingImage, r.Product.ID)
		}
	}
	stock := b2bStockByProduct(shop.UserID, ids)
	images := b2bPrimaryImages(shop.UserID, missingImage)

	out := make([]publicB2BProduct, 0, len(rows))
	for _, r := range rows {
		item := r.catalogItem()
		p := r.Product
		description := strings.TrimSpace(item.Description)
		if description == "" {
			description = p.Description
		}
		image := p.ImageUrl
		if image == "" {
			image = images[p.ID]
		}
		qty := stock[p.ID]
		isService := strings.EqualFold(p.ItemType, "service")
		minQty := b2bMinQty(item)
		row := publicB2BProduct{
			ID:          p.ID,
			Name:        p.Name,
			Category:    p.Category,
			Unit:        p.Unit,
			Price:       b2bEffectivePrice(item, p),
			MRP:         p.MRP,
			MinOrderQty: minQty,
			IsFeatured:  item.IsFeatured,
			Description: description,
			ImageURL:    image,
			HSNCode:     p.HSNCode,
			// Orderable only when stock covers the minimum order quantity.
			InStock: isService || qty >= minQty || shop.AllowBackorders,
		}
		if shop.ShowStock && !isService {
			v := qty
			row.StockQty = &v
		}
		// Only reveal the cap when stock is public; the server still enforces it.
		if shop.ShowStock && !shop.AllowBackorders && !isService {
			v := qty
			if v < 0 {
				v = 0
			}
			row.MaxQty = &v
		}
		out = append(out, row)
	}
	return out
}

// GetPublicB2BProduct returns one listed product with its full image
// gallery and up to 8 related listed products (same category first).
func GetPublicB2BProduct(c *gin.Context) {
	shop, ok := loadPublicB2BShop(c)
	if !ok {
		return
	}
	productID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Product not found"})
		return
	}
	rows, err := listedB2BProducts(shop.UserID, []uuid.UUID{productID})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load product"})
		return
	}
	if len(rows) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "This product is not available in the shop"})
		return
	}
	row := rows[0]
	product := toPublicB2BProducts(shop, rows)[0]

	// Gallery: the main image first, then gallery images without duplicates.
	images := []string{}
	seen := map[string]bool{}
	add := func(u string) {
		if u = strings.TrimSpace(u); u != "" && !seen[u] {
			seen[u] = true
			images = append(images, u)
		}
	}
	add(row.Product.ImageUrl)
	var gallery []models.ProductImage
	utils.DB.Where("user_id = ? AND product_id = ?", shop.UserID, productID).
		Order("is_primary DESC, sort_order ASC, created_at ASC").Find(&gallery)
	for _, img := range gallery {
		add(img.ImageURL)
	}

	// Related: same category, featured first; topped up with other featured
	// products when the category is small.
	var relatedRows []b2bCatalogRow
	if row.Product.Category != "" {
		browsableB2BProductsQuery(shop).Select(b2bCatalogRowSelect).
			Where("p.category = ? AND p.id <> ?", row.Product.Category, productID).
			Order("c.is_featured DESC, LOWER(p.name) ASC").Limit(8).Scan(&relatedRows)
	}
	if len(relatedRows) < 8 {
		exclude := []uuid.UUID{productID}
		for _, r := range relatedRows {
			exclude = append(exclude, r.Product.ID)
		}
		var more []b2bCatalogRow
		browsableB2BProductsQuery(shop).Select(b2bCatalogRowSelect).
			Where("p.id NOT IN ?", exclude).
			Order("c.is_featured DESC, p.created_at DESC").Limit(8 - len(relatedRows)).Scan(&more)
		relatedRows = append(relatedRows, more...)
	}

	c.JSON(http.StatusOK, gin.H{
		"product":   product,
		"images":    images,
		"item_code": row.Product.ItemCode,
		"related":   toPublicB2BProducts(shop, relatedRows),
	})
}

// b2bBuyerParties scopes party lookups for the storefront. Every active party
// in Parties can buy — customers and vendors alike (a supplier may also buy
// wholesale from you).
func b2bBuyerParties(tx *gorm.DB, userID uuid.UUID) *gorm.DB {
	return tx.Model(&models.Party{}).Where("user_id = ? AND is_active = ?", userID, true)
}

// SearchPublicB2BBuyers lets a returning buyer find their business in
// Parties by name, phone or GSTIN. Results are deliberately thin (name, city,
// masked phone); ordering as that party still requires its registered phone.
func SearchPublicB2BBuyers(c *gin.Context) {
	shop, ok := loadPublicB2BShop(c)
	if !ok {
		return
	}
	q := strings.TrimSpace(c.Query("q"))
	if len([]rune(q)) < 3 {
		c.JSON(http.StatusOK, []gin.H{})
		return
	}

	query := b2bBuyerParties(utils.DB, shop.UserID)
	digits := normalizePhone(q)
	compact := strings.NewReplacer(" ", "", "+", "", "-", "").Replace(q)
	upper := strings.ToUpper(compact)
	phoneSearch := len(digits) >= 6 && len(digits) == len(compact)
	switch {
	case phoneSearch:
		tail := digits[len(digits)-min(len(digits), 10):]
		query = query.Where(sqlPhoneDigits("phone")+" LIKE ?", "%"+tail)
	case gstinPattern.MatchString(upper):
		query = query.Where("UPPER(gstin) = ?", upper)
	default:
		// Whitespace-insensitive, so "Sharma  Traders" finds "SharmaTraders".
		query = query.Where(utils.SQLCompactLike("name"), utils.CompactLikePattern(q))
	}

	var parties []models.Party
	if err := query.Order("name ASC").Limit(8).Find(&parties).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Search failed"})
		return
	}

	out := make([]gin.H, 0, len(parties))
	for _, p := range parties {
		out = append(out, gin.H{
			"id":         p.ID,
			"name":       p.Name,
			"city":       p.City,
			"phone_hint": maskPhone(p.Phone),
			"has_phone":  phoneKey(p.Phone) != "",
		})
	}
	c.JSON(http.StatusOK, out)
}

type placeB2BOrderInput struct {
	// Existing buyer: PartyID plus the phone registered on that party.
	PartyID *uuid.UUID `json:"party_id"`
	Phone   string     `json:"phone"`
	// New buyer profile (used when PartyID is empty). BuyerType is
	// "business" (default) or "individual".
	Business *struct {
		BuyerType string `json:"buyer_type"`
		Name      string `json:"name"`
		Phone     string `json:"phone"`
		Email     string `json:"email"`
		GSTIN     string `json:"gstin"`
		Address   string `json:"address"`
		City      string `json:"city"`
		State     string `json:"state"`
		Pincode   string `json:"pincode"`
	} `json:"business"`
	ContactPerson   string `json:"contact_person"`
	ShippingAddress string `json:"shipping_address"`
	City            string `json:"city"`
	State           string `json:"state"`
	Pincode         string `json:"pincode"`
	Notes           string `json:"notes"`
	Items           []struct {
		ProductID uuid.UUID `json:"product_id"`
		Quantity  float64   `json:"quantity"`
	} `json:"items"`
}

type b2bOrderError struct {
	status  int
	message string
}

func (e *b2bOrderError) Error() string { return e.message }

func newB2BOrderError(status int, format string, args ...interface{}) *b2bOrderError {
	return &b2bOrderError{status: status, message: fmt.Sprintf(format, args...)}
}

// PlacePublicB2BOrder creates an order request from the storefront cart.
// Prices are always taken from the catalogue, never from the client.
func PlacePublicB2BOrder(c *gin.Context) {
	shop, ok := loadPublicB2BShop(c)
	if !ok {
		return
	}

	var input placeB2BOrderInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid order details"})
		return
	}

	unbind := audit.BindActor(&audit.Actor{
		UserID:    shop.UserID,
		UserName:  "B2B shop buyer",
		ScopeID:   shop.UserID,
		IPAddress: c.ClientIP(),
		UserAgent: c.GetHeader("User-Agent"),
		RequestID: uuid.NewString(),
	})
	defer unbind()

	items, err := buildB2BOrderItems(shop, &input)
	if err != nil {
		writeB2BOrderError(c, err)
		return
	}

	var order *models.B2BOrder
	// Retry on order-number collisions from concurrent checkouts.
	for attempt := 0; attempt < 4; attempt++ {
		order, err = createB2BOrder(shop, &input, items, c.ClientIP())
		if err == nil || !isUniqueViolation(err) {
			break
		}
	}
	if err != nil {
		writeB2BOrderError(c, err)
		return
	}

	notifyNewB2BOrder(order)
	c.JSON(http.StatusCreated, gin.H{
		"order_number": order.OrderNumber,
		"party_id":     order.PartyID,
		"buyer_match":  order.BuyerMatch,
		"verified":     order.BuyerVerified,
		"status":       order.Status,
		"total_amount": order.TotalAmount,
		"item_count":   order.ItemCount,
		"buyer_name":   order.BuyerName,
		"created_at":   order.CreatedAt,
	})
}

func writeB2BOrderError(c *gin.Context, err error) {
	var oe *b2bOrderError
	if errors.As(err, &oe) {
		c.JSON(oe.status, gin.H{"error": oe.message})
		return
	}
	fmt.Printf("[b2b-shop] order failed: %v\n", err)
	c.JSON(http.StatusInternalServerError, gin.H{"error": "Could not place the order. Please try again."})
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique") || strings.Contains(msg, "duplicate key")
}

// buildB2BOrderItems validates the cart against the live catalogue.
func buildB2BOrderItems(shop *models.B2BShopSettings, input *placeB2BOrderInput) ([]models.B2BOrderItem, error) {
	if len(input.Items) == 0 {
		return nil, newB2BOrderError(http.StatusBadRequest, "Your cart is empty")
	}
	if len(input.Items) > b2bMaxOrderLines {
		return nil, newB2BOrderError(http.StatusBadRequest, "An order can contain at most %d products", b2bMaxOrderLines)
	}

	// Merge duplicate lines while keeping cart order.
	qtyByProduct := map[uuid.UUID]float64{}
	var order []uuid.UUID
	for _, it := range input.Items {
		if it.Quantity <= 0 || it.Quantity > b2bMaxLineQuantity {
			return nil, newB2BOrderError(http.StatusBadRequest, "Invalid quantity in cart")
		}
		if _, seen := qtyByProduct[it.ProductID]; !seen {
			order = append(order, it.ProductID)
		}
		qtyByProduct[it.ProductID] += it.Quantity
	}

	rows, err := listedB2BProducts(shop.UserID, order)
	if err != nil {
		return nil, err
	}
	byID := map[uuid.UUID]b2bCatalogRow{}
	for _, r := range rows {
		byID[r.Product.ID] = r
	}
	stock := b2bStockByProduct(shop.UserID, order)

	items := make([]models.B2BOrderItem, 0, len(order))
	var total float64
	for _, pid := range order {
		row, ok := byID[pid]
		if !ok {
			return nil, newB2BOrderError(http.StatusConflict, "Some products in your cart are no longer available. Please refresh and review your cart.")
		}
		p := row.Product
		item := row.catalogItem()
		qty := qtyByProduct[pid]
		if minQty := b2bMinQty(item); qty < minQty {
			return nil, newB2BOrderError(http.StatusBadRequest, "Minimum order quantity for %s is %s %s", p.Name, formatQty(minQty), p.Unit)
		}
		if !shop.AllowBackorders && !strings.EqualFold(p.ItemType, "service") && qty > stock[pid] {
			avail := stock[pid]
			if avail <= 0 {
				return nil, newB2BOrderError(http.StatusConflict, "%s is out of stock", p.Name)
			}
			return nil, newB2BOrderError(http.StatusConflict, "Only %s %s of %s available", formatQty(avail), p.Unit, p.Name)
		}
		price := b2bEffectivePrice(item, p)
		image := p.ImageUrl
		if strings.HasPrefix(image, "data:") {
			// Don't copy inline image blobs into every order line.
			image = ""
		}
		productID := p.ID
		line := models.B2BOrderItem{
			ID:        uuid.New(),
			UserID:    shop.UserID,
			ProductID: &productID,
			Name:      p.Name,
			Unit:      p.Unit,
			HSNCode:   p.HSNCode,
			ImageURL:  image,
			Quantity:  qty,
			UnitPrice: price,
			Total:     roundMoney(qty * price),
		}
		total += line.Total
		items = append(items, line)
	}

	if shop.MinOrderValue > 0 && total < shop.MinOrderValue {
		return nil, newB2BOrderError(http.StatusBadRequest, "Minimum order value is %.2f", shop.MinOrderValue)
	}
	return items, nil
}

func formatQty(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

type b2bBuyer struct {
	party    models.Party
	verified bool   // buyer proved the party's registered phone
	isNew    bool   // party was created by this order
	match    string // selected | phone | gstin | name | created
}

// resolveB2BBuyer ties an order to a party in Parties.
//
// Existing buyers pick their party from the lookup and confirm its phone.
// Sign-ups are matched against Parties before anything is created — by
// phone, then GSTIN, then exact (whitespace/case-insensitive) name — so
// returning businesses never get duplicate party records. Only a phone match
// counts as verified; GSTIN or name matches link the order to the existing
// party but flag it for the store to confirm. Unmatched sign-ups become new
// parties: businesses as Wholesale customers, individuals as Retail.
func resolveB2BBuyer(tx *gorm.DB, shop *models.B2BShopSettings, input *placeB2BOrderInput) (b2bBuyer, error) {
	if input.PartyID != nil && *input.PartyID != uuid.Nil {
		var party models.Party
		if err := b2bBuyerParties(tx, shop.UserID).Where("id = ?", *input.PartyID).First(&party).Error; err != nil {
			return b2bBuyer{}, newB2BOrderError(http.StatusBadRequest, "Business not found. Please search again or register as a new buyer.")
		}
		registered := phoneKey(party.Phone)
		if registered != "" && phoneKey(input.Phone) != registered {
			return b2bBuyer{}, newB2BOrderError(http.StatusForbidden, "The phone number does not match our records for %s", party.Name)
		}
		// No phone on file: accept, but the order is flagged as unverified.
		return b2bBuyer{party: party, verified: registered != "", match: "selected"}, nil
	}

	b := input.Business
	if b == nil {
		return b2bBuyer{}, newB2BOrderError(http.StatusBadRequest, "Select your business or register a new one")
	}
	if !shop.AllowNewBuyers {
		return b2bBuyer{}, newB2BOrderError(http.StatusForbidden, "This shop only accepts orders from registered businesses. Please contact the seller.")
	}
	individual := strings.EqualFold(strings.TrimSpace(b.BuyerType), "individual")
	name := strings.TrimSpace(b.Name)
	if len([]rune(name)) < 2 {
		if individual {
			return b2bBuyer{}, newB2BOrderError(http.StatusBadRequest, "Your name is required")
		}
		return b2bBuyer{}, newB2BOrderError(http.StatusBadRequest, "Business name is required")
	}
	key := phoneKey(b.Phone)
	if key == "" {
		return b2bBuyer{}, newB2BOrderError(http.StatusBadRequest, "A valid 10-digit phone number is required")
	}
	gstin := strings.ToUpper(strings.TrimSpace(b.GSTIN))
	if gstin != "" && !gstinPattern.MatchString(gstin) {
		return b2bBuyer{}, newB2BOrderError(http.StatusBadRequest, "GSTIN must be 15 characters (e.g. 29ABCDE1234F1Z5)")
	}

	// 1. Phone — proves identity, so missing details may be filled in.
	var candidates []models.Party
	b2bBuyerParties(tx, shop.UserID).Where(sqlPhoneDigits("phone")+" LIKE ?", "%"+key).Order("created_at ASC").Find(&candidates)
	for _, cand := range candidates {
		if phoneKey(cand.Phone) == key {
			if err := fillB2BPartyBlanks(tx, &cand, b.Email, gstin, b.Address, b.City, b.State, b.Pincode); err != nil {
				return b2bBuyer{}, err
			}
			return b2bBuyer{party: cand, verified: true, match: "phone"}, nil
		}
	}

	// 2. GSTIN, 3. exact name — link without trusting the new details.
	var existing models.Party
	if gstin != "" && b2bBuyerParties(tx, shop.UserID).Where("UPPER(gstin) = ?", gstin).Order("created_at ASC").First(&existing).Error == nil {
		return b2bBuyer{party: existing, match: "gstin"}, nil
	}
	if b2bBuyerParties(tx, shop.UserID).
		Where(utils.SQLCompactColumn("name")+" = ?", utils.CompactSearchTerm(name)).
		Order("created_at ASC").First(&existing).Error == nil {
		return b2bBuyer{party: existing, match: "name"}, nil
	}

	// 4. New party.
	stateCode := ""
	if len(gstin) >= 2 {
		stateCode = gstin[:2]
	}
	category, notes := "Wholesale", "Registered via B2B shop (business)"
	if individual {
		category, notes = "Retail", "Registered via B2B shop (individual)"
	}
	party := models.Party{
		ID:        uuid.New(),
		UserID:    shop.UserID,
		Name:      name,
		Phone:     strings.TrimSpace(b.Phone),
		Email:     strings.TrimSpace(b.Email),
		GSTIN:     gstin,
		Address:   strings.TrimSpace(b.Address),
		City:      strings.TrimSpace(b.City),
		State:     strings.TrimSpace(b.State),
		Pincode:   strings.TrimSpace(b.Pincode),
		StateCode: stateCode,
		Category:  category,
		PartyType: "customer",
		Notes:     notes,
		IsActive:  true,
	}
	if err := tx.Create(&party).Error; err != nil {
		return b2bBuyer{}, err
	}
	return b2bBuyer{party: party, isNew: true, match: "created"}, nil
}

// fillB2BPartyBlanks copies sign-up details onto a phone-verified party, only
// into fields that are empty — existing data in Parties is never overwritten.
func fillB2BPartyBlanks(tx *gorm.DB, party *models.Party, email, gstin, address, city, state, pincode string) error {
	updates := map[string]interface{}{}
	set := func(column string, current *string, value string) {
		if value = strings.TrimSpace(value); value != "" && strings.TrimSpace(*current) == "" {
			*current = value
			updates[column] = value
		}
	}
	set("email", &party.Email, email)
	set("gstin", &party.GSTIN, gstin)
	set("address", &party.Address, address)
	set("city", &party.City, city)
	set("state", &party.State, state)
	set("pincode", &party.Pincode, pincode)
	if _, ok := updates["gstin"]; ok && party.StateCode == "" && len(gstin) >= 2 {
		party.StateCode = gstin[:2]
		updates["state_code"] = party.StateCode
	}
	if len(updates) == 0 {
		return nil
	}
	return tx.Model(&models.Party{}).Where("id = ?", party.ID).Updates(updates).Error
}

func createB2BOrder(shop *models.B2BShopSettings, input *placeB2BOrderInput, items []models.B2BOrderItem, ip string) (*models.B2BOrder, error) {
	var order models.B2BOrder
	err := utils.DB.Transaction(func(tx *gorm.DB) error {
		buyer, err := resolveB2BBuyer(tx, shop, input)
		if err != nil {
			return err
		}
		party := buyer.party

		// The buyer's own details: the party on file, except for sign-ups
		// matched only by GSTIN/name, where the details they typed are what
		// identify and reach them (the party record is left untouched).
		contact := struct{ name, phone, email, gstin, address, city, state, pincode string }{
			party.Name, party.Phone, party.Email, party.GSTIN, party.Address, party.City, party.State, party.Pincode,
		}
		if b := input.Business; b != nil && (buyer.match == "gstin" || buyer.match == "name") {
			contact.name = firstNonEmpty(b.Name, party.Name)
			contact.phone = strings.TrimSpace(b.Phone)
			contact.email = firstNonEmpty(b.Email, party.Email)
			contact.gstin = firstNonEmpty(strings.ToUpper(b.GSTIN), party.GSTIN)
			contact.address, contact.city, contact.state, contact.pincode = b.Address, b.City, b.State, b.Pincode
		}
		if phoneKey(contact.phone) == "" {
			contact.phone = strings.TrimSpace(input.Phone)
		}

		address := firstNonEmpty(input.ShippingAddress, contact.address)
		city := firstNonEmpty(input.City, contact.city)
		state := firstNonEmpty(input.State, contact.state)
		pincode := firstNonEmpty(input.Pincode, contact.pincode)
		if address == "" && shop.RequireDeliveryAddress {
			return newB2BOrderError(http.StatusBadRequest, "Delivery address is required")
		}

		lines := make([]models.B2BOrderItem, len(items))
		copy(lines, items)
		order = models.B2BOrder{
			ID:              uuid.New(),
			UserID:          shop.UserID,
			OrderNumber:     allocateB2BOrderNumber(tx, shop.UserID),
			PartyID:         party.ID,
			Status:          models.B2BOrderPending,
			BuyerVerified:   buyer.verified,
			IsNewBuyer:      buyer.isNew,
			BuyerMatch:      buyer.match,
			BuyerName:       truncate(contact.name, 200),
			ContactPerson:   truncate(strings.TrimSpace(input.ContactPerson), 120),
			BuyerPhone:      contact.phone,
			BuyerEmail:      contact.email,
			BuyerGSTIN:      contact.gstin,
			ShippingAddress: truncate(address, 500),
			City:            truncate(city, 100),
			State:           truncate(state, 100),
			Pincode:         truncate(pincode, 12),
			Notes:           truncate(strings.TrimSpace(input.Notes), 2000),
			PlacedFromIP:    ip,
			Items:           lines,
		}
		for i := range order.Items {
			order.Items[i].ID = uuid.New()
			order.Items[i].OrderID = order.ID
		}
		recalcB2BOrderTotals(&order)
		appendB2BOrderEvent(&order, models.B2BOrderPending, "Order placed", party.Name)
		return tx.Create(&order).Error
	})
	if err != nil {
		return nil, err
	}
	return &order, nil
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// TrackPublicB2BOrder lets a buyer check an order using its number and the
// phone it was placed with.
func TrackPublicB2BOrder(c *gin.Context) {
	shop, ok := loadPublicB2BShop(c)
	if !ok {
		return
	}
	number := strings.ToUpper(strings.TrimSpace(c.Param("number")))
	key := phoneKey(c.Query("phone"))

	var order models.B2BOrder
	if number == "" || key == "" ||
		utils.DB.Preload("Items").Where("user_id = ? AND order_number = ?", shop.UserID, number).First(&order).Error != nil ||
		phoneKey(order.BuyerPhone) != key {
		c.JSON(http.StatusNotFound, gin.H{"error": "No order found for that number and phone"})
		return
	}

	timeline := make([]gin.H, 0, len(order.Timeline))
	for _, ev := range order.Timeline {
		timeline = append(timeline, gin.H{"status": ev.Status, "note": ev.Note, "at": ev.At})
	}
	sort.SliceStable(order.Items, func(i, j int) bool { return order.Items[i].CreatedAt.Before(order.Items[j].CreatedAt) })
	items := make([]gin.H, 0, len(order.Items))
	for _, it := range order.Items {
		items = append(items, gin.H{
			"name": it.Name, "unit": it.Unit, "quantity": it.Quantity,
			"unit_price": it.UnitPrice, "total": it.Total,
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"order_number":     order.OrderNumber,
		"status":           order.Status,
		"buyer_name":       order.BuyerName,
		"total_amount":     order.TotalAmount,
		"item_count":       order.ItemCount,
		"shipping_address": order.ShippingAddress,
		"city":             order.City,
		"created_at":       order.CreatedAt,
		"updated_at":       order.UpdatedAt,
		"timeline":         timeline,
		"items":            items,
		"invoiced":         order.InvoiceID != nil,
		"tracked_at":       time.Now(),
	})
}
