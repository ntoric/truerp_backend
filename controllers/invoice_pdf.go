package controllers

import (
	"truerp/models"
	"truerp/utils"
	"encoding/json"
	"fmt"
	"html"
	"strings"

	"github.com/google/uuid"
)

type invoicePDFOptions struct {
	Settings      models.InvoiceSettings
	PrintSettings models.PrintSettings
	Business      *models.Business
	FieldLabels   map[string]string
}

func buildInvoicePDFOptions(userID uuid.UUID, invoice models.Invoice) invoicePDFOptions {
	opts := invoicePDFOptions{
		Settings:      loadInvoiceSettings(userID),
		PrintSettings: loadPrintSettings(userID),
		FieldLabels:   map[string]string{},
	}
	var business models.Business
	if err := utils.DB.Where("user_id = ?", userID).First(&business).Error; err == nil {
		opts.Business = &business
	}
	var defs []models.InvoiceCustomFieldDefinition
	utils.DB.Where("user_id = ? AND show_on_pdf = ?", userID, true).Find(&defs)
	for _, d := range defs {
		opts.FieldLabels[d.FieldKey] = d.Label
	}
	return opts
}

func documentPageCSS(ps models.PrintSettings) (pageRule string, bodyFontSize int, bodyPadding string) {
	paper := strings.ToLower(ps.PaperSize)
	switch paper {
	case "letter":
		paper = "letter"
	case "legal":
		paper = "legal"
	default:
		paper = "A4"
	}
	orientation := strings.ToLower(ps.Orientation)
	if orientation != "landscape" {
		orientation = "portrait"
	}
	mt, mb, ml, mr := ps.MarginTop, ps.MarginBottom, ps.MarginLeft, ps.MarginRight
	if mt < 0 {
		mt = 0.5
	}
	if mb < 0 {
		mb = 0.5
	}
	if ml < 0 {
		ml = 0.5
	}
	if mr < 0 {
		mr = 0.5
	}
	fontSize := ps.FontSize
	if fontSize <= 0 {
		fontSize = 12
	}
	pageRule = fmt.Sprintf(
		"@page { size: %s %s; margin: %.2fin %.2fin %.2fin %.2fin; }",
		paper, orientation, mt, mr, mb, ml,
	)
	bodyPadding = fmt.Sprintf("%.2fin", 0.15)
	return pageRule, fontSize, bodyPadding
}

func InvoicePDFHTML(invoice models.Invoice) string {
	return InvoicePDFHTMLWithOptions(invoice, buildInvoicePDFOptions(invoice.UserID, invoice))
}

