package controllers

import (
	"bytes"
	"fmt"
	"html"
	"net/http"
	"sort"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/go-pdf/fpdf"
	"github.com/google/uuid"
	"github.com/tealeg/xlsx/v3"
)

// -----------------------------------------------------------------------------
// Party Statement (Ledger)
//
// A computed, myBillBook-style party ledger: every transaction document for a
// party listed chronologically with Debit/Credit columns and a running balance.
// Positive balance = the party owes the business (receivable); negative = the
// business owes the party (payable).
//
// Debit (+): sales invoice, payment out, purchase return, debit note.
// Credit (-): payment in, sales return, purchase bill, credit note.
//
// The opening balance is party.opening_balance plus the net delta of all
// qualifying documents dated before the requested range, so the ledger always
// reconciles: opening + Σ entries = closing. When the party has no documents
// at all, party.balance (e.g. an imported closing balance) is shown as the
// opening instead.
// -----------------------------------------------------------------------------

type partyLedgerRow struct {
	Type        string
	RefID       uuid.UUID
	RefNumber   string
	Date        time.Time
	CreatedAt   time.Time
	PaymentMode string
	Debit       float64
	Credit      float64
	DueDate     *time.Time
	DueStatus   string
	OverdueDays int
}

type PartyLedgerEntry struct {
	Type        string     `json:"type"`
	Voucher     string     `json:"voucher"`
	RefID       string     `json:"ref_id,omitempty"`
	RefNumber   string     `json:"ref_number"`
	Date        string     `json:"date"`
	PaymentMode string     `json:"payment_mode"`
	Debit       float64    `json:"debit"`
	Credit      float64    `json:"credit"`
	Balance     float64    `json:"balance"`
	DueDate     *time.Time `json:"due_date,omitempty"`
	DueStatus   string     `json:"due_status,omitempty"`
	OverdueDays int        `json:"overdue_days,omitempty"`
	// CreatedAt is when the document was recorded; with Date it orders entries
	// by date and time.
	CreatedAt *time.Time `json:"created_at,omitempty"`
}

type PartyLedger struct {
	Party          models.Party       `json:"party"`
	FromDate       string             `json:"from_date"`
	ToDate         string             `json:"to_date"`
	Label          string             `json:"label"`
	OpeningBalance float64            `json:"opening_balance"`
	ClosingBalance float64            `json:"closing_balance"`
	TotalDebit     float64            `json:"total_debit"`
	TotalCredit    float64            `json:"total_credit"`
	TotalSales     float64            `json:"total_sales"`
	TotalPurchases float64            `json:"total_purchases"`
	TotalReceived  float64            `json:"total_received"`
	TotalPaid      float64            `json:"total_paid"`
	OverdueAmount  float64            `json:"overdue_amount"`
	Entries        []PartyLedgerEntry `json:"entries"`
}

func partyLedgerDate(d time.Time) string {
	return d.Format("2006-01-02")
}

func ledgerDueStatusInvoice(inv models.Invoice, asOf time.Time) (string, int) {
	outstanding := inv.TotalAmount - inv.AmountPaid
	switch inv.Status {
	case "paid":
		return "paid", 0
	case "partial":
		return "partial", ledgerOverdueDays(inv.DueDate, outstanding, asOf)
	default:
		if inv.DueDate == nil {
			return "", 0
		}
		return "unpaid", ledgerOverdueDays(inv.DueDate, outstanding, asOf)
	}
}

func ledgerDueStatusBill(bill models.PurchaseBill, asOf time.Time) (string, int) {
	outstanding := bill.TotalAmount - bill.PaidAmount
	switch bill.Status {
	case "paid":
		return "paid", 0
	case "partial":
		return "partial", ledgerOverdueDays(bill.DueDate, outstanding, asOf)
	default:
		if bill.DueDate == nil {
			return "", 0
		}
		return "unpaid", ledgerOverdueDays(bill.DueDate, outstanding, asOf)
	}
}

