package controllers

import (
	"truerp/models"
	"truerp/utils"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type GSTSummary struct {
	Period        string  `json:"period"`
	TotalSales    float64 `json:"total_sales"`
	TotalPurchases float64 `json:"total_purchases"`
	CGST          float64 `json:"cgst"`
	SGST          float64 `json:"sgst"`
	IGST          float64 `json:"igst"`
	TotalTax      float64 `json:"total_tax"`
	Liability     float64 `json:"liability"`
}

func GetGSTSummary(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	period := c.Query("period")

	if period == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "period is required (format: YYYY-MM)"})
		return
	}

	year, month, _ := time.Now().Date()
	var startDate, endDate time.Time
	if _, err := fmt.Sscanf(period, "%d-%d", &year, &month); err == nil {
		startDate = time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
		endDate = startDate.AddDate(0, 1, -1)
	}

	var salesResult struct {
		CGST         float64
		SGST         float64
		IGST         float64
		TotalSales   float64
		TaxableValue float64
		Exempted     float64
		NilRated     float64
	}
	utils.DB.Model(&models.Invoice{}).
		Where("user_id = ? AND date >= ? AND date <= ? AND status != 'cancelled'", userID, startDate, endDate).
		Select(`
			COALESCE(SUM(cgst_total), 0) as cgst,
			COALESCE(SUM(sgst_total), 0) as sgst,
			COALESCE(SUM(igst_total), 0) as igst,
			COALESCE(SUM(total_amount), 0) as total_sales,
			COALESCE(SUM(CASE WHEN tax_rate > 0 THEN sub_total - discount_total ELSE 0 END), 0) as taxable_value,
			COALESCE(SUM(CASE WHEN tax_rate = 0 AND is_inter_state = false THEN sub_total ELSE 0 END), 0) as exempted,
			COALESCE(SUM(CASE WHEN tax_rate = 0 AND is_inter_state = true THEN sub_total ELSE 0 END), 0) as nil_rated
		`).
		Scan(&salesResult)

	var purchaseResult struct {
		CGST         float64
		SGST         float64
		IGST         float64
		TotalPurchases float64
		TaxableValue float64
	}
	utils.DB.Model(&models.PurchaseReceipt{}).
		Where("user_id = ? AND receipt_date >= ? AND receipt_date <= ? AND status = 'submitted'", userID, startDate, endDate).
		Select(`
			COALESCE(SUM(tax_total * 0.5), 0) as cgst,
			COALESCE(SUM(tax_total * 0.5), 0) as sgst,
			COALESCE(SUM(tax_total), 0) as igst,
			COALESCE(SUM(total_amount), 0) as total_purchases,
			COALESCE(SUM(sub_total), 0) as taxable_value
		`).
		Scan(&purchaseResult)

	// Get ITC available
	var itcResult struct {
		CGSTAvailable float64
		SGSTAvailable float64
		IGSTAvailable float64
	}
	utils.DB.Model(&models.InputTaxCredit{}).
		Where("user_id = ? AND tax_period = ? AND status = 'available'", userID, period).
		Select(`
			COALESCE(SUM(cgst_available), 0) as cgst_available,
			COALESCE(SUM(sgst_available), 0) as sgst_available,
			COALESCE(SUM(igst_available), 0) as igst_available
		`).
		Scan(&itcResult)

	summary := GSTSummary{
		Period:         period,
		TotalSales:     salesResult.TotalSales,
		TotalPurchases: purchaseResult.TotalPurchases,
		CGST:           salesResult.CGST - purchaseResult.CGST,
		SGST:           salesResult.SGST - purchaseResult.SGST,
		IGST:           salesResult.IGST - purchaseResult.IGST,
		TotalTax:       (salesResult.CGST + salesResult.SGST + salesResult.IGST) - (purchaseResult.CGST + purchaseResult.SGST + purchaseResult.IGST),
		Liability:      (salesResult.CGST + salesResult.SGST + salesResult.IGST) - (purchaseResult.CGST + purchaseResult.SGST + purchaseResult.IGST),
	}

	c.JSON(http.StatusOK, gin.H{
		"summary": summary,
		"details": gin.H{
			"sales": gin.H{
				"total_sales":     salesResult.TotalSales,
				"taxable_value":   salesResult.TaxableValue,
				"exempted":        salesResult.Exempted,
				"nil_rated":       salesResult.NilRated,
				"cgst":            salesResult.CGST,
				"sgst":            salesResult.SGST,
				"igst":            salesResult.IGST,
			},
			"purchases": gin.H{
				"total_purchases": purchaseResult.TotalPurchases,
				"taxable_value":   purchaseResult.TaxableValue,
				"cgst":            purchaseResult.CGST,
				"sgst":            purchaseResult.SGST,
				"igst":            purchaseResult.IGST,
			},
			"itc": gin.H{
				"cgst_available": itcResult.CGSTAvailable,
				"sgst_available": itcResult.SGSTAvailable,
				"igst_available": itcResult.IGSTAvailable,
				"total_itc":      itcResult.CGSTAvailable + itcResult.SGSTAvailable + itcResult.IGSTAvailable,
			},
		},
	})
}

