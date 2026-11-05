package controllers

import (
	"crypto/rand"
	"encoding/base32"
	"net/http"
	"net/url"
	"strings"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

func canManageUsers(role string) bool {
	switch role {
	case "owner", "super_admin", "admin":
		return true
	default:
		return false
	}
}

func isProtectedRole(role string) bool {
	return role == "owner" || role == "super_admin"
}

// Roles a store admin may assign when creating/updating users.
func isStoreAdminAssignableRole(role string) bool {
	return role == "admin" || role == "staff"
}

func actorCanAccessUser(c *gin.Context, actor models.User, target models.User) bool {
	if utils.IsSuperAdminRole(actor.Role) {
		return true
	}
	if actor.Role != "admin" {
		return false
	}
	storeID, ok := resolveManagedStoreID(c, actor)
	if !ok {
		return false
	}
	if target.StoreID != nil && *target.StoreID == storeID {
		return true
	}
	var count int64
	utils.DB.Model(&models.UserStore{}).
		Where("user_id = ? AND store_id = ?", target.ID, storeID).
		Count(&count)
	return count > 0
}

// usersAssignedToStoreQuery selects non-owner users assigned to a store,
// either as their primary store (users.store_id) or as an extra assignment
// (user_stores).
func usersAssignedToStoreQuery(storeID uuid.UUID) *gorm.DB {
	extra := utils.DB.Model(&models.UserStore{}).Select("user_id").Where("store_id = ?", storeID)
	return utils.DB.Model(&models.User{}).
		Where("is_store_owner = ? AND (store_id = ? OR id IN (?))", false, storeID, extra)
}

func resolveManagedStoreID(c *gin.Context, actor models.User) (uuid.UUID, bool) {
	if utils.IsSuperAdminRole(actor.Role) {
		if storeID, ok := currentStoreID(c); ok {
			return storeID, true
		}
		return uuid.Nil, false
	}
	// Follow the actor's currently selected store so multi-store admins
	// manage the store they switched into.
	if storeID, ok := currentStoreID(c); ok {
		return storeID, true
	}
	if actor.StoreID != nil {
		return *actor.StoreID, true
	}
	return uuid.Nil, false
}

func userPublicResponse(u models.User) gin.H {
	resp := gin.H{
		"id":                   u.ID,
		"name":                 u.Name,
		"email":                u.Email,
		"phone":                u.Phone,
		"role":                 u.Role,
		"is_active":            u.IsActive,
		"two_factor_enabled":   u.TwoFactorEnabled,
		"must_change_password": u.MustChangePassword,
		"created_at":           u.CreatedAt,
	}
	if u.StoreID != nil {
		resp["store_id"] = u.StoreID
	}
	return resp
}

func GetUserManagementOverview(c *gin.Context) {
	actor, err := loadActor(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}
	if !canManageUsers(actor.Role) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Insufficient permissions"})
		return
	}

	scopeID := c.MustGet("user_id").(uuid.UUID)
	var users []models.User
	if utils.IsSuperAdminRole(actor.Role) {
		if c.Query("all") == "true" {
			utils.DB.Where("is_store_owner = ?", false).Find(&users)
		} else if storeID, ok := currentStoreID(c); ok {
			usersAssignedToStoreQuery(storeID).Find(&users)
		} else {
			utils.DB.Where("is_store_owner = ?", false).Find(&users)
		}
	} else {
		storeID, ok := resolveManagedStoreID(c, actor)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Store assignment required"})
			return
		}
		usersAssignedToStoreQuery(storeID).Find(&users)
	}

	countByRole := map[string]int64{}
	var twoFactorCount int64
	for _, u := range users {
		countByRole[u.Role]++
		if u.TwoFactorEnabled {
			twoFactorCount++
		}
	}

	var roleCount int64
	utils.DB.Model(&models.Role{}).Where("user_id = ?", scopeID).Count(&roleCount)

	var activityToday int64
	utils.DB.Model(&models.AuditLog{}).Where("user_id = ?", scopeID).Where("DATE(created_at) = DATE('now')").Count(&activityToday)

	var auditTotal int64
	utils.DB.Model(&models.AuditLog{}).Where("user_id = ?", scopeID).Count(&auditTotal)

	c.JSON(http.StatusOK, gin.H{
		"total_users":        len(users),
		"super_admin_count":  countByRole["owner"] + countByRole["super_admin"],
		"admin_count":        countByRole["admin"],
		"staff_count":        countByRole["staff"] + countByRole["accountant"] + countByRole["manager"],
		"roles_defined":      roleCount,
		"two_factor_enabled": twoFactorCount,
		"activity_today":     activityToday,
		"audit_total":        auditTotal,
	})
}

