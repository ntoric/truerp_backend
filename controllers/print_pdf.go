package controllers

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"truerp/models"

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
	pdf.SetAutoPageBreak(true, 15)
	pdf.SetMargins(mmFromInches(ps.MarginLeft), mmFromInches(ps.MarginTop), mmFromInches(ps.MarginRight))
	pdf.SetAutoPageBreak(true, mmFromInches(ps.MarginBottom))
	pdf.AddPage()

	fontSize := float64(ps.FontSize)
	if fontSize <= 0 {
		fontSize = 12
	}

	if ps.PrintHeader {
		if business != nil && business.Name != "" {
			pdf.SetFont("Arial", "B", fontSize+4)
			pdf.SetTextColor(30, 30, 30)
			pdf.CellFormat(0, 8, sanitizePDFText(business.Name), "", 1, "L", false, 0, "")
			pdf.SetFont("Arial", "", fontSize-1)
			pdf.SetTextColor(80, 80, 80)
			if business.Address != "" {
				pdf.MultiCell(0, 5, sanitizePDFText(business.Address), "", "L", false)
			}
			loc := strings.TrimSpace(strings.Join([]string{business.City, business.State, business.Pincode}, ", "))
			if loc != "" && loc != ", ," {
				pdf.CellFormat(0, 5, sanitizePDFText(loc), "", 1, "L", false, 0, "")
			}
			if business.Phone != "" {
				pdf.CellFormat(0, 5, "Ph: "+sanitizePDFText(business.Phone), "", 1, "L", false, 0, "")
			}
			if business.GSTIN != "" {
				pdf.CellFormat(0, 5, "GSTIN: "+sanitizePDFText(business.GSTIN), "", 1, "L", false, 0, "")
			}
			pdf.Ln(4)
		}
	}

	pdf.SetFont("Arial", "B", fontSize+6)
	pdf.SetTextColor(37, 99, 235)
	pdf.CellFormat(0, 10, "TAX INVOICE", "", 1, "L", false, 0, "")
	pdf.SetFont("Arial", "", fontSize)
	pdf.SetTextColor(80, 80, 80)
	pdf.CellFormat(0, 6, sanitizePDFText(invoice.InvoiceNumber), "", 1, "L", false, 0, "")
	pdf.SetFont("Arial", "B", fontSize-1)
	pdf.CellFormat(0, 6, strings.ToUpper(invoice.Status), "", 1, "L", false, 0, "")
	pdf.Ln(4)

	// Bill To
	pdf.SetFont("Arial", "B", fontSize)
	pdf.SetTextColor(50, 50, 50)
	pdf.CellFormat(95, 6, "Bill To", "", 0, "L", false, 0, "")
	pdf.CellFormat(0, 6, "Invoice Details", "", 1, "L", false, 0, "")
	pdf.SetFont("Arial", "", fontSize-1)
	pdf.SetTextColor(60, 60, 60)

	leftY := pdf.GetY()
	pdf.MultiCell(95, 5, sanitizePDFText(invoice.Party.Name), "", "L", false)
	if invoice.Party.Address != "" {
		pdf.MultiCell(95, 5, sanitizePDFText(invoice.Party.Address), "", "L", false)
	}
	partyLoc := strings.TrimSpace(fmt.Sprintf("%s, %s - %s", invoice.Party.City, invoice.Party.State, invoice.Party.Pincode))
	if partyLoc != ",  - " && partyLoc != "" {
		pdf.MultiCell(95, 5, sanitizePDFText(partyLoc), "", "L", false)
	}
	if invoice.Party.GSTIN != "" {
		pdf.MultiCell(95, 5, "GSTIN: "+sanitizePDFText(invoice.Party.GSTIN), "", "L", false)
	}
	leftBottom := pdf.GetY()

	pdf.SetXY(pdf.GetX()+95, leftY)
	pdf.CellFormat(0, 5, "Date: "+invoice.Date.Format("02-01-2006"), "", 1, "L", false, 0, "")
	pdf.SetX(pdf.GetX() + 95)
	due := "N/A"
	if invoice.DueDate != nil {
		due = invoice.DueDate.Format("02-01-2006")
	}
	pdf.CellFormat(0, 5, "Due: "+due, "", 1, "L", false, 0, "")
	if invoice.PlaceOfSupply != "" {
		pdf.SetX(pdf.GetX() + 95)
		pdf.CellFormat(0, 5, "Place of Supply: "+sanitizePDFText(invoice.PlaceOfSupply), "", 1, "L", false, 0, "")
	}
	rightBottom := pdf.GetY()
	if leftBottom > rightBottom {
		pdf.SetY(leftBottom)
	} else {
		pdf.SetY(rightBottom)
	}
	pdf.Ln(6)

	// Items table
	pageW, _ := pdf.GetPageSize()
	leftM, _, rightM, _ := pdf.GetMargins()
	usable := pageW - leftM - rightM
	colDesc := usable * 0.42
	colQty := usable * 0.10
	colRate := usable * 0.16
	colTax := usable * 0.12
	colTotal := usable - colDesc - colQty - colRate - colTax

	pdf.SetFillColor(243, 244, 246)
	pdf.SetDrawColor(200, 200, 200)
	pdf.SetFont("Arial", "B", fontSize-2)
	pdf.SetTextColor(40, 40, 40)
	pdf.CellFormat(colDesc, 8, "Description", "1", 0, "L", true, 0, "")
	pdf.CellFormat(colQty, 8, "Qty", "1", 0, "R", true, 0, "")
	pdf.CellFormat(colRate, 8, "Rate", "1", 0, "R", true, 0, "")
	pdf.CellFormat(colTax, 8, "Tax%", "1", 0, "R", true, 0, "")
	pdf.CellFormat(colTotal, 8, "Total", "1", 1, "R", true, 0, "")

	pdf.SetFont("Arial", "", fontSize-2)
	pdf.SetTextColor(50, 50, 50)
	for _, item := range invoice.Items {
		rowH := 7.0
		pdf.CellFormat(colDesc, rowH, truncatePDF(item.Description, 42), "1", 0, "L", false, 0, "")
		pdf.CellFormat(colQty, rowH, fmt.Sprintf("%.2f", item.Quantity), "1", 0, "R", false, 0, "")
		pdf.CellFormat(colRate, rowH, fmt.Sprintf("%.2f", item.UnitPrice), "1", 0, "R", false, 0, "")
		pdf.CellFormat(colTax, rowH, fmt.Sprintf("%.1f", item.TaxRate), "1", 0, "R", false, 0, "")
		pdf.CellFormat(colTotal, rowH, fmt.Sprintf("%.2f", item.Total), "1", 1, "R", false, 0, "")
	}
	pdf.Ln(4)

	pdf.SetFont("Arial", "", fontSize-1)
	writeTotalRow(pdf, usable, "Sub Total", fmt.Sprintf("Rs. %.2f", invoice.SubTotal), false)
	disc := invoice.DiscountTotal + invoice.InvoiceDiscount
	if disc > 0 {
		writeTotalRow(pdf, usable, "Discount", fmt.Sprintf("-Rs. %.2f", disc), false)
	}
	if invoice.IsInterState {
		writeTotalRow(pdf, usable, "IGST", fmt.Sprintf("Rs. %.2f", invoice.IGSTTotal), false)
	} else {
		writeTotalRow(pdf, usable, "CGST", fmt.Sprintf("Rs. %.2f", invoice.CGSTTotal), false)
		writeTotalRow(pdf, usable, "SGST", fmt.Sprintf("Rs. %.2f", invoice.SGSTTotal), false)
	}
	if invoice.AdditionalCharges > 0 {
		writeTotalRow(pdf, usable, "Additional", fmt.Sprintf("Rs. %.2f", invoice.AdditionalCharges), false)
	}
	if invoice.RoundOff != 0 {
		writeTotalRow(pdf, usable, "Round Off", fmt.Sprintf("Rs. %.2f", invoice.RoundOff), false)
	}
	pdf.SetFont("Arial", "B", fontSize)
	writeTotalRow(pdf, usable, "Grand Total", fmt.Sprintf("Rs. %.2f", invoice.TotalAmount), true)
	if label := formatPaymentSplitsLabel(invoice.PaymentSplits, invoice.PaymentMode); label != "" {
		pdf.SetFont("Arial", "", fontSize-1)
		writeTotalRow(pdf, usable, "Payment", sanitizePDFText(label), false)
	}
	if invoice.AmountPaid > 0 {
		pdf.SetFont("Arial", "", fontSize-1)
		writeTotalRow(pdf, usable, "Paid", fmt.Sprintf("Rs. %.2f", invoice.AmountPaid), false)
		bal := invoice.TotalAmount - invoice.AmountPaid
		if bal > 0 {
			writeTotalRow(pdf, usable, "Balance", fmt.Sprintf("Rs. %.2f", bal), false)
		}
	}

	if invoice.Terms != "" {
		pdf.Ln(6)
		pdf.SetFont("Arial", "B", fontSize-1)
		pdf.CellFormat(0, 6, "Terms & Conditions", "", 1, "L", false, 0, "")
		pdf.SetFont("Arial", "", fontSize-2)
		pdf.SetTextColor(80, 80, 80)
		pdf.MultiCell(0, 5, sanitizePDFText(invoice.Terms), "", "L", false)
	}

	if ps.PrintFooter {
		pdf.Ln(8)
		pdf.SetFont("Arial", "I", 8)
		pdf.SetTextColor(140, 140, 140)
		footer := "Generated by TruERP"
		if business != nil && business.Name != "" {
			footer = sanitizePDFText(business.Name) + " · " + footer
		}
		pdf.CellFormat(0, 6, footer, "", 1, "C", false, 0, "")
	}

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
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
