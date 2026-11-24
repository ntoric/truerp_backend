package controllers

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"truerp/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	LoyaltyEarnOnCategory = "category"

	LoyaltyCategoryScopeItem    = "item"
	LoyaltyCategoryScopeInvoice = "invoice"

	loyaltyCondMarginPercent = "margin_percent"
	loyaltyCondProfitAmount  = "profit_amount"

	loyaltyRewardKindPercent = "percent"
	loyaltyRewardKindFixed   = "fixed"

	loyaltyRewardOnSale   = "sale"
	loyaltyRewardOnProfit = "profit"

	maxLoyaltyCategories            = 50
	maxLoyaltyConditionsPerCategory = 10
)

// IsCategoryLoyalty reports whether rewards come from loyalty categories.
func IsCategoryLoyalty(settings *models.LoyaltySettings) bool {
	return settings != nil && settings.EarnBasis == LoyaltyEarnOnCategory
}

// loadLoyaltyCategories returns the user's categories in priority order.
func loadLoyaltyCategories(db *gorm.DB, userID uuid.UUID) []models.LoyaltyCategory {
	var cats []models.LoyaltyCategory
	db.Where("user_id = ?", userID).Order("sort_order ASC, created_at ASC").Find(&cats)
	if cats == nil {
		cats = []models.LoyaltyCategory{}
	}
	return cats
}

// validateLoyaltyCategories normalises categories sent by the client and
// returns them ready to save (IDs and sort order assigned).
func validateLoyaltyCategories(userID uuid.UUID, cats []models.LoyaltyCategory, cash bool) ([]models.LoyaltyCategory, error) {
	if len(cats) > maxLoyaltyCategories {
		return nil, fmt.Errorf("at most %d loyalty categories are allowed", maxLoyaltyCategories)
	}
	seen := map[string]bool{}
	out := make([]models.LoyaltyCategory, 0, len(cats))
	for i, c := range cats {
		c.Name = strings.TrimSpace(c.Name)
		if c.Name == "" {
			return nil, fmt.Errorf("category %d needs a name", i+1)
		}
		key := strings.ToLower(c.Name)
		if seen[key] {
			return nil, fmt.Errorf("category name %q is used twice", c.Name)
		}
		seen[key] = true

		if c.RewardKind == "" {
			c.RewardKind = loyaltyRewardKindPercent
		}
		if c.RewardKind != loyaltyRewardKindPercent && c.RewardKind != loyaltyRewardKindFixed {
			return nil, fmt.Errorf("%s: reward must be 'percent' or 'fixed'", c.Name)
		}
		if c.RewardOn == "" {
			c.RewardOn = loyaltyRewardOnSale
		}
		if c.RewardOn != loyaltyRewardOnSale && c.RewardOn != loyaltyRewardOnProfit {
			return nil, fmt.Errorf("%s: reward base must be 'sale' or 'profit'", c.Name)
		}
		if c.RewardValue <= 0 || math.IsNaN(c.RewardValue) || math.IsInf(c.RewardValue, 0) {
			return nil, fmt.Errorf("%s: reward value must be greater than 0", c.Name)
		}
		if cash && c.RewardKind == loyaltyRewardKindPercent && c.RewardValue > 100 {
			return nil, fmt.Errorf("%s: cashback percent cannot exceed 100", c.Name)
		}
		if !cash && c.RewardKind == loyaltyRewardKindFixed {
			c.RewardValue = math.Floor(c.RewardValue)
			if c.RewardValue < 1 {
				return nil, fmt.Errorf("%s: fixed points must be at least 1", c.Name)
			}
		}

		if len(c.Conditions) > maxLoyaltyConditionsPerCategory {
			return nil, fmt.Errorf("%s: at most %d conditions are allowed", c.Name, maxLoyaltyConditionsPerCategory)
		}
		conds := make(models.LoyaltyConditions, 0, len(c.Conditions))
		for j, cond := range c.Conditions {
			if cond.Field != loyaltyCondMarginPercent && cond.Field != loyaltyCondProfitAmount {
				return nil, fmt.Errorf("%s: condition %d has an unknown criterion", c.Name, j+1)
			}
			switch cond.Op {
			case "gte", "gt", "lte", "lt", "eq":
			default:
				return nil, fmt.Errorf("%s: condition %d has an unknown operator", c.Name, j+1)
			}
			if math.IsNaN(cond.Value) || math.IsInf(cond.Value, 0) {
				return nil, fmt.Errorf("%s: condition %d needs a number", c.Name, j+1)
			}
			if j == 0 {
				cond.Join = ""
			} else if cond.Join != "or" {
				cond.Join = "and"
			}
			conds = append(conds, cond)
		}
		c.Conditions = conds

		if c.ID == uuid.Nil {
			c.ID = uuid.New()
		}
		c.UserID = userID
		c.SortOrder = i
		out = append(out, c)
	}
	return out, nil
}

