package controllers

import (
	"errors"
	"fmt"
	"math"
	"truerp/models"
	"truerp/utils"

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
		RewardType:       LoyaltyRewardPoints,
		CashbackPercent:  1,
		EarnBasis:        LoyaltyEarnOnBill,
	}
	if err := utils.DB.Create(&settings).Error; err != nil {
		return nil, err
	}
	return &settings, nil
}

var errLoyaltyExcluded = errors.New("this customer is excluded from the loyalty program")

const (
	LoyaltyRewardPoints = "points"
	LoyaltyRewardCash   = "cash"

	LoyaltyEarnOnBill   = "bill"
	LoyaltyEarnOnMargin = "margin"
)

// IsMarginLoyalty reports whether rewards are earned on item profit margin
// instead of the bill total.
func IsMarginLoyalty(settings *models.LoyaltySettings) bool {
	return settings != nil && settings.EarnBasis == LoyaltyEarnOnMargin
}

// computeEligibleMarginProfit sums the profit of items whose margin on
// selling price is at least minMarginPct.
//
// Each item's net sale is its taxable value (qty × rate − item discount)
// minus its proportional share of billDiscount (invoice-level + loyalty
// discount). Cost is qty × unit purchase price. Items with no product or no
// known purchase price (cost ≤ 0) are skipped: their margin is unknown, and
// counting them as 100% margin would over-reward.
func computeEligibleMarginProfit(items []models.InvoiceItem, costs map[uuid.UUID]float64, billDiscount, minMarginPct float64) float64 {
	var gross float64
	for _, item := range items {
		gross += itemTaxableValue(item)
	}
	if gross <= 0 {
		return 0
	}
	ratio := 1.0
	if billDiscount > 0 {
		ratio = math.Max(0, 1-billDiscount/gross)
	}

	var profit float64
	for _, item := range items {
		if item.ProductID == nil || item.Quantity <= 0 {
			continue
		}
		unitCost := costs[*item.ProductID]
		if item.PurchasePrice != nil && *item.PurchasePrice > 0 {
			unitCost = *item.PurchasePrice
		}
		if unitCost <= 0 {
			continue
		}
		sale := itemTaxableValue(item) * ratio
		if sale <= 0 {
			continue
		}
		itemProfit := sale - item.Quantity*unitCost
		if itemProfit <= 0 {
			continue
		}
		if itemProfit/sale*100+1e-9 < minMarginPct {
			continue
		}
		profit += itemProfit
	}
	return roundMoney(profit)
}

// loyaltyProductCosts loads current purchase prices for the invoice's
// products (used when an item has no purchase-price snapshot).
func loyaltyProductCosts(tx *gorm.DB, userID uuid.UUID, items []models.InvoiceItem) map[uuid.UUID]float64 {
	ids := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		if item.ProductID != nil {
			ids = append(ids, *item.ProductID)
		}
	}
	costs := make(map[uuid.UUID]float64, len(ids))
	if len(ids) == 0 {
		return costs
	}
	var products []models.Product
	tx.Where("user_id = ? AND id IN ?", userID, ids).Select("id", "purchase_price").Find(&products)
	for _, p := range products {
		costs[p.ID] = p.PurchasePrice
	}
	return costs
}

// loyaltyEarnBase is the amount the earn rate applies to for an invoice:
// the bill total, or the eligible profit in margin mode.
func loyaltyEarnBase(tx *gorm.DB, userID uuid.UUID, settings *models.LoyaltySettings, invoice *models.Invoice) float64 {
	if !IsMarginLoyalty(settings) {
		return invoice.TotalAmount
	}
	costs := loyaltyProductCosts(tx, userID, invoice.Items)
	return computeEligibleMarginProfit(invoice.Items, costs, invoice.InvoiceDiscount+invoice.LoyaltyDiscount, settings.MinMarginPercent)
}

// invoiceLoyaltyEarnCash is the ₹ cashback an invoice earns, plus a note
// naming the matched categories in category mode.
func invoiceLoyaltyEarnCash(tx *gorm.DB, userID uuid.UUID, settings *models.LoyaltySettings, invoice *models.Invoice) (float64, string) {
	if IsCategoryLoyalty(settings) {
		e := invoiceCategoryLoyaltyEarn(tx, userID, settings, invoice)
		return e.Value, e.summary()
	}
	return CalculateEarnCash(settings, loyaltyEarnBase(tx, userID, settings, invoice)), ""
}

