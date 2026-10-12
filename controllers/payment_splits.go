package controllers

import (
	"math"
	"strings"
	"truerp/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type resolvedPaymentSplit struct {
	Mode          string
	Amount        float64
	BankAccountID *uuid.UUID
}

func paymentSplitAmount(amount float64) float64 {
	if amount < 0 {
		return 0
	}
	return math.Round(amount*100) / 100
}

func mergePaymentSplits(splits []models.PaymentSplit) []models.PaymentSplit {
	byKey := make(map[string]int, len(splits))
	out := make([]models.PaymentSplit, 0, len(splits))
	for _, split := range splits {
		mode := normalizePaymentMethod(split.Mode)
		amount := paymentSplitAmount(split.Amount)
		if amount <= 0.009 {
			continue
		}
		// Two splits with the same mode but different bank accounts (e.g.
		// bank_transfer to two different accounts) must stay separate.
		key := mode + "|" + bankAccountKey(split.BankAccountID)
		if idx, exists := byKey[key]; exists {
			out[idx].Amount = paymentSplitAmount(out[idx].Amount + amount)
			continue
		}
		byKey[key] = len(out)
		out = append(out, models.PaymentSplit{
			Mode:          mode,
			Amount:        amount,
			BankAccountID: split.BankAccountID,
		})
	}
	return out
}

func bankAccountKey(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

func paymentSplitsTotal(splits []models.PaymentSplit) float64 {
	var total float64
	for _, split := range splits {
		total += split.Amount
	}
	return paymentSplitAmount(total)
}

func clampPaymentSplitsToTotal(splits []models.PaymentSplit, total float64) []models.PaymentSplit {
	total = paymentSplitAmount(total)
	if total <= 0.009 {
		return nil
	}
	var sum float64
	out := make([]models.PaymentSplit, 0, len(splits))
	for _, split := range splits {
		if sum >= total-0.009 {
			break
		}
		amount := paymentSplitAmount(split.Amount)
		if sum+amount > total {
			amount = paymentSplitAmount(total - sum)
		}
		if amount <= 0.009 {
			continue
		}
		out = append(out, models.PaymentSplit{Mode: split.Mode, Amount: amount, BankAccountID: split.BankAccountID})
		sum += amount
	}
	return out
}

func adjustPaymentSplitsToAmount(splits []models.PaymentSplit, amount float64) []models.PaymentSplit {
	amount = paymentSplitAmount(amount)
	if amount <= 0.009 {
		return nil
	}
	merged := mergePaymentSplits(splits)
	if len(merged) == 0 {
		return []models.PaymentSplit{{Mode: "cash", Amount: amount}}
	}
	sum := paymentSplitsTotal(merged)
	if math.Abs(sum-amount) <= 0.009 {
		return merged
	}
	if amount > sum {
		merged[len(merged)-1].Amount = paymentSplitAmount(merged[len(merged)-1].Amount + (amount - sum))
		return merged
	}
	remaining := amount
	out := make([]models.PaymentSplit, 0, len(merged))
	for _, split := range merged {
		if remaining <= 0.009 {
			break
		}
		if split.Amount <= remaining+0.009 {
			out = append(out, split)
			remaining = paymentSplitAmount(remaining - split.Amount)
			continue
		}
		out = append(out, models.PaymentSplit{Mode: split.Mode, Amount: remaining, BankAccountID: split.BankAccountID})
		break
	}
	return out
}

func paymentSplitsEqual(a, b []models.PaymentSplit) bool {
	left := mergePaymentSplits(a)
	right := mergePaymentSplits(b)
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].Mode != right[i].Mode {
			return false
		}
		if math.Abs(left[i].Amount-right[i].Amount) > 0.009 {
			return false
		}
	}
	return true
}

func modelSplitsFromResolved(resolved []resolvedPaymentSplit) []models.PaymentSplit {
	out := make([]models.PaymentSplit, 0, len(resolved))
	for _, split := range resolved {
		out = append(out, models.PaymentSplit{Mode: split.Mode, Amount: split.Amount, BankAccountID: split.BankAccountID})
	}
	return out
}

