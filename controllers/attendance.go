package controllers

import (
	"truerp/models"
	"truerp/utils"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func GetAttendance(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var attendances []models.Attendance
	query := utils.DB.Where("user_id = ?", userID)

	if staffID := c.Query("staff_id"); staffID != "" {
		query = query.Where("staff_id = ?", staffID)
	}

	if startDate := c.Query("start_date"); startDate != "" {
		query = query.Where("date >= ?", startDate)
	}

	if endDate := c.Query("end_date"); endDate != "" {
		query = query.Where("date <= ?", endDate)
	}

	if err := query.Order("date DESC, created_at DESC").Find(&attendances).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch attendance"})
		return
	}

	c.JSON(http.StatusOK, attendances)
}

func GetAttendanceStats(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	date := c.Query("date")
	
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}

	var stats models.AttendanceStats
	stats.Date = date

	// Get total active staff
	utils.DB.Model(&models.Staff{}).Where("user_id = ? AND is_active = ?", userID, true).Count(&stats.TotalStaff)

	// Count attendance by status for the given date
	utils.DB.Model(&models.Attendance{}).Where("user_id = ? AND date = ?", userID, date).Where("status = ?", "present").Count(&stats.Present)
	utils.DB.Model(&models.Attendance{}).Where("user_id = ? AND date = ?", userID, date).Where("status = ?", "absent").Count(&stats.Absent)
	utils.DB.Model(&models.Attendance{}).Where("user_id = ? AND date = ?", userID, date).Where("status = ?", "half_day").Count(&stats.HalfDay)
	utils.DB.Model(&models.Attendance{}).Where("user_id = ? AND date = ?", userID, date).Where("status = ?", "paid_leave").Count(&stats.PaidLeave)
	utils.DB.Model(&models.Attendance{}).Where("user_id = ? AND date = ?", userID, date).Where("status = ?", "weekly_off").Count(&stats.WeeklyOff)

	c.JSON(http.StatusOK, stats)
}

func MarkAttendance(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		StaffID      uuid.UUID  `json:"staff_id" binding:"required"`
		Date         time.Time  `json:"date" binding:"required"`
		Status       string     `json:"status" binding:"required"`
		CheckInTime  *time.Time `json:"check_in_time"`
		CheckOutTime *time.Time `json:"check_out_time"`
		WorkHours    float64    `json:"work_hours"`
		Notes        string     `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Check if attendance already exists for this staff and date
	var existingAttendance models.Attendance
	err := utils.DB.Where("user_id = ? AND staff_id = ? AND date = ?", userID, input.StaffID, input.Date.Format("2006-01-02")).First(&existingAttendance).Error

	if err == nil {
		// Update existing attendance
		updates := map[string]interface{}{
			"status":        input.Status,
			"check_in_time": input.CheckInTime,
			"check_out_time": input.CheckOutTime,
			"work_hours":    input.WorkHours,
			"notes":         input.Notes,
		}

		if err := utils.DB.Model(&existingAttendance).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update attendance"})
			return
		}

		c.JSON(http.StatusOK, existingAttendance)
		return
	}

	// Create new attendance
	attendance := models.Attendance{
		ID:           uuid.New(),
		UserID:       userID,
		StaffID:      input.StaffID,
		Date:         input.Date,
		Status:       input.Status,
		CheckInTime:  input.CheckInTime,
		CheckOutTime: input.CheckOutTime,
		WorkHours:    input.WorkHours,
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
		Date         time.Time `json:"date" binding:"required"`
		Attendance   []struct {
			StaffID      uuid.UUID  `json:"staff_id" binding:"required"`
			Status       string     `json:"status" binding:"required"`
			CheckInTime  *time.Time `json:"check_in_time"`
			CheckOutTime *time.Time `json:"check_out_time"`
			WorkHours    float64    `json:"work_hours"`
			Notes        string     `json:"notes"`
		} `json:"attendance" binding:"required"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var attendances []models.Attendance
	dateStr := input.Date.Format("2006-01-02")

	for _, att := range input.Attendance {
		// Check if attendance already exists
		var existingAttendance models.Attendance
		err := utils.DB.Where("user_id = ? AND staff_id = ? AND date = ?", userID, att.StaffID, dateStr).First(&existingAttendance).Error

		if err == nil {
			// Update existing
			updates := map[string]interface{}{
				"status":        att.Status,
				"check_in_time": att.CheckInTime,
				"check_out_time": att.CheckOutTime,
				"work_hours":    att.WorkHours,
				"notes":         att.Notes,
			}
			utils.DB.Model(&existingAttendance).Updates(updates)
			attendances = append(attendances, existingAttendance)
		} else {
			// Create new
			attendance := models.Attendance{
				ID:           uuid.New(),
				UserID:       userID,
				StaffID:      att.StaffID,
				Date:         input.Date,
				Status:       att.Status,
				CheckInTime:  att.CheckInTime,
				CheckOutTime: att.CheckOutTime,
				WorkHours:    att.WorkHours,
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

	var attendances []models.Attendance
	query := utils.DB.Where("user_id = ? AND staff_id = ?", userID, staffID)

	if startDate := c.Query("start_date"); startDate != "" {
		query = query.Where("date >= ?", startDate)
	}

	if endDate := c.Query("end_date"); endDate != "" {
		query = query.Where("date <= ?", endDate)
	}

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
