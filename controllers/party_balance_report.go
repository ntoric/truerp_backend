package controllers

import (
	"bytes"
	"fmt"
	"math"
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
	"gorm.io/gorm"
)

// -----------------------------------------------------------------------------
// Party Balance Report
//
// Lists every party with a non-zero balance on a given day: the opening
// balance (close of the previous day), the day's debit/credit movement and the
// closing balance (end of the day, or "now" for today since future-dated
// documents are excluded). Parties whose opening AND closing balances are both
// zero are omitted.
//
// Balances follow the Party Statement (Ledger) rules exactly, so each line
// reconciles with that party's ledger for the same date: positive = the party
// owes the business (receivable), negative = the business owes the party
// (payable). Instead of loading every document per party, each document type is
// aggregated with one grouped query.
// -----------------------------------------------------------------------------

type PartyBalanceLine struct {
	PartyID        uuid.UUID `json:"party_id"`
	PartyName      string    `json:"party_name"`
	PartyType      string    `json:"party_type"`
	Phone          string    `json:"phone"`
	OpeningBalance float64   `json:"opening_balance"`
	Debit          float64   `json:"debit"`
	Credit         float64   `json:"credit"`
	ClosingBalance float64   `json:"closing_balance"`
	Difference     float64   `json:"difference"` // closing - opening
}

type PartyBalanceReport struct {
	BusinessName      string             `json:"business_name"`
	Date              string             `json:"date"`
	PartyType         string             `json:"party_type"`
	OpeningReceivable float64            `json:"opening_receivable"`
	OpeningPayable    float64            `json:"opening_payable"`
	ClosingReceivable float64            `json:"closing_receivable"`
	ClosingPayable    float64            `json:"closing_payable"`
	TotalDebit        float64            `json:"total_debit"`
	TotalCredit       float64            `json:"total_credit"`
	NetOpening        float64            `json:"net_opening"`
	NetClosing        float64            `json:"net_closing"`
	NetDifference     float64            `json:"net_difference"`
	Lines             []PartyBalanceLine `json:"lines"`
}

// partyBalanceSource describes one ledger document type as a grouped query.
type partyBalanceSource struct {
	model   interface{}
	dateCol string
	amount  string // SQL expression for the ledger amount
	sign    float64
	where   string
	args    []interface{}
}

var partyBalanceSources = []partyBalanceSource{
	{&models.Invoice{}, "date", "total_amount", 1, "status NOT IN ?", []interface{}{[]string{"draft", "cancelled"}}},
	{&models.Payment{}, "date", "amount_received - payment_in_discount", -1, "COALESCE(status, '') != ?", []interface{}{"cancelled"}},
	// Invoice-linked payment outs are sale refunds settled in cash — no
	// receivable leg (mirrors loadPartyLedgerRows).
	{&models.PaymentOut{}, "date", "amount_paid - payment_out_discount", 1, "invoice_id IS NULL AND COALESCE(status, '') != ?", []interface{}{"cancelled"}},
	{&models.SalesReturn{}, "date", "CASE WHEN amount - deduction_total > 0 THEN amount - deduction_total ELSE 0 END", -1, "status = ?", []interface{}{"processed"}},
	{&models.PurchaseBill{}, "bill_date", "total_amount", -1, "status != ?", []interface{}{"draft"}},
	{&models.PurchaseReturn{}, "date", "amount", 1, "status = ?", []interface{}{"processed"}},
	{&models.CreditNote{}, "date", "total_amount", -1, "status = ?", []interface{}{"issued"}},
	{&models.DebitNote{}, "date", "total_amount", 1, "status = ?", []interface{}{"issued"}},
}

type partyBalanceAgg struct {
	before   float64 // net (debit - credit) dated before the report day
	debit    float64 // debits dated on the report day
	credit   float64 // credits dated on the report day
	docCount int64   // all qualifying documents regardless of date
}

