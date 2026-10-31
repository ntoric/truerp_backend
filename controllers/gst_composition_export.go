package controllers

// GST Composition exports: Excel workbooks, CSV registers and PDF filing
// summaries. Every successful export is recorded in GSTReportExport for
// audit. Exported files are working papers — they are never labelled as
// GSTN-uploadable returns, and exporting never marks a return filed.

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/go-pdf/fpdf"
	"github.com/google/uuid"
	"github.com/tealeg/xlsx/v3"
)

const gstMoneyFormat = "#,##0.00"
const gstDateFormat = "2006-01-02"

// gstExportMeta is the standard header block on every export.
type gstExportMeta struct {
	BusinessName string
	GSTIN        string
	Report       string
	Period       string
	GeneratedAt  time.Time
}

func gstMetaFromScope(c *gin.Context, scope *gstReportScope, report string) gstExportMeta {
	userID := c.MustGet("user_id").(uuid.UUID)
	var biz models.Business
	utils.DB.Where("user_id = ?", userID).First(&biz)
	m := gstExportMeta{
		BusinessName: biz.Name,
		Report:       report,
		Period:       fmt.Sprintf("%s to %s", scope.Start.Format(gstDateFormat), scope.End.Format(gstDateFormat)),
		GeneratedAt:  time.Now().UTC(),
	}
	if scope.Registration != nil {
		m.GSTIN = scope.Registration.GSTIN
		if m.BusinessName == "" {
			m.BusinessName = firstNonEmpty(scope.Registration.TradeName, scope.Registration.LegalName)
		}
	}
	return m
}

func writeMetaRows(sheet *xlsx.Sheet, m gstExportMeta) int {
	writeRow := func(cols ...string) {
		row := sheet.AddRow()
		for _, col := range cols {
			row.AddCell().SetValue(col)
		}
	}
	writeRow(m.Report)
	writeRow("Business", m.BusinessName)
	writeRow("GSTIN", m.GSTIN)
	writeRow("Period", m.Period)
	writeRow("Generated At (UTC)", m.GeneratedAt.Format("2006-01-02 15:04:05"))
	writeRow("Note", "Working paper for review. Export does not constitute GST return filing.")
	writeRow("")
	return 7 // rows consumed
}

func writeHeaderRow(sheet *xlsx.Sheet, headers []string) {
	row := sheet.AddRow()
	for _, h := range headers {
		cell := row.AddCell()
		cell.SetValue(h)
		cell.GetStyle().Font.Bold = true
	}
}

func freezeHeader(sheet *xlsx.Sheet, rows int) {
	sheet.SheetViews = append(sheet.SheetViews, xlsx.SheetView{Pane: &xlsx.Pane{
		YSplit:      float64(rows),
		TopLeftCell: fmt.Sprintf("A%d", rows+1),
		ActivePane:  "bottomLeft",
		State:       "frozen",
	}})
}

func addTextCell(row *xlsx.Row, v string)  { row.AddCell().SetValue(v) }
func addMoneyCell(row *xlsx.Row, v float64) {
	cell := row.AddCell()
	cell.SetFloat(roundMoney(v))
	cell.SetFormat(gstMoneyFormat)
}
func addDateCell(row *xlsx.Row, t time.Time) { row.AddCell().SetValue(t.Format(gstDateFormat)) }
func addBoolCell(row *xlsx.Row, b bool) {
	if b {
		row.AddCell().SetValue("Yes")
	} else {
		row.AddCell().SetValue("No")
	}
}

var gstSalesRegisterHeaders = []string{
	"Financial Year", "Tax Period", "Invoice Number", "Invoice Date",
	"Invoice Status", "Document Type", "Customer Name", "Customer GSTIN",
	"Customer State", "Place of Supply", "Transaction Type",
	"Gross Amount", "Discount Amount", "Sales Return Amount", "Net Amount",
	"Taxable Value", "Exempt Value", "Nil Rated Value", "Non GST Value",
	"Tax Amount", "Supply Classification", "Composition Turnover Category",
	"Inter State", "Included In Totals", "Source Transaction ID",
}

func writeSalesRegisterRows(sheet *xlsx.Sheet, rows []gstSalesRegisterRow) {
	for _, r := range rows {
		row := sheet.AddRow()
		addTextCell(row, r.FinancialYear)
		addTextCell(row, r.TaxPeriod)
		addTextCell(row, r.InvoiceNumber)
		addDateCell(row, r.InvoiceDate)
		addTextCell(row, r.InvoiceStatus)
		addTextCell(row, r.DocumentType)
		addTextCell(row, r.CustomerName)
		addTextCell(row, r.CustomerGSTIN)
		addTextCell(row, r.CustomerState)
		addTextCell(row, r.PlaceOfSupply)
		addTextCell(row, r.TransactionType)
		addMoneyCell(row, r.GrossAmount)
		addMoneyCell(row, r.DiscountAmount)
		addMoneyCell(row, r.ReturnAmount)
		addMoneyCell(row, r.NetAmount)
		addMoneyCell(row, r.TaxableValue)
		addMoneyCell(row, r.ExemptValue)
		addMoneyCell(row, r.NilRatedValue)
		addMoneyCell(row, r.NonGSTValue)
		addMoneyCell(row, r.TaxAmount)
		addTextCell(row, r.SupplyClassification)
		addTextCell(row, r.CompositionTurnover)
		addBoolCell(row, r.IsInterState)
		addBoolCell(row, r.IncludedInTotals)
		addTextCell(row, r.SourceID.String())
	}
}

var gstPurchaseRegisterHeaders = []string{
	"Financial Year", "Tax Period", "Purchase Invoice Number", "Purchase Date",
	"Status", "Document Type", "Supplier Name", "Supplier GSTIN",
	"Supplier Registration Status", "Supplier State", "Place of Supply",
	"Purchase Category", "Gross Amount", "Discount Amount", "Return Amount",
	"Net Amount", "Taxable Value", "Exempt Value",
	"CGST Amount", "SGST Amount", "IGST Amount",
	"Reverse Charge Applicable", "Reverse Charge Taxable Value",
	"Reverse Charge Tax Amount", "Included In Totals", "Source Transaction ID",
}

