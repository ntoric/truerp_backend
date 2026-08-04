package controllers

import (
	"truerp/models"
	"truerp/utils"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// GetDeveloperSettings retrieves developer settings for the authenticated user
func GetDeveloperSettings(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var settings models.DeveloperSettings
	if err := utils.DB.Where("user_id = ?", userID).First(&settings).Error; err != nil {
		// Return empty settings if not found
		c.JSON(http.StatusOK, models.DeveloperSettings{
			ID:              uuid.New(),
			UserID:          userID,
			EmailProvider:   "smtp",
			WhatsAppProvider: "meta",
		})
		return
	}

	// Decrypt sensitive fields before returning
	if settings.SMTPPassword != "" {
		settings.SMTPPassword, _ = utils.Decrypt(settings.SMTPPassword)
	}
	if settings.SendGridAPIKey != "" {
		settings.SendGridAPIKey, _ = utils.Decrypt(settings.SendGridAPIKey)
	}
	if settings.SESAccessKey != "" {
		settings.SESAccessKey, _ = utils.Decrypt(settings.SESAccessKey)
	}
	if settings.SESSecretKey != "" {
		settings.SESSecretKey, _ = utils.Decrypt(settings.SESSecretKey)
	}
	if settings.MailgunAPIKey != "" {
		settings.MailgunAPIKey, _ = utils.Decrypt(settings.MailgunAPIKey)
	}
	if settings.WhatsAppAPIKey != "" {
		settings.WhatsAppAPIKey, _ = utils.Decrypt(settings.WhatsAppAPIKey)
	}
	if settings.TwilioAuthToken != "" {
		settings.TwilioAuthToken, _ = utils.Decrypt(settings.TwilioAuthToken)
	}
	if settings.TwilioSMSAuthToken != "" {
		settings.TwilioSMSAuthToken, _ = utils.Decrypt(settings.TwilioSMSAuthToken)
	}
	if settings.TextLocalAPIKey != "" {
		settings.TextLocalAPIKey, _ = utils.Decrypt(settings.TextLocalAPIKey)
	}
	if settings.AWSSecretKey != "" {
		settings.AWSSecretKey, _ = utils.Decrypt(settings.AWSSecretKey)
	}
	if settings.SendGridSMSAPIKey != "" {
		settings.SendGridSMSAPIKey, _ = utils.Decrypt(settings.SendGridSMSAPIKey)
	}

	c.JSON(http.StatusOK, settings)
}

// UpdateDeveloperSettings updates developer settings for the authenticated user
func UpdateDeveloperSettings(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input models.DeveloperSettings
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var settings models.DeveloperSettings
	if err := utils.DB.Where("user_id = ?", userID).First(&settings).Error; err != nil {
		// Create new settings if not found
		settings = models.DeveloperSettings{
			ID:              uuid.New(),
			UserID:          userID,
			EmailProvider:   input.EmailProvider,
			SMTPHost:        input.SMTPHost,
			SMTPPort:        input.SMTPPort,
			SMTPUsername:    input.SMTPUsername,
			FromEmail:       input.FromEmail,
			FromName:        input.FromName,
			WhatsAppProvider: input.WhatsAppProvider,
			WhatsAppPhoneNumberID: input.WhatsAppPhoneNumberID,
			WhatsAppBusinessAccountID: input.WhatsAppBusinessAccountID,
			TwilioAccountSID: input.TwilioAccountSID,
			TwilioPhoneNumber: input.TwilioPhoneNumber,
			TwilioSMSAccountSID: input.TwilioSMSAccountSID,
			TwilioSMSPhoneNumber: input.TwilioSMSPhoneNumber,
			Msg91SenderID:   input.Msg91SenderID,
			AWSAccessKey:    input.AWSAccessKey,
			AWSRegion:       input.AWSRegion,
		}

		// Encrypt sensitive fields
		if input.SMTPPassword != "" {
			settings.SMTPPassword, _ = utils.Encrypt(input.SMTPPassword)
		}
		if input.SendGridAPIKey != "" {
			settings.SendGridAPIKey, _ = utils.Encrypt(input.SendGridAPIKey)
		}
		if input.SESAccessKey != "" {
			settings.SESAccessKey, _ = utils.Encrypt(input.SESAccessKey)
		}
		if input.SESSecretKey != "" {
			settings.SESSecretKey, _ = utils.Encrypt(input.SESSecretKey)
		}
		if input.MailgunAPIKey != "" {
			settings.MailgunAPIKey, _ = utils.Encrypt(input.MailgunAPIKey)
		}
		if input.WhatsAppAPIKey != "" {
			settings.WhatsAppAPIKey, _ = utils.Encrypt(input.WhatsAppAPIKey)
		}
		if input.TwilioAuthToken != "" {
			settings.TwilioAuthToken, _ = utils.Encrypt(input.TwilioAuthToken)
		}
		if input.TwilioSMSAuthToken != "" {
			settings.TwilioSMSAuthToken, _ = utils.Encrypt(input.TwilioSMSAuthToken)
		}
		if input.TextLocalAPIKey != "" {
			settings.TextLocalAPIKey, _ = utils.Encrypt(input.TextLocalAPIKey)
		}
		if input.AWSSecretKey != "" {
			settings.AWSSecretKey, _ = utils.Encrypt(input.AWSSecretKey)
		}
		if input.SendGridSMSAPIKey != "" {
			settings.SendGridSMSAPIKey, _ = utils.Encrypt(input.SendGridSMSAPIKey)
		}

		if err := utils.DB.Create(&settings).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create developer settings"})
			return
		}
	} else {
		// Update existing settings
		updates := map[string]interface{}{
			"email_provider":                  input.EmailProvider,
			"smtp_host":                       input.SMTPHost,
			"smtp_port":                       input.SMTPPort,
			"smtp_username":                   input.SMTPUsername,
			"from_email":                      input.FromEmail,
			"from_name":                       input.FromName,
			"whatsapp_provider":               input.WhatsAppProvider,
			"whatsapp_phone_number_id":        input.WhatsAppPhoneNumberID,
			"whatsapp_business_account_id":    input.WhatsAppBusinessAccountID,
			"twilio_account_sid":              input.TwilioAccountSID,
			"twilio_phone_number":             input.TwilioPhoneNumber,
			"twilio_sms_account_sid":          input.TwilioSMSAccountSID,
			"twilio_sms_phone_number":         input.TwilioSMSPhoneNumber,
			"msg91_sender_id":                 input.Msg91SenderID,
			"aws_access_key":                  input.AWSAccessKey,
			"aws_region":                     input.AWSRegion,
		}

		// Encrypt and update sensitive fields only if provided
		if input.SMTPPassword != "" {
			encrypted, _ := utils.Encrypt(input.SMTPPassword)
			updates["smtp_password"] = encrypted
		}
		if input.SendGridAPIKey != "" {
			encrypted, _ := utils.Encrypt(input.SendGridAPIKey)
			updates["sendgrid_api_key"] = encrypted
		}
		if input.SESAccessKey != "" {
			encrypted, _ := utils.Encrypt(input.SESAccessKey)
			updates["ses_access_key"] = encrypted
		}
		if input.SESSecretKey != "" {
			encrypted, _ := utils.Encrypt(input.SESSecretKey)
			updates["ses_secret_key"] = encrypted
		}
		if input.MailgunAPIKey != "" {
			encrypted, _ := utils.Encrypt(input.MailgunAPIKey)
			updates["mailgun_api_key"] = encrypted
		}
		if input.WhatsAppAPIKey != "" {
			encrypted, _ := utils.Encrypt(input.WhatsAppAPIKey)
			updates["whatsapp_api_key"] = encrypted
		}
		if input.TwilioAuthToken != "" {
			encrypted, _ := utils.Encrypt(input.TwilioAuthToken)
			updates["twilio_auth_token"] = encrypted
		}
		if input.TwilioSMSAuthToken != "" {
			encrypted, _ := utils.Encrypt(input.TwilioSMSAuthToken)
			updates["twilio_sms_auth_token"] = encrypted
		}
		if input.TextLocalAPIKey != "" {
			encrypted, _ := utils.Encrypt(input.TextLocalAPIKey)
			updates["textlocal_api_key"] = encrypted
		}
		if input.AWSSecretKey != "" {
			encrypted, _ := utils.Encrypt(input.AWSSecretKey)
			updates["aws_secret_key"] = encrypted
		}
		if input.SendGridSMSAPIKey != "" {
			encrypted, _ := utils.Encrypt(input.SendGridSMSAPIKey)
			updates["sendgrid_sms_api_key"] = encrypted
		}

		if err := utils.DB.Model(&settings).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update developer settings"})
			return
		}
	}

	// Return decrypted settings
	c.JSON(http.StatusOK, gin.H{"message": "Developer settings updated successfully"})
}

