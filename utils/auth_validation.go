package utils

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

var authEmailRE = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
var authPhoneRE = regexp.MustCompile(`^(\+91|91)?[6-9]\d{9}$`)

func NormalizeEmail(email string) string {
	return strings.TrimSpace(strings.ToLower(email))
}

func ValidateAuthEmail(email string) (normalized string, errMsg string) {
	email = NormalizeEmail(email)
	if email == "" {
		return "", "Email is required"
	}
	if !authEmailRE.MatchString(email) {
		return "", "Enter a valid email address"
	}
	if len(email) > 254 {
		return "", "Email is too long"
	}
	return email, ""
}

func ValidateAuthPassword(password string) string {
	if password == "" {
		return "Password is required"
	}
	if len(password) < 6 {
		return "Password must be at least 6 characters"
	}
	if len(password) > 128 {
		return "Password must be 128 characters or less"
	}
	return ""
}

func ValidateAuthName(name string) (normalized string, errMsg string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", "Name is required"
	}
	if utf8.RuneCountInString(name) < 2 {
		return "", "Name must be at least 2 characters"
	}
	if utf8.RuneCountInString(name) > 100 {
		return "", "Name must be 100 characters or less"
	}
	return name, ""
}

func ValidateAuthPhone(phone string) (normalized string, errMsg string) {
	phone = strings.TrimSpace(phone)
	if phone == "" {
		return "", ""
	}
	normalized = strings.NewReplacer(" ", "", "-", "").Replace(phone)
	if !authPhoneRE.MatchString(normalized) {
		return "", "Enter a valid 10-digit mobile number"
	}
	return phone, ""
}

func ValidateAuthTotpCode(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return "Authenticator code is required"
	}
	if len(code) != 6 {
		return "Authenticator code must be 6 digits"
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return "Authenticator code must contain only digits"
		}
	}
	return ""
}

func NormalizeAuthOTP(code string) string {
	return strings.TrimSpace(code)
}

func ValidateAuthResetOTP(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return "Verification code is required"
	}
	if len(code) != 6 {
		return "Verification code must be 6 digits"
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return "Verification code must contain only digits"
		}
	}
	return ""
}
