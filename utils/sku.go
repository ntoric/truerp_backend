package utils

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
	"unicode"

	"truerp/models"
)

const maxSKUBaseLen = 24

// SKUFromName builds a SKU base from a product name (uppercase, hyphenated alphanumerics).
func SKUFromName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "PROD"
	}

	var b strings.Builder
	prevHyphen := true // avoid leading hyphen
	for _, r := range strings.ToUpper(name) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			prevHyphen = false
		default:
			if !prevHyphen && b.Len() > 0 {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
	}

	base := strings.Trim(b.String(), "-")
	if base == "" {
		return "PROD"
	}
	if len(base) > maxSKUBaseLen {
		base = strings.Trim(base[:maxSKUBaseLen], "-")
		if base == "" {
			return "PROD"
		}
	}
	return base
}

func randomSKUSuffix(digits int) (string, error) {
	if digits < 1 {
		digits = 4
	}
	max := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(digits)), nil)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%0*d", digits, n.Int64()), nil
}

func skuExists(sku string) bool {
	var count int64
	if err := DB.Model(&models.Product{}).Where("sku = ?", sku).Limit(1).Count(&count).Error; err != nil {
		return true
	}
	return count > 0
}

// GenerateUniqueProductSKU returns a unique SKU derived from the product name.
// If the name-based SKU is taken, a random numeric suffix is appended.
func GenerateUniqueProductSKU(name string) string {
	base := SKUFromName(name)
	if !skuExists(base) {
		return base
	}

	for attempt := 0; attempt < 20; attempt++ {
		digits := 4
		if attempt >= 10 {
			digits = 6
		}
		suffix, err := randomSKUSuffix(digits)
		if err != nil {
			continue
		}
		candidate := base + "-" + suffix
		if !skuExists(candidate) {
			return candidate
		}
	}

	// Last resort: longer random suffix
	suffix, err := randomSKUSuffix(8)
	if err != nil {
		return base + "-" + fmt.Sprintf("%d", randFallback())
	}
	return base + "-" + suffix
}

func randFallback() int64 {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000_000))
	if err != nil {
		return 0
	}
	return n.Int64()
}