func GetGSTR1(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	period := c.Query("period")

	if period == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "period is required"})
		return
	}

	year, month, _ := time.Now().Date()
	var startDate, endDate time.Time
	if _, err := fmt.Sscanf(period, "%d-%d", &year, &month); err == nil {
		startDate = time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
		endDate = startDate.AddDate(0, 1, -1)
	}

	var invoices []models.Invoice
	utils.DB.Where("user_id = ? AND date >= ? AND date <= ? AND status != 'cancelled'", userID, startDate, endDate).
		Preload("Party").Preload("Items").Find(&invoices)

	type GSTR1Record struct {
		InvoiceNumber  string  `json:"invoice_number"`
		InvoiceDate    string  `json:"invoice_date"`
		PartyName      string  `json:"party_name"`
		GSTIN          string  `json:"party_gstin"`
		PlaceOfSupply  string  `json:"place_of_supply"`
		InvoiceValue   float64 `json:"invoice_value"`
		TaxableValue   float64 `json:"taxable_value"`
		CGST           float64 `json:"cgst"`
		SGST           float64 `json:"sgst"`
		IGST           float64 `json:"igst"`
		InvoiceType    string  `json:"invoice_type"`
	}

	var records []GSTR1Record
	for _, inv := range invoices {
		records = append(records, GSTR1Record{
			InvoiceNumber:  inv.InvoiceNumber,
			InvoiceDate:    inv.Date.Format("02-01-2006"),
			PartyName:      inv.Party.Name,
			GSTIN:          inv.Party.GSTIN,
			PlaceOfSupply:  inv.Party.State,
			InvoiceValue:   inv.TotalAmount,
			TaxableValue:   inv.SubTotal - inv.DiscountTotal,
			CGST:           inv.CGSTTotal,
			SGST:           inv.SGSTTotal,
			IGST:           inv.IGSTTotal,
			InvoiceType:    inv.InvoiceType,
		})
	}

	c.JSON(http.StatusOK, gin.H{"period": period, "data": records})
}

func GetGSTR2(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	period := c.Query("period")

	if period == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "period is required"})
		return
	}

	year, month, _ := time.Now().Date()
	var startDate, endDate time.Time
	if _, err := fmt.Sscanf(period, "%d-%d", &year, &month); err == nil {
		startDate = time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
		endDate = startDate.AddDate(0, 1, -1)
	}

	var receipts []models.PurchaseReceipt
	utils.DB.Where("user_id = ? AND receipt_date >= ? AND receipt_date <= ? AND status = 'submitted'", userID, startDate, endDate).
		Preload("Party").Preload("Items").Find(&receipts)

	type GSTR2Record struct {
		BillNumber    string  `json:"bill_number"`
		ReceiptDate   string  `json:"receipt_date"`
		PartyName     string  `json:"party_name"`
		GSTIN         string  `json:"party_gstin"`
		InvoiceValue  float64 `json:"invoice_value"`
		TaxableValue  float64 `json:"taxable_value"`
		CGST          float64 `json:"cgst"`
		SGST          float64 `json:"sgst"`
		IGST          float64 `json:"igst"`
	}

	var records []GSTR2Record
	for _, rec := range receipts {
		records = append(records, GSTR2Record{
			BillNumber:   rec.ReceiptNumber,
			ReceiptDate:  rec.ReceiptDate.Format("02-01-2006"),
			PartyName:    rec.Party.Name,
			GSTIN:        rec.Party.GSTIN,
			InvoiceValue: rec.TotalAmount,
			TaxableValue: rec.SubTotal,
			CGST:         rec.TaxTotal * 0.5,
			SGST:         rec.TaxTotal * 0.5,
			IGST:         rec.TaxTotal,
		})
	}

	c.JSON(http.StatusOK, gin.H{"period": period, "data": records})
}

func GetGSTR3B(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	period := c.Query("period")

	if period == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "period is required"})
		return
	}

	year, month, _ := time.Now().Date()
	var startDate, endDate time.Time
	if _, err := fmt.Sscanf(period, "%d-%d", &year, &month); err == nil {
		startDate = time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
		endDate = startDate.AddDate(0, 1, -1)
	}

	type GSTR3B struct {
		Period           string  `json:"period"`
		TotalTaxLiability float64 `json:"total_tax_liability"`
		IGSTLiability    float64 `json:"igst_liability"`
		CGSTLiability    float64 `json:"cgst_liability"`
		SGSTLiability    float64 `json:"sgst_liability"`
		TotalITC         float64 `json:"total_itc"`
		TaxPayable       float64 `json:"tax_payable"`
	}

	var gstr3bResult struct {
		CGST  float64
		SGST  float64
		IGST  float64
	}
	utils.DB.Model(&models.Invoice{}).
		Where("user_id = ? AND date >= ? AND date <= ? AND status != 'cancelled'", userID, startDate, endDate).
		Select("COALESCE(SUM(cgst_total), 0) as cgst, COALESCE(SUM(sgst_total), 0) as sgst, COALESCE(SUM(igst_total), 0) as igst").
		Scan(&gstr3bResult)

	summary := GSTR3B{
		Period:            period,
		CGSTLiability:     gstr3bResult.CGST,
		SGSTLiability:     gstr3bResult.SGST,
		IGSTLiability:     gstr3bResult.IGST,
		TotalTaxLiability: gstr3bResult.CGST + gstr3bResult.SGST + gstr3bResult.IGST,
		TaxPayable:        gstr3bResult.CGST + gstr3bResult.SGST + gstr3bResult.IGST,
	}

	c.JSON(http.StatusOK, summary)
}

type EInvoiceRequest struct {
	InvoiceID uuid.UUID `json:"invoice_id" binding:"required"`
}

type EInvoiceResponse struct {
	IRN           string    `json:"irn"`
	InvoiceID     uuid.UUID `json:"invoice_id"`
	Status        string    `json:"status"`
	QRCode        string    `json:"qr_code"`
	EWBNumber     string    `json:"ewb_number,omitempty"`
	GeneratedAt   time.Time `json:"generated_at"`
}

func GenerateEInvoice(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input EInvoiceRequest
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var invoice models.Invoice
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, input.InvoiceID).First(&invoice).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Invoice not found"})
		return
	}

	if invoice.TotalAmount < 10000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "E-invoice not required for amounts below ₹10,000"})
		return
	}

	now := time.Now()
	irn := generateIRN(invoice.InvoiceNumber, invoice.TotalAmount)

	if err := utils.DB.Model(&invoice).Updates(map[string]interface{}{
		"irn":                    irn,
		"e_invoice_status":       "generated",
		"e_invoice_generated_at": now,
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save e-invoice"})
		return
	}

	response := EInvoiceResponse{
		IRN:         irn,
		InvoiceID:   invoice.ID,
		Status:      "Generated",
		QRCode:      "IRN:" + irn,
		GeneratedAt: now,
	}

	c.JSON(http.StatusOK, response)
}

