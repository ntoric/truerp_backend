package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// B2BShopSettings configures a store's public wholesale storefront. One row
// per store data scope (UserID = store owner). The storefront is reached at
// /<Slug>/shop; nothing is visible publicly until IsEnabled is set.
type B2BShopSettings struct {
	ID        uuid.UUID `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID    uuid.UUID `json:"user_id" gorm:"type:uuid;not null;uniqueIndex"`
	IsEnabled bool      `json:"is_enabled" gorm:"default:false"`
	Slug      string    `json:"slug" gorm:"not null;uniqueIndex"`
	// ShopName overrides the business name on the storefront when set.
	ShopName     string `json:"shop_name"`
	Tagline      string `json:"tagline"`
	Announcement string `json:"announcement"`
	ContactPhone string `json:"contact_phone"`
	ContactEmail string `json:"contact_email"`
	// MinOrderValue is the smallest order total the shop accepts (0 = none).
	MinOrderValue float64 `json:"min_order_value" gorm:"default:0"`
	// ShowStock exposes available quantities to buyers.
	ShowStock bool `json:"show_stock" gorm:"default:true"`
	// ShowPrice displays wholesale prices on the storefront's product cards.
	ShowPrice bool `json:"show_price" gorm:"default:true"`
	// AllowBackorders lets buyers order more than the available stock.
	AllowBackorders bool `json:"allow_backorders" gorm:"default:false"`
	// HideOutOfStock leaves goods with no available stock out of the
	// storefront listing, categories and related products. Services are
	// always shown.
	HideOutOfStock bool `json:"hide_out_of_stock" gorm:"default:false"`
	// AllowNewBuyers lets unknown businesses register while placing an order.
	AllowNewBuyers bool `json:"allow_new_buyers" gorm:"default:true"`
	// RequireDeliveryAddress makes buyers give an address at checkout.
	RequireDeliveryAddress bool           `json:"require_delivery_address" gorm:"default:true"`
	Terms                  string         `json:"terms" gorm:"type:text"`
	CreatedAt              time.Time      `json:"created_at"`
	UpdatedAt              time.Time      `json:"updated_at"`
	DeletedAt              gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

// B2BCatalogItem marks a product as listed on the storefront, with optional
// wholesale pricing. A product without a row (or with IsListed=false) is not
// shown publicly. B2BPrice 0 falls back to the product's sale price.
type B2BCatalogItem struct {
	ID          uuid.UUID `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID      uuid.UUID `json:"user_id" gorm:"type:uuid;not null;uniqueIndex:idx_b2b_catalog_product"`
	ProductID   uuid.UUID `json:"product_id" gorm:"type:uuid;not null;uniqueIndex:idx_b2b_catalog_product"`
	IsListed    bool      `json:"is_listed" gorm:"not null;default:false"`
	B2BPrice    float64   `json:"b2b_price" gorm:"column:b2b_price;default:0"`
	MinOrderQty float64   `json:"min_order_qty" gorm:"default:1"`
	IsFeatured  bool      `json:"is_featured" gorm:"default:false"`
	// Description overrides the product description on the storefront.
	Description string    `json:"description" gorm:"type:text"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// B2B order lifecycle.
const (
	B2BOrderPending    = "pending"
	B2BOrderConfirmed  = "confirmed"
	B2BOrderProcessing = "processing"
	B2BOrderDispatched = "dispatched"
	B2BOrderDelivered  = "delivered"
	B2BOrderCancelled  = "cancelled"
)

// B2BOrderEvent is one entry of an order's status timeline.
type B2BOrderEvent struct {
	Status string    `json:"status"`
	Note   string    `json:"note,omitempty"`
	By     string    `json:"by,omitempty"`
	At     time.Time `json:"at"`
}

// B2BOrder is an order request placed from the public storefront. It does
// not move stock or touch accounts; fulfilment goes through an Estimate
// (QuotationID) that is converted to an invoice with the regular flow.
// Buyer fields are a snapshot taken at order time.
type B2BOrder struct {
	ID          uuid.UUID `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID      uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_b2b_order_number,priority:1"`
	OrderNumber string    `json:"order_number" gorm:"not null;uniqueIndex:idx_b2b_order_number,priority:2"`
	PartyID     uuid.UUID `json:"party_id" gorm:"type:uuid;not null;index"`
	Status      string    `json:"status" gorm:"not null;default:'pending';index"`
	// BuyerVerified is true when the buyer proved the party's registered phone
	// (or registered as a new business with that phone).
	BuyerVerified bool `json:"buyer_verified" gorm:"default:false"`
	IsNewBuyer    bool `json:"is_new_buyer" gorm:"default:false"`
	// BuyerMatch records how the order was tied to its party:
	// "selected" (buyer picked it from the lookup), "phone", "gstin" or
	// "name" (a sign-up matched an existing party), or "created" (new party).
	BuyerMatch      string          `json:"buyer_match"`
	BuyerName       string          `json:"buyer_name"`
	ContactPerson   string          `json:"contact_person"`
	BuyerPhone      string          `json:"buyer_phone"`
	BuyerEmail      string          `json:"buyer_email"`
	BuyerGSTIN      string          `json:"buyer_gstin"`
	ShippingAddress string          `json:"shipping_address"`
	City            string          `json:"city"`
	State           string          `json:"state"`
	Pincode         string          `json:"pincode"`
	Notes           string          `json:"notes" gorm:"type:text"`
	InternalNotes   string          `json:"internal_notes" gorm:"type:text"`
	ItemCount       int             `json:"item_count" gorm:"default:0"`
	TotalQty        float64         `json:"total_qty" gorm:"default:0"`
	TotalAmount     float64         `json:"total_amount" gorm:"default:0"`
	QuotationID     *uuid.UUID      `json:"quotation_id,omitempty" gorm:"type:uuid"`
	InvoiceID       *uuid.UUID      `json:"invoice_id,omitempty" gorm:"type:uuid"`
	Timeline        []B2BOrderEvent `json:"timeline" gorm:"type:text;serializer:json"`
	PlacedFromIP    string          `json:"-"`
	Items           []B2BOrderItem  `json:"items,omitempty" gorm:"foreignKey:OrderID;constraint:OnDelete:CASCADE;"`
	CreatedAt       time.Time       `json:"created_at" gorm:"index"`
	UpdatedAt       time.Time       `json:"updated_at"`
	DeletedAt       gorm.DeletedAt  `json:"deleted_at,omitempty" gorm:"index"`
}

