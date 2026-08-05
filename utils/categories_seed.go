package utils

import (
	"errors"
	"log"
	"strings"
	"truerp/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// DefaultCategoryName is the pre-seeded catch-all for products and expenses.
const DefaultCategoryName = "General"

// ResolveCategoryName returns DefaultCategoryName when category is blank.
func ResolveCategoryName(category string) string {
	if strings.TrimSpace(category) == "" {
		return DefaultCategoryName
	}
	return strings.TrimSpace(category)
}

// EnsureDefaultCategories creates the "General" product and expense categories for a user if missing.
func EnsureDefaultCategories(db *gorm.DB, userID uuid.UUID) error {
	if err := ensureProductCategory(db, userID, DefaultCategoryName); err != nil {
		return err
	}
	return ensureExpenseCategory(db, userID, DefaultCategoryName)
}

func ensureProductCategory(db *gorm.DB, userID uuid.UUID, name string) error {
	var existing models.Category
	err := db.Where("user_id = ? AND name = ?", userID, name).First(&existing).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	cat := models.Category{
		ID:       uuid.New(),
		UserID:   userID,
		Name:     name,
		IsActive: true,
	}
	return db.Create(&cat).Error
}

func ensureExpenseCategory(db *gorm.DB, userID uuid.UUID, name string) error {
	var existing models.ExpenseCategory
	err := db.Where("user_id = ? AND name = ?", userID, name).First(&existing).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	cat := models.ExpenseCategory{
		ID:       uuid.New(),
		UserID:   userID,
		Name:     name,
		IsActive: true,
	}
	return db.Create(&cat).Error
}

// SeedDefaultCategoriesForAllUsers ensures every user has General categories and
// backfills products/expenses that have a blank category string.
func SeedDefaultCategoriesForAllUsers(db *gorm.DB) {
	var userIDs []uuid.UUID
	if err := db.Model(&models.User{}).Pluck("id", &userIDs).Error; err != nil {
		log.Printf("SeedDefaultCategoriesForAllUsers: failed to list users: %v", err)
		return
	}
	for _, userID := range userIDs {
		if err := EnsureDefaultCategories(db, userID); err != nil {
			log.Printf("SeedDefaultCategoriesForAllUsers: ensure failed for %s: %v", userID, err)
		}
	}

	if err := db.Model(&models.Product{}).
		Where("category = '' OR category IS NULL").
		Update("category", DefaultCategoryName).Error; err != nil {
		log.Printf("SeedDefaultCategoriesForAllUsers: product backfill failed: %v", err)
	}

	if err := db.Model(&models.Expense{}).
		Where("category = '' OR category IS NULL").
		Update("category", DefaultCategoryName).Error; err != nil {
		log.Printf("SeedDefaultCategoriesForAllUsers: expense backfill failed: %v", err)
	}
}