func writePurchaseRegisterRows(sheet *xlsx.Sheet, rows []gstPurchaseRegisterRow) {
	for _, r := range rows {
		row := sheet.AddRow()
		addTextCell(row, r.FinancialYear)
		addTextCell(row, r.TaxPeriod)
		addTextCell(row, r.InvoiceNumber)
		addDateCell(row, r.InvoiceDate)
		addTextCell(row, r.DocStatus)
		addTextCell(row, r.DocumentType)
		addTextCell(row, r.SupplierName)
		addTextCell(row, r.SupplierGSTIN)
		addTextCell(row, r.SupplierRegistration)
		addTextCell(row, r.SupplierState)
		addTextCell(row, r.PlaceOfSupply)
		addTextCell(row, r.PurchaseCategory)
		addMoneyCell(row, r.GrossAmount)
		addMoneyCell(row, r.DiscountAmount)
		addMoneyCell(row, r.ReturnAmount)
		addMoneyCell(row, r.NetAmount)
		addMoneyCell(row, r.TaxableValue)
		addMoneyCell(row, r.ExemptValue)
		addMoneyCell(row, r.CGSTAmount)
		addMoneyCell(row, r.SGSTAmount)
		addMoneyCell(row, r.IGSTAmount)
		addBoolCell(row, r.ReverseCharge)
		addMoneyCell(row, r.RCMTaxableValue)
		addMoneyCell(row, r.RCMTaxAmount)
		addBoolCell(row, r.IncludedInTotals)
		addTextCell(row, r.SourceID.String())
	}
}

func writeSalesSummarySheet(file *xlsx.File, ss gstSalesSummary) {
	sheet, err := file.AddSheet("Sales Summary")
	if err != nil {
		return
	}
	writeHeaderRow(sheet, []string{"Particulars", "Value"})
	for _, r := range [][2]interface{}{
		{"Period", ss.Period},
		{"Invoice Count", ss.InvoiceCount},
		{"Cancelled Invoice Count", ss.CancelledInvoiceCount},
		{"Gross Sales", ss.GrossSales},
		{"Discount Amount", ss.DiscountAmount},
		{"Sales Return Amount", ss.SalesReturnAmount},
		{"Credit Note Value", ss.CreditNoteValue},
		{"Net Sales", ss.NetSales},
		{"Taxable Supply Value", ss.TaxableSupplyValue},
		{"Exempt Supply Value", ss.ExemptSupplyValue},
		{"Nil Rated Supply Value", ss.NilRatedSupplyValue},
		{"Non GST Supply Value", ss.NonGSTSupplyValue},
		{"Composition Turnover Basis", ss.CompositionTurnoverBasis},
		{"Inter State Outward Supply Value (review)", ss.InterStateOutwardValue},
	} {
		row := sheet.AddRow()
		addTextCell(row, r[0].(string))
		switch v := r[1].(type) {
		case float64:
			addMoneyCell(row, v)
		case int:
			row.AddCell().SetInt(v)
		default:
			row.AddCell().SetValue(fmt.Sprint(v))
		}
	}
}

func writePurchaseSummarySheet(file *xlsx.File, ps gstPurchaseSummary) {
	sheet, err := file.AddSheet("Purchase Summary")
	if err != nil {
		return
	}
	writeHeaderRow(sheet, []string{"Particulars", "Value"})
	for _, r := range [][2]interface{}{
		{"Period", ps.Period},
		{"Purchase Invoice Count", ps.PurchaseInvoiceCount},
		{"Registered Supplier Purchase Value", ps.RegisteredSupplierPurchaseValue},
		{"Unregistered Supplier Purchase Value", ps.UnregisteredSupplierPurchaseValue},
		{"Purchase Return Value", ps.PurchaseReturnValue},
		{"Purchase Discount Value", ps.PurchaseDiscountValue},
		{"Intra-State Purchase Value", ps.IntraStatePurchaseValue},
		{"Inter-State Purchase Value", ps.InterStatePurchaseValue},
		{"Taxable Purchase Value", ps.TaxablePurchaseValue},
		{"Exempt Purchase Value", ps.ExemptPurchaseValue},
		{"Supplier GST Amount (no ITC under composition)", ps.SupplierGSTAmount},
		{"RCM Taxable Value", ps.RCMTaxableValue},
		{"RCM Tax Liability", ps.RCMTaxLiability},
	} {
		row := sheet.AddRow()
		addTextCell(row, r[0].(string))
		switch v := r[1].(type) {
		case float64:
			addMoneyCell(row, v)
		case int:
			row.AddCell().SetInt(v)
		default:
			row.AddCell().SetValue(fmt.Sprint(v))
		}
	}
}

func writeTaxCalculationSheet(file *xlsx.File, liab gstTaxLiability, ss gstSalesSummary, adjustments float64) {
	sheet, err := file.AddSheet("Tax Calculation")
	if err != nil {
		return
	}
	writeHeaderRow(sheet, []string{"Step", "Particulars", "Amount"})
	steps := [][3]interface{}{
		{"1", "Composition turnover basis (taxable + exempt + nil, net of returns)", liab.TurnoverBasis},
		{"2", "Applicable composition rate (%)", liab.CompositionRate},
		{"3", "Composition tax = basis x rate", liab.Total},
		{"3a", "  CGST component", liab.CGST},
		{"3b", "  SGST component", liab.SGST},
		{"4", "Reverse charge taxable value", liab.RCMTaxableValue},
		{"5", "Reverse charge tax liability", liab.RCMLiability},
		{"6", "Documented adjustments", adjustments},
		{"7", "Total payable", liab.Total + liab.RCMLiability + adjustments},
	}
	for _, s := range steps {
		row := sheet.AddRow()
		addTextCell(row, s[0].(string))
		addTextCell(row, s[1].(string))
		addMoneyCell(row, s[2].(float64))
	}
	row := sheet.AddRow()
	addTextCell(row, "")
	addTextCell(row, "Basis includes gross sales "+fmt.Sprintf("%.2f", ss.GrossSales)+" less discounts/returns; interstate supplies flagged separately.")
	addTextCell(row, "")
}

