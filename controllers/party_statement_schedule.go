package controllers

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Scheduled party statements. Each schedule stores its next run time; a
// once-a-minute scheduler claims due schedules with a conditional update
// (next_run_at must still hold the value it read), so a restart or a second
// server instance can never send the same run twice.

const (
	statementFreqOnce    = "once"
	statementFreqDaily   = "daily"
	statementFreqWeekly  = "weekly"
	statementFreqMonthly = "monthly"

	maxPartyStatementSchedules = 50
)

func isKnownStatementFrequency(f string) bool {
	switch f {
	case statementFreqOnce, statementFreqDaily, statementFreqWeekly, statementFreqMonthly:
		return true
	}
	return false
}

const (
	defaultStatementPeriodDays = 30
	maxStatementPeriodDays     = 366
)

func validateStatementPeriodDays(days int) error {
	if days < 1 || days > maxStatementPeriodDays {
		return fmt.Errorf("number of days must be 1–%d", maxStatementPeriodDays)
	}
	return nil
}

// resolveStatementPeriod turns a period preset into an inclusive date range
// relative to the run day (the store's local date). days is N for
// last_n_days. Mirrors the frontend's statementPeriodRange; weeks run
// Monday–Sunday and the financial year April–March.
func resolveStatementPeriod(period string, days int, day time.Time, customFrom, customTo string) (string, string, error) {
	d := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	y, m := d.Year(), d.Month()
	f := func(t time.Time) string { return t.Format("2006-01-02") }
	today := f(d)
	qStart := time.Month((int(m)-1)/3*3 + 1)
	fyStart := y
	if m < time.April {
		fyStart = y - 1
	}
	weekStart := d.AddDate(0, 0, -((int(d.Weekday()) + 6) % 7)) // Monday
	switch period {
	case "this_week":
		return f(weekStart), today, nil
	case "previous_week":
		return f(weekStart.AddDate(0, 0, -7)), f(weekStart.AddDate(0, 0, -1)), nil
	case "this_month":
		return f(time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)), today, nil
	case "previous_month":
		first := time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
		return f(first.AddDate(0, -1, 0)), f(first.AddDate(0, 0, -1)), nil
	case "last_n_days", "last_30_days":
		if period == "last_30_days" || days <= 0 {
			days = defaultStatementPeriodDays
		}
		if err := validateStatementPeriodDays(days); err != nil {
			return "", "", err
		}
		return f(d.AddDate(0, 0, -(days - 1))), today, nil
	case "this_quarter":
		return f(time.Date(y, qStart, 1, 0, 0, 0, 0, time.UTC)), today, nil
	case "previous_quarter":
		first := time.Date(y, qStart, 1, 0, 0, 0, 0, time.UTC)
		return f(first.AddDate(0, -3, 0)), f(first.AddDate(0, 0, -1)), nil
	case "this_fy":
		return fmt.Sprintf("%d-04-01", fyStart), today, nil
	case "previous_fy":
		return fmt.Sprintf("%d-04-01", fyStart-1), fmt.Sprintf("%d-03-31", fyStart), nil
	case "custom":
		return parseStatementRange(customFrom, customTo)
	}
	return "", "", fmt.Errorf("unknown statement period %q", period)
}

