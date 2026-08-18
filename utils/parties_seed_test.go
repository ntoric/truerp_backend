package utils

import (
	"testing"
	"truerp/models"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestEnsureDefaultVendorCreatesOnce(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.User{}, &models.Party{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	userID := uuid.New()
	if err := db.Create(&models.User{ID: userID, Name: "Owner", Email: userID.String() + "@example.com", Password: "x", Role: "owner", IsActive: true}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	if err := EnsureDefaultVendor(db, userID); err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if err := EnsureDefaultVendor(db, userID); err != nil {
		t.Fatalf("second ensure: %v", err)
	}

	var count int64
	if err := db.Model(&models.Party{}).Where("user_id = ? AND party_type = ?", userID, "vendor").Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 default vendor, got %d", count)
	}

	var party models.Party
	if err := db.Where("user_id = ?", userID).First(&party).Error; err != nil {
		t.Fatalf("load party: %v", err)
	}
	if party.Name != DefaultVendorName {
		t.Fatalf("name = %q, want %q", party.Name, DefaultVendorName)
	}
}
