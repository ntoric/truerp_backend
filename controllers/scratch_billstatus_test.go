package controllers

import (
	"encoding/json"
	"testing"
	"time"
	"truerp/models"

	"github.com/google/uuid"
)

func TestScratchPaymentOutUpdatesBillStatus(t *testing.T) {
	db := openPaymentCashLedgerTestDB(t)
	userID := uuid.New()
	party := seedCashLedgerVendor(t, db, userID)

	bill := models.PurchaseBill{
		ID:          uuid.New(),
		UserID:      userID,
		PartyID:     party.ID,
		BillNumber:  "BILL-1",
		BillDate:    time.Now(),
		TotalAmount: 1000,
		PaidAmount:  0,
		BalanceDue:  1000,
		Status:      "unpaid",
	}
	if err := db.Create(&bill).Error; err != nil {
		t.Fatalf("create bill: %v", err)
	}

	c, rec := postJSONContext("POST", "/payment-outs", map[string]interface{}{
		"party_id":         party.ID,
		"purchase_bill_id": bill.ID,
		"amount_paid":      400,
		"mode":             "cash",
		"date":             time.Now().Format(time.RFC3339),
	})
	c.Set("user_id", userID)
	CreatePaymentOut(c)
	if rec.Code != 201 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	var reloaded models.PurchaseBill
	db.First(&reloaded, "id = ?", bill.ID)
	t.Logf("after 400 payment: paid=%.2f due=%.2f status=%s", reloaded.PaidAmount, reloaded.BalanceDue, reloaded.Status)
	if reloaded.Status != "partial" || reloaded.PaidAmount != 400 {
		t.Fatalf("expected partial/400, got %s/%.2f", reloaded.Status, reloaded.PaidAmount)
	}

	var created models.PaymentOut
	json.Unmarshal(rec.Body.Bytes(), &created)

	// pay the rest
	c2, rec2 := postJSONContext("POST", "/payment-outs", map[string]interface{}{
		"party_id":         party.ID,
		"purchase_bill_id": bill.ID,
		"amount_paid":      600,
		"mode":             "cash",
		"date":             time.Now().Format(time.RFC3339),
	})
	c2.Set("user_id", userID)
	CreatePaymentOut(c2)
	if rec2.Code != 201 {
		t.Fatalf("status = %d: %s", rec2.Code, rec2.Body.String())
	}
	db.First(&reloaded, "id = ?", bill.ID)
	t.Logf("after full payment: paid=%.2f due=%.2f status=%s", reloaded.PaidAmount, reloaded.BalanceDue, reloaded.Status)
	if reloaded.Status != "paid" {
		t.Fatalf("expected paid, got %s", reloaded.Status)
	}

	// delete first payment -> back to partial
	dc, drec := deleteContext("/payment-outs/"+created.ID.String(), created.ID.String())
	dc.Set("user_id", userID)
	DeletePaymentOut(dc)
	if drec.Code != 200 {
		t.Fatalf("delete = %d: %s", drec.Code, drec.Body.String())
	}
	db.First(&reloaded, "id = ?", bill.ID)
	t.Logf("after delete: paid=%.2f due=%.2f status=%s", reloaded.PaidAmount, reloaded.BalanceDue, reloaded.Status)
	if reloaded.Status != "partial" || reloaded.PaidAmount != 600 {
		t.Fatalf("expected partial/600, got %s/%.2f", reloaded.Status, reloaded.PaidAmount)
	}
}
