package controllers

import (
	"net/http"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// Invoice Settings Controllers
func GetInvoiceSettings(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var settings models.InvoiceSettings
	if err := utils.DB.Where("user_id = ?", userID).First(&settings).Error; err != nil {
		// Return default settings if not found
		defaultSettings := models.InvoiceSettings{
			ID:              uuid.New(),
			UserID:          userID,
			Template:        "stylish",
			PrimaryColor:    "#111827",
			SecondaryColor:  "#374151",
			Theme:           "light",
			ShowLogo:        true,
			ShowSignature:   false,
			ShowBankDetails: true,
			ShowTerms:       true,
			InvoicePrefix:   "INV",
			StartingNumber:  1,
			Customization:   utils.EncodeInvoiceTemplateCustomization(utils.DefaultInvoiceTemplateCustomization()),
		}
		c.JSON(http.StatusOK, defaultSettings)
		return
	}

	normalizeInvoiceSettingsResponse(&settings)
	c.JSON(http.StatusOK, settings)
}

func normalizeInvoiceSettingsResponse(settings *models.InvoiceSettings) {
	if settings.Customization == "" {
		settings.Customization = utils.EncodeInvoiceTemplateCustomization(utils.DefaultInvoiceTemplateCustomization())
	}
}

func UpdateInvoiceSettings(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input models.InvoiceSettings
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var settings models.InvoiceSettings
	if err := utils.DB.Where("user_id = ?", userID).First(&settings).Error; err != nil {
		// Create new settings if not found
		settings = models.InvoiceSettings{
			ID:              uuid.New(),
			UserID:          userID,
			Template:        input.Template,
			PrimaryColor:    input.PrimaryColor,
			SecondaryColor:  input.SecondaryColor,
			Theme:           input.Theme,
			ShowLogo:        input.ShowLogo,
			ShowSignature:   input.ShowSignature,
			ShowBankDetails: input.ShowBankDetails,
			ShowTerms:       input.ShowTerms,
			DefaultTerms:    input.DefaultTerms,
			InvoicePrefix:   input.InvoicePrefix,
			StartingNumber:  input.StartingNumber,
			Customization:   input.Customization,
		}
		if settings.Customization == "" {
			settings.Customization = utils.EncodeInvoiceTemplateCustomization(utils.DefaultInvoiceTemplateCustomization())
		}
		if err := utils.DB.Create(&settings).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create invoice settings"})
			return
		}
	} else {
		// Update existing settings
		if err := utils.DB.Model(&settings).Updates(map[string]interface{}{
			"template":         input.Template,
			"primary_color":    input.PrimaryColor,
			"secondary_color":  input.SecondaryColor,
			"theme":            input.Theme,
			"show_logo":        input.ShowLogo,
			"show_signature":   input.ShowSignature,
			"show_bank_details": input.ShowBankDetails,
			"show_terms":       input.ShowTerms,
			"default_terms":    input.DefaultTerms,
			"invoice_prefix":   input.InvoicePrefix,
			"starting_number":  input.StartingNumber,
			"customization":    input.Customization,
		}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update invoice settings"})
			return
		}
	}

	c.JSON(http.StatusOK, settings)
}

// Print Settings Controllers
func GetPrintSettings(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var settings models.PrintSettings
	if err := utils.DB.Where("user_id = ?", userID).First(&settings).Error; err != nil {
		// Return default settings if not found
		defaultSettings := defaultPrintSettings(userID)
		c.JSON(http.StatusOK, defaultSettings)
		return
	}

	normalizePrintSettings(&settings)
	c.JSON(http.StatusOK, settings)
}

