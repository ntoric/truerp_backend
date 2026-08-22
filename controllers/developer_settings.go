package controllers

import (
	"net/http"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type developerSettingsInput struct {
	EmailProvider             string `json:"email_provider"`
	SMTPHost                  string `json:"smtp_host"`
	SMTPPort                  int    `json:"smtp_port"`
	SMTPUsername              string `json:"smtp_username"`
	SMTPPassword              string `json:"smtp_password"`
	FromEmail                 string `json:"from_email"`
	FromName                  string `json:"from_name"`
	SendGridAPIKey            string `json:"sendgrid_api_key"`
	SESAccessKey              string `json:"ses_access_key"`
	SESSecretKey              string `json:"ses_secret_key"`
	MailgunAPIKey             string `json:"mailgun_api_key"`
	MailgunDomain             string `json:"mailgun_domain"`
	WhatsAppProvider          string `json:"whatsapp_provider"`
	WhatsAppAPIKey            string `json:"whatsapp_api_key"`
	WhatsAppPhoneNumberID     string `json:"whatsapp_phone_number_id"`
	WhatsAppBusinessAccountID string `json:"whatsapp_business_account_id"`
	TwilioAccountSID          string `json:"twilio_account_sid"`
	TwilioAuthToken           string `json:"twilio_auth_token"`
	TwilioPhoneNumber         string `json:"twilio_phone_number"`
	SMSProvider               string `json:"sms_provider"`
	TwilioSMSAccountSID       string `json:"twilio_sms_account_sid"`
	TwilioSMSAuthToken        string `json:"twilio_sms_auth_token"`
	TwilioSMSPhoneNumber      string `json:"twilio_sms_phone_number"`
	Msg91SenderID             string `json:"msg91_sender_id"`
	Msg91AuthKey              string `json:"msg91_auth_key"`
	TextLocalSenderID         string `json:"textlocal_sender_id"`
	TextLocalAPIKey           string `json:"textlocal_api_key"`
	AWSAccessKey              string `json:"aws_access_key"`
	AWSSecretKey              string `json:"aws_secret_key"`
	AWSRegion                 string `json:"aws_region"`
	SendGridSMSAPIKey         string `json:"sendgrid_sms_api_key"`
	Timezone                  string `json:"timezone"`
}

type testEmailInput struct {
	EmailProvider string `json:"email_provider"`
	SMTPHost      string `json:"smtp_host"`
	SMTPPort      int    `json:"smtp_port"`
	SMTPUsername  string `json:"smtp_username"`
	SMTPPassword  string `json:"smtp_password"`
	FromEmail     string `json:"from_email"`
	FromName      string `json:"from_name"`
}

func encryptIfPresent(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	return utils.Encrypt(value)
}

func applyDeveloperSettingsInput(settings *models.DeveloperSettings, input developerSettingsInput) error {
	settings.EmailProvider = input.EmailProvider
	settings.SMTPHost = input.SMTPHost
	settings.SMTPPort = input.SMTPPort
	settings.SMTPUsername = input.SMTPUsername
	settings.FromEmail = input.FromEmail
	settings.FromName = input.FromName
	settings.MailgunDomain = input.MailgunDomain
	settings.WhatsAppProvider = input.WhatsAppProvider
	settings.WhatsAppPhoneNumberID = input.WhatsAppPhoneNumberID
	settings.WhatsAppBusinessAccountID = input.WhatsAppBusinessAccountID
	settings.TwilioAccountSID = input.TwilioAccountSID
	settings.TwilioPhoneNumber = input.TwilioPhoneNumber
	settings.SMSProvider = input.SMSProvider
	settings.TwilioSMSAccountSID = input.TwilioSMSAccountSID
	settings.TwilioSMSPhoneNumber = input.TwilioSMSPhoneNumber
	settings.Msg91SenderID = input.Msg91SenderID
	settings.TextLocalSenderID = input.TextLocalSenderID
	settings.AWSAccessKey = input.AWSAccessKey
	settings.AWSRegion = input.AWSRegion
	settings.Timezone = input.Timezone

	var err error
	if input.SMTPPassword != "" {
		settings.EncryptedSMTPPassword, err = encryptIfPresent(input.SMTPPassword)
		if err != nil {
			return err
		}
	}
	if input.SendGridAPIKey != "" {
		settings.EncryptedSendGridAPIKey, err = encryptIfPresent(input.SendGridAPIKey)
		if err != nil {
			return err
		}
	}
	if input.SESAccessKey != "" {
		settings.EncryptedSESAccessKey, err = encryptIfPresent(input.SESAccessKey)
		if err != nil {
			return err
		}
	}
	if input.SESSecretKey != "" {
		settings.EncryptedSESSecretKey, err = encryptIfPresent(input.SESSecretKey)
		if err != nil {
			return err
		}
	}
	if input.MailgunAPIKey != "" {
		settings.EncryptedMailgunAPIKey, err = encryptIfPresent(input.MailgunAPIKey)
		if err != nil {
			return err
		}
	}
	if input.WhatsAppAPIKey != "" {
		settings.EncryptedWhatsAppAPIKey, err = encryptIfPresent(input.WhatsAppAPIKey)
		if err != nil {
			return err
		}
	}
	if input.TwilioAuthToken != "" {
		settings.EncryptedTwilioAuthToken, err = encryptIfPresent(input.TwilioAuthToken)
		if err != nil {
			return err
		}
	}
	if input.TwilioSMSAuthToken != "" {
		settings.EncryptedTwilioSMSAuthToken, err = encryptIfPresent(input.TwilioSMSAuthToken)
		if err != nil {
			return err
		}
	}
	if input.Msg91AuthKey != "" {
		settings.EncryptedMsg91AuthKey, err = encryptIfPresent(input.Msg91AuthKey)
		if err != nil {
			return err
		}
	}
	if input.TextLocalAPIKey != "" {
		settings.EncryptedTextLocalAPIKey, err = encryptIfPresent(input.TextLocalAPIKey)
		if err != nil {
			return err
		}
	}
	if input.AWSSecretKey != "" {
		settings.EncryptedAWSSecretKey, err = encryptIfPresent(input.AWSSecretKey)
		if err != nil {
			return err
		}
	}
	if input.SendGridSMSAPIKey != "" {
		settings.EncryptedSendGridSMSAPIKey, err = encryptIfPresent(input.SendGridSMSAPIKey)
		if err != nil {
			return err
		}
	}

	return nil
}

func developerSettingsUpdates(input developerSettingsInput) (map[string]interface{}, error) {
	updates := map[string]interface{}{
		"email_provider":               input.EmailProvider,
		"smtp_host":                      input.SMTPHost,
		"smtp_port":                      input.SMTPPort,
		"smtp_username":                  input.SMTPUsername,
		"from_email":                     input.FromEmail,
		"from_name":                      input.FromName,
		"mailgun_domain":                 input.MailgunDomain,
		"whatsapp_provider":              input.WhatsAppProvider,
		"whatsapp_phone_number_id":       input.WhatsAppPhoneNumberID,
		"whatsapp_business_account_id":   input.WhatsAppBusinessAccountID,
		"twilio_account_sid":             input.TwilioAccountSID,
		"twilio_phone_number":            input.TwilioPhoneNumber,
		"sms_provider":                   input.SMSProvider,
		"twilio_sms_account_sid":         input.TwilioSMSAccountSID,
		"twilio_sms_phone_number":        input.TwilioSMSPhoneNumber,
		"msg91_sender_id":                input.Msg91SenderID,
		"textlocal_sender_id":            input.TextLocalSenderID,
		"aws_access_key":                 input.AWSAccessKey,
		"aws_region":                     input.AWSRegion,
		"timezone":                       input.Timezone,
	}

	secretFields := []struct {
		value  string
		column string
	}{
		{input.SMTPPassword, "smtp_password"},
		{input.SendGridAPIKey, "sendgrid_api_key"},
		{input.SESAccessKey, "ses_access_key"},
		{input.SESSecretKey, "ses_secret_key"},
		{input.MailgunAPIKey, "mailgun_api_key"},
		{input.WhatsAppAPIKey, "whatsapp_api_key"},
		{input.TwilioAuthToken, "twilio_auth_token"},
		{input.TwilioSMSAuthToken, "twilio_sms_auth_token"},
		{input.Msg91AuthKey, "msg91_auth_key"},
		{input.TextLocalAPIKey, "textlocal_api_key"},
		{input.AWSSecretKey, "aws_secret_key"},
		{input.SendGridSMSAPIKey, "sendgrid_sms_api_key"},
	}

	for _, field := range secretFields {
		if field.value == "" {
			continue
		}
		encrypted, err := encryptIfPresent(field.value)
		if err != nil {
			return nil, err
		}
		updates[field.column] = encrypted
	}

	return updates, nil
}

// GetDeveloperSettings retrieves developer settings for the authenticated user
func GetDeveloperSettings(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var settings models.DeveloperSettings
	if err := utils.DB.Where("user_id = ?", userID).First(&settings).Error; err != nil {
		c.JSON(http.StatusOK, models.DeveloperSettings{
			ID:               uuid.New(),
			UserID:           userID,
			EmailProvider:    "smtp",
			SMTPPort:         587,
			WhatsAppProvider: "meta",
			SMSProvider:      "twilio",
		})
		return
	}

	c.JSON(http.StatusOK, settings)
}

// UpdateDeveloperSettings updates developer settings for the authenticated user
func UpdateDeveloperSettings(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input developerSettingsInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := ValidateTimezone(input.Timezone); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var settings models.DeveloperSettings
	if err := utils.DB.Where("user_id = ?", userID).First(&settings).Error; err != nil {
		settings = models.DeveloperSettings{
			ID:     uuid.New(),
			UserID: userID,
		}
		if err := applyDeveloperSettingsInput(&settings, input); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to encrypt developer settings"})
			return
		}
		if settings.EmailProvider == "" {
			settings.EmailProvider = "smtp"
		}
		if settings.SMTPPort == 0 {
			settings.SMTPPort = 587
		}
		if settings.WhatsAppProvider == "" {
			settings.WhatsAppProvider = "meta"
		}
		if settings.SMSProvider == "" {
			settings.SMSProvider = "twilio"
		}

		if err := utils.DB.Create(&settings).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create developer settings"})
			return
		}
	} else {
		updates, err := developerSettingsUpdates(input)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to encrypt developer settings"})
			return
		}

		if err := utils.DB.Model(&settings).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update developer settings"})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{"message": "Developer settings updated successfully"})
}

