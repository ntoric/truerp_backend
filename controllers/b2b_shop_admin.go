package controllers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func b2bActorName(c *gin.Context) string {
	if name, ok := c.Get("user_name"); ok {
		if s, ok := name.(string); ok && s != "" {
			return s
		}
	}
	return "Staff"
}

// ---------------------------------------------------------------------------
// Settings
// ---------------------------------------------------------------------------

func GetB2BShopSettings(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	settings, err := getOrCreateB2BShopSettings(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load shop settings"})
		return
	}

	var business models.Business
	utils.DB.Where("user_id = ?", userID).First(&business)

	var listed, pending int64
	utils.DB.Table("b2b_catalog_items AS c").
		Joins("JOIN products AS p ON p.id = c.product_id AND p.user_id = c.user_id").
		Where("c.user_id = ? AND c.is_listed = ? AND p.deleted_at IS NULL AND p.is_active = ?", userID, true, true).
		Count(&listed)
	utils.DB.Model(&models.B2BOrder{}).Where("user_id = ? AND status = ?", userID, models.B2BOrderPending).Count(&pending)

	c.JSON(http.StatusOK, gin.H{
		"settings":       settings,
		"business":       gin.H{"name": business.Name, "logo_url": business.LogoURL},
		"shop_path":      "/" + settings.Slug + "/shop",
		"listed_count":   listed,
		"pending_orders": pending,
	})
}