func ledgerOverdueDays(dueDate *time.Time, outstanding float64, asOf time.Time) int {
	if dueDate == nil || outstanding <= 0.005 {
		return 0
	}
	days := int(asOf.Sub(*dueDate).Hours() / 24)
	if days < 0 {
		return 0
	}
	return days
}

// loadPartyLedgerRows fetches every balance-relevant document for the party
// (unfiltered by date) so the same set can seed the opening balance.
func loadPartyLedgerRows(userID, partyID uuid.UUID, asOf time.Time) []partyLedgerRow {
	var rows []partyLedgerRow

	var invoices []models.Invoice
	utils.DB.Where("user_id = ? AND party_id = ? AND status NOT IN ?", userID, partyID, []string{"draft", "cancelled"}).
		Find(&invoices)
	for _, inv := range invoices {
		dueStatus, overdueDays := ledgerDueStatusInvoice(inv, asOf)
		rows = append(rows, partyLedgerRow{
			Type: "invoice", RefID: inv.ID, RefNumber: inv.InvoiceNumber,
			Date: inv.Date, CreatedAt: inv.CreatedAt,
			Debit: inv.TotalAmount, DueDate: inv.DueDate, DueStatus: dueStatus,
			OverdueDays: overdueDays,
		})
	}

	var payments []models.Payment
	utils.DB.Where("user_id = ? AND party_id = ? AND COALESCE(status, '') != ?", userID, partyID, "cancelled").Find(&payments)
	for _, p := range payments {
		rows = append(rows, partyLedgerRow{
			Type: "payment_in", RefID: p.ID, RefNumber: p.PaymentInNumber,
			Date: p.Date, CreatedAt: p.CreatedAt, PaymentMode: p.Mode,
			Credit: p.AmountReceived - p.PaymentInDiscount,
		})
	}

	// Invoice-linked payment outs are money refunds tagged to a sale: the
	// refund settles in cash, not credit, so it has no receivable leg here
	// (including it as a debit would wrongly show the customer as owing the
	// refunded sum back).
	var paymentOuts []models.PaymentOut
	utils.DB.Where("user_id = ? AND party_id = ? AND invoice_id IS NULL AND COALESCE(status, '') != ?", userID, partyID, "cancelled").Find(&paymentOuts)
	for _, p := range paymentOuts {
		rows = append(rows, partyLedgerRow{
			Type: "payment_out", RefID: p.ID, RefNumber: p.PaymentOutNumber,
			Date: p.Date, CreatedAt: p.CreatedAt, PaymentMode: p.Mode,
			Debit: p.AmountPaid - p.PaymentOutDiscount,
		})
	}

	var salesReturns []models.SalesReturn
	utils.DB.Where("user_id = ? AND party_id = ? AND status = ?", userID, partyID, "processed").
		Find(&salesReturns)
	for _, sr := range salesReturns {
		setSalesReturnRefund(&sr)
		rows = append(rows, partyLedgerRow{
			Type: "sales_return", RefID: sr.ID, RefNumber: sr.ReturnNumber,
			Date: sr.Date, CreatedAt: sr.CreatedAt,
			Credit: sr.RefundAmount,
		})
	}

	var bills []models.PurchaseBill
	utils.DB.Where("user_id = ? AND party_id = ? AND status != ?", userID, partyID, "draft").
		Find(&bills)
	for _, bill := range bills {
		dueStatus, overdueDays := ledgerDueStatusBill(bill, asOf)
		rows = append(rows, partyLedgerRow{
			Type: "purchase_bill", RefID: bill.ID, RefNumber: bill.BillNumber,
			Date: bill.BillDate, CreatedAt: bill.CreatedAt,
			Credit: bill.TotalAmount, DueDate: bill.DueDate, DueStatus: dueStatus,
			OverdueDays: overdueDays,
		})
	}

	var purchaseReturns []models.PurchaseReturn
	utils.DB.Where("user_id = ? AND party_id = ? AND status = ?", userID, partyID, "processed").
		Find(&purchaseReturns)
	for _, pr := range purchaseReturns {
		rows = append(rows, partyLedgerRow{
			Type: "purchase_return", RefID: pr.ID, RefNumber: pr.ReturnNumber,
			Date: pr.Date, CreatedAt: pr.CreatedAt,
			Debit: pr.Amount,
		})
	}

	var creditNotes []models.CreditNote
	utils.DB.Where("user_id = ? AND party_id = ? AND status = ?", userID, partyID, "issued").
		Find(&creditNotes)
	for _, cn := range creditNotes {
		rows = append(rows, partyLedgerRow{
			Type: "credit_note", RefID: cn.ID, RefNumber: cn.CreditNoteNumber,
			Date: cn.Date, CreatedAt: cn.CreatedAt,
			Credit: cn.TotalAmount,
		})
	}

	var debitNotes []models.DebitNote
	utils.DB.Where("user_id = ? AND party_id = ? AND status = ?", userID, partyID, "issued").
		Find(&debitNotes)
	for _, dn := range debitNotes {
		rows = append(rows, partyLedgerRow{
			Type: "debit_note", RefID: dn.ID, RefNumber: dn.DebitNoteNumber,
			Date: dn.Date, CreatedAt: dn.CreatedAt,
			Debit: dn.TotalAmount,
		})
	}

	sort.SliceStable(rows, func(i, j int) bool {
		di, dj := partyLedgerDate(rows[i].Date), partyLedgerDate(rows[j].Date)
		if di != dj {
			return di < dj
		}
		if !rows[i].CreatedAt.Equal(rows[j].CreatedAt) {
			return rows[i].CreatedAt.Before(rows[j].CreatedAt)
		}
		return rows[i].RefNumber < rows[j].RefNumber
	})

	return rows
}

