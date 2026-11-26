package controllers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Subscription plans & store subscriptions.
//
// The super admin defines plans (monthly / annual), assigns one to a store and
// records payments per billing cycle. A background job (plus a refresh on
// every read) rolls subscriptions over at the end of each period:
//   - next cycle already exists (paid in advance) → move into it
//   - auto-renew on → open the next cycle (unpaid until a payment is recorded)
//   - otherwise → the subscription is cancelled
//
// Payment gateways, automatic capture and plan-based menu access are future
// work; payments here are recorded manually.

const (
	subCycleMonthly = "monthly"
	subCycleAnnual  = "annual"

	subStatusActive    = "active"
	subStatusCancelled = "cancelled"

	subPayUnpaid  = "unpaid"
	subPayPartial = "partial"
	subPayPaid    = "paid"

	subSourceInitial      = "initial"
	subSourceAutoRenewal  = "auto_renewal"
	subSourcePrepaid      = "prepaid"
	subSourceReactivation = "reactivation"

	myPlanPageKey = "/my-plan"
	subDateLayout = "2006-01-02"
)

var subPaymentMethods = map[string]bool{
	"cash": true, "upi": true, "bank_transfer": true, "card": true, "cheque": true, "other": true,
}

// ---------------------------------------------------------------------------
// Date helpers (calendar dates, no timezone shift)
// ---------------------------------------------------------------------------

func parseSubDate(s string) (time.Time, error) {
	return time.Parse(subDateLayout, strings.TrimSpace(s))
}

func daysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// addSubPeriod returns the end of a period starting at start. anchorDay keeps
// month-end subscriptions on their day: an anchor of 31 gives Jan 31 → Feb 28
// → Mar 31 rather than drifting to the 28th.
func addSubPeriod(start time.Time, cycle string, anchorDay int) time.Time {
	months := 1
	if cycle == subCycleAnnual {
		months = 12
	}
	y, m := start.Year(), start.Month()
	total := int(m) - 1 + months
	y += total / 12
	m = time.Month(total%12 + 1)
	day := anchorDay
	if dim := daysInMonth(y, m); day > dim {
		day = dim
	}
	return time.Date(y, m, day, 0, 0, 0, 0, time.UTC)
}

func subPeriodEnd(startStr, cycle, anchorStr string) (string, error) {
	start, err := parseSubDate(startStr)
	if err != nil {
		return "", err
	}
	anchor, err := parseSubDate(anchorStr)
	if err != nil {
		anchor = start
	}
	return addSubPeriod(start, cycle, anchor.Day()).Format(subDateLayout), nil
}

func subDaysBetween(from, to string) int {
	a, err1 := parseSubDate(from)
	b, err2 := parseSubDate(to)
	if err1 != nil || err2 != nil {
		return 0
	}
	return int(b.Sub(a).Hours() / 24)
}

// storeToday is today's date in the store owner's configured timezone.
func storeToday(storeID uuid.UUID) string {
	loc := time.Local
	if store, err := utils.FindStoreByID(utils.DB, storeID); err == nil {
		loc, _ = ConfiguredLocationForUser(store.OwnerUserID)
	}
	return time.Now().In(loc).Format(subDateLayout)
}

func normalizeSubCycle(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "monthly", "month":
		return subCycleMonthly, true
	case "annual", "annually", "yearly", "year":
		return subCycleAnnual, true
	}
	return "", false
}

func encodeFeatures(features []string) string {
	clean := make([]string, 0, len(features))
	for _, f := range features {
		if f = strings.TrimSpace(f); f != "" {
			clean = append(clean, f)
		}
	}
	b, _ := json.Marshal(clean)
	return string(b)
}

