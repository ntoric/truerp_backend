package controllers

import (
	"net/http"
	"strings"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func GetWarehouses(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var warehouses []models.Warehouse
	query := utils.DB.Where("user_id = ?", userID)

	if isActive := c.Query("is_active"); isActive != "" {
		query = query.Where("is_active = ?", isActive == "true")
	}

	if err := query.Order("is_default DESC, name ASC").Find(&warehouses).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch warehouses"})
		return
	}

	c.JSON(http.StatusOK, warehouses)
}

func GetWarehouse(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var warehouse models.Warehouse
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&warehouse).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Warehouse not found"})
		return
	}

	c.JSON(http.StatusOK, warehouse)
}

func CreateWarehouse(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		Name          string `json:"name" binding:"required"`
		Code          string `json:"code" binding:"required"`
		Address       string `json:"address"`
		City          string `json:"city"`
		State         string `json:"state"`
		Pincode       string `json:"pincode"`
		ContactPerson string `json:"contact_person"`
		ContactPhone  string `json:"contact_phone"`
		ContactEmail  string `json:"contact_email"`
		IsDefault     bool   `json:"is_default"`
		Notes         string `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	name := strings.TrimSpace(input.Name)
	code := strings.ToUpper(strings.TrimSpace(input.Code))
	if name == "" || code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Warehouse name and code are required"})
		return
	}

	var existing models.Warehouse
	if err := utils.DB.Where("user_id = ? AND code = ?", userID, code).First(&existing).Error; err == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "A warehouse with this code already exists"})
		return
	} else if err != gorm.ErrRecordNotFound {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create warehouse"})
		return
	}

	// Soft-deleted rows still occupy the unique (user_id, code) slot — purge them
	// so the same code can be reused after delete.
	utils.DB.Unscoped().
		Where("user_id = ? AND code = ? AND deleted_at IS NOT NULL", userID, code).
		Delete(&models.Warehouse{})

	// If setting as default, remove default from other warehouses
	if input.IsDefault {
		utils.DB.Model(&models.Warehouse{}).Where("user_id = ?", userID).Update("is_default", false)
	}

	warehouse := models.Warehouse{
		ID:            uuid.New(),
		UserID:        userID,
		Name:          name,
		Code:          code,
		Address:       strings.TrimSpace(input.Address),
		City:          strings.TrimSpace(input.City),
		State:         strings.TrimSpace(input.State),
		Pincode:       strings.TrimSpace(input.Pincode),
		ContactPerson: strings.TrimSpace(input.ContactPerson),
		ContactPhone:  strings.TrimSpace(input.ContactPhone),
		ContactEmail:  strings.TrimSpace(input.ContactEmail),
		IsActive:      true,
		IsDefault:     input.IsDefault,
		Notes:         strings.TrimSpace(input.Notes),
	}

	if err := utils.DB.Create(&warehouse).Error; err != nil {
		if isUniqueConstraintError(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "A warehouse with this code already exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create warehouse"})
		return
	}

	c.JSON(http.StatusCreated, warehouse)
}

func UpdateWarehouse(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var input struct {
		Name          string `json:"name"`
		Code          string `json:"code"`
		Address       string `json:"address"`
		City          string `json:"city"`
		State         string `json:"state"`
		Pincode       string `json:"pincode"`
		ContactPerson string `json:"contact_person"`
		ContactPhone  string `json:"contact_phone"`
		ContactEmail  string `json:"contact_email"`
		IsActive      *bool  `json:"is_active"`
		IsDefault     *bool  `json:"is_default"`
		Notes         string `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var warehouse models.Warehouse
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&warehouse).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Warehouse not found"})
		return
	}

	name := strings.TrimSpace(input.Name)
	code := strings.ToUpper(strings.TrimSpace(input.Code))
	if name == "" || code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Warehouse name and code are required"})
		return
	}

	if !strings.EqualFold(warehouse.Code, code) {
		var existing models.Warehouse
		if err := utils.DB.Where("user_id = ? AND code = ? AND id != ?", userID, code, warehouse.ID).First(&existing).Error; err == nil {
			c.JSON(http.StatusConflict, gin.H{"error": "A warehouse with this code already exists"})
			return
		} else if err != gorm.ErrRecordNotFound {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update warehouse"})
			return
		}
		utils.DB.Unscoped().
			Where("user_id = ? AND code = ? AND id != ? AND deleted_at IS NOT NULL", userID, code, warehouse.ID).
			Delete(&models.Warehouse{})
	}

	// If setting as default, remove default from other warehouses
	if input.IsDefault != nil && *input.IsDefault {
		utils.DB.Model(&models.Warehouse{}).Where("user_id = ? AND id != ?", userID, id).Update("is_default", false)
	}

	updates := map[string]interface{}{
		"name":           name,
		"code":           code,
		"address":        strings.TrimSpace(input.Address),
		"city":           strings.TrimSpace(input.City),
		"state":          strings.TrimSpace(input.State),
		"pincode":        strings.TrimSpace(input.Pincode),
		"contact_person": strings.TrimSpace(input.ContactPerson),
		"contact_phone":  strings.TrimSpace(input.ContactPhone),
		"contact_email":  strings.TrimSpace(input.ContactEmail),
		"notes":          strings.TrimSpace(input.Notes),
	}

	if input.IsActive != nil {
		updates["is_active"] = *input.IsActive
	}
	if input.IsDefault != nil {
		updates["is_default"] = *input.IsDefault
	}

	if err := utils.DB.Model(&warehouse).Updates(updates).Error; err != nil {
		if isUniqueConstraintError(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "A warehouse with this code already exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update warehouse"})
		return
	}

	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&warehouse).Error; err != nil {
		c.JSON(http.StatusOK, warehouse)
		return
	}

	c.JSON(http.StatusOK, warehouse)
}

