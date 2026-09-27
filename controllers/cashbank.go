package controllers

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
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
		AccountName    string  `json:"account_name" binding:"required"`
		AccountNumber  string  `json:"account_number" binding:"required"`
		BankName       string  `json:"bank_name" binding:"required"`
		IFSCCode       string  `json:"ifsc_code"`
		AccountType    string  `json:"account_type"`
		OpeningBalance float64 `json:"opening_balance"`
		Notes          string  `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	account := models.BankAccount{
		ID:             uuid.New(),
		UserID:         userID,
		AccountName:    input.AccountName,
		AccountNumber:  input.AccountNumber,
		BankName:       input.BankName,
		IFSCCode:       input.IFSCCode,
		AccountType:    input.AccountType,
		OpeningBalance: input.OpeningBalance,
		Balance:        input.OpeningBalance,
		IsActive:       true,
		Notes:          input.Notes,
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
		AccountName   string `json:"account_name"`
		AccountNumber string `json:"account_number"`
		BankName      string `json:"bank_name"`
		IFSCCode      string `json:"ifsc_code"`
		AccountType   string `json:"account_type"`
		IsActive      bool   `json:"is_active"`
		Notes         string `json:"notes"`
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
	search := strings.TrimSpace(c.Query("search"))

	query := utils.DB.Model(&models.CashTransaction{}).Where("user_id = ?", userID)

	// Filter by account ("cash" = cash in-hand, stored as NULL account_id)
	if accountID := c.Query("account_id"); accountID != "" {
		if accountID == "cash" {
			query = query.Where("account_id IS NULL")
		} else {
			query = query.Where("account_id = ?", accountID)
		}
	}

	// Filter by transaction type
	if transType := c.Query("transaction_type"); transType != "" {
		query = query.Where("transaction_type = ?", transType)
	}

	// Filter by unlinked transactions
	if unlinked := c.Query("unlinked"); unlinked == "true" {
		query = query.Where("is_linked = ?", false)
	}

	// Period filter (day-inclusive: date is a timestamp)
	if startDate := c.Query("start_date"); startDate != "" {
		if _, err := time.Parse("2006-01-02", startDate); err == nil {
			query = query.Where(utils.SQLDateGTE("date"), startDate)
		}
	}
	if endDate := c.Query("end_date"); endDate != "" {
		if _, err := time.Parse("2006-01-02", endDate); err == nil {
			query = query.Where(utils.SQLDateLTE("date"), endDate)
		}
	}

	if search != "" {
		like := "%" + strings.ToLower(search) + "%"
		query = query.Where("LOWER(description) LIKE ? OR LOWER(reference) LIKE ?", like, like)
	}
	// Ordering is applied only to row fetches — aggregate/count queries run on
	// `query` directly so Postgres doesn't see ORDER BY on a SUM/COUNT.
	orderBy := "date DESC, created_at DESC"

	transactions := make([]models.CashTransaction, 0)

	// Paginated mode (opt-in via page/per_page); per_page <= 0 returns every
	// matching row. Unparameterized callers keep the legacy plain array.
	if c.Query("page") != "" || c.Query("per_page") != "" {
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		if page < 1 {
			page = 1
		}
		perPage, _ := strconv.Atoi(c.DefaultQuery("per_page", "25"))

		var total int64
		if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch cash transactions"})
			return
		}

		// In/out totals across the entire filtered set (not just this page).
		var totals struct {
			TotalIn  float64
			TotalOut float64
		}
		if err := query.Session(&gorm.Session{}).Select(
			"COALESCE(SUM(CASE WHEN transaction_type IN ('add','transfer_in') THEN amount ELSE 0 END), 0) AS total_in, " +
				"COALESCE(SUM(CASE WHEN transaction_type IN ('add','transfer_in') THEN 0 ELSE amount END), 0) AS total_out",
		).Scan(&totals).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch cash transactions"})
			return
		}

		pageQuery := query.Order(orderBy).Preload("Account")
		if perPage > 0 {
			pageQuery = pageQuery.Limit(perPage).Offset((page - 1) * perPage)
		}
		if err := pageQuery.Find(&transactions).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch cash transactions"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"transactions": transactions,
			"total":        total,
			"page":         page,
			"per_page":     perPage,
			"total_in":     totals.TotalIn,
			"total_out":    totals.TotalOut,
		})
		return
	}

	if err := query.Order(orderBy).Preload("Account").Find(&transactions).Error; err != nil {
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

	// Optional period filter (YYYY-MM-DD, day-inclusive): balances are reported
	// as of end_date while flow stats are scoped to [start_date, end_date].
	startDate := c.Query("start_date")
	if _, err := time.Parse("2006-01-02", startDate); err != nil {
		startDate = ""
	}
	endDate := c.Query("end_date")
	if _, err := time.Parse("2006-01-02", endDate); err != nil {
		endDate = ""
	}

	var accounts []models.BankAccount
	if err := utils.DB.Where("user_id = ? AND is_active = ?", userID, true).Order("is_primary DESC, created_at DESC").Find(&accounts).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch bank accounts"})
		return
	}

	c.JSON(http.StatusOK, buildCashBankSummary(utils.DB, userID, accounts, startDate, endDate))
}
