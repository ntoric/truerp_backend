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
		Category           string               `json:"category" binding:"required"`
		Description         string               `json:"description"`
		OriginalInvoiceNum  string               `json:"original_invoice_num"`
		Date                time.Time            `json:"date" binding:"required"`
		Vendor              string               `json:"vendor"`
		PaymentMode         string               `json:"payment_mode"`
		Notes               string               `json:"notes"`
		WithGST             bool                 `json:"with_gst"`
		TaxRate             float64              `json:"tax_rate"`
		Items               []models.ExpenseItem `json:"items"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

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
		Notes:              input.Notes,
	}

	if err := utils.DB.Create(&expense).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create expense"})
		return
	}

	// Create expense items (IDs must be set in app code — SQLite has no uuid_generate_v4())
	for _, item := range input.Items {
		item.ID = uuid.New()
		item.ExpenseID = expense.ID
		if err := utils.DB.Create(&item).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create expense items"})
			return
		}
	}

	// Reload with items
	utils.DB.Preload("Items").First(&expense, expense.ID)

	if err := postExpenseAccounting(utils.DB, userID, &expense); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Expense saved but failed to post to accounting"})
		return
	}

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

	var input models.Expense
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var expense models.Expense
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&expense).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Expense not found"})
		return
	}

	if err := utils.DB.Model(&expense).Updates(map[string]interface{}{
		"category":     input.Category,
		"description":  input.Description,
		"amount":       input.Amount,
		"date":         input.Date,
		"vendor":       input.Vendor,
		"payment_mode": input.PaymentMode,
		"notes":        input.Notes,
	}).Error; err != nil {
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

	if err := utils.DB.Delete(&expense).Error; err != nil {
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
