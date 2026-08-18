package controllers

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func recordSalePaymentIn(tx *gorm.DB, userID uuid.UUID, accountID *uuid.UUID, amount float64, date time.Time, reference, description string) error {
	if amount <= 0 {
		return nil
	}
	if accountID != nil {
		var account models.BankAccount
		if err := tx.Where("user_id = ? AND id = ? AND is_active = ?", userID, *accountID, true).First(&account).Error; err != nil {
			return err
		}
		account.Balance += amount
		if err := tx.Save(&account).Error; err != nil {
			return err
		}
	}
	transaction := models.CashTransaction{
		ID:              uuid.New(),
		UserID:          userID,
		AccountID:       accountID,
		TransactionType: "add",
		Amount:          amount,
		Date:            date,
		Description:     description,
		Reference:       reference,
		IsLinked:        true,
	}
	if err := tx.Create(&transaction).Error; err != nil {
		return err
	}
	return postSalePaymentAccounting(tx, userID, transaction.ID, accountID, amount, date, reference, description)
}

func recordPurchasePaymentOut(tx *gorm.DB, userID uuid.UUID, accountID *uuid.UUID, amount float64, date time.Time, reference, description string) error {
	if amount <= 0 {
		return nil
	}
	if accountID != nil {
		var account models.BankAccount
		if err := tx.Where("user_id = ? AND id = ? AND is_active = ?", userID, *accountID, true).First(&account).Error; err != nil {
			return err
		}
		account.Balance -= amount
		if err := tx.Save(&account).Error; err != nil {
			return err
		}
	}
	transaction := models.CashTransaction{
		ID:              uuid.New(),
		UserID:          userID,
		AccountID:       accountID,
		TransactionType: "reduce",
		Amount:          amount,
		Date:            date,
		Description:     description,
		Reference:       reference,
		IsLinked:        true,
	}
	if err := tx.Create(&transaction).Error; err != nil {
		return err
	}
	return postPurchasePaymentAccounting(tx, userID, transaction.ID, accountID, amount, date, reference, description)
}

// createLinkedSalePaymentIn records Payment row(s) for a sales invoice payment
// and posts cash/bank + AR reduction. Invoice amount_paid/status must already be set.
// When invoice.PaymentSplits is set, one Payment + cash transaction is created per split.
func createLinkedSalePaymentIn(tx *gorm.DB, userID uuid.UUID, invoice *models.Invoice, amount float64, date time.Time, notes string) error {
	if amount <= 0 || invoice == nil {
		return nil
	}
	resolved, err := resolveInvoicePaymentSplits(userID, invoice.PaymentSplits, invoice.PaymentMode, amount)
	if err != nil {
		return err
	}
	for _, split := range resolved {
		if err := createLinkedSalePaymentInWithMode(tx, userID, invoice, split.Amount, split.Mode, split.BankAccountID, date, notes); err != nil {
			return err
		}
	}
	return nil
}

func createLinkedSalePaymentInWithMode(tx *gorm.DB, userID uuid.UUID, invoice *models.Invoice, amount float64, mode string, accountID *uuid.UUID, date time.Time, notes string) error {
	if amount <= 0 || invoice == nil {
		return nil
	}

	number := allocateUniquePaymentInNumber(tx, userID, "")

	mode = normalizePaymentMethod(mode)
	if mode == "" {
		mode = "cash"
	}

	invoiceID := invoice.ID
	payment := models.Payment{
		ID:              uuid.New(),
		UserID:          userID,
		InvoiceID:       &invoiceID,
		PartyID:         invoice.PartyID,
		AmountReceived:  amount,
		PaymentInNumber: number,
		Mode:            mode,
		Date:            date,
		Reference:       invoice.InvoiceNumber,
		Notes:           notes,
	}
	if err := tx.Create(&payment).Error; err != nil {
		return err
	}

	desc := salePaymentDescription(invoice)
	if err := recordSalePaymentIn(tx, userID, accountID, amount, date, invoice.InvoiceNumber, desc); err != nil {
		return err
	}

	var party models.Party
	if err := tx.Where("user_id = ? AND id = ?", userID, invoice.PartyID).First(&party).Error; err == nil {
		if err := tx.Model(&party).Update("balance", party.Balance-amount).Error; err != nil {
			return err
		}
	}

	return nil
}

