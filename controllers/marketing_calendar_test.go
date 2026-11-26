package controllers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestOccasionDateRules(t *testing.T) {
	cases := map[string]map[int]string{
		"easter":           {2026: "2026-04-05", 2027: "2027-03-28", 2028: "2028-04-16"},
		"thanksgiving":     {2026: "2026-11-26", 2027: "2027-11-25"},
		"black_friday":     {2026: "2026-11-27", 2027: "2027-11-26"},
		"cyber_monday":     {2027: "2027-11-29"},
		"mothers_day":      {2027: "2027-05-09"},
		"fathers_day":      {2027: "2027-06-20"},
		"friendship_day":   {2027: "2027-08-01"},
		"independence_day": {2031: "2031-08-15"},
		"diwali":           {2027: "2027-10-29", 2028: "2028-10-17"},
	}
	for key, years := range cases {
		o := marketingOccasionByKey[key]
		if o == nil {
			t.Fatalf("missing occasion %s", key)
		}
		for y, want := range years {
			d, ok := o.date(y)
			if !ok || d.Format(ymdLayout) != want {
				t.Errorf("%s %d = %v %v, want %s", key, y, d.Format(ymdLayout), ok, want)
			}
		}
	}
	if _, ok := marketingOccasionByKey["diwali"].date(2035); ok {
		t.Error("lunar festival outside the table should have no date")
	}
	// Every occasion has a date in a covered year and a known campaign type.
	for _, o := range marketingOccasions {
		if _, ok := o.date(2027); !ok {
			t.Errorf("%s has no 2027 date", o.Key)
		}
		if !marketingCampaignTypes[marketingOccasionByKey[o.Key].CampaignType] {
			t.Errorf("%s: bad campaign type", o.Key)
		}
	}
	got := occasionsBetween(mustYMD(t, "2027-10-01"), mustYMD(t, "2027-10-31"), "india", "")
	if len(got) == 0 || got[0].Date > got[len(got)-1].Date {
		t.Fatalf("occasions = %v", got)
	}
	for _, o := range got {
		if o.Region != "india" {
			t.Fatalf("region filter leaked %v", o)
		}
	}
}

