package controllers

import "testing"

func TestParseOptionalUUID(t *testing.T) {
	if got := parseOptionalUUID(""); got.String() != "00000000-0000-0000-0000-000000000000" {
		t.Fatalf("empty should be nil uuid, got %s", got)
	}
	if got := parseOptionalUUID("not-a-uuid"); got.String() != "00000000-0000-0000-0000-000000000000" {
		t.Fatalf("invalid should be nil uuid, got %s", got)
	}
	raw := "3d8e0c2a-1b7a-4d3e-9f11-2c4a6b8d0e1f"
	got := parseOptionalUUID(raw)
	if got.String() != raw {
		t.Fatalf("expected %s, got %s", raw, got)
	}
}
