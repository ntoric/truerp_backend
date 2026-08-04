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

func GetPayments(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var payments []models.Payment
	query := utils.DB.Where("user_id = ?", userID).Preload("Party")

	if invoiceID := c.Query("invoice_id"); invoiceID != "" {
		query = query.Where("invoice_id = ?", invoiceID)
	}
	if partyID := c.Query("party_id"); partyID != "" {
		query = query.Where("party_id = ?", partyID)
	}

	if err := query.Order("date DESC").Find(&payments).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch payments"})
		return
	}

	c.JSON(http.StatusOK, payments)
}

func CreatePayment(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	userName := ""
	if name, exists := c.Get("user_name"); exists {
		userName = name.(string)
	}

	var input struct {
		InvoiceID         *uuid.UUID `json:"invoice_id"`
		PartyID           uuid.UUID  `json:"party_id" binding:"required"`
		AmountReceived    float64    `json:"amount_received" binding:"required,gt=0"`
		PaymentInDiscount float64    `json:"payment_in_discount" binding:"required"`
		PaymentInNumber   string     `json:"payment_in_number"`
		Mode              string     `json:"mode" binding:"required"`
		Date              time.Time  `json:"date" binding:"required"`
		Reference         string     `json:"reference"`
		Notes             string     `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Validate party
	var party models.Party
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, input.PartyID).First(&party).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid party"})
		return
	}

	// Calculate net amount (amount received minus discount)
	netAmount := input.AmountReceived - input.PaymentInDiscount

	payment := models.Payment{
		ID:                uuid.New(),
		UserID:            userID,
		InvoiceID:         input.InvoiceID,
		PartyID:           input.PartyID,
		AmountReceived:    input.AmountReceived,
		PaymentInDiscount: input.PaymentInDiscount,
		PaymentInNumber:   input.PaymentInNumber,
		Mode:              input.Mode,
		Date:              input.Date,
		Reference:         input.Reference,
		Notes:             input.Notes,
	}

	if err := utils.DB.Create(&payment).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create payment"})
		return
	}

	if err := postStandalonePaymentInAccounting(utils.DB, userID, &payment, netAmount); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Payment saved but failed to post to accounting"})
		return
	}

	// Update invoice if provided
	if input.InvoiceID != nil {
		var invoice models.Invoice
		if err := utils.DB.Where("user_id = ? AND id = ?", userID, *input.InvoiceID).First(&invoice).Error; err == nil {
			newPaid := invoice.AmountPaid + netAmount
			status := invoice.Status
			if newPaid >= invoice.TotalAmount {
				status = "paid"
			}
			utils.DB.Model(&invoice).Updates(map[string]interface{}{
				"amount_paid": newPaid,
				"status":      status,
			})
		}
	}

	// Update party balance
	utils.DB.Model(&party).Update("balance", party.Balance-netAmount)

	// Log payment creation
	CreateAuditLog(
		userID,
		userName,
		"create",
		"payment",
		&payment.ID,
		payment.PaymentInNumber,
		fmt.Sprintf("Received payment: %.2f from %s via %s", netAmount, party.Name, input.Mode),
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		map[string]interface{}{
			"party_id":    input.PartyID,
			"party_name":  party.Name,
			"amount":      netAmount,
			"mode":        input.Mode,
			"invoice_id":  input.InvoiceID,
		},
		"success",
		"",
	)

	c.JSON(http.StatusCreated, payment)
}

func DeletePayment(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	userName := ""
	if name, exists := c.Get("user_name"); exists {
		userName = name.(string)
	}
	id := c.Param("id")

	var payment models.Payment
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&payment).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Payment not found"})
		return
	}

	// Calculate net amount (amount received minus discount)
	netAmount := payment.AmountReceived - payment.PaymentInDiscount

	// Reverse invoice payment
	if payment.InvoiceID != nil {
		var invoice models.Invoice
		if err := utils.DB.Where("user_id = ? AND id = ?", userID, *payment.InvoiceID).First(&invoice).Error; err == nil {
			newPaid := invoice.AmountPaid - netAmount
			if newPaid < 0 {
				newPaid = 0
			}
			status := "sent"
			if newPaid <= 0 {
				status = "sent"
			}
			utils.DB.Model(&invoice).Updates(map[string]interface{}{
				"amount_paid": newPaid,
				"status":      status,
			})
		}
	}

	// Reverse party balance
	var party models.Party
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, payment.PartyID).First(&party).Error; err == nil {
		utils.DB.Model(&party).Update("balance", party.Balance+netAmount)
	}

	if err := utils.DB.Delete(&payment).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete payment"})
		return
	}

	// Log payment deletion
	CreateAuditLog(
		userID,
		userName,
		"delete",
		"payment",
		&payment.ID,
		payment.PaymentInNumber,
		fmt.Sprintf("Deleted payment: %.2f", netAmount),
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		map[string]interface{}{
			"amount":     netAmount,
			"invoice_id": payment.InvoiceID,
		},
		"success",
		"",
	)

	c.JSON(http.StatusOK, gin.H{"message": "Payment deleted successfully"})
}
