package controllers

import (
	"testing"
	"truerp/models"

	"github.com/google/uuid"
)

func cond(field, op string, v float64, join string) models.LoyaltyCondition {
	return models.LoyaltyCondition{Field: field, Op: op, Value: v, Join: join}
}

func TestLoyaltyCategoryMatchesAndOr(t *testing.T) {
	// (margin ≥ 30 AND profit ≥ 100) OR profit ≥ 500
	cat := models.LoyaltyCategory{IsActive: true, Conditions: models.LoyaltyConditions{
		cond(loyaltyCondMarginPercent, "gte", 30, ""),
		cond(loyaltyCondProfitAmount, "gte", 100, "and"),
		cond(loyaltyCondProfitAmount, "gte", 500, "or"),
	}}
	cases := []struct {
		name         string
		sale, profit float64
		want         bool
	}{
		{"both AND terms hold", 400, 150, true},   // 37.5%, ₹150
		{"margin ok, profit low", 200, 80, false}, // 40%, ₹80
		{"profit ok, margin low", 1000, 200, false},
		{"OR branch only", 5000, 600, true}, // 12%, ₹600
	}
	for _, tc := range cases {
		f := loyaltySaleFigures{Sale: tc.sale, Profit: tc.profit, CostKnown: true}
		if got := loyaltyCategoryMatches(cat, f); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
	if loyaltyCategoryMatches(cat, loyaltySaleFigures{Sale: 400}) {
		t.Errorf("unknown cost should not match margin/profit conditions")
	}
	if !loyaltyCategoryMatches(models.LoyaltyCategory{}, loyaltySaleFigures{Sale: 10}) {
		t.Errorf("category with no conditions should match everything")
	}
}

func TestComputeCategoryLoyaltyEarnItemScope(t *testing.T) {
	pA, pB, pC := uuid.New(), uuid.New(), uuid.New()
	items := []models.InvoiceItem{
		{ProductID: &pA, Quantity: 2, UnitPrice: 100}, // sale 200, cost 120 → profit 80, 40%
		{ProductID: &pB, Quantity: 1, UnitPrice: 100}, // sale 100, cost 90 → profit 10, 10%
		{ProductID: &pC, Quantity: 1, UnitPrice: 50},  // unknown cost
	}
	costs := map[uuid.UUID]float64{pA: 60, pB: 90}
	cats := []models.LoyaltyCategory{
		{Name: "Gold", IsActive: true, RewardKind: loyaltyRewardKindPercent, RewardOn: loyaltyRewardOnProfit, RewardValue: 10,
			Conditions: models.LoyaltyConditions{cond(loyaltyCondMarginPercent, "gte", 30, "")}},
		{Name: "Off", IsActive: false, RewardKind: loyaltyRewardKindFixed, RewardValue: 99},
		{Name: "Base", IsActive: true, RewardKind: loyaltyRewardKindPercent, RewardOn: loyaltyRewardOnSale, RewardValue: 1},
	}

	// Cash: Gold on A = 10% of ₹80 = 8; Base on B = 1% of 100 = 1; Base on C = 0.50.
	got := computeCategoryLoyaltyEarn(items, costs, 0, 350, LoyaltyCategoryScopeItem, cats, true)
	if got.Value != 9.5 {
		t.Fatalf("cash item scope = %v, want 9.5", got.Value)
	}
	if got.Matches["Gold"] != 1 || got.Matches["Base"] != 2 || got.summary() != "Base ×2, Gold" {
		t.Fatalf("matches = %v (%q)", got.Matches, got.summary())
	}

	// Points: same maths per ₹100 → 8 + 1 + 0.5 = 9.5 → 9 points.
	if p := computeCategoryLoyaltyEarn(items, costs, 0, 350, LoyaltyCategoryScopeItem, cats, false); p.Value != 9 {
		t.Fatalf("points item scope = %v, want 9", p.Value)
	}

	// Fixed reward per matching line.
	fixed := []models.LoyaltyCategory{{Name: "Any profit", IsActive: true, RewardKind: loyaltyRewardKindFixed, RewardValue: 5,
		Conditions: models.LoyaltyConditions{cond(loyaltyCondProfitAmount, "gt", 0, "")}}}
	if f := computeCategoryLoyaltyEarn(items, costs, 0, 350, LoyaltyCategoryScopeItem, fixed, true); f.Value != 10 {
		t.Fatalf("fixed = %v, want 10", f.Value)
	}
}

func TestComputeCategoryLoyaltyEarnInvoiceScope(t *testing.T) {
	pA, pB := uuid.New(), uuid.New()
	items := []models.InvoiceItem{
		{ProductID: &pA, Quantity: 2, UnitPrice: 100},
		{ProductID: &pB, Quantity: 1, UnitPrice: 100},
	}
	costs := map[uuid.UUID]float64{pA: 60, pB: 90}
	// Bill: sale 300, profit 90 → 30%.
	cats := []models.LoyaltyCategory{
		{Name: "High", IsActive: true, RewardKind: loyaltyRewardKindPercent, RewardOn: loyaltyRewardOnSale, RewardValue: 5,
			Conditions: models.LoyaltyConditions{cond(loyaltyCondMarginPercent, "gt", 30, "")}},
		{Name: "Mid", IsActive: true, RewardKind: loyaltyRewardKindPercent, RewardOn: loyaltyRewardOnSale, RewardValue: 2,
			Conditions: models.LoyaltyConditions{
				cond(loyaltyCondMarginPercent, "gte", 30, ""),
				cond(loyaltyCondProfitAmount, "gte", 1000, "or"),
			}},
	}
	got := computeCategoryLoyaltyEarn(items, costs, 0, 354, LoyaltyCategoryScopeInvoice, cats, true)
	if got.Value != 7.08 || got.Matches["Mid"] != 1 {
		t.Fatalf("invoice scope = %+v, want 2%% of 354 via Mid", got)
	}
	// A ₹30 bill discount (10%) drops profit to 60 on sale 270 → 22%: no match.
	if d := computeCategoryLoyaltyEarn(items, costs, 30, 324, LoyaltyCategoryScopeInvoice, cats, true); d.Value != 0 {
		t.Fatalf("discounted invoice = %v, want 0", d.Value)
	}
}

func TestValidateLoyaltyCategories(t *testing.T) {
	uid := uuid.New()
	ok := []models.LoyaltyCategory{{Name: " Gold ", IsActive: true, RewardValue: 3, Conditions: models.LoyaltyConditions{
		cond(loyaltyCondMarginPercent, "gte", 20, "or"), cond(loyaltyCondProfitAmount, "gte", 50, "bogus"),
	}}}
	out, err := validateLoyaltyCategories(uid, ok, true)
	if err != nil {
		t.Fatal(err)
	}
	c := out[0]
	if c.Name != "Gold" || c.RewardKind != "percent" || c.RewardOn != "sale" || c.Conditions[0].Join != "" || c.Conditions[1].Join != "and" || c.UserID != uid {
		t.Fatalf("normalised = %+v", c)
	}
	bad := [][]models.LoyaltyCategory{
		{{Name: ""}},
		{{Name: "A", RewardValue: 1}, {Name: "a", RewardValue: 1}},
		{{Name: "A", RewardValue: 0}},
		{{Name: "A", RewardValue: 150}},
		{{Name: "A", RewardValue: 1, Conditions: models.LoyaltyConditions{cond("qty", "gte", 1, "")}}},
		{{Name: "A", RewardValue: 1, Conditions: models.LoyaltyConditions{cond(loyaltyCondProfitAmount, "ne", 1, "")}}},
	}
	for i, b := range bad {
		if _, err := validateLoyaltyCategories(uid, b, true); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
}
