package controllers

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/go-pdf/fpdf"
)

func buildInvoiceDocumentPDF(invoice models.Invoice, business *models.Business, ps models.PrintSettings) ([]byte, error) {
	orientation := "P"
	if strings.EqualFold(ps.Orientation, "landscape") {
		orientation = "L"
	}
	paper := strings.ToUpper(ps.PaperSize)
	switch paper {
	case "LETTER", "LEGAL":
		// ok
	default:
		paper = "A4"
	}

	pdf := fpdf.New(orientation, "mm", paper, "")
	pdf.SetMargins(mmFromInches(ps.MarginLeft), mmFromInches(ps.MarginTop), mmFromInches(ps.MarginRight))
	pdf.SetAutoPageBreak(true, mmFromInches(ps.MarginBottom))
	pdf.AddPage()

	fontSize := float64(ps.FontSize)
	if fontSize <= 0 {
		fontSize = 12
	}

	invSettings := loadInvoiceSettings(invoice.UserID)
	custom := utils.ParseInvoiceTemplateCustomization(invSettings.Customization)

	pageW, _ := pdf.GetPageSize()
	leftM, _, rightM, _ := pdf.GetMargins()
	usable := pageW - leftM - rightM
	rightX := leftM + usable*0.52
	rightW := pageW - rightM - rightX

	docTitle := invoiceDocumentTitle(invoice.InvoiceType)
	isTaxInvoice := docTitle == "TAX INVOICE"

	// Document head: document type + boxed ORIGINAL FOR RECIPIENT, logo right.
	pdf.SetFont("Arial", "B", fontSize+1)
	pdf.SetTextColor(20, 20, 20)
	pdf.CellFormat(pdf.GetStringWidth(docTitle)+4, 7, docTitle, "", 0, "L", false, 0, "")
	pdf.SetFont("Arial", "B", fontSize-3)
	pdf.SetTextColor(90, 90, 90)
	pdf.SetDrawColor(150, 150, 150)
	pdf.SetLineWidth(0.3)
	pdf.CellFormat(pdf.GetStringWidth(" ORIGINAL FOR RECIPIENT ")+2, 5.5, " ORIGINAL FOR RECIPIENT ", "1", 1, "L", false, 0, "")

	if ps.PrintHeader && business != nil && business.LogoURL != "" {
		if data, imgType := fetchThermalLogoBytes(business.LogoURL); len(data) > 0 {
			name := "invoice-logo"
			if info := pdf.RegisterImageOptionsReader(name, fpdf.ImageOptions{ImageType: imgType, ReadDpi: true}, bytes.NewReader(data)); info != nil {
				imgW := 30.0
				imgH := imgW
				if info.Width() > 0 {
					imgH = imgW * info.Height() / info.Width()
				}
				if imgH > 20 {
					imgH = 20
					imgW = imgH * info.Width() / info.Height()
				}
				pdf.ImageOptions(name, pageW-rightM-imgW, mmFromInches(ps.MarginTop), imgW, imgH, false, fpdf.ImageOptions{}, 0, "")
			}
		}
	}
	pdf.Ln(4)

	// Composition dealers must declare this prominently on a bill of supply.
	if docTitle == "BILL OF SUPPLY" {
		pdf.SetFont("Arial", "B", fontSize)
		pdf.SetTextColor(20, 20, 20)
		pdf.MultiCell(0, 6, compositionDeclaration, "", "C", false)
		pdf.Ln(2)
	}

	// Supplier (seller) block.
	if business != nil && business.Name != "" {
		pdf.SetFont("Arial", "B", fontSize+6)
		pdf.SetTextColor(15, 15, 15)
		pdf.MultiCell(0, 8, sanitizePDFText(business.Name), "", "L", false)
		pdf.SetFont("Arial", "", fontSize-1)
		pdf.SetTextColor(45, 45, 45)
		var addrParts []string
		if strings.TrimSpace(business.Address) != "" {
			addrParts = append(addrParts, strings.TrimSpace(business.Address))
		}
		if loc := strings.Trim(strings.TrimSpace(fmt.Sprintf("%s, %s, %s", business.City, business.State, business.Pincode)), ", "); loc != "" {
			addrParts = append(addrParts, loc)
		}
		if len(addrParts) > 0 {
			pdf.MultiCell(0, 4.8, sanitizePDFText(strings.Join(addrParts, ", ")), "", "L", false)
		}
		var idLine []string
		if business.GSTIN != "" {
			idLine = append(idLine, "GSTIN: "+business.GSTIN)
		}
		if pan := sellerPAN(business); pan != "" {
			idLine = append(idLine, "PAN Number: "+pan)
		}
		if len(idLine) > 0 {
			pdf.MultiCell(0, 4.8, sanitizePDFText(strings.Join(idLine, "   ")), "", "L", false)
		}
		if business.Email != "" {
			pdf.MultiCell(0, 4.8, "Email: "+sanitizePDFText(business.Email), "", "L", false)
		}
		if business.Phone != "" {
			pdf.MultiCell(0, 4.8, "MOBILE : "+sanitizePDFText(business.Phone), "", "L", false)
		}
	}
	pdf.Ln(3)

	// Grey band with invoice number and date over a thick rule.
	bandY := pdf.GetY()
	pdf.SetDrawColor(26, 26, 26)
	pdf.SetLineWidth(1.0)
	pdf.Line(leftM, bandY, pageW-rightM, bandY)
	pdf.SetXY(leftM, bandY+0.4)
	pdf.SetFillColor(236, 236, 236)
	pdf.SetFont("Arial", "B", fontSize-1)
	pdf.SetTextColor(15, 15, 15)
	pdf.CellFormat(usable*0.5, 8, "Invoice No.: "+sanitizePDFText(invoice.InvoiceNumber), "", 0, "L", true, 0, "")
	pdf.CellFormat(usable*0.5, 8, "Invoice Date: "+invoice.Date.Format("02/01/2006"), "", 1, "R", true, 0, "")
	pdf.Ln(5)

	// BILL TO / SHIP TO columns.
	colW := usable * 0.5
	billY := pdf.GetY()
	pdf.SetFont("Arial", "B", fontSize-1)
	pdf.SetTextColor(30, 30, 30)
	pdf.CellFormat(colW, 5, "BILL TO", "", 0, "L", false, 0, "")
	pdf.CellFormat(0, 5, "SHIP TO", "", 1, "L", false, 0, "")
	pdf.SetFont("Arial", "B", fontSize-1)
	pdf.SetTextColor(40, 40, 40)
	pdf.SetX(leftM)
	pdf.MultiCell(colW, 5, sanitizePDFText(invoice.Party.Name), "", "L", false)
	pdf.SetFont("Arial", "", fontSize-2)
	pdf.SetTextColor(60, 60, 60)
	if invoice.Party.Address != "" {
		pdf.SetX(leftM)
		pdf.MultiCell(colW, 4.5, sanitizePDFText(invoice.Party.Address), "", "L", false)
	}
	partyLoc := strings.Trim(strings.TrimSpace(fmt.Sprintf("%s, %s - %s", invoice.Party.City, invoice.Party.State, invoice.Party.Pincode)), ", -")
	if partyLoc != "" {
		pdf.SetX(leftM)
		pdf.MultiCell(colW, 4.5, sanitizePDFText(partyLoc), "", "L", false)
	}
	if st := stateNameAndCode(invoice.Party.State, partyStateCode(invoice.Party)); st != "" {
		pdf.SetX(leftM)
		pdf.MultiCell(colW, 4.5, "State: "+sanitizePDFText(st), "", "L", false)
	}
	if invoice.Party.GSTIN != "" {
		pdf.SetX(leftM)
		pdf.MultiCell(colW, 4.5, "GSTIN/UIN: "+sanitizePDFText(invoice.Party.GSTIN), "", "L", false)
	}
	if pos := placeOfSupplyText(invoice); pos != "" {
		pdf.Ln(1.5)
		pdf.SetX(leftM)
		pdf.MultiCell(colW, 4.5, "Place of Supply: "+sanitizePDFText(pos), "", "L", false)
	}
	billBottom := pdf.GetY()

	shipContent := strings.TrimSpace(invoice.ShippingAddress)
	shipIsAddress := shipContent != ""
	if !shipIsAddress {
		shipContent = invoice.Party.Name
	}
	pdf.SetXY(rightX, billY+5)
	pdf.SetFont("Arial", "B", fontSize-1)
	pdf.SetTextColor(40, 40, 40)
	pdf.MultiCell(rightW, 5, sanitizePDFText(shipContent), "", "L", false)
	if shipIsAddress && invoice.Party.Name != "" {
		pdf.SetX(rightX)
		pdf.SetFont("Arial", "", fontSize-2)
		pdf.SetTextColor(60, 60, 60)
		pdf.MultiCell(rightW, 4.5, sanitizePDFText(invoice.Party.Name), "", "L", false)
	}
	if pdf.GetY() > billBottom {
		billBottom = pdf.GetY()
	}
	pdf.SetY(billBottom)
	pdf.Ln(3)

	// GST meta (tax invoices only): FY, reverse charge, original reference.
	if isTaxInvoice {
		pdf.SetFont("Arial", "", fontSize-3)
		pdf.SetTextColor(60, 60, 60)
		meta := financialYearLabel(invoice.Date)
		reverseCharge := "No"
		if invoice.ReverseCharge {
			reverseCharge = "Yes"
		}
		meta += "   |   Reverse Charge: " + reverseCharge
		if invoice.OriginalInvoiceNumber != "" {
			meta += "   |   Original Inv: " + invoice.OriginalInvoiceNumber
			if invoice.OriginalInvoiceDate != nil {
				meta += " dated " + invoice.OriginalInvoiceDate.Format("02/01/2006")
			}
		}
		pdf.CellFormat(0, 4.5, sanitizePDFText(meta), "", 1, "L", false, 0, "")
		pdf.Ln(2)
	}

	// Items table — borderless with thick rules (myBillBook style).
	type pdfCol struct {
		label string
		w     float64
		align string
	}
	cols := custom.ItemColumns
	hasGST := invoiceHasGST(invoice)
	hasCess := invoiceHasCess(invoice)
	colDefs := []pdfCol{{"ITEMS", -1, "L"}}
	if cols.HSN && (hasGST || anyItemHasHSN(invoice.Items)) {
		colDefs = append(colDefs, pdfCol{"HSN/SAC", usable * 0.085, "L"})
	}
	if cols.Qty {
		colDefs = append(colDefs, pdfCol{"QTY.", usable * 0.075, "R"})
	}
	if cols.MRP || !hasGST {
		colDefs = append(colDefs, pdfCol{"MRP", usable * 0.075, "R"})
	}
	if cols.Rate {
		colDefs = append(colDefs, pdfCol{"RATE", usable * 0.08, "R"})
	}
	if hasGST {
		if cols.Gross {
			colDefs = append(colDefs, pdfCol{"GROSS", usable * 0.085, "R"})
		}
		if cols.Disc {
			colDefs = append(colDefs, pdfCol{"DISC%", usable * 0.055, "R"})
		}
		if cols.Taxable {
			colDefs = append(colDefs, pdfCol{"TAXABLE", usable * 0.09, "R"})
		}
		if cols.Tax {
			colDefs = append(colDefs, pdfCol{"GST%", usable * 0.055, "R"})
		}
		if invoice.IsInterState {
			colDefs = append(colDefs, pdfCol{"IGST", usable * 0.07, "R"})
		} else {
			colDefs = append(colDefs, pdfCol{"CGST", usable * 0.065, "R"}, pdfCol{"SGST", usable * 0.065, "R"})
		}
		if hasCess {
			colDefs = append(colDefs, pdfCol{"CESS", usable * 0.06, "R"})
		}
	}
	if cols.Amount {
		colDefs = append(colDefs, pdfCol{"AMOUNT", usable * 0.095, "R"})
	}
	fixed := 0.0
	for _, c := range colDefs {
		if c.w > 0 {
			fixed += c.w
		}
	}
	colDefs[0].w = usable - fixed

	cellSize := fontSize - 3
	if cellSize < 6 {
		cellSize = 6
	}

	// Header row: bold with thick top/bottom rules.
	pdf.SetDrawColor(34, 34, 34)
	pdf.SetLineWidth(0.55)
	pdf.SetFont("Arial", "B", cellSize)
	pdf.SetTextColor(30, 30, 30)
	for i, c := range colDefs {
		ln := 0
		if i == len(colDefs)-1 {
			ln = 1
		}
		pdf.CellFormat(c.w, 7, c.label, "TB", ln, c.align, false, 0, "")
	}

	// Body rows: thin grey bottom separators.
	pdf.SetLineWidth(0.15)
	pdf.SetDrawColor(225, 225, 225)
	pdf.SetFont("Arial", "", cellSize)
	pdf.SetTextColor(45, 45, 45)
	rowH := 6.5
	for _, item := range invoice.Items {
		var values []string
		descW := colDefs[0].w - 2
		values = append(values, truncateToWidthPDF(pdf, item.Description, descW))
		if cols.HSN && (hasGST || anyItemHasHSN(invoice.Items)) {
			hsn := item.HSNCode
			if hsn == "" {
				hsn = item.SACCode
			}
			values = append(values, hsn)
		}
		if cols.Qty {
			values = append(values, fmt.Sprintf("%s %s", formatINR(item.Quantity), item.Unit))
		}
		if cols.MRP || !hasGST {
			mrp := "-"
			if itemMRP(item) > 0 {
				mrp = formatINR(itemMRP(item))
			}
			values = append(values, mrp)
		}
		if cols.Rate {
			values = append(values, formatINR(item.UnitPrice))
		}
		if hasGST {
			if cols.Gross {
				values = append(values, formatINR(itemGrossAmount(item)))
			}
			if cols.Disc {
				values = append(values, formatINR(item.Discount))
			}
			if cols.Taxable {
				values = append(values, formatINR(itemTaxableValue(item)))
			}
			if cols.Tax {
				values = append(values, formatINR(item.TaxRate))
			}
			if invoice.IsInterState {
				values = append(values, formatINR(item.IGST))
			} else {
				values = append(values, formatINR(item.CGST), formatINR(item.SGST))
			}
			if hasCess {
				values = append(values, formatINR(item.CessAmount))
			}
		}
		if cols.Amount {
			values = append(values, formatINR(item.Total))
		}
		for j, c := range colDefs {
			ln := 0
			if j == len(colDefs)-1 {
				ln = 1
			}
			v := ""
			if j < len(values) {
				v = values[j]
			}
			pdf.CellFormat(c.w, rowH, sanitizePDFText(v), "B", ln, c.align, false, 0, "")
		}
	}

	// SUBTOTAL row: thick rules, total qty + total amount.
	pdf.SetDrawColor(34, 34, 34)
	pdf.SetLineWidth(0.55)
	pdf.SetFont("Arial", "B", cellSize+0.5)
	pdf.SetTextColor(20, 20, 20)
	for i, c := range colDefs {
		ln := 0
		if i == len(colDefs)-1 {
			ln = 1
		}
		v := ""
		switch c.label {
		case "ITEMS":
			v = "SUBTOTAL"
		case "QTY.":
			v = invoiceQtyTotal(invoice)
		case "AMOUNT":
			v = "Rs. " + formatINR(invoice.SubTotal)
		}
		pdf.CellFormat(c.w, 8, v, "TB", ln, c.align, false, 0, "")
	}
	pdf.SetLineWidth(0.2)
	pdf.Ln(5)

	bottomTop := pdf.GetY()

	// Bottom left: bank details, payment QR, terms.
	pdf.SetXY(leftM, bottomTop)
	leftEnd := bottomTop
	if invSettings.ShowBankDetails && custom.Miscellaneous.ShowBankDetails && business != nil &&
		(business.BankName != "" || business.AccountNumber != "" || business.IFSCCode != "") {
		pdf.SetFont("Arial", "B", fontSize-1)
		pdf.SetTextColor(30, 30, 30)
		pdf.MultiCell(colW, 5, "BANK DETAILS", "", "L", false)
		pdf.SetFont("Arial", "", fontSize-2)
		pdf.SetTextColor(55, 55, 55)
		kv := func(k, v string) {
			if v == "" {
				return
			}
			pdf.SetX(leftM)
			pdf.CellFormat(30, 4.6, k+":", "", 0, "L", false, 0, "")
			pdf.MultiCell(colW-30, 4.6, sanitizePDFText(v), "", "L", false)
		}
		kv("Name", business.Name)
		kv("IFSC Code", business.IFSCCode)
		kv("Account No", business.AccountNumber)
		kv("Bank", business.BankName)
		leftEnd = pdf.GetY()
	}
	if custom.Miscellaneous.ShowQRCode && business != nil && strings.TrimSpace(business.UPIID) != "" {
		pdf.SetXY(leftM, leftEnd+3)
		pdf.SetFont("Arial", "B", fontSize-1)
		pdf.SetTextColor(30, 30, 30)
		pdf.MultiCell(colW, 5, "PAYMENT QR CODE", "", "L", false)
		pdf.SetX(leftM)
		pdf.SetFont("Arial", "", fontSize-2)
		pdf.SetTextColor(55, 55, 55)
		pdf.MultiCell(colW, 4.6, "UPI ID: "+sanitizePDFText(business.UPIID), "", "L", false)
		if png := upiQRPNG(business.UPIID, business.Name, invoice.TotalAmount); png != nil {
			name := "upi-qr"
			if pdf.RegisterImageOptionsReader(name, fpdf.ImageOptions{ImageType: "PNG", ReadDpi: true}, bytes.NewReader(png)) != nil {
				qrSize := 26.0
				pdf.ImageOptions(name, leftM, pdf.GetY()+1, qrSize, qrSize, false, fpdf.ImageOptions{}, 0, "")
				pdf.SetY(pdf.GetY() + qrSize + 1)
			}
		}
		leftEnd = pdf.GetY()
	}
	if invoice.Terms != "" && invSettings.ShowTerms && custom.InvoiceDetails.ShowTermsAndConditions {
		pdf.SetXY(leftM, leftEnd+3)
		pdf.SetFont("Arial", "B", fontSize-2)
		pdf.SetTextColor(30, 30, 30)
		pdf.MultiCell(colW, 4.6, "TERMS AND CONDITIONS", "", "L", false)
		pdf.SetX(leftM)
		pdf.SetFont("Arial", "", fontSize-3)
		pdf.SetTextColor(80, 80, 80)
		pdf.MultiCell(colW, 4.2, sanitizePDFText(invoice.Terms), "", "L", false)
		leftEnd = pdf.GetY()
	}
	if label := formatPaymentSplitsLabel(invoice.PaymentSplits, invoice.PaymentMode); label != "" {
		pdf.SetXY(leftM, leftEnd+2)
		pdf.SetFont("Arial", "", fontSize-2)
		pdf.SetTextColor(55, 55, 55)
		pdf.MultiCell(colW, 4.6, "Payment: "+sanitizePDFText(label), "", "L", false)
		leftEnd = pdf.GetY()
	}

	// Bottom right: totals, amount in words, authorised signatory.
	pdf.SetY(bottomTop)
	pdf.SetFont("Arial", "", fontSize-1)
	pdf.SetTextColor(50, 50, 50)
	totRow := func(label string, value float64, bold bool) {
		if bold {
			pdf.SetFont("Arial", "B", fontSize-1)
			pdf.SetTextColor(25, 25, 25)
		} else {
			pdf.SetFont("Arial", "", fontSize-1)
			pdf.SetTextColor(50, 50, 50)
		}
		pdf.SetX(rightX)
		pdf.CellFormat(rightW*0.55, 5.4, label, "", 0, "R", false, 0, "")
		pdf.CellFormat(rightW*0.45, 5.4, "Rs. "+formatINR(value), "", 1, "R", false, 0, "")
	}
	if hasGST {
		totRow("Total Taxable Value", invoiceTaxableTotal(invoice), false)
		if invoice.IsInterState {
			totRow("IGST", invoice.IGSTTotal, false)
		} else {
			totRow("CGST", invoice.CGSTTotal, false)
			totRow("SGST", invoice.SGSTTotal, false)
		}
	}
	if hasCess {
		totRow("Cess", invoice.CessTotal, false)
	}
	if disc := invoice.DiscountTotal + invoice.InvoiceDiscount; disc > 0 {
		totRow("Discount", -disc, false)
	}
	for _, charge := range additionalChargeRows(invoice.AdditionalChargeItems, invoice.AdditionalCharges) {
		totRow(charge.Label, charge.Amount, false)
	}
	if invoice.RoundOff != 0 {
		totRow("Round Off", invoice.RoundOff, false)
	}
	totRow("Total Amount", invoice.TotalAmount, true)
	if custom.InvoiceDetails.ShowReceivedAmount && invoice.AmountPaid > 0 {
		totRow("Received Amount", invoice.AmountPaid, false)
	}
	if custom.InvoiceDetails.ShowBalanceDue {
		totRow("Balance", invoice.TotalAmount-invoice.AmountPaid, false)
	}
	if custom.InvoiceDetails.ShowAmountInWords {
		pdf.Ln(2)
		pdf.SetFont("Arial", "B", fontSize-2)
		pdf.SetTextColor(40, 40, 40)
		pdf.SetX(rightX)
		pdf.MultiCell(rightW, 4.8, "Total Amount (in words)", "", "R", false)
		pdf.SetX(rightX)
		pdf.SetFont("Arial", "", fontSize-2)
		pdf.SetTextColor(50, 50, 50)
		pdf.MultiCell(rightW, 4.8, sanitizePDFText(amountWordsRupees(invoice.TotalAmount)), "", "R", false)
	}
	rightEnd := pdf.GetY()

	endY := leftEnd
	if rightEnd > endY {
		endY = rightEnd
	}
	pdf.SetY(endY)

	// Authorised signatory: invoice signature image, else business signature.
	sigType, sigData, sigOK := decodeSignatureImage(invoice.Signature)
	bizName := ""
	if business != nil {
		bizName = business.Name
		if !sigOK && custom.Miscellaneous.ShowSignature && business.SignatureURL != "" {
			if data, typ := fetchThermalLogoBytes(business.SignatureURL); len(data) > 0 {
				sigType, sigData, sigOK = typ, data, true
			}
		}
	}
	if sigOK {
		name := fmt.Sprintf("invoice-sig-%d", time.Now().UnixNano())
		if pdf.RegisterImageOptionsReader(name, fpdf.ImageOptions{ImageType: sigType, ReadDpi: true}, bytes.NewReader(sigData)) != nil {
			pdf.Ln(6)
			imgW := 45.0
			imgH := 15.0
			pdf.ImageOptions(name, pageW-rightM-imgW, pdf.GetY(), imgW, imgH, false, fpdf.ImageOptions{}, 0, "")
			pdf.SetY(pdf.GetY() + imgH + 1)
		}
	}
	pdf.Ln(2)
	pdf.SetFont("Arial", "B", fontSize-2)
	pdf.SetTextColor(40, 40, 40)
	pdf.CellFormat(0, 5, sanitizePDFText("AUTHORISED SIGNATORY FOR "+strings.ToUpper(bizName)), "", 1, "R", false, 0, "")

	if ps.PrintFooter {
		pdf.Ln(8)
		pdf.SetFont("Arial", "I", 8)
		pdf.SetTextColor(140, 140, 140)
		pdf.CellFormat(0, 6, "Invoice created using TruERP", "", 1, "C", false, 0, "")
	}

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// truncateToWidthPDF shortens text so it fits inside w millimetres at the
// current font, appending "..." when truncated.
func truncateToWidthPDF(pdf *fpdf.Fpdf, s string, w float64) string {
	s = sanitizePDFText(s)
	if pdf.GetStringWidth(s) <= w {
		return s
	}
	for len(s) > 0 && pdf.GetStringWidth(s+"...") > w {
		s = s[:len(s)-1]
	}
	return s + "..."
}

func parseThermalLineMarkers(line string) (center, bold bool, text string) {
	rest := line
	for {
		if strings.HasPrefix(rest, "@C@") {
			center = true
			rest = rest[3:]
			continue
		}
		if strings.HasPrefix(rest, "@B@") {
			bold = true
			rest = rest[3:]
			continue
		}
		if strings.HasPrefix(rest, "@N@") {
			center = false
			bold = false
			rest = rest[3:]
			continue
		}
		break
	}
	return center, bold, rest
}

func fetchThermalLogoBytes(logoURL string) ([]byte, string) {
	logoURL = strings.TrimSpace(logoURL)
	if logoURL == "" || !strings.HasPrefix(logoURL, "http") {
		return nil, ""
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(logoURL)
	if err != nil {
		return nil, ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, ""
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil || len(data) == 0 {
		return nil, ""
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	switch {
	case strings.Contains(ct, "png"), bytes.HasPrefix(data, []byte{0x89, 'P', 'N', 'G'}):
		return data, "PNG"
	case strings.Contains(ct, "jpeg"), strings.Contains(ct, "jpg"), bytes.HasPrefix(data, []byte{0xff, 0xd8}):
		return data, "JPG"
	case strings.Contains(ct, "gif"), bytes.HasPrefix(data, []byte("GIF")):
		return data, "GIF"
	default:
		return nil, ""
	}
}

func buildThermalReceiptPDF(content string, widthMM int, logoURL string) ([]byte, error) {
	if widthMM <= 0 {
		widthMM = 58
	}
	fontSize := 9.0
	lineH := 4.2
	margin := 2.0
	switch {
	case widthMM <= 28:
		fontSize = 6
		lineH = 3.0
		margin = 1.2
	case widthMM <= 42:
		fontSize = 7
		lineH = 3.4
		margin = 1.5
	case widthMM <= 60:
		fontSize = 8
		lineH = 3.8
		margin = 2.0
	}
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	logoBytes, logoType := fetchThermalLogoBytes(logoURL)
	logoH := 0.0
	if len(logoBytes) > 0 {
		logoH = float64(widthMM) * 0.22
		if logoH < 10 {
			logoH = 10
		}
		if logoH > 18 {
			logoH = 18
		}
	}
	// Keep page height tight to content — a large min height left long blank gaps on thermal rolls.
	height := margin*2 + logoH + float64(len(lines)+2)*lineH
	if height < 40 {
		height = 40
	}
	if height > 2000 {
		height = 2000
	}

	pdf := fpdf.NewCustom(&fpdf.InitType{
		UnitStr: "mm",
		Size:    fpdf.SizeType{Wd: float64(widthMM), Ht: height},
	})
	pdf.SetMargins(margin, margin, margin)
	pdf.SetAutoPageBreak(false, 0)
	pdf.AddPage()
	pdf.SetTextColor(0, 0, 0)

	usableW := float64(widthMM) - margin*2
	if len(logoBytes) > 0 && logoType != "" {
		opt := fpdf.ImageOptions{ImageType: logoType, ReadDpi: true}
		info := pdf.RegisterImageOptionsReader("thermal-logo", opt, bytes.NewReader(logoBytes))
		if info != nil {
			imgW := usableW * 0.55
			if info.Width() > 0 && info.Height() > 0 {
				scale := imgW / info.Width()
				imgH := info.Height() * scale
				if imgH > logoH && logoH > 0 {
					imgH = logoH
					imgW = info.Width() * (imgH / info.Height())
				}
				x := margin + (usableW-imgW)/2
				pdf.ImageOptions("thermal-logo", x, pdf.GetY(), imgW, imgH, false, opt, 0, "")
				pdf.SetY(pdf.GetY() + imgH + 1)
			}
		}
	}

	for _, line := range lines {
		center, bold, text := parseThermalLineMarkers(line)
		style := ""
		if bold {
			style = "B"
		}
		pdf.SetFont("Courier", style, fontSize)
		align := "L"
		if center {
			align = "C"
		}
		pdf.MultiCell(usableW, lineH, sanitizePDFText(text), "", align, false)
	}

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeTotalRow(pdf *fpdf.Fpdf, usable float64, label, value string, emphasize bool) {
	labelW := usable * 0.65
	valueW := usable - labelW
	if emphasize {
		pdf.SetTextColor(37, 99, 235)
	} else {
		pdf.SetTextColor(60, 60, 60)
	}
	pdf.CellFormat(labelW, 6, label, "", 0, "R", false, 0, "")
	pdf.CellFormat(valueW, 6, value, "", 1, "R", false, 0, "")
}

func mmFromInches(inches float64) float64 {
	if inches < 0 {
		inches = 0.5
	}
	return inches * 25.4
}

func sanitizePDFText(s string) string {
	// fpdf core fonts are Latin-1/WinAnsi; map common INR / unicode punctuation
	// then encode remaining runes as single Latin-1 bytes so UTF-8 sequences
	// like "·" (U+00B7) do not render as "Â·".
	r := strings.NewReplacer(
		"₹", "Rs.",
		"–", "-",
		"—", "-",
		"‘", "'",
		"’", "'",
		"“", "\"",
		"”", "\"",
		"…", "...",
	)
	s = r.Replace(s)
	var b strings.Builder
	b.Grow(len(s))
	for _, rr := range s {
		switch {
		case rr == '\n' || rr == '\r' || rr == '\t':
			b.WriteByte(byte(rr))
		case rr < 32:
			continue
		case rr <= 255:
			b.WriteByte(byte(rr))
		default:
			b.WriteByte('?')
		}
	}
	return b.String()
}

// wrapPDFText splits text so each line's GetStringWidth is <= maxWidth.
// The current font must already be selected.
func wrapPDFText(pdf *fpdf.Fpdf, text string, maxWidth float64) []string {
	text = strings.ReplaceAll(sanitizePDFText(text), "\r", "")
	if strings.TrimSpace(text) == "" {
		return []string{""}
	}
	if maxWidth <= 0 {
		return []string{text}
	}
	var lines []string
	for _, para := range strings.Split(text, "\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			lines = append(lines, "")
			continue
		}
		lines = append(lines, wrapPDFParagraph(pdf, para, maxWidth)...)
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func wrapPDFParagraph(pdf *fpdf.Fpdf, text string, maxWidth float64) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{""}
	}
	var lines []string
	current := ""
	for _, word := range words {
		for _, part := range splitPDFWord(pdf, word, maxWidth) {
			candidate := part
			if current != "" {
				candidate = current + " " + part
			}
			if pdf.GetStringWidth(candidate) <= maxWidth {
				current = candidate
				continue
			}
			if current != "" {
				lines = append(lines, current)
			}
			current = part
		}
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}

func splitPDFWord(pdf *fpdf.Fpdf, word string, maxWidth float64) []string {
	if pdf.GetStringWidth(word) <= maxWidth {
		return []string{word}
	}
	var parts []string
	cur := ""
	for _, rr := range word {
		trial := cur + string(rr)
		if cur != "" && pdf.GetStringWidth(trial) > maxWidth {
			parts = append(parts, cur)
			cur = string(rr)
			continue
		}
		cur = trial
	}
	if cur != "" {
		parts = append(parts, cur)
	}
	if len(parts) == 0 {
		return []string{word}
	}
	return parts
}

func writePDFWrappedLines(pdf *fpdf.Fpdf, x, y, w, lineH float64, lines []string) {
	prevMargin := pdf.GetCellMargin()
	pdf.SetCellMargin(0.4)
	for i, line := range lines {
		pdf.SetXY(x, y+float64(i)*lineH)
		pdf.CellFormat(w, lineH, line, "", 0, "L", false, 0, "")
	}
	pdf.SetCellMargin(prevMargin)
}

func truncatePDF(s string, max int) string {
	s = sanitizePDFText(s)
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
