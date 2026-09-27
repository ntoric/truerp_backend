package controllers

import (
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

func GetPaymentOuts(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	search := strings.TrimSpace(c.Query("search"))

	query := utils.DB.Model(&models.PaymentOut{}).Where("user_id = ?", userID)

	if partyID := c.Query("party_id"); partyID != "" {
		query = query.Where("party_id = ?", partyID)
	}
	if purchaseBillID := c.Query("purchase_bill_id"); purchaseBillID != "" {
		query = query.Where("purchase_bill_id = ?", purchaseBillID)
	}
	if mode := c.Query("mode"); mode != "" {
		query = query.Where("mode = ?", mode)
	}
	// Day-inclusive range: payment_outs.date is a timestamp.
	if from := c.Query("from"); from != "" {
		query = query.Where(utils.SQLDateGTE("payment_outs.date"), from)
	}
	if to := c.Query("to"); to != "" {
		query = query.Where(utils.SQLDateLTE("payment_outs.date"), to)
	}
	if search != "" {
		like := "%" + strings.ToLower(search) + "%"
		query = query.Where(
			"LOWER(payment_outs.payment_out_number) LIKE ? OR LOWER(payment_outs.reference) LIKE ? OR LOWER(payment_outs.notes) LIKE ? OR LOWER(payment_outs.mode) LIKE ? OR payment_outs.party_id IN (SELECT id FROM parties WHERE LOWER(name) LIKE ?) OR payment_outs.purchase_bill_id IN (SELECT id FROM purchase_bills WHERE LOWER(bill_number) LIKE ?)",
			like, like, like, like, like, like,
		)
	}
	query = query.Order("payment_outs.updated_at DESC")

	paymentOuts := make([]models.PaymentOut, 0)

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
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch payment outs"})
			return
		}

		pageQuery := query.Preload("Party").Preload("PurchaseBill")
		if perPage > 0 {
			pageQuery = pageQuery.Limit(perPage).Offset((page - 1) * perPage)
		}
		if err := pageQuery.Find(&paymentOuts).Error; err != nil {
			// Fall back without preloads so a relation/schema mismatch still returns data.
			fallback := query.Session(&gorm.Session{})
			if perPage > 0 {
				fallback = fallback.Limit(perPage).Offset((page - 1) * perPage)
			}
			if err2 := fallback.Find(&paymentOuts).Error; err2 != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch payment outs"})
				return
			}
		}

		c.JSON(http.StatusOK, gin.H{
			"payment_outs": paymentOuts,
			"total":        total,
			"page":         page,
			"per_page":     perPage,
		})
		return
	}

	if err := query.Preload("Party").Preload("PurchaseBill").Find(&paymentOuts).Error; err != nil {
		// Fall back without preloads so a relation/schema mismatch still returns data.
		if err2 := query.Session(&gorm.Session{}).Find(&paymentOuts).Error; err2 != nil {
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
		ID:                 uuid.New(),
		UserID:             userID,
		PurchaseBillID:     input.PurchaseBillID,
		PartyID:            input.PartyID,
		AmountPaid:         input.AmountPaid,
		PaymentOutDiscount: input.PaymentOutDiscount,
		PaymentOutNumber:   input.PaymentOutNumber,
		Mode:               input.Mode,
		Date:               input.Date,
		Reference:          input.Reference,
		Notes:              input.Notes,
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
				"paid_amount": newPaid,
				"balance_due": bill.TotalAmount - newPaid,
				"status":      status,
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
				"paid_amount": newPaid,
				"balance_due": bill.TotalAmount - newPaid,
				"status":      status,
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
