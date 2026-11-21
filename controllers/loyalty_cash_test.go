package controllers

import (
	"testing"
	"truerp/models"

	"github.com/google/uuid"
)

func cashSettings() *models.LoyaltySettings {
	return &models.LoyaltySettings{
		IsEnabled:        true,
		RewardType:       LoyaltyRewardCash,
		CashbackPercent:  2,
		MinRedeemAmount:  10,
		MaxRedeemPercent: 50,
	}
}

func TestCalculateEarnCash(t *testing.T) {
	s := cashSettings()
	if got := CalculateEarnCash(s, 1234.5); got != 24.69 {
		t.Fatalf("earn on 1234.5 @2%% = %v, want 24.69", got)
	}
	if got := CalculateEarnPoints(s, 1000); got != 0 {
		t.Fatalf("cash mode must not earn points, got %v", got)
	}
	s.IsEnabled = false
	if got := CalculateEarnCash(s, 1000); got != 0 {
		t.Fatalf("disabled program earned %v", got)
	}
}

func TestComputeLoyaltyCashRedemption(t *testing.T) {
	s := cashSettings()
	party := &models.Party{PartyType: "customer", LoyaltyCash: 100}

	if d, err := ComputeLoyaltyCashRedemption(s, party, 500, 40.255); err != nil || d != 40.26 {
		t.Fatalf("redeem 40.255 = %v, %v; want 40.26", d, err)
	}
	cases := []struct {
		name   string
		amount float64
		bill   float64
	}{
		{"below minimum", 5, 500},
		{"over balance", 150, 1000},
		{"over max percent", 60, 100},
	}
	for _, tc := range cases {
		if _, err := ComputeLoyaltyCashRedemption(s, party, tc.bill, tc.amount); err == nil {
			t.Errorf("%s: expected error", tc.name)
		}
	}
	if _, err := ComputeLoyaltyRedemption(s, party, 500, 10); err == nil {
		t.Errorf("points redemption should be rejected in cash mode")
	}
	vendor := &models.Party{PartyType: "vendor", LoyaltyCash: 100}
	if _, err := ComputeLoyaltyCashRedemption(s, vendor, 500, 20); err == nil {
		t.Errorf("vendor redemption should be rejected")
	}
}

func TestComputeEligibleMarginProfit(t *testing.T) {
	pA, pB, pC := uuid.New(), uuid.New(), uuid.New()
	snap := 90.0
	items := []models.InvoiceItem{
		// sale 200, cost 2×60=120 → profit 80, margin 40%
		{ProductID: &pA, Quantity: 2, UnitPrice: 100},
		// sale 100, snapshot cost 90 → profit 10, margin 10%
		{ProductID: &pB, Quantity: 1, UnitPrice: 100, PurchasePrice: &snap},
		// no known cost → skipped
		{ProductID: &pC, Quantity: 1, UnitPrice: 100},
		// free-text line, no product → skipped
		{Quantity: 1, UnitPrice: 100},
	}
	costs := map[uuid.UUID]float64{pA: 60, pB: 1}

	if got := computeEligibleMarginProfit(items, costs, 0, 0); got != 90 {
		t.Fatalf("no threshold: got %v, want 90", got)
	}
	if got := computeEligibleMarginProfit(items, costs, 0, 20); got != 80 {
		t.Fatalf("min 20%%: got %v, want 80", got)
	}
	if got := computeEligibleMarginProfit(items, costs, 0, 50); got != 0 {
		t.Fatalf("min 50%%: got %v, want 0", got)
	}
	// ₹50 bill discount over ₹500 gross = 10% off every line:
	// A: sale 180 → profit 60 (33.3%); B: sale 90 → profit 0 (skipped).
	if got := computeEligibleMarginProfit(items, costs, 50, 0); got != 60 {
		t.Fatalf("with bill discount: got %v, want 60", got)
	}
}

func TestMarginBasisUsesSameRates(t *testing.T) {
	s := &models.LoyaltySettings{IsEnabled: true, RewardType: LoyaltyRewardPoints, EarnBasis: LoyaltyEarnOnMargin, SpendAmount: 10, PointsPerSpend: 1}
	if got := CalculateEarnPoints(s, 85); got != 8 {
		t.Fatalf("points on ₹85 profit = %v, want 8", got)
	}
}
