package controllers

import (
	"fmt"
	"net/http"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Partner Controllers

func GetPartners(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var partners []models.Partner
	query := utils.DB.Where("user_id = ?", userID)

	if active := c.Query("is_active"); active != "" {
		query = query.Where("is_active = ?", active == "true")
	}
	if search := c.Query("search"); search != "" {
		like := "%" + strings.ToLower(search) + "%"
		query = query.Where("LOWER(name) LIKE ? OR LOWER(phone) LIKE ? OR LOWER(email) LIKE ?", like, like, like)
	}

	if err := query.Order("name ASC").Find(&partners).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch partners"})
		return
	}

	c.JSON(http.StatusOK, partners)
}

func GetPartner(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var partner models.Partner
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&partner).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Partner not found"})
		return
	}

	c.JSON(http.StatusOK, partner)
}

func CreatePartner(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		Name    string `json:"name" binding:"required"`
		Phone   string `json:"phone"`
		Email   string `json:"email"`
		Address string `json:"address"`
		PAN     string `json:"pan"`
		Notes   string `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	partner := models.Partner{
		ID:       uuid.New(),
		UserID:   userID,
		Name:     input.Name,
		Phone:    input.Phone,
		Email:    input.Email,
		Address:  input.Address,
		PAN:      input.PAN,
		Notes:    input.Notes,
		IsActive: true,
	}

	if err := utils.DB.Create(&partner).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create partner"})
		return
	}

	c.JSON(http.StatusCreated, partner)
}

func UpdatePartner(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var input struct {
		Name     string `json:"name"`
		Phone    string `json:"phone"`
		Email    string `json:"email"`
		Address  string `json:"address"`
		PAN      string `json:"pan"`
		Notes    string `json:"notes"`
		IsActive *bool  `json:"is_active"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var partner models.Partner
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&partner).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Partner not found"})
		return
	}

	updates := map[string]interface{}{
		"name":    input.Name,
		"phone":   input.Phone,
		"email":   input.Email,
		"address": input.Address,
		"pan":     input.PAN,
		"notes":   input.Notes,
	}
	if input.IsActive != nil {
		updates["is_active"] = *input.IsActive
	}

	if err := utils.DB.Model(&partner).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update partner"})
		return
	}

	c.JSON(http.StatusOK, partner)
}

func DeletePartner(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var partner models.Partner
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&partner).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Partner not found"})
		return
	}

	var count int64
	utils.DB.Model(&models.ProfitDistribution{}).Where("user_id = ? AND partner_id = ?", userID, partner.ID).Count(&count)
	if count > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot delete partner with existing profit distributions"})
		return
	}

	if err := utils.DB.Delete(&partner).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete partner"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Partner deleted successfully"})
}

// Profit Distribution Controllers

func GetProfitDistributions(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var distributions []models.ProfitDistribution
	query := utils.DB.Where("user_id = ?", userID).Preload("Partner").Preload("Account")

	if partnerID := c.Query("partner_id"); partnerID != "" {
		query = query.Where("partner_id = ?", partnerID)
	}
	if startDate := c.Query("start_date"); startDate != "" {
		if parsed, err := time.Parse("2006-01-02", startDate); err == nil {
			query = query.Where("date >= ?", parsed)
		}
	}
	if endDate := c.Query("end_date"); endDate != "" {
		if parsed, err := time.Parse("2006-01-02", endDate); err == nil {
			query = query.Where("date <= ?", parsed)
		}
	}

	if err := query.Order("date DESC, created_at DESC").Find(&distributions).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch profit distributions"})
		return
	}

	c.JSON(http.StatusOK, distributions)
}

func GetProfitDistribution(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var distribution models.ProfitDistribution
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).
		Preload("Partner").Preload("Account").First(&distribution).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Profit distribution not found"})
		return
	}

	c.JSON(http.StatusOK, distribution)
}

