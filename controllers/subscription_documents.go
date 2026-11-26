package controllers

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/go-pdf/fpdf"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Subscription invoices (one per billing cycle) and payment receipts (one per
// payment), rendered as A4 PDFs from the super admin's billing profile.

type rgb = [3]int

func hexRGB(hex string, fallback rgb) rgb {
	h := strings.TrimPrefix(strings.TrimSpace(hex), "#")
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	if len(h) != 6 {
		return fallback
	}
	v, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return fallback
	}
	return rgb{int(v >> 16 & 0xff), int(v >> 8 & 0xff), int(v & 0xff)}
}

// tint mixes c with white; f=0 keeps c, f=1 is white.
func tint(c rgb, f float64) rgb {
	mix := func(x int) int { return int(math.Round(float64(x) + (255-float64(x))*f)) }
	return rgb{mix(c[0]), mix(c[1]), mix(c[2])}
}

// decodeLogoData turns a data: URL into image bytes and an fpdf image type.
func decodeLogoData(dataURL string) ([]byte, string) {
	i := strings.Index(dataURL, ";base64,")
	if !strings.HasPrefix(dataURL, "data:image/") || i < 0 {
		return nil, ""
	}
	data, err := base64.StdEncoding.DecodeString(dataURL[i+len(";base64,"):])
	if err != nil || len(data) == 0 {
		return nil, ""
	}
	switch {
	case bytes.HasPrefix(data, []byte{0x89, 'P', 'N', 'G'}):
		return data, "PNG"
	case bytes.HasPrefix(data, []byte{0xff, 0xd8}):
		return data, "JPG"
	case bytes.HasPrefix(data, []byte("GIF")):
		return data, "GIF"
	}
	return data, "" // safeRegisterImage re-encodes other formats (e.g. WebP fails; skipped)
}

func subLongDate(ymd string) string {
	t, err := parseSubDate(ymd)
	if err != nil {
		return ymd
	}
	return t.Format("02 Jan 2006")
}

func subMoney(v float64) string { return "Rs. " + formatINRAmount(roundMoney(v)) }

var subMethodLabels = map[string]string{
	"cash": "Cash", "upi": "UPI", "bank_transfer": "Bank transfer", "card": "Card", "cheque": "Cheque", "other": "Other",
}

func subMethodLabel(m string) string {
	if l, ok := subMethodLabels[m]; ok {
		return l
	}
	return m
}

// subInvoiceTotals breaks a cycle into the figures printed on its invoice.
// Prices are tax-inclusive, so tax is carved out of the payable total.
type subInvoiceTotals struct {
	Gross, Discount, Total float64
	Taxable, CGST, SGST    float64
	IGST                   float64
	Paid, Balance          float64
	TaxRate                float64
	IntraState             bool
}

func computeSubInvoiceTotals(s models.SubscriptionBillingSettings, store models.Store, cy models.SubscriptionCycle) subInvoiceTotals {
	t := subInvoiceTotals{
		Gross:    roundMoney(cy.Amount + cy.WaivedAmount),
		Discount: roundMoney(cy.WaivedAmount),
		Total:    roundMoney(cy.Amount),
		Paid:     roundMoney(cy.AmountPaid),
		TaxRate:  s.TaxRate,
	}
	t.Balance = roundMoney(math.Max(0, t.Total-t.Paid))
	t.Taxable = t.Total
	if s.TaxRate > 0 && t.Total > 0 {
		t.Taxable = roundMoney(t.Total / (1 + s.TaxRate/100))
		tax := roundMoney(t.Total - t.Taxable)
		issuerSC, storeSC := strings.TrimSpace(s.StateCode), strings.TrimSpace(store.StateCode)
		t.IntraState = issuerSC != "" && issuerSC == storeSC
		if t.IntraState {
			t.CGST = roundMoney(tax / 2)
			t.SGST = roundMoney(tax - t.CGST)
		} else {
			t.IGST = tax
		}
	}
	return t
}

// subDoc carries everything one invoice or receipt needs.
type subDoc struct {
	Settings models.SubscriptionBillingSettings
	Store    models.Store
	Sub      models.StoreSubscription
	Cycle    models.SubscriptionCycle
	Payments []models.SubscriptionPayment // payments applied to Cycle
	Payment  *models.SubscriptionPayment  // the receipt's payment
}

// subPDF wraps fpdf with the document's palette and shared blocks.
type subPDF struct {
	*fpdf.Fpdf
	d                       subDoc
	accent, highlight       rgb
	ink, slate, muted, line rgb
	panel                   rgb
	margin, pageW, pageH    float64
	usable                  float64
}

func newSubPDF(d subDoc, title string) *subPDF {
	pdf := fpdf.New("P", "mm", "A4", "")
	p := &subPDF{
		Fpdf:      pdf,
		d:         d,
		accent:    hexRGB(d.Settings.AccentColor, rgb{30, 27, 75}),
		highlight: hexRGB(d.Settings.HighlightCol, rgb{201, 151, 63}),
		ink:       rgb{24, 32, 48},
		slate:     rgb{88, 99, 118},
		muted:     rgb{138, 147, 163},
		line:      rgb{226, 230, 237},
		margin:    14,
	}
	p.panel = tint(p.accent, 0.95)
	p.pageW, p.pageH = pdf.GetPageSize()
	p.usable = p.pageW - 2*p.margin
	pdf.SetMargins(p.margin, p.margin, p.margin)
	pdf.SetAutoPageBreak(true, 17)
	pdf.AliasNbPages("")
	pdf.SetTitle(sanitizePDFText(title), false)
	pdf.SetAuthor(sanitizePDFText(d.Settings.IssuerName), false)
	pdf.SetFooterFunc(p.footer)
	return p
}

// fitFont picks the largest size (down to min) at which s fits in w.
func (p *subPDF) fitFont(style, s string, size, min, w float64) {
	for ; size > min; size -= 0.5 {
		p.SetFont("Arial", style, size)
		if p.GetStringWidth(s) <= w {
			return
		}
	}
	p.SetFont("Arial", style, min)
}

