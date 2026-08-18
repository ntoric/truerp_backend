package controllers

import (
	"encoding/base64"
	"fmt"
	"html"
	"math"
	"net/http"
	"strings"
	"text/template"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type ThermalPrintRequest struct {
	DocumentType string    `json:"document_type" binding:"required"` // invoice, expense
	DocumentID   uuid.UUID `json:"document_id" binding:"required"`
	PrintSize    string    `json:"print_size" binding:"required"`    // 1inch, 1.5inch, 2inch, 3inch
}

// normalizeThermalPrintSize accepts receipt widths: 1 / 1.5 / 2 / 3 inch.
func normalizeThermalPrintSize(size string) string {
	switch strings.TrimSpace(strings.ToLower(size)) {
	case "1inch", "1.5inch", "2inch", "3inch":
		return strings.TrimSpace(strings.ToLower(size))
	default:
		return "2inch"
	}
}

func thermalWidthMM(printSize string) int {
	switch normalizeThermalPrintSize(printSize) {
	case "1inch":
		return 25
	case "1.5inch":
		return 38
	case "3inch":
		return 80
	default:
		return 58
	}
}

func isWideThermal(printSize string) bool {
	return normalizeThermalPrintSize(printSize) == "3inch"
}

// thermalCols returns monospace character columns for each roll width (matches ESC/POS).
func thermalCols(printSize string) int {
	switch normalizeThermalPrintSize(printSize) {
	case "1inch":
		return 16
	case "1.5inch":
		return 24
	case "3inch":
		return 48
	default:
		return 32
	}
}

func thermalDescLen(printSize string) int {
	cols := thermalCols(printSize)
	switch normalizeThermalPrintSize(printSize) {
	case "3inch":
		return 28
	case "1inch":
		return cols
	default:
		return cols
	}
}

func thermalSep(cols int, ch rune) string {
	if cols < 8 {
		cols = 8
	}
	return strings.Repeat(string(ch), cols)
}

func padRightRunes(s string, n int) string {
	r := []rune(s)
	if len(r) >= n {
		return string(r[:n])
	}
	return s + strings.Repeat(" ", n-len(r))
}

func padLeftRunes(s string, n int) string {
	r := []rune(s)
	if len(r) >= n {
		return string(r[:n])
	}
	return strings.Repeat(" ", n-len(r)) + s
}

func formatLabelValue(label, value string, cols int) string {
	label = strings.TrimSpace(label)
	value = strings.TrimSpace(value)
	space := cols - runeLen(label) - runeLen(value)
	if space < 1 {
		maxLabel := cols - runeLen(value) - 1
		if maxLabel < 4 {
			return truncateString(label+" "+value, cols)
		}
		return truncateString(label, maxLabel) + " " + value
	}
	return label + strings.Repeat(" ", space) + value
}

func runeLen(s string) int {
	return len([]rune(s))
}

type ThermalPrintResponse struct {
	Content    string `json:"content"`
	Width      int    `json:"width"` // in mm
	LogoURL    string `json:"logo_url,omitempty"`
	LogoBase64 string `json:"logo_base64,omitempty"` // data URL for print (avoids CORS)
}

type DocumentPrintRequest struct {
	DocumentType string    `json:"document_type" binding:"required"` // invoice, expense
	DocumentID   uuid.UUID `json:"document_id" binding:"required"`
	Mode         string    `json:"mode"` // a4, thermal, or empty to use settings
	PrintSize    string    `json:"print_size"`
}

type DocumentPrintResponse struct {
	Mode        string `json:"mode"` // a4, thermal
	PDFBase64   string `json:"pdf_base64"`
	ContentType string `json:"content_type"`
	Content     string `json:"content,omitempty"` // thermal text (preview)
	Width       int    `json:"width,omitempty"`
	PrinterName string `json:"printer_name,omitempty"`
	Title       string `json:"title"`
	LogoURL     string `json:"logo_url,omitempty"`
	LogoBase64  string `json:"logo_base64,omitempty"` // data URL for print (avoids CORS)
}

// Narrow / stacked item layout (1", 1.5", 2")
const templateNarrow = `{{.Header}}
{{.SepStrong}}
{{.DocumentInfo}}
{{.SepWeak}}
{{.PartyInfo}}
{{.SepWeak}}
{{range .Items}}{{.Description}}
{{.ItemLine}}
{{end}}{{.SepWeak}}
{{.Totals}}
{{.Footer}}
{{.SepStrong}}
{{.Terms}}
`

// Wide column layout (3" / 80mm)
const templateWide = `{{.Header}}
{{.SepStrong}}
{{.DocumentInfo}}
{{.SepWeak}}
{{.PartyInfo}}
{{.SepWeak}}
{{.ItemsHeader}}
{{range .Items}}{{.ItemLine}}
{{end}}{{.SepWeak}}
{{.Totals}}
{{.TaxDetails}}
{{.Footer}}
{{.SepStrong}}
{{.Terms}}
`

func GenerateThermalPrint(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var req ThermalPrintRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var content string
	var width int
	var logoURL string

	switch req.DocumentType {
	case "invoice":
		content, width, logoURL = generateInvoiceThermal(userID, req.DocumentID, req.PrintSize)
	case "expense":
		content, width, logoURL = generateExpenseThermal(userID, req.DocumentID, req.PrintSize)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid document type"})
		return
	}

	c.JSON(http.StatusOK, ThermalPrintResponse{
		Content:    content,
		Width:      width,
		LogoURL:    logoURL,
		LogoBase64: thermalLogoDataURL(logoURL),
	})
}

// GenerateDocumentPrint returns a real PDF page (base64) for A4 or thermal based on settings/mode.
func GenerateDocumentPrint(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var req DocumentPrintRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	settings := loadPrintSettings(userID)
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = settings.InvoicePrintMode
	}
	if mode != "thermal" {
		mode = "a4"
	}

	printSize := normalizeThermalPrintSize(req.PrintSize)
	if req.PrintSize == "" {
		printSize = normalizeThermalPrintSize(settings.ThermalPrintSize)
	}

	switch req.DocumentType {
	case "invoice":
		var invoice models.Invoice
		if err := utils.DB.Where("user_id = ? AND id = ?", userID, req.DocumentID).
			Preload("Party").
			Preload("Items").
			First(&invoice).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Invoice not found"})
			return
		}
		attachInvoicePaymentSplits(utils.DB, &invoice)
		title := "Invoice " + invoice.InvoiceNumber
		var business models.Business
		_ = utils.DB.Where("user_id = ?", userID).First(&business)

		if mode == "thermal" {
			content, width, logoURL := generateInvoiceThermal(userID, req.DocumentID, printSize)
			if content == "" {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate thermal print"})
				return
			}
			// PDF is optional for thermal — desktop/browser print from text/ESC-POS.
			pdfBase64 := ""
			if pdfBytes, err := buildThermalReceiptPDF(content, width, logoURL); err == nil {
				pdfBase64 = base64.StdEncoding.EncodeToString(pdfBytes)
			}
			c.JSON(http.StatusOK, DocumentPrintResponse{
				Mode:        "thermal",
				PDFBase64:   pdfBase64,
				ContentType: "application/pdf",
				Content:     content,
				Width:       width,
				PrinterName: settings.ThermalPrinterName,
				Title:       title,
				LogoURL:     logoURL,
				LogoBase64:  thermalLogoDataURL(logoURL),
			})
			return
		}

		pdfBytes, err := buildInvoiceDocumentPDF(invoice, &business, settings)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to build invoice PDF page"})
			return
		}
		c.JSON(http.StatusOK, DocumentPrintResponse{
			Mode:        "a4",
			PDFBase64:   base64.StdEncoding.EncodeToString(pdfBytes),
			ContentType: "application/pdf",
			PrinterName: settings.DocumentPrinterName,
			Title:       title,
		})
	case "expense":
		var expense models.Expense
		if err := utils.DB.Where("user_id = ? AND id = ?", userID, req.DocumentID).
			First(&expense).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Expense not found"})
			return
		}
		title := "Expense " + expense.ExpenseNumber
		content, width, logoURL := generateExpenseThermal(userID, req.DocumentID, printSize)
		if content == "" {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate thermal print"})
			return
		}
		pdfBytes, err := buildThermalReceiptPDF(content, width, logoURL)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to build expense PDF page"})
			return
		}
		printerName := settings.ThermalPrinterName
		if mode == "a4" {
			printerName = settings.DocumentPrinterName
		}
		c.JSON(http.StatusOK, DocumentPrintResponse{
			Mode:        "thermal",
			PDFBase64:   base64.StdEncoding.EncodeToString(pdfBytes),
			ContentType: "application/pdf",
			Content:     content,
			Width:       width,
			PrinterName: printerName,
			Title:       title,
			LogoURL:     logoURL,
			LogoBase64:  thermalLogoDataURL(logoURL),
		})
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid document type"})
	}
}

