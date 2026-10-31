package controllers

// GST Composition reporting: configuration, return periods, payments and
// reconciliation. Report computation lives in gst_composition_reports.go,
// the validation engine in gst_composition_validation.go and exports in
// gst_composition_export.go.

import (
	"errors"
	"math"
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

// gstCalcRuleVersion identifies the calculation rules used in snapshots so a
// rule change can be detected against historical reports.
const gstCalcRuleVersion = "composition-v1"

// Composition levy rates by taxpayer category (Notification 8/2017-Central
// Tax (Rate) as amended, and the 6% services scheme under Notification
// 2/2019-Central Tax (Rate)). These are defaults — a per-registration
// override on GSTRegistration.CompositionRate takes precedence.
var gstCompositionCategoryRates = map[string]float64{
	"trader":           1.0,
	"manufacturer":     1.0,
	"restaurant":       5.0,
	"service_provider": 6.0,
}

var gstCompositionCategories = []string{"trader", "manufacturer", "restaurant", "service_provider"}

// roundMoney rounds to 2 decimals (paise) half away from zero. Composition
// tax and CMP-08 amounts are declared in whole rupees/2-dp on the portal;
// we keep paise precision for the working papers.
func roundMoney(v float64) float64 {
	return math.Round(v*100) / 100
}

// currentIndianFY returns the Indian financial year label ("2026-27") for t.
func currentIndianFY(t time.Time) string {
	y := t.Year()
	if t.Month() < time.April {
		y--
	}
	return fyLabel(y)
}

func fyLabel(startYear int) string {
	return strconv.Itoa(startYear) + "-" + strconv.Itoa((startYear+1)%100)
}

// parseFY parses "YYYY-YY" and returns the starting calendar year.
func parseFY(fy string) (int, error) {
	parts := strings.Split(strings.TrimSpace(fy), "-")
	if len(parts) != 2 || len(parts[0]) != 4 || len(parts[1]) != 2 {
		return 0, errors.New("financial_year must be in YYYY-YY format (e.g. 2026-27)")
	}
	start, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, errors.New("invalid financial_year")
	}
	end, err := strconv.Atoi(parts[1])
	if err != nil || (start+1)%100 != end {
		return 0, errors.New("financial_year end must be start+1 (e.g. 2026-27)")
	}
	return start, nil
}

// fyBounds returns the Apr 1 - Mar 31 bounds for a FY label.
func fyBounds(fy string) (time.Time, time.Time, error) {
	start, err := parseFY(fy)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return time.Date(start, 4, 1, 0, 0, 0, 0, time.UTC),
		time.Date(start+1, 3, 31, 23, 59, 59, 0, time.UTC), nil
}

