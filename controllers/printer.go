package controllers

import (
	"encoding/base64"
	"fmt"
	"html"
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

func thermalDescLen(printSize string) int {
	switch normalizeThermalPrintSize(printSize) {
	case "1inch":
		return 12
	case "1.5inch":
		return 16
	case "3inch":
		return 30
	default:
		return 20
	}
}

type ThermalPrintResponse struct {
	Content string `json:"content"`
	Width   int    `json:"width"` // in mm
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
}

// 2-inch (58mm) template
const template2Inch = `{{.Header}}
================================
{{.DocumentInfo}}
--------------------------------
{{.PartyInfo}}
--------------------------------
{{range .Items}}{{.Description}}
  {{.Qty}} x {{.Rate}} = {{.Total}}
{{end}}
--------------------------------
{{.Totals}}
{{.Footer}}
================================
{{.Terms}}
`

// 3-inch (80mm) template
const template3Inch = `{{.Header}}
========================================
{{.DocumentInfo}}
----------------------------------------
{{.PartyInfo}}
----------------------------------------
{{.ItemsHeader}}
{{range .Items}}{{.ItemLine}}
{{end}}
----------------------------------------
{{.Totals}}
{{.TaxDetails}}
{{.Footer}}
========================================
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

	switch req.DocumentType {
	case "invoice":
		content, width = generateInvoiceThermal(userID, req.DocumentID, req.PrintSize)
	case "expense":
		content, width = generateExpenseThermal(userID, req.DocumentID, req.PrintSize)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid document type"})
		return
	}

	c.JSON(http.StatusOK, ThermalPrintResponse{
		Content: content,
		Width:   width,
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
		title := "Invoice " + invoice.InvoiceNumber
		var business models.Business
		_ = utils.DB.Where("user_id = ?", userID).First(&business)

		if mode == "thermal" {
			content, width := generateInvoiceThermal(userID, req.DocumentID, printSize)
			if content == "" {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate thermal print"})
				return
			}
			pdfBytes, err := buildThermalReceiptPDF(content, width)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to build thermal PDF page"})
				return
			}
			c.JSON(http.StatusOK, DocumentPrintResponse{
				Mode:        "thermal",
				PDFBase64:   base64.StdEncoding.EncodeToString(pdfBytes),
				ContentType: "application/pdf",
				Content:     content,
				Width:       width,
				PrinterName: settings.ThermalPrinterName,
				Title:       title,
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
		content, width := generateExpenseThermal(userID, req.DocumentID, printSize)
		if content == "" {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate thermal print"})
			return
		}
		pdfBytes, err := buildThermalReceiptPDF(content, width)
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
		})
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid document type"})
	}
}

func GetThermalPrintPreview(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	printSize := normalizeThermalPrintSize(c.DefaultQuery("print_size", "2inch"))

	content, width := generateSampleInvoiceThermal(userID, printSize)
	if content == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate preview"})
		return
	}

	c.JSON(http.StatusOK, ThermalPrintResponse{
		Content: content,
		Width:   width,
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

func generateSampleInvoiceThermal(userID uuid.UUID, printSize string) (string, int) {
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
			{Description: "Sample Item A", Quantity: 2, UnitPrice: 150, Total: 300},
			{Description: "Sample Item B", Quantity: 1, UnitPrice: 150, Total: 150},
		},
	}

	printSize = normalizeThermalPrintSize(printSize)
	width := thermalWidthMM(printSize)

	data := prepareInvoiceData(sample, business, printSize)

	tmplStr := template2Inch
	if isWideThermal(printSize) {
		tmplStr = template3Inch
	}

	tmpl, err := template.New("thermal-preview").Parse(tmplStr)
	if err != nil {
		return "", 0
	}

	var builder strings.Builder
	if err := tmpl.Execute(&builder, data); err != nil {
		return "", 0
	}

	return builder.String(), width
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
	if columns < 1 || columns > 5 {
		columns = 3
	}
	a4Width := business.LabelWidthMM
	a4Height := business.LabelHeightMM
	if a4Width < 10 {
		a4Width = 50
	}
	if a4Height < 10 {
		a4Height = 30
	}
	a4Size := BarcodeLabelSize{
		Key: "a4", WidthMM: a4Width, HeightMM: a4Height,
		NameFontPx: 11, SkuFontPx: 9, PriceFontPx: 12, MetaFontPx: 8,
		BarcodeH: 28, BarcodeW: 1.2, PaddingMM: 3,
	}
	singleLabel := buildProductLabelHTML(sample, a4Size, false)
	previewCount := columns * 2
	if previewCount > 6 {
		previewCount = 6
	}
	labelsHTML := strings.Repeat(singleLabel, previewCount)
	colWidth := 100.0 / float64(columns)

	css := fmt.Sprintf(`
body { font-family: Arial, sans-serif; margin: 0; padding: 8px; background: #f3f4f6; }
.sheet {
	background: white;
	border: 1px solid #d1d5db;
	padding: %.2fmm;
	max-width: 210mm;
	margin: 0 auto;
}
.labels-grid {
	display: grid;
	grid-template-columns: repeat(%d, %s);
	gap: 5mm;
}
.label {
	border: 1px solid #000;
	padding: %.2fmm;
	text-align: center;
	box-sizing: border-box;
	width: %.2fmm;
	height: %.2fmm;
	display: flex;
	flex-direction: column;
	align-items: center;
	justify-content: center;
	overflow: hidden;
}
.product-name { font-size: %.1fpx; font-weight: bold; margin-bottom: 2px; max-width: 100%%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.product-sku { font-size: %.1fpx; margin-bottom: 2px; }
.product-barcode { width: 100%%; line-height: 0; }
.product-barcode svg { max-width: 100%%; height: auto; }
.product-price { font-size: %.1fpx; font-weight: bold; }
.product-mrp, .product-category { font-size: %.1fpx; color: #666; }
.caption { font-size: 11px; color: #6b7280; text-align: center; margin-top: 8px; }
`, business.LabelMarginMM, columns, fmt.Sprintf("%.2f%%", colWidth), a4Size.PaddingMM, a4Width, a4Height,
		a4Size.NameFontPx, a4Size.SkuFontPx, a4Size.PriceFontPx, a4Size.MetaFontPx)

	body := fmt.Sprintf(`<div class="sheet">
<div class="labels-grid">%s</div>
<p class="caption">A4 sheet · %d columns × %d rows (preview shows sample labels)</p>
</div>`, labelsHTML, columns, business.LabelRows)

	return wrapBarcodePreviewDocument(css, body)
}

func generateInvoiceThermal(userID uuid.UUID, invoiceID uuid.UUID, printSize string) (string, int) {
	var invoice models.Invoice
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, invoiceID).
		Preload("Party").
		Preload("Items").
		First(&invoice).Error; err != nil {
		return "", 0
	}

	// Get business info
	var business models.Business
	utils.DB.Where("user_id = ?", userID).First(&business)

	printSize = normalizeThermalPrintSize(printSize)
	width := thermalWidthMM(printSize)

	data := prepareInvoiceData(invoice, business, printSize)

	tmplStr := template2Inch
	if isWideThermal(printSize) {
		tmplStr = template3Inch
	}

	tmpl, err := template.New("thermal").Parse(tmplStr)
	if err != nil {
		return "", 0
	}

	var builder strings.Builder
	err = tmpl.Execute(&builder, data)
	if err != nil {
		return "", 0
	}

	return builder.String(), width
}

func generateExpenseThermal(userID uuid.UUID, expenseID uuid.UUID, printSize string) (string, int) {
	var expense models.Expense
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, expenseID).
		Preload("Items").
		First(&expense).Error; err != nil {
		return "", 0
	}

	// Get business info
	var business models.Business
	utils.DB.Where("user_id = ?", userID).First(&business)

	printSize = normalizeThermalPrintSize(printSize)
	width := thermalWidthMM(printSize)

	data := prepareExpenseData(expense, business, printSize)

	tmplStr := template2Inch
	if isWideThermal(printSize) {
		tmplStr = template3Inch
	}

	tmpl, err := template.New("thermal").Parse(tmplStr)
	if err != nil {
		return "", 0
	}

	var builder strings.Builder
	err = tmpl.Execute(&builder, data)
	if err != nil {
		return "", 0
	}

	return builder.String(), width
}

type InvoiceTemplateData struct {
	Header       string
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

func prepareInvoiceData(invoice models.Invoice, business models.Business, printSize string) InvoiceTemplateData {
	// Header
	header := fmt.Sprintf("%s", business.Name)
	if business.Address != "" {
		header += fmt.Sprintf("\n%s", business.Address)
	}
	if business.Phone != "" {
		header += fmt.Sprintf("\nPh: %s", business.Phone)
	}
	if business.GSTIN != "" {
		header += fmt.Sprintf("\nGSTIN: %s", business.GSTIN)
	}

	// Document Info
	docInfo := fmt.Sprintf("TAX INVOICE: %s", invoice.InvoiceNumber)
	docInfo += fmt.Sprintf("\nDate: %s", invoice.Date.Format("02-01-2006"))
	if invoice.DueDate != nil {
		docInfo += fmt.Sprintf("\nDue: %s", invoice.DueDate.Format("02-01-2006"))
	}

	// Party Info
	partyInfo := fmt.Sprintf("Bill To: %s", invoice.Party.Name)
	if invoice.Party.Address != "" {
		partyInfo += fmt.Sprintf("\n%s", invoice.Party.Address)
	}
	if invoice.Party.Phone != "" {
		partyInfo += fmt.Sprintf("\nPh: %s", invoice.Party.Phone)
	}
	if invoice.Party.GSTIN != "" {
		partyInfo += fmt.Sprintf("\nGSTIN: %s", invoice.Party.GSTIN)
	}

	// Items
	items := make([]ItemTemplateData, len(invoice.Items))
	maxDescLen := thermalDescLen(printSize)
	wide := isWideThermal(printSize)
	for i, item := range invoice.Items {
		items[i] = ItemTemplateData{
			Description: truncateString(item.Description, maxDescLen),
			Qty:         fmt.Sprintf("%.0f", item.Quantity),
			Rate:        fmt.Sprintf("%.2f", item.UnitPrice),
			Total:       fmt.Sprintf("%.2f", item.Total),
		}
		if wide {
			items[i].ItemLine = fmt.Sprintf("%-30s %5s x %8s = %8s",
				truncateString(item.Description, 30),
				fmt.Sprintf("%.0f", item.Quantity),
				fmt.Sprintf("%.2f", item.UnitPrice),
				fmt.Sprintf("%.2f", item.Total))
		}
	}

	// Items Header (3-inch only)
	itemsHeader := ""
	if wide {
		itemsHeader = fmt.Sprintf("%-30s %5s %8s %8s", "Item", "Qty", "Rate", "Total")
	}

	// Totals
	totals := fmt.Sprintf("Sub Total: %.2f", invoice.SubTotal)
	if invoice.DiscountTotal > 0 {
		totals += fmt.Sprintf("\nDiscount: -%.2f", invoice.DiscountTotal)
	}
	if invoice.InvoiceDiscount > 0 {
		totals += fmt.Sprintf("\nInv Disc: -%.2f", invoice.InvoiceDiscount)
	}
	if invoice.IsInterState {
		totals += fmt.Sprintf("\nIGST: %.2f", invoice.IGSTTotal)
	} else {
		totals += fmt.Sprintf("\nCGST: %.2f", invoice.CGSTTotal)
		totals += fmt.Sprintf("\nSGST: %.2f", invoice.SGSTTotal)
	}
	if invoice.AdditionalCharges > 0 {
		totals += fmt.Sprintf("\nAddl Charges: %.2f", invoice.AdditionalCharges)
	}
	totals += fmt.Sprintf("\n----------------")
	totals += fmt.Sprintf("\nTOTAL: %.2f", invoice.TotalAmount)
	if invoice.AmountPaid > 0 {
		totals += fmt.Sprintf("\nPaid: %.2f", invoice.AmountPaid)
		balance := invoice.TotalAmount - invoice.AmountPaid
		if balance > 0 {
			totals += fmt.Sprintf("\nBalance: %.2f", balance)
		}
	}

	// Tax Details (3-inch only)
	taxDetails := ""
	if wide {
		taxDetails = fmt.Sprintf("Payment Mode: %s", invoice.PaymentMode)
	}

	// Footer
	footer := fmt.Sprintf("Status: %s", strings.ToUpper(invoice.Status))

	// Terms
	terms := invoice.Terms
	if terms == "" {
		terms = "Thank you for your business!"
	}

	return InvoiceTemplateData{
		Header:       header,
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
	// Header
	header := fmt.Sprintf("%s", business.Name)
	if business.Address != "" {
		header += fmt.Sprintf("\n%s", business.Address)
	}
	if business.Phone != "" {
		header += fmt.Sprintf("\nPh: %s", business.Phone)
	}

	// Document Info
	docInfo := fmt.Sprintf("EXPENSE: %s", expense.ExpenseNumber)
	docInfo += fmt.Sprintf("\nDate: %s", expense.Date.Format("02-01-2006"))
	docInfo += fmt.Sprintf("\nCategory: %s", expense.Category)

	// Party Info
	partyInfo := fmt.Sprintf("Vendor: %s", expense.Vendor)
	if expense.PaymentMode != "" {
		partyInfo += fmt.Sprintf("\nPayment: %s", expense.PaymentMode)
	}

	// Items
	items := make([]ItemTemplateData, len(expense.Items))
	maxDescLen := thermalDescLen(printSize)
	wide := isWideThermal(printSize)
	for i, item := range expense.Items {
		items[i] = ItemTemplateData{
			Description: truncateString(item.Description, maxDescLen),
			Qty:         fmt.Sprintf("%.0f", item.Quantity),
			Rate:        fmt.Sprintf("%.2f", item.UnitPrice),
			Total:       fmt.Sprintf("%.2f", item.Total),
		}
		if wide {
			items[i].ItemLine = fmt.Sprintf("%-30s %5s x %8s = %8s",
				truncateString(item.Description, 30),
				fmt.Sprintf("%.0f", item.Quantity),
				fmt.Sprintf("%.2f", item.UnitPrice),
				fmt.Sprintf("%.2f", item.Total))
		}
	}

	// Items Header (3-inch only)
	itemsHeader := ""
	if wide {
		itemsHeader = fmt.Sprintf("%-30s %5s %8s %8s", "Item", "Qty", "Rate", "Total")
	}

	// Totals
	totals := fmt.Sprintf("Sub Total: %.2f", expense.SubTotal)
	if expense.WithGST {
		totals += fmt.Sprintf("\nTax (%.0f%%): %.2f", expense.TaxRate, expense.TaxTotal)
	}
	totals += fmt.Sprintf("\n----------------")
	totals += fmt.Sprintf("\nTOTAL: %.2f", expense.Amount)

	// Tax Details (3-inch only)
	taxDetails := ""
	if wide {
		if expense.Notes != "" {
			taxDetails = fmt.Sprintf("Notes: %s", expense.Notes)
		}
	}

	// Footer
	footer := "EXPENSE RECEIPT"

	// Terms
	terms := "Thank you for your business!"

	return ExpenseTemplateData{
		Header:       header,
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
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}
