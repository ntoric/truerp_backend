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
	"gorm.io/gorm"
)

// -----------------------------------------------------------------------------
// Cash & Bank statement importer — POST /api/v1/migration/cash-bank-statement/import/csv
//
// Expected header (myBillBook "Cash and Bank Statement"):
//   Date, Type, Txn No, Party, Invoice numbers, Mode, Paid, Received,
//   Balance, Notes
//
// This importer is a payment-mode fix-up pass: it runs after the transactions
// themselves have been imported (Daybook / per-entity importers) and only
// updates how each transaction's money was settled. Rows are matched by
// (Type, Txn No) against the existing documents — nothing new is created:
//
//   Sales Invoice   -> invoice <Txn No>: payment_mode + bank_account_id are
//                      set, its sale-time payment-in records (auto-numbered
//                      PIN-*) are re-moded, and the linked "Sales invoice <n>"
//                      cash transaction is moved to the resolved account.
//   Purchase Bill   -> bill <Txn No> / P-<Txn No>: payment fields plus the
//                      bill-time payment-out (POUT-* or PB-<Txn No>) and its
//                      cash reduce transaction.
//   Expense         -> expense <Txn No> / EXP-<Txn No>: payment fields, the
//                      linked expense cash transaction, and the GL entry.
//   Payment-in      -> payment rows numbered <Txn No>: mode plus their cash
//                      add transactions and standalone GL entries.
//   Payment-out     -> payment-out rows numbered <Txn No>: symmetric.
//   Sales Return    -> payment-out SR-<Txn No> (the refund recorded by the
//                      transaction import) + its cash reduce transaction.
//   Purchase Return -> payment-in PR-<Txn No> + its cash add transaction.
//   Add/Reduce Money-> unlinked cash transactions CB-ADD-<Txn No> /
//                      CB-RED-<Txn No> are moved to the resolved account.
//
// Mode ("Cash", "Upi", "Card", "Bank", "Netbanking") is resolved through the
// payment-method -> bank-account mapping in Cash & Bank settings: cash stays
// in hand (no account) and every other mode must have an account configured.
// If any non-cash mode used by a money-moving row has no mapped account, the
// whole import is aborted with a warning — nothing is written.
//
// The import is idempotent: matched rows whose mode and account already agree
// are reported as skipped, so re-running the same file is a no-op.
// -----------------------------------------------------------------------------

func ImportCashBankStatementCSV(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	content, err := importFile(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	result, errs, perr := importCashBankStatementRows(userID, content, nil, nil)
	if perr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": perr.Error()})
		return
	}
	result["errors"] = errs
	c.JSON(http.StatusOK, result)
}

// stmtUpdateTypes are the statement row types that carry an updatable money
// movement. Anything else (Opening Balance, …) is skipped.
var stmtUpdateTypes = map[string]bool{
	"Sales Invoice":   true,
	"Payment-in":      true,
	"Payment-out":     true,
	"Purchase Bill":   true,
	"Expense":         true,
	"Add Money":       true,
	"Reduce Money":    true,
	"Sales Return":    true,
	"Purchase Return": true,
}

// stmtMatchData indexes the user's existing documents so each statement row
// can be matched by (type, txn number) without a query per row. Entries are
// pointers into the loaded slices so updates made during the import stay
// visible to later rows.
type stmtMatchData struct {
	invExact     map[string]*models.Invoice
	invNorm      map[string]*models.Invoice
	billExact    map[string]*models.PurchaseBill
	billNorm     map[string]*models.PurchaseBill
	expExact     map[string]*models.Expense
	expNorm      map[string]*models.Expense
	payIn        map[string][]*models.Payment
	payInByInv   map[uuid.UUID][]*models.Payment
	payOut       map[string][]*models.PaymentOut
	payOutByBill map[uuid.UUID][]*models.PaymentOut
	txns         []models.CashTransaction
	txnByDesc    map[string][]int
	txnByRef     map[string][]int
}

