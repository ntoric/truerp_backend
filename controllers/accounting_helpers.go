package controllers

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	acCodeCash     = "1100"
	acCodeBank     = "1200"
	acCodeAR       = "1300"
	acCodeAP       = "2100"
	acCodeEquity   = "3100"
	acCodeSales    = "4100"
	acCodePurchase = "5100"
	acCodeExpense  = "5200"
	acCodePayroll  = "5300"
)

type glLine struct {
	AccountCode string
	Debit       float64
	Credit      float64
	Description string
}

func EnsureDefaultChartOfAccounts(tx *gorm.DB, userID uuid.UUID) error {
	defaults := []models.Account{
		{UserID: userID, Code: acCodeCash, Name: "Cash in Hand", AccountType: "asset", SubType: "current_asset", IsDefault: true, IsActive: true},
		{UserID: userID, Code: acCodeBank, Name: "Bank", AccountType: "asset", SubType: "current_asset", IsDefault: true, IsActive: true},
		{UserID: userID, Code: acCodeAR, Name: "Accounts Receivable", AccountType: "asset", SubType: "current_asset", IsDefault: true, IsActive: true},
		{UserID: userID, Code: acCodeAP, Name: "Accounts Payable", AccountType: "liability", SubType: "current_liability", IsDefault: true, IsActive: true},
		{UserID: userID, Code: acCodeEquity, Name: "Owner's Equity", AccountType: "equity", SubType: "equity", IsDefault: true, IsActive: true},
		{UserID: userID, Code: acCodeSales, Name: "Sales", AccountType: "income", IsDefault: true, IsActive: true},
		{UserID: userID, Code: acCodePurchase, Name: "Purchases", AccountType: "expense", IsDefault: true, IsActive: true},
		{UserID: userID, Code: acCodeExpense, Name: "General Expenses", AccountType: "expense", IsDefault: true, IsActive: true},
		{UserID: userID, Code: acCodePayroll, Name: "Payroll", AccountType: "expense", IsDefault: true, IsActive: true},
	}
	for i := range defaults {
		var existing models.Account
		err := tx.Where("user_id = ? AND code = ?", userID, defaults[i].Code).First(&existing).Error
		if err == nil {
			continue
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		defaults[i].ID = uuid.New()
		if err := tx.Create(&defaults[i]).Error; err != nil {
			return err
		}
	}
	return nil
}

func accountIDByCode(tx *gorm.DB, userID uuid.UUID, code string) (uuid.UUID, error) {
	var account models.Account
	if err := tx.Where("user_id = ? AND code = ?", userID, code).First(&account).Error; err != nil {
		return uuid.Nil, err
	}
	return account.ID, nil
}

func glAssetAccountForBank(tx *gorm.DB, userID uuid.UUID, bankAccountID *uuid.UUID) (uuid.UUID, error) {
	if bankAccountID != nil {
		return accountIDByCode(tx, userID, acCodeBank)
	}
	return accountIDByCode(tx, userID, acCodeCash)
}

func glAssetAccountForPaymentMode(tx *gorm.DB, userID uuid.UUID, mode string) (uuid.UUID, error) {
	bankID, err := resolveBankAccountForPaymentMode(userID, mode, nil)
	if err != nil {
		return uuid.Nil, err
	}
	return glAssetAccountForBank(tx, userID, bankID)
}

func accountingRefExists(tx *gorm.DB, userID uuid.UUID, refType string, refID uuid.UUID) bool {
	var count int64
	tx.Model(&models.Ledger{}).
		Where("user_id = ? AND transaction_type = ? AND reference_id = ?", userID, refType, refID).
		Count(&count)
	return count > 0
}

func applyAccountBalance(account *models.Account, debit, credit float64) float64 {
	switch account.AccountType {
	case "asset", "expense":
		return account.Balance + debit - credit
	case "liability", "income", "equity":
		return account.Balance + credit - debit
	default:
		return account.Balance + debit - credit
	}
}

func nextAccountCode(tx *gorm.DB, userID uuid.UUID, accountType string) string {
	bases := map[string]int{
		"asset": 1000, "liability": 2000, "equity": 3000, "income": 4000, "expense": 5000,
	}
	base := bases[accountType]
	if base == 0 {
		base = 9000
	}

	var accounts []models.Account
	tx.Where("user_id = ? AND account_type = ?", userID, accountType).Find(&accounts)

	maxNum := base
	for _, a := range accounts {
		var n int
		if _, err := fmt.Sscanf(a.Code, "%d", &n); err == nil && n > maxNum {
			maxNum = n
		}
	}
	if maxNum == base && len(accounts) == 0 {
		return fmt.Sprintf("%d", base+100)
	}
	return fmt.Sprintf("%d", maxNum+100)
}

func applyJournalLinesToAccountsAndLedger(tx *gorm.DB, userID uuid.UUID, entryDate time.Time, refType string, refID uuid.UUID, refNumber, entryDescription string, lines []models.JournalEntryLine) error {
	for _, line := range lines {
		var account models.Account
		if err := tx.Where("user_id = ? AND id = ?", userID, line.AccountID).First(&account).Error; err != nil {
			return err
		}
		newBalance := applyAccountBalance(&account, line.Debit, line.Credit)
		if err := tx.Model(&account).Update("balance", newBalance).Error; err != nil {
			return err
		}
		desc := line.Description
		if desc == "" {
			desc = entryDescription
		}
		if err := tx.Create(&models.Ledger{
			ID:              uuid.New(),
			UserID:          userID,
			AccountID:       line.AccountID,
			TransactionDate: entryDate,
			TransactionType: refType,
			ReferenceID:     refID,
			ReferenceNumber: refNumber,
			Description:     desc,
			Debit:           line.Debit,
			Credit:          line.Credit,
			Balance:         newBalance,
		}).Error; err != nil {
			return err
		}
	}
	return nil
}

func postAutoJournal(tx *gorm.DB, userID uuid.UUID, entryDate time.Time, description, refType string, refID uuid.UUID, refNumber string, lines []glLine) error {
	if len(lines) < 2 {
		return nil
	}
	if err := EnsureDefaultChartOfAccounts(tx, userID); err != nil {
		return err
	}
	if accountingRefExists(tx, userID, refType, refID) {
		return nil
	}

	var totalDebit, totalCredit float64
	for _, line := range lines {
		totalDebit += line.Debit
		totalCredit += line.Credit
	}
	if totalDebit != totalCredit || totalDebit == 0 {
		return fmt.Errorf("journal lines must balance with non-zero total")
	}

	var entryCount int64
	tx.Model(&models.JournalEntry{}).Where("user_id = ?", userID).Count(&entryCount)

	entry := models.JournalEntry{
		ID:          uuid.New(),
		UserID:      userID,
		EntryNumber: fmt.Sprintf("JE-%04d", entryCount+1),
		EntryDate:   entryDate,
		Description: description,
		TotalDebit:  totalDebit,
		TotalCredit: totalCredit,
		Status:      "posted",
	}

	for _, line := range lines {
		accountID, err := accountIDByCode(tx, userID, line.AccountCode)
		if err != nil {
			return err
		}
		entry.Lines = append(entry.Lines, models.JournalEntryLine{
			ID:          uuid.New(),
			EntryID:     entry.ID,
			AccountID:   accountID,
			Debit:       line.Debit,
			Credit:      line.Credit,
			Description: line.Description,
		})
	}

	if err := tx.Create(&entry).Error; err != nil {
		return err
	}

	return applyJournalLinesToAccountsAndLedger(tx, userID, entryDate, refType, refID, refNumber, description, entry.Lines)
}

func postInvoiceAccounting(tx *gorm.DB, userID uuid.UUID, invoice *models.Invoice) error {
	if invoice.TotalAmount <= 0 {
		return nil
	}
	desc := fmt.Sprintf("Sales invoice %s", invoice.InvoiceNumber)
	return postAutoJournal(tx, userID, invoice.Date, desc, "invoice", invoice.ID, invoice.InvoiceNumber, []glLine{
		{AccountCode: acCodeAR, Debit: invoice.TotalAmount, Description: desc},
		{AccountCode: acCodeSales, Credit: invoice.TotalAmount, Description: desc},
	})
}

func postSalePaymentAccounting(tx *gorm.DB, userID uuid.UUID, refID uuid.UUID, bankAccountID *uuid.UUID, amount float64, date time.Time, refNumber, description string) error {
	if amount <= 0 {
		return nil
	}
	assetCode := acCodeCash
	if bankAccountID != nil {
		assetCode = acCodeBank
	}
	return postAutoJournal(tx, userID, date, description, "payment_in", refID, refNumber, []glLine{
		{AccountCode: assetCode, Debit: amount, Description: description},
		{AccountCode: acCodeAR, Credit: amount, Description: description},
	})
}

func postPurchaseBillAccounting(tx *gorm.DB, userID uuid.UUID, bill *models.PurchaseBill) error {
	if bill.TotalAmount <= 0 {
		return nil
	}
	desc := fmt.Sprintf("Purchase bill %s", bill.BillNumber)
	return postAutoJournal(tx, userID, bill.BillDate, desc, "purchase_bill", bill.ID, bill.BillNumber, []glLine{
		{AccountCode: acCodePurchase, Debit: bill.TotalAmount, Description: desc},
		{AccountCode: acCodeAP, Credit: bill.TotalAmount, Description: desc},
	})
}

func postPurchasePaymentAccounting(tx *gorm.DB, userID uuid.UUID, refID uuid.UUID, bankAccountID *uuid.UUID, amount float64, date time.Time, refNumber, description string) error {
	if amount <= 0 {
		return nil
	}
	assetCode := acCodeCash
	if bankAccountID != nil {
		assetCode = acCodeBank
	}
	return postAutoJournal(tx, userID, date, description, "payment_out", refID, refNumber, []glLine{
		{AccountCode: acCodeAP, Debit: amount, Description: description},
		{AccountCode: assetCode, Credit: amount, Description: description},
	})
}

// expenseAccountCodeForCategory resolves the GL expense account for an expense category.
// General maps to the default General Expenses account; Payroll to Payroll; other categories
// get a dedicated expense account named after the category.
func expenseAccountCodeForCategory(tx *gorm.DB, userID uuid.UUID, category string) (string, error) {
	category = utils.ResolveCategoryName(category)
	switch strings.EqualFold(category, utils.DefaultCategoryName) {
	case true:
		return acCodeExpense, nil
	}
	if strings.EqualFold(category, "Payroll") {
		return acCodePayroll, nil
	}

	var account models.Account
	err := tx.Where("user_id = ? AND account_type = ? AND name = ? AND is_active = ?", userID, "expense", category, true).
		First(&account).Error
	if err == nil {
		return account.Code, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", err
	}

	if err := EnsureDefaultChartOfAccounts(tx, userID); err != nil {
		return "", err
	}
	code := nextAccountCode(tx, userID, "expense")
	account = models.Account{
		ID:          uuid.New(),
		UserID:      userID,
		Code:        code,
		Name:        category,
		AccountType: "expense",
		IsActive:    true,
	}
	if err := tx.Create(&account).Error; err != nil {
		return "", err
	}
	return code, nil
}

func postExpenseAccounting(tx *gorm.DB, userID uuid.UUID, expense *models.Expense) error {
	if expense.Amount <= 0 {
		return nil
	}
	expenseAccountCode, err := expenseAccountCodeForCategory(tx, userID, expense.Category)
	if err != nil {
		return err
	}
	desc := fmt.Sprintf("Expense %s", expense.ExpenseNumber)
	assetCode := acCodeCash
	if expense.BankAccountID != nil {
		assetCode = acCodeBank
	} else if bankID, err := resolveBankAccountForPaymentMode(userID, expense.PaymentMode, nil); err == nil && bankID != nil {
		assetCode = acCodeBank
	}
	return postAutoJournal(tx, userID, expense.Date, desc, "expense", expense.ID, expense.ExpenseNumber, []glLine{
		{AccountCode: expenseAccountCode, Debit: expense.Amount, Description: desc},
		{AccountCode: assetCode, Credit: expense.Amount, Description: desc},
	})
}

// postPayrollSalaryAccounting posts payroll expense against cash/bank, keyed by payroll ID
// so reverse/re-apply is idempotent for the general ledger.
func postPayrollSalaryAccounting(tx *gorm.DB, userID uuid.UUID, payroll *models.Payroll, expense *models.Expense) error {
	if payroll.NetSalary <= 0 {
		return nil
	}
	desc := expense.Description
	if desc == "" {
		desc = fmt.Sprintf("Payroll %s", payroll.PaymentNumber)
	}
	assetCode := acCodeCash
	if payroll.BankAccountID != nil {
		assetCode = acCodeBank
	}
	return postAutoJournal(tx, userID, payroll.PaymentDate, desc, "payroll", payroll.ID, payroll.PaymentNumber, []glLine{
		{AccountCode: acCodePayroll, Debit: payroll.NetSalary, Description: desc},
		{AccountCode: assetCode, Credit: payroll.NetSalary, Description: desc},
	})
}

func postStandalonePaymentInAccounting(tx *gorm.DB, userID uuid.UUID, payment *models.Payment, netAmount float64) error {
	if netAmount <= 0 {
		return nil
	}
	desc := fmt.Sprintf("Payment in %s", payment.PaymentInNumber)
	assetCode := acCodeCash
	if id, err := glAssetAccountForPaymentMode(tx, userID, payment.Mode); err == nil {
		var account models.Account
		if tx.Where("id = ?", id).First(&account).Error == nil && account.Code == acCodeBank {
			assetCode = acCodeBank
		}
	}
	refNumber := payment.PaymentInNumber
	if refNumber == "" {
		refNumber = payment.ID.String()
	}
	return postAutoJournal(tx, userID, payment.Date, desc, "payment_in_record", payment.ID, refNumber, []glLine{
		{AccountCode: assetCode, Debit: netAmount, Description: desc},
		{AccountCode: acCodeAR, Credit: netAmount, Description: desc},
	})
}

func postStandalonePaymentOutAccounting(tx *gorm.DB, userID uuid.UUID, payment *models.PaymentOut, netAmount float64) error {
	if netAmount <= 0 {
		return nil
	}
	desc := fmt.Sprintf("Payment out %s", payment.PaymentOutNumber)
	assetCode := acCodeCash
	if id, err := glAssetAccountForPaymentMode(tx, userID, payment.Mode); err == nil {
		var account models.Account
		if tx.Where("id = ?", id).First(&account).Error == nil && account.Code == acCodeBank {
			assetCode = acCodeBank
		}
	}
	refNumber := payment.PaymentOutNumber
	if refNumber == "" {
		refNumber = payment.ID.String()
	}
	return postAutoJournal(tx, userID, payment.Date, desc, "payment_out_record", payment.ID, refNumber, []glLine{
		{AccountCode: acCodeAP, Debit: netAmount, Description: desc},
		{AccountCode: assetCode, Credit: netAmount, Description: desc},
	})
}

func postManualCashAddAccounting(tx *gorm.DB, userID uuid.UUID, txn *models.CashTransaction) error {
	if txn.Amount <= 0 {
		return nil
	}
	assetCode := acCodeCash
	if txn.AccountID != nil {
		assetCode = acCodeBank
	}
	desc := txn.Description
	if desc == "" {
		desc = "Cash added"
	}
	return postAutoJournal(tx, userID, txn.Date, desc, "cash_add", txn.ID, txn.Reference, []glLine{
		{AccountCode: assetCode, Debit: txn.Amount, Description: desc},
		{AccountCode: acCodeEquity, Credit: txn.Amount, Description: desc},
	})
}

func postManualCashReduceAccounting(tx *gorm.DB, userID uuid.UUID, txn *models.CashTransaction) error {
	if txn.Amount <= 0 {
		return nil
	}
	assetCode := acCodeCash
	if txn.AccountID != nil {
		assetCode = acCodeBank
	}
	desc := txn.Description
	if desc == "" {
		desc = "Cash reduced"
	}
	return postAutoJournal(tx, userID, txn.Date, desc, "cash_reduce", txn.ID, txn.Reference, []glLine{
		{AccountCode: acCodeExpense, Debit: txn.Amount, Description: desc},
		{AccountCode: assetCode, Credit: txn.Amount, Description: desc},
	})
}