func UpdateB2BShopSettings(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	settings, err := getOrCreateB2BShopSettings(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load shop settings"})
		return
	}

	var input struct {
		IsEnabled              *bool    `json:"is_enabled"`
		Slug                   *string  `json:"slug"`
		ShopName               *string  `json:"shop_name"`
		Tagline                *string  `json:"tagline"`
		Announcement           *string  `json:"announcement"`
		ContactPhone           *string  `json:"contact_phone"`
		ContactEmail           *string  `json:"contact_email"`
		MinOrderValue          *float64 `json:"min_order_value"`
		ShowStock              *bool    `json:"show_stock"`
		AllowBackorders        *bool    `json:"allow_backorders"`
		HideOutOfStock         *bool    `json:"hide_out_of_stock"`
		AllowNewBuyers         *bool    `json:"allow_new_buyers"`
		RequireDeliveryAddress *bool    `json:"require_delivery_address"`
		Terms                  *string  `json:"terms"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if input.Slug != nil {
		raw := strings.TrimSpace(*input.Slug)
		if raw == "" {
			c.JSON(http.StatusBadRequest, gin.H{"fields": gin.H{"slug": "Shop URL is required"}, "error": "Shop URL is required"})
			return
		}
		slug := slugify(raw)
		if isReservedShopSlug(slug) {
			c.JSON(http.StatusBadRequest, gin.H{"fields": gin.H{"slug": "This shop URL is reserved"}, "error": "This shop URL is reserved"})
			return
		}
		var taken int64
		utils.DB.Unscoped().Model(&models.B2BShopSettings{}).Where("slug = ? AND user_id <> ?", slug, userID).Count(&taken)
		if taken > 0 {
			c.JSON(http.StatusBadRequest, gin.H{"fields": gin.H{"slug": "This shop URL is already taken"}, "error": "This shop URL is already taken"})
			return
		}
		settings.Slug = slug
	}
	if input.MinOrderValue != nil {
		if *input.MinOrderValue < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"fields": gin.H{"min_order_value": "Cannot be negative"}, "error": "Minimum order value cannot be negative"})
			return
		}
		settings.MinOrderValue = *input.MinOrderValue
	}
	setStr := func(dst *string, src *string, max int) {
		if src != nil {
			*dst = truncate(strings.TrimSpace(*src), max)
		}
	}
	setStr(&settings.ShopName, input.ShopName, 120)
	setStr(&settings.Tagline, input.Tagline, 200)
	setStr(&settings.Announcement, input.Announcement, 300)
	setStr(&settings.ContactPhone, input.ContactPhone, 30)
	setStr(&settings.ContactEmail, input.ContactEmail, 120)
	setStr(&settings.Terms, input.Terms, 5000)
	if input.IsEnabled != nil {
		settings.IsEnabled = *input.IsEnabled
	}
	if input.ShowStock != nil {
		settings.ShowStock = *input.ShowStock
	}
	if input.AllowBackorders != nil {
		settings.AllowBackorders = *input.AllowBackorders
	}
	if input.HideOutOfStock != nil {
		settings.HideOutOfStock = *input.HideOutOfStock
	}
	if input.AllowNewBuyers != nil {
		settings.AllowNewBuyers = *input.AllowNewBuyers
	}
	if input.RequireDeliveryAddress != nil {
		settings.RequireDeliveryAddress = *input.RequireDeliveryAddress
	}

	if err := utils.DB.Save(settings).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save shop settings"})
		return
	}
	c.JSON(http.StatusOK, settings)
}

// ---------------------------------------------------------------------------
// Catalogue
// ---------------------------------------------------------------------------

type b2bAdminCatalogRow struct {
	ProductID     uuid.UUID  `json:"product_id" gorm:"column:product_id"`
	Name          string     `json:"name" gorm:"column:name"`
	Category      string     `json:"category" gorm:"column:category"`
	Unit          string     `json:"unit" gorm:"column:unit"`
	ItemCode      string     `json:"item_code" gorm:"column:item_code"`
	ItemType      string     `json:"item_type" gorm:"column:item_type"`
	SalePrice     float64    `json:"sale_price" gorm:"column:sale_price"`
	PurchasePrice float64    `json:"purchase_price" gorm:"column:purchase_price"`
	MRP           float64    `json:"mrp" gorm:"column:mrp"`
	ImageURL      string     `json:"image_url" gorm:"column:image_url"`
	IsActive      bool       `json:"is_active" gorm:"column:is_active"`
	StockQty      float64    `json:"stock_qty" gorm:"column:stock_qty"`
	CatalogID     *uuid.UUID `json:"catalog_id" gorm:"column:catalog_id"`
	IsListed      bool       `json:"is_listed" gorm:"column:is_listed"`
	B2BPrice      float64    `json:"b2b_price" gorm:"column:b2b_price"`
	MinOrderQty   float64    `json:"min_order_qty" gorm:"column:min_order_qty"`
	IsFeatured    bool       `json:"is_featured" gorm:"column:is_featured"`
	Description   string     `json:"description" gorm:"column:description"`
}

const b2bStockSubquery = "(SELECT COALESCE(SUM(s.available_qty), 0) FROM inventory_stocks s WHERE s.product_id = p.id AND s.user_id = p.user_id)"

func b2bAdminCatalogBase(userID uuid.UUID) *gorm.DB {
	return utils.DB.Table("products AS p").
		Joins("LEFT JOIN b2b_catalog_items AS c ON c.product_id = p.id AND c.user_id = p.user_id").
		Where("p.user_id = ? AND p.deleted_at IS NULL", userID)
}

// ListB2BCatalog lists every product in the store with its storefront
// listing state, so staff can pick what to publish.
func ListB2BCatalog(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	q := b2bAdminCatalogBase(userID)
	if category := strings.TrimSpace(c.Query("category")); category != "" && category != "all" {
		q = q.Where("p.category = ?", category)
	}
	if search := strings.TrimSpace(c.Query("search")); search != "" {
		like := "%" + strings.ToLower(search) + "%"
		q = q.Where("(LOWER(p.name) LIKE ? OR LOWER(p.item_code) LIKE ? OR LOWER(p.sku) LIKE ? OR LOWER(p.category) LIKE ?)", like, like, like, like)
	}
	switch c.Query("filter") {
	case "listed":
		q = q.Where("c.is_listed = ?", true)
	case "unlisted":
		q = q.Where("(c.id IS NULL OR c.is_listed = ?)", false)
	case "in_stock":
		q = q.Where(b2bStockSubquery + " > 0")
	case "out_of_stock":
		q = q.Where(b2bStockSubquery + " <= 0")
	case "featured":
		q = q.Where("c.is_featured = ?", true)
	}

	var total int64
	if err := q.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load catalogue"})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	perPage, _ := strconv.Atoi(c.DefaultQuery("per_page", "25"))
	if perPage < 1 || perPage > 200 {
		perPage = 25
	}

	var rows []b2bAdminCatalogRow
	err := q.Select(`p.id AS product_id, p.name, p.category, p.unit, p.item_code, p.item_type,
			p.sale_price, p.purchase_price, p.mrp, p.image_url, p.is_active,
			` + b2bStockSubquery + ` AS stock_qty,
			c.id AS catalog_id, COALESCE(c.is_listed, false) AS is_listed,
			COALESCE(c.b2b_price, 0) AS b2b_price, COALESCE(c.min_order_qty, 1) AS min_order_qty,
			COALESCE(c.is_featured, false) AS is_featured, COALESCE(c.description, '') AS description`).
		Order("COALESCE(c.is_listed, false) DESC, LOWER(p.name) ASC").
		Limit(perPage).Offset((page - 1) * perPage).
		Scan(&rows).Error
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load catalogue"})
		return
	}
	// Inline data: images can be large; the list only needs a thumbnail URL.
	var missing []uuid.UUID
	for _, r := range rows {
		if r.ImageURL == "" {
			missing = append(missing, r.ProductID)
		}
	}
	images := b2bPrimaryImages(userID, missing)
	for i := range rows {
		if rows[i].ImageURL == "" {
			rows[i].ImageURL = images[rows[i].ProductID]
		}
	}

	var summary struct {
		TotalProducts    int64 `json:"total_products"`
		Listed           int64 `json:"listed"`
		InStock          int64 `json:"in_stock"`
		InStockNotListed int64 `json:"in_stock_not_listed"`
	}
	b2bAdminCatalogBase(userID).Count(&summary.TotalProducts)
	b2bAdminCatalogBase(userID).Where("c.is_listed = ?", true).Count(&summary.Listed)
	b2bAdminCatalogBase(userID).Where(b2bStockSubquery + " > 0").Count(&summary.InStock)
	b2bAdminCatalogBase(userID).Where(b2bStockSubquery+" > 0").
		Where("(c.id IS NULL OR c.is_listed = ?)", false).Count(&summary.InStockNotListed)

	var categories []string
	utils.DB.Model(&models.Product{}).Where("user_id = ? AND category <> ''", userID).
		Distinct("category").Order("category").Pluck("category", &categories)

	c.JSON(http.StatusOK, gin.H{
		"items":      rows,
		"total":      total,
		"page":       page,
		"per_page":   perPage,
		"summary":    summary,
		"categories": categories,
	})
}

// UpsertB2BCatalogItem lists/unlists a product or edits its wholesale terms.
func UpsertB2BCatalogItem(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	productID, err := uuid.Parse(c.Param("product_id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid product id"})
		return
	}
	var product models.Product
	if err := utils.DB.Where("id = ? AND user_id = ?", productID, userID).First(&product).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Product not found"})
		return
	}

	var input struct {
		IsListed    *bool    `json:"is_listed"`
		B2BPrice    *float64 `json:"b2b_price"`
		MinOrderQty *float64 `json:"min_order_qty"`
		IsFeatured  *bool    `json:"is_featured"`
		Description *string  `json:"description"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if input.B2BPrice != nil && *input.B2BPrice < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Wholesale price cannot be negative"})
		return
	}
	if input.MinOrderQty != nil && *input.MinOrderQty <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Minimum order quantity must be greater than zero"})
		return
	}

	var item models.B2BCatalogItem
	if err := utils.DB.Where("user_id = ? AND product_id = ?", userID, productID).First(&item).Error; err != nil {
		// A new row is only published when the caller asks for it explicitly, so
		// editing terms on an unlisted product never puts it in the shop.
		item = models.B2BCatalogItem{ID: uuid.New(), UserID: userID, ProductID: productID, IsListed: false, MinOrderQty: 1}
	}
	if input.IsListed != nil {
		item.IsListed = *input.IsListed
	}
	if input.B2BPrice != nil {
		item.B2BPrice = *input.B2BPrice
	}
	if input.MinOrderQty != nil {
		item.MinOrderQty = *input.MinOrderQty
	}
	if input.IsFeatured != nil {
		item.IsFeatured = *input.IsFeatured
	}
	if input.Description != nil {
		item.Description = truncate(strings.TrimSpace(*input.Description), 2000)
	}
	if err := utils.DB.Save(&item).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update catalogue"})
		return
	}
	c.JSON(http.StatusOK, item)
}