func decodeFeatures(raw string) []string {
	out := []string{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

func subPaymentStatus(amount, paid float64) string {
	switch {
	case paid >= amount-0.005:
		return subPayPaid
	case paid > 0.005:
		return subPayPartial
	default:
		return subPayUnpaid
	}
}

// ---------------------------------------------------------------------------
// Renewal engine
// ---------------------------------------------------------------------------

// cycleIsComplimentary reports whether cycle number n of sub is free.
func cycleIsComplimentary(sub *models.StoreSubscription, n int) bool {
	return sub.IsComplimentary && (sub.ComplimentaryUntilCycle <= 0 || n <= sub.ComplimentaryUntilCycle)
}

func newSubCycle(sub *models.StoreSubscription, number int, start, end, source string) models.SubscriptionCycle {
	amount := roundMoney(sub.Price)
	comp := cycleIsComplimentary(sub, number)
	waived := 0.0
	if comp {
		waived, amount = amount, 0
	}
	cycle := models.SubscriptionCycle{
		ID:             uuid.New(),
		SubscriptionID: sub.ID,
		StoreID:        sub.StoreID,
		CycleNumber:    number,
		PeriodStart:    start,
		PeriodEnd:      end,
		Amount:         amount,
		PaymentStatus:  subPaymentStatus(amount, 0),
		Source:         source,
		Complimentary:  comp,
		WaivedAmount:   waived,
		InvoiceDate:    start,
	}
	// Billed ahead of its start (paid in advance): invoice it today.
	if today := storeToday(sub.StoreID); start > today {
		cycle.InvoiceDate = today
	}
	if cycle.PaymentStatus == subPayPaid {
		now := time.Now()
		cycle.PaidAt = &now
	}
	return cycle
}

// rollSubscription advances an active subscription through every period that
// has ended by today. Returns true when the row changed.
func rollSubscription(tx *gorm.DB, sub *models.StoreSubscription, today string) (bool, error) {
	changed := false
	for sub.Status == subStatusActive && sub.CurrentPeriodEnd <= today {
		var next models.SubscriptionCycle
		err := tx.Where("subscription_id = ? AND cycle_number = ?", sub.ID, sub.CurrentCycle+1).First(&next).Error
		switch {
		case err == nil:
			// Paid ahead: roll into it regardless of auto-renew.
		case errors.Is(err, gorm.ErrRecordNotFound) && sub.AutoRenew:
			end, perr := subPeriodEnd(sub.CurrentPeriodEnd, sub.BillingCycle, sub.AnchorDate)
			if perr != nil {
				return changed, perr
			}
			next = newSubCycle(sub, sub.CurrentCycle+1, sub.CurrentPeriodEnd, end, subSourceAutoRenewal)
			if err := tx.Create(&next).Error; err != nil {
				return changed, err
			}
		case errors.Is(err, gorm.ErrRecordNotFound):
			now := time.Now()
			sub.Status = subStatusCancelled
			sub.CancelledAt = &now
			sub.CancellationReason = "Period ended without auto-renewal or advance payment"
			return true, tx.Save(sub).Error
		default:
			return changed, err
		}
		sub.CurrentCycle = next.CycleNumber
		sub.CurrentPeriodStart = next.PeriodStart
		sub.CurrentPeriodEnd = next.PeriodEnd
		// A limited complimentary period is over once a paid cycle starts.
		if sub.IsComplimentary && !cycleIsComplimentary(sub, sub.CurrentCycle) {
			sub.IsComplimentary = false
			sub.ComplimentaryUntilCycle = 0
		}
		changed = true
	}
	if changed {
		return true, tx.Save(sub).Error
	}
	return false, nil
}

func refreshSubscription(sub *models.StoreSubscription) {
	if sub.Status != subStatusActive {
		return
	}
	today := storeToday(sub.StoreID)
	if sub.CurrentPeriodEnd > today {
		return
	}
	if err := utils.DB.Transaction(func(tx *gorm.DB) error {
		_, err := rollSubscription(tx, sub, today)
		return err
	}); err != nil {
		log.Printf("subscriptions: renew %s: %v", sub.ID, err)
	}
}

// processSubscriptionRenewals rolls over every active subscription whose
// period has ended. The date filter is loose (UTC+14 is the furthest-ahead
// timezone); refreshSubscription applies each store's own "today".
func processSubscriptionRenewals() {
	cutoff := time.Now().UTC().Add(14 * time.Hour).Format(subDateLayout)
	var due []models.StoreSubscription
	if err := utils.DB.Where("status = ? AND current_period_end <= ?", subStatusActive, cutoff).
		Find(&due).Error; err != nil {
		log.Printf("subscriptions: load due: %v", err)
		return
	}
	for i := range due {
		refreshSubscription(&due[i])
	}
}

// StartSubscriptionRenewalScheduler runs the renewal job at startup and then
// every 15 minutes.
func StartSubscriptionRenewalScheduler() {
	go func() {
		backfillSubscriptionDocNumbers()
		processSubscriptionRenewals()
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			processSubscriptionRenewals()
		}
	}()
}

// backfillSubscriptionDocNumbers numbers cycles and payments recorded before
// invoices and receipts existed, oldest first.
func backfillSubscriptionDocNumbers() {
	var cycles []models.SubscriptionCycle
	utils.DB.Where("invoice_number = '' OR invoice_number IS NULL").Order("created_at ASC, cycle_number ASC").Find(&cycles)
	for _, cy := range cycles {
		if cy.InvoiceDate == "" {
			cy.InvoiceDate = cy.PeriodStart
		}
		_ = utils.DB.Transaction(func(tx *gorm.DB) error {
			n, err := models.NextSubscriptionDocNumber(tx, "invoice", cy.InvoiceDate)
			if err != nil {
				return err
			}
			return tx.Model(&models.SubscriptionCycle{}).Where("id = ?", cy.ID).
				Updates(map[string]interface{}{"invoice_number": n, "invoice_date": cy.InvoiceDate}).Error
		})
	}
	var payments []models.SubscriptionPayment
	utils.DB.Where("receipt_number = '' OR receipt_number IS NULL").Order("created_at ASC").Find(&payments)
	for _, p := range payments {
		_ = utils.DB.Transaction(func(tx *gorm.DB) error {
			n, err := models.NextSubscriptionDocNumber(tx, "receipt", p.PaymentDate)
			if err != nil {
				return err
			}
			return tx.Model(&models.SubscriptionPayment{}).Where("id = ?", p.ID).Update("receipt_number", n).Error
		})
	}
}

// recomputeCycle re-derives a cycle's paid amount and status from its payments.
func recomputeCycle(tx *gorm.DB, cycleID uuid.UUID) (models.SubscriptionCycle, error) {
	var cycle models.SubscriptionCycle
	if err := tx.First(&cycle, "id = ?", cycleID).Error; err != nil {
		return cycle, err
	}
	var total struct{ Sum float64 }
	if err := tx.Model(&models.SubscriptionPayment{}).Select("COALESCE(SUM(amount), 0) AS sum").
		Where("cycle_id = ?", cycleID).Scan(&total).Error; err != nil {
		return cycle, err
	}
	cycle.AmountPaid = roundMoney(total.Sum)
	cycle.PaymentStatus = subPaymentStatus(cycle.Amount, cycle.AmountPaid)
	if cycle.PaymentStatus == subPayPaid {
		if cycle.PaidAt == nil {
			now := time.Now()
			cycle.PaidAt = &now
		}
	} else {
		cycle.PaidAt = nil
	}
	return cycle, tx.Save(&cycle).Error
}

// ---------------------------------------------------------------------------
// Response shaping
// ---------------------------------------------------------------------------

type subscriptionView struct {
	models.StoreSubscription
	FeatureList    []string `json:"feature_list"`
	StoreName      string   `json:"store_name"`
	StoreCode      string   `json:"store_code"`
	PlanDesc       string   `json:"plan_description"`
	Today          string   `json:"today"`
	RemainingDays  int      `json:"remaining_days"`
	TotalDays      int      `json:"total_days"`
	CurrentPayment string   `json:"current_payment_status"`
	AmountDue      float64  `json:"amount_due"`
	NextCyclePaid  bool     `json:"next_cycle_paid"`
	TotalPaid      float64  `json:"total_paid"`
	// CurrentComplimentary: the running cycle is free. ComplimentaryCyclesLeft
	// counts free cycles from the current one (-1 = no end date).
	CurrentComplimentary    bool                         `json:"current_complimentary"`
	ComplimentaryCyclesLeft int                          `json:"complimentary_cycles_left"`
	TotalWaived             float64                      `json:"total_waived"`
	Cycles                  []models.SubscriptionCycle   `json:"cycles,omitempty"`
	Payments                []models.SubscriptionPayment `json:"payments,omitempty"`
}

func buildSubscriptionView(sub models.StoreSubscription, withDetail bool) subscriptionView {
	v := subscriptionView{StoreSubscription: sub, FeatureList: decodeFeatures(sub.Features)}
	v.Today = storeToday(sub.StoreID)
	if store, err := utils.FindStoreByID(utils.DB, sub.StoreID); err == nil {
		v.StoreName, v.StoreCode = store.Name, store.Code
	}
	if sub.PlanID != nil {
		var plan models.SubscriptionPlan
		if utils.DB.Unscoped().Select("description").First(&plan, "id = ?", *sub.PlanID).Error == nil {
			v.PlanDesc = plan.Description
		}
	}
	v.TotalDays = subDaysBetween(sub.CurrentPeriodStart, sub.CurrentPeriodEnd)
	if sub.Status == subStatusActive {
		v.RemainingDays = subDaysBetween(v.Today, sub.CurrentPeriodEnd)
		if v.RemainingDays < 0 {
			v.RemainingDays = 0
		}
	}

	var cycles []models.SubscriptionCycle
	utils.DB.Where("subscription_id = ?", sub.ID).Order("cycle_number DESC").Find(&cycles)
	for _, c := range cycles {
		if c.CycleNumber == sub.CurrentCycle {
			v.CurrentPayment = c.PaymentStatus
			v.CurrentComplimentary = c.Complimentary
		}
		v.TotalWaived += c.WaivedAmount
		if c.CycleNumber == sub.CurrentCycle+1 && c.PaymentStatus == subPayPaid {
			v.NextCyclePaid = true
		}
		// Due = started cycles not yet fully paid.
		if c.PeriodStart <= v.Today && c.PaymentStatus != subPayPaid {
			v.AmountDue += c.Amount - c.AmountPaid
		}
		v.TotalPaid += c.AmountPaid
	}
	v.AmountDue = roundMoney(v.AmountDue)
	v.TotalPaid = roundMoney(v.TotalPaid)
	v.TotalWaived = roundMoney(v.TotalWaived)
	switch {
	case !sub.IsComplimentary:
		v.ComplimentaryCyclesLeft = 0
	case sub.ComplimentaryUntilCycle <= 0:
		v.ComplimentaryCyclesLeft = -1
	default:
		v.ComplimentaryCyclesLeft = max(sub.ComplimentaryUntilCycle-sub.CurrentCycle+1, 0)
	}
	if withDetail {
		v.Cycles = cycles
		var payments []models.SubscriptionPayment
		utils.DB.Where("subscription_id = ?", sub.ID).Order("payment_date DESC, created_at DESC").Find(&payments)
		v.Payments = payments
	}
	return v
}

func activeSubscriptionForStore(tx *gorm.DB, storeID uuid.UUID) (models.StoreSubscription, error) {
	var sub models.StoreSubscription
	err := tx.Where("store_id = ? AND status = ?", storeID, subStatusActive).
		Order("created_at DESC").First(&sub).Error
	return sub, err
}

// ---------------------------------------------------------------------------
// Admin: plans
// ---------------------------------------------------------------------------

type subscriptionPlanInput struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	BillingCycle string   `json:"billing_cycle"`
	Price        float64  `json:"price"`
	Currency     string   `json:"currency"`
	Features     []string `json:"features"`
	Highlight    *bool    `json:"highlight"`
	IsActive     *bool    `json:"is_active"`
	SortOrder    int      `json:"sort_order"`
}