func (p *subPDF) text(c rgb) { p.SetTextColor(c[0], c[1], c[2]) }
func (p *subPDF) fill(c rgb) { p.SetFillColor(c[0], c[1], c[2]) }
func (p *subPDF) draw(c rgb) { p.SetDrawColor(c[0], c[1], c[2]) }

func (p *subPDF) footer() {
	y := p.pageH - 14
	p.draw(p.line)
	p.SetLineWidth(0.2)
	p.Line(p.margin, y, p.pageW-p.margin, y)
	p.SetXY(p.margin, y+2)
	p.SetFont("Arial", "", 7.5)
	p.text(p.slate)
	footer := strings.TrimSpace(p.d.Settings.FooterText)
	if footer == "" {
		footer = "This is a computer-generated document."
	}
	p.CellFormat(p.usable*0.75, 4, sanitizePDFText(truncatePDF(footer, 120)), "", 0, "L", false, 0, "")
	p.text(p.muted)
	p.CellFormat(p.usable*0.25, 4, fmt.Sprintf("Page %d of {nb}", p.PageNo()), "", 0, "R", false, 0, "")
}

// ensureSpace starts a new page when h mm will not fit above the footer.
func (p *subPDF) ensureSpace(h float64) {
	if p.GetY()+h > p.pageH-17 {
		p.AddPage()
		p.SetY(p.margin)
	}
}

// header draws the brand band: logo + issuer on the left, title, number and
// a status pill on the right, then a highlight rule.
func (p *subPDF) header(title, number, status string, statusColor rgb) {
	const bandH = 36.0
	p.fill(p.accent)
	p.Rect(0, 0, p.pageW, bandH, "F")
	// Soft decorative disc in the corner.
	p.SetAlpha(0.10, "Normal")
	p.fill(p.highlight)
	p.Circle(p.pageW-18, -6, 34, "F")
	p.SetAlpha(1, "Normal")
	p.fill(p.highlight)
	p.Rect(0, bandH, p.pageW, 1.1, "F")

	s := p.d.Settings
	x := p.margin
	if data, typ := decodeLogoData(s.LogoData); data != nil {
		if info, name := safeRegisterImage(p.Fpdf, "sub-logo", typ, data); info != nil {
			maxW, maxH := 34.0, 20.0
			w, h := info.Width(), info.Height()
			scale := math.Min(maxW/w, maxH/h)
			w, h = w*scale, h*scale
			// White tile so dark logos stay legible on the band.
			p.SetFillColor(255, 255, 255)
			p.RoundedRect(x, (bandH-h)/2-2, w+4, h+4, 2, "1234", "F")
			p.ImageOptions(name, x+2, (bandH-h)/2, w, h, false, fpdf.ImageOptions{}, 0, "")
			x += w + 9
		}
	}
	// Give the title what it needs; the issuer gets the rest.
	t := sanitizePDFText(strings.ToUpper(truncatePDF(title, 30)))
	p.SetFont("Arial", "B", 17)
	titleW := math.Min(math.Max(p.GetStringWidth(t), 52)+2, p.usable*0.48)
	split := p.pageW - p.margin - titleW
	leftW := split - x - 6
	p.SetXY(x, 10)
	issuer := sanitizePDFText(truncatePDF(s.IssuerName, 48))
	p.fitFont("B", issuer, 15, 10, leftW)
	p.SetTextColor(255, 255, 255)
	p.CellFormat(leftW, 7, issuer, "", 2, "L", false, 0, "")
	sub := []string{}
	if loc := joinNonEmpty(", ", s.City, s.State); loc != "" {
		sub = append(sub, loc)
	}
	if s.GSTIN != "" {
		sub = append(sub, "GSTIN "+s.GSTIN)
	}
	if s.Website != "" {
		sub = append(sub, s.Website)
	}
	p.SetX(x)
	p.SetFont("Arial", "", 8)
	soft := tint(p.accent, 0.72)
	p.text(soft)
	// Drop trailing items (website, then GSTIN) rather than clipping text.
	subline := sanitizePDFText(strings.Join(sub, "  |  "))
	for len(sub) > 1 && p.GetStringWidth(subline) > leftW {
		sub = sub[:len(sub)-1]
		subline = sanitizePDFText(strings.Join(sub, "  |  "))
	}
	p.CellFormat(leftW, 4.5, sanitizePDFText(truncatePDF(subline, 80)), "", 2, "L", false, 0, "")

	rightX := split
	rightW := p.pageW - p.margin - rightX
	p.SetXY(rightX, 8.5)
	p.fitFont("B", t, 17, 11, rightW)
	p.SetTextColor(255, 255, 255)
	p.CellFormat(rightW, 8, t, "", 2, "R", false, 0, "")
	p.SetX(rightX)
	p.SetFont("Arial", "", 9)
	p.text(soft)
	p.CellFormat(rightW, 5, sanitizePDFText("# "+number), "", 2, "R", false, 0, "")
	if status != "" {
		p.SetFont("Arial", "B", 7.5)
		w := p.GetStringWidth(status) + 7
		px := p.pageW - p.margin - w
		p.fill(statusColor)
		p.RoundedRect(px, 25, w, 5.6, 2.8, "1234", "F")
		p.SetXY(px, 25)
		p.SetTextColor(255, 255, 255)
		p.CellFormat(w, 5.6, status, "", 0, "C", false, 0, "")
	}
	p.SetY(bandH + 7)
}