func InvoicePDFHTMLWithOptions(invoice models.Invoice, opts invoicePDFOptions) string {
	settings := opts.Settings
	printSettings := opts.PrintSettings
	custom := utils.ParseInvoiceTemplateCustomization(settings.Customization)
	templateName := settings.Template
	if invoice.PDFTemplate != "" {
		templateName = invoice.PDFTemplate
	}
	if templateName == "" {
		templateName = "classic"
	}
	primary := settings.PrimaryColor
	if primary == "" {
		primary = "#2563eb"
	}
	secondary := settings.SecondaryColor
	if secondary == "" {
		secondary = "#1e40af"
	}
	bgColor := "#ffffff"
	textColor := "#333333"
	if settings.Theme == "dark" {
		bgColor = "#111827"
		textColor = "#f3f4f6"
	}

	pageRule, fontSize, bodyPadding := documentPageCSS(printSettings)

	headerStyle := "display:flex;justify-content:space-between;margin-bottom:30px;"
	titleStyle := fmt.Sprintf("font-size:24px;font-weight:bold;color:%s;", primary)
	if templateName == "modern" {
		headerStyle = fmt.Sprintf("display:flex;justify-content:space-between;margin-bottom:30px;padding:16px;border-radius:8px;background:linear-gradient(135deg,%s 0%%,%s 100%%);color:#fff;", primary, secondary)
		titleStyle = "font-size:24px;font-weight:bold;color:#fff;"
	}
	if templateName == "minimal" {
		titleStyle = fmt.Sprintf("font-size:20px;font-weight:600;color:%s;letter-spacing:0.05em;text-transform:uppercase;", textColor)
	}
	if templateName == "stylish" || templateName == "luxury" || templateName == "advanced_gst" {
		headerStyle = fmt.Sprintf("display:flex;justify-content:space-between;margin-bottom:30px;padding-bottom:12px;border-bottom:4px solid %s;", primary)
	}

	logoHTML := ""
	if printSettings.PrintHeader && settings.ShowLogo && opts.Business != nil && opts.Business.LogoURL != "" {
		logoHTML = fmt.Sprintf(`<img src="%s" alt="Logo" style="max-height:60px;margin-bottom:8px;" />`, html.EscapeString(opts.Business.LogoURL))
	}

	showBank := settings.ShowBankDetails && custom.Miscellaneous.ShowBankDetails
	bankHTML := ""
	if showBank && opts.Business != nil && opts.Business.BankName != "" {
		bankHTML = fmt.Sprintf(`<div class="section" style="margin-top:16px;">
			<div class="section-title">Bank Details</div>
			<div>%s · A/C %s · IFSC %s</div>
		</div>`, html.EscapeString(opts.Business.BankName), html.EscapeString(opts.Business.AccountNumber), html.EscapeString(opts.Business.IFSCCode))
	}

	customFieldsHTML := renderCustomFieldsOnPDF(invoice.CustomFields, opts.FieldLabels)
	termsBlock := html.EscapeString(invoice.Terms)
	if !settings.ShowTerms || !custom.InvoiceDetails.ShowTermsAndConditions {
		termsBlock = ""
	}
	if termsBlock == "" && settings.DefaultTerms != "" && custom.InvoiceDetails.ShowTermsAndConditions {
		termsBlock = html.EscapeString(settings.DefaultTerms)
	}
	termsHTML := ""
	if termsBlock != "" {
		termsHTML = fmt.Sprintf(`<div class="section"><div class="section-title">Terms &amp; Conditions</div><div>%s</div></div>`, termsBlock)
	}

	thStyle := fmt.Sprintf("background-color:%s;font-weight:bold;", primary)
	if templateName == "minimal" {
		thStyle = "border-bottom:2px solid #111;font-weight:600;background:transparent;"
	}

	invoiceNumberLine := html.EscapeString(invoice.InvoiceNumber)
	if !custom.InvoiceDetails.ShowInvoiceNumber {
		invoiceNumberLine = ""
	}

	billToHTML := renderBillToSection(invoice, custom)
	invoiceMetaHTML := renderInvoiceMetaSection(invoice, custom)
	itemTableHTML := renderInvoiceItemTable(invoice.Items, custom)
	balanceHTML := renderPartyBalanceTotals(invoice, custom)

	headerBlock := ""
	if printSettings.PrintHeader {
		headerBlock = fmt.Sprintf(`
	<div class="header">
		<div>
			%s
			<div class="title">TAX INVOICE</div>
			<div class="invoice-number">%s</div>
		</div>
		<div><span class="status status-%s">%s</span></div>
	</div>`, logoHTML, invoiceNumberLine, html.EscapeString(invoice.Status), strings.ToUpper(invoice.Status))
	} else {
		headerBlock = fmt.Sprintf(`
	<div class="header" style="margin-bottom:16px;">
		<div>
			<div class="title">TAX INVOICE</div>
			<div class="invoice-number">%s</div>
		</div>
		<div><span class="status status-%s">%s</span></div>
	</div>`, invoiceNumberLine, html.EscapeString(invoice.Status), strings.ToUpper(invoice.Status))
	}

	footerHTML := ""
	if printSettings.PrintFooter {
		bizName := ""
		if opts.Business != nil {
			bizName = opts.Business.Name
		}
		footerHTML = fmt.Sprintf(`<div class="print-footer">%s · Generated by TruERP</div>`, html.EscapeString(bizName))
	}

	return fmt.Sprintf(`
<!DOCTYPE html>
<html>
<head>
	<meta charset="UTF-8">
	<title>Invoice - %s</title>
	<style>
		%s
		body { font-family: Arial, sans-serif; margin: 0; padding: %s; color: %s; background: %s; font-size: %dpx; }
		.header { %s }
		.title { %s }
		.invoice-number { font-size: 18px; opacity: 0.85; }
		.section { margin-bottom: 20px; }
		.section-title { font-size: 14px; font-weight: bold; color: %s; margin-bottom: 10px; }
		.info-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
		table { width: 100%%; border-collapse: collapse; margin-top: 10px; }
		th, td { border: 1px solid #ddd; padding: 10px; text-align: left; }
		th { %s color: %s; }
		.totals { margin-top: 20px; text-align: right; }
		.total-row { display: flex; justify-content: flex-end; margin-bottom: 5px; }
		.total-label { width: 150px; font-weight: bold; }
		.total-value { width: 100px; }
		.grand-total { font-size: 18px; font-weight: bold; color: %s; }
		.status { display: inline-block; padding: 5px 10px; border-radius: 4px; font-size: 12px; font-weight: bold; text-transform: uppercase; }
		.status-draft { background-color: #f3f4f6; color: #666; }
		.status-sent { background-color: #dbeafe; color: #1e40af; }
		.status-paid { background-color: #d1fae5; color: #065f46; }
		.status-partial { background-color: #fef3c7; color: #92400e; }
		.status-overdue { background-color: #fee2e2; color: #991b1b; }
		.status-cancelled { background-color: #f3f4f6; color: #666; }
		.print-footer { margin-top: 28px; padding-top: 10px; border-top: 1px solid #ddd; font-size: 11px; color: #666; text-align: center; }
		@media print { body { margin: 0; padding: 0; } .print-footer { position: running(footer); } }
	</style>
</head>
<body>
	%s
	%s
	%s
	%s
	<div class="section">
		<div class="section-title">Items</div>
		%s
	</div>
	<div class="totals">
		<div class="total-row"><span class="total-label">Sub Total:</span><span class="total-value">₹%.2f</span></div>
		<div class="total-row"><span class="total-label">Discount:</span><span class="total-value">-₹%.2f</span></div>
		<div class="total-row"><span class="total-label">Tax Total:</span><span class="total-value">₹%.2f</span></div>
		<div class="total-row"><span class="total-label">Additional:</span><span class="total-value">₹%.2f</span></div>
		<div class="total-row"><span class="total-label">Round Off:</span><span class="total-value">₹%.2f</span></div>
		<div class="total-row grand-total"><span class="total-label">Grand Total:</span><span class="total-value">₹%.2f</span></div>
		%s
	</div>
	%s
	%s
	%s
	<script>window.onload=function(){window.print();};</script>
</body>
</html>`,
		html.EscapeString(invoice.InvoiceNumber),
		pageRule, bodyPadding, textColor, bgColor, fontSize,
		headerStyle, titleStyle,
		secondary,
		thStyle, func() string {
			if templateName == "modern" {
				return "#fff"
			}
			return "#fff"
		}(),
		primary,
		headerBlock,
		billToHTML,
		invoiceMetaHTML,
		customFieldsHTML,
		itemTableHTML,
		invoice.SubTotal,
		invoice.DiscountTotal+invoice.InvoiceDiscount,
		invoice.TaxTotal,
		invoice.AdditionalCharges,
		invoice.RoundOff,
		invoice.TotalAmount,
		balanceHTML,
		bankHTML,
		termsHTML,
		footerHTML,
	)
}

