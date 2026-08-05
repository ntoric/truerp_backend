package controllers

import (
	"truerp/models"
	"truerp/utils"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Bank Account Controllers

func GetBankAccounts(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var accounts []models.BankAccount
	if err := utils.DB.Where("user_id = ?", userID).Order("is_primary DESC, created_at DESC").Find(&accounts).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch bank accounts"})
		return
	}

	c.JSON(http.StatusOK, accounts)
}

func GetBankAccount(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var account models.BankAccount
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&account).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Bank account not found"})
		return
	}

	c.JSON(http.StatusOK, account)
}

func CreateBankAccount(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		AccountName     string  `json:"account_name" binding:"required"`
		AccountNumber   string  `json:"account_number" binding:"required"`
		BankName        string  `json:"bank_name" binding:"required"`
		IFSCCode        string  `json:"ifsc_code"`
		AccountType     string  `json:"account_type"`
		OpeningBalance float64 `json:"opening_balance"`
		Notes           string  `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	account := models.BankAccount{
		ID:              uuid.New(),
		UserID:          userID,
		AccountName:     input.AccountName,
		AccountNumber:   input.AccountNumber,
		BankName:        input.BankName,
		IFSCCode:        input.IFSCCode,
		AccountType:     input.AccountType,
		OpeningBalance: input.OpeningBalance,
		Balance:         input.OpeningBalance,
		IsActive:        true,
		Notes:           input.Notes,
	}

	if err := utils.DB.Create(&account).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create bank account"})
		return
	}

	var accountCount int64
	utils.DB.Model(&models.BankAccount{}).Where("user_id = ?", userID).Count(&accountCount)
	if accountCount == 1 {
		utils.DB.Model(&account).Update("is_primary", true)
		account.IsPrimary = true
	}

	c.JSON(http.StatusCreated, account)
}

func UpdateBankAccount(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var input struct {
		AccountName     string  `json:"account_name"`
		AccountNumber   string  `json:"account_number"`
		BankName        string  `json:"bank_name"`
		IFSCCode        string  `json:"ifsc_code"`
		AccountType     string  `json:"account_type"`
		IsActive        bool    `json:"is_active"`
		Notes           string  `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var account models.BankAccount
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&account).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Bank account not found"})
		return
	}

	updates := map[string]interface{}{
		"account_name":   input.AccountName,
		"account_number": input.AccountNumber,
		"bank_name":      input.BankName,
		"ifsc_code":      input.IFSCCode,
		"account_type":   input.AccountType,
		"is_active":      input.IsActive,
		"notes":          input.Notes,
	}

	if err := utils.DB.Model(&account).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update bank account"})
		return
	}

	c.JSON(http.StatusOK, account)
}

func DeleteBankAccount(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var account models.BankAccount
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&account).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Bank account not found"})
		return
	}

	if err := utils.DB.Delete(&account).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete bank account"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Bank account deleted successfully"})
}

func SetPrimaryBankAccount(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var account models.BankAccount
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&account).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Bank account not found"})
		return
	}

	tx := utils.DB.Begin()
	if err := tx.Model(&models.BankAccount{}).Where("user_id = ?", userID).Update("is_primary", false).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update primary account"})
		return
	}
	if err := tx.Model(&account).Updates(map[string]interface{}{
		"is_primary": true,
		"is_active":  true,
	}).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to set primary account"})
		return
	}
	tx.Commit()

	account.IsPrimary = true
	account.IsActive = true
	c.JSON(http.StatusOK, account)
}

// Cash Transaction Controllers