// quarterBounds returns the statutory quarter bounds inside an Indian FY.
// Q1 Apr-Jun, Q2 Jul-Sep, Q3 Oct-Dec, Q4 Jan-Mar.
func quarterBounds(fy string, quarter int) (time.Time, time.Time, error) {
	start, err := parseFY(fy)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	switch quarter {
	case 1:
		return time.Date(start, 4, 1, 0, 0, 0, 0, time.UTC), time.Date(start, 6, 30, 23, 59, 59, 0, time.UTC), nil
	case 2:
		return time.Date(start, 7, 1, 0, 0, 0, 0, time.UTC), time.Date(start, 9, 30, 23, 59, 59, 0, time.UTC), nil
	case 3:
		return time.Date(start, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(start, 12, 31, 23, 59, 59, 0, time.UTC), nil
	case 4:
		return time.Date(start+1, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(start+1, 3, 31, 23, 59, 59, 0, time.UTC), nil
	}
	return time.Time{}, time.Time{}, errors.New("quarter must be 1-4")
}

// compositionRateFor resolves the applicable levy rate for a registration.
func compositionRateFor(reg *models.GSTRegistration) float64 {
	if reg.CompositionRate > 0 {
		return reg.CompositionRate
	}
	if r, ok := gstCompositionCategoryRates[strings.ToLower(reg.CompositionCategory)]; ok {
		return r
	}
	return 0
}

// gstReportScope is the resolved reporting context for a request.
type gstReportScope struct {
	Registration *models.GSTRegistration
	Start        time.Time
	End          time.Time
	FY           string
	Quarter      int
	PeriodType   string // quarterly, annual, custom
	Period       *models.GSTReturnPeriod // non-nil when period_id given
}

// loadGSTRegistration fetches a registration scoped to the owner.
func loadGSTRegistration(userID, id uuid.UUID) (*models.GSTRegistration, error) {
	var reg models.GSTRegistration
	if err := utils.DB.Where("id = ? AND user_id = ?", id, userID).First(&reg).Error; err != nil {
		return nil, err
	}
	return &reg, nil
}

// resolveGSTScope resolves registration + period query params into a scope.
// Supported params: registration_id, period_id, fy, quarter, period_type,
// start_date, end_date (YYYY-MM-DD).
func resolveGSTScope(c *gin.Context) (*gstReportScope, error) {
	userID := c.MustGet("user_id").(uuid.UUID)
	scope := &gstReportScope{}

	// Registration: explicit or the active composition registration.
	regID := strings.TrimSpace(c.Query("registration_id"))
	if regID != "" {
		id, err := uuid.Parse(regID)
		if err != nil {
			return nil, errors.New("invalid registration_id")
		}
		reg, err := loadGSTRegistration(userID, id)
		if err != nil {
			return nil, errors.New("GST registration not found")
		}
		scope.Registration = reg
	} else {
		var reg models.GSTRegistration
		err := utils.DB.Where("user_id = ? AND status = 'active'", userID).
			Order("CASE WHEN registration_type = 'composition' THEN 0 ELSE 1 END, created_at ASC").
			First(&reg).Error
		if err == nil {
			scope.Registration = &reg
		}
		// A missing registration is not an error at scope resolution; the
		// validator reports MISSING_GST_REGISTRATION.
	}

	// Explicit return period takes precedence over ad-hoc ranges.
	if pid := strings.TrimSpace(c.Query("period_id")); pid != "" {
		id, err := uuid.Parse(pid)
		if err != nil {
			return nil, errors.New("invalid period_id")
		}
		var p models.GSTReturnPeriod
		if err := utils.DB.Where("id = ? AND user_id = ?", id, userID).First(&p).Error; err != nil {
			return nil, errors.New("GST return period not found")
		}
		scope.Period = &p
		scope.Start, scope.End = p.PeriodStart, p.PeriodEnd
		scope.FY, scope.Quarter, scope.PeriodType = p.FinancialYear, p.Quarter, p.PeriodType
		reg, err := loadGSTRegistration(userID, p.GSTRegistrationID)
		if err == nil {
			scope.Registration = reg
		}
		return scope, nil
	}

	fy := strings.TrimSpace(c.Query("fy"))
	quarterStr := strings.TrimSpace(c.Query("quarter"))
	startStr := strings.TrimSpace(c.Query("start_date"))
	endStr := strings.TrimSpace(c.Query("end_date"))

	if startStr != "" && endStr != "" {
		start, err1 := time.Parse("2006-01-02", startStr)
		end, err2 := time.Parse("2006-01-02", endStr)
		if err1 != nil || err2 != nil {
			return nil, errors.New("start_date and end_date must be YYYY-MM-DD")
		}
		if end.Before(start) {
			return nil, errors.New("end_date must not be before start_date")
		}
		scope.Start, scope.End = start, end.Add(24*time.Hour-time.Second)
		scope.FY = currentIndianFY(start)
		scope.PeriodType = "custom"
		return scope, nil
	}

	if fy == "" {
		fy = currentIndianFY(time.Now())
	}
	scope.FY = fy
	if quarterStr != "" {
		q, err := strconv.Atoi(quarterStr)
		if err != nil {
			return nil, errors.New("invalid quarter")
		}
		start, end, err := quarterBounds(fy, q)
		if err != nil {
			return nil, err
		}
		scope.Start, scope.End, scope.Quarter, scope.PeriodType = start, end, q, models.GSTPeriodTypeQuarterly
		return scope, nil
	}
	start, end, err := fyBounds(fy)
	if err != nil {
		return nil, err
	}
	scope.Start, scope.End, scope.PeriodType = start, end, models.GSTPeriodTypeAnnual
	return scope, nil
}

// ---- Registrations -------------------------------------------------------

func ListGSTRegistrations(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var regs []models.GSTRegistration
	if err := utils.DB.Where("user_id = ?", userID).Order("created_at ASC").Find(&regs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load GST registrations"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": regs})
}

type gstRegistrationInput struct {
	GSTIN               string  `json:"gstin"`
	LegalName           string  `json:"legal_name"`
	TradeName           string  `json:"trade_name"`
	StateCode           string  `json:"state_code"`
	RegistrationType    string  `json:"registration_type"`
	CompositionCategory string  `json:"composition_category"`
	CompositionRate     float64 `json:"composition_rate"`
	EffectiveFrom       string  `json:"effective_from"`
	EffectiveTo         string  `json:"effective_to"`
	FilingFrequency     string  `json:"filing_frequency"`
	Status              string  `json:"status"`
	Notes               string  `json:"notes"`
}

func (in *gstRegistrationInput) validate() (string, bool) {
	in.GSTIN = strings.ToUpper(strings.TrimSpace(in.GSTIN))
	if !utils.ValidateGSTIN(in.GSTIN) {
		return "Invalid GSTIN format", false
	}
	if strings.TrimSpace(in.LegalName) == "" {
		return "legal_name is required", false
	}
	stateCode := utils.GetStateCodeFromGSTIN(in.GSTIN)
	if in.StateCode == "" {
		in.StateCode = stateCode
	} else if !strings.EqualFold(in.StateCode, stateCode) {
		return "state_code does not match the GSTIN state code (" + stateCode + ")", false
	}
	if in.RegistrationType == "" {
		in.RegistrationType = "composition"
	}
	if in.RegistrationType != "composition" && in.RegistrationType != "regular" {
		return "registration_type must be composition or regular", false
	}
	if in.RegistrationType == "composition" {
		cat := strings.ToLower(strings.TrimSpace(in.CompositionCategory))
		if cat == "" {
			return "composition_category is required for composition registrations", false
		}
		valid := false
		for _, c := range gstCompositionCategories {
			if c == cat {
				valid = true
			}
		}
		if !valid {
			return "composition_category must be one of: " + strings.Join(gstCompositionCategories, ", "), false
		}
		in.CompositionCategory = cat
	} else {
		in.CompositionCategory = ""
		in.CompositionRate = 0
	}
	if in.CompositionRate < 0 || in.CompositionRate > 30 {
		return "composition_rate must be between 0 and 30 percent", false
	}
	if in.FilingFrequency == "" {
		in.FilingFrequency = "quarterly"
	}
	return "", true
}

func CreateGSTRegistration(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var in gstRegistrationInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}
	if msg, ok := in.validate(); !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": msg})
		return
	}

	var dup int64
	utils.DB.Model(&models.GSTRegistration{}).
		Where("user_id = ? AND gstin = ?", userID, in.GSTIN).Count(&dup)
	if dup > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "A registration with this GSTIN already exists"})
		return
	}

	var effFrom time.Time
	if in.EffectiveFrom != "" {
		var err error
		effFrom, err = time.Parse("2006-01-02", in.EffectiveFrom)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "effective_from must be YYYY-MM-DD"})
			return
		}
	}
	var effTo *time.Time
	if in.EffectiveTo != "" {
		t, err := time.Parse("2006-01-02", in.EffectiveTo)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "effective_to must be YYYY-MM-DD"})
			return
		}
		effTo = &t
	}

	status := in.Status
	if status == "" {
		status = "active"
	}
	reg := models.GSTRegistration{
		ID:                  uuid.New(),
		UserID:              userID,
		GSTIN:               in.GSTIN,
		LegalName:           strings.TrimSpace(in.LegalName),
		TradeName:           strings.TrimSpace(in.TradeName),
		StateCode:           in.StateCode,
		State:               utils.StateCodes[in.StateCode],
		RegistrationType:    in.RegistrationType,
		CompositionCategory: in.CompositionCategory,
		CompositionRate:     in.CompositionRate,
		EffectiveFrom:       effFrom,
		EffectiveTo:         effTo,
		FilingFrequency:     in.FilingFrequency,
		Status:              status,
		Notes:               in.Notes,
	}
	if err := utils.DB.Create(&reg).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create GST registration"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"data": reg})
}