func GetThermalPrintPreview(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	printSize := normalizeThermalPrintSize(c.DefaultQuery("print_size", "2inch"))

	content, width, logoURL := generateSampleInvoiceThermal(userID, printSize)
	if content == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate preview"})
		return
	}

	c.JSON(http.StatusOK, ThermalPrintResponse{
		Content:    content,
		Width:      width,
		LogoURL:    logoURL,
		LogoBase64: thermalLogoDataURL(logoURL),
	})
}

func GetBarcodePrintPreview(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	mode := c.DefaultQuery("mode", "a4")
	if mode != "label" && mode != "a4" {
		mode = "a4"
	}

	size := "2inch"
	if q := c.Query("size"); q != "" {
		size = normalizeBarcodeLabelSize(q)
	} else {
		var printSettings models.PrintSettings
		if err := utils.DB.Where("user_id = ?", userID).First(&printSettings).Error; err == nil {
			size = normalizeBarcodeLabelSize(printSettings.BarcodeLabelSize)
		}
	}

	pageHTML := buildBarcodePreviewHTML(userID, mode, size)
	c.JSON(http.StatusOK, gin.H{"html": pageHTML, "label_size": size})
}

func generateSampleInvoiceThermal(userID uuid.UUID, printSize string) (string, int, string) {
	var business models.Business
	utils.DB.Where("user_id = ?", userID).First(&business)
	if business.Name == "" {
		business.Name = "Your Business Name"
		business.Address = "123 Business Street, City"
		business.Phone = "+91 98765 43210"
		business.GSTIN = "29ABCDE1234F1Z5"
	}

	dueDate := time.Now().AddDate(0, 0, 7)
	sample := models.Invoice{
		InvoiceNumber: "INV-PREVIEW-001",
		Date:          time.Now(),
		DueDate:       &dueDate,
		SubTotal:      450,
		DiscountTotal: 0,
		CGSTTotal:     40.5,
		SGSTTotal:     40.5,
		TotalAmount:   531,
		AmountPaid:    0,
		Status:        "draft",
		PaymentMode:   "Cash",
		Terms:         "Thank you for your business!",
		Party: models.Party{
			Name:    "Sample Customer",
			Address: "456 Customer Lane",
			Phone:   "+91 91234 56789",
			GSTIN:   "29XYZAB5678C1D2",
		},
		Items: []models.InvoiceItem{
			{Description: "Sample Item A", Quantity: 2, UnitPrice: 150, TaxRate: 18, Total: 354},
			{Description: "Sample Item B", Quantity: 1, UnitPrice: 150, TaxRate: 18, Total: 177},
		},
	}

	printSize = normalizeThermalPrintSize(printSize)
	width := thermalWidthMM(printSize)
	content := renderInvoiceThermal(sample, business, printSize)
	return content, width, thermalLogoURL(userID, business, printSize)
}

