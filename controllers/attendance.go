package controllers

import (
	"errors"
	"math"
	"net/http"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func parseAttendanceDate(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	layouts := []string{
		"2006-01-02",
		time.RFC3339,
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02T15:04:05",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), nil
		}
	}
	return time.Time{}, errors.New("invalid date")
}

func calculateWorkHours(checkIn, checkOut *time.Time) float64 {
	if checkIn == nil || checkOut == nil {
		return 0
	}
	if !checkOut.After(*checkIn) {
		return 0
	}
	hours := checkOut.Sub(*checkIn).Hours()
	return math.Round(hours*100) / 100
}

func resolveWorkHours(status string, checkIn, checkOut *time.Time, provided float64) float64 {
	if checkIn != nil && checkOut != nil {
		return calculateWorkHours(checkIn, checkOut)
	}
	if provided > 0 {
		return provided
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "present":
		return 8
	case "half_day":
		return 4
	default:
		return 0
	}
}

// attendanceOnDate scopes rows to a calendar day across SQLite and PostgreSQL.
func attendanceOnDate(db *gorm.DB, dateStr string) *gorm.DB {
	return db.Where(utils.SQLDateEquals("date"), dateStr)
}

func attendanceBetweenDates(db *gorm.DB, startDate, endDate string) *gorm.DB {
	if startDate != "" {
		db = db.Where(utils.SQLDateGTE("date"), startDate)
	}
	if endDate != "" {
		db = db.Where(utils.SQLDateLTE("date"), endDate)
	}
	return db
}

func findAttendanceForDay(userID, staffID uuid.UUID, dateStr string) (models.Attendance, error) {
	var attendance models.Attendance
	err := attendanceOnDate(utils.DB.Where("user_id = ? AND staff_id = ?", userID, staffID), dateStr).
		Order("updated_at DESC, created_at DESC").
		First(&attendance).Error
	return attendance, err
}

func removeDuplicateAttendance(userID, staffID, keepID uuid.UUID, dateStr string) {
	attendanceOnDate(
		utils.DB.Where("user_id = ? AND staff_id = ? AND id != ?", userID, staffID, keepID),
		dateStr,
	).Delete(&models.Attendance{})
}

func countAttendanceStatus(userID uuid.UUID, dateStr, status string) int64 {
	var count int64
	attendanceOnDate(utils.DB.Model(&models.Attendance{}).Where("user_id = ? AND status = ?", userID, status), dateStr).
		Distinct("staff_id").
		Count(&count)
	return count
}

func GetAttendance(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	attendances := make([]models.Attendance, 0)
	query := utils.DB.Where("user_id = ?", userID)

	if staffID := c.Query("staff_id"); staffID != "" {
		query = query.Where("staff_id = ?", staffID)
	}

	startDate := strings.TrimSpace(c.Query("start_date"))
	endDate := strings.TrimSpace(c.Query("end_date"))
	if startDate != "" {
		if parsed, err := parseAttendanceDate(startDate); err == nil {
			startDate = parsed.Format("2006-01-02")
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid start_date. Use YYYY-MM-DD"})
			return
		}
	}
	if endDate != "" {
		if parsed, err := parseAttendanceDate(endDate); err == nil {
			endDate = parsed.Format("2006-01-02")
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid end_date. Use YYYY-MM-DD"})
			return
		}
	}
	query = attendanceBetweenDates(query, startDate, endDate)

	if err := query.Order("date DESC, created_at DESC").Find(&attendances).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch attendance"})
		return
	}

	c.JSON(http.StatusOK, attendances)
}