func GetGSTRegistration(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid registration id"})
		return
	}
	reg, err := loadGSTRegistration(userID, id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "GST registration not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": reg})
}

func UpdateGSTRegistration(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid registration id"})
		return
	}
	reg, err := loadGSTRegistration(userID, id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "GST registration not found"})
		return
	}
	var in gstRegistrationInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}
	if msg, ok := in.validate(); !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": msg})
		return
	}
	var dup int64
	utils.DB.Model(&models.GSTRegistration{}).
		Where("user_id = ? AND gstin = ? AND id != ?", userID, in.GSTIN, reg.ID).Count(&dup)
	if dup > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "A registration with this GSTIN already exists"})
		return
	}
	reg.GSTIN = in.GSTIN
	reg.LegalName = strings.TrimSpace(in.LegalName)
	reg.TradeName = strings.TrimSpace(in.TradeName)
	reg.StateCode = in.StateCode
	reg.State = utils.StateCodes[in.StateCode]
	reg.RegistrationType = in.RegistrationType
	reg.CompositionCategory = in.CompositionCategory
	reg.CompositionRate = in.CompositionRate
	if in.EffectiveFrom != "" {
		if t, err := time.Parse("2006-01-02", in.EffectiveFrom); err == nil {
			reg.EffectiveFrom = t
		}
	}
	if in.EffectiveTo != "" {
		if t, err := time.Parse("2006-01-02", in.EffectiveTo); err == nil {
			reg.EffectiveTo = &t
		}
	} else {
		reg.EffectiveTo = nil
	}
	reg.FilingFrequency = in.FilingFrequency
	if in.Status != "" {
		reg.Status = in.Status
	}
	reg.Notes = in.Notes
	if err := utils.DB.Save(reg).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update GST registration"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": reg})
}