func loadStatementMatchData(userID uuid.UUID) (*stmtMatchData, error) {
	d := &stmtMatchData{
		invExact:     map[string]*models.Invoice{},
		invNorm:      map[string]*models.Invoice{},
		billExact:    map[string]*models.PurchaseBill{},
		billNorm:     map[string]*models.PurchaseBill{},
		expExact:     map[string]*models.Expense{},
		expNorm:      map[string]*models.Expense{},
		payIn:        map[string][]*models.Payment{},
		payInByInv:   map[uuid.UUID][]*models.Payment{},
		payOut:       map[string][]*models.PaymentOut{},
		payOutByBill: map[uuid.UUID][]*models.PaymentOut{},
		txnByDesc:    map[string][]int{},
		txnByRef:     map[string][]int{},
	}

	var invoices []models.Invoice
	if err := utils.DB.Where("user_id = ?", userID).Find(&invoices).Error; err != nil {
		return nil, err
	}
	for i := range invoices {
		inv := &invoices[i]
		d.invExact[inv.InvoiceNumber] = inv
		if key := docNumberKey(inv.InvoiceNumber); key != "" {
			if _, ok := d.invNorm[key]; !ok {
				d.invNorm[key] = inv
			}
		}
	}

	var bills []models.PurchaseBill
	if err := utils.DB.Where("user_id = ?", userID).Find(&bills).Error; err != nil {
		return nil, err
	}
	for i := range bills {
		b := &bills[i]
		d.billExact[b.BillNumber] = b
		if key := docNumberKey(b.BillNumber); key != "" {
			if _, ok := d.billNorm[key]; !ok {
				d.billNorm[key] = b
			}
		}
	}

	var expenses []models.Expense
	if err := utils.DB.Where("user_id = ?", userID).Find(&expenses).Error; err != nil {
		return nil, err
	}
	for i := range expenses {
		e := &expenses[i]
		d.expExact[e.ExpenseNumber] = e
		if key := docNumberKey(e.ExpenseNumber); key != "" {
			if _, ok := d.expNorm[key]; !ok {
				d.expNorm[key] = e
			}
		}
	}

	var payments []models.Payment
	if err := utils.DB.Where("user_id = ?", userID).Find(&payments).Error; err != nil {
		return nil, err
	}
	for i := range payments {
		p := &payments[i]
		d.payIn[p.PaymentInNumber] = append(d.payIn[p.PaymentInNumber], p)
		if p.InvoiceID != nil {
			d.payInByInv[*p.InvoiceID] = append(d.payInByInv[*p.InvoiceID], p)
		}
	}

	var payOuts []models.PaymentOut
	if err := utils.DB.Where("user_id = ?", userID).Find(&payOuts).Error; err != nil {
		return nil, err
	}
	for i := range payOuts {
		po := &payOuts[i]
		d.payOut[po.PaymentOutNumber] = append(d.payOut[po.PaymentOutNumber], po)
		if po.PurchaseBillID != nil {
			d.payOutByBill[*po.PurchaseBillID] = append(d.payOutByBill[*po.PurchaseBillID], po)
		}
	}

	if err := utils.DB.Where("user_id = ?", userID).Find(&d.txns).Error; err != nil {
		return nil, err
	}
	for i := range d.txns {
		txn := &d.txns[i]
		if txn.Description != "" {
			d.txnByDesc[txn.Description] = append(d.txnByDesc[txn.Description], i)
		}
		if txn.Reference != "" {
			d.txnByRef[txn.Reference] = append(d.txnByRef[txn.Reference], i)
		}
	}

	return d, nil
}

func (d *stmtMatchData) findInvoice(number string) (*models.Invoice, bool) {
	if inv, ok := d.invExact[number]; ok {
		return inv, true
	}
	if key := docNumberKey(number); key != "" {
		if inv, ok := d.invNorm[key]; ok {
			return inv, true
		}
	}
	return nil, false
}

func (d *stmtMatchData) findBill(number string) (*models.PurchaseBill, bool) {
	if b, ok := d.billExact[number]; ok {
		return b, true
	}
	if key := docNumberKey(number); key != "" {
		if b, ok := d.billNorm[key]; ok {
			return b, true
		}
	}
	return nil, false
}

func (d *stmtMatchData) findExpense(number string) (*models.Expense, bool) {
	candidates := []string{number, "EXP-" + number}
	for _, c := range candidates {
		if e, ok := d.expExact[c]; ok {
			return e, true
		}
	}
	if key := docNumberKey(number); key != "" {
		if e, ok := d.expNorm[key]; ok {
			return e, true
		}
	}
	return nil, false
}

// mappedStatementAccount resolves the bank account configured for a payment
// method in Cash & Bank → Payment method accounts. Unlike the permissive
// runtime resolver, there is no primary-account fallback: the import requires
// an explicit mapping so money cannot silently land in the wrong account.
func mappedStatementAccount(userID uuid.UUID, mode string) (*uuid.UUID, bool) {
	var mapping models.PaymentMethodAccountMap
	if err := utils.DB.Where("user_id = ? AND payment_method = ?", userID, mode).First(&mapping).Error; err != nil {
		return nil, false
	}
	if mapping.BankAccountID == nil {
		return nil, false
	}
	if err := validateUserBankAccount(userID, mapping.BankAccountID); err != nil {
		return nil, false
	}
	return mapping.BankAccountID, true
}

