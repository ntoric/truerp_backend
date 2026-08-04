package controllers

import (
	"truerp/models"
	"truerp/utils"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func GetAccounts(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	if err := EnsureDefaultChartOfAccounts(utils.DB, userID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to initialize accounts"})
		return
	}

	var accounts []models.Account
	query := utils.DB.Where("user_id = ?", userID)

	if accountType := c.Query("account_type"); accountType != "" {
		query = query.Where("account_type = ?", accountType)
	}

	if isGroup := c.Query("is_group"); isGroup != "" {
		query = query.Where("parent_id IS NULL")
	}

	if err := query.Order("account_type, name").Find(&accounts).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch accounts"})
		return
	}

	c.JSON(http.StatusOK, accounts)
}

func GetAccount(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var account models.Account
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Preload("Parent").First(&account).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Account not found"})
		return
	}

	c.JSON(http.StatusOK, account)
}

func CreateAccount(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		Name           string     `json:"name" binding:"required"`
		AccountType    string     `json:"account_type" binding:"required,oneof=asset liability equity income expense"`
		ParentID       *uuid.UUID `json:"parent_id"`
		OpeningBalance float64    `json:"opening_balance"`
		IsDefault      bool       `json:"is_default"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	account := models.Account{
		ID:             uuid.New(),
		UserID:         userID,
		Code:           nextAccountCode(utils.DB, userID, input.AccountType),
		Name:           input.Name,
		AccountType:    input.AccountType,
		ParentID:       input.ParentID,
		OpeningBalance: input.OpeningBalance,
		Balance:        input.OpeningBalance,
		IsDefault:      input.IsDefault,
		IsActive:       true,
	}

	if err := utils.DB.Create(&account).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create account"})
		return
	}

	c.JSON(http.StatusCreated, account)
}

func UpdateAccount(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var input struct {
		Name          string     `json:"name"`
		ParentID      *uuid.UUID `json:"parent_id"`
		IsActive      bool       `json:"is_active"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var account models.Account
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&account).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Account not found"})
		return
	}

	updates := map[string]interface{}{
		"name":       input.Name,
		"parent_id":  input.ParentID,
		"is_active":  input.IsActive,
	}

	if err := utils.DB.Model(&account).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update account"})
		return
	}

	c.JSON(http.StatusOK, account)
}

func DeleteAccount(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var account models.Account
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&account).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Account not found"})
		return
	}

	if account.IsDefault {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot delete default system account"})
		return
	}

	if account.Balance != 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot delete account with non-zero balance"})
		return
	}

	utils.DB.Delete(&account)
	c.JSON(http.StatusOK, gin.H{"message": "Account deleted"})
}

func GetJournalEntries(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var entries []models.JournalEntry
	query := utils.DB.Where("user_id = ?", userID).Preload("Lines").Preload("Lines.Account")

	if status := c.Query("status"); status != "" {
		query = query.Where("status = ?", status)
	}
	if fromDate := c.Query("from_date"); fromDate != "" {
		query = query.Where("entry_date >= ?", fromDate)
	}
	if toDate := c.Query("to_date"); toDate != "" {
		query = query.Where("entry_date <= ?", toDate)
	}

	if err := query.Order("entry_date DESC").Find(&entries).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch entries"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": entries})
}

func GetJournalEntry(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var entry models.JournalEntry
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Preload("Lines").Preload("Lines.Account").First(&entry).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Entry not found"})
		return
	}

	c.JSON(http.StatusOK, entry)
}

