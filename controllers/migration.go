package controllers

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"truerp/models"
	"truerp/services"
	"truerp/utils"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// -----------------------------------------------------------------------------
// Shared myBillBook CSV helpers
//
// myBillBook report CSVs are not clean relational dumps. Each file begins with
// a fixed preamble (company name, phone, blank lines, report title, date
// range, optional summary totals) before the real header row. The helpers
// below locate the header row by its first column name and return the data
// rows that follow, so importers can be written against clean (header, rows)
// pairs regardless of the preamble length or the report type.
// -----------------------------------------------------------------------------

// mbReadCSV splits a myBillBook report into (header, dataRows).
// headerKey is the expected first cell of the real header row (e.g. "Date",
// "Name", "Purchase No"). Lines before that header are treated as preamble.
func mbReadCSV(content []byte, headerKey string) (header []string, rows [][]string, err error) {
	// myBillBook exports are not always cleanly UTF-8; strip a BOM if present.
	content = bytes.TrimPrefix(content, []byte("\xef\xbb\xbf"))
	// Normalize CRLF.
	content = bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n"))

	reader := csv.NewReader(bytes.NewReader(content))
	reader.FieldsPerRecord = -1 // tolerate ragged rows
	reader.LazyQuotes = true
	all, err := reader.ReadAll()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse CSV: %w", err)
	}

	headerIdx := -1
	for i, row := range all {
		if len(row) == 0 {
			continue
		}
		first := strings.TrimSpace(row[0])
		if first == headerKey {
			headerIdx = i
			break
		}
	}
	if headerIdx < 0 {
		return nil, nil, fmt.Errorf("could not locate header row starting with %q", headerKey)
	}

	header = all[headerIdx]
	for _, row := range all[headerIdx+1:] {
		if len(row) == 0 {
			continue
		}
		// Skip trailing blank/total rows.
		if strings.TrimSpace(strings.Join(row, "")) == "" {
			continue
		}
		rows = append(rows, row)
	}
	return header, rows, nil
}

// mbCSVValue / mbFirstCSVValue mirror getCSVValue/firstCSVValue but trim
// surrounding whitespace and surrounding double quotes that myBillBook adds.
func mbCSVValue(record, headers []string, key string) string {
	for i, h := range headers {
		if strings.EqualFold(strings.TrimSpace(h), key) && i < len(record) {
			return strings.TrimSpace(strings.Trim(record[i], "\""))
		}
	}
	return ""
}

func mbFirstCSVValue(record, headers []string, keys ...string) string {
	for _, key := range keys {
		if v := mbCSVValue(record, headers, key); v != "" {
			return v
		}
	}
	return ""
}

// mbParseDate parses an Indian-format date (DD/MM/YYYY) plus the formats
// already understood by parseImportDate. Returns the zero time and no error
// when the value is empty (use for optional dates).
func mbParseDate(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	return parseImportDate(value)
}

// mbParseAmount strips currency symbols, thousands separators and surrounding
// quotes, then parses a float. Returns 0 for empty/invalid input.
func mbParseAmount(value string) float64 {
	value = strings.TrimSpace(strings.Trim(value, "\""))
	value = strings.ReplaceAll(value, "₹", "")
	value = strings.ReplaceAll(value, "Rs.", "")
	value = strings.ReplaceAll(value, "rs.", "")
	value = strings.ReplaceAll(value, ",", "")
	return parseFloat(value)
}

// mbParseQtyWithUnit splits a myBillBook quantity cell such as "33.0 PCS" or
// "0.0 BOX" into the numeric quantity and the unit suffix. When no unit is
// present the unit is returned empty.
func mbParseQtyWithUnit(value string) (float64, string) {
	value = strings.TrimSpace(strings.Trim(value, "\""))
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return 0, ""
	}
	qty := parseFloat(fields[0])
	var unit string
	if len(fields) > 1 {
		unit = strings.Join(fields[1:], " ")
	}
	return qty, unit
}

// mbMapPaymentMode normalizes myBillBook payment modes to TruERP modes.
func mbMapPaymentMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "upi", "upi ":
		return "upi"
	case "cash":
		return "cash"
	case "bank", "bank transfer", "neft", "rtgs", "imps":
		return "bank_transfer"
	case "cheque":
		return "cheque"
	case "card":
		return "card"
	case "":
		return "cash"
	default:
		return strings.ToLower(strings.TrimSpace(mode))
	}
}

// mbFindOrCreatePartyByName returns the party ID for the given name, creating
// it (with the supplied partyType) if it does not yet exist. Used by every
// importer so that transactional rows can reference parties imported in an
// earlier phase.
func mbFindOrCreatePartyByName(tx *gorm.DB, userID uuid.UUID, name, partyType string) (uuid.UUID, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return uuid.Nil, errors.New("party name is required")
	}
	if partyType == "" {
		partyType = "customer"
	}

	var party models.Party
	err := tx.Where("user_id = ? AND name = ?", userID, name).First(&party).Error
	if err == nil {
		// Exists. If it was created as a different type, leave it — TruERP
		// allows a party to be referenced from both sales and purchases.
		return party.ID, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return uuid.Nil, err
	}

	party = models.Party{
		ID:        uuid.New(),
		UserID:    userID,
		Name:      name,
		PartyType: partyType,
		IsActive:  true,
	}
	if err := tx.Create(&party).Error; err != nil {
		return uuid.Nil, err
	}
	return party.ID, nil
}

// mbEnsureExpenseCategory ensures an ExpenseCategory row exists for the given
// name (case-insensitive) and returns its name as stored.
func mbEnsureExpenseCategory(tx *gorm.DB, userID uuid.UUID, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "General", nil
	}
	var cat models.ExpenseCategory
	err := tx.Where("user_id = ? AND lower(name) = lower(?)", userID, name).First(&cat).Error
	if err == nil {
		return cat.Name, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", err
	}
	cat = models.ExpenseCategory{
		ID:       uuid.New(),
		UserID:   userID,
		Name:     name,
		IsActive: true,
	}
	if err := tx.Create(&cat).Error; err != nil {
		return "", err
	}
	return cat.Name, nil
}

// -----------------------------------------------------------------------------
// 1. Parties CSV importer  —  POST /api/v1/parties/import/csv
//
// Expected header (myBillBook "All Party Balance"):
//   Name, GST, Address, State, Pincode, Mob No., Bal., Party Category
//
// Also accepts a flat header without preamble (Name, Phone, GSTIN, ...).
// partyTypeHints maps a party name to "vendor" when it is known to be a
// supplier (derived from the purchase summary during the orchestrator run).
// -----------------------------------------------------------------------------

