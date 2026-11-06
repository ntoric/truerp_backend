package controllers

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// DiscountTransaction is a unified row in the discount history report. Each
// row is one document that carries a discount: a sales invoice (item-level
// discount_total + invoice_discount + loyalty_discount), an estimate, a
// purchase bill (bill-level + per-item percent discounts), a payment-in, or a
// payment-out.
type DiscountTransaction struct {
	ID               string    `json:"id"`
	Type             string    `json:"type"`
	TypeLabel        string    `json:"type_label"`
	Reference        string    `json:"reference"`
	Date             time.Time `json:"date"`
	PartyName        string    `json:"party_name"`
	ItemDiscount     float64   `json:"item_discount"`
	DocumentDiscount float64   `json:"document_discount"`
	TotalDiscount    float64   `json:"total_discount"`
	DocumentTotal    float64   `json:"document_total"`
}

var discountTransactionTypes = map[string]string{
	"sale_invoice":  "Sale Invoice",
	"estimate":      "Estimate",
	"purchase_bill": "Purchase Bill",
	"payment_in":    "Payment In",
	"payment_out":   "Payment Out",
}

// GetDiscountTransactions returns the discount transaction history for the
// active store: every document where a discount was granted. Supports
// ?type=, ?from=, ?to=, ?search= filters and ?page=/&per_page= pagination
// (per_page <= 0 returns all rows).
func GetDiscountTransactions(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	typeFilter := strings.TrimSpace(c.Query("type"))
	search := strings.ToLower(strings.TrimSpace(c.Query("search")))
	from := strings.TrimSpace(c.Query("from"))
	to := strings.TrimSpace(c.Query("to"))

	wants := func(t string) bool { return typeFilter == "" || typeFilter == t }
	rows := make([]DiscountTransaction, 0)

	// Sales invoices — item discounts + invoice-level + loyalty discounts.
	if wants("sale_invoice") {
		var invoices []models.Invoice
		query := utils.DB.Preload("Party").Model(&models.Invoice{}).
			Where("user_id = ?", userID).
			Where("(discount_total > 0 OR invoice_discount > 0 OR loyalty_discount > 0)")
		if from != "" {
			query = query.Where(utils.SQLDateGTE("date"), from)
		}
		if to != "" {
			query = query.Where(utils.SQLDateLTE("date"), to)
		}
		if err := query.Find(&invoices).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load discount history"})
			return
		}
		for _, inv := range invoices {
			docDiscount := inv.InvoiceDiscount + inv.LoyaltyDiscount
			rows = append(rows, DiscountTransaction{
				ID:               "sale_invoice:" + inv.ID.String(),
				Type:             "sale_invoice",
				TypeLabel:        discountTransactionTypes["sale_invoice"],
				Reference:        inv.InvoiceNumber,
				Date:             inv.Date,
				PartyName:        inv.Party.Name,
				ItemDiscount:     inv.DiscountTotal,
				DocumentDiscount: docDiscount,
				TotalDiscount:    inv.DiscountTotal + docDiscount,
				DocumentTotal:    inv.TotalAmount,
			})
		}
	}

	// Estimates (quotations) — item discounts + document-level discount.
	if wants("estimate") {
		var quotes []models.Quotation
		query := utils.DB.Preload("Party").Model(&models.Quotation{}).
			Where("user_id = ?", userID).
			Where("(discount_total > 0 OR quotation_discount > 0)")
		if from != "" {
			query = query.Where(utils.SQLDateGTE("date"), from)
		}
		if to != "" {
			query = query.Where(utils.SQLDateLTE("date"), to)
		}
		if err := query.Find(&quotes).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load discount history"})
			return
		}
		for _, q := range quotes {
			rows = append(rows, DiscountTransaction{
				ID:               "estimate:" + q.ID.String(),
				Type:             "estimate",
				TypeLabel:        discountTransactionTypes["estimate"],
				Reference:        q.QuotationNumber,
				Date:             q.Date,
				PartyName:        q.Party.Name,
				ItemDiscount:     q.DiscountTotal,
				DocumentDiscount: q.QuotationDiscount,
				TotalDiscount:    q.DiscountTotal + q.QuotationDiscount,
				DocumentTotal:    q.TotalAmount,
			})
		}
	}

	// Purchase bills — bill-level discount + per-item percent discounts.
	if wants("purchase_bill") {
		var bills []models.PurchaseBill
		query := utils.DB.Preload("Party").Model(&models.PurchaseBill{}).
			Where("user_id = ?", userID).
			Where("invoice_discount > 0 OR id IN (SELECT bill_id FROM purchase_bill_items WHERE discount > 0)")
		if from != "" {
			query = query.Where(utils.SQLDateGTE("bill_date"), from)
		}
		if to != "" {
			query = query.Where(utils.SQLDateLTE("bill_date"), to)
		}
		if err := query.Find(&bills).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load discount history"})
			return
		}
		billIDs := make([]uuid.UUID, 0, len(bills))
		for _, b := range bills {
			billIDs = append(billIDs, b.ID)
		}
		itemDiscounts := map[uuid.UUID]float64{}
		if len(billIDs) > 0 {
			var sums []struct {
				BillID uuid.UUID `gorm:"column:bill_id"`
				Amount float64   `gorm:"column:amount"`
			}
			if err := utils.DB.Model(&models.PurchaseBillItem{}).
				Select("bill_id, SUM(quantity * unit_price * (discount / 100.0)) AS amount").
				Where("bill_id IN ?", billIDs).
				Group("bill_id").
				Scan(&sums).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load discount history"})
				return
			}
			for _, s := range sums {
				itemDiscounts[s.BillID] = s.Amount
			}
		}
		for _, b := range bills {
			itemDisc := itemDiscounts[b.ID]
			rows = append(rows, DiscountTransaction{
				ID:               "purchase_bill:" + b.ID.String(),
				Type:             "purchase_bill",
				TypeLabel:        discountTransactionTypes["purchase_bill"],
				Reference:        b.BillNumber,
				Date:             b.BillDate,
				PartyName:        b.Party.Name,
				ItemDiscount:     itemDisc,
				DocumentDiscount: b.InvoiceDiscount,
				TotalDiscount:    itemDisc + b.InvoiceDiscount,
				DocumentTotal:    b.TotalAmount,
			})
		}
	}

	// Payments in — settlement discounts recorded on received payments.
	if wants("payment_in") {
		var payments []models.Payment
		query := utils.DB.Preload("Party").Preload("Invoice").Model(&models.Payment{}).
			Where("user_id = ?", userID).
			Where("payment_in_discount > 0").
			Where("COALESCE(status, '') != ?", "cancelled")
		if from != "" {
			query = query.Where(utils.SQLDateGTE("date"), from)
		}
		if to != "" {
			query = query.Where(utils.SQLDateLTE("date"), to)
		}
		if err := query.Find(&payments).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load discount history"})
			return
		}
		for _, p := range payments {
			reference := p.PaymentInNumber
			if reference == "" && p.Invoice != nil {
				reference = p.Invoice.InvoiceNumber
			}
			rows = append(rows, DiscountTransaction{
				ID:               "payment_in:" + p.ID.String(),
				Type:             "payment_in",
				TypeLabel:        discountTransactionTypes["payment_in"],
				Reference:        reference,
				Date:             p.Date,
				PartyName:        p.Party.Name,
				DocumentDiscount: p.PaymentInDiscount,
				TotalDiscount:    p.PaymentInDiscount,
				DocumentTotal:    p.AmountReceived,
			})
		}
	}

	// Payments out — settlement discounts recorded on outgoing payments.
	if wants("payment_out") {
		var paymentOuts []models.PaymentOut
		query := utils.DB.Preload("Party").Preload("PurchaseBill").Model(&models.PaymentOut{}).
			Where("user_id = ?", userID).
			Where("payment_out_discount > 0").
			Where("COALESCE(status, '') != ?", "cancelled")
		if from != "" {
			query = query.Where(utils.SQLDateGTE("date"), from)
		}
		if to != "" {
			query = query.Where(utils.SQLDateLTE("date"), to)
		}
		if err := query.Find(&paymentOuts).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load discount history"})
			return
		}
		for _, p := range paymentOuts {
			reference := p.PaymentOutNumber
			if reference == "" && p.PurchaseBill != nil {
				reference = p.PurchaseBill.BillNumber
			}
			rows = append(rows, DiscountTransaction{
				ID:               "payment_out:" + p.ID.String(),
				Type:             "payment_out",
				TypeLabel:        discountTransactionTypes["payment_out"],
				Reference:        reference,
				Date:             p.Date,
				PartyName:        p.Party.Name,
				DocumentDiscount: p.PaymentOutDiscount,
				TotalDiscount:    p.PaymentOutDiscount,
				DocumentTotal:    p.AmountPaid,
			})
		}
	}

	// Search matches the document reference or party name.
	if search != "" {
		filtered := rows[:0]
		for _, r := range rows {
			if strings.Contains(strings.ToLower(r.Reference), search) ||
				strings.Contains(strings.ToLower(r.PartyName), search) {
				filtered = append(filtered, r)
			}
		}
		rows = filtered
	}

	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].Date.After(rows[j].Date)
	})

	var totalDiscount float64
	for _, r := range rows {
		totalDiscount += r.TotalDiscount
	}

	total := len(rows)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	perPage, _ := strconv.Atoi(c.DefaultQuery("per_page", "25"))
	paged := rows
	if perPage > 0 {
		start := (page - 1) * perPage
		if start > total {
			start = total
		}
		end := start + perPage
		if end > total {
			end = total
		}
		paged = rows[start:end]
	}

	c.JSON(http.StatusOK, gin.H{
		"transactions": paged,
		"total":        total,
		"page":         page,
		"per_page":     perPage,
		"summary": gin.H{
			"count":          total,
			"total_discount": totalDiscount,
		},
	})
}