// invoiceLoyaltyEarnPoints is the points an invoice earns, plus a note
// naming the matched categories in category mode.
func invoiceLoyaltyEarnPoints(tx *gorm.DB, userID uuid.UUID, settings *models.LoyaltySettings, invoice *models.Invoice) (int64, string) {
	if IsCategoryLoyalty(settings) {
		e := invoiceCategoryLoyaltyEarn(tx, userID, settings, invoice)
		return int64(e.Value), e.summary()
	}
	return CalculateEarnPoints(settings, loyaltyEarnBase(tx, userID, settings, invoice)), ""
}

func earnNote(base, categories string) string {
	if categories == "" {
		return base
	}
	return base + " (" + categories + ")"
}

// IsCashLoyalty reports whether the store rewards customers with ₹ cashback
// instead of points.
func IsCashLoyalty(settings *models.LoyaltySettings) bool {
	return settings != nil && settings.RewardType == LoyaltyRewardCash
}

// CalculateEarnCash returns the ₹ cashback credited for a bill of amount.
func CalculateEarnCash(settings *models.LoyaltySettings, amount float64) float64 {
	if settings == nil || !settings.IsEnabled || amount <= 0 || settings.CashbackPercent <= 0 {
		return 0
	}
	return roundMoney(amount * settings.CashbackPercent / 100)
}

// ComputeLoyaltyCashRedemption validates redeeming amount ₹ of the customer's
// cashback wallet against a bill and returns the discount (== amount).
func ComputeLoyaltyCashRedemption(settings *models.LoyaltySettings, party *models.Party, billTotal float64, amount float64) (float64, error) {
	amount = roundMoney(amount)
	if amount <= 0 {
		return 0, nil
	}
	if settings == nil || !settings.IsEnabled {
		return 0, errors.New("loyalty program is not enabled")
	}
	if !IsCashLoyalty(settings) {
		return 0, errors.New("loyalty program uses points, not cashback")
	}
	if party.PartyType != "customer" {
		return 0, errors.New("loyalty cashback applies to customers only")
	}
	if party.LoyaltyExcluded {
		return 0, errLoyaltyExcluded
	}
	if settings.MinRedeemAmount > 0 && amount < settings.MinRedeemAmount {
		return 0, fmt.Errorf("minimum ₹%.2f cashback required to redeem", settings.MinRedeemAmount)
	}
	if party.LoyaltyCash+0.001 < amount {
		return 0, errors.New("insufficient loyalty cashback balance")
	}
	if billTotal > 0 && settings.MaxRedeemPercent > 0 {
		maxDiscount := billTotal * (settings.MaxRedeemPercent / 100)
		if amount > maxDiscount+0.001 {
			return 0, fmt.Errorf("maximum %.0f%% of bill can be paid with cashback", settings.MaxRedeemPercent)
		}
	}
	if amount > billTotal && billTotal > 0 {
		return 0, errors.New("redemption amount exceeds bill total")
	}
	return amount, nil
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
	if IsCashLoyalty(settings) {
		return 0, errors.New("loyalty program uses cashback, not points")
	}
	if party.PartyType != "customer" {
		return 0, errors.New("loyalty points apply to customers only")
	}
	if party.LoyaltyExcluded {
		return 0, errLoyaltyExcluded
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
		RewardType:      LoyaltyRewardPoints,
	}
	return tx.Create(&entry).Error
}

func recordLoyaltyCashTransaction(tx *gorm.DB, userID, partyID uuid.UUID, txnType string, amount, balanceAfter float64, referenceType string, referenceID *uuid.UUID, referenceNumber, notes string) error {
	entry := models.LoyaltyTransaction{
		ID:                 uuid.New(),
		UserID:             userID,
		PartyID:            partyID,
		TransactionType:    txnType,
		RewardType:         LoyaltyRewardCash,
		Amount:             roundMoney(amount),
		AmountBalanceAfter: roundMoney(balanceAfter),
		ReferenceType:      referenceType,
		ReferenceID:        referenceID,
		ReferenceNumber:    referenceNumber,
		Notes:              notes,
	}
	return tx.Create(&entry).Error
}