// metaStrip draws label/value boxes in one row.
func (p *subPDF) metaStrip(items [][2]string) {
	gap := 3.0
	w := (p.usable - gap*float64(len(items)-1)) / float64(len(items))
	y := p.GetY()
	for i, it := range items {
		x := p.margin + float64(i)*(w+gap)
		p.fill(p.panel)
		p.RoundedRect(x, y, w, 15, 2, "1234", "F")
		p.fill(p.highlight)
		p.Rect(x, y+3, 0.9, 9, "F")
		p.SetXY(x+4, y+2.6)
		p.SetFont("Arial", "B", 6.5)
		p.text(p.muted)
		p.CellFormat(w-6, 4, strings.ToUpper(it[0]), "", 2, "L", false, 0, "")
		p.SetX(x + 4)
		p.SetFont("Arial", "B", 9.5)
		p.text(p.ink)
		p.CellFormat(w-6, 5.5, sanitizePDFText(truncatePDF(it[1], 34)), "", 0, "L", false, 0, "")
	}
	p.SetY(y + 15 + 7)
}

type partyBlock struct {
	Label, Name string
	Lines       []string
}

func (p *subPDF) parties(a, b partyBlock) {
	gap := 8.0
	w := (p.usable - gap) / 2
	y := p.GetY()
	draw := func(x float64, pb partyBlock) float64 {
		p.SetXY(x, y)
		p.SetFont("Arial", "B", 7)
		p.text(p.accent)
		p.CellFormat(w, 4, strings.ToUpper(pb.Label), "", 2, "L", false, 0, "")
		p.fill(p.highlight)
		p.Rect(x, y+4.6, 10, 0.7, "F")
		p.SetXY(x, y+6.5)
		p.SetFont("Arial", "B", 10.5)
		p.text(p.ink)
		p.CellFormat(w, 5.5, sanitizePDFText(truncatePDF(pb.Name, 48)), "", 2, "L", false, 0, "")
		p.SetFont("Arial", "", 8.5)
		p.text(p.slate)
		cy := p.GetY() + 0.5
		for _, l := range pb.Lines {
			for _, wl := range wrapPDFText(p.Fpdf, sanitizePDFText(l), w) {
				p.SetXY(x, cy)
				p.CellFormat(w, 4.3, wl, "", 0, "L", false, 0, "")
				cy += 4.3
			}
		}
		return cy
	}
	h1 := draw(p.margin, a)
	h2 := draw(p.margin+w+gap, b)
	p.SetY(math.Max(h1, h2) + 6)
}

func issuerBlock(s models.SubscriptionBillingSettings, label string) partyBlock {
	lines := []string{}
	if s.Address != "" {
		lines = append(lines, s.Address)
	}
	if l := joinNonEmpty(", ", s.City, joinNonEmpty(" - ", s.State, s.Pincode)); l != "" {
		lines = append(lines, l)
	}
	if s.GSTIN != "" {
		lines = append(lines, "GSTIN: "+s.GSTIN)
	}
	if s.PAN != "" {
		lines = append(lines, "PAN: "+s.PAN)
	}
	if l := joinNonEmpty("  |  ", s.Email, s.Phone); l != "" {
		lines = append(lines, l)
	}
	return partyBlock{Label: label, Name: s.IssuerName, Lines: lines}
}

func storeBlock(st models.Store, label string) partyBlock {
	lines := []string{}
	if st.Address != "" {
		lines = append(lines, st.Address)
	}
	if l := joinNonEmpty(", ", st.City, joinNonEmpty(" - ", st.State, st.Pincode)); l != "" {
		lines = append(lines, l)
	}
	if st.GSTIN != "" {
		lines = append(lines, "GSTIN: "+st.GSTIN)
	}
	if l := joinNonEmpty("  |  ", st.Email, st.Phone); l != "" {
		lines = append(lines, l)
	}
	lines = append(lines, "Store code: "+st.Code)
	return partyBlock{Label: label, Name: st.Name, Lines: lines}
}

// tableHeader draws a filled header row; aligns per column.
func (p *subPDF) tableHeader(cols []string, widths []float64, aligns []string) {
	p.ensureSpace(16)
	p.fill(p.accent)
	p.SetFont("Arial", "B", 7.5)
	p.SetTextColor(255, 255, 255)
	p.SetX(p.margin)
	for i, c := range cols {
		p.CellFormat(widths[i], 7.5, " "+c+" ", "", 0, aligns[i], true, 0, "")
	}
	p.Ln(-1)
}

func (p *subPDF) sectionTitle(t string) {
	p.ensureSpace(14)
	p.SetFont("Arial", "B", 8)
	p.text(p.accent)
	p.SetX(p.margin)
	p.CellFormat(p.usable, 5, strings.ToUpper(t), "", 2, "L", false, 0, "")
	p.fill(p.highlight)
	p.Rect(p.margin, p.GetY()+0.3, 10, 0.7, "F")
	p.SetY(p.GetY() + 3)
}

// paragraphIn writes a titled paragraph in the left w mm of the page.
func (p *subPDF) paragraphIn(title, body string, w float64) {
	body = strings.TrimSpace(body)
	if body == "" {
		return
	}
	p.ensureSpace(14)
	p.SetFont("Arial", "B", 8)
	p.text(p.accent)
	p.SetX(p.margin)
	p.CellFormat(w, 5, strings.ToUpper(title), "", 2, "L", false, 0, "")
	p.fill(p.highlight)
	p.Rect(p.margin, p.GetY()+0.3, 10, 0.7, "F")
	p.SetY(p.GetY() + 3)
	p.SetFont("Arial", "", 8.5)
	p.text(p.slate)
	for _, l := range wrapPDFText(p.Fpdf, sanitizePDFText(body), w) {
		p.ensureSpace(5)
		p.SetX(p.margin)
		p.CellFormat(w, 4.4, l, "", 2, "L", false, 0, "")
	}
	p.Ln(3)
}

