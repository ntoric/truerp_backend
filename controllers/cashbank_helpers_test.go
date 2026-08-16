package controllers

import (
	"testing"

	"github.com/google/uuid"
	"truerp/models"
)

func TestInvoiceCashLedgerNeedsResync(t *testing.T) {
	bankA := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	bankB := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	partyA := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	partyB := uuid.MustParse("44444444-4444-4444-4444-444444444444")

	base := models.Invoice{
		InvoiceNumber: "INV-0001",
		AmountPaid:    100,
		PaymentMode:   "cash",
		PartyID:       partyA,
	}

	t.Run("unchanged", func(t *testing.T) {
		current := base
		if invoiceCashLedgerNeedsResync(&base, &current) {
			t.Fatal("expected no resync when payment fields are unchanged")
		}
	})

	t.Run("amount changed", func(t *testing.T) {
		current := base
		current.AmountPaid = 80
		if !invoiceCashLedgerNeedsResync(&base, &current) {
			t.Fatal("expected resync when amount paid changes")
		}
	})

	t.Run("payment method changed", func(t *testing.T) {
		current := base
		current.PaymentMode = "upi"
		current.BankAccountID = &bankA
		if !invoiceCashLedgerNeedsResync(&base, &current) {
			t.Fatal("expected resync when payment method changes")
		}
	})

	t.Run("bank account changed", func(t *testing.T) {
		previous := base
		previous.PaymentMode = "upi"
		previous.BankAccountID = &bankA
		current := previous
		current.BankAccountID = &bankB
		if !invoiceCashLedgerNeedsResync(&previous, &current) {
			t.Fatal("expected resync when destination account changes")
		}
	})

	t.Run("party changed", func(t *testing.T) {
		current := base
		current.PartyID = partyB
		if !invoiceCashLedgerNeedsResync(&base, &current) {
			t.Fatal("expected resync when party changes")
		}
	})

	t.Run("date changed", func(t *testing.T) {
		current := base
		current.Date = current.Date.AddDate(0, 0, 1)
		if !invoiceCashLedgerNeedsResync(&base, &current) {
			t.Fatal("expected resync when invoice date changes")
		}
	})
}