func DeleteGSTRegistration(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid registration id"})
		return
	}
	reg, err := loadGSTRegistration(userID, id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "GST registration not found"})
		return
	}
	var periods int64
	utils.DB.Model(&models.GSTReturnPeriod{}).Where("gst_registration_id = ?", reg.ID).Count(&periods)
	if periods > 0 {
		// Keep history: deactivate instead of deleting.
		reg.Status = "inactive"
		utils.DB.Save(reg)
		c.JSON(http.StatusOK, gin.H{"data": reg, "message": "Registration has return periods; marked inactive instead of deleted"})
		return
	}
	if err := utils.DB.Delete(reg).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete GST registration"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "GST registration deleted"})
}

// ---- Return periods ------------------------------------------------------

func ListGSTReturnPeriods(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	q := utils.DB.Where("user_id = ?", userID)
	if rid := strings.TrimSpace(c.Query("registration_id")); rid != "" {
		q = q.Where("gst_registration_id = ?", rid)
	}
	if fy := strings.TrimSpace(c.Query("fy")); fy != "" {
		q = q.Where("financial_year = ?", fy)
	}
	var periods []models.GSTReturnPeriod
	if err := q.Preload("Registration").Order("period_start ASC").Find(&periods).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load GST return periods"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": periods})
}

type gstPeriodInput struct {
	GSTRegistrationID string `json:"gst_registration_id"`
	FinancialYear     string `json:"financial_year"`
	PeriodType        string `json:"period_type"` // quarterly, annual
	Quarter           int    `json:"quarter"`
}

func CreateGSTReturnPeriod(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	actorID := actorUserID(c)
	var in gstPeriodInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}
	regID, err := uuid.Parse(strings.TrimSpace(in.GSTRegistrationID))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "gst_registration_id is required"})
		return
	}
	if _, err := loadGSTRegistration(userID, regID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "GST registration not found"})
		return
	}

	var start, end time.Time
	var returnType string
	switch in.PeriodType {
	case models.GSTPeriodTypeQuarterly:
		start, end, err = quarterBounds(in.FinancialYear, in.Quarter)
		returnType = models.GSTReturnTypeCMP08
	case models.GSTPeriodTypeAnnual:
		in.Quarter = 0
		start, end, err = fyBounds(in.FinancialYear)
		returnType = models.GSTReturnTypeGSTR4
	default:
		err = errors.New("period_type must be quarterly or annual")
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var existing models.GSTReturnPeriod
	err = utils.DB.Where("user_id = ? AND gst_registration_id = ? AND financial_year = ? AND period_type = ? AND quarter = ?",
		userID, regID, in.FinancialYear, in.PeriodType, in.Quarter).First(&existing).Error
	if err == nil {
		c.JSON(http.StatusOK, gin.H{"data": existing, "message": "Period already exists"})
		return
	}

	p := models.GSTReturnPeriod{
		ID:                uuid.New(),
		UserID:            userID,
		GSTRegistrationID: regID,
		FinancialYear:     in.FinancialYear,
		PeriodType:        in.PeriodType,
		Quarter:           in.Quarter,
		PeriodStart:       start,
		PeriodEnd:         end,
		ReturnType:        returnType,
		Status:            models.GSTPeriodStatusDraft,
		CreatedBy:         actorID,
	}
	if err := utils.DB.Create(&p).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create GST return period"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"data": p})
}