func defaultPrintSettings(userID uuid.UUID) models.PrintSettings {
	return models.PrintSettings{
		ID:                  uuid.New(),
		UserID:              userID,
		InvoicePrintMode:    "a4",
		PaperSize:           "a4",
		Orientation:         "portrait",
		MarginTop:           0.5,
		MarginBottom:        0.5,
		MarginLeft:          0.5,
		MarginRight:         0.5,
		FontSize:            12,
		PrintHeader:         true,
		PrintFooter:         true,
		ThermalPrintSize:    "2inch",
		BarcodePrintMode:    "a4",
		BarcodeLabelSize:    "2inch",
		ThermalPrinterName:  "",
		DocumentPrinterName: "",
		AutoPrintOnPOS:      true,
	}
}

func normalizePrintSettings(settings *models.PrintSettings) {
	if settings.InvoicePrintMode != "thermal" && settings.InvoicePrintMode != "a4" {
		settings.InvoicePrintMode = "a4"
	}
	settings.ThermalPrintSize = normalizeThermalPrintSize(settings.ThermalPrintSize)
	if settings.BarcodePrintMode != "label" && settings.BarcodePrintMode != "a4" {
		settings.BarcodePrintMode = "a4"
	}
	settings.BarcodeLabelSize = normalizeBarcodeLabelSize(settings.BarcodeLabelSize)
	if settings.PaperSize == "" {
		settings.PaperSize = "a4"
	}
	if settings.Orientation == "" {
		settings.Orientation = "portrait"
	}
	if settings.FontSize <= 0 {
		settings.FontSize = 12
	}
}

func loadPrintSettings(userID uuid.UUID) models.PrintSettings {
	var settings models.PrintSettings
	if err := utils.DB.Where("user_id = ?", userID).First(&settings).Error; err != nil {
		return defaultPrintSettings(userID)
	}
	normalizePrintSettings(&settings)
	return settings
}

func UpdatePrintSettings(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input models.PrintSettings
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	normalizePrintSettings(&input)

	var settings models.PrintSettings
	if err := utils.DB.Where("user_id = ?", userID).First(&settings).Error; err != nil {
		// Create new settings if not found
		settings = models.PrintSettings{
			ID:                  uuid.New(),
			UserID:              userID,
			InvoicePrintMode:    input.InvoicePrintMode,
			PaperSize:           input.PaperSize,
			Orientation:         input.Orientation,
			MarginTop:           input.MarginTop,
			MarginBottom:        input.MarginBottom,
			MarginLeft:          input.MarginLeft,
			MarginRight:         input.MarginRight,
			FontSize:            input.FontSize,
			PrintHeader:         input.PrintHeader,
			PrintFooter:         input.PrintFooter,
			ThermalPrintSize:    input.ThermalPrintSize,
			BarcodePrintMode:    input.BarcodePrintMode,
			BarcodeLabelSize:    input.BarcodeLabelSize,
			ThermalPrinterName:  input.ThermalPrinterName,
			DocumentPrinterName: input.DocumentPrinterName,
			AutoPrintOnPOS:      input.AutoPrintOnPOS,
		}
		if err := utils.DB.Create(&settings).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create print settings"})
			return
		}
	} else {
		// Update existing settings
		if err := utils.DB.Model(&settings).Updates(map[string]interface{}{
			"invoice_print_mode":    input.InvoicePrintMode,
			"paper_size":            input.PaperSize,
			"orientation":           input.Orientation,
			"margin_top":            input.MarginTop,
			"margin_bottom":         input.MarginBottom,
			"margin_left":           input.MarginLeft,
			"margin_right":          input.MarginRight,
			"font_size":             input.FontSize,
			"print_header":          input.PrintHeader,
			"print_footer":          input.PrintFooter,
			"thermal_print_size":    input.ThermalPrintSize,
			"barcode_print_mode":    input.BarcodePrintMode,
			"barcode_label_size":    input.BarcodeLabelSize,
			"thermal_printer_name":  input.ThermalPrinterName,
			"document_printer_name": input.DocumentPrinterName,
			"auto_print_on_pos":     input.AutoPrintOnPOS,
		}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update print settings"})
			return
		}
	}

	normalizePrintSettings(&settings)
	c.JSON(http.StatusOK, settings)
}

