package controllers

import (
	"fmt"
	"time"
	"truerp/models"
	"truerp/utils"

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

// createLinkedPurchasePaymentOut records a PaymentOut row for a purchase bill payment
// and posts cash/bank + AP reduction. Bill paid_amount/balance_due must already be updated.
func createLinkedPurchasePaymentOut(tx *gorm.DB, userID uuid.UUID, bill *models.PurchaseBill, amount float64, date time.Time, notes string) error {
	if amount <= 0 || bill == nil {
		return nil
	}

	var count int64
	if err := tx.Model(&models.PaymentOut{}).Where("user_id = ?", userID).Count(&count).Error; err != nil {
		return err
	}
	number := fmt.Sprintf("POUT-%04d", count+1)

	mode := bill.PaymentMode
	if mode == "" {
		mode = "cash"
	}

	billID := bill.ID
	paymentOut := models.PaymentOut{
		ID:               uuid.New(),
		UserID:           userID,
		PurchaseBillID:   &billID,
		PartyID:          bill.PartyID,
		AmountPaid:       amount,
		PaymentOutNumber: number,
		Mode:             mode,
		Date:             date,
		Reference:        bill.BillNumber,
		Notes:            notes,
	}
	if err := tx.Create(&paymentOut).Error; err != nil {
		return err
	}

	desc := fmt.Sprintf("Payment out %s for purchase %s", number, bill.BillNumber)
	if err := recordPurchasePaymentOut(tx, userID, bill.BankAccountID, amount, date, bill.BillNumber, desc); err != nil {
		return err
	}

	// Match standalone PaymentOut behaviour: bump party balance by amount paid.
	var party models.Party
	if err := tx.Where("user_id = ? AND id = ?", userID, bill.PartyID).First(&party).Error; err == nil {
		if err := tx.Model(&party).Update("balance", party.Balance+amount).Error; err != nil {
			return err
		}
	}

	return nil
}

// recordPayrollCashOut deducts net salary from a bank account or cash in-hand
// and writes a linked cash-bank transaction typed as payroll.
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
		TransactionType: "payroll",
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
	// Include legacy "reduce" rows created before payroll had its own transaction type.
	if err := tx.Where(
		"user_id = ? AND reference = ? AND is_linked = ? AND transaction_type IN ?",
		userID, reference, true, []string{"payroll", "reduce"},
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
