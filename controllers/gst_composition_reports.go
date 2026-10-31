package controllers

// GST Composition report computation: sales/purchase registers and
// summaries, composition tax liability, CMP-08 quarterly working paper,
// GSTR-4 annual working paper and the dashboard.
//
// Money rules: all reported amounts are pre-tax (taxable value) unless the
// field name says otherwise. Aggregation happens on raw float64 values read
// from the billing documents and is rounded to 2 decimals once per output
// figure via roundMoney — line items are never individually rounded before
// summation.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// ---- Register row types ---------------------------------------------------

type gstSalesRegisterRow struct {
	FinancialYear   string    `json:"financial_year"`
	TaxPeriod       string    `json:"tax_period"`
	InvoiceNumber   string    `json:"invoice_number"`
	InvoiceDate     time.Time `json:"invoice_date"`
	InvoiceStatus   string    `json:"invoice_status"`
	DocumentType    string    `json:"document_type"` // sales_invoice, sales_return, credit_note
	CustomerName    string    `json:"customer_name"`
	CustomerGSTIN   string    `json:"customer_gstin"`
	CustomerState   string    `json:"customer_state"`
	PlaceOfSupply   string    `json:"place_of_supply"`
	TransactionType string    `json:"transaction_type"` // B2B, B2C
	GrossAmount     float64   `json:"gross_amount"`
	DiscountAmount  float64   `json:"discount_amount"`
	ReturnAmount    float64   `json:"return_amount"`
	NetAmount       float64   `json:"net_amount"`
	TaxableValue    float64   `json:"taxable_value"`
	ExemptValue     float64   `json:"exempt_value"`
	NilRatedValue   float64   `json:"nil_rated_value"`
	NonGSTValue     float64   `json:"non_gst_value"`
	TaxAmount       float64   `json:"tax_amount"`
	// SupplyClassification: taxable, nil_rated, exempt, non_gst, mixed, cancelled.
	SupplyClassification string `json:"supply_classification"`
	// CompositionTurnover: included, excluded, review (e.g. interstate).
	CompositionTurnover string `json:"composition_turnover"`
	IsInterState        bool   `json:"is_inter_state"`
	// IncludedInTotals is false for cancelled/draft docs kept for audit.
	IncludedInTotals bool      `json:"included_in_totals"`
	SourceType       string    `json:"source_type"`
	SourceID         uuid.UUID `json:"source_id"`
}

type gstPurchaseRegisterRow struct {
	FinancialYear    string    `json:"financial_year"`
	TaxPeriod        string    `json:"tax_period"`
	InvoiceNumber    string    `json:"purchase_invoice_number"`
	InvoiceDate      time.Time `json:"purchase_date"`
	DocStatus        string    `json:"status"`
	DocumentType     string    `json:"document_type"` // purchase_bill, purchase_return, debit_note
	SupplierName     string    `json:"supplier_name"`
	SupplierGSTIN    string    `json:"supplier_gstin"`
	// SupplierRegistration: registered, unregistered.
	SupplierRegistration string `json:"supplier_registration"`
	SupplierState        string `json:"supplier_state"`
	PlaceOfSupply        string `json:"place_of_supply"`
	PurchaseCategory     string `json:"purchase_category"`
	GrossAmount          float64 `json:"gross_amount"`
	DiscountAmount       float64 `json:"discount_amount"`
	ReturnAmount         float64 `json:"return_amount"`
	NetAmount            float64 `json:"net_amount"`
	TaxableValue         float64 `json:"taxable_value"`
	ExemptValue          float64 `json:"exempt_value"`
	CGSTAmount           float64 `json:"cgst_amount"`
	SGSTAmount           float64 `json:"sgst_amount"`
	IGSTAmount           float64 `json:"igst_amount"`
	ReverseCharge        bool    `json:"reverse_charge"`
	RCMTaxableValue      float64 `json:"rcm_taxable_value"`
	RCMTaxAmount         float64 `json:"rcm_tax_amount"`
	IsInterState         bool    `json:"is_inter_state"`
	IncludedInTotals     bool    `json:"included_in_totals"`
	SourceType           string  `json:"source_type"`
	SourceID             uuid.UUID `json:"source_id"`
}

// ---- Summary types --------------------------------------------------------

type gstSalesSummary struct {
	Period                     string  `json:"period"`
	InvoiceCount               int     `json:"invoice_count"`
	CancelledInvoiceCount      int     `json:"cancelled_invoice_count"`
	GrossSales                 float64 `json:"gross_sales"`
	DiscountAmount             float64 `json:"discount_amount"`
	SalesReturnAmount          float64 `json:"sales_return_amount"`
	NetSales                   float64 `json:"net_sales"`
	TaxableSupplyValue         float64 `json:"taxable_supply_value"`
	ExemptSupplyValue          float64 `json:"exempt_supply_value"`
	NilRatedSupplyValue        float64 `json:"nil_rated_supply_value"`
	NonGSTSupplyValue          float64 `json:"non_gst_supply_value"`
	CompositionTurnoverBasis   float64 `json:"composition_turnover_basis"`
	InterStateOutwardValue     float64 `json:"interstate_outward_supply_value"`
	CreditNoteValue            float64 `json:"credit_note_value"`
	DebitNoteValue             float64 `json:"debit_note_value"`
}

type gstPurchaseSummary struct {
	Period                          string  `json:"period"`
	PurchaseInvoiceCount            int     `json:"purchase_invoice_count"`
	RegisteredSupplierPurchaseValue float64 `json:"registered_supplier_purchase_value"`
	UnregisteredSupplierPurchaseValue float64 `json:"unregistered_supplier_purchase_value"`
	PurchaseReturnValue             float64 `json:"purchase_return_value"`
	PurchaseDiscountValue           float64 `json:"purchase_discount_value"`
	IntraStatePurchaseValue         float64 `json:"intrastate_purchase_value"`
	InterStatePurchaseValue         float64 `json:"interstate_purchase_value"`
	TaxablePurchaseValue            float64 `json:"taxable_purchase_value"`
	ExemptPurchaseValue             float64 `json:"exempt_purchase_value"`
	SupplierGSTAmount               float64 `json:"supplier_gst_amount"`
	RCMTaxableValue                 float64 `json:"rcm_taxable_value"`
	RCMTaxLiability                 float64 `json:"rcm_tax_liability"`
}

// gstTaxLiability is the composition levy computation output.
type gstTaxLiability struct {
	TurnoverBasis      float64 `json:"turnover_basis"`
	CompositionRate    float64 `json:"composition_rate"`
	CGST               float64 `json:"cgst_liability"`
	SGST               float64 `json:"sgst_liability"`
	IGST               float64 `json:"igst_liability"`
	Total              float64 `json:"total_composition_liability"`
	RCMTaxableValue    float64 `json:"rcm_taxable_value"`
	RCMLiability       float64 `json:"rcm_liability"`
	RateNote           string  `json:"rate_note"`
}

// ---- Loaders ---------------------------------------------------------------

// classifyZeroTaxLines splits pre-tax line value among nil_rated / exempt /
// non_gst using the linked product's flags. GST has no reliable exemption
// marker on the billing doc, so: products with gst_enabled=false are treated
// as non-GST supplies, services (item_type=service) as exempt, and remaining
// 0%-rated goods as nil-rated.
func classifyLineBuckets(it models.InvoiceItem) (taxable, nilRated, exempt, nonGST float64) {
	lineNet := it.Quantity * it.UnitPrice * (1 - it.Discount/100)
	if it.TaxRate > 0 {
		return lineNet, 0, 0, 0
	}
	p := it.Product
	if p.ID == uuid.Nil {
		// No product linked: treat 0% lines as nil-rated goods.
		return 0, lineNet, 0, 0
	}
	if !p.GstEnabled {
		if strings.EqualFold(p.ItemType, "service") {
			return 0, 0, lineNet, 0
		}
		return 0, 0, 0, lineNet
	}
	return 0, lineNet, 0, 0
}