// TestEmailConnection tests the email service configuration
func TestEmailConnection(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var settings models.DeveloperSettings
	if err := utils.DB.Where("user_id = ?", userID).First(&settings).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Developer settings not found"})
		return
	}

	// Decrypt password
	_, err := utils.Decrypt(settings.EncryptedSMTPPassword)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to decrypt password"})
		return
	}

	// Here you would implement actual email connection testing
	// For now, just return success if configuration exists
	c.JSON(http.StatusOK, gin.H{
		"message": "Email configuration test",
		"provider": settings.EmailProvider,
		"host": settings.SMTPHost,
		"username": settings.SMTPUsername,
		"status": "configured",
	})
}

// TestWhatsAppConnection tests the WhatsApp service configuration
func TestWhatsAppConnection(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var settings models.DeveloperSettings
	if err := utils.DB.Where("user_id = ?", userID).First(&settings).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Developer settings not found"})
		return
	}

	// Here you would implement actual WhatsApp connection testing
	c.JSON(http.StatusOK, gin.H{
		"message": "WhatsApp configuration test",
		"provider": settings.WhatsAppProvider,
		"status": "configured",
	})
}

// TestSMSConnection tests the SMS service configuration
func TestSMSConnection(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var settings models.DeveloperSettings
	if err := utils.DB.Where("user_id = ?", userID).First(&settings).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Developer settings not found"})
		return
	}

	// Here you would implement actual SMS connection testing
	c.JSON(http.StatusOK, gin.H{
		"message": "SMS configuration test",
		"provider": settings.SMSProvider,
		"status": "configured",
	})
}