func resolveInvoicePaymentSplits(userID uuid.UUID, splits []models.PaymentSplit, fallbackMode string, fallbackAmount float64, explicitAccount ...*uuid.UUID) ([]resolvedPaymentSplit, error) {
	merged := mergePaymentSplits(splits)
	if len(merged) == 0 && fallbackAmount > 0.009 {
		merged = []models.PaymentSplit{{
			Mode:   normalizePaymentMethod(fallbackMode),
			Amount: paymentSplitAmount(fallbackAmount),
		}}
	}
	var explicit *uuid.UUID
	if len(explicitAccount) > 0 {
		explicit = explicitAccount[0]
	}
	out := make([]resolvedPaymentSplit, 0, len(merged))
	for _, split := range merged {
		// A split can pin its own bank account (purchase bills); otherwise a
		// lone split inherits the document-level account and multi-splits
		// resolve each mode through the payment-method mapping.
		accountID := split.BankAccountID
		if accountID == nil && len(merged) == 1 {
			accountID = explicit
		}
		resolvedID, err := resolveBankAccountForPaymentMode(userID, split.Mode, accountID)
		if err != nil {
			return nil, err
		}
		out = append(out, resolvedPaymentSplit{
			Mode:          split.Mode,
			Amount:        split.Amount,
			BankAccountID: resolvedID,
		})
	}
	return out, nil
}

// selectInvoicePaymentSplits keeps omitted payment_splits backward compatible:
// nil input (old clients) preserves existing linked methods; an explicit list replaces them.
func selectInvoicePaymentSplits(input, existing []models.PaymentSplit, amount float64) []models.PaymentSplit {
	if input == nil {
		if len(mergePaymentSplits(existing)) == 0 {
			return nil
		}
		return adjustPaymentSplitsToAmount(existing, amount)
	}
	return clampPaymentSplitsToTotal(mergePaymentSplits(input), amount)
}

func applyResolvedSplitsToInvoice(invoice *models.Invoice, resolved []resolvedPaymentSplit) {
	if invoice == nil {
		return
	}
	invoice.PaymentSplits = modelSplitsFromResolved(resolved)
	var total float64
	for _, split := range resolved {
		total += split.Amount
	}
	invoice.AmountPaid = paymentSplitAmount(total)
	if len(resolved) > 0 {
		invoice.PaymentMode = resolved[0].Mode
		invoice.BankAccountID = resolved[0].BankAccountID
		return
	}
	if strings.TrimSpace(invoice.PaymentMode) == "" {
		invoice.PaymentMode = "cash"
	}
}

func finalizeInvoicePaymentSplits(userID uuid.UUID, invoice *models.Invoice, inputSplits []models.PaymentSplit, fallbackMode string, explicitAccount *uuid.UUID) error {
	if invoice == nil {
		return nil
	}
	amount := invoice.AmountPaid
	if invoice.Status == "paid" {
		amount = invoice.TotalAmount
		invoice.AmountPaid = amount
	} else if amount > invoice.TotalAmount {
		amount = invoice.TotalAmount
		invoice.AmountPaid = amount
	}
	if amount < 0 {
		amount = 0
		invoice.AmountPaid = 0
	}

	splits := selectInvoicePaymentSplits(inputSplits, invoice.PaymentSplits, amount)
	resolved, err := resolveInvoicePaymentSplits(userID, splits, fallbackMode, amount, explicitAccount)
	if err != nil {
		return err
	}

	var sum float64
	for _, split := range resolved {
		sum += split.Amount
	}
	if invoice.Status == "paid" && invoice.TotalAmount-sum > 0.009 {
		if len(resolved) == 0 {
			resolved, err = resolveInvoicePaymentSplits(userID, nil, fallbackMode, invoice.TotalAmount, explicitAccount)
			if err != nil {
				return err
			}
		} else {
			resolved[len(resolved)-1].Amount = paymentSplitAmount(resolved[len(resolved)-1].Amount + (invoice.TotalAmount - sum))
		}
	}

	applyResolvedSplitsToInvoice(invoice, resolved)
	return nil
}

func attachInvoicePaymentSplits(db *gorm.DB, invoice *models.Invoice) {
	if invoice == nil || db == nil {
		return
	}
	var payments []models.Payment
	db.Where("invoice_id = ?", invoice.ID).Order("created_at ASC").Find(&payments)
	splits := paymentSplitsFromRecords(payments)
	if len(splits) == 0 && invoice.AmountPaid > 0.009 {
		splits = []models.PaymentSplit{{
			Mode:   normalizePaymentMethod(invoice.PaymentMode),
			Amount: paymentSplitAmount(invoice.AmountPaid),
		}}
	}
	invoice.PaymentSplits = splits
}

