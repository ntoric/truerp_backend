package controllers

import (
	"fmt"
	"net/http"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func GetStaffExtraAmounts(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var extras []models.StaffExtraAmount
	query := utils.DB.Where("user_id = ?", userID).Preload("Staff").Preload("BankAccount")

	if staffID := c.Query("staff_id"); staffID != "" {
		query = query.Where("staff_id = ?", staffID)
	}

	if direction := c.Query("direction"); direction != "" {
		query = query.Where("direction = ?", direction)
	}

	if status := c.Query("status"); status != "" {
		query = query.Where("status = ?", status)
	}

	if from := c.Query("from"); from != "" {
		query = query.Where("entry_date >= ?", from)
	}

	if to := c.Query("to"); to != "" {
		query = query.Where("entry_date <= ?", to)
	}

	if search := c.Query("search"); search != "" {
		like := "%" + strings.ToLower(search) + "%"
		query = query.Where("LOWER(reason) LIKE ? OR LOWER(extra_number) LIKE ?", like, like)
	}

	if err := query.Order("entry_date DESC, created_at DESC").Find(&extras).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch extra amounts"})
		return
	}

	c.JSON(http.StatusOK, extras)
}

func GetStaffExtraAmount(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var extra models.StaffExtraAmount
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Preload("Staff").Preload("BankAccount").First(&extra).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Extra amount not found"})
		return
	}

	c.JSON(http.StatusOK, extra)
}

// applyStaffExtraPayout records the extra amount payout as a Staff Extra
// expense, deducts it from the chosen bank account or cash in-hand, and posts
// it to the general ledger.
func applyStaffExtraPayout(tx *gorm.DB, userID uuid.UUID, extra *models.StaffExtraAmount, staffName string) error {
	if extra.Amount <= 0 {
		return nil
	}
	desc := fmt.Sprintf("Staff extra amount %s — %s", extra.ExtraNumber, staffName)
	expense := models.Expense{
		ID:            uuid.New(),
		UserID:        userID,
		ExpenseNumber: nextExpenseNumber(tx, userID),
		Category:      "Staff Extra",
		Description:   desc,
		Amount:        extra.Amount,
		SubTotal:      extra.Amount,
		Date:          *extra.RedeemedAt,
		Vendor:        staffName,
		PaymentMode:   extra.PaymentMode,
		BankAccountID: extra.BankAccountID,
		Notes:         extra.Notes,
	}
	if err := tx.Create(&expense).Error; err != nil {
		return err
	}
	item := models.ExpenseItem{
		ID:          uuid.New(),
		ExpenseID:   expense.ID,
		Description: desc,
		Quantity:    1,
		UnitPrice:   extra.Amount,
		Total:       extra.Amount,
	}
	if err := tx.Create(&item).Error; err != nil {
		return err
	}
	if err := recordExpenseCashOut(tx, userID, extra.BankAccountID, extra.Amount, *extra.RedeemedAt, expense.ExpenseNumber, desc); err != nil {
		return err
	}
	if err := postExpenseAccounting(tx, userID, &expense); err != nil {
		return err
	}
	extra.ExpenseID = &expense.ID
	return tx.Model(extra).Update("expense_id", expense.ID).Error
}

// reverseStaffExtraPayout undoes the expense/cash-out/GL posted for a redeemed
// extra amount.
func reverseStaffExtraPayout(tx *gorm.DB, userID uuid.UUID, extra *models.StaffExtraAmount) error {
	if extra.ExpenseID == nil {
		return nil
	}
	var expense models.Expense
	if err := tx.Where("user_id = ? AND id = ?", userID, *extra.ExpenseID).First(&expense).Error; err != nil {
		return nil // expense already gone
	}
	if err := reverseExpenseCashOut(tx, userID, expense.ExpenseNumber); err != nil {
		return err
	}
	if err := reverseAccountingByRef(tx, userID, "expense", expense.ID); err != nil {
		return err
	}
	if err := tx.Where("expense_id = ?", expense.ID).Delete(&models.ExpenseItem{}).Error; err != nil {
		return err
	}
	return tx.Delete(&expense).Error
}