func writeRCMSheet(file *xlsx.File, rows []gstPurchaseRegisterRow) {
	sheet, err := file.AddSheet("RCM Details")
	if err != nil {
		return
	}
	writeHeaderRow(sheet, []string{
		"Purchase Invoice Number", "Purchase Date", "Supplier Name", "Supplier GSTIN",
		"RCM Taxable Value", "RCM Tax Amount", "Source Transaction ID",
	})
	for _, r := range rows {
		if !r.ReverseCharge {
			continue
		}
		row := sheet.AddRow()
		addTextCell(row, r.InvoiceNumber)
		addDateCell(row, r.InvoiceDate)
		addTextCell(row, r.SupplierName)
		addTextCell(row, r.SupplierGSTIN)
		addMoneyCell(row, r.RCMTaxableValue)
		addMoneyCell(row, r.RCMTaxAmount)
		addTextCell(row, r.SourceID.String())
	}
}

func writePaymentReconSheet(file *xlsx.File, period *models.GSTReturnPeriod, payable float64) {
	sheet, err := file.AddSheet("Payment Reconciliation")
	if err != nil {
		return
	}
	writeHeaderRow(sheet, []string{"Particulars", "Value"})
	row := sheet.AddRow()
	addTextCell(row, "Current Period Liability")
	addMoneyCell(row, payable)

	var paid, confirmed float64
	if period != nil {
		var payments []models.GSTTaxPayment
		utils.DB.Where("gst_return_period_id = ?", period.ID).Order("payment_date ASC").Find(&payments)
		row = sheet.AddRow()
		addTextCell(row, "")
		addTextCell(row, "")
		writeHeaderRow(sheet, []string{"Payment Date", "Amount", "Component", "Challan Reference", "Portal Confirmed", "Portal Reference"})
		for _, p := range payments {
			row = sheet.AddRow()
			addDateCell(row, p.PaymentDate)
			addMoneyCell(row, p.Amount)
			addTextCell(row, p.Component)
			addTextCell(row, p.ChallanReference)
			addBoolCell(row, p.PortalConfirmed)
			addTextCell(row, p.PortalReference)
			paid += p.Amount
			if p.PortalConfirmed {
				confirmed += p.Amount
			}
		}
	}
	row = sheet.AddRow()
	addTextCell(row, "")
	addTextCell(row, "")
	for _, r := range [][2]interface{}{
		{"Tax Paid (recorded)", paid},
		{"Tax Paid (portal confirmed)", confirmed},
		{"Unverified Payment Amount", paid - confirmed},
		{"Outstanding Balance", payable - paid},
	} {
		row = sheet.AddRow()
		addTextCell(row, r[0].(string))
		addMoneyCell(row, r[1].(float64))
	}
}

func writeValidationSheet(file *xlsx.File, period *models.GSTReturnPeriod) {
	sheet, err := file.AddSheet("Validation Report")
	if err != nil {
		return
	}
	writeHeaderRow(sheet, []string{"Severity", "Code", "Message", "Source Type", "Source Reference", "Status"})
	if period == nil {
		return
	}
	var issues []models.GSTValidationIssue
	utils.DB.Where("gst_return_period_id = ?", period.ID).Order("severity ASC").Find(&issues)
	for _, i := range issues {
		row := sheet.AddRow()
		addTextCell(row, i.Severity)
		addTextCell(row, i.Code)
		addTextCell(row, i.Message)
		addTextCell(row, i.SourceTransactionType)
		addTextCell(row, i.SourceReference)
		addTextCell(row, i.ResolutionStatus)
	}
}

func writeSummarySheet(file *xlsx.File, m gstExportMeta, ss gstSalesSummary, ps gstPurchaseSummary, liab gstTaxLiability, payable float64) {
	sheet, err := file.AddSheet("Summary")
	if err != nil {
		return
	}
	consumed := writeMetaRows(sheet, m)
	_ = consumed
	writeHeaderRow(sheet, []string{"Section", "Particulars", "Amount"})
	rows := [][3]interface{}{
		{"Sales", "Gross Sales", ss.GrossSales},
		{"Sales", "Net Sales", ss.NetSales},
		{"Sales", "Taxable Outward Supplies", ss.TaxableSupplyValue},
		{"Sales", "Exempt + Nil Rated Supplies", ss.ExemptSupplyValue + ss.NilRatedSupplyValue},
		{"Sales", "Non GST Supplies", ss.NonGSTSupplyValue},
		{"Sales", "Composition Turnover Basis", ss.CompositionTurnoverBasis},
		{"Purchases", "Registered Supplier Purchases", ps.RegisteredSupplierPurchaseValue},
		{"Purchases", "Unregistered Supplier Purchases", ps.UnregisteredSupplierPurchaseValue},
		{"Purchases", "RCM Taxable Value", ps.RCMTaxableValue},
		{"Liability", "Composition Tax (CGST + SGST)", liab.Total},
		{"Liability", "Reverse Charge Liability", liab.RCMLiability},
		{"Liability", "Total Payable", payable},
	}
	for _, r := range rows {
		row := sheet.AddRow()
		addTextCell(row, r[0].(string))
		addTextCell(row, r[1].(string))
		addMoneyCell(row, r[2].(float64))
	}
}

// recordGSTExport persists an export-history row; failures are logged via
// the response only — history is best-effort after the file is built.
func recordGSTExport(userID, actorID uuid.UUID, period *models.GSTReturnPeriod, exportType, format, fileName string, recordCount int) {
	var periodID *uuid.UUID
	version := 0
	validationStatus := "not_validated"
	if period != nil {
		periodID = &period.ID
		if snap, err := latestGSTSnapshot(period.ID); err == nil {
			version = snap.Version
		}
		var openErr, openWarn int64
		utils.DB.Model(&models.GSTValidationIssue{}).
			Where("gst_return_period_id = ? AND resolution_status = 'open' AND severity = 'ERROR'", period.ID).Count(&openErr)
		utils.DB.Model(&models.GSTValidationIssue{}).
			Where("gst_return_period_id = ? AND resolution_status = 'open' AND severity = 'WARNING'", period.ID).Count(&openWarn)
		switch {
		case openErr > 0:
			validationStatus = "errors"
		case openWarn > 0:
			validationStatus = "warnings"
		default:
			validationStatus = "clean"
		}
		// Record export on the period workflow (does NOT mean filed).
		if period.Status != models.GSTPeriodStatusFiled && period.Status != models.GSTPeriodStatusFinalized {
			period.Status = models.GSTPeriodStatusExported
			utils.DB.Save(period)
		}
	}
	utils.DB.Create(&models.GSTReportExport{
		ID:                uuid.New(),
		UserID:            userID,
		GSTReturnPeriodID: periodID,
		ExportType:        exportType,
		FileFormat:        format,
		FileName:          fileName,
		ReportVersion:     version,
		RecordCount:       recordCount,
		ValidationStatus:  validationStatus,
		GeneratedAt:       time.Now(),
		GeneratedBy:       actorID,
	})
}