type subscriptionPlanView struct {
	models.SubscriptionPlan
	FeatureList []string `json:"feature_list"`
	ActiveCount int64    `json:"active_subscriptions"`
}

func planView(p models.SubscriptionPlan) subscriptionPlanView {
	v := subscriptionPlanView{SubscriptionPlan: p, FeatureList: decodeFeatures(p.Features)}
	utils.DB.Model(&models.StoreSubscription{}).
		Where("plan_id = ? AND status = ?", p.ID, subStatusActive).Count(&v.ActiveCount)
	return v
}

func applyPlanInput(plan *models.SubscriptionPlan, in subscriptionPlanInput) error {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return errors.New("Plan name is required")
	}
	cycle, ok := normalizeSubCycle(in.BillingCycle)
	if !ok {
		return errors.New("Renewal type must be monthly or annual")
	}
	if in.Price < 0 {
		return errors.New("Price cannot be negative")
	}
	plan.Name = name
	plan.Description = strings.TrimSpace(in.Description)
	plan.BillingCycle = cycle
	plan.Price = roundMoney(in.Price)
	plan.Currency = strings.ToUpper(strings.TrimSpace(in.Currency))
	if plan.Currency == "" {
		plan.Currency = "INR"
	}
	plan.Features = encodeFeatures(in.Features)
	plan.Highlight = boolOr(in.Highlight, plan.Highlight)
	plan.IsActive = boolOr(in.IsActive, plan.IsActive)
	plan.SortOrder = in.SortOrder
	return nil
}