func GetAttendanceStats(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	date := strings.TrimSpace(c.Query("date"))

	if date == "" {
		date = time.Now().UTC().Format("2006-01-02")
	}
	parsedDate, err := parseAttendanceDate(date)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid date. Use YYYY-MM-DD"})
		return
	}
	date = parsedDate.Format("2006-01-02")

	var stats models.AttendanceStats
	stats.Date = date

	utils.DB.Model(&models.Staff{}).Where("user_id = ? AND is_active = ?", userID, true).Count(&stats.TotalStaff)

	stats.Present = countAttendanceStatus(userID, date, "present")
	stats.Absent = countAttendanceStatus(userID, date, "absent")
	stats.HalfDay = countAttendanceStatus(userID, date, "half_day")
	stats.PaidLeave = countAttendanceStatus(userID, date, "paid_leave")
	stats.WeeklyOff = countAttendanceStatus(userID, date, "weekly_off")

	c.JSON(http.StatusOK, stats)
}

func MarkAttendance(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		StaffID      string     `json:"staff_id"`
		Date         string     `json:"date"`
		Status       string     `json:"status"`
		CheckInTime  *time.Time `json:"check_in_time"`
		CheckOutTime *time.Time `json:"check_out_time"`
		WorkHours    float64    `json:"work_hours"`
		Notes        string     `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request data", "fields": gin.H{"_form": "Invalid request data"}})
		return
	}

	fields := map[string]string{}
	staffID, staffParseErr := uuid.Parse(strings.TrimSpace(input.StaffID))
	if strings.TrimSpace(input.StaffID) == "" {
		fields["staff_id"] = "Staff is required"
	} else if staffParseErr != nil {
		fields["staff_id"] = "Invalid staff selected"
	}
	if strings.TrimSpace(input.Date) == "" {
		fields["date"] = "Date is required"
	}
	status := strings.ToLower(strings.TrimSpace(input.Status))
	switch status {
	case "present", "absent", "half_day", "paid_leave", "weekly_off":
	case "":
		fields["status"] = "Status is required"
	default:
		fields["status"] = "Select a valid attendance status"
	}
	parsedDate, dateErr := parseAttendanceDate(input.Date)
	if strings.TrimSpace(input.Date) != "" && dateErr != nil {
		fields["date"] = "Enter a valid date"
	}
	if input.CheckInTime != nil && input.CheckOutTime != nil && !input.CheckOutTime.After(*input.CheckInTime) {
		fields["check_out_time"] = "Check-out time must be after check-in time"
	}
	if len(fields) > 0 {
		msg := "Please fix the highlighted fields"
		for _, v := range fields {
			msg = v
			break
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": msg, "fields": fields})
		return
	}

	var staff models.Staff
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, staffID).First(&staff).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  "Staff not found in the active store",
			"fields": gin.H{"staff_id": "Staff not found in the active store"},
		})
		return
	}

	workHours := resolveWorkHours(status, input.CheckInTime, input.CheckOutTime, input.WorkHours)
	dateStr := parsedDate.Format("2006-01-02")

	existingAttendance, err := findAttendanceForDay(userID, staffID, dateStr)
	if err == nil {
		updates := map[string]interface{}{
			"status":         status,
			"check_in_time":  input.CheckInTime,
			"check_out_time": input.CheckOutTime,
			"work_hours":     workHours,
			"notes":          input.Notes,
			"date":           parsedDate,
		}

		if err := utils.DB.Model(&existingAttendance).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update attendance"})
			return
		}

		removeDuplicateAttendance(userID, staffID, existingAttendance.ID, dateStr)
		utils.DB.First(&existingAttendance, "id = ?", existingAttendance.ID)
		c.JSON(http.StatusOK, existingAttendance)
		return
	}

	attendance := models.Attendance{
		ID:           uuid.New(),
		UserID:       userID,
		StaffID:      staffID,
		Date:         parsedDate,
		Status:       status,
		CheckInTime:  input.CheckInTime,
		CheckOutTime: input.CheckOutTime,
		WorkHours:    workHours,
		Notes:        input.Notes,
	}

	if err := utils.DB.Create(&attendance).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to mark attendance"})
		return
	}

	c.JSON(http.StatusCreated, attendance)
}

func BulkMarkAttendance(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		Date       string `json:"date"`
		Attendance []struct {
			StaffID      uuid.UUID  `json:"staff_id"`
			Status       string     `json:"status"`
			CheckInTime  *time.Time `json:"check_in_time"`
			CheckOutTime *time.Time `json:"check_out_time"`
			WorkHours    float64    `json:"work_hours"`
			Notes        string     `json:"notes"`
		} `json:"attendance"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request data", "fields": gin.H{"_form": "Invalid request data"}})
		return
	}

	parsedDate, dateErr := parseAttendanceDate(input.Date)
	if dateErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Enter a valid date", "fields": gin.H{"date": "Enter a valid date"}})
		return
	}
	if len(input.Attendance) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Select at least one staff member", "fields": gin.H{"attendance": "Select at least one staff member"}})
		return
	}

	attendances := make([]models.Attendance, 0, len(input.Attendance))
	dateStr := parsedDate.Format("2006-01-02")

	for _, att := range input.Attendance {
		if att.StaffID == uuid.Nil {
			continue
		}
		status := strings.ToLower(strings.TrimSpace(att.Status))
		switch status {
		case "present", "absent", "half_day", "paid_leave", "weekly_off":
		default:
			continue
		}

		var staff models.Staff
		if err := utils.DB.Where("user_id = ? AND id = ?", userID, att.StaffID).First(&staff).Error; err != nil {
			continue
		}

		workHours := resolveWorkHours(status, att.CheckInTime, att.CheckOutTime, att.WorkHours)

		existingAttendance, err := findAttendanceForDay(userID, att.StaffID, dateStr)
		if err == nil {
			updates := map[string]interface{}{
				"status":         status,
				"check_in_time":  att.CheckInTime,
				"check_out_time": att.CheckOutTime,
				"work_hours":     workHours,
				"notes":          att.Notes,
				"date":           parsedDate,
			}
			utils.DB.Model(&existingAttendance).Updates(updates)
			removeDuplicateAttendance(userID, att.StaffID, existingAttendance.ID, dateStr)
			utils.DB.First(&existingAttendance, "id = ?", existingAttendance.ID)
			attendances = append(attendances, existingAttendance)
		} else {
			attendance := models.Attendance{
				ID:           uuid.New(),
				UserID:       userID,
				StaffID:      att.StaffID,
				Date:         parsedDate,
				Status:       status,
				CheckInTime:  att.CheckInTime,
				CheckOutTime: att.CheckOutTime,
				WorkHours:    workHours,
				Notes:        att.Notes,
			}
			utils.DB.Create(&attendance)
			attendances = append(attendances, attendance)
		}
	}

	c.JSON(http.StatusOK, attendances)
}

