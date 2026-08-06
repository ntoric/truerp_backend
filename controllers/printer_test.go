package controllers

import (
	"testing"

	"truerp/models"
)

func TestFormatThermalQuantity(t *testing.T) {
	tests := []struct {
		qty  float64
		unit string
		want string
	}{
		{1.5, "KG", "1.5"},
		{1.5, "kg", "1.5"},
		{2, "KG", "2"},
		{2, "PCS", "2"},
		{1.25, "PCS", "1.25"},
		{0.5, "GM", "0.5"},
		{1.234, "KG", "1.234"},
		{1.200, "KG", "1.2"},
	}
	for _, tc := range tests {
		got := formatThermalQuantity(tc.qty, tc.unit)
		if got != tc.want {
			t.Errorf("formatThermalQuantity(%v, %q) = %q, want %q", tc.qty, tc.unit, got, tc.want)
		}
	}
}

func TestInvoiceItemInclusiveUnitPrice(t *testing.T) {
	item := models.InvoiceItem{
		Quantity:  1.5,
		UnitPrice: 127.12,
		TaxRate:   18,
		Total:     225,
	}
	got := invoiceItemInclusiveUnitPrice(item)
	if got < 149.99 || got > 150.01 {
		t.Errorf("inclusive unit price = %v, want ~150", got)
	}
}
