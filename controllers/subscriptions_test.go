package controllers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupSubscriptionTestDB(t *testing.T) (*gorm.DB, models.Store) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Store{}, &models.User{}, &models.DeveloperSettings{}, &models.PageFeatureSettings{},
		&models.SubscriptionPlan{}, &models.StoreSubscription{}, &models.SubscriptionCycle{}, &models.SubscriptionPayment{},
		&models.SubscriptionBillingSettings{}, &models.SubscriptionDocCounter{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	prev := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = prev })

	store := models.Store{ID: uuid.New(), Name: "Main", Code: "MAIN", OwnerUserID: uuid.New(), IsActive: true}
	if err := db.Create(&store).Error; err != nil {
		t.Fatalf("store: %v", err)
	}
	return db, store
}

func subDate(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := parseSubDate(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestAddSubPeriodClampsMonthEnd(t *testing.T) {
	cases := []struct {
		start, cycle string
		anchor       int
		want         string
	}{
		{"2026-01-31", subCycleMonthly, 31, "2026-02-28"},
		{"2026-02-28", subCycleMonthly, 31, "2026-03-31"},
		{"2026-11-26", subCycleMonthly, 26, "2026-12-26"},
		{"2026-12-15", subCycleMonthly, 15, "2027-01-15"},
		{"2028-02-29", subCycleAnnual, 29, "2029-02-28"},
		{"2026-11-26", subCycleAnnual, 26, "2027-11-26"},
	}
	for _, tc := range cases {
		got := addSubPeriod(subDate(t, tc.start), tc.cycle, tc.anchor).Format(subDateLayout)
		if got != tc.want {
			t.Errorf("addSubPeriod(%s,%s,%d) = %s, want %s", tc.start, tc.cycle, tc.anchor, got, tc.want)
		}
	}
}

func makeSub(t *testing.T, db *gorm.DB, store models.Store, start string, autoRenew bool, price float64) models.StoreSubscription {
	t.Helper()
	end, _ := subPeriodEnd(start, subCycleMonthly, start)
	sub := models.StoreSubscription{
		ID: uuid.New(), StoreID: store.ID, PlanName: "Pro", BillingCycle: subCycleMonthly, Price: price,
		Currency: "INR", Status: subStatusActive, AutoRenew: autoRenew, AnchorDate: start,
		CurrentCycle: 1, CurrentPeriodStart: start, CurrentPeriodEnd: end,
	}
	if err := db.Create(&sub).Error; err != nil {
		t.Fatal(err)
	}
	cycle := newSubCycle(&sub, 1, start, end, subSourceInitial)
	if err := db.Create(&cycle).Error; err != nil {
		t.Fatal(err)
	}
	return sub
}

func TestRollSubscriptionAutoRenewOpensUnpaidCycles(t *testing.T) {
	db, store := setupSubscriptionTestDB(t)
	sub := makeSub(t, db, store, "2026-08-31", true, 999)

	// Three periods have ended: Sep 30, Oct 31, Nov 30 is still ahead on Nov 26.
	if _, err := rollSubscription(db, &sub, "2026-11-26"); err != nil {
		t.Fatal(err)
	}
	if sub.Status != subStatusActive || sub.CurrentCycle != 3 {
		t.Fatalf("status=%s cycle=%d, want active/3", sub.Status, sub.CurrentCycle)
	}
	if sub.CurrentPeriodStart != "2026-10-31" || sub.CurrentPeriodEnd != "2026-11-30" {
		t.Fatalf("period %s..%s", sub.CurrentPeriodStart, sub.CurrentPeriodEnd)
	}
	var cycles []models.SubscriptionCycle
	db.Where("subscription_id = ?", sub.ID).Order("cycle_number").Find(&cycles)
	if len(cycles) != 3 || cycles[1].PeriodEnd != "2026-10-31" || cycles[2].PaymentStatus != subPayUnpaid {
		t.Fatalf("unexpected cycles: %+v", cycles)
	}
}

func TestRollSubscriptionWithoutAutoRenewCancels(t *testing.T) {
	db, store := setupSubscriptionTestDB(t)
	sub := makeSub(t, db, store, "2026-10-01", false, 999)

	if _, err := rollSubscription(db, &sub, "2026-10-31"); err != nil {
		t.Fatal(err)
	}
	if sub.Status != subStatusActive {
		t.Fatalf("should still be active on the last day, got %s", sub.Status)
	}
	if _, err := rollSubscription(db, &sub, "2026-11-01"); err != nil {
		t.Fatal(err)
	}
	if sub.Status != subStatusCancelled || sub.CancelledAt == nil {
		t.Fatalf("want cancelled, got %s", sub.Status)
	}
}

func TestRollSubscriptionPrepaidRenewsWithoutAutoRenew(t *testing.T) {
	db, store := setupSubscriptionTestDB(t)
	sub := makeSub(t, db, store, "2026-10-01", false, 999)
	next := newSubCycle(&sub, 2, "2026-11-01", "2026-12-01", subSourcePrepaid)
	db.Create(&next)

	if _, err := rollSubscription(db, &sub, "2026-11-26"); err != nil {
		t.Fatal(err)
	}
	if sub.Status != subStatusActive || sub.CurrentCycle != 2 || sub.CurrentPeriodEnd != "2026-12-01" {
		t.Fatalf("got %s cycle %d end %s", sub.Status, sub.CurrentCycle, sub.CurrentPeriodEnd)
	}
}

func TestFreePlanCyclesArePaid(t *testing.T) {
	db, store := setupSubscriptionTestDB(t)
	sub := makeSub(t, db, store, "2026-10-01", true, 0)
	rollSubscription(db, &sub, "2026-11-26")
	var unpaid int64
	db.Model(&models.SubscriptionCycle{}).Where("payment_status <> ?", subPayPaid).Count(&unpaid)
	if unpaid != 0 {
		t.Fatalf("free plan left %d unpaid cycles", unpaid)
	}
}

func subRequest(t *testing.T, handler gin.HandlerFunc, method, path, pattern string, body any) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("actor_user_id", uuid.New())
		c.Set("user_id", uuid.New())
		c.Next()
	})
	r.Handle(method, pattern, handler)
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestCreateSubscriptionAndPaymentsFlow(t *testing.T) {
	db, store := setupSubscriptionTestDB(t)
	plan := models.SubscriptionPlan{ID: uuid.New(), Name: "Growth", BillingCycle: subCycleMonthly, Price: 1499, Currency: "INR",
		Features: encodeFeatures([]string{"POS", "  ", "GST"}), IsActive: true}
	db.Create(&plan)

	today := storeToday(store.ID)
	w := subRequest(t, AdminCreateSubscription, http.MethodPost, "/subs", "/subs", map[string]any{
		"store_id": store.ID, "plan_id": plan.ID, "auto_renew": false,
		"initial_payment": map[string]any{"amount": 1499, "method": "upi"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var view subscriptionView
	json.Unmarshal(w.Body.Bytes(), &view)
	if view.CurrentPayment != subPayPaid || view.AmountDue != 0 || view.CurrentPeriodStart != today {
		t.Fatalf("unexpected view: payment=%s due=%v start=%s", view.CurrentPayment, view.AmountDue, view.CurrentPeriodStart)
	}
	if view.AutoRenew {
		t.Fatal("auto_renew=false was not persisted")
	}
	if len(view.FeatureList) != 2 {
		t.Fatalf("features not cleaned: %v", view.FeatureList)
	}

	// A second assignment conflicts unless replace_existing is set.
	w = subRequest(t, AdminCreateSubscription, http.MethodPost, "/subs", "/subs", map[string]any{"store_id": store.ID, "plan_id": plan.ID})
	if w.Code != http.StatusConflict {
		t.Fatalf("expected conflict, got %d", w.Code)
	}

	// Pay the next cycle in advance.
	path := "/subs/" + view.ID.String() + "/payments"
	w = subRequest(t, AdminCreateSubscriptionPayment, http.MethodPost, path, "/subs/:id/payments", map[string]any{
		"apply_to": "next", "amount": 1499, "method": "cash",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("pay next: %d %s", w.Code, w.Body.String())
	}
	var payResp struct {
		Payment      models.SubscriptionPayment `json:"payment"`
		Subscription subscriptionView           `json:"subscription"`
	}
	json.Unmarshal(w.Body.Bytes(), &payResp)
	if !payResp.Subscription.NextCyclePaid {
		t.Fatal("next cycle should be marked paid")
	}

	// Deleting that payment removes the unstarted prepaid cycle.
	w = subRequest(t, AdminDeleteSubscriptionPayment, http.MethodDelete,
		path+"/"+payResp.Payment.ID.String(), "/subs/:id/payments/:paymentId", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("delete payment: %d %s", w.Code, w.Body.String())
	}
	var cycles int64
	db.Model(&models.SubscriptionCycle{}).Where("subscription_id = ?", view.ID).Count(&cycles)
	if cycles != 1 {
		t.Fatalf("want 1 cycle after deleting prepaid payment, got %d", cycles)
	}

	// Cancel, then reactivate with a payment.
	w = subRequest(t, AdminCancelSubscription, http.MethodPost, "/subs/"+view.ID.String()+"/cancel", "/subs/:id/cancel", map[string]any{})
	if w.Code != http.StatusOK {
		t.Fatalf("cancel: %d", w.Code)
	}
	w = subRequest(t, AdminCreateSubscriptionPayment, http.MethodPost, path, "/subs/:id/payments", map[string]any{
		"apply_to": "reactivate", "amount": 500,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("reactivate: %d %s", w.Code, w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &payResp)
	s := payResp.Subscription
	if s.Status != subStatusActive || s.CurrentCycle != 2 || s.CurrentPayment != subPayPartial || s.AmountDue != 999 {
		t.Fatalf("reactivated: status=%s cycle=%d pay=%s due=%v", s.Status, s.CurrentCycle, s.CurrentPayment, s.AmountDue)
	}
}

func TestMyPlanPageGate(t *testing.T) {
	db, store := setupSubscriptionTestDB(t)
	db.Create(&models.PageFeatureSettings{ID: uuid.New(), StoreID: &store.ID, PagesJSON: `{"/my-plan":false}`})

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("store_id", store.ID); c.Next() })
	r.GET("/my-plan", MyPlanPageRequired(), GetMyPlan)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/my-plan", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403 when page disabled, got %d", w.Code)
	}
}

func TestComplimentaryLimitedCyclesThenBillsRegularPrice(t *testing.T) {
	db, store := setupSubscriptionTestDB(t)
	sub := makeSub(t, db, store, "2026-08-01", true, 999)
	// Turn cycle 1 complimentary for 2 cycles, as the create handler does.
	sub.IsComplimentary, sub.ComplimentaryUntilCycle = true, 2
	db.Save(&sub)
	if err := repriceCycles(db, &sub, true); err != nil {
		t.Fatal(err)
	}

	if _, err := rollSubscription(db, &sub, "2026-10-15"); err != nil {
		t.Fatal(err)
	}
	var cycles []models.SubscriptionCycle
	db.Where("subscription_id = ?", sub.ID).Order("cycle_number").Find(&cycles)
	if len(cycles) != 3 {
		t.Fatalf("want 3 cycles, got %d", len(cycles))
	}
	for i, c := range cycles[:2] {
		if !c.Complimentary || c.Amount != 0 || c.WaivedAmount != 999 || c.PaymentStatus != subPayPaid {
			t.Fatalf("cycle %d should be free and waived: %+v", i+1, c)
		}
	}
	if cycles[2].Complimentary || cycles[2].Amount != 999 || cycles[2].PaymentStatus != subPayUnpaid {
		t.Fatalf("cycle 3 should bill the regular price: %+v", cycles[2])
	}
	if sub.IsComplimentary {
		t.Fatal("complimentary flag should clear once the free period ends")
	}
}

func TestComplimentaryCreateUpdateAndReactivate(t *testing.T) {
	db, store := setupSubscriptionTestDB(t)
	plan := models.SubscriptionPlan{ID: uuid.New(), Name: "Growth", BillingCycle: subCycleMonthly, Price: 1499, Currency: "INR", IsActive: true}
	db.Create(&plan)

	// Complimentary with no end date; an initial payment is ignored.
	w := subRequest(t, AdminCreateSubscription, http.MethodPost, "/subs", "/subs", map[string]any{
		"store_id": store.ID, "plan_id": plan.ID, "is_complimentary": true, "complimentary_reason": "Launch partner",
		"initial_payment": map[string]any{"amount": 1499},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var v subscriptionView
	json.Unmarshal(w.Body.Bytes(), &v)
	if !v.CurrentComplimentary || v.AmountDue != 0 || v.TotalPaid != 0 || v.TotalWaived != 1499 || v.ComplimentaryCyclesLeft != -1 || len(v.Payments) != 0 {
		t.Fatalf("unexpected complimentary view: comp=%v due=%v paid=%v waived=%v left=%d payments=%d",
			v.CurrentComplimentary, v.AmountDue, v.TotalPaid, v.TotalWaived, v.ComplimentaryCyclesLeft, len(v.Payments))
	}
	id := v.ID.String()

	// Paying the next cycle of a free plan is refused.
	w = subRequest(t, AdminCreateSubscriptionPayment, http.MethodPost, "/subs/"+id+"/payments", "/subs/:id/payments", map[string]any{"apply_to": "next", "amount": 1499})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 paying a complimentary cycle, got %d", w.Code)
	}

	// Overview: not revenue, but counted as complimentary.
	w = subRequest(t, AdminSubscriptionOverview, http.MethodGet, "/ov", "/ov", nil)
	var ov map[string]float64
	json.Unmarshal(w.Body.Bytes(), &ov)
	if ov["mrr"] != 0 || ov["complimentary"] != 1 || ov["complimentary_value"] != 1499 {
		t.Fatalf("overview: %v", ov)
	}

	// Switch off: current cycle stays free, future cycles bill again.
	w = subRequest(t, AdminUpdateSubscription, http.MethodPatch, "/subs/"+id, "/subs/:id", map[string]any{"is_complimentary": false})
	json.Unmarshal(w.Body.Bytes(), &v)
	if v.IsComplimentary || !v.CurrentComplimentary {
		t.Fatalf("after disabling: is=%v current=%v", v.IsComplimentary, v.CurrentComplimentary)
	}

	// Switch back on for 1 cycle, then cancel and reactivate without payment.
	subRequest(t, AdminUpdateSubscription, http.MethodPatch, "/subs/"+id, "/subs/:id", map[string]any{"is_complimentary": true})
	subRequest(t, AdminCancelSubscription, http.MethodPost, "/subs/"+id+"/cancel", "/subs/:id/cancel", map[string]any{})
	w = subRequest(t, AdminCreateSubscriptionPayment, http.MethodPost, "/subs/"+id+"/payments", "/subs/:id/payments", map[string]any{"apply_to": "reactivate"})
	if w.Code != http.StatusCreated {
		t.Fatalf("free reactivation: %d %s", w.Code, w.Body.String())
	}
	var r struct {
		Subscription subscriptionView `json:"subscription"`
	}
	json.Unmarshal(w.Body.Bytes(), &r)
	if r.Subscription.Status != subStatusActive || !r.Subscription.CurrentComplimentary || r.Subscription.AmountDue != 0 {
		t.Fatalf("reactivated: %+v", r.Subscription.StoreSubscription)
	}
}

func TestMakingPaidSubscriptionComplimentaryWaivesCurrentBalance(t *testing.T) {
	db, store := setupSubscriptionTestDB(t)
	today := storeToday(store.ID)
	sub := makeSub(t, db, store, today, true, 1000)
	var cycle models.SubscriptionCycle
	db.Where("subscription_id = ?", sub.ID).First(&cycle)
	recordSubPayment(db, &sub, cycle.ID, subscriptionPaymentInput{Amount: 400, PaymentDate: today, Method: "cash"}, uuid.New())

	w := subRequest(t, AdminUpdateSubscription, http.MethodPatch, "/subs/"+sub.ID.String(), "/subs/:id",
		map[string]any{"is_complimentary": true, "complimentary_cycles": 3})
	var v subscriptionView
	json.Unmarshal(w.Body.Bytes(), &v)
	if v.AmountDue != 0 || v.TotalWaived != 600 || v.ComplimentaryUntilCycle != 3 || v.ComplimentaryCyclesLeft != 3 {
		t.Fatalf("due=%v waived=%v until=%d left=%d", v.AmountDue, v.TotalWaived, v.ComplimentaryUntilCycle, v.ComplimentaryCyclesLeft)
	}
}
