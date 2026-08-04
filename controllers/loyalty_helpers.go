package controllers

import (
	"truerp/models"
	"truerp/utils"
	"errors"
	"fmt"
	"math"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func GetOrCreateLoyaltySettings(userID uuid.UUID) (*models.LoyaltySettings, error) {
	var settings models.LoyaltySettings
	err := utils.DB.Where("user_id = ?", userID).First(&settings).Error
	if err == nil {
		return &settings, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	settings = models.LoyaltySettings{
		ID:               uuid.New(),
		UserID:           userID,
		IsEnabled:        false,
		SpendAmount:      100,
		PointsPerSpend:   1,
		PointValue:       1,
		MinRedeemPoints:  50,
		MaxRedeemPercent: 25,
	}
	if err := utils.DB.Create(&settings).Error; err != nil {
		return nil, err
	}
	return &settings, nil
}

func CalculateEarnPoints(settings *models.LoyaltySettings, amount float64) int64 {
	if settings == nil || !settings.IsEnabled || amount <= 0 || settings.SpendAmount <= 0 {
		return 0
	}
	points := math.Floor(amount / settings.SpendAmount * float64(settings.PointsPerSpend))
	if points < 0 {
		return 0
	}
	return int64(points)
}

func ComputeLoyaltyRedemption(settings *models.LoyaltySettings, party *models.Party, billTotal float64, pointsToRedeem int64) (discount float64, err error) {
	if pointsToRedeem <= 0 {
		return 0, nil
	}
	if settings == nil || !settings.IsEnabled {
		return 0, errors.New("loyalty program is not enabled")
	}
	if party.PartyType != "customer" {
		return 0, errors.New("loyalty points apply to customers only")
	}
	if pointsToRedeem < settings.MinRedeemPoints {
		return 0, fmt.Errorf("minimum %d points required to redeem", settings.MinRedeemPoints)
	}
	if party.LoyaltyPoints < pointsToRedeem {
		return 0, errors.New("insufficient loyalty points")
	}
	if settings.PointValue <= 0 {
		return 0, errors.New("invalid point redemption value")
	}

	discount = float64(pointsToRedeem) * settings.PointValue
	if billTotal > 0 && settings.MaxRedeemPercent > 0 {
		maxDiscount := billTotal * (settings.MaxRedeemPercent / 100)
		if discount > maxDiscount {
			return 0, fmt.Errorf("maximum %.0f%% of bill can be paid with points", settings.MaxRedeemPercent)
		}
	}
	if discount > billTotal && billTotal > 0 {
		return 0, errors.New("redemption amount exceeds bill total")
	}
	return discount, nil
}

func recordLoyaltyTransaction(tx *gorm.DB, userID, partyID uuid.UUID, txnType string, points int64, balanceAfter int64, referenceType string, referenceID *uuid.UUID, referenceNumber, notes string) error {
	entry := models.LoyaltyTransaction{
		ID:              uuid.New(),
		UserID:          userID,
		PartyID:         partyID,
		TransactionType: txnType,
		Points:          points,
		BalanceAfter:    balanceAfter,
		ReferenceType:   referenceType,
		ReferenceID:     referenceID,
		ReferenceNumber: referenceNumber,
		Notes:           notes,
	}
	return tx.Create(&entry).Error
}

func applyLoyaltyForInvoice(tx *gorm.DB, userID uuid.UUID, party *models.Party, invoice *models.Invoice, settings *models.LoyaltySettings, pointsRedeemed int64) error {
	if party.PartyType != "customer" || settings == nil || !settings.IsEnabled {
		return nil
	}

	balance := party.LoyaltyPoints

	if pointsRedeemed > 0 {
		balance -= pointsRedeemed
		if err := recordLoyaltyTransaction(tx, userID, party.ID, "redeem", -pointsRedeemed, balance, "invoice", &invoice.ID, invoice.InvoiceNumber, "Redeemed on invoice"); err != nil {
			return err
		}
	}

	if invoice.Status != "draft" && invoice.Status != "cancelled" {
		earnPoints := CalculateEarnPoints(settings, invoice.TotalAmount)
		if earnPoints > 0 {
			balance += earnPoints
			invoice.LoyaltyPointsEarned = earnPoints
			if err := recordLoyaltyTransaction(tx, userID, party.ID, "earn", earnPoints, balance, "invoice", &invoice.ID, invoice.InvoiceNumber, "Earned on invoice"); err != nil {
				return err
			}
		}
	}

	if pointsRedeemed == 0 && invoice.LoyaltyPointsEarned == 0 {
		return nil
	}

	if err := tx.Model(party).Update("loyalty_points", balance).Error; err != nil {
		return err
	}
	party.LoyaltyPoints = balance
	return tx.Model(invoice).Updates(map[string]interface{}{
		"loyalty_points_earned":   invoice.LoyaltyPointsEarned,
		"loyalty_points_redeemed": invoice.LoyaltyPointsRedeemed,
		"loyalty_discount":        invoice.LoyaltyDiscount,
	}).Error
}

func AdjustPartyLoyaltyPoints(tx *gorm.DB, userID uuid.UUID, party *models.Party, pointsDelta int64, notes string) error {
	newBalance := party.LoyaltyPoints + pointsDelta
	if newBalance < 0 {
		return errors.New("insufficient loyalty points")
	}
	if err := recordLoyaltyTransaction(tx, userID, party.ID, "adjust", pointsDelta, newBalance, "manual", nil, "", notes); err != nil {
		return err
	}
	return tx.Model(party).Update("loyalty_points", newBalance).Error
}
