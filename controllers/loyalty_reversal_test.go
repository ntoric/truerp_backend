package controllers

import (
	"testing"
	"truerp/models"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func loyaltyReversalDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Party{}, &models.LoyaltyTransaction{}, &models.Invoice{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func partyBalances(t *testing.T, db *gorm.DB, id uuid.UUID) (int64, float64) {
	t.Helper()
	var p models.Party
	if err := db.First(&p, "id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	return p.LoyaltyPoints, p.LoyaltyCash
}

func TestCashLoyaltyCancelReinstateDelete(t *testing.T) {
	db := loyaltyReversalDB(t)
	uid := uuid.New()
	party := models.Party{ID: uuid.New(), UserID: uid, Name: "Asha", PartyType: "customer", LoyaltyCash: 50}
	if err := db.Create(&party).Error; err != nil {
		t.Fatal(err)
	}
	inv := models.Invoice{ID: uuid.New(), UserID: uid, PartyID: party.ID, InvoiceNumber: "INV-1", Status: "paid",
		TotalAmount: 1000, LoyaltyCashRedeemed: 20, LoyaltyDiscount: 20}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatal(err)
	}
	settings := &models.LoyaltySettings{IsEnabled: true, RewardType: LoyaltyRewardCash, CashbackPercent: 2, EarnBasis: LoyaltyEarnOnBill}

	// Sale: redeem ₹20, earn 2% of ₹1000 = ₹20 → 50 − 20 + 20 = 50.
	if err := db.Transaction(func(tx *gorm.DB) error {
		return applyLoyaltyForInvoice(tx, uid, &party, &inv, settings, 0)
	}); err != nil {
		t.Fatal(err)
	}
	if _, cash := partyBalances(t, db, party.ID); cash != 50 || inv.LoyaltyCashEarned != 20 {
		t.Fatalf("after sale cash=%v earned=%v", cash, inv.LoyaltyCashEarned)
	}

	// Cancel: take back ₹20 earned, give back ₹20 redeemed → 50.
	// Use different amounts to see both legs: bump the wallet first.
	db.Model(&models.Party{}).Where("id = ?", party.ID).Update("loyalty_cash", 100)
	inv.Status = "cancelled"
	if err := syncInvoiceLoyaltyOnStatusChange(db, uid, &inv, "paid"); err != nil {
		t.Fatal(err)
	}
	if _, cash := partyBalances(t, db, party.ID); cash != 100 {
		t.Fatalf("after cancel cash=%v, want 100 (−20 earned +20 redeemed)", cash)
	}
	if !inv.LoyaltyReversed {
		t.Fatalf("invoice not marked reversed")
	}
	var reversalRows int64
	db.Model(&models.LoyaltyTransaction{}).Where("reference_id = ? AND notes LIKE ?", inv.ID, "%cancellation%").Count(&reversalRows)
	if reversalRows != 2 {
		t.Fatalf("cancellation rows = %d, want 2 (earn reversed, redeem restored)", reversalRows)
	}

	// Cancelling twice (e.g. PATCH retried) must not reverse again.
	if err := syncInvoiceLoyaltyOnStatusChange(db, uid, &inv, "paid"); err != nil {
		t.Fatal(err)
	}
	if _, cash := partyBalances(t, db, party.ID); cash != 100 {
		t.Fatalf("double cancel changed balance: %v", cash)
	}

	// Reinstate: re-applies −20 redeemed, +20 earned; net back to 0 change on
	// wallet but ledger net is non-zero again.
	inv.Status = "paid"
	if err := syncInvoiceLoyaltyOnStatusChange(db, uid, &inv, "cancelled"); err != nil {
		t.Fatal(err)
	}
	var reinstated int64
	db.Model(&models.LoyaltyTransaction{}).Where("reference_id = ? AND notes LIKE ?", inv.ID, "%reinstatement%").Count(&reinstated)
	if reinstated != 2 || inv.LoyaltyReversed {
		t.Fatalf("reinstatement rows = %d (want 2), reversed=%v", reinstated, inv.LoyaltyReversed)
	}
	// Reinstating again (no prior cancel) must not add rows.
	if err := syncInvoiceLoyaltyOnStatusChange(db, uid, &inv, "cancelled"); err != nil {
		t.Fatal(err)
	}
	db.Model(&models.LoyaltyTransaction{}).Where("reference_id = ? AND notes LIKE ?", inv.ID, "%reinstatement%").Count(&reinstated)
	if reinstated != 2 {
		t.Fatalf("double reinstate added rows: %d", reinstated)
	}
}

func TestCashLoyaltyCancelUnequalAmounts(t *testing.T) {
	db := loyaltyReversalDB(t)
	uid := uuid.New()
	party := models.Party{ID: uuid.New(), UserID: uid, Name: "Meera", PartyType: "customer", LoyaltyCash: 100}
	db.Create(&party)
	inv := models.Invoice{ID: uuid.New(), UserID: uid, PartyID: party.ID, InvoiceNumber: "INV-9", Status: "paid",
		TotalAmount: 2000, LoyaltyCashRedeemed: 30, LoyaltyDiscount: 30}
	db.Create(&inv)
	settings := &models.LoyaltySettings{IsEnabled: true, RewardType: LoyaltyRewardCash, CashbackPercent: 5, EarnBasis: LoyaltyEarnOnBill}
	db.Transaction(func(tx *gorm.DB) error { return applyLoyaltyForInvoice(tx, uid, &party, &inv, settings, 0) })
	// 100 − 30 redeemed + 100 earned (5% of 2000) = 170
	if _, cash := partyBalances(t, db, party.ID); cash != 170 {
		t.Fatalf("after sale = %v, want 170", cash)
	}
	inv.Status = "cancelled"
	if err := syncInvoiceLoyaltyOnStatusChange(db, uid, &inv, "paid"); err != nil {
		t.Fatal(err)
	}
	if _, cash := partyBalances(t, db, party.ID); cash != 100 {
		t.Fatalf("after cancel = %v, want 100", cash)
	}
	inv.Status = "paid"
	syncInvoiceLoyaltyOnStatusChange(db, uid, &inv, "cancelled")
	if _, cash := partyBalances(t, db, party.ID); cash != 170 {
		t.Fatalf("after reinstate = %v, want 170", cash)
	}
}

func TestPointsLoyaltyCancelThenDeleteNoDoubleReversal(t *testing.T) {
	db := loyaltyReversalDB(t)
	uid := uuid.New()
	party := models.Party{ID: uuid.New(), UserID: uid, Name: "Ravi", PartyType: "customer", LoyaltyPoints: 0}
	db.Create(&party)
	inv := models.Invoice{ID: uuid.New(), UserID: uid, PartyID: party.ID, InvoiceNumber: "INV-2", Status: "paid", TotalAmount: 1000}
	db.Create(&inv)
	settings := &models.LoyaltySettings{IsEnabled: true, RewardType: LoyaltyRewardPoints, SpendAmount: 100, PointsPerSpend: 1, EarnBasis: LoyaltyEarnOnBill}

	if err := db.Transaction(func(tx *gorm.DB) error { return applyLoyaltyForInvoice(tx, uid, &party, &inv, settings, 0) }); err != nil {
		t.Fatal(err)
	}
	if pts, _ := partyBalances(t, db, party.ID); pts != 10 {
		t.Fatalf("earned = %d, want 10", pts)
	}

	inv.Status = "cancelled"
	if err := syncInvoiceLoyaltyOnStatusChange(db, uid, &inv, "paid"); err != nil {
		t.Fatal(err)
	}
	if pts, _ := partyBalances(t, db, party.ID); pts != 0 {
		t.Fatalf("after cancel = %d, want 0", pts)
	}

	// Deleting the cancelled invoice must not take the points again.
	var p models.Party
	db.First(&p, "id = ?", party.ID)
	if err := db.Transaction(func(tx *gorm.DB) error { return reverseLoyaltyForInvoice(tx, uid, &p, &inv, "deletion") }); err != nil {
		t.Fatal(err)
	}
	if pts, _ := partyBalances(t, db, party.ID); pts != 0 {
		t.Fatalf("after delete = %d, want 0 (no double reversal)", pts)
	}

	// Deleting a live invoice reverses it once.
	inv2 := models.Invoice{ID: uuid.New(), UserID: uid, PartyID: party.ID, InvoiceNumber: "INV-3", Status: "paid", TotalAmount: 500}
	db.Create(&inv2)
	db.First(&p, "id = ?", party.ID)
	db.Transaction(func(tx *gorm.DB) error { return applyLoyaltyForInvoice(tx, uid, &p, &inv2, settings, 0) })
	db.First(&p, "id = ?", party.ID)
	db.Transaction(func(tx *gorm.DB) error { return reverseLoyaltyForInvoice(tx, uid, &p, &inv2, "deletion") })
	if pts, _ := partyBalances(t, db, party.ID); pts != 0 {
		t.Fatalf("after deleting live invoice = %d, want 0", pts)
	}
}