func renderBillToSection(invoice models.Invoice, custom models.InvoiceTemplateCustomization) string {
	var lines []string
	if custom.PartyDetails.ShowPartyName {
		lines = append(lines, fmt.Sprintf("<strong>%s</strong>", html.EscapeString(invoice.Party.Name)))
	}
	if custom.PartyDetails.ShowPartyAddress {
		lines = append(lines, html.EscapeString(invoice.Party.Address))
		lines = append(lines, html.EscapeString(fmt.Sprintf("%s, %s - %s", invoice.Party.City, invoice.Party.State, invoice.Party.Pincode)))
	}
	if custom.PartyDetails.ShowPartyPhone && custom.ThemeSettings.ShowPhoneOnInvoice && invoice.Party.Phone != "" {
		lines = append(lines, "Phone: "+html.EscapeString(invoice.Party.Phone))
	}
	if custom.PartyDetails.ShowPartyGSTIN && invoice.Party.GSTIN != "" {
		lines = append(lines, "GSTIN: "+html.EscapeString(invoice.Party.GSTIN))
	}
	if len(lines) == 0 {
		return ""
	}
	return fmt.Sprintf(`<div class="section"><div class="section-title">Bill To</div>%s</div>`, strings.Join(lines, "<br>"))
}

func renderInvoiceMetaSection(invoice models.Invoice, custom models.InvoiceTemplateCustomization) string {
	var cells []string
	if custom.InvoiceDetails.ShowInvoiceDate {
		cells = append(cells, fmt.Sprintf("<div>Invoice Date: %s</div>", invoice.Date.Format("02-01-2006")))
	}
	if custom.InvoiceDetails.ShowDueDate {
		due := "N/A"
		if invoice.DueDate != nil {
			due = invoice.DueDate.Format("02-01-2006")
		}
		cells = append(cells, fmt.Sprintf("<div>Due Date: %s</div>", due))
	}
	if custom.InvoiceDetails.ShowPaymentTerms {
		cells = append(cells, fmt.Sprintf("<div>Payment Terms: %d days</div>", invoice.PaymentTerms))
	}
	if custom.InvoiceDetails.ShowPlaceOfSupply && invoice.PlaceOfSupply != "" {
		cells = append(cells, fmt.Sprintf("<div>Place of Supply: %s</div>", html.EscapeString(invoice.PlaceOfSupply)))
	}
	if len(cells) == 0 {
		return ""
	}
	return fmt.Sprintf(`<div class="section"><div class="section-title">Invoice Details</div><div class="info-grid">%s</div></div>`, strings.Join(cells, ""))
}

