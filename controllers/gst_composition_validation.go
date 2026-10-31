package controllers

// GST validation engine. Checks billing data before a report can be
// finalized, stores findings per return period and report version, and lets
// users resolve each issue with a note. Severities:
//   ERROR   – data problem that must be corrected or explicitly overridden
//   WARNING – potential compliance issue requiring review
//   INFO    – observation only; never blocks export/finalization

import (
	"fmt"
	"net/http"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Aggregate-turnover eligibility thresholds (section 10 limits). Traders,
// manufacturers and restaurants: Rs 1.5 crore; the 6% services scheme:
// Rs 50 lakh. Special-category states have lower limits — those live in
// configuration rather than being hardcoded per state.
const (
	gstCompositionThresholdGoods    = 15000000.0 // 1.5 crore
	gstCompositionThresholdServices = 5000000.0  // 50 lakh
)

func gstTurnoverThreshold(reg *models.GSTRegistration) float64 {
	if reg != nil && reg.CompositionCategory == "service_provider" {
		return gstCompositionThresholdServices
	}
	return gstCompositionThresholdGoods
}

type gstIssue struct {
	Severity    string
	Code        string
	Message     string
	SourceType  string
	SourceID    *uuid.UUID
	SourceRef   string
}

func newIssue(sev, code, msg string) gstIssue {
	return gstIssue{Severity: sev, Code: code, Message: msg}
}

func docIssue(sev, code, msg, srcType string, srcID uuid.UUID, ref string) gstIssue {
	return gstIssue{Severity: sev, Code: code, Message: msg, SourceType: srcType, SourceID: &srcID, SourceRef: ref}
}

// runGSTValidation evaluates all rules against the current data for a scope
// and returns the issue list (unpersisted).
func runGSTValidation(userID uuid.UUID, scope *gstReportScope, ss gstSalesSummary, ps gstPurchaseSummary, reportVersion int) []models.GSTValidationIssue {
	var found []gstIssue

	if scope.Registration == nil {
		found = append(found, newIssue("ERROR", "MISSING_GST_REGISTRATION",
			"No GST registration is configured. Add one under GST Configuration before preparing returns."))
	} else {
		reg := scope.Registration
		if reg.RegistrationType != "composition" {
			found = append(found, docIssue("WARNING", "REGISTRATION_NOT_COMPOSITION",
				fmt.Sprintf("GSTIN %s is registered as %q, not composition. Composition reports may not apply.", reg.GSTIN, reg.RegistrationType),
				"registration", reg.ID, reg.GSTIN))
		}
		if reg.EffectiveFrom.After(scope.End) || (reg.EffectiveTo != nil && reg.EffectiveTo.Before(scope.Start)) {
			found = append(found, docIssue("ERROR", "REGISTRATION_NOT_EFFECTIVE",
				fmt.Sprintf("The selected period %s to %s falls outside the registration's effective dates.", scope.Start.Format("2006-01-02"), scope.End.Format("2006-01-02")),
				"registration", reg.ID, reg.GSTIN))
		}
		if !utils.ValidateGSTIN(reg.GSTIN) {
			found = append(found, docIssue("ERROR", "INVALID_GSTIN",
				"Registration GSTIN fails format validation.", "registration", reg.ID, reg.GSTIN))
		}
	}

	if !scope.End.After(scope.Start) {
		found = append(found, newIssue("ERROR", "INVALID_PERIOD", "The reporting period end is not after its start."))
	}

	sellerState := regState(scope)
	taxPeriod := scopeTaxPeriod(scope)

	// --- Sales-side checks ---------------------------------------------------
	salesRows := loadGSTSalesRegister(userID, scope.Start, scope.End, sellerState, scope.FY, taxPeriod)

	seenNumbers := map[string][]gstSalesRegisterRow{}
	for _, r := range salesRows {
		if r.DocumentType != "sales_invoice" || !r.IncludedInTotals {
			continue
		}
		seenNumbers[r.InvoiceNumber] = append(seenNumbers[r.InvoiceNumber], r)
	}
	for num, rs := range seenNumbers {
		if len(rs) > 1 {
			for _, r := range rs {
				found = append(found, docIssue("ERROR", "DUPLICATE_INVOICE",
					fmt.Sprintf("Invoice number %s appears %d times in the period; each document must be unique.", num, len(rs)),
					r.SourceType, r.SourceID, num))
			}
		}
	}
	for _, r := range salesRows {
		if r.DocumentType == "sales_invoice" && r.InvoiceStatus == "cancelled" {
			found = append(found, docIssue("INFO", "CANCELLED_INVOICE_EXCLUDED",
				fmt.Sprintf("Cancelled invoice %s is excluded from valid sales totals.", r.InvoiceNumber),
				r.SourceType, r.SourceID, r.InvoiceNumber))
		}
		if r.IncludedInTotals && r.SupplyClassification == "unclassified" {
			found = append(found, docIssue("ERROR", "UNCLASSIFIED_SALE",
				fmt.Sprintf("Document %s has no classifiable supply value; review its line items.", r.InvoiceNumber),
				r.SourceType, r.SourceID, r.InvoiceNumber))
		}
		if r.IncludedInTotals && r.IsInterState && r.NetAmount > 0 {
			found = append(found, docIssue("WARNING", "INTERSTATE_OUTWARD_SUPPLY",
				fmt.Sprintf("Document %s is an inter-state outward supply of Rs %.2f. Composition taxpayers generally cannot make inter-state supplies; review eligibility.", r.InvoiceNumber, r.NetAmount),
				r.SourceType, r.SourceID, r.InvoiceNumber))
		}
	}

	// Sales returns / credit notes referencing missing source invoices.
	var orphanReturns []models.SalesReturn
	utils.DB.Preload("Items").Where("user_id = ? AND date >= ? AND date <= ? AND status = 'processed'", userID, scope.Start, scope.End).Find(&orphanReturns)
	for _, r := range orphanReturns {
		var cnt int64
		utils.DB.Model(&models.Invoice{}).Where("id = ? AND user_id = ?", r.InvoiceID, userID).Count(&cnt)
		if cnt == 0 {
			found = append(found, docIssue("WARNING", "MISSING_SOURCE_DOCUMENT",
				fmt.Sprintf("Sales return %s references a source invoice that no longer exists.", r.ReturnNumber),
				"sales_return", r.ID, r.ReturnNumber))
		}
	}

	// --- Purchase-side checks -------------------------------------------------
	purchaseRows := loadGSTPurchaseRegister(userID, scope.Start, scope.End, sellerState, scope.FY, taxPeriod)
	for _, r := range purchaseRows {
		if r.DocumentType == "purchase_return" || r.DocumentType == "debit_note" {
			continue
		}
		if !r.IncludedInTotals {
			continue
		}
		if r.InvoiceNumber == "" {
			found = append(found, docIssue("WARNING", "MISSING_PURCHASE_DETAILS",
				"A purchase bill has no invoice/bill number.", r.SourceType, r.SourceID, r.InvoiceNumber))
		}
		if r.SupplierGSTIN == "" && r.SupplierState == "" && r.NetAmount > 0 {
			found = append(found, docIssue("WARNING", "MISSING_SUPPLIER_INFO",
				fmt.Sprintf("Purchase bill %s has no supplier GSTIN or state; intra/inter-state classification may be wrong.", r.InvoiceNumber),
				r.SourceType, r.SourceID, r.InvoiceNumber))
		}
		if r.SupplierRegistration == "unregistered" && !r.ReverseCharge && r.NetAmount > 0 {
			// Not every URD purchase attracts RCM — flag for assessment only.
			found = append(found, docIssue("WARNING", "UNASSESSED_RCM",
				fmt.Sprintf("Purchase bill %s from an unregistered supplier (Rs %.2f) is not marked for reverse charge; confirm whether RCM applies.", r.InvoiceNumber, r.NetAmount),
				r.SourceType, r.SourceID, r.InvoiceNumber))
		}
	}
	var orphanPR []models.PurchaseReturn
	utils.DB.Where("user_id = ? AND date >= ? AND date <= ? AND status = 'processed'", userID, scope.Start, scope.End).Find(&orphanPR)
	for _, r := range orphanPR {
		var cnt int64
		utils.DB.Model(&models.PurchaseBill{}).Where("id = ? AND user_id = ?", r.PurchaseBillID, userID).Count(&cnt)
		if cnt == 0 {
			found = append(found, docIssue("WARNING", "MISSING_SOURCE_DOCUMENT",
				fmt.Sprintf("Purchase return %s references a source bill that no longer exists.", r.ReturnNumber),
				"purchase_return", r.ID, r.ReturnNumber))
		}
	}

	// --- Eligibility monitoring ------------------------------------------------
	if scope.Registration != nil {
		// Aggregate turnover is measured for the full financial year, not
		// just the selected period.
		fyStart, fyEnd, err := fyBounds(scope.FY)
		if err == nil {
			fyRows := loadGSTSalesRegister(userID, fyStart, fyEnd, sellerState, scope.FY, scope.FY)
			fySummary := summarizeSales(fyRows, scope.FY)
			threshold := gstTurnoverThreshold(scope.Registration)
			turnover := fySummary.CompositionTurnoverBasis
			if turnover >= threshold {
				found = append(found, newIssue("WARNING", "TURNOVER_EXCEEDS_THRESHOLD",
					fmt.Sprintf("Aggregate FY turnover Rs %.2f exceeds the composition threshold Rs %.0f for category %q. Review scheme eligibility and effective dates. Note: aggregate turnover is PAN-wide; other GSTINs on the same PAN must be considered too.", turnover, threshold, scope.Registration.CompositionCategory)))
			} else if turnover >= threshold*0.9 {
				found = append(found, newIssue("WARNING", "TURNOVER_NEAR_THRESHOLD",
					fmt.Sprintf("Aggregate FY turnover Rs %.2f is within 10%% of the composition threshold Rs %.0f.", turnover, threshold)))
			}
		}
	}

	// --- Reconciliation checks --------------------------------------------------
	if scope.Period != nil {
		var recs []models.GSTReconciliationRecord
		utils.DB.Where("gst_return_period_id = ?", scope.Period.ID).Find(&recs)
		for _, r := range recs {
			if r.ResolutionStatus == "open" && (r.Difference > 0.005 || r.Difference < -0.005) {
				found = append(found, newIssue("WARNING", "RETURN_CALC_MISMATCH",
					fmt.Sprintf("%s: recorded return value Rs %.2f differs from the system calculation Rs %.2f by Rs %.2f. Reconcile or add an explanation.", r.ReconciliationType, r.RecordedReturnValue, r.SystemCalculatedValue, r.Difference)))
			}
		}

		var payments []models.GSTTaxPayment
		utils.DB.Where("gst_return_period_id = ?", scope.Period.ID).Find(&payments)
		unverified := false
		for _, p := range payments {
			if !p.PortalConfirmed {
				unverified = true
			}
		}
		if unverified {
			found = append(found, newIssue("INFO", "PAYMENT_UNVERIFIED",
				"Some recorded tax payments are not verified against GST portal records."))
		}

		// For annual periods, compare against quarterly snapshots.
		if scope.Period.PeriodType == models.GSTPeriodTypeAnnual {
			var qSnaps []models.GSTCalculationSnapshot
			utils.DB.Joins("JOIN gst_return_periods p ON p.id = gst_calculation_snapshots.gst_return_period_id").
				Where("p.user_id = ? AND p.financial_year = ? AND p.period_type = ?", userID, scope.FY, models.GSTPeriodTypeQuarterly).
				Where("gst_calculation_snapshots.id IN (SELECT id FROM gst_calculation_snapshots s2 WHERE s2.gst_return_period_id = p.id ORDER BY s2.version DESC LIMIT 1)").
				Find(&qSnaps)
			var qTotal float64
			for _, s := range qSnaps {
				qTotal += s.TurnoverBasis
			}
			if len(qSnaps) > 0 {
				diff := ss.CompositionTurnoverBasis - qTotal
				if diff > 1 || diff < -1 {
					found = append(found, newIssue("WARNING", "ANNUAL_QUARTERLY_MISMATCH",
						fmt.Sprintf("Annual turnover Rs %.2f differs from the sum of quarterly CMP-08 snapshots Rs %.2f by Rs %.2f. Investigate missing quarters or data changes after filing.", ss.CompositionTurnoverBasis, qTotal, diff)))
				}
			}
		}
	}

	out := make([]models.GSTValidationIssue, 0, len(found))
	for _, f := range found {
		out = append(out, models.GSTValidationIssue{
			Severity:              f.Severity,
			Code:                  f.Code,
			Message:               f.Message,
			SourceTransactionType: f.SourceType,
			SourceTransactionID:   f.SourceID,
			SourceReference:       f.SourceRef,
			ReportVersion:         reportVersion,
		})
	}
	return out
}

// persistGSTValidationIssues stores a fresh validation run for the period.
// Previously stored *open* issues are replaced by the new run; resolved
// issues are kept for audit.
func persistGSTValidationIssues(periodID, userID uuid.UUID, issues []models.GSTValidationIssue) error {
	return utils.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("gst_return_period_id = ? AND resolution_status = 'open'", periodID).
			Delete(&models.GSTValidationIssue{}).Error; err != nil {
			return err
		}
		for i := range issues {
			issues[i].ID = uuid.New()
			issues[i].UserID = userID
			issues[i].GSTReturnPeriodID = periodID
			issues[i].ResolutionStatus = "open"
			issues[i].CreatedAt = time.Now()
			if err := tx.Create(&issues[i]).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// ---- HTTP handlers -----------------------------------------------------------

// RunGSTValidation re-runs the engine for a period without generating a new
// snapshot (issues are tagged with the latest snapshot version, or 0).
func RunGSTValidation(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	p, ok := loadGSTReturnPeriod(c)
	if !ok {
		return
	}
	reg, err := loadGSTRegistration(userID, p.GSTRegistrationID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "GST registration not found for this period"})
		return
	}
	scope := &gstReportScope{
		Registration: reg, Start: p.PeriodStart, End: p.PeriodEnd,
		FY: p.FinancialYear, Quarter: p.Quarter, PeriodType: p.PeriodType, Period: p,
	}
	_, _, ss, ps, _ := computeGSTReport(userID, scope)
	version := 0
	if snap, err := latestGSTSnapshot(p.ID); err == nil {
		version = snap.Version
	}
	issues := runGSTValidation(userID, scope, ss, ps, version)
	if err := persistGSTValidationIssues(p.ID, userID, issues); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to store validation issues"})
		return
	}
	var errors, warnings, infos int
	for _, i := range issues {
		switch i.Severity {
		case "ERROR":
			errors++
		case "WARNING":
			warnings++
		case "INFO":
			infos++
		}
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"issues":   issues,
		"errors":   errors,
		"warnings": warnings,
		"infos":    infos,
	}})
}

