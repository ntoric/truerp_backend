package controllers

import "testing"

func TestParseInvoiceSequence(t *testing.T) {
	tests := []struct {
		number string
		prefix string
		want   int64
	}{
		{"INV-0114", "INV", 114},
		{"INV-0001", "INV", 1},
		{"inv-0014", "INV", 14},
		{"BILL-9", "BILL", 9},
		{"INV-0114", "BILL", 0},
		{"INV-0114-A", "INV", 0},
		{"", "INV", 0},
		{"INV-0114", "", 0},
		{"  INV-0020  ", "INV", 20},
	}
	for _, tt := range tests {
		if got := parseInvoiceSequence(tt.number, tt.prefix); got != tt.want {
			t.Fatalf("parseInvoiceSequence(%q, %q) = %d, want %d", tt.number, tt.prefix, got, tt.want)
		}
	}
}