var partyLedgerVouchers = map[string]string{
	"invoice":         "Sales Invoices",
	"payment_in":      "Payment In",
	"payment_out":     "Payment Out",
	"sales_return":    "Sales Return",
	"purchase_bill":   "Purchase Invoices",
	"purchase_return": "Purchase Return",
	"credit_note":     "Credit Note",
	"debit_note":      "Debit Note",
}

func loadPartyLedger(userID, partyID uuid.UUID, fromDate, toDate string) (*PartyLedger, error) {
	var party models.Party
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, partyID).First(&party).Error; err != nil {
		return nil, fmt.Errorf("party not found")
	}

	asOf := time.Now()
	if toDate != "" {
		if t, err := time.Parse("2006-01-02", toDate); err == nil {
			asOf = t.Add(24*time.Hour - time.Nanosecond)
		}
	}

	rows := loadPartyLedgerRows(userID, partyID, asOf)

	ledger := &PartyLedger{
		Party:    party,
		FromDate: fromDate,
		ToDate:   toDate,
		Entries:  []PartyLedgerEntry{},
	}
	switch {
	case fromDate != "" && toDate != "":
		ledger.Label = fmt.Sprintf("%s – %s", fromDate, toDate)
	case fromDate != "":
		ledger.Label = "From " + fromDate
	case toDate != "":
		ledger.Label = "Till " + toDate
	default:
		ledger.Label = "All time"
	}

	opening := party.OpeningBalance
	if opening == 0 && len(rows) == 0 {
		// A party imported from the balance file alone carries only its
		// closing balance with no underlying documents — show it as the
		// opening so the statement still reflects the payable/receivable.
		opening = party.Balance
	}
	var inRange []partyLedgerRow
	for _, r := range rows {
		d := partyLedgerDate(r.Date)
		if fromDate != "" && d < fromDate {
			opening += r.Debit - r.Credit
			continue
		}
		if toDate != "" && d > toDate {
			continue
		}
		inRange = append(inRange, r)
	}
	ledger.OpeningBalance = opening

	// Overdue: outstanding on unpaid invoices/bills whose due date has passed asOf.
	utils.DB.Model(&models.Invoice{}).
		Where("user_id = ? AND party_id = ? AND status NOT IN ? AND due_date IS NOT NULL AND due_date < ? AND total_amount > amount_paid",
			userID, partyID, []string{"draft", "cancelled", "paid"}, asOf).
		Select("COALESCE(SUM(total_amount - amount_paid), 0)").
		Scan(&ledger.OverdueAmount)
	var payableOverdue float64
	utils.DB.Model(&models.PurchaseBill{}).
		Where("user_id = ? AND party_id = ? AND status IN ? AND due_date IS NOT NULL AND due_date < ? AND total_amount > paid_amount",
			userID, partyID, []string{"unpaid", "partial"}, asOf).
		Select("COALESCE(SUM(total_amount - paid_amount), 0)").
		Scan(&payableOverdue)
	ledger.OverdueAmount += payableOverdue

	// Opening balance row.
	openingEntry := PartyLedgerEntry{
		Type: "opening_balance", Voucher: "Opening Balance",
		RefNumber: "-", Date: "", PaymentMode: "-", Balance: opening,
	}
	if opening > 0 {
		openingEntry.Debit = opening
	} else if opening < 0 {
		openingEntry.Credit = -opening
	}
	ledger.Entries = append(ledger.Entries, openingEntry)

	running := opening
	for _, r := range inRange {
		running += r.Debit - r.Credit
		entry := PartyLedgerEntry{
			Type:        r.Type,
			Voucher:     partyLedgerVouchers[r.Type],
			RefID:       r.RefID.String(),
			RefNumber:   r.RefNumber,
			Date:        partyLedgerDate(r.Date),
			PaymentMode: r.PaymentMode,
			Debit:       r.Debit,
			Credit:      r.Credit,
			Balance:     running,
			DueDate:     r.DueDate,
			DueStatus:   r.DueStatus,
			OverdueDays: r.OverdueDays,
		}
		if !r.CreatedAt.IsZero() {
			created := r.CreatedAt
			entry.CreatedAt = &created
		}
		ledger.Entries = append(ledger.Entries, entry)

		ledger.TotalDebit += r.Debit
		ledger.TotalCredit += r.Credit
		switch r.Type {
		case "invoice":
			ledger.TotalSales += r.Debit
		case "purchase_bill":
			ledger.TotalPurchases += r.Credit
		case "payment_in":
			ledger.TotalReceived += r.Credit
		case "payment_out":
			ledger.TotalPaid += r.Debit
		}
	}

	ledger.ClosingBalance = running

	closingEntry := PartyLedgerEntry{
		Type: "closing_balance", Voucher: "Closing Balance",
		RefNumber: "-", Date: "", PaymentMode: "-", Balance: running,
	}
	if running > 0 {
		closingEntry.Debit = running
	} else if running < 0 {
		closingEntry.Credit = -running
	}
	ledger.Entries = append(ledger.Entries, closingEntry)

	return ledger, nil
}