func defaultWeighingScaleSettings(userID uuid.UUID) models.WeighingScaleSettings {
	return models.WeighingScaleSettings{
		ID:                     uuid.New(),
		UserID:                 userID,
		Enabled:                false,
		Connection:             "serial",
		Protocol:               "generic",
		BaudRate:               9600,
		DataBits:               8,
		StopBits:               1,
		Parity:                 "none",
		ScaleWeightUnit:        "kg",
		DecimalPlaces:          3,
		MinWeight:              0.001,
		TareWeight:             0,
		StableReadingsRequired: 3,
		RequireStableWeight:    true,
		AutoApplyOnPos:         true,
		AutoApplyOnInvoice:     true,
		CsvImportEnabled:       true,
		CsvHasHeader:           true,
		CsvDelimiter:           ",",
		CsvItemMatchField:      "auto",
		CsvItemCodeColumn:      "",
		CsvNameColumn:          "",
		CsvUnitColumn:          "",
		CsvPriceColumn:         "",
		CsvExportWeightItemsOnly: true,
		BarcodeScanEnabled:     true,
		BarcodePrefix:          "w",
		BarcodePrefixStart:     20,
		BarcodePrefixEnd:       29,
		BarcodePluDigits:       5,
		BarcodePayloadDigits:   5,
		BarcodePayloadType:     "weight_grams",
	}
}

func GetWeighingScaleSettings(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var settings models.WeighingScaleSettings
	if err := utils.DB.Where("user_id = ?", userID).First(&settings).Error; err != nil {
		c.JSON(http.StatusOK, defaultWeighingScaleSettings(userID))
		return
	}

	if strings.TrimSpace(settings.BarcodePrefix) == "" {
		settings.BarcodePrefix = "w"
	}

	c.JSON(http.StatusOK, settings)
}