// loadGSTReturnPeriod fetches a period owned by the user.
func loadGSTReturnPeriod(c *gin.Context) (*models.GSTReturnPeriod, bool) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid period id"})
		return nil, false
	}
	var p models.GSTReturnPeriod
	if err := utils.DB.Where("id = ? AND user_id = ?", id, userID).First(&p).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "GST return period not found"})
		return nil, false
	}
	return &p, true
}

func GetGSTReturnPeriod(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	p, ok := loadGSTReturnPeriod(c)
	if !ok {
		return
	}
	reg, _ := loadGSTRegistration(userID, p.GSTRegistrationID)

	var snapshots []models.GSTCalculationSnapshot
	utils.DB.Where("gst_return_period_id = ?", p.ID).Order("version DESC").Find(&snapshots)

	var openErrors, openWarnings int64
	utils.DB.Model(&models.GSTValidationIssue{}).
		Where("gst_return_period_id = ? AND resolution_status = 'open' AND severity = 'ERROR'", p.ID).Count(&openErrors)
	utils.DB.Model(&models.GSTValidationIssue{}).
		Where("gst_return_period_id = ? AND resolution_status = 'open' AND severity = 'WARNING'", p.ID).Count(&openWarnings)

	var payments []models.GSTTaxPayment
	utils.DB.Where("gst_return_period_id = ?", p.ID).Order("payment_date ASC").Find(&payments)

	c.JSON(http.StatusOK, gin.H{
		"data": gin.H{
			"period":         p,
			"registration":   reg,
			"snapshots":      snapshots,
			"open_errors":    openErrors,
			"open_warnings":  openWarnings,
			"payments":       payments,
			"latest_snapshot": func() interface{} {
				if len(snapshots) > 0 {
					return snapshots[0]
				}
				return nil
			}(),
		},
	})
}

type gstPeriodStatusInput struct {
	Action          string `json:"action"` // finalize, reopen, mark_filed
	PortalReference string `json:"portal_reference"`
	FiledAt         string `json:"filed_at"`
	FilingNotes     string `json:"filing_notes"`
	Force           bool   `json:"force"` // allow finalize despite open warnings
}

func UpdateGSTReturnPeriodStatus(c *gin.Context) {
	p, ok := loadGSTReturnPeriod(c)
	if !ok {
		return
	}
	var in gstPeriodStatusInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}
	now := time.Now()
	switch in.Action {
	case "generate":
		// Regenerate a fresh snapshot + validation run.
		snap, _, err := generateGSTSnapshot(c, p)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": gin.H{"period": p, "snapshot": snap}})
	case "finalize":
		if p.Status == models.GSTPeriodStatusFiled {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Period is already marked filed"})
			return
		}
		var openErrors int64
		utils.DB.Model(&models.GSTValidationIssue{}).
			Where("gst_return_period_id = ? AND resolution_status = 'open' AND severity = 'ERROR'", p.ID).Count(&openErrors)
		if openErrors > 0 && !in.Force {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":        "Unresolved validation errors prevent finalization",
				"open_errors":  openErrors,
				"can_override": true,
			})
			return
		}
		p.Status = models.GSTPeriodStatusFinalized
		p.FinalizedAt = &now
		utils.DB.Save(p)
		c.JSON(http.StatusOK, gin.H{"data": p})
	case "reopen":
		if p.Status == models.GSTPeriodStatusFiled {
			c.JSON(http.StatusBadRequest, gin.H{"error": "A filed period cannot be reopened; correct it through a new version"})
			return
		}
		p.Status = models.GSTPeriodStatusDraft
		p.FinalizedAt = nil
		utils.DB.Save(p)
		c.JSON(http.StatusOK, gin.H{"data": p})
	case "mark_filed":
		// FILED requires portal evidence; report generation/export never sets it.
		if strings.TrimSpace(in.PortalReference) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "portal_reference (ARN / acknowledgement) is required to mark a period as filed"})
			return
		}
		filedAt := now
		if in.FiledAt != "" {
			t, err := time.Parse("2006-01-02", in.FiledAt)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "filed_at must be YYYY-MM-DD"})
				return
			}
			filedAt = t
		}
		p.Status = models.GSTPeriodStatusFiled
		p.FiledAt = &filedAt
		p.PortalReference = strings.TrimSpace(in.PortalReference)
		p.FilingNotes = in.FilingNotes
		utils.DB.Save(p)
		c.JSON(http.StatusOK, gin.H{"data": p})
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "action must be one of: generate, finalize, reopen, mark_filed"})
	}
}