func salePaymentDescription(invoice *models.Invoice) string {
	if invoice != nil && invoice.IsPOS {
		return fmt.Sprintf("POS sale %s", invoice.InvoiceNumber)
	}
	if invoice == nil {
		return "Sales invoice"
	}
	return fmt.Sprintf("Sales invoice %s", invoice.InvoiceNumber)
}

func findInvoiceCashTransactions(tx *gorm.DB, userID uuid.UUID, invoiceNumber string) ([]models.CashTransaction, error) {
	var txns []models.CashTransaction
	invoiceNumber = strings.TrimSpace(invoiceNumber)
	if invoiceNumber == "" {
		return txns, nil
	}
	err := tx.Where(
		"user_id = ? AND transaction_type = ? AND (reference = ? OR description IN ?)",
		userID,
		"add",
		invoiceNumber,
		[]string{
			fmt.Sprintf("Sales invoice %s", invoiceNumber),
			fmt.Sprintf("POS sale %s", invoiceNumber),
		},
	).Order("created_at ASC").Find(&txns).Error
	return txns, err
}

func mergeCashTransactions(groups ...[]models.CashTransaction) []models.CashTransaction {
	seen := make(map[uuid.UUID]bool)
	out := make([]models.CashTransaction, 0)
	for _, group := range groups {
		for _, txn := range group {
			if seen[txn.ID] {
				continue
			}
			seen[txn.ID] = true
			out = append(out, txn)
		}
	}
	return out
}

func bankAccountIDValue(id *uuid.UUID) interface{} {
	if id == nil {
		return nil
	}
	return *id
}