// AdminListSubscriptionPlans lists every plan (active and inactive).
func AdminListSubscriptionPlans(c *gin.Context) {
	var plans []models.SubscriptionPlan
	if err := utils.DB.Order("sort_order ASC, price ASC, created_at ASC").Find(&plans).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load plans"})
		return
	}
	out := make([]subscriptionPlanView, 0, len(plans))
	for _, p := range plans {
		out = append(out, planView(p))
	}
	c.JSON(http.StatusOK, out)
}

// AdminCreateSubscriptionPlan creates a plan.
func AdminCreateSubscriptionPlan(c *gin.Context) {
	var in subscriptionPlanInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	plan := models.SubscriptionPlan{ID: uuid.New(), IsActive: true}
	if err := applyPlanInput(&plan, in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := utils.DB.Create(&plan).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create plan"})
		return
	}
	c.JSON(http.StatusCreated, planView(plan))
}

// AdminUpdateSubscriptionPlan edits a plan. Existing subscriptions keep the
// name, price and features they were assigned with.
func AdminUpdateSubscriptionPlan(c *gin.Context) {
	var plan models.SubscriptionPlan
	if err := utils.DB.First(&plan, "id = ?", c.Param("id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Plan not found"})
		return
	}
	var in subscriptionPlanInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := applyPlanInput(&plan, in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := utils.DB.Save(&plan).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update plan"})
		return
	}
	c.JSON(http.StatusOK, planView(plan))
}

// AdminDeleteSubscriptionPlan deletes a plan that no active subscription uses.
func AdminDeleteSubscriptionPlan(c *gin.Context) {
	var plan models.SubscriptionPlan
	if err := utils.DB.First(&plan, "id = ?", c.Param("id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Plan not found"})
		return
	}
	var active int64
	utils.DB.Model(&models.StoreSubscription{}).
		Where("plan_id = ? AND status = ?", plan.ID, subStatusActive).Count(&active)
	if active > 0 {
		c.JSON(http.StatusConflict, gin.H{
			"error": fmt.Sprintf("%d store(s) are on this plan. Deactivate it instead, or move those stores first.", active),
		})
		return
	}
	if err := utils.DB.Delete(&plan).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete plan"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Plan deleted"})
}

// ---------------------------------------------------------------------------
// Admin: subscriptions
// ---------------------------------------------------------------------------

func findSubscription(c *gin.Context) (models.StoreSubscription, bool) {
	var sub models.StoreSubscription
	if err := utils.DB.First(&sub, "id = ?", c.Param("id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Subscription not found"})
		return sub, false
	}
	refreshSubscription(&sub)
	return sub, true
}

// AdminSubscriptionOverview returns headline numbers for the backoffice.
func AdminSubscriptionOverview(c *gin.Context) {
	processSubscriptionRenewals()

	var subs []models.StoreSubscription
	utils.DB.Where("status = ?", subStatusActive).Find(&subs)

	var mrr, compValue float64
	renewingSoon, autoRenewOff, complimentary := 0, 0, 0
	for _, s := range subs {
		if cycleIsComplimentary(&s, s.CurrentCycle) {
			// Free right now: not revenue, but track what is given away.
			complimentary++
			if s.BillingCycle == subCycleAnnual {
				compValue += s.Price / 12
			} else {
				compValue += s.Price
			}
		} else if s.BillingCycle == subCycleAnnual {
			mrr += s.Price / 12
		} else {
			mrr += s.Price
		}
		if d := subDaysBetween(storeToday(s.StoreID), s.CurrentPeriodEnd); d <= 7 {
			renewingSoon++
		}
		if !s.AutoRenew {
			autoRenewOff++
		}
	}

	var storeCount int64
	utils.DB.Model(&models.Store{}).Where("is_active = ?", true).Count(&storeCount)

	today := time.Now().Format(subDateLayout)
	var outstanding struct{ Sum float64 }
	utils.DB.Model(&models.SubscriptionCycle{}).
		Select("COALESCE(SUM(subscription_cycles.amount - subscription_cycles.amount_paid), 0) AS sum").
		Joins("JOIN store_subscriptions ON store_subscriptions.id = subscription_cycles.subscription_id AND store_subscriptions.deleted_at IS NULL").
		Where("subscription_cycles.payment_status <> ? AND subscription_cycles.period_start <= ?", subPayPaid, today).
		Scan(&outstanding)

	monthStart := time.Now().Format("2006-01") + "-01"
	var collected struct{ Sum float64 }
	utils.DB.Model(&models.SubscriptionPayment{}).Select("COALESCE(SUM(amount), 0) AS sum").
		Where("payment_date >= ?", monthStart).Scan(&collected)

	c.JSON(http.StatusOK, gin.H{
		"active_subscriptions": len(subs),
		"stores":               storeCount,
		"stores_without_plan":  max(int(storeCount)-countDistinctStores(subs), 0),
		"mrr":                  roundMoney(mrr),
		"renewing_in_7_days":   renewingSoon,
		"auto_renew_off":       autoRenewOff,
		"complimentary":        complimentary,
		"complimentary_value":  roundMoney(compValue),
		"outstanding":          roundMoney(outstanding.Sum),
		"collected_this_month": roundMoney(collected.Sum),
	})
}