// nextStatementRun returns the first run time strictly after `after`, in loc,
// or nil when a one-time schedule has nothing left to run.
func nextStatementRun(s models.PartyStatementSchedule, after time.Time, loc *time.Location) *time.Time {
	hm, err := time.Parse("15:04", s.SendTime)
	if err != nil {
		return nil
	}
	after = after.In(loc)
	at := func(y int, m time.Month, d int) time.Time {
		return time.Date(y, m, d, hm.Hour(), hm.Minute(), 0, 0, loc)
	}
	var next time.Time
	switch s.Frequency {
	case statementFreqOnce:
		day, err := time.Parse("2006-01-02", s.RunDate)
		if err != nil {
			return nil
		}
		next = at(day.Year(), day.Month(), day.Day())
		if !next.After(after) {
			return nil
		}
		return &next
	case statementFreqDaily:
		next = at(after.Year(), after.Month(), after.Day())
		if !next.After(after) {
			next = next.AddDate(0, 0, 1)
		}
	case statementFreqWeekly:
		next = at(after.Year(), after.Month(), after.Day())
		for next.Weekday() != time.Weekday(s.Weekday) || !next.After(after) {
			next = next.AddDate(0, 0, 1)
		}
	case statementFreqMonthly:
		monthDay := func(y int, m time.Month) time.Time {
			last := time.Date(y, m+1, 0, 0, 0, 0, 0, loc).Day()
			d := s.DayOfMonth
			if d <= 0 || d > last {
				d = last
			}
			return at(y, m, d)
		}
		next = monthDay(after.Year(), after.Month())
		if !next.After(after) {
			first := time.Date(after.Year(), after.Month(), 1, 0, 0, 0, 0, loc).AddDate(0, 1, 0)
			next = monthDay(first.Year(), first.Month())
		}
	default:
		return nil
	}
	return &next
}

type partyStatementScheduleInput struct {
	Name               string           `json:"name"`
	IsEnabled          *bool            `json:"is_enabled"`
	Channel            string           `json:"channel"`
	Frequency          string           `json:"frequency"`
	RunDate            string           `json:"run_date"`
	Weekday            int              `json:"weekday"`
	DayOfMonth         int              `json:"day_of_month"`
	SendTime           string           `json:"send_time"`
	Period             string           `json:"period"`
	PeriodDays         int              `json:"period_days"`
	FromDate           string           `json:"from_date"`
	ToDate             string           `json:"to_date"`
	PartyIDs           []uuid.UUID      `json:"party_ids"`
	Filters            PartyBulkFilters `json:"filters"`
	IncludeZeroBalance bool             `json:"include_zero_balance"`
}

// applyScheduleInput validates input onto s (creating or editing).
func applyScheduleInput(userID uuid.UUID, s *models.PartyStatementSchedule, in partyStatementScheduleInput) error {
	channel := strings.ToLower(strings.TrimSpace(in.Channel))
	if channel == "" {
		channel = partyStatementChannelWhatsApp
	}
	if !isKnownPartyStatementChannel(channel) {
		return fmt.Errorf("sending statements by %q is not available yet", in.Channel)
	}
	freq := strings.ToLower(strings.TrimSpace(in.Frequency))
	if !isKnownStatementFrequency(freq) {
		return errors.New("frequency must be once, daily, weekly or monthly")
	}
	sendTime := strings.TrimSpace(in.SendTime)
	if _, err := time.Parse("15:04", sendTime); err != nil {
		return errors.New("send time must be HH:MM (24h), e.g. 10:00")
	}
	switch freq {
	case statementFreqOnce:
		if _, err := time.Parse("2006-01-02", strings.TrimSpace(in.RunDate)); err != nil {
			return errors.New("pick the date to send on")
		}
	case statementFreqWeekly:
		if in.Weekday < 0 || in.Weekday > 6 {
			return errors.New("weekday must be 0 (Sunday) to 6 (Saturday)")
		}
	case statementFreqMonthly:
		if in.DayOfMonth < 0 || in.DayOfMonth > 28 {
			return errors.New("day of month must be 1–28, or last day")
		}
	}

	period := strings.TrimSpace(in.Period)
	if !isKnownPartyStatementPeriod(period) {
		return fmt.Errorf("unknown statement period %q", in.Period)
	}
	if period == "last_30_days" {
		period, in.PeriodDays = "last_n_days", 30
	}
	if period == "last_n_days" {
		if err := validateStatementPeriodDays(in.PeriodDays); err != nil {
			return err
		}
	}
	if period == "custom" {
		if freq != statementFreqOnce {
			return errors.New("a custom date range only works for a one-time schedule; pick a period like Previous month for repeating schedules")
		}
		if _, _, err := parseStatementRange(in.FromDate, in.ToDate); err != nil {
			return err
		}
	}

	if len(in.PartyIDs) > maxPartyStatementBulk {
		return fmt.Errorf("select at most %d parties", maxPartyStatementBulk)
	}
	ids := make([]string, 0, len(in.PartyIDs))
	if len(in.PartyIDs) > 0 {
		var owned []uuid.UUID
		utils.DB.Model(&models.Party{}).Where("user_id = ? AND id IN ?", userID, in.PartyIDs).Pluck("id", &owned)
		if len(owned) == 0 {
			return errors.New("none of the selected parties were found")
		}
		for _, id := range owned {
			ids = append(ids, id.String())
		}
	}

	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = "Statement schedule"
	}
	s.Name = name
	if in.IsEnabled != nil {
		s.IsEnabled = *in.IsEnabled
	}
	s.Channel = channel
	s.Frequency = freq
	s.RunDate = strings.TrimSpace(in.RunDate)
	s.Weekday = in.Weekday
	s.DayOfMonth = in.DayOfMonth
	s.SendTime = sendTime
	s.Period = period
	s.PeriodDays = 0
	if period == "last_n_days" {
		s.PeriodDays = in.PeriodDays
	}
	s.FromDate, s.ToDate = "", ""
	if period == "custom" {
		s.FromDate, s.ToDate = strings.TrimSpace(in.FromDate), strings.TrimSpace(in.ToDate)
	}
	s.PartyIDs = strings.Join(ids, ",")
	if s.PartyIDs != "" {
		s.PartyType, s.Category, s.Balance, s.Search = "", "", "", ""
	} else {
		s.PartyType = strings.TrimSpace(in.Filters.PartyType)
		s.Category = strings.TrimSpace(in.Filters.Category)
		s.Balance = strings.TrimSpace(in.Filters.Balance)
		s.Search = strings.TrimSpace(in.Filters.Search)
	}
	s.IncludeZeroBalance = in.IncludeZeroBalance
	return nil
}

