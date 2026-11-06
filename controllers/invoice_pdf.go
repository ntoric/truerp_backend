package controllers

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"strings"
	"truerp/models"
	"truerp/utils"

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
	business := resolveSellerBusiness(userID, nil)
	if business.ID != uuid.Nil {
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

func logoImgStyle(aspect string) string {
	switch strings.ToLower(strings.TrimSpace(aspect)) {
	case "landscape":
		return "max-height:70px;max-width:120px;width:auto;height:auto;object-fit:contain;"
	case "portrait":
		return "max-height:110px;max-width:70px;width:auto;height:auto;object-fit:contain;"
	default:
		return "max-height:80px;max-width:90px;width:auto;height:auto;object-fit:contain;"
	}
}

func InvoicePDFHTML(invoice models.Invoice) string {
	return InvoicePDFHTMLWithOptions(invoice, buildInvoicePDFOptions(invoice.UserID, invoice))
}

// InvoicePDFHTMLWithOptions renders the invoice document in the myBillBook
// bill-of-supply layout: document-type + "ORIGINAL FOR RECIPIENT" header,
// seller block with logo/GSTIN/PAN, grey invoice-number band, BILL TO /
// SHIP TO columns, a borderless items table with a SUBTOTAL rule row, then
// bank details + payment QR on the left and totals + amount in words +
// authorised signatory on the right.
func InvoicePDFHTMLWithOptions(invoice models.Invoice, opts invoicePDFOptions) string {
	settings := opts.Settings
	printSettings := opts.PrintSettings
	custom := utils.ParseInvoiceTemplateCustomization(settings.Customization)
	biz := opts.Business

	pageRule, fontSize, bodyPadding := documentPageCSS(printSettings)
	docTitle := invoiceDocumentTitle(invoice.InvoiceType)
	isTaxInvoice := docTitle == "TAX INVOICE"

	// --- document head: doc type + ORIGINAL FOR RECIPIENT -------------------
	headRight := ""
	if printSettings.PrintHeader && settings.ShowLogo && biz != nil && biz.LogoURL != "" {
		headRight = fmt.Sprintf(`<img src="%s" alt="Logo" style="%s" />`,
			html.EscapeString(biz.LogoURL), logoImgStyle(biz.LogoAspectRatio))
	}
	docHead := fmt.Sprintf(`<div class="doc-head">
		<div><span class="doc-title">%s</span><span class="orig-box">ORIGINAL FOR RECIPIENT</span></div>
		<div>%s</div>
	</div>`, docTitle, headRight)

	compositionHTML := ""
	if docTitle == "BILL OF SUPPLY" {
		compositionHTML = fmt.Sprintf(`<div class="comp-note">%s</div>`, compositionDeclaration)
	}

	// --- seller (supplier) block -------------------------------------------
	sellerHTML := ""
	if biz != nil {
		var lines []string
		lines = append(lines, fmt.Sprintf(`<div class="biz-name">%s</div>`, html.EscapeString(biz.Name)))
		addrParts := []string{}
		if strings.TrimSpace(biz.Address) != "" {
			addrParts = append(addrParts, strings.TrimSpace(biz.Address))
		}
		if loc := strings.Trim(strings.TrimSpace(fmt.Sprintf("%s, %s, %s", biz.City, biz.State, biz.Pincode)), ", "); loc != "" {
			addrParts = append(addrParts, loc)
		}
		if len(addrParts) > 0 {
			lines = append(lines, "<div>"+html.EscapeString(strings.Join(addrParts, ", "))+"</div>")
		}
		var idRow []string
		if biz.GSTIN != "" {
			idRow = append(idRow, "GSTIN: "+biz.GSTIN)
		}
		if pan := sellerPAN(biz); pan != "" {
			idRow = append(idRow, "PAN Number: "+pan)
		}
		if len(idRow) > 0 {
			lines = append(lines, "<div>"+html.EscapeString(strings.Join(idRow, "    "))+"</div>")
		}
		if biz.Email != "" {
			lines = append(lines, "<div>Email: "+html.EscapeString(biz.Email)+"</div>")
		}
		if biz.Phone != "" {
			lines = append(lines, "<div>MOBILE : "+html.EscapeString(biz.Phone)+"</div>")
		}
		sellerHTML = fmt.Sprintf(`<div class="seller"><div class="seller-logo">%s</div><div class="seller-info">%s</div></div>`,
			logoImgTag(biz, printSettings.PrintHeader && settings.ShowLogo), strings.Join(lines, ""))
	}

	// --- invoice number / date band -----------------------------------------
	var bandCells []string
	if custom.InvoiceDetails.ShowInvoiceNumber {
		bandCells = append(bandCells, fmt.Sprintf(`<span>Invoice No.: %s</span>`, html.EscapeString(invoice.InvoiceNumber)))
	}
	if custom.InvoiceDetails.ShowInvoiceDate {
		bandCells = append(bandCells, fmt.Sprintf(`<span>Invoice Date: %s</span>`, invoice.Date.Format("02/01/2006")))
	}
	bandHTML := ""
	if len(bandCells) > 0 {
		bandHTML = fmt.Sprintf(`<div class="inv-band">%s</div>`, strings.Join(bandCells, ""))
	}

	// --- BILL TO / SHIP TO --------------------------------------------------
	partyHTML := renderPartyColumns(invoice, custom, isTaxInvoice)

	// --- items table --------------------------------------------------------
	itemsTable := renderInvoiceItemsTable(invoice, custom)

	// --- bottom: bank + QR + terms (left) | totals + words + sign (right) ---
	bottomHTML := renderInvoiceBottom(invoice, custom, opts, settings)

	customFieldsHTML := renderCustomFieldsOnPDF(invoice.CustomFields, opts.FieldLabels)

	footerHTML := ""
	if printSettings.PrintFooter {
		footerHTML = `<div class="print-footer">Invoice created using TruERP</div>`
	}

	return fmt.Sprintf(`
<!DOCTYPE html>
<html>
<head>
	<meta charset="UTF-8">
	<title>Invoice - %s</title>
	<style>
		%s
		* { box-sizing: border-box; }
		body { font-family: Arial, Helvetica, sans-serif; margin: 0; padding: %s; color: #111; background: #fff; font-size: %dpx; }
		.doc-head { display: flex; justify-content: space-between; align-items: center; margin-bottom: 18px; }
		.doc-title { font-size: 15px; font-weight: bold; color: #111; }
		.orig-box { border: 1.5px solid #9aa0a6; color: #555; font-size: 10px; font-weight: bold; padding: 3px 8px; margin-left: 10px; border-radius: 2px; letter-spacing: 0.04em; }
		.comp-note { font-size: 14px; font-weight: bold; color: #111; text-align: center; margin: -6px 0 14px; }
		.seller { display: flex; gap: 18px; align-items: flex-start; margin-bottom: 20px; }
		.seller-logo { min-width: 0; }
		.biz-name { font-size: 21px; font-weight: bold; color: #111; margin-bottom: 5px; }
		.seller-info div { font-size: 12px; color: #222; margin: 2px 0; }
		.inv-band { background: #ececec; border-top: 3px solid #1a1a1a; padding: 9px 12px; display: flex; justify-content: space-between; font-weight: bold; font-size: 13px; margin-bottom: 16px; }
		.party-cols { display: grid; grid-template-columns: 1fr 1fr; gap: 28px; margin-bottom: 6px; }
		.party-label { font-weight: bold; font-size: 13px; }
		.party-name { font-weight: bold; font-size: 14px; margin: 4px 0; }
		.party-line { font-size: 12px; color: #333; margin: 2px 0; }
		.party-sub { font-size: 12px; color: #333; margin-top: 10px; }
		.meta-row { display: flex; gap: 28px; font-size: 11px; color: #333; margin: 10px 0 16px; flex-wrap: wrap; }
		table.items { width: 100%%; border-collapse: collapse; margin-top: 10px; }
		.items th { border-top: 2.5px solid #222; border-bottom: 2.5px solid #222; padding: 9px 6px; font-size: 11.5px; text-align: right; letter-spacing: 0.03em; }
		.items th.l { text-align: left; }
		.items td { padding: 9px 6px; border-bottom: 1px solid #e5e7eb; font-size: 12.5px; text-align: right; vertical-align: top; }
		.items td.l { text-align: left; }
		.items tfoot td { border-top: 2.5px solid #222; border-bottom: 2.5px solid #222; border-left: none; border-right: none; font-weight: bold; padding: 9px 6px; font-size: 13px; }
		.bottom-cols { display: grid; grid-template-columns: 1fr 1fr; gap: 28px; margin-top: 20px; }
		.sec-label { font-weight: bold; font-size: 12.5px; margin-bottom: 8px; letter-spacing: 0.02em; }
		.kv-row { display: flex; font-size: 12px; color: #222; margin: 3px 0; }
		.kv-row .k { width: 110px; flex: none; color: #444; }
		.qr-img { width: 110px; height: 110px; margin-top: 6px; }
		.tot-row { display: flex; justify-content: flex-end; gap: 36px; font-size: 13px; margin: 4px 0; }
		.tot-row .v { min-width: 95px; text-align: right; }
		.tot-row.grand { font-weight: bold; font-size: 14px; }
		.words-label { font-weight: bold; font-size: 13px; margin-top: 16px; text-align: right; }
		.words { font-size: 12px; text-align: right; margin-top: 3px; }
		.sig { text-align: right; margin-top: 26px; }
		.sig img { max-height: 70px; max-width: 180px; object-fit: contain; }
		.sig-label { font-size: 11px; font-weight: bold; margin-top: 5px; letter-spacing: 0.03em; }
		.terms-block { margin-top: 18px; font-size: 11px; color: #333; }
		.terms-block .sec-label { margin-bottom: 4px; }
		.cf-block { margin-top: 16px; font-size: 12px; }
		.print-footer { margin-top: 30px; padding-top: 10px; border-top: 1px solid #ddd; font-size: 11px; color: #888; text-align: center; }
		@media print { body { margin: 0; padding: 0; } }
	</style>
</head>
<body>
	%s
	%s
	%s
	%s
	%s
	%s
	%s
	%s
	%s
	<script>window.onload=function(){window.print();};</script>
</body>
</html>`,
		html.EscapeString(invoice.InvoiceNumber),
		pageRule, bodyPadding, fontSize,
		docHead,
		compositionHTML,
		sellerHTML,
		bandHTML,
		partyHTML,
		itemsTable,
		bottomHTML,
		customFieldsHTML,
		footerHTML,
	)
}

func logoImgTag(biz *models.Business, show bool) string {
	if !show || biz == nil || biz.LogoURL == "" {
		return ""
	}
	return fmt.Sprintf(`<img src="%s" alt="Logo" style="%s" />`,
		html.EscapeString(biz.LogoURL), logoImgStyle(biz.LogoAspectRatio))
}

// renderPartyColumns prints BILL TO / SHIP TO side by side with the buyer's
// name, billing address, GSTIN/UIN, state + code, and place of supply under
// the bill-to column. For tax invoices the GST meta (FY, reverse charge,
// original invoice reference) follows beneath.
func renderPartyColumns(invoice models.Invoice, custom models.InvoiceTemplateCustomization, isTaxInvoice bool) string {
	var billTo []string
	if custom.PartyDetails.ShowPartyName {
		billTo = append(billTo, fmt.Sprintf(`<div class="party-name">%s</div>`, html.EscapeString(invoice.Party.Name)))
	}
	if custom.PartyDetails.ShowPartyAddress {
		if invoice.Party.Address != "" {
			billTo = append(billTo, fmt.Sprintf(`<div class="party-line">%s</div>`, html.EscapeString(invoice.Party.Address)))
		}
		if loc := strings.Trim(strings.TrimSpace(fmt.Sprintf("%s, %s - %s", invoice.Party.City, invoice.Party.State, invoice.Party.Pincode)), ", -"); loc != "" {
			billTo = append(billTo, fmt.Sprintf(`<div class="party-line">%s</div>`, html.EscapeString(loc)))
		}
		if st := stateNameAndCode(invoice.Party.State, partyStateCode(invoice.Party)); st != "" {
			billTo = append(billTo, fmt.Sprintf(`<div class="party-line">State: %s</div>`, html.EscapeString(st)))
		}
	}
	if custom.PartyDetails.ShowPartyGSTIN && invoice.Party.GSTIN != "" {
		billTo = append(billTo, fmt.Sprintf(`<div class="party-line">GSTIN/UIN: %s</div>`, html.EscapeString(invoice.Party.GSTIN)))
	}
	if custom.PartyDetails.ShowPartyPhone && custom.ThemeSettings.ShowPhoneOnInvoice && invoice.Party.Phone != "" {
		billTo = append(billTo, fmt.Sprintf(`<div class="party-line">Phone: %s</div>`, html.EscapeString(invoice.Party.Phone)))
	}
	if custom.InvoiceDetails.ShowPlaceOfSupply {
		if pos := placeOfSupplyText(invoice); pos != "" {
			billTo = append(billTo, fmt.Sprintf(`<div class="party-sub">Place of Supply: %s</div>`, html.EscapeString(pos)))
		}
	}

	// Ship To falls back to the billing party name when no separate shipping
	// address was recorded (myBillBook behaviour).
	shipContent := ""
	if custom.PartyDetails.ShowShippingAddress && strings.TrimSpace(invoice.ShippingAddress) != "" {
		shipContent = fmt.Sprintf(`<div class="party-line">%s</div>`, html.EscapeString(invoice.ShippingAddress))
		if invoice.Party.Name != "" {
			shipContent += fmt.Sprintf(`<div class="party-line">%s</div>`, html.EscapeString(invoice.Party.Name))
		}
	} else if invoice.Party.Name != "" {
		shipContent = fmt.Sprintf(`<div class="party-name">%s</div>`, html.EscapeString(invoice.Party.Name))
	}

	leftCol := fmt.Sprintf(`<div><div class="party-label">BILL TO</div>%s</div>`, strings.Join(billTo, ""))
	rightCol := fmt.Sprintf(`<div><div class="party-label">SHIP TO</div>%s</div>`, shipContent)
	out := fmt.Sprintf(`<div class="party-cols">%s%s</div>`, leftCol, rightCol)

	if isTaxInvoice {
		var meta []string
		meta = append(meta, fmt.Sprintf("<span><strong>Financial Year:</strong> %s</span>", financialYearLabel(invoice.Date)))
		reverseCharge := "No"
		if invoice.ReverseCharge {
			reverseCharge = "Yes"
		}
		meta = append(meta, "<span><strong>Reverse Charge:</strong> "+reverseCharge+"</span>")
		if invoice.OriginalInvoiceNumber != "" {
			ref := html.EscapeString(invoice.OriginalInvoiceNumber)
			if invoice.OriginalInvoiceDate != nil {
				ref += " dated " + invoice.OriginalInvoiceDate.Format("02/01/2006")
			}
			meta = append(meta, "<span><strong>Original Invoice:</strong> "+ref+"</span>")
		}
		if custom.InvoiceDetails.ShowDueDate && invoice.DueDate != nil {
			meta = append(meta, fmt.Sprintf("<span><strong>Due Date:</strong> %s</span>", invoice.DueDate.Format("02/01/2006")))
		}
		out += fmt.Sprintf(`<div class="meta-row">%s</div>`, strings.Join(meta, ""))
	}
	return out
}

// renderInvoiceItemsTable renders the borderless items table. Tax-free
// (bill-of-supply) invoices use the compact ITEMS/QTY/MRP/RATE/AMOUNT layout;
// taxable invoices expand into the full GST column set.
func renderInvoiceItemsTable(invoice models.Invoice, custom models.InvoiceTemplateCustomization) string {
	cols := custom.ItemColumns
	hasGST := invoiceHasGST(invoice)
	hasCess := invoiceHasCess(invoice)

	type col struct {
		label string
		left  bool // left-aligned; numeric columns are right-aligned
	}
	var headers []col
	if cols.Items {
		headers = append(headers, col{"ITEMS", true})
	}
	if hasGST {
		if cols.HSN {
			headers = append(headers, col{"HSN/SAC", true})
		}
	} else if cols.HSN && anyItemHasHSN(invoice.Items) {
		headers = append(headers, col{"HSN/SAC", true})
	}
	if cols.Batch && anyItemHasBatch(invoice.Items) {
		headers = append(headers, col{"BATCH", true})
	}
	if cols.Qty {
		headers = append(headers, col{"QTY.", false})
	}
	if cols.MRP || !hasGST {
		headers = append(headers, col{"MRP", false})
	}
	if cols.Rate {
		headers = append(headers, col{"RATE", false})
	}
	if hasGST {
		if cols.Gross {
			headers = append(headers, col{"GROSS", false})
		}
		if cols.Disc {
			headers = append(headers, col{"DISC%", false})
		}
		if cols.Taxable {
			headers = append(headers, col{"TAXABLE", false})
		}
		if cols.Tax {
			headers = append(headers, col{"GST%", false})
		}
		if invoice.IsInterState {
			headers = append(headers, col{"IGST", false})
		} else {
			headers = append(headers, col{"CGST", false}, col{"SGST", false})
		}
		if hasCess {
			headers = append(headers, col{"CESS", false})
		}
	}
	if cols.Amount {
		headers = append(headers, col{"AMOUNT", false})
	}
	if len(headers) == 0 {
		return "<table class=\"items\"><tbody></tbody></table>"
	}

	var head strings.Builder
	for _, h := range headers {
		cls := ""
		if h.left {
			cls = ` class="l"`
		}
		head.WriteString(fmt.Sprintf("<th%s>%s</th>", cls, h.label))
	}

	var body strings.Builder
	for _, item := range invoice.Items {
		var cells strings.Builder
		write := func(content string, left bool) {
			cls := ""
			if left {
				cls = ` class="l"`
			}
			cells.WriteString(fmt.Sprintf("<td%s>%s</td>", cls, content))
		}
		if cols.Items {
			write(html.EscapeString(item.Description), true)
		}
		if (hasGST && cols.HSN) || (!hasGST && cols.HSN && anyItemHasHSN(invoice.Items)) {
			hsn := item.HSNCode
			if hsn == "" {
				hsn = item.SACCode
			}
			write(html.EscapeString(hsn), true)
		}
		if cols.Batch && anyItemHasBatch(invoice.Items) {
			batchLabel := html.EscapeString(item.BatchNo)
			if item.ExpDate != nil {
				batchLabel += fmt.Sprintf(`<br/><span style="font-size:10px;color:#666">Exp %s</span>`, item.ExpDate.Format("02/01/2006"))
			}
			if batchLabel == "" {
				batchLabel = "-"
			}
			write(batchLabel, true)
		}
		if cols.Qty {
			write(fmt.Sprintf("%s %s", formatINR(item.Quantity), html.EscapeString(item.Unit)), false)
		}
		if cols.MRP || !hasGST {
			mrp := itemMRP(item)
			mrpStr := "-"
			if mrp > 0 {
				mrpStr = formatINR(mrp)
			}
			write(mrpStr, false)
		}
		if cols.Rate {
			write(formatINR(item.UnitPrice), false)
		}
		if hasGST {
			if cols.Gross {
				write(formatINR(itemGrossAmount(item)), false)
			}
			if cols.Disc {
				write(formatINR(item.Discount), false)
			}
			if cols.Taxable {
				write(formatINR(itemTaxableValue(item)), false)
			}
			if cols.Tax {
				write(formatINR(item.TaxRate), false)
			}
			if invoice.IsInterState {
				write(formatINR(item.IGST), false)
			} else {
				write(formatINR(item.CGST), false)
				write(formatINR(item.SGST), false)
			}
			if hasCess {
				write(formatINR(item.CessAmount), false)
			}
		}
		if cols.Amount {
			write(formatINR(item.Total), false)
		}
		body.WriteString("<tr>" + cells.String() + "</tr>")
	}

	// SUBTOTAL row: label in the first column, total qty under QTY, total
	// amount under AMOUNT.
	foot := make([]string, len(headers))
	for i := range foot {
		foot[i] = ""
	}
	foot[0] = "SUBTOTAL"
	for i, h := range headers {
		switch h.label {
		case "QTY.":
			foot[i] = invoiceQtyTotal(invoice)
		case "AMOUNT":
			foot[i] = "₹ " + formatINR(invoice.SubTotal)
		}
	}
	var footHTML strings.Builder
	for i, v := range foot {
		cls := ""
		if headers[i].left {
			cls = ` class="l"`
		}
		footHTML.WriteString(fmt.Sprintf("<td%s>%s</td>", cls, v))
	}

	return fmt.Sprintf(`<table class="items"><thead><tr>%s</tr></thead><tbody>%s</tbody><tfoot><tr>%s</tr></tfoot></table>`,
		head.String(), body.String(), footHTML.String())
}

func anyItemHasHSN(items []models.InvoiceItem) bool {
	for _, it := range items {
		if it.HSNCode != "" || it.SACCode != "" {
			return true
		}
	}
	return false
}

func anyItemHasBatch(items []models.InvoiceItem) bool {
	for _, it := range items {
		if it.BatchNo != "" {
			return true
		}
	}
	return false
}

// renderInvoiceBottom renders the bottom two-column block: bank details,
// payment QR and terms on the left; totals, amount in words and the
// authorised signatory caption on the right.
func renderInvoiceBottom(invoice models.Invoice, custom models.InvoiceTemplateCustomization, opts invoicePDFOptions, settings models.InvoiceSettings) string {
	biz := opts.Business

	var left strings.Builder
	if settings.ShowBankDetails && custom.Miscellaneous.ShowBankDetails && biz != nil &&
		(biz.BankName != "" || biz.AccountNumber != "" || biz.IFSCCode != "") {
		left.WriteString(`<div><div class="sec-label">BANK DETAILS</div>`)
		kv := func(k, v string) {
			if v != "" {
				left.WriteString(fmt.Sprintf(`<div class="kv-row"><span class="k">%s:</span><span>%s</span></div>`, k, html.EscapeString(v)))
			}
		}
		kv("Name", biz.Name)
		kv("IFSC Code", biz.IFSCCode)
		kv("Account No", biz.AccountNumber)
		kv("Bank", biz.BankName)
		left.WriteString(`</div>`)
	}
	if custom.Miscellaneous.ShowQRCode && biz != nil && strings.TrimSpace(biz.UPIID) != "" {
		left.WriteString(`<div style="margin-top:16px"><div class="sec-label">PAYMENT QR CODE</div>`)
		left.WriteString(fmt.Sprintf(`<div class="kv-row"><span class="k">UPI ID:</span><span>%s</span></div>`, html.EscapeString(biz.UPIID)))
		if png := upiQRPNG(biz.UPIID, biz.Name, invoice.TotalAmount); png != nil {
			left.WriteString(fmt.Sprintf(`<img class="qr-img" src="data:image/png;base64,%s" alt="UPI QR"/>`, base64.StdEncoding.EncodeToString(png)))
		}
		left.WriteString(`</div>`)
	}
	termsBlock := html.EscapeString(invoice.Terms)
	if !settings.ShowTerms || !custom.InvoiceDetails.ShowTermsAndConditions {
		termsBlock = ""
	}
	if termsBlock == "" && settings.DefaultTerms != "" && custom.InvoiceDetails.ShowTermsAndConditions {
		termsBlock = html.EscapeString(settings.DefaultTerms)
	}
	if termsBlock != "" {
		left.WriteString(fmt.Sprintf(`<div class="terms-block"><div class="sec-label">TERMS AND CONDITIONS</div><div>%s</div></div>`, termsBlock))
	}
	if label := formatPaymentSplitsLabel(invoice.PaymentSplits, invoice.PaymentMode); label != "" {
		left.WriteString(fmt.Sprintf(`<div class="kv-row" style="margin-top:10px"><span class="k">Payment:</span><span>%s</span></div>`, html.EscapeString(label)))
	}

	var right strings.Builder
	hasGST := invoiceHasGST(invoice)
	if hasGST {
		right.WriteString(totRow("Total Taxable Value", "₹ "+formatINR(invoiceTaxableTotal(invoice)), false))
		if invoice.IsInterState {
			right.WriteString(totRow("IGST", "₹ "+formatINR(invoice.IGSTTotal), false))
		} else {
			right.WriteString(totRow("CGST", "₹ "+formatINR(invoice.CGSTTotal), false))
			right.WriteString(totRow("SGST", "₹ "+formatINR(invoice.SGSTTotal), false))
		}
	}
	if invoiceHasCess(invoice) {
		right.WriteString(totRow("Cess", "₹ "+formatINR(invoice.CessTotal), false))
	}
	if disc := invoice.DiscountTotal + invoice.InvoiceDiscount; disc > 0 {
		right.WriteString(totRow("Discount", "-₹ "+formatINR(disc), false))
	}
	for _, charge := range additionalChargeRows(invoice.AdditionalChargeItems, invoice.AdditionalCharges) {
		right.WriteString(totRow(charge.Label, "₹ "+formatINR(charge.Amount), false))
	}
	if invoice.RoundOff != 0 {
		right.WriteString(totRow("Round Off", "₹ "+formatINR(invoice.RoundOff), false))
	}
	right.WriteString(totRow("Total Amount", "₹ "+formatINR(invoice.TotalAmount), true))
	if custom.InvoiceDetails.ShowReceivedAmount && invoice.AmountPaid > 0 {
		right.WriteString(totRow("Received Amount", "₹ "+formatINR(invoice.AmountPaid), false))
	}
	if custom.InvoiceDetails.ShowBalanceDue {
		right.WriteString(totRow("Balance", "₹ "+formatINR(invoice.TotalAmount-invoice.AmountPaid), false))
	}
	if custom.InvoiceDetails.ShowAmountInWords {
		right.WriteString(`<div class="words-label">Total Amount (in words)</div>`)
		right.WriteString(`<div class="words">` + html.EscapeString(amountWordsRupees(invoice.TotalAmount)) + `</div>`)
	}

	// Authorised signatory: invoice signature image, else business signature.
	sig := strings.TrimSpace(invoice.Signature)
	if sig == "" && custom.Miscellaneous.ShowSignature && biz != nil {
		sig = strings.TrimSpace(biz.SignatureURL)
	}
	if sig != "" {
		bizName := ""
		if biz != nil {
			bizName = biz.Name
		}
		right.WriteString(fmt.Sprintf(`<div class="sig"><img src="%s" alt="Signature"/><div class="sig-label">AUTHORISED SIGNATORY FOR %s</div></div>`,
			html.EscapeString(sig), html.EscapeString(strings.ToUpper(bizName))))
	}

	return fmt.Sprintf(`<div class="bottom-cols"><div>%s</div><div>%s</div></div>`, left.String(), right.String())
}

func totRow(label, value string, grand bool) string {
	cls := "tot-row"
	if grand {
		cls += " grand"
	}
	return fmt.Sprintf(`<div class="%s"><span>%s</span><span class="v">%s</span></div>`, cls, html.EscapeString(label), html.EscapeString(value))
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
	return fmt.Sprintf(`<div class="cf-block"><div class="sec-label">ADDITIONAL INFORMATION</div>%s</div>`, rows)
}