func countDistinctStores(subs []models.StoreSubscription) int {
	seen := map[uuid.UUID]bool{}
	for _, s := range subs {
		seen[s.StoreID] = true
	}
	return len(seen)
}

// AdminListSubscriptions lists subscriptions, filterable by store and status.
func AdminListSubscriptions(c *gin.Context) {
	processSubscriptionRenewals()

	query := utils.DB.Order("created_at DESC")
	if storeID := strings.TrimSpace(c.Query("store_id")); storeID != "" {
		query = query.Where("store_id = ?", storeID)
	}
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		query = query.Where("status = ?", status)
	}
	var subs []models.StoreSubscription
	if err := query.Find(&subs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load subscriptions"})
		return
	}
	out := make([]subscriptionView, 0, len(subs))
	for _, s := range subs {
		out = append(out, buildSubscriptionView(s, false))
	}
	// Active first, then soonest renewal.
	sort.SliceStable(out, func(i, j int) bool {
		ai, aj := out[i].Status == subStatusActive, out[j].Status == subStatusActive
		if ai != aj {
			return ai
		}
		if ai {
			return out[i].CurrentPeriodEnd < out[j].CurrentPeriodEnd
		}
		return false
	})
	c.JSON(http.StatusOK, out)
}

// AdminGetSubscription returns a subscription with its cycles and payments.
func AdminGetSubscription(c *gin.Context) {
	sub, ok := findSubscription(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, buildSubscriptionView(sub, true))
}

type subscriptionPaymentInput struct {
	Amount      float64 `json:"amount"`
	PaymentDate string  `json:"payment_date"`
	Method      string  `json:"method"`
	Reference   string  `json:"reference"`
	Notes       string  `json:"notes"`
}

func (in *subscriptionPaymentInput) validate(today string) error {
	if in.Amount <= 0 {
		return errors.New("Payment amount must be greater than zero")
	}
	in.Amount = roundMoney(in.Amount)
	in.PaymentDate = strings.TrimSpace(in.PaymentDate)
	if in.PaymentDate == "" {
		in.PaymentDate = today
	}
	if _, err := parseSubDate(in.PaymentDate); err != nil {
		return errors.New("Payment date must be YYYY-MM-DD")
	}
	in.Method = strings.ToLower(strings.TrimSpace(in.Method))
	if in.Method == "" {
		in.Method = "bank_transfer"
	}
	if !subPaymentMethods[in.Method] {
		return errors.New("Unknown payment method")
	}
	in.Reference = strings.TrimSpace(in.Reference)
	in.Notes = strings.TrimSpace(in.Notes)
	return nil
}

func recordSubPayment(tx *gorm.DB, sub *models.StoreSubscription, cycleID uuid.UUID, in subscriptionPaymentInput, actor uuid.UUID) (models.SubscriptionPayment, error) {
	p := models.SubscriptionPayment{
		ID:             uuid.New(),
		SubscriptionID: sub.ID,
		CycleID:        cycleID,
		StoreID:        sub.StoreID,
		Amount:         in.Amount,
		PaymentDate:    in.PaymentDate,
		Method:         in.Method,
		Reference:      in.Reference,
		Notes:          in.Notes,
		CreatedByID:    actor,
	}
	if err := tx.Create(&p).Error; err != nil {
		return p, err
	}
	_, err := recomputeCycle(tx, cycleID)
	return p, err
}

type createSubscriptionInput struct {
	StoreID         string                    `json:"store_id"`
	PlanID          string                    `json:"plan_id"`
	StartDate       string                    `json:"start_date"`
	AutoRenew       *bool                     `json:"auto_renew"`
	Price           *float64                  `json:"price"`
	Notes           string                    `json:"notes"`
	ReplaceExisting bool                      `json:"replace_existing"`
	InitialPayment  *subscriptionPaymentInput `json:"initial_payment"`
	// Complimentary: free of charge for ComplimentaryCycles cycles (0 = no
	// end). Price stays the regular price billed after the free period.
	IsComplimentary     bool   `json:"is_complimentary"`
	ComplimentaryCycles int    `json:"complimentary_cycles"`
	ComplimentaryReason string `json:"complimentary_reason"`
}