// applyLoyaltyCashForInvoice is the cash-mode counterpart of
// applyLoyaltyForInvoice: debits redeemed cashback and credits earned cashback.
func applyLoyaltyCashForInvoice(tx *gorm.DB, userID uuid.UUID, party *models.Party, invoice *models.Invoice, settings *models.LoyaltySettings, cashRedeemed float64) error {
	balance := party.LoyaltyCash
	cashRedeemed = roundMoney(cashRedeemed)

	if cashRedeemed > 0 {
		balance = roundMoney(balance - cashRedeemed)
		if err := recordLoyaltyCashTransaction(tx, userID, party.ID, "redeem", -cashRedeemed, balance, "invoice", &invoice.ID, invoice.InvoiceNumber, "Cashback redeemed on invoice"); err != nil {
			return err
		}
	}

	if invoice.Status != "draft" && invoice.Status != "cancelled" {
		earned, cats := invoiceLoyaltyEarnCash(tx, userID, settings, invoice)
		if earned > 0 {
			balance = roundMoney(balance + earned)
			invoice.LoyaltyCashEarned = earned
			if err := recordLoyaltyCashTransaction(tx, userID, party.ID, "earn", earned, balance, "invoice", &invoice.ID, invoice.InvoiceNumber, earnNote("Cashback earned on invoice", cats)); err != nil {
				return err
			}
		}
	}

	if cashRedeemed == 0 && invoice.LoyaltyCashEarned == 0 {
		return nil
	}

	if err := tx.Model(party).Update("loyalty_cash", balance).Error; err != nil {
		return err
	}
	party.LoyaltyCash = balance
	return tx.Model(invoice).Updates(map[string]interface{}{
		"loyalty_cash_earned":   invoice.LoyaltyCashEarned,
		"loyalty_cash_redeemed": invoice.LoyaltyCashRedeemed,
		"loyalty_discount":      invoice.LoyaltyDiscount,
	}).Error
}

