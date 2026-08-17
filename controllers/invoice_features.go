package controllers

import (
	"truerp/models"
	"truerp/services"
	"truerp/utils"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

var allowedInvoiceStatuses = map[string]bool{
	"draft": true, "sent": true, "paid": true, "partial": true, "overdue": true, "cancelled": true,
}

func syncOverdueInvoices(userID uuid.UUID) {
	now := time.Now()
	var invoices []models.Invoice
	utils.DB.Where("user_id = ? AND status IN ? AND due_date IS NOT NULL AND due_date < ? AND total_amount > amount_paid",
		userID, []string{"sent", "partial"}, now).Find(&invoices)
	for _, inv := range invoices {
		prev := inv.Status
		inv.Status = "overdue"
		if err := utils.DB.Model(&inv).Update("status", "overdue").Error; err == nil && prev != "overdue" {
			recordInvoiceStatusHistory(inv.ID, userID, prev, "overdue", "Automatically marked overdue", "system")
		}
	}
}

func normalizeInvoicePaymentStatus(invoice *models.Invoice) {
	if invoice.Status == "cancelled" || invoice.Status == "draft" {
		return
	}
	if invoice.TotalAmount > 0 && invoice.AmountPaid+0.01 >= invoice.TotalAmount {
		invoice.Status = "paid"
		return
	}
	if invoice.AmountPaid > 0 {
		invoice.Status = "partial"
		return
	}
	if invoice.DueDate != nil && invoice.DueDate.Before(time.Now()) && invoice.Status != "draft" {
		invoice.Status = "overdue"
	}
}

func recordInvoiceStatusHistory(invoiceID, userID uuid.UUID, fromStatus, toStatus, note, changedBy string) {
	if fromStatus == toStatus {
		return
	}
	entry := models.InvoiceStatusHistory{
		ID:         uuid.New(),
		InvoiceID:  invoiceID,
		UserID:     userID,
		FromStatus: fromStatus,
		ToStatus:   toStatus,
		Note:       note,
		ChangedBy:  changedBy,
		CreatedAt:  time.Now(),
	}
	utils.DB.Create(&entry)
}

func encodeCustomFieldsMap(fields map[string]interface{}) string {
	if len(fields) == 0 {
		return ""
	}
	b, err := json.Marshal(fields)
	if err != nil {
		return ""
	}
	return string(b)
}

func parseCustomFieldsJSON(raw string) map[string]interface{} {
	out := map[string]interface{}{}
	if raw == "" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

func validateCustomFields(userID uuid.UUID, fields map[string]interface{}) error {
	var defs []models.InvoiceCustomFieldDefinition
	utils.DB.Where("user_id = ?", userID).Order("sort_order ASC, created_at ASC").Find(&defs)
	for _, def := range defs {
		if !def.IsRequired {
			continue
		}
		val, ok := fields[def.FieldKey]
		if !ok || val == nil || fmt.Sprint(val) == "" {
			return fmt.Errorf("required custom field missing: %s", def.Label)
		}
	}
	return nil
}

func loadInvoiceSettings(userID uuid.UUID) models.InvoiceSettings {
	var settings models.InvoiceSettings
	if err := utils.DB.Where("user_id = ?", userID).First(&settings).Error; err != nil {
		return models.InvoiceSettings{
			Template:       "classic",
			PrimaryColor:   "#2563eb",
			SecondaryColor: "#1e40af",
			Theme:          "light",
			ShowLogo:       true,
			ShowTerms:      true,
		}
	}
	return settings
}

// --- Saved invoice templates ---

func GetSavedInvoiceTemplates(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var templates []models.SavedInvoiceTemplate
	if err := utils.DB.Where("user_id = ?", userID).Order("is_default DESC, name ASC").Find(&templates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load templates"})
		return
	}
	c.JSON(http.StatusOK, templates)
}

func CreateSavedInvoiceTemplate(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var input struct {
		Name        string `json:"name" binding:"required"`
		Description string `json:"description"`
		Payload     string `json:"payload" binding:"required"`
		IsDefault   bool   `json:"is_default"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if input.IsDefault {
		utils.DB.Model(&models.SavedInvoiceTemplate{}).Where("user_id = ?", userID).Update("is_default", false)
	}
	tpl := models.SavedInvoiceTemplate{
		ID:          uuid.New(),
		UserID:      userID,
		Name:        input.Name,
		Description: input.Description,
		Payload:     input.Payload,
		IsDefault:   input.IsDefault,
	}
	if err := utils.DB.Create(&tpl).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create template"})
		return
	}
	c.JSON(http.StatusCreated, tpl)
}

func UpdateSavedInvoiceTemplate(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")
	var tpl models.SavedInvoiceTemplate
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&tpl).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Template not found"})
		return
	}
	var input struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Payload     string `json:"payload"`
		IsDefault   *bool  `json:"is_default"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if input.Name != "" {
		tpl.Name = input.Name
	}
	tpl.Description = input.Description
	if input.Payload != "" {
		tpl.Payload = input.Payload
	}
	if input.IsDefault != nil {
		if *input.IsDefault {
			utils.DB.Model(&models.SavedInvoiceTemplate{}).Where("user_id = ?", userID).Update("is_default", false)
		}
		tpl.IsDefault = *input.IsDefault
	}
	if err := utils.DB.Save(&tpl).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update template"})
		return
	}
	c.JSON(http.StatusOK, tpl)
}

func DeleteSavedInvoiceTemplate(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Delete(&models.SavedInvoiceTemplate{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete template"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Template deleted"})
}

// --- Custom field definitions ---

func GetInvoiceCustomFieldDefinitions(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var defs []models.InvoiceCustomFieldDefinition
	utils.DB.Where("user_id = ?", userID).Order("sort_order ASC, created_at ASC").Find(&defs)
	c.JSON(http.StatusOK, defs)
}

func CreateInvoiceCustomFieldDefinition(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var input models.InvoiceCustomFieldDefinition
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if input.Label == "" || input.FieldKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Label and field key are required"})
		return
	}
	input.ID = uuid.New()
	input.UserID = userID
	input.FieldKey = strings.TrimSpace(strings.ToLower(strings.ReplaceAll(input.FieldKey, " ", "_")))
	if input.FieldType == "" {
		input.FieldType = "text"
	}
	if err := utils.DB.Create(&input).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create custom field"})
		return
	}
	c.JSON(http.StatusCreated, input)
}

