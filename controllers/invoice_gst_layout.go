package controllers

import (
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/skip2/go-qrcode"
)

// compositionDeclaration is the mandatory declaration composition dealers
// must print prominently on a bill of supply.
const compositionDeclaration = "Composition taxable person, not eligible to collect tax on supplies."

// invoiceDocumentTitle maps invoice_type to the document heading printed on
// GST documents. Unknown/empty types fall back to a tax invoice.
func invoiceDocumentTitle(invoiceType string) string {
	switch strings.ToLower(strings.TrimSpace(invoiceType)) {
	case "bill_of_supply":
		return "BILL OF SUPPLY"
	case "export":
		return "EXPORT INVOICE"
	default:
		return "TAX INVOICE"
	}
}

// financialYearLabel returns the Indian financial year (April–March) for the
// invoice date, e.g. "FY 2026-27".
func financialYearLabel(t time.Time) string {
	start := t.Year()
	if t.Month() < time.April {
		start--
	}
	return fmt.Sprintf("FY %d-%02d", start, (start+1)%100)
}

// itemGrossAmount is qty x rate before any discount.
func itemGrossAmount(item models.InvoiceItem) float64 {
	return item.Quantity * item.UnitPrice
}

// itemTaxableValue is the amount GST is levied on (gross minus discount).
func itemTaxableValue(item models.InvoiceItem) float64 {
	gross := itemGrossAmount(item)
	return gross - gross*(item.Discount/100)
}

// invoiceHasGST reports whether any line item actually carries GST. Billing is
// tax-free by default, so per-item CGST/SGST/IGST columns are only printed
// when they apply.
func invoiceHasGST(invoice models.Invoice) bool {
	if invoice.TaxTotal > 0 {
		return true
	}
	for _, item := range invoice.Items {
		if item.CGST > 0 || item.SGST > 0 || item.IGST > 0 {
			return true
		}
	}
	return false
}

// invoiceHasCess reports whether any line item carries GST cess.
func invoiceHasCess(invoice models.Invoice) bool {
	if invoice.CessTotal > 0 {
		return true
	}
	for _, item := range invoice.Items {
		if item.CessAmount > 0 || item.CessRate > 0 {
			return true
		}
	}
	return false
}

// invoiceTaxableTotal is the total taxable value across line items.
func invoiceTaxableTotal(invoice models.Invoice) float64 {
	return invoice.SubTotal - invoice.DiscountTotal - invoice.InvoiceDiscount
}

// partyStateCode resolves the buyer's GST state code: the stored state_code
// first, then the state prefix embedded in a valid GSTIN.
func partyStateCode(party models.Party) string {
	code := strings.TrimSpace(party.StateCode)
	if code != "" {
		return code
	}
	if code = utils.GetStateCodeFromGSTIN(party.GSTIN); code != "" {
		if _, ok := utils.StateCodes[code]; ok {
			return code
		}
	}
	return stateCodeFromName(party.State)
}

// businessStateCode resolves the seller's GST state code the same way.
func businessStateCode(business *models.Business) string {
	if business == nil {
		return ""
	}
	if code := strings.TrimSpace(business.StateCode); code != "" {
		return code
	}
	if code := utils.GetStateCodeFromGSTIN(business.GSTIN); code != "" {
		if _, ok := utils.StateCodes[code]; ok {
			return code
		}
	}
	return stateCodeFromName(business.State)
}

// stateCodeFromName resolves a GST state code from a state name
// (e.g. "Tamil Nadu" -> "33"), case-insensitive.
func stateCodeFromName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return ""
	}
	for code, stateName := range utils.StateCodes {
		if strings.ToLower(stateName) == n {
			return code
		}
	}
	return ""
}

// stateNameAndCode formats a state as "Maharashtra (Code: 27)"; when only one
// part is known it prints whichever exists, deriving a missing code from the
// state name.
func stateNameAndCode(name, code string) string {
	name = strings.TrimSpace(name)
	code = strings.TrimSpace(code)
	if code == "" {
		code = stateCodeFromName(name)
	}
	if code != "" {
		if mapped := utils.GetStateNameFromCode(code); mapped != "" && name == "" {
			name = mapped
		}
	}
	switch {
	case name != "" && code != "":
		return fmt.Sprintf("%s (Code: %s)", name, code)
	case code != "":
		return fmt.Sprintf("Code: %s", code)
	default:
		return name
	}
}

// placeOfSupplyText renders the place of supply as "Maharashtra (27)" when a
// state code is known, falling back to the buyer's state code, GSTIN prefix
// and state name.
func placeOfSupplyText(invoice models.Invoice) string {
	pos := strings.TrimSpace(invoice.PlaceOfSupply)
	if pos == "" {
		pos = partyStateCode(invoice.Party)
	}
	if pos == "" {
		pos = strings.TrimSpace(invoice.Party.State)
	}
	if pos == "" {
		return ""
	}
	if name := utils.GetStateNameFromCode(pos); name != "" {
		return fmt.Sprintf("%s (%s)", name, pos)
	}
	if code := stateCodeFromName(pos); code != "" {
		return fmt.Sprintf("%s (%s)", pos, code)
	}
	return pos
}