func aggregatePartyBalances(db *gorm.DB, userID uuid.UUID, date string) (map[uuid.UUID]*partyBalanceAgg, error) {
	out := make(map[uuid.UUID]*partyBalanceAgg)
	for _, src := range partyBalanceSources {
		var rows []struct {
			PartyID uuid.UUID
			Before  float64
			OnDay   float64
			Cnt     int64
		}
		dateExpr := "DATE(" + src.dateCol + ")"
		sel := fmt.Sprintf(
			"party_id, COALESCE(SUM(CASE WHEN %[1]s < ? THEN %[2]s ELSE 0 END), 0) AS before, "+
				"COALESCE(SUM(CASE WHEN %[1]s = ? THEN %[2]s ELSE 0 END), 0) AS on_day, COUNT(*) AS cnt",
			dateExpr, src.amount)
		args := append([]interface{}{userID}, src.args...)
		err := db.Model(src.model).
			Select(sel, date, date).
			Where("user_id = ? AND "+src.where, args...).
			Group("party_id").
			Scan(&rows).Error
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			a := out[r.PartyID]
			if a == nil {
				a = &partyBalanceAgg{}
				out[r.PartyID] = a
			}
			a.before += src.sign * r.Before
			if src.sign > 0 {
				a.debit += r.OnDay
			} else {
				a.credit += r.OnDay
			}
			a.docCount += r.Cnt
		}
	}
	return out, nil
}

func loadPartyBalanceReport(userID uuid.UUID, date, partyType, search string) (*PartyBalanceReport, error) {
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return nil, fmt.Errorf("invalid date %q — use YYYY-MM-DD", date)
	}
	partyType = strings.ToLower(strings.TrimSpace(partyType))
	if partyType != "customer" && partyType != "vendor" {
		partyType = "all"
	}

	report := &PartyBalanceReport{Date: date, PartyType: partyType, Lines: []PartyBalanceLine{}}
	var business models.Business
	if err := utils.DB.Where("user_id = ?", userID).First(&business).Error; err == nil {
		report.BusinessName = business.Name
	}

	q := utils.DB.Where("user_id = ?", userID)
	if partyType != "all" {
		q = q.Where("party_type = ?", partyType)
	}
	if s := strings.TrimSpace(search); s != "" {
		like := "%" + strings.ToLower(s) + "%"
		q = q.Where("LOWER(name) LIKE ? OR phone LIKE ?", like, "%"+s+"%")
	}
	var parties []models.Party
	if err := q.Select("id", "name", "party_type", "phone", "opening_balance", "balance").Find(&parties).Error; err != nil {
		return nil, fmt.Errorf("failed to load parties")
	}

	aggs, err := aggregatePartyBalances(utils.DB, userID, date)
	if err != nil {
		return nil, fmt.Errorf("failed to compute party balances")
	}

	round := func(v float64) float64 { return math.Round(v*100) / 100 }
	for _, p := range parties {
		a := aggs[p.ID]
		if a == nil {
			a = &partyBalanceAgg{}
		}
		opening := p.OpeningBalance
		if opening == 0 && a.docCount == 0 {
			// Imported closing balance with no documents (same as the ledger).
			opening = p.Balance
		}
		opening = round(opening + a.before)
		closing := round(opening + a.debit - a.credit)
		if opening == 0 && closing == 0 {
			continue
		}
		line := PartyBalanceLine{
			PartyID: p.ID, PartyName: p.Name, PartyType: p.PartyType, Phone: p.Phone,
			OpeningBalance: opening, Debit: round(a.debit), Credit: round(a.credit), ClosingBalance: closing,
			Difference: round(closing - opening),
		}
		report.Lines = append(report.Lines, line)

		if opening > 0 {
			report.OpeningReceivable += opening
		} else {
			report.OpeningPayable -= opening
		}
		if closing > 0 {
			report.ClosingReceivable += closing
		} else {
			report.ClosingPayable -= closing
		}
		report.TotalDebit += line.Debit
		report.TotalCredit += line.Credit
	}
	sort.Slice(report.Lines, func(i, j int) bool {
		return strings.ToLower(report.Lines[i].PartyName) < strings.ToLower(report.Lines[j].PartyName)
	})
	report.OpeningReceivable = round(report.OpeningReceivable)
	report.OpeningPayable = round(report.OpeningPayable)
	report.ClosingReceivable = round(report.ClosingReceivable)
	report.ClosingPayable = round(report.ClosingPayable)
	report.TotalDebit = round(report.TotalDebit)
	report.TotalCredit = round(report.TotalCredit)
	report.NetOpening = round(report.OpeningReceivable - report.OpeningPayable)
	report.NetClosing = round(report.ClosingReceivable - report.ClosingPayable)
	report.NetDifference = round(report.NetClosing - report.NetOpening)
	return report, nil
}