// loadGSTSalesRegister builds the outward-supply register for [start, end].
// Includes cancelled and draft documents flagged included_in_totals=false.
func loadGSTSalesRegister(userID uuid.UUID, start, end time.Time, sellerState, fy, taxPeriod string) []gstSalesRegisterRow {
	var invoices []models.Invoice
	utils.DB.Preload("Items").Preload("Items.Product").Preload("Party").
		Where("user_id = ? AND date >= ? AND date <= ?", userID, start, end).
		Order("date ASC, invoice_number ASC").Find(&invoices)

	rows := make([]gstSalesRegisterRow, 0, len(invoices))
	for _, inv := range invoices {
		row := gstSalesRegisterRow{
			FinancialYear: fy, TaxPeriod: taxPeriod,
			InvoiceNumber: inv.InvoiceNumber,
			InvoiceDate:   inv.Date,
			InvoiceStatus: inv.Status,
			DocumentType:  "sales_invoice",
			CustomerName:  inv.Party.Name,
			CustomerGSTIN: inv.Party.GSTIN,
			CustomerState: inv.Party.State,
			PlaceOfSupply: inv.PlaceOfSupply,
			GrossAmount:   roundMoney(inv.SubTotal),
			DiscountAmount: roundMoney(inv.DiscountTotal + inv.InvoiceDiscount),
			TaxAmount:     roundMoney(inv.CGSTTotal + inv.SGSTTotal + inv.IGSTTotal),
			IsInterState:  inv.IsInterState,
			SourceType:    "invoice",
			SourceID:      inv.ID,
		}
		if inv.Party.GSTIN != "" {
			row.TransactionType = "B2B"
		} else {
			row.TransactionType = "B2C"
		}
		if row.PlaceOfSupply == "" && inv.Party.StateCode != "" {
			row.PlaceOfSupply = inv.Party.StateCode
		}
		// Prefer the explicit IsInterState flag; fall back to comparing the
		// place of supply / customer state with the seller state.
		if !row.IsInterState && sellerState != "" {
			pos := row.PlaceOfSupply
			if pos == "" {
				pos = inv.Party.StateCode
			}
			if pos != "" && pos != sellerState {
				row.IsInterState = true
			}
		}

		var taxable, nilR, exempt, nonGST float64
		var netBase float64
		lineNets := make([]struct{ t, n, e, g, net float64 }, 0, len(inv.Items))
		for _, it := range inv.Items {
			t, n, e, g := classifyLineBuckets(it)
			net := t + n + e + g
			netBase += net
			lineNets = append(lineNets, struct{ t, n, e, g, net float64 }{t, n, e, g, net})
		}
		// Allocate invoice-level discount / additional charges pro-rata over
		// line net so classified buckets reconcile to net sales exactly.
		adj := inv.AdditionalCharges - inv.InvoiceDiscount
		for _, ln := range lineNets {
			share := 0.0
			if netBase > 0 {
				share = adj * (ln.net / netBase)
			}
			if ln.t > 0 {
				taxable += ln.t + share
			} else if ln.n > 0 {
				nilR += ln.n + share
			} else if ln.e > 0 {
				exempt += ln.e + share
			} else {
				nonGST += ln.g + share
			}
		}
		row.NetAmount = roundMoney(netBase + adj)
		row.TaxableValue = roundMoney(taxable)
		row.NilRatedValue = roundMoney(nilR)
		row.ExemptValue = roundMoney(exempt)
		row.NonGSTValue = roundMoney(nonGST)

		switch {
		case inv.Status == "cancelled":
			row.SupplyClassification = "cancelled"
			row.IncludedInTotals = false
			row.CompositionTurnover = "excluded"
		case inv.Status == "draft":
			row.SupplyClassification = "draft"
			row.IncludedInTotals = false
			row.CompositionTurnover = "excluded"
		default:
			row.IncludedInTotals = true
			cls := classifyBuckets(taxable, nilR, exempt, nonGST)
			row.SupplyClassification = cls
			if row.IsInterState {
				// Inter-state outward supplies are not permitted under
				// composition — included in turnover but flagged for review.
				row.CompositionTurnover = "review"
			} else if nonGST > 0 && taxable+nilR+exempt == 0 {
				row.CompositionTurnover = "excluded"
			} else {
				row.CompositionTurnover = "included"
			}
		}
		rows = append(rows, row)
	}

	// Processed sales returns reduce turnover. Returns linked to processed
	// returns via CreditNote.SalesReturnID are skipped to avoid double count.
	var returns []models.SalesReturn
	utils.DB.Preload("Items").Preload("Party").
		Where("user_id = ? AND date >= ? AND date <= ? AND status = 'processed'", userID, start, end).
		Order("date ASC").Find(&returns)
	for _, r := range returns {
		var taxable, zero float64
		for _, it := range r.Items {
			lineBase := it.Quantity * it.UnitPrice
			if it.TaxRate > 0 {
				taxable += lineBase
			} else {
				zero += lineBase
			}
		}
		rows = append(rows, gstSalesRegisterRow{
			FinancialYear: fy, TaxPeriod: taxPeriod,
			InvoiceNumber: r.ReturnNumber, InvoiceDate: r.Date, InvoiceStatus: r.Status,
			DocumentType:  "sales_return",
			CustomerName:  r.Party.Name, CustomerGSTIN: r.Party.GSTIN, CustomerState: r.Party.State,
			TransactionType: txnType(r.Party.GSTIN),
			GrossAmount:   -roundMoney(r.Amount),
			NetAmount:     -roundMoney(taxable + zero),
			TaxableValue:  -roundMoney(taxable),
			NilRatedValue: -roundMoney(zero),
			SupplyClassification: "return",
			CompositionTurnover:  "included",
			IncludedInTotals:     true,
			SourceType:           "sales_return",
			SourceID:             r.ID,
		})
	}

	// Standalone issued credit notes (not generated by a sales return).
	var notes []models.CreditNote
	utils.DB.Preload("Items").Preload("Party").
		Where("user_id = ? AND date >= ? AND date <= ? AND status = 'issued' AND sales_return_id IS NULL", userID, start, end).
		Order("date ASC").Find(&notes)
	for _, n := range notes {
		var taxable, zero float64
		for _, it := range n.Items {
			lineBase := it.Quantity * it.UnitPrice
			if it.TaxRate > 0 {
				taxable += lineBase
			} else {
				zero += lineBase
			}
		}
		rows = append(rows, gstSalesRegisterRow{
			FinancialYear: fy, TaxPeriod: taxPeriod,
			InvoiceNumber: n.CreditNoteNumber, InvoiceDate: n.Date, InvoiceStatus: n.Status,
			DocumentType:  "credit_note",
			CustomerName:  n.Party.Name, CustomerGSTIN: n.Party.GSTIN, CustomerState: n.Party.State,
			TransactionType: txnType(n.Party.GSTIN),
			GrossAmount:   -roundMoney(n.TotalAmount),
			NetAmount:     -roundMoney(taxable + zero),
			TaxableValue:  -roundMoney(taxable),
			NilRatedValue: -roundMoney(zero),
			SupplyClassification: "credit_note",
			CompositionTurnover:  "included",
			IncludedInTotals:     true,
			SourceType:           "credit_note",
			SourceID:             n.ID,
		})
	}
	return rows
}

