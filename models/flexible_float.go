package models

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// FlexibleFloat accepts JSON numbers or numeric strings (e.g. "100.50", "1,250").
// HTML number inputs often leave string values in client state; this keeps create/update
// endpoints working for desktop WebView and browser clients.
type FlexibleFloat float64

func (f FlexibleFloat) Float64() float64 {
	return float64(f)
}

func (f *FlexibleFloat) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		*f = 0
		return nil
	}

	// JSON number (including integers)
	if data[0] != '"' {
		var num float64
		if err := json.Unmarshal(data, &num); err != nil {
			return fmt.Errorf("flexible float: %w", err)
		}
		*f = FlexibleFloat(num)
		return nil
	}

	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("flexible float: %w", err)
	}

	parsed, ok := parseFlexibleFloatString(s)
	if !ok {
		return fmt.Errorf("flexible float: cannot parse %q", s)
	}
	*f = FlexibleFloat(parsed)
	return nil
}

func parseFlexibleFloatString(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, true
	}

	s = strings.ReplaceAll(s, ",", "")
	s = strings.ReplaceAll(s, "₹", "")
	s = strings.ReplaceAll(s, "Rs.", "")
	s = strings.ReplaceAll(s, "rs.", "")
	s = strings.ReplaceAll(s, "RS.", "")
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, true
	}

	if parsed, err := strconv.ParseFloat(s, 64); err == nil {
		return parsed, true
	}

	// Allow values like "1.5 kg" / "2 pcs" from scanners or OCR
	start := -1
	for i, r := range s {
		if unicode.IsDigit(r) || r == '-' || r == '+' || r == '.' {
			start = i
			break
		}
	}
	if start < 0 {
		return 0, false
	}
	end := start + 1
	for end < len(s) {
		r := rune(s[end])
		if unicode.IsDigit(r) || r == '.' || r == 'e' || r == 'E' || r == '-' || r == '+' {
			end++
			continue
		}
		break
	}
	parsed, err := strconv.ParseFloat(s[start:end], 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func (f FlexibleFloat) MarshalJSON() ([]byte, error) {
	return json.Marshal(float64(f))
}