func buildBarcodePreviewHTML(userID uuid.UUID, mode string, labelSizeKey string) string {
	var business models.Business
	if err := utils.DB.Where("user_id = ?", userID).First(&business).Error; err != nil {
		business = models.Business{
			LabelPaperSize: "A4",
			LabelWidthMM:   50,
			LabelHeightMM:  30,
			LabelColumns:   3,
			LabelRows:      8,
			LabelMarginMM:  10,
		}
	}

	sample := productLabelData{
		Name:      "Sample Product",
		SKU:       "DEMO-001",
		ItemCode:  "8901234567890",
		Category:  "General",
		SalePrice: 199,
		MRP:       249,
	}

	if mode == "label" {
		size := getBarcodeLabelSize(labelSizeKey)
		compact := labelSizeKey == "1inch" || labelSizeKey == "1.5inch"
		singleLabel := buildProductLabelHTML(sample, size, compact)
		css := barcodeLabelPageCSS(size) + `
body { display: flex; align-items: center; justify-content: center; min-height: 100vh; background: #f3f4f6; }
.label {
	border: 1px dashed #9ca3af;
	background: white;
	box-shadow: 0 1px 3px rgba(0,0,0,0.08);
}
.caption {
	position: fixed;
	bottom: 12px;
	left: 0;
	right: 0;
	text-align: center;
	font-size: 12px;
	color: #6b7280;
	font-family: Arial, sans-serif;
}
`
		body := singleLabel + fmt.Sprintf(`<p class="caption">%s · %.0f×%.0f mm thermal label</p>`,
			html.EscapeString(size.Label), size.WidthMM, size.HeightMM)
		return wrapBarcodePreviewDocument(css, body)
	}

	columns := business.LabelColumns
	if columns < 1 || columns > 6 {
		columns = 4
	}
	sheetLayout := a4LabelSheetLayoutFromBusiness(business)
	a4Size := barcodeLabelSizeForA4Layout(sheetLayout)
	singleLabel := buildProductLabelHTML(sample, a4Size, false)
	previewCount := sheetLayout.labelsPerSheet()
	if previewCount > 8 {
		previewCount = 8
	}
	labelHTMLs := make([]string, previewCount)
	for i := range labelHTMLs {
		labelHTMLs[i] = singleLabel
	}

	bodyDoc := buildA4LabelsSheetDocument("A4 Label Preview", labelHTMLs, sheetLayout, 1, true)
	caption := fmt.Sprintf(
		`<p class="caption" style="font-size:11px;color:#6b7280;text-align:center;margin:8px 0;font-family:Arial,sans-serif">A4 sheet · %s · %d×%d grid · %d labels/sheet · %.1f×%.1f mm</p>`,
		html.EscapeString(sheetLayout.PaperSize),
		sheetLayout.Columns, sheetLayout.Rows, sheetLayout.labelsPerSheet(),
		sheetLayout.LabelWidthMM, sheetLayout.LabelHeightMM,
	)
	// Inject caption before closing body in preview document.
	if idx := strings.LastIndex(bodyDoc, "</body>"); idx >= 0 {
		bodyDoc = bodyDoc[:idx] + caption + bodyDoc[idx:]
	}
	return bodyDoc
}

