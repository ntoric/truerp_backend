package controllers

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type RegisterInput struct {
	Name     string `json:"name" binding:"required"`
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required,min=6"`
	Phone    string `json:"phone"`
}

type LoginInput struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required"`
	TotpCode string `json:"totp_code"`
}

type ForgotPasswordInput struct {
	Email string `json:"email" binding:"required,email"`
}

type ResetPasswordInput struct {
	Token       string `json:"token" binding:"required"`
	NewPassword string `json:"new_password" binding:"required,min=6"`
}

const passwordResetTokenTTL = time.Hour

func authValidationError(c *gin.Context, fields map[string]string) {
	message := "Please fix the highlighted fields"
	for _, msg := range fields {
		message = msg
		break
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": message, "fields": fields})
}

func hashPasswordResetToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func generatePasswordResetToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func ForgotPassword(c *gin.Context) {
	var input ForgotPasswordInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	fields := map[string]string{}
	email, emailErr := utils.ValidateAuthEmail(input.Email)
	if emailErr != "" {
		fields["email"] = emailErr
	}
	if len(fields) > 0 {
		authValidationError(c, fields)
		return
	}

	var user models.User
	err := utils.DB.Where("email = ?", email).First(&user).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "No account found with this email address"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
		return
	}

	if !user.IsActive {
		c.JSON(http.StatusForbidden, gin.H{"error": "This account is deactivated. Contact your administrator."})
		return
	}

	token, genErr := generatePasswordResetToken()
	if genErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate reset token"})
		return
	}

	expiresAt := time.Now().Add(passwordResetTokenTTL)
	tokenHash := hashPasswordResetToken(token)
	if updateErr := utils.DB.Model(&user).Updates(map[string]interface{}{
		"password_reset_token_hash":  tokenHash,
		"password_reset_expires_at": expiresAt,
	}).Error; updateErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save reset token"})
		return
	}

	resetURL := utils.FrontendURL() + "/reset-password?token=" + token
	if utils.EmailConfigured() {
		if sendErr := utils.SendPasswordResetEmail(user.Email, resetURL); sendErr != nil {
			utils.LogPasswordResetLink(user.Email, resetURL)
		}
	} else {
		utils.LogPasswordResetLink(user.Email, resetURL)
	}

	CreateAuditLog(
		user.ID,
		user.Name,
		"forgot_password",
		"user",
		&user.ID,
		user.Email,
		"Password reset requested",
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		nil,
		"success",
		"",
	)

	c.JSON(http.StatusOK, gin.H{
		"message": "Password reset link has been sent to your email.",
	})
}

func ValidateResetToken(c *gin.Context) {
	token := c.Query("token")
	if token == "" {
		c.JSON(http.StatusBadRequest, gin.H{"valid": false, "error": "Token is required"})
		return
	}

	tokenHash := hashPasswordResetToken(token)
	var user models.User
	err := utils.DB.Where("password_reset_token_hash = ? AND password_reset_expires_at > ?", tokenHash, time.Now()).
		First(&user).Error
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"valid": false})
		return
	}

	c.JSON(http.StatusOK, gin.H{"valid": true})
}

