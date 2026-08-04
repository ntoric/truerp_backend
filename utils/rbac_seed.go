package utils

import (
	"truerp/models"
	"log"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var defaultPermissions = []models.Permission{
	{Name: "invoices.read", Resource: "invoices", Action: "read", Description: "View invoices"},
	{Name: "invoices.create", Resource: "invoices", Action: "create", Description: "Create invoices"},
	{Name: "invoices.update", Resource: "invoices", Action: "update", Description: "Edit invoices"},
	{Name: "invoices.delete", Resource: "invoices", Action: "delete", Description: "Delete invoices"},
	{Name: "payments.read", Resource: "payments", Action: "read", Description: "View payments"},
	{Name: "payments.create", Resource: "payments", Action: "create", Description: "Record payments"},
	{Name: "parties.read", Resource: "parties", Action: "read", Description: "View parties"},
	{Name: "parties.create", Resource: "parties", Action: "create", Description: "Create parties"},
	{Name: "parties.update", Resource: "parties", Action: "update", Description: "Edit parties"},
	{Name: "products.read", Resource: "products", Action: "read", Description: "View products"},
	{Name: "products.create", Resource: "products", Action: "create", Description: "Create products"},
	{Name: "products.update", Resource: "products", Action: "update", Description: "Edit products"},
	{Name: "reports.read", Resource: "reports", Action: "read", Description: "View reports"},
	{Name: "reports.export", Resource: "reports", Action: "export", Description: "Export reports"},
	{Name: "settings.read", Resource: "settings", Action: "read", Description: "View settings"},
	{Name: "settings.update", Resource: "settings", Action: "update", Description: "Update settings"},
	{Name: "users.read", Resource: "users", Action: "read", Description: "View users"},
	{Name: "users.create", Resource: "users", Action: "create", Description: "Create users"},
	{Name: "users.update", Resource: "users", Action: "update", Description: "Update users"},
	{Name: "users.delete", Resource: "users", Action: "delete", Description: "Delete users"},
	{Name: "audit.read", Resource: "audit", Action: "read", Description: "View audit logs"},
}

func SeedPermissions(db *gorm.DB) {
	var count int64
	db.Model(&models.Permission{}).Count(&count)
	if count > 0 {
		return
	}
	for _, p := range defaultPermissions {
		p.ID = uuid.New()
		if err := db.Create(&p).Error; err != nil {
			log.Printf("Warning: failed to seed permission %s: %v", p.Name, err)
		}
	}
}

func EnsureDefaultRoles(db *gorm.DB, ownerUserID uuid.UUID) {
	SeedPermissions(db)

	var existing int64
	db.Model(&models.Role{}).Where("user_id = ?", ownerUserID).Count(&existing)
	if existing > 0 {
		return
	}

	var allPerms []models.Permission
	db.Find(&allPerms)

	readOnly := filterPermissions(allPerms, func(p models.Permission) bool {
		return p.Action == "read"
	})

	staffPerms := filterPermissions(allPerms, func(p models.Permission) bool {
		return p.Resource != "users" && p.Resource != "settings" && p.Resource != "audit"
	})

	roles := []struct {
		name        string
		description string
		isDefault   bool
		perms       []models.Permission
	}{
		{"Super Admin", "Full access to all modules", true, allPerms},
		{"Admin", "Manage operations except destructive user actions", false, staffPerms},
		{"Staff", "Day-to-day billing and inventory", false, readOnly},
	}

	for _, r := range roles {
		role := models.Role{
			ID:          uuid.New(),
			UserID:      ownerUserID,
			Name:        r.name,
			Description: r.description,
			IsDefault:   r.isDefault,
			IsActive:    true,
		}
		if err := db.Create(&role).Error; err != nil {
			continue
		}
		if len(r.perms) > 0 {
			db.Model(&role).Association("Permissions").Append(&r.perms)
		}
	}
}

func filterPermissions(all []models.Permission, keep func(models.Permission) bool) []models.Permission {
	out := make([]models.Permission, 0, len(all))
	for _, p := range all {
		if keep(p) {
			out = append(out, p)
		}
	}
	return out
}