func CancelEInvoice(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		InvoiceID    uuid.UUID `json:"invoice_id" binding:"required"`
		CancelReason string    `json:"cancel_reason" binding:"required"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var invoice models.Invoice
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, input.InvoiceID).First(&invoice).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Invoice not found"})
		return
	}

	if invoice.IRN == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No e-invoice found for this invoice"})
		return
	}

	now := time.Now()
	if err := utils.DB.Model(&invoice).Updates(map[string]interface{}{
		"e_invoice_status": "cancelled",
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to cancel e-invoice"})
		return
	}

	response := EInvoiceResponse{
		IRN:         invoice.IRN,
		InvoiceID:   input.InvoiceID,
		Status:      "Cancelled",
		GeneratedAt: now,
	}

	c.JSON(http.StatusOK, response)
}

type EWayBillRequest struct {
	InvoiceID   uuid.UUID `json:"invoice_id" binding:"required"`
	Transporter string    `json:"transporter"`
	VehicleNo   string    `json:"vehicle_no"`
	Distance    int       `json:"distance"`
}

type EWayBillResponse struct {
	EWBNumber   string    `json:"ewb_number"`
	InvoiceID   uuid.UUID `json:"invoice_id"`
	Status      string    `json:"status"`
	ValidUntil  time.Time `json:"valid_until"`
	GeneratedAt time.Time `json:"generated_at"`
}

func GenerateEWayBill(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input EWayBillRequest
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var invoice models.Invoice
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, input.InvoiceID).First(&invoice).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Invoice not found"})
		return
	}

	now := time.Now()
	validUntil := now.AddDate(0, 0, 8)
	ewbNumber := generateEWBNumber()

	if err := utils.DB.Model(&invoice).Updates(map[string]interface{}{
		"eway_bill_number":      ewbNumber,
		"eway_bill_status":      "generated",
		"eway_bill_valid_until": validUntil,
		"eway_bill_required":    true,
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save e-way bill"})
		return
	}

	response := EWayBillResponse{
		EWBNumber:   ewbNumber,
		InvoiceID:   input.InvoiceID,
		Status:      "Generated",
		ValidUntil:  validUntil,
		GeneratedAt: now,
	}

	c.JSON(http.StatusOK, response)
}

func CancelEWayBill(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		EWBNo        string `json:"ewb_no" binding:"required"`
		CancelReason string `json:"cancel_reason" binding:"required"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var invoice models.Invoice
	if err := utils.DB.Where("user_id = ? AND eway_bill_number = ?", userID, input.EWBNo).First(&invoice).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "E-way bill not found"})
		return
	}

	if err := utils.DB.Model(&invoice).Updates(map[string]interface{}{
		"eway_bill_status": "cancelled",
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to cancel e-way bill"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "Cancelled", "ewb_number": input.EWBNo})
}

func GetEInvoiceHistory(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var invoices []models.Invoice
	if err := utils.DB.Where("user_id = ? AND irn != '' AND e_invoice_status IN ?", userID, []string{"generated", "cancelled"}).
		Order("updated_at DESC").
		Find(&invoices).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch e-invoice history"})
		return
	}

	history := make([]gin.H, 0, len(invoices))
	for _, inv := range invoices {
		status := "Generated"
		if inv.EInvoiceStatus == "cancelled" {
			status = "Cancelled"
		}
		generatedAt := inv.UpdatedAt
		if inv.EInvoiceGeneratedAt != nil {
			generatedAt = *inv.EInvoiceGeneratedAt
		}
		history = append(history, gin.H{
			"irn":            inv.IRN,
			"invoice_id":     inv.ID,
			"invoice_number": inv.InvoiceNumber,
			"status":         status,
			"generated_at":   generatedAt,
		})
	}

	c.JSON(http.StatusOK, history)
}

func GetEWayBillHistory(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var invoices []models.Invoice
	if err := utils.DB.Where("user_id = ? AND eway_bill_number != '' AND eway_bill_status IN ?", userID, []string{"generated", "cancelled"}).
		Order("updated_at DESC").
		Find(&invoices).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch e-way bill history"})
		return
	}

	history := make([]gin.H, 0, len(invoices))
	for _, inv := range invoices {
		status := "Generated"
		if inv.EWayBillStatus == "cancelled" {
			status = "Cancelled"
		}
		validUntil := inv.UpdatedAt.AddDate(0, 0, 8)
		if inv.EWayBillValidUntil != nil {
			validUntil = *inv.EWayBillValidUntil
		}
		history = append(history, gin.H{
			"ewb_number":     inv.EWayBillNumber,
			"invoice_id":     inv.ID,
			"invoice_number": inv.InvoiceNumber,
			"status":         status,
			"valid_until":    validUntil,
		})
	}

	c.JSON(http.StatusOK, history)
}

func GetHSNRates(c *gin.Context) {
	code := c.Query("code")

	hsnRates := map[string]struct {
		Code        string  `json:"code"`
		Description string  `json:"description"`
		CGSTRate    float64 `json:"cgst_rate"`
		SGSTRate    float64 `json:"sgst_rate"`
		IGSTRate    float64 `json:"igst_rate"`
	}{
		"1001": {Code: "1001", Description: "Wheat and meslin", CGSTRate: 0, SGSTRate: 0, IGSTRate: 0},
		"0201": {Code: "0201", Description: "Meat of bovine animals", CGSTRate: 2.5, SGSTRate: 2.5, IGSTRate: 5},
		"2401": {Code: "2401", Description: "Unmanufactured tobacco", CGSTRate: 2.5, SGSTRate: 2.5, IGSTRate: 5},
		"8702": {Code: "8702", Description: "Motor vehicles for transport", CGSTRate: 9, SGSTRate: 9, IGSTRate: 18},
		"6111": {Code: "6111", Description: "Babies garments and clothing accessories", CGSTRate: 5, SGSTRate: 5, IGSTRate: 10},
		"8517": {Code: "8517", Description: "Telephone sets", CGSTRate: 9, SGSTRate: 9, IGSTRate: 18},
	}

	if rate, ok := hsnRates[strings.ToUpper(code)]; ok {
		c.JSON(http.StatusOK, rate)
	} else {
		c.JSON(http.StatusOK, gin.H{"code": code, "cgst_rate": 9, "sgst_rate": 9, "igst_rate": 18})
	}
}

