package controllers

import (
	"truerp/models"
	"truerp/utils"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func recordSalePaymentIn(tx *gorm.DB, userID uuid.UUID, accountID *uuid.UUID, amount float64, date time.Time, reference, description string) error {
	if amount <= 0 {
		return nil
	}
	if accountID != nil {
		var account models.BankAccount
		if err := tx.Where("user_id = ? AND id = ? AND is_active = ?", userID, *accountID, true).First(&account).Error; err != nil {
			return err
		}
		account.Balance += amount
		if err := tx.Save(&account).Error; err != nil {
			return err
		}
	}
	transaction := models.CashTransaction{
		ID:              uuid.New(),
		UserID:          userID,
		AccountID:       accountID,
		TransactionType: "add",
		Amount:          amount,
		Date:            date,
		Description:     description,
		Reference:       reference,
		IsLinked:        true,
	}
	if err := tx.Create(&transaction).Error; err != nil {
		return err
	}
	return postSalePaymentAccounting(tx, userID, transaction.ID, accountID, amount, date, reference, description)
}

func recordPurchasePaymentOut(tx *gorm.DB, userID uuid.UUID, accountID *uuid.UUID, amount float64, date time.Time, reference, description string) error {
	if amount <= 0 {
		return nil
	}
	if accountID != nil {
		var account models.BankAccount
		if err := tx.Where("user_id = ? AND id = ? AND is_active = ?", userID, *accountID, true).First(&account).Error; err != nil {
			return err
		}
		account.Balance -= amount
		if err := tx.Save(&account).Error; err != nil {
			return err
		}
	}
	transaction := models.CashTransaction{
		ID:              uuid.New(),
		UserID:          userID,
		AccountID:       accountID,
		TransactionType: "reduce",
		Amount:          amount,
		Date:            date,
		Description:     description,
		Reference:       reference,
		IsLinked:        true,
	}
	if err := tx.Create(&transaction).Error; err != nil {
		return err
	}
	return postPurchasePaymentAccounting(tx, userID, transaction.ID, accountID, amount, date, reference, description)
}

// recordPayrollCashOut deducts net salary from a bank account or cash in-hand
// and writes a linked cash-bank transaction (no AP journal — salary uses expense GL).
func recordPayrollCashOut(tx *gorm.DB, userID uuid.UUID, accountID *uuid.UUID, amount float64, date time.Time, reference, description string) error {
	if amount <= 0 {
		return nil
	}
	if accountID != nil {
		var account models.BankAccount
		if err := tx.Where("user_id = ? AND id = ? AND is_active = ?", userID, *accountID, true).First(&account).Error; err != nil {
			return err
		}
		account.Balance -= amount
		if err := tx.Save(&account).Error; err != nil {
			return err
		}
	}
	transaction := models.CashTransaction{
		ID:              uuid.New(),
		UserID:          userID,
		AccountID:       accountID,
		TransactionType: "reduce",
		Amount:          amount,
		Date:            date,
		Description:     description,
		Reference:       reference,
		IsLinked:        true,
	}
	return tx.Create(&transaction).Error
}

// reversePayrollCashOut restores cash/bank for a payroll payment referenced by payment number.
func reversePayrollCashOut(tx *gorm.DB, userID uuid.UUID, reference string) error {
	if reference == "" {
		return nil
	}
	var transactions []models.CashTransaction
	if err := tx.Where(
		"user_id = ? AND reference = ? AND transaction_type = ? AND is_linked = ?",
		userID, reference, "reduce", true,
	).Find(&transactions).Error; err != nil {
		return err
	}
	for _, txn := range transactions {
		if txn.AccountID != nil {
			var account models.BankAccount
			if err := tx.Where("user_id = ? AND id = ?", userID, *txn.AccountID).First(&account).Error; err == nil {
				account.Balance += txn.Amount
				if err := tx.Save(&account).Error; err != nil {
					return err
				}
			}
		}
		if err := tx.Delete(&txn).Error; err != nil {
			return err
		}
	}
	return nil
}

func validateUserBankAccount(userID uuid.UUID, accountID *uuid.UUID) error {
	if accountID == nil {
		return nil
	}
	var account models.BankAccount
	return utils.DB.Where("user_id = ? AND id = ? AND is_active = ?", userID, *accountID, true).First(&account).Error
}