func generateInvoiceThermal(userID uuid.UUID, invoiceID uuid.UUID, printSize string) (string, int, string) {
	var invoice models.Invoice
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, invoiceID).
		Preload("Party").
		Preload("Items").
		First(&invoice).Error; err != nil {
		return "", 0, ""
	}

	var business models.Business
	utils.DB.Where("user_id = ?", userID).First(&business)

	printSize = normalizeThermalPrintSize(printSize)
	width := thermalWidthMM(printSize)
	content := renderInvoiceThermal(invoice, business, printSize)
	return content, width, thermalLogoURL(userID, business, printSize)
}

func generateExpenseThermal(userID uuid.UUID, expenseID uuid.UUID, printSize string) (string, int, string) {
	var expense models.Expense
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, expenseID).
		Preload("Items").
		First(&expense).Error; err != nil {
		return "", 0, ""
	}

	var business models.Business
	utils.DB.Where("user_id = ?", userID).First(&business)

	printSize = normalizeThermalPrintSize(printSize)
	width := thermalWidthMM(printSize)
	content := renderExpenseThermal(expense, business, printSize)
	return content, width, thermalLogoURL(userID, business, printSize)
}

func thermalLogoURL(userID uuid.UUID, business models.Business, printSize string) string {
	// Very narrow rolls can't show a useful logo.
	size := normalizeThermalPrintSize(printSize)
	if size == "1inch" || size == "1.5inch" {
		return ""
	}
	settings := loadInvoiceSettings(userID)
	if !settings.ShowLogo {
		return ""
	}
	return strings.TrimSpace(business.LogoURL)
}