func ResetPassword(c *gin.Context) {
	var input ResetPasswordInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	tokenHash := hashPasswordResetToken(input.Token)
	var user models.User
	err := utils.DB.Where("password_reset_token_hash = ? AND password_reset_expires_at > ?", tokenHash, time.Now()).
		First(&user).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid or expired reset link"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
		return
	}

	if !user.IsActive {
		c.JSON(http.StatusForbidden, gin.H{"error": "Account is deactivated"})
		return
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(input.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to hash password"})
		return
	}

	if err := utils.DB.Model(&user).Updates(map[string]interface{}{
		"password":                    string(hashedPassword),
		"password_reset_token_hash":   "",
		"password_reset_expires_at":   nil,
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update password"})
		return
	}

	CreateAuditLog(
		user.ID,
		user.Name,
		"reset_password",
		"user",
		&user.ID,
		user.Email,
		"Password reset via email link",
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		nil,
		"success",
		"",
	)

	c.JSON(http.StatusOK, gin.H{"message": "Password reset successfully. You can now log in with your new password."})
}

func Register(c *gin.Context) {
	var input RegisterInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	fields := map[string]string{}
	name, nameErr := utils.ValidateAuthName(input.Name)
	if nameErr != "" {
		fields["name"] = nameErr
	}
	email, emailErr := utils.ValidateAuthEmail(input.Email)
	if emailErr != "" {
		fields["email"] = emailErr
	}
	if passwordErr := utils.ValidateAuthPassword(input.Password); passwordErr != "" {
		fields["password"] = passwordErr
	}
	phone, phoneErr := utils.ValidateAuthPhone(input.Phone)
	if phoneErr != "" {
		fields["phone"] = phoneErr
	}
	if len(fields) > 0 {
		authValidationError(c, fields)
		return
	}

	var existingUser models.User
	if err := utils.DB.Where("email = ?", email).First(&existingUser).Error; err == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "Email already registered", "fields": gin.H{"email": "Email already registered"}})
		return
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to hash password"})
		return
	}

	user := models.User{
		ID:       uuid.New(),
		Name:     name,
		Email:    email,
		Password: string(hashedPassword),
		Phone:    phone,
		Role:     "owner",
	}

	var store models.Store
	err = utils.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&user).Error; err != nil {
			return err
		}
		if err := EnsureDefaultChartOfAccounts(tx, user.ID); err != nil {
			return err
		}
		business := models.Business{
			ID:     uuid.New(),
			UserID: user.ID,
			Name:   name + "'s Business",
		}
		if err := tx.Create(&business).Error; err != nil {
			return err
		}
		utils.EnsureDefaultRoles(tx, user.ID)
		if err := utils.EnsureDefaultCategories(tx, user.ID); err != nil {
			return err
		}

		code := utils.UniqueStoreCode(tx, utils.NormalizeStoreCode("", name+" Store"))
		store = models.Store{
			ID:          uuid.New(),
			Name:        name + "'s Store",
			Code:        code,
			OwnerUserID: user.ID,
			IsActive:    true,
		}
		if err := tx.Create(&store).Error; err != nil {
			return err
		}
		return tx.Model(&user).Updates(map[string]interface{}{
			"store_id": store.ID,
		}).Error
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create user"})
		return
	}
	user.StoreID = &store.ID

	token, err := utils.GenerateToken(user.ID, user.Name, user.Email, user.Role, user.StoreID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate token"})
		return
	}

	// Log registration
	CreateAuditLog(
		user.ID,
		user.Name,
		"register",
		"user",
		&user.ID,
		user.Email,
		"User registered new account",
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		nil,
		"success",
		"",
	)

	c.JSON(http.StatusCreated, gin.H{
		"token": token,
		"user": gin.H{
			"id":                 user.ID,
			"name":               user.Name,
			"email":              user.Email,
			"phone":              user.Phone,
			"role":               user.Role,
			"store_id":           user.StoreID,
			"two_factor_enabled": user.TwoFactorEnabled,
		},
		"store": utils.StorePublicJSON(store),
	})
}