// replaceLoyaltyCategories swaps the user's category list for cats.
func replaceLoyaltyCategories(tx *gorm.DB, userID uuid.UUID, cats []models.LoyaltyCategory) error {
	if err := tx.Unscoped().Where("user_id = ?", userID).Delete(&models.LoyaltyCategory{}).Error; err != nil {
		return err
	}
	for i := range cats {
		// Select("*") so IsActive=false and other zero values are written.
		if err := tx.Select("*").Create(&cats[i]).Error; err != nil {
			return err
		}
	}
	return nil
}

// loyaltySaleFigures is what a category's conditions are tested against.
// CostKnown is false when no purchase cost is known, in which case every
// margin/profit condition fails (only condition-less categories match).
type loyaltySaleFigures struct {
	Sale      float64
	Profit    float64
	CostKnown bool
}

func (f loyaltySaleFigures) marginPercent() float64 {
	if f.Sale <= 0 {
		return 0
	}
	return f.Profit / f.Sale * 100
}

func loyaltyConditionHolds(cond models.LoyaltyCondition, f loyaltySaleFigures) bool {
	if !f.CostKnown {
		return false
	}
	var v float64
	switch cond.Field {
	case loyaltyCondMarginPercent:
		v = f.marginPercent()
	case loyaltyCondProfitAmount:
		v = f.Profit
	default:
		return false
	}
	const eps = 1e-9
	switch cond.Op {
	case "gte":
		return v+eps >= cond.Value
	case "gt":
		return v > cond.Value+eps
	case "lte":
		return v <= cond.Value+eps
	case "lt":
		return v+eps < cond.Value
	case "eq":
		return math.Abs(v-cond.Value) <= 0.005
	}
	return false
}

// loyaltyCategoryMatches evaluates the conditions with AND binding tighter
// than OR. No conditions = always matches.
func loyaltyCategoryMatches(cat models.LoyaltyCategory, f loyaltySaleFigures) bool {
	if len(cat.Conditions) == 0 {
		return true
	}
	groupOK := true
	for i, cond := range cat.Conditions {
		if i > 0 && cond.Join == "or" {
			if groupOK {
				return true
			}
			groupOK = true
		}
		if groupOK && !loyaltyConditionHolds(cond, f) {
			groupOK = false
		}
	}
	return groupOK
}

// loyaltyCategoryReward is the raw (unrounded) reward a match earns, in ₹ for
// cash mode and points for points mode.
func loyaltyCategoryReward(cat models.LoyaltyCategory, f loyaltySaleFigures, cash bool) float64 {
	if cat.RewardKind == loyaltyRewardKindFixed {
		return cat.RewardValue
	}
	base := f.Sale
	if cat.RewardOn == loyaltyRewardOnProfit {
		base = f.Profit
	}
	if base <= 0 {
		return 0
	}
	// Cash: value % of base. Points: value points per ₹100 of base. Both
	// reduce to base × value / 100.
	return base * cat.RewardValue / 100
}

func firstMatchingLoyaltyCategory(cats []models.LoyaltyCategory, f loyaltySaleFigures) *models.LoyaltyCategory {
	for i := range cats {
		if cats[i].IsActive && loyaltyCategoryMatches(cats[i], f) {
			return &cats[i]
		}
	}
	return nil
}