func attachInvoicePaymentSplitsList(db *gorm.DB, invoices []models.Invoice) {
	if db == nil || len(invoices) == 0 {
		return
	}
	ids := make([]uuid.UUID, 0, len(invoices))
	for i := range invoices {
		ids = append(ids, invoices[i].ID)
	}
	var payments []models.Payment
	db.Where("invoice_id IN ?", ids).Order("created_at ASC").Find(&payments)
	byInvoice := make(map[uuid.UUID][]models.PaymentSplit, len(invoices))
	for _, payment := range payments {
		if payment.InvoiceID == nil {
			continue
		}
		amount := paymentSplitAmount(payment.AmountReceived - payment.PaymentInDiscount)
		if amount <= 0.009 {
			continue
		}
		byInvoice[*payment.InvoiceID] = append(byInvoice[*payment.InvoiceID], models.PaymentSplit{
			Mode:   normalizePaymentMethod(payment.Mode),
			Amount: amount,
		})
	}
	for i := range invoices {
		splits := mergePaymentSplits(byInvoice[invoices[i].ID])
		if len(splits) == 0 && invoices[i].AmountPaid > 0.009 {
			splits = []models.PaymentSplit{{
				Mode:   normalizePaymentMethod(invoices[i].PaymentMode),
				Amount: paymentSplitAmount(invoices[i].AmountPaid),
			}}
		}
		invoices[i].PaymentSplits = splits
	}
}

func paymentSplitsFromRecords(payments []models.Payment) []models.PaymentSplit {
	splits := make([]models.PaymentSplit, 0, len(payments))
	for _, payment := range payments {
		amount := paymentSplitAmount(payment.AmountReceived - payment.PaymentInDiscount)
		if amount <= 0.009 {
			continue
		}
		splits = append(splits, models.PaymentSplit{
			Mode:   normalizePaymentMethod(payment.Mode),
			Amount: amount,
		})
	}
	return mergePaymentSplits(splits)
}

func formatPaymentSplitsLabel(splits []models.PaymentSplit, fallbackMode string) string {
	cleaned := mergePaymentSplits(splits)
	if len(cleaned) == 0 {
		if strings.TrimSpace(fallbackMode) == "" {
			return ""
		}
		return paymentMethodLabel(fallbackMode)
	}
	if len(cleaned) == 1 {
		return paymentMethodLabel(cleaned[0].Mode)
	}
	parts := make([]string, 0, len(cleaned))
	for _, split := range cleaned {
		parts = append(parts, paymentMethodLabel(split.Mode))
	}
	return strings.Join(parts, " + ")
}

func copyPaymentSplits(splits []models.PaymentSplit) []models.PaymentSplit {
	if splits == nil {
		return nil
	}
	out := make([]models.PaymentSplit, len(splits))
	copy(out, splits)
	return out
}

// applyResolvedSplitsToBill mirrors applyResolvedSplitsToInvoice for purchase
// bills: PaidAmount becomes the resolved total, and the first split supplies
// the headline payment mode / account.
func applyResolvedSplitsToBill(bill *models.PurchaseBill, resolved []resolvedPaymentSplit) {
	if bill == nil {
		return
	}
	bill.PaymentSplits = modelSplitsFromResolved(resolved)
	var total float64
	for _, split := range resolved {
		total += split.Amount
	}
	bill.PaidAmount = paymentSplitAmount(total)
	if len(resolved) > 0 {
		bill.PaymentMode = resolved[0].Mode
		bill.BankAccountID = resolved[0].BankAccountID
		return
	}
	if strings.TrimSpace(bill.PaymentMode) == "" {
		bill.PaymentMode = "cash"
	}
}

// finalizePurchaseBillPaymentSplits mirrors finalizeInvoicePaymentSplits for
// purchase bills: PaidAmount is clamped to the bill total, explicit input
// splits replace the stored ones, and a "paid" bill always splits the full
// total. The resolved splits are stored on the bill (persisted JSON) so draft
// split configurations survive; createLinkedPurchasePaymentOut then writes
// one PaymentOut per split.
func finalizePurchaseBillPaymentSplits(userID uuid.UUID, bill *models.PurchaseBill, inputSplits []models.PaymentSplit, fallbackMode string, explicitAccount *uuid.UUID) error {
	if bill == nil {
		return nil
	}
	amount := bill.PaidAmount
	if bill.Status == "paid" {
		amount = bill.TotalAmount
		bill.PaidAmount = amount
	} else if amount > bill.TotalAmount {
		amount = bill.TotalAmount
		bill.PaidAmount = amount
	}
	if amount < 0 {
		amount = 0
		bill.PaidAmount = 0
	}

	splits := selectInvoicePaymentSplits(inputSplits, bill.PaymentSplits, amount)
	resolved, err := resolveInvoicePaymentSplits(userID, splits, fallbackMode, amount, explicitAccount)
	if err != nil {
		return err
	}

	var sum float64
	for _, split := range resolved {
		sum += split.Amount
	}
	if bill.Status == "paid" && bill.TotalAmount-sum > 0.009 {
		if len(resolved) == 0 {
			resolved, err = resolveInvoicePaymentSplits(userID, nil, fallbackMode, bill.TotalAmount, explicitAccount)
			if err != nil {
				return err
			}
		} else {
			resolved[len(resolved)-1].Amount = paymentSplitAmount(resolved[len(resolved)-1].Amount + (bill.TotalAmount - sum))
		}
	}

	applyResolvedSplitsToBill(bill, resolved)
	return nil
}

