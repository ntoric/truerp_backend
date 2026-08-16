package controllers

import (
	"truerp/models"
	"truerp/utils"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func GetPaymentOuts(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var paymentOuts []models.PaymentOut
	query := utils.DB.Where("user_id = ?", userID).Preload("Party").Preload("PurchaseBill")

	if partyID := c.Query("party_id"); partyID != "" {
		query = query.Where("party_id = ?", partyID)
	}
	if purchaseBillID := c.Query("purchase_bill_id"); purchaseBillID != "" {
		query = query.Where("purchase_bill_id = ?", purchaseBillID)
	}

	if err := query.Order("updated_at DESC").Find(&paymentOuts).Error; err != nil {
		// Fall back without preloads so a relation/schema mismatch still returns data.
		fallback := utils.DB.Where("user_id = ?", userID)
		if partyID := c.Query("party_id"); partyID != "" {
			fallback = fallback.Where("party_id = ?", partyID)
		}
		if purchaseBillID := c.Query("purchase_bill_id"); purchaseBillID != "" {
			fallback = fallback.Where("purchase_bill_id = ?", purchaseBillID)
		}
		if err2 := fallback.Order("updated_at DESC").Find(&paymentOuts).Error; err2 != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch payment outs"})
			return
		}
	}

	c.JSON(http.StatusOK, paymentOuts)
}

func CreatePaymentOut(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		PurchaseBillID     *uuid.UUID `json:"purchase_bill_id"`
		PartyID            uuid.UUID  `json:"party_id" binding:"required"`
		AmountPaid         float64    `json:"amount_paid" binding:"required,gt=0"`
		PaymentOutDiscount float64    `json:"payment_out_discount" binding:"gte=0"`
		PaymentOutNumber   string     `json:"payment_out_number"`
		Mode               string     `json:"mode" binding:"required"`
		Date               time.Time  `json:"date" binding:"required"`
		Reference          string     `json:"reference"`
		Notes              string     `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Calculate net amount (amount paid minus discount)
	netAmount := input.AmountPaid - input.PaymentOutDiscount

	paymentOut := models.PaymentOut{
		ID:                   uuid.New(),
		UserID:               userID,
		PurchaseBillID:       input.PurchaseBillID,
		PartyID:              input.PartyID,
		AmountPaid:           input.AmountPaid,
		PaymentOutDiscount:   input.PaymentOutDiscount,
		PaymentOutNumber:     input.PaymentOutNumber,
		Mode:                 input.Mode,
		Date:                 input.Date,
		Reference:            input.Reference,
		Notes:                input.Notes,
	}

	if err := utils.DB.Create(&paymentOut).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create payment out"})
		return
	}

	if err := postStandalonePaymentOutAccounting(utils.DB, userID, &paymentOut, netAmount); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Payment out saved but failed to post to accounting"})
		return
	}

	// Update purchase bill if provided
	if input.PurchaseBillID != nil {
		var bill models.PurchaseBill
		if err := utils.DB.Where("user_id = ? AND id = ?", userID, *input.PurchaseBillID).First(&bill).Error; err == nil {
			newPaid := bill.PaidAmount + netAmount
			status := bill.Status
			if newPaid >= bill.TotalAmount {
				status = "paid"
			} else if newPaid > 0 {
				status = "partial"
			}
			utils.DB.Model(&bill).Updates(map[string]interface{}{
				"paid_amount":  newPaid,
				"balance_due":  bill.TotalAmount - newPaid,
				"status":       status,
			})
		}
	}

	// Update party balance (increase since we paid them)
	var party models.Party
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, input.PartyID).First(&party).Error; err == nil {
		utils.DB.Model(&party).Update("balance", party.Balance+netAmount)
	}

	c.JSON(http.StatusCreated, paymentOut)
}

func DeletePaymentOut(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var paymentOut models.PaymentOut
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&paymentOut).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Payment out not found"})
		return
	}

	// Calculate net amount (amount paid minus discount)
	netAmount := paymentOut.AmountPaid - paymentOut.PaymentOutDiscount

	// Reverse purchase bill payment
	if paymentOut.PurchaseBillID != nil {
		var bill models.PurchaseBill
		if err := utils.DB.Where("user_id = ? AND id = ?", userID, *paymentOut.PurchaseBillID).First(&bill).Error; err == nil {
			newPaid := bill.PaidAmount - netAmount
			if newPaid < 0 {
				newPaid = 0
			}
			status := "unpaid"
			if newPaid > 0 && newPaid < bill.TotalAmount {
				status = "partial"
			} else if newPaid >= bill.TotalAmount {
				status = "paid"
			}
			utils.DB.Model(&bill).Updates(map[string]interface{}{
				"paid_amount":  newPaid,
				"balance_due":  bill.TotalAmount - newPaid,
				"status":       status,
			})
		}
	}

	// Reverse party balance (decrease since we're reversing the payment)
	var party models.Party
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, paymentOut.PartyID).First(&party).Error; err == nil {
		utils.DB.Model(&party).Update("balance", party.Balance-netAmount)
	}

	if err := utils.DB.Delete(&paymentOut).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete payment out"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Payment out deleted successfully"})
}
