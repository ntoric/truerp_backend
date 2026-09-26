package controllers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"truerp/models"
	"truerp/services"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// -----------------------------------------------------------------------------
// Cash & Bank statement importer — POST /api/v1/migration/cash-bank-statement/import/csv
//
// Expected header (myBillBook "Cash and Bank Statement"):
//   Date, Type, Txn No, Party, Invoice numbers, Mode, Paid, Received,
//   Balance, Notes
//
// Unlike the narrower payments importer (which only records Payment-in /
// Payment-out documents), this importer rebuilds the full cash & bank ledger
// from the statement — every row that moves money produces a CashTransaction
// plus the matching business document where one can be derived:
//
//   Sales Invoice   -> invoice (created as a paid summary invoice when the
//                      Txn No is not already on file) + payment-in + cash add
//   Payment-in      -> models.Payment rows allocated across the listed
//                      "Invoice numbers"; any remainder becomes an unlinked
//                      payment. Each payment writes a linked cash add.
//   Payment-out     -> models.PaymentOut rows allocated across the listed
//                      purchase bill numbers (capped at each bill's balance
//                      due); any remainder becomes an unlinked payment-out.
//   Purchase Bill   -> payment-out "PB-<Txn No>" linked to bill P-<Txn No>
//                      when the bill exists, otherwise an unlinked
//                      payment-out (the PB- prefix keeps it out of the
//                      Payment-out numbering space — Txn No is a per-type
//                      serial).
//   Expense         -> matches expense EXP-<Txn No>; adds the missing cash
//                      "expense" entry, or creates a General expense when no
//                      expense with that number exists.
//   Add Money       -> unlinked cash "add" transaction.
//   Reduce Money    -> unlinked cash "reduce" transaction.
//   Sales Return    -> payment-out "SR-<Txn No>" to the party + cash reduce.
//   Purchase Return -> payment-in "PR-<Txn No>" from the party + cash add.
//   Opening Balance -> skipped (no amount columns in the export).
//
// Mode ("Cash", "Upi", "Card", "Bank", "Netbanking") is routed through the
// payment-method -> bank-account mapping; cash stays in hand. The optional
// "default_bank_account" option names an account that receives any non-cash
// mode that has no mapping and no primary account.
//
// The import is idempotent: invoices are deduplicated by number, payments by
// payment number, expenses by expense number, and manual cash rows by their
// "CB-ADD-<txn>" / "CB-RED-<txn>" reference, so re-running with the same file
// is safe.
// -----------------------------------------------------------------------------

func ImportCashBankStatementCSV(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	content, err := importFile(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	options := map[string]string{
		"default_bank_account": strings.TrimSpace(c.PostForm("default_bank_account")),
	}
	result, errs, perr := importCashBankStatementRows(userID, content, options, nil)
	if perr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": perr.Error()})
		return
	}
	result["errors"] = errs
	c.JSON(http.StatusOK, result)
}

type cashBankStmtCounts struct {
	imported         int
	salesInvoices    int
	paymentIn        int
	paymentOut       int
	expenses         int
	cashTransactions int
	skipped          int
}