// B2BOrderItem is one line of a B2B order. Name/unit/price are snapshots so
// the order stays readable if the product later changes or is deleted.
type B2BOrderItem struct {
	ID        uuid.UUID  `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID    uuid.UUID  `json:"user_id" gorm:"type:uuid;not null;index"`
	OrderID   uuid.UUID  `json:"order_id" gorm:"type:uuid;not null;index"`
	ProductID *uuid.UUID `json:"product_id,omitempty" gorm:"type:uuid;index"`
	Name      string     `json:"name" gorm:"not null"`
	Unit      string     `json:"unit"`
	HSNCode   string     `json:"hsn_code"`
	ImageURL  string     `json:"image_url"`
	Quantity  float64    `json:"quantity" gorm:"default:0"`
	UnitPrice float64    `json:"unit_price" gorm:"default:0"`
	Total     float64    `json:"total" gorm:"default:0"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// Explicit table names: GORM's default naming splits "B2B" into "b2_b",
// and the shop queries join these tables by name.
func (B2BShopSettings) TableName() string { return "b2b_shop_settings" }
func (B2BCatalogItem) TableName() string  { return "b2b_catalog_items" }
func (B2BOrder) TableName() string        { return "b2b_orders" }
func (B2BOrderItem) TableName() string    { return "b2b_order_items" }