func sendXLSX(c *gin.Context, file *xlsx.File, fileName string) {
	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fileName))
	if err := file.Write(c.Writer); err != nil {
		c.Status(http.StatusInternalServerError)
	}
}

func sendCSV(c *gin.Context, fileName string, write func(w *csv.Writer) error) {
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fileName))
	c.Writer.Write([]byte{0xEF, 0xBB, 0xBF}) // BOM so Excel opens UTF-8 correctly
	w := csv.NewWriter(c.Writer)
	_ = write(w)
	w.Flush()
}

// gstFileName builds e.g. GST_Composition_FY2026-27_Q2_sales_register.xlsx
func gstFileName(scope *gstReportScope, suffix, ext string) string {
	name := fmt.Sprintf("GST_Composition_FY%s", scope.FY)
	if scope.Quarter > 0 {
		name += fmt.Sprintf("_Q%d", scope.Quarter)
	}
	if suffix != "" {
		name += "_" + suffix
	}
	return name + "." + ext
}

// ---- Export handlers ---------------------------------------------------------

// ExportGSTFilingSummaryExcel — the full multi-sheet workbook.
func ExportGSTFilingSummaryExcel(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	scope, ok := scopeOr400(c)
	if !ok {
		return
	}
	sales, purchases, ss, ps, liab := computeGSTReport(userID, scope)
	period := findPeriodForScope(userID, scope)
	payable := liab.Total + liab.RCMLiability
	meta := gstMetaFromScope(c, scope, "GST Filing Summary (Composition)")

	file := xlsx.NewFile()
	writeSummarySheet(file, meta, ss, ps, liab, payable)
	writeSalesSummarySheet(file, ss)
	if sheet, err := file.AddSheet("Sales Register"); err == nil {
		writeHeaderRow(sheet, gstSalesRegisterHeaders)
		writeSalesRegisterRows(sheet, sales)
		freezeHeader(sheet, 1)
		lastCol := string(rune('A' + len(gstSalesRegisterHeaders) - 1))
		sheet.AutoFilter = &xlsx.AutoFilter{TopLeftCell: "A1", BottomRightCell: lastCol + "1"}
	}
	writePurchaseSummarySheet(file, ps)
	if sheet, err := file.AddSheet("Purchase Register"); err == nil {
		writeHeaderRow(sheet, gstPurchaseRegisterHeaders)
		writePurchaseRegisterRows(sheet, purchases)
		freezeHeader(sheet, 1)
		lastCol := string(rune('A' + len(gstPurchaseRegisterHeaders) - 1))
		sheet.AutoFilter = &xlsx.AutoFilter{TopLeftCell: "A1", BottomRightCell: lastCol + "1"}
	}
	writeRCMSheet(file, purchases)
	writeTaxCalculationSheet(file, liab, ss, 0)
	writePaymentReconSheet(file, period, payable)
	writeValidationSheet(file, period)

	fileName := gstFileName(scope, "filing_summary", "xlsx")
	recordGSTExport(userID, actorUserID(c), period, "filing_summary", "xlsx", fileName, len(sales)+len(purchases))
	sendXLSX(c, file, fileName)
}

func ExportGSTSalesRegisterExcel(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	scope, ok := scopeOr400(c)
	if !ok {
		return
	}
	rows := loadGSTSalesRegister(userID, scope.Start, scope.End, regState(scope), scope.FY, scopeTaxPeriod(scope))
	sortSalesRowsByDate(rows)
	meta := gstMetaFromScope(c, scope, "Sales Register (Outward Supplies)")

	file := xlsx.NewFile()
	sheet, err := file.AddSheet("Sales Register")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create Excel sheet"})
		return
	}
	consumed := writeMetaRows(sheet, meta)
	writeHeaderRow(sheet, gstSalesRegisterHeaders)
	writeSalesRegisterRows(sheet, rows)
	headerRow := consumed + 1
	freezeHeader(sheet, headerRow)
	lastCol := string(rune('A' + len(gstSalesRegisterHeaders) - 1))
	sheet.AutoFilter = &xlsx.AutoFilter{TopLeftCell: fmt.Sprintf("A%d", headerRow), BottomRightCell: fmt.Sprintf("%s%d", lastCol, headerRow)}

	fileName := gstFileName(scope, "sales_register", "xlsx")
	recordGSTExport(userID, actorUserID(c), findPeriodForScope(userID, scope), "sales_register", "xlsx", fileName, len(rows))
	sendXLSX(c, file, fileName)
}

func ExportGSTSalesRegisterCSV(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	scope, ok := scopeOr400(c)
	if !ok {
		return
	}
	rows := loadGSTSalesRegister(userID, scope.Start, scope.End, regState(scope), scope.FY, scopeTaxPeriod(scope))
	sortSalesRowsByDate(rows)
	fileName := gstFileName(scope, "sales_register", "csv")
	recordGSTExport(userID, actorUserID(c), findPeriodForScope(userID, scope), "sales_register", "csv", fileName, len(rows))
	sendCSV(c, fileName, func(w *csv.Writer) error {
		w.Write(gstSalesRegisterHeaders)
		for _, r := range rows {
			w.Write([]string{
				r.FinancialYear, r.TaxPeriod, r.InvoiceNumber, r.InvoiceDate.Format(gstDateFormat),
				r.InvoiceStatus, r.DocumentType, r.CustomerName, r.CustomerGSTIN,
				r.CustomerState, r.PlaceOfSupply, r.TransactionType,
				fmt.Sprintf("%.2f", r.GrossAmount), fmt.Sprintf("%.2f", r.DiscountAmount),
				fmt.Sprintf("%.2f", r.ReturnAmount), fmt.Sprintf("%.2f", r.NetAmount),
				fmt.Sprintf("%.2f", r.TaxableValue), fmt.Sprintf("%.2f", r.ExemptValue),
				fmt.Sprintf("%.2f", r.NilRatedValue), fmt.Sprintf("%.2f", r.NonGSTValue),
				fmt.Sprintf("%.2f", r.TaxAmount),
				r.SupplyClassification, r.CompositionTurnover,
				boolStr(r.IsInterState), boolStr(r.IncludedInTotals), r.SourceID.String(),
			})
		}
		return nil
	})
}

