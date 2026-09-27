package controllers

import (
	"fmt"
	"html"
	"strings"

	"truerp/models"
)

// additionalChargeRows returns the labelled charge rows for display. For
// legacy documents that only have the aggregate amount, it falls back to a
// single generic row so the rendered totals still reconcile.
func additionalChargeRows(items []models.AdditionalCharge, aggregate float64) []models.AdditionalCharge {
	if len(items) > 0 {
		return items
	}
	if aggregate > 0 {
		return []models.AdditionalCharge{{Label: "Additional Charges", Amount: aggregate}}
	}
	return nil
}

// additionalChargeRowsHTML renders labelled charge rows for the HTML
// document templates (invoice, quotation, purchase bill).
func additionalChargeRowsHTML(items []models.AdditionalCharge, aggregate float64) string {
	var sb strings.Builder
	for _, charge := range additionalChargeRows(items, aggregate) {
		sb.WriteString(fmt.Sprintf(
			`<div class="total-row"><span class="total-label">%s:</span><span class="total-value">₹%.2f</span></div>`,
			html.EscapeString(charge.Label), charge.Amount))
	}
	return sb.String()
}