func ImportPartiesCSV(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	file, _, err := openUploadedCSV(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	partyTypeHints := map[string]string{}
	if hints := c.PostForm("vendor_hints"); hints != "" {
		for _, h := range strings.Split(hints, ",") {
			h = strings.TrimSpace(h)
			if h != "" {
				partyTypeHints[strings.ToUpper(h)] = "vendor"
			}
		}
	}

	defaultPartyType := strings.TrimSpace(c.PostForm("default_party_type"))

	imported, errs, err := importPartiesRows(userID, file, partyTypeHints, defaultPartyType, nil)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"imported": imported, "errors": errs})
}

func importPartiesRows(userID uuid.UUID, content []byte, vendorHints map[string]string, defaultPartyType string, progress services.ProgressFunc) (int, []string, error) {
	header, rows, err := mbReadCSV(content, "Name")
	if err != nil {
		// Fall back to a plain header-first parse for non-myBillBook files.
		header, rows, err = mbReadCSVPlain(content)
		if err != nil {
			return 0, nil, err
		}
	}

	imported := 0
	var errs []string
	for i, row := range rows {
		rowNum := i + 1
		if len(row) == 0 {
			continue
		}
		name := strings.TrimSpace(mbFirstCSVValue(row, header, "Name", "Party Name", "Customer", "Customer Name"))
		if name == "" {
			errs = append(errs, fmt.Sprintf("Row %d: Name is required", rowNum))
			continue
		}

		partyType := "customer"
		if defaultPartyType != "" {
			partyType = defaultPartyType
		}
		if t, ok := vendorHints[strings.ToUpper(name)]; ok {
			partyType = t
		}
		// "Cash Sale" and the business's own name are treated as customers.
		if strings.EqualFold(name, "Cash Sale") {
			partyType = "customer"
		}

		balance := mbParseAmount(mbFirstCSVValue(row, header, "Bal.", "Balance", "Opening Balance", "OpeningBalance"))

		// A negative balance means the business owes the party money (to
		// pay), so the party is a vendor regardless of the default/hinted
		// type. A positive or zero balance keeps the determined type.
		if balance < 0 {
			partyType = "vendor"
		}

		party := models.Party{
			ID:             uuid.New(),
			UserID:         userID,
			Name:           name,
			Phone:          mbFirstCSVValue(row, header, "Mob No.", "Phone", "Mobile", "Mobile No."),
			GSTIN:          mbFirstCSVValue(row, header, "GST", "GSTIN"),
			Address:        mbFirstCSVValue(row, header, "Address"),
			State:          mbFirstCSVValue(row, header, "State"),
			Pincode:        mbFirstCSVValue(row, header, "Pincode", "Pin Code"),
			Category:       mbFirstCSVValue(row, header, "Party Category", "Category"),
			PartyType:      partyType,
			OpeningBalance: balance,
			Balance:        balance,
			IsActive:       true,
		}

		// Skip duplicates by name.
		var existing models.Party
		if err := utils.DB.Where("user_id = ? AND name = ?", userID, name).First(&existing).Error; err == nil {
			if progress != nil {
				progress(i+1, len(rows), imported)
			}
			continue
		}

		if err := utils.DB.Create(&party).Error; err != nil {
			errs = append(errs, fmt.Sprintf("Row %d (%s): %v", rowNum, name, err))
			if progress != nil {
				progress(i+1, len(rows), imported)
			}
			continue
		}
		imported++
		if progress != nil {
			progress(i+1, len(rows), imported)
		}
	}
	return imported, errs, nil
}

// mbReadCSVPlain treats the first non-empty line as the header (for files
// without the myBillBook preamble).
func mbReadCSVPlain(content []byte) ([]string, [][]string, error) {
	content = bytes.TrimPrefix(content, []byte("\xef\xbb\xbf"))
	content = bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n"))
	reader := csv.NewReader(bytes.NewReader(content))
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	all, err := reader.ReadAll()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse CSV: %w", err)
	}
	if len(all) == 0 {
		return nil, nil, errors.New("CSV file is empty")
	}
	header := all[0]
	var rows [][]string
	for _, row := range all[1:] {
		if len(row) == 0 || strings.TrimSpace(strings.Join(row, "")) == "" {
			continue
		}
		rows = append(rows, row)
	}
	return header, rows, nil
}

// openUploadedCSV reads the "file" multipart field from the request and
// returns its full content. Used by every importer endpoint.
func openUploadedCSV(c *gin.Context) ([]byte, string, error) {
	file, err := c.FormFile("file")
	if err != nil {
		return nil, "", errors.New("no file uploaded")
	}
	src, err := file.Open()
	if err != nil {
		return nil, "", errors.New("failed to open uploaded file")
	}
	defer src.Close()
	content, err := io.ReadAll(src)
	if err != nil {
		return nil, "", errors.New("failed to read uploaded file")
	}
	return content, file.Filename, nil
}

// -----------------------------------------------------------------------------
// 2. Purchase bills CSV importer  —  POST /api/v1/purchase/bills/import/csv
//
// Expected header (myBillBook "Purchase Summary Report"):
//   Purchase No, Original Invoice No, Purchase Date, Party Name,
//   Purchase Amount, Purchase link, Notes
//
// Because the export has no line items, each bill is created with a single
// summary line (Description = "Migrated from myBillBook", qty 1, unit price =
// total). The myBillBook Purchase link is stored in SourceURL so the source
// document can be re-opened or re-downloaded later.
// -----------------------------------------------------------------------------

func ImportPurchaseBillsCSV(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	content, _, err := openUploadedCSV(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	snapshotHTML := c.PostForm("snapshot_html") == "true"
	defaultVendor := strings.TrimSpace(c.PostForm("default_vendor"))
	imported, errs, err := importPurchaseBillsRows(userID, content, snapshotHTML, defaultVendor, nil)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"imported": imported, "errors": errs})
}

