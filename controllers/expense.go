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

func GetExpenses(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var expenses []models.Expense
	query := utils.DB.Where("user_id = ?", userID).Preload("Items")

	if category := c.Query("category"); category != "" {
		query = query.Where("category = ?", category)
	}
	if from := c.Query("from"); from != "" {
		query = query.Where("date >= ?", from)
	}
	if to := c.Query("to"); to != "" {
		query = query.Where("date <= ?", to)
	}
	if search := c.Query("search"); search != "" {
		query = query.Where("vendor ILIKE ? OR description ILIKE ? OR expense_number ILIKE ?",
			"%"+search+"%", "%"+search+"%", "%"+search+"%")
	}

	if err := query.Order("date DESC").Find(&expenses).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch expenses"})
		return
	}

	c.JSON(http.StatusOK, expenses)
}

func CreateExpense(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	userName := ""
	if name, exists := c.Get("user_name"); exists {
		userName = name.(string)
	}

	var input struct {
		Category           string               `json:"category"`
		Description         string               `json:"description"`
		OriginalInvoiceNum  string               `json:"original_invoice_num"`
		Date                time.Time            `json:"date" binding:"required"`
		Vendor              string               `json:"vendor"`
		PaymentMode         string               `json:"payment_mode"`
		BankAccountID       *uuid.UUID           `json:"bank_account_id"`
		Notes               string               `json:"notes"`
		WithGST             bool                 `json:"with_gst"`
		TaxRate             float64              `json:"tax_rate"`
		Items               []models.ExpenseItem `json:"items"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := validateUserBankAccount(userID, input.BankAccountID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid bank account"})
		return
	}

	resolvedBankAccount, err := resolveBankAccountForPaymentMode(userID, input.PaymentMode, input.BankAccountID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	input.Category = utils.ResolveCategoryName(input.Category)
	_ = utils.EnsureDefaultCategories(utils.DB, userID)

	// Generate expense number
	var count int64
	utils.DB.Model(&models.Expense{}).Where("user_id = ?", userID).Count(&count)
	expenseNumber := fmt.Sprintf("EXP-%04d", count+1)

	// Calculate totals
	var subTotal, taxTotal, totalAmount float64
	for i := range input.Items {
		item := &input.Items[i]
		itemTotal := item.Quantity * item.UnitPrice
		item.Total = itemTotal
		if input.WithGST {
			item.TaxRate = input.TaxRate
			item.TaxAmount = itemTotal * (input.TaxRate / 100)
			item.Total = itemTotal + item.TaxAmount
		}
		subTotal += itemTotal
		taxTotal += item.TaxAmount
		totalAmount += item.Total
	}

	// If no items, use amount from description or set to 0
	if len(input.Items) == 0 {
		totalAmount = 0
	}

	expense := models.Expense{
		ID:                 uuid.New(),
		UserID:             userID,
		ExpenseNumber:      expenseNumber,
		OriginalInvoiceNum: input.OriginalInvoiceNum,
		Category:           input.Category,
		Description:        input.Description,
		Amount:             totalAmount,
		SubTotal:           subTotal,
		TaxTotal:           taxTotal,
		WithGST:            input.WithGST,
		TaxRate:            input.TaxRate,
		Date:               input.Date,
		Vendor:             input.Vendor,
		PaymentMode:        input.PaymentMode,
		BankAccountID:      resolvedBankAccount,
		Notes:              input.Notes,
	}

	tx := utils.DB.Begin()
	if err := tx.Create(&expense).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create expense"})
		return
	}

	// Create expense items (IDs must be set in app code — SQLite has no uuid_generate_v4())
	for _, item := range input.Items {
		item.ID = uuid.New()
		item.ExpenseID = expense.ID
		if err := tx.Create(&item).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create expense items"})
			return
		}
	}

	desc := fmt.Sprintf("Expense %s", expense.ExpenseNumber)
	if input.Description != "" {
		desc = input.Description
	}
	if err := recordExpenseCashOut(tx, userID, resolvedBankAccount, totalAmount, input.Date, expense.ExpenseNumber, desc); err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to deduct from account"})
		return
	}

	if err := postExpenseAccounting(tx, userID, &expense); err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Expense saved but failed to post to accounting"})
		return
	}

	if err := tx.Commit().Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create expense"})
		return
	}

	// Reload with items
	utils.DB.Preload("Items").First(&expense, expense.ID)

	// Log expense creation
	CreateAuditLog(
		userID,
		userName,
		"create",
		"expense",
		&expense.ID,
		expenseNumber,
		fmt.Sprintf("Created expense: %s - %s", expenseNumber, input.Description),
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		map[string]interface{}{
			"category": input.Category,
			"amount":   totalAmount,
			"vendor":   input.Vendor,
		},
		"success",
		"",
	)

	c.JSON(http.StatusCreated, expense)
}

func UpdateExpense(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	userName := ""
	if name, exists := c.Get("user_name"); exists {
		userName = name.(string)
	}
	id := c.Param("id")

	var input struct {
		Category      string     `json:"category"`
		Description   string     `json:"description"`
		Amount        float64    `json:"amount"`
		Date          time.Time  `json:"date"`
		Vendor        string     `json:"vendor"`
		PaymentMode   string     `json:"payment_mode"`
		BankAccountID *uuid.UUID `json:"bank_account_id"`
		Notes         string     `json:"notes"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var expense models.Expense
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&expense).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Expense not found"})
		return
	}

	if err := validateUserBankAccount(userID, input.BankAccountID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid bank account"})
		return
	}

	resolvedBankAccount, err := resolveBankAccountForPaymentMode(userID, input.PaymentMode, input.BankAccountID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	input.Category = utils.ResolveCategoryName(input.Category)
	_ = utils.EnsureDefaultCategories(utils.DB, userID)

	paymentChanged := expense.Amount != input.Amount ||
		expense.PaymentMode != input.PaymentMode ||
		!bankAccountIDsEqual(expense.BankAccountID, resolvedBankAccount) ||
		!expense.Date.Equal(input.Date)

	tx := utils.DB.Begin()
	if paymentChanged {
		if err := reverseExpenseCashOut(tx, userID, expense.ExpenseNumber); err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reverse previous account deduction"})
			return
		}
	}

	if err := tx.Model(&expense).Updates(map[string]interface{}{
		"category":        input.Category,
		"description":     input.Description,
		"amount":          input.Amount,
		"date":            input.Date,
		"vendor":          input.Vendor,
		"payment_mode":    input.PaymentMode,
		"bank_account_id": resolvedBankAccount,
		"notes":           input.Notes,
	}).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update expense"})
		return
	}

	expense.Category = input.Category
	expense.Description = input.Description
	expense.Amount = input.Amount
	expense.Date = input.Date
	expense.Vendor = input.Vendor
	expense.PaymentMode = input.PaymentMode
	expense.BankAccountID = resolvedBankAccount
	expense.Notes = input.Notes

	if paymentChanged && input.Amount > 0 {
		desc := fmt.Sprintf("Expense %s", expense.ExpenseNumber)
		if input.Description != "" {
			desc = input.Description
		}
		if err := recordExpenseCashOut(tx, userID, resolvedBankAccount, input.Amount, input.Date, expense.ExpenseNumber, desc); err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to deduct from account"})
			return
		}
	}

	if err := tx.Commit().Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update expense"})
		return
	}

	// Log expense update
	CreateAuditLog(
		userID,
		userName,
		"update",
		"expense",
		&expense.ID,
		expense.ExpenseNumber,
		fmt.Sprintf("Updated expense: %s", expense.ExpenseNumber),
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		map[string]interface{}{
			"category":     input.Category,
			"amount":       input.Amount,
			"vendor":       input.Vendor,
			"payment_mode": input.PaymentMode,
		},
		"success",
		"",
	)

	c.JSON(http.StatusOK, expense)
}