func txnType(gstin string) string {
	if strings.TrimSpace(gstin) != "" {
		return "B2B"
	}
	return "B2C"
}

func classifyBuckets(taxable, nilR, exempt, nonGST float64) string {
	kinds := 0
	cls := ""
	for c, v := range map[string]float64{"taxable": taxable, "nil_rated": nilR, "exempt": exempt, "non_gst": nonGST} {
		if v > 0.005 {
			kinds++
			cls = c
		}
	}
	if kinds == 0 {
		return "unclassified"
	}
	if kinds > 1 {
		return "mixed"
	}
	return cls
}

// loadGSTPurchaseRegister builds the inward-supply register.
func loadGSTPurchaseRegister(userID uuid.UUID, start, end time.Time, buyerState, fy, taxPeriod string) []gstPurchaseRegisterRow {
	var bills []models.PurchaseBill
	utils.DB.Preload("Items").Preload("Party").
		Where("user_id = ? AND bill_date >= ? AND bill_date <= ?", userID, start, end).
		Order("bill_date ASC, bill_number ASC").Find(&bills)

	rows := make([]gstPurchaseRegisterRow, 0, len(bills))
	for _, b := range bills {
		supplierState := b.Party.StateCode
		if supplierState == "" {
			supplierState = b.Party.State
		}
		isInter := buyerState != "" && supplierState != "" && supplierState != buyerState
		row := gstPurchaseRegisterRow{
			FinancialYear: fy, TaxPeriod: taxPeriod,
			InvoiceNumber: b.BillNumber, InvoiceDate: b.BillDate, DocStatus: b.Status,
			DocumentType: "purchase_bill",
			SupplierName: b.Party.Name, SupplierGSTIN: b.Party.GSTIN,
			SupplierState: b.Party.State,
			GrossAmount:   roundMoney(b.SubTotal),
			DiscountAmount: roundMoney(b.InvoiceDiscount),
			NetAmount:     roundMoney(b.SubTotal - b.InvoiceDiscount + b.AdditionalCharges),
			IsInterState:  isInter,
			IncludedInTotals: b.Status != "draft",
			SourceType:    "purchase_bill",
			SourceID:      b.ID,
		}
		if strings.TrimSpace(b.Party.GSTIN) != "" {
			row.SupplierRegistration = "registered"
		} else {
			row.SupplierRegistration = "unregistered"
		}
		var taxable, exempt, lineTax float64
		var netBase float64
		for _, it := range b.Items {
			lineNet := it.Quantity * it.UnitPrice * (1 - it.Discount/100)
			netBase += lineNet
			if it.TaxRate > 0 {
				taxable += lineNet
			} else {
				exempt += lineNet
			}
			lineTax += it.TaxAmount
		}
		// Allocate invoice discount pro-rata; additional charges extend the
		// taxable base only when there are taxable lines.
		adj := b.AdditionalCharges - b.InvoiceDiscount
		if netBase > 0 && taxable > 0 {
			taxable += adj * (taxable / netBase)
			exempt += adj * (exempt / netBase)
		} else if netBase > 0 {
			exempt += adj
		}
		if b.TaxExempt {
			exempt += taxable
			taxable = 0
		}
		row.TaxableValue = roundMoney(taxable)
		row.ExemptValue = roundMoney(exempt)

		supplierGST := b.TaxTotal
		if supplierGST == 0 && lineTax > 0 {
			supplierGST = lineTax
		}
		if isInter {
			row.IGSTAmount = roundMoney(supplierGST)
		} else {
			row.CGSTAmount = roundMoney(supplierGST / 2)
			row.SGSTAmount = roundMoney(supplierGST - row.CGSTAmount)
		}
		row.PurchaseCategory = purchaseCategoryName(b)

		if b.ReverseCharge {
			row.ReverseCharge = true
			rcmBase := b.RCMTaxableValue
			if rcmBase == 0 {
				rcmBase = taxable + exempt
			}
			rcmTax := b.RCMTaxAmount
			if rcmTax == 0 {
				rcmTax = lineTax
			}
			if rcmTax == 0 {
				rcmTax = supplierGST
			}
			row.RCMTaxableValue = roundMoney(rcmBase)
			row.RCMTaxAmount = roundMoney(rcmTax)
		}
		rows = append(rows, row)
	}

	var returns []models.PurchaseReturn
	utils.DB.Preload("Items").Preload("Party").
		Where("user_id = ? AND date >= ? AND date <= ? AND status = 'processed'", userID, start, end).
		Order("date ASC").Find(&returns)
	for _, r := range returns {
		var taxable, exempt float64
		for _, it := range r.Items {
			lineBase := it.Quantity * it.UnitPrice
			if it.TaxRate > 0 {
				taxable += lineBase
			} else {
				exempt += lineBase
			}
		}
		supplierState := r.Party.StateCode
		rows = append(rows, gstPurchaseRegisterRow{
			FinancialYear: fy, TaxPeriod: taxPeriod,
			InvoiceNumber: r.ReturnNumber, InvoiceDate: r.Date, DocStatus: r.Status,
			DocumentType: "purchase_return",
			SupplierName: r.Party.Name, SupplierGSTIN: r.Party.GSTIN,
			SupplierRegistration: txnRegistration(r.Party.GSTIN),
			SupplierState:        r.Party.State,
			GrossAmount:          -roundMoney(r.Amount),
			NetAmount:            -roundMoney(taxable + exempt),
			TaxableValue:         -roundMoney(taxable),
			ExemptValue:          -roundMoney(exempt),
			IsInterState:         buyerState != "" && supplierState != "" && supplierState != buyerState,
			IncludedInTotals:     true,
			SourceType:           "purchase_return",
			SourceID:             r.ID,
		})
	}

	// Standalone issued debit notes (not generated by a purchase return).
	var notes []models.DebitNote
	utils.DB.Preload("Items").Preload("Party").
		Where("user_id = ? AND date >= ? AND date <= ? AND status = 'issued' AND purchase_return_id IS NULL", userID, start, end).
		Order("date ASC").Find(&notes)
	for _, n := range notes {
		var taxable, exempt float64
		for _, it := range n.Items {
			lineBase := it.Quantity * it.UnitPrice
			if it.TaxRate > 0 {
				taxable += lineBase
			} else {
				exempt += lineBase
			}
		}
		rows = append(rows, gstPurchaseRegisterRow{
			FinancialYear: fy, TaxPeriod: taxPeriod,
			InvoiceNumber: n.DebitNoteNumber, InvoiceDate: n.Date, DocStatus: n.Status,
			DocumentType: "debit_note",
			SupplierName: n.Party.Name, SupplierGSTIN: n.Party.GSTIN,
			SupplierRegistration: txnRegistration(n.Party.GSTIN),
			SupplierState:        n.Party.State,
			GrossAmount:          -roundMoney(n.TotalAmount),
			NetAmount:            -roundMoney(taxable + exempt),
			TaxableValue:         -roundMoney(taxable),
			ExemptValue:          -roundMoney(exempt),
			IncludedInTotals:     true,
			SourceType:           "debit_note",
			SourceID:             n.ID,
		})
	}
	return rows
}