// closing lays out the notes (left) beside the signature (right), so the
// signature does not need its own band at the bottom of the page.
func (p *subPDF) closing(paras [][2]string) {
	sigW := 72.0
	leftW := p.usable
	if p.d.Settings.ShowSignature {
		leftW = p.usable - sigW - 8
	}
	// Measure the notes so the block can move to a new page as a unit
	// when it is short enough to fit on one.
	p.SetFont("Arial", "", 8.5)
	h := 0.0
	for _, pr := range paras {
		if strings.TrimSpace(pr[1]) != "" {
			h += 11 + 4.4*float64(len(wrapPDFText(p.Fpdf, sanitizePDFText(strings.TrimSpace(pr[1])), leftW)))
		}
	}
	if p.d.Settings.ShowSignature {
		h = math.Max(h, 24)
	}
	if h < 120 {
		p.ensureSpace(h)
	}
	startPage, startY := p.PageNo(), p.GetY()
	for _, pr := range paras {
		p.paragraphIn(pr[0], pr[1], leftW)
	}
	endPage, endY := p.PageNo(), p.GetY()
	if !p.d.Settings.ShowSignature {
		return
	}
	if endPage == startPage {
		p.SetY(startY)
		p.signatureAt(p.pageW - p.margin - sigW)
		p.SetY(math.Max(endY, startY+24))
		return
	}
	p.signatureAt(p.pageW - p.margin - sigW)
}

func (p *subPDF) signatureAt(x float64) {
	s := p.d.Settings
	p.ensureSpace(24)
	w := p.pageW - p.margin - x
	y := p.GetY()
	p.SetXY(x, y)
	p.SetFont("Arial", "B", 8.5)
	p.text(p.ink)
	p.CellFormat(w, 5, sanitizePDFText("For "+truncatePDF(s.IssuerName, 40)), "", 0, "R", false, 0, "")
	p.draw(p.muted)
	p.SetLineWidth(0.25)
	p.Line(x+12, y+17, x+w, y+17)
	p.SetXY(x, y+18)
	p.SetFont("Arial", "", 7.5)
	p.text(p.slate)
	label := "Authorised Signatory"
	if n := strings.TrimSpace(s.SignatoryName); n != "" {
		label = n + "  |  " + label
	}
	p.CellFormat(w, 4.5, sanitizePDFText(label), "", 0, "R", false, 0, "")
	p.SetY(y + 24)
}

func (p *subPDF) bankDetails() {
	s := p.d.Settings
	if !s.ShowBankDetails {
		return
	}
	pairs := [][2]string{}
	for _, kv := range [][2]string{{"Bank", s.BankName}, {"Account name", s.AccountName}, {"Account no.", s.AccountNumber}, {"IFSC", s.IFSC}, {"UPI ID", s.UPIID}} {
		if strings.TrimSpace(kv[1]) != "" {
			pairs = append(pairs, kv)
		}
	}
	if len(pairs) == 0 {
		return
	}
	rows := (len(pairs) + 1) / 2
	h := 9 + float64(rows)*5.2
	p.ensureSpace(h + 4)
	y := p.GetY()
	p.fill(p.panel)
	p.RoundedRect(p.margin, y, p.usable, h, 2, "1234", "F")
	p.SetXY(p.margin+4, y+3)
	p.SetFont("Arial", "B", 7)
	p.text(p.accent)
	p.CellFormat(p.usable, 4, "BANK DETAILS FOR PAYMENT", "", 2, "L", false, 0, "")
	colW := (p.usable - 8) / 2
	for i, kv := range pairs {
		cx := p.margin + 4 + float64(i%2)*colW
		cy := y + 8.5 + float64(i/2)*5.2
		p.SetXY(cx, cy)
		p.SetFont("Arial", "", 8)
		p.text(p.muted)
		p.CellFormat(24, 4.5, kv[0], "", 0, "L", false, 0, "")
		p.SetFont("Arial", "B", 8.5)
		p.text(p.ink)
		p.CellFormat(colW-24, 4.5, sanitizePDFText(truncatePDF(kv[1], 40)), "", 0, "L", false, 0, "")
	}
	p.SetY(y + h + 6)
}

func cyclePeriodText(cy models.SubscriptionCycle) string {
	return subLongDate(cy.PeriodStart) + " to " + subLongDate(cy.PeriodEnd)
}

func (d subDoc) invoiceTitle() string {
	if t := strings.TrimSpace(d.Settings.InvoiceTitle); t != "" {
		return t
	}
	if d.Settings.TaxRate > 0 && strings.TrimSpace(d.Settings.GSTIN) != "" {
		return "Tax Invoice"
	}
	return "Invoice"
}