func partyBalanceReportFromRequest(c *gin.Context) (*PartyBalanceReport, bool) {
	userID := c.MustGet("user_id").(uuid.UUID)
	report, err := loadPartyBalanceReport(userID, strings.TrimSpace(c.Query("date")), c.Query("party_type"), c.Query("search"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return nil, false
	}
	return report, true
}

// partyBalanceLabel renders a signed balance as "₹x Dr/Cr"-style text for exports.
func partyBalanceLabel(v float64) string {
	switch {
	case v > 0:
		return fmt.Sprintf("%.2f Dr", v)
	case v < 0:
		return fmt.Sprintf("%.2f Cr", -v)
	default:
		return "0.00"
	}
}

func GetPartyBalanceReport(c *gin.Context) {
	if report, ok := partyBalanceReportFromRequest(c); ok {
		c.JSON(http.StatusOK, report)
	}
}

func ExportPartyBalanceReportExcel(c *gin.Context) {
	report, ok := partyBalanceReportFromRequest(c)
	if !ok {
		return
	}

	file := xlsx.NewFile()
	sheet, err := file.AddSheet("Party Balances")
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
	writeNum := func(row *xlsx.Row, v float64) *xlsx.Cell {
		cell := row.AddCell()
		cell.SetFloat(v)
		cell.SetFormat("#,##0.00")
		return cell
	}

	writeRow("Party Balance Report")
	if report.BusinessName != "" {
		writeRow("Business", report.BusinessName)
	}
	writeRow("Date", report.Date)
	writeRow("Parties", report.PartyType)
	writeRow("")
	for _, s := range []struct {
		label string
		v     float64
	}{
		{"Opening receivable", report.OpeningReceivable},
		{"Opening payable", report.OpeningPayable},
		{"Closing receivable", report.ClosingReceivable},
		{"Closing payable", report.ClosingPayable},
	} {
		row := sheet.AddRow()
		row.AddCell().SetValue(s.label)
		writeNum(row, s.v)
	}
	writeRow("")

	header := sheet.AddRow()
	for _, h := range []string{"Party", "Type", "Phone", "Opening Balance (INR)", "Debit (INR)", "Credit (INR)", "Closing Balance (INR)", "Difference (INR)", "Closing Status"} {
		cell := header.AddCell()
		cell.SetValue(h)
		cell.GetStyle().Font.Bold = true
	}
	for _, l := range report.Lines {
		row := sheet.AddRow()
		row.AddCell().SetValue(l.PartyName)
		row.AddCell().SetValue(l.PartyType)
		row.AddCell().SetValue(l.Phone)
		for _, v := range []float64{l.OpeningBalance, l.Debit, l.Credit, l.ClosingBalance, l.Difference} {
			writeNum(row, v)
		}
		status := "Settled"
		if l.ClosingBalance > 0 {
			status = "To collect"
		} else if l.ClosingBalance < 0 {
			status = "To pay"
		}
		row.AddCell().SetValue(status)
	}
	total := sheet.AddRow()
	lbl := total.AddCell()
	lbl.SetValue(fmt.Sprintf("Total (%d parties)", len(report.Lines)))
	lbl.GetStyle().Font.Bold = true
	total.AddCell()
	total.AddCell()
	for _, v := range []float64{report.NetOpening, report.TotalDebit, report.TotalCredit, report.NetClosing, report.NetDifference} {
		writeNum(total, v).GetStyle().Font.Bold = true
	}
	writeRow("")
	writeRow("Note", "Positive balance = party owes you (receivable); negative = you owe the party (payable). Opening is the close of the previous day; closing is the end of the selected day.")

	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"party_balance_report_%s.xlsx\"", report.Date))
	if err := file.Write(c.Writer); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to write Excel file"})
	}
}