// AdminCreateSubscription assigns a plan to a store. A store has at most one
// active subscription; pass replace_existing to cancel the current one.
func AdminCreateSubscription(c *gin.Context) {
	var in createSubscriptionInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	storeID, err := uuid.Parse(strings.TrimSpace(in.StoreID))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Select a store"})
		return
	}
	store, err := utils.FindStoreByID(utils.DB, storeID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Store not found"})
		return
	}
	var plan models.SubscriptionPlan
	if err := utils.DB.First(&plan, "id = ?", strings.TrimSpace(in.PlanID)).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Select a plan"})
		return
	}
	if !plan.IsActive {
		c.JSON(http.StatusBadRequest, gin.H{"error": "This plan is inactive"})
		return
	}

	today := storeToday(store.ID)
	start := strings.TrimSpace(in.StartDate)
	if start == "" {
		start = today
	}
	if _, err := parseSubDate(start); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Start date must be YYYY-MM-DD"})
		return
	}
	if start > today {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Start date cannot be in the future"})
		return
	}
	price := plan.Price
	if in.Price != nil {
		if *in.Price < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Price cannot be negative"})
			return
		}
		price = roundMoney(*in.Price)
	}
	if in.ComplimentaryCycles < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Complimentary cycles cannot be negative"})
		return
	}
	if in.IsComplimentary {
		in.InitialPayment = nil // nothing to pay for a free cycle
	}
	if in.InitialPayment != nil {
		if err := in.InitialPayment.validate(today); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}

	existing, existErr := activeSubscriptionForStore(utils.DB, store.ID)
	if existErr == nil && !in.ReplaceExisting {
		c.JSON(http.StatusConflict, gin.H{
			"error":            fmt.Sprintf("%s already has an active %s subscription", store.Name, existing.PlanName),
			"existing_id":      existing.ID,
			"requires_replace": true,
		})
		return
	}

	end, _ := subPeriodEnd(start, plan.BillingCycle, start)
	actor := actorUserID(c)
	sub := models.StoreSubscription{
		ID:                 uuid.New(),
		StoreID:            store.ID,
		PlanID:             &plan.ID,
		PlanName:           plan.Name,
		BillingCycle:       plan.BillingCycle,
		Price:              price,
		Currency:           plan.Currency,
		Features:           plan.Features,
		Status:             subStatusActive,
		AutoRenew:          boolOr(in.AutoRenew, true),
		AnchorDate:         start,
		CurrentCycle:       1,
		CurrentPeriodStart: start,
		CurrentPeriodEnd:   end,
		Notes:              strings.TrimSpace(in.Notes),
		CreatedByID:        actor,
	}
	if in.IsComplimentary {
		sub.IsComplimentary = true
		sub.ComplimentaryUntilCycle = in.ComplimentaryCycles // cycle 1 is the first
		sub.ComplimentaryReason = strings.TrimSpace(in.ComplimentaryReason)
	}

	err = utils.DB.Transaction(func(tx *gorm.DB) error {
		if existErr == nil {
			now := time.Now()
			existing.Status = subStatusCancelled
			existing.CancelledAt = &now
			existing.CancellationReason = "Replaced by " + plan.Name
			if err := tx.Save(&existing).Error; err != nil {
				return err
			}
		}
		if err := tx.Create(&sub).Error; err != nil {
			return err
		}
		cycle := newSubCycle(&sub, 1, start, end, subSourceInitial)
		if err := tx.Create(&cycle).Error; err != nil {
			return err
		}
		if in.InitialPayment != nil {
			if _, err := recordSubPayment(tx, &sub, cycle.ID, *in.InitialPayment, actor); err != nil {
				return err
			}
		}
		// A back-dated start may already be past its first period.
		_, err := rollSubscription(tx, &sub, today)
		return err
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create subscription"})
		return
	}
	c.JSON(http.StatusCreated, buildSubscriptionView(sub, true))
}

// repriceCycles applies the subscription's complimentary terms to cycles that
// have not been paid for: upcoming cycles always, and the current cycle when
// waiveCurrent is set (making an active plan complimentary waives what is
// still owed for the running period). Cycles that already started stay as
// they are when complimentary is turned off.
func repriceCycles(tx *gorm.DB, sub *models.StoreSubscription, waiveCurrent bool) error {
	var cycles []models.SubscriptionCycle
	if err := tx.Where("subscription_id = ? AND cycle_number >= ?", sub.ID, sub.CurrentCycle).Find(&cycles).Error; err != nil {
		return err
	}
	for _, cy := range cycles {
		comp := cycleIsComplimentary(sub, cy.CycleNumber)
		switch {
		case cy.CycleNumber == sub.CurrentCycle:
			if !waiveCurrent || !comp || cy.Complimentary {
				continue
			}
			// Waive the unpaid remainder of the running cycle.
			cy.WaivedAmount = roundMoney(cy.Amount - cy.AmountPaid)
			cy.Amount = cy.AmountPaid
			cy.Complimentary = true
		case cy.AmountPaid > 0.005:
			continue // paid in advance: leave it
		case comp:
			cy.WaivedAmount, cy.Amount, cy.Complimentary = roundMoney(sub.Price), 0, true
		default:
			cy.WaivedAmount, cy.Amount, cy.Complimentary = 0, roundMoney(sub.Price), false
		}
		cy.PaymentStatus = subPaymentStatus(cy.Amount, cy.AmountPaid)
		if cy.PaymentStatus == subPayPaid && cy.PaidAt == nil {
			now := time.Now()
			cy.PaidAt = &now
		} else if cy.PaymentStatus != subPayPaid {
			cy.PaidAt = nil
		}
		if err := tx.Save(&cy).Error; err != nil {
			return err
		}
	}
	return nil
}

