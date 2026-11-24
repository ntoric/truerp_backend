package controllers

import (
	"testing"
	"truerp/models"
)

func TestExcludedCustomerCannotRedeemOrEarn(t *testing.T) {
	excluded := &models.Party{PartyType: "customer", LoyaltyPoints: 500, LoyaltyCash: 100, LoyaltyExcluded: true}

	if _, err := ComputeLoyaltyCashRedemption(cashSettings(), excluded, 500, 20); err != errLoyaltyExcluded {
		t.Fatalf("cash redemption err = %v, want errLoyaltyExcluded", err)
	}
	points := &models.LoyaltySettings{IsEnabled: true, RewardType: LoyaltyRewardPoints, PointValue: 1, MinRedeemPoints: 10, MaxRedeemPercent: 50}
	if _, err := ComputeLoyaltyRedemption(points, excluded, 500, 50); err != errLoyaltyExcluded {
		t.Fatalf("points redemption err = %v, want errLoyaltyExcluded", err)
	}

	// applyLoyaltyForInvoice must return before touching the DB (tx is nil).
	inv := &models.Invoice{Status: "paid", TotalAmount: 1000}
	if err := applyLoyaltyForInvoice(nil, excluded.UserID, excluded, inv, points, 0); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if inv.LoyaltyPointsEarned != 0 || inv.LoyaltyCashEarned != 0 || excluded.LoyaltyPoints != 500 || excluded.LoyaltyCash != 100 {
		t.Fatalf("excluded customer earned: invoice %+v, party %+v", inv, excluded)
	}

	// Re-included: redemption works again on the kept balance.
	excluded.LoyaltyExcluded = false
	if d, err := ComputeLoyaltyRedemption(points, excluded, 500, 50); err != nil || d != 50 {
		t.Fatalf("after re-include: %v, %v", d, err)
	}
}