func thermalLogoDataURL(logoURL string) string {
	data, kind := fetchThermalLogoBytes(logoURL)
	if len(data) == 0 || kind == "" {
		return ""
	}
	mime := "image/png"
	switch kind {
	case "JPG":
		mime = "image/jpeg"
	case "GIF":
		mime = "image/gif"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
}

type InvoiceTemplateData struct {
	Header       string
	SepStrong    string
	SepWeak      string
	DocumentInfo string
	PartyInfo    string
	Items        []ItemTemplateData
	ItemsHeader  string
	Totals       string
	TaxDetails   string
	Footer       string
	Terms        string
}

type ExpenseTemplateData struct {
	Header       string
	SepStrong    string
	SepWeak      string
	DocumentInfo string
	PartyInfo    string
	Items        []ItemTemplateData
	ItemsHeader  string
	Totals       string
	TaxDetails   string
	Footer       string
	Terms        string
}

type ItemTemplateData struct {
	Description string
	Qty         string
	Rate        string
	Total       string
	ItemLine    string
}

func buildThermalBusinessHeader(business models.Business, cols int, title string) string {
	var lines []string
	if t := strings.TrimSpace(title); t != "" {
		lines = append(lines, "@C@@B@"+truncateString(t, cols))
	}
	name := strings.TrimSpace(business.Name)
	if name != "" {
		// @C@ = center, @B@ = bold (interpreted by ESC/POS / HTML printers)
		lines = append(lines, "@C@@B@"+truncateString(strings.ToUpper(name), cols))
	}
	addr := strings.TrimSpace(business.Address)
	if city := strings.TrimSpace(business.City); city != "" {
		if addr != "" {
			addr += ", " + city
		} else {
			addr = city
		}
	}
	if state := strings.TrimSpace(business.State); state != "" {
		if addr != "" {
			addr += ", " + state
		} else {
			addr = state
		}
	}
	if pin := strings.TrimSpace(business.Pincode); pin != "" {
		if addr != "" {
			addr += " - " + pin
		} else {
			addr = pin
		}
	}
	for _, part := range wrapThermalText(addr, cols) {
		lines = append(lines, "@C@"+part)
	}
	if phone := strings.TrimSpace(business.Phone); phone != "" {
		lines = append(lines, "@C@"+truncateString("Ph: "+phone, cols))
	}
	if gstin := strings.TrimSpace(business.GSTIN); gstin != "" {
		lines = append(lines, "@C@"+truncateString("GSTIN: "+gstin, cols))
	}
	return strings.Join(lines, "\n")
}

func wrapThermalText(s string, cols int) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if cols < 8 {
		cols = 8
	}
	words := strings.Fields(s)
	var lines []string
	var cur string
	for _, w := range words {
		if cur == "" {
			cur = w
			continue
		}
		if runeLen(cur)+1+runeLen(w) <= cols {
			cur += " " + w
			continue
		}
		lines = append(lines, truncateString(cur, cols))
		cur = w
	}
	if cur != "" {
		lines = append(lines, truncateString(cur, cols))
	}
	return lines
}

// formatThermalQuantity prints fractional quantities (e.g. 1.5 KG) instead of rounding to integers.
func formatThermalQuantity(qty float64, unit string) string {
	if qty <= 0 {
		return "0"
	}
	isWhole := math.Abs(qty-math.Round(qty)) < 0.0005
	if isWeightBasedUnit(unit) || !isWhole {
		s := fmt.Sprintf("%.3f", qty)
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ".")
		if s == "" {
			return "0"
		}
		return s
	}
	return fmt.Sprintf("%.0f", qty)
}