func parsePartyLedgerRange(c *gin.Context) (uuid.UUID, string, string, error) {
	partyID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return uuid.Nil, "", "", fmt.Errorf("invalid party id")
	}
	fromDate := strings.TrimSpace(c.Query("from_date"))
	toDate := strings.TrimSpace(c.Query("to_date"))
	for _, d := range []string{fromDate, toDate} {
		if d == "" {
			continue
		}
		if _, err := time.Parse("2006-01-02", d); err != nil {
			return uuid.Nil, "", "", fmt.Errorf("invalid date %q — use YYYY-MM-DD", d)
		}
	}
	if fromDate != "" && toDate != "" && toDate < fromDate {
		return uuid.Nil, "", "", fmt.Errorf("to_date must be on or after from_date")
	}
	return partyID, fromDate, toDate, nil
}

func GetPartyLedger(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	partyID, fromDate, toDate, err := parsePartyLedgerRange(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ledger, err := loadPartyLedger(userID, partyID, fromDate, toDate)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, ledger)
}

func ExportPartyLedgerExcel(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	partyID, fromDate, toDate, err := parsePartyLedgerRange(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ledger, err := loadPartyLedger(userID, partyID, fromDate, toDate)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	file := xlsx.NewFile()
	sheet, err := file.AddSheet("Party Statement")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create Excel sheet"})
		return
	}

	writeRow := func(cols ...string) {
		row := sheet.AddRow()
		for _, col := range cols {
			row.AddCell().SetValue(col)
		}
	}

	writeRow("Party Statement (Ledger)")
	writeRow("Party", ledger.Party.Name)
	writeRow("Party Type", ledger.Party.PartyType)
	if ledger.Party.GSTIN != "" {
		writeRow("GSTIN", ledger.Party.GSTIN)
	}
	writeRow("Period", ledger.Label)
	writeRow("")

	for _, card := range partyLedgerCards(ledger) {
		writeRow(card.label, fmt.Sprintf("%.2f", card.value))
	}
	writeRow("")

	header := sheet.AddRow()
	for _, h := range []string{"Date", "Voucher", "Sr No", "Payment Mode", "Credit (INR)", "Debit (INR)", "Balance (INR)", "Due Date (Overdue by)"} {
		cell := header.AddCell()
		cell.SetValue(h)
		cell.GetStyle().Font.Bold = true
	}

	for _, e := range ledger.Entries {
		row := sheet.AddRow()
		date := e.Date
		if date == "" {
			date = "-"
		}
		row.AddCell().SetValue(date)
		row.AddCell().SetValue(e.Voucher)
		row.AddCell().SetValue(e.RefNumber)
		mode := e.PaymentMode
		if mode == "" {
			mode = "-"
		}
		row.AddCell().SetValue(mode)
		if e.Credit > 0 {
			cell := row.AddCell()
			cell.SetFloat(e.Credit)
			cell.SetFormat("#,##0.00")
		} else {
			row.AddCell().SetValue("-")
		}
		if e.Debit > 0 {
			cell := row.AddCell()
			cell.SetFloat(e.Debit)
			cell.SetFormat("#,##0.00")
		} else {
			row.AddCell().SetValue("-")
		}
		bal := row.AddCell()
		bal.SetFloat(e.Balance)
		bal.SetFormat("#,##0.00")
		row.AddCell().SetValue(ledgerDueCell(e))
	}

	filename := fmt.Sprintf("party_statement_%s_%s.xlsx",
		strings.ReplaceAll(strings.ToLower(ledger.Party.Name), " ", "_"), accountingExportStamp())
	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	if err := file.Write(c.Writer); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to write Excel file"})
		return
	}
}

