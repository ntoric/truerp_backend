package controllers

import (
	"net/http"
	"regexp"
	"strings"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

var attendanceReminderTimePattern = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

func defaultAttendanceReminderSettings(userID uuid.UUID) models.AttendanceReminderSettings {
	return models.AttendanceReminderSettings{
		ID:                 uuid.New(),
		UserID:             userID,
		Enabled:            false,
		ReminderTime:       "09:00",
		Timezone:           "",
		RemindLaterMinutes: 30,
	}
}

func attendanceReminderSettingsResponse(settings models.AttendanceReminderSettings) gin.H {
	return gin.H{
		"id":                   settings.ID,
		"user_id":              settings.UserID,
		"enabled":              settings.Enabled,
		"reminder_time":        settings.ReminderTime,
		"timezone":             settings.Timezone,
		"remind_later_minutes": settings.RemindLaterMinutes,
		"common_timezones":     CommonTimezones,
		"created_at":           settings.CreatedAt,
		"updated_at":           settings.UpdatedAt,
	}
}

func GetAttendanceReminderSettings(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var settings models.AttendanceReminderSettings
	if err := utils.DB.Where("user_id = ?", userID).First(&settings).Error; err != nil {
		c.JSON(http.StatusOK, attendanceReminderSettingsResponse(defaultAttendanceReminderSettings(userID)))
		return
	}
	if strings.TrimSpace(settings.ReminderTime) == "" {
		settings.ReminderTime = "09:00"
	}
	if settings.RemindLaterMinutes <= 0 {
		settings.RemindLaterMinutes = 30
	}

	c.JSON(http.StatusOK, attendanceReminderSettingsResponse(settings))
}

func UpdateAttendanceReminderSettings(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		Enabled            bool   `json:"enabled"`
		ReminderTime       string `json:"reminder_time"`
		Timezone           string `json:"timezone"`
		RemindLaterMinutes int    `json:"remind_later_minutes"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	reminderTime := strings.TrimSpace(input.ReminderTime)
	if reminderTime == "" {
		reminderTime = "09:00"
	}
	if !attendanceReminderTimePattern.MatchString(reminderTime) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Reminder time must be in HH:MM (24h) format"})
		return
	}

	timezone := strings.TrimSpace(input.Timezone)
	if err := ValidateTimezone(timezone); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid timezone"})
		return
	}

	remindLaterMinutes := input.RemindLaterMinutes
	if remindLaterMinutes <= 0 {
		remindLaterMinutes = 30
	}
	if remindLaterMinutes > 1440 {
		remindLaterMinutes = 1440
	}

	var settings models.AttendanceReminderSettings
	if err := utils.DB.Where("user_id = ?", userID).First(&settings).Error; err != nil {
		settings = defaultAttendanceReminderSettings(userID)
		settings.Enabled = input.Enabled
		settings.ReminderTime = reminderTime
		settings.Timezone = timezone
		settings.RemindLaterMinutes = remindLaterMinutes
		if err := utils.DB.Create(&settings).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save attendance reminder settings"})
			return
		}
	} else {
		if err := utils.DB.Model(&settings).Updates(map[string]interface{}{
			"enabled":              input.Enabled,
			"reminder_time":        reminderTime,
			"timezone":             timezone,
			"remind_later_minutes": remindLaterMinutes,
		}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update attendance reminder settings"})
			return
		}
		if err := utils.DB.Where("user_id = ?", userID).First(&settings).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load attendance reminder settings"})
			return
		}
	}

	c.JSON(http.StatusOK, attendanceReminderSettingsResponse(settings))
}
