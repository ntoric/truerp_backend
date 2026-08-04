package utils

import (
	"fmt"
	"regexp"
	"strings"
	"truerp/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var nonAlphanumeric = regexp.MustCompile(`[^a-z0-9]+`)

func IsSuperAdminRole(role string) bool {
	return role == "owner" || role == "super_admin"
}

func NormalizeStoreCode(code, name string) string {
	raw := strings.TrimSpace(strings.ToLower(code))
	if raw == "" {
		raw = strings.TrimSpace(strings.ToLower(name))
	}
	raw = nonAlphanumeric.ReplaceAllString(raw, "-")
	raw = strings.Trim(raw, "-")
	if raw == "" {
		raw = "store"
	}
	if len(raw) > 32 {
		raw = raw[:32]
		raw = strings.Trim(raw, "-")
	}
	return raw
}

func UniqueStoreCode(db *gorm.DB, base string) string {
	code := base
	for i := 1; i < 1000; i++ {
		var count int64
		db.Model(&models.Store{}).Where("code = ?", code).Count(&count)
		if count == 0 {
			return code
		}
		suffix := fmt.Sprintf("-%d", i+1)
		trim := 32 - len(suffix)
		if trim < 1 {
			trim = 1
		}
		code = base
		if len(code) > trim {
			code = code[:trim]
		}
		code = strings.Trim(code, "-") + suffix
	}
	return fmt.Sprintf("%s-%s", base[:8], uuid.New().String()[:8])
}

func FindStoreByID(db *gorm.DB, storeID uuid.UUID) (models.Store, error) {
	var store models.Store
	err := db.First(&store, "id = ?", storeID).Error
	return store, err
}

func FindStoreByOwnerUserID(db *gorm.DB, ownerUserID uuid.UUID) (models.Store, error) {
	var store models.Store
	err := db.Where("owner_user_id = ?", ownerUserID).First(&store).Error
	return store, err
}

func StorePublicJSON(store models.Store) map[string]interface{} {
	return map[string]interface{}{
		"id":          store.ID,
		"name":        store.Name,
		"code":        store.Code,
		"description": store.Description,
		"address":     store.Address,
		"city":        store.City,
		"state":       store.State,
		"pincode":     store.Pincode,
		"phone":       store.Phone,
		"email":       store.Email,
		"is_active":   store.IsActive,
		"created_at":  store.CreatedAt,
		"updated_at":  store.UpdatedAt,
	}
}