func txnRegistration(gstin string) string {
	if strings.TrimSpace(gstin) != "" {
		return "registered"
	}
	return "unregistered"
}

// purchaseCategoryName picks a display category from the bill's dominant
// item category.
func purchaseCategoryName(b models.PurchaseBill) string {
	counts := map[string]float64{}
	for _, it := range b.Items {
		cat := it.Category
		if cat == "" {
			cat = "uncategorized"
		}
		counts[cat] += it.Quantity * it.UnitPrice
	}
	best, bestV := "uncategorized", -1.0
	for c, v := range counts {
		if v > bestV {
			best, bestV = c, v
		}
	}
	return best
}

// ---- Summaries -------------------------------------------------------------

func summarizeSales(rows []gstSalesRegisterRow, period string) gstSalesSummary {
	var s gstSalesSummary
	s.Period = period
	for _, r := range rows {
		switch r.DocumentType {
		case "sales_invoice":
			if r.InvoiceStatus == "cancelled" {
				s.CancelledInvoiceCount++
				continue
			}
			if !r.IncludedInTotals {
				continue
			}
			s.InvoiceCount++
			s.GrossSales += r.GrossAmount
			s.DiscountAmount += r.DiscountAmount
			s.TaxableSupplyValue += r.TaxableValue
			s.NilRatedSupplyValue += r.NilRatedValue
			s.ExemptSupplyValue += r.ExemptValue
			s.NonGSTSupplyValue += r.NonGSTValue
			if r.IsInterState {
				s.InterStateOutwardValue += r.NetAmount
			}
		case "sales_return":
			s.SalesReturnAmount += -r.NetAmount
			s.TaxableSupplyValue += r.TaxableValue
			s.NilRatedSupplyValue += r.NilRatedValue
		case "credit_note":
			s.CreditNoteValue += -r.NetAmount
			s.TaxableSupplyValue += r.TaxableValue
			s.NilRatedSupplyValue += r.NilRatedValue
		}
	}
	s.NetSales = s.GrossSales - s.DiscountAmount - s.SalesReturnAmount - s.CreditNoteValue
	// Composition turnover basis: taxable + exempt + nil-rated outward
	// supplies net of returns; non-GST supplies are outside the levy.
	s.CompositionTurnoverBasis = s.TaxableSupplyValue + s.NilRatedSupplyValue + s.ExemptSupplyValue
	s.GrossSales = roundMoney(s.GrossSales)
	s.DiscountAmount = roundMoney(s.DiscountAmount)
	s.SalesReturnAmount = roundMoney(s.SalesReturnAmount)
	s.NetSales = roundMoney(s.NetSales)
	s.TaxableSupplyValue = roundMoney(s.TaxableSupplyValue)
	s.ExemptSupplyValue = roundMoney(s.ExemptSupplyValue)
	s.NilRatedSupplyValue = roundMoney(s.NilRatedSupplyValue)
	s.NonGSTSupplyValue = roundMoney(s.NonGSTSupplyValue)
	s.CompositionTurnoverBasis = roundMoney(s.CompositionTurnoverBasis)
	s.InterStateOutwardValue = roundMoney(s.InterStateOutwardValue)
	s.CreditNoteValue = roundMoney(s.CreditNoteValue)
	return s
}

func summarizePurchases(rows []gstPurchaseRegisterRow, period string) gstPurchaseSummary {
	var s gstPurchaseSummary
	s.Period = period
	for _, r := range rows {
		if !r.IncludedInTotals {
			continue
		}
		switch r.DocumentType {
		case "purchase_bill":
			s.PurchaseInvoiceCount++
			if r.SupplierRegistration == "registered" {
				s.RegisteredSupplierPurchaseValue += r.NetAmount
			} else {
				s.UnregisteredSupplierPurchaseValue += r.NetAmount
			}
			s.PurchaseDiscountValue += r.DiscountAmount
			if r.IsInterState {
				s.InterStatePurchaseValue += r.NetAmount
			} else {
				s.IntraStatePurchaseValue += r.NetAmount
			}
			s.TaxablePurchaseValue += r.TaxableValue
			s.ExemptPurchaseValue += r.ExemptValue
			s.SupplierGSTAmount += r.CGSTAmount + r.SGSTAmount + r.IGSTAmount
			s.RCMTaxableValue += r.RCMTaxableValue
			s.RCMTaxLiability += r.RCMTaxAmount
		case "purchase_return":
			s.PurchaseReturnValue += -r.NetAmount
			s.TaxablePurchaseValue += r.TaxableValue
			s.ExemptPurchaseValue += r.ExemptValue
		case "debit_note":
			s.TaxablePurchaseValue += r.TaxableValue
			s.ExemptPurchaseValue += r.ExemptValue
		}
	}
	s.RegisteredSupplierPurchaseValue = roundMoney(s.RegisteredSupplierPurchaseValue)
	s.UnregisteredSupplierPurchaseValue = roundMoney(s.UnregisteredSupplierPurchaseValue)
	s.PurchaseReturnValue = roundMoney(s.PurchaseReturnValue)
	s.PurchaseDiscountValue = roundMoney(s.PurchaseDiscountValue)
	s.IntraStatePurchaseValue = roundMoney(s.IntraStatePurchaseValue)
	s.InterStatePurchaseValue = roundMoney(s.InterStatePurchaseValue)
	s.TaxablePurchaseValue = roundMoney(s.TaxablePurchaseValue)
	s.ExemptPurchaseValue = roundMoney(s.ExemptPurchaseValue)
	s.SupplierGSTAmount = roundMoney(s.SupplierGSTAmount)
	s.RCMTaxableValue = roundMoney(s.RCMTaxableValue)
	s.RCMTaxLiability = roundMoney(s.RCMTaxLiability)
	return s
}

// computeCompositionLiability applies the levy rate to the turnover basis.
// Rate is per-registration (category default or explicit override); the levy
// splits equally between CGST and SGST for intra-state supplies. Any
// inter-state flagged turnover is included in the basis but surfaced through
// validation as a composition-eligibility review item.
func computeCompositionLiability(sales gstSalesSummary, purchases gstPurchaseSummary, reg *models.GSTRegistration) gstTaxLiability {
	rate := compositionRateFor(reg)
	basis := sales.CompositionTurnoverBasis
	if basis < 0 {
		basis = 0
	}
	total := roundMoney(basis * rate / 100)
	half := roundMoney(total / 2)
	return gstTaxLiability{
		TurnoverBasis:   roundMoney(basis),
		CompositionRate: rate,
		CGST:            half,
		SGST:            roundMoney(total - half),
		Total:           total,
		RCMTaxableValue: purchases.RCMTaxableValue,
		RCMLiability:    purchases.RCMTaxLiability,
		RateNote:        fmt.Sprintf("%.2f%% composition levy on turnover in state for category %q", rate, reg.CompositionCategory),
	}
}

// ---- Snapshots --------------------------------------------------------------

func latestGSTSnapshot(periodID uuid.UUID) (*models.GSTCalculationSnapshot, error) {
	var snap models.GSTCalculationSnapshot
	err := utils.DB.Where("gst_return_period_id = ?", periodID).
		Order("version DESC").First(&snap).Error
	if err != nil {
		return nil, err
	}
	return &snap, nil
}