// buildSubscriptionInvoicePDF renders the invoice for one billing cycle.
func buildSubscriptionInvoicePDF(d subDoc) ([]byte, error) {
	s, cy := d.Settings, d.Cycle
	t := computeSubInvoiceTotals(s, d.Store, cy)
	p := newSubPDF(d, d.invoiceTitle()+" "+cy.InvoiceNumber)
	p.AddPage()

	status, statusColor := "DUE", rgb{190, 40, 60}
	switch {
	case cy.Complimentary:
		status, statusColor = "COMPLIMENTARY", p.highlight
	case cy.PaymentStatus == subPayPaid:
		status, statusColor = "PAID", rgb{22, 128, 82}
	case cy.PaymentStatus == subPayPartial:
		status, statusColor = "PARTIALLY PAID", rgb{196, 120, 20}
	}
	p.header(d.invoiceTitle(), cy.InvoiceNumber, status, statusColor)
	p.metaStrip([][2]string{
		{"Invoice no.", cy.InvoiceNumber},
		{"Invoice date", subLongDate(cy.InvoiceDate)},
		{"Due date", subLongDate(cy.PeriodStart)},
		{"Billing cycle", fmt.Sprintf("#%d  |  %s", cy.CycleNumber, cycleLabelText(d.Sub.BillingCycle))},
	})
	p.parties(issuerBlock(s, "Billed by"), storeBlock(d.Store, "Billed to"))

	// Line item.
	sacW, amtW, numW := 24.0, 36.0, 10.0
	descW := p.usable - sacW - amtW - numW
	p.tableHeader([]string{"#", "Description", "SAC", "Amount"}, []float64{numW, descW, sacW, amtW}, []string{"C", "L", "C", "R"})
	y := p.GetY()
	rowH := 17.0
	if cy.Complimentary {
		rowH = 21
	}
	p.SetXY(p.margin, y+2.5)
	p.SetFont("Arial", "", 9)
	p.text(p.slate)
	p.CellFormat(numW, 5, "1", "", 0, "C", false, 0, "")
	p.SetFont("Arial", "B", 9.5)
	p.text(p.ink)
	p.CellFormat(descW, 5, sanitizePDFText(truncatePDF(d.Sub.PlanName+" plan - "+cycleLabelText(d.Sub.BillingCycle)+" subscription", 70)), "", 0, "L", false, 0, "")
	p.SetFont("Arial", "", 8.5)
	p.text(p.slate)
	p.CellFormat(sacW, 5, sanitizePDFText(s.SACCode), "", 0, "C", false, 0, "")
	p.SetFont("Arial", "B", 9.5)
	p.text(p.ink)
	p.CellFormat(amtW, 5, sanitizePDFText(subMoney(t.Gross)), "", 0, "R", false, 0, "")
	p.SetXY(p.margin+numW, y+8)
	p.SetFont("Arial", "", 8)
	p.text(p.slate)
	p.CellFormat(descW, 4.3, sanitizePDFText("Service period "+cyclePeriodText(cy)), "", 2, "L", false, 0, "")
	if cy.Complimentary {
		p.SetX(p.margin + numW)
		p.text(p.highlight)
		p.SetFont("Arial", "B", 8)
		reason := "Complimentary access"
		if r := strings.TrimSpace(d.Sub.ComplimentaryReason); r != "" {
			reason += " - " + r
		}
		p.CellFormat(descW, 4.3, sanitizePDFText(truncatePDF(reason, 80)), "", 0, "L", false, 0, "")
	}
	p.draw(p.line)
	p.SetLineWidth(0.25)
	p.Line(p.margin, y+rowH, p.pageW-p.margin, y+rowH)
	p.SetY(y + rowH + 4)

	// Totals (right) with amount in words (left).
	type row struct {
		label, value string
		bold         bool
		color        *rgb
	}
	rows := []row{{label: "Subtotal", value: subMoney(t.Gross)}}
	if t.Discount > 0 {
		hl := p.highlight
		rows = append(rows, row{label: "Complimentary discount", value: "- " + subMoney(t.Discount), color: &hl})
	}
	if s.TaxRate > 0 && t.Total > 0 {
		rows = append(rows, row{label: "Taxable value", value: subMoney(t.Taxable)})
		half := strconv.FormatFloat(s.TaxRate/2, 'f', -1, 64)
		full := strconv.FormatFloat(s.TaxRate, 'f', -1, 64)
		if t.IntraState {
			rows = append(rows, row{label: "CGST @ " + half + "%", value: subMoney(t.CGST)}, row{label: "SGST @ " + half + "%", value: subMoney(t.SGST)})
		} else {
			rows = append(rows, row{label: "IGST @ " + full + "%", value: subMoney(t.IGST)})
		}
	}
	totW := 84.0
	totX := p.pageW - p.margin - totW
	startY := p.GetY()
	p.ensureSpace(float64(len(rows))*5.5 + 30)
	if p.GetY() < startY { // moved to a new page
		startY = p.GetY()
	}
	cyY := startY
	for _, r := range rows {
		p.SetXY(totX, cyY)
		p.SetFont("Arial", "", 8.5)
		p.text(p.slate)
		if r.color != nil {
			p.text(*r.color)
		}
		p.CellFormat(totW-38, 5.5, sanitizePDFText(r.label), "", 0, "L", false, 0, "")
		p.SetFont("Arial", "B", 9)
		p.text(p.ink)
		if r.color != nil {
			p.text(*r.color)
		}
		p.CellFormat(38, 5.5, sanitizePDFText(r.value), "", 0, "R", false, 0, "")
		cyY += 5.5
	}
	cyY += 1.5
	p.fill(p.accent)
	p.RoundedRect(totX, cyY, totW, 9, 1.8, "1234", "F")
	p.SetXY(totX+3, cyY)
	p.SetFont("Arial", "B", 9.5)
	p.SetTextColor(255, 255, 255)
	p.CellFormat(totW-41, 9, "Total", "", 0, "L", false, 0, "")
	p.SetFont("Arial", "B", 11)
	p.CellFormat(35, 9, sanitizePDFText(subMoney(t.Total)), "", 0, "R", false, 0, "")
	cyY += 11.5
	for _, r := range []row{{label: "Amount paid", value: subMoney(t.Paid)}, {label: "Balance due", value: subMoney(t.Balance), bold: true}} {
		p.SetXY(totX, cyY)
		p.SetFont("Arial", "", 8.5)
		p.text(p.slate)
		p.CellFormat(totW-38, 5.5, r.label, "", 0, "L", false, 0, "")
		p.SetFont("Arial", "B", 9.5)
		switch {
		case r.bold && t.Balance > 0:
			p.SetTextColor(190, 40, 60)
		case r.bold:
			p.SetTextColor(22, 128, 82)
		default:
			p.text(p.ink)
		}
		p.CellFormat(38, 5.5, sanitizePDFText(r.value), "", 0, "R", false, 0, "")
		cyY += 5.5
	}
	leftW := p.usable - totW - 8
	ly := startY
	if s.ShowAmountInWords {
		p.SetXY(p.margin, ly)
		p.SetFont("Arial", "B", 7)
		p.text(p.muted)
		p.CellFormat(leftW, 4, "AMOUNT IN WORDS", "", 2, "L", false, 0, "")
		p.SetFont("Arial", "B", 8.5)
		p.text(p.ink)
		ly = p.GetY() + 0.5
		for _, l := range wrapPDFText(p.Fpdf, amountInWordsINR(t.Total), leftW) {
			p.SetXY(p.margin, ly)
			p.CellFormat(leftW, 4.4, l, "", 0, "L", false, 0, "")
			ly += 4.4
		}
		ly += 3
	}
	if s.TaxRate > 0 && t.Total > 0 {
		p.SetXY(p.margin, ly)
		p.SetFont("Arial", "", 7.5)
		p.text(p.muted)
		p.CellFormat(leftW, 4, sanitizePDFText(fmt.Sprintf("Prices are inclusive of %s.", s.TaxLabel)), "", 0, "L", false, 0, "")
		ly += 5
	}
	p.SetY(math.Max(cyY, ly) + 7)

	// Payments applied to this invoice.
	if s.ShowPaymentHistory && len(d.Payments) > 0 {
		p.sectionTitle("Payments received")
		ws := []float64{38, 28, 32, p.usable - 38 - 28 - 32 - 34, 34}
		al := []string{"L", "L", "L", "L", "R"}
		p.tableHeader([]string{"Receipt no.", "Date", "Method", "Reference", "Amount"}, ws, al)
		for i, pm := range d.Payments {
			p.ensureSpace(7)
			if i%2 == 1 {
				p.fill(p.panel)
			} else {
				p.SetFillColor(255, 255, 255)
			}
			p.SetX(p.margin)
			p.SetFont("Arial", "", 8.5)
			p.text(p.ink)
			vals := []string{pm.ReceiptNumber, subLongDate(pm.PaymentDate), subMethodLabel(pm.Method), truncatePDF(pm.Reference, 30), subMoney(pm.Amount)}
			for j, v := range vals {
				p.CellFormat(ws[j], 6.5, " "+sanitizePDFText(v)+" ", "", 0, al[j], true, 0, "")
			}
			p.Ln(-1)
		}
		p.Ln(5)
	}

	if t.Balance > 0 {
		p.bankDetails()
	}
	p.closing([][2]string{{"Notes", s.InvoiceNotes}, {"Terms & conditions", s.InvoiceTerms}})

	var buf bytes.Buffer
	if err := p.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func cycleLabelText(c string) string {
	if c == subCycleAnnual {
		return "Annual"
	}
	return "Monthly"
}

// buildSubscriptionReceiptPDF renders the receipt for one payment.
func buildSubscriptionReceiptPDF(d subDoc) ([]byte, error) {
	s, cy, pm := d.Settings, d.Cycle, d.Payment
	if pm == nil {
		return nil, fmt.Errorf("no payment")
	}
	p := newSubPDF(d, s.ReceiptTitle+" "+pm.ReceiptNumber)
	p.AddPage()
	p.header(s.ReceiptTitle, pm.ReceiptNumber, "RECEIVED", rgb{22, 128, 82})
	ref := strings.TrimSpace(pm.Reference)
	if ref == "" {
		ref = "-"
	}
	p.metaStrip([][2]string{
		{"Receipt no.", pm.ReceiptNumber},
		{"Receipt date", subLongDate(pm.PaymentDate)},
		{"Payment method", subMethodLabel(pm.Method)},
		{"Reference", ref},
	})

	// Amount hero.
	y := p.GetY()
	h := 27.0
	if s.ShowAmountInWords {
		h = 32
	}
	p.fill(p.panel)
	p.RoundedRect(p.margin, y, p.usable, h, 3, "1234", "F")
	p.fill(p.highlight)
	p.Rect(p.margin, y+4, 1.2, h-8, "F")
	p.SetXY(p.margin+7, y+5)
	p.SetFont("Arial", "B", 7.5)
	p.text(p.muted)
	p.CellFormat(90, 4, "AMOUNT RECEIVED", "", 2, "L", false, 0, "")
	p.SetX(p.margin + 7)
	p.SetFont("Arial", "B", 22)
	p.text(p.accent)
	p.CellFormat(110, 11, sanitizePDFText(subMoney(pm.Amount)), "", 2, "L", false, 0, "")
	if s.ShowAmountInWords {
		p.SetX(p.margin + 7)
		p.SetFont("Arial", "", 8)
		p.text(p.slate)
		p.CellFormat(110, 5, sanitizePDFText(truncatePDF(amountInWordsINR(pm.Amount), 90)), "", 0, "L", false, 0, "")
	}
	rx := p.margin + p.usable - 66
	p.SetXY(rx, y+6)
	p.SetFont("Arial", "B", 7.5)
	p.text(p.muted)
	p.CellFormat(60, 4, "AGAINST INVOICE", "", 2, "R", false, 0, "")
	p.SetX(rx)
	p.SetFont("Arial", "B", 10.5)
	p.text(p.ink)
	p.CellFormat(60, 6, sanitizePDFText(cy.InvoiceNumber), "", 2, "R", false, 0, "")
	p.SetX(rx)
	p.SetFont("Arial", "", 8)
	p.text(p.slate)
	p.CellFormat(60, 4.5, sanitizePDFText(truncatePDF(d.Sub.PlanName+" | Cycle #"+strconv.Itoa(cy.CycleNumber), 40)), "", 0, "R", false, 0, "")
	p.SetY(y + h + 8)

	p.parties(storeBlock(d.Store, "Received from"), issuerBlock(s, "Received by"))

	// What the payment was for.
	p.sectionTitle("Payment for")
	ws := []float64{38, p.usable - 38 - 34 - 34, 34, 34}
	al := []string{"L", "L", "R", "R"}
	p.tableHeader([]string{"Invoice no.", "Description", "Invoice total", "This payment"}, ws, al)
	p.SetX(p.margin)
	p.SetFont("Arial", "", 8.5)
	p.text(p.ink)
	desc := d.Sub.PlanName + " plan  |  " + cyclePeriodText(cy)
	for j, v := range []string{cy.InvoiceNumber, truncatePDF(desc, 60), subMoney(cy.Amount), subMoney(pm.Amount)} {
		p.CellFormat(ws[j], 8, " "+sanitizePDFText(v)+" ", "B", 0, al[j], false, 0, "")
	}
	p.Ln(-1)
	p.Ln(4)

	if s.ShowReceiptBalance {
		// Paid to date counts payments up to and including this one.
		paidToDate := 0.0
		for _, other := range d.Payments {
			if other.PaymentDate < pm.PaymentDate || (other.PaymentDate == pm.PaymentDate && !other.CreatedAt.After(pm.CreatedAt)) || other.ID == pm.ID {
				paidToDate += other.Amount
			}
		}
		balance := math.Max(0, cy.Amount-paidToDate)
		totW := 84.0
		x := p.pageW - p.margin - totW
		y := p.GetY()
		for i, r := range [][2]string{{"Invoice total", subMoney(cy.Amount)}, {"Paid to date", subMoney(paidToDate)}, {"Balance due", subMoney(balance)}} {
			p.SetXY(x, y+float64(i)*5.8)
			p.SetFont("Arial", "", 8.5)
			p.text(p.slate)
			p.CellFormat(totW-40, 5.8, r[0], "", 0, "L", false, 0, "")
			p.SetFont("Arial", "B", 9.5)
			p.text(p.ink)
			if i == 2 {
				if balance > 0.005 {
					p.SetTextColor(190, 40, 60)
				} else {
					p.SetTextColor(22, 128, 82)
				}
			}
			p.CellFormat(40, 5.8, sanitizePDFText(r[1]), "", 0, "R", false, 0, "")
		}
		p.SetY(y + 3*5.8 + 6)
	}

	p.closing([][2]string{{"Payment note", pm.Notes}, {"Notes", s.ReceiptNotes}})

	var buf bytes.Buffer
	if err := p.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ---------------------------------------------------------------------------
// Loading & handlers
// ---------------------------------------------------------------------------

func loadSubDoc(subID, cycleID uuid.UUID) (subDoc, error) {
	d := subDoc{Settings: models.LoadSubscriptionBillingSettings(utils.DB)}
	if err := utils.DB.First(&d.Sub, "id = ?", subID).Error; err != nil {
		return d, err
	}
	if err := utils.DB.Where("id = ? AND subscription_id = ?", cycleID, subID).First(&d.Cycle).Error; err != nil {
		return d, err
	}
	store, err := utils.FindStoreByID(utils.DB, d.Sub.StoreID)
	if err != nil {
		return d, err
	}
	d.Store = store
	utils.DB.Where("cycle_id = ?", cycleID).Order("payment_date ASC, created_at ASC").Find(&d.Payments)
	return d, nil
}

func sendPDF(c *gin.Context, filename string, data []byte) {
	disposition := "inline"
	if c.Query("download") == "1" {
		disposition = "attachment"
	}
	c.Header("Content-Disposition", fmt.Sprintf(`%s; filename="%s"`, disposition, filename))
	c.Data(http.StatusOK, "application/pdf", data)
}

func renderInvoice(c *gin.Context, subID, cycleID uuid.UUID) {
	d, err := loadSubDoc(subID, cycleID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Invoice not found"})
		return
	}
	data, err := buildSubscriptionInvoicePDF(d)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate invoice"})
		return
	}
	sendPDF(c, d.Cycle.InvoiceNumber+".pdf", data)
}

func renderReceipt(c *gin.Context, payment models.SubscriptionPayment) {
	d, err := loadSubDoc(payment.SubscriptionID, payment.CycleID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Receipt not found"})
		return
	}
	d.Payment = &payment
	data, err := buildSubscriptionReceiptPDF(d)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate receipt"})
		return
	}
	sendPDF(c, payment.ReceiptNumber+".pdf", data)
}

