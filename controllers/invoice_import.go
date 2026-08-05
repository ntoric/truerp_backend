package controllers

import (
	"encoding/csv"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type invoiceImportLine struct {
	rowNum          int
	invoiceNumber   string
	date            time.Time
	partyName       string
	dueDate         *time.Time
	status          string
	paymentMode     string
	amountPaid      float64
	isInterState    bool
	notes           string
	itemDescription string
	quantity        float64
	unit            string
	unitPrice       float64
	discount        float64
	taxRate         float64
}

func ImportInvoicesCSV(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	userName := ""
	if name, exists := c.Get("user_name"); exists {
		userName = name.(string)
	}

	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No file uploaded"})
		return
	}

	src, err := file.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to open file"})
		return
	}
	defer src.Close()

	reader := csv.NewReader(src)
	records, err := reader.ReadAll()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to read CSV"})
		return
	}

	if len(records) < 2 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "CSV file is empty or has no data"})
		return
	}

	headers := records[0]
	groups := make(map[string][]invoiceImportLine)
	groupOrder := []string{}
	var errors []string

	for i, record := range records[1:] {
		rowNum := i + 2
		if len(record) == 0 || strings.TrimSpace(strings.Join(record, "")) == "" {
			continue
		}

		line, parseErr := parseInvoiceImportRow(rowNum, record, headers)
		if parseErr != nil {
			errors = append(errors, parseErr.Error())
			continue
		}

		groupKey := line.invoiceNumber
		if groupKey == "" {
			groupKey = fmt.Sprintf("__auto_%d__", rowNum)
		}

		if _, exists := groups[groupKey]; !exists {
			groupOrder = append(groupOrder, groupKey)
		}
		groups[groupKey] = append(groups[groupKey], line)
	}

	importedCount := 0
	for _, groupKey := range groupOrder {
		lines := groups[groupKey]
		if len(lines) == 0 {
			continue
		}

		if err := createImportedInvoice(userID, userName, lines, c.ClientIP(), c.GetHeader("User-Agent")); err != nil {
			errors = append(errors, fmt.Sprintf("Invoice %s: %v", displayInvoiceImportKey(groupKey, lines[0]), err))
			continue
		}
		importedCount++
	}

	c.JSON(http.StatusOK, gin.H{
		"imported": importedCount,
		"errors":   errors,
	})
}

func parseInvoiceImportRow(rowNum int, record, headers []string) (invoiceImportLine, error) {
	line := invoiceImportLine{rowNum: rowNum}

	line.invoiceNumber = strings.TrimSpace(firstCSVValue(record, headers,
		"Invoice Number", "Invoice #", "Invoice No", "InvoiceNo"))
	line.partyName = strings.TrimSpace(firstCSVValue(record, headers, "Party Name", "Customer", "Customer Name"))
	if line.partyName == "" {
		return line, fmt.Errorf("Row %d: Party Name is required", rowNum)
	}

	dateStr := strings.TrimSpace(firstCSVValue(record, headers, "Date", "Invoice Date"))
	parsedDate, err := parseImportDate(dateStr)
	if err != nil {
		return line, fmt.Errorf("Row %d: %v", rowNum, err)
	}
	line.date = parsedDate

	if dueStr := strings.TrimSpace(firstCSVValue(record, headers, "Due Date", "DueDate")); dueStr != "" {
		if dueDate, err := parseImportDate(dueStr); err == nil {
			line.dueDate = &dueDate
		}
	}

	line.status = strings.ToLower(strings.TrimSpace(firstCSVValue(record, headers, "Status")))
	if line.status == "" {
		line.status = "draft"
	}
	if !allowedInvoiceStatuses[line.status] {
		return line, fmt.Errorf("Row %d: Invalid status %q", rowNum, line.status)
	}

	line.paymentMode = strings.TrimSpace(firstCSVValue(record, headers, "Payment Mode", "PaymentMode"))
	line.amountPaid = parseCurrencyAmount(firstCSVValue(record, headers, "Amount Paid", "AmountPaid", "Received Amount"))
	line.isInterState = parseBool(firstCSVValue(record, headers, "Is Inter State", "Inter State", "IsInterState"))
	line.notes = strings.TrimSpace(firstCSVValue(record, headers, "Notes"))

	line.itemDescription = strings.TrimSpace(firstCSVValue(record, headers, "Item Description", "Description", "Item"))
	line.quantity = parseFloat(firstCSVValue(record, headers, "Quantity", "Qty"))
	line.unit = strings.TrimSpace(firstCSVValue(record, headers, "Unit"))
	line.unitPrice = parseCurrencyAmount(firstCSVValue(record, headers, "Unit Price", "Rate", "Price"))
	line.discount = parseFloat(firstCSVValue(record, headers, "Discount %", "Discount", "Disc %"))
	line.taxRate = parseFloat(firstCSVValue(record, headers, "Tax Rate %", "Tax Rate", "Tax %", "GST %"))
	if line.taxRate == 0 {
		line.taxRate = 18
	}

	amount := parseCurrencyAmount(firstCSVValue(record, headers, "Amount", "Total Amount", "Total"))
	if line.itemDescription == "" && amount > 0 {
		line.itemDescription = "Imported item"
		line.quantity = 1
		line.unitPrice = amount
	}

	if line.itemDescription == "" {
		return line, fmt.Errorf("Row %d: Item Description or Amount is required", rowNum)
	}
	if line.quantity <= 0 {
		line.quantity = 1
	}
	if line.unit == "" {
		line.unit = "pcs"
	}
	if line.unitPrice <= 0 {
		return line, fmt.Errorf("Row %d: Unit Price or Amount is required", rowNum)
	}

	return line, nil
}