// ---- Tax payments --------------------------------------------------------

func ListGSTTaxPayments(c *gin.Context) {
	p, ok := loadGSTReturnPeriod(c)
	if !ok {
		return
	}
	var payments []models.GSTTaxPayment
	utils.DB.Where("gst_return_period_id = ?", p.ID).Order("payment_date ASC").Find(&payments)
	c.JSON(http.StatusOK, gin.H{"data": payments})
}

type gstTaxPaymentInput struct {
	PaymentDate       string  `json:"payment_date"`
	Amount            float64 `json:"amount"`
	Component         string  `json:"component"`
	ChallanReference  string  `json:"challan_reference"`
	PortalConfirmed   bool    `json:"portal_confirmed"`
	PortalReference   string  `json:"portal_reference"`
	Notes             string  `json:"notes"`
}

func CreateGSTTaxPayment(c *gin.Context) {
	actorID := actorUserID(c)
	p, ok := loadGSTReturnPeriod(c)
	if !ok {
		return
	}
	var in gstTaxPaymentInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}
	if in.Amount <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "amount must be positive"})
		return
	}
	payDate, err := time.Parse("2006-01-02", strings.TrimSpace(in.PaymentDate))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "payment_date must be YYYY-MM-DD"})
		return
	}
	component := in.Component
	if component == "" {
		component = "composition_tax"
	}
	payment := models.GSTTaxPayment{
		ID:                uuid.New(),
		UserID:            p.UserID,
		GSTReturnPeriodID: p.ID,
		PaymentDate:       payDate,
		Amount:            roundMoney(in.Amount),
		Component:         component,
		ChallanReference:  strings.TrimSpace(in.ChallanReference),
		PortalConfirmed:   in.PortalConfirmed,
		PortalReference:   strings.TrimSpace(in.PortalReference),
		Notes:             in.Notes,
		CreatedBy:         actorID,
	}
	if err := utils.DB.Create(&payment).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to record payment"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"data": payment})
}

func UpdateGSTTaxPayment(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid payment id"})
		return
	}
	var payment models.GSTTaxPayment
	if err := utils.DB.Where("id = ? AND user_id = ?", id, userID).First(&payment).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Payment not found"})
		return
	}
	var in gstTaxPaymentInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}
	if in.PaymentDate != "" {
		if t, err := time.Parse("2006-01-02", in.PaymentDate); err == nil {
			payment.PaymentDate = t
		}
	}
	if in.Amount > 0 {
		payment.Amount = roundMoney(in.Amount)
	}
	if in.Component != "" {
		payment.Component = in.Component
	}
	payment.ChallanReference = strings.TrimSpace(in.ChallanReference)
	payment.PortalReference = strings.TrimSpace(in.PortalReference)
	// Portal confirmation requires explicit evidence.
	payment.PortalConfirmed = in.PortalConfirmed && payment.PortalReference != ""
	payment.Notes = in.Notes
	utils.DB.Save(&payment)
	c.JSON(http.StatusOK, gin.H{"data": payment})
}

func DeleteGSTTaxPayment(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid payment id"})
		return
	}
	var payment models.GSTTaxPayment
	if err := utils.DB.Where("id = ? AND user_id = ?", id, userID).First(&payment).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Payment not found"})
		return
	}
	utils.DB.Delete(&payment)
	c.JSON(http.StatusOK, gin.H{"message": "Payment deleted"})
}