// sourceDataFingerprint produces a cheap staleness marker over the documents
// feeding the report: row counts + max(updated_at) per source table.
func sourceDataFingerprint(userID uuid.UUID, start, end time.Time) string {
	type probe struct{ model string; dateCol string; extra string }
	probes := []probe{
		{"invoices", "date", "AND status != 'draft'"},
		{"purchase_bills", "bill_date", "AND status != 'draft'"},
		{"sales_returns", "date", "AND status = 'processed'"},
		{"purchase_returns", "date", "AND status = 'processed'"},
		{"credit_notes", "date", "AND status = 'issued'"},
		{"debit_notes", "date", "AND status = 'issued'"},
	}
	parts := make([]string, 0, len(probes))
	for _, pr := range probes {
		var count int64
		var maxUpd *time.Time
		q := fmt.Sprintf("SELECT COUNT(*), MAX(updated_at) FROM %s WHERE user_id = ? AND deleted_at IS NULL AND %s >= ? AND %s <= ? %s", pr.model, pr.dateCol, pr.dateCol, pr.extra)
		utils.DB.Raw(q, userID, start, end).Row().Scan(&count, &maxUpd)
		ts := "none"
		if maxUpd != nil {
			ts = maxUpd.UTC().Format(time.RFC3339)
		}
		parts = append(parts, fmt.Sprintf("%s:%d@%s", pr.model, count, ts))
	}
	return strings.Join(parts, ";")
}

// computeGSTReport is the shared engine behind the JSON endpoints, snapshot
// generation and the export builders.
func computeGSTReport(userID uuid.UUID, scope *gstReportScope) (sales []gstSalesRegisterRow, purchases []gstPurchaseRegisterRow, ss gstSalesSummary, ps gstPurchaseSummary, liab gstTaxLiability) {
	sellerState := ""
	if scope.Registration != nil {
		sellerState = scope.Registration.StateCode
	}
	taxPeriod := scope.FY
	if scope.Quarter > 0 {
		taxPeriod = fmt.Sprintf("%s-Q%d", scope.FY, scope.Quarter)
	}
	sales = loadGSTSalesRegister(userID, scope.Start, scope.End, sellerState, scope.FY, taxPeriod)
	purchases = loadGSTPurchaseRegister(userID, scope.Start, scope.End, sellerState, scope.FY, taxPeriod)
	periodLabel := fmt.Sprintf("%s to %s", scope.Start.Format("2006-01-02"), scope.End.Format("2006-01-02"))
	ss = summarizeSales(sales, periodLabel)
	ps = summarizePurchases(purchases, periodLabel)
	if scope.Registration != nil && scope.Registration.RegistrationType == "composition" {
		liab = computeCompositionLiability(ss, ps, scope.Registration)
	} else {
		liab = gstTaxLiability{
			TurnoverBasis:   ss.CompositionTurnoverBasis,
			RCMTaxableValue: ps.RCMTaxableValue,
			RCMLiability:    ps.RCMTaxLiability,
			RateNote:        "No active composition registration — liability not computed",
		}
	}
	return
}

// generateGSTSnapshot stores a frozen calculation + fresh validation run for
// the period.
func generateGSTSnapshot(c *gin.Context, p *models.GSTReturnPeriod) (*models.GSTCalculationSnapshot, []models.GSTValidationIssue, error) {
	userID := p.UserID
	actorID := actorUserID(c)
	reg, err := loadGSTRegistration(userID, p.GSTRegistrationID)
	if err != nil {
		return nil, nil, errors.New("GST registration not found for this period")
	}
	scope := &gstReportScope{
		Registration: reg,
		Start: p.PeriodStart, End: p.PeriodEnd,
		FY: p.FinancialYear, Quarter: p.Quarter, PeriodType: p.PeriodType,
		Period: p,
	}
	sales, purchases, ss, ps, liab := computeGSTReport(userID, scope)

	// Manual adjustment context: prior payments toward this period are not
	// part of liability; adjustments come from reconciliation records.
	var adjustments float64
	var reconRows []models.GSTReconciliationRecord
	utils.DB.Where("gst_return_period_id = ?", p.ID).Find(&reconRows)
	for _, r := range reconRows {
		if r.ReconciliationType == "adjustment" && r.ResolutionStatus != "open" {
			adjustments += r.RecordedReturnValue
		}
	}

	var maxVersion int
	utils.DB.Model(&models.GSTCalculationSnapshot{}).
		Where("gst_return_period_id = ?", p.ID).
		Select("COALESCE(MAX(version), 0)").Scan(&maxVersion)

	details, _ := json.Marshal(gin.H{
		"sales_summary":    ss,
		"purchase_summary": ps,
		"liability":        liab,
		"sales_rows":       len(sales),
		"purchase_rows":    len(purchases),
		"registration": gin.H{
			"gstin": reg.GSTIN, "category": reg.CompositionCategory,
			"rate": compositionRateFor(reg),
		},
	})

	snap := models.GSTCalculationSnapshot{
		ID:                uuid.New(),
		UserID:            userID,
		GSTReturnPeriodID: p.ID,
		Version:           maxVersion + 1,
		CalculationRule:   gstCalcRuleVersion,
		SourceDataVersion: sourceDataFingerprint(userID, p.PeriodStart, p.PeriodEnd),
		TurnoverBasis:     liab.TurnoverBasis,
		CompositionRate:   liab.CompositionRate,
		CGSTLiability:     liab.CGST,
		SGSTLiability:     liab.SGST,
		IGSTLiability:     liab.IGST,
		TotalCompositionLiability: liab.Total,
		RCMTaxableValue:   liab.RCMTaxableValue,
		RCMLiability:      liab.RCMLiability,
		AdjustmentsTotal:  roundMoney(adjustments),
		TotalPayable:      roundMoney(liab.Total + liab.RCMLiability + adjustments),
		CalculationDetails: string(details),
		GeneratedAt:       time.Now(),
		GeneratedBy:       actorID,
	}
	if err := utils.DB.Create(&snap).Error; err != nil {
		return nil, nil, errors.New("failed to store calculation snapshot")
	}

	issues := runGSTValidation(userID, scope, ss, ps, snap.Version)
	if err := persistGSTValidationIssues(p.ID, userID, issues); err != nil {
		return &snap, issues, errors.New("snapshot saved but failed to store validation issues")
	}

	now := time.Now()
	p.GeneratedAt = &now
	openErrors := 0
	for _, i := range issues {
		if i.Severity == "ERROR" {
			openErrors++
		}
	}
	if openErrors > 0 {
		p.Status = models.GSTPeriodStatusGenerated
	} else {
		p.Status = models.GSTPeriodStatusValidated
	}
	utils.DB.Save(p)
	return &snap, issues, nil
}

// ---- HTTP handlers ----------------------------------------------------------

func scopeOr400(c *gin.Context) (*gstReportScope, bool) {
	scope, err := resolveGSTScope(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return nil, false
	}
	return scope, true
}