// BulkUpdateB2BCatalog lists or unlists many products at once.
// action: "list" | "unlist" (with product_ids) or "list_in_stock".
// b2bMaxImageURLLen caps a stored image (inline data: URLs included) at ~4 MB.
const b2bMaxImageURLLen = 4 << 20

func validB2BImageURL(u string) bool {
	if len(u) > b2bMaxImageURLLen {
		return false
	}
	lower := strings.ToLower(u)
	return strings.HasPrefix(lower, "data:image/") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://") || strings.HasPrefix(u, "/")
}

// GetB2BCatalogImages returns a product's main image and its gallery, for
// managing pictures from the shop catalogue.
func GetB2BCatalogImages(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var product models.Product
	if err := utils.DB.Select("id, image_url").Where("id = ? AND user_id = ?", c.Param("product_id"), userID).First(&product).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Product not found"})
		return
	}
	var images []models.ProductImage
	utils.DB.Where("user_id = ? AND product_id = ?", userID, product.ID).Order("sort_order ASC, created_at ASC").Find(&images)
	c.JSON(http.StatusOK, gin.H{"main_image": product.ImageUrl, "images": images})
}

// SetB2BCatalogMainImage sets (or clears, with an empty URL) the product's
// main image, which is the picture the storefront shows first.
func SetB2BCatalogMainImage(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var input struct {
		ImageURL string `json:"image_url"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	input.ImageURL = strings.TrimSpace(input.ImageURL)
	if input.ImageURL != "" && !validB2BImageURL(input.ImageURL) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Image must be an uploaded picture or an http(s) URL under 4 MB"})
		return
	}
	res := utils.DB.Model(&models.Product{}).Where("id = ? AND user_id = ?", c.Param("product_id"), userID).Update("image_url", input.ImageURL)
	if res.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update image"})
		return
	}
	if res.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Product not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"image_url": input.ImageURL})
}

func BulkUpdateB2BCatalog(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var input struct {
		Action     string      `json:"action" binding:"required"`
		ProductIDs []uuid.UUID `json:"product_ids"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var ids []uuid.UUID
	listed := true
	switch input.Action {
	case "list", "unlist":
		listed = input.Action == "list"
		if len(input.ProductIDs) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Select at least one product"})
			return
		}
		utils.DB.Model(&models.Product{}).Where("user_id = ? AND id IN ?", userID, input.ProductIDs).Pluck("id", &ids)
	case "list_in_stock":
		utils.DB.Table("products AS p").
			Where("p.user_id = ? AND p.deleted_at IS NULL AND p.is_active = ? AND "+b2bStockSubquery+" > 0", userID, true).
			Pluck("p.id", &ids)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "Unknown action"})
		return
	}
	if len(ids) == 0 {
		c.JSON(http.StatusOK, gin.H{"updated": 0})
		return
	}

	err := utils.DB.Transaction(func(tx *gorm.DB) error {
		var existing []uuid.UUID
		if err := tx.Model(&models.B2BCatalogItem{}).Where("user_id = ? AND product_id IN ?", userID, ids).
			Pluck("product_id", &existing).Error; err != nil {
			return err
		}
		if len(existing) > 0 {
			if err := tx.Model(&models.B2BCatalogItem{}).Where("user_id = ? AND product_id IN ?", userID, existing).
				Update("is_listed", listed).Error; err != nil {
				return err
			}
		}
		if !listed {
			return nil
		}
		have := map[uuid.UUID]bool{}
		for _, id := range existing {
			have[id] = true
		}
		var fresh []models.B2BCatalogItem
		for _, id := range ids {
			if !have[id] {
				fresh = append(fresh, models.B2BCatalogItem{ID: uuid.New(), UserID: userID, ProductID: id, IsListed: true, MinOrderQty: 1})
			}
		}
		if len(fresh) == 0 {
			return nil
		}
		return tx.CreateInBatches(fresh, 200).Error
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update catalogue"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"updated": len(ids)})
}

// ---------------------------------------------------------------------------
// Orders
// ---------------------------------------------------------------------------

// b2bOrderFilters applies the order list's search and date filters. prefix
// qualifies the b2b_orders columns (e.g. "o.") when the query joins tables.
func b2bOrderFilters(c *gin.Context, q *gorm.DB, prefix string) *gorm.DB {
	if search := strings.TrimSpace(c.Query("search")); search != "" {
		like := "%" + strings.ToLower(search) + "%"
		q = q.Where(fmt.Sprintf("(LOWER(%[1]sorder_number) LIKE ? OR LOWER(%[1]sbuyer_name) LIKE ? OR %[1]sbuyer_phone LIKE ? OR LOWER(%[1]scity) LIKE ?)", prefix),
			like, like, "%"+search+"%", like)
	}
	if from := strings.TrimSpace(c.Query("from")); from != "" {
		if t, err := time.ParseInLocation("2006-01-02", from, time.Local); err == nil {
			q = q.Where(prefix+"created_at >= ?", t)
		}
	}
	if to := strings.TrimSpace(c.Query("to")); to != "" {
		if t, err := time.ParseInLocation("2006-01-02", to, time.Local); err == nil {
			q = q.Where(prefix+"created_at < ?", t.AddDate(0, 0, 1))
		}
	}
	return q
}

func ListB2BOrders(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	base := b2bOrderFilters(c, utils.DB.Model(&models.B2BOrder{}).Where("user_id = ?", userID), "")

	// Counts per status for the tab badges (ignores the status filter itself).
	var statusRows []struct {
		Status string
		Count  int64
		Amount float64
	}
	base.Session(&gorm.Session{}).Select("status, COUNT(*) AS count, COALESCE(SUM(total_amount), 0) AS amount").
		Group("status").Scan(&statusRows)
	counts := map[string]int64{"all": 0}
	amounts := map[string]float64{}
	for _, r := range statusRows {
		counts[r.Status] = r.Count
		amounts[r.Status] = r.Amount
		counts["all"] += r.Count
	}

	q := base.Session(&gorm.Session{})
	if status := strings.TrimSpace(c.Query("status")); status != "" && status != "all" {
		q = q.Where("status = ?", status)
	}
	var total int64
	q.Session(&gorm.Session{}).Count(&total)

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	perPage, _ := strconv.Atoi(c.DefaultQuery("per_page", "25"))
	if perPage < 1 || perPage > 200 {
		perPage = 25
	}

	var orders []models.B2BOrder
	if err := q.Order("created_at DESC").Limit(perPage).Offset((page - 1) * perPage).Find(&orders).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load orders"})
		return
	}
	syncB2BOrderInvoiceLinks(userID, orders)

	c.JSON(http.StatusOK, gin.H{
		"orders":   orders,
		"total":    total,
		"page":     page,
		"per_page": perPage,
		"counts":   counts,
		"amounts":  amounts,
	})
}

