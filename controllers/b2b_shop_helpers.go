package controllers

import (
	"fmt"
	"math"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const b2bOrderNumberPrefix = "B2B-"

// b2bStatusRank orders the forward fulfilment steps. Cancelled is handled
// separately because it can be reached from any non-final step.
var b2bStatusRank = map[string]int{
	models.B2BOrderPending:    0,
	models.B2BOrderConfirmed:  1,
	models.B2BOrderProcessing: 2,
	models.B2BOrderDispatched: 3,
	models.B2BOrderDelivered:  4,
}

// phoneKey reduces a phone number to its last 10 digits so "+91 98765-43210"
// and "9876543210" compare equal. Returns "" when fewer than 10 digits exist.
func phoneKey(phone string) string {
	d := normalizePhone(phone)
	if len(d) < 10 {
		return ""
	}
	return d[len(d)-10:]
}

// sqlPhoneDigits strips common phone formatting from a column in SQL so stored
// numbers like "+91 98765-43210" can be suffix-matched on digits. Portable
// across SQLite and Postgres.
func sqlPhoneDigits(column string) string {
	expr := column
	for _, ch := range []string{" ", "-", "+", "(", ")", "."} {
		expr = "REPLACE(" + expr + ", '" + ch + "', '')"
	}
	return expr
}

// maskPhone renders a phone as "••••••3210" for public display.
func maskPhone(phone string) string {
	d := normalizePhone(phone)
	if len(d) < 4 {
		return ""
	}
	return "••••••" + d[len(d)-4:]
}

func roundMoney(v float64) float64 {
	return math.Round(v*100) / 100
}

func getOrCreateB2BShopSettings(userID uuid.UUID) (*models.B2BShopSettings, error) {
	var settings models.B2BShopSettings
	err := utils.DB.Where("user_id = ?", userID).First(&settings).Error
	if err == nil {
		return &settings, nil
	}
	if err != gorm.ErrRecordNotFound {
		return nil, err
	}

	base := "shop"
	var business models.Business
	if utils.DB.Where("user_id = ?", userID).First(&business).Error == nil && strings.TrimSpace(business.Name) != "" {
		base = slugify(business.Name)
	}
	slug, err := uniqueB2BSlug(base, userID)
	if err != nil {
		return nil, err
	}

	settings = models.B2BShopSettings{
		ID:                     uuid.New(),
		UserID:                 userID,
		IsEnabled:              false,
		Slug:                   slug,
		Tagline:                "Wholesale ordering made simple",
		ShowStock:              true,
		ShowPrice:              true,
		AllowBackorders:        false,
		AllowNewBuyers:         true,
		RequireDeliveryAddress: true,
	}
	if err := utils.DB.Create(&settings).Error; err != nil {
		return nil, err
	}
	return &settings, nil
}

// reservedShopSlugs are top-level frontend routes. Storefronts live at
// /<slug>/shop, so a shop can't take a name that would shadow an app page.
var reservedShopSlugs = map[string]bool{
	"accounting": true, "announcements": true, "api": true, "attendance": true, "attendance-reminder": true,
	"audit": true, "b2b-orders": true, "b2b-shop": true, "cash-bank": true, "categories": true,
	"change-password-required": true, "credit-notes": true, "customer-portal": true, "dashboard": true,
	"debit-notes": true, "delivery-challans": true, "design-kit": true, "developer-settings": true,
	"discount": true, "e-invoicing": true, "email-marketing": true, "estimates": true,
	"expense-categories": true, "expenses": true, "forgot-password": true, "gst": true, "health": true,
	"inventory": true, "invoices": true, "login": true, "logo": true, "loyalty": true,
	"notification-center": true, "notifications": true, "parties": true, "payment-outs": true,
	"payments": true, "payroll": true, "portal": true, "pos": true, "products": true,
	"profit-distribution": true, "purchase-invoices": true, "purchase-orders": true,
	"purchase-returns": true, "purchases": true, "register": true, "reports": true,
	"reset-password": true, "sales-returns": true, "settings": true, "shop": true,
	"sms-marketing": true, "staff": true, "static": true, "stores": true, "support": true,
	"support-requests": true, "uploads": true, "user-management": true, "warehouses": true,
	"whatsapp-marketing": true,
}

func isReservedShopSlug(slug string) bool {
	return reservedShopSlugs[slug] || strings.HasPrefix(slug, "_next")
}

// uniqueB2BSlug returns base, or base-N, that no other store's shop uses.
func uniqueB2BSlug(base string, userID uuid.UUID) (string, error) {
	slug := base
	for i := 1; i < 1000; i++ {
		if isReservedShopSlug(slug) {
			slug = fmt.Sprintf("%s-%d", base, i)
			continue
		}
		var count int64
		if err := utils.DB.Unscoped().Model(&models.B2BShopSettings{}).
			Where("slug = ? AND user_id <> ?", slug, userID).Count(&count).Error; err != nil {
			return "", err
		}
		if count == 0 {
			return slug, nil
		}
		slug = fmt.Sprintf("%s-%d", base, i)
	}
	return fmt.Sprintf("%s-%s", base, uuid.NewString()[:8]), nil
}

// b2bStockByProduct returns the available quantity for each product.
func b2bStockByProduct(userID uuid.UUID, productIDs []uuid.UUID) map[uuid.UUID]float64 {
	out := map[uuid.UUID]float64{}
	if len(productIDs) == 0 {
		return out
	}
	var rows []struct {
		ProductID uuid.UUID
		StockQty  float64
	}
	utils.DB.Model(&models.InventoryStock{}).
		Select("product_id, COALESCE(SUM(available_qty), 0) AS stock_qty").
		Where("user_id = ? AND product_id IN ?", userID, productIDs).
		Group("product_id").
		Scan(&rows)
	for _, r := range rows {
		out[r.ProductID] = r.StockQty
	}
	return out
}

// b2bPrimaryImages fills in an image for products whose image_url is empty,
// using their primary (or first) gallery image.
func b2bPrimaryImages(userID uuid.UUID, productIDs []uuid.UUID) map[uuid.UUID]string {
	out := map[uuid.UUID]string{}
	if len(productIDs) == 0 {
		return out
	}
	var images []models.ProductImage
	utils.DB.Where("user_id = ? AND product_id IN ?", userID, productIDs).
		Order("is_primary DESC, sort_order ASC, created_at ASC").
		Find(&images)
	for _, img := range images {
		if _, ok := out[img.ProductID]; !ok {
			out[img.ProductID] = img.ImageURL
		}
	}
	return out
}

// b2bEffectivePrice is the wholesale price when set, else the sale price.
func b2bEffectivePrice(item models.B2BCatalogItem, product models.Product) float64 {
	if item.B2BPrice > 0 {
		return item.B2BPrice
	}
	return product.SalePrice
}

func b2bMinQty(item models.B2BCatalogItem) float64 {
	if item.MinOrderQty > 0 {
		return item.MinOrderQty
	}
	return 1
}

// allocateB2BOrderNumber returns the next B2B-XXXX number for the store.
func allocateB2BOrderNumber(tx *gorm.DB, userID uuid.UUID) string {
	var numbers []string
	tx.Unscoped().Model(&models.B2BOrder{}).
		Where("user_id = ? AND order_number LIKE ?", userID, b2bOrderNumberPrefix+"%").
		Pluck("order_number", &numbers)
	var maxSeq int64
	for _, n := range numbers {
		if seq := trailingNumberSequence(n); seq > maxSeq {
			maxSeq = seq
		}
	}
	return fmt.Sprintf("%s%04d", b2bOrderNumberPrefix, maxSeq+1)
}

// recalcB2BOrderTotals recomputes the aggregate columns from the items.
func recalcB2BOrderTotals(order *models.B2BOrder) {
	var qty, total float64
	for i := range order.Items {
		order.Items[i].Total = roundMoney(order.Items[i].Quantity * order.Items[i].UnitPrice)
		qty += order.Items[i].Quantity
		total += order.Items[i].Total
	}
	order.ItemCount = len(order.Items)
	order.TotalQty = qty
	order.TotalAmount = roundMoney(total)
}

func appendB2BOrderEvent(order *models.B2BOrder, status, note, by string) {
	order.Timeline = append(order.Timeline, models.B2BOrderEvent{
		Status: status,
		Note:   note,
		By:     by,
		At:     time.Now(),
	})
}

// syncB2BOrderInvoiceLinks records the invoice on orders whose estimate has
// since been converted through the regular estimate → invoice flow.
func syncB2BOrderInvoiceLinks(userID uuid.UUID, orders []models.B2BOrder) {
	quotationToOrder := map[uuid.UUID]int{}
	var quotationIDs []uuid.UUID
	for i, o := range orders {
		if o.QuotationID != nil && o.InvoiceID == nil {
			quotationToOrder[*o.QuotationID] = i
			quotationIDs = append(quotationIDs, *o.QuotationID)
		}
	}
	if len(quotationIDs) == 0 {
		return
	}
	var quotations []models.Quotation
	utils.DB.Select("id, converted_to_invoice_id").
		Where("user_id = ? AND id IN ? AND converted_to_invoice_id IS NOT NULL", userID, quotationIDs).
		Find(&quotations)
	for _, q := range quotations {
		idx, ok := quotationToOrder[q.ID]
		if !ok || q.ConvertedToInvoiceID == nil {
			continue
		}
		invoiceID := *q.ConvertedToInvoiceID
		orders[idx].InvoiceID = &invoiceID
		utils.DB.Model(&models.B2BOrder{}).Where("id = ?", orders[idx].ID).Update("invoice_id", invoiceID)
	}
}

// notifyNewB2BOrder drops an in-app notification for the store team.
func notifyNewB2BOrder(order *models.B2BOrder) {
	now := time.Now()
	n := models.Notification{
		ID:          uuid.New(),
		UserID:      order.UserID,
		Type:        "b2b_order",
		Title:       fmt.Sprintf("New B2B order %s", order.OrderNumber),
		Message:     fmt.Sprintf("%s placed an order for %d item(s) worth %.2f", order.BuyerName, order.ItemCount, order.TotalAmount),
		Channels:    "internal",
		RelatedID:   &order.ID,
		RelatedType: "b2b_order",
		Priority:    "high",
		Status:      "sent",
		ScheduledAt: &now,
		SentAt:      &now,
	}
	if err := utils.DB.Create(&n).Error; err != nil {
		fmt.Printf("[b2b-shop] failed to create order notification: %v\n", err)
	}
}