func DeleteExpense(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	userName := ""
	if name, exists := c.Get("user_name"); exists {
		userName = name.(string)
	}
	id := c.Param("id")

	var expense models.Expense
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&expense).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Expense not found"})
		return
	}

	tx := utils.DB.Begin()
	if err := reverseExpenseCashOut(tx, userID, expense.ExpenseNumber); err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reverse account deduction"})
		return
	}
	if err := tx.Delete(&expense).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete expense"})
		return
	}
	if err := tx.Commit().Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete expense"})
		return
	}

	// Log expense deletion
	CreateAuditLog(
		userID,
		userName,
		"delete",
		"expense",
		&expense.ID,
		expense.ExpenseNumber,
		fmt.Sprintf("Deleted expense: %s", expense.ExpenseNumber),
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		map[string]interface{}{
			"category": expense.Category,
			"amount":   expense.Amount,
			"vendor":   expense.Vendor,
		},
		"success",
		"",
	)

	c.JSON(http.StatusOK, gin.H{"message": "Expense deleted successfully"})
}

func GetNextExpenseNumber(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var count int64
	utils.DB.Model(&models.Expense{}).Where("user_id = ?", userID).Count(&count)

	nextNum := fmt.Sprintf("EXP-%04d", count+1)
	c.JSON(http.StatusOK, gin.H{"expense_number": nextNum})
}

func bankAccountIDsEqual(a, b *uuid.UUID) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}