type b2bPendingItemRow struct {
	ProductID  *uuid.UUID `json:"product_id"`
	Name       string     `json:"name"`
	Unit       string     `json:"unit"`
	Quantity   float64    `json:"quantity"`
	Amount     float64    `json:"amount"`
	OrderCount int64      `json:"order_count"`
	StockQty   *float64   `json:"stock_qty" gorm:"-"`
	// Orders breaks the consolidated quantity down by pending order.
	Orders []b2bPendingItemOrder `json:"orders" gorm:"-"`
}

type b2bPendingItemOrder struct {
	OrderID     uuid.UUID  `json:"order_id"`
	OrderNumber string     `json:"order_number"`
	BuyerName   string     `json:"buyer_name"`
	CreatedAt   time.Time  `json:"created_at"`
	ProductID   *uuid.UUID `json:"-"`
	Name        string     `json:"-"`
	Unit        string     `json:"-"`
	Quantity    float64    `json:"quantity"`
	Amount      float64    `json:"amount"`
}

// b2bPendingItemKey mirrors ListB2BPendingItems' GROUP BY: product, or name
// when the product was deleted, plus unit.
func b2bPendingItemKey(productID *uuid.UUID, name, unit string) string {
	if productID != nil {
		return productID.String() + "|" + unit
	}
	return "name:" + strings.ToLower(name) + "|" + unit
}