func CreateProfitDistribution(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	userName := ""
	if name, exists := c.Get("user_name"); exists {
		userName = name.(string)
	}

	var input struct {
		PartnerID   uuid.UUID  `json:"partner_id" binding:"required"`
		Amount      float64    `json:"amount" binding:"required,gt=0"`
		Date        time.Time  `json:"date" binding:"required"`
		AccountID   *uuid.UUID `json:"account_id"` // nil = cash in-hand
		Description string     `json:"description"`
		Reference   string     `json:"reference"`
		Notes       string     `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var partner models.Partner
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, input.PartnerID).First(&partner).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Partner not found"})
		return
	}

	var account *models.BankAccount
	if input.AccountID != nil {
		var acc models.BankAccount
		if err := utils.DB.Where("user_id = ? AND id = ? AND is_active = ?", userID, *input.AccountID, true).First(&acc).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Account not found"})
			return
		}
		if acc.Balance < input.Amount {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Insufficient balance in selected account"})
			return
		}
		account = &acc
	}

	var count int64
	utils.DB.Model(&models.ProfitDistribution{}).Where("user_id = ?", userID).Count(&count)
	distributionNumber := fmt.Sprintf("PD-%04d", count+1)

	description := input.Description
	if description == "" {
		description = fmt.Sprintf("Profit distribution to %s", partner.Name)
	}

	distribution := models.ProfitDistribution{
		ID:                 uuid.New(),
		UserID:             userID,
		PartnerID:          input.PartnerID,
		DistributionNumber: distributionNumber,
		Amount:             input.Amount,
		Date:               input.Date,
		AccountID:          input.AccountID,
		Description:        description,
		Reference:          input.Reference,
		Notes:              input.Notes,
	}

	tx := utils.DB.Begin()

	// Deduct from the selected bank account (nil account = cash in-hand).
	if account != nil {
		account.Balance -= input.Amount
		if err := tx.Save(account).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update account balance"})
			return
		}
	}

	cashTxn := models.CashTransaction{
		ID:              uuid.New(),
		UserID:          userID,
		AccountID:       input.AccountID,
		TransactionType: "profit_distribution",
		Amount:          input.Amount,
		Date:            input.Date,
		Description:     description,
		Reference:       distributionNumber,
		IsLinked:        true,
	}
	if err := tx.Create(&cashTxn).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to record transaction"})
		return
	}

	if err := tx.Create(&distribution).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create profit distribution"})
		return
	}

	// GL: profit distribution reduces owner's equity and cash/bank.
	assetCode := acCodeCash
	if input.AccountID != nil {
		assetCode = acCodeBank
	}
	if err := postAutoJournal(tx, userID, input.Date, description, "profit_distribution", distribution.ID, distributionNumber, []glLine{
		{AccountCode: acCodeEquity, Debit: input.Amount, Description: description},
		{AccountCode: assetCode, Credit: input.Amount, Description: description},
	}); err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to post accounting entry"})
		return
	}

	tx.Commit()

	utils.DB.Where("id = ?", distribution.ID).Preload("Partner").Preload("Account").First(&distribution)

	CreateAuditLog(
		userID,
		userName,
		"create",
		"profit_distribution",
		&distribution.ID,
		distributionNumber,
		fmt.Sprintf("Created profit distribution: %s - %.2f to %s", distributionNumber, input.Amount, partner.Name),
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		map[string]interface{}{
			"partner_id": input.PartnerID,
			"amount":     input.Amount,
			"account_id": input.AccountID,
		},
		"success",
		"",
	)

	c.JSON(http.StatusCreated, distribution)
}

func DeleteProfitDistribution(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	userName := ""
	if name, exists := c.Get("user_name"); exists {
		userName = name.(string)
	}
	id := c.Param("id")

	var distribution models.ProfitDistribution
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&distribution).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Profit distribution not found"})
		return
	}

	tx := utils.DB.Begin()

	// Restore the bank account balance.
	if distribution.AccountID != nil {
		var account models.BankAccount
		if err := tx.Where("user_id = ? AND id = ?", userID, *distribution.AccountID).First(&account).Error; err == nil {
			account.Balance += distribution.Amount
			if err := tx.Save(&account).Error; err != nil {
				tx.Rollback()
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to restore account balance"})
				return
			}
		}
	}

	// Remove the linked cash-bank transaction.
	if err := tx.Where(
		"user_id = ? AND reference = ? AND transaction_type = ?",
		userID, distribution.DistributionNumber, "profit_distribution",
	).Delete(&models.CashTransaction{}).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to remove transaction record"})
		return
	}

	if err := reverseAccountingByRef(tx, userID, "profit_distribution", distribution.ID); err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reverse accounting entry"})
		return
	}

	if err := tx.Delete(&distribution).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete profit distribution"})
		return
	}

	tx.Commit()

	CreateAuditLog(
		userID,
		userName,
		"delete",
		"profit_distribution",
		&distribution.ID,
		distribution.DistributionNumber,
		fmt.Sprintf("Deleted profit distribution: %s", distribution.DistributionNumber),
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		map[string]interface{}{
			"amount": distribution.Amount,
		},
		"success",
		"",
	)

	c.JSON(http.StatusOK, gin.H{"message": "Profit distribution deleted successfully"})
}

func GetNextDistributionNumber(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var count int64
	utils.DB.Model(&models.ProfitDistribution{}).Where("user_id = ?", userID).Count(&count)

	c.JSON(http.StatusOK, gin.H{"distribution_number": fmt.Sprintf("PD-%04d", count+1)})
}
