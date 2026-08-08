package utils

import (
	"fmt"
	"strconv"
	"strings"
	"truerp/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const ProductPLUMaxLen = 5

func productDB(db *gorm.DB) *gorm.DB {
	if db != nil {
		return db
	}
	return DB
}

// ParseProductPLUNumber returns the numeric value when plu is a positive integer string.
func ParseProductPLUNumber(plu string) (int, bool) {
	code := strings.TrimSpace(plu)
	if code == "" {
		return 0, false
	}
	if len(code) > ProductPLUMaxLen {
		return 0, false
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(code)
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

func productPLUInUse(db *gorm.DB, userID uuid.UUID, plu string, excludeID *uuid.UUID) bool {
	code := strings.TrimSpace(plu)
	if code == "" {
		return false
	}
	query := productDB(db).Model(&models.Product{}).Where("user_id = ? AND TRIM(plu) = ?", userID, code)
	if excludeID != nil {
		query = query.Where("id <> ?", *excludeID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return true
	}
	return count > 0
}

func usedNumericPLUs(db *gorm.DB, userID uuid.UUID) map[int]struct{} {
	used := make(map[int]struct{})
	var products []models.Product
	if err := productDB(db).Select("plu").Where("user_id = ?", userID).Find(&products).Error; err != nil {
		return used
	}
	for _, p := range products {
		if n, ok := ParseProductPLUNumber(p.PLU); ok {
			used[n] = struct{}{}
		}
	}
	return used
}

// NextProductPLU returns the next ascending numeric PLU for the user's catalog.
func NextProductPLU(userID uuid.UUID) (string, error) {
	return nextProductPLU(nil, userID)
}

func nextProductPLU(db *gorm.DB, userID uuid.UUID) (string, error) {
	used := usedNumericPLUs(db, userID)
	next := 1
	for {
		if _, taken := used[next]; !taken {
			if next > 99999 {
				return "", fmt.Errorf("no available PLU numbers")
			}
			return strconv.Itoa(next), nil
		}
		next++
	}
}

// AssignProductPLU sets product.PLU to the next available code when empty.
func AssignProductPLU(userID uuid.UUID, product *models.Product) error {
	if product == nil {
		return fmt.Errorf("product is nil")
	}
	product.PLU = strings.TrimSpace(product.PLU)
	if product.PLU != "" {
		return nil
	}
	plu, err := NextProductPLU(userID)
	if err != nil {
		return err
	}
	product.PLU = plu
	return nil
}

// ValidateProductPLU checks format and uniqueness. required=true rejects empty PLU.
func ValidateProductPLU(userID uuid.UUID, plu string, excludeID *uuid.UUID, required bool) string {
	return validateProductPLU(nil, userID, plu, excludeID, required)
}

func validateProductPLU(db *gorm.DB, userID uuid.UUID, plu string, excludeID *uuid.UUID, required bool) string {
	code := strings.TrimSpace(plu)
	if code == "" {
		if required {
			return "PLU is required"
		}
		return ""
	}
	if len(code) > ProductPLUMaxLen {
		return fmt.Sprintf("PLU must be at most %d digits", ProductPLUMaxLen)
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return "PLU must contain digits only"
		}
	}
	if n, ok := ParseProductPLUNumber(code); !ok || n < 1 {
		return "PLU must be a positive number"
	}
	if productPLUInUse(db, userID, code, excludeID) {
		return "PLU is already assigned to another product"
	}
	return ""
}

// BackfillProductPLUs assigns ascending PLU numbers to products missing one, per user.
func BackfillProductPLUs(db *gorm.DB) {
	if db == nil {
		return
	}
	if !db.Migrator().HasColumn(&models.Product{}, "plu") {
		return
	}

	var userIDs []uuid.UUID
	if err := db.Model(&models.Product{}).Distinct("user_id").Pluck("user_id", &userIDs).Error; err != nil {
		return
	}

	for _, userID := range userIDs {
		used := usedNumericPLUs(db, userID)

		var products []models.Product
		if err := db.Where(
			"user_id = ? AND (plu IS NULL OR TRIM(plu) = '')",
			userID,
		).Order("created_at ASC").Find(&products).Error; err != nil {
			continue
		}
		if len(products) == 0 {
			continue
		}

		next := 1
		for _, product := range products {
			for {
				if _, taken := used[next]; !taken {
					break
				}
				next++
			}
			if next > 99999 {
				break
			}
			plu := strconv.Itoa(next)
			if err := db.Model(&product).Update("plu", plu).Error; err != nil {
				continue
			}
			used[next] = struct{}{}
			next++
		}
	}
}