func applyLoyaltyForInvoice(tx *gorm.DB, userID uuid.UUID, party *models.Party, invoice *models.Invoice, settings *models.LoyaltySettings, pointsRedeemed int64) error {
	if party.PartyType != "customer" || party.LoyaltyExcluded || settings == nil || !settings.IsEnabled {
		return nil
	}
	if IsCashLoyalty(settings) {
		return applyLoyaltyCashForInvoice(tx, userID, party, invoice, settings, invoice.LoyaltyCashRedeemed)
	}

	balance := party.LoyaltyPoints

	if pointsRedeemed > 0 {
		balance -= pointsRedeemed
		if err := recordLoyaltyTransaction(tx, userID, party.ID, "redeem", -pointsRedeemed, balance, "invoice", &invoice.ID, invoice.InvoiceNumber, "Redeemed on invoice"); err != nil {
			return err
		}
	}

	if invoice.Status != "draft" && invoice.Status != "cancelled" {
		earnPoints, cats := invoiceLoyaltyEarnPoints(tx, userID, settings, invoice)
		if earnPoints > 0 {
			balance += earnPoints
			invoice.LoyaltyPointsEarned = earnPoints
			if err := recordLoyaltyTransaction(tx, userID, party.ID, "earn", earnPoints, balance, "invoice", &invoice.ID, invoice.InvoiceNumber, earnNote("Earned on invoice", cats)); err != nil {
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

// reverseLoyaltyForInvoice undoes what an invoice did to the customer's
// loyalty balances: earned points/cashback are taken back and redeemed
// points/cashback are given back. action ("deletion", "cancellation") goes
// into the ledger notes. Invoices already reversed (cancelled, then deleted)
// are left alone. The balance may go negative when the customer already
// spent what this invoice earned; that is the honest position and blocks
// redemption until new earnings cover it.
func reverseLoyaltyForInvoice(tx *gorm.DB, userID uuid.UUID, party *models.Party, invoice *models.Invoice, action string) error {
	if party == nil || invoice == nil || party.ID == uuid.Nil || invoice.LoyaltyReversed {
		return nil
	}
	if invoice.LoyaltyPointsEarned == 0 && invoice.LoyaltyPointsRedeemed == 0 &&
		invoice.LoyaltyCashEarned == 0 && invoice.LoyaltyCashRedeemed == 0 {
		return nil
	}
	if err := postInvoiceLoyalty(tx, userID, party, invoice, -1,
		"adjust", "Reversed earned points on invoice "+action, "Reversed earned cashback on invoice "+action,
		"adjust", "Restored redeemed points on invoice "+action, "Restored redeemed cashback on invoice "+action); err != nil {
		return err
	}
	invoice.LoyaltyReversed = true
	return tx.Model(&models.Invoice{}).Where("id = ?", invoice.ID).Update("loyalty_reversed", true).Error
}

// reinstateLoyaltyForInvoice re-applies a reversed invoice's recorded
// loyalty when the invoice is reinstated: redeemed points/cashback are taken
// again and earned points/cashback are credited again.
func reinstateLoyaltyForInvoice(tx *gorm.DB, userID uuid.UUID, party *models.Party, invoice *models.Invoice) error {
	if party == nil || invoice == nil || party.ID == uuid.Nil || !invoice.LoyaltyReversed {
		return nil
	}
	const note = " on invoice reinstatement"
	if err := postInvoiceLoyalty(tx, userID, party, invoice, 1,
		"earn", "Earned points re-credited"+note, "Earned cashback re-credited"+note,
		"redeem", "Redeemed points re-applied"+note, "Redeemed cashback re-applied"+note); err != nil {
		return err
	}
	invoice.LoyaltyReversed = false
	return tx.Model(&models.Invoice{}).Where("id = ?", invoice.ID).Update("loyalty_reversed", false).Error
}

// postInvoiceLoyalty books an invoice's recorded earn and redeem amounts
// against the customer's balances: sign +1 applies them as the original sale
// did (earn credited, redeem debited), -1 reverses them.
func postInvoiceLoyalty(tx *gorm.DB, userID uuid.UUID, party *models.Party, invoice *models.Invoice, sign int,
	earnType, earnPointsNote, earnCashNote, redeemType, redeemPointsNote, redeemCashNote string) error {
	if invoice.LoyaltyPointsEarned != 0 || invoice.LoyaltyPointsRedeemed != 0 {
		balance := party.LoyaltyPoints
		if v := invoice.LoyaltyPointsEarned; v > 0 {
			balance += int64(sign) * v
			if err := recordLoyaltyTransaction(tx, userID, party.ID, earnType, int64(sign)*v, balance,
				"invoice", &invoice.ID, invoice.InvoiceNumber, earnPointsNote); err != nil {
				return err
			}
		}
		if v := invoice.LoyaltyPointsRedeemed; v > 0 {
			balance -= int64(sign) * v
			if err := recordLoyaltyTransaction(tx, userID, party.ID, redeemType, -int64(sign)*v, balance,
				"invoice", &invoice.ID, invoice.InvoiceNumber, redeemPointsNote); err != nil {
				return err
			}
		}
		if err := tx.Model(party).Update("loyalty_points", balance).Error; err != nil {
			return err
		}
		party.LoyaltyPoints = balance
	}
	if invoice.LoyaltyCashEarned != 0 || invoice.LoyaltyCashRedeemed != 0 {
		balance := party.LoyaltyCash
		f := float64(sign)
		if v := invoice.LoyaltyCashEarned; v > 0 {
			balance = roundMoney(balance + f*v)
			if err := recordLoyaltyCashTransaction(tx, userID, party.ID, earnType, f*v, balance,
				"invoice", &invoice.ID, invoice.InvoiceNumber, earnCashNote); err != nil {
				return err
			}
		}
		if v := invoice.LoyaltyCashRedeemed; v > 0 {
			balance = roundMoney(balance - f*v)
			if err := recordLoyaltyCashTransaction(tx, userID, party.ID, redeemType, -f*v, balance,
				"invoice", &invoice.ID, invoice.InvoiceNumber, redeemCashNote); err != nil {
				return err
			}
		}
		if err := tx.Model(party).Update("loyalty_cash", balance).Error; err != nil {
			return err
		}
		party.LoyaltyCash = balance
	}
	return nil
}

// syncInvoiceLoyaltyOnStatusChange reverses an invoice's loyalty when it is
// cancelled and re-applies it when a cancelled invoice is reinstated.
func syncInvoiceLoyaltyOnStatusChange(db *gorm.DB, userID uuid.UUID, invoice *models.Invoice, prevStatus string) error {
	cancelling := prevStatus != "cancelled" && invoice.Status == "cancelled"
	reinstating := prevStatus == "cancelled" && invoice.Status != "cancelled"
	if (!cancelling && !reinstating) || invoice.PartyID == uuid.Nil {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var party models.Party
		if err := tx.Where("user_id = ? AND id = ?", userID, invoice.PartyID).First(&party).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if cancelling {
			return reverseLoyaltyForInvoice(tx, userID, &party, invoice, "cancellation")
		}
		return reinstateLoyaltyForInvoice(tx, userID, &party, invoice)
	})
}

// AdjustPartyLoyaltyCash manually credits (positive) or debits (negative) the
// customer's ₹ cashback wallet.
func AdjustPartyLoyaltyCash(tx *gorm.DB, userID uuid.UUID, party *models.Party, amountDelta float64, notes string) error {
	amountDelta = roundMoney(amountDelta)
	newBalance := roundMoney(party.LoyaltyCash + amountDelta)
	if newBalance < 0 {
		return errors.New("insufficient loyalty cashback balance")
	}
	if err := recordLoyaltyCashTransaction(tx, userID, party.ID, "adjust", amountDelta, newBalance, "manual", nil, "", notes); err != nil {
		return err
	}
	return tx.Model(party).Update("loyalty_cash", newBalance).Error
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

// invoiceLoyaltyPrint is the loyalty block printed on an invoice.
type invoiceLoyaltyPrint struct {
	Redeemed int64
	Discount float64
	Earned   int64
	Balance  int64
	// Cash-mode figures (IsCash == true): ₹ cashback redeemed / credited and
	// the wallet balance right after this invoice.
	IsCash       bool
	CashRedeemed float64
	CashEarned   float64
	CashBalance  float64
}

// invoiceLoyaltyPrintSummary returns the loyalty figures to print on an
// invoice, or nil when the loyalty program is disabled for the invoice owner
// or the invoice party is not a customer. Balance is the customer's points
// balance right after this invoice (falling back to the current balance when
// the invoice has no loyalty ledger entries). Expects invoice.Party preloaded.
func invoiceLoyaltyPrintSummary(invoice models.Invoice) *invoiceLoyaltyPrint {
	if utils.DB == nil || invoice.Party.ID == uuid.Nil || invoice.Party.PartyType != "customer" {
		return nil
	}
	// Excluded customers get no loyalty block unless this invoice predates
	// the exclusion and actually moved their balance.
	if invoice.Party.LoyaltyExcluded && invoice.LoyaltyDiscount == 0 &&
		invoice.LoyaltyPointsEarned == 0 && invoice.LoyaltyCashEarned == 0 {
		return nil
	}
	var settings models.LoyaltySettings
	if err := utils.DB.Where("user_id = ?", invoice.UserID).First(&settings).Error; err != nil || !settings.IsEnabled {
		return nil
	}
	if IsCashLoyalty(&settings) {
		summary := &invoiceLoyaltyPrint{
			IsCash:       true,
			Discount:     invoice.LoyaltyDiscount,
			CashRedeemed: invoice.LoyaltyCashRedeemed,
			CashEarned:   invoice.LoyaltyCashEarned,
			CashBalance:  invoice.Party.LoyaltyCash,
		}
		var last models.LoyaltyTransaction
		if err := utils.DB.
			Where("user_id = ? AND party_id = ? AND reference_type = ? AND reference_id = ? AND reward_type = ?", invoice.UserID, invoice.Party.ID, "invoice", invoice.ID, LoyaltyRewardCash).
			Order("created_at DESC").
			First(&last).Error; err == nil {
			summary.CashBalance = last.AmountBalanceAfter
		}
		return summary
	}
	summary := &invoiceLoyaltyPrint{
		Redeemed: invoice.LoyaltyPointsRedeemed,
		Discount: invoice.LoyaltyDiscount,
		Earned:   invoice.LoyaltyPointsEarned,
		Balance:  invoice.Party.LoyaltyPoints,
	}
	var last models.LoyaltyTransaction
	if err := utils.DB.
		Where("user_id = ? AND party_id = ? AND reference_type = ? AND reference_id = ? AND (reward_type = ? OR reward_type IS NULL OR reward_type = '')", invoice.UserID, invoice.Party.ID, "invoice", invoice.ID, LoyaltyRewardPoints).
		Order("created_at DESC").
		First(&last).Error; err == nil {
		summary.Balance = last.BalanceAfter
	}
	return summary
}