func CreateJournalEntry(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		EntryDate   time.Time `json:"entry_date" binding:"required"`
		Description string    `json:"description"`
		Lines       []struct {
			AccountID   uuid.UUID `json:"account_id" binding:"required"`
			Debit       float64   `json:"debit"`
			Credit      float64   `json:"credit"`
			Description string    `json:"description"`
		} `json:"lines" binding:"required,min=2"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var totalDebit, totalCredit float64
	for _, line := range input.Lines {
		totalDebit += line.Debit
		totalCredit += line.Credit
	}

	if totalDebit != totalCredit {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Debit and credit must be equal"})
		return
	}

	var count int64
	utils.DB.Model(&models.JournalEntry{}).Where("user_id = ?", userID).Count(&count)

	entry := models.JournalEntry{
		ID:            uuid.New(),
		UserID:        userID,
		EntryNumber:   fmt.Sprintf("JE-%04d", count+1),
		EntryDate:     input.EntryDate,
		Description:   input.Description,
		TotalDebit:    totalDebit,
		TotalCredit:   totalCredit,
		Status:        "draft",
	}

	for _, line := range input.Lines {
		entry.Lines = append(entry.Lines, models.JournalEntryLine{
			ID:          uuid.New(),
			EntryID:     entry.ID,
			AccountID:   line.AccountID,
			Debit:       line.Debit,
			Credit:      line.Credit,
			Description: line.Description,
		})
	}

	if err := utils.DB.Create(&entry).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create entry"})
		return
	}

	c.JSON(http.StatusCreated, entry)
}

func UpdateJournalEntry(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var entry models.JournalEntry
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&entry).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Entry not found"})
		return
	}

	if entry.Status != "draft" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot edit posted entry"})
		return
	}

	var input struct {
		Description string `json:"description"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	utils.DB.Model(&entry).Update("description", input.Description)

	c.JSON(http.StatusOK, entry)
}

func PostJournalEntry(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var entry models.JournalEntry
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Preload("Lines").First(&entry).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Entry not found"})
		return
	}

	if entry.Status == "posted" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Entry already posted"})
		return
	}

	if err := utils.DB.Transaction(func(tx *gorm.DB) error {
		entry.Status = "posted"
		if err := tx.Save(&entry).Error; err != nil {
			return err
		}
		return applyJournalLinesToAccountsAndLedger(tx, userID, entry.EntryDate, "journal_entry", entry.ID, entry.EntryNumber, entry.Description, entry.Lines)
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to post entry"})
		return
	}

	c.JSON(http.StatusOK, entry)
}

func DeleteJournalEntry(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var entry models.JournalEntry
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&entry).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Entry not found"})
		return
	}

	if entry.Status != "draft" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot delete posted entry"})
		return
	}

	utils.DB.Delete(&entry)
	c.JSON(http.StatusOK, gin.H{"message": "Entry deleted"})
}

func GetTrialBalance(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	asOfDate := c.Query("as_of_date")

	var accounts []models.Account
	query := utils.DB.Where("user_id = ? AND is_active = ?", userID, true)

	if err := query.Order("code ASC").Find(&accounts).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch accounts"})
		return
	}

	type TrialBalanceItem struct {
		AccountID   uuid.UUID `json:"account_id"`
		AccountCode string    `json:"account_code"`
		AccountName string    `json:"account_name"`
		AccountType string    `json:"account_type"`
		Debit       float64   `json:"debit"`
		Credit      float64   `json:"credit"`
	}

	var items []TrialBalanceItem
	var totalDebit, totalCredit float64

	for _, account := range accounts {
		balance := account.Balance
		var debit, credit float64

		switch account.AccountType {
		case "asset", "expense":
			if balance > 0 {
				debit = balance
			} else {
				credit = -balance
			}
		case "liability", "equity", "income":
			if balance > 0 {
				credit = balance
			} else {
				debit = -balance
			}
		}

		if debit > 0 || credit > 0 {
			items = append(items, TrialBalanceItem{
				AccountID:   account.ID,
				AccountCode: account.Code,
				AccountName: account.Name,
				AccountType: account.AccountType,
				Debit:       debit,
				Credit:      credit,
			})
			totalDebit += debit
			totalCredit += credit
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"as_of_date":   asOfDate,
		"items":       items,
		"total_debit":  totalDebit,
		"total_credit": totalCredit,
		"is_balanced":  totalDebit == totalCredit,
	})
}