func parseUUIDParam(c *gin.Context, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Not found"})
		return uuid.Nil, false
	}
	return id, true
}

// AdminSubscriptionInvoicePDF renders a cycle's invoice.
func AdminSubscriptionInvoicePDF(c *gin.Context) {
	subID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	cycleID, ok := parseUUIDParam(c, "cycleId")
	if !ok {
		return
	}
	renderInvoice(c, subID, cycleID)
}

// AdminSubscriptionReceiptPDF renders a payment's receipt.
func AdminSubscriptionReceiptPDF(c *gin.Context) {
	var payment models.SubscriptionPayment
	if err := utils.DB.Where("id = ? AND subscription_id = ?", c.Param("paymentId"), c.Param("id")).First(&payment).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Receipt not found"})
		return
	}
	renderReceipt(c, payment)
}

// MyPlanInvoicePDF renders an invoice of the active store.
func MyPlanInvoicePDF(c *gin.Context) {
	storeID, ok := currentStoreID(c)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "Invoice not found"})
		return
	}
	var cycle models.SubscriptionCycle
	if err := utils.DB.Where("id = ? AND store_id = ?", c.Param("cycleId"), storeID).First(&cycle).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Invoice not found"})
		return
	}
	renderInvoice(c, cycle.SubscriptionID, cycle.ID)
}

