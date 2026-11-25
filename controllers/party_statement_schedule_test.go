package controllers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestResolveStatementPeriod(t *testing.T) {
	day := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC) // Thursday
	cases := map[string][2]string{
		"this_week":        {"2026-01-12", "2026-01-15"},
		"previous_week":    {"2026-01-05", "2026-01-11"},
		"last_n_days":      {"2026-01-09", "2026-01-15"}, // N = 7
		"this_month":       {"2026-01-01", "2026-01-15"},
		"previous_month":   {"2025-12-01", "2025-12-31"},
		"last_30_days":     {"2025-12-17", "2026-01-15"},
		"this_quarter":     {"2026-01-01", "2026-01-15"},
		"previous_quarter": {"2025-10-01", "2025-12-31"},
		"this_fy":          {"2025-04-01", "2026-01-15"},
		"previous_fy":      {"2024-04-01", "2025-03-31"},
	}
	for p, want := range cases {
		from, to, err := resolveStatementPeriod(p, 7, day, "", "")
		if err != nil || from != want[0] || to != want[1] {
			t.Errorf("%s = %s..%s (%v), want %s..%s", p, from, to, err, want[0], want[1])
		}
	}
	// March 1 → previous month is February (non-leap 2026).
	from, to, _ := resolveStatementPeriod("previous_month", 0, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), "", "")
	if from != "2026-02-01" || to != "2026-02-28" {
		t.Errorf("previous_month on Mar 1 = %s..%s", from, to)
	}
	// Sunday still belongs to the week that started on Monday.
	sunday := time.Date(2026, 1, 18, 0, 0, 0, 0, time.UTC)
	if from, to, _ := resolveStatementPeriod("this_week", 0, sunday, "", ""); from != "2026-01-12" || to != "2026-01-18" {
		t.Errorf("this_week on Sunday = %s..%s", from, to)
	}
	if from, to, _ := resolveStatementPeriod("previous_week", 0, sunday, "", ""); from != "2026-01-05" || to != "2026-01-11" {
		t.Errorf("previous_week on Sunday = %s..%s", from, to)
	}
	if _, _, err := resolveStatementPeriod("last_n_days", 500, day, "", ""); err == nil {
		t.Error("last_n_days with 500 days should fail")
	}
}

func TestNextStatementRun(t *testing.T) {
	loc := time.FixedZone("IST", 5*3600+1800)
	after := time.Date(2026, 1, 31, 12, 0, 0, 0, loc) // Saturday
	cases := []struct {
		name string
		s    models.PartyStatementSchedule
		want string // "" = nil
	}{
		{"daily later today", models.PartyStatementSchedule{Frequency: "daily", SendTime: "18:00"}, "2026-01-31 18:00"},
		{"daily passed", models.PartyStatementSchedule{Frequency: "daily", SendTime: "09:00"}, "2026-02-01 09:00"},
		{"weekly monday", models.PartyStatementSchedule{Frequency: "weekly", Weekday: 1, SendTime: "10:00"}, "2026-02-02 10:00"},
		{"weekly same day later", models.PartyStatementSchedule{Frequency: "weekly", Weekday: 6, SendTime: "13:00"}, "2026-01-31 13:00"},
		{"monthly 1st", models.PartyStatementSchedule{Frequency: "monthly", DayOfMonth: 1, SendTime: "10:00"}, "2026-02-01 10:00"},
		{"monthly last day today", models.PartyStatementSchedule{Frequency: "monthly", DayOfMonth: 0, SendTime: "20:00"}, "2026-01-31 20:00"},
		{"monthly last day passed", models.PartyStatementSchedule{Frequency: "monthly", DayOfMonth: 0, SendTime: "10:00"}, "2026-02-28 10:00"},
		{"once future", models.PartyStatementSchedule{Frequency: "once", RunDate: "2026-02-05", SendTime: "10:00"}, "2026-02-05 10:00"},
		{"once passed", models.PartyStatementSchedule{Frequency: "once", RunDate: "2026-01-31", SendTime: "11:00"}, ""},
	}
	for _, c := range cases {
		got := nextStatementRun(c.s, after, loc)
		s := ""
		if got != nil {
			s = got.In(loc).Format("2006-01-02 15:04")
		}
		if s != c.want {
			t.Errorf("%s: got %q want %q", c.name, s, c.want)
		}
	}
}