func GetCashTransactions(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var transactions []models.CashTransaction
	query := utils.DB.Where("user_id = ?", userID).Preload("Account")

	// Filter by account
	if accountID := c.Query("account_id"); accountID != "" {
		query = query.Where("account_id = ?", accountID)
	}

	// Filter by transaction type
	if transType := c.Query("transaction_type"); transType != "" {
		query = query.Where("transaction_type = ?", transType)
	}

	// Filter by unlinked transactions
	if unlinked := c.Query("unlinked"); unlinked == "true" {
		query = query.Where("is_linked = ?", false)
	}

	// Period filter
	if startDate := c.Query("start_date"); startDate != "" {
		if parsedDate, err := time.Parse("2006-01-02", startDate); err == nil {
			query = query.Where("date >= ?", parsedDate)
		}
	}
	if endDate := c.Query("end_date"); endDate != "" {
		if parsedDate, err := time.Parse("2006-01-02", endDate); err == nil {
			query = query.Where("date <= ?", parsedDate)
		}
	}

	if err := query.Order("date DESC, created_at DESC").Find(&transactions).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch cash transactions"})
		return
	}

	c.JSON(http.StatusOK, transactions)
}

func GetCashTransaction(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var transaction models.CashTransaction
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Preload("Account").First(&transaction).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Cash transaction not found"})
		return
	}

	c.JSON(http.StatusOK, transaction)
}

func AddMoney(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		AccountID   *uuid.UUID `json:"account_id"`
		Amount      float64    `json:"amount" binding:"required,gt=0"`
		Date        time.Time  `json:"date" binding:"required"`
		Description string     `json:"description"`
		Reference   string     `json:"reference"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	transaction := models.CashTransaction{
		ID:              uuid.New(),
		UserID:          userID,
		AccountID:       input.AccountID,
		TransactionType: "add",
		Amount:          input.Amount,
		Date:            input.Date,
		Description:     input.Description,
		Reference:       input.Reference,
		IsLinked:        false,
	}

	// Update account balance if account is specified
	if input.AccountID != nil {
		var account models.BankAccount
		if err := utils.DB.Where("user_id = ? AND id = ?", userID, input.AccountID).First(&account).Error; err == nil {
			account.Balance += input.Amount
			utils.DB.Save(&account)
		}
	}

	if err := utils.DB.Create(&transaction).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to add money"})
		return
	}

	if err := postManualCashAddAccounting(utils.DB, userID, &transaction); err != nil {
		fmt.Printf("[cash-bank] accounting post failed: %v\n", err)
	}

	c.JSON(http.StatusCreated, transaction)
}

func ReduceMoney(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		AccountID   *uuid.UUID `json:"account_id"`
		Amount      float64    `json:"amount" binding:"required,gt=0"`
		Date        time.Time  `json:"date" binding:"required"`
		Description string     `json:"description"`
		Reference   string     `json:"reference"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	transaction := models.CashTransaction{
		ID:              uuid.New(),
		UserID:          userID,
		AccountID:       input.AccountID,
		TransactionType: "reduce",
		Amount:          input.Amount,
		Date:            input.Date,
		Description:     input.Description,
		Reference:       input.Reference,
		IsLinked:        false,
	}

	// Update account balance if account is specified
	if input.AccountID != nil {
		var account models.BankAccount
		if err := utils.DB.Where("user_id = ? AND id = ?", userID, input.AccountID).First(&account).Error; err == nil {
			account.Balance -= input.Amount
			utils.DB.Save(&account)
		}
	}

	if err := utils.DB.Create(&transaction).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reduce money"})
		return
	}

	if err := postManualCashReduceAccounting(utils.DB, userID, &transaction); err != nil {
		fmt.Printf("[cash-bank] accounting post failed: %v\n", err)
	}

	c.JSON(http.StatusCreated, transaction)
}