func adjustBankAccountBalance(tx *gorm.DB, userID uuid.UUID, accountID *uuid.UUID, delta float64, requireActive bool) error {
	if accountID == nil || delta == 0 {
		return nil
	}
	var account models.BankAccount
	query := tx.Where("user_id = ? AND id = ?", userID, *accountID)
	if requireActive {
		query = query.Where("is_active = ?", true)
	}
	if err := query.First(&account).Error; err != nil {
		if !requireActive && errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	account.Balance += delta
	return tx.Save(&account).Error
}

func adjustPartyBalance(tx *gorm.DB, userID, partyID uuid.UUID, delta float64) error {
	if delta == 0 {
		return nil
	}
	var party models.Party
	if err := tx.Where("user_id = ? AND id = ?", userID, partyID).First(&party).Error; err != nil {
		return err
	}
	return tx.Model(&party).Update("balance", party.Balance+delta).Error
}

func reverseCashAddTransaction(tx *gorm.DB, userID uuid.UUID, txn models.CashTransaction) error {
	if err := adjustBankAccountBalance(tx, userID, txn.AccountID, -txn.Amount, false); err != nil {
		return err
	}
	if err := reverseAccountingByRef(tx, userID, "payment_in", txn.ID); err != nil {
		return err
	}
	return tx.Delete(&txn).Error
}

func updateInvoiceCashTransaction(tx *gorm.DB, userID uuid.UUID, txn *models.CashTransaction, invoice *models.Invoice, amount float64) error {
	if txn == nil || invoice == nil {
		return nil
	}
	if err := adjustBankAccountBalance(tx, userID, txn.AccountID, -txn.Amount, false); err != nil {
		return err
	}
	if err := reverseAccountingByRef(tx, userID, "payment_in", txn.ID); err != nil {
		return err
	}

	desc := salePaymentDescription(invoice)
	if err := tx.Model(txn).
		Select("account_id", "amount", "date", "description", "reference", "is_linked", "transaction_type").
		Updates(map[string]interface{}{
			"account_id":       bankAccountIDValue(invoice.BankAccountID),
			"amount":           amount,
			"date":             invoice.Date,
			"description":      desc,
			"reference":        invoice.InvoiceNumber,
			"is_linked":        true,
			"transaction_type": "add",
		}).Error; err != nil {
		return err
	}

	if err := adjustBankAccountBalance(tx, userID, invoice.BankAccountID, amount, true); err != nil {
		return err
	}
	return postSalePaymentAccounting(tx, userID, txn.ID, invoice.BankAccountID, amount, invoice.Date, invoice.InvoiceNumber, desc)
}

func updateInvoicePaymentRecord(tx *gorm.DB, userID uuid.UUID, payment *models.Payment, invoice *models.Invoice, amount float64) error {
	if payment == nil || invoice == nil {
		return nil
	}
	if err := reverseAccountingByRef(tx, userID, "payment_in_record", payment.ID); err != nil {
		return err
	}
	mode := invoice.PaymentMode
	if mode == "" {
		mode = "cash"
	}
	invoiceID := invoice.ID
	return tx.Model(payment).Updates(map[string]interface{}{
		"amount_received":     amount,
		"payment_in_discount": 0,
		"mode":                mode,
		"date":                invoice.Date,
		"reference":           invoice.InvoiceNumber,
		"party_id":            invoice.PartyID,
		"invoice_id":          invoiceID,
		"notes":               linkedSalePaymentNotes(invoice),
	}).Error
}

func createInvoicePaymentRecord(tx *gorm.DB, userID uuid.UUID, invoice *models.Invoice, amount float64) error {
	if amount <= 0 || invoice == nil {
		return nil
	}
	mode := invoice.PaymentMode
	if mode == "" {
		mode = "cash"
	}
	invoiceID := invoice.ID
	payment := models.Payment{
		ID:              uuid.New(),
		UserID:          userID,
		InvoiceID:       &invoiceID,
		PartyID:         invoice.PartyID,
		AmountReceived:  amount,
		PaymentInNumber: allocateUniquePaymentInNumber(tx, userID, ""),
		Mode:            mode,
		Date:            invoice.Date,
		Reference:       invoice.InvoiceNumber,
		Notes:           linkedSalePaymentNotes(invoice),
	}
	return tx.Create(&payment).Error
}

func invoicePaymentsTotal(payments []models.Payment) float64 {
	var total float64
	for _, payment := range payments {
		total += payment.AmountReceived - payment.PaymentInDiscount
	}
	return total
}

func cashTransactionsTotal(txns []models.CashTransaction) float64 {
	var total float64
	for _, txn := range txns {
		total += txn.Amount
	}
	return total
}

// reverseLinkedInvoicePayments removes payment-ins created for a sales invoice and
// restores cash/bank balances plus linked GL postings.
func reverseLinkedInvoicePayments(tx *gorm.DB, userID uuid.UUID, invoice *models.Invoice, extraInvoiceNumbers ...string) error {
	if invoice == nil {
		return nil
	}

	var payments []models.Payment
	if err := tx.Where("user_id = ? AND invoice_id = ?", userID, invoice.ID).Find(&payments).Error; err != nil {
		return err
	}
	totalPaid := invoicePaymentsTotal(payments)

	numbers := []string{strings.TrimSpace(invoice.InvoiceNumber)}
	for _, number := range extraInvoiceNumbers {
		number = strings.TrimSpace(number)
		if number == "" {
			continue
		}
		duplicate := false
		for _, existing := range numbers {
			if existing == number {
				duplicate = true
				break
			}
		}
		if !duplicate {
			numbers = append(numbers, number)
		}
	}

	var txns []models.CashTransaction
	for _, number := range numbers {
		found, err := findInvoiceCashTransactions(tx, userID, number)
		if err != nil {
			return err
		}
		txns = mergeCashTransactions(txns, found)
	}

	for _, txn := range txns {
		if err := reverseCashAddTransaction(tx, userID, txn); err != nil {
			return err
		}
	}

	for _, payment := range payments {
		if err := reverseAccountingByRef(tx, userID, "payment_in_record", payment.ID); err != nil {
			return err
		}
		if err := tx.Delete(&payment).Error; err != nil {
			return err
		}
	}

	if totalPaid > 0 {
		if err := adjustPartyBalance(tx, userID, invoice.PartyID, totalPaid); err != nil {
			return err
		}
	}

	return nil
}

func invoiceCashSnapshot(invoice models.Invoice) models.Invoice {
	return models.Invoice{
		ID:            invoice.ID,
		PartyID:       invoice.PartyID,
		InvoiceNumber: invoice.InvoiceNumber,
		AmountPaid:    invoice.AmountPaid,
		PaymentMode:   invoice.PaymentMode,
		BankAccountID: invoice.BankAccountID,
		Date:          invoice.Date,
		IsPOS:         invoice.IsPOS,
		PaymentSplits: copyPaymentSplits(invoice.PaymentSplits),
	}
}

func invoiceCashLedgerNeedsResync(previous, current *models.Invoice) bool {
	if previous == nil || current == nil {
		return false
	}
	if math.Abs(previous.AmountPaid-current.AmountPaid) > 0.009 {
		return true
	}
	if normalizePaymentMethod(previous.PaymentMode) != normalizePaymentMethod(current.PaymentMode) {
		return true
	}
	if !bankAccountIDsEqual(previous.BankAccountID, current.BankAccountID) {
		return true
	}
	if previous.InvoiceNumber != current.InvoiceNumber {
		return true
	}
	if previous.PartyID != current.PartyID {
		return true
	}
	if previous.Date.Format("2006-01-02") != current.Date.Format("2006-01-02") {
		return true
	}
	if current.PaymentSplits != nil && !paymentSplitsEqual(previous.PaymentSplits, current.PaymentSplits) {
		return true
	}
	return false
}

func linkedSalePaymentNotes(invoice *models.Invoice) string {
	if invoice == nil {
		return "Auto-created from sales invoice"
	}
	if invoice.IsPOS {
		return fmt.Sprintf("Auto-created from POS sale %s", invoice.InvoiceNumber)
	}
	return fmt.Sprintf("Auto-created from sales invoice %s", invoice.InvoiceNumber)
}

func rewriteLinkedInvoicePayments(tx *gorm.DB, userID uuid.UUID, previous, current *models.Invoice) error {
	if previous == nil || current == nil {
		return nil
	}

	resolved, err := ledgerSplitsForInvoice(tx, userID, previous, current)
	if err != nil {
		return err
	}

	reversal := *current
	reversal.PartyID = previous.PartyID
	if err := reverseLinkedInvoicePayments(tx, userID, &reversal, previous.InvoiceNumber); err != nil {
		return err
	}

	if current.AmountPaid <= 0 || len(resolved) == 0 {
		return nil
	}

	notes := linkedSalePaymentNotes(current)
	for _, split := range resolved {
		if err := createLinkedSalePaymentInWithMode(tx, userID, current, split.Amount, split.Mode, split.BankAccountID, current.Date, notes); err != nil {
			return err
		}
	}
	return nil
}

// resyncLinkedInvoicePayments updates existing cash/bank transactions and payment-in
// rows for an invoice when the amount, method, or destination account changes.
func resyncLinkedInvoicePayments(db *gorm.DB, userID uuid.UUID, previous, current *models.Invoice) error {
	if !invoiceCashLedgerNeedsResync(previous, current) {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		return rewriteLinkedInvoicePayments(tx, userID, previous, current)
	})
}

