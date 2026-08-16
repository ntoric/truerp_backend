package controllers

import (
	"encoding/json"
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

var allowedColorThemes = map[string]bool{
	"blue":    true,
	"sky":     true,
	"teal":    true,
	"emerald": true,
	"violet":  true,
	"purple":  true,
	"rose":    true,
	"orange":  true,
	"amber":   true,
	"slate":   true,
	"custom":  true,
}

func normalizeCustomHex(hex string) string {
	value := strings.TrimSpace(hex)
	if len(value) == 4 && value[0] == '#' {
		return strings.ToLower("#" + string(value[1]) + string(value[1]) + string(value[2]) + string(value[2]) + string(value[3]) + string(value[3]))
	}
	if len(value) == 7 && value[0] == '#' {
		for _, ch := range value[1:] {
			if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')) {
				return "#2563eb"
			}
		}
		return strings.ToLower(value)
	}
	return "#2563eb"
}

func GetAppearanceSettings(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var settings models.AppearanceSettings
	if err := utils.DB.Where("user_id = ?", userID).First(&settings).Error; err != nil {
		c.JSON(http.StatusOK, gin.H{"color_theme": "", "custom_hex": ""})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"color_theme": settings.ColorTheme,
		"custom_hex":  settings.CustomHex,
	})
}

