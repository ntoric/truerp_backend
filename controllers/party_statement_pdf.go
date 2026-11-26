package controllers

import (
	"bytes"
	"fmt"
	"math"
	"strings"
	"time"
	"truerp/models"

	"github.com/go-pdf/fpdf"
)

// Statement palette: deep navy brand with a warm gold accent, slate text,
// and muted red / green for the Dr / Cr sides.
var (
	stNavy      = [3]int{17, 34, 64}
	stNavySoft  = [3]int{190, 202, 222}
	stGold      = [3]int{191, 150, 74}
	stInk       = [3]int{24, 32, 48}
	stSlate     = [3]int{92, 104, 124}
	stMuted     = [3]int{140, 150, 166}
	stHairline  = [3]int{226, 230, 237}
	stPanel     = [3]int{246, 248, 251}
	stZebra     = [3]int{250, 251, 253}
	stSpecialBg = [3]int{239, 243, 249}

	statementDebitText  = [3]int{168, 38, 52}
	statementCreditText = [3]int{20, 112, 72}
)

// buildPartyStatementPDF renders the detailed periodic statement sent with a
// balance reminder: previous balance, then every sale, payment in and other
// balance-affecting document in date order (same-day entries in the order
// they were recorded) with a running balance, then the current balance.
// Balances are shown Dr (party owes the business) or Cr (advance / business
// owes the party).
func buildPartyStatementPDF(ledger *PartyLedger, business *models.Business, loc *time.Location) ([]byte, error) {
	if loc == nil {
		loc = time.Local
	}
	const margin = 14.0
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(margin, margin, margin)
	pdf.SetAutoPageBreak(true, 20)
	pdf.AliasNbPages("")
	pdf.SetTitle(sanitizePDFText("Statement of Account - "+ledger.Party.Name), false)
	pdf.SetAuthor(sanitizePDFText(business.Name), false)

	pageW, pageH := pdf.GetPageSize()
	usable := pageW - 2*margin
	isVendor := ledger.Party.PartyType == "vendor"
	periodText := statementLongDate(ledger.FromDate) + "  -  " + statementLongDate(ledger.ToDate)

	setText := func(c [3]int) { pdf.SetTextColor(c[0], c[1], c[2]) }
	setFill := func(c [3]int) { pdf.SetFillColor(c[0], c[1], c[2]) }
	setDraw := func(c [3]int) { pdf.SetDrawColor(c[0], c[1], c[2]) }
	txt := sanitizePDFText

	// Compact running header on continuation pages.
	pdf.SetHeaderFunc(func() {
		if pdf.PageNo() == 1 {
			return
		}
		setFill(stNavy)
		pdf.Rect(0, 0, pageW, 13, "F")
		setFill(stGold)
		pdf.Rect(0, 13, pageW, 0.7, "F")
		pdf.SetXY(margin, 3.5)
		pdf.SetFont("Arial", "B", 9.5)
		pdf.SetTextColor(255, 255, 255)
		pdf.CellFormat(usable*0.5, 6, txt(truncatePDF(business.Name, 44)), "", 0, "L", false, 0, "")
		pdf.SetFont("Arial", "", 7.5)
		setText(stNavySoft)
		pdf.CellFormat(usable*0.5, 6, txt(truncatePDF(ledger.Party.Name, 30)+"  |  "+periodText), "", 0, "R", false, 0, "")
		pdf.SetY(20)
	})

	pdf.SetFooterFunc(func() {
		y := pageH - 13
		setDraw(stHairline)
		pdf.SetLineWidth(0.2)
		pdf.Line(margin, y, pageW-margin, y)
		pdf.SetXY(margin, y+2)
		pdf.SetFont("Arial", "B", 7)
		setText(stSlate)
		name := txt(truncatePDF(business.Name, 40))
		nameW := pdf.GetStringWidth(name) + 1.5
		pdf.CellFormat(nameW, 4, name, "", 0, "L", false, 0, "")
		pdf.SetFont("Arial", "", 7)
		setText(stMuted)
		pdf.CellFormat(usable-nameW-30, 4, "|  Computer-generated statement - no signature required", "", 0, "L", false, 0, "")
		pdf.CellFormat(30, 4, fmt.Sprintf("Page %d of {nb}", pdf.PageNo()), "", 0, "R", false, 0, "")
	})

	pdf.AddPage()

	// ------------------------------------------------------------------
	// Brand band
	// ------------------------------------------------------------------
	const bandH = 42.0
	setFill(stNavy)
	pdf.Rect(0, 0, pageW, bandH, "F")
	setFill(stGold)
	pdf.Rect(0, bandH, pageW, 1, "F")

	const logoBox = 22.0
	logoX, logoY := margin, (bandH-logoBox)/2
	drewLogo := false
	if business.LogoURL != "" {
		if data, imgType := fetchThermalLogoBytes(business.LogoURL); len(data) > 0 {
			if info, name := safeRegisterImage(pdf, "statement-logo", imgType, data); info != nil && info.Width() > 0 && info.Height() > 0 {
				pdf.SetFillColor(255, 255, 255)
				pdf.RoundedRect(logoX, logoY, logoBox, logoBox, 2.5, "1234", "F")
				inner := logoBox - 3
				w, h := inner, inner*info.Height()/info.Width()
				if h > inner {
					h, w = inner, inner*info.Width()/info.Height()
				}
				pdf.ImageOptions(name, logoX+(logoBox-w)/2, logoY+(logoBox-h)/2, w, h, false, fpdf.ImageOptions{}, 0, "")
				drewLogo = true
			}
		}
	}
	if !drewLogo {
		// Monogram fallback.
		setFill(stGold)
		pdf.RoundedRect(logoX, logoY, logoBox, logoBox, 2.5, "1234", "F")
		pdf.SetFont("Arial", "B", 15)
		setText(stNavy)
		pdf.SetXY(logoX, logoY)
		pdf.CellFormat(logoBox, logoBox, txt(statementInitials(business.Name)), "", 0, "C", false, 0, "")
	}

	infoX := logoX + logoBox + 5
	infoW := usable*0.62 - logoBox - 5
	pdf.SetXY(infoX, logoY-0.5)
	pdf.SetFont("Arial", "B", 15)
	pdf.SetTextColor(255, 255, 255)
	pdf.CellFormat(infoW, 7.5, txt(truncatePDF(business.Name, 40)), "", 2, "L", false, 0, "")
	pdf.SetFont("Arial", "", 7.8)
	setText(stNavySoft)
	addr := joinNonEmpty(", ", business.Address, business.City, joinNonEmpty(" - ", business.State, business.Pincode))
	if addr != "" {
		lines := pdf.SplitText(txt(addr), infoW)
		if len(lines) > 2 {
			lines = lines[:2]
			lines[1] = strings.TrimRight(lines[1], " ,") + "..."
		}
		for _, l := range lines {
			pdf.SetX(infoX)
			pdf.CellFormat(infoW, 3.9, l, "", 2, "L", false, 0, "")
		}
	}
	for _, line := range []string{
		joinNonEmpty("   ", prefixIf("Ph ", business.Phone), business.Email),
		joinNonEmpty("   ", prefixIf("GSTIN ", business.GSTIN), prefixIf("PAN ", business.PAN)),
	} {
		if line != "" {
			pdf.SetX(infoX)
			pdf.CellFormat(infoW, 3.9, txt(truncatePDF(line, 80)), "", 2, "L", false, 0, "")
		}
	}

	rightW := usable * 0.38
	rightX := pageW - margin - rightW
	pdf.SetXY(rightX, logoY-0.5)
	pdf.SetFont("Arial", "B", 7)
	setText(stGold)
	pdf.CellFormat(rightW, 4, "ACCOUNT", "", 2, "R", false, 0, "")
	pdf.SetX(rightX)
	pdf.SetFont("Arial", "B", 18)
	pdf.SetTextColor(255, 255, 255)
	pdf.CellFormat(rightW, 9, "Statement", "", 2, "R", false, 0, "")
	pdf.SetX(rightX)
	pdf.SetFont("Arial", "", 8)
	setText(stNavySoft)
	pdf.CellFormat(rightW, 4.5, txt(periodText), "", 2, "R", false, 0, "")

	// ------------------------------------------------------------------
	// Party + statement details cards
	// ------------------------------------------------------------------
	cardY := bandH + 8
	gap := 6.0
	leftW := usable*0.58 - gap/2
	detailW := usable - leftW - gap

	sectionLabel := func(x, y, w float64, label string) {
		pdf.SetXY(x, y)
		pdf.SetFont("Arial", "B", 7)
		setText(stGold)
		pdf.CellFormat(w, 4, label, "", 2, "L", false, 0, "")
	}

	forLabel := "STATEMENT FOR"
	sectionLabel(margin, cardY, leftW, forLabel)
	pdf.SetX(margin)
	pdf.SetFont("Arial", "B", 12.5)
	setText(stInk)
	pdf.CellFormat(leftW, 6.5, txt(truncatePDF(ledger.Party.Name, 46)), "", 2, "L", false, 0, "")
	pdf.SetFont("Arial", "", 8)
	setText(stSlate)
	partyAddr := joinNonEmpty(", ", ledger.Party.Address, ledger.Party.City, joinNonEmpty(" - ", ledger.Party.State, ledger.Party.Pincode))
	if partyAddr != "" {
		lines := pdf.SplitText(txt(partyAddr), leftW)
		if len(lines) > 2 {
			lines = lines[:2]
			lines[1] = strings.TrimRight(lines[1], " ,") + "..."
		}
		for _, l := range lines {
			pdf.SetX(margin)
			pdf.CellFormat(leftW, 4.2, l, "", 2, "L", false, 0, "")
		}
	}
	for _, line := range []string{
		joinNonEmpty("   ", prefixIf("Ph ", ledger.Party.Phone), ledger.Party.Email),
		prefixIf("GSTIN ", ledger.Party.GSTIN),
	} {
		if line != "" {
			pdf.SetX(margin)
			pdf.CellFormat(leftW, 4.2, txt(truncatePDF(line, 80)), "", 2, "L", false, 0, "")
		}
	}
	leftBottom := pdf.GetY()

	// Details panel.
	accountType := "Customer"
	if isVendor {
		accountType = "Supplier"
	}
	txnCount := 0
	for _, e := range ledger.Entries {
		if e.Type != "opening_balance" && e.Type != "closing_balance" {
			txnCount++
		}
	}
	details := [][2]string{
		{"Statement date", time.Now().In(loc).Format("02 Jan 2006")},
		{"Period from", statementLongDate(ledger.FromDate)},
		{"Period to", statementLongDate(ledger.ToDate)},
		{"Account type", accountType},
		{"Transactions", fmt.Sprintf("%d", txnCount)},
	}
	detailX := margin + leftW + gap
	panelH := 6 + float64(len(details))*5.2 + 2
	setFill(stPanel)
	pdf.RoundedRect(detailX, cardY, detailW, panelH, 2, "1234", "F")
	pdf.SetXY(detailX+4, cardY+3)
	pdf.SetFont("Arial", "B", 7)
	setText(stGold)
	pdf.CellFormat(detailW-8, 4, "STATEMENT DETAILS", "", 2, "L", false, 0, "")
	for _, kv := range details {
		pdf.SetX(detailX + 4)
		pdf.SetFont("Arial", "", 7.8)
		setText(stSlate)
		pdf.CellFormat((detailW-8)*0.5, 5.2, kv[0], "", 0, "L", false, 0, "")
		pdf.SetFont("Arial", "B", 7.8)
		setText(stInk)
		pdf.CellFormat((detailW-8)*0.5, 5.2, txt(kv[1]), "", 2, "R", false, 0, "")
	}

	// ------------------------------------------------------------------
	// Balance hero
	// ------------------------------------------------------------------
	heroY := math.Max(leftBottom, cardY+panelH) + 7
	const heroH = 26.0
	closing, opening := ledger.ClosingBalance, ledger.OpeningBalance
	sideColor := statementSideColor(closing)

	setFill(stPanel)
	pdf.RoundedRect(margin, heroY, usable, heroH, 2, "1234", "F")
	setFill(sideColor)
	pdf.Rect(margin, heroY, 1.6, heroH, "F")

	heroLabel := "CLOSING BALANCE"
	if !isVendor {
		switch {
		case closing > 0.005:
			heroLabel = "TOTAL AMOUNT DUE"
		case closing < -0.005:
			heroLabel = "ADVANCE BALANCE"
		default:
			heroLabel = "ACCOUNT SETTLED"
		}
	}
	heroLeftW := usable * 0.36
	pdf.SetXY(margin+6, heroY+4)
	pdf.SetFont("Arial", "B", 7)
	setText(stSlate)
	pdf.CellFormat(heroLeftW, 4, heroLabel, "", 2, "L", false, 0, "")
	pdf.SetX(margin + 6)
	pdf.SetFont("Arial", "B", 19)
	setText(sideColor)
	pdf.CellFormat(heroLeftW, 10, txt("Rs. "+statementBalanceText(closing)), "", 2, "L", false, 0, "")
	pdf.SetX(margin + 6)
	pdf.SetFont("Arial", "", 7.5)
	setText(stMuted)
	pdf.CellFormat(heroLeftW, 4, txt("as on "+statementLongDate(ledger.ToDate)), "", 2, "L", false, 0, "")

	docLabel, docTotal := "Sales", ledger.TotalSales
	moneyLabel, moneyTotal := "Payments Received", ledger.TotalReceived
	// Other = everything else that moved the balance (returns, notes, refunds).
	other := ledger.ClosingBalance - ledger.OpeningBalance - ledger.TotalSales + ledger.TotalReceived
	if isVendor {
		docLabel, docTotal = "Purchases", ledger.TotalPurchases
		moneyLabel, moneyTotal = "Payments Made", ledger.TotalPaid
		other = ledger.ClosingBalance - ledger.OpeningBalance + ledger.TotalPurchases - ledger.TotalPaid
	}
	hasOther := math.Abs(other) >= 0.005
	signed := func(v float64) string {
		if v < 0 {
			return "- " + formatINRAmount(-v)
		}
		return "+ " + formatINRAmount(v)
	}

	metrics := [][2]string{
		{"Previous Balance", statementBalanceText(opening)},
		{docLabel, formatINRAmount(docTotal)},
		{moneyLabel, formatINRAmount(moneyTotal)},
	}
	if hasOther {
		metrics = append(metrics, [2]string{"Adjustments", signed(other)})
	}
	mx := margin + 6 + heroLeftW
	mw := (margin + usable - 4 - mx) / float64(len(metrics))
	setDraw(stHairline)
	pdf.SetLineWidth(0.25)
	for i, m := range metrics {
		x := mx + float64(i)*mw
		pdf.Line(x, heroY+5, x, heroY+heroH-5)
		pdf.SetXY(x+4, heroY+8)
		pdf.SetFont("Arial", "", 7)
		setText(stSlate)
		pdf.CellFormat(mw-6, 4, m[0], "", 2, "L", false, 0, "")
		pdf.SetX(x + 4)
		pdf.SetFont("Arial", "B", 9.5)
		setText(stInk)
		if i == 0 {
			setText(statementSideColor(opening))
		}
		pdf.CellFormat(mw-6, 6, txt(m[1]), "", 2, "L", false, 0, "")
	}

	// ------------------------------------------------------------------
	// Transactions table
	// ------------------------------------------------------------------
	pdf.SetXY(margin, heroY+heroH+8)
	pdf.SetFont("Arial", "B", 10.5)
	setText(stInk)
	pdf.CellFormat(usable*0.6, 6, "Transaction Details", "", 0, "L", false, 0, "")
	pdf.SetFont("Arial", "", 7.5)
	setText(stMuted)
	pdf.CellFormat(usable*0.4, 6, "Amounts in INR", "", 1, "R", false, 0, "")
	setFill(stGold)
	pdf.Rect(margin, pdf.GetY()+0.5, 12, 0.7, "F")
	pdf.Ln(3.5)

	debitHead, creditHead := "Sales (Dr)", "Received (Cr)"
	if isVendor {
		debitHead, creditHead = "Debit", "Credit"
	}
	heads := []string{"#", "Date", "Particulars", "Ref No.", debitHead, creditHead, "Balance"}
	widths := []float64{8, 21, 50, 29, 25, 25, 28}
	scale := usable / sumFloats(widths)
	for i := range widths {
		widths[i] *= scale
	}
	aligns := []string{"C", "L", "L", "L", "R", "R", "R"}
	const pad = 2.0

	drawHeader := func() {
		y := pdf.GetY()
		setFill(stNavy)
		pdf.RoundedRect(margin, y, usable, 8, 1.5, "12", "F")
		pdf.SetFont("Arial", "B", 7.3)
		pdf.SetTextColor(255, 255, 255)
		x := margin
		for i, h := range heads {
			pdf.SetXY(x+pad, y)
			pdf.CellFormat(widths[i]-2*pad, 8, h, "", 0, aligns[i], false, 0, "")
			x += widths[i]
		}
		pdf.SetXY(margin, y+8)
	}

	const rowH = 7.0
	bottomLimit := pageH - 20
	drawRow := func(cells []string, fill [3]int, colors [][3]int, bold bool) {
		y := pdf.GetY()
		setFill(fill)
		pdf.Rect(margin, y, usable, rowH, "F")
		setDraw(stHairline)
		pdf.SetLineWidth(0.2)
		pdf.Line(margin, y+rowH, margin+usable, y+rowH)
		style := ""
		if bold {
			style = "B"
		}
		x := margin
		for i, v := range cells {
			pdf.SetFont("Arial", style, 7.6)
			if i == 6 {
				pdf.SetFont("Arial", "B", 7.6)
			}
			setText(colors[i])
			pdf.SetXY(x+pad, y)
			pdf.CellFormat(widths[i]-2*pad, rowH, txt(v), "", 0, aligns[i], false, 0, "")
			x += widths[i]
		}
		pdf.SetXY(margin, y+rowH)
	}

	drawHeader()
	n := 0
	for _, e := range ledger.Entries {
		if pdf.GetY()+rowH > bottomLimit {
			pdf.AddPage()
			drawHeader()
		}
		colors := [][3]int{stMuted, stSlate, stInk, stSlate, statementDebitText, statementCreditText, statementSideColor(e.Balance)}
		if e.Type == "opening_balance" || e.Type == "closing_balance" {
			label, when := "Opening Balance", statementDisplayDate(ledger.FromDate)
			if e.Type == "closing_balance" {
				label, when = "Closing Balance", statementDisplayDate(ledger.ToDate)
			}
			colors[1], colors[2] = stInk, stNavy
			drawRow([]string{"", when, label, "", "", "", statementBalanceText(e.Balance)}, stSpecialBg, colors, true)
			continue
		}
		n++
		fill := [3]int{255, 255, 255}
		if n%2 == 0 {
			fill = stZebra
		}
		cells := []string{
			fmt.Sprintf("%d", n),
			statementDisplayDate(e.Date),
			statementParticulars(e),
			truncatePDF(e.RefNumber, 18),
			amountOrDash(e.Debit),
			amountOrDash(e.Credit),
			statementBalanceText(e.Balance),
		}
		if cells[4] == "-" {
			colors[4] = stMuted
		}
		if cells[5] == "-" {
			colors[5] = stMuted
		}
		drawRow(cells, fill, colors, false)
	}
	if n == 0 {
		if pdf.GetY()+10 > bottomLimit {
			pdf.AddPage()
			drawHeader()
		}
		pdf.SetFont("Arial", "I", 8)
		setText(stMuted)
		pdf.CellFormat(usable, 10, "No transactions in this period.", "", 1, "C", false, 0, "")
	}

	// Totals row.
	if pdf.GetY()+rowH+1 > bottomLimit {
		pdf.AddPage()
		drawHeader()
	}
	ty := pdf.GetY()
	setDraw(stNavy)
	pdf.SetLineWidth(0.5)
	pdf.Line(margin, ty, margin+usable, ty)
	pdf.SetLineWidth(0.2)
	pdf.SetFont("Arial", "B", 7.8)
	setText(stInk)
	labelW := widths[0] + widths[1] + widths[2] + widths[3]
	pdf.SetXY(margin+pad, ty)
	pdf.CellFormat(labelW-2*pad, rowH+1, "Total for the period", "", 0, "R", false, 0, "")
	x := margin + labelW
	for i, v := range []struct {
		s string
		c [3]int
	}{
		{formatINRAmount(ledger.TotalDebit), statementDebitText},
		{formatINRAmount(ledger.TotalCredit), statementCreditText},
		{statementBalanceText(ledger.ClosingBalance), statementSideColor(ledger.ClosingBalance)},
	} {
		setText(v.c)
		pdf.SetXY(x+pad, ty)
		pdf.CellFormat(widths[4+i]-2*pad, rowH+1, txt(v.s), "", 0, "R", false, 0, "")
		x += widths[4+i]
	}
	pdf.SetXY(margin, ty+rowH+1)
	setDraw(stHairline)
	pdf.Line(margin, pdf.GetY(), margin+usable, pdf.GetY())

	// ------------------------------------------------------------------
	// Summary + payment details
	// ------------------------------------------------------------------
	type kv struct {
		label, value string
		color        [3]int
	}
	summary := []kv{
		{"Opening balance", statementBalanceText(opening), statementSideColor(opening)},
		{"Add: " + docLabel, formatINRAmount(docTotal), stInk},
		{"Less: " + moneyLabel, formatINRAmount(moneyTotal), stInk},
	}
	if hasOther {
		summary = append(summary, kv{"Returns & adjustments", signed(other), stInk})
	}
	var bank []kv
	for _, b := range []kv{
		{"Bank", business.BankName, stInk},
		{"Account No.", business.AccountNumber, stInk},
		{"IFSC", business.IFSCCode, stInk},
		{"UPI ID", business.UPIID, stInk},
	} {
		if strings.TrimSpace(b.value) != "" {
			bank = append(bank, b)
		}
	}

	const lineH = 5.6
	sumH := 9 + float64(len(summary))*lineH + 11
	bankH := 9 + float64(len(bank))*lineH + 2
	blockH := math.Max(sumH, bankH) + 22
	if pdf.GetY()+8+blockH > bottomLimit {
		pdf.AddPage()
	} else {
		pdf.Ln(8)
	}
	blockY := pdf.GetY()

	sumW := usable * 0.45
	sumX := margin + usable - sumW
	setFill(stPanel)
	pdf.RoundedRect(sumX, blockY, sumW, sumH, 2, "1234", "F")
	pdf.SetXY(sumX+5, blockY+4)
	pdf.SetFont("Arial", "B", 7)
	setText(stGold)
	pdf.CellFormat(sumW-10, 4, "ACCOUNT SUMMARY", "", 2, "L", false, 0, "")
	pdf.Ln(1)
	for _, s := range summary {
		pdf.SetX(sumX + 5)
		pdf.SetFont("Arial", "", 8)
		setText(stSlate)
		pdf.CellFormat((sumW-10)*0.55, lineH, txt(s.label), "", 0, "L", false, 0, "")
		pdf.SetFont("Arial", "B", 8)
		setText(s.color)
		pdf.CellFormat((sumW-10)*0.45, lineH, txt(s.value), "", 2, "R", false, 0, "")
	}
	ly := pdf.GetY() + 1.5
	setDraw(stNavy)
	pdf.SetLineWidth(0.35)
	pdf.Line(sumX+5, ly, sumX+sumW-5, ly)
	pdf.SetLineWidth(0.2)
	pdf.SetXY(sumX+5, ly+1.5)
	pdf.SetFont("Arial", "B", 9)
	setText(stInk)
	pdf.CellFormat((sumW-10)*0.5, 7, "Closing balance", "", 0, "L", false, 0, "")
	pdf.SetFont("Arial", "B", 11)
	setText(sideColor)
	pdf.CellFormat((sumW-10)*0.5, 7, txt("Rs. "+statementBalanceText(closing)), "", 2, "R", false, 0, "")

	leftBlockW := usable - sumW - gap
	if len(bank) > 0 && !isVendor {
		setDraw(stHairline)
		pdf.RoundedRect(margin, blockY, leftBlockW, bankH, 2, "1234", "D")
		pdf.SetXY(margin+5, blockY+4)
		pdf.SetFont("Arial", "B", 7)
		setText(stGold)
		pdf.CellFormat(leftBlockW-10, 4, "PAYMENT DETAILS", "", 2, "L", false, 0, "")
		pdf.Ln(1)
		for _, b := range bank {
			pdf.SetX(margin + 5)
			pdf.SetFont("Arial", "", 8)
			setText(stSlate)
			pdf.CellFormat(24, lineH, b.label, "", 0, "L", false, 0, "")
			pdf.SetFont("Arial", "B", 8)
			setText(stInk)
			pdf.CellFormat(leftBlockW-34, lineH, txt(truncatePDF(b.value, 40)), "", 2, "L", false, 0, "")
		}
	} else {
		pdf.SetXY(margin, blockY+2)
		pdf.SetFont("Arial", "B", 9)
		setText(stInk)
		pdf.CellFormat(leftBlockW, 5, "Thank you for your business.", "", 2, "L", false, 0, "")
		pdf.SetFont("Arial", "", 7.8)
		setText(stSlate)
		contact := joinNonEmpty(" or ", business.Phone, business.Email)
		msg := "For any queries regarding this statement, please contact us"
		if contact != "" {
			msg += " at " + contact
		}
		pdf.MultiCell(leftBlockW, 4.2, txt(msg+"."), "", "L", false)
	}

	// Notes.
	pdf.SetXY(margin, blockY+math.Max(sumH, bankH)+7)
	setDraw(stHairline)
	pdf.Line(margin, pdf.GetY(), margin+usable, pdf.GetY())
	pdf.Ln(3)
	pdf.SetFont("Arial", "B", 7)
	setText(stSlate)
	pdf.CellFormat(usable, 4, "NOTES", "", 1, "L", false, 0, "")
	pdf.SetFont("Arial", "", 7.3)
	setText(stMuted)
	pdf.MultiCell(usable, 3.9, "Dr = amount due from you to us;  Cr = advance / amount due to you.  "+
		"Please report any discrepancy within 7 days of receiving this statement, after which it will be treated as correct. "+
		"Generated on "+time.Now().In(loc).Format("02 Jan 2006, 03:04 PM")+".", "", "L", false)

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// statementLongDate renders 2026-10-01 as "01 Oct 2026".
func statementLongDate(iso string) string {
	t, err := time.Parse("2006-01-02", iso)
	if err != nil {
		return iso
	}
	return t.Format("02 Jan 2006")
}

// statementInitials is the monogram shown when the business has no logo.
func statementInitials(name string) string {
	var out []rune
	for _, w := range strings.Fields(name) {
		for _, r := range w {
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				out = append(out, r)
				break
			}
		}
		if len(out) == 2 {
			break
		}
	}
	if len(out) == 0 {
		return "S"
	}
	return strings.ToUpper(string(out))
}