func UpdateBusinessUser(c *gin.Context) {
	targetID := c.Param("id")
	actor, err := loadActor(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}
	if !canManageUsers(actor.Role) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Insufficient permissions"})
		return
	}

	var target models.User
	if err := utils.DB.First(&target, "id = ?", targetID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	if !actorCanAccessUser(c, actor, target) {
		c.JSON(http.StatusForbidden, gin.H{"error": "User does not belong to your store"})
		return
	}

	if (isProtectedRole(target.Role) || target.IsStoreOwner) && actor.ID != target.ID {
		c.JSON(http.StatusForbidden, gin.H{"error": "Cannot modify super admin accounts"})
		return
	}

	var input struct {
		Name     string    `json:"name"`
		Phone    string    `json:"phone"`
		Role     string    `json:"role"`
		IsActive *bool     `json:"is_active"`
		Password string    `json:"password"`
		StoreID  *string   `json:"store_id"`
		StoreIDs *[]string `json:"store_ids"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	updates := map[string]interface{}{}
	if input.Name != "" {
		updates["name"] = input.Name
	}
	if input.Phone != "" {
		updates["phone"] = input.Phone
	}
	if input.Role != "" {
		role, ok := normalizeAllowedRole(input.Role)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid role"})
			return
		}
		if isProtectedRole(role) && !utils.IsSuperAdminRole(actor.Role) {
			c.JSON(http.StatusForbidden, gin.H{"error": "Only super admins can assign super admin role"})
			return
		}
		if !utils.IsSuperAdminRole(actor.Role) && !isStoreAdminAssignableRole(role) {
			c.JSON(http.StatusForbidden, gin.H{"error": "Store admins can only assign admin or staff roles"})
			return
		}
		if target.Role == "owner" && role != "owner" {
			c.JSON(http.StatusForbidden, gin.H{"error": "Cannot change business owner role"})
			return
		}
		updates["role"] = role
	}
	if input.IsActive != nil {
		if isProtectedRole(target.Role) {
			c.JSON(http.StatusForbidden, gin.H{"error": "Cannot deactivate super admin"})
			return
		}
		updates["is_active"] = *input.IsActive
	}
	if input.Password != "" {
		if len(input.Password) < 6 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Password must be at least 6 characters"})
			return
		}
		hashed, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to hash password"})
			return
		}
		updates["password"] = string(hashed)
	}
	var singleStoreID *uuid.UUID
	if input.StoreID != nil {
		if !utils.IsSuperAdminRole(actor.Role) {
			c.JSON(http.StatusForbidden, gin.H{"error": "Only super admins can change user stores"})
			return
		}
		if isProtectedRole(target.Role) || target.IsStoreOwner {
			c.JSON(http.StatusForbidden, gin.H{"error": "Cannot change store for super admin accounts"})
			return
		}
		storeID, err := uuid.Parse(strings.TrimSpace(*input.StoreID))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid store_id"})
			return
		}
		if _, err := utils.FindStoreByID(utils.DB, storeID); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Store not found"})
			return
		}
		singleStoreID = &storeID
		updates["store_id"] = storeID
	}

	var newExtraIDs []uuid.UUID
	if input.StoreIDs != nil {
		if !utils.IsSuperAdminRole(actor.Role) {
			c.JSON(http.StatusForbidden, gin.H{"error": "Only super admins can change user stores"})
			return
		}
		if isProtectedRole(target.Role) || target.IsStoreOwner {
			c.JSON(http.StatusForbidden, gin.H{"error": "Cannot change store for super admin accounts"})
			return
		}
		newStoreIDs := []uuid.UUID{}
		seen := map[uuid.UUID]bool{}
		for _, raw := range *input.StoreIDs {
			storeID, err := uuid.Parse(strings.TrimSpace(raw))
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid store_ids"})
				return
			}
			if seen[storeID] {
				continue
			}
			if _, err := utils.FindStoreByID(utils.DB, storeID); err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "Store not found"})
				return
			}
			seen[storeID] = true
			newStoreIDs = append(newStoreIDs, storeID)
		}
		// Keep the current primary store when it's still assigned; otherwise
		// the first listed store becomes primary and the rest become extras.
		primary := uuid.Nil
		for _, id := range newStoreIDs {
			if target.StoreID != nil && id == *target.StoreID {
				primary = id
				break
			}
		}
		if primary == uuid.Nil && len(newStoreIDs) > 0 {
			primary = newStoreIDs[0]
		}
		if primary == uuid.Nil {
			updates["store_id"] = nil
		} else {
			updates["store_id"] = primary
		}
		for _, id := range newStoreIDs {
			if id != primary {
				newExtraIDs = append(newExtraIDs, id)
			}
		}
	}

	if len(updates) == 0 && input.StoreIDs == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No updates provided"})
		return
	}

	err = utils.DB.Transaction(func(tx *gorm.DB) error {
		if len(updates) > 0 {
			if err := tx.Model(&target).Updates(updates).Error; err != nil {
				return err
			}
		}
		if input.StoreIDs != nil {
			if err := utils.ReplaceExtraStoreAssignments(tx, target.ID, newExtraIDs); err != nil {
				return err
			}
		} else if singleStoreID != nil {
			// Primary store changed via the legacy field — drop any duplicate
			// extra assignment pointing at the new primary.
			if err := tx.Where("user_id = ? AND store_id = ?", target.ID, *singleStoreID).
				Delete(&models.UserStore{}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update user"})
		return
	}

	utils.DB.First(&target, "id = ?", targetID)
	CreateAuditLog(
		actor.ID,
		actor.Name,
		"update",
		"user",
		&target.ID,
		target.Email,
		"Updated user account",
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		updates,
		"success",
		"",
	)

	c.JSON(http.StatusOK, userPublicResponse(target))
}

func GetActivityLogs(c *gin.Context) {
	actor, err := loadActor(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}
	if !isProtectedRole(actor.Role) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Super admin access required"})
		return
	}

	scopeID := c.MustGet("user_id").(uuid.UUID)
	var logs []models.AuditLog
	q := utils.DB.Where("user_id = ?", scopeID).Order("created_at DESC").Limit(100)
	if action := c.Query("action"); action != "" {
		q = q.Where("action = ?", action)
	}
	if err := q.Find(&logs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch activity logs"})
		return
	}

	c.JSON(http.StatusOK, logs)
}

func buildOtpAuthURL(email, secret string) string {
	issuer := "TruERP"
	label := url.PathEscape(issuer + ":" + email)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	return "otpauth://totp/" + label + "?" + q.Encode()
}

func GetTwoFactorStatus(c *gin.Context) {
	userID := actorUserID(c)
	var user models.User
	if err := utils.DB.First(&user, "id = ?", userID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"two_factor_enabled": user.TwoFactorEnabled,
		"has_pending_secret": user.TotpSecret != "" && !user.TwoFactorEnabled,
		"secret":             pendingSecret(user),
		"otpauth_url":        pendingOtpAuthURL(user),
	})
}

func pendingSecret(user models.User) string {
	if user.TotpSecret != "" && !user.TwoFactorEnabled {
		return user.TotpSecret
	}
	return ""
}

func pendingOtpAuthURL(user models.User) string {
	if user.TotpSecret != "" && !user.TwoFactorEnabled {
		return buildOtpAuthURL(user.Email, user.TotpSecret)
	}
	return ""
}

func SetupTwoFactor(c *gin.Context) {
	userID := actorUserID(c)
	var user models.User
	if err := utils.DB.First(&user, "id = ?", userID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	secretBytes := make([]byte, 20)
	if _, err := rand.Read(secretBytes); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate secret"})
		return
	}
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secretBytes)

	if err := utils.DB.Model(&user).Updates(map[string]interface{}{
		"totp_secret":        secret,
		"two_factor_enabled": false,
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save secret"})
		return
	}

	otpauthURL := buildOtpAuthURL(user.Email, secret)

	c.JSON(http.StatusOK, gin.H{
		"secret":      secret,
		"otpauth_url": otpauthURL,
	})
}

func EnableTwoFactor(c *gin.Context) {
	userID := actorUserID(c)
	var input struct {
		Code string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var user models.User
	if err := utils.DB.First(&user, "id = ?", userID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}
	if user.TotpSecret == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Run setup before enabling 2FA"})
		return
	}
	if !totp.Validate(input.Code, user.TotpSecret) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid verification code"})
		return
	}

	utils.DB.Model(&user).Update("two_factor_enabled", true)
	CreateAuditLog(userID, user.Name, "update", "user", &userID, user.Email, "Enabled two-factor authentication", c.ClientIP(), c.GetHeader("User-Agent"), nil, "success", "")

	c.JSON(http.StatusOK, gin.H{"message": "Two-factor authentication enabled"})
}

func DisableTwoFactor(c *gin.Context) {
	userID := actorUserID(c)
	var input struct {
		Password string `json:"password" binding:"required"`
		Code     string `json:"code"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var user models.User
	if err := utils.DB.First(&user, "id = ?", userID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(input.Password)); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid password"})
		return
	}
	if user.TwoFactorEnabled && input.Code != "" && !totp.Validate(input.Code, user.TotpSecret) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid verification code"})
		return
	}

	utils.DB.Model(&user).Updates(map[string]interface{}{
		"two_factor_enabled": false,
		"totp_secret":        "",
	})
	CreateAuditLog(userID, user.Name, "update", "user", &userID, user.Email, "Disabled two-factor authentication", c.ClientIP(), c.GetHeader("User-Agent"), nil, "success", "")

	c.JSON(http.StatusOK, gin.H{"message": "Two-factor authentication disabled"})
}

