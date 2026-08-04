package models

import (
	"encoding/json"
	"testing"
)

func TestFlexibleFloatUnmarshal(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    float64
		wantErr bool
	}{
		{name: "number", raw: `100.5`, want: 100.5},
		{name: "zero", raw: `0`, want: 0},
		{name: "null", raw: `null`, want: 0},
		{name: "string", raw: `"250.75"`, want: 250.75},
		{name: "comma", raw: `"1,250.50"`, want: 1250.5},
		{name: "rupee", raw: `"₹118"`, want: 118},
		{name: "rs", raw: `"Rs. 99.9"`, want: 99.9},
		{name: "empty", raw: `""`, want: 0},
		{name: "invalid", raw: `"abc"`, wantErr: true},
		{name: "with unit", raw: `"1.5 kg"`, want: 1.5},
		{name: "bool", raw: `true`, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var f FlexibleFloat
			err := json.Unmarshal([]byte(tc.raw), &f)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %v", f)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if f.Float64() != tc.want {
				t.Fatalf("got %v want %v", f.Float64(), tc.want)
			}
		})
	}
}
