package utils

import (
	"errors"
	"log"
	"strings"
	"truerp/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// DefaultVendorName is the pre-seeded catch-all vendor for purchase invoices.
const DefaultVendorName = "General Vendor"

// EnsureDefaultVendor creates the default vendor for a user if missing.
func EnsureDefaultVendor(db *gorm.DB, userID uuid.UUID) error {
	var existing models.Party
	err := db.Where("user_id = ? AND party_type = ? AND LOWER(TRIM(name)) = ?",
		userID, "vendor", strings.ToLower(DefaultVendorName)).
		First(&existing).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	party := models.Party{
		ID:        uuid.New(),
		UserID:    userID,
		Name:      DefaultVendorName,
		PartyType: "vendor",
		Notes:     "System default vendor",
		IsActive:  true,
	}
	return db.Create(&party).Error
}

// SeedDefaultVendorsForAllUsers ensures every user has the default vendor.
func SeedDefaultVendorsForAllUsers(db *gorm.DB) {
	var userIDs []uuid.UUID
	if err := db.Model(&models.User{}).Pluck("id", &userIDs).Error; err != nil {
		log.Printf("SeedDefaultVendorsForAllUsers: failed to list users: %v", err)
		return
	}
	for _, userID := range userIDs {
		if err := EnsureDefaultVendor(db, userID); err != nil {
			log.Printf("SeedDefaultVendorsForAllUsers: ensure failed for %s: %v", userID, err)
		}
	}
}