func normalizeAllowedRole(role string) (string, bool) {
	role = strings.TrimSpace(strings.ToLower(role))
	switch role {
	case "super_admin", "admin", "staff", "accountant", "manager", "owner":
		return role, true
	default:
		return "", false
	}
}

// ResetUserPassword generates a temporary password for a user (super admin only).
func ResetUserPassword(c *gin.Context) {
	actor, err := loadActor(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	if !utils.IsSuperAdminRole(actor.Role) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Super admin access required"})
		return
	}

	targetID, parseErr := uuid.Parse(c.Param("id"))
	if parseErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID"})
		return
	}

	var target models.User
	if err := utils.DB.First(&target, "id = ?", targetID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	if target.IsStoreOwner {
		c.JSON(http.StatusForbidden, gin.H{"error": "Cannot reset password for internal store owner accounts"})
		return
	}

	if isProtectedRole(target.Role) && target.ID != actor.ID {
		c.JSON(http.StatusForbidden, gin.H{"error": "Cannot reset password for super admin accounts"})
		return
	}

	tempPassword, err := generateTemporaryPassword()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate temporary password"})
		return
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(tempPassword), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to hash password"})
		return
	}

	if err := utils.DB.Model(&target).Updates(map[string]interface{}{
		"password":             string(hashedPassword),
		"must_change_password": true,
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reset password"})
		return
	}

	CreateAuditLog(
		actor.ID,
		actor.Name,
		"update",
		"user",
		&target.ID,
		target.Email,
		"Super admin generated temporary password for user",
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		nil,
		"success",
		"",
	)

	c.JSON(http.StatusOK, gin.H{
		"message":            "Temporary password generated. Share it securely with the user.",
		"temporary_password": tempPassword,
		"user":               storeUserResponse(target),
	})
}