// paymentSplitsFromOutRecords builds the tender breakdown from a bill's
// linked PaymentOut rows, recovering each row's bank account from its linked
// cash/bank ledger transaction so the edit form can re-select the account.
func paymentSplitsFromOutRecords(db *gorm.DB, userID uuid.UUID, paymentOuts []models.PaymentOut) []models.PaymentSplit {
	splits := make([]models.PaymentSplit, 0, len(paymentOuts))
	for _, paymentOut := range paymentOuts {
		amount := paymentSplitAmount(paymentOut.AmountPaid - paymentOut.PaymentOutDiscount)
		if amount <= 0.009 {
			continue
		}
		var accountID *uuid.UUID
		if !isInitialInvestmentPayment(paymentOut.Mode) {
			txns, err := findPaymentCashTransactions(db, userID, "out", paymentOutCashRef(&paymentOut))
			if err == nil && len(txns) > 0 {
				accountID = txns[0].AccountID
			}
		}
		splits = append(splits, models.PaymentSplit{
			Mode:          normalizePaymentMethod(paymentOut.Mode),
			Amount:        amount,
			BankAccountID: accountID,
		})
	}
	return mergePaymentSplits(splits)
}

// attachPurchaseBillPaymentSplits populates bill.PaymentSplits for responses.
// Linked PaymentOut rows are the source of truth; the persisted column (draft
// bills record no payments) and finally payment_mode + paid_amount are
// fallbacks so older bills still rehydrate.
func attachPurchaseBillPaymentSplits(db *gorm.DB, bill *models.PurchaseBill) {
	if bill == nil || db == nil {
		return
	}
	var paymentOuts []models.PaymentOut
	db.Where("user_id = ? AND purchase_bill_id = ?", bill.UserID, bill.ID).
		Order("created_at ASC").Find(&paymentOuts)
	splits := paymentSplitsFromOutRecords(db, bill.UserID, paymentOuts)
	if len(splits) == 0 {
		splits = mergePaymentSplits(bill.PaymentSplits)
	}
	if len(splits) == 0 && bill.PaidAmount > 0.009 {
		splits = []models.PaymentSplit{{
			Mode:          normalizePaymentMethod(bill.PaymentMode),
			Amount:        paymentSplitAmount(bill.PaidAmount),
			BankAccountID: bill.BankAccountID,
		}}
	}
	bill.PaymentSplits = splits
}

func ledgerSplitsForInvoice(tx *gorm.DB, userID uuid.UUID, previous, current *models.Invoice) ([]resolvedPaymentSplit, error) {
	if current == nil {
		return nil, nil
	}
	if current.PaymentSplits != nil {
		splits := clampPaymentSplitsToTotal(mergePaymentSplits(current.PaymentSplits), current.AmountPaid)
		return resolveInvoicePaymentSplits(userID, splits, current.PaymentMode, current.AmountPaid)
	}

	var payments []models.Payment
	if err := tx.Where("user_id = ? AND invoice_id = ?", userID, current.ID).Order("created_at ASC").Find(&payments).Error; err != nil {
		return nil, err
	}
	existing := paymentSplitsFromRecords(payments)

	modeChanged := previous != nil && normalizePaymentMethod(previous.PaymentMode) != normalizePaymentMethod(current.PaymentMode)
	accountChanged := previous != nil && !bankAccountIDsEqual(previous.BankAccountID, current.BankAccountID)
	if modeChanged || accountChanged || len(existing) == 0 {
		return resolveInvoicePaymentSplits(userID, nil, current.PaymentMode, current.AmountPaid)
	}

	adjusted := adjustPaymentSplitsToAmount(existing, current.AmountPaid)
	return resolveInvoicePaymentSplits(userID, adjusted, current.PaymentMode, current.AmountPaid)
}
