package controllers

import (
	"strings"
	"testing"
	"truerp/models"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestDiffLoyaltySettings(t *testing.T) {
	before := &models.LoyaltySettings{IsEnabled: false, RewardType: LoyaltyRewardPoints, EarnBasis: LoyaltyEarnOnBill, CashbackPercent: 1, MaxRedeemPercent: 25}
	same := *before
	if e := diffLoyaltySettings(before, &same); e != nil {
		t.Fatalf("no change should give no entry, got %+v", e)
	}
	after := *before
	after.IsEnabled = true
	after.RewardType = LoyaltyRewardCash
	after.EarnBasis = LoyaltyEarnOnCategory
	after.CashbackPercent = 2.5
	e := diffLoyaltySettings(before, &after)
	if e == nil || e.Action != "update" || e.EntityType != auditEntityLoyaltySettings {
		t.Fatalf("entry = %+v", e)
	}
	for _, want := range []string{"Program inactive → active", "Reward type Points → Cashback", "Earn on Bill amount → Categories", "Cashback 1% → 2.5%"} {
		if !strings.Contains(e.Description, want) {
			t.Errorf("description %q missing %q", e.Description, want)
		}
	}
	if len(e.Changes) != 4 || e.Changes["max_redeem_percent"] != nil {
		t.Errorf("changes = %v", e.Changes)
	}
}

func TestDiffLoyaltyCategories(t *testing.T) {
	idKeep, idEdit, idGone := uuid.New(), uuid.New(), uuid.New()
	gold := models.LoyaltyCategory{ID: idEdit, Name: "Gold", SortOrder: 0, IsActive: true, RewardKind: "percent", RewardValue: 2, RewardOn: "sale",
		Conditions: models.LoyaltyConditions{cond(loyaltyCondMarginPercent, "gte", 30, "")}}
	silver := models.LoyaltyCategory{ID: idKeep, Name: "Silver", SortOrder: 1, IsActive: true, RewardKind: "fixed", RewardValue: 5, RewardOn: "sale"}
	old := models.LoyaltyCategory{ID: idGone, Name: "Old", SortOrder: 2, IsActive: true, RewardKind: "percent", RewardValue: 1, RewardOn: "profit"}
	before := []models.LoyaltyCategory{gold, silver, old}

	goldNew := gold
	goldNew.Name = "Gold+"
	goldNew.Conditions = models.LoyaltyConditions{cond(loyaltyCondMarginPercent, "gte", 25, ""), cond(loyaltyCondProfitAmount, "gte", 100, "and")}
	goldNew.RewardValue = 3
	goldNew.RewardOn = "profit"
	fresh := models.LoyaltyCategory{ID: uuid.New(), Name: "Bronze", SortOrder: 2, IsActive: false, RewardKind: "percent", RewardValue: 0.5, RewardOn: "sale"}
	after := []models.LoyaltyCategory{goldNew, silver, fresh}

	entries := diffLoyaltyCategories(before, after, true, true)
	if len(entries) != 3 {
		t.Fatalf("want 3 entries (update, create, delete), got %d: %+v", len(entries), entries)
	}
	byAction := map[string]loyaltyAuditEntry{}
	for _, e := range entries {
		byAction[e.Action] = e
	}
	up := byAction["update"]
	for _, want := range []string{
		`renamed "Gold" → "Gold+"`,
		"criteria: margin ≥ 30% → margin ≥ 25% AND profit ≥ ₹100",
		"reward: 2% of sale value as cashback → 3% of profit as cashback",
	} {
		if !strings.Contains(up.Description, want) {
			t.Errorf("update description %q missing %q", up.Description, want)
		}
	}
	if up.Changes["conditions"] == nil || up.Changes["reward"] == nil || up.Changes["name"] == nil || up.Changes["priority"] != nil {
		t.Errorf("update changes = %v", up.Changes)
	}
	if c := byAction["create"]; c.EntityName != "Bronze" || !strings.Contains(c.Description, "inactive") || !strings.Contains(c.Description, "always (no criteria)") {
		t.Errorf("create = %+v", c)
	}
	if d := byAction["delete"]; d.EntityID != idGone || !strings.Contains(d.Description, "1% of profit as cashback") || d.Previous == nil {
		t.Errorf("delete = %+v", d)
	}

	// Reordering alone is logged as a priority change on both categories.
	swapped := []models.LoyaltyCategory{silver, gold}
	swapped[0].SortOrder, swapped[1].SortOrder = 0, 1
	moves := diffLoyaltyCategories([]models.LoyaltyCategory{gold, silver}, swapped, false, false)
	if len(moves) != 2 || !strings.Contains(moves[0].Description, "priority 2 → 1") {
		t.Errorf("reorder entries = %+v", moves)
	}
}

func TestSyncLoyaltyCategories(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.LoyaltyCategory{}); err != nil {
		t.Fatal(err)
	}
	uid, other := uuid.New(), uuid.New()
	foreign := models.LoyaltyCategory{ID: uuid.New(), UserID: other, Name: "Theirs", IsActive: true, RewardKind: "fixed", RewardValue: 1}
	db.Select("*").Create(&foreign)

	a := models.LoyaltyCategory{ID: uuid.New(), UserID: uid, Name: "A", SortOrder: 0, IsActive: true, RewardKind: "percent", RewardValue: 1, RewardOn: "sale",
		Conditions: models.LoyaltyConditions{cond(loyaltyCondMarginPercent, "gte", 10, "")}}
	b := models.LoyaltyCategory{ID: uuid.New(), UserID: uid, Name: "B", SortOrder: 1, IsActive: true, RewardKind: "fixed", RewardValue: 2, RewardOn: "sale"}
	if err := syncLoyaltyCategories(db, uid, nil, []models.LoyaltyCategory{a, b}); err != nil {
		t.Fatal(err)
	}
	existing := loadLoyaltyCategories(db, uid)
	if len(existing) != 2 {
		t.Fatalf("after create: %d rows", len(existing))
	}

	// Edit A (disable + new criteria), drop B, add C, and try to hijack the
	// other store's row by sending its ID.
	a2 := existing[0]
	a2.IsActive = false
	a2.Conditions = models.LoyaltyConditions{cond(loyaltyCondProfitAmount, "gt", 50, "")}
	c := models.LoyaltyCategory{UserID: uid, Name: "C", SortOrder: 1, IsActive: true, RewardKind: "percent", RewardValue: 3, RewardOn: "profit"}
	hijack := models.LoyaltyCategory{ID: foreign.ID, UserID: uid, Name: "Mine now", SortOrder: 2, IsActive: true, RewardKind: "fixed", RewardValue: 9}
	desired := []models.LoyaltyCategory{a2, c, hijack}
	reconcileLoyaltyCategoryIDs(existing, desired)
	if desired[0].ID != a.ID || desired[1].ID == uuid.Nil || desired[2].ID == foreign.ID {
		t.Fatalf("reconciled IDs wrong: %v %v %v", desired[0].ID, desired[1].ID, desired[2].ID)
	}
	if err := syncLoyaltyCategories(db, uid, existing, desired); err != nil {
		t.Fatal(err)
	}

	got := loadLoyaltyCategories(db, uid)
	if len(got) != 3 || got[0].ID != a.ID || got[0].IsActive || len(got[0].Conditions) != 1 || got[0].Conditions[0].Field != loyaltyCondProfitAmount {
		t.Fatalf("after sync: %+v", got)
	}
	if got[1].Name != "C" || got[2].Name != "Mine now" {
		t.Fatalf("names: %q %q", got[1].Name, got[2].Name)
	}
	var theirs models.LoyaltyCategory
	db.First(&theirs, "id = ?", foreign.ID)
	if theirs.Name != "Theirs" || theirs.UserID != other {
		t.Fatalf("other store's category was modified: %+v", theirs)
	}
	var bCount int64
	db.Unscoped().Model(&models.LoyaltyCategory{}).Where("id = ?", b.ID).Count(&bCount)
	if bCount != 0 {
		t.Fatalf("B should be deleted")
	}
}
