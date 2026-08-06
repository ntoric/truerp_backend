package controllers

import (
	"truerp/models"
	"truerp/utils"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func GetPOSSessions(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var sessions []models.POSSession
	query := utils.DB.Where("user_id = ?", userID)

	if status := c.Query("status"); status != "" {
		query = query.Where("status = ?", status)
	}

	if fromDate := c.Query("from_date"); fromDate != "" {
		if parsed, err := time.Parse("2006-01-02", fromDate); err == nil {
			query = query.Where("opened_at >= ?", parsed)
		} else {
			query = query.Where("opened_at >= ?", fromDate)
		}
	}
	if toDate := c.Query("to_date"); toDate != "" {
		if parsed, err := time.Parse("2006-01-02", toDate); err == nil {
			endOfDay := parsed.Add(24*time.Hour - time.Nanosecond)
			query = query.Where("opened_at <= ?", endOfDay)
		} else {
			query = query.Where("opened_at <= ?", toDate)
		}
	}

	if err := query.Order("opened_at DESC").Find(&sessions).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch sessions"})
		return
	}

	c.JSON(http.StatusOK, sessions)
}

func GetActivePOSSession(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var session models.POSSession
	if err := utils.DB.Where("user_id = ? AND status = ?", userID, "open").First(&session).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "No active session"})
		return
	}

	c.JSON(http.StatusOK, session)
}

func GetPOSSession(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var session models.POSSession
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&session).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Session not found"})
		return
	}

	c.JSON(http.StatusOK, session)
}

func OpenPOSSession(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		OutletID    uuid.UUID `json:"outlet_id"`
		OpeningCash float64   `json:"opening_cash"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var activeSession models.POSSession
	if err := utils.DB.Where("user_id = ? AND status = ?", userID, "open").First(&activeSession).Error; err == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "An active session already exists"})
		return
	}

	session := models.POSSession{
		ID:          uuid.New(),
		UserID:      userID,
		CashierID:   userID,
		OutletID:    input.OutletID,
		Status:      "open",
		OpeningCash: input.OpeningCash,
		OpenedAt:    time.Now(),
	}

	if err := utils.DB.Create(&session).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to open session"})
		return
	}

	c.JSON(http.StatusCreated, session)
}

func ClosePOSSession(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var input struct {
		ClosingCash float64  `json:"closing_cash"`
		Notes       string   `json:"notes"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var session models.POSSession
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&session).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Session not found"})
		return
	}

	if session.Status == "closed" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Session already closed"})
		return
	}

	now := time.Now()
	session.Status = "closed"
	session.ClosingCash = input.ClosingCash
	session.Notes = input.Notes
	session.ClosedAt = &now

	if err := utils.DB.Save(&session).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to close session"})
		return
	}

	c.JSON(http.StatusOK, session)
}

func GetPOSSessionSummary(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var session models.POSSession
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&session).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Session not found"})
		return
	}

	var invoiceCount int64
	utils.DB.Model(&models.Invoice{}).Where("user_id = ? AND created_at >= ? AND created_at <= ?", 
		userID, session.OpenedAt, session.ClosedAt).Count(&invoiceCount)

	c.JSON(http.StatusOK, gin.H{
		"session":           session,
		"total_invoices":    invoiceCount,
		"total_sales":       session.TotalSales,
		"total_returns":     session.TotalReturns,
		"payment_breakdown": gin.H{"cash": session.TotalSales - session.CashOutTotal + session.CashInTotal},
	})
}

func GetCashMovements(c *gin.Context) {
	sessionID := c.Param("id")

	var movements []models.CashMovement
	if err := utils.DB.Where("session_id = ?", sessionID).Order("created_at DESC").Find(&movements).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch cash movements"})
		return
	}

	c.JSON(http.StatusOK, movements)
}

func AddCashMovement(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	sessionID := c.Param("id")

	var input struct {
		Type   string  `json:"type" binding:"required,oneof=cash_in cash_out"`
		Amount float64 `json:"amount" binding:"required,gt=0"`
		Reason string  `json:"reason"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var session models.POSSession
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, sessionID).First(&session).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Session not found"})
		return
	}

	if session.Status != "open" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Session is closed"})
		return
	}

	movement := models.CashMovement{
		ID:        uuid.New(),
		SessionID: uuid.MustParse(sessionID),
		Type:      input.Type,
		Amount:    input.Amount,
		Reason:    input.Reason,
	}

	if err := utils.DB.Create(&movement).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to record movement"})
		return
	}

	if input.Type == "cash_in" {
		session.CashInTotal += input.Amount
	} else {
		session.CashOutTotal += input.Amount
	}
	utils.DB.Save(&session)

	c.JSON(http.StatusCreated, movement)
}

func GetNextPOSSessionNumber(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var count int64
	utils.DB.Model(&models.POSSession{}).Where("user_id = ?", userID).Count(&count)

	nextNum := fmt.Sprintf("POS-%04d", count+1)
	c.JSON(http.StatusOK, gin.H{"session_number": nextNum})
}