func ExportGSTPurchaseRegisterExcel(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	scope, ok := scopeOr400(c)
	if !ok {
		return
	}
	rows := loadGSTPurchaseRegister(userID, scope.Start, scope.End, regState(scope), scope.FY, scopeTaxPeriod(scope))
	sortPurchaseRowsByDate(rows)
	meta := gstMetaFromScope(c, scope, "Purchase Register (Inward Supplies)")

	file := xlsx.NewFile()
	sheet, err := file.AddSheet("Purchase Register")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create Excel sheet"})
		return
	}
	consumed := writeMetaRows(sheet, meta)
	writeHeaderRow(sheet, gstPurchaseRegisterHeaders)
	writePurchaseRegisterRows(sheet, rows)
	headerRow := consumed + 1
	freezeHeader(sheet, headerRow)
	lastCol := string(rune('A' + len(gstPurchaseRegisterHeaders) - 1))
	sheet.AutoFilter = &xlsx.AutoFilter{TopLeftCell: fmt.Sprintf("A%d", headerRow), BottomRightCell: fmt.Sprintf("%s%d", lastCol, headerRow)}

	fileName := gstFileName(scope, "purchase_register", "xlsx")
	recordGSTExport(userID, actorUserID(c), findPeriodForScope(userID, scope), "purchase_register", "xlsx", fileName, len(rows))
	sendXLSX(c, file, fileName)
}

func ExportGSTPurchaseRegisterCSV(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	scope, ok := scopeOr400(c)
	if !ok {
		return
	}
	rows := loadGSTPurchaseRegister(userID, scope.Start, scope.End, regState(scope), scope.FY, scopeTaxPeriod(scope))
	sortPurchaseRowsByDate(rows)
	fileName := gstFileName(scope, "purchase_register", "csv")
	recordGSTExport(userID, actorUserID(c), findPeriodForScope(userID, scope), "purchase_register", "csv", fileName, len(rows))
	sendCSV(c, fileName, func(w *csv.Writer) error {
		w.Write(gstPurchaseRegisterHeaders)
		for _, r := range rows {
			w.Write([]string{
				r.FinancialYear, r.TaxPeriod, r.InvoiceNumber, r.InvoiceDate.Format(gstDateFormat),
				r.DocStatus, r.DocumentType, r.SupplierName, r.SupplierGSTIN,
				r.SupplierRegistration, r.SupplierState, r.PlaceOfSupply, r.PurchaseCategory,
				fmt.Sprintf("%.2f", r.GrossAmount), fmt.Sprintf("%.2f", r.DiscountAmount),
				fmt.Sprintf("%.2f", r.ReturnAmount), fmt.Sprintf("%.2f", r.NetAmount),
				fmt.Sprintf("%.2f", r.TaxableValue), fmt.Sprintf("%.2f", r.ExemptValue),
				fmt.Sprintf("%.2f", r.CGSTAmount), fmt.Sprintf("%.2f", r.SGSTAmount),
				fmt.Sprintf("%.2f", r.IGSTAmount),
				boolStr(r.ReverseCharge), fmt.Sprintf("%.2f", r.RCMTaxableValue),
				fmt.Sprintf("%.2f", r.RCMTaxAmount), boolStr(r.IncludedInTotals), r.SourceID.String(),
			})
		}
		return nil
	})
}

func boolStr(b bool) string {
	if b {
		return "Yes"
	}
	return "No"
}

// ExportGSTCMP08Excel — quarterly working paper workbook.
func ExportGSTCMP08Excel(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	scope, ok := scopeOr400(c)
	if !ok {
		return
	}
	if scope.PeriodType != models.GSTPeriodTypeQuarterly && scope.Period == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "CMP-08 export requires a quarterly period"})
		return
	}
	sales, purchases, ss, ps, liab := computeGSTReport(userID, scope)
	period := findPeriodForScope(userID, scope)
	var adjustments float64
	if snap, err := func() (*models.GSTCalculationSnapshot, error) {
		if period == nil {
			return nil, fmt.Errorf("no period")
		}
		return latestGSTSnapshot(period.ID)
	}(); err == nil {
		adjustments = snap.AdjustmentsTotal
	}
	payable := liab.Total + liab.RCMLiability + adjustments
	meta := gstMetaFromScope(c, scope, fmt.Sprintf("CMP-08 Working Paper — %s Q%d", scope.FY, scope.Quarter))

	file := xlsx.NewFile()
	// Summary sheet with the CMP-08 statement layout.
	if sheet, err := file.AddSheet("CMP-08"); err == nil {
		writeMetaRows(sheet, meta)
		writeHeaderRow(sheet, []string{"Particulars", "Amount"})
		for _, r := range [][2]interface{}{
			{"Taxable outward turnover", ss.TaxableSupplyValue},
			{"Exempt outward turnover (incl. nil rated)", ss.ExemptSupplyValue + ss.NilRatedSupplyValue},
			{"Non-GST supplies", ss.NonGSTSupplyValue},
			{"Inter-state outward supplies (review)", ss.InterStateOutwardValue},
			{"Composition turnover basis", liab.TurnoverBasis},
			{"Applicable composition rate (%)", liab.CompositionRate},
			{"CGST composition liability", liab.CGST},
			{"SGST composition liability", liab.SGST},
			{"Total composition liability", liab.Total},
			{"RCM taxable value", liab.RCMTaxableValue},
			{"RCM tax liability", liab.RCMLiability},
			{"Documented adjustments", adjustments},
			{"Total payable", payable},
		} {
			row := sheet.AddRow()
			addTextCell(row, r[0].(string))
			addMoneyCell(row, r[1].(float64))
		}
	}
	writeSalesSummarySheet(file, ss)
	if sheet, err := file.AddSheet("Sales Register"); err == nil {
		writeHeaderRow(sheet, gstSalesRegisterHeaders)
		writeSalesRegisterRows(sheet, sales)
		freezeHeader(sheet, 1)
	}
	writePurchaseSummarySheet(file, ps)
	if sheet, err := file.AddSheet("Purchase Register"); err == nil {
		writeHeaderRow(sheet, gstPurchaseRegisterHeaders)
		writePurchaseRegisterRows(sheet, purchases)
		freezeHeader(sheet, 1)
	}
	writeRCMSheet(file, purchases)
	writeTaxCalculationSheet(file, liab, ss, adjustments)
	writePaymentReconSheet(file, period, payable)
	writeValidationSheet(file, period)

	fileName := gstFileName(scope, "cmp08", "xlsx")
	recordGSTExport(userID, actorUserID(c), period, "cmp08", "xlsx", fileName, len(sales)+len(purchases))
	sendXLSX(c, file, fileName)
}