// loyaltyCategoryEarn is the outcome of category-based earning on a sale.
type loyaltyCategoryEarn struct {
	Value   float64        // ₹ (rounded to paise) or points (floored)
	Matches map[string]int // category name → number of matches
}

func (e loyaltyCategoryEarn) summary() string {
	if len(e.Matches) == 0 {
		return ""
	}
	names := make([]string, 0, len(e.Matches))
	for n := range e.Matches {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		if e.Matches[n] > 1 {
			parts = append(parts, fmt.Sprintf("%s ×%d", n, e.Matches[n]))
		} else {
			parts = append(parts, n)
		}
	}
	return strings.Join(parts, ", ")
}

// computeCategoryLoyaltyEarn applies loyalty categories to a sale.
//
// Item scope: each line is matched on its own net sale (taxable value less
// its share of billDiscount), profit and margin; lines with no product or no
// known purchase price have unknown margin. Invoice scope: the bill is
// matched once, on the combined profit and margin of costed lines; the
// "sale" reward base is invoiceTotal.
func computeCategoryLoyaltyEarn(items []models.InvoiceItem, costs map[uuid.UUID]float64, billDiscount, invoiceTotal float64, scope string, cats []models.LoyaltyCategory, cash bool) loyaltyCategoryEarn {
	res := loyaltyCategoryEarn{Matches: map[string]int{}}
	if len(cats) == 0 {
		return res
	}

	var gross float64
	for _, item := range items {
		gross += itemTaxableValue(item)
	}
	ratio := 1.0
	if gross > 0 && billDiscount > 0 {
		ratio = math.Max(0, 1-billDiscount/gross)
	}

	lineFigures := func(item models.InvoiceItem) loyaltySaleFigures {
		f := loyaltySaleFigures{Sale: itemTaxableValue(item) * ratio}
		if item.ProductID == nil || item.Quantity <= 0 {
			return f
		}
		unitCost := costs[*item.ProductID]
		if item.PurchasePrice != nil && *item.PurchasePrice > 0 {
			unitCost = *item.PurchasePrice
		}
		if unitCost <= 0 || f.Sale <= 0 {
			return f
		}
		f.Profit = f.Sale - item.Quantity*unitCost
		f.CostKnown = true
		return f
	}

	var total float64
	if scope == LoyaltyCategoryScopeInvoice {
		var agg loyaltySaleFigures
		for _, item := range items {
			f := lineFigures(item)
			if f.CostKnown {
				agg.Sale += f.Sale
				agg.Profit += f.Profit
				agg.CostKnown = true
			}
		}
		if cat := firstMatchingLoyaltyCategory(cats, agg); cat != nil {
			agg.Sale = invoiceTotal
			if r := loyaltyCategoryReward(*cat, agg, cash); r > 0 {
				total = r
				res.Matches[cat.Name]++
			}
		}
	} else {
		for _, item := range items {
			if item.Quantity <= 0 {
				continue
			}
			f := lineFigures(item)
			cat := firstMatchingLoyaltyCategory(cats, f)
			if cat == nil {
				continue
			}
			if r := loyaltyCategoryReward(*cat, f, cash); r > 0 {
				total += r
				res.Matches[cat.Name]++
			}
		}
	}

	if cash {
		res.Value = roundMoney(total)
	} else {
		res.Value = math.Floor(total + 1e-9)
	}
	if res.Value <= 0 {
		res.Value = 0
		res.Matches = map[string]int{}
	}
	return res
}

// invoiceCategoryLoyaltyEarn loads what computeCategoryLoyaltyEarn needs for
// an invoice. settings.Categories is used when already loaded.
func invoiceCategoryLoyaltyEarn(tx *gorm.DB, userID uuid.UUID, settings *models.LoyaltySettings, invoice *models.Invoice) loyaltyCategoryEarn {
	cats := settings.Categories
	if cats == nil {
		cats = loadLoyaltyCategories(tx, userID)
	}
	costs := loyaltyProductCosts(tx, userID, invoice.Items)
	return computeCategoryLoyaltyEarn(invoice.Items, costs, invoice.InvoiceDiscount+invoice.LoyaltyDiscount, invoice.TotalAmount, settings.CategoryScope, cats, IsCashLoyalty(settings))
}