func SearchHSNCodes(c *gin.Context) {
	search := strings.ToUpper(c.Query("search"))
	useAI := c.Query("use_ai") == "true"

	fmt.Printf("[DEBUG] SearchHSNCodes - search: %s, useAI: %v\n", search, useAI)

	// If AI search is requested, use Gemini
	if useAI {
		searchHSNWithAI(c, search)
		return
	}
	
	// Read HSN codes from CSV file
	file, err := os.Open("HSN_DATASET.csv")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to open HSN dataset"})
		return
	}
	defer file.Close()

	reader := csv.NewReader(file)
	records, err := reader.ReadAll()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read HSN dataset"})
		return
	}

	type HSNCode struct {
		Code        string  `json:"code"`
		Description string  `json:"description"`
		CGSTRate    float64 `json:"cgst_rate"`
		SGSTRate    float64 `json:"sgst_rate"`
		IGSTRate    float64 `json:"igst_rate"`
	}

	var hsnCodes []HSNCode
	// Skip header row
	for i := 1; i < len(records); i++ {
		if len(records[i]) >= 2 {
			hsnCodes = append(hsnCodes, HSNCode{
				Code:        records[i][0],
				Description: records[i][1],
				CGSTRate:    9, // Default rate
				SGSTRate:    9, // Default rate
				IGSTRate:    18, // Default rate
			})
		}
	}

	var results []interface{}
	if search == "" {
		// Return first 100 results if no search
		limit := 100
		if len(hsnCodes) < limit {
			limit = len(hsnCodes)
		}
		results = make([]interface{}, limit)
		for i := 0; i < limit; i++ {
			results[i] = hsnCodes[i]
		}
	} else {
		for _, code := range hsnCodes {
			if strings.Contains(code.Code, search) || strings.Contains(strings.ToUpper(code.Description), search) {
				results = append(results, code)
				if len(results) >= 10 {
					break
				}
			}
		}
	}
	
	c.JSON(http.StatusOK, results)
}