// invoiceItemInclusiveUnitPrice returns the per-unit price including tax and line discount,
// so qty × rate matches the printed line total on thermal receipts.
func invoiceItemInclusiveUnitPrice(item models.InvoiceItem) float64 {
	if item.Quantity > 0 {
		return item.Total / item.Quantity
	}
	if item.TaxRate > 0 {
		discounted := item.UnitPrice * (1 - item.Discount/100)
		return discounted * (1 + item.TaxRate/100)
	}
	return item.UnitPrice
}

func formatStackedItemLine(qty, rate, total string, cols int) string {
	body := fmt.Sprintf("%s x %s = %s", qty, rate, total)
	if runeLen(body)+2 <= cols {
		return "  " + body
	}
	return truncateString(body, cols)
}

func formatWideItemLine(desc, qty, rate, total string, cols int) string {
	// Item (flex) | Qty 4 | Rate 8 | Total 8  (+ spaces)
	qtyW, rateW, totalW := 4, 8, 8
	fixed := qtyW + rateW + totalW + 3
	descW := cols - fixed
	if descW < 8 {
		descW = 8
	}
	return padRightRunes(truncateString(desc, descW), descW) + " " +
		padLeftRunes(qty, qtyW) + " " +
		padLeftRunes(rate, rateW) + " " +
		padLeftRunes(total, totalW)
}

func formatWideItemsHeader(cols int) string {
	qtyW, rateW, totalW := 4, 8, 8
	fixed := qtyW + rateW + totalW + 3
	descW := cols - fixed
	if descW < 8 {
		descW = 8
	}
	return padRightRunes("Item", descW) + " " +
		padLeftRunes("Qty", qtyW) + " " +
		padLeftRunes("Rate", rateW) + " " +
		padLeftRunes("Total", totalW)
}

func renderInvoiceThermal(invoice models.Invoice, business models.Business, printSize string) string {
	printSize = normalizeThermalPrintSize(printSize)
	data := prepareInvoiceData(invoice, business, printSize)

	tmplStr := templateNarrow
	if isWideThermal(printSize) {
		tmplStr = templateWide
	}
	tmpl, err := template.New("thermal").Parse(tmplStr)
	if err != nil {
		return ""
	}
	var builder strings.Builder
	if err := tmpl.Execute(&builder, data); err != nil {
		return ""
	}
	return strings.TrimRight(builder.String(), "\n") + "\n"
}

func renderExpenseThermal(expense models.Expense, business models.Business, printSize string) string {
	printSize = normalizeThermalPrintSize(printSize)
	data := prepareExpenseData(expense, business, printSize)

	tmplStr := templateNarrow
	if isWideThermal(printSize) {
		tmplStr = templateWide
	}
	tmpl, err := template.New("thermal").Parse(tmplStr)
	if err != nil {
		return ""
	}
	var builder strings.Builder
	if err := tmpl.Execute(&builder, data); err != nil {
		return ""
	}
	return strings.TrimRight(builder.String(), "\n") + "\n"
}