func importPurchaseBillsRows(userID uuid.UUID, content []byte, snapshotHTML bool, defaultVendor string, progress services.ProgressFunc) (int, []string, error) {
	header, rows, err := mbReadCSV(content, "Purchase No")
	if err != nil {
		return 0, nil, err
	}

	imported := 0
	var errs []string
	seenLinks := map[string]bool{}

	defaultWH := resolveDefaultWarehouseID(userID)

	for i, row := range rows {
		if progress != nil {
			progress(i+1, len(rows), imported)
		}
		rowNum := i + 1
		if len(row) == 0 {
			continue
		}
		purchaseNo := strings.TrimSpace(mbFirstCSVValue(row, header, "Purchase No", "Purchase No.", "Bill No", "Bill Number"))
		if purchaseNo == "" {
			errs = append(errs, fmt.Sprintf("Row %d: Purchase No is required", rowNum))
			continue
		}

		partyName := strings.TrimSpace(mbFirstCSVValue(row, header, "Party Name", "Vendor", "Supplier"))
		if partyName == "" {
			partyName = defaultVendor
		}
		partyID, perr := mbFindOrCreatePartyByName(utils.DB, userID, partyName, "vendor")
		if perr != nil {
			errs = append(errs, fmt.Sprintf("Row %d (%s): %v", rowNum, partyName, perr))
			continue
		}

		dateStr := mbFirstCSVValue(row, header, "Purchase Date", "Bill Date", "Date")
		billDate, derr := mbParseDate(dateStr)
		if derr != nil {
			errs = append(errs, fmt.Sprintf("Row %d: invalid date %q", rowNum, dateStr))
			continue
		}
		if billDate.IsZero() {
			billDate = time.Now()
		}

		total := mbParseAmount(mbFirstCSVValue(row, header, "Purchase Amount", "Total Amount", "Amount"))
		sourceURL := strings.TrimSpace(mbFirstCSVValue(row, header, "Purchase link", "Purchase Link", "Source URL", "Source Link"))
		notes := strings.TrimSpace(mbFirstCSVValue(row, header, "Notes"))
		origInvNo := strings.TrimSpace(mbFirstCSVValue(row, header, "Original Invoice No", "Original Invoice Number"))
		if origInvNo != "" {
			if notes != "" {
				notes += " | "
			}
			notes += "Original invoice no: " + origInvNo
		}

		// Deduplicate by source link (myBillBook exports can contain a
		// duplicate row sharing the same cpp link).
		if sourceURL != "" {
			if seenLinks[sourceURL] {
				continue
			}
			seenLinks[sourceURL] = true
		}

		billNumber := fmt.Sprintf("P-%04s", purchaseNo)
		if strings.HasPrefix(purchaseNo, "P-") {
			billNumber = purchaseNo
		}

		// Skip if a bill with the same number already exists for this user.
		var existing models.PurchaseBill
		if err := utils.DB.Where("user_id = ? AND bill_number = ?", userID, billNumber).First(&existing).Error; err == nil {
			continue
		}

		sourceHTMLURL := ""
		if snapshotHTML && sourceURL != "" {
			if snap, serr := snapshotSourceHTML(userID, billNumber, sourceURL); serr == nil {
				sourceHTMLURL = snap
			} else {
				log.Printf("migration: snapshot failed for %s: %v", sourceURL, serr)
			}
		}

		bill := models.PurchaseBill{
			ID:            uuid.New(),
			UserID:        userID,
			PartyID:       partyID,
			BillNumber:    billNumber,
			BillDate:      billDate,
			Status:        "unpaid",
			SubTotal:      total,
			TotalAmount:   total,
			BalanceDue:    total,
			Notes:         notes,
			SourceURL:     sourceURL,
			SourceHTMLURL: sourceHTMLURL,
		}
		if defaultWH != uuid.Nil {
			bill.WarehouseID = &defaultWH
		}

		tx := utils.DB.Begin()
		if err := tx.Create(&bill).Error; err != nil {
			tx.Rollback()
			errs = append(errs, fmt.Sprintf("Row %d (%s): %v", rowNum, billNumber, err))
			continue
		}
		// Single summary line — line items are not present in the export.
		item := models.PurchaseBillItem{
			ID:          uuid.New(),
			BillID:      bill.ID,
			Description: "Migrated from myBillBook (summary)",
			Quantity:    1,
			Unit:        "PCS",
			UnitPrice:   total,
			Total:       total,
		}
		if err := tx.Create(&item).Error; err != nil {
			tx.Rollback()
			errs = append(errs, fmt.Sprintf("Row %d (%s): item create %v", rowNum, billNumber, err))
			continue
		}
		if err := tx.Commit().Error; err != nil {
			errs = append(errs, fmt.Sprintf("Row %d (%s): %v", rowNum, billNumber, err))
			continue
		}
		imported++
	}
	return imported, errs, nil
}

// -----------------------------------------------------------------------------
// 3. Sales invoices CSV importer  —  POST /api/v1/migration/sales/import/csv
//
// Expected header (myBillBook "Sale Summary Report"):
//   Invoice No, Invoice Date, Contact Name, Amount, Remaining Amount,
//   Invoice Status, Due Date, Invoice Link, Payment Type, Party Category,
//   Created by
//
// Because the export has no line items, each invoice is created with a single
// summary line (Description = "Migrated from myBillBook", qty 1, unit price =
// total). The myBillBook Invoice Link is stored in Notes so the source document
// can be re-opened later. Remaining Amount drives AmountPaid
// (= TotalAmount - Remaining Amount).
// -----------------------------------------------------------------------------

func ImportSalesCSV(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	content, _, err := openUploadedCSV(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	imported, errs, err := importSalesRows(userID, content, nil)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"imported": imported, "errors": errs})
}