func accountingExportStamp() string {
	return time.Now().Format("2006-01-02")
}

type partyLedgerCard struct {
	label string
	value float64
}

// partyLedgerCards returns the four summary figures shown above the ledger
// table. Receivable/payable is sign-aware; the document and money totals follow
// the party type (customer → sales/received, vendor → purchases/paid).
func partyLedgerCards(ledger *PartyLedger) []partyLedgerCard {
	balanceLabel := "Total Receivable Amount"
	balanceValue := ledger.ClosingBalance
	if ledger.ClosingBalance < 0 {
		balanceLabel = "Total Payable Amount"
		balanceValue = -ledger.ClosingBalance
	}
	docLabel, docTotal := "Total Sales Amount", ledger.TotalSales
	moneyLabel, moneyTotal := "Total Received Amount", ledger.TotalReceived
	if ledger.Party.PartyType == "vendor" {
		docLabel, docTotal = "Total Purchase Amount", ledger.TotalPurchases
		moneyLabel, moneyTotal = "Total Paid Amount", ledger.TotalPaid
	}
	return []partyLedgerCard{
		{label: balanceLabel, value: balanceValue},
		{label: "Overdue Amount", value: ledger.OverdueAmount},
		{label: docLabel, value: docTotal},
		{label: moneyLabel, value: moneyTotal},
	}
}