func prepareInvoiceData(invoice models.Invoice, business models.Business, printSize string) InvoiceTemplateData {
	cols := thermalCols(printSize)
	wide := isWideThermal(printSize)
	sepStrong := thermalSep(cols, '=')
	sepWeak := thermalSep(cols, '-')

	header := buildThermalBusinessHeader(business, cols, "TAX INVOICE")

	docInfo := formatLabelValue("Invoice", invoice.InvoiceNumber, cols)
	docInfo += "\n" + formatLabelValue("Date", invoice.Date.Format("02-01-2006"), cols)
	if invoice.DueDate != nil {
		docInfo += "\n" + formatLabelValue("Due", invoice.DueDate.Format("02-01-2006"), cols)
	}

	partyInfo := "Bill To: " + truncateString(invoice.Party.Name, cols-9)
	if invoice.Party.Address != "" {
		for _, line := range wrapThermalText(invoice.Party.Address, cols) {
			partyInfo += "\n" + line
		}
	}
	if invoice.Party.Phone != "" {
		partyInfo += "\n" + truncateString("Ph: "+invoice.Party.Phone, cols)
	}
	if invoice.Party.GSTIN != "" {
		partyInfo += "\n" + truncateString("GSTIN: "+invoice.Party.GSTIN, cols)
	}

	items := make([]ItemTemplateData, len(invoice.Items))
	maxDescLen := thermalDescLen(printSize)
	for i, item := range invoice.Items {
		qty := formatThermalQuantity(item.Quantity, item.Unit)
		rate := fmt.Sprintf("%.2f", invoiceItemInclusiveUnitPrice(item))
		total := fmt.Sprintf("%.2f", item.Total)
		desc := truncateString(item.Description, maxDescLen)
		entry := ItemTemplateData{
			Description: desc,
			Qty:         qty,
			Rate:        rate,
			Total:       total,
		}
		if wide {
			entry.ItemLine = formatWideItemLine(item.Description, qty, rate, total, cols)
		} else {
			entry.ItemLine = formatStackedItemLine(qty, rate, total, cols)
		}
		items[i] = entry
	}

	itemsHeader := ""
	if wide {
		itemsHeader = formatWideItemsHeader(cols)
	}

	var totalLines []string
	totalLines = append(totalLines, formatLabelValue("Sub Total", fmt.Sprintf("%.2f", invoice.SubTotal), cols))
	if invoice.DiscountTotal > 0 {
		totalLines = append(totalLines, formatLabelValue("Discount", fmt.Sprintf("-%.2f", invoice.DiscountTotal), cols))
	}
	if invoice.InvoiceDiscount > 0 {
		totalLines = append(totalLines, formatLabelValue("Inv Disc", fmt.Sprintf("-%.2f", invoice.InvoiceDiscount), cols))
	}
	if invoice.IsInterState {
		totalLines = append(totalLines, formatLabelValue("IGST", fmt.Sprintf("%.2f", invoice.IGSTTotal), cols))
	} else {
		if invoice.CGSTTotal > 0 {
			totalLines = append(totalLines, formatLabelValue("CGST", fmt.Sprintf("%.2f", invoice.CGSTTotal), cols))
		}
		if invoice.SGSTTotal > 0 {
			totalLines = append(totalLines, formatLabelValue("SGST", fmt.Sprintf("%.2f", invoice.SGSTTotal), cols))
		}
	}
	if invoice.AdditionalCharges > 0 {
		totalLines = append(totalLines, formatLabelValue("Addl Charges", fmt.Sprintf("%.2f", invoice.AdditionalCharges), cols))
	}
	if invoice.LoyaltyDiscount > 0 {
		totalLines = append(totalLines, formatLabelValue("Loyalty", fmt.Sprintf("-%.2f", invoice.LoyaltyDiscount), cols))
	}
	if invoice.RoundOff != 0 {
		roundOffLabel := fmt.Sprintf("%.2f", invoice.RoundOff)
		if invoice.RoundOff > 0 {
			roundOffLabel = "+" + roundOffLabel
		}
		totalLines = append(totalLines, formatLabelValue("Round Off", roundOffLabel, cols))
	}
	totalLines = append(totalLines, sepWeak)
	totalLines = append(totalLines, formatLabelValue("TOTAL", fmt.Sprintf("%.2f", invoice.TotalAmount), cols))
	attachInvoicePaymentSplits(utils.DB, &invoice)
	if label := formatPaymentSplitsLabel(invoice.PaymentSplits, invoice.PaymentMode); label != "" {
		totalLines = append(totalLines, formatLabelValue("Payment", label, cols))
	}
	if invoice.AmountPaid > 0 {
		totalLines = append(totalLines, formatLabelValue("Paid", fmt.Sprintf("%.2f", invoice.AmountPaid), cols))
		balance := invoice.TotalAmount - invoice.AmountPaid
		if balance > 0.009 {
			totalLines = append(totalLines, formatLabelValue("Balance", fmt.Sprintf("%.2f", balance), cols))
		}
	}
	totals := strings.Join(totalLines, "\n")

	taxDetails := ""

	footer := formatLabelValue("Status", strings.ToUpper(invoice.Status), cols)
	terms := invoice.Terms
	if terms == "" {
		terms = "Thank you for your business!"
	}
	terms = strings.Join(wrapThermalText(terms, cols), "\n")

	return InvoiceTemplateData{
		Header:       header,
		SepStrong:    sepStrong,
		SepWeak:      sepWeak,
		DocumentInfo: docInfo,
		PartyInfo:    partyInfo,
		Items:        items,
		ItemsHeader:  itemsHeader,
		Totals:       totals,
		TaxDetails:   taxDetails,
		Footer:       footer,
		Terms:        terms,
	}
}