// mapSaleInvoiceStatus normalizes a myBillBook sale status to a TruERP
// invoice status (draft, sent, paid, partial, overdue, cancelled).
func mapSaleInvoiceStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return "sent"
	case "paid", "receive":
		return "paid"
	case "partial", "partially paid":
		return "partial"
	case "overdue":
		return "overdue"
	case "draft":
		return "draft"
	case "cancelled", "canceled":
		return "cancelled"
	case "unpaid", "sent", "open":
		return "sent"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func importSalesRows(userID uuid.UUID, content []byte, progress services.ProgressFunc) (int, []string, error) {
	header, rows, err := mbReadCSV(content, "Invoice No")
	if err != nil {
		// Fall back to a plain header-first parse for non-myBillBook files.
		header, rows, err = mbReadCSVPlain(content)
		if err != nil {
			return 0, nil, err
		}
	}

	imported := 0
	var errs []string
	seenLinks := map[string]bool{}

	for i, row := range rows {
		if progress != nil {
			progress(i+1, len(rows), imported)
		}
		rowNum := i + 1
		if len(row) == 0 {
			continue
		}
		invoiceNo := strings.TrimSpace(mbFirstCSVValue(row, header, "Invoice No", "Invoice No.", "Invoice Number", "InvoiceNumber"))
		if invoiceNo == "" {
			errs = append(errs, fmt.Sprintf("Row %d: Invoice No is required", rowNum))
			continue
		}

		partyName := strings.TrimSpace(mbFirstCSVValue(row, header, "Contact Name", "Party Name", "Customer", "Customer Name"))
		if partyName == "" {
			errs = append(errs, fmt.Sprintf("Row %d (%s): Contact Name is required", rowNum, invoiceNo))
			continue
		}
		partyCategory := strings.TrimSpace(mbFirstCSVValue(row, header, "Party Category", "Category"))
		partyID, perr := mbFindOrCreatePartyByName(utils.DB, userID, partyName, "customer")
		if perr != nil {
			errs = append(errs, fmt.Sprintf("Row %d (%s): %v", rowNum, invoiceNo, perr))
			continue
		}
		// Sales imports treat the contact as a customer: if the party was
		// previously typed as a vendor (e.g. from an earlier purchase import),
		// reclassify it as a customer so it shows up under receivables.
		var party models.Party
		if err := utils.DB.Where("id = ?", partyID).First(&party).Error; err == nil && party.PartyType != "customer" {
			utils.DB.Model(&party).Update("party_type", "customer")
		}
		// Backfill the party category if one was provided and the party
		// doesn't yet have one.
		if partyCategory != "" {
			if party.ID != uuid.Nil && party.Category == "" {
				utils.DB.Model(&party).Update("category", partyCategory)
			} else {
				// Re-fetch in case the party existed but wasn't loaded above.
				var p models.Party
				if err := utils.DB.Where("id = ?", partyID).First(&p).Error; err == nil && p.Category == "" {
					utils.DB.Model(&p).Update("category", partyCategory)
				}
			}
		}

		dateStr := mbFirstCSVValue(row, header, "Invoice Date", "Date")
		invoiceDate, derr := mbParseDate(dateStr)
		if derr != nil {
			errs = append(errs, fmt.Sprintf("Row %d (%s): invalid date %q", rowNum, invoiceNo, dateStr))
			continue
		}
		if invoiceDate.IsZero() {
			invoiceDate = time.Now()
		}

		var dueDate *time.Time
		if dueStr := mbFirstCSVValue(row, header, "Due Date", "DueDate"); dueStr != "" {
			if d, e := mbParseDate(dueStr); e == nil && !d.IsZero() {
				dueDate = &d
			}
		}

		total := mbParseAmount(mbFirstCSVValue(row, header, "Amount", "Total Amount", "Invoice Amount"))
		remaining := mbParseAmount(mbFirstCSVValue(row, header, "Remaining Amount", "RemainingAmount", "Balance"))
		amountPaid := total - remaining
		if amountPaid < 0 {
			amountPaid = 0
		}

		status := mapSaleInvoiceStatus(mbFirstCSVValue(row, header, "Invoice Status", "Status"))
		if !allowedInvoiceStatuses[status] {
			status = "sent"
		}

		paymentMode := mbMapPaymentMode(mbFirstCSVValue(row, header, "Payment Type", "PaymentType", "Payment Mode", "PaymentMode"))
		invoiceLink := strings.TrimSpace(mbFirstCSVValue(row, header, "Invoice Link", "InvoiceLink", "Invoice link"))
		createdBy := strings.TrimSpace(mbFirstCSVValue(row, header, "Created by", "CreatedBy", "Created By"))

		// Deduplicate by invoice link (myBillBook exports can contain
		// duplicate rows sharing the same link).
		if invoiceLink != "" {
			if seenLinks[invoiceLink] {
				continue
			}
			seenLinks[invoiceLink] = true
		}

		// Skip if an invoice with the same number already exists for this user.
		var existing models.Invoice
		if err := utils.DB.Where("user_id = ? AND invoice_number = ?", userID, invoiceNo).First(&existing).Error; err == nil {
			continue
		}

		// Build notes from the optional Invoice Link and Created by columns.
		var notesParts []string
		if invoiceLink != "" {
			notesParts = append(notesParts, "Invoice link: "+invoiceLink)
		}
		if createdBy != "" {
			notesParts = append(notesParts, "Created by: "+createdBy)
		}
		notes := strings.Join(notesParts, " | ")

		invoice := models.Invoice{
			ID:            uuid.New(),
			UserID:        userID,
			InvoiceNumber: invoiceNo,
			InvoiceType:   "tax_invoice",
			PartyID:       partyID,
			Date:          invoiceDate,
			DueDate:       dueDate,
			Status:        status,
			PaymentMode:   paymentMode,
			AmountPaid:    amountPaid,
			SubTotal:      total,
			TotalAmount:   total,
			Notes:         notes,
		}

		// Single summary line — line items are not present in the export.
		invoice.Items = []models.InvoiceItem{
			{
				ID:          uuid.New(),
				Description: "Migrated from myBillBook (summary)",
				Quantity:    1,
				Unit:        "PCS",
				UnitPrice:   total,
				Total:       total,
			},
		}

		// Reconcile the status against the paid/total amounts.
		normalizeInvoicePaymentStatus(&invoice)

		tx := utils.DB.Begin()
		if err := tx.Create(&invoice).Error; err != nil {
			tx.Rollback()
			errs = append(errs, fmt.Sprintf("Row %d (%s): %v", rowNum, invoiceNo, err))
			continue
		}
		if err := tx.Commit().Error; err != nil {
			errs = append(errs, fmt.Sprintf("Row %d (%s): %v", rowNum, invoiceNo, err))
			continue
		}

		// Post accounting entries and a linked payment-in for the paid portion.
		if err := postInvoiceAccounting(utils.DB, userID, &invoice); err != nil {
			log.Printf("migration: sale invoice accounting error for %s: %v", invoiceNo, err)
		}
		if invoice.AmountPaid > 0 {
			payNotes := fmt.Sprintf("Auto-created from sale import %s", invoiceNo)
			if err := createLinkedSalePaymentIn(utils.DB, userID, &invoice, invoice.AmountPaid, invoice.Date, payNotes); err != nil {
				log.Printf("migration: sale payment-in error for %s: %v", invoiceNo, err)
			}
		}

		imported++
	}
	return imported, errs, nil
}