func GetProfitLoss(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	fromDate := c.Query("from_date")
	toDate := c.Query("to_date")

	var accounts []models.Account
	query := utils.DB.Where("user_id = ? AND is_active = ? AND account_type IN ('income', 'expense')", userID, true)

	if err := query.Order("account_type, code ASC").Find(&accounts).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch accounts"})
		return
	}

	type PLItem struct {
		AccountID   uuid.UUID `json:"account_id"`
		AccountCode string    `json:"account_code"`
		AccountName string    `json:"account_name"`
		AccountType string    `json:"account_type"`
		Amount      float64   `json:"amount"`
	}

	var incomeItems, expenseItems []PLItem
	var totalIncome, totalExpense float64

	for _, account := range accounts {
		amount := account.Balance
		if amount == 0 {
			continue
		}

		item := PLItem{
			AccountID:   account.ID,
			AccountCode: account.Code,
			AccountName: account.Name,
			AccountType: account.AccountType,
			Amount:      amount,
		}

		if account.AccountType == "income" {
			incomeItems = append(incomeItems, item)
			totalIncome += amount
		} else {
			expenseItems = append(expenseItems, item)
			totalExpense += amount
		}
	}

	netProfit := totalIncome - totalExpense

	c.JSON(http.StatusOK, gin.H{
		"from_date":    fromDate,
		"to_date":      toDate,
		"income":       incomeItems,
		"total_income": totalIncome,
		"expenses":     expenseItems,
		"total_expense": totalExpense,
		"net_profit":   netProfit,
	})
}

func GetBalanceSheet(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	asOfDate := c.Query("as_of_date")

	var accounts []models.Account
	query := utils.DB.Where("user_id = ? AND is_active = ? AND account_type IN ('asset', 'liability', 'equity')", userID, true)

	if err := query.Order("account_type, code ASC").Find(&accounts).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch accounts"})
		return
	}

	type BSItem struct {
		AccountID   uuid.UUID `json:"account_id"`
		AccountCode string    `json:"account_code"`
		AccountName string    `json:"account_name"`
		AccountType string    `json:"account_type"`
		SubType     string    `json:"sub_type"`
		Amount      float64   `json:"amount"`
	}

	var assets, liabilities, equity []BSItem
	var totalAssets, totalLiabilities, totalEquity float64

	for _, account := range accounts {
		amount := account.Balance
		if amount == 0 {
			continue
		}

		item := BSItem{
			AccountID:   account.ID,
			AccountCode: account.Code,
			AccountName: account.Name,
			AccountType: account.AccountType,
			SubType:     account.SubType,
			Amount:      amount,
		}

		switch account.AccountType {
		case "asset":
			assets = append(assets, item)
			totalAssets += amount
		case "liability":
			liabilities = append(liabilities, item)
			totalLiabilities += amount
		case "equity":
			equity = append(equity, item)
			totalEquity += amount
		}
	}

	var plAccounts []models.Account
	if err := utils.DB.Where("user_id = ? AND is_active = ? AND account_type IN ('income', 'expense')", userID, true).Find(&plAccounts).Error; err == nil {
		var totalIncome, totalExpense float64
		for _, a := range plAccounts {
			if a.AccountType == "income" {
				totalIncome += a.Balance
			} else {
				totalExpense += a.Balance
			}
		}
		netProfit := totalIncome - totalExpense
		if netProfit != 0 {
			equity = append(equity, BSItem{
				AccountName: "Current period profit",
				AccountType: "equity",
				Amount:      netProfit,
			})
			totalEquity += netProfit
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"as_of_date":          asOfDate,
		"assets":              assets,
		"total_assets":        totalAssets,
		"liabilities":         liabilities,
		"total_liabilities":   totalLiabilities,
		"equity":             equity,
		"total_equity":       totalEquity,
		"total_liabilities_equity": totalLiabilities + totalEquity,
		"is_balanced":         totalAssets == (totalLiabilities + totalEquity),
	})
}