func prepareExpenseData(expense models.Expense, business models.Business, printSize string) ExpenseTemplateData {
	cols := thermalCols(printSize)
	wide := isWideThermal(printSize)
	sepStrong := thermalSep(cols, '=')
	sepWeak := thermalSep(cols, '-')

	header := buildThermalBusinessHeader(business, cols, "EXPENSE")

	docInfo := formatLabelValue("No.", expense.ExpenseNumber, cols)
	docInfo += "\n" + formatLabelValue("Date", expense.Date.Format("02-01-2006"), cols)
	docInfo += "\n" + formatLabelValue("Category", truncateString(expense.Category, cols/2), cols)

	partyInfo := "Vendor: " + truncateString(expense.Vendor, cols-8)
	if expense.PaymentMode != "" {
		partyInfo += "\n" + formatLabelValue("Payment", paymentMethodLabel(expense.PaymentMode), cols)
	}

	items := make([]ItemTemplateData, len(expense.Items))
	maxDescLen := thermalDescLen(printSize)
	for i, item := range expense.Items {
		qty := formatThermalQuantity(item.Quantity, "")
		rate := fmt.Sprintf("%.2f", item.UnitPrice)
		total := fmt.Sprintf("%.2f", item.Total)
		entry := ItemTemplateData{
			Description: truncateString(item.Description, maxDescLen),
			Qty:         qty,
			Rate:        rate,
			Total:       total,
		}
		if wide {
			entry.ItemLine = formatWideItemLine(item.Description, qty, rate, total, cols)
		} else {
			entry.ItemLine = formatStackedItemLine(qty, rate, total, cols)
		}
		items[i] = entry
	}

	itemsHeader := ""
	if wide {
		itemsHeader = formatWideItemsHeader(cols)
	}

	var totalLines []string
	totalLines = append(totalLines, formatLabelValue("Sub Total", fmt.Sprintf("%.2f", expense.SubTotal), cols))
	if expense.WithGST {
		totalLines = append(totalLines, formatLabelValue(fmt.Sprintf("Tax (%.0f%%)", expense.TaxRate), fmt.Sprintf("%.2f", expense.TaxTotal), cols))
	}
	totalLines = append(totalLines, sepWeak)
	totalLines = append(totalLines, formatLabelValue("TOTAL", fmt.Sprintf("%.2f", expense.Amount), cols))
	totals := strings.Join(totalLines, "\n")

	taxDetails := ""
	if wide && expense.Notes != "" {
		taxDetails = strings.Join(wrapThermalText("Notes: "+expense.Notes, cols), "\n")
	}

	footer := "@C@EXPENSE RECEIPT"
	terms := strings.Join(wrapThermalText("Thank you for your business!", cols), "\n")

	return ExpenseTemplateData{
		Header:       header,
		SepStrong:    sepStrong,
		SepWeak:      sepWeak,
		DocumentInfo: docInfo,
		PartyInfo:    partyInfo,
		Items:        items,
		ItemsHeader:  itemsHeader,
		Totals:       totals,
		TaxDetails:   taxDetails,
		Footer:       footer,
		Terms:        terms,
	}
}

func truncateString(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	if maxLen <= 1 {
		return string(r[:maxLen])
	}
	return string(r[:maxLen-1]) + "."
}
