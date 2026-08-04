package controllers

import (
	"truerp/models"
	"truerp/utils"
	"log"
	"os"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// EnsureDefaultSuperAdmin creates the bootstrap super admin from environment variables
// when SUPER_ADMIN_EMAIL and SUPER_ADMIN_PASSWORD are set and that user does not exist yet.
func EnsureDefaultSuperAdmin() {
	email := strings.TrimSpace(os.Getenv("SUPER_ADMIN_EMAIL"))
	password := os.Getenv("SUPER_ADMIN_PASSWORD")
	if email == "" || password == "" {
		return
	}
	if len(password) < 6 {
		log.Println("SUPER_ADMIN_PASSWORD must be at least 6 characters; skipping default super admin bootstrap")
		return
	}

	var existing models.User
	if err := utils.DB.Where("email = ?", email).First(&existing).Error; err == nil {
		// User already exists — still ensure they have a business record.
		if _, err := ensureBusinessForUser(existing.ID); err != nil {
			log.Printf("Default super admin bootstrap: failed to ensure business: %v", err)
		}
		return
	} else if err != gorm.ErrRecordNotFound {
		log.Printf("Default super admin bootstrap: database error: %v", err)
		return
	}

	name := strings.TrimSpace(os.Getenv("SUPER_ADMIN_NAME"))
	if name == "" {
		name = "Super Admin"
	}
	phone := strings.TrimSpace(os.Getenv("SUPER_ADMIN_PHONE"))

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		log.Printf("Default super admin bootstrap: failed to hash password: %v", err)
		return
	}

	user := models.User{
		ID:       uuid.New(),
		Name:     name,
		Email:    email,
		Password: string(hashedPassword),
		Phone:    phone,
		Role:     "super_admin",
		IsActive: true,
	}

	if err := utils.DB.Create(&user).Error; err != nil {
		log.Printf("Default super admin bootstrap: failed to create user: %v", err)
		return
	}

	if err := EnsureDefaultChartOfAccounts(utils.DB, user.ID); err != nil {
		log.Printf("Default super admin bootstrap: failed to init accounting: %v", err)
	}

	business := models.Business{
		ID:     uuid.New(),
		UserID: user.ID,
		Name:   name + "'s Business",
	}
	if err := utils.DB.Create(&business).Error; err != nil {
		log.Printf("Default super admin bootstrap: failed to create business: %v", err)
	}

	utils.EnsureDefaultRoles(utils.DB, user.ID)

	log.Printf("Default super admin account ready for %s", email)
}
