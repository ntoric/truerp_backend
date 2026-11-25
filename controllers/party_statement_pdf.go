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

// buildPartyStatementPDF renders the detailed periodic statement sent with a
// balance reminder: previous balance, then every sale, payment in and other
// balance-affecting document in date order (same-day entries in the order
// they were recorded) with a running balance, then the current balance. Balances are shown Dr (party owes the business)
// or Cr (advance / business owes the party).
func buildPartyStatementPDF(ledger *PartyLedger, business *models.Business, loc *time.Location) ([]byte, error) {
	if loc == nil {
		loc = time.Local
	}
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(12, 12, 12)
	pdf.SetAutoPageBreak(true, 16)
	pdf.AliasNbPages("")
	pdf.SetFooterFunc(func() {
		pdf.SetY(-11)
		pdf.SetFont("Arial", "I", 7)
		pdf.SetTextColor(140, 140, 140)
		pdf.CellFormat(0, 4, "This is a computer-generated statement.", "", 0, "L", false, 0, "")
		pdf.CellFormat(0, 4, fmt.Sprintf("Page %d of {nb}", pdf.PageNo()), "", 0, "R", false, 0, "")
	})
	pdf.AddPage()

	pageW, pageH := pdf.GetPageSize()
	left, _, right, _ := pdf.GetMargins()
	usable := pageW - left - right
	isVendor := ledger.Party.PartyType == "vendor"

	// --- Business header ---
	topY := pdf.GetY()
	pdf.SetFont("Arial", "B", 14)
	pdf.SetTextColor(30, 30, 30)
	pdf.CellFormat(usable*0.6, 7, sanitizePDFText(truncatePDF(business.Name, 48)), "", 2, "L", false, 0, "")
	pdf.SetFont("Arial", "", 8.5)
	pdf.SetTextColor(100, 100, 100)
	addr := joinNonEmpty(", ", business.Address, business.City, business.State)
	if addr != "" {
		pdf.MultiCell(usable*0.6, 4.2, sanitizePDFText(addr), "", "L", false)
	}
	contact := joinNonEmpty("  |  ", prefixIf("Ph: ", business.Phone), prefixIf("GSTIN: ", business.GSTIN))
	if contact != "" {
		pdf.CellFormat(usable*0.6, 4.2, sanitizePDFText(contact), "", 1, "L", false, 0, "")
	}
	headerBottom := pdf.GetY()

	pdf.SetXY(left+usable*0.6, topY)
	pdf.SetFont("Arial", "B", 13)
	pdf.SetTextColor(37, 99, 235)
	pdf.CellFormat(usable*0.4, 7, "STATEMENT OF ACCOUNT", "", 2, "R", false, 0, "")
	pdf.SetFont("Arial", "", 8.5)
	pdf.SetTextColor(100, 100, 100)
	pdf.CellFormat(usable*0.4, 4.5, sanitizePDFText(fmt.Sprintf("Period: %s to %s",
		statementDisplayDate(ledger.FromDate), statementDisplayDate(ledger.ToDate))), "", 2, "R", false, 0, "")
	pdf.CellFormat(usable*0.4, 4.5, "Generated: "+time.Now().In(loc).Format("02-01-2006 03:04 PM"), "", 2, "R", false, 0, "")

	pdf.SetY(math.Max(headerBottom, pdf.GetY()) + 2)
	pdf.SetDrawColor(220, 222, 226)
	pdf.Line(left, pdf.GetY(), pageW-right, pdf.GetY())
	pdf.Ln(3)

	// --- Party block ---
	pdf.SetFont("Arial", "", 8)
	pdf.SetTextColor(120, 120, 120)
	pdf.CellFormat(0, 4, "STATEMENT FOR", "", 1, "L", false, 0, "")
	pdf.SetFont("Arial", "B", 11)
	pdf.SetTextColor(30, 30, 30)
	pdf.CellFormat(0, 6, sanitizePDFText(ledger.Party.Name), "", 1, "L", false, 0, "")
	pdf.SetFont("Arial", "", 8.5)
	pdf.SetTextColor(100, 100, 100)
	partyMeta := joinNonEmpty("  |  ", prefixIf("Ph: ", ledger.Party.Phone), prefixIf("GSTIN: ", ledger.Party.GSTIN),
		joinNonEmpty(", ", ledger.Party.City, ledger.Party.State))
	if partyMeta != "" {
		pdf.CellFormat(0, 4.5, sanitizePDFText(partyMeta), "", 1, "L", false, 0, "")
	}
	pdf.Ln(3)

	// --- Summary boxes ---
	docLabel, docTotal := "Sales", ledger.TotalSales
	moneyLabel, moneyTotal := "Payments Received", ledger.TotalReceived
	// Other = everything else that moved the balance (returns, notes, refunds).
	other := ledger.ClosingBalance - ledger.OpeningBalance - ledger.TotalSales + ledger.TotalReceived
	if isVendor {
		docLabel, docTotal = "Purchases", ledger.TotalPurchases
		moneyLabel, moneyTotal = "Payments Made", ledger.TotalPaid
		other = ledger.ClosingBalance - ledger.OpeningBalance + ledger.TotalPurchases - ledger.TotalPaid
	}
	type box struct {
		label, value string
		strong       bool
		balance      *float64 // colour by Dr/Cr side
	}
	opening, closing := ledger.OpeningBalance, ledger.ClosingBalance
	boxes := []box{
		{"Previous Balance", "Rs. " + statementBalanceText(opening), false, &opening},
		{docLabel, "Rs. " + formatINRAmount(docTotal), false, nil},
		{moneyLabel, "Rs. " + formatINRAmount(moneyTotal), false, nil},
	}
	if math.Abs(other) >= 0.005 {
		sign := "+"
		if other < 0 {
			sign = "-"
		}
		boxes = append(boxes, box{"Returns & Adjustments", sign + " Rs. " + formatINRAmount(math.Abs(other)), false, nil})
	}
	boxes = append(boxes, box{"Current Balance", "Rs. " + statementBalanceText(closing), true, &closing})

	gap := 3.0
	boxW := (usable - gap*float64(len(boxes)-1)) / float64(len(boxes))
	boxY := pdf.GetY()
	for i, b := range boxes {
		x := left + float64(i)*(boxW+gap)
		if b.strong {
			fill, border := statementSideTint(*b.balance)
			pdf.SetFillColor(fill[0], fill[1], fill[2])
			pdf.SetDrawColor(border[0], border[1], border[2])
		} else {
			pdf.SetFillColor(255, 255, 255)
			pdf.SetDrawColor(220, 222, 226)
		}
		pdf.Rect(x, boxY, boxW, 15, "FD")
		pdf.SetXY(x+2.5, boxY+2)
		pdf.SetFont("Arial", "", 7.5)
		pdf.SetTextColor(110, 110, 110)
		pdf.CellFormat(boxW-5, 4, b.label, "", 2, "L", false, 0, "")
		pdf.SetFont("Arial", "B", 10)
		pdf.SetTextColor(30, 30, 30)
		if b.balance != nil {
			c := statementSideColor(*b.balance)
			pdf.SetTextColor(c[0], c[1], c[2])
		}
		pdf.CellFormat(boxW-5, 6, sanitizePDFText(b.value), "", 0, "L", false, 0, "")
	}
	pdf.SetXY(left, boxY+19)

	// --- Transactions table ---
	debitHead, creditHead := "Sales (Dr)", "Payment In (Cr)"
	if isVendor {
		debitHead, creditHead = "Debit", "Credit"
	}
	heads := []string{"#", "Date", "Particulars", "Ref No.", debitHead, creditHead, "Balance"}
	widths := []float64{7, 20, 51, 30, 25, 25, 28}
	scale := usable / sumFloats(widths)
	for i := range widths {
		widths[i] *= scale
	}
	aligns := []string{"R", "L", "L", "L", "R", "R", "R"}

	drawHeader := func() {
		pdf.SetFillColor(243, 244, 246)
		pdf.SetDrawColor(220, 222, 226)
		pdf.SetFont("Arial", "B", 7.5)
		pdf.SetTextColor(70, 70, 70)
		for i, h := range heads {
			ln := 0
			if i == len(heads)-1 {
				ln = 1
			}
			pdf.CellFormat(widths[i], 7, h, "TB", ln, aligns[i], true, 0, "")
		}
	}
	drawHeader()

	const rowH = 6.5
	_, _, _, bottom := pdf.GetMargins()
	n := 0
	for _, e := range ledger.Entries {
		if pdf.GetY()+rowH > pageH-bottom {
			pdf.AddPage()
			drawHeader()
		}
		special := e.Type == "opening_balance" || e.Type == "closing_balance"
		var cells []string
		// Debit rows (sales / purchases) are tinted red, credit rows
		// (payments) green; the running balance is red when Dr, green when Cr.
		rowFill := [3]int{255, 255, 255}
		if special {
			label := "Previous Balance"
			when := statementDisplayDate(ledger.FromDate)
			if e.Type == "closing_balance" {
				label = "Current Balance"
				when = statementDisplayDate(ledger.ToDate)
			}
			cells = []string{"", when, label, "", "", "", statementBalanceText(e.Balance)}
			rowFill = [3]int{249, 250, 251}
			pdf.SetFont("Arial", "B", 7.5)
		} else {
			n++
			cells = []string{
				fmt.Sprintf("%d", n),
				statementDisplayDate(e.Date),
				statementParticulars(e),
				truncatePDF(e.RefNumber, 18),
				amountOrDash(e.Debit),
				amountOrDash(e.Credit),
				statementBalanceText(e.Balance),
			}
			switch {
			case e.Debit > 0.005:
				rowFill = statementDebitFill
			case e.Credit > 0.005:
				rowFill = statementCreditFill
			}
			pdf.SetFont("Arial", "", 7.5)
		}
		pdf.SetFillColor(rowFill[0], rowFill[1], rowFill[2])
		for i, v := range cells {
			c := [3]int{40, 40, 40}
			switch {
			case i == 4 && v != "-" && v != "":
				c = statementDebitText
			case i == 5 && v != "-" && v != "":
				c = statementCreditText
			case i == 6:
				c = statementSideColor(e.Balance)
			}
			pdf.SetTextColor(c[0], c[1], c[2])
			ln := 0
			if i == len(cells)-1 {
				ln = 1
			}
			pdf.CellFormat(widths[i], rowH, sanitizePDFText(v), "B", ln, aligns[i], true, 0, "")
		}
	}
	if n == 0 {
		pdf.SetFont("Arial", "I", 8)
		pdf.SetTextColor(120, 120, 120)
		pdf.CellFormat(usable, 8, "No transactions in this period.", "B", 1, "C", false, 0, "")
	}

	// Totals row.
	pdf.SetFont("Arial", "B", 7.5)
	pdf.SetTextColor(30, 30, 30)
	pdf.SetFillColor(243, 244, 246)
	pdf.CellFormat(widths[0]+widths[1]+widths[2]+widths[3], rowH, "Total for the period", "TB", 0, "R", true, 0, "")
	pdf.SetTextColor(statementDebitText[0], statementDebitText[1], statementDebitText[2])
	pdf.CellFormat(widths[4], rowH, formatINRAmount(ledger.TotalDebit), "TB", 0, "R", true, 0, "")
	pdf.SetTextColor(statementCreditText[0], statementCreditText[1], statementCreditText[2])
	pdf.CellFormat(widths[5], rowH, formatINRAmount(ledger.TotalCredit), "TB", 0, "R", true, 0, "")
	cc := statementSideColor(ledger.ClosingBalance)
	pdf.SetTextColor(cc[0], cc[1], cc[2])
	pdf.CellFormat(widths[6], rowH, statementBalanceText(ledger.ClosingBalance), "TB", 1, "R", true, 0, "")

	pdf.Ln(4)
	pdf.SetFont("Arial", "", 7.5)
	pdf.SetTextColor(120, 120, 120)
	pdf.MultiCell(usable, 4, "Dr = amount due from you to us; Cr = advance / amount due to you. "+
		"Please report any discrepancy within 7 days of receiving this statement.", "", "L", false)

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Statement colours: debit (Dr) red, credit (Cr) green.
var (
	statementDebitText  = [3]int{185, 28, 28}
	statementCreditText = [3]int{21, 128, 61}
	statementDebitFill  = [3]int{254, 242, 242}
	statementCreditFill = [3]int{240, 253, 244}
)

// statementSideColor is the text colour for a balance: red when Dr, green
// when Cr, neutral when settled.
func statementSideColor(v float64) [3]int {
	switch {
	case v > 0.005:
		return statementDebitText
	case v < -0.005:
		return statementCreditText
	}
	return [3]int{40, 40, 40}
}

// statementSideTint is the fill and border for the Current Balance box.
func statementSideTint(v float64) (fill, border [3]int) {
	switch {
	case v > 0.005:
		return statementDebitFill, [3]int{252, 165, 165}
	case v < -0.005:
		return statementCreditFill, [3]int{134, 239, 172}
	}
	return [3]int{249, 250, 251}, [3]int{220, 222, 226}
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