func TestProcessDuePartyStatementSchedules(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = w.Write([]byte(`{"messages":[{"id":"wamid.S"}]}`))
	}))
	defer srv.Close()
	userID, party := seedStatementFixture(t, srv.URL)
	if err := utils.DB.AutoMigrate(&models.PartyStatementSchedule{}); err != nil {
		t.Fatal(err)
	}

	due := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	s := models.PartyStatementSchedule{
		ID: uuid.New(), UserID: userID, Name: "Monthly", IsEnabled: true, Channel: "whatsapp",
		Frequency: "monthly", DayOfMonth: 1, SendTime: "10:00", Period: "previous_month",
		Balance: "", PartyType: "customer", NextRunAt: &due,
	}
	if err := utils.DB.Select("*").Create(&s).Error; err != nil {
		t.Fatal(err)
	}

	now := due.Add(30 * time.Second)
	processDuePartyStatementSchedules(now)
	// A second tick while the first run is in flight must not send again.
	processDuePartyStatementSchedules(now)
	waitFor(t, func() bool { return !partyStatementBulkRunning(userID) })

	var logs []models.PartyStatementLog
	utils.DB.Where("party_id = ? AND trigger = ?", party.ID, "scheduled").Find(&logs)
	if len(logs) != 1 || logs[0].Status != "sent" || logs[0].FromDate != "2026-10-01" || logs[0].ToDate != "2026-10-31" {
		t.Fatalf("logs = %+v", logs)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("gateway calls = %d", calls)
	}

	var after models.PartyStatementSchedule
	utils.DB.First(&after, "id = ?", s.ID)
	if after.NextRunAt == nil || !after.NextRunAt.After(now) || after.NextRunAt.Day() != 1 {
		t.Fatalf("next run = %v", after.NextRunAt)
	}
	if after.LastRunStatus != "success" || after.LastRunSummary == "" {
		t.Fatalf("last run = %q %q", after.LastRunStatus, after.LastRunSummary)
	}
}

func TestProcessDueOnceScheduleDisablesItself(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"messages":[{"id":"wamid.O"}]}`))
	}))
	defer srv.Close()
	userID, party := seedStatementFixture(t, srv.URL)
	if err := utils.DB.AutoMigrate(&models.PartyStatementSchedule{}); err != nil {
		t.Fatal(err)
	}
	due := time.Now().Add(-time.Minute)
	s := models.PartyStatementSchedule{
		ID: uuid.New(), UserID: userID, Name: "Once", IsEnabled: true, Channel: "whatsapp",
		Frequency: "once", RunDate: due.Format("2006-01-02"), SendTime: due.Format("15:04"),
		Period: "custom", FromDate: "2026-10-01", ToDate: "2026-10-31",
		PartyIDs: party.ID.String(), NextRunAt: &due,
	}
	if err := utils.DB.Select("*").Create(&s).Error; err != nil {
		t.Fatal(err)
	}
	processDuePartyStatementSchedules(time.Now())
	waitFor(t, func() bool { return !partyStatementBulkRunning(userID) })

	var after models.PartyStatementSchedule
	utils.DB.First(&after, "id = ?", s.ID)
	if after.IsEnabled || after.NextRunAt != nil || after.LastRunStatus != "success" {
		t.Fatalf("once schedule after run = enabled %v next %v status %q", after.IsEnabled, after.NextRunAt, after.LastRunStatus)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond) // let the final DB write land
}

// Creating a schedule before the statement template is set keeps the user's
// work: it is saved paused with a warning instead of being rejected.
func TestCreateScheduleWithoutTemplateSavesPaused(t *testing.T) {
	userID, _ := seedStatementFixture(t, "http://unused.test")
	if err := utils.DB.AutoMigrate(&models.PartyStatementSchedule{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if res := utils.DB.Model(&models.PartyStatementSettings{}).Where("user_id = ?", userID).Update("whats_app_message_id", ""); res.Error != nil || res.RowsAffected != 1 {
		t.Fatalf("clear template: %v rows=%d", res.Error, res.RowsAffected)
	}

	body := `{"name":"weekly","frequency":"weekly","weekday":1,"send_time":"10:00","period":"previous_week",
		"filters":{"party_type":"customer","balance":"to_collect"}}`
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/x", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user_id", userID)
	CreatePartyStatementSchedule(c)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got partyStatementScheduleView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.IsEnabled || got.NextRunAt != nil {
		t.Errorf("want paused with no next run, got enabled=%v next=%v", got.IsEnabled, got.NextRunAt)
	}
	if !strings.Contains(got.Warning, "template") {
		t.Errorf("warning = %q", got.Warning)
	}
	var stored models.PartyStatementSchedule
	if err := utils.DB.First(&stored, "id = ?", got.ID).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	if stored.IsEnabled {
		t.Error("stored schedule should be paused")
	}
}