func GetStaffAttendance(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	staffID := c.Param("staff_id")

	attendances := make([]models.Attendance, 0)
	query := utils.DB.Where("user_id = ? AND staff_id = ?", userID, staffID)

	startDate := strings.TrimSpace(c.Query("start_date"))
	endDate := strings.TrimSpace(c.Query("end_date"))
	if startDate != "" {
		if parsed, err := parseAttendanceDate(startDate); err == nil {
			startDate = parsed.Format("2006-01-02")
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid start_date. Use YYYY-MM-DD"})
			return
		}
	}
	if endDate != "" {
		if parsed, err := parseAttendanceDate(endDate); err == nil {
			endDate = parsed.Format("2006-01-02")
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid end_date. Use YYYY-MM-DD"})
			return
		}
	}
	query = attendanceBetweenDates(query, startDate, endDate)

	if err := query.Order("date DESC").Find(&attendances).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch staff attendance"})
		return
	}

	c.JSON(http.StatusOK, attendances)
}

func DeleteAttendance(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var attendance models.Attendance
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&attendance).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Attendance not found"})
		return
	}

	if err := utils.DB.Delete(&attendance).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete attendance"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Attendance deleted successfully"})
}

func BulkDeleteAttendance(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		IDs []uuid.UUID `json:"ids" binding:"required"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	result := utils.DB.Where("user_id = ? AND id IN ?", userID, input.IDs).Delete(&models.Attendance{})
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete attendance"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Attendance deleted successfully",
		"deleted": result.RowsAffected,
	})
}