// refreshNextRun recomputes next_run_at from now; a one-time schedule whose
// moment has passed is left without a next run.
func refreshNextRun(s *models.PartyStatementSchedule) {
	loc, _ := ConfiguredLocationForUser(s.UserID)
	s.NextRunAt = nil
	if s.IsEnabled {
		s.NextRunAt = nextStatementRun(*s, time.Now(), loc)
	}
}

type partyStatementScheduleView struct {
	models.PartyStatementSchedule
	PartyCount int `json:"party_count"`
	// Warning is set when the schedule was saved paused because the channel
	// isn't set up yet.
	Warning string `json:"warning,omitempty"`
}

func scheduleView(s models.PartyStatementSchedule) partyStatementScheduleView {
	n := 0
	if s.PartyIDs != "" {
		n = len(strings.Split(s.PartyIDs, ","))
	}
	return partyStatementScheduleView{PartyStatementSchedule: s, PartyCount: n}
}

func ListPartyStatementSchedules(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var rows []models.PartyStatementSchedule
	if err := utils.DB.Where("user_id = ?", userID).Order("created_at DESC").Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load schedules"})
		return
	}
	out := make([]partyStatementScheduleView, 0, len(rows))
	for _, s := range rows {
		out = append(out, scheduleView(s))
	}
	c.JSON(http.StatusOK, out)
}