// AdminUpdateSubscription toggles auto-renew, edits notes and switches the
// subscription to or from complimentary.
func AdminUpdateSubscription(c *gin.Context) {
	sub, ok := findSubscription(c)
	if !ok {
		return
	}
	var in struct {
		AutoRenew           *bool   `json:"auto_renew"`
		Notes               *string `json:"notes"`
		IsComplimentary     *bool   `json:"is_complimentary"`
		ComplimentaryCycles *int    `json:"complimentary_cycles"`
		ComplimentaryReason *string `json:"complimentary_reason"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if in.AutoRenew != nil {
		sub.AutoRenew = *in.AutoRenew
	}
	if in.Notes != nil {
		sub.Notes = strings.TrimSpace(*in.Notes)
	}
	if in.ComplimentaryReason != nil {
		sub.ComplimentaryReason = strings.TrimSpace(*in.ComplimentaryReason)
	}
	compChanged, waiveCurrent := false, false
	if in.IsComplimentary != nil || in.ComplimentaryCycles != nil {
		if sub.Status != subStatusActive {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Only an active subscription can be made complimentary"})
			return
		}
		enable := sub.IsComplimentary
		if in.IsComplimentary != nil {
			enable = *in.IsComplimentary
		}
		if enable {
			// complimentary_cycles counts from the current cycle (0 = no end).
			n := 0
			if in.ComplimentaryCycles != nil {
				n = *in.ComplimentaryCycles
			} else if sub.IsComplimentary && sub.ComplimentaryUntilCycle > 0 {
				n = sub.ComplimentaryUntilCycle - sub.CurrentCycle + 1
			}
			if n < 0 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Complimentary cycles cannot be negative"})
				return
			}
			waiveCurrent = !sub.IsComplimentary
			sub.IsComplimentary = true
			sub.ComplimentaryUntilCycle = 0
			if n > 0 {
				sub.ComplimentaryUntilCycle = sub.CurrentCycle + n - 1
			}
		} else {
			sub.IsComplimentary = false
			sub.ComplimentaryUntilCycle = 0
		}
		compChanged = true
	}
	if err := utils.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(&sub).Error; err != nil {
			return err
		}
		if compChanged {
			return repriceCycles(tx, &sub, waiveCurrent)
		}
		return nil
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update subscription"})
		return
	}
	c.JSON(http.StatusOK, buildSubscriptionView(sub, true))
}

// AdminCancelSubscription cancels a subscription immediately. (Turning off
// auto-renew instead lets it run to the end of the paid period.)
func AdminCancelSubscription(c *gin.Context) {
	sub, ok := findSubscription(c)
	if !ok {
		return
	}
	if sub.Status != subStatusActive {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Subscription is not active"})
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	_ = c.ShouldBindJSON(&in)
	now := time.Now()
	sub.Status = subStatusCancelled
	sub.CancelledAt = &now
	sub.CancellationReason = strings.TrimSpace(in.Reason)
	if sub.CancellationReason == "" {
		sub.CancellationReason = "Cancelled by administrator"
	}
	if err := utils.DB.Save(&sub).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to cancel subscription"})
		return
	}
	c.JSON(http.StatusOK, buildSubscriptionView(sub, true))
}

// AdminCreateSubscriptionPayment records a payment. apply_to selects the
// cycle:
//   - "cycle" (default when cycle_id is set): an existing cycle
//   - "next": the period after the current one; opens that cycle now so the
//     subscription renews into it even with auto-renew off
//   - "reactivate": a cancelled subscription restarts today with a new cycle
func AdminCreateSubscriptionPayment(c *gin.Context) {
	sub, ok := findSubscription(c)
	if !ok {
		return
	}
	var in struct {
		subscriptionPaymentInput
		ApplyTo string `json:"apply_to"`
		CycleID string `json:"cycle_id"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	today := storeToday(sub.StoreID)
	applyTo := strings.ToLower(strings.TrimSpace(in.ApplyTo))
	if applyTo == "" {
		if in.CycleID != "" {
			applyTo = "cycle"
		} else {
			applyTo = "next"
		}
	}
	// Reactivating a complimentary subscription needs no payment.
	freeReactivation := applyTo == "reactivate" && sub.IsComplimentary
	if !freeReactivation {
		if err := in.validate(today); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	actor := actorUserID(c)

	var payment models.SubscriptionPayment
	err := utils.DB.Transaction(func(tx *gorm.DB) error {
		var cycleID uuid.UUID
		switch applyTo {
		case "cycle":
			var cycle models.SubscriptionCycle
			if err := tx.Where("id = ? AND subscription_id = ?", in.CycleID, sub.ID).First(&cycle).Error; err != nil {
				return badRequest("Billing cycle not found")
			}
			if cycle.Amount-cycle.AmountPaid <= 0.005 {
				if cycle.Complimentary {
					return badRequest("This cycle is complimentary — no payment needed")
				}
				return badRequest("This cycle is already fully paid")
			}
			cycleID = cycle.ID
		case "next":
			if sub.Status != subStatusActive {
				return badRequest("Subscription is cancelled — reactivate it instead")
			}
			if cycleIsComplimentary(&sub, sub.CurrentCycle+1) {
				return badRequest("The next cycle is complimentary — no payment needed")
			}
			var next models.SubscriptionCycle
			err := tx.Where("subscription_id = ? AND cycle_number = ?", sub.ID, sub.CurrentCycle+1).First(&next).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				end, perr := subPeriodEnd(sub.CurrentPeriodEnd, sub.BillingCycle, sub.AnchorDate)
				if perr != nil {
					return perr
				}
				next = newSubCycle(&sub, sub.CurrentCycle+1, sub.CurrentPeriodEnd, end, subSourcePrepaid)
				if err := tx.Create(&next).Error; err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
			cycleID = next.ID
		case "reactivate":
			if sub.Status == subStatusActive {
				return badRequest("Subscription is already active")
			}
			if _, err := activeSubscriptionForStore(tx, sub.StoreID); err == nil {
				return badRequest("This store already has another active subscription")
			}
			var maxCycle struct{ Max int }
			tx.Model(&models.SubscriptionCycle{}).Select("COALESCE(MAX(cycle_number), 0) AS max").
				Where("subscription_id = ?", sub.ID).Scan(&maxCycle)
			end, _ := subPeriodEnd(today, sub.BillingCycle, today)
			cycle := newSubCycle(&sub, maxCycle.Max+1, today, end, subSourceReactivation)
			if err := tx.Create(&cycle).Error; err != nil {
				return err
			}
			sub.Status = subStatusActive
			sub.CancelledAt = nil
			sub.CancellationReason = ""
			sub.AnchorDate = today
			sub.CurrentCycle = cycle.CycleNumber
			sub.CurrentPeriodStart = today
			sub.CurrentPeriodEnd = end
			if err := tx.Save(&sub).Error; err != nil {
				return err
			}
			cycleID = cycle.ID
			if cycle.Complimentary {
				return nil // free cycle: nothing to record
			}
			if freeReactivation {
				// Past the complimentary window: this cycle is billable.
				if err := in.validate(today); err != nil {
					return badRequest(err.Error())
				}
			}
		default:
			return badRequest("apply_to must be cycle, next or reactivate")
		}
		var err error
		payment, err = recordSubPayment(tx, &sub, cycleID, in.subscriptionPaymentInput, actor)
		return err
	})
	var br badRequestError
	if errors.As(err, &br) {
		c.JSON(http.StatusBadRequest, gin.H{"error": br.msg})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to record payment"})
		return
	}
	utils.DB.First(&sub, "id = ?", sub.ID)
	resp := gin.H{"subscription": buildSubscriptionView(sub, true)}
	if payment.ID != uuid.Nil {
		resp["payment"] = payment
	}
	c.JSON(http.StatusCreated, resp)
}

// AdminDeleteSubscriptionPayment removes a mistaken payment. A not-yet-started
// prepaid cycle left with no payments is removed too, so it no longer forces
// a renewal.
func AdminDeleteSubscriptionPayment(c *gin.Context) {
	sub, ok := findSubscription(c)
	if !ok {
		return
	}
	var payment models.SubscriptionPayment
	if err := utils.DB.Where("id = ? AND subscription_id = ?", c.Param("paymentId"), sub.ID).First(&payment).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Payment not found"})
		return
	}
	err := utils.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&payment).Error; err != nil {
			return err
		}
		cycle, err := recomputeCycle(tx, payment.CycleID)
		if err != nil {
			return err
		}
		if cycle.CycleNumber > sub.CurrentCycle && cycle.AmountPaid <= 0 && cycle.Source == subSourcePrepaid {
			return tx.Delete(&cycle).Error
		}
		return nil
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete payment"})
		return
	}
	c.JSON(http.StatusOK, buildSubscriptionView(sub, true))
}