func UpdateWeighingScaleSettings(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input models.WeighingScaleSettings
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var settings models.WeighingScaleSettings
	if err := utils.DB.Where("user_id = ?", userID).First(&settings).Error; err != nil {
		settings = defaultWeighingScaleSettings(userID)
		settings.Enabled = input.Enabled
		settings.Connection = input.Connection
		settings.Protocol = input.Protocol
		settings.BaudRate = input.BaudRate
		settings.DataBits = input.DataBits
		settings.StopBits = input.StopBits
		settings.Parity = input.Parity
		settings.ScaleWeightUnit = input.ScaleWeightUnit
		settings.DecimalPlaces = input.DecimalPlaces
		settings.MinWeight = input.MinWeight
		settings.TareWeight = input.TareWeight
		settings.StableReadingsRequired = input.StableReadingsRequired
		settings.RequireStableWeight = input.RequireStableWeight
		settings.AutoApplyOnPos = input.AutoApplyOnPos
		settings.AutoApplyOnInvoice = input.AutoApplyOnInvoice
		settings.CsvImportEnabled = input.CsvImportEnabled
		settings.CsvHasHeader = input.CsvHasHeader
		settings.CsvDelimiter = input.CsvDelimiter
		settings.CsvItemMatchField = input.CsvItemMatchField
		settings.CsvItemCodeColumn = input.CsvItemCodeColumn
		settings.CsvNameColumn = input.CsvNameColumn
		settings.CsvUnitColumn = input.CsvUnitColumn
		settings.CsvPriceColumn = input.CsvPriceColumn
		if input.CsvPriceColumn == "" && input.CsvWeightColumn != "" {
			settings.CsvPriceColumn = input.CsvWeightColumn
		}
		settings.CsvExportWeightItemsOnly = input.CsvExportWeightItemsOnly
		settings.BarcodeScanEnabled = input.BarcodeScanEnabled
		settings.BarcodePrefix = normalizeBarcodePrefix(input.BarcodePrefix)
		settings.BarcodePrefixStart = input.BarcodePrefixStart
		settings.BarcodePrefixEnd = input.BarcodePrefixEnd
		settings.BarcodePluDigits = input.BarcodePluDigits
		settings.BarcodePayloadDigits = input.BarcodePayloadDigits
		settings.BarcodePayloadType = input.BarcodePayloadType
		if err := utils.DB.Create(&settings).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create weighing scale settings"})
			return
		}
	} else {
		if err := utils.DB.Model(&settings).Updates(map[string]interface{}{
			"enabled":                    input.Enabled,
			"connection":                 input.Connection,
			"protocol":                   input.Protocol,
			"baud_rate":                  input.BaudRate,
			"data_bits":                  input.DataBits,
			"stop_bits":                  input.StopBits,
			"parity":                     input.Parity,
			"scale_weight_unit":          input.ScaleWeightUnit,
			"decimal_places":             input.DecimalPlaces,
			"min_weight":                 input.MinWeight,
			"tare_weight":                input.TareWeight,
			"stable_readings_required":   input.StableReadingsRequired,
			"require_stable_weight":      input.RequireStableWeight,
			"auto_apply_on_pos":          input.AutoApplyOnPos,
			"auto_apply_on_invoice":      input.AutoApplyOnInvoice,
			"csv_import_enabled":         input.CsvImportEnabled,
			"csv_has_header":             input.CsvHasHeader,
			"csv_delimiter":              input.CsvDelimiter,
			"csv_item_match_field":       input.CsvItemMatchField,
			"csv_item_code_column":       input.CsvItemCodeColumn,
			"csv_name_column":            input.CsvNameColumn,
			"csv_unit_column":            input.CsvUnitColumn,
			"csv_price_column":           input.CsvPriceColumn,
			"csv_export_weight_items_only": input.CsvExportWeightItemsOnly,
			"barcode_scan_enabled":       input.BarcodeScanEnabled,
			"barcode_prefix":             normalizeBarcodePrefix(input.BarcodePrefix),
			"barcode_prefix_start":       input.BarcodePrefixStart,
			"barcode_prefix_end":         input.BarcodePrefixEnd,
			"barcode_plu_digits":         input.BarcodePluDigits,
			"barcode_payload_digits":     input.BarcodePayloadDigits,
			"barcode_payload_type":       input.BarcodePayloadType,
		}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update weighing scale settings"})
			return
		}
		settings.BarcodePrefix = normalizeBarcodePrefix(input.BarcodePrefix)
	}

	c.JSON(http.StatusOK, settings)
}

func normalizeBarcodePrefix(prefix string) string {
	trimmed := strings.TrimSpace(prefix)
	if trimmed == "" {
		return "w"
	}
	return trimmed
}

// Reminders Controllers
func GetReminders(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var reminders []models.Reminder
	if err := utils.DB.Where("user_id = ?", userID).Order("reminder_date ASC").Find(&reminders).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch reminders"})
		return
	}

	c.JSON(http.StatusOK, reminders)
}

func CreateReminder(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input models.Reminder
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	reminder := models.Reminder{
		ID:            uuid.New(),
		UserID:        userID,
		Title:         input.Title,
		Description:   input.Description,
		ReminderDate:  input.ReminderDate,
		ReminderType:  input.ReminderType,
		RelatedID:     input.RelatedID,
		IsCompleted:   false,
		Repeat:        input.Repeat,
		NextReminderDate: input.NextReminderDate,
	}

	if err := utils.DB.Create(&reminder).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create reminder"})
		return
	}

	c.JSON(http.StatusCreated, reminder)
}

func UpdateReminder(c *gin.Context) {
	id := c.Param("id")
	userID := c.MustGet("user_id").(uuid.UUID)

	var input models.Reminder
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var reminder models.Reminder
	if err := utils.DB.Where("id = ? AND user_id = ?", id, userID).First(&reminder).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Reminder not found"})
		return
	}

	if err := utils.DB.Model(&reminder).Updates(map[string]interface{}{
		"title":             input.Title,
		"description":       input.Description,
		"reminder_date":     input.ReminderDate,
		"reminder_type":     input.ReminderType,
		"related_id":        input.RelatedID,
		"is_completed":      input.IsCompleted,
		"repeat":            input.Repeat,
		"next_reminder_date": input.NextReminderDate,
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update reminder"})
		return
	}

	c.JSON(http.StatusOK, reminder)
}