func CreatePartyStatementSchedule(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var in partyStatementScheduleInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var count int64
	utils.DB.Model(&models.PartyStatementSchedule{}).Where("user_id = ?", userID).Count(&count)
	if count >= maxPartyStatementSchedules {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("A store can have at most %d statement schedules", maxPartyStatementSchedules)})
		return
	}
	s := models.PartyStatementSchedule{ID: uuid.New(), UserID: userID, IsEnabled: true}
	if err := applyScheduleInput(userID, &s, in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	refreshNextRun(&s)
	if s.IsEnabled && s.NextRunAt == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "That date and time has already passed. Pick a time in the future."})
		return
	}
	// Keep the user's work when the channel isn't set up yet: save it paused
	// and say what's missing; they switch it on once setup is done.
	warning := ""
	if msg := partyStatementChannelReadyError(userID, s.Channel); msg != "" && s.IsEnabled {
		s.IsEnabled = false
		s.NextRunAt = nil
		warning = "Saved but paused: " + msg + ". Switch it on once that's done."
	}
	enabled := s.IsEnabled
	if err := utils.DB.Create(&s).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save schedule"})
		return
	}
	// GORM swaps a false is_enabled for the column default (true) on insert,
	// so write a paused flag explicitly.
	if !enabled {
		s.IsEnabled = false
		if err := utils.DB.Model(&s).Update("is_enabled", false).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save schedule"})
			return
		}
	}
	view := scheduleView(s)
	view.Warning = warning
	c.JSON(http.StatusCreated, view)
}

func loadOwnSchedule(c *gin.Context) (*models.PartyStatementSchedule, bool) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var s models.PartyStatementSchedule
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, c.Param("sid")).First(&s).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Schedule not found"})
		return nil, false
	}
	return &s, true
}

func UpdatePartyStatementSchedule(c *gin.Context) {
	s, ok := loadOwnSchedule(c)
	if !ok {
		return
	}
	var in partyStatementScheduleInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// Keep the existing party list when the editor doesn't resend it.
	if len(in.PartyIDs) == 0 && s.PartyIDs != "" {
		for _, raw := range strings.Split(s.PartyIDs, ",") {
			if id, err := uuid.Parse(raw); err == nil {
				in.PartyIDs = append(in.PartyIDs, id)
			}
		}
	}
	if err := applyScheduleInput(s.UserID, s, in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	refreshNextRun(s)
	if s.IsEnabled && s.NextRunAt == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "That date and time has already passed. Pick a time in the future."})
		return
	}
	if err := utils.DB.Save(s).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save schedule"})
		return
	}
	c.JSON(http.StatusOK, scheduleView(*s))
}

// SetPartyStatementScheduleEnabled pauses or resumes a schedule.
func SetPartyStatementScheduleEnabled(c *gin.Context) {
	s, ok := loadOwnSchedule(c)
	if !ok {
		return
	}
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || in.Enabled == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "enabled is required"})
		return
	}
	s.IsEnabled = *in.Enabled
	refreshNextRun(s)
	if s.IsEnabled {
		if s.NextRunAt == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "This one-time schedule's date has passed. Edit it to pick a new date."})
			return
		}
		if msg := partyStatementChannelReadyError(s.UserID, s.Channel); msg != "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": msg})
			return
		}
	}
	if err := utils.DB.Model(s).Select("is_enabled", "next_run_at").Updates(s).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update schedule"})
		return
	}
	c.JSON(http.StatusOK, scheduleView(*s))
}

func DeletePartyStatementSchedule(c *gin.Context) {
	s, ok := loadOwnSchedule(c)
	if !ok {
		return
	}
	if err := utils.DB.Delete(s).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete schedule"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Schedule deleted"})
}

// RunPartyStatementScheduleNow runs a schedule immediately in the background
// without changing its next scheduled run.
func RunPartyStatementScheduleNow(c *gin.Context) {
	s, ok := loadOwnSchedule(c)
	if !ok {
		return
	}
	if msg := partyStatementChannelReadyError(s.UserID, s.Channel); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msg})
		return
	}
	if _, running := partyStatementBulkRuns.LoadOrStore(s.UserID, struct{}{}); running {
		c.JSON(http.StatusConflict, gin.H{"error": "A statement run is already in progress for this store. Try again when it finishes."})
		return
	}
	sched := *s
	go func() {
		defer partyStatementBulkRuns.Delete(sched.UserID)
		executePartyStatementSchedule(sched, time.Now())
	}()
	c.JSON(http.StatusAccepted, gin.H{"message": "Sending statements in the background"})
}