func GetGeneralLedger(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	accountID := c.Param("id")
	fromDate := c.Query("from_date")
	toDate := c.Query("to_date")

	var account models.Account
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, accountID).First(&account).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Account not found"})
		return
	}

	var ledgerEntries []models.Ledger
	query := utils.DB.Where("user_id = ? AND account_id = ?", userID, accountID)

	if fromDate != "" {
		query = query.Where("transaction_date >= ?", fromDate)
	}
	if toDate != "" {
		query = query.Where("transaction_date <= ?", toDate)
	}

	if err := query.Order("transaction_date ASC").Find(&ledgerEntries).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch ledger entries"})
		return
	}

	// Calculate opening balance
	var openingBalance float64
	if fromDate != "" {
		var lastLedger models.Ledger
		err := utils.DB.Where("user_id = ? AND account_id = ? AND transaction_date < ?", userID, accountID, fromDate).
			Order("transaction_date DESC, created_at DESC").
			First(&lastLedger).Error
		if err == nil {
			openingBalance = lastLedger.Balance
		} else {
			openingBalance = account.OpeningBalance
		}
	} else {
		openingBalance = account.OpeningBalance
	}

	c.JSON(http.StatusOK, gin.H{
		"account":          account,
		"opening_balance":  openingBalance,
		"entries":          ledgerEntries,
		"closing_balance":  account.Balance,
	})
}

func GetLedgers(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	query := utils.DB.Where("user_id = ?", userID).Preload("Account")

	if accountID := c.Query("account_id"); accountID != "" {
		query = query.Where("account_id = ?", accountID)
	}
	if fromDate := c.Query("from_date"); fromDate != "" {
		query = query.Where("transaction_date >= ?", fromDate)
	}
	if toDate := c.Query("to_date"); toDate != "" {
		query = query.Where("transaction_date <= ?", toDate)
	}
	if txnType := c.Query("transaction_type"); txnType != "" {
		query = query.Where("transaction_type = ?", txnType)
	}

	var entries []models.Ledger
	if err := query.Order("transaction_date DESC, created_at DESC").Limit(500).Find(&entries).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch ledger entries"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": entries})
}

func CreateBankReconciliation(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		BankAccountID     uuid.UUID  `json:"bank_account_id" binding:"required"`
		StatementDate     time.Time  `json:"statement_date" binding:"required"`
		StatementBalance  float64    `json:"statement_balance" binding:"required"`
		ReconciledItems  []string   `json:"reconciled_items"`
		UnreconciledItems []string  `json:"unreconciled_items"`
		Notes             string     `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Get book balance from operational bank account
	var bankAccount models.BankAccount
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, input.BankAccountID).First(&bankAccount).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Bank account not found"})
		return
	}
	bookBalance := bankAccount.Balance

	difference := input.StatementBalance - bookBalance

	reconciliation := models.BankReconciliation{
		ID:                uuid.New(),
		UserID:            userID,
		BankAccountID:     input.BankAccountID,
		StatementDate:     input.StatementDate,
		StatementBalance:  input.StatementBalance,
		BookBalance:       bookBalance,
		Difference:        difference,
		ReconciledItems:   fmt.Sprintf("%v", input.ReconciledItems),
		UnreconciledItems: fmt.Sprintf("%v", input.UnreconciledItems),
		Notes:             input.Notes,
		Status:            "draft",
	}

	if err := utils.DB.Create(&reconciliation).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create reconciliation"})
		return
	}

	c.JSON(http.StatusCreated, reconciliation)
}

func GetBankReconciliations(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var reconciliations []models.BankReconciliation
	if err := utils.DB.Where("user_id = ?", userID).Order("statement_date DESC").Find(&reconciliations).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch reconciliations"})
		return
	}

	c.JSON(http.StatusOK, reconciliations)
}

func CompleteBankReconciliation(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var reconciliation models.BankReconciliation
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&reconciliation).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Reconciliation not found"})
		return
	}

	now := time.Now()
	reconciliation.Status = "reconciled"
	reconciliation.ReconciledAt = &now

	if err := utils.DB.Save(&reconciliation).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update reconciliation"})
		return
	}

	c.JSON(http.StatusOK, reconciliation)
}