func ledgerDueCell(e PartyLedgerEntry) string {
	switch e.DueStatus {
	case "paid":
		return "Paid"
	case "partial":
		return "Partially Paid"
	case "unpaid":
		if e.OverdueDays > 0 {
			return fmt.Sprintf("Overdue by %dd", e.OverdueDays)
		}
		return "Unpaid"
	}
	return "-"
}

func GetPartyLedgerPDF(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	partyID, fromDate, toDate, err := parsePartyLedgerRange(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ledger, err := loadPartyLedger(userID, partyID, fromDate, toDate)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	pdfBytes, err := buildPartyLedgerPDF(ledger)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate PDF"})
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", partyLedgerFilename(ledger, "pdf")))
	c.Data(http.StatusOK, "application/pdf", pdfBytes)
}

func esc(s string) string { return html.EscapeString(s) }

func fmtINR(v float64) string {
	return fmt.Sprintf("₹%.2f", v)
}

func partyLedgerFilename(ledger *PartyLedger, ext string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		case r == ' ':
			return '_'
		}
		return -1
	}, strings.ToLower(strings.TrimSpace(ledger.Party.Name)))
	if safe == "" {
		safe = "ledger"
	}
	return fmt.Sprintf("party_statement_%s.%s", safe, ext)
}