func DeleteReminder(c *gin.Context) {
	id := c.Param("id")
	userID := c.MustGet("user_id").(uuid.UUID)

	if err := utils.DB.Where("id = ? AND user_id = ?", id, userID).Delete(&models.Reminder{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete reminder"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Reminder deleted successfully"})
}

// CA Report Sharing Controllers
func GetCAReportSharing(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var shares []models.CAReportSharing
	if err := utils.DB.Where("user_id = ?", userID).Find(&shares).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch CA report sharing"})
		return
	}

	c.JSON(http.StatusOK, shares)
}

func CreateCAReportSharing(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input models.CAReportSharing
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	share := models.CAReportSharing{
		ID:          uuid.New(),
		UserID:      userID,
		CAEmail:     input.CAEmail,
		CAName:      input.CAName,
		AccessLevel: input.AccessLevel,
		IsActive:    true,
		Notes:       input.Notes,
	}

	now := time.Now()
	share.LastSharedAt = &now

	if err := utils.DB.Create(&share).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create CA report sharing"})
		return
	}

	c.JSON(http.StatusCreated, share)
}

func UpdateCAReportSharing(c *gin.Context) {
	id := c.Param("id")
	userID := c.MustGet("user_id").(uuid.UUID)

	var input models.CAReportSharing
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var share models.CAReportSharing
	if err := utils.DB.Where("id = ? AND user_id = ?", id, userID).First(&share).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "CA report sharing not found"})
		return
	}

	if err := utils.DB.Model(&share).Updates(map[string]interface{}{
		"ca_email":     input.CAEmail,
		"ca_name":      input.CAName,
		"access_level": input.AccessLevel,
		"is_active":    input.IsActive,
		"notes":        input.Notes,
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update CA report sharing"})
		return
	}

	c.JSON(http.StatusOK, share)
}

func DeleteCAReportSharing(c *gin.Context) {
	id := c.Param("id")
	userID := c.MustGet("user_id").(uuid.UUID)

	if err := utils.DB.Where("id = ? AND user_id = ?", id, userID).Delete(&models.CAReportSharing{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete CA report sharing"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "CA report sharing deleted successfully"})
}

// Account Settings - Change Password
func ChangePassword(c *gin.Context) {
	userID := actorUserID(c)

	var input struct {
		CurrentPassword string `json:"current_password" binding:"required"`
		NewPassword     string `json:"new_password" binding:"required,min=6"`
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

	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(input.CurrentPassword)); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Current password is incorrect"})
		return
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(input.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to hash password"})
		return
	}

	if err := utils.DB.Model(&user).Update("password", string(hashedPassword)).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update password"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Password changed successfully"})
}

// Manage Users - Get all users (for business owner)
func GetBusinessUsers(c *gin.Context) {
	actor, err := loadActor(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	if !canManageUsers(actor.Role) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Insufficient permissions to manage users"})
		return
	}

	var users []models.User
	if utils.IsSuperAdminRole(actor.Role) && c.Query("all") == "true" {
		if err := utils.DB.Where("is_store_owner = ?", false).Order("created_at ASC").Find(&users).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch users"})
			return
		}
	} else {
		storeID, ok := resolveManagedStoreID(c, actor)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Select a store to manage users"})
			return
		}
		if err := utils.DB.Where("store_id = ? AND is_store_owner = ?", storeID, false).
			Order("created_at ASC").Find(&users).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch users"})
			return
		}
	}

	storeNames := map[uuid.UUID]string{}
	var stores []models.Store
	utils.DB.Select("id, name").Find(&stores)
	for _, s := range stores {
		storeNames[s.ID] = s.Name
	}

	out := make([]gin.H, 0, len(users))
	for _, u := range users {
		resp := storeUserResponse(u)
		if u.StoreID != nil {
			if name, ok := storeNames[*u.StoreID]; ok {
				resp["store_name"] = name
			}
		}
		out = append(out, resp)
	}

	c.JSON(http.StatusOK, out)
}