// ListB2BPendingItems consolidates the line items of all pending orders
// (honouring the list's search/date filters) into one row per product, so
// staff can see the total quantity they need to fulfil. Lines whose product
// was deleted are grouped by name and unit instead.
//
// The response is paginated (page/per_page). total, short and amount cover
// every consolidated item, not just the returned page; the per-order
// breakdown is loaded only for the page's items.
func ListB2BPendingItems(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	rows, err := loadB2BPendingItemSummaries(c, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load pending items"})
		return
	}

	var short int64
	var amount float64
	for _, r := range rows {
		if r.StockQty != nil && *r.StockQty < r.Quantity {
			short++
		}
		amount += r.Amount
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	perPage, _ := strconv.Atoi(c.DefaultQuery("per_page", "25"))
	if perPage < 1 || perPage > 200 {
		perPage = 25
	}
	total := len(rows)
	lo := (page - 1) * perPage
	if lo > total {
		lo = total
	}
	hi := lo + perPage
	if hi > total {
		hi = total
	}
	pageRows := rows[lo:hi]

	if err := attachB2BPendingItemOrders(c, userID, pageRows); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load pending items"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"items":    pageRows,
		"total":    total,
		"page":     page,
		"per_page": perPage,
		"short":    short,
		"amount":   amount,
	})
}

// b2bPendingItemsBase joins pending orders' line items, honouring the list's
// search/date filters.
func b2bPendingItemsBase(c *gin.Context, userID uuid.UUID) *gorm.DB {
	q := utils.DB.Table("b2b_order_items AS i").
		Joins("JOIN b2b_orders AS o ON o.id = i.order_id AND o.user_id = i.user_id AND o.deleted_at IS NULL").
		Where("i.user_id = ? AND o.status = ?", userID, models.B2BOrderPending)
	return b2bOrderFilters(c, q, "o.")
}

