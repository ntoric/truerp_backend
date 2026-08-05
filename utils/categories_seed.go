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

type defaultExpenseCategory struct {
	Name        string
	Description string
}

// DefaultExpenseCategories are seeded for every user on signup and startup backfill.
var DefaultExpenseCategories = []defaultExpenseCategory{
	{Name: DefaultCategoryName, Description: "General and uncategorized expenses"},
	{Name: "Rent", Description: "Shop, office, or warehouse rent"},
	{Name: "Utilities", Description: "Electricity, water, gas, and other utilities"},
	{Name: "Payroll", Description: "Salaries, wages, and staff payments"},
	{Name: "Office Supplies", Description: "Stationery, printing, and office consumables"},
	{Name: "Travel & Conveyance", Description: "Travel, fuel, and transportation expenses"},
	{Name: "Marketing & Advertising", Description: "Promotions, ads, and marketing spend"},
	{Name: "Repairs & Maintenance", Description: "Equipment and property maintenance"},
	{Name: "Professional Fees", Description: "Legal, accounting, and consulting fees"},
	{Name: "Insurance", Description: "Business and asset insurance premiums"},
	{Name: "Bank Charges", Description: "Bank fees, interest, and transaction charges"},
	{Name: "Telephone & Internet", Description: "Mobile, landline, and internet bills"},
	{Name: "Miscellaneous", Description: "Other expenses not covered elsewhere"},
}

// ResolveCategoryName returns DefaultCategoryName when category is blank.
func ResolveCategoryName(category string) string {
	if strings.TrimSpace(category) == "" {
		return DefaultCategoryName
	}
	return strings.TrimSpace(category)
}

// EnsureDefaultCategories creates default product and expense categories for a user if missing.
func EnsureDefaultCategories(db *gorm.DB, userID uuid.UUID) error {
	if err := ensureProductCategory(db, userID, DefaultCategoryName); err != nil {
		return err
	}
	return EnsureDefaultExpenseCategories(db, userID)
}

// EnsureDefaultExpenseCategories seeds common expense categories for a user if missing.
func EnsureDefaultExpenseCategories(db *gorm.DB, userID uuid.UUID) error {
	for _, cat := range DefaultExpenseCategories {
		if err := ensureExpenseCategory(db, userID, cat.Name, cat.Description); err != nil {
			return err
		}
	}
	return nil
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

func ensureExpenseCategory(db *gorm.DB, userID uuid.UUID, name, description string) error {
	var existing models.ExpenseCategory
	err := db.Where("user_id = ? AND name = ?", userID, name).First(&existing).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	cat := models.ExpenseCategory{
		ID:          uuid.New(),
		UserID:      userID,
		Name:        name,
		Description: description,
		IsActive:    true,
	}
	return db.Create(&cat).Error
}

// SeedDefaultCategoriesForAllUsers ensures every user has default product/expense categories and
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