// buildPartyLedgerPDF renders the party statement as an A4 landscape PDF.
func buildPartyLedgerPDF(ledger *PartyLedger) ([]byte, error) {
	pdf := fpdf.New("L", "mm", "A4", "")
	pdf.SetMargins(12, 12, 12)
	pdf.SetAutoPageBreak(true, 14)
	pdf.AliasNbPages("")
	pdf.SetFooterFunc(func() {
		pdf.SetY(-10)
		pdf.SetFont("Arial", "", 8)
		pdf.SetTextColor(140, 140, 140)
		pdf.CellFormat(0, 5, fmt.Sprintf("Page %d of {nb}", pdf.PageNo()), "", 0, "R", false, 0, "")
	})
	pdf.AddPage()

	pageW, _ := pdf.GetPageSize()
	l, _, r, _ := pdf.GetMargins()
	usable := pageW - l - r

	// Header
	pdf.SetFont("Arial", "B", 16)
	pdf.SetTextColor(30, 30, 30)
	pdf.CellFormat(usable*0.6, 8, "Party Statement (Ledger)", "", 0, "L", false, 0, "")
	pdf.SetFont("Arial", "", 9)
	pdf.SetTextColor(110, 110, 110)
	pdf.CellFormat(usable*0.4, 8, "Generated: "+time.Now().Format("02-01-2006 15:04"), "", 1, "R", false, 0, "")
	pdf.CellFormat(0, 5, sanitizePDFText(ledger.Label), "", 1, "L", false, 0, "")
	pdf.Ln(3)

	// Party line
	pdf.SetFont("Arial", "B", 11)
	pdf.SetTextColor(30, 30, 30)
	pdf.CellFormat(0, 6, sanitizePDFText(ledger.Party.Name), "", 1, "L", false, 0, "")
	meta := []string{}
	if ledger.Party.Phone != "" {
		meta = append(meta, ledger.Party.Phone)
	}
	if ledger.Party.PartyType != "" {
		meta = append(meta, ledger.Party.PartyType)
	}
	if ledger.Party.GSTIN != "" {
		meta = append(meta, "GSTIN: "+ledger.Party.GSTIN)
	}
	if len(meta) > 0 {
		pdf.SetFont("Arial", "", 9)
		pdf.SetTextColor(110, 110, 110)
		pdf.CellFormat(0, 5, sanitizePDFText(strings.Join(meta, " | ")), "", 1, "L", false, 0, "")
	}
	pdf.Ln(3)

	// Summary cards
	cards := partyLedgerCards(ledger)
	gap := 4.0
	cardW := (usable - gap*float64(len(cards)-1)) / float64(len(cards))
	cardY := pdf.GetY()
	pdf.SetDrawColor(220, 222, 226)
	for i, card := range cards {
		x := l + float64(i)*(cardW+gap)
		pdf.Rect(x, cardY, cardW, 16, "D")
		pdf.SetXY(x+3, cardY+2)
		pdf.SetFont("Arial", "", 8)
		pdf.SetTextColor(110, 110, 110)
		pdf.CellFormat(cardW-6, 4, sanitizePDFText(card.label), "", 2, "L", false, 0, "")
		pdf.SetFont("Arial", "B", 12)
		pdf.SetTextColor(30, 30, 30)
		pdf.CellFormat(cardW-6, 7, sanitizePDFText(fmtINR(card.value)), "", 0, "L", false, 0, "")
	}
	pdf.SetXY(l, cardY+20)

	// Table
	cols := []float64{
		usable * 0.10, usable * 0.14, usable * 0.12, usable * 0.11,
		usable * 0.12, usable * 0.12, usable * 0.13, usable * 0.16,
	}
	heads := []string{"Date", "Voucher", "Sr No", "Payment Mode", "Credit", "Debit", "Balance", "Due Date (Overdue by)"}
	aligns := []string{"L", "L", "L", "L", "R", "R", "R", "L"}
	drawHeader := func() {
		pdf.SetFillColor(243, 244, 246)
		pdf.SetDrawColor(220, 222, 226)
		pdf.SetFont("Arial", "B", 8)
		pdf.SetTextColor(90, 90, 90)
		for i, h := range heads {
			ln := 0
			if i == len(heads)-1 {
				ln = 1
			}
			pdf.CellFormat(cols[i], 8, h, "B", ln, aligns[i], true, 0, "")
		}
	}
	drawHeader()

	_, pageH := pdf.GetPageSize()
	_, _, _, bottom := pdf.GetMargins()
	rowH := 7.0
	for _, e := range ledger.Entries {
		if pdf.GetY()+rowH > pageH-bottom {
			pdf.AddPage()
			drawHeader()
		}
		special := e.Type == "opening_balance" || e.Type == "closing_balance"
		date := e.Date
		if date == "" {
			date = "-"
		}
		mode := e.PaymentMode
		if mode == "" {
			mode = "-"
		}
		credit, debit := "-", "-"
		if e.Credit > 0 {
			credit = fmtINR(e.Credit)
		}
		if e.Debit > 0 {
			debit = fmtINR(e.Debit)
		}
		values := []string{date, e.Voucher, e.RefNumber, mode, credit, debit, fmtINR(e.Balance), ledgerDueCell(e)}

		style := ""
		if special {
			style = "B"
			pdf.SetFillColor(249, 250, 251)
		}
		pdf.SetFont("Arial", style, 8)
		for i, v := range values {
			switch {
			case i == 4 && v != "-":
				pdf.SetTextColor(22, 163, 74)
			case i == 5 && v != "-":
				pdf.SetTextColor(220, 38, 38)
			case i == 7 && e.DueStatus == "paid":
				pdf.SetTextColor(22, 163, 74)
			case i == 7 && e.DueStatus == "partial":
				pdf.SetTextColor(217, 119, 6)
			case i == 7 && e.DueStatus == "unpaid":
				pdf.SetTextColor(220, 38, 38)
			default:
				pdf.SetTextColor(40, 40, 40)
			}
			ln := 0
			if i == len(values)-1 {
				ln = 1
			}
			pdf.CellFormat(cols[i], rowH, sanitizePDFText(v), "B", ln, aligns[i], special, 0, "")
		}
	}

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