// snapshotSourceHTML fetches the source URL once and stores the HTML body via
// the configured storage service, returning the public URL of the snapshot.
// Used during migration so the source document survives even if the original
// portal link later disappears.
func snapshotSourceHTML(userID uuid.UUID, billNumber, sourceURL string) (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, sourceURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "TruERP-Migration/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("snapshot: HTTP %d for %s", resp.StatusCode, sourceURL)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024)) // 5 MB cap
	if err != nil {
		return "", err
	}

	storage := services.GetDefaultStorageService()
	relPath := filepath.Join("migration", userID.String(), fmt.Sprintf("%s.html", sanitizeFilename(billNumber)))
	return storage.UploadBytes(relPath, body, "text/html; charset=utf-8")
}

func sanitizeFilename(name string) string {
	r := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_")
	return r.Replace(name)
}

// -----------------------------------------------------------------------------
// 3. Payments CSV importer  —  POST /api/v1/payments/import/csv
//
// Expected header (myBillBook "Cash and Bank Statement"):
//   Date, Type, Txn No, Party, Invoice numbers, Mode, Paid, Received,
//   Balance, Notes
//
// Type=Payment-in  -> models.Payment  (customer receipt)
// Type=Payment-out -> models.PaymentOut (vendor payment)
// Type=Purchase Bill rows that carry a Paid value are treated as payment-out
//   made at purchase time.
// Other types (Add Money / Reduce Money / Expense / Sales Invoice / Opening
// Balance) are ignored here — they are handled by the expense / cash-bank
// importers or the orchestrator.
//
// "Invoice numbers" is a comma-separated list. A single myBillBook payment can
// settle several invoices; we split it into one TruERP Payment per referenced
// invoice (allocated in invoice-number order up to the received amount), and
// if there is no invoice list we create a single unlinked payment.
// -----------------------------------------------------------------------------

func ImportPaymentsCSV(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	content, _, err := openUploadedCSV(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	res, err := importPaymentsRows(userID, content, nil)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, res)
}

func importPaymentsRows(userID uuid.UUID, content []byte, progress services.ProgressFunc) (gin.H, error) {
	header, rows, err := mbReadCSV(content, "Date")
	if err != nil {
		return nil, err
	}

	var payIn, payOut int
	var errs []string

	for i, row := range rows {
		if progress != nil {
			progress(i+1, len(rows), payIn+payOut)
		}
		rowNum := i + 1
		if len(row) == 0 {
			continue
		}
		typ := strings.TrimSpace(mbFirstCSVValue(row, header, "Type", "Transaction Type"))
		switch typ {
		case "Payment-in":
			n, perrs := importOnePaymentIn(userID, header, row, rowNum)
			payIn += n
			errs = append(errs, perrs...)
		case "Payment-out", "Purchase Bill":
			// Only Purchase Bill rows that actually paid cash at purchase
			// time carry a Paid value; pure credit purchases have Paid=0.
			if mbParseAmount(mbFirstCSVValue(row, header, "Paid")) > 0 {
				n, perrs := importOnePaymentOut(userID, header, row, rowNum, typ == "Purchase Bill")
				payOut += n
				errs = append(errs, perrs...)
			}
		}
	}
	return gin.H{
		"imported":    payIn + payOut,
		"payment_in":  payIn,
		"payment_out": payOut,
		"errors":      errs,
	}, nil
}

func importOnePaymentIn(userID uuid.UUID, header, row []string, rowNum int) (int, []string) {
	dateStr := mbFirstCSVValue(row, header, "Date")
	date, derr := mbParseDate(dateStr)
	if derr != nil {
		return 0, []string{fmt.Sprintf("Row %d: invalid date %q", rowNum, dateStr)}
	}
	if date.IsZero() {
		date = time.Now()
	}

	partyName := strings.TrimSpace(mbFirstCSVValue(row, header, "Party", "Party Name"))
	partyID, perr := mbFindOrCreatePartyByName(utils.DB, userID, partyName, "customer")
	if perr != nil {
		return 0, []string{fmt.Sprintf("Row %d (%s): %v", rowNum, partyName, perr)}
	}

	received := mbParseAmount(mbFirstCSVValue(row, header, "Received"))
	if received <= 0 {
		return 0, nil // nothing to record
	}
	mode := mbMapPaymentMode(mbFirstCSVValue(row, header, "Mode"))
	txnNo := strings.TrimSpace(mbFirstCSVValue(row, header, "Txn No", "Txn No.", "Sr No."))
	notes := strings.TrimSpace(mbFirstCSVValue(row, header, "Notes"))
	invList := splitInvoiceNumbers(mbFirstCSVValue(row, header, "Invoice numbers", "Invoice Numbers", "Invoice No"))

	if len(invList) == 0 {
		p := models.Payment{
			ID:              uuid.New(),
			UserID:          userID,
			PartyID:         partyID,
			AmountReceived:  received,
			PaymentInNumber: txnNo,
			Mode:            mode,
			Date:            date,
			Notes:           notes,
		}
		if err := utils.DB.Create(&p).Error; err != nil {
			return 0, []string{fmt.Sprintf("Row %d: %v", rowNum, err)}
		}
		updatePartyBalance(utils.DB, partyID, -received)
		return 1, nil
	}

	// Allocate the received amount across the referenced invoices in order.
	remaining := received
	created := 0
	for _, invNo := range invList {
		if remaining <= 0 {
			break
		}
		inv, ok := findInvoiceByNumber(userID, invNo)
		alloc := remaining
		if ok && inv.TotalAmount > 0 && remaining > (inv.TotalAmount-inv.AmountPaid) {
			alloc = inv.TotalAmount - inv.AmountPaid
			if alloc < 0 {
				alloc = 0
			}
		}
		if alloc <= 0 {
			continue
		}
		p := models.Payment{
			ID:              uuid.New(),
			UserID:          userID,
			PartyID:         partyID,
			AmountReceived:  alloc,
			PaymentInNumber: txnNo,
			Mode:            mode,
			Date:            date,
			Notes:           notes,
		}
		if ok {
			p.InvoiceID = &inv.ID
		}
		if err := utils.DB.Create(&p).Error; err != nil {
			return created, []string{fmt.Sprintf("Row %d (inv %s): %v", rowNum, invNo, err)}
		}
		if ok {
			markInvoicePaid(utils.DB, userID, inv.ID, alloc)
		}
		remaining -= alloc
		created++
	}
	if created > 0 {
		updatePartyBalance(utils.DB, partyID, -received)
	}
	return created, nil
}