func GetGSTDashboard(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	scope, ok := scopeOr400(c)
	if !ok {
		return
	}
	sales, purchases, ss, ps, liab := computeGSTReport(userID, scope)

	// Payment position for the matching return period (if one exists).
	var paid, portalConfirmedPaid float64
	var period *models.GSTReturnPeriod
	var p models.GSTReturnPeriod
	periodQ := utils.DB.Where("user_id = ? AND period_start = ? AND period_end = ?", userID, scope.Start, scope.End)
	if scope.Registration != nil {
		periodQ = periodQ.Where("gst_registration_id = ?", scope.Registration.ID)
	}
	if err := periodQ.First(&p).Error; err == nil {
		period = &p
		var payments []models.GSTTaxPayment
		utils.DB.Where("gst_return_period_id = ?", p.ID).Find(&payments)
		for _, pay := range payments {
			paid += pay.Amount
			if pay.PortalConfirmed {
				portalConfirmedPaid += pay.Amount
			}
		}
	}

	var openErrors, openWarnings int64
	if period != nil {
		utils.DB.Model(&models.GSTValidationIssue{}).
			Where("gst_return_period_id = ? AND resolution_status = 'open' AND severity = 'ERROR'", period.ID).Count(&openErrors)
		utils.DB.Model(&models.GSTValidationIssue{}).
			Where("gst_return_period_id = ? AND resolution_status = 'open' AND severity = 'WARNING'", period.ID).Count(&openWarnings)
	}

	totalPayable := liab.Total + liab.RCMLiability
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"registration":          scope.Registration,
		"financial_year":        scope.FY,
		"quarter":               scope.Quarter,
		"period_type":           scope.PeriodType,
		"period_start":          scope.Start.Format("2006-01-02"),
		"period_end":            scope.End.Format("2006-01-02"),
		"sales_summary":         ss,
		"purchase_summary":      ps,
		"estimated_liability":   roundMoney(totalPayable),
		"liability_status":      "estimated", // never portal-verified
		"liability":             liab,
		"tax_paid":              roundMoney(paid),
		"tax_paid_confirmed":    roundMoney(portalConfirmedPaid),
		"tax_paid_unverified":   roundMoney(paid - portalConfirmedPaid),
		"outstanding_liability": roundMoney(totalPayable - paid),
		"open_errors":           openErrors,
		"open_warnings":         openWarnings,
		"return_period":         period,
		"cmp08_status":          periodStatusForReturn(period, models.GSTReturnTypeCMP08),
		"gstr4_status":          periodStatusForReturn(period, models.GSTReturnTypeGSTR4),
		"row_counts": gin.H{
			"sales": len(sales), "purchases": len(purchases),
		},
		"generated_at": time.Now().UTC(),
	}})
}

func periodStatusForReturn(p *models.GSTReturnPeriod, returnType string) string {
	if p == nil || p.ReturnType != returnType {
		return "not_started"
	}
	return p.Status
}

func GetGSTSalesSummary(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	scope, ok := scopeOr400(c)
	if !ok {
		return
	}
	rows := loadGSTSalesRegister(userID, scope.Start, scope.End, regState(scope), scope.FY, scopeTaxPeriod(scope))
	summary := summarizeSales(rows, fmt.Sprintf("%s to %s", scope.Start.Format("2006-01-02"), scope.End.Format("2006-01-02")))
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"summary": summary, "scope": scopeJSON(scope)}})
}

func GetGSTSalesRegister(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	scope, ok := scopeOr400(c)
	if !ok {
		return
	}
	rows := loadGSTSalesRegister(userID, scope.Start, scope.End, regState(scope), scope.FY, scopeTaxPeriod(scope))
	rows = filterSalesRegister(c, rows)
	summary := summarizeSales(rows, fmt.Sprintf("%s to %s", scope.Start.Format("2006-01-02"), scope.End.Format("2006-01-02")))
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"rows": rows, "summary": summary, "scope": scopeJSON(scope)}})
}

func filterSalesRegister(c *gin.Context, rows []gstSalesRegisterRow) []gstSalesRegisterRow {
	docType := strings.TrimSpace(c.Query("document_type"))
	status := strings.TrimSpace(c.Query("status"))
	search := strings.ToLower(strings.TrimSpace(c.Query("q")))
	if docType == "" && status == "" && search == "" {
		return rows
	}
	out := rows[:0]
	for _, r := range rows {
		if docType != "" && r.DocumentType != docType {
			continue
		}
		if status != "" && r.InvoiceStatus != status {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(r.InvoiceNumber+r.CustomerName+r.CustomerGSTIN), search) {
			continue
		}
		out = append(out, r)
	}
	return out
}

func GetGSTPurchaseSummary(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	scope, ok := scopeOr400(c)
	if !ok {
		return
	}
	rows := loadGSTPurchaseRegister(userID, scope.Start, scope.End, regState(scope), scope.FY, scopeTaxPeriod(scope))
	summary := summarizePurchases(rows, fmt.Sprintf("%s to %s", scope.Start.Format("2006-01-02"), scope.End.Format("2006-01-02")))
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"summary": summary, "scope": scopeJSON(scope)}})
}

func GetGSTPurchaseRegister(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	scope, ok := scopeOr400(c)
	if !ok {
		return
	}
	rows := loadGSTPurchaseRegister(userID, scope.Start, scope.End, regState(scope), scope.FY, scopeTaxPeriod(scope))
	rows = filterPurchaseRegister(c, rows)
	summary := summarizePurchases(rows, fmt.Sprintf("%s to %s", scope.Start.Format("2006-01-02"), scope.End.Format("2006-01-02")))
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"rows": rows, "summary": summary, "scope": scopeJSON(scope)}})
}

func filterPurchaseRegister(c *gin.Context, rows []gstPurchaseRegisterRow) []gstPurchaseRegisterRow {
	docType := strings.TrimSpace(c.Query("document_type"))
	reg := strings.TrimSpace(c.Query("supplier_registration"))
	rcm := strings.TrimSpace(c.Query("reverse_charge"))
	search := strings.ToLower(strings.TrimSpace(c.Query("q")))
	if docType == "" && reg == "" && rcm == "" && search == "" {
		return rows
	}
	out := rows[:0]
	for _, r := range rows {
		if docType != "" && r.DocumentType != docType {
			continue
		}
		if reg != "" && r.SupplierRegistration != reg {
			continue
		}
		if rcm == "true" && !r.ReverseCharge {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(r.InvoiceNumber+r.SupplierName+r.SupplierGSTIN), search) {
			continue
		}
		out = append(out, r)
	}
	return out
}

func regState(scope *gstReportScope) string {
	if scope.Registration == nil {
		return ""
	}
	return scope.Registration.StateCode
}

func scopeTaxPeriod(scope *gstReportScope) string {
	if scope.Quarter > 0 {
		return fmt.Sprintf("%s-Q%d", scope.FY, scope.Quarter)
	}
	return scope.FY
}

func scopeJSON(scope *gstReportScope) gin.H {
	return gin.H{
		"financial_year": scope.FY,
		"quarter":        scope.Quarter,
		"period_type":    scope.PeriodType,
		"period_start":   scope.Start.Format("2006-01-02"),
		"period_end":     scope.End.Format("2006-01-02"),
		"registration":   scope.Registration,
	}
}

// GetGSTTaxLiability returns the composition levy + RCM + payment position.
func GetGSTTaxLiability(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	scope, ok := scopeOr400(c)
	if !ok {
		return
	}
	_, _, ss, ps, liab := computeGSTReport(userID, scope)
	var paid float64
	if scope.Period != nil {
		utils.DB.Model(&models.GSTTaxPayment{}).
			Where("gst_return_period_id = ?", scope.Period.ID).
			Select("COALESCE(SUM(amount),0)").Scan(&paid)
	}
	total := liab.Total + liab.RCMLiability
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"scope":               scopeJSON(scope),
		"sales_summary":       ss,
		"purchase_summary":    ps,
		"liability":           liab,
		"total_liability":     roundMoney(total),
		"tax_paid":            roundMoney(paid),
		"outstanding":         roundMoney(total - paid),
		"liability_status":    "estimated",
	}})
}