func searchHSNWithAI(c *gin.Context, search string) {
	fmt.Printf("[DEBUG] searchHSNWithAI - search: %s\n", search)

	userID, exists := c.Get("user_id")
	if !exists {
		fmt.Printf("[DEBUG] searchHSNWithAI - user_id not found in context\n")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	userIDStr, ok := userID.(uuid.UUID)
	if !ok {
		fmt.Printf("[DEBUG] searchHSNWithAI - user_id is not uuid.UUID, got type: %T\n", userID)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid user ID format"})
		return
	}

	fmt.Printf("[DEBUG] searchHSNWithAI - userID: %s\n", userIDStr)

	// Get business settings to check if AI is enabled and get API key
	var business models.Business
	if err := utils.DB.Where("user_id = ?", userIDStr).First(&business).Error; err != nil {
		fmt.Printf("[DEBUG] searchHSNWithAI - Business settings not found: %v\n", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Business settings not found"})
		return
	}

	fmt.Printf("[DEBUG] searchHSNWithAI - EnableAIHSNSearch: %v, GeminiAPIKey configured: %v\n", business.EnableAIHSNSearch, business.GeminiAPIKey != "")
	
	if !business.EnableAIHSNSearch {
		c.JSON(http.StatusBadRequest, gin.H{"error": "AI HSN search is not enabled"})
		return
	}
	
	if business.GeminiAPIKey == "" {
		fmt.Printf("[DEBUG] searchHSNWithAI - Gemini API key not configured\n")
		c.JSON(http.StatusBadRequest, gin.H{"error": "Gemini API key not configured"})
		return
	}

	apiKey := business.GeminiAPIKey
	fmt.Printf("[DEBUG] searchHSNWithAI - Calling Gemini API for search: %s\n", search)

	// Call Gemini API with strict JSON response requirement
	prompt := fmt.Sprintf(`You are a HSN code expert. Find the most appropriate HSN code for: "%s".

IMPORTANT: Return ONLY a valid JSON object. No markdown, no explanations, no extra text.

If you find a matching HSN code, return this exact format:
{
  "status": "success",
  "code": "HSN_CODE",
  "description": "Description",
  "cgst_rate": 9,
  "sgst_rate": 9,
  "igst_rate": 18
}

If you cannot find a matching HSN code or there's an error, return this exact format:
{
  "status": "error",
  "error": "Error message describing why you couldn't find it"
}

Return ONLY the JSON, nothing else.`, search)
	
	requestBody := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"parts": []map[string]interface{}{
					{
						"text": prompt,
					},
				},
			},
		},
	}
	
	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create request"})
		return
	}
	
	req, err := http.NewRequest("POST", "https://generativelanguage.googleapis.com/v1/models/gemini-2.5-flash:generateContent?key="+apiKey, bytes.NewBuffer(jsonBody))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create request"})
		return
	}
	
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("[DEBUG] searchHSNWithAI - Failed to call Gemini API: %v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to call Gemini API"})
		return
	}
	defer resp.Body.Close()

	fmt.Printf("[DEBUG] searchHSNWithAI - Gemini API response status: %d\n", resp.StatusCode)
	
	if resp.StatusCode != http.StatusOK {
		// Read error body for more details
		bodyBytes, _ := io.ReadAll(resp.Body)
		fmt.Printf("[DEBUG] searchHSNWithAI - Gemini API error response: %s\n", string(bodyBytes))
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Gemini API error: %s", string(bodyBytes))})
		return
	}
	
	var geminiResponse struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	
	if err := json.NewDecoder(resp.Body).Decode(&geminiResponse); err != nil {
		fmt.Printf("[DEBUG] searchHSNWithAI - Failed to parse Gemini response: %v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse Gemini response"})
		return
	}
	
	if len(geminiResponse.Candidates) == 0 || len(geminiResponse.Candidates[0].Content.Parts) == 0 {
		fmt.Printf("[DEBUG] searchHSNWithAI - No response from Gemini (candidates: %d)\n", len(geminiResponse.Candidates))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "No response from Gemini"})
		return
	}
	
	// Extract text from response
	text := strings.TrimSpace(geminiResponse.Candidates[0].Content.Parts[0].Text)
	fmt.Printf("[DEBUG] searchHSNWithAI - Raw Gemini response text: %s\n", text)

	// Try to extract JSON from the text (in case AI added markdown)
	text = extractJSON(text)
	fmt.Printf("[DEBUG] searchHSNWithAI - Extracted JSON: %s\n", text)
	
	// Parse the JSON response from Gemini
	var aiResponse struct {
		Status      string  `json:"status"`
		Code        string  `json:"code,omitempty"`
		Description string  `json:"description,omitempty"`
		CGSTRate    float64 `json:"cgst_rate,omitempty"`
		SGSTRate    float64 `json:"sgst_rate,omitempty"`
		IGSTRate    float64 `json:"igst_rate,omitempty"`
		Error       string  `json:"error,omitempty"`
	}
	
	if err := json.Unmarshal([]byte(text), &aiResponse); err != nil {
		fmt.Printf("[DEBUG] searchHSNWithAI - Failed to parse Gemini result as JSON: %v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse Gemini result as JSON"})
		return
	}
	
	// Check if AI returned an error
	if aiResponse.Status == "error" {
		fmt.Printf("[DEBUG] searchHSNWithAI - AI returned error: %s\n", aiResponse.Error)
		c.JSON(http.StatusNotFound, gin.H{"error": aiResponse.Error})
		return
	}
	
	// Validate required fields for success response
	if aiResponse.Code == "" {
		fmt.Printf("[DEBUG] searchHSNWithAI - Invalid response: missing HSN code\n")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid response: missing HSN code"})
		return
	}

	fmt.Printf("[DEBUG] searchHSNWithAI - Successfully found HSN code: %s - %s\n", aiResponse.Code, aiResponse.Description)
	
	result := struct {
		Code        string  `json:"code"`
		Description string  `json:"description"`
		CGSTRate    float64 `json:"cgst_rate"`
		SGSTRate    float64 `json:"sgst_rate"`
		IGSTRate    float64 `json:"igst_rate"`
	}{
		Code:        aiResponse.Code,
		Description: aiResponse.Description,
		CGSTRate:    aiResponse.CGSTRate,
		SGSTRate:    aiResponse.SGSTRate,
		IGSTRate:    aiResponse.IGSTRate,
	}
	
	c.JSON(http.StatusOK, []interface{}{result})
}

// extractJSON extracts JSON from a string that might contain markdown or other text
func extractJSON(text string) string {
	// Try to find JSON object boundaries
	startIdx := strings.Index(text, "{")
	endIdx := strings.LastIndex(text, "}")
	
	if startIdx != -1 && endIdx != -1 && endIdx > startIdx {
		return text[startIdx : endIdx+1]
	}
	
	return text
}

func generateIRN(invoiceNum string, amount float64) string {
	raw := uuid.New().String() + uuid.New().String()
	return strings.ReplaceAll(raw, "-", "")
}

func generateEWBNumber() string {
	return fmt.Sprintf("29%010d", time.Now().UnixNano()%1e10)
}