func createImportedInvoice(userID uuid.UUID, userName string, lines []invoiceImportLine, clientIP, userAgent string) error {
	header := lines[0]

	var party models.Party
	if err := utils.DB.Where("user_id = ? AND LOWER(name) = LOWER(?)", userID, header.partyName).First(&party).Error; err != nil {
		return fmt.Errorf("party %q not found", header.partyName)
	}

	invoiceNumber := header.invoiceNumber
	if invoiceNumber == "" {
		var count int64
		utils.DB.Model(&models.Invoice{}).Where("user_id = ?", userID).Count(&count)
		invoiceNumber = fmt.Sprintf("INV-%04d", count+1)
	} else {
		var existing int64
		utils.DB.Model(&models.Invoice{}).Where("user_id = ? AND invoice_number = ?", userID, invoiceNumber).Count(&existing)
		if existing > 0 {
			return fmt.Errorf("invoice number %q already exists", invoiceNumber)
		}
	}

	invoice := models.Invoice{
		ID:            uuid.New(),
		UserID:        userID,
		InvoiceNumber: invoiceNumber,
		InvoiceType:   "tax_invoice",
		PartyID:       party.ID,
		Date:          header.date,
		DueDate:       header.dueDate,
		Status:        header.status,
		PaymentMode:   header.paymentMode,
		AmountPaid:    header.amountPaid,
		Notes:         header.notes,
		IsInterState:  header.isInterState,
	}

	var subTotal, discountTotal, taxTotal, cgstTotal, sgstTotal, igstTotal float64
	for _, line := range lines {
		itemTotal := line.quantity * line.unitPrice
		itemDiscount := itemTotal * (line.discount / 100)
		taxableAmount := itemTotal - itemDiscount
		itemTax := taxableAmount * (line.taxRate / 100)

		var cgst, sgst, igst float64
		if header.isInterState {
			igst = itemTax
		} else {
			cgst = itemTax / 2
			sgst = itemTax / 2
		}

		var productID *uuid.UUID
		var product models.Product
		if err := utils.DB.Where("user_id = ? AND LOWER(name) = LOWER(?)", userID, line.itemDescription).First(&product).Error; err == nil {
			productID = &product.ID
		}

		invoice.Items = append(invoice.Items, models.InvoiceItem{
			ID:          uuid.New(),
			ProductID:   productID,
			Description: line.itemDescription,
			Quantity:    line.quantity,
			Unit:        line.unit,
			UnitPrice:   line.unitPrice,
			Discount:    line.discount,
			TaxRate:     line.taxRate,
			CGST:        cgst,
			SGST:        sgst,
			IGST:        igst,
			Total:       taxableAmount + cgst + sgst + igst,
		})

		subTotal += itemTotal
		discountTotal += itemDiscount
		taxTotal += itemTax
		cgstTotal += cgst
		sgstTotal += sgst
		igstTotal += igst
	}

	total := subTotal - discountTotal + cgstTotal + sgstTotal + igstTotal
	roundedTotal := math.Round(total*100) / 100
	roundOff := roundedTotal - total

	invoice.SubTotal = subTotal
	invoice.DiscountTotal = discountTotal
	invoice.TaxTotal = taxTotal
	invoice.CGSTTotal = cgstTotal
	invoice.SGSTTotal = sgstTotal
	invoice.IGSTTotal = igstTotal
	invoice.RoundOff = roundOff
	invoice.TotalAmount = roundedTotal

	if invoice.Status == "paid" {
		invoice.AmountPaid = invoice.TotalAmount
	} else if invoice.AmountPaid > invoice.TotalAmount {
		invoice.AmountPaid = invoice.TotalAmount
	}

	normalizeInvoicePaymentStatus(&invoice)

	if err := utils.DB.Create(&invoice).Error; err != nil {
		return fmt.Errorf("failed to create invoice: %w", err)
	}

	recordInvoiceStatusHistory(invoice.ID, userID, "", invoice.Status, "Invoice imported from CSV", userName)

	applyInvoiceSaleStock(userID, &invoice)

	if err := postInvoiceAccounting(utils.DB, userID, &invoice); err != nil {
		fmt.Printf("[DEBUG] createImportedInvoice - accounting error: %v\n", err)
	}

	if invoice.AmountPaid > 0 {
		desc := fmt.Sprintf("Sales invoice %s", invoice.InvoiceNumber)
		if err := recordSalePaymentIn(utils.DB, userID, invoice.BankAccountID, invoice.AmountPaid, invoice.Date, invoice.InvoiceNumber, desc); err != nil {
			fmt.Printf("[DEBUG] createImportedInvoice - cash ledger error: %v\n", err)
		}
	}

	utils.DB.Model(&party).Update("balance", gorm.Expr("balance + ?", invoice.TotalAmount))

	CreateAuditLog(
		userID,
		userName,
		"create",
		"invoice",
		&invoice.ID,
		invoice.InvoiceNumber,
		fmt.Sprintf("Imported invoice: %s for %s - Amount: %.2f", invoice.InvoiceNumber, party.Name, invoice.TotalAmount),
		clientIP,
		userAgent,
		map[string]interface{}{
			"party_id":     party.ID,
			"party_name":   party.Name,
			"total_amount": invoice.TotalAmount,
			"status":       invoice.Status,
			"source":       "csv_import",
		},
		"success",
		"",
	)

	return nil
}

func parseImportDate(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("date is required")
	}

	formats := []string{
		"2006-01-02",
		"02 Jan 2006",
		"2 Jan 2006",
		"01/02/2006",
		"02/01/2006",
		"2006/01/02",
		time.RFC3339,
	}

	for _, format := range formats {
		if parsed, err := time.Parse(format, value); err == nil {
			return parsed, nil
		}
	}

	return time.Time{}, fmt.Errorf("invalid date %q", value)
}

func parseCurrencyAmount(value string) float64 {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "₹", "")
	value = strings.ReplaceAll(value, "Rs.", "")
	value = strings.ReplaceAll(value, "rs.", "")
	value = strings.ReplaceAll(value, ",", "")
	return parseFloat(value)
}

func displayInvoiceImportKey(groupKey string, line invoiceImportLine) string {
	if strings.HasPrefix(groupKey, "__auto_") {
		return fmt.Sprintf("row %d", line.rowNum)
	}
	return groupKey
}