// createLinkedPurchasePaymentOut records a PaymentOut row for a purchase bill payment
// and posts cash/bank + AP reduction. Bill paid_amount/balance_due must already be updated.
func createLinkedPurchasePaymentOut(tx *gorm.DB, userID uuid.UUID, bill *models.PurchaseBill, amount float64, date time.Time, notes string) error {
	if amount <= 0 || bill == nil {
		return nil
	}

	var count int64
	if err := tx.Model(&models.PaymentOut{}).Where("user_id = ?", userID).Count(&count).Error; err != nil {
		return err
	}
	number := fmt.Sprintf("POUT-%04d", count+1)

	mode := bill.PaymentMode
	if mode == "" {
		mode = "cash"
	}

	billID := bill.ID
	paymentOut := models.PaymentOut{
		ID:               uuid.New(),
		UserID:           userID,
		PurchaseBillID:   &billID,
		PartyID:          bill.PartyID,
		AmountPaid:       amount,
		PaymentOutNumber: number,
		Mode:             mode,
		Date:             date,
		Reference:        bill.BillNumber,
		Notes:            notes,
	}
	if err := tx.Create(&paymentOut).Error; err != nil {
		return err
	}

	desc := fmt.Sprintf("Payment out %s for purchase %s", number, bill.BillNumber)
	if err := recordPurchasePaymentOut(tx, userID, bill.BankAccountID, amount, date, bill.BillNumber, desc); err != nil {
		return err
	}

	// Match standalone PaymentOut behaviour: bump party balance by amount paid.
	var party models.Party
	if err := tx.Where("user_id = ? AND id = ?", userID, bill.PartyID).First(&party).Error; err == nil {
		if err := tx.Model(&party).Update("balance", party.Balance+amount).Error; err != nil {
			return err
		}
	}

	return nil
}

