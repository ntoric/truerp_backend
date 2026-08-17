package controllers

import (
	"testing"
	"truerp/models"
)

func TestMergePaymentSplits(t *testing.T) {
	got := mergePaymentSplits([]models.PaymentSplit{
		{Mode: "cash", Amount: 50},
		{Mode: "UPI", Amount: 30},
		{Mode: "cash", Amount: 20},
		{Mode: "card", Amount: 0},
	})
	if len(got) != 2 {
		t.Fatalf("got %d splits, want 2", len(got))
	}
	if got[0].Mode != "cash" || got[0].Amount != 70 {
		t.Fatalf("first split = %+v, want cash 70", got[0])
	}
	if got[1].Mode != "upi" || got[1].Amount != 30 {
		t.Fatalf("second split = %+v, want upi 30", got[1])
	}
}

func TestAdjustPaymentSplitsToAmount(t *testing.T) {
	existing := []models.PaymentSplit{
		{Mode: "cash", Amount: 40},
		{Mode: "upi", Amount: 40},
	}

	t.Run("increase adds to last method", func(t *testing.T) {
		got := adjustPaymentSplitsToAmount(existing, 100)
		if len(got) != 2 || got[0].Amount != 40 || got[1].Amount != 60 {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("decrease trims last method", func(t *testing.T) {
		got := adjustPaymentSplitsToAmount(existing, 50)
		if len(got) != 2 || got[0].Amount != 40 || got[1].Amount != 10 {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("decrease drops last method", func(t *testing.T) {
		got := adjustPaymentSplitsToAmount(existing, 40)
		if len(got) != 1 || got[0].Mode != "cash" || got[0].Amount != 40 {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("zero clears", func(t *testing.T) {
		got := adjustPaymentSplitsToAmount(existing, 0)
		if got != nil {
			t.Fatalf("got %+v, want nil", got)
		}
	})
}

func TestPaymentSplitsEqual(t *testing.T) {
	a := []models.PaymentSplit{{Mode: "cash", Amount: 50}, {Mode: "upi", Amount: 50}}
	b := []models.PaymentSplit{{Mode: "CASH", Amount: 50}, {Mode: "upi", Amount: 50}}
	if !paymentSplitsEqual(a, b) {
		t.Fatal("expected equal after normalize")
	}
	c := []models.PaymentSplit{{Mode: "cash", Amount: 100}}
	if paymentSplitsEqual(a, c) {
		t.Fatal("expected different splits")
	}
}

func TestSelectInvoicePaymentSplits(t *testing.T) {
	existing := []models.PaymentSplit{
		{Mode: "cash", Amount: 40},
		{Mode: "upi", Amount: 60},
	}

	t.Run("omitted input keeps existing methods", func(t *testing.T) {
		got := selectInvoicePaymentSplits(nil, existing, 100)
		if len(got) != 2 || got[0].Mode != "cash" || got[0].Amount != 40 || got[1].Mode != "upi" || got[1].Amount != 60 {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("omitted input resizes last method", func(t *testing.T) {
		got := selectInvoicePaymentSplits(nil, existing, 70)
		if len(got) != 2 || got[0].Amount != 40 || got[1].Mode != "upi" || got[1].Amount != 30 {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("explicit splits replace existing", func(t *testing.T) {
		got := selectInvoicePaymentSplits([]models.PaymentSplit{
			{Mode: "card", Amount: 25},
			{Mode: "upi", Amount: 75},
		}, existing, 100)
		if len(got) != 2 || got[0].Mode != "card" || got[1].Mode != "upi" {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("omitted with no existing uses fallback later", func(t *testing.T) {
		got := selectInvoicePaymentSplits(nil, nil, 100)
		if got != nil {
			t.Fatalf("got %+v, want nil so payment_mode fallback applies", got)
		}
	})

	t.Run("empty list clears payment", func(t *testing.T) {
		got := selectInvoicePaymentSplits([]models.PaymentSplit{}, existing, 100)
		if len(got) != 0 {
			t.Fatalf("got %+v, want empty", got)
		}
	})
}

func TestFormatPaymentSplitsLabel(t *testing.T) {
	if got := formatPaymentSplitsLabel(nil, "upi"); got != "UPI" {
		t.Fatalf("fallback = %q", got)
	}
	got := formatPaymentSplitsLabel([]models.PaymentSplit{
		{Mode: "cash", Amount: 50},
		{Mode: "upi", Amount: 50},
	}, "cash")
	if got != "Cash + UPI" {
		t.Fatalf("label = %q, want Cash + UPI", got)
	}
}
