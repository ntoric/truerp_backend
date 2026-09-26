package controllers

import (
	"fmt"
	"net/http"
	"strings"
	"time"
	"truerp/models"
	"truerp/services"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// -----------------------------------------------------------------------------
// Daybook importer — POST /api/v1/migration/daybook/import/csv
//
// Expected header (myBillBook "Daybook Report"):
//   Date, Name, Transaction Type, Sr No., Total Amount, Money In, Money Out,
//   Balance Amount, Created By
//
// The daybook is a combined export — a single file carries every transaction
// type, so this importer rebuilds all of them in one pass:
//
//   Sales Invoice   -> invoice numbered by "Sr No." with Total Amount as the
//                      invoice total and Money In as the amount paid at sale
//                      time; the paid portion writes a linked payment-in and
//                      cash add. Balance Amount is the unpaid remainder.
//   Purchase Bill   -> purchase bill P-<Sr No.> with Money Out as the paid
//                      amount; the paid portion writes a linked payment-out
//                      and cash reduce. Balance Amount is the unpaid due.
//   Payment-in      -> models.Payment rows. The daybook does not list the
//                      invoices a receipt settles, so the amount is allocated
//                      across the party's open invoices oldest-first; any
//                      remainder becomes an unlinked payment-in.
//   Payment-out     -> models.PaymentOut rows allocated oldest-first across
//                      the party's open purchase bills; remainder unlinked.
//   Expense         -> expense EXP-<Sr No.> under a category named after the
//                      row's Name, plus the linked cash "expense" entry.
//   Add Money       -> unlinked cash "add" transaction in cash in hand.
//   Reduce Money    -> unlinked cash "reduce" transaction.
//   Sales Return    -> payment-out "SR-<Sr No.>" refund to the customer.
//   Purchase Return -> payment-in "PR-<Sr No.>" refund from the vendor (only
//                      when Money In moved; pure credit rows are skipped).
//
// The export carries no payment mode or account column, so every money
// movement posts to cash in hand.
//
// The import is idempotent: invoices dedupe by invoice number, bills by bill
// number, payments by payment number, expenses by expense number and manual
// cash rows by their CB-ADD/CB-RED reference, so re-running with the same
// file is safe.
// -----------------------------------------------------------------------------

func ImportDaybookCSV(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	content, err := importFile(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	result, errs, perr := importDaybookRows(userID, content, nil, nil)
	if perr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": perr.Error()})
		return
	}
	result["errors"] = errs
	c.JSON(http.StatusOK, result)
}

type daybookCounts struct {
	imported         int
	salesInvoices    int
	purchaseBills    int
	paymentIn        int
	paymentOut       int
	expenses         int
	cashTransactions int
	skipped          int
}

func importDaybookRows(userID uuid.UUID, content []byte, options map[string]string, progress services.ProgressFunc) (map[string]interface{}, []string, error) {
	header, rows, err := mbReadCSV(content, "Date")
	if err != nil {
		// Fall back to a plain header-first parse for files without the
		// myBillBook preamble.
		header, rows, err = mbReadCSVPlain(content)
		if err != nil {
			return nil, nil, err
		}
	}

	// Cache party lookups so repeated counter parties (Cash Sale, frequent
	// customers, …) do not hit the database for every one of thousands of rows.
	partyCache := map[string]uuid.UUID{}
	findParty := func(name, partyType string) (uuid.UUID, error) {
		key := strings.ToLower(strings.TrimSpace(name))
		if id, ok := partyCache[key]; ok {
			return id, nil
		}
		id, err := mbFindOrCreatePartyByName(utils.DB, userID, name, partyType)
		if err != nil {
			return uuid.Nil, err
		}
		partyCache[key] = id
		return id, nil
	}

	defaultWH := resolveDefaultWarehouseID(userID)

	counts := daybookCounts{}
	var errs []string

	for i, row := range rows {
		if progress != nil {
			progress(i+1, len(rows), counts.imported)
		}
		rowNum := i + 1
		if len(row) == 0 {
			continue
		}

		typ := strings.TrimSpace(mbFirstCSVValue(row, header, "Transaction Type", "Type"))
		txnNo := strings.TrimSpace(mbFirstCSVValue(row, header, "Sr No.", "Sr No", "Txn No", "Txn No."))
		name := strings.TrimSpace(mbFirstCSVValue(row, header, "Name", "Party", "Party Name"))
		total := mbParseAmount(mbFirstCSVValue(row, header, "Total Amount", "Amount"))
		moneyIn := mbParseAmount(mbFirstCSVValue(row, header, "Money In", "Received"))
		moneyOut := mbParseAmount(mbFirstCSVValue(row, header, "Money Out", "Paid"))
		createdBy := strings.TrimSpace(mbFirstCSVValue(row, header, "Created By", "Created by"))

		notes := ""
		if createdBy != "" {
			notes = "Created by: " + createdBy
		}

		dateStr := strings.TrimSpace(mbFirstCSVValue(row, header, "Date"))
		date, derr := mbParseDate(dateStr)
		if derr != nil {
			// Rows with no date at all carry no transaction — skip rather
			// than error.
			if dateStr == "" && total <= 0 && moneyIn <= 0 && moneyOut <= 0 {
				counts.skipped++
				continue
			}
			errs = append(errs, fmt.Sprintf("Row %d: invalid date %q", rowNum, dateStr))
			continue
		}
		if date.IsZero() {
			date = time.Now()
		}

		var n int
		var perrs []string
		switch typ {
		case "Sales Invoice":
			if _, found := findInvoiceByNumber(userID, txnNo); found {
				counts.skipped++
				continue
			}
			n, perrs = daybookImportSaleRow(userID, txnNo, name, notes, total, moneyIn, date, findParty)
			counts.salesInvoices += n
		case "Purchase Bill":
			n, perrs = daybookImportPurchaseBillRow(userID, txnNo, name, notes, total, moneyOut, date, defaultWH, findParty)
			counts.purchaseBills += n
		case "Payment-in":
			if paymentInNumberInUse(utils.DB, userID, txnNo) {
				counts.skipped++
				continue
			}
			n, perrs = daybookImportPaymentInRow(userID, txnNo, name, notes, moneyIn, date, findParty)
			counts.paymentIn += n
		case "Payment-out":
			if stmtPaymentOutNumberInUse(userID, txnNo) {
				counts.skipped++
				continue
			}
			n, perrs = daybookImportPaymentOutRow(userID, txnNo, name, notes, moneyOut, date, findParty)
			counts.paymentOut += n
		case "Expense":
			n, perrs = daybookImportExpenseRow(userID, txnNo, name, notes, moneyOut, date)
			counts.expenses += n
		case "Add Money":
			n, perrs = stmtImportManualCashRow(userID, txnNo, notes, nil, moneyIn, date, "add")
			counts.cashTransactions += n
		case "Reduce Money":
			n, perrs = stmtImportManualCashRow(userID, txnNo, notes, nil, moneyOut, date, "reduce")
			counts.cashTransactions += n
		case "Sales Return":
			number := "SR-" + txnNo
			if stmtPaymentOutNumberInUse(userID, number) {
				counts.skipped++
				continue
			}
			n, perrs = stmtImportSalesReturnRow(userID, number, txnNo, name, notes, "cash", nil, moneyOut, date, findParty)
			counts.paymentOut += n
		case "Purchase Return":
			number := "PR-" + txnNo
			if paymentInNumberInUse(utils.DB, userID, number) {
				counts.skipped++
				continue
			}
			n, perrs = stmtImportPurchaseReturnRow(userID, number, txnNo, name, notes, "cash", nil, moneyIn, date, findParty)
			counts.paymentIn += n
		default:
			// Anything unrecognised carries no importable transaction.
			counts.skipped++
			continue
		}

		errs = append(errs, perrs...)
		if n > 0 {
			counts.imported++
		} else if len(perrs) == 0 {
			counts.skipped++
		}
	}

	return map[string]interface{}{
		"imported":          counts.imported,
		"sales_invoices":    counts.salesInvoices,
		"purchase_bills":    counts.purchaseBills,
		"payment_in":        counts.paymentIn,
		"payment_out":       counts.paymentOut,
		"expenses":          counts.expenses,
		"cash_transactions": counts.cashTransactions,
		"skipped":           counts.skipped,
	}, errs, nil
}

// -----------------------------------------------------------------------------
// Row handlers
// -----------------------------------------------------------------------------

// daybookImportSaleRow handles a "Sales Invoice" daybook row: one invoice per
// row, numbered by Sr No., with Total Amount as the invoice total and Money In
// as the amount paid at sale time. The paid portion records a linked
// payment-in and cash add.
func daybookImportSaleRow(userID uuid.UUID, txnNo, partyName, notes string, total, moneyIn float64, date time.Time, findParty func(string, string) (uuid.UUID, error)) (int, []string) {
	if txnNo == "" {
		return 0, []string{"Sales Invoice row: Sr No. is required"}
	}
	if total <= 0 && moneyIn <= 0 {
		return 0, nil
	}
	if partyName == "" {
		partyName = "Cash Sale"
	}
	partyID, perr := findParty(partyName, "customer")
	if perr != nil {
		return 0, []string{fmt.Sprintf("invoice %s (%s): %v", txnNo, partyName, perr)}
	}

	invoice := models.Invoice{
		ID:            uuid.New(),
		UserID:        userID,
		InvoiceNumber: txnNo,
		InvoiceType:   "tax_invoice",
		PartyID:       partyID,
		Date:          date,
		Status:        "sent",
		PaymentMode:   "cash",
		AmountPaid:    moneyIn,
		SubTotal:      total,
		TotalAmount:   total,
		Notes:         notes,
	}
	invoice.Items = []models.InvoiceItem{
		{
			ID:          uuid.New(),
			Description: "Migrated from myBillBook daybook (summary)",
			Quantity:    1,
			Unit:        "PCS",
			UnitPrice:   total,
			Total:       total,
		},
	}
	normalizeInvoicePaymentStatus(&invoice)

	tx := utils.DB.Begin()
	if err := tx.Create(&invoice).Error; err != nil {
		tx.Rollback()
		return 0, []string{fmt.Sprintf("invoice %s: %v", txnNo, err)}
	}
	if err := postInvoiceAccounting(tx, userID, &invoice); err != nil {
		tx.Rollback()
		return 0, []string{fmt.Sprintf("invoice %s: %v", txnNo, err)}
	}
	if err := createLinkedSalePaymentInWithMode(tx, userID, &invoice, moneyIn, "cash", nil, date, notes); err != nil {
		tx.Rollback()
		return 0, []string{fmt.Sprintf("invoice %s: %v", txnNo, err)}
	}
	if err := tx.Commit().Error; err != nil {
		return 0, []string{fmt.Sprintf("invoice %s: %v", txnNo, err)}
	}
	return 1, nil
}

// daybookImportPurchaseBillRow handles a "Purchase Bill" daybook row: one
// purchase bill per row, numbered P-<Sr No.>, with Total Amount as the bill
// total and Money Out as the amount paid at purchase time. The paid portion
// records a linked payment-out and cash reduce.
func daybookImportPurchaseBillRow(userID uuid.UUID, txnNo, partyName, notes string, total, moneyOut float64, date time.Time, defaultWH uuid.UUID, findParty func(string, string) (uuid.UUID, error)) (int, []string) {
	if txnNo == "" {
		return 0, []string{"Purchase Bill row: Sr No. is required"}
	}
	if total <= 0 && moneyOut <= 0 {
		return 0, nil
	}
	if partyName == "" {
		partyName = "General Vendor"
	}
	partyID, perr := findParty(partyName, "vendor")
	if perr != nil {
		return 0, []string{fmt.Sprintf("purchase bill %s (%s): %v", txnNo, partyName, perr)}
	}

	billNumber := fmt.Sprintf("P-%04s", txnNo)
	if strings.HasPrefix(strings.ToUpper(txnNo), "P-") {
		billNumber = txnNo
	}
	if _, found := findPurchaseBillByNumber(userID, billNumber); found {
		return 0, nil // already imported — skip
	}

	paid := moneyOut
	due := total - paid
	if due < 0 {
		due = 0
	}
	status := "unpaid"
	if total > 0 && due <= 0.005 {
		status = "paid"
	} else if paid > 0 {
		status = "partial"
	}

	bill := models.PurchaseBill{
		ID:          uuid.New(),
		UserID:      userID,
		PartyID:     partyID,
		BillNumber:  billNumber,
		BillDate:    date,
		Status:      status,
		SubTotal:    total,
		TotalAmount: total,
		PaidAmount:  paid,
		BalanceDue:  due,
		PaymentMode: "cash",
		Notes:       notes,
	}
	if defaultWH != uuid.Nil {
		bill.WarehouseID = &defaultWH
	}

	tx := utils.DB.Begin()
	if err := tx.Create(&bill).Error; err != nil {
		tx.Rollback()
		return 0, []string{fmt.Sprintf("purchase bill %s: %v", billNumber, err)}
	}
	item := models.PurchaseBillItem{
		ID:          uuid.New(),
		BillID:      bill.ID,
		Description: "Migrated from myBillBook daybook (summary)",
		Quantity:    1,
		Unit:        "PCS",
		UnitPrice:   total,
		Total:       total,
	}
	if err := tx.Create(&item).Error; err != nil {
		tx.Rollback()
		return 0, []string{fmt.Sprintf("purchase bill %s: item %v", billNumber, err)}
	}
	if err := postPurchaseBillAccounting(tx, userID, &bill); err != nil {
		tx.Rollback()
		return 0, []string{fmt.Sprintf("purchase bill %s: %v", billNumber, err)}
	}
	if err := createLinkedPurchasePaymentOut(tx, userID, &bill, paid, date, notes); err != nil {
		tx.Rollback()
		return 0, []string{fmt.Sprintf("purchase bill %s: %v", billNumber, err)}
	}
	if err := tx.Commit().Error; err != nil {
		return 0, []string{fmt.Sprintf("purchase bill %s: %v", billNumber, err)}
	}
	return 1, nil
}

// daybookImportPaymentInRow handles a "Payment-in" daybook row. The export
// does not list which invoices the receipt settles, so the received amount is
// allocated across the party's open invoices oldest-first; any remainder
// becomes an unlinked payment-in.
func daybookImportPaymentInRow(userID uuid.UUID, txnNo, partyName, notes string, received float64, date time.Time, findParty func(string, string) (uuid.UUID, error)) (int, []string) {
	if received <= 0 {
		return 0, nil
	}
	if partyName == "" {
		return 0, []string{fmt.Sprintf("payment-in %s: Name is required", txnNo)}
	}
	partyID, perr := findParty(partyName, "customer")
	if perr != nil {
		return 0, []string{fmt.Sprintf("payment-in %s (%s): %v", txnNo, partyName, perr)}
	}

	number := txnNo
	if number == "" {
		number = allocateUniquePaymentInNumber(utils.DB, userID, "")
	}

	tx := utils.DB.Begin()
	created := 0
	remaining := received

	var invoices []models.Invoice
	if err := tx.Where(
		"user_id = ? AND party_id = ? AND total_amount > amount_paid AND status NOT IN ?",
		userID, partyID, []string{"cancelled", "draft"},
	).Order("date ASC, created_at ASC").Find(&invoices).Error; err != nil {
		tx.Rollback()
		return 0, []string{fmt.Sprintf("payment-in %s: %v", txnNo, err)}
	}

	for _, inv := range invoices {
		if remaining <= 0.005 {
			break
		}
		balance := inv.TotalAmount - inv.AmountPaid
		if balance <= 0.005 {
			continue
		}
		alloc := remaining
		if alloc > balance {
			alloc = balance
		}
		payment := models.Payment{
			ID:              uuid.New(),
			UserID:          userID,
			InvoiceID:       &inv.ID,
			PartyID:         partyID,
			AmountReceived:  alloc,
			PaymentInNumber: number,
			Mode:            "cash",
			Date:            date,
			Reference:       inv.InvoiceNumber,
			Notes:           notes,
		}
		if err := tx.Create(&payment).Error; err != nil {
			tx.Rollback()
			return 0, []string{fmt.Sprintf("payment-in %s (inv %s): %v", txnNo, inv.InvoiceNumber, err)}
		}
		desc := fmt.Sprintf("Payment in %s for invoice %s", number, inv.InvoiceNumber)
		if err := recordSalePaymentIn(tx, userID, nil, alloc, date, inv.InvoiceNumber, desc); err != nil {
			tx.Rollback()
			return 0, []string{fmt.Sprintf("payment-in %s (inv %s): %v", txnNo, inv.InvoiceNumber, err)}
		}
		markInvoicePaid(tx, userID, inv.ID, alloc)
		remaining -= alloc
		created++
	}
	// Money not attributable to an open invoice becomes an unlinked payment.
	if remaining > 0.005 {
		if err := stmtCreateUnlinkedPaymentIn(tx, userID, partyID, allocateUniquePaymentInNumber(tx, userID, number), remaining, "cash", nil, date, notes); err != nil {
			tx.Rollback()
			return 0, []string{fmt.Sprintf("payment-in %s: %v", txnNo, err)}
		}
		created++
	}
	if created > 0 {
		updatePartyBalance(tx, partyID, -received)
	}
	if err := tx.Commit().Error; err != nil {
		return 0, []string{fmt.Sprintf("payment-in %s: %v", txnNo, err)}
	}
	return created, nil
}

// daybookImportPaymentOutRow handles a "Payment-out" daybook row: the paid
// amount is allocated across the party's open purchase bills oldest-first;
// any remainder becomes an unlinked payment-out.
func daybookImportPaymentOutRow(userID uuid.UUID, txnNo, partyName, notes string, paid float64, date time.Time, findParty func(string, string) (uuid.UUID, error)) (int, []string) {
	if paid <= 0 {
		return 0, nil
	}
	if partyName == "" {
		return 0, []string{fmt.Sprintf("payment-out %s: Name is required", txnNo)}
	}
	partyID, perr := findParty(partyName, "vendor")
	if perr != nil {
		return 0, []string{fmt.Sprintf("payment-out %s (%s): %v", txnNo, partyName, perr)}
	}

	tx := utils.DB.Begin()
	created := 0
	remaining := paid

	var bills []models.PurchaseBill
	if err := tx.Where(
		"user_id = ? AND party_id = ? AND balance_due > 0 AND status IN ?",
		userID, partyID, []string{"unpaid", "partial"},
	).Order("bill_date ASC, created_at ASC").Find(&bills).Error; err != nil {
		tx.Rollback()
		return 0, []string{fmt.Sprintf("payment-out %s: %v", txnNo, err)}
	}

	for _, bill := range bills {
		if remaining <= 0.005 {
			break
		}
		if bill.BalanceDue <= 0.005 {
			continue
		}
		alloc := remaining
		if alloc > bill.BalanceDue {
			alloc = bill.BalanceDue
		}
		paymentOut := models.PaymentOut{
			ID:               uuid.New(),
			UserID:           userID,
			PurchaseBillID:   &bill.ID,
			PartyID:          partyID,
			AmountPaid:       alloc,
			PaymentOutNumber: txnNo,
			Mode:             "cash",
			Date:             date,
			Reference:        bill.BillNumber,
			Notes:            notes,
		}
		if err := tx.Create(&paymentOut).Error; err != nil {
			tx.Rollback()
			return 0, []string{fmt.Sprintf("payment-out %s (bill %s): %v", txnNo, bill.BillNumber, err)}
		}
		desc := fmt.Sprintf("Payment out %s for purchase %s", txnNo, bill.BillNumber)
		if err := recordPurchasePaymentOut(tx, userID, nil, alloc, date, bill.BillNumber, desc); err != nil {
			tx.Rollback()
			return 0, []string{fmt.Sprintf("payment-out %s (bill %s): %v", txnNo, bill.BillNumber, err)}
		}
		markPurchaseBillPaid(tx, userID, bill.ID, alloc)
		remaining -= alloc
		created++
	}
	if remaining > 0.005 {
		if err := stmtCreateUnlinkedPaymentOut(tx, userID, partyID, txnNo, remaining, "cash", nil, date, notes); err != nil {
			tx.Rollback()
			return 0, []string{fmt.Sprintf("payment-out %s: %v", txnNo, err)}
		}
		created++
	}
	if created > 0 {
		updatePartyBalance(tx, partyID, paid)
	}
	if err := tx.Commit().Error; err != nil {
		return 0, []string{fmt.Sprintf("payment-out %s: %v", txnNo, err)}
	}
	return created, nil
}

// daybookImportExpenseRow handles an "Expense" daybook row. The row's Name is
// the expense name — it becomes both the category (created on demand) and the
// description. The expense number is EXP-<Sr No.>, matching the expenses
// importer's convention.
func daybookImportExpenseRow(userID uuid.UUID, txnNo, name, notes string, amount float64, date time.Time) (int, []string) {
	if amount <= 0 {
		return 0, nil
	}
	if name == "" {
		name = "General"
	}
	// Skip contra entries that are not real expenses.
	if strings.EqualFold(name, "Payment In Discount") {
		return 0, nil
	}

	expenseNumber := "EXP-" + txnNo
	if strings.HasPrefix(strings.ToUpper(txnNo), "EXP-") {
		expenseNumber = txnNo
	}
	if txnNo == "" {
		var count int64
		utils.DB.Model(&models.Expense{}).Where("user_id = ?", userID).Count(&count)
		expenseNumber = fmt.Sprintf("EXP-%04d", count+1)
	}

	var existing models.Expense
	if err := utils.DB.Where("user_id = ? AND expense_number = ?", userID, expenseNumber).First(&existing).Error; err == nil {
		return 0, nil // already imported — skip
	}

	category, cerr := mbEnsureExpenseCategory(utils.DB, userID, name)
	if cerr != nil {
		return 0, []string{fmt.Sprintf("expense %s: %v", expenseNumber, cerr)}
	}

	expense := models.Expense{
		ID:            uuid.New(),
		UserID:        userID,
		ExpenseNumber: expenseNumber,
		Category:      category,
		Description:   name,
		Amount:        amount,
		SubTotal:      amount,
		Date:          date,
		PaymentMode:   "cash",
		Notes:         notes,
	}
	tx := utils.DB.Begin()
	if err := tx.Create(&expense).Error; err != nil {
		tx.Rollback()
		return 0, []string{fmt.Sprintf("expense %s: %v", expenseNumber, err)}
	}
	item := models.ExpenseItem{
		ID:          uuid.New(),
		ExpenseID:   expense.ID,
		Description: name,
		Quantity:    1,
		UnitPrice:   amount,
		Total:       amount,
	}
	if err := tx.Create(&item).Error; err != nil {
		tx.Rollback()
		return 0, []string{fmt.Sprintf("expense %s: item %v", expenseNumber, err)}
	}
	if err := recordExpenseCashOut(tx, userID, nil, amount, date, expenseNumber, name); err != nil {
		tx.Rollback()
		return 0, []string{fmt.Sprintf("expense %s: %v", expenseNumber, err)}
	}
	if err := postExpenseAccounting(tx, userID, &expense); err != nil {
		tx.Rollback()
		return 0, []string{fmt.Sprintf("expense %s: %v", expenseNumber, err)}
	}
	if err := tx.Commit().Error; err != nil {
		return 0, []string{fmt.Sprintf("expense %s: %v", expenseNumber, err)}
	}
	return 1, nil
}