// statementSideColor is the text colour for a balance: red when Dr, green
// when Cr, neutral when settled.
func statementSideColor(v float64) [3]int {
	switch {
	case v > 0.005:
		return statementDebitText
	case v < -0.005:
		return statementCreditText
	}
	return stInk
}

// statementBalanceText renders a running balance with its Dr/Cr side.
func statementBalanceText(v float64) string {
	switch {
	case v > 0.005:
		return formatINRAmount(v) + " Dr"
	case v < -0.005:
		return formatINRAmount(-v) + " Cr"
	}
	return "0.00"
}

func statementParticulars(e PartyLedgerEntry) string {
	label := e.Voucher
	switch e.Type {
	case "invoice":
		label = "Sale"
	case "purchase_bill":
		label = "Purchase"
	}
	if mode := strings.TrimSpace(e.PaymentMode); mode != "" && mode != "-" {
		label += " (" + statementModeLabel(mode) + ")"
	}
	return truncatePDF(label, 30)
}

func statementModeLabel(mode string) string {
	switch strings.ToLower(mode) {
	case "upi":
		return "UPI"
	case "neft", "rtgs", "imps":
		return strings.ToUpper(mode)
	}
	mode = strings.ReplaceAll(mode, "_", " ")
	if mode == "" {
		return mode
	}
	return strings.ToUpper(mode[:1]) + mode[1:]
}

func amountOrDash(v float64) string {
	if v > 0.005 {
		return formatINRAmount(v)
	}
	return "-"
}

func sumFloats(v []float64) float64 {
	t := 0.0
	for _, x := range v {
		t += x
	}
	return t
}

func joinNonEmpty(sep string, parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}

func prefixIf(prefix, v string) string {
	if strings.TrimSpace(v) == "" {
		return ""
	}
	return prefix + strings.TrimSpace(v)
}