func CreateStaffExtraAmount(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	userName := ""
	if name, exists := c.Get("user_name"); exists {
		userName = name.(string)
	}

	var input struct {
		StaffID       uuid.UUID  `json:"staff_id" binding:"required"`
		Direction     string     `json:"direction" binding:"required"`
		Amount        float64    `json:"amount" binding:"required"`
		Reason        string     `json:"reason"`
		EntryDate     time.Time  `json:"entry_date" binding:"required"`
		RedeemNow     bool       `json:"redeem_now"`
		PaymentMode   string     `json:"payment_mode"`
		BankAccountID *uuid.UUID `json:"bank_account_id"`
		Reference     string     `json:"reference"`
		Notes         string     `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if input.Direction != "add" && input.Direction != "deduct" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "direction must be add or deduct"})
		return
	}
	if input.Amount <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Amount must be greater than 0"})
		return
	}
	if err := validateUserBankAccount(userID, input.BankAccountID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid bank account"})
		return
	}

	var staff models.Staff
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, input.StaffID).First(&staff).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Staff not found"})
		return
	}

	var count int64
	utils.DB.Model(&models.StaffExtraAmount{}).Where("user_id = ?", userID).Count(&count)
	extraNumber := fmt.Sprintf("EXT-%04d", count+1)

	extra := models.StaffExtraAmount{
		ID:          uuid.New(),
		UserID:      userID,
		StaffID:     input.StaffID,
		ExtraNumber: extraNumber,
		Direction:   input.Direction,
		Amount:      input.Amount,
		Reason:      input.Reason,
		EntryDate:   input.EntryDate,
		Reference:   input.Reference,
		Notes:       input.Notes,
		Status:      "pending",
	}

	if input.RedeemNow {
		redeemedAt := input.EntryDate
		extra.IsRedeemed = true
		extra.RedeemedAt = &redeemedAt
		extra.PaymentMode = input.PaymentMode
		if extra.PaymentMode == "" {
			if input.BankAccountID == nil {
				extra.PaymentMode = "cash"
			} else {
				extra.PaymentMode = "bank_transfer"
			}
		}
		extra.BankAccountID = input.BankAccountID
		// A paid-out "add" entry is settled immediately; a "deduct" entry stays
		// pending until a payroll deducts it.
		if extra.Direction == "add" {
			extra.Status = "settled"
			extra.SettledAmount = extra.Amount
		}
	}

	err := utils.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&extra).Error; err != nil {
			return err
		}
		if extra.IsRedeemed {
			return applyStaffExtraPayout(tx, userID, &extra, staff.Name)
		}
		return nil
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create extra amount"})
		return
	}
	utils.DB.Preload("BankAccount").First(&extra, extra.ID)

	CreateAuditLog(
		userID,
		userName,
		"create",
		"staff_extra",
		&extra.ID,
		extraNumber,
		fmt.Sprintf("Created staff extra amount: %s - %s %s for %s", extraNumber, input.Direction, input.Reason, staff.Name),
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		map[string]interface{}{
			"staff_id":  input.StaffID,
			"direction": input.Direction,
			"amount":    input.Amount,
			"redeemed":  extra.IsRedeemed,
		},
		"success",
		"",
	)

	c.JSON(http.StatusCreated, extra)
}

func UpdateStaffExtraAmount(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	userName := ""
	if name, exists := c.Get("user_name"); exists {
		userName = name.(string)
	}
	id := c.Param("id")

	var input struct {
		Direction string    `json:"direction"`
		Amount    float64   `json:"amount"`
		Reason    string    `json:"reason"`
		EntryDate time.Time `json:"entry_date"`
		Status    string    `json:"status"`
		Reference string    `json:"reference"`
		Notes     string    `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var extra models.StaffExtraAmount
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&extra).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Extra amount not found"})
		return
	}

	if extra.Status != "pending" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Only pending extra amounts can be updated"})
		return
	}
	if extra.IsRedeemed {
		c.JSON(http.StatusBadRequest, gin.H{"error": "A redeemed extra amount cannot be updated"})
		return
	}

	direction := input.Direction
	if direction == "" {
		direction = extra.Direction
	}
	if direction != "add" && direction != "deduct" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "direction must be add or deduct"})
		return
	}
	entryDate := input.EntryDate
	if entryDate.IsZero() {
		entryDate = extra.EntryDate
	}
	status := input.Status
	if status == "" {
		status = extra.Status
	}
	if status != "pending" && status != "cancelled" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Status can only be changed to pending or cancelled"})
		return
	}
	amount := input.Amount
	if amount <= 0 {
		amount = extra.Amount
	}

	updates := map[string]interface{}{
		"direction":  direction,
		"amount":     amount,
		"reason":     input.Reason,
		"entry_date": entryDate,
		"status":     status,
		"reference":  input.Reference,
		"notes":      input.Notes,
	}

	if err := utils.DB.Model(&extra).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update extra amount"})
		return
	}

	CreateAuditLog(
		userID,
		userName,
		"update",
		"staff_extra",
		&extra.ID,
		extra.ExtraNumber,
		fmt.Sprintf("Updated staff extra amount: %s", extra.ExtraNumber),
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		map[string]interface{}{
			"direction": direction,
			"amount":    input.Amount,
			"status":    status,
		},
		"success",
		"",
	)

	c.JSON(http.StatusOK, extra)
}