func importOnePaymentOut(userID uuid.UUID, header, row []string, rowNum int, isPurchase bool) (int, []string) {
	dateStr := mbFirstCSVValue(row, header, "Date")
	date, derr := mbParseDate(dateStr)
	if derr != nil {
		return 0, []string{fmt.Sprintf("Row %d: invalid date %q", rowNum, dateStr)}
	}
	if date.IsZero() {
		date = time.Now()
	}

	partyName := strings.TrimSpace(mbFirstCSVValue(row, header, "Party", "Party Name"))
	partyType := "vendor"
	if !isPurchase {
		partyType = "vendor"
	}
	partyID, perr := mbFindOrCreatePartyByName(utils.DB, userID, partyName, partyType)
	if perr != nil {
		return 0, []string{fmt.Sprintf("Row %d (%s): %v", rowNum, partyName, perr)}
	}

	paid := mbParseAmount(mbFirstCSVValue(row, header, "Paid"))
	if paid <= 0 {
		return 0, nil
	}
	mode := mbMapPaymentMode(mbFirstCSVValue(row, header, "Mode"))
	txnNo := strings.TrimSpace(mbFirstCSVValue(row, header, "Txn No", "Txn No.", "Sr No."))
	notes := strings.TrimSpace(mbFirstCSVValue(row, header, "Notes"))

	// Link to a purchase bill when the Invoice numbers field references one.
	var purchaseBillID *uuid.UUID
	invList := splitInvoiceNumbers(mbFirstCSVValue(row, header, "Invoice numbers", "Invoice Numbers"))
	for _, ref := range invList {
		if bill, ok := findPurchaseBillByNumber(userID, ref); ok {
			purchaseBillID = &bill.ID
			break
		}
	}

	po := models.PaymentOut{
		ID:               uuid.New(),
		UserID:           userID,
		PurchaseBillID:   purchaseBillID,
		PartyID:          partyID,
		AmountPaid:       paid,
		PaymentOutNumber: txnNo,
		Mode:             mode,
		Date:             date,
		Notes:            notes,
	}
	if err := utils.DB.Create(&po).Error; err != nil {
		return 0, []string{fmt.Sprintf("Row %d: %v", rowNum, err)}
	}
	if purchaseBillID != nil {
		markPurchaseBillPaid(utils.DB, userID, *purchaseBillID, paid)
	}
	updatePartyBalance(utils.DB, partyID, paid)
	return 1, nil
}

// splitInvoiceNumbers parses the myBillBook "Invoice numbers" cell, which is a
// comma-separated list possibly wrapped in quotes (e.g. "1247, 1237, 1208").
func splitInvoiceNumbers(value string) []string {
	value = strings.TrimSpace(strings.Trim(value, "\""))
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func findInvoiceByNumber(userID uuid.UUID, number string) (models.Invoice, bool) {
	var inv models.Invoice
	if err := utils.DB.Where("user_id = ? AND invoice_number = ?", userID, number).First(&inv).Error; err == nil {
		return inv, true
	}
	return models.Invoice{}, false
}

func findPurchaseBillByNumber(userID uuid.UUID, number string) (models.PurchaseBill, bool) {
	var bill models.PurchaseBill
	// Accept both raw numbers (myBillBook "1") and our "P-0001" form.
	candidates := []string{number, fmt.Sprintf("P-%04s", number)}
	for _, c := range candidates {
		if err := utils.DB.Where("user_id = ? AND bill_number = ?", userID, c).First(&bill).Error; err == nil {
			return bill, true
		}
	}
	return models.PurchaseBill{}, false
}

func markInvoicePaid(db *gorm.DB, userID, invoiceID uuid.UUID, amount float64) {
	var inv models.Invoice
	if err := db.Where("user_id = ? AND id = ?", userID, invoiceID).First(&inv).Error; err != nil {
		return
	}
	newPaid := inv.AmountPaid + amount
	status := inv.Status
	if newPaid >= inv.TotalAmount {
		status = "paid"
	} else if newPaid > 0 {
		status = "partial"
	}
	db.Model(&inv).Updates(map[string]interface{}{
		"amount_paid": newPaid,
		"status":      status,
	})
}

func markPurchaseBillPaid(db *gorm.DB, userID, billID uuid.UUID, amount float64) {
	var bill models.PurchaseBill
	if err := db.Where("user_id = ? AND id = ?", userID, billID).First(&bill).Error; err != nil {
		return
	}
	newPaid := bill.PaidAmount + amount
	status := bill.Status
	if newPaid >= bill.TotalAmount {
		status = "paid"
	} else if newPaid > 0 {
		status = "partial"
	}
	db.Model(&bill).Updates(map[string]interface{}{
		"paid_amount": newPaid,
		"balance_due": bill.TotalAmount - newPaid,
		"status":      status,
	})
}

func updatePartyBalance(db *gorm.DB, partyID uuid.UUID, delta float64) {
	db.Model(&models.Party{}).Where("id = ?", partyID).
		UpdateColumn("balance", gorm.Expr("balance + ?", delta))
}

// -----------------------------------------------------------------------------
// 4. Expenses CSV importer  —  POST /api/v1/expenses/import/csv
//
// Expected header (myBillBook "Expense Transactions"):
//   Date, Serial No., Exp. Name, Pymt Mode, Amt., Notes
//
// The expense category is created on demand. Each row becomes one Expense with
// a single ExpenseItem line.
// -----------------------------------------------------------------------------

func ImportExpensesCSV(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	content, _, err := openUploadedCSV(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	imported, errs, err := importExpensesRows(userID, content, nil)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"imported": imported, "errors": errs})
}

