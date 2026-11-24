package controllers

import (
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"truerp/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Audit entity types for the loyalty program.
const (
	auditEntityLoyaltySettings = "loyalty_settings"
	auditEntityLoyaltyCategory = "loyalty_category"
	// Customer excluded from / re-included in the program.
	auditEntityLoyaltyExclusion = "loyalty_exclusion"
)

// loyaltyAuditEntry is one audit log row produced by a loyalty settings save.
type loyaltyAuditEntry struct {
	Action      string // create | update | delete
	EntityType  string
	EntityID    uuid.UUID
	EntityName  string
	Description string
	Previous    interface{}
	New         interface{}
	Changes     map[string]interface{}
}

func fmtNum(v float64) string {
	return strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64)
}

func fmtRupees(v float64) string { return "₹" + fmtNum(v) }

func fmtYesNo(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

var loyaltyEnumLabels = map[string]string{
	LoyaltyRewardPoints:         "Points",
	LoyaltyRewardCash:           "Cashback",
	LoyaltyEarnOnBill:           "Bill amount",
	LoyaltyEarnOnMargin:         "Profit margin",
	LoyaltyEarnOnCategory:       "Categories",
	LoyaltyCategoryScopeItem:    "Each item",
	LoyaltyCategoryScopeInvoice: "Whole bill",
}

func loyaltyEnumLabel(v string) string {
	if l, ok := loyaltyEnumLabels[v]; ok {
		return l
	}
	if v == "" {
		return "—"
	}
	return v
}

// loyaltySettingFields lists the audited settings fields in display order.
var loyaltySettingFields = []struct {
	key   string
	label string
	get   func(s *models.LoyaltySettings) interface{}
	show  func(v interface{}) string
}{
	{"is_enabled", "Program", func(s *models.LoyaltySettings) interface{} { return s.IsEnabled },
		func(v interface{}) string {
			if v.(bool) {
				return "active"
			}
			return "inactive"
		}},
	{"reward_type", "Reward type", func(s *models.LoyaltySettings) interface{} { return s.RewardType }, func(v interface{}) string { return loyaltyEnumLabel(v.(string)) }},
	{"earn_basis", "Earn on", func(s *models.LoyaltySettings) interface{} { return s.EarnBasis }, func(v interface{}) string { return loyaltyEnumLabel(v.(string)) }},
	{"category_scope", "Apply categories to", func(s *models.LoyaltySettings) interface{} { return s.CategoryScope }, func(v interface{}) string { return loyaltyEnumLabel(v.(string)) }},
	{"min_margin_percent", "Minimum item margin", func(s *models.LoyaltySettings) interface{} { return s.MinMarginPercent }, func(v interface{}) string { return fmtNum(v.(float64)) + "%" }},
	{"cashback_percent", "Cashback", func(s *models.LoyaltySettings) interface{} { return s.CashbackPercent }, func(v interface{}) string { return fmtNum(v.(float64)) + "%" }},
	{"min_redeem_amount", "Minimum cashback to redeem", func(s *models.LoyaltySettings) interface{} { return s.MinRedeemAmount }, func(v interface{}) string { return fmtRupees(v.(float64)) }},
	{"spend_amount", "Earn rate amount", func(s *models.LoyaltySettings) interface{} { return s.SpendAmount }, func(v interface{}) string { return fmtRupees(v.(float64)) }},
	{"points_per_spend", "Points per earn amount", func(s *models.LoyaltySettings) interface{} { return s.PointsPerSpend }, func(v interface{}) string { return strconv.FormatInt(v.(int64), 10) }},
	{"point_value", "Value per point", func(s *models.LoyaltySettings) interface{} { return s.PointValue }, func(v interface{}) string { return fmtRupees(v.(float64)) }},
	{"min_redeem_points", "Minimum points to redeem", func(s *models.LoyaltySettings) interface{} { return s.MinRedeemPoints }, func(v interface{}) string { return strconv.FormatInt(v.(int64), 10) }},
	{"max_redeem_percent", "Max bill % payable", func(s *models.LoyaltySettings) interface{} { return s.MaxRedeemPercent }, func(v interface{}) string { return fmtNum(v.(float64)) + "%" }},
}

// loyaltySettingsSnapshot is the audited view of the settings row.
func loyaltySettingsSnapshot(s *models.LoyaltySettings) map[string]interface{} {
	out := make(map[string]interface{}, len(loyaltySettingFields))
	for _, f := range loyaltySettingFields {
		out[f.key] = f.get(s)
	}
	return out
}

// diffLoyaltySettings returns the audit entry for a settings change, or nil
// when nothing audited changed.
func diffLoyaltySettings(before, after *models.LoyaltySettings) *loyaltyAuditEntry {
	changes := map[string]interface{}{}
	var parts []string
	for _, f := range loyaltySettingFields {
		ov, nv := f.get(before), f.get(after)
		if ov == nv {
			continue
		}
		changes[f.key] = map[string]interface{}{"old": ov, "new": nv}
		parts = append(parts, fmt.Sprintf("%s %s → %s", f.label, f.show(ov), f.show(nv)))
	}
	if len(parts) == 0 {
		return nil
	}
	return &loyaltyAuditEntry{
		Action:      "update",
		EntityType:  auditEntityLoyaltySettings,
		EntityID:    after.ID,
		EntityName:  "Loyalty program",
		Description: "Updated loyalty settings: " + strings.Join(parts, "; "),
		Previous:    loyaltySettingsSnapshot(before),
		New:         loyaltySettingsSnapshot(after),
		Changes:     changes,
	}
}

func loyaltyConditionText(c models.LoyaltyCondition) string {
	op := map[string]string{"gte": "≥", "gt": ">", "lte": "≤", "lt": "<", "eq": "="}[c.Op]
	if c.Field == loyaltyCondMarginPercent {
		return fmt.Sprintf("margin %s %s%%", op, fmtNum(c.Value))
	}
	return fmt.Sprintf("profit %s %s", op, fmtRupees(c.Value))
}

// loyaltyCriteriaText renders conditions, e.g. "margin ≥ 30% AND profit ≥ ₹100".
func loyaltyCriteriaText(conds models.LoyaltyConditions) string {
	if len(conds) == 0 {
		return "always (no criteria)"
	}
	var b strings.Builder
	for i, c := range conds {
		if i > 0 {
			join := "AND"
			if c.Join == "or" {
				join = "OR"
			}
			b.WriteString(" " + join + " ")
		}
		b.WriteString(loyaltyConditionText(c))
	}
	return b.String()
}

// loyaltyRewardText renders a category reward in the store's reward unit.
func loyaltyRewardText(cat models.LoyaltyCategory, cash bool) string {
	base := "sale value"
	if cat.RewardOn == loyaltyRewardOnProfit {
		base = "profit"
	}
	switch {
	case cat.RewardKind == loyaltyRewardKindFixed && cash:
		return fmtRupees(cat.RewardValue) + " cashback per match"
	case cat.RewardKind == loyaltyRewardKindFixed:
		return fmtNum(cat.RewardValue) + " points per match"
	case cash:
		return fmtNum(cat.RewardValue) + "% of " + base + " as cashback"
	default:
		return fmtNum(cat.RewardValue) + " points per ₹100 of " + base
	}
}

func loyaltyCategoryRuleText(cat models.LoyaltyCategory, cash bool) string {
	return "when " + loyaltyCriteriaText(cat.Conditions) + " → " + loyaltyRewardText(cat, cash)
}

// loyaltyCategorySnapshot is the audited view of a category.
func loyaltyCategorySnapshot(cat models.LoyaltyCategory, cash bool) map[string]interface{} {
	return map[string]interface{}{
		"name":         cat.Name,
		"priority":     cat.SortOrder + 1,
		"is_active":    cat.IsActive,
		"conditions":   cat.Conditions,
		"criteria":     loyaltyCriteriaText(cat.Conditions),
		"reward_kind":  cat.RewardKind,
		"reward_value": cat.RewardValue,
		"reward_on":    cat.RewardOn,
		"reward":       loyaltyRewardText(cat, cash),
	}
}

func sameLoyaltyConditions(a, b models.LoyaltyConditions) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

// loyaltyCategoryChanged reports whether a stored category differs from its
// new version in any saved field.
func loyaltyCategoryChanged(a, b models.LoyaltyCategory) bool {
	return a.Name != b.Name || a.SortOrder != b.SortOrder || a.IsActive != b.IsActive ||
		a.RewardKind != b.RewardKind || a.RewardValue != b.RewardValue || a.RewardOn != b.RewardOn ||
		!sameLoyaltyConditions(a.Conditions, b.Conditions)
}

// diffLoyaltyCategories returns one audit entry per created, deleted or
// changed category. oldCash/newCash are the reward modes before and after the
// save, used to word rewards in the unit that applied at the time.
func diffLoyaltyCategories(before, after []models.LoyaltyCategory, oldCash, newCash bool) []loyaltyAuditEntry {
	var entries []loyaltyAuditEntry
	afterIDs := make(map[uuid.UUID]bool, len(after))
	beforeByID := make(map[uuid.UUID]models.LoyaltyCategory, len(before))
	for _, c := range before {
		beforeByID[c.ID] = c
	}

	for _, n := range after {
		afterIDs[n.ID] = true
		o, existed := beforeByID[n.ID]
		if !existed {
			entries = append(entries, loyaltyAuditEntry{
				Action:      "create",
				EntityType:  auditEntityLoyaltyCategory,
				EntityID:    n.ID,
				EntityName:  n.Name,
				Description: fmt.Sprintf("Created loyalty category %s (priority %d, %s): %s", n.Name, n.SortOrder+1, activeWord(n.IsActive), loyaltyCategoryRuleText(n, newCash)),
				New:         loyaltyCategorySnapshot(n, newCash),
			})
			continue
		}
		if !loyaltyCategoryChanged(o, n) {
			continue
		}
		changes := map[string]interface{}{}
		var parts []string
		if o.Name != n.Name {
			changes["name"] = map[string]interface{}{"old": o.Name, "new": n.Name}
			parts = append(parts, fmt.Sprintf("renamed %q → %q", o.Name, n.Name))
		}
		if o.IsActive != n.IsActive {
			changes["is_active"] = map[string]interface{}{"old": o.IsActive, "new": n.IsActive}
			if n.IsActive {
				parts = append(parts, "enabled")
			} else {
				parts = append(parts, "disabled")
			}
		}
		if o.SortOrder != n.SortOrder {
			changes["priority"] = map[string]interface{}{"old": o.SortOrder + 1, "new": n.SortOrder + 1}
			parts = append(parts, fmt.Sprintf("priority %d → %d", o.SortOrder+1, n.SortOrder+1))
		}
		if !sameLoyaltyConditions(o.Conditions, n.Conditions) {
			oldText, newText := loyaltyCriteriaText(o.Conditions), loyaltyCriteriaText(n.Conditions)
			changes["conditions"] = map[string]interface{}{"old": o.Conditions, "new": n.Conditions}
			changes["criteria"] = map[string]interface{}{"old": oldText, "new": newText}
			parts = append(parts, fmt.Sprintf("criteria: %s → %s", oldText, newText))
		}
		if o.RewardKind != n.RewardKind || o.RewardValue != n.RewardValue || o.RewardOn != n.RewardOn {
			oldText, newText := loyaltyRewardText(o, oldCash), loyaltyRewardText(n, newCash)
			changes["reward"] = map[string]interface{}{
				"old": map[string]interface{}{"kind": o.RewardKind, "value": o.RewardValue, "on": o.RewardOn, "text": oldText},
				"new": map[string]interface{}{"kind": n.RewardKind, "value": n.RewardValue, "on": n.RewardOn, "text": newText},
			}
			// A base switch on a fixed reward has no effect; don't describe it.
			if oldText != newText {
				parts = append(parts, fmt.Sprintf("reward: %s → %s", oldText, newText))
			}
		}
		desc := "Updated loyalty category " + n.Name
		if len(parts) > 0 {
			desc += ": " + strings.Join(parts, "; ")
		}
		entries = append(entries, loyaltyAuditEntry{
			Action:      "update",
			EntityType:  auditEntityLoyaltyCategory,
			EntityID:    n.ID,
			EntityName:  n.Name,
			Description: desc,
			Previous:    loyaltyCategorySnapshot(o, oldCash),
			New:         loyaltyCategorySnapshot(n, newCash),
			Changes:     changes,
		})
	}

	for _, o := range before {
		if afterIDs[o.ID] {
			continue
		}
		entries = append(entries, loyaltyAuditEntry{
			Action:      "delete",
			EntityType:  auditEntityLoyaltyCategory,
			EntityID:    o.ID,
			EntityName:  o.Name,
			Description: fmt.Sprintf("Deleted loyalty category %s: %s", o.Name, loyaltyCategoryRuleText(o, oldCash)),
			Previous:    loyaltyCategorySnapshot(o, oldCash),
		})
	}
	return entries
}

func activeWord(active bool) string {
	if active {
		return "active"
	}
	return "inactive"
}

// reconcileLoyaltyCategoryIDs keeps a client-sent ID only when it names one of
// the user's existing categories; anything else gets a fresh ID so a client
// cannot overwrite another store's row or collide on the primary key.
func reconcileLoyaltyCategoryIDs(existing, desired []models.LoyaltyCategory) {
	known := make(map[uuid.UUID]bool, len(existing))
	for _, c := range existing {
		known[c.ID] = true
	}
	used := map[uuid.UUID]bool{}
	for i := range desired {
		if !known[desired[i].ID] || used[desired[i].ID] {
			desired[i].ID = uuid.New()
		}
		used[desired[i].ID] = true
	}
}

// syncLoyaltyCategories makes the stored categories match desired, writing
// only rows that were added, changed or removed.
func syncLoyaltyCategories(tx *gorm.DB, userID uuid.UUID, existing, desired []models.LoyaltyCategory) error {
	byID := make(map[uuid.UUID]models.LoyaltyCategory, len(existing))
	for _, c := range existing {
		byID[c.ID] = c
	}
	keep := make(map[uuid.UUID]bool, len(desired))
	for i := range desired {
		d := desired[i]
		keep[d.ID] = true
		old, ok := byID[d.ID]
		if !ok {
			// Select("*") so IsActive=false and other zero values are written.
			if err := tx.Select("*").Create(&d).Error; err != nil {
				return err
			}
			continue
		}
		if !loyaltyCategoryChanged(old, d) {
			continue
		}
		if err := tx.Model(&models.LoyaltyCategory{}).
			Where("id = ? AND user_id = ?", d.ID, userID).
			Select("name", "sort_order", "is_active", "conditions", "reward_kind", "reward_value", "reward_on", "updated_at").
			Updates(&d).Error; err != nil {
			return err
		}
	}
	var removed []uuid.UUID
	for _, c := range existing {
		if !keep[c.ID] {
			removed = append(removed, c.ID)
		}
	}
	if len(removed) > 0 {
		if err := tx.Unscoped().Where("user_id = ? AND id IN ?", userID, removed).Delete(&models.LoyaltyCategory{}).Error; err != nil {
			return err
		}
	}
	return nil
}

func writeLoyaltyAudit(c *gin.Context, entries []loyaltyAuditEntry) {
	for _, e := range entries {
		id := e.EntityID
		var changes interface{}
		if len(e.Changes) > 0 {
			changes = e.Changes
		}
		LogDataChangeWithDiff(c, e.Action, e.EntityType, &id, e.EntityName, e.Description, e.Previous, e.New, changes)
	}
}