func UpdateInvoiceCustomFieldDefinition(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")
	var def models.InvoiceCustomFieldDefinition
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&def).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Custom field not found"})
		return
	}
	var input models.InvoiceCustomFieldDefinition
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if input.Label != "" {
		def.Label = input.Label
	}
	if input.FieldType != "" {
		def.FieldType = input.FieldType
	}
	def.IsRequired = input.IsRequired
	def.ShowOnPDF = input.ShowOnPDF
	def.SortOrder = input.SortOrder
	if err := utils.DB.Save(&def).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update custom field"})
		return
	}
	c.JSON(http.StatusOK, def)
}

func DeleteInvoiceCustomFieldDefinition(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Delete(&models.InvoiceCustomFieldDefinition{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete custom field"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Custom field deleted"})
}

// --- Attachments ---

func GetInvoiceAttachments(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")
	var invoice models.Invoice
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&invoice).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Invoice not found"})
		return
	}
	var files []models.MediaFile
	utils.DB.Where("user_id = ? AND entity_type = ? AND entity_id = ?", userID, "invoice_attachment", invoice.ID).
		Order("created_at DESC").Find(&files)
	c.JSON(http.StatusOK, files)
}

func UploadInvoiceAttachment(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")
	var invoice models.Invoice
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&invoice).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Invoice not found"})
		return
	}

	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No file uploaded"})
		return
	}
	if !isValidAttachmentFile(file) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid file type. Allowed: PDF, JPG, PNG, GIF, WebP, DOC, DOCX, XLS, XLSX"})
		return
	}

	storageService := services.GetDefaultStorageService()
	filePath := "invoice-attachments/" + services.GenerateUniquePath(file.Filename)
	publicURL, err := storageService.UploadFile(file, filePath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to upload file"})
		return
	}

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
		EntityType:   "invoice_attachment",
		EntityID:     invoice.ID,
	}
	if err := utils.DB.Create(&mediaFile).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save attachment record"})
		return
	}
	c.JSON(http.StatusCreated, mediaFile)
}

func DeleteInvoiceAttachment(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	invoiceID := c.Param("id")
	attachmentID := c.Param("attachmentId")
	var file models.MediaFile
	if err := utils.DB.Where("user_id = ? AND entity_type = ? AND entity_id = ? AND id = ?",
		userID, "invoice_attachment", invoiceID, attachmentID).First(&file).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Attachment not found"})
		return
	}
	if err := utils.DB.Delete(&file).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete attachment"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Attachment deleted"})
}

func isValidAttachmentFile(file *multipart.FileHeader) bool {
	ext := strings.ToLower(file.Filename[strings.LastIndex(file.Filename, "."):])
	allowed := map[string]bool{
		".pdf": true, ".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true,
		".doc": true, ".docx": true, ".xls": true, ".xlsx": true,
	}
	return allowed[ext]
}

// --- Status tracking ---

func GetInvoiceStatusHistory(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")
	var invoice models.Invoice
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&invoice).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Invoice not found"})
		return
	}
	var history []models.InvoiceStatusHistory
	utils.DB.Where("invoice_id = ?", invoice.ID).Order("created_at DESC").Find(&history)
	c.JSON(http.StatusOK, history)
}

func UpdateInvoiceStatus(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	userName := ""
	if name, exists := c.Get("user_name"); exists {
		userName = name.(string)
	}
	id := c.Param("id")

	var invoice models.Invoice
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&invoice).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Invoice not found"})
		return
	}

	var input struct {
		Status string  `json:"status" binding:"required"`
		Note   string  `json:"note"`
		AmountPaid *float64 `json:"amount_paid"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !allowedInvoiceStatuses[input.Status] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid status"})
		return
	}

	previousInvoice := invoiceCashSnapshot(invoice)
	prevStatus := invoice.Status
	invoice.Status = input.Status
	if input.AmountPaid != nil {
		invoice.AmountPaid = *input.AmountPaid
		if invoice.AmountPaid > invoice.TotalAmount {
			invoice.AmountPaid = invoice.TotalAmount
		}
	}
	if input.Status == "paid" {
		invoice.AmountPaid = invoice.TotalAmount
	}
	normalizeInvoicePaymentStatus(&invoice)

	if err := utils.DB.Save(&invoice).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update status"})
		return
	}

	if err := resyncLinkedInvoicePayments(utils.DB, userID, &previousInvoice, &invoice); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Status updated but failed to update cash/bank"})
		return
	}

	changedBy := userName
	if changedBy == "" {
		changedBy = "user"
	}
	recordInvoiceStatusHistory(invoice.ID, userID, prevStatus, invoice.Status, input.Note, changedBy)

	attachInvoicePaymentSplits(utils.DB, &invoice)
	c.JSON(http.StatusOK, invoice)
}
