package controllers

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupAttendanceReminderTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.AttendanceReminderSettings{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	previousDB := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = previousDB })
	return db
}

func newReminderContext(method, body string) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	c.Request = httptest.NewRequest(method, "/settings/attendance-reminder", reader)
	c.Request.Header.Set("Content-Type", "application/json")
	return c, rec
}

func TestGetAttendanceReminderSettingsDefaults(t *testing.T) {
	setupAttendanceReminderTestDB(t)
	userID := uuid.New()

	c, rec := newReminderContext("GET", "")
	c.Set("user_id", userID)
	GetAttendanceReminderSettings(c)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Enabled            bool   `json:"enabled"`
		ReminderTime       string `json:"reminder_time"`
		Timezone           string `json:"timezone"`
		RemindLaterMinutes int    `json:"remind_later_minutes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Enabled || body.ReminderTime != "09:00" || body.Timezone != "" || body.RemindLaterMinutes != 30 {
		t.Fatalf("unexpected defaults: %+v", body)
	}
}

func TestUpdateAttendanceReminderSettingsRoundTrip(t *testing.T) {
	setupAttendanceReminderTestDB(t)
	userID := uuid.New()

	c, rec := newReminderContext("PUT", `{"enabled":true,"reminder_time":"18:30","timezone":"Asia/Kolkata","remind_later_minutes":45}`)
	c.Set("user_id", userID)
	UpdateAttendanceReminderSettings(c)

	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Enabled            bool   `json:"enabled"`
		ReminderTime       string `json:"reminder_time"`
		Timezone           string `json:"timezone"`
		RemindLaterMinutes int    `json:"remind_later_minutes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Enabled || body.ReminderTime != "18:30" || body.Timezone != "Asia/Kolkata" || body.RemindLaterMinutes != 45 {
		t.Fatalf("unexpected response: %+v", body)
	}

	// Persisted row, then updated by a second PUT.
	var stored models.AttendanceReminderSettings
	if err := utils.DB.Where("user_id = ?", userID).First(&stored).Error; err != nil {
		t.Fatalf("load stored: %v", err)
	}
	if !stored.Enabled || stored.ReminderTime != "18:30" || stored.Timezone != "Asia/Kolkata" || stored.RemindLaterMinutes != 45 {
		t.Fatalf("unexpected stored row: %+v", stored)
	}

	c, rec = newReminderContext("PUT", `{"enabled":false,"reminder_time":"07:15","timezone":"","remind_later_minutes":0}`)
	c.Set("user_id", userID)
	UpdateAttendanceReminderSettings(c)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if err := utils.DB.Where("user_id = ?", userID).First(&stored).Error; err != nil {
		t.Fatalf("reload stored: %v", err)
	}
	// A non-positive snooze interval falls back to the default.
	if stored.Enabled || stored.ReminderTime != "07:15" || stored.Timezone != "" || stored.RemindLaterMinutes != 30 {
		t.Fatalf("unexpected updated row: %+v", stored)
	}
}

func TestUpdateAttendanceReminderSettingsValidation(t *testing.T) {
	setupAttendanceReminderTestDB(t)
	userID := uuid.New()

	for _, payload := range []string{
		`{"enabled":true,"reminder_time":"25:00","timezone":"Asia/Kolkata"}`,
		`{"enabled":true,"reminder_time":"9am","timezone":"Asia/Kolkata"}`,
		`{"enabled":true,"reminder_time":"09:00","timezone":"Not/AZone"}`,
	} {
		c, rec := newReminderContext("PUT", payload)
		c.Set("user_id", userID)
		UpdateAttendanceReminderSettings(c)
		if rec.Code != 400 {
			t.Fatalf("payload %s: status = %d, want 400", payload, rec.Code)
		}
	}
}
