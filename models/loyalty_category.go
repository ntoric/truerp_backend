package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// LoyaltyCondition is one criterion of a loyalty category, e.g.
// "margin_percent gte 20". Join ("and" / "or") links it to the previous
// condition and is ignored on the first one. AND binds tighter than OR, so
// [A and B or C] means (A AND B) OR C.
type LoyaltyCondition struct {
	Field string  `json:"field"` // margin_percent | profit_amount
	Op    string  `json:"op"`    // gte | gt | lte | lt | eq
	Value float64 `json:"value"`
	Join  string  `json:"join"` // and | or
}

// LoyaltyConditions is stored as a JSON array in a text column.
type LoyaltyConditions []LoyaltyCondition

func (c LoyaltyConditions) Value() (driver.Value, error) {
	if c == nil {
		return "[]", nil
	}
	b, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func (c *LoyaltyConditions) Scan(value interface{}) error {
	var raw []byte
	switch v := value.(type) {
	case nil:
		*c = LoyaltyConditions{}
		return nil
	case string:
		raw = []byte(v)
	case []byte:
		raw = v
	default:
		return fmt.Errorf("cannot scan %T into LoyaltyConditions", value)
	}
	if len(raw) == 0 {
		*c = LoyaltyConditions{}
		return nil
	}
	return json.Unmarshal(raw, c)
}

// LoyaltyCategory is a reward slab used when LoyaltySettings.EarnBasis is
// "category". A sale (each item, or the whole invoice — see
// LoyaltySettings.CategoryScope) earns the reward of the first active
// category, by SortOrder, whose conditions it meets. A category with no
// conditions matches everything and works as a catch-all.
//
// The reward unit follows the store's reward type:
//   - cash mode:   RewardKind "percent" = RewardValue % of the base as ₹;
//     "fixed" = ₹ RewardValue per match.
//   - points mode: "percent" = RewardValue points per ₹100 of the base;
//     "fixed" = RewardValue points per match.
//
// RewardOn picks the base for "percent": "sale" (net sale value) or
// "profit" (sale − purchase cost).
type LoyaltyCategory struct {
	ID          uuid.UUID         `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID      uuid.UUID         `json:"user_id" gorm:"type:uuid;not null;index"`
	Name        string            `json:"name" gorm:"not null"`
	SortOrder   int               `json:"sort_order" gorm:"default:0"`
	IsActive    bool              `json:"is_active"` // no gorm default: a default would override an explicit false on create
	Conditions  LoyaltyConditions `json:"conditions" gorm:"type:text"`
	RewardKind  string            `json:"reward_kind" gorm:"default:'percent'"`
	RewardValue float64           `json:"reward_value" gorm:"default:0"`
	RewardOn    string            `json:"reward_on" gorm:"default:'sale'"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	DeletedAt   gorm.DeletedAt    `json:"deleted_at,omitempty" gorm:"index"`
}
