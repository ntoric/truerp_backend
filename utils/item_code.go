package utils

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"

	"github.com/google/uuid"
	"truerp/models"
)

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// ItemCodeLookupClause matches a scanned code against a string column.
// Includes 12↔13 digit EAN variants because some scanners omit the check digit.
func ItemCodeLookupClause(column, code string) (clause string, args []interface{}) {
	code = strings.TrimSpace(code)
	parts := []string{fmt.Sprintf("TRIM(%s) = ?", column)}
	args = []interface{}{code}
	if isAllDigits(code) && len(code) == 13 {
		parts = append(parts, fmt.Sprintf("TRIM(%s) = ?", column))
		args = append(args, code[:12])
	}
	if isAllDigits(code) && len(code) == 12 {
		parts = append(parts, fmt.Sprintf("LENGTH(TRIM(%s)) = 13 AND LEFT(TRIM(%s), 12) = ?", column, column))
		args = append(args, code)
	}
	return "(" + strings.Join(parts, " OR ") + ")", args
}

func productItemCodeExists(userID uuid.UUID, code string) bool {
	code = strings.TrimSpace(code)
	if code == "" {
		return false
	}
	clause, args := ItemCodeLookupClause("item_code", code)
	var count int64
	if err := DB.Model(&models.Product{}).
		Where("user_id = ? AND "+clause, append([]interface{}{userID}, args...)...).
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

// retailItemCodePrefix is used for generated 13-digit item codes.
// 20–29 is GS1 in-store / variable-weight, so using 200 made generated
// barcodes look like scale labels and fail normal product lookup.
const retailItemCodePrefix = "100"

// GenerateUniqueProductItemCode returns a unique item code / barcode for the user's catalog.
// Weighing items use a 5-digit numeric PLU; others use a 13-digit EAN-style code (prefix 100).
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
		base12 := retailItemCodePrefix + suffix
		code := base12 + fmt.Sprintf("%d", ean13CheckDigit(base12))
		if !productItemCodeExists(userID, code) {
			return code, nil
		}
	}
	return "", fmt.Errorf("failed to generate unique item code")
}
