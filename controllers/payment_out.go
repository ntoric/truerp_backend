package controllers

import (
	"fmt"
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
	if invoiceID := c.Query("invoice_id"); invoiceID != "" {
		query = query.Where("invoice_id = ?", invoiceID)
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
			"LOWER(payment_outs.payment_out_number) LIKE ? OR LOWER(payment_outs.reference) LIKE ? OR LOWER(payment_outs.notes) LIKE ? OR LOWER(payment_outs.mode) LIKE ? OR payment_outs.party_id IN (SELECT id FROM parties WHERE LOWER(name) LIKE ?) OR payment_outs.purchase_bill_id IN (SELECT id FROM purchase_bills WHERE LOWER(bill_number) LIKE ?) OR payment_outs.sales_return_id IN (SELECT id FROM sales_returns WHERE LOWER(return_number) LIKE ?) OR payment_outs.credit_note_id IN (SELECT id FROM credit_notes WHERE LOWER(credit_note_number) LIKE ?) OR payment_outs.invoice_id IN (SELECT id FROM invoices WHERE LOWER(invoice_number) LIKE ?)",
			like, like, like, like, like, like, like, like, like,
		)
	}
	// Order by payment date — covered by idx_payment_outs_user_date, unlike
	// updated_at which forces a full sort of the user's rows.
	query = query.Order("payment_outs.date DESC, payment_outs.created_at DESC")

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

		pageQuery := query.Preload("Party").Preload("PurchaseBill").Preload("SalesReturn").Preload("CreditNote").Preload("Invoice")
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

	if err := query.Preload("Party").Preload("PurchaseBill").Preload("SalesReturn").Preload("CreditNote").Preload("Invoice").Find(&paymentOuts).Error; err != nil {
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
		SalesReturnID      *uuid.UUID `json:"sales_return_id"`
		CreditNoteID       *uuid.UUID `json:"credit_note_id"`
		InvoiceID          *uuid.UUID `json:"invoice_id"`
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
	if netAmount <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Amount paid must be greater than the discount"})
		return
	}
	mode := normalizePaymentMethod(input.Mode)

	paymentOut := models.PaymentOut{
		ID:                 uuid.New(),
		UserID:             userID,
		PurchaseBillID:     input.PurchaseBillID,
		SalesReturnID:      input.SalesReturnID,
		CreditNoteID:       input.CreditNoteID,
		InvoiceID:          input.InvoiceID,
		PartyID:            input.PartyID,
		AmountPaid:         input.AmountPaid,
		PaymentOutDiscount: input.PaymentOutDiscount,
		PaymentOutNumber:   input.PaymentOutNumber,
		Mode:               mode,
		Date:               input.Date,
		Reference:          input.Reference,
		Notes:              input.Notes,
	}

	// Load the linked bill up front — bill edits locate payment-out cash
	// transactions by the bill number, so the cash row must carry it. An
	// unknown or mismatched bill must fail loudly: saving the payment anyway
	// would leave it looking linked while the bill's paid amount and status
	// never update.
	var linkedBill models.PurchaseBill
	if input.PurchaseBillID != nil && *input.PurchaseBillID != uuid.Nil {
		if err := utils.DB.Where("user_id = ? AND id = ?", userID, *input.PurchaseBillID).First(&linkedBill).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid purchase bill"})
			return
		}
		if linkedBill.PartyID != input.PartyID {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Purchase bill does not belong to the selected vendor"})
			return
		}
	}

	// The same goes for a linked sales return or credit note — the payment
	// documents a refund owed to that party, so a mismatch corrupts both
	// the document trail and the party balance.
	var linkedReturn models.SalesReturn
	if input.SalesReturnID != nil && *input.SalesReturnID != uuid.Nil {
		if err := utils.DB.Where("user_id = ? AND id = ?", userID, *input.SalesReturnID).First(&linkedReturn).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid sales return"})
			return
		}
		if linkedReturn.PartyID != input.PartyID {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Sales return does not belong to the selected party"})
			return
		}
		if paymentOut.Reference == "" {
			paymentOut.Reference = linkedReturn.ReturnNumber
		}
	}
	var linkedCreditNote models.CreditNote
	if input.CreditNoteID != nil && *input.CreditNoteID != uuid.Nil {
		if err := utils.DB.Where("user_id = ? AND id = ?", userID, *input.CreditNoteID).First(&linkedCreditNote).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid credit note"})
			return
		}
		if linkedCreditNote.PartyID != input.PartyID {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Credit note does not belong to the selected party"})
			return
		}
		if paymentOut.Reference == "" {
			paymentOut.Reference = linkedCreditNote.CreditNoteNumber
		}
	}

	// A linked sales invoice means the payment refunds money the customer
	// already paid — the standalone fallback for refunds when no sales
	// return document exists. Same validation as the other linked docs.
	// It settles in money like a money-mode return refund: the invoice and
	// the party balance stay untouched.
	var linkedInvoice models.Invoice
	if input.InvoiceID != nil && *input.InvoiceID != uuid.Nil {
		if err := utils.DB.Where("user_id = ? AND id = ?", userID, *input.InvoiceID).First(&linkedInvoice).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid invoice"})
			return
		}
		if linkedInvoice.PartyID != input.PartyID {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invoice does not belong to the selected party"})
			return
		}
		if paymentOut.Reference == "" {
			paymentOut.Reference = linkedInvoice.InvoiceNumber
		}
	}

	// A payment linked to a money-settled sales return is the refund itself:
	// it mirrors the auto-created refund payment-outs, which settle in money
	// and never move the party balance. An invoice-linked payment is the
	// same kind of money refund — it just lacks the return document.
	returnRefundInMoney := linkedReturn.ID != uuid.Nil && refundSettlesInMoney(linkedReturn.RefundMode)
	invoiceRefund := linkedInvoice.ID != uuid.Nil

	// Resolve the destination account up front. Initial-investment payments
	// settle against owner's equity and never touch cash or bank.
	var accountID *uuid.UUID
	if !isInitialInvestmentPayment(mode) && netAmount > 0 {
		resolved, err := resolveBankAccountForPaymentMode(userID, mode, nil)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid bank account for payment method"})
			return
		}
		accountID = resolved
	}

	if err := utils.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&paymentOut).Error; err != nil {
			return err
		}

		// Money-settled refunds post the refund journal (revenue down) like
		// the auto-created ones — that covers both sales-return refunds and
		// refunds tagged straight to a sales invoice. Other payment-outs
		// stay on the generic payable leg.
		switch {
		case returnRefundInMoney:
			if err := postSalesReturnRefundAccounting(tx, userID, &paymentOut, netAmount); err != nil {
				return err
			}
		case invoiceRefund:
			if err := postInvoiceRefundAccounting(tx, userID, &paymentOut, &linkedInvoice, netAmount); err != nil {
				return err
			}
		default:
			if err := postStandalonePaymentOutAccounting(tx, userID, &paymentOut, netAmount); err != nil {
				return err
			}
		}

		// Record the cash/bank movement so the payment out shows in Cash &
		// Bank.
		if !isInitialInvestmentPayment(mode) && netAmount > 0 {
			ref := paymentOutCashRef(&paymentOut)
			cashRef := ref
			desc := fmt.Sprintf("Payment out %s", ref)
			if linkedBill.ID != uuid.Nil {
				cashRef = linkedBill.BillNumber
				desc = fmt.Sprintf("Payment out %s for purchase %s", ref, linkedBill.BillNumber)
			} else if linkedReturn.ID != uuid.Nil {
				cashRef = linkedReturn.ReturnNumber
				desc = fmt.Sprintf("Payment out %s for sales return %s", ref, linkedReturn.ReturnNumber)
			} else if linkedCreditNote.ID != uuid.Nil {
				cashRef = linkedCreditNote.CreditNoteNumber
				desc = fmt.Sprintf("Payment out %s for credit note %s", ref, linkedCreditNote.CreditNoteNumber)
			} else if linkedInvoice.ID != uuid.Nil {
				cashRef = linkedInvoice.InvoiceNumber
				desc = fmt.Sprintf("Payment out %s for sales invoice %s", ref, linkedInvoice.InvoiceNumber)
			}
			if err := recordPaymentCashTxn(tx, userID, accountID, "out", netAmount, input.Date, cashRef, desc); err != nil {
				return err
			}
		}

		// Update purchase bill if provided
		if linkedBill.ID != uuid.Nil {
			newPaid := linkedBill.PaidAmount + netAmount
			status := linkedBill.Status
			if linkedBill.TotalAmount > 0 && newPaid+0.01 >= linkedBill.TotalAmount {
				status = "paid"
			} else if newPaid > 0 {
				status = "partial"
			}
			balanceDue := linkedBill.TotalAmount - newPaid
			if balanceDue < 0 {
				balanceDue = 0
			}
			if err := tx.Model(&linkedBill).Updates(map[string]interface{}{
				"paid_amount": newPaid,
				"balance_due": balanceDue,
				"status":      status,
			}).Error; err != nil {
				return err
			}
		}

		// Update party balance (increase since we paid them). Money-settled
		// refunds skip this — the refund settled in money, matching the
		// auto-created refund payment-outs and their delete path. The same
		// goes for a refund tagged straight to a sales invoice.
		if !returnRefundInMoney && !invoiceRefund {
			var party models.Party
			if err := tx.Where("user_id = ? AND id = ?", userID, input.PartyID).First(&party).Error; err == nil {
				if err := tx.Model(&party).Update("balance", party.Balance+netAmount).Error; err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create payment out"})
		return
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

	if err := utils.DB.Transaction(func(tx *gorm.DB) error {
		// Reverse the standalone journal and the linked cash/bank ledger row.
		if err := reverseAccountingByRef(tx, userID, "payment_out_record", paymentOut.ID); err != nil {
			return err
		}
		txns, err := findPaymentCashTransactions(tx, userID, "out", paymentOutCashRef(&paymentOut))
		if err != nil {
			return err
		}
		for _, txn := range txns {
			if err := reverseCashReduceTransaction(tx, userID, txn); err != nil {
				return err
			}
		}

		// Reverse purchase bill payment
		if paymentOut.PurchaseBillID != nil {
			var bill models.PurchaseBill
			if err := tx.Where("user_id = ? AND id = ?", userID, *paymentOut.PurchaseBillID).First(&bill).Error; err == nil {
				newPaid := bill.PaidAmount - netAmount
				if newPaid < 0 {
					newPaid = 0
				}
				status := "unpaid"
				if bill.TotalAmount > 0 && newPaid+0.01 >= bill.TotalAmount {
					status = "paid"
				} else if newPaid > 0 {
					status = "partial"
				}
				if err := tx.Model(&bill).Updates(map[string]interface{}{
					"paid_amount": newPaid,
					"balance_due": bill.TotalAmount - newPaid,
					"status":      status,
				}).Error; err != nil {
					return err
				}
			}
		}

		// Reverse party balance (decrease since we're reversing the payment).
		// Payments that settled a refund in money — a money-mode sales return
		// refund or a refund tagged to a sales invoice — never moved the
		// party balance, so there is nothing to reverse. All other payments
		// (including ones linked to a credit-settled return or a credit
		// note) did.
		skipBalanceReversal := paymentOut.InvoiceID != nil
		if paymentOut.SalesReturnID != nil {
			var salesReturn models.SalesReturn
			if err := tx.Where("user_id = ? AND id = ?", userID, *paymentOut.SalesReturnID).First(&salesReturn).Error; err == nil {
				skipBalanceReversal = skipBalanceReversal || refundSettlesInMoney(salesReturn.RefundMode)
			}
		}
		if !skipBalanceReversal {
			var party models.Party
			if err := tx.Where("user_id = ? AND id = ?", userID, paymentOut.PartyID).First(&party).Error; err == nil {
				if err := tx.Model(&party).Update("balance", party.Balance-netAmount).Error; err != nil {
					return err
				}
			}
		}

		return tx.Delete(&paymentOut).Error
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete payment out"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Payment out deleted successfully"})
}
