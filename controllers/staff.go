package controllers

import (
	"truerp/models"
	"truerp/utils"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func GetStaffs(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var staffs []models.Staff
	query := utils.DB.Where("user_id = ?", userID)

	if search := c.Query("search"); search != "" {
		query = query.Where("name LIKE ? OR phone LIKE ? OR email LIKE ? OR designation LIKE ?", "%"+search+"%", "%"+search+"%", "%"+search+"%", "%"+search+"%")
	}

	if err := query.Order("name ASC").Find(&staffs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch staffs"})
		return
	}

	c.JSON(http.StatusOK, staffs)
}

func GetStaff(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var staff models.Staff
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&staff).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Staff not found"})
		return
	}

	c.JSON(http.StatusOK, staff)
}

func CreateStaff(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input models.Staff
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	staff := models.Staff{
		ID:            uuid.New(),
		UserID:        userID,
		Name:          input.Name,
		Phone:         input.Phone,
		Email:         input.Email,
		Address:       input.Address,
		Designation:   input.Designation,
		Department:    input.Department,
		JoiningDate:   input.JoiningDate,
		Salary:        input.Salary,
		SalaryType:    input.SalaryType,
		BankName:      input.BankName,
		AccountNumber: input.AccountNumber,
		IFSCCode:      input.IFSCCode,
		AadharNumber:  input.AadharNumber,
		PANNumber:     input.PANNumber,
		IsActive:      true,
		Notes:         input.Notes,
	}

	if err := utils.DB.Create(&staff).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create staff"})
		return
	}

	c.JSON(http.StatusCreated, staff)
}

func UpdateStaff(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var input models.Staff
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var staff models.Staff
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&staff).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Staff not found"})
		return
	}

	updates := map[string]interface{}{
		"name":           input.Name,
		"phone":          input.Phone,
		"email":          input.Email,
		"address":        input.Address,
		"designation":    input.Designation,
		"department":     input.Department,
		"joining_date":   input.JoiningDate,
		"salary":         input.Salary,
		"salary_type":    input.SalaryType,
		"bank_name":      input.BankName,
		"account_number": input.AccountNumber,
		"ifsc_code":      input.IFSCCode,
		"aadhar_number":  input.AadharNumber,
		"pan_number":     input.PANNumber,
		"is_active":      input.IsActive,
		"notes":          input.Notes,
	}

	if err := utils.DB.Model(&staff).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update staff"})
		return
	}

	c.JSON(http.StatusOK, staff)
}

func DeleteStaff(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var staff models.Staff
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&staff).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Staff not found"})
		return
	}

	if err := utils.DB.Delete(&staff).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete staff"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Staff deleted successfully"})
}

func BulkDeleteStaff(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		IDs []uuid.UUID `json:"ids" binding:"required"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	result := utils.DB.Where("user_id = ? AND id IN ?", userID, input.IDs).Delete(&models.Staff{})
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete staff"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Staff deleted successfully",
		"deleted": result.RowsAffected,
	})
}

func BulkUpdateStaffStatus(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		IDs      []uuid.UUID `json:"ids" binding:"required"`
		IsActive bool        `json:"is_active"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	result := utils.DB.Model(&models.Staff{}).
		Where("user_id = ? AND id IN ?", userID, input.IDs).
		Update("is_active", input.IsActive)
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update staff status"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Staff status updated successfully",
		"updated": result.RowsAffected,
	})
}