func importCashBankStatementRows(userID uuid.UUID, content []byte, options map[string]string, progress services.ProgressFunc) (map[string]interface{}, []string, error) {
	header, rows, err := mbReadCSV(content, "Date")
	if err != nil {
		// Fall back to a plain header-first parse for files without the
		// myBillBook preamble.
		header, rows, err = mbReadCSVPlain(content)
		if err != nil {
			return nil, nil, err
		}
	}

	// Optional account that receives non-cash modes when no payment-method
	// mapping or primary account exists.
	var fallbackAccount *uuid.UUID
	if name := strings.TrimSpace(options["default_bank_account"]); name != "" {
		var account models.BankAccount
		if err := utils.DB.Where("user_id = ? AND account_name = ?", userID, name).First(&account).Error; err != nil {
			return nil, nil, fmt.Errorf("bank account %q not found", name)
		}
		fallbackAccount = &account.ID
	}

	// Cache party lookups so repeated counter parties (Cash Sale, UPI SALE, …)
	// do not hit the database for every one of thousands of rows.
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

	counts := cashBankStmtCounts{}
	var errs []string

	for i, row := range rows {
		if progress != nil {
			progress(i+1, len(rows), counts.imported)
		}
		rowNum := i + 1
		if len(row) == 0 {
			continue
		}

		typ := strings.TrimSpace(mbFirstCSVValue(row, header, "Type", "Transaction Type"))
		txnNo := strings.TrimSpace(mbFirstCSVValue(row, header, "Txn No", "Txn No.", "Sr No."))
		partyName := strings.TrimSpace(mbFirstCSVValue(row, header, "Party", "Party Name"))
		notes := strings.TrimSpace(mbFirstCSVValue(row, header, "Notes"))
		mode := mbMapPaymentMode(mbFirstCSVValue(row, header, "Mode", "Payment Mode"))
		paid := mbParseAmount(mbFirstCSVValue(row, header, "Paid"))
		received := mbParseAmount(mbFirstCSVValue(row, header, "Received"))
		invList := splitInvoiceNumbers(mbFirstCSVValue(row, header, "Invoice numbers", "Invoice Numbers", "Invoice No"))

		dateStr := strings.TrimSpace(mbFirstCSVValue(row, header, "Date"))
		date, derr := mbParseDate(dateStr)
		if derr != nil {
			// Rows with no date at all (e.g. the Opening Balance summary row)
			// carry no transaction — skip rather than error.
			if dateStr == "" && paid <= 0 && received <= 0 {
				counts.skipped++
				continue
			}
			errs = append(errs, fmt.Sprintf("Row %d: invalid date %q", rowNum, dateStr))
			continue
		}
		if date.IsZero() {
			date = time.Now()
		}

		accountID := resolveStatementAccount(userID, mode, fallbackAccount)

		var n int
		var perrs []string
		switch typ {
		case "Sales Invoice":
			n, perrs = stmtImportSaleRow(userID, txnNo, partyName, notes, mode, accountID, received, date, findParty)
			counts.salesInvoices += n
		case "Payment-in":
			if paymentInNumberInUse(utils.DB, userID, txnNo) {
				counts.skipped++
				continue
			}
			n, perrs = stmtImportPaymentInRow(userID, txnNo, partyName, notes, mode, accountID, received, date, invList, findParty)
			counts.paymentIn += n
		case "Payment-out":
			if stmtPaymentOutNumberInUse(userID, txnNo) {
				counts.skipped++
				continue
			}
			n, perrs = stmtImportPaymentOutRow(userID, txnNo, partyName, notes, mode, accountID, paid, date, invList, findParty)
			counts.paymentOut += n
		case "Purchase Bill":
			if paid <= 0 {
				counts.skipped++
				continue
			}
			// Statement txn numbers are a per-type serial, so the payment-out
			// for a purchase bill gets its own "PB-" numbering space — a raw
			// Txn No would collide with Payment-out rows using the same number.
			if stmtPaymentOutNumberInUse(userID, "PB-"+txnNo) {
				counts.skipped++
				continue
			}
			n, perrs = stmtImportPurchaseBillRow(userID, "PB-"+txnNo, txnNo, partyName, notes, mode, accountID, paid, date, findParty)
			counts.paymentOut += n
		case "Expense":
			n, perrs = stmtImportExpenseRow(userID, txnNo, partyName, notes, mode, accountID, paid, date)
			counts.expenses += n
		case "Add Money":
			n, perrs = stmtImportManualCashRow(userID, txnNo, notes, accountID, received, date, "add")
			counts.cashTransactions += n
		case "Reduce Money":
			n, perrs = stmtImportManualCashRow(userID, txnNo, notes, accountID, paid, date, "reduce")
			counts.cashTransactions += n
		case "Sales Return":
			number := "SR-" + txnNo
			if stmtPaymentOutNumberInUse(userID, number) {
				counts.skipped++
				continue
			}
			n, perrs = stmtImportSalesReturnRow(userID, number, txnNo, partyName, notes, mode, accountID, paid, date, findParty)
			counts.paymentOut += n
		case "Purchase Return":
			number := "PR-" + txnNo
			if paymentInNumberInUse(utils.DB, userID, number) {
				counts.skipped++
				continue
			}
			n, perrs = stmtImportPurchaseReturnRow(userID, number, txnNo, partyName, notes, mode, accountID, received, date, findParty)
			counts.paymentIn += n
		default:
			// Opening Balance rows and anything else unrecognised carry no
			// importable money movement.
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
		"payment_in":        counts.paymentIn,
		"payment_out":       counts.paymentOut,
		"expenses":          counts.expenses,
		"cash_transactions": counts.cashTransactions,
		"skipped":           counts.skipped,
	}, errs, nil
}

// resolveStatementAccount picks the bank account for a statement row: the
// payment-method mapping (then primary account) first, then the import
// option's fallback account for non-cash modes. Cash stays in hand (nil).
func resolveStatementAccount(userID uuid.UUID, mode string, fallback *uuid.UUID) *uuid.UUID {
	if normalizePaymentMethod(mode) == "cash" {
		return nil
	}
	if id, err := resolveBankAccountForPaymentMode(userID, mode, nil); err == nil && id != nil {
		return id
	}
	return fallback
}

// stmtPaymentOutNumberInUse reports whether a payment-out with the given
// number already exists for the user (dedupe key for payment-out rows).
func stmtPaymentOutNumberInUse(userID uuid.UUID, number string) bool {
	number = strings.TrimSpace(number)
	if number == "" {
		return false
	}
	var n int64
	utils.DB.Model(&models.PaymentOut{}).Where("user_id = ? AND payment_out_number = ?", userID, number).Count(&n)
	return n > 0
}

// stmtCashTxnExists reports whether a cash transaction with the given type
// and reference already exists (dedupe key for expense and manual cash rows).
func stmtCashTxnExists(userID uuid.UUID, txnType, reference string, linked bool) bool {
	var n int64
	utils.DB.Model(&models.CashTransaction{}).
		Where("user_id = ? AND transaction_type = ? AND reference = ? AND is_linked = ?",
			userID, txnType, reference, linked).
		Count(&n)
	return n > 0
}

// -----------------------------------------------------------------------------
// Row handlers
// -----------------------------------------------------------------------------

// stmtImportSaleRow handles a "Sales Invoice" statement row: the money
// received at sale time. When the invoice already exists it is topped up with
// a linked payment for the still-unpaid portion; otherwise a paid summary
// invoice is created (matching the sales importer's shape).
func stmtImportSaleRow(userID uuid.UUID, txnNo, partyName, notes, mode string, accountID *uuid.UUID, received float64, date time.Time, findParty func(string, string) (uuid.UUID, error)) (int, []string) {
	if received <= 0 {
		return 0, nil
	}
	if txnNo == "" {
		return 0, []string{"Sales Invoice row: Txn No is required"}
	}
	if partyName == "" {
		partyName = "Cash Sale"
	}

	invoice, found := findInvoiceByNumber(userID, txnNo)
	if found {
		remaining := invoice.TotalAmount - invoice.AmountPaid
		if remaining <= 0.005 {
			return 0, nil // already fully paid — nothing to record
		}
		alloc := received
		if alloc > remaining {
			alloc = remaining
		}

		tx := utils.DB.Begin()
		markInvoicePaid(tx, userID, invoice.ID, alloc)
		payNotes := stmtNotes(notes, txnNo)
		if err := createLinkedSalePaymentInWithMode(tx, userID, &invoice, alloc, mode, accountID, date, payNotes); err != nil {
			tx.Rollback()
			return 0, []string{fmt.Sprintf("invoice %s: %v", txnNo, err)}
		}
		// Any excess over the invoice balance becomes an unlinked payment.
		if leftover := received - alloc; leftover > 0.005 {
			if err := stmtCreateUnlinkedPaymentIn(tx, userID, invoice.PartyID, allocateUniquePaymentInNumber(tx, userID, txnNo), leftover, mode, accountID, date, payNotes); err != nil {
				tx.Rollback()
				return 0, []string{fmt.Sprintf("invoice %s: %v", txnNo, err)}
			}
			updatePartyBalance(tx, invoice.PartyID, -leftover)
		}
		if err := tx.Commit().Error; err != nil {
			return 0, []string{fmt.Sprintf("invoice %s: %v", txnNo, err)}
		}
		return 1, nil
	}

	partyID, perr := findParty(partyName, "customer")
	if perr != nil {
		return 0, []string{fmt.Sprintf("invoice %s (%s): %v", txnNo, partyName, perr)}
	}

	invoice = models.Invoice{
		ID:            uuid.New(),
		UserID:        userID,
		InvoiceNumber: txnNo,
		InvoiceType:   "tax_invoice",
		PartyID:       partyID,
		Date:          date,
		Status:        "paid",
		PaymentMode:   mode,
		BankAccountID: accountID,
		AmountPaid:    received,
		SubTotal:      received,
		TotalAmount:   received,
		Notes:         stmtNotes(notes, txnNo),
	}
	invoice.Items = []models.InvoiceItem{
		{
			ID:          uuid.New(),
			Description: "Migrated from myBillBook cash & bank statement (summary)",
			Quantity:    1,
			Unit:        "PCS",
			UnitPrice:   received,
			Total:       received,
		},
	}

	tx := utils.DB.Begin()
	if err := tx.Create(&invoice).Error; err != nil {
		tx.Rollback()
		return 0, []string{fmt.Sprintf("invoice %s: %v", txnNo, err)}
	}
	// Same convention as invoice creation: a sale raises the party's
	// outstanding balance; the linked payment below brings it back down.
	updatePartyBalance(tx, partyID, invoice.TotalAmount)
	if err := postInvoiceAccounting(tx, userID, &invoice); err != nil {
		tx.Rollback()
		return 0, []string{fmt.Sprintf("invoice %s: %v", txnNo, err)}
	}
	if err := createLinkedSalePaymentInWithMode(tx, userID, &invoice, received, mode, accountID, date, stmtNotes(notes, txnNo)); err != nil {
		tx.Rollback()
		return 0, []string{fmt.Sprintf("invoice %s: %v", txnNo, err)}
	}
	if err := tx.Commit().Error; err != nil {
		return 0, []string{fmt.Sprintf("invoice %s: %v", txnNo, err)}
	}
	return 1, nil
}

// stmtImportPaymentInRow handles a "Payment-in" row: the received amount is
// allocated across the invoices listed in "Invoice numbers" (each capped at
// its unpaid balance); the remainder becomes an unlinked payment.
func stmtImportPaymentInRow(userID uuid.UUID, txnNo, partyName, notes, mode string, accountID *uuid.UUID, received float64, date time.Time, invList []string, findParty func(string, string) (uuid.UUID, error)) (int, []string) {
	if received <= 0 {
		return 0, nil
	}
	partyID, perr := findParty(partyName, "customer")
	if perr != nil {
		return 0, []string{fmt.Sprintf("payment-in %s (%s): %v", txnNo, partyName, perr)}
	}

	tx := utils.DB.Begin()
	created := 0
	remaining := received
	for _, invNo := range invList {
		if remaining <= 0.005 {
			break
		}
		inv, ok := stmtFindInvoiceByNumber(tx, userID, invNo)
		if !ok {
			continue
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
			PaymentInNumber: txnNo,
			Mode:            mode,
			Date:            date,
			Reference:       inv.InvoiceNumber,
			Notes:           notes,
		}
		if err := tx.Create(&payment).Error; err != nil {
			tx.Rollback()
			return 0, []string{fmt.Sprintf("payment-in %s (inv %s): %v", txnNo, invNo, err)}
		}
		desc := fmt.Sprintf("Payment in %s for invoice %s", txnNo, inv.InvoiceNumber)
		if err := recordSalePaymentIn(tx, userID, accountID, alloc, date, inv.InvoiceNumber, desc); err != nil {
			tx.Rollback()
			return 0, []string{fmt.Sprintf("payment-in %s (inv %s): %v", txnNo, invNo, err)}
		}
		markInvoicePaid(tx, userID, inv.ID, alloc)
		remaining -= alloc
		created++
	}
	// Money not attributable to a listed invoice becomes an unlinked payment.
	if remaining > 0.005 {
		if err := stmtCreateUnlinkedPaymentIn(tx, userID, partyID, allocateUniquePaymentInNumber(tx, userID, txnNo), remaining, mode, accountID, date, notes); err != nil {
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

// stmtImportPaymentOutRow handles a "Payment-out" row: the paid amount is
// allocated across the purchase bills listed in "Invoice numbers" (each
// capped at its balance due); the remainder becomes an unlinked payment-out.
func stmtImportPaymentOutRow(userID uuid.UUID, txnNo, partyName, notes, mode string, accountID *uuid.UUID, paid float64, date time.Time, invList []string, findParty func(string, string) (uuid.UUID, error)) (int, []string) {
	if paid <= 0 {
		return 0, nil
	}
	partyID, perr := findParty(partyName, "vendor")
	if perr != nil {
		return 0, []string{fmt.Sprintf("payment-out %s (%s): %v", txnNo, partyName, perr)}
	}

	tx := utils.DB.Begin()
	created := 0
	remaining := paid
	for _, ref := range invList {
		if remaining <= 0.005 {
			break
		}
		bill, ok := stmtFindPurchaseBillByNumber(tx, userID, ref)
		if !ok {
			continue
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
			Mode:             mode,
			Date:             date,
			Reference:        bill.BillNumber,
			Notes:            notes,
		}
		if err := tx.Create(&paymentOut).Error; err != nil {
			tx.Rollback()
			return 0, []string{fmt.Sprintf("payment-out %s (bill %s): %v", txnNo, ref, err)}
		}
		desc := fmt.Sprintf("Payment out %s for purchase %s", txnNo, bill.BillNumber)
		if err := recordPurchasePaymentOut(tx, userID, accountID, alloc, date, bill.BillNumber, desc); err != nil {
			tx.Rollback()
			return 0, []string{fmt.Sprintf("payment-out %s (bill %s): %v", txnNo, ref, err)}
		}
		markPurchaseBillPaid(tx, userID, bill.ID, alloc)
		remaining -= alloc
		created++
	}
	if remaining > 0.005 {
		if err := stmtCreateUnlinkedPaymentOut(tx, userID, partyID, txnNo, remaining, mode, accountID, date, notes); err != nil {
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

// stmtImportPurchaseBillRow handles a "Purchase Bill" statement row — money
// paid at purchase time. It becomes a payment-out linked to bill P-<Txn No>
// when that bill exists.
func stmtImportPurchaseBillRow(userID uuid.UUID, number, txnNo, partyName, notes, mode string, accountID *uuid.UUID, paid float64, date time.Time, findParty func(string, string) (uuid.UUID, error)) (int, []string) {
	partyID, perr := findParty(partyName, "vendor")
	if perr != nil {
		return 0, []string{fmt.Sprintf("purchase bill %s (%s): %v", txnNo, partyName, perr)}
	}

	tx := utils.DB.Begin()
	var billID *uuid.UUID
	refNumber := txnNo
	if bill, ok := stmtFindPurchaseBillByNumber(tx, userID, txnNo); ok {
		billID = &bill.ID
		refNumber = bill.BillNumber
	}
	paymentOut := models.PaymentOut{
		ID:               uuid.New(),
		UserID:           userID,
		PurchaseBillID:   billID,
		PartyID:          partyID,
		AmountPaid:       paid,
		PaymentOutNumber: number,
		Mode:             mode,
		Date:             date,
		Reference:        refNumber,
		Notes:            notes,
	}
	if err := tx.Create(&paymentOut).Error; err != nil {
		tx.Rollback()
		return 0, []string{fmt.Sprintf("purchase bill %s: %v", txnNo, err)}
	}
	if billID != nil {
		desc := fmt.Sprintf("Payment out %s for purchase %s", txnNo, refNumber)
		if err := recordPurchasePaymentOut(tx, userID, accountID, paid, date, refNumber, desc); err != nil {
			tx.Rollback()
			return 0, []string{fmt.Sprintf("purchase bill %s: %v", txnNo, err)}
		}
		markPurchaseBillPaid(tx, userID, *billID, paid)
	} else {
		if err := stmtRecordCashOut(tx, userID, accountID, paid, date, txnNo, fmt.Sprintf("Purchase bill %s (statement)", txnNo)); err != nil {
			tx.Rollback()
			return 0, []string{fmt.Sprintf("purchase bill %s: %v", txnNo, err)}
		}
		if err := postStandalonePaymentOutAccounting(tx, userID, &paymentOut, paid); err != nil {
			tx.Rollback()
			return 0, []string{fmt.Sprintf("purchase bill %s: %v", txnNo, err)}
		}
	}
	updatePartyBalance(tx, partyID, paid)
	if err := tx.Commit().Error; err != nil {
		return 0, []string{fmt.Sprintf("purchase bill %s: %v", txnNo, err)}
	}
	return 1, nil
}

// stmtImportExpenseRow handles an "Expense" statement row. Expenses carry no
// name in the statement, so the row is matched to expense EXP-<Txn No> (the
// number the expense importer assigns from the serial). When found, only the
// missing cash "expense" entry is added; otherwise a minimal General expense
// is created so the money movement is not lost.
func stmtImportExpenseRow(userID uuid.UUID, txnNo, partyName, notes, mode string, accountID *uuid.UUID, paid float64, date time.Time) (int, []string) {
	if paid <= 0 {
		return 0, nil
	}
	expenseNumber := "EXP-" + txnNo
	if strings.HasPrefix(strings.ToUpper(txnNo), "EXP-") {
		expenseNumber = txnNo
	}

	var expense models.Expense
	findErr := utils.DB.Where("user_id = ? AND expense_number = ?", userID, expenseNumber).First(&expense).Error
	if findErr == nil {
		if stmtCashTxnExists(userID, "expense", expense.ExpenseNumber, true) {
			return 0, nil // cash entry already recorded
		}
		tx := utils.DB.Begin()
		desc := expense.Description
		if desc == "" {
			desc = "Expense " + expense.ExpenseNumber
		}
		if err := recordExpenseCashOut(tx, userID, accountID, paid, date, expense.ExpenseNumber, desc); err != nil {
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
	if !errors.Is(findErr, gorm.ErrRecordNotFound) {
		return 0, []string{fmt.Sprintf("expense %s: %v", expenseNumber, findErr)}
	}

	description := notes
	if description == "" {
		description = fmt.Sprintf("Migrated expense %s", txnNo)
	}
	expense = models.Expense{
		ID:            uuid.New(),
		UserID:        userID,
		ExpenseNumber: expenseNumber,
		Category:      utils.DefaultCategoryName,
		Description:   description,
		Amount:        paid,
		SubTotal:      paid,
		Date:          date,
		Vendor:        partyName,
		PaymentMode:   mode,
		BankAccountID: accountID,
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
		Description: description,
		Quantity:    1,
		UnitPrice:   paid,
		Total:       paid,
	}
	if err := tx.Create(&item).Error; err != nil {
		tx.Rollback()
		return 0, []string{fmt.Sprintf("expense %s: item %v", expenseNumber, err)}
	}
	if err := recordExpenseCashOut(tx, userID, accountID, paid, date, expenseNumber, description); err != nil {
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

// stmtImportManualCashRow handles "Add Money" / "Reduce Money" rows —
// unlinked cash movements with no underlying business document.
func stmtImportManualCashRow(userID uuid.UUID, txnNo, notes string, accountID *uuid.UUID, amount float64, date time.Time, txnType string) (int, []string) {
	if amount <= 0 {
		return 0, nil
	}
	dir := "ADD"
	desc := "Add money"
	if txnType == "reduce" {
		dir = "RED"
		desc = "Reduce money"
	}
	reference := fmt.Sprintf("CB-%s-%s", dir, txnNo)
	if stmtCashTxnExists(userID, txnType, reference, false) {
		return 0, nil
	}
	if notes != "" {
		desc = notes
	}

	tx := utils.DB.Begin()
	delta := amount
	if txnType == "reduce" {
		delta = -amount
	}
	if err := adjustBankAccountBalance(tx, userID, accountID, delta, false); err != nil {
		tx.Rollback()
		return 0, []string{fmt.Sprintf("%s %s: %v", desc, txnNo, err)}
	}
	txn := models.CashTransaction{
		ID:              uuid.New(),
		UserID:          userID,
		AccountID:       accountID,
		TransactionType: txnType,
		Amount:          amount,
		Date:            date,
		Description:     desc,
		Reference:       reference,
		IsLinked:        false,
	}
	if err := tx.Create(&txn).Error; err != nil {
		tx.Rollback()
		return 0, []string{fmt.Sprintf("%s %s: %v", desc, txnNo, err)}
	}
	var aerr error
	if txnType == "add" {
		aerr = postManualCashAddAccounting(tx, userID, &txn)
	} else {
		aerr = postManualCashReduceAccounting(tx, userID, &txn)
	}
	if aerr != nil {
		tx.Rollback()
		return 0, []string{fmt.Sprintf("%s %s: %v", desc, txnNo, aerr)}
	}
	if err := tx.Commit().Error; err != nil {
		return 0, []string{fmt.Sprintf("%s %s: %v", desc, txnNo, err)}
	}
	return 1, nil
}

// stmtImportSalesReturnRow handles a "Sales Return" row — a refund paid back
// to the customer. Recorded as a payment-out "SR-<Txn No>" plus a cash
// reduce; a full SalesReturn document is not created because the statement
// does not carry the originating invoice.
func stmtImportSalesReturnRow(userID uuid.UUID, number, txnNo, partyName, notes, mode string, accountID *uuid.UUID, paid float64, date time.Time, findParty func(string, string) (uuid.UUID, error)) (int, []string) {
	if paid <= 0 {
		return 0, nil
	}
	partyID, perr := findParty(partyName, "customer")
	if perr != nil {
		return 0, []string{fmt.Sprintf("sales return %s (%s): %v", txnNo, partyName, perr)}
	}
	if notes != "" {
		notes = "Sales return refund — " + notes
	} else {
		notes = fmt.Sprintf("Sales return refund (statement txn %s)", txnNo)
	}

	tx := utils.DB.Begin()
	paymentOut := models.PaymentOut{
		ID:               uuid.New(),
		UserID:           userID,
		PartyID:          partyID,
		AmountPaid:       paid,
		PaymentOutNumber: number,
		Mode:             mode,
		Date:             date,
		Reference:        number,
		Notes:            notes,
	}
	if err := tx.Create(&paymentOut).Error; err != nil {
		tx.Rollback()
		return 0, []string{fmt.Sprintf("sales return %s: %v", txnNo, err)}
	}
	if err := recordPurchasePaymentOut(tx, userID, accountID, paid, date, number, notes); err != nil {
		tx.Rollback()
		return 0, []string{fmt.Sprintf("sales return %s: %v", txnNo, err)}
	}
	updatePartyBalance(tx, partyID, paid)
	if err := tx.Commit().Error; err != nil {
		return 0, []string{fmt.Sprintf("sales return %s: %v", txnNo, err)}
	}
	return 1, nil
}

// stmtImportPurchaseReturnRow handles a "Purchase Return" row — a refund
// received back from the vendor. Recorded as a payment-in "PR-<Txn No>" plus
// a cash add.
func stmtImportPurchaseReturnRow(userID uuid.UUID, number, txnNo, partyName, notes, mode string, accountID *uuid.UUID, received float64, date time.Time, findParty func(string, string) (uuid.UUID, error)) (int, []string) {
	if received <= 0 {
		return 0, nil
	}
	partyID, perr := findParty(partyName, "vendor")
	if perr != nil {
		return 0, []string{fmt.Sprintf("purchase return %s (%s): %v", txnNo, partyName, perr)}
	}
	if notes != "" {
		notes = "Purchase return refund — " + notes
	} else {
		notes = fmt.Sprintf("Purchase return refund (statement txn %s)", txnNo)
	}

	tx := utils.DB.Begin()
	payment := models.Payment{
		ID:              uuid.New(),
		UserID:          userID,
		PartyID:         partyID,
		AmountReceived:  received,
		PaymentInNumber: number,
		Mode:            mode,
		Date:            date,
		Reference:       number,
		Notes:           notes,
	}
	if err := tx.Create(&payment).Error; err != nil {
		tx.Rollback()
		return 0, []string{fmt.Sprintf("purchase return %s: %v", txnNo, err)}
	}
	if err := recordSalePaymentIn(tx, userID, accountID, received, date, number, notes); err != nil {
		tx.Rollback()
		return 0, []string{fmt.Sprintf("purchase return %s: %v", txnNo, err)}
	}
	updatePartyBalance(tx, partyID, -received)
	if err := tx.Commit().Error; err != nil {
		return 0, []string{fmt.Sprintf("purchase return %s: %v", txnNo, err)}
	}
	return 1, nil
}

// -----------------------------------------------------------------------------
// Shared helpers
// -----------------------------------------------------------------------------

// stmtCreateUnlinkedPaymentIn creates a payment-in with no invoice link plus
// its cash add transaction and accounting entry.
func stmtCreateUnlinkedPaymentIn(tx *gorm.DB, userID, partyID uuid.UUID, number string, amount float64, mode string, accountID *uuid.UUID, date time.Time, notes string) error {
	payment := models.Payment{
		ID:              uuid.New(),
		UserID:          userID,
		PartyID:         partyID,
		AmountReceived:  amount,
		PaymentInNumber: number,
		Mode:            mode,
		Date:            date,
		Reference:       number,
		Notes:           notes,
	}
	if err := tx.Create(&payment).Error; err != nil {
		return err
	}
	if err := stmtRecordCashIn(tx, userID, accountID, amount, date, number, fmt.Sprintf("Payment in %s", number)); err != nil {
		return err
	}
	return postStandalonePaymentInAccounting(tx, userID, &payment, amount)
}

// stmtCreateUnlinkedPaymentOut creates a payment-out with no bill link plus
// its cash reduce transaction and accounting entry.
func stmtCreateUnlinkedPaymentOut(tx *gorm.DB, userID, partyID uuid.UUID, number string, amount float64, mode string, accountID *uuid.UUID, date time.Time, notes string) error {
	paymentOut := models.PaymentOut{
		ID:               uuid.New(),
		UserID:           userID,
		PartyID:          partyID,
		AmountPaid:       amount,
		PaymentOutNumber: number,
		Mode:             mode,
		Date:             date,
		Reference:        number,
		Notes:            notes,
	}
	if err := tx.Create(&paymentOut).Error; err != nil {
		return err
	}
	if err := stmtRecordCashOut(tx, userID, accountID, amount, date, number, fmt.Sprintf("Payment out %s", number)); err != nil {
		return err
	}
	return postStandalonePaymentOutAccounting(tx, userID, &paymentOut, amount)
}

// stmtRecordCashIn writes a linked cash "add" transaction and updates the
// account balance. Used for payments that are not tied to a sales invoice.
func stmtRecordCashIn(tx *gorm.DB, userID uuid.UUID, accountID *uuid.UUID, amount float64, date time.Time, reference, description string) error {
	if amount <= 0 {
		return nil
	}
	if err := adjustBankAccountBalance(tx, userID, accountID, amount, false); err != nil {
		return err
	}
	return tx.Create(&models.CashTransaction{
		ID:              uuid.New(),
		UserID:          userID,
		AccountID:       accountID,
		TransactionType: "add",
		Amount:          amount,
		Date:            date,
		Description:     description,
		Reference:       reference,
		IsLinked:        true,
	}).Error
}

// stmtRecordCashOut writes a linked cash "reduce" transaction and updates the
// account balance.
func stmtRecordCashOut(tx *gorm.DB, userID uuid.UUID, accountID *uuid.UUID, amount float64, date time.Time, reference, description string) error {
	if amount <= 0 {
		return nil
	}
	if err := adjustBankAccountBalance(tx, userID, accountID, -amount, false); err != nil {
		return err
	}
	return tx.Create(&models.CashTransaction{
		ID:              uuid.New(),
		UserID:          userID,
		AccountID:       accountID,
		TransactionType: "reduce",
		Amount:          amount,
		Date:            date,
		Description:     description,
		Reference:       reference,
		IsLinked:        true,
	}).Error
}

// stmtFindInvoiceByNumber / stmtFindPurchaseBillByNumber are the tx-scoped
// versions of findInvoiceByNumber / findPurchaseBillByNumber — reads must run
// on the open transaction's connection, not a different pooled utils.DB
// connection (which would see stale state and can deadlock on SQLite).
func stmtFindInvoiceByNumber(db *gorm.DB, userID uuid.UUID, number string) (models.Invoice, bool) {
	var inv models.Invoice
	if err := db.Where("user_id = ? AND invoice_number = ?", userID, number).First(&inv).Error; err == nil {
		return inv, true
	}
	return models.Invoice{}, false
}

func stmtFindPurchaseBillByNumber(db *gorm.DB, userID uuid.UUID, number string) (models.PurchaseBill, bool) {
	var bill models.PurchaseBill
	candidates := []string{number, fmt.Sprintf("P-%04s", number)}
	for _, c := range candidates {
		if err := db.Where("user_id = ? AND bill_number = ?", userID, c).First(&bill).Error; err == nil {
			return bill, true
		}
	}
	return models.PurchaseBill{}, false
}

// stmtNotes appends the statement txn reference to the row notes so imported
// records keep their source transaction number.
func stmtNotes(notes, txnNo string) string {
	tag := fmt.Sprintf("Statement txn %s", txnNo)
	if notes == "" {
		return tag
	}
	return notes + " | " + tag
}