// MyPlanReceiptPDF renders a receipt of the active store.
func MyPlanReceiptPDF(c *gin.Context) {
	storeID, ok := currentStoreID(c)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "Receipt not found"})
		return
	}
	var payment models.SubscriptionPayment
	if err := utils.DB.Where("id = ? AND store_id = ?", c.Param("paymentId"), storeID).First(&payment).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Receipt not found"})
		return
	}
	renderReceipt(c, payment)
}

// ---------------------------------------------------------------------------
// Billing settings (customization)
// ---------------------------------------------------------------------------

const maxLogoDataLen = 400 * 1024

func validateBillingSettings(s *models.SubscriptionBillingSettings) error {
	trim := func(p *string) { *p = strings.TrimSpace(*p) }
	for _, f := range []*string{&s.IssuerName, &s.City, &s.State, &s.StateCode, &s.Pincode, &s.GSTIN, &s.PAN, &s.Email, &s.Phone,
		&s.Website, &s.AccentColor, &s.HighlightCol, &s.InvoicePrefix, &s.ReceiptPrefix, &s.TaxLabel, &s.SACCode,
		&s.InvoiceTitle, &s.BankName, &s.AccountName, &s.AccountNumber, &s.IFSC, &s.UPIID, &s.ReceiptTitle,
		&s.SignatoryName, &s.FooterText} {
		trim(f)
	}
	s.GSTIN, s.PAN, s.IFSC = strings.ToUpper(s.GSTIN), strings.ToUpper(s.PAN), strings.ToUpper(s.IFSC)
	s.InvoicePrefix, s.ReceiptPrefix = strings.ToUpper(s.InvoicePrefix), strings.ToUpper(s.ReceiptPrefix)
	if s.IssuerName == "" {
		return fmt.Errorf("Business name is required")
	}
	prefixOK := func(p string) bool {
		if p == "" || len(p) > 12 {
			return false
		}
		for _, r := range p {
			if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '/' || r == '_') {
				return false
			}
		}
		return true
	}
	if !prefixOK(s.InvoicePrefix) || !prefixOK(s.ReceiptPrefix) {
		return fmt.Errorf("Prefixes must be 1-12 letters or digits")
	}
	if s.InvoicePrefix == s.ReceiptPrefix {
		return fmt.Errorf("Invoice and receipt prefixes must differ")
	}
	if s.NumberPadding < 3 || s.NumberPadding > 8 {
		return fmt.Errorf("Number length must be between 3 and 8 digits")
	}
	if s.TaxRate < 0 || s.TaxRate > 50 {
		return fmt.Errorf("Tax rate must be between 0 and 50%%")
	}
	for _, c := range []string{s.AccentColor, s.HighlightCol} {
		if _, err := strconv.ParseUint(strings.TrimPrefix(c, "#"), 16, 32); err != nil || len(strings.TrimPrefix(c, "#")) != 6 {
			return fmt.Errorf("Colours must be hex values like #1E1B4B")
		}
	}
	if s.LogoData != "" {
		if len(s.LogoData) > maxLogoDataLen {
			return fmt.Errorf("Logo is too large — use an image under 300 KB")
		}
		if data, _ := decodeLogoData(s.LogoData); data == nil {
			return fmt.Errorf("Logo must be a PNG or JPG image")
		}
	}
	return nil
}