// ValidateGSTIN validates a GSTIN
func ValidateGSTIN(c *gin.Context) {
	var input struct {
		GSTIN string `json:"gstin" binding:"required"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	isValid := utils.ValidateGSTIN(input.GSTIN)
	
	response := gin.H{
		"gstin":    input.GSTIN,
		"valid":    isValid,
	}

	if isValid {
		stateCode := utils.GetStateCodeFromGSTIN(input.GSTIN)
		stateName := utils.GetStateNameFromCode(stateCode)
		response["state_code"] = stateCode
		response["state_name"] = stateName
		response["masked_gstin"] = utils.MaskGSTIN(input.GSTIN)
	}

	c.JSON(http.StatusOK, response)
}

// GetStateCodes returns all Indian state codes for GST
func GetStateCodes(c *gin.Context) {
	c.JSON(http.StatusOK, utils.StateCodes)
}

// CreateTaxPeriod creates a new tax period
func CreateTaxPeriod(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		Period string `json:"period" binding:"required"` // Format: YYYY-MM
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Parse period
	year, month, err := parsePeriod(input.Period)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid period format. Use YYYY-MM"})
		return
	}

	startDate := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
	endDate := startDate.AddDate(0, 1, -1)

	// Check if period already exists
	var existing models.TaxPeriod
	if err := utils.DB.Where("user_id = ? AND period = ?", userID, input.Period).First(&existing).Error; err == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "Tax period already exists", "tax_period": existing})
		return
	}

	taxPeriod := models.TaxPeriod{
		ID:        uuid.New(),
		UserID:    userID,
		Period:    input.Period,
		StartDate: startDate,
		EndDate:   endDate,
		Status:    "open",
	}

	if err := utils.DB.Create(&taxPeriod).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create tax period"})
		return
	}

	c.JSON(http.StatusCreated, taxPeriod)
}

// GetTaxPeriods returns all tax periods for the user
func GetTaxPeriods(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var taxPeriods []models.TaxPeriod
	if err := utils.DB.Where("user_id = ?", userID).Order("period DESC").Find(&taxPeriods).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch tax periods"})
		return
	}

	c.JSON(http.StatusOK, taxPeriods)
}

// GetTaxPeriod returns a specific tax period
func GetTaxPeriod(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var taxPeriod models.TaxPeriod
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&taxPeriod).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Tax period not found"})
		return
	}

	c.JSON(http.StatusOK, taxPeriod)
}

// UpdateTaxPeriod updates a tax period
func UpdateTaxPeriod(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var input struct {
		Status      string     `json:"status"`
		GSTR1Status string     `json:"gstr1_status"`
		GSTR3BStatus string    `json:"gstr3b_status"`
		GSTR1FiledAt *time.Time `json:"gstr1_filed_at"`
		GSTR3BFiledAt *time.Time `json:"gstr3b_filed_at"`
		Notes       string     `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var taxPeriod models.TaxPeriod
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&taxPeriod).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Tax period not found"})
		return
	}

	updates := map[string]interface{}{}
	if input.Status != "" {
		updates["status"] = input.Status
	}
	if input.GSTR1Status != "" {
		updates["gstr1_status"] = input.GSTR1Status
	}
	if input.GSTR3BStatus != "" {
		updates["gstr3b_status"] = input.GSTR3BStatus
	}
	if input.GSTR1FiledAt != nil {
		updates["gstr1_filed_at"] = input.GSTR1FiledAt
	}
	if input.GSTR3BFiledAt != nil {
		updates["gstr3b_filed_at"] = input.GSTR3BFiledAt
	}
	if input.Notes != "" {
		updates["notes"] = input.Notes
	}

	if err := utils.DB.Model(&taxPeriod).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update tax period"})
		return
	}

	utils.DB.First(&taxPeriod, taxPeriod.ID)
	c.JSON(http.StatusOK, taxPeriod)
}

// CreateITC creates Input Tax Credit entry
func CreateITC(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		TaxPeriod    string    `json:"tax_period" binding:"required"`
		SourceID     uuid.UUID `json:"source_id" binding:"required"`
		SourceType   string    `json:"source_type" binding:"required"` // purchase_receipt, purchase_bill
		CGSTAvailable float64   `json:"cgst_available"`
		SGSTAvailable float64   `json:"sgst_available"`
		IGSTAvailable float64   `json:"igst_available"`
		Notes        string    `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	itc := models.InputTaxCredit{
		ID:            uuid.New(),
		UserID:        userID,
		TaxPeriod:     input.TaxPeriod,
		SourceID:      input.SourceID,
		SourceType:    input.SourceType,
		CGSTAvailable: input.CGSTAvailable,
		SGSTAvailable: input.SGSTAvailable,
		IGSTAvailable: input.IGSTAvailable,
		EligibleDate:  time.Now(),
		Status:        "available",
		Notes:         input.Notes,
	}

	if err := utils.DB.Create(&itc).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create ITC entry"})
		return
	}

	c.JSON(http.StatusCreated, itc)
}

// GetITC returns ITC entries for a tax period
func GetITC(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	taxPeriod := c.Query("tax_period")

	var itcEntries []models.InputTaxCredit
	query := utils.DB.Where("user_id = ?", userID)
	
	if taxPeriod != "" {
		query = query.Where("tax_period = ?", taxPeriod)
	}

	if err := query.Order("created_at DESC").Find(&itcEntries).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch ITC entries"})
		return
	}

	c.JSON(http.StatusOK, itcEntries)
}

// UtilizeITC marks ITC as utilized
func UtilizeITC(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var input struct {
		CGSTUtilized float64 `json:"cgst_utilized"`
		SGSTUtilized float64 `json:"sgst_utilized"`
		IGSTUtilized float64 `json:"igst_utilized"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var itc models.InputTaxCredit
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&itc).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "ITC entry not found"})
		return
	}

	if err := utils.DB.Model(&itc).Updates(map[string]interface{}{
		"cgst_utilized": itc.CGSTUtilized + input.CGSTUtilized,
		"sgst_utilized": itc.SGSTUtilized + input.SGSTUtilized,
		"igst_utilized": itc.IGSTUtilized + input.IGSTUtilized,
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update ITC"})
		return
	}

	utils.DB.First(&itc, itc.ID)
	c.JSON(http.StatusOK, itc)
}