func DeleteWarehouse(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var warehouse models.Warehouse
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&warehouse).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Warehouse not found"})
		return
	}

	if warehouse.IsDefault {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot delete default warehouse"})
		return
	}

	if err := utils.DB.Delete(&warehouse).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete warehouse"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Warehouse deleted successfully"})
}

func BulkDeleteWarehouses(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		IDs []uuid.UUID `json:"ids" binding:"required"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	result := utils.DB.Where("user_id = ? AND id IN ? AND is_default = ?", userID, input.IDs, false).Delete(&models.Warehouse{})
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete warehouses"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Warehouses deleted successfully",
		"deleted": result.RowsAffected,
	})
}

func BulkUpdateWarehouseStatus(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		IDs      []uuid.UUID `json:"ids" binding:"required"`
		IsActive bool        `json:"is_active"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var warehouses []models.Warehouse
	if err := utils.DB.Where("user_id = ? AND id IN ?", userID, input.IDs).Find(&warehouses).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch warehouses"})
		return
	}

	if len(warehouses) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "No warehouses found"})
		return
	}

	if err := utils.DB.Model(&models.Warehouse{}).
		Where("user_id = ? AND id IN ?", userID, input.IDs).
		Update("is_active", input.IsActive).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update warehouses"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Warehouses updated successfully"})
}

func GetWarehouseStock(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	warehouseID := c.Param("id")

	type StockItem struct {
		ItemName  string  `json:"item_name"`
		StockQty  float64 `json:"stock_qty"`
		CostPrice float64 `json:"cost_price"`
		Value     float64 `json:"value"`
	}

	var results []StockItem
	query := `
		SELECT item_name, SUM(quantity) as stock_qty, AVG(cost_price) as cost_price
		FROM stock_entries
		WHERE user_id = ? AND outlet_id = ?
		GROUP BY item_name
	`

	if err := utils.DB.Raw(query, userID, warehouseID).Scan(&results).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch warehouse stock"})
		return
	}

	var totalValue float64
	for i := range results {
		results[i].Value = results[i].StockQty * results[i].CostPrice
		totalValue += results[i].Value
	}

	c.JSON(http.StatusOK, gin.H{
		"items":       results,
		"total_value": totalValue,
	})
}

func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "uniqueconstraint") ||
		strings.Contains(msg, "duplicate key") ||
		strings.Contains(msg, "idx_warehouses_user_code") ||
		strings.Contains(msg, "idx_warehouses_code")
}