func mustYMD(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse(ymdLayout, s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestAddMonthsClamped(t *testing.T) {
	start := time.Date(2027, 1, 31, 10, 0, 0, 0, time.Local)
	for n, want := range map[int]string{1: "2027-02-28", 2: "2027-03-31", 3: "2027-04-30", 13: "2028-02-29"} {
		got := addMonthsClamped(start, n)
		if got.Format(ymdLayout) != want || got.Hour() != 10 {
			t.Errorf("+%d months = %v, want %s 10:00", n, got, want)
		}
	}
}

func TestNextMarketingRunYearlyOccasionKeepsOffset(t *testing.T) {
	camp := &models.MarketingCampaign{Recurrence: mktRecurYearlyOccasion, OccasionKey: "diwali"}
	// Sent the day before Diwali at 10:00.
	prev := marketingRun{At: time.Date(2027, 10, 28, 10, 0, 0, 0, time.Local), OccasionDate: "2027-10-29"}
	next, ok := nextMarketingRun(camp, prev, 1)
	if !ok || next.OccasionDate != "2028-10-17" || !next.At.Equal(time.Date(2028, 10, 16, 10, 0, 0, 0, time.Local)) {
		t.Fatalf("next = %+v %v", next, ok)
	}
	if _, ok := nextMarketingRun(camp, next, 2); ok {
		t.Fatal("series should end when the festival table runs out")
	}

	camp.RecurrenceUntil = "2028-10-15"
	if _, ok := nextMarketingRun(camp, prev, 1); ok {
		t.Fatal("run after repeat-until date should end the series")
	}

	monthly := &models.MarketingCampaign{Recurrence: mktRecurMonthly}
	start := time.Date(2027, 1, 31, 9, 0, 0, 0, time.Local)
	monthly.SeriesStart = &start
	r1, _ := nextMarketingRun(monthly, marketingRun{At: start}, 1)
	r2, _ := nextMarketingRun(monthly, r1, 2)
	if r1.At.Day() != 28 || r2.At.Day() != 31 {
		t.Fatalf("monthly runs = %v, %v", r1.At, r2.At)
	}
}

func TestMarketingRecurrenceValidation(t *testing.T) {
	db := setupMarketingTestDB(t)
	store := uuid.New()
	connectAllMarketingChannels(t, db, store)
	base := func() map[string]interface{} {
		return map[string]interface{}{"name": "Fest", "channels": []string{"sms"}, "message": "Hi {{occasion}}"}
	}
	bad := []map[string]interface{}{
		{"recurrence": "yearly_occasion"},
		{"occasion_key": "diwali", "occasion_date": "2027-10-30"},
		{"occasion_key": "not_a_day"},
		{"recurrence": "fortnightly"},
		{"recurrence": "weekly", "recurrence_until": "2027-01-01", "scheduled_at": time.Date(2027, 2, 1, 9, 0, 0, 0, time.Local)},
	}
	for _, extra := range bad {
		in := base()
		for k, v := range extra {
			in[k] = v
		}
		if code, out := callMarketing(t, CreateMarketingCampaign, http.MethodPost, store, "", in); code != http.StatusBadRequest {
			t.Errorf("%v accepted: %d %v", extra, code, out)
		}
	}
	in := base()
	in["occasion_key"], in["occasion_date"], in["recurrence"] = "diwali", "2027-10-29", "yearly_occasion"
	code, out := callMarketing(t, CreateMarketingCampaign, http.MethodPost, store, "", in)
	if code != http.StatusCreated || out["occasion_name"] != "Diwali" || out["status"] != mktStatusDraft {
		t.Fatalf("draft series = %d %v", code, out)
	}
}

// scheduleSeries creates a scheduled repeating SMS campaign and returns it.
func scheduleSeries(t *testing.T, store uuid.UUID, extra map[string]interface{}) models.MarketingCampaign {
	t.Helper()
	in := map[string]interface{}{
		"name": "Weekly deal", "channels": []string{"sms"}, "message": "Deal for {{first_name}}",
		"recurrence": "weekly", "scheduled_at": time.Now().Add(2 * time.Minute),
	}
	for k, v := range extra {
		in[k] = v
	}
	code, out := callMarketing(t, CreateMarketingCampaign, http.MethodPost, store, "", in)
	if code != http.StatusCreated || out["status"] != mktStatusScheduled || out["series_start"] == nil {
		t.Fatalf("schedule series = %d %v", code, out)
	}
	var camp models.MarketingCampaign
	if err := utils.DB.First(&camp, "id = ?", out["id"]).Error; err != nil {
		t.Fatal(err)
	}
	return camp
}

func TestMarketingSeriesSchedulerSendsRunAndAdvances(t *testing.T) {
	db := setupMarketingTestDB(t)
	_, sms := stubMarketingTransports(t)
	store := uuid.New()
	connectAllMarketingChannels(t, db, store)
	db.Create(&models.Party{ID: uuid.New(), UserID: store, Name: "Asha Rao", PartyType: "customer", Phone: "9000000001", IsActive: true})

	series := scheduleSeries(t, store, nil)
	start := *series.SeriesStart
	db.Model(&series).Update("scheduled_at", time.Now().Add(-time.Second))

	processDueMarketingCampaigns()
	processDueMarketingCampaigns() // a second tick must not resend
	if len(*sms) != 1 || (*sms)[0].Body != "Deal for Asha" {
		t.Fatalf("sms = %v", *sms)
	}
	var runs []models.MarketingCampaign
	db.Where("parent_id = ?", series.ID).Find(&runs)
	if len(runs) != 1 || runs[0].Status != mktStatusSent || runs[0].Recurrence != "" || runs[0].SentCount != 1 {
		t.Fatalf("runs = %+v", runs)
	}
	db.First(&series, "id = ?", series.ID)
	if series.Status != mktStatusScheduled || series.RunCount != 1 || !series.ScheduledAt.Equal(start.AddDate(0, 0, 7)) {
		t.Fatalf("series = status %s run %d at %v (start %v)", series.Status, series.RunCount, series.ScheduledAt, start)
	}
}

func TestMarketingSeriesCompletesAndSkipsLateRuns(t *testing.T) {
	db := setupMarketingTestDB(t)
	_, sms := stubMarketingTransports(t)
	store := uuid.New()
	connectAllMarketingChannels(t, db, store)
	db.Create(&models.Party{ID: uuid.New(), UserID: store, Name: "A", PartyType: "customer", Phone: "9000000001", IsActive: true})

	// Last run: repeat-until is today, so the next weekly run is past it.
	series := scheduleSeries(t, store, map[string]interface{}{"recurrence_until": localYMD(time.Now().Add(2 * time.Minute)).Format(ymdLayout)})
	db.Model(&series).Update("scheduled_at", time.Now().Add(-time.Second))
	processDueMarketingCampaigns()
	db.First(&series, "id = ?", series.ID)
	if series.Status != mktStatusCompleted || len(*sms) != 1 {
		t.Fatalf("series = %s, sms = %d", series.Status, len(*sms))
	}

	// Two days late (server offline): skipped, series moves on.
	late := scheduleSeries(t, store, nil)
	db.Model(&late).Updates(map[string]interface{}{"scheduled_at": time.Now().Add(-48 * time.Hour), "series_start": time.Now().Add(-48 * time.Hour)})
	processDueMarketingCampaigns()
	db.First(&late, "id = ?", late.ID)
	var n int64
	db.Model(&models.MarketingCampaign{}).Where("parent_id = ?", late.ID).Count(&n)
	if len(*sms) != 1 || n != 0 || late.Status != mktStatusScheduled || !late.ScheduledAt.After(time.Now()) || late.LastError == "" {
		t.Fatalf("late: sms %d runs %d status %s at %v err %q", len(*sms), n, late.Status, late.ScheduledAt, late.LastError)
	}
}

func TestMarketingSendNowOnSeriesKeepsSchedule(t *testing.T) {
	db := setupMarketingTestDB(t)
	stubMarketingTransports(t)
	store := uuid.New()
	connectAllMarketingChannels(t, db, store)
	series := scheduleSeries(t, store, nil)
	prevAsync := marketingRunAsync
	marketingRunAsync = runMarketingCampaign
	t.Cleanup(func() { marketingRunAsync = prevAsync })

	code, out := callMarketing(t, SendMarketingCampaign, http.MethodPost, store, series.ID.String(), nil)
	if code != http.StatusAccepted || out["parent_id"] != series.ID.String() {
		t.Fatalf("send now = %d %v", code, out)
	}
	var run models.MarketingCampaign
	db.First(&run, "id = ?", out["id"])
	if run.Status == mktStatusSending || run.Status == mktStatusScheduled {
		t.Fatalf("run status = %s", run.Status)
	}
	db.First(&series, "id = ?", series.ID)
	if series.Status != mktStatusScheduled || series.RunCount != 0 {
		t.Fatalf("series after send now = %s / %d", series.Status, series.RunCount)
	}
}

func TestMarketingCalendarProjectsSeriesRuns(t *testing.T) {
	db := setupMarketingTestDB(t)
	store := uuid.New()
	connectAllMarketingChannels(t, db, store)
	scheduleSeries(t, store, map[string]interface{}{
		"name": "Diwali greetings", "recurrence": "yearly_occasion",
		"occasion_key": "diwali", "occasion_date": "2027-10-29",
		"scheduled_at": time.Date(2027, 10, 29, 9, 0, 0, 0, time.Local),
	})

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/?from=2027-10-01&to=2028-10-31&region=india", nil)
	c.Set("user_id", store)
	GetMarketingCalendar(c)
	if w.Code != http.StatusOK {
		t.Fatalf("calendar = %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Occasions []marketingOccasionDTO   `json:"occasions"`
		Events    []marketingCalendarEvent `json:"events"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	diwalis := 0
	for _, o := range out.Occasions {
		if o.Key == "diwali" {
			diwalis++
		}
		if o.Region != "india" {
			t.Fatalf("region filter: %v", o)
		}
	}
	if diwalis != 2 {
		t.Fatalf("diwali occurrences = %d", diwalis)
	}
	if len(out.Events) != 2 || out.Events[0].Projected || !out.Events[1].Projected ||
		out.Events[1].At.In(time.Local).Format(ymdLayout) != "2028-10-17" || out.Events[0].Repeat != "Every year on Diwali" {
		t.Fatalf("events = %+v", out.Events)
	}
}

func TestNextMarketingRunOccasionTagOnFixedRepeat(t *testing.T) {
	at := time.Date(2027, 10, 29, 9, 0, 0, 0, time.Local)
	diwali := &models.MarketingCampaign{Recurrence: mktRecurYearly, OccasionKey: "diwali", SeriesStart: &at}
	next, _ := nextMarketingRun(diwali, marketingRun{At: at, OccasionDate: "2027-10-29"}, 1)
	if next.OccasionDate != "" {
		t.Fatalf("Diwali tag kept on 29 Oct 2028: %q", next.OccasionDate)
	}
	xmas := time.Date(2027, 12, 25, 9, 0, 0, 0, time.Local)
	christmas := &models.MarketingCampaign{Recurrence: mktRecurYearly, OccasionKey: "christmas", SeriesStart: &xmas}
	next, _ = nextMarketingRun(christmas, marketingRun{At: xmas, OccasionDate: "2027-12-25"}, 1)
	if next.OccasionDate != "2028-12-25" {
		t.Fatalf("Christmas tag = %q", next.OccasionDate)
	}
}