// GenerateGSTR1Export generates GSTR-1 data in GST portal format
func GenerateGSTR1Export(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	period := c.Query("period")

	if period == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "period is required (format: YYYY-MM)"})
		return
	}

	year, month, err := parsePeriod(period)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid period format"})
		return
	}

	startDate := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
	endDate := startDate.AddDate(0, 1, -1)

	// Get business details for state code
	var business models.Business
	utils.DB.Where("user_id = ?", userID).First(&business)

	// Fetch invoices for the period
	var invoices []models.Invoice
	utils.DB.Where("user_id = ? AND date >= ? AND date <= ? AND status != 'cancelled'", 
		userID, startDate, endDate).
		Preload("Party").Preload("Items").Find(&invoices)

	// Prepare GSTR-1 data structure
	gstr1Data := gin.H{
		"gstin":    business.GSTIN,
		"period":   period,
		"fp":       period, // Filing period
		"b2b":      []gin.H{},
		"b2cl":     []gin.H{},
		"b2cs":     []gin.H{},
		"exp":      []gin.H{},
		"at":       []gin.H{},
		"txpd":     []gin.H{},
		"hsn":      []gin.H{},
		"doc_issue": gin.H{},
	}

	for _, inv := range invoices {
		if inv.Party.GSTIN != "" {
			// B2B invoices
			b2bRecord := gin.H{
				"ctin":       inv.Party.GSTIN,
				"inv": []gin.H{{
					"inum":   inv.InvoiceNumber,
					"idt":    inv.Date.Format("02-01-2006"),
					"val":    inv.TotalAmount,
					"pos":    inv.PlaceOfSupply,
					"rchrg":  map[bool]string{true: "Y", false: "N"}[inv.ReverseCharge],
					"inv_typ": "R", // Regular
					"itms":   []gin.H{},
				}},
			}

			for _, item := range inv.Items {
				itemData := gin.H{
					"txval": item.Total - item.CGST - item.SGST - item.IGST,
					"rt":    item.TaxRate,
					"iamt":  item.IGST,
					"camt":  item.CGST,
					"samt":  item.SGST,
					"csamt": 0,
				}
				b2bRecord["inv"].([]gin.H)[0]["itms"] = append(b2bRecord["inv"].([]gin.H)[0]["itms"].([]gin.H), itemData)
			}

			gstr1Data["b2b"] = append(gstr1Data["b2b"].([]gin.H), b2bRecord)
		} else {
			// B2C invoices (without GSTIN)
			if inv.TotalAmount >= 250000 {
				// B2C Large
				b2clRecord := gin.H{
					"pos":  inv.PlaceOfSupply,
					"inv": []gin.H{{
						"inum": inv.InvoiceNumber,
						"idt":  inv.Date.Format("02-01-2006"),
						"val":  inv.TotalAmount,
						"itms": []gin.H{},
					}},
				}

				for _, item := range inv.Items {
					itemData := gin.H{
						"txval": item.Total - item.CGST - item.SGST - item.IGST,
						"rt":    item.TaxRate,
						"iamt":  item.IGST,
						"camt":  item.CGST,
						"samt":  item.SGST,
					}
					b2clRecord["inv"].([]gin.H)[0]["itms"] = append(b2clRecord["inv"].([]gin.H)[0]["itms"].([]gin.H), itemData)
				}

				gstr1Data["b2cl"] = append(gstr1Data["b2cl"].([]gin.H), b2clRecord)
			} else {
				// B2C Small
				b2csRecord := gin.H{
					"pos":   inv.PlaceOfSupply,
					"typ":   "OE", // Other than Exports
					"txval": inv.SubTotal,
					"iamt":  inv.IGSTTotal,
					"camt":  inv.CGSTTotal,
					"samt":  inv.SGSTTotal,
				}
				gstr1Data["b2cs"] = append(gstr1Data["b2cs"].([]gin.H), b2csRecord)
			}
		}
	}

	c.JSON(http.StatusOK, gstr1Data)
}

// GenerateGSTR3BExport generates GSTR-3B data in GST portal format
func GenerateGSTR3BExport(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	period := c.Query("period")

	if period == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "period is required (format: YYYY-MM)"})
		return
	}

	year, month, err := parsePeriod(period)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid period format"})
		return
	}

	startDate := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
	endDate := startDate.AddDate(0, 1, -1)

	// Get business details
	var business models.Business
	utils.DB.Where("user_id = ?", userID).First(&business)

	// Calculate outward supplies (sales)
	var salesResult struct {
		Taxable   float64
		NilRated  float64
		Exempted  float64
		NonGST    float64
		CGST      float64
		SGST      float64
		IGST      float64
		CESS      float64
	}

	utils.DB.Model(&models.Invoice{}).
		Where("user_id = ? AND date >= ? AND date <= ? AND status != 'cancelled'", userID, startDate, endDate).
		Select(`
			COALESCE(SUM(CASE WHEN tax_rate = 0 THEN sub_total ELSE 0 END), 0) as nil_rated,
			COALESCE(SUM(CASE WHEN tax_rate = 0 THEN sub_total ELSE 0 END), 0) as exempted,
			COALESCE(SUM(sub_total), 0) as taxable,
			COALESCE(SUM(cgst_total), 0) as cgst,
			COALESCE(SUM(sgst_total), 0) as sgst,
			COALESCE(SUM(igst_total), 0) as igst
		`).
		Scan(&salesResult)

	// Calculate ITC from purchases
	var itcResult struct {
		CGST      float64
		SGST      float64
		IGST      float64
		CESS      float64
	}

	utils.DB.Model(&models.InputTaxCredit{}).
		Where("user_id = ? AND tax_period = ? AND status = 'available'", userID, period).
		Select(`
			COALESCE(SUM(cgst_available), 0) as cgst,
			COALESCE(SUM(sgst_available), 0) as sgst,
			COALESCE(SUM(igst_available), 0) as igst
		`).
		Scan(&itcResult)

	// Calculate tax payable
	cgstPayable := salesResult.CGST - itcResult.CGST
	sgstPayable := salesResult.SGST - itcResult.SGST
	igstPayable := salesResult.IGST - itcResult.IGST
	_ = cgstPayable + sgstPayable + igstPayable // totalTaxPayable (unused for now)

	gstr3bData := gin.H{
		"gstin": business.GSTIN,
		"ret_pd": period,
		"sup_details": gin.H{
			"osup_det": gin.H{
				"txval": salesResult.Taxable,
				"iamt":  salesResult.IGST,
				"camt":  salesResult.CGST,
				"samt":  salesResult.SGST,
				"csamt": salesResult.CESS,
			},
			"isup_det": gin.H{
				"txval": 0,
				"iamt":  0,
				"camt":  0,
				"samt":  0,
				"csamt": 0,
			},
		},
		"itc_elg": gin.H{
			"itc_avl": gin.H{
				"ia": gin.H{
					"txval": 0,
					"iamt":  itcResult.IGST,
					"camt":  itcResult.CGST,
					"samt":  itcResult.SGST,
					"csamt": itcResult.CESS,
				},
			},
		},
		"inpt_tx": gin.H{
			"ia": gin.H{
				"camt": cgstPayable,
				"samt": sgstPayable,
				"iamt": igstPayable,
				"csamt": 0,
			},
		},
		"tx_pd": gin.H{
			"tx": gin.H{
				"camt": cgstPayable,
				"samt": sgstPayable,
				"iamt": igstPayable,
				"csamt": 0,
			},
		},
	}

	c.JSON(http.StatusOK, gstr3bData)
}