// ExportGSTGSTR4Excel — annual working paper workbook.
func ExportGSTGSTR4Excel(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	scope, ok := scopeOr400(c)
	if !ok {
		return
	}
	sales, purchases, ss, ps, liab := computeGSTReport(userID, scope)
	period := findPeriodForScope(userID, scope)
	payable := liab.Total + liab.RCMLiability
	meta := gstMetaFromScope(c, scope, fmt.Sprintf("GSTR-4 Annual Working Paper — FY %s", scope.FY))

	file := xlsx.NewFile()
	if sheet, err := file.AddSheet("Annual Summary"); err == nil {
		writeMetaRows(sheet, meta)
		writeHeaderRow(sheet, []string{"Particulars", "Amount"})
		for _, r := range [][2]interface{}{
			{"Annual turnover (composition basis)", ss.CompositionTurnoverBasis},
			{"Taxable outward supplies", ss.TaxableSupplyValue},
			{"Exempt outward supplies", ss.ExemptSupplyValue},
			{"Nil rated supplies", ss.NilRatedSupplyValue},
			{"Non-GST supplies", ss.NonGSTSupplyValue},
			{"Registered supplier purchases", ps.RegisteredSupplierPurchaseValue},
			{"Unregistered supplier purchases", ps.UnregisteredSupplierPurchaseValue},
			{"Inter-state purchases", ps.InterStatePurchaseValue},
			{"RCM taxable value", ps.RCMTaxableValue},
			{"RCM liability", ps.RCMTaxLiability},
			{"Composition tax liability", liab.Total},
		} {
			row := sheet.AddRow()
			addTextCell(row, r[0].(string))
			addMoneyCell(row, r[1].(float64))
		}
	}
	// Quarterly reconciliation sheet.
	if sheet, err := file.AddSheet("Quarterly Reconciliation"); err == nil {
		writeHeaderRow(sheet, []string{"Quarter", "Sales Turnover", "Calculated Liability", "CMP-08 Liability", "CMP-08 Status", "Tax Paid", "Difference"})
		for q := 1; q <= 4; q++ {
			qs, qe, err := quarterBounds(scope.FY, q)
			if err != nil {
				continue
			}
			qScope := &gstReportScope{Registration: scope.Registration, Start: qs, End: qe, FY: scope.FY, Quarter: q, PeriodType: models.GSTPeriodTypeQuarterly}
			_, _, qss, _, qliab := computeGSTReport(userID, qScope)
			row := sheet.AddRow()
			addTextCell(row, fmt.Sprintf("Q%d", q))
			addMoneyCell(row, qss.CompositionTurnoverBasis)
			addMoneyCell(row, qliab.Total)
			var p models.GSTReturnPeriod
			if err := utils.DB.Where("user_id = ? AND financial_year = ? AND period_type = ? AND quarter = ?",
				userID, scope.FY, models.GSTPeriodTypeQuarterly, q).First(&p).Error; err == nil {
				if snap, serr := latestGSTSnapshot(p.ID); serr == nil {
					addMoneyCell(row, snap.TotalCompositionLiability)
				} else {
					addTextCell(row, "")
				}
				addTextCell(row, p.Status)
				var paid float64
				utils.DB.Model(&models.GSTTaxPayment{}).Where("gst_return_period_id = ?", p.ID).Select("COALESCE(SUM(amount),0)").Scan(&paid)
				addMoneyCell(row, paid)
				if snap, serr := latestGSTSnapshot(p.ID); serr == nil {
					addMoneyCell(row, snap.TotalCompositionLiability-qliab.Total)
				} else {
					addTextCell(row, "")
				}
			} else {
				addTextCell(row, "")
				addTextCell(row, "not_started")
				addMoneyCell(row, 0)
				addTextCell(row, "")
			}
		}
	}
	writeSalesSummarySheet(file, ss)
	if sheet, err := file.AddSheet("Sales Register"); err == nil {
		writeHeaderRow(sheet, gstSalesRegisterHeaders)
		writeSalesRegisterRows(sheet, sales)
		freezeHeader(sheet, 1)
	}
	writePurchaseSummarySheet(file, ps)
	if sheet, err := file.AddSheet("Purchase Register"); err == nil {
		writeHeaderRow(sheet, gstPurchaseRegisterHeaders)
		writePurchaseRegisterRows(sheet, purchases)
		freezeHeader(sheet, 1)
	}
	writeRCMSheet(file, purchases)
	writeTaxCalculationSheet(file, liab, ss, 0)
	writePaymentReconSheet(file, period, payable)
	writeValidationSheet(file, period)

	fileName := gstFileName(scope, "gstr4", "xlsx")
	recordGSTExport(userID, actorUserID(c), period, "gstr4", "xlsx", fileName, len(sales)+len(purchases))
	sendXLSX(c, file, fileName)
}

// ---- PDF exports ---------------------------------------------------------------

func gstPDFBase(title string, m gstExportMeta) *fpdf.Fpdf {
	pdf := fpdf.New("L", "mm", "A4", "")
	pdf.SetMargins(12, 12, 12)
	pdf.AddPage()
	pdf.SetFont("Helvetica", "B", 14)
	pdf.Cell(0, 8, title)
	pdf.Ln(8)
	pdf.SetFont("Helvetica", "", 9)
	pdf.Cell(0, 5, fmt.Sprintf("Business: %s    GSTIN: %s    Period: %s", m.BusinessName, m.GSTIN, m.Period))
	pdf.Ln(5)
	pdf.Cell(0, 5, fmt.Sprintf("Generated (UTC): %s    Working paper only - export does not file the return", m.GeneratedAt.Format("2006-01-02 15:04")))
	pdf.Ln(8)
	return pdf
}