func TransferMoney(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		FromAccountID uuid.UUID `json:"from_account_id" binding:"required"`
		ToAccountID   uuid.UUID `json:"to_account_id" binding:"required"`
		Amount        float64   `json:"amount" binding:"required,gt=0"`
		Date          time.Time `json:"date" binding:"required"`
		Description   string    `json:"description"`
		Reference     string    `json:"reference"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if input.FromAccountID == input.ToAccountID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot transfer to same account"})
		return
	}

	// Verify both accounts exist and belong to user
	var fromAccount, toAccount models.BankAccount
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, input.FromAccountID).First(&fromAccount).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Source account not found"})
		return
	}
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, input.ToAccountID).First(&toAccount).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Destination account not found"})
		return
	}

	// Check sufficient balance
	if fromAccount.Balance < input.Amount {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Insufficient balance in source account"})
		return
	}

	// Create transfer out transaction
	transferOut := models.CashTransaction{
		ID:              uuid.New(),
		UserID:          userID,
		AccountID:       &input.FromAccountID,
		TransactionType: "transfer_out",
		Amount:          input.Amount,
		Date:            input.Date,
		Description:     input.Description,
		Reference:       input.Reference,
		FromAccountID:   &input.FromAccountID,
		ToAccountID:     &input.ToAccountID,
		IsLinked:        false,
	}

	// Create transfer in transaction
	transferIn := models.CashTransaction{
		ID:              uuid.New(),
		UserID:          userID,
		AccountID:       &input.ToAccountID,
		TransactionType: "transfer_in",
		Amount:          input.Amount,
		Date:            input.Date,
		Description:     input.Description,
		Reference:       input.Reference,
		FromAccountID:   &input.FromAccountID,
		ToAccountID:     &input.ToAccountID,
		IsLinked:        false,
	}

	// Update balances
	fromAccount.Balance -= input.Amount
	toAccount.Balance += input.Amount

	// Execute transaction
	tx := utils.DB.Begin()
	if err := tx.Create(&transferOut).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create transfer out transaction"})
		return
	}
	if err := tx.Create(&transferIn).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create transfer in transaction"})
		return
	}
	if err := tx.Save(&fromAccount).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update source account"})
		return
	}
	if err := tx.Save(&toAccount).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update destination account"})
		return
	}
	tx.Commit()

	c.JSON(http.StatusCreated, gin.H{
		"transfer_out": transferOut,
		"transfer_in":  transferIn,
	})
}

func DeleteCashTransaction(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var transaction models.CashTransaction
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&transaction).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Cash transaction not found"})
		return
	}

	// Revert balance if account is specified
	if transaction.AccountID != nil {
		var account models.BankAccount
		if err := utils.DB.Where("user_id = ? AND id = ?", userID, transaction.AccountID).First(&account).Error; err == nil {
			switch transaction.TransactionType {
			case "add", "transfer_in":
				account.Balance -= transaction.Amount
			case "reduce", "transfer_out", "payroll", "expense":
				account.Balance += transaction.Amount
			}
			utils.DB.Save(&account)
		}
	}

	if err := utils.DB.Delete(&transaction).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete cash transaction"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Cash transaction deleted successfully"})
}

// Summary Controller

func GetCashBankSummary(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var accounts []models.BankAccount
	if err := utils.DB.Where("user_id = ? AND is_active = ?", userID, true).Order("is_primary DESC, created_at DESC").Find(&accounts).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch bank accounts"})
		return
	}

	// Calculate total balance from bank accounts
	totalBankBalance := 0.0
	for _, acc := range accounts {
		totalBankBalance += acc.Balance
	}

	// Calculate cash in hand (transactions without account)
	var cashInHand float64
	utils.DB.Model(&models.CashTransaction{}).
		Where("user_id = ? AND account_id IS NULL", userID).
		Select("COALESCE(SUM(CASE WHEN transaction_type = 'add' THEN amount ELSE -amount END), 0)").
		Scan(&cashInHand)

	// Calculate unlinked transactions
	var unlinkedCount int64
	var unlinkedAmount float64
	utils.DB.Model(&models.CashTransaction{}).
		Where("user_id = ? AND is_linked = ?", userID, false).
		Count(&unlinkedCount)
	utils.DB.Model(&models.CashTransaction{}).
		Where("user_id = ? AND is_linked = ?", userID, false).
		Select("COALESCE(SUM(amount), 0)").
		Scan(&unlinkedAmount)

	summary := models.CashBankSummary{
		TotalBalance:   totalBankBalance + cashInHand,
		CashInHand:     cashInHand,
		BankAccounts:   accounts,
		UnlinkedCount:  unlinkedCount,
		UnlinkedAmount: unlinkedAmount,
	}

	c.JSON(http.StatusOK, summary)
}