func buildPartyBalanceReportPDF(report *PartyBalanceReport) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(12, 16, 12)
	pdf.SetAutoPageBreak(true, 16)
	pdf.AddPage()

	pdf.SetFont("Arial", "B", 18)
	pdf.SetTextColor(37, 99, 235)
	pdf.CellFormat(0, 10, "PARTY BALANCE REPORT", "", 1, "L", false, 0, "")
	pdf.SetFont("Arial", "", 10)
	pdf.SetTextColor(80, 80, 80)
	if report.BusinessName != "" {
		pdf.CellFormat(0, 6, sanitizePDFText(report.BusinessName), "", 1, "L", false, 0, "")
	}
	pdf.CellFormat(0, 6, "Date: "+report.Date, "", 1, "L", false, 0, "")
	pdf.CellFormat(0, 6, fmt.Sprintf("Opening: receivable %.2f | payable %.2f    Closing: receivable %.2f | payable %.2f",
		report.OpeningReceivable, report.OpeningPayable, report.ClosingReceivable, report.ClosingPayable), "", 1, "L", false, 0, "")
	pdf.Ln(3)

	pageW, _ := pdf.GetPageSize()
	l, _, r, _ := pdf.GetMargins()
	usable := pageW - l - r
	cols := []float64{usable * 0.25, usable * 0.09, usable * 0.15, usable * 0.11, usable * 0.11, usable * 0.15, usable * 0.14}
	heads := []string{"Party", "Type", "Opening", "Debit", "Credit", "Closing", "Difference"}

	pdf.SetFillColor(243, 244, 246)
	pdf.SetDrawColor(200, 200, 200)
	pdf.SetFont("Arial", "B", 8)
	pdf.SetTextColor(40, 40, 40)
	for i, h := range heads {
		align := "R"
		if i < 2 {
			align = "L"
		}
		ln := 0
		if i == len(heads)-1 {
			ln = 1
		}
		pdf.CellFormat(cols[i], 8, h, "1", ln, align, true, 0, "")
	}

	pdf.SetFillColor(255, 255, 255)
	pdf.SetFont("Arial", "", 8)
	for _, line := range report.Lines {
		pdf.SetTextColor(40, 40, 40)
		pdf.CellFormat(cols[0], 6.5, sanitizePDFText(truncatePDF(line.PartyName, 42)), "1", 0, "L", true, 0, "")
		pdf.CellFormat(cols[1], 6.5, line.PartyType, "1", 0, "L", true, 0, "")
		pdf.CellFormat(cols[2], 6.5, partyBalanceLabel(line.OpeningBalance), "1", 0, "R", true, 0, "")
		pdf.CellFormat(cols[3], 6.5, fmt.Sprintf("%.2f", line.Debit), "1", 0, "R", true, 0, "")
		pdf.CellFormat(cols[4], 6.5, fmt.Sprintf("%.2f", line.Credit), "1", 0, "R", true, 0, "")
		if line.ClosingBalance > 0 {
			pdf.SetTextColor(22, 101, 52)
		} else if line.ClosingBalance < 0 {
			pdf.SetTextColor(153, 27, 27)
		}
		pdf.CellFormat(cols[5], 6.5, partyBalanceLabel(line.ClosingBalance), "1", 0, "R", true, 0, "")
		pdf.SetTextColor(40, 40, 40)
		pdf.CellFormat(cols[6], 6.5, fmt.Sprintf("%+.2f", line.Difference), "1", 1, "R", true, 0, "")
	}

	pdf.SetFillColor(243, 244, 246)
	pdf.SetFont("Arial", "B", 8)
	pdf.SetTextColor(40, 40, 40)
	pdf.CellFormat(cols[0]+cols[1], 7, fmt.Sprintf("Total (%d parties)", len(report.Lines)), "1", 0, "L", true, 0, "")
	pdf.CellFormat(cols[2], 7, partyBalanceLabel(report.NetOpening), "1", 0, "R", true, 0, "")
	pdf.CellFormat(cols[3], 7, fmt.Sprintf("%.2f", report.TotalDebit), "1", 0, "R", true, 0, "")
	pdf.CellFormat(cols[4], 7, fmt.Sprintf("%.2f", report.TotalCredit), "1", 0, "R", true, 0, "")
	pdf.CellFormat(cols[5], 7, partyBalanceLabel(report.NetClosing), "1", 0, "R", true, 0, "")
	pdf.CellFormat(cols[6], 7, fmt.Sprintf("%+.2f", report.NetDifference), "1", 1, "R", true, 0, "")

	pdf.Ln(6)
	pdf.SetFont("Arial", "I", 8)
	pdf.SetTextColor(140, 140, 140)
	pdf.MultiCell(0, 4.5, "Dr = party owes you (receivable); Cr = you owe the party (payable). Opening is the close of the previous day; closing is the end of the selected day. Parties with zero opening and closing balance are omitted. Generated from TruERP.", "", "L", false)

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func ExportPartyBalanceReportPDF(c *gin.Context) {
	report, ok := partyBalanceReportFromRequest(c)
	if !ok {
		return
	}
	pdfBytes, err := buildPartyBalanceReportPDF(report)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate PDF"})
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"party_balance_report_%s.pdf\"", report.Date))
	c.Data(http.StatusOK, "application/pdf", pdfBytes)
}