func gstPDFTable(pdf *fpdf.Fpdf, headers []string, widths []float64, rows [][]string) {
	pdf.SetFont("Helvetica", "B", 8)
	pdf.SetFillColor(230, 230, 230)
	for i, h := range headers {
		pdf.CellFormat(widths[i], 6, sanitizePDFText(h), "1", 0, "L", true, 0, "")
	}
	pdf.Ln(-1)
	pdf.SetFont("Helvetica", "", 8)
	for _, r := range rows {
		// Page-break guard.
		if pdf.GetY() > 185 {
			pdf.AddPage()
			pdf.SetFont("Helvetica", "B", 8)
			for i, h := range headers {
				pdf.CellFormat(widths[i], 6, sanitizePDFText(h), "1", 0, "L", true, 0, "")
			}
			pdf.Ln(-1)
			pdf.SetFont("Helvetica", "", 8)
		}
		for i, cell := range r {
			pdf.CellFormat(widths[i], 5.5, sanitizePDFText(cell), "1", 0, "L", false, 0, "")
		}
		pdf.Ln(-1)
	}
	pdf.Ln(4)
}

func sendPDF(c *gin.Context, pdf *fpdf.Fpdf, fileName string) {
	c.Header("Content-Type", "application/pdf")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fileName))
	if err := pdf.Output(c.Writer); err != nil {
		c.Status(http.StatusInternalServerError)
	}
}

// ExportGSTFilingSummaryPDF — one-page(ish) filing summary PDF.
func ExportGSTFilingSummaryPDF(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	scope, ok := scopeOr400(c)
	if !ok {
		return
	}
	_, _, ss, ps, liab := computeGSTReport(userID, scope)
	period := findPeriodForScope(userID, scope)
	payable := liab.Total + liab.RCMLiability
	meta := gstMetaFromScope(c, scope, "GST Filing Summary (Composition)")

	pdf := gstPDFBase("GST Filing Summary - Composition Scheme", meta)

	pdf.SetFont("Helvetica", "B", 10)
	pdf.Cell(0, 6, "Outward Supplies")
	pdf.Ln(6)
	gstPDFTable(pdf,
		[]string{"Particulars", "Amount (INR)"},
		[]float64{140, 40},
		[][]string{
			{"Gross Sales", money(ss.GrossSales)},
			{"Discounts", money(ss.DiscountAmount)},
			{"Sales Returns + Credit Notes", money(ss.SalesReturnAmount + ss.CreditNoteValue)},
			{"Net Sales", money(ss.NetSales)},
			{"Taxable Outward Supplies", money(ss.TaxableSupplyValue)},
			{"Exempt Supplies", money(ss.ExemptSupplyValue)},
			{"Nil Rated Supplies", money(ss.NilRatedSupplyValue)},
			{"Non-GST Supplies", money(ss.NonGSTSupplyValue)},
			{"Inter-state Outward Supplies (review)", money(ss.InterStateOutwardValue)},
		})

	pdf.SetFont("Helvetica", "B", 10)
	pdf.Cell(0, 6, "Inward Supplies")
	pdf.Ln(6)
	gstPDFTable(pdf,
		[]string{"Particulars", "Amount (INR)"},
		[]float64{140, 40},
		[][]string{
			{"Purchases from Registered Suppliers", money(ps.RegisteredSupplierPurchaseValue)},
			{"Purchases from Unregistered Suppliers", money(ps.UnregisteredSupplierPurchaseValue)},
			{"RCM Taxable Value", money(ps.RCMTaxableValue)},
			{"RCM Tax Liability", money(ps.RCMTaxLiability)},
		})

	pdf.SetFont("Helvetica", "B", 10)
	pdf.Cell(0, 6, "Tax Liability")
	pdf.Ln(6)
	gstPDFTable(pdf,
		[]string{"Particulars", "Amount (INR)"},
		[]float64{140, 40},
		[][]string{
			{"Composition Turnover Basis", money(liab.TurnoverBasis)},
			{fmt.Sprintf("Composition Rate %.2f%%", liab.CompositionRate), ""},
			{"CGST Liability", money(liab.CGST)},
			{"SGST Liability", money(liab.SGST)},
			{"Total Composition Liability", money(liab.Total)},
			{"Reverse Charge Liability", money(liab.RCMLiability)},
			{"Total Payable", money(payable)},
		})

	// Payment position.
	var paid, confirmed float64
	if period != nil {
		var payments []models.GSTTaxPayment
		utils.DB.Where("gst_return_period_id = ?", period.ID).Find(&payments)
		for _, p := range payments {
			paid += p.Amount
			if p.PortalConfirmed {
				confirmed += p.Amount
			}
		}
	}
	pdf.SetFont("Helvetica", "B", 10)
	pdf.Cell(0, 6, "Payment Reconciliation")
	pdf.Ln(6)
	gstPDFTable(pdf,
		[]string{"Particulars", "Amount (INR)"},
		[]float64{140, 40},
		[][]string{
			{"Tax Paid (recorded)", money(paid)},
			{"Tax Paid (portal confirmed)", money(confirmed)},
			{"Outstanding", money(payable - paid)},
		})

	// Validation summary.
	if period != nil {
		var openErr, openWarn int64
		utils.DB.Model(&models.GSTValidationIssue{}).Where("gst_return_period_id = ? AND resolution_status = 'open' AND severity = 'ERROR'", period.ID).Count(&openErr)
		utils.DB.Model(&models.GSTValidationIssue{}).Where("gst_return_period_id = ? AND resolution_status = 'open' AND severity = 'WARNING'", period.ID).Count(&openWarn)
		pdf.SetFont("Helvetica", "B", 10)
		pdf.Cell(0, 6, "Validation")
		pdf.Ln(6)
		pdf.SetFont("Helvetica", "", 9)
		pdf.Cell(0, 5, fmt.Sprintf("Open errors: %d    Open warnings: %d", openErr, openWarn))
		pdf.Ln(6)
	}

	fileName := gstFileName(scope, "filing_summary", "pdf")
	recordGSTExport(userID, actorUserID(c), period, "filing_summary", "pdf", fileName, 0)
	sendPDF(c, pdf, fileName)
}