// reverseCashReduceTransaction restores cash/bank for a linked "reduce" cash
// transaction (used by purchase payment outs) and removes the transaction.
func reverseCashReduceTransaction(tx *gorm.DB, userID uuid.UUID, txn models.CashTransaction) error {
	if err := adjustBankAccountBalance(tx, userID, txn.AccountID, txn.Amount, false); err != nil {
		return err
	}
	if err := reverseAccountingByRef(tx, userID, "payment_out", txn.ID); err != nil {
		return err
	}
	return tx.Delete(&txn).Error
}

// reverseLinkedPurchasePaymentOuts removes PaymentOut rows created for a
// purchase bill and restores cash/bank balances plus linked GL postings.
// The bill should carry the *previous* party ID and bill number so that
// cash transactions and party balance are reversed correctly.
func reverseLinkedPurchasePaymentOuts(tx *gorm.DB, userID uuid.UUID, bill *models.PurchaseBill) error {
	if bill == nil {
		return nil
	}

	var paymentOuts []models.PaymentOut
	if err := tx.Where("user_id = ? AND purchase_bill_id = ?", userID, bill.ID).Find(&paymentOuts).Error; err != nil {
		return err
	}
	totalPaid := 0.0
	for _, po := range paymentOuts {
		totalPaid += po.AmountPaid
	}

	// Find and reverse linked cash/bank transactions (reference = bill number, type = reduce).
	billNumber := strings.TrimSpace(bill.BillNumber)
	if billNumber != "" {
		var txns []models.CashTransaction
		if err := tx.Where(
			"user_id = ? AND transaction_type = ? AND reference = ? AND is_linked = ?",
			userID, "reduce", billNumber, true,
		).Order("created_at ASC").Find(&txns).Error; err != nil {
			return err
		}
		for _, txn := range txns {
			if err := reverseCashReduceTransaction(tx, userID, txn); err != nil {
				return err
			}
		}
	}

	// Reverse accounting and delete payment out records.
	for _, po := range paymentOuts {
		if err := reverseAccountingByRef(tx, userID, "payment_out_record", po.ID); err != nil {
			return err
		}
		if err := tx.Delete(&po).Error; err != nil {
			return err
		}
	}

	// Restore party balance (we had increased it by totalPaid when creating payment outs).
	if totalPaid > 0 {
		if err := adjustPartyBalance(tx, userID, bill.PartyID, -totalPaid); err != nil {
			return err
		}
	}

	return nil
}

// recordExpenseCashOut deducts an expense from a bank account or cash in-hand.
func recordExpenseCashOut(tx *gorm.DB, userID uuid.UUID, accountID *uuid.UUID, amount float64, date time.Time, reference, description string) error {
	if amount <= 0 {
		return nil
	}
	if accountID != nil {
		var account models.BankAccount
		if err := tx.Where("user_id = ? AND id = ? AND is_active = ?", userID, *accountID, true).First(&account).Error; err != nil {
			return err
		}
		account.Balance -= amount
		if err := tx.Save(&account).Error; err != nil {
			return err
		}
	}
	transaction := models.CashTransaction{
		ID:              uuid.New(),
		UserID:          userID,
		AccountID:       accountID,
		TransactionType: "expense",
		Amount:          amount,
		Date:            date,
		Description:     description,
		Reference:       reference,
		IsLinked:        true,
	}
	return tx.Create(&transaction).Error
}

// reverseExpenseCashOut restores cash/bank for an expense referenced by expense number.
func reverseExpenseCashOut(tx *gorm.DB, userID uuid.UUID, reference string) error {
	if reference == "" {
		return nil
	}
	var transactions []models.CashTransaction
	if err := tx.Where(
		"user_id = ? AND reference = ? AND is_linked = ? AND transaction_type = ?",
		userID, reference, true, "expense",
	).Find(&transactions).Error; err != nil {
		return err
	}
	for _, txn := range transactions {
		if txn.AccountID != nil {
			var account models.BankAccount
			if err := tx.Where("user_id = ? AND id = ?", userID, *txn.AccountID).First(&account).Error; err == nil {
				account.Balance += txn.Amount
				if err := tx.Save(&account).Error; err != nil {
					return err
				}
			}
		}
		if err := tx.Delete(&txn).Error; err != nil {
			return err
		}
	}
	return nil
}