// loadB2BPendingItems builds every consolidated pending-item row with its
// per-order breakdown (used by the export).
func loadB2BPendingItems(c *gin.Context, userID uuid.UUID) ([]b2bPendingItemRow, error) {
	rows, err := loadB2BPendingItemSummaries(c, userID)
	if err != nil {
		return nil, err
	}
	if err := attachB2BPendingItemOrders(c, userID, rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// loadB2BPendingItemSummaries returns the consolidated rows with stock, but
// without the per-order breakdown.
func loadB2BPendingItemSummaries(c *gin.Context, userID uuid.UUID) ([]b2bPendingItemRow, error) {
	var rows []b2bPendingItemRow
	if err := b2bPendingItemsBase(c, userID).Select(`i.product_id, MAX(i.name) AS name, i.unit,
			COALESCE(SUM(i.quantity), 0) AS quantity, COALESCE(SUM(i.total), 0) AS amount,
			COUNT(DISTINCT i.order_id) AS order_count`).
		Group("i.product_id, CASE WHEN i.product_id IS NULL THEN LOWER(i.name) END, i.unit").
		Order("quantity DESC, name ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}

	productIDs := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		if r.ProductID != nil {
			productIDs = append(productIDs, *r.ProductID)
		}
	}
	stock := b2bStockByProduct(userID, productIDs)
	for i := range rows {
		rows[i].Orders = []b2bPendingItemOrder{}
		if rows[i].ProductID != nil {
			s := stock[*rows[i].ProductID]
			rows[i].StockQty = &s
		}
	}
	if rows == nil {
		rows = []b2bPendingItemRow{}
	}
	return rows, nil
}

// attachB2BPendingItemOrders fills each row's per-order breakdown, oldest
// order first (fulfilment order). Only the given rows' lines are queried.
func attachB2BPendingItemOrders(c *gin.Context, userID uuid.UUID, rows []b2bPendingItemRow) error {
	if len(rows) == 0 {
		return nil
	}
	var productIDs []uuid.UUID
	var names []string
	for _, r := range rows {
		if r.ProductID != nil {
			productIDs = append(productIDs, *r.ProductID)
		} else {
			names = append(names, strings.ToLower(r.Name))
		}
	}
	q := b2bPendingItemsBase(c, userID)
	switch {
	case len(productIDs) > 0 && len(names) > 0:
		q = q.Where("(i.product_id IN ? OR (i.product_id IS NULL AND LOWER(i.name) IN ?))", productIDs, names)
	case len(productIDs) > 0:
		q = q.Where("i.product_id IN ?", productIDs)
	default:
		q = q.Where("i.product_id IS NULL AND LOWER(i.name) IN ?", names)
	}

	var lines []b2bPendingItemOrder
	if err := q.Select(`o.id AS order_id, o.order_number, o.buyer_name, o.created_at,
			i.product_id, i.name, i.unit,
			COALESCE(SUM(i.quantity), 0) AS quantity, COALESCE(SUM(i.total), 0) AS amount`).
		Group("o.id, o.order_number, o.buyer_name, o.created_at, i.product_id, i.name, i.unit").
		Order("o.created_at ASC, o.order_number ASC").
		Scan(&lines).Error; err != nil {
		return err
	}
	byKey := make(map[string]int, len(rows))
	for i := range rows {
		rows[i].Orders = []b2bPendingItemOrder{}
		byKey[b2bPendingItemKey(rows[i].ProductID, rows[i].Name, rows[i].Unit)] = i
	}
	for _, l := range lines {
		if i, ok := byKey[b2bPendingItemKey(l.ProductID, l.Name, l.Unit)]; ok {
			// One order can carry the same product on several lines with
			// different names; merge them into a single order row.
			orders := rows[i].Orders
			if n := len(orders); n > 0 && orders[n-1].OrderID == l.OrderID {
				orders[n-1].Quantity += l.Quantity
				orders[n-1].Amount += l.Amount
				continue
			}
			rows[i].Orders = append(orders, l)
		}
	}
	return nil
}

func loadB2BOrder(c *gin.Context) (*models.B2BOrder, bool) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var order models.B2BOrder
	if err := utils.DB.Preload("Items", func(db *gorm.DB) *gorm.DB { return db.Order("created_at ASC") }).
		Where("id = ? AND user_id = ?", c.Param("id"), userID).First(&order).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Order not found"})
		return nil, false
	}
	orders := []models.B2BOrder{order}
	syncB2BOrderInvoiceLinks(userID, orders)
	order = orders[0]
	return &order, true
}

func b2bOrderDetailResponse(order *models.B2BOrder) gin.H {
	var party models.Party
	partyInfo := gin.H(nil)
	if utils.DB.Where("id = ? AND user_id = ?", order.PartyID, order.UserID).First(&party).Error == nil {
		partyInfo = gin.H{"id": party.ID, "name": party.Name, "phone": party.Phone, "balance": party.Balance, "gstin": party.GSTIN}
	}
	var quotationInfo, invoiceInfo gin.H
	if order.QuotationID != nil {
		var q models.Quotation
		if utils.DB.Where("id = ? AND user_id = ?", *order.QuotationID, order.UserID).First(&q).Error == nil {
			quotationInfo = gin.H{"id": q.ID, "quotation_number": q.QuotationNumber, "status": q.Status, "total_amount": q.TotalAmount}
		}
	}
	if order.InvoiceID != nil {
		var inv models.Invoice
		if utils.DB.Where("id = ? AND user_id = ?", *order.InvoiceID, order.UserID).First(&inv).Error == nil {
			invoiceInfo = gin.H{"id": inv.ID, "invoice_number": inv.InvoiceNumber, "status": inv.Status, "total_amount": inv.TotalAmount}
		}
	}
	productIDs := make([]uuid.UUID, 0, len(order.Items))
	for _, it := range order.Items {
		if it.ProductID != nil {
			productIDs = append(productIDs, *it.ProductID)
		}
	}
	return gin.H{
		"order":     order,
		"party":     partyInfo,
		"quotation": quotationInfo,
		"invoice":   invoiceInfo,
		"stock":     b2bStockByProduct(order.UserID, productIDs),
	}
}

