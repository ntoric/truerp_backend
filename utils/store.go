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

// AssignedStoreIDs returns every store a user can operate in: their primary
// store (users.store_id) plus extra assignments from the user_stores table.
// The primary store always comes first; duplicates are removed.
func AssignedStoreIDs(db *gorm.DB, userID uuid.UUID, primaryStoreID *uuid.UUID) []uuid.UUID {
	ids := []uuid.UUID{}
	seen := map[uuid.UUID]bool{}
	if primaryStoreID != nil && *primaryStoreID != uuid.Nil {
		seen[*primaryStoreID] = true
		ids = append(ids, *primaryStoreID)
	}
	var extras []uuid.UUID
	db.Model(&models.UserStore{}).Where("user_id = ?", userID).Pluck("store_id", &extras)
	for _, id := range extras {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

// ReplaceExtraStoreAssignments rewrites a user's user_stores rows to exactly
// the given set. Callers pass only extra stores; the primary store lives on
// users.store_id.
func ReplaceExtraStoreAssignments(db *gorm.DB, userID uuid.UUID, storeIDs []uuid.UUID) error {
	if err := db.Where("user_id = ?", userID).Delete(&models.UserStore{}).Error; err != nil {
		return err
	}
	for _, storeID := range storeIDs {
		row := models.UserStore{ID: uuid.New(), UserID: userID, StoreID: storeID}
		if err := db.Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

func StorePublicJSON(store models.Store) map[string]interface{} {
	return map[string]interface{}{
		"id":                  store.ID,
		"name":                store.Name,
		"code":                store.Code,
		"description":         store.Description,
		"address":             store.Address,
		"city":                store.City,
		"state":               store.State,
		"pincode":             store.Pincode,
		"phone":               store.Phone,
		"email":               store.Email,
		"is_active":           store.IsActive,
		"force_update":        store.ForceUpdate,
		"app_version":         store.AppVersion,
		"app_version_seen_at": store.AppVersionSeenAt,
		"created_at":          store.CreatedAt,
		"updated_at":          store.UpdatedAt,
	}
}
