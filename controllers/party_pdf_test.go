package controllers

import (
	"bytes"
	"testing"
	"truerp/models"
)

func TestFormatPartyAmount(t *testing.T) {
	cases := map[float64]string{
		0:          "0.00",
		999.5:      "999.50",
		1234:       "1,234.00",
		123456.78:  "1,23,456.78",
		12345678.9: "1,23,45,678.90",
		-1500:      "-1,500.00",
	}
	for in, want := range cases {
		if got := formatPartyAmount(in); got != want {
			t.Errorf("formatPartyAmount(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildPartiesPDF(t *testing.T) {
	parties := make([]models.Party, 0, 120)
	for i := 0; i < 120; i++ {
		parties = append(parties, models.Party{Name: "Party ₹ – test", PartyType: "customer", Balance: float64(i*100 - 5000)})
	}
	out, err := buildPartiesPDF("Acme", "Category: Retail", parties)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(out, []byte("%PDF")) {
		t.Fatal("output is not a PDF")
	}
	if _, err := buildPartiesPDF("", "", nil); err != nil {
		t.Fatalf("empty list: %v", err)
	}
}
