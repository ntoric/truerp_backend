package controllers

import (
	"truerp/models"
	"truerp/utils"

	"github.com/google/uuid"
)

// invoicePartyBalance is the party's running balance around one invoice, as
// printed when the store enables "Show party balance in invoice" (invoice
// template customization → theme_settings.show_party_balance). Positive =
// the party owes the business; negative = advance held for the party.
type invoicePartyBalance struct {
	Previous float64 // balance just before this invoice
	Current  float64 // balance after this invoice (and the payments against it)
}

// loadInvoicePartyBalance derives the party balance as of the invoice rather
// than today, so reprinting an old invoice shows the figures that applied
// when it was issued. It starts from the stored party balance (the figure on
// the party list) and rolls back every ledger document recorded after this
// invoice — later-dated, or same-day but created later — using the same
// document set as the Party Balance Report. Payments linked to this invoice
// are treated as part of it, so Current reflects what is still due on it.
//
// Returns ok=false when the balance cannot be determined (no party, the party
// is missing, cancelled invoice, no DB).
func loadInvoicePartyBalance(invoice models.Invoice) (invoicePartyBalance, bool) {
	if utils.DB == nil || invoice.PartyID == uuid.Nil || invoice.ID == uuid.Nil || invoice.Status == "cancelled" {
		return invoicePartyBalance{}, false
	}
	db := utils.DB
	var party models.Party
	if err := db.Select("id", "balance").
		Where("user_id = ? AND id = ?", invoice.UserID, invoice.PartyID).
		First(&party).Error; err != nil {
		return invoicePartyBalance{}, false
	}

	// Compare against the invoice row's own stored values so date/timezone
	// handling matches the database exactly.
	const invDay = "(SELECT DATE(date) FROM invoices WHERE id = ?)"
	const invCreated = "(SELECT created_at FROM invoices WHERE id = ?)"

	after := 0.0
	for _, src := range partyBalanceSources {
		dateExpr := "DATE(" + src.dateCol + ")"
		q := db.Model(src.model).
			Select("COALESCE(SUM("+src.amount+"), 0)").
			Where("user_id = ? AND party_id = ?", invoice.UserID, invoice.PartyID).
			Where(src.where, src.args...).
			Where("("+dateExpr+" > "+invDay+" OR ("+dateExpr+" = "+invDay+" AND created_at > "+invCreated+"))",
				invoice.ID, invoice.ID, invoice.ID)
		switch src.model.(type) {
		case *models.Invoice:
			q = q.Where("id <> ?", invoice.ID)
		case *models.Payment:
			q = q.Where("(invoice_id IS NULL OR invoice_id <> ?)", invoice.ID)
		}
		var sum float64
		if err := q.Scan(&sum).Error; err != nil {
			return invoicePartyBalance{}, false
		}
		after += src.sign * sum
	}

	// This invoice's own effect on the stored balance: its receivable leg
	// (drafts are not posted) less the payments recorded against it.
	var linkedPaid float64
	if err := db.Model(&models.Payment{}).
		Select("COALESCE(SUM(amount_received - payment_in_discount), 0)").
		Where("user_id = ? AND party_id = ? AND invoice_id = ? AND COALESCE(status, '') != ?",
			invoice.UserID, invoice.PartyID, invoice.ID, "cancelled").
		Scan(&linkedPaid).Error; err != nil {
		return invoicePartyBalance{}, false
	}
	own := -linkedPaid
	if invoice.Status != "draft" {
		own += invoicePartyDebit(&invoice)
	}

	previous := party.Balance - after - own
	current := previous + own
	if invoice.Status == "draft" {
		// Not yet posted: project what the balance will be once it is.
		current = previous + invoice.TotalAmount - invoice.AmountPaid
	}
	return invoicePartyBalance{Previous: roundMoney(previous), Current: roundMoney(current)}, true
}
