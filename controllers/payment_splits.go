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
	order := make([]string, 0, len(splits))
	byMode := make(map[string]float64, len(splits))
	for _, split := range splits {
		mode := normalizePaymentMethod(split.Mode)
		amount := paymentSplitAmount(split.Amount)
		if amount <= 0.009 {
			continue
		}
		if _, exists := byMode[mode]; !exists {
			order = append(order, mode)
		}
		byMode[mode] += amount
	}
	out := make([]models.PaymentSplit, 0, len(order))
	for _, mode := range order {
		out = append(out, models.PaymentSplit{
			Mode:   mode,
			Amount: paymentSplitAmount(byMode[mode]),
		})
	}
	return out
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
		out = append(out, models.PaymentSplit{Mode: split.Mode, Amount: amount})
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
		out = append(out, models.PaymentSplit{Mode: split.Mode, Amount: remaining})
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
		out = append(out, models.PaymentSplit{Mode: split.Mode, Amount: split.Amount})
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
		accountID := explicit
		if accountID == nil || len(merged) > 1 {
			resolvedID, err := resolveBankAccountForPaymentMode(userID, split.Mode, nil)
			if err != nil {
				return nil, err
			}
			accountID = resolvedID
		} else if err := validateUserBankAccount(userID, accountID); err != nil {
			return nil, err
		}
		out = append(out, resolvedPaymentSplit{
			Mode:          split.Mode,
			Amount:        split.Amount,
			BankAccountID: accountID,
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