type cashBankStmtCounts struct {
	updated          int
	salesInvoices    int
	purchaseBills    int
	paymentIn        int
	paymentOut       int
	expenses         int
	cashTransactions int
	skipped          int
}

func importCashBankStatementRows(userID uuid.UUID, content []byte, _ map[string]string, progress services.ProgressFunc) (map[string]interface{}, []string, error) {
	header, rows, err := mbReadCSV(content, "Date")
	if err != nil {
		// Fall back to a plain header-first parse for files without the
		// myBillBook preamble.
		header, rows, err = mbReadCSVPlain(content)
		if err != nil {
			return nil, nil, err
		}
	}

	rowMode := func(row []string) string {
		return mbMapPaymentMode(mbFirstCSVValue(row, header, "Mode", "Payment Mode"))
	}

	// Pass 1 — resolve every non-cash mode used by a money-moving row through
	// the payment-method account mapping. Cash stays in hand; any other mode
	// without a configured account aborts the whole import so no money lands
	// in the wrong place.
	accounts := map[string]*uuid.UUID{}
	missing := map[string]bool{}
	for _, row := range rows {
		if len(row) == 0 {
			continue
		}
		typ := strings.TrimSpace(mbFirstCSVValue(row, header, "Type", "Transaction Type"))
		if !stmtUpdateTypes[typ] {
			continue
		}
		paid := mbParseAmount(mbFirstCSVValue(row, header, "Paid"))
		received := mbParseAmount(mbFirstCSVValue(row, header, "Received"))
		if paid <= 0 && received <= 0 {
			continue
		}
		mode := rowMode(row)
		if mode == "cash" {
			continue
		}
		if _, done := accounts[mode]; done || missing[mode] {
			continue
		}
		if id, ok := mappedStatementAccount(userID, mode); ok {
			accounts[mode] = id
		} else {
			missing[mode] = true
		}
	}
	if len(missing) > 0 {
		labels := make([]string, 0, len(missing))
		for mode := range missing {
			labels = append(labels, paymentMethodLabel(mode))
		}
		return nil, nil, fmt.Errorf(
			"no bank account is configured for payment mode(s): %s — map them under Cash & Bank settings (Payment method accounts), then re-run the import",
			strings.Join(labels, ", "),
		)
	}

	match, err := loadStatementMatchData(userID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load existing transactions: %w", err)
	}
	moved := map[uuid.UUID]bool{}

	counts := cashBankStmtCounts{}
	var errs []string

	for i, row := range rows {
		if progress != nil {
			progress(i+1, len(rows), counts.updated)
		}
		rowNum := i + 1
		if len(row) == 0 {
			continue
		}

		typ := strings.TrimSpace(mbFirstCSVValue(row, header, "Type", "Transaction Type"))
		txnNo := strings.TrimSpace(mbFirstCSVValue(row, header, "Txn No", "Txn No.", "Sr No."))
		mode := rowMode(row)
		paid := mbParseAmount(mbFirstCSVValue(row, header, "Paid"))
		received := mbParseAmount(mbFirstCSVValue(row, header, "Received"))

		accountID := accounts[mode] // nil for cash -> cash in hand

		var changed bool
		var perrs []string
		switch typ {
		case "Sales Invoice":
			changed, perrs = stmtUpdateSaleMode(match, userID, rowNum, txnNo, mode, accountID, received, moved)
			if changed {
				counts.salesInvoices++
			}
		case "Payment-in":
			changed, perrs = stmtUpdatePaymentInMode(match, userID, rowNum, txnNo, mode, accountID, received, moved)
			if changed {
				counts.paymentIn++
			}
		case "Payment-out":
			changed, perrs = stmtUpdatePaymentOutMode(match, userID, rowNum, txnNo, mode, accountID, paid, moved)
			if changed {
				counts.paymentOut++
			}
		case "Purchase Bill":
			changed, perrs = stmtUpdatePurchaseBillMode(match, userID, rowNum, txnNo, mode, accountID, paid, moved)
			if changed {
				counts.purchaseBills++
			}
		case "Expense":
			changed, perrs = stmtUpdateExpenseMode(match, userID, rowNum, txnNo, mode, accountID, paid, moved)
			if changed {
				counts.expenses++
			}
		case "Add Money":
			changed, perrs = stmtUpdateManualCashMode(match, userID, rowNum, txnNo, accountID, received, "add", moved)
			if changed {
				counts.cashTransactions++
			}
		case "Reduce Money":
			changed, perrs = stmtUpdateManualCashMode(match, userID, rowNum, txnNo, accountID, paid, "reduce", moved)
			if changed {
				counts.cashTransactions++
			}
		case "Sales Return":
			number := txnNo
			if !strings.HasPrefix(strings.ToUpper(number), "SR-") {
				number = "SR-" + number
			}
			changed, perrs = stmtUpdateReturnMode(match, userID, rowNum, number, "Sales Return", mode, accountID, paid, "reduce", moved)
			if changed {
				counts.paymentOut++
			}
		case "Purchase Return":
			number := txnNo
			if !strings.HasPrefix(strings.ToUpper(number), "PR-") {
				number = "PR-" + number
			}
			changed, perrs = stmtUpdateReturnMode(match, userID, rowNum, number, "Purchase Return", mode, accountID, received, "add", moved)
			if changed {
				counts.paymentIn++
			}
		default:
			// Opening Balance rows and anything else unrecognised carry no
			// updatable money movement.
			counts.skipped++
			continue
		}

		errs = append(errs, perrs...)
		if changed {
			counts.updated++
		} else if len(perrs) == 0 {
			counts.skipped++
		}
	}

	return map[string]interface{}{
		"imported":          counts.updated,
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
// Shared update helpers
// -----------------------------------------------------------------------------

// stmtReassignCashTxn moves a cash/bank ledger transaction to a different
// account (nil = cash in hand): bank account balances are re-adjusted and the
// transaction's GL journal (refType "payment_in", "payment_out", "cash_add"
// or "cash_reduce") is reversed and reposted so the cash/bank ledger leg
// follows the new mode. refType "" means the transaction has no
// per-transaction journal (expense postings are keyed to the expense row and
// resynced separately).
func stmtReassignCashTxn(tx *gorm.DB, d *stmtMatchData, txnIdx int, accountID *uuid.UUID, refType string) (bool, error) {
	txn := &d.txns[txnIdx]
	if bankAccountIDsEqual(txn.AccountID, accountID) {
		return false, nil
	}

	signed := txn.Amount
	if txn.TransactionType != "add" && txn.TransactionType != "transfer_in" {
		signed = -txn.Amount
	}
	if err := adjustBankAccountBalance(tx, txn.UserID, txn.AccountID, -signed, false); err != nil {
		return false, err
	}
	if err := tx.Model(&models.CashTransaction{}).Where("id = ?", txn.ID).
		Update("account_id", bankAccountIDValue(accountID)).Error; err != nil {
		return false, err
	}
	if err := adjustBankAccountBalance(tx, txn.UserID, accountID, signed, false); err != nil {
		return false, err
	}
	txn.AccountID = accountID

	if refType == "" {
		return true, nil
	}
	// Only transactions that already carry a journal get re-posted — e.g.
	// unlinked payment cash rows have no "payment_in"/"payment_out" entry
	// (their GL lives on the payment's *_record reference instead), and
	// creating one now would double-post the AR/AP leg.
	if !accountingRefExists(tx, txn.UserID, refType, txn.ID) {
		return true, nil
	}
	if err := reverseAccountingByRef(tx, txn.UserID, refType, txn.ID); err != nil {
		return false, err
	}
	var rerr error
	switch refType {
	case "payment_in":
		rerr = postSalePaymentAccounting(tx, txn.UserID, txn.ID, accountID, txn.Amount, txn.Date, txn.Reference, txn.Description)
	case "payment_out":
		rerr = postPurchasePaymentAccounting(tx, txn.UserID, txn.ID, accountID, txn.Amount, txn.Date, txn.Reference, txn.Description)
	case "cash_add":
		rerr = postManualCashAddAccounting(tx, txn.UserID, txn)
	case "cash_reduce":
		rerr = postManualCashReduceAccounting(tx, txn.UserID, txn)
	}
	if rerr != nil {
		return false, rerr
	}
	return true, nil
}

// stmtMovePaymentTxns moves every linked cash transaction written for a
// payment row. Allocated payments are described "Payment <dir> <num> for
// <invoice|purchase> <ref>"; unlinked ones are just "Payment <dir> <num>".
func stmtMovePaymentTxns(tx *gorm.DB, d *stmtMatchData, moved map[uuid.UUID]bool, number, direction, reference string, accountID *uuid.UUID, refType string, txnType string) (bool, error) {
	descs := []string{fmt.Sprintf("Payment %s %s", direction, number)}
	if reference != "" {
		for _, suffix := range []string{"invoice", "purchase"} {
			descs = append(descs, fmt.Sprintf("Payment %s %s for %s %s", direction, number, suffix, reference))
		}
	}

	changed := false
	for _, desc := range descs {
		for _, idx := range d.txnByDesc[desc] {
			txn := &d.txns[idx]
			if txn.TransactionType != txnType || !txn.IsLinked || moved[txn.ID] {
				continue
			}
			moved[txn.ID] = true
			m, err := stmtReassignCashTxn(tx, d, idx, accountID, refType)
			if err != nil {
				return changed, err
			}
			changed = changed || m
		}
	}
	return changed, nil
}

// stmtMoveRefTxns moves linked cash transactions located by their reference
// (used for the SR-/PR- refund payments, whose descriptions are free-text
// notes rather than the "Payment <dir> <num>" convention).
func stmtMoveRefTxns(tx *gorm.DB, d *stmtMatchData, moved map[uuid.UUID]bool, reference, txnType string, accountID *uuid.UUID, refType string) (bool, error) {
	changed := false
	for _, idx := range d.txnByRef[reference] {
		txn := &d.txns[idx]
		if txn.TransactionType != txnType || !txn.IsLinked || moved[txn.ID] {
			continue
		}
		moved[txn.ID] = true
		m, err := stmtReassignCashTxn(tx, d, idx, accountID, refType)
		if err != nil {
			return changed, err
		}
		changed = changed || m
	}
	return changed, nil
}

// -----------------------------------------------------------------------------
// Row handlers — each matches one statement row to the already-imported
// transaction and updates its payment mode + destination account.
// -----------------------------------------------------------------------------

// stmtUpdateSaleMode handles a "Sales Invoice" row: the invoice's payment
// fields, its sale-time payment-in records (auto-numbered PIN-*) and the
// linked "Sales invoice <n>" cash transaction all move to the resolved mode
// and account.
func stmtUpdateSaleMode(d *stmtMatchData, userID uuid.UUID, rowNum int, txnNo, mode string, accountID *uuid.UUID, received float64, moved map[uuid.UUID]bool) (bool, []string) {
	if received <= 0 {
		return false, nil
	}
	if txnNo == "" {
		return false, []string{fmt.Sprintf("Row %d: Sales Invoice — Txn No is required", rowNum)}
	}
	inv, found := d.findInvoice(txnNo)
	if !found {
		return false, []string{fmt.Sprintf("Row %d: no matching sales invoice for Txn No %s — skipped", rowNum, txnNo)}
	}

	changed := false
	err := utils.DB.Transaction(func(tx *gorm.DB) error {
		if normalizePaymentMethod(inv.PaymentMode) != mode || !bankAccountIDsEqual(inv.BankAccountID, accountID) {
			if err := tx.Model(&models.Invoice{}).Where("id = ?", inv.ID).Updates(map[string]interface{}{
				"payment_mode":    mode,
				"bank_account_id": bankAccountIDValue(accountID),
			}).Error; err != nil {
				return err
			}
			inv.PaymentMode = mode
			inv.BankAccountID = accountID
			changed = true
		}

		for _, p := range d.payInByInv[inv.ID] {
			// Only the payments created at sale time carry auto-allocated
			// PIN- numbers; receipts matched by "Payment-in" rows keep their
			// own statement number.
			if !strings.HasPrefix(p.PaymentInNumber, paymentInNumberPrefix+"-") {
				continue
			}
			if normalizePaymentMethod(p.Mode) == mode {
				continue
			}
			if err := tx.Model(&models.Payment{}).Where("id = ?", p.ID).Update("mode", mode).Error; err != nil {
				return err
			}
			p.Mode = mode
			changed = true
			if accountingRefExists(tx, userID, "payment_in_record", p.ID) {
				if err := reverseAccountingByRef(tx, userID, "payment_in_record", p.ID); err != nil {
					return err
				}
				if err := postStandalonePaymentInAccounting(tx, userID, p, p.AmountReceived-p.PaymentInDiscount); err != nil {
					return err
				}
			}
		}

		for _, desc := range []string{
			fmt.Sprintf("Sales invoice %s", inv.InvoiceNumber),
			fmt.Sprintf("POS sale %s", inv.InvoiceNumber),
		} {
			for _, idx := range d.txnByDesc[desc] {
				txn := &d.txns[idx]
				if txn.TransactionType != "add" || !txn.IsLinked || moved[txn.ID] {
					continue
				}
				moved[txn.ID] = true
				m, merr := stmtReassignCashTxn(tx, d, idx, accountID, "payment_in")
				if merr != nil {
					return merr
				}
				changed = changed || m
			}
		}
		return nil
	})
	if err != nil {
		return false, []string{fmt.Sprintf("Row %d: invoice %s: %v", rowNum, txnNo, err)}
	}
	return changed, nil
}

// stmtUpdatePurchaseBillMode handles a "Purchase Bill" row: the bill's payment
// fields plus the bill-time payment-out (auto-numbered POUT-*, or PB-<Txn No>
// from the legacy statement importer) and its cash reduce transaction.
func stmtUpdatePurchaseBillMode(d *stmtMatchData, userID uuid.UUID, rowNum int, txnNo, mode string, accountID *uuid.UUID, paid float64, moved map[uuid.UUID]bool) (bool, []string) {
	if paid <= 0 {
		return false, nil
	}
	if txnNo == "" {
		return false, []string{fmt.Sprintf("Row %d: Purchase Bill — Txn No is required", rowNum)}
	}
	bill, found := d.findBill(txnNo)
	if !found {
		return false, []string{fmt.Sprintf("Row %d: no matching purchase bill for Txn No %s — skipped", rowNum, txnNo)}
	}

	changed := false
	err := utils.DB.Transaction(func(tx *gorm.DB) error {
		if normalizePaymentMethod(bill.PaymentMode) != mode || !bankAccountIDsEqual(bill.BankAccountID, accountID) {
			if err := tx.Model(&models.PurchaseBill{}).Where("id = ?", bill.ID).Updates(map[string]interface{}{
				"payment_mode":    mode,
				"bank_account_id": bankAccountIDValue(accountID),
			}).Error; err != nil {
				return err
			}
			bill.PaymentMode = mode
			bill.BankAccountID = accountID
			changed = true
		}

		for _, po := range d.payOutByBill[bill.ID] {
			num := po.PaymentOutNumber
			if !strings.HasPrefix(num, "POUT-") && num != "PB-"+txnNo {
				// Statement "Payment-out" rows keep their own Txn No —
				// they are updated by their own rows.
				continue
			}
			if normalizePaymentMethod(po.Mode) != mode {
				if err := tx.Model(&models.PaymentOut{}).Where("id = ?", po.ID).Update("mode", mode).Error; err != nil {
					return err
				}
				po.Mode = mode
				changed = true
				if accountingRefExists(tx, userID, "payment_out_record", po.ID) {
					if err := reverseAccountingByRef(tx, userID, "payment_out_record", po.ID); err != nil {
						return err
					}
					if err := postStandalonePaymentOutAccounting(tx, userID, po, po.AmountPaid-po.PaymentOutDiscount); err != nil {
						return err
					}
				}
			}
			m, merr := stmtMovePaymentTxns(tx, d, moved, num, "out", po.Reference, accountID, "payment_out", "reduce")
			if merr != nil {
				return merr
			}
			changed = changed || m
		}

		// Legacy unlinked statement import wrote the cash reduce with the raw
		// txn number as reference and a "Purchase bill <n> (statement)"
		// description.
		for _, idx := range d.txnByRef[txnNo] {
			txn := &d.txns[idx]
			if txn.TransactionType != "reduce" || !txn.IsLinked || moved[txn.ID] {
				continue
			}
			if txn.Description != fmt.Sprintf("Purchase bill %s (statement)", txnNo) {
				continue
			}
			moved[txn.ID] = true
			m, merr := stmtReassignCashTxn(tx, d, idx, accountID, "payment_out")
			if merr != nil {
				return merr
			}
			changed = changed || m
		}
		return nil
	})
	if err != nil {
		return false, []string{fmt.Sprintf("Row %d: purchase bill %s: %v", rowNum, txnNo, err)}
	}
	return changed, nil
}

// stmtUpdatePaymentInMode handles a "Payment-in" row: every payment carrying
// the statement Txn No (a row can be split across several invoices) is
// re-moded, its standalone GL entry re-posted when present, and its linked
// cash add transactions moved to the resolved account.
func stmtUpdatePaymentInMode(d *stmtMatchData, userID uuid.UUID, rowNum int, txnNo, mode string, accountID *uuid.UUID, received float64, moved map[uuid.UUID]bool) (bool, []string) {
	if received <= 0 {
		return false, nil
	}
	if txnNo == "" {
		return false, []string{fmt.Sprintf("Row %d: Payment-in — Txn No is required", rowNum)}
	}
	payments := d.payIn[txnNo]
	if len(payments) == 0 {
		return false, []string{fmt.Sprintf("Row %d: no matching payment-in for Txn No %s — skipped", rowNum, txnNo)}
	}

	changed := false
	err := utils.DB.Transaction(func(tx *gorm.DB) error {
		for _, p := range payments {
			if normalizePaymentMethod(p.Mode) != mode {
				if err := tx.Model(&models.Payment{}).Where("id = ?", p.ID).Update("mode", mode).Error; err != nil {
					return err
				}
				p.Mode = mode
				changed = true

				// Unlinked/standalone payments carry their own GL entry; the
				// asset leg follows the resolved mode.
				if accountingRefExists(tx, userID, "payment_in_record", p.ID) {
					if err := reverseAccountingByRef(tx, userID, "payment_in_record", p.ID); err != nil {
						return err
					}
					if err := postStandalonePaymentInAccounting(tx, userID, p, p.AmountReceived-p.PaymentInDiscount); err != nil {
						return err
					}
				}
			}
			m, merr := stmtMovePaymentTxns(tx, d, moved, p.PaymentInNumber, "in", p.Reference, accountID, "payment_in", "add")
			if merr != nil {
				return merr
			}
			changed = changed || m
		}
		return nil
	})
	if err != nil {
		return false, []string{fmt.Sprintf("Row %d: payment-in %s: %v", rowNum, txnNo, err)}
	}
	return changed, nil
}

// stmtUpdatePaymentOutMode handles a "Payment-out" row — symmetric to
// stmtUpdatePaymentInMode.
func stmtUpdatePaymentOutMode(d *stmtMatchData, userID uuid.UUID, rowNum int, txnNo, mode string, accountID *uuid.UUID, paid float64, moved map[uuid.UUID]bool) (bool, []string) {
	if paid <= 0 {
		return false, nil
	}
	if txnNo == "" {
		return false, []string{fmt.Sprintf("Row %d: Payment-out — Txn No is required", rowNum)}
	}
	payOuts := d.payOut[txnNo]
	if len(payOuts) == 0 {
		return false, []string{fmt.Sprintf("Row %d: no matching payment-out for Txn No %s — skipped", rowNum, txnNo)}
	}

	changed := false
	err := utils.DB.Transaction(func(tx *gorm.DB) error {
		for _, po := range payOuts {
			if normalizePaymentMethod(po.Mode) != mode {
				if err := tx.Model(&models.PaymentOut{}).Where("id = ?", po.ID).Update("mode", mode).Error; err != nil {
					return err
				}
				po.Mode = mode
				changed = true

				if accountingRefExists(tx, userID, "payment_out_record", po.ID) {
					if err := reverseAccountingByRef(tx, userID, "payment_out_record", po.ID); err != nil {
						return err
					}
					if err := postStandalonePaymentOutAccounting(tx, userID, po, po.AmountPaid-po.PaymentOutDiscount); err != nil {
						return err
					}
				}
			}
			m, merr := stmtMovePaymentTxns(tx, d, moved, po.PaymentOutNumber, "out", po.Reference, accountID, "payment_out", "reduce")
			if merr != nil {
				return merr
			}
			changed = changed || m
		}
		return nil
	})
	if err != nil {
		return false, []string{fmt.Sprintf("Row %d: payment-out %s: %v", rowNum, txnNo, err)}
	}
	return changed, nil
}

// stmtUpdateExpenseMode handles an "Expense" row: the expense EXP-<Txn No>
// gets its payment fields updated, the GL entry re-posted, and the linked
// expense cash transaction moved to the resolved account.
func stmtUpdateExpenseMode(d *stmtMatchData, userID uuid.UUID, rowNum int, txnNo, mode string, accountID *uuid.UUID, paid float64, moved map[uuid.UUID]bool) (bool, []string) {
	if paid <= 0 {
		return false, nil
	}
	if txnNo == "" {
		return false, []string{fmt.Sprintf("Row %d: Expense — Txn No is required", rowNum)}
	}
	expense, found := d.findExpense(txnNo)
	if !found {
		return false, []string{fmt.Sprintf("Row %d: no matching expense for Txn No %s — skipped", rowNum, txnNo)}
	}

	changed := false
	err := utils.DB.Transaction(func(tx *gorm.DB) error {
		if normalizePaymentMethod(expense.PaymentMode) != mode || !bankAccountIDsEqual(expense.BankAccountID, accountID) {
			if err := tx.Model(&models.Expense{}).Where("id = ?", expense.ID).Updates(map[string]interface{}{
				"payment_mode":    mode,
				"bank_account_id": bankAccountIDValue(accountID),
			}).Error; err != nil {
				return err
			}
			expense.PaymentMode = mode
			expense.BankAccountID = accountID
			changed = true

			// The expense GL leg (cash vs bank) follows the account.
			if err := reverseAccountingByRef(tx, userID, "expense", expense.ID); err != nil {
				return err
			}
			if err := postExpenseAccounting(tx, userID, expense); err != nil {
				return err
			}
		}

		for _, idx := range d.txnByRef[expense.ExpenseNumber] {
			txn := &d.txns[idx]
			if txn.TransactionType != "expense" || !txn.IsLinked || moved[txn.ID] {
				continue
			}
			moved[txn.ID] = true
			// Expense postings are keyed to the expense row — no per-txn GL.
			m, merr := stmtReassignCashTxn(tx, d, idx, accountID, "")
			if merr != nil {
				return merr
			}
			changed = changed || m
		}
		return nil
	})
	if err != nil {
		return false, []string{fmt.Sprintf("Row %d: expense %s: %v", rowNum, txnNo, err)}
	}
	return changed, nil
}

// stmtUpdateReturnMode handles "Sales Return" / "Purchase Return" rows: the
// transaction import records the refund as a payment-out SR-<Txn No> or a
// payment-in PR-<Txn No>; its mode and cash transaction are updated here.
func stmtUpdateReturnMode(d *stmtMatchData, userID uuid.UUID, rowNum int, number, label, mode string, accountID *uuid.UUID, amount float64, txnType string, moved map[uuid.UUID]bool) (bool, []string) {
	if amount <= 0 {
		return false, nil
	}
	if number == "SR-" || number == "PR-" || strings.TrimSpace(number) == "" {
		return false, []string{fmt.Sprintf("Row %d: %s — Txn No is required", rowNum, label)}
	}

	changed := false
	err := utils.DB.Transaction(func(tx *gorm.DB) error {
		if txnType == "add" {
			payments := d.payIn[number]
			if len(payments) == 0 {
				return fmt.Errorf("no matching payment-in %s", number)
			}
			for _, p := range payments {
				if normalizePaymentMethod(p.Mode) != mode {
					if err := tx.Model(&models.Payment{}).Where("id = ?", p.ID).Update("mode", mode).Error; err != nil {
						return err
					}
					p.Mode = mode
					changed = true
					if accountingRefExists(tx, userID, "payment_in_record", p.ID) {
						if err := reverseAccountingByRef(tx, userID, "payment_in_record", p.ID); err != nil {
							return err
						}
						if err := postStandalonePaymentInAccounting(tx, userID, p, p.AmountReceived-p.PaymentInDiscount); err != nil {
							return err
						}
					}
				}
			}
		} else {
			payOuts := d.payOut[number]
			if len(payOuts) == 0 {
				return fmt.Errorf("no matching payment-out %s", number)
			}
			for _, po := range payOuts {
				if normalizePaymentMethod(po.Mode) != mode {
					if err := tx.Model(&models.PaymentOut{}).Where("id = ?", po.ID).Update("mode", mode).Error; err != nil {
						return err
					}
					po.Mode = mode
					changed = true
					if accountingRefExists(tx, userID, "payment_out_record", po.ID) {
						if err := reverseAccountingByRef(tx, userID, "payment_out_record", po.ID); err != nil {
							return err
						}
						if err := postStandalonePaymentOutAccounting(tx, userID, po, po.AmountPaid-po.PaymentOutDiscount); err != nil {
							return err
						}
					}
				}
			}
		}

		refType := "payment_in"
		if txnType != "add" {
			refType = "payment_out"
		}
		m, merr := stmtMoveRefTxns(tx, d, moved, number, txnType, accountID, refType)
		if merr != nil {
			return merr
		}
		changed = changed || m
		return nil
	})
	if err != nil {
		return false, []string{fmt.Sprintf("Row %d: %s %s: %v — skipped", rowNum, label, number, err)}
	}
	return changed, nil
}

// stmtUpdateManualCashMode handles "Add Money" / "Reduce Money" rows: the
// unlinked cash transaction recorded as CB-ADD-<Txn No> / CB-RED-<Txn No> is
// moved to the resolved account.
func stmtUpdateManualCashMode(d *stmtMatchData, userID uuid.UUID, rowNum int, txnNo string, accountID *uuid.UUID, amount float64, txnType string, moved map[uuid.UUID]bool) (bool, []string) {
	if amount <= 0 {
		return false, nil
	}
	if txnNo == "" {
		return false, []string{fmt.Sprintf("Row %d: %s Money — Txn No is required", rowNum, txnType)}
	}
	dir := "ADD"
	refType := "cash_add"
	if txnType == "reduce" {
		dir = "RED"
		refType = "cash_reduce"
	}
	reference := fmt.Sprintf("CB-%s-%s", dir, txnNo)

	matched := false
	changed := false
	err := utils.DB.Transaction(func(tx *gorm.DB) error {
		for _, idx := range d.txnByRef[reference] {
			txn := &d.txns[idx]
			if txn.TransactionType != txnType || txn.IsLinked || moved[txn.ID] {
				continue
			}
			matched = true
			moved[txn.ID] = true
			m, merr := stmtReassignCashTxn(tx, d, idx, accountID, refType)
			if merr != nil {
				return merr
			}
			changed = changed || m
		}
		return nil
	})
	if err != nil {
		return false, []string{fmt.Sprintf("Row %d: %s: %v", rowNum, reference, err)}
	}
	if !matched {
		return false, []string{fmt.Sprintf("Row %d: no matching cash transaction %s — skipped", rowNum, reference)}
	}
	return changed, nil
}

// -----------------------------------------------------------------------------
// Shared helpers used by the daybook importer
// -----------------------------------------------------------------------------

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