// RedeemStaffExtra pays out a pending extra amount immediately. "add" entries
// are settled by the payout; "deduct" entries stay pending for salary recovery.
func RedeemStaffExtra(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	userName := ""
	if name, exists := c.Get("user_name"); exists {
		userName = name.(string)
	}
	id := c.Param("id")

	var input struct {
		PaymentDate   time.Time  `json:"payment_date"`
		PaymentMode   string     `json:"payment_mode"`
		BankAccountID *uuid.UUID `json:"bank_account_id"`
		Reference     string     `json:"reference"`
		Notes         string     `json:"notes"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := validateUserBankAccount(userID, input.BankAccountID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid bank account"})
		return
	}

	var extra models.StaffExtraAmount
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).Preload("Staff").First(&extra).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Extra amount not found"})
		return
	}
	if extra.Status != "pending" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Only pending extra amounts can be redeemed"})
		return
	}
	if extra.IsRedeemed {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Extra amount is already redeemed"})
		return
	}

	redeemedAt := input.PaymentDate
	if redeemedAt.IsZero() {
		redeemedAt = time.Now()
	}
	paymentMode := input.PaymentMode
	if paymentMode == "" {
		if input.BankAccountID == nil {
			paymentMode = "cash"
		} else {
			paymentMode = "bank_transfer"
		}
	}

	err := utils.DB.Transaction(func(tx *gorm.DB) error {
		extra.IsRedeemed = true
		extra.RedeemedAt = &redeemedAt
		extra.PaymentMode = paymentMode
		extra.BankAccountID = input.BankAccountID
		if input.Reference != "" {
			extra.Reference = input.Reference
		}
		if input.Notes != "" {
			extra.Notes = input.Notes
		}
		if extra.Direction == "add" {
			extra.Status = "settled"
			extra.SettledAmount = extra.Amount
		}
		if err := tx.Model(&extra).Updates(map[string]interface{}{
			"is_redeemed":     extra.IsRedeemed,
			"redeemed_at":     extra.RedeemedAt,
			"payment_mode":    extra.PaymentMode,
			"bank_account_id": extra.BankAccountID,
			"reference":       extra.Reference,
			"notes":           extra.Notes,
			"status":          extra.Status,
			"settled_amount":  extra.SettledAmount,
		}).Error; err != nil {
			return err
		}
		return applyStaffExtraPayout(tx, userID, &extra, extra.Staff.Name)
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to redeem extra amount"})
		return
	}

	CreateAuditLog(
		userID,
		userName,
		"update",
		"staff_extra",
		&extra.ID,
		extra.ExtraNumber,
		fmt.Sprintf("Redeemed staff extra amount: %s (%.2f)", extra.ExtraNumber, extra.Amount),
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		map[string]interface{}{
			"amount":       extra.Amount,
			"payment_mode": extra.PaymentMode,
		},
		"success",
		"",
	)

	utils.DB.Preload("Staff").Preload("BankAccount").First(&extra, extra.ID)
	c.JSON(http.StatusOK, extra)
}

func DeleteStaffExtraAmount(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	userName := ""
	if name, exists := c.Get("user_name"); exists {
		userName = name.(string)
	}
	id := c.Param("id")

	var extra models.StaffExtraAmount
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&extra).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Extra amount not found"})
		return
	}
	if extra.SettledByPayrollID != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "This extra amount was applied to a payroll — delete the payroll record first"})
		return
	}

	err := utils.DB.Transaction(func(tx *gorm.DB) error {
		if err := reverseStaffExtraPayout(tx, userID, &extra); err != nil {
			return err
		}
		return tx.Delete(&extra).Error
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete extra amount"})
		return
	}

	CreateAuditLog(
		userID,
		userName,
		"delete",
		"staff_extra",
		&extra.ID,
		extra.ExtraNumber,
		fmt.Sprintf("Deleted staff extra amount: %s", extra.ExtraNumber),
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		map[string]interface{}{
			"direction": extra.Direction,
			"amount":    extra.Amount,
		},
		"success",
		"",
	)

	c.JSON(http.StatusOK, gin.H{"message": "Extra amount deleted successfully"})
}

func GetNextExtraNumber(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var count int64
	utils.DB.Model(&models.StaffExtraAmount{}).Where("user_id = ?", userID).Count(&count)

	nextNum := fmt.Sprintf("EXT-%04d", count+1)
	c.JSON(http.StatusOK, gin.H{"extra_number": nextNum})
}