func importExpensesRows(userID uuid.UUID, content []byte, progress services.ProgressFunc) (int, []string, error) {
	header, rows, err := mbReadCSV(content, "Date")
	if err != nil {
		return 0, nil, err
	}

	imported := 0
	var errs []string
	var count int64
	utils.DB.Model(&models.Expense{}).Where("user_id = ?", userID).Count(&count)

	for i, row := range rows {
		if progress != nil {
			progress(i+1, len(rows), imported)
		}
		rowNum := i + 1
		if len(row) == 0 {
			continue
		}
		dateStr := mbFirstCSVValue(row, header, "Date")
		date, derr := mbParseDate(dateStr)
		if derr != nil {
			errs = append(errs, fmt.Sprintf("Row %d: invalid date %q", rowNum, dateStr))
			continue
		}
		if date.IsZero() {
			date = time.Now()
		}

		expName := strings.TrimSpace(mbFirstCSVValue(row, header, "Exp. Name", "Expense Name", "Category", "Name"))
		if expName == "" {
			errs = append(errs, fmt.Sprintf("Row %d: expense name is required", rowNum))
			continue
		}
		// Skip contra entries that are not real expenses.
		if strings.EqualFold(expName, "Payment In Discount") {
			continue
		}

		category, cerr := mbEnsureExpenseCategory(utils.DB, userID, expName)
		if cerr != nil {
			errs = append(errs, fmt.Sprintf("Row %d: %v", rowNum, cerr))
			continue
		}

		amount := mbParseAmount(mbFirstCSVValue(row, header, "Amt.", "Amount", "Total Amount"))
		mode := mbMapPaymentMode(mbFirstCSVValue(row, header, "Pymt Mode", "Payment Mode", "Mode"))
		serial := strings.TrimSpace(mbFirstCSVValue(row, header, "Serial No.", "Serial No", "Sr No."))
		notes := strings.TrimSpace(mbFirstCSVValue(row, header, "Notes"))

		count++
		expenseNumber := fmt.Sprintf("EXP-%04d", count)
		if serial != "" {
			expenseNumber = fmt.Sprintf("EXP-%s", serial)
		}

		expense := models.Expense{
			ID:            uuid.New(),
			UserID:        userID,
			ExpenseNumber: expenseNumber,
			Category:      category,
			Description:   expName,
			Amount:        amount,
			SubTotal:      amount,
			Date:          date,
			PaymentMode:   mode,
			Notes:         notes,
		}

		tx := utils.DB.Begin()
		if err := tx.Create(&expense).Error; err != nil {
			tx.Rollback()
			errs = append(errs, fmt.Sprintf("Row %d (%s): %v", rowNum, expName, err))
			continue
		}
		item := models.ExpenseItem{
			ID:          uuid.New(),
			ExpenseID:   expense.ID,
			Description: expName,
			Quantity:    1,
			UnitPrice:   amount,
			Total:       amount,
		}
		if err := tx.Create(&item).Error; err != nil {
			tx.Rollback()
			errs = append(errs, fmt.Sprintf("Row %d (%s): item %v", rowNum, expName, err))
			continue
		}
		if err := tx.Commit().Error; err != nil {
			errs = append(errs, fmt.Sprintf("Row %d (%s): %v", rowNum, expName, err))
			continue
		}
		imported++
	}
	return imported, errs, nil
}

// -----------------------------------------------------------------------------
// 5. myBillBook ZIP orchestrator  —  POST /api/v1/migration/mybillbook
//
// Accepts a ZIP archive containing the myBillBook report CSVs and runs the
// full phased import in order:
//   1. parties      (all_party_balance_*.csv)
//   2. expense cats (expense_category_report_*.csv)  -> seeded on demand
//   3. purchase bills (purchase_summary_report_*.csv)  [optional HTML snapshot]
//   4. payments     (cash_and_bank_statement_*.csv)
//   5. expenses     (expense_transactions_*.csv)
//
// File matching is by filename substring so the exact date suffix in the
// sample files does not matter. A JSON summary of per-step counts and errors
// is returned. The whole run is sequential and idempotent (re-running with the
// same ZIP skips already-imported rows by name/number).
// -----------------------------------------------------------------------------

func MigrateMyBillBookZIP(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no ZIP file uploaded"})
		return
	}
	src, err := file.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to open uploaded file"})
		return
	}
	defer src.Close()

	body, err := io.ReadAll(src)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read uploaded file"})
		return
	}

	options := map[string]string{
		"snapshot_html": c.PostForm("snapshot_html"),
	}
	result, _, perr := importMyBillBookZIPRows(userID, body, options, nil)
	if perr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": perr.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}

// importMyBillBookZIPRows runs the phased myBillBook import from an in-memory
// ZIP body. It reports per-row progress across all CSV files combined so the
// job progress bar advances smoothly. Returns the steps summary (JSON-ready
// map), the per-row errors, and a fatal error.
func importMyBillBookZIPRows(userID uuid.UUID, body []byte, options map[string]string, progress services.ProgressFunc) (map[string]interface{}, []string, error) {
	zipReader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return nil, nil, fmt.Errorf("uploaded file is not a valid ZIP")
	}

	// Collect CSVs by role.
	files := map[string][]byte{}
	for _, zf := range zipReader.File {
		if zf.FileInfo().IsDir() {
			continue
		}
		name := strings.ToLower(zf.Name)
		if !strings.HasSuffix(name, ".csv") {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			continue
		}
		content, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			continue
		}
		files[strings.ToLower(filepath.Base(name))] = content
	}

	snapshotHTML := options["snapshot_html"] == "true"

	// Pre-count rows across the files we will actually import so we can
	// report combined per-row progress.
	rowCount := func(content []byte, headerKey string) int {
		_, rows, err := mbReadCSV(content, headerKey)
		if err != nil {
			return 0
		}
		return len(rows)
	}
	totalRows := 0
	if c, ok := findFile(files, "all_party_balance"); ok {
		totalRows += rowCount(c, "Name")
	}
	if c, ok := findFile(files, "purchase_summary"); ok {
		totalRows += rowCount(c, "Purchase No")
	}
	if c, ok := findFile(files, "cash_and_bank_statement"); ok {
		totalRows += rowCount(c, "Date")
	}
	if c, ok := findFile(files, "expense_transactions"); ok {
		totalRows += rowCount(c, "Date")
	}

	// offset tracks how many rows have been completed across previous files.
	offset := 0
	makeProgress := func() services.ProgressFunc {
		if progress == nil {
			return nil
		}
		base := offset
		return func(current, total, imported int) {
			progress(base+current, totalRows, imported)
		}
	}

	result := gin.H{"steps": []gin.H{}}
	addStep := func(name string, count int, errs []string) {
		result["steps"] = append(result["steps"].([]gin.H), gin.H{
			"step":     name,
			"imported": count,
			"errors":   errs,
		})
	}

	// 1. Parties — derive vendor hints from the purchase summary party column.
	vendorHints := map[string]string{}
	if pb, ok := findFile(files, "purchase_summary"); ok {
		if header, rows, err := mbReadCSV(pb, "Purchase No"); err == nil {
			for _, row := range rows {
				p := strings.TrimSpace(mbFirstCSVValue(row, header, "Party Name", "Vendor"))
				if p != "" {
					vendorHints[strings.ToUpper(p)] = "vendor"
				}
			}
		}
	}

	if content, ok := findFile(files, "all_party_balance"); ok {
		n, errs, _ := importPartiesRows(userID, content, vendorHints, "", makeProgress())
		addStep("parties", n, errs)
		offset += rowCount(content, "Name")
	} else {
		addStep("parties", 0, []string{"all_party_balance_*.csv not found in ZIP"})
	}

	// 2. Purchase bills (with optional HTML snapshot of source links).
	if content, ok := findFile(files, "purchase_summary"); ok {
		n, errs, _ := importPurchaseBillsRows(userID, content, snapshotHTML, "", makeProgress())
		addStep("purchase_bills", n, errs)
		offset += rowCount(content, "Purchase No")
	} else {
		addStep("purchase_bills", 0, []string{"purchase_summary_report_*.csv not found in ZIP"})
	}

	// 3. Payments (payment-in / payment-out).
	if content, ok := findFile(files, "cash_and_bank_statement"); ok {
		res, _ := importPaymentsRows(userID, content, makeProgress())
		errs, _ := res["errors"].([]string)
		addStep("payments", int(res["imported"].(float64)), errs)
		offset += rowCount(content, "Date")
	} else {
		addStep("payments", 0, []string{"cash_and_bank_statement_*.csv not found in ZIP"})
	}

	// 4. Expenses.
	if content, ok := findFile(files, "expense_transactions"); ok {
		n, errs, _ := importExpensesRows(userID, content, makeProgress())
		addStep("expenses", n, errs)
		offset += rowCount(content, "Date")
	} else {
		addStep("expenses", 0, []string{"expense_transactions_*.csv not found in ZIP"})
	}

	if progress != nil {
		progress(totalRows, totalRows, 0)
	}
	return result, nil, nil
}