// Create Business User (staff/employee)
func CreateBusinessUser(c *gin.Context) {
	actor, err := loadActor(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	if !canManageUsers(actor.Role) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Insufficient permissions to create users"})
		return
	}

	var input struct {
		Name     string  `json:"name"`
		Email    string  `json:"email"`
		Password string  `json:"password"`
		Phone    string  `json:"phone"`
		Role     string  `json:"role"`
		StoreID  *string `json:"store_id"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request data", "fields": gin.H{"_form": "Invalid request data"}})
		return
	}

	form, fieldErrs := utils.ValidateStoreUserForm(utils.StoreUserFormInput{
		Name: input.Name, Email: input.Email, Password: input.Password, Phone: input.Phone, Role: input.Role,
	})
	if len(fieldErrs) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": utils.FirstFieldMessage(fieldErrs), "fields": fieldErrs})
		return
	}

	if !utils.IsSuperAdminRole(actor.Role) && !isStoreAdminAssignableRole(form.Role) {
		c.JSON(http.StatusForbidden, gin.H{
			"error":  "Store admins can only create admin or staff users",
			"fields": gin.H{"role": "Select admin or staff"},
		})
		return
	}

	var storeID uuid.UUID
	if !utils.IsSuperAdminRole(actor.Role) {
		// Store admins always create users in their own store.
		if actor.StoreID == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Your account has no store assignment"})
			return
		}
		storeID = *actor.StoreID
	} else if input.StoreID != nil && strings.TrimSpace(*input.StoreID) != "" {
		parsed, err := uuid.Parse(strings.TrimSpace(*input.StoreID))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid store_id", "fields": gin.H{"store_id": "Invalid store"}})
			return
		}
		if _, err := utils.FindStoreByID(utils.DB, parsed); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Store not found", "fields": gin.H{"store_id": "Store not found"}})
			return
		}
		storeID = parsed
	} else {
		sid, ok := resolveManagedStoreID(c, actor)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Select a store to create users"})
			return
		}
		storeID = sid
	}

	var existingUser models.User
	if err := utils.DB.Where("email = ?", form.Email).First(&existingUser).Error; err == nil {
		c.JSON(http.StatusConflict, gin.H{
			"error":  "Email already registered",
			"fields": gin.H{"email": "Email already registered"},
		})
		return
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(form.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to hash password"})
		return
	}

	sid := storeID
	newUser := models.User{
		ID:       uuid.New(),
		Name:     form.Name,
		Email:    form.Email,
		Password: string(hashedPassword),
		Phone:    form.Phone,
		Role:     form.Role,
		StoreID:  &sid,
		IsActive: true,
	}

	if err := utils.DB.Create(&newUser).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create user"})
		return
	}

	CreateAuditLog(
		actor.ID,
		actor.Name,
		"create",
		"user",
		&newUser.ID,
		newUser.Email,
		"Created user account",
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		nil,
		"success",
		"",
	)

	c.JSON(http.StatusCreated, storeUserResponse(newUser))
}

// Delete Business User
func DeleteBusinessUser(c *gin.Context) {
	id := c.Param("id")
	actor, err := loadActor(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	if !canManageUsers(actor.Role) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Insufficient permissions to delete users"})
		return
	}

	var targetUser models.User
	if err := utils.DB.First(&targetUser, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	if isProtectedRole(targetUser.Role) || targetUser.IsStoreOwner {
		c.JSON(http.StatusForbidden, gin.H{"error": "Cannot delete super admin account"})
		return
	}

	if !actorCanAccessUser(actor, targetUser) {
		c.JSON(http.StatusForbidden, gin.H{"error": "User does not belong to your store"})
		return
	}

	if err := utils.DB.Delete(&targetUser).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete user"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "User deleted successfully"})
}
