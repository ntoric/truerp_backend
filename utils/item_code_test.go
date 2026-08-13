package utils

import "testing"

func TestEAN13CheckDigit(t *testing.T) {
	// 100000000000 → check digit 5 (1+0+0 + 0*3+0*3+0*3 + 0+0+0 + 0*3+0*3+0*3 = 1 → (10-1)%10 = 9)
	// Recalculate: positions 1-12, odd positions (0-index even) ×1, odd ×3
	got := ean13CheckDigit("100000000000")
	if got != 9 {
		t.Errorf("ean13CheckDigit(100000000000) = %d, want 9", got)
	}
	if ean13CheckDigit("400638133393") != 1 {
		t.Errorf("ean13CheckDigit(400638133393) = %d, want 1", ean13CheckDigit("400638133393"))
	}
}

func TestRetailItemCodePrefixAvoidsScaleRange(t *testing.T) {
	if retailItemCodePrefix == "200" || retailItemCodePrefix == "20" {
		t.Fatal("retail item codes must not use GS1 in-store prefix 20-29")
	}
	if len(retailItemCodePrefix) != 3 {
		t.Fatalf("retail prefix %q should be 3 digits", retailItemCodePrefix)
	}
	if retailItemCodePrefix[0] == '2' && retailItemCodePrefix[1] >= '0' && retailItemCodePrefix[1] <= '9' {
		t.Fatalf("prefix %q starts with 2x and collides with scale EAN lookup", retailItemCodePrefix)
	}
}

func TestItemCodeLookupClauseIncludesCheckDigitVariant(t *testing.T) {
	clause, args := ItemCodeLookupClause("item_code", "1001234567890")
	if len(args) < 2 {
		t.Fatalf("expected 12-digit variant for 13-digit scan, got %v", args)
	}
	if args[1] != "100123456789" {
		t.Errorf("12-digit variant = %v, want 100123456789", args[1])
	}
	if clause == "" {
		t.Fatal("empty clause")
	}

	clause12, args12 := ItemCodeLookupClause("item_code", "100123456789")
	found := false
	for _, a := range args12 {
		if a == "100123456789" {
			found = true
		}
	}
	if !found {
		t.Errorf("12-digit lookup missing scanned code, args=%v clause=%s", args12, clause12)
	}
}