// RecordGSTFiling records GST return filing status
func RecordGSTFiling(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		TaxPeriod       string    `json:"tax_period" binding:"required"`
		ReturnType      string    `json:"return_type" binding:"required"` // GSTR1, GSTR3B
		Status          string    `json:"status" binding:"required"`
		ARN             string    `json:"arn"`
		ReferenceNumber string    `json:"reference_number"`
		TotalTaxLiability float64 `json:"total_tax_liability"`
		TaxPaid         float64   `json:"tax_paid"`
		Interest        float64   `json:"interest"`
		Penalty         float64   `json:"penalty"`
		Notes           string    `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	filing := models.GSTFilingStatus{
		ID:              uuid.New(),
		UserID:          userID,
		TaxPeriod:       input.TaxPeriod,
		ReturnType:      input.ReturnType,
		Status:          input.Status,
		ARN:             input.ARN,
		ReferenceNumber: input.ReferenceNumber,
		TotalTaxLiability: input.TotalTaxLiability,
		TaxPaid:         input.TaxPaid,
		Interest:        input.Interest,
		Penalty:         input.Penalty,
		TotalAmountPaid: input.TaxPaid + input.Interest + input.Penalty,
		Notes:           input.Notes,
	}

	if input.Status == "filed" || input.Status == "late_filed" {
		now := time.Now()
		filing.FilingDate = &now
	}

	if err := utils.DB.Create(&filing).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to record filing status"})
		return
	}

	// Update tax period status
	if input.ReturnType == "GSTR1" {
		utils.DB.Model(&models.TaxPeriod{}).
			Where("user_id = ? AND period = ?", userID, input.TaxPeriod).
			Updates(map[string]interface{}{
				"gstr1_status": input.Status,
				"gstr1_filed_at": filing.FilingDate,
			})
	} else if input.ReturnType == "GSTR3B" {
		utils.DB.Model(&models.TaxPeriod{}).
			Where("user_id = ? AND period = ?", userID, input.TaxPeriod).
			Updates(map[string]interface{}{
				"gstr3b_status": input.Status,
				"gstr3b_filed_at": filing.FilingDate,
			})
	}

	c.JSON(http.StatusCreated, filing)
}

// GetGSTFilingStatus returns filing status for a tax period
func GetGSTFilingStatus(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	taxPeriod := c.Query("tax_period")

	var filings []models.GSTFilingStatus
	query := utils.DB.Where("user_id = ?", userID)
	
	if taxPeriod != "" {
		query = query.Where("tax_period = ?", taxPeriod)
	}

	if err := query.Order("created_at DESC").Find(&filings).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch filing status"})
		return
	}

	c.JSON(http.StatusOK, filings)
}

// parsePeriod parses period string in YYYY-MM format
func parsePeriod(period string) (int, time.Month, error) {
	var year int
	var month int
	_, err := fmt.Sscanf(period, "%d-%d", &year, &month)
	if err != nil {
		return 0, 0, err
	}
	return year, time.Month(month), nil
}

// CalculateTax calculates tax based on pricing type (inclusive or exclusive)
func CalculateTax(c *gin.Context) {
	var input struct {
		Amount      float64 `json:"amount" binding:"required"`
		TaxRate     float64 `json:"tax_rate" binding:"required"`
		IsInclusive bool    `json:"is_inclusive"` // true if amount includes tax
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var taxableAmount, taxAmount, totalAmount float64

	if input.IsInclusive {
		// Amount includes tax, calculate taxable amount
		taxableAmount = input.Amount / (1 + input.TaxRate/100)
		taxAmount = input.Amount - taxableAmount
		totalAmount = input.Amount
	} else {
		// Amount is exclusive, calculate tax
		taxableAmount = input.Amount
		taxAmount = input.Amount * (input.TaxRate / 100)
		totalAmount = input.Amount + taxAmount
	}

	// For GST, split into CGST and SGST (assuming intra-state)
	cgst := taxAmount / 2
	sgst := taxAmount / 2
	igst := taxAmount // For inter-state

	c.JSON(http.StatusOK, gin.H{
		"taxable_amount": taxableAmount,
		"tax_amount":     taxAmount,
		"total_amount":   totalAmount,
		"cgst":           cgst,
		"sgst":           sgst,
		"igst":           igst,
		"tax_rate":       input.TaxRate,
		"is_inclusive":   input.IsInclusive,
	})
}

// ConvertPrice converts price between tax-inclusive and tax-exclusive
func ConvertPrice(c *gin.Context) {
	var input struct {
		Price       float64 `json:"price" binding:"required"`
		TaxRate     float64 `json:"tax_rate" binding:"required"`
		FromType    string  `json:"from_type" binding:"required"` // inclusive, exclusive
		ToType      string  `json:"to_type" binding:"required"`   // inclusive, exclusive
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var convertedPrice, taxableAmount, taxAmount float64

	if input.FromType == "inclusive" && input.ToType == "exclusive" {
		// Convert from inclusive to exclusive
		taxableAmount = input.Price / (1 + input.TaxRate/100)
		taxAmount = input.Price - taxableAmount
		convertedPrice = taxableAmount
	} else if input.FromType == "exclusive" && input.ToType == "inclusive" {
		// Convert from exclusive to inclusive
		taxableAmount = input.Price
		taxAmount = input.Price * (input.TaxRate / 100)
		convertedPrice = input.Price + taxAmount
	} else {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid conversion type"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"original_price":  input.Price,
		"converted_price": convertedPrice,
		"taxable_amount":  taxableAmount,
		"tax_amount":      taxAmount,
		"tax_rate":        input.TaxRate,
		"from_type":       input.FromType,
		"to_type":         input.ToType,
	})
}