func UpdateAppearanceSettings(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		ColorTheme string `json:"color_theme"`
		CustomHex  string `json:"custom_hex"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	theme := strings.TrimSpace(strings.ToLower(input.ColorTheme))
	if !allowedColorThemes[theme] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid colour theme"})
		return
	}

	customHex := normalizeCustomHex(input.CustomHex)

	var settings models.AppearanceSettings
	if err := utils.DB.Where("user_id = ?", userID).First(&settings).Error; err != nil {
		settings = models.AppearanceSettings{
			ID:         uuid.New(),
			UserID:     userID,
			ColorTheme: theme,
			CustomHex:  customHex,
		}
		if err := utils.DB.Create(&settings).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save appearance settings"})
			return
		}
	} else if err := utils.DB.Model(&settings).Updates(map[string]interface{}{
		"color_theme": theme,
		"custom_hex":  customHex,
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save appearance settings"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"color_theme": theme,
		"custom_hex":  customHex,
	})
}

// printSettingsPayload is PrintSettings plus A4 barcode label layout (stored on Business).
type printSettingsPayload struct {
	models.PrintSettings
	LabelPaperSize    string  `json:"label_paper_size"`
	LabelSheetPreset  string  `json:"label_sheet_preset"`
	LabelWidthMM      float64 `json:"label_width_mm"`
	LabelHeightMM     float64 `json:"label_height_mm"`
	LabelColumns      int     `json:"label_columns"`
	LabelRows         int     `json:"label_rows"`
	LabelMarginMM     float64 `json:"label_margin_mm"`
	LabelMarginTopMM  float64 `json:"label_margin_top_mm"`
	LabelMarginLeftMM float64 `json:"label_margin_left_mm"`
	LabelGapHMM       float64 `json:"label_gap_h_mm"`
	LabelGapVMM       float64 `json:"label_gap_v_mm"`
}

func defaultLabelLayout() (paperSize string, widthMM, heightMM float64, columns, rows int, marginMM float64) {
	return "A4", 48.5, 25.4, 4, 11, 5
}

func loadLabelLayout(userID uuid.UUID) (paperSize string, widthMM, heightMM float64, columns, rows int, marginMM float64) {
	paperSize, widthMM, heightMM, columns, rows, marginMM = defaultLabelLayout()
	var business models.Business
	if err := utils.DB.Where("user_id = ?", userID).First(&business).Error; err != nil {
		return
	}
	if business.LabelPaperSize != "" {
		paperSize = business.LabelPaperSize
	}
	if business.LabelWidthMM > 0 {
		widthMM = business.LabelWidthMM
	}
	if business.LabelHeightMM > 0 {
		heightMM = business.LabelHeightMM
	}
	if business.LabelColumns > 0 {
		columns = business.LabelColumns
	}
	if business.LabelRows > 0 {
		rows = business.LabelRows
	}
	if business.LabelMarginMM >= 0 {
		marginMM = business.LabelMarginMM
	}
	return
}

func loadLabelLayoutPayload(userID uuid.UUID) printSettingsPayload {
	var business models.Business
	defPaper, defW, defH, defCols, defRows, defMargin := defaultLabelLayout()
	p := printSettingsPayload{
		LabelPaperSize:    defPaper,
		LabelSheetPreset:  "48.5x25.4",
		LabelWidthMM:      defW,
		LabelHeightMM:     defH,
		LabelColumns:      defCols,
		LabelRows:         defRows,
		LabelMarginMM:     defMargin,
		LabelMarginTopMM:  8.8,
		LabelMarginLeftMM: defMargin,
		LabelGapHMM:       2,
		LabelGapVMM:       0,
	}
	if err := utils.DB.Where("user_id = ?", userID).First(&business).Error; err != nil {
		return p
	}
	if business.LabelPaperSize != "" {
		p.LabelPaperSize = business.LabelPaperSize
	}
	if business.LabelSheetPreset != "" {
		p.LabelSheetPreset = business.LabelSheetPreset
	}
	if business.LabelWidthMM > 0 {
		p.LabelWidthMM = business.LabelWidthMM
	}
	if business.LabelHeightMM > 0 {
		p.LabelHeightMM = business.LabelHeightMM
	}
	if business.LabelColumns > 0 {
		p.LabelColumns = business.LabelColumns
	}
	if business.LabelRows > 0 {
		p.LabelRows = business.LabelRows
	}
	if business.LabelMarginMM >= 0 {
		p.LabelMarginMM = business.LabelMarginMM
	}
	if business.LabelMarginTopMM >= 0 {
		p.LabelMarginTopMM = business.LabelMarginTopMM
	}
	if business.LabelMarginLeftMM >= 0 {
		p.LabelMarginLeftMM = business.LabelMarginLeftMM
	}
	if business.LabelGapHMM >= 0 {
		p.LabelGapHMM = business.LabelGapHMM
	}
	if business.LabelGapVMM >= 0 {
		p.LabelGapVMM = business.LabelGapVMM
	}
	return p
}

func labelLayoutMatchesPreset(p printSettingsPayload, preset A4LabelSheetLayout) bool {
	const eps = 0.05
	close := func(a, b float64) bool {
		diff := a - b
		if diff < 0 {
			diff = -diff
		}
		return diff < eps
	}
	paper := strings.TrimSpace(p.LabelPaperSize)
	if paper == "" {
		paper = "A4"
	}
	return strings.EqualFold(paper, preset.PaperSize) &&
		close(p.LabelWidthMM, preset.LabelWidthMM) &&
		close(p.LabelHeightMM, preset.LabelHeightMM) &&
		p.LabelColumns == preset.Columns &&
		p.LabelRows == preset.Rows &&
		close(p.LabelMarginTopMM, preset.MarginTopMM) &&
		close(p.LabelMarginLeftMM, preset.MarginLeftMM) &&
		close(p.LabelGapHMM, preset.GapHMM) &&
		close(p.LabelGapVMM, preset.GapVMM)
}

func normalizeLabelLayout(p *printSettingsPayload) {
	// Named presets are applied in the UI when selected. On save, persist the submitted
	// layout fields as-is; if they no longer match the preset, treat as custom.
	if preset, ok := a4LabelSheetPresetByKey(p.LabelSheetPreset); ok {
		if !labelLayoutMatchesPreset(*p, preset) {
			p.LabelSheetPreset = "custom"
		}
	}
	defPaper, defW, defH, defCols, defRows, defMargin := defaultLabelLayout()
	if p.LabelPaperSize == "" {
		p.LabelPaperSize = defPaper
	}
	if p.LabelSheetPreset == "" {
		p.LabelSheetPreset = "custom"
	}
	if p.LabelWidthMM < 10 || p.LabelWidthMM > 200 {
		p.LabelWidthMM = defW
	}
	if p.LabelHeightMM < 10 || p.LabelHeightMM > 200 {
		p.LabelHeightMM = defH
	}
	if p.LabelColumns < 1 || p.LabelColumns > 6 {
		p.LabelColumns = defCols
	}
	if p.LabelRows < 1 || p.LabelRows > 20 {
		p.LabelRows = defRows
	}
	if p.LabelMarginMM < 0 || p.LabelMarginMM > 50 {
		p.LabelMarginMM = defMargin
	}
	if p.LabelMarginTopMM < 0 || p.LabelMarginTopMM > 50 {
		p.LabelMarginTopMM = p.LabelMarginMM
	}
	if p.LabelMarginLeftMM < 0 || p.LabelMarginLeftMM > 50 {
		p.LabelMarginLeftMM = p.LabelMarginMM
	}
	if p.LabelGapHMM < 0 || p.LabelGapHMM > 20 {
		p.LabelGapHMM = 2
	}
	if p.LabelGapVMM < 0 || p.LabelGapVMM > 20 {
		p.LabelGapVMM = 0
	}
}

func saveLabelLayout(userID uuid.UUID, p printSettingsPayload) error {
	business, err := ensureBusinessForUser(userID)
	if err != nil {
		return err
	}
	updates := models.Business{
		LabelPaperSize:    p.LabelPaperSize,
		LabelSheetPreset:  p.LabelSheetPreset,
		LabelWidthMM:      p.LabelWidthMM,
		LabelHeightMM:     p.LabelHeightMM,
		LabelColumns:      p.LabelColumns,
		LabelRows:         p.LabelRows,
		LabelMarginMM:     p.LabelMarginMM,
		LabelMarginTopMM:  p.LabelMarginTopMM,
		LabelMarginLeftMM: p.LabelMarginLeftMM,
		LabelGapHMM:       p.LabelGapHMM,
		LabelGapVMM:       p.LabelGapVMM,
	}
	return utils.DB.Model(&business).Select(
		"LabelPaperSize", "LabelSheetPreset", "LabelWidthMM", "LabelHeightMM",
		"LabelColumns", "LabelRows", "LabelMarginMM", "LabelMarginTopMM",
		"LabelMarginLeftMM", "LabelGapHMM", "LabelGapVMM",
	).Updates(&updates).Error
}

func toPrintSettingsPayload(settings models.PrintSettings, userID uuid.UUID) printSettingsPayload {
	layout := loadLabelLayoutPayload(userID)
	return printSettingsPayload{
		PrintSettings:     settings,
		LabelPaperSize:    layout.LabelPaperSize,
		LabelSheetPreset:  layout.LabelSheetPreset,
		LabelWidthMM:      layout.LabelWidthMM,
		LabelHeightMM:     layout.LabelHeightMM,
		LabelColumns:      layout.LabelColumns,
		LabelRows:         layout.LabelRows,
		LabelMarginMM:     layout.LabelMarginMM,
		LabelMarginTopMM:  layout.LabelMarginTopMM,
		LabelMarginLeftMM: layout.LabelMarginLeftMM,
		LabelGapHMM:       layout.LabelGapHMM,
		LabelGapVMM:       layout.LabelGapVMM,
	}
}

// Print Settings Controllers
func GetPrintSettings(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var settings models.PrintSettings
	if err := utils.DB.Where("user_id = ?", userID).First(&settings).Error; err != nil {
		// Return default settings if not found
		defaultSettings := defaultPrintSettings(userID)
		c.JSON(http.StatusOK, toPrintSettingsPayload(defaultSettings, userID))
		return
	}

	normalizePrintSettings(&settings)
	c.JSON(http.StatusOK, toPrintSettingsPayload(settings, userID))
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

	var input printSettingsPayload
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	normalizePrintSettings(&input.PrintSettings)
	normalizeLabelLayout(&input)

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

	if err := saveLabelLayout(userID, input); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update label layout settings"})
		return
	}

	normalizePrintSettings(&settings)
	c.JSON(http.StatusOK, toPrintSettingsPayload(settings, userID))
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
		CsvItemMatchField:      "item_code",
		CsvItemCodeColumn:      "",
		CsvPluColumn:           "",
		CsvSlugColumn:          "",
		CsvNameColumn:          "",
		CsvUnitColumn:          "",
		CsvPriceColumn:         "",
		CsvExportWeightItemsOnly: true,
		CsvExtraFields:         "[]",
		CsvExportPath:          "",
		CsvExportFilename:      "",
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
		c.JSON(http.StatusOK, weighingScaleSettingsResponse(defaultWeighingScaleSettings(userID)))
		return
	}

	if strings.TrimSpace(settings.BarcodePrefix) == "" {
		settings.BarcodePrefix = "w"
	}
	if strings.TrimSpace(settings.CsvExtraFields) == "" {
		settings.CsvExtraFields = "[]"
	}

	c.JSON(http.StatusOK, weighingScaleSettingsResponse(settings))
}

func UpdateWeighingScaleSettings(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input weighingScaleSettingsInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	extraFieldsJSON := normalizeCsvExtraFieldsJSON(input.CsvExtraFields)
	input.BarcodePluDigits = clampInt(input.BarcodePluDigits, 3, 5)
	input.BarcodePayloadDigits = clampInt(input.BarcodePayloadDigits, 3, 8)

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
		settings.CsvPluColumn = input.CsvPluColumn
		settings.CsvSlugColumn = input.CsvSlugColumn
		settings.CsvNameColumn = input.CsvNameColumn
		settings.CsvUnitColumn = input.CsvUnitColumn
		settings.CsvPriceColumn = input.CsvPriceColumn
		if input.CsvPriceColumn == "" && input.CsvWeightColumn != "" {
			settings.CsvPriceColumn = input.CsvWeightColumn
		}
		settings.CsvExportWeightItemsOnly = input.CsvExportWeightItemsOnly
		settings.CsvExtraFields = extraFieldsJSON
		settings.CsvExportPath = input.CsvExportPath
		settings.CsvExportFilename = input.CsvExportFilename
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
			"enabled":                      input.Enabled,
			"connection":                   input.Connection,
			"protocol":                     input.Protocol,
			"baud_rate":                    input.BaudRate,
			"data_bits":                    input.DataBits,
			"stop_bits":                    input.StopBits,
			"parity":                       input.Parity,
			"scale_weight_unit":            input.ScaleWeightUnit,
			"decimal_places":               input.DecimalPlaces,
			"min_weight":                   input.MinWeight,
			"tare_weight":                  input.TareWeight,
			"stable_readings_required":     input.StableReadingsRequired,
			"require_stable_weight":        input.RequireStableWeight,
			"auto_apply_on_pos":            input.AutoApplyOnPos,
			"auto_apply_on_invoice":        input.AutoApplyOnInvoice,
			"csv_import_enabled":           input.CsvImportEnabled,
			"csv_has_header":               input.CsvHasHeader,
			"csv_delimiter":                input.CsvDelimiter,
			"csv_item_match_field":         input.CsvItemMatchField,
			"csv_item_code_column":         input.CsvItemCodeColumn,
			"csv_plu_column":               input.CsvPluColumn,
			"csv_slug_column":              input.CsvSlugColumn,
			"csv_name_column":              input.CsvNameColumn,
			"csv_unit_column":              input.CsvUnitColumn,
			"csv_price_column":             input.CsvPriceColumn,
			"csv_export_weight_items_only": input.CsvExportWeightItemsOnly,
			"csv_extra_fields":             extraFieldsJSON,
			"csv_export_path":              input.CsvExportPath,
			"csv_export_filename":          input.CsvExportFilename,
			"barcode_scan_enabled":         input.BarcodeScanEnabled,
			"barcode_prefix":               normalizeBarcodePrefix(input.BarcodePrefix),
			"barcode_prefix_start":         input.BarcodePrefixStart,
			"barcode_prefix_end":           input.BarcodePrefixEnd,
			"barcode_plu_digits":           input.BarcodePluDigits,
			"barcode_payload_digits":       input.BarcodePayloadDigits,
			"barcode_payload_type":         input.BarcodePayloadType,
		}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update weighing scale settings"})
			return
		}
		if err := utils.DB.Where("user_id = ?", userID).First(&settings).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load weighing scale settings"})
			return
		}
	}

	c.JSON(http.StatusOK, weighingScaleSettingsResponse(settings))
}

type weighingScaleSettingsInput struct {
	Enabled                  bool            `json:"enabled"`
	Connection               string          `json:"connection"`
	Protocol                 string          `json:"protocol"`
	BaudRate                 int             `json:"baud_rate"`
	DataBits                 int             `json:"data_bits"`
	StopBits                 int             `json:"stop_bits"`
	Parity                   string          `json:"parity"`
	ScaleWeightUnit          string          `json:"scale_weight_unit"`
	DecimalPlaces            int             `json:"decimal_places"`
	MinWeight                float64         `json:"min_weight"`
	TareWeight               float64         `json:"tare_weight"`
	StableReadingsRequired   int             `json:"stable_readings_required"`
	RequireStableWeight      bool            `json:"require_stable_weight"`
	AutoApplyOnPos           bool            `json:"auto_apply_on_pos"`
	AutoApplyOnInvoice       bool            `json:"auto_apply_on_invoice"`
	CsvImportEnabled         bool            `json:"csv_import_enabled"`
	CsvHasHeader             bool            `json:"csv_has_header"`
	CsvDelimiter             string          `json:"csv_delimiter"`
	CsvItemMatchField        string          `json:"csv_item_match_field"`
	CsvItemCodeColumn        string          `json:"csv_item_code_column"`
	CsvPluColumn             string          `json:"csv_plu_column"`
	CsvSlugColumn            string          `json:"csv_slug_column"`
	CsvNameColumn            string          `json:"csv_name_column"`
	CsvUnitColumn            string          `json:"csv_unit_column"`
	CsvPriceColumn           string          `json:"csv_price_column"`
	CsvWeightColumn          string          `json:"csv_weight_column"`
	CsvExportWeightItemsOnly bool            `json:"csv_export_weight_items_only"`
	CsvExtraFields           json.RawMessage `json:"csv_extra_fields"`
	CsvExportPath            string          `json:"csv_export_path"`
	CsvExportFilename        string          `json:"csv_export_filename"`
	BarcodeScanEnabled       bool            `json:"barcode_scan_enabled"`
	BarcodePrefix            string          `json:"barcode_prefix"`
	BarcodePrefixStart       int             `json:"barcode_prefix_start"`
	BarcodePrefixEnd         int             `json:"barcode_prefix_end"`
	BarcodePluDigits         int             `json:"barcode_plu_digits"`
	BarcodePayloadDigits     int             `json:"barcode_payload_digits"`
	BarcodePayloadType       string          `json:"barcode_payload_type"`
}

func normalizeCsvExtraFieldsJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "[]"
	}
	var asList []string
	if err := json.Unmarshal(raw, &asList); err == nil {
		out, err := json.Marshal(asList)
		if err != nil {
			return "[]"
		}
		return string(out)
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		trimmed := strings.TrimSpace(asString)
		if trimmed == "" {
			return "[]"
		}
		if json.Valid([]byte(trimmed)) {
			return trimmed
		}
		parts := strings.Split(trimmed, ",")
		clean := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				clean = append(clean, p)
			}
		}
		out, err := json.Marshal(clean)
		if err != nil {
			return "[]"
		}
		return string(out)
	}
	return "[]"
}

func weighingScaleSettingsResponse(settings models.WeighingScaleSettings) gin.H {
	var extraFields interface{} = []string{}
	raw := strings.TrimSpace(settings.CsvExtraFields)
	if raw == "" {
		raw = "[]"
	}
	var parsed []string
	if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
		extraFields = parsed
	}

	return gin.H{
		"id":                           settings.ID,
		"user_id":                      settings.UserID,
		"enabled":                      settings.Enabled,
		"connection":                   settings.Connection,
		"protocol":                     settings.Protocol,
		"baud_rate":                    settings.BaudRate,
		"data_bits":                    settings.DataBits,
		"stop_bits":                    settings.StopBits,
		"parity":                       settings.Parity,
		"scale_weight_unit":            settings.ScaleWeightUnit,
		"decimal_places":               settings.DecimalPlaces,
		"min_weight":                   settings.MinWeight,
		"tare_weight":                  settings.TareWeight,
		"stable_readings_required":     settings.StableReadingsRequired,
		"require_stable_weight":        settings.RequireStableWeight,
		"auto_apply_on_pos":            settings.AutoApplyOnPos,
		"auto_apply_on_invoice":        settings.AutoApplyOnInvoice,
		"csv_import_enabled":           settings.CsvImportEnabled,
		"csv_has_header":               settings.CsvHasHeader,
		"csv_delimiter":                settings.CsvDelimiter,
		"csv_item_match_field":         settings.CsvItemMatchField,
		"csv_item_code_column":         settings.CsvItemCodeColumn,
		"csv_plu_column":               settings.CsvPluColumn,
		"csv_slug_column":              settings.CsvSlugColumn,
		"csv_name_column":              settings.CsvNameColumn,
		"csv_unit_column":              settings.CsvUnitColumn,
		"csv_price_column":             settings.CsvPriceColumn,
		"csv_export_weight_items_only": settings.CsvExportWeightItemsOnly,
		"csv_extra_fields":             extraFields,
		"csv_export_path":              settings.CsvExportPath,
		"csv_export_filename":          settings.CsvExportFilename,
		"barcode_scan_enabled":         settings.BarcodeScanEnabled,
		"barcode_prefix":               settings.BarcodePrefix,
		"barcode_prefix_start":         settings.BarcodePrefixStart,
		"barcode_prefix_end":           settings.BarcodePrefixEnd,
		"barcode_plu_digits":           settings.BarcodePluDigits,
		"barcode_payload_digits":       settings.BarcodePayloadDigits,
		"barcode_payload_type":         settings.BarcodePayloadType,
		"created_at":                   settings.CreatedAt,
		"updated_at":                   settings.UpdatedAt,
	}
}

func normalizeBarcodePrefix(prefix string) string {
	trimmed := strings.TrimSpace(prefix)
	if trimmed == "" {
		return "w"
	}
	return trimmed
}

func clampInt(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
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