// ExportGSTCMP08PDF — CMP-08 working paper PDF.
func ExportGSTCMP08PDF(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	scope, ok := scopeOr400(c)
	if !ok {
		return
	}
	if scope.PeriodType != models.GSTPeriodTypeQuarterly && scope.Period == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "CMP-08 export requires a quarterly period"})
		return
	}
	sales, purchases, ss, _, liab := computeGSTReport(userID, scope)
	period := findPeriodForScope(userID, scope)
	payable := liab.Total + liab.RCMLiability
	meta := gstMetaFromScope(c, scope, fmt.Sprintf("CMP-08 Working Paper - %s Q%d", scope.FY, scope.Quarter))

	pdf := gstPDFBase(fmt.Sprintf("CMP-08 Statement Working Paper - FY %s Q%d", scope.FY, scope.Quarter), meta)

	pdf.SetFont("Helvetica", "B", 10)
	pdf.Cell(0, 6, "Turnover and Self-Assessed Tax")
	pdf.Ln(6)
	gstPDFTable(pdf,
		[]string{"Particulars", "Amount (INR)"},
		[]float64{140, 40},
		[][]string{
			{"Taxable outward turnover", money(ss.TaxableSupplyValue)},
			{"Exempt outward turnover (incl. nil rated)", money(ss.ExemptSupplyValue + ss.NilRatedSupplyValue)},
			{"Non-GST supplies", money(ss.NonGSTSupplyValue)},
			{"Inter-state outward supplies (review)", money(ss.InterStateOutwardValue)},
			{"Composition turnover basis", money(liab.TurnoverBasis)},
			{fmt.Sprintf("Rate %.2f%% - CGST", liab.CompositionRate/2), money(liab.CGST)},
			{fmt.Sprintf("Rate %.2f%% - SGST", liab.CompositionRate/2), money(liab.SGST)},
			{"Total composition tax", money(liab.Total)},
			{"RCM taxable value", money(liab.RCMTaxableValue)},
			{"RCM tax liability", money(liab.RCMLiability)},
			{"Total payable", money(payable)},
		})

	if len(purchases) > 0 {
		pdf.SetFont("Helvetica", "B", 10)
		pdf.Cell(0, 6, "Reverse Charge Transactions")
		pdf.Ln(6)
		var rcmRows [][]string
		for _, r := range purchases {
			if !r.ReverseCharge {
				continue
			}
			rcmRows = append(rcmRows, []string{
				r.InvoiceNumber, r.InvoiceDate.Format(gstDateFormat), r.SupplierName, r.SupplierGSTIN,
				money(r.RCMTaxableValue), money(r.RCMTaxAmount),
			})
		}
		if len(rcmRows) == 0 {
			pdf.SetFont("Helvetica", "", 9)
			pdf.Cell(0, 5, "No reverse charge transactions recorded in this period.")
			pdf.Ln(8)
		} else {
			gstPDFTable(pdf,
				[]string{"Bill No", "Date", "Supplier", "GSTIN", "RCM Taxable", "RCM Tax"},
				[]float64{35, 25, 70, 40, 30, 30},
				rcmRows)
		}
	}

	// Payment position.
	var paid, confirmed float64
	var payments []models.GSTTaxPayment
	if period != nil {
		utils.DB.Where("gst_return_period_id = ?", period.ID).Order("payment_date ASC").Find(&payments)
	}
	pdf.SetFont("Helvetica", "B", 10)
	pdf.Cell(0, 6, "Payment Reconciliation")
	pdf.Ln(6)
	var payRows [][]string
	for _, p := range payments {
		paid += p.Amount
		conf := "No"
		if p.PortalConfirmed {
			conf = "Yes"
			confirmed += p.Amount
		}
		payRows = append(payRows, []string{p.PaymentDate.Format(gstDateFormat), money(p.Amount), p.Component, p.ChallanReference, conf})
	}
	gstPDFTable(pdf,
		[]string{"Date", "Amount", "Component", "Challan Ref", "Portal Confirmed"},
		[]float64{30, 30, 50, 50, 30},
		payRows)
	pdf.SetFont("Helvetica", "", 9)
	pdf.Cell(0, 5, fmt.Sprintf("Tax paid (recorded): %s    Portal confirmed: %s    Outstanding: %s",
		money(paid), money(confirmed), money(payable-paid)))
	pdf.Ln(6)

	// Outstanding validation issues.
	if period != nil {
		var issues []models.GSTValidationIssue
		utils.DB.Where("gst_return_period_id = ? AND resolution_status = 'open'", period.ID).
			Order("severity ASC").Limit(30).Find(&issues)
		if len(issues) > 0 {
			pdf.SetFont("Helvetica", "B", 10)
			pdf.Cell(0, 6, "Outstanding Validation Issues")
			pdf.Ln(6)
			var rows [][]string
			for _, i := range issues {
				msg := i.Message
				if len(msg) > 110 {
					msg = msg[:107] + "..."
				}
				rows = append(rows, []string{i.Severity, i.Code, msg})
			}
			gstPDFTable(pdf, []string{"Severity", "Code", "Message"}, []float64{25, 50, 200}, rows)
		}
	}

	fileName := gstFileName(scope, "cmp08", "pdf")
	recordGSTExport(userID, actorUserID(c), period, "cmp08", "pdf", fileName, len(sales)+len(purchases))
	sendPDF(c, pdf, fileName)
}

func money(v float64) string {
	return fmt.Sprintf("%.2f", roundMoney(v))
}

// ListGSTReportExports — export history.
func ListGSTReportExports(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	q := utils.DB.Where("user_id = ?", userID)
	if pid := strings.TrimSpace(c.Query("period_id")); pid != "" {
		q = q.Where("gst_return_period_id = ?", pid)
	}
	if et := strings.TrimSpace(c.Query("export_type")); et != "" {
		q = q.Where("export_type = ?", et)
	}
	var exports []models.GSTReportExport
	q.Order("generated_at DESC").Limit(200).Find(&exports)
	c.JSON(http.StatusOK, gin.H{"data": exports})
}