// ListGSTValidationIssues lists stored issues for a period (filters:
// severity, resolution_status, code).
func ListGSTValidationIssues(c *gin.Context) {
	p, ok := loadGSTReturnPeriod(c)
	if !ok {
		return
	}
	q := utils.DB.Where("gst_return_period_id = ?", p.ID)
	if sev := c.Query("severity"); sev != "" {
		q = q.Where("severity = ?", sev)
	}
	if st := c.Query("resolution_status"); st != "" {
		q = q.Where("resolution_status = ?", st)
	}
	if code := c.Query("code"); code != "" {
		q = q.Where("code = ?", code)
	}
	var issues []models.GSTValidationIssue
	q.Order("CASE severity WHEN 'ERROR' THEN 0 WHEN 'WARNING' THEN 1 ELSE 2 END, created_at ASC").Find(&issues)
	c.JSON(http.StatusOK, gin.H{"data": issues})
}

// ResolveGSTValidationIssue records a resolution (or re-opens) an issue.
func ResolveGSTValidationIssue(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	actorID := actorUserID(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid issue id"})
		return
	}
	var issue models.GSTValidationIssue
	if err := utils.DB.Where("id = ? AND user_id = ?", id, userID).First(&issue).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Validation issue not found"})
		return
	}
	var in struct {
		ResolutionStatus string `json:"resolution_status"` // resolved, open
		ResolutionNote   string `json:"resolution_note"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}
	switch in.ResolutionStatus {
	case "resolved":
		now := time.Now()
		issue.ResolutionStatus = "resolved"
		issue.ResolutionNote = in.ResolutionNote
		issue.ResolvedBy = &actorID
		issue.ResolvedAt = &now
	case "open":
		issue.ResolutionStatus = "open"
		issue.ResolutionNote = in.ResolutionNote
		issue.ResolvedBy = nil
		issue.ResolvedAt = nil
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "resolution_status must be resolved or open"})
		return
	}
	utils.DB.Save(&issue)
	c.JSON(http.StatusOK, gin.H{"data": issue})
}