type badRequestError struct{ msg string }

func (e badRequestError) Error() string { return e.msg }

func badRequest(msg string) error { return badRequestError{msg: msg} }

// ---------------------------------------------------------------------------
// Store: My Plan
// ---------------------------------------------------------------------------

// MyPlanPageRequired blocks the My Plan API for a store whose My Plan page is
// switched off in Developer Settings → Pages & Menus.
func MyPlanPageRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		var storeID *uuid.UUID
		if id, ok := currentStoreID(c); ok {
			storeID = &id
		}
		if !pageFeatureEnabledForStore(storeID, myPlanPageKey) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": "My Plan is disabled for this store (Developer Settings → Pages & Menus)",
			})
			return
		}
		c.Next()
	}
}

// GetMyPlan returns the active store's current subscription (or its most
// recent one when none is active).
func GetMyPlan(c *gin.Context) {
	storeID, ok := currentStoreID(c)
	if !ok {
		c.JSON(http.StatusOK, gin.H{"subscription": nil})
		return
	}
	var subs []models.StoreSubscription
	utils.DB.Where("store_id = ? AND status = ?", storeID, subStatusActive).Find(&subs)
	for i := range subs {
		refreshSubscription(&subs[i])
	}

	sub, err := activeSubscriptionForStore(utils.DB, storeID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = utils.DB.Where("store_id = ?", storeID).Order("updated_at DESC").First(&sub).Error
	}
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"subscription": nil})
		return
	}
	view := buildSubscriptionView(sub, true)
	c.JSON(http.StatusOK, gin.H{"subscription": view})
}

// GetMyPlanHistory returns every subscription of the active store with its
// billing cycles and payments, newest first.
func GetMyPlanHistory(c *gin.Context) {
	storeID, ok := currentStoreID(c)
	if !ok {
		c.JSON(http.StatusOK, []subscriptionView{})
		return
	}
	var subs []models.StoreSubscription
	if err := utils.DB.Where("store_id = ?", storeID).Order("created_at DESC").Find(&subs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load subscription history"})
		return
	}
	out := make([]subscriptionView, 0, len(subs))
	for i := range subs {
		refreshSubscription(&subs[i])
		out = append(out, buildSubscriptionView(subs[i], true))
	}
	c.JSON(http.StatusOK, out)
}