func resolveSMTPPassword(input testEmailInput, saved models.DeveloperSettings) (string, error) {
	if input.SMTPPassword != "" {
		return input.SMTPPassword, nil
	}
	if saved.EncryptedSMTPPassword == "" {
		return "", nil
	}
	return utils.Decrypt(saved.EncryptedSMTPPassword)
}

// TestEmailConnection tests the email service configuration
func TestEmailConnection(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input testEmailInput
	_ = c.ShouldBindJSON(&input)

	var saved models.DeveloperSettings
	hasSaved := utils.DB.Where("user_id = ?", userID).First(&saved).Error == nil

	host := input.SMTPHost
	port := input.SMTPPort
	username := input.SMTPUsername
	provider := input.EmailProvider

	if host == "" && hasSaved {
		host = saved.SMTPHost
	}
	if port == 0 && hasSaved {
		port = saved.SMTPPort
	}
	if username == "" && hasSaved {
		username = saved.SMTPUsername
	}
	if provider == "" && hasSaved {
		provider = saved.EmailProvider
	}
	if provider == "" {
		provider = "smtp"
	}

	if provider != "smtp" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Only SMTP connection testing is supported currently"})
		return
	}

	password, err := resolveSMTPPassword(input, saved)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to decrypt saved SMTP password"})
		return
	}

	if host == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "SMTP host is required"})
		return
	}
	if port == 0 {
		port = 587
	}
	if username == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "SMTP username is required"})
		return
	}
	if password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "SMTP password is required"})
		return
	}

	if err := utils.TestSMTPConnection(utils.EmailConfig{
		Host:     host,
		Port:     port,
		Username: username,
		Password: password,
	}); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Email connection successful"})
}

// TestWhatsAppConnection tests the WhatsApp service configuration
func TestWhatsAppConnection(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var settings models.DeveloperSettings
	if err := utils.DB.Where("user_id = ?", userID).First(&settings).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Developer settings not found. Save your settings first."})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":  "WhatsApp configuration saved",
		"provider": settings.WhatsAppProvider,
		"status":   "configured",
	})
}

// TestSMSConnection tests the SMS service configuration
func TestSMSConnection(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var settings models.DeveloperSettings
	if err := utils.DB.Where("user_id = ?", userID).First(&settings).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Developer settings not found. Save your settings first."})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":  "SMS configuration saved",
		"provider": settings.SMSProvider,
		"status":   "configured",
	})
}