// GetGSTCMP08 returns the quarterly CMP-08 working paper.
func GetGSTCMP08(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	scope, ok := scopeOr400(c)
	if !ok {
		return
	}
	if scope.PeriodType != models.GSTPeriodTypeQuarterly && scope.Period == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "CMP-08 requires a quarterly period (pass quarter=1..4 with fy, or period_id)"})
		return
	}
	sales, purchases, ss, ps, liab := computeGSTReport(userID, scope)

	var payments []models.GSTTaxPayment
	var recon []models.GSTReconciliationRecord
	var issues []models.GSTValidationIssue
	var snapshot *models.GSTCalculationSnapshot
	if scope.Period != nil {
		utils.DB.Where("gst_return_period_id = ?", scope.Period.ID).Order("payment_date ASC").Find(&payments)
		utils.DB.Where("gst_return_period_id = ?", scope.Period.ID).Find(&recon)
		utils.DB.Where("gst_return_period_id = ? AND resolution_status = 'open'", scope.Period.ID).
			Order("severity ASC").Find(&issues)
		if s, err := latestGSTSnapshot(scope.Period.ID); err == nil {
			snapshot = s
		}
	}

	var paid, confirmedPaid float64
	for _, pay := range payments {
		paid += pay.Amount
		if pay.PortalConfirmed {
			confirmedPaid += pay.Amount
		}
	}
	totalPayable := liab.Total + liab.RCMLiability + snapshotAdjustments(snapshot)

	var biz models.Business
	utils.DB.Where("user_id = ?", userID).First(&biz)

	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"header": gin.H{
			"business_name":     firstNonEmpty(biz.Name, ""),
			"gstin":             regField(scope, func(r *models.GSTRegistration) string { return r.GSTIN }),
			"legal_name":        regField(scope, func(r *models.GSTRegistration) string { return r.LegalName }),
			"trade_name":        regField(scope, func(r *models.GSTRegistration) string { return r.TradeName }),
			"state":             regField(scope, func(r *models.GSTRegistration) string { return r.State }),
			"state_code":        regField(scope, func(r *models.GSTRegistration) string { return r.StateCode }),
			"registration_type": regField(scope, func(r *models.GSTRegistration) string { return r.RegistrationType }),
			"financial_year":    scope.FY,
			"quarter":           scope.Quarter,
			"period_start":      scope.Start.Format("2006-01-02"),
			"period_end":        scope.End.Format("2006-01-02"),
			"generated_at":      time.Now().UTC(),
			"period_status":     periodStatus(scope),
		},
		"turnover": gin.H{
			"taxable_outward_turnover":  ss.TaxableSupplyValue,
			"exempt_outward_turnover":   roundMoney(ss.ExemptSupplyValue + ss.NilRatedSupplyValue),
			"exempt_supply_value":       ss.ExemptSupplyValue,
			"nil_rated_supply_value":    ss.NilRatedSupplyValue,
			"non_gst_supply_value":      ss.NonGSTSupplyValue,
			"other_relevant_turnover":   ss.NonGSTSupplyValue,
			"gross_sales":               ss.GrossSales,
			"discount_amount":           ss.DiscountAmount,
			"sales_return_amount":       ss.SalesReturnAmount,
			"credit_note_value":         ss.CreditNoteValue,
			"net_sales":                 ss.NetSales,
			"composition_turnover_basis": ss.CompositionTurnoverBasis,
			"interstate_outward_supply_value": ss.InterStateOutwardValue,
		},
		"liability": gin.H{
			"applicable_composition_rate": liab.CompositionRate,
			"cgst_composition_liability":  liab.CGST,
			"sgst_composition_liability":  liab.SGST,
			"total_composition_liability": liab.Total,
			"rcm_taxable_value":           liab.RCMTaxableValue,
			"rcm_tax_liability":           liab.RCMLiability,
			"interest_amount":             snapshotOrZero(snapshot, func(s *models.GSTCalculationSnapshot) float64 { return s.InterestAmount }),
			"late_fee_amount":             snapshotOrZero(snapshot, func(s *models.GSTCalculationSnapshot) float64 { return s.LateFeeAmount }),
			"adjustments_total":           snapshotAdjustments(snapshot),
			"total_payable":               roundMoney(totalPayable),
			"rate_note":                   liab.RateNote,
		},
		"payment_reconciliation": gin.H{
			"current_period_liability":   roundMoney(totalPayable),
			"tax_paid":                   roundMoney(paid),
			"portal_confirmed_paid":      roundMoney(confirmedPaid),
			"unverified_paid":            roundMoney(paid - confirmedPaid),
			"outstanding_balance":        roundMoney(totalPayable - paid),
			"payments":                   payments,
		},
		"reconciliation_records": recon,
		"open_issues":            issues,
		"latest_snapshot":        snapshot,
		"row_counts":             gin.H{"sales": len(sales), "purchases": len(purchases)},
		"purchase_summary":       ps,
		"disclaimer":             "This is a working paper for review. Generating or exporting it does not file CMP-08 with the GST portal.",
	}})
}

func snapshotAdjustments(s *models.GSTCalculationSnapshot) float64 {
	if s == nil {
		return 0
	}
	return s.AdjustmentsTotal
}

func snapshotOrZero(s *models.GSTCalculationSnapshot, f func(*models.GSTCalculationSnapshot) float64) float64 {
	if s == nil {
		return 0
	}
	return f(s)
}