// ---- Reconciliation ------------------------------------------------------

func ListGSTReconciliation(c *gin.Context) {
	p, ok := loadGSTReturnPeriod(c)
	if !ok {
		return
	}
	var records []models.GSTReconciliationRecord
	utils.DB.Where("gst_return_period_id = ?", p.ID).Order("created_at ASC").Find(&records)
	c.JSON(http.StatusOK, gin.H{"data": records})
}

type gstReconciliationInput struct {
	ReconciliationType  string   `json:"reconciliation_type"`
	RecordedReturnValue float64  `json:"recorded_return_value"`
	PortalConfirmedValue *float64 `json:"portal_confirmed_value"`
}

// UpsertGSTReconciliation records the value declared on the filed return (or
// portal-confirmed value) for a reconciliation type, and computes the
// difference against the latest system calculation.
func UpsertGSTReconciliation(c *gin.Context) {
	p, ok := loadGSTReturnPeriod(c)
	if !ok {
		return
	}
	var in gstReconciliationInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}
	rt := strings.TrimSpace(in.ReconciliationType)
	if rt == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "reconciliation_type is required"})
		return
	}
	snap, err := latestGSTSnapshot(p.ID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Generate the report first so there is a system value to reconcile"})
		return
	}
	sysVal := reconciliationSystemValue(snap, rt)

	var rec models.GSTReconciliationRecord
	err = utils.DB.Where("gst_return_period_id = ? AND reconciliation_type = ?", p.ID, rt).First(&rec).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		rec = models.GSTReconciliationRecord{
			ID:                uuid.New(),
			UserID:            p.UserID,
			GSTReturnPeriodID: p.ID,
			ReconciliationType: rt,
		}
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load reconciliation record"})
		return
	}
	rec.SystemCalculatedValue = sysVal
	rec.RecordedReturnValue = roundMoney(in.RecordedReturnValue)
	rec.PortalConfirmedValue = in.PortalConfirmedValue
	rec.Difference = roundMoney(rec.RecordedReturnValue - sysVal)
	if rec.ResolutionStatus == "" {
		rec.ResolutionStatus = "open"
	}
	if rec.ID == uuid.Nil {
		if err := utils.DB.Create(&rec).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save reconciliation record"})
			return
		}
	} else {
		utils.DB.Save(&rec)
	}
	// A material unexplained difference flags the period for reconciliation.
	if math.Abs(rec.Difference) > 0.005 && rec.ResolutionStatus == "open" && p.Status != models.GSTPeriodStatusFiled {
		p.Status = models.GSTPeriodStatusReconciliationRequired
		utils.DB.Save(p)
	}
	c.JSON(http.StatusOK, gin.H{"data": rec})
}

func reconciliationSystemValue(snap *models.GSTCalculationSnapshot, rt string) float64 {
	switch rt {
	case "cmp08_turnover", "gstr4_turnover":
		return snap.TurnoverBasis
	case "cmp08_liability", "gstr4_liability":
		return snap.TotalCompositionLiability
	case "cmp08_tax_paid":
		return snap.TotalPayable
	case "rcm_liability":
		return snap.RCMLiability
	}
	return 0
}

func ResolveGSTReconciliation(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	actorID := actorUserID(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid reconciliation id"})
		return
	}
	var rec models.GSTReconciliationRecord
	if err := utils.DB.Where("id = ? AND user_id = ?", id, userID).First(&rec).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Reconciliation record not found"})
		return
	}
	var in struct {
		ResolutionStatus string `json:"resolution_status"` // explained, resolved
		ResolutionNote   string `json:"resolution_note"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}
	if in.ResolutionStatus != "explained" && in.ResolutionStatus != "resolved" && in.ResolutionStatus != "open" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "resolution_status must be open, explained or resolved"})
		return
	}
	now := time.Now()
	rec.ResolutionStatus = in.ResolutionStatus
	rec.ResolutionNote = in.ResolutionNote
	if in.ResolutionStatus == "open" {
		rec.ResolvedBy, rec.ResolvedAt = nil, nil
	} else {
		rec.ResolvedBy, rec.ResolvedAt = &actorID, &now
	}
	utils.DB.Save(&rec)
	c.JSON(http.StatusOK, gin.H{"data": rec})
}