// recordPayrollCashOut deducts net salary from a bank account or cash in-hand
// and writes a linked cash-bank transaction typed as payroll.
func recordPayrollCashOut(tx *gorm.DB, userID uuid.UUID, accountID *uuid.UUID, amount float64, date time.Time, reference, description string) error {
	if amount <= 0 {
		return nil
	}
	if accountID != nil {
		var account models.BankAccount
		if err := tx.Where("user_id = ? AND id = ? AND is_active = ?", userID, *accountID, true).First(&account).Error; err != nil {
			return err
		}
		account.Balance -= amount
		if err := tx.Save(&account).Error; err != nil {
			return err
		}
	}
	transaction := models.CashTransaction{
		ID:              uuid.New(),
		UserID:          userID,
		AccountID:       accountID,
		TransactionType: "payroll",
		Amount:          amount,
		Date:            date,
		Description:     description,
		Reference:       reference,
		IsLinked:        true,
	}
	return tx.Create(&transaction).Error
}

// reversePayrollCashOut restores cash/bank for a payroll payment referenced by payment number.
func reversePayrollCashOut(tx *gorm.DB, userID uuid.UUID, reference string) error {
	if reference == "" {
		return nil
	}
	var transactions []models.CashTransaction
	// Include legacy "reduce" rows created before payroll had its own transaction type.
	if err := tx.Where(
		"user_id = ? AND reference = ? AND is_linked = ? AND transaction_type IN ?",
		userID, reference, true, []string{"payroll", "reduce"},
	).Find(&transactions).Error; err != nil {
		return err
	}
	for _, txn := range transactions {
		if txn.AccountID != nil {
			var account models.BankAccount
			if err := tx.Where("user_id = ? AND id = ?", userID, *txn.AccountID).First(&account).Error; err == nil {
				account.Balance += txn.Amount
				if err := tx.Save(&account).Error; err != nil {
					return err
				}
			}
		}
		if err := tx.Delete(&txn).Error; err != nil {
			return err
		}
	}
	return nil
}

func validateUserBankAccount(userID uuid.UUID, accountID *uuid.UUID) error {
	if accountID == nil {
		return nil
	}
	var account models.BankAccount
	return utils.DB.Where("user_id = ? AND id = ? AND is_active = ?", userID, *accountID, true).First(&account).Error
}

const paymentInNumberPrefix = "PIN"

func paymentInNumberInUse(db *gorm.DB, userID uuid.UUID, number string) bool {
	number = strings.TrimSpace(number)
	if number == "" {
		return false
	}
	var n int64
	db.Model(&models.Payment{}).Where("user_id = ? AND payment_in_number = ?", userID, number).Count(&n)
	return n > 0
}

func parsePaymentNumberSequence(number, prefix string) int64 {
	raw := strings.TrimSpace(number)
	p := strings.TrimSpace(prefix)
	if raw == "" || p == "" {
		return 0
	}
	want := strings.ToUpper(p) + "-"
	if !strings.HasPrefix(strings.ToUpper(raw), want) {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(raw[len(want):]), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func maxPaymentInSequence(db *gorm.DB, userID uuid.UUID) int64 {
	var numbers []string
	db.Model(&models.Payment{}).
		Where("user_id = ? AND payment_in_number LIKE ?", userID, paymentInNumberPrefix+"-%").
		Pluck("payment_in_number", &numbers)
	var max int64
	for _, number := range numbers {
		if seq := parsePaymentNumberSequence(number, paymentInNumberPrefix); seq > max {
			max = seq
		}
	}
	return max
}

func allocateUniquePaymentInNumber(db *gorm.DB, userID uuid.UUID, preferred string) string {
	preferred = strings.TrimSpace(preferred)
	if preferred != "" && !paymentInNumberInUse(db, userID, preferred) {
		return preferred
	}
	start := maxPaymentInSequence(db, userID) + 1
	if start < 1 {
		start = 1
	}
	for i := start; i < start+10000; i++ {
		candidate := fmt.Sprintf("%s-%04d", paymentInNumberPrefix, i)
		if !paymentInNumberInUse(db, userID, candidate) {
			return candidate
		}
	}
	return fmt.Sprintf("%s-%d", paymentInNumberPrefix, time.Now().Unix())
}