var inrOnes = []string{
	"", "One", "Two", "Three", "Four", "Five", "Six", "Seven", "Eight", "Nine",
	"Ten", "Eleven", "Twelve", "Thirteen", "Fourteen", "Fifteen", "Sixteen",
	"Seventeen", "Eighteen", "Nineteen",
}

var inrTens = []string{
	"", "", "Twenty", "Thirty", "Forty", "Fifty", "Sixty", "Seventy", "Eighty", "Ninety",
}

func inrBelowHundred(n int64) string {
	switch {
	case n < 20:
		return inrOnes[n]
	default:
		word := inrTens[n/10]
		if n%10 > 0 {
			word += " " + inrOnes[n%10]
		}
		return word
	}
}

// numberToWordsINR spells a whole number using the Indian numbering system
// (crore, lakh, thousand, hundred).
func numberToWordsINR(n int64) string {
	if n <= 0 {
		return ""
	}
	var parts []string
	if crore := n / 10000000; crore > 0 {
		parts = append(parts, numberToWordsINR(crore)+" Crore")
		n %= 10000000
	}
	if lakh := n / 100000; lakh > 0 {
		parts = append(parts, inrBelowHundred(lakh)+" Lakh")
		n %= 100000
	}
	if thousand := n / 1000; thousand > 0 {
		parts = append(parts, inrBelowHundred(thousand)+" Thousand")
		n %= 1000
	}
	if hundred := n / 100; hundred > 0 {
		parts = append(parts, inrOnes[hundred]+" Hundred")
		n %= 100
	}
	if n > 0 {
		parts = append(parts, inrBelowHundred(n))
	}
	return strings.Join(parts, " ")
}

// amountInWordsINR renders "Rupees One Lakh Two Thousand and Paise Fifty Only".
func amountInWordsINR(amount float64) string {
	total := math.Round(math.Abs(amount)*100) / 100
	rupees := int64(math.Floor(total))
	paise := int64(math.Round((total - float64(rupees)) * 100))
	if paise >= 100 {
		rupees++
		paise -= 100
	}
	words := numberToWordsINR(rupees)
	if words == "" {
		words = "Zero"
	}
	result := "Rupees " + words
	if paise > 0 {
		result += " and Paise " + numberToWordsINR(paise)
	}
	return result + " Only"
}

// amountWordsRupees renders the myBillBook-style phrasing used on the invoice
// document: "Seven Hundred Rupees" / "... and Fifty Paise".
func amountWordsRupees(amount float64) string {
	total := math.Round(math.Abs(amount)*100) / 100
	rupees := int64(math.Floor(total))
	paise := int64(math.Round((total - float64(rupees)) * 100))
	if paise >= 100 {
		rupees++
		paise -= 100
	}
	words := numberToWordsINR(rupees)
	if words == "" {
		words = "Zero"
	}
	result := words + " Rupees"
	if paise > 0 {
		result += " and " + numberToWordsINR(paise) + " Paise"
	}
	return result
}

// sellerPAN returns the configured PAN when set, else derives it from GSTIN.
func sellerPAN(business *models.Business) string {
	if business == nil {
		return ""
	}
	if pan := strings.ToUpper(strings.TrimSpace(business.PAN)); pan != "" {
		return pan
	}
	return panFromGSTIN(business.GSTIN)
}

// panFromGSTIN extracts the PAN embedded in a 15-character GSTIN
// (characters 3–12), e.g. 33GCXPP7235R1ZC -> GCXPP7235R.
func panFromGSTIN(gstin string) string {
	gstin = strings.ToUpper(strings.TrimSpace(gstin))
	if len(gstin) != 15 {
		return ""
	}
	return gstin[2:12]
}

// formatINR trims a money value to "400" when whole, "370.50" otherwise.
func formatINR(v float64) string {
	v = math.Round(v*100) / 100
	if v == math.Trunc(v) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'f', 2, 64)
}

// invoiceQtyTotal sums line quantities, rendered without a decimal point when
// the total is a whole number.
func invoiceQtyTotal(invoice models.Invoice) string {
	var total float64
	for _, item := range invoice.Items {
		total += item.Quantity
	}
	if total == math.Trunc(total) {
		return strconv.FormatInt(int64(total), 10)
	}
	return strconv.FormatFloat(total, 'f', 2, 64)
}

// itemMRP returns the product's MRP snapshot for the line, 0 when unknown.
func itemMRP(item models.InvoiceItem) float64 {
	return item.Product.MRP
}

// upiQRPNG encodes a upi://pay QR payload (payee address, name, amount, INR)
// as PNG bytes for embedding in invoice documents. Returns nil on failure.
func upiQRPNG(upiID, payeeName string, amount float64) []byte {
	upiID = strings.TrimSpace(upiID)
	if upiID == "" {
		return nil
	}
	payload := fmt.Sprintf("upi://pay?pa=%s&pn=%s&cu=INR",
		url.QueryEscape(upiID), url.QueryEscape(payeeName))
	if amount > 0 {
		payload += fmt.Sprintf("&am=%.2f", amount)
	}
	png, err := qrcode.Encode(payload, qrcode.Medium, 240)
	if err != nil {
		return nil
	}
	return png
}