func renderInvoiceItemTable(items []models.InvoiceItem, custom models.InvoiceTemplateCustomization) string {
	cols := custom.ItemColumns
	var headers []string
	if cols.Items {
		headers = append(headers, "<th>Description</th>")
	}
	if cols.HSN {
		headers = append(headers, "<th>HSN</th>")
	}
	if cols.Batch {
		headers = append(headers, "<th>Batch</th>")
	}
	if cols.Qty {
		headers = append(headers, "<th>Qty</th>")
	}
	if cols.Rate {
		headers = append(headers, "<th>Rate</th>")
	}
	if cols.Disc {
		headers = append(headers, "<th>Disc%</th>")
	}
	if cols.Tax {
		headers = append(headers, "<th>Tax%</th>")
	}
	if cols.Amount {
		headers = append(headers, "<th>Total</th>")
	}
	if len(headers) == 0 {
		return "<table><tbody></tbody></table>"
	}
	return fmt.Sprintf("<table><thead><tr>%s</tr></thead><tbody>%s</tbody></table>",
		strings.Join(headers, ""), renderInvoiceItemRows(items, custom))
}

func renderPartyBalanceTotals(invoice models.Invoice, custom models.InvoiceTemplateCustomization) string {
	var rows string
	balanceDue := invoice.TotalAmount - invoice.AmountPaid
	if custom.InvoiceDetails.ShowReceivedAmount {
		rows += fmt.Sprintf(`<div class="total-row"><span class="total-label">Received:</span><span class="total-value">₹%.2f</span></div>`, invoice.AmountPaid)
	}
	if custom.InvoiceDetails.ShowBalanceDue {
		rows += fmt.Sprintf(`<div class="total-row"><span class="total-label">Balance Due:</span><span class="total-value">₹%.2f</span></div>`, balanceDue)
	}
	if custom.ThemeSettings.ShowPartyBalance {
		rows += `<div class="total-row"><span class="total-label">Previous Balance:</span><span class="total-value">₹0.00</span></div>`
		rows += fmt.Sprintf(`<div class="total-row"><span class="total-label">Current Balance:</span><span class="total-value">₹%.2f</span></div>`, balanceDue)
	}
	return rows
}

func renderInvoiceItemRows(items []models.InvoiceItem, custom models.InvoiceTemplateCustomization) string {
	cols := custom.ItemColumns
	var rows string
	for _, item := range items {
		var cells []string
		desc := html.EscapeString(item.Description)
		if cols.Items {
			cells = append(cells, fmt.Sprintf("<td>%s</td>", desc))
		}
		if cols.HSN {
			cells = append(cells, fmt.Sprintf("<td>%s</td>", html.EscapeString(item.HSNCode)))
		}
		if cols.Batch {
			batchLabel := html.EscapeString(item.BatchNo)
			if item.ExpDate != nil {
				batchLabel = fmt.Sprintf("%s<br/><span style=\"font-size:10px;color:#666\">Exp %s</span>",
					batchLabel, item.ExpDate.Format("02/01/2006"))
			}
			if batchLabel == "" {
				batchLabel = "-"
			}
			cells = append(cells, fmt.Sprintf("<td>%s</td>", batchLabel))
		}
		if cols.Qty {
			cells = append(cells, fmt.Sprintf("<td>%.2f %s</td>", item.Quantity, html.EscapeString(item.Unit)))
		}
		if cols.Rate {
			cells = append(cells, fmt.Sprintf("<td>₹%.2f</td>", item.UnitPrice))
		}
		if cols.Disc {
			cells = append(cells, fmt.Sprintf("<td>%.2f%%</td>", item.Discount))
		}
		if cols.Tax {
			cells = append(cells, fmt.Sprintf("<td>%.2f%%</td>", item.TaxRate))
		}
		if cols.Amount {
			cells = append(cells, fmt.Sprintf("<td>₹%.2f</td>", item.Total))
		}
		if len(cells) > 0 {
			rows += "<tr>" + strings.Join(cells, "") + "</tr>"
		}
	}
	return rows
}

func renderCustomFieldsOnPDF(raw string, labels map[string]string) string {
	if raw == "" || len(labels) == 0 {
		return ""
	}
	values := map[string]interface{}{}
	if err := json.Unmarshal([]byte(raw), &values); err != nil || len(values) == 0 {
		return ""
	}
	var rows string
	for key, label := range labels {
		val, ok := values[key]
		if !ok || fmt.Sprint(val) == "" {
			continue
		}
		rows += fmt.Sprintf("<div><strong>%s:</strong> %s</div>", html.EscapeString(label), html.EscapeString(fmt.Sprint(val)))
	}
	if rows == "" {
		return ""
	}
	return fmt.Sprintf(`<div class="section"><div class="section-title">Additional Information</div>%s</div>`, rows)
}