func GetB2BOrder(c *gin.Context) {
	order, ok := loadB2BOrder(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, b2bOrderDetailResponse(order))
}

// UpdateB2BOrderStatus moves an order along its lifecycle. Forward steps may
// be skipped; cancellation is allowed until delivery unless invoiced; a
// cancelled order can be reopened as pending.
func UpdateB2BOrderStatus(c *gin.Context) {
	order, ok := loadB2BOrder(c)
	if !ok {
		return
	}
	var input struct {
		Status string `json:"status" binding:"required"`
		Note   string `json:"note"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	target := strings.ToLower(strings.TrimSpace(input.Status))
	if target == order.Status {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Order is already " + target})
		return
	}

	switch {
	case target == models.B2BOrderCancelled:
		if order.Status == models.B2BOrderDelivered {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Delivered orders cannot be cancelled"})
			return
		}
		if order.InvoiceID != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "This order has been invoiced. Cancel or return the invoice instead."})
			return
		}
	case order.Status == models.B2BOrderCancelled:
		if target != models.B2BOrderPending {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Reopen a cancelled order as pending first"})
			return
		}
	default:
		to, okTo := b2bStatusRank[target]
		from := b2bStatusRank[order.Status]
		if !okTo {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Unknown status"})
			return
		}
		if to <= from {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Cannot move an order from %s back to %s", order.Status, target)})
			return
		}
	}

	note := truncate(strings.TrimSpace(input.Note), 500)
	order.Status = target
	appendB2BOrderEvent(order, target, note, b2bActorName(c))
	if err := utils.DB.Model(order).Select("status", "timeline").Updates(order).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update order"})
		return
	}
	c.JSON(http.StatusOK, b2bOrderDetailResponse(order))
}

// UpdateB2BOrder edits internal notes and — before an estimate exists —
// line quantities and prices (quantity 0 removes a line).
func UpdateB2BOrder(c *gin.Context) {
	order, ok := loadB2BOrder(c)
	if !ok {
		return
	}
	var input struct {
		InternalNotes *string `json:"internal_notes"`
		Items         []struct {
			ID        uuid.UUID `json:"id"`
			Quantity  float64   `json:"quantity"`
			UnitPrice float64   `json:"unit_price"`
		} `json:"items"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if input.InternalNotes != nil {
		order.InternalNotes = truncate(strings.TrimSpace(*input.InternalNotes), 2000)
	}

	itemsChanged := false
	var removed []uuid.UUID
	if input.Items != nil {
		editable := order.QuotationID == nil &&
			(order.Status == models.B2BOrderPending || order.Status == models.B2BOrderConfirmed)
		if !editable {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Items can only be edited on pending or confirmed orders that have no estimate yet"})
			return
		}
		edits := map[uuid.UUID]int{}
		for i, e := range input.Items {
			if e.Quantity < 0 || e.Quantity > b2bMaxLineQuantity || e.UnitPrice < 0 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid quantity or price"})
				return
			}
			edits[e.ID] = i
		}
		kept := order.Items[:0]
		for _, it := range order.Items {
			idx, ok := edits[it.ID]
			if !ok {
				kept = append(kept, it)
				continue
			}
			e := input.Items[idx]
			if e.Quantity == 0 {
				removed = append(removed, it.ID)
				itemsChanged = true
				continue
			}
			if e.Quantity != it.Quantity || e.UnitPrice != it.UnitPrice {
				it.Quantity = e.Quantity
				it.UnitPrice = e.UnitPrice
				itemsChanged = true
			}
			kept = append(kept, it)
		}
		if len(kept) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "An order needs at least one item. Cancel the order instead."})
			return
		}
		order.Items = kept
		recalcB2BOrderTotals(order)
		if itemsChanged {
			appendB2BOrderEvent(order, order.Status, "Order items updated", b2bActorName(c))
		}
	}

	err := utils.DB.Transaction(func(tx *gorm.DB) error {
		if len(removed) > 0 {
			if err := tx.Where("order_id = ? AND id IN ?", order.ID, removed).Delete(&models.B2BOrderItem{}).Error; err != nil {
				return err
			}
		}
		if itemsChanged {
			for _, it := range order.Items {
				if err := tx.Model(&models.B2BOrderItem{}).Where("id = ? AND order_id = ?", it.ID, order.ID).
					Updates(map[string]interface{}{"quantity": it.Quantity, "unit_price": it.UnitPrice, "total": it.Total}).Error; err != nil {
					return err
				}
			}
		}
		return tx.Model(order).
			Select("internal_notes", "timeline", "item_count", "total_qty", "total_amount").
			Updates(order).Error
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update order"})
		return
	}
	c.JSON(http.StatusOK, b2bOrderDetailResponse(order))
}