func Login(c *gin.Context) {
	var input LoginInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	fields := map[string]string{}
	email, emailErr := utils.ValidateAuthEmail(input.Email)
	if emailErr != "" {
		fields["email"] = emailErr
	}
	if input.Password == "" {
		fields["password"] = "Password is required"
	}
	if len(fields) > 0 {
		authValidationError(c, fields)
		return
	}

	var user models.User
	if err := utils.DB.Where("email = ?", email).First(&user).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid email or password"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(input.Password)); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid email or password"})
		return
	}

	if !user.IsActive {
		c.JSON(http.StatusForbidden, gin.H{"error": "Account is deactivated"})
		return
	}

	if user.TwoFactorEnabled {
		if input.TotpCode == "" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error":        "Two-factor authentication code required",
				"requires_2fa": true,
			})
			return
		}
		if totpErr := utils.ValidateAuthTotpCode(input.TotpCode); totpErr != "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":  totpErr,
				"fields": gin.H{"totp_code": totpErr},
			})
			return
		}
		if !totp.Validate(input.TotpCode, user.TotpSecret) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid two-factor code"})
			return
		}
	}

	if !utils.IsSuperAdminRole(user.Role) && user.StoreID == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "No store assigned. Contact your administrator."})
		return
	}

	token, err := utils.GenerateToken(user.ID, user.Name, user.Email, user.Role, user.StoreID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate token"})
		return
	}

	// Log login
	CreateAuditLog(
		user.ID,
		user.Name,
		"login",
		"user",
		&user.ID,
		user.Email,
		"User logged in",
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		nil,
		"success",
		"",
	)

	resp := gin.H{
		"token": token,
		"user": gin.H{
			"id":                 user.ID,
			"name":               user.Name,
			"email":              user.Email,
			"phone":              user.Phone,
			"role":               user.Role,
			"store_id":           user.StoreID,
			"two_factor_enabled": user.TwoFactorEnabled,
		},
	}

	if user.StoreID != nil {
		if store, storeErr := utils.FindStoreByID(utils.DB, *user.StoreID); storeErr == nil {
			resp["store"] = utils.StorePublicJSON(store)
		}
	} else if utils.IsSuperAdminRole(user.Role) {
		var stores []models.Store
		utils.DB.Where("is_active = ?", true).Order("name ASC").Find(&stores)
		list := make([]gin.H, 0, len(stores))
		for _, s := range stores {
			list = append(list, gin.H(utils.StorePublicJSON(s)))
		}
		resp["stores"] = list
		if len(stores) > 0 {
			resp["store"] = utils.StorePublicJSON(stores[0])
		}
	}

	c.JSON(http.StatusOK, resp)
}

func GetProfile(c *gin.Context) {
	userID := actorUserID(c)

	var user models.User
	if err := utils.DB.Preload("Business").First(&user, "id = ?", userID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	resp := gin.H{
		"id":                 user.ID,
		"name":               user.Name,
		"email":              user.Email,
		"phone":              user.Phone,
		"role":               user.Role,
		"store_id":           user.StoreID,
		"two_factor_enabled": user.TwoFactorEnabled,
		"business":           user.Business,
	}

	if storeID, ok := currentStoreID(c); ok {
		if store, err := utils.FindStoreByID(utils.DB, storeID); err == nil {
			resp["active_store"] = utils.StorePublicJSON(store)
			resp["store_id"] = store.ID
		}
	} else if user.StoreID != nil {
		if store, err := utils.FindStoreByID(utils.DB, *user.StoreID); err == nil {
			resp["active_store"] = utils.StorePublicJSON(store)
		}
	}

	if utils.IsSuperAdminRole(user.Role) {
		var stores []models.Store
		utils.DB.Where("is_active = ?", true).Order("name ASC").Find(&stores)
		list := make([]gin.H, 0, len(stores))
		for _, s := range stores {
			list = append(list, gin.H(utils.StorePublicJSON(s)))
		}
		resp["stores"] = list
		resp["can_switch_stores"] = true
	} else {
		resp["can_switch_stores"] = false
	}

	c.JSON(http.StatusOK, resp)
}

func UpdateProfile(c *gin.Context) {
	userID := actorUserID(c)

	var input struct {
		Name  string `json:"name"`
		Phone string `json:"phone"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := utils.DB.Model(&models.User{}).Where("id = ?", userID).Updates(map[string]interface{}{
		"name":  input.Name,
		"phone": input.Phone,
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update profile"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Profile updated successfully"})
}
