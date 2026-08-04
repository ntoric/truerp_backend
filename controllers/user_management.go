package controllers

import (
	"truerp/models"
	"truerp/utils"
	"crypto/rand"
	"encoding/base32"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
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

func actorCanAccessUser(actor models.User, target models.User) bool {
	if utils.IsSuperAdminRole(actor.Role) {
		return true
	}
	if actor.Role != "admin" {
		return false
	}
	if actor.StoreID == nil || target.StoreID == nil {
		return false
	}
	return *actor.StoreID == *target.StoreID
}

func resolveManagedStoreID(c *gin.Context, actor models.User) (uuid.UUID, bool) {
	if utils.IsSuperAdminRole(actor.Role) {
		if storeID, ok := currentStoreID(c); ok {
			return storeID, true
		}
		return uuid.Nil, false
	}
	if actor.StoreID != nil {
		return *actor.StoreID, true
	}
	return uuid.Nil, false
}

func userPublicResponse(u models.User) gin.H {
	resp := gin.H{
		"id":                 u.ID,
		"name":               u.Name,
		"email":              u.Email,
		"phone":              u.Phone,
		"role":               u.Role,
		"is_active":          u.IsActive,
		"two_factor_enabled": u.TwoFactorEnabled,
		"created_at":         u.CreatedAt,
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
			utils.DB.Where("store_id = ? AND is_store_owner = ?", storeID, false).Find(&users)
		} else {
			utils.DB.Where("is_store_owner = ?", false).Find(&users)
		}
	} else {
		storeID, ok := resolveManagedStoreID(c, actor)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Store assignment required"})
			return
		}
		utils.DB.Where("store_id = ? AND is_store_owner = ?", storeID, false).Find(&users)
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

	if !actorCanAccessUser(actor, target) {
		c.JSON(http.StatusForbidden, gin.H{"error": "User does not belong to your store"})
		return
	}

	if (isProtectedRole(target.Role) || target.IsStoreOwner) && actor.ID != target.ID {
		c.JSON(http.StatusForbidden, gin.H{"error": "Cannot modify super admin accounts"})
		return
	}

	var input struct {
		Name     string  `json:"name"`
		Phone    string  `json:"phone"`
		Role     string  `json:"role"`
		IsActive *bool   `json:"is_active"`
		Password string  `json:"password"`
		StoreID  *string `json:"store_id"`
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
		updates["store_id"] = storeID
	}

	if len(updates) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No updates provided"})
		return
	}

	if err := utils.DB.Model(&target).Updates(updates).Error; err != nil {
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
		"totp_secret":          secret,
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