// CreateB2BOrderEstimate turns the order into an Estimate so it can be billed
// through the standard estimate → invoice conversion (which handles stock,
// accounting and party balances). Idempotent: an existing estimate is reused.
func CreateB2BOrderEstimate(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	order, ok := loadB2BOrder(c)
	if !ok {
		return
	}
	if order.Status == models.B2BOrderCancelled {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cancelled orders cannot be billed"})
		return
	}

	if order.QuotationID != nil {
		var existing models.Quotation
		if utils.DB.Where("id = ? AND user_id = ?", *order.QuotationID, userID).First(&existing).Error == nil {
			c.JSON(http.StatusOK, gin.H{
				"quotation": gin.H{"id": existing.ID, "quotation_number": existing.QuotationNumber, "total_amount": existing.TotalAmount, "status": existing.Status},
				"created":   false,
			})
			return
		}
	}

	var party models.Party
	if err := utils.DB.Where("id = ? AND user_id = ?", order.PartyID, userID).First(&party).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "The buyer's party record no longer exists"})
		return
	}

	notes := fmt.Sprintf("B2B order %s", order.OrderNumber)
	if order.Notes != "" {
		notes += "\nBuyer notes: " + order.Notes
	}
	now := time.Now()
	quotation := models.Quotation{
		ID:              uuid.New(),
		UserID:          userID,
		QuotationNumber: allocateUniqueQuotationNumber(userID),
		PartyID:         party.ID,
		Date:            now,
		Status:          "accepted",
		ApprovalStatus:  "approved",
		Notes:           notes,
		PlaceOfSupply:   order.State,
		Version:         1,
	}
	var subTotal float64
	for _, it := range order.Items {
		quotation.Items = append(quotation.Items, models.QuotationItem{
			ID:          uuid.New(),
			ProductID:   it.ProductID,
			Description: it.Name,
			Quantity:    it.Quantity,
			Unit:        it.Unit,
			UnitPrice:   it.UnitPrice,
			Total:       it.Total,
			HSNCode:     it.HSNCode,
		})
		subTotal += it.Total
	}
	// Billing is tax-free across the app (see CreateQuotation), so the
	// estimate total is the plain line sum.
	quotation.SubTotal = roundMoney(subTotal)
	quotation.TotalAmount = roundMoney(subTotal)

	err := utils.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&quotation).Error; err != nil {
			return err
		}
		data, _ := json.Marshal(quotation)
		if err := tx.Create(&models.QuotationVersion{
			ID: uuid.New(), QuotationID: quotation.ID, VersionNumber: 1,
			QuotationData: string(data), ChangeReason: "Created from B2B order " + order.OrderNumber, CreatedBy: actorUserID(c),
		}).Error; err != nil {
			return err
		}
		order.QuotationID = &quotation.ID
		cols := []interface{}{"timeline"}
		if order.Status == models.B2BOrderPending {
			order.Status = models.B2BOrderConfirmed
			cols = append(cols, "status")
		}
		appendB2BOrderEvent(order, order.Status, "Estimate "+quotation.QuotationNumber+" created", b2bActorName(c))
		return tx.Model(order).Select("quotation_id", cols...).Updates(order).Error
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create estimate"})
		return
	}

	CreateAuditLog(userID, b2bActorName(c), "create", "quotation", &quotation.ID, quotation.QuotationNumber,
		fmt.Sprintf("Created estimate %s from B2B order %s", quotation.QuotationNumber, order.OrderNumber),
		c.ClientIP(), c.GetHeader("User-Agent"),
		map[string]interface{}{"b2b_order": order.OrderNumber, "total_amount": quotation.TotalAmount}, "success", "")

	c.JSON(http.StatusCreated, gin.H{
		"quotation": gin.H{"id": quotation.ID, "quotation_number": quotation.QuotationNumber, "total_amount": quotation.TotalAmount, "status": quotation.Status},
		"created":   true,
	})
}

// DeleteB2BOrder removes spam or test orders. Only cancelled orders, or
// pending ones without an estimate, can be deleted.
func DeleteB2BOrder(c *gin.Context) {
	order, ok := loadB2BOrder(c)
	if !ok {
		return
	}
	deletable := order.Status == models.B2BOrderCancelled ||
		(order.Status == models.B2BOrderPending && order.QuotationID == nil)
	if !deletable || order.InvoiceID != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Only cancelled orders, or pending orders without an estimate, can be deleted"})
		return
	}
	if err := utils.DB.Delete(order).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete order"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Order deleted"})
}