func regField(scope *gstReportScope, f func(*models.GSTRegistration) string) string {
	if scope.Registration == nil {
		return ""
	}
	return f(scope.Registration)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func periodStatus(scope *gstReportScope) string {
	if scope.Period == nil {
		return "not_started"
	}
	return scope.Period.Status
}

// GetGSTGSTR4 returns the annual GSTR-4 working paper with quarterly
// CMP-08 reconciliation.
func GetGSTGSTR4(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	scope, ok := scopeOr400(c)
	if !ok {
		return
	}
	if scope.PeriodType != models.GSTPeriodTypeAnnual && scope.Period == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "GSTR-4 requires an annual period (pass fy without quarter, or period_id)"})
		return
	}
	sales, purchases, ss, ps, liab := computeGSTReport(userID, scope)

	// Quarter-wise reconciliation against CMP-08 periods.
	type quarterRow struct {
		Quarter            int     `json:"quarter"`
		PeriodStart        string  `json:"period_start"`
		PeriodEnd          string  `json:"period_end"`
		SalesTurnover      float64 `json:"sales_turnover"`
		CalculatedLiability float64 `json:"calculated_liability"`
		CMP08Liability     *float64 `json:"cmp08_liability"`
		CMP08Status        string  `json:"cmp08_status"`
		PaymentStatus      string  `json:"payment_status"`
		TaxPaid            float64 `json:"tax_paid"`
		Difference         *float64 `json:"difference"`
	}
	quarters := make([]quarterRow, 0, 4)
	for q := 1; q <= 4; q++ {
		qs, qe, err := quarterBounds(scope.FY, q)
		if err != nil {
			continue
		}
		row := quarterRow{
			Quarter: q,
			PeriodStart: qs.Format("2006-01-02"),
			PeriodEnd:   qe.Format("2006-01-02"),
			CMP08Status: "not_started",
			PaymentStatus: "not_recorded",
		}
		qScope := &gstReportScope{Registration: scope.Registration, Start: qs, End: qe, FY: scope.FY, Quarter: q, PeriodType: models.GSTPeriodTypeQuarterly}
		_, _, qss, _, qliab := computeGSTReport(userID, qScope)
		row.SalesTurnover = qss.CompositionTurnoverBasis
		row.CalculatedLiability = qliab.Total

		var p models.GSTReturnPeriod
		err = utils.DB.Where("user_id = ? AND financial_year = ? AND period_type = ? AND quarter = ?",
			userID, scope.FY, models.GSTPeriodTypeQuarterly, q).First(&p).Error
		if err == nil {
			row.CMP08Status = p.Status
			if snap, serr := latestGSTSnapshot(p.ID); serr == nil {
				v := snap.TotalCompositionLiability
				row.CMP08Liability = &v
				diff := roundMoney(v - row.CalculatedLiability)
				row.Difference = &diff
			}
			var paid float64
			var allConfirmed = true
			var payments []models.GSTTaxPayment
			utils.DB.Where("gst_return_period_id = ?", p.ID).Find(&payments)
			for _, pay := range payments {
				paid += pay.Amount
				if !pay.PortalConfirmed {
					allConfirmed = false
				}
			}
			row.TaxPaid = roundMoney(paid)
			switch {
			case p.Status == models.GSTPeriodStatusFiled && paid >= row.CalculatedLiability && allConfirmed && paid > 0:
				row.PaymentStatus = "paid_confirmed"
			case paid > 0 && !allConfirmed:
				row.PaymentStatus = "pending_verification"
			case paid > 0:
				row.PaymentStatus = "recorded"
			case p.Status == models.GSTPeriodStatusFiled:
				row.PaymentStatus = "filed_no_payment_recorded"
			default:
				row.PaymentStatus = "not_recorded"
			}
		}
		quarters = append(quarters, row)
	}

	var issues []models.GSTValidationIssue
	var snapshot *models.GSTCalculationSnapshot
	var payments []models.GSTTaxPayment
	if scope.Period != nil {
		utils.DB.Where("gst_return_period_id = ? AND resolution_status = 'open'", scope.Period.ID).
			Order("severity ASC").Find(&issues)
		if s, err := latestGSTSnapshot(scope.Period.ID); err == nil {
			snapshot = s
		}
		utils.DB.Where("gst_return_period_id = ?", scope.Period.ID).Find(&payments)
	}

	var biz models.Business
	utils.DB.Where("user_id = ?", userID).First(&biz)

	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"header": gin.H{
			"business_name": biz.Name,
			"gstin":         regField(scope, func(r *models.GSTRegistration) string { return r.GSTIN }),
			"financial_year": scope.FY,
			"period_start":  scope.Start.Format("2006-01-02"),
			"period_end":    scope.End.Format("2006-01-02"),
			"generated_at":  time.Now().UTC(),
			"period_status": periodStatus(scope),
		},
		"annual_summary": gin.H{
			"annual_turnover":             ss.CompositionTurnoverBasis,
			"taxable_outward_supplies":    ss.TaxableSupplyValue,
			"exempt_outward_supplies":     ss.ExemptSupplyValue,
			"nil_rated_supplies":          ss.NilRatedSupplyValue,
			"non_gst_supplies":            ss.NonGSTSupplyValue,
			"gross_sales":                 ss.GrossSales,
			"net_sales":                   ss.NetSales,
			"registered_supplier_purchases": ps.RegisteredSupplierPurchaseValue,
			"unregistered_supplier_purchases": ps.UnregisteredSupplierPurchaseValue,
			"interstate_purchases":        ps.InterStatePurchaseValue,
			"intrastate_purchases":        ps.IntraStatePurchaseValue,
			"rcm_taxable_value":           ps.RCMTaxableValue,
			"rcm_liability":               ps.RCMTaxLiability,
			"composition_tax_liability":   liab.Total,
			"composition_rate":            liab.CompositionRate,
		},
		"quarterly_reconciliation": quarters,
		"open_issues":              issues,
		"latest_snapshot":          snapshot,
		"payments":                 payments,
		"row_counts":               gin.H{"sales": len(sales), "purchases": len(purchases)},
		"disclaimer":               "This is an annual working paper for review. Generating or exporting it does not file GSTR-4 with the GST portal.",
	}})
}

// ListGSTSnapshots returns the snapshot history for a period.
func ListGSTSnapshots(c *gin.Context) {
	p, ok := loadGSTReturnPeriod(c)
	if !ok {
		return
	}
	var snaps []models.GSTCalculationSnapshot
	utils.DB.Where("gst_return_period_id = ?", p.ID).Order("version DESC").Find(&snaps)
	c.JSON(http.StatusOK, gin.H{"data": snaps})
}

// GetGSTSnapshotDiff compares two snapshot versions for a period.
func GetGSTSnapshotDiff(c *gin.Context) {
	p, ok := loadGSTReturnPeriod(c)
	if !ok {
		return
	}
	v1s, v2s := c.Query("v1"), c.Query("v2")
	var v1, v2 int
	fmt.Sscanf(v1s, "%d", &v1)
	fmt.Sscanf(v2s, "%d", &v2)
	if v1 == 0 || v2 == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "v1 and v2 query params are required"})
		return
	}
	var a, b models.GSTCalculationSnapshot
	if err := utils.DB.Where("gst_return_period_id = ? AND version = ?", p.ID, v1).First(&a).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("snapshot v%d not found", v1)})
		return
	}
	if err := utils.DB.Where("gst_return_period_id = ? AND version = ?", p.ID, v2).First(&b).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("snapshot v%d not found", v2)})
		return
	}
	diff := gin.H{
		"turnover_basis":        roundMoney(b.TurnoverBasis - a.TurnoverBasis),
		"total_composition_liability": roundMoney(b.TotalCompositionLiability - a.TotalCompositionLiability),
		"cgst_liability":        roundMoney(b.CGSTLiability - a.CGSTLiability),
		"sgst_liability":        roundMoney(b.SGSTLiability - a.SGSTLiability),
		"rcm_liability":         roundMoney(b.RCMLiability - a.RCMLiability),
		"total_payable":         roundMoney(b.TotalPayable - a.TotalPayable),
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"v1": a, "v2": b, "difference": diff}})
}

// ensurePeriodForScope finds (or optionally creates) the return period that
// matches the resolved scope. Used by exports so history attaches correctly.
func findPeriodForScope(userID uuid.UUID, scope *gstReportScope) *models.GSTReturnPeriod {
	if scope.Period != nil {
		return scope.Period
	}
	var p models.GSTReturnPeriod
	q := utils.DB.Where("user_id = ? AND period_start = ? AND period_end = ?", userID, scope.Start, scope.End)
	if scope.Registration != nil {
		q = q.Where("gst_registration_id = ?", scope.Registration.ID)
	}
	if scope.PeriodType != "custom" {
		q = q.Where("period_type = ?", scope.PeriodType)
	}
	if err := q.First(&p).Error; err == nil {
		return &p
	}
	return nil
}

// sortSalesRowsByDate orders register rows chronologically then by number.
func sortSalesRowsByDate(rows []gstSalesRegisterRow) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].InvoiceDate.Equal(rows[j].InvoiceDate) {
			return rows[i].InvoiceNumber < rows[j].InvoiceNumber
		}
		return rows[i].InvoiceDate.Before(rows[j].InvoiceDate)
	})
}

func sortPurchaseRowsByDate(rows []gstPurchaseRegisterRow) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].InvoiceDate.Equal(rows[j].InvoiceDate) {
			return rows[i].InvoiceNumber < rows[j].InvoiceNumber
		}
		return rows[i].InvoiceDate.Before(rows[j].InvoiceDate)
	})
}