// scheduleRecipients resolves who a run goes to: the fixed party list, or
// every party matching the filters right now.
func scheduleRecipients(s models.PartyStatementSchedule) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	q := partyFilterQuery(s.UserID, s.Search, s.Category, s.PartyType, s.Balance)
	if s.PartyIDs != "" {
		q = utils.DB.Model(&models.Party{}).Where("user_id = ? AND id IN ?", s.UserID, strings.Split(s.PartyIDs, ","))
	}
	if err := q.Order("name ASC").Limit(maxPartyStatementBulk).Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// executePartyStatementSchedule sends one run and records its outcome. at is
// the moment the run is for; the period is resolved from its local date.
func executePartyStatementSchedule(s models.PartyStatementSchedule, at time.Time) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[party-statement] schedule %s panic: %v", s.ID, r)
		}
	}()
	record := func(status, summary string) {
		now := time.Now()
		utils.DB.Model(&models.PartyStatementSchedule{}).Where("id = ?", s.ID).Updates(map[string]interface{}{
			"last_run_at": now, "last_run_status": status, "last_run_summary": summary,
		})
	}
	if msg := partyStatementChannelReadyError(s.UserID, s.Channel); msg != "" {
		record("failed", msg)
		return
	}
	loc, _ := ConfiguredLocationForUser(s.UserID)
	from, to, err := resolveStatementPeriod(s.Period, s.PeriodDays, at.In(loc), s.FromDate, s.ToDate)
	if err != nil {
		record("failed", err.Error())
		return
	}
	ids, err := scheduleRecipients(s)
	if err != nil {
		record("failed", "failed to load parties")
		return
	}
	if len(ids) == 0 {
		record("success", "No parties matched")
		return
	}
	res := runPartyStatementBatch(s.UserID, ids, s.Channel, from, to, "scheduled", !s.IncludeZeroBalance)
	status := "success"
	switch {
	case res.Failed > 0 && res.Sent == 0:
		status = "failed"
	case res.Failed > 0:
		status = "partial"
	}
	summary := fmt.Sprintf("%s to %s: %s", statementDisplayDate(from), statementDisplayDate(to), res.summary())
	record(status, summary)
	log.Printf("[party-statement] schedule %s (%s): %s", s.ID, s.Name, summary)
}

// StartPartyStatementScheduler checks for due schedules every minute.
func StartPartyStatementScheduler() {
	go func() {
		tick := func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[party-statement] scheduler panic: %v", r)
				}
			}()
			processDuePartyStatementSchedules(time.Now())
		}
		tick()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			tick()
		}
	}()
}

// processDuePartyStatementSchedules claims every due schedule and runs each
// in the background. A schedule whose store already has a run going is left
// due and retried on the next tick.
func processDuePartyStatementSchedules(now time.Time) {
	var due []models.PartyStatementSchedule
	if err := utils.DB.Where("is_enabled = ? AND next_run_at IS NOT NULL AND next_run_at <= ?", true, now).
		Find(&due).Error; err != nil {
		log.Printf("[party-statement] scheduler query failed: %v", err)
		return
	}
	for _, s := range due {
		if _, running := partyStatementBulkRuns.LoadOrStore(s.UserID, struct{}{}); running {
			continue
		}
		loc, _ := ConfiguredLocationForUser(s.UserID)
		runAt := *s.NextRunAt
		next := nextStatementRun(s, now, loc)
		updates := map[string]interface{}{"next_run_at": next}
		if next == nil {
			updates["is_enabled"] = false // one-time schedule done
		}
		claim := utils.DB.Model(&models.PartyStatementSchedule{}).
			Where("id = ? AND next_run_at = ?", s.ID, runAt).Updates(updates)
		if claim.Error != nil || claim.RowsAffected == 0 {
			partyStatementBulkRuns.Delete(s.UserID) // another instance took it
			continue
		}
		go func(s models.PartyStatementSchedule, at time.Time) {
			defer partyStatementBulkRuns.Delete(s.UserID)
			executePartyStatementSchedule(s, at)
		}(s, runAt)
	}
}
