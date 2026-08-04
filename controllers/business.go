package controllers

import (
	"truerp/models"
	"truerp/services"
	"truerp/utils"
	"fmt"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func GetBusiness(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var business models.Business
	if err := utils.DB.Where("user_id = ?", userID).First(&business).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Business not found"})
		return
	}

	// Don't send the actual API key to the frontend for security
	// Send a masked version instead
	if business.GeminiAPIKey != "" {
		business.GeminiAPIKey = "********"
	}

	c.JSON(http.StatusOK, business)
}

func UpdateBusiness(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input models.Business
	if err := c.ShouldBindJSON(&input); err != nil {
		fmt.Println("ERROR: Failed to bind JSON:", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	fmt.Println("DEBUG: Input received:", input.Name, "EnableAIHSNSearch:", input.EnableAIHSNSearch, "EnableAIBillParsing:", input.EnableAIBillParsing, "HasAPIKey:", input.GeminiAPIKey != "")

	var business models.Business
	if err := utils.DB.Where("user_id = ?", userID).First(&business).Error; err != nil {
		fmt.Println("ERROR: Business not found for user:", userID, "Error:", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "Business not found"})
		return
	}
	fmt.Println("DEBUG: Business found:", business.ID)

	updates := map[string]interface{}{
		"name":                 input.Name,
		"gstin":                input.GSTIN,
		"address":              input.Address,
		"city":                 input.City,
		"state":                input.State,
		"pincode":              input.Pincode,
		"phone":                input.Phone,
		"email":                input.Email,
		"logo_url":             input.LogoURL,
		"signature_url":        input.SignatureURL,
		"state_code":           input.StateCode,
		"bank_name":            input.BankName,
		"account_number":       input.AccountNumber,
		"ifsc_code":            input.IFSCCode,
		"upi_id":               input.UPIID,
		"enable_aihsn_search":  input.EnableAIHSNSearch,
		"enable_ai_bill_parsing": input.EnableAIBillParsing,
	}
	fmt.Println("DEBUG: Updates map created")

	// Update Gemini API key only if provided and not masked
	// Skip if it's the masked value from frontend (means user didn't change it)
	if input.GeminiAPIKey != "" && input.GeminiAPIKey != "********" {
		fmt.Println("DEBUG: Updating API key, length:", len(input.GeminiAPIKey))
		updates["gemini_api_key"] = input.GeminiAPIKey
		fmt.Println("DEBUG: API key updated successfully")
	}

	fmt.Println("DEBUG: About to update business with updates:", updates)
	if err := utils.DB.Model(&business).Updates(updates).Error; err != nil {
		fmt.Println("ERROR: Failed to update business:", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update business"})
		return
	}
	fmt.Println("DEBUG: Business updated successfully")

	c.JSON(http.StatusOK, business)
}

func GetBusinessByID(c *gin.Context) {
	id := c.Param("id")
	var business models.Business
	if err := utils.DB.First(&business, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Business not found"})
		return
	}
	c.JSON(http.StatusOK, business)
}

func UploadLogo(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	file, err := c.FormFile("logo")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No file uploaded"})
		return
	}

	// Validate file type
	if !isValidImageFile(file) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid file type. Only JPG, PNG, GIF, and WebP are allowed"})
		return
	}

	// Get storage service
	storageService := services.GetDefaultStorageService()

	// Generate unique file path
	filePath := services.GenerateUniquePath(file.Filename)
	filePath = "logos/" + filePath

	// Upload file using storage service
	publicURL, err := storageService.UploadFile(file, filePath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to upload file"})
		return
	}

	// Create media file record
	mediaFile := models.MediaFile{
		ID:           uuid.New(),
		UserID:       userID,
		FileName:     filePath,
		OriginalName: file.Filename,
		FilePath:     filePath,
		FileSize:     file.Size,
		MimeType:     file.Header.Get("Content-Type"),
		StorageType:  string(services.GetStorageConfig().Type),
		PublicURL:    publicURL,
		EntityType:   "logo",
	}
	if err := utils.DB.Create(&mediaFile).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create media record"})
		return
	}

	// Update business record
	if err := utils.DB.Model(&models.Business{}).Where("user_id = ?", userID).Update("logo_url", publicURL).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update logo"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"logo_url": publicURL})
}

func UploadSignature(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	file, err := c.FormFile("signature")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No file uploaded"})
		return
	}

	// Validate file type
	if !isValidImageFile(file) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid file type. Only JPG, PNG, GIF, and WebP are allowed"})
		return
	}

	// Get storage service
	storageService := services.GetDefaultStorageService()

	// Generate unique file path
	filePath := services.GenerateUniquePath(file.Filename)
	filePath = "signatures/" + filePath

	// Upload file using storage service
	publicURL, err := storageService.UploadFile(file, filePath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to upload file"})
		return
	}

	// Create media file record
	mediaFile := models.MediaFile{
		ID:           uuid.New(),
		UserID:       userID,
		FileName:     filePath,
		OriginalName: file.Filename,
		FilePath:     filePath,
		FileSize:     file.Size,
		MimeType:     file.Header.Get("Content-Type"),
		StorageType:  string(services.GetStorageConfig().Type),
		PublicURL:    publicURL,
		EntityType:   "signature",
	}
	if err := utils.DB.Create(&mediaFile).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create media record"})
		return
	}

	// Update business record
	if err := utils.DB.Model(&models.Business{}).Where("user_id = ?", userID).Update("signature_url", publicURL).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update signature"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"signature_url": publicURL})
}

func isValidImageFile(file *multipart.FileHeader) bool {
	ext := strings.ToLower(file.Filename[strings.LastIndex(file.Filename, "."):])
	allowedExts := map[string]bool{
		".jpg":  true,
		".jpeg": true,
		".png":  true,
		".gif":  true,
		".webp": true,
	}
	return allowedExts[ext]
}
