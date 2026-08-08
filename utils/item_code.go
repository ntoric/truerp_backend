package utils

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"

	"github.com/google/uuid"
	"truerp/models"
)

func productItemCodeExists(userID uuid.UUID, code string) bool {
	code = strings.TrimSpace(code)
	if code == "" {
		return false
	}
	var count int64
	if err := DB.Model(&models.Product{}).
		Where("user_id = ? AND TRIM(item_code) = ?", userID, code).
		Limit(1).
		Count(&count).Error; err != nil {
		return true
	}
	return count > 0
}

func randomNumericDigits(length int) (string, error) {
	if length < 1 {
		return "", fmt.Errorf("invalid length")
	}
	max := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(length)), nil)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%0*d", length, n.Int64()), nil
}

// ean13CheckDigit returns the check digit for the first 12 digits of an EAN-13 code.
func ean13CheckDigit(digits12 string) int {
	sum := 0
	for i := 0; i < 12; i++ {
		d := int(digits12[i] - '0')
		if i%2 == 0 {
			sum += d
		} else {
			sum += d * 3
		}
	}
	return (10 - sum%10) % 10
}

// GenerateUniqueProductItemCode returns a unique item code / barcode for the user's catalog.
// Weighing items use a 5-digit numeric PLU; others use a 13-digit EAN-style code (prefix 200).
func GenerateUniqueProductItemCode(userID uuid.UUID, forWeighing bool) (string, error) {
	if forWeighing {
		for attempt := 0; attempt < 50; attempt++ {
			n, err := rand.Int(rand.Reader, big.NewInt(99999))
			if err != nil {
				continue
			}
			code := fmt.Sprintf("%05d", n.Int64()+1)
			if !productItemCodeExists(userID, code) {
				return code, nil
			}
		}
		return "", fmt.Errorf("failed to generate unique weighing item code")
	}

	for attempt := 0; attempt < 50; attempt++ {
		suffix, err := randomNumericDigits(9)
		if err != nil {
			continue
		}
		base12 := "200" + suffix
		code := base12 + fmt.Sprintf("%d", ean13CheckDigit(base12))
		if !productItemCodeExists(userID, code) {
			return code, nil
		}
	}
	return "", fmt.Errorf("failed to generate unique item code")
}
