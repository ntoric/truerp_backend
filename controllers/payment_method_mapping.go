package controllers

import (
	"truerp/models"
	"truerp/utils"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

var standardPaymentMethods = []struct {
	Key   string `json:"payment_method"`
	Label string `json:"label"`
}{
	{Key: "cash", Label: "Cash"},
	{Key: "upi", Label: "UPI"},
	{Key: "card", Label: "Card"},
	{Key: "bank_transfer", Label: "Bank Transfer"},
	{Key: "cheque", Label: "Cheque"},
}

func normalizePaymentMethod(mode string) string {
	m := strings.ToLower(strings.TrimSpace(mode))
	if m == "" {
		return "cash"
	}
	return m
}

func resolveBankAccountForPaymentMode(userID uuid.UUID, paymentMode string, explicit *uuid.UUID) (*uuid.UUID, error) {
	if explicit != nil {
		if err := validateUserBankAccount(userID, explicit); err != nil {
			return nil, err
		}
		return explicit, nil
	}

	mode := normalizePaymentMethod(paymentMode)
	var mapping models.PaymentMethodAccountMap
	if err := utils.DB.Where("user_id = ? AND payment_method = ?", userID, mode).First(&mapping).Error; err == nil {
		if mapping.BankAccountID != nil {
			if err := validateUserBankAccount(userID, mapping.BankAccountID); err != nil {
				return nil, err
			}
		}
		return mapping.BankAccountID, nil
	}

	if mode == "cash" {
		return nil, nil
	}

	var primary models.BankAccount
	if err := utils.DB.Where("user_id = ? AND is_primary = ? AND is_active = ?", userID, true, true).First(&primary).Error; err == nil {
		id := primary.ID
		return &id, nil
	}
	return nil, nil
}

func GetPaymentMethodMappings(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var saved []models.PaymentMethodAccountMap
	utils.DB.Where("user_id = ?", userID).Find(&saved)

	savedByMethod := make(map[string]*uuid.UUID, len(saved))
	for _, row := range saved {
		id := row.BankAccountID
		savedByMethod[row.PaymentMethod] = id
	}

	type mappingRow struct {
		PaymentMethod string     `json:"payment_method"`
		Label         string     `json:"label"`
		BankAccountID *uuid.UUID `json:"bank_account_id"`
	}

	rows := make([]mappingRow, 0, len(standardPaymentMethods))
	for _, method := range standardPaymentMethods {
		accountID, ok := savedByMethod[method.Key]
		if !ok {
			rows = append(rows, mappingRow{
				PaymentMethod: method.Key,
				Label:         method.Label,
				BankAccountID: nil,
			})
			continue
		}
		rows = append(rows, mappingRow{
			PaymentMethod: method.Key,
			Label:         method.Label,
			BankAccountID: accountID,
		})
	}

	c.JSON(http.StatusOK, gin.H{"mappings": rows})
}

func UpdatePaymentMethodMappings(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		Mappings []struct {
			PaymentMethod string     `json:"payment_method" binding:"required"`
			BankAccountID *uuid.UUID `json:"bank_account_id"`
		} `json:"mappings" binding:"required"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	allowed := make(map[string]bool, len(standardPaymentMethods))
	for _, m := range standardPaymentMethods {
		allowed[m.Key] = true
	}

	tx := utils.DB.Begin()
	for _, row := range input.Mappings {
		mode := normalizePaymentMethod(row.PaymentMethod)
		if !allowed[mode] {
			tx.Rollback()
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid payment method: " + row.PaymentMethod})
			return
		}
		if err := validateUserBankAccount(userID, row.BankAccountID); err != nil {
			tx.Rollback()
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid bank account for " + mode})
			return
		}

		var existing models.PaymentMethodAccountMap
		err := tx.Where("user_id = ? AND payment_method = ?", userID, mode).First(&existing).Error
		if err != nil {
			create := models.PaymentMethodAccountMap{
				ID:            uuid.New(),
				UserID:        userID,
				PaymentMethod: mode,
				BankAccountID: row.BankAccountID,
			}
			if err := tx.Create(&create).Error; err != nil {
				tx.Rollback()
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save payment method mapping"})
				return
			}
			continue
		}

		if err := tx.Model(&existing).Updates(map[string]interface{}{
			"bank_account_id": row.BankAccountID,
		}).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update payment method mapping"})
			return
		}
	}
	tx.Commit()

	GetPaymentMethodMappings(c)
}