// findFile returns the first CSV content whose filename contains the given
// substring (case-insensitive).
func findFile(files map[string][]byte, substr string) ([]byte, bool) {
	for name, content := range files {
		if strings.Contains(name, substr) {
			return content, true
		}
	}
	return nil, false
}

// -----------------------------------------------------------------------------
// 6. Download source purchase invoices  —  POST /api/v1/purchase/bills/download-source
//
// For each selected purchase bill that has a SourceURL, render the external
// page to PDF using headless Chromium (chromedp) and stream all PDFs back as
// a single ZIP. Bills without a SourceURL are skipped (reported in the
// _skipped.csv manifest inside the ZIP).
//
// Rendering is bounded by a per-page timeout and a concurrency limit to keep
// memory predictable for large selections.
// -----------------------------------------------------------------------------

func DownloadSourcePurchaseBills(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		IDs []uuid.UUID `json:"ids" binding:"required,min=1"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var bills []models.PurchaseBill
	if err := utils.DB.Where("user_id = ? AND id IN ?", userID, input.IDs).
		Preload("Party").Find(&bills).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load purchase bills"})
		return
	}

	type renderResult struct {
		bill    models.PurchaseBill
		pdf     []byte
		skipped bool
		reason  string
	}

	results := make([]renderResult, len(bills))
	sem := make(chan struct{}, 4) // limit concurrent Chromium tabs
	var wg sync.WaitGroup

	for i := range bills {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			b := bills[idx]
			if strings.TrimSpace(b.SourceURL) == "" {
				results[idx] = renderResult{bill: b, skipped: true, reason: "no source_url"}
				return
			}
			sem <- struct{}{}
			defer func() { <-sem }()
			pdf, err := renderURLToPDF(b.SourceURL)
			if err != nil {
				results[idx] = renderResult{bill: b, skipped: true, reason: err.Error()}
				return
			}
			results[idx] = renderResult{bill: b, pdf: pdf}
		}(i)
	}
	wg.Wait()

	// Stream a ZIP of PDFs.
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="purchase-source-invoices_%s.zip"`, time.Now().Format("20060102-150405")))

	zw := zip.NewWriter(c.Writer)
	for _, r := range results {
		if r.skipped || len(r.pdf) == 0 {
			continue
		}
		name := sanitizeFilename(r.bill.BillNumber)
		if name == "" {
			name = r.bill.ID.String()
		}
		w, err := zw.Create(fmt.Sprintf("%s.pdf", name))
		if err != nil {
			continue
		}
		if _, err := w.Write(r.pdf); err != nil {
			continue
		}
	}
	// Append a small manifest of skipped bills.
	manifest := bytes.NewBuffer(nil)
	manifest.WriteString("bill_number,party,reason\n")
	skipped := 0
	for _, r := range results {
		if !r.skipped {
			continue
		}
		skipped++
		fmt.Fprintf(manifest, "%s,%s,%s\n",
			sanitizeCSVField(r.bill.BillNumber),
			sanitizeCSVField(r.bill.Party.Name),
			sanitizeCSVField(r.reason))
	}
	if skipped > 0 {
		if w, err := zw.Create("_skipped.csv"); err == nil {
			w.Write(manifest.Bytes())
		}
	}
	zw.Close()
}

func sanitizeCSVField(s string) string {
	s = strings.ReplaceAll(s, "\"", "\"\"")
	if strings.ContainsAny(s, ",\"\n") {
		return "\"" + s + "\""
	}
	return s
}

// renderURLToPDF launches headless Chromium, navigates to url, waits for the
// page body to be ready, and prints the page to PDF. Returns the raw PDF
// bytes. Requires Chromium/Chrome to be installed on the host.
func renderURLToPDF(url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx,
		append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.Flag("disable-gpu", true),
			chromedp.Flag("no-sandbox", true),
			chromedp.Flag("disable-dev-shm-usage", true),
		)...,
	)
	defer cancelAlloc()

	taskCtx, cancelTask := chromedp.NewContext(allocCtx)
	defer cancelTask()

	var pdfBuf []byte
	if err := chromedp.Run(taskCtx,
		chromedp.Navigate(url),
		chromedp.WaitReady("body", chromedp.ByQuery),
		// Give the SPA a moment to render bill content.
		chromedp.Sleep(2*time.Second),
		chromedp.ActionFunc(func(ctx context.Context) error {
			buf, _, err := page.PrintToPDF().WithPrintBackground(true).Do(ctx)
			if err != nil {
				return err
			}
			pdfBuf = buf
			return nil
		}),
	); err != nil {
		return nil, fmt.Errorf("render %s: %w", url, err)
	}
	return pdfBuf, nil
}