// AdminGetSubscriptionBillingSettings returns the billing profile (defaults
// until saved) plus the next numbers that will be issued.
func AdminGetSubscriptionBillingSettings(c *gin.Context) {
	s := models.LoadSubscriptionBillingSettings(utils.DB)
	c.JSON(http.StatusOK, gin.H{"settings": s, "next_numbers": nextDocNumberPreview(s)})
}

func nextDocNumberPreview(s models.SubscriptionBillingSettings) gin.H {
	year := time.Now().Format("2006")
	peek := func(kind, prefix string) string {
		var ctr models.SubscriptionDocCounter
		utils.DB.First(&ctr, "key = ?", kind+"|"+prefix+"|"+year)
		return fmt.Sprintf("%s-%s-%0*d", prefix, year, s.NumberPadding, ctr.LastNumber+1)
	}
	return gin.H{"invoice": peek("invoice", s.InvoicePrefix), "receipt": peek("receipt", s.ReceiptPrefix)}
}

// AdminUpdateSubscriptionBillingSettings saves the billing profile.
func AdminUpdateSubscriptionBillingSettings(c *gin.Context) {
	var in models.SubscriptionBillingSettings
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := validateBillingSettings(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var existing models.SubscriptionBillingSettings
	err := utils.DB.Order("created_at ASC").First(&existing).Error
	in.CreatedAt = time.Time{}
	switch {
	case err == nil:
		in.ID, in.CreatedAt = existing.ID, existing.CreatedAt
		err = utils.DB.Save(&in).Error
	case err == gorm.ErrRecordNotFound:
		in.ID = uuid.New()
		err = utils.DB.Create(&in).Error
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save billing settings"})
		return
	}
	s := models.LoadSubscriptionBillingSettings(utils.DB)
	c.JSON(http.StatusOK, gin.H{"settings": s, "next_numbers": nextDocNumberPreview(s)})
}

// AdminPreviewSubscriptionDocument renders a sample invoice or receipt
// (?doc=invoice|receipt) from posted, unsaved settings.
func AdminPreviewSubscriptionDocument(c *gin.Context) {
	var s models.SubscriptionBillingSettings
	if err := c.ShouldBindJSON(&s); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := validateBillingSettings(&s); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	year := time.Now().Format("2006")
	num := func(prefix string, n int) string { return fmt.Sprintf("%s-%s-%0*d", prefix, year, s.NumberPadding, n) }
	start := time.Now().Format(subDateLayout)
	end := addSubPeriod(time.Now(), subCycleMonthly, time.Now().Day()).Format(subDateLayout)
	sampleState := s.StateCode
	d := subDoc{
		Settings: s,
		Store: models.Store{Name: "Sample Store Pvt Ltd", Code: "SAMPLE-01", Address: "12, MG Road, Indiranagar",
			City: "Bengaluru", State: "Karnataka", Pincode: "560038", GSTIN: "29ABCDE1234F1Z5", StateCode: sampleState,
			Email: "accounts@samplestore.in", Phone: "+91 98765 43210"},
		Sub: models.StoreSubscription{PlanName: "Growth", BillingCycle: subCycleMonthly, Price: 1499},
		Cycle: models.SubscriptionCycle{ID: uuid.New(), CycleNumber: 3, PeriodStart: start, PeriodEnd: end, Amount: 1499,
			AmountPaid: 1000, PaymentStatus: subPayPartial, InvoiceNumber: num(s.InvoicePrefix, 42), InvoiceDate: start},
	}
	pay := models.SubscriptionPayment{ID: uuid.New(), Amount: 1000, PaymentDate: start, Method: "upi",
		Reference: "UPI-4471-8820", ReceiptNumber: num(s.ReceiptPrefix, 57), CreatedAt: time.Now()}
	d.Payments = []models.SubscriptionPayment{pay}

	var data []byte
	var err error
	if c.Query("doc") == "receipt" {
		d.Payment = &pay
		data, err = buildSubscriptionReceiptPDF(d)
	} else {
		data, err = buildSubscriptionInvoicePDF(d)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to render preview"})
		return
	}
	sendPDF(c, "preview.pdf", data)
}
