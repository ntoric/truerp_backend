package controllers

import (
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

var supportRequestStatuses = map[string]bool{
	"open":        true,
	"in_progress": true,
	"resolved":    true,
	"closed":      true,
}

var supportRequestCategories = map[string]bool{
	"general":         true,
	"billing":         true,
	"bug":             true,
	"feature_request": true,
	"account":         true,
}

const (
	defaultSupportEmail    = "support@runerail.com"
	defaultSupportWhatsApp = "918606490227"
)

// getOrCreateSupportContactSettings returns the singleton contact-settings row,
// creating one with the default channels if none exists yet.
func getOrCreateSupportContactSettings() (models.SupportContactSettings, error) {
	var settings models.SupportContactSettings
	if err := utils.DB.Order("created_at asc").First(&settings).Error; err == nil {
		return settings, nil
	}
	settings = models.SupportContactSettings{
		ID:             uuid.New(),
		SupportEmail:   defaultSupportEmail,
		WhatsAppNumber: defaultSupportWhatsApp,
	}
	if err := utils.DB.Create(&settings).Error; err != nil {
		return settings, err
	}
	return settings, nil
}

// GetSupportContactSettings returns the public support channels for the
// Support page. Any signed-in user may read them.
func GetSupportContactSettings(c *gin.Context) {
	settings, err := getOrCreateSupportContactSettings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load support settings"})
		return
	}
	c.JSON(http.StatusOK, settings)
}

// UpdateSupportContactSettings updates the public support channels (super admin).
func UpdateSupportContactSettings(c *gin.Context) {
	var input struct {
		SupportEmail   string `json:"support_email"`
		WhatsAppNumber string `json:"whatsapp_number"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	email := strings.TrimSpace(input.SupportEmail)
	if email == "" || !strings.Contains(email, "@") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "A valid support email is required"})
		return
	}
	whatsapp := strings.TrimSpace(input.WhatsAppNumber)
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, whatsapp)
	if len(digits) < 8 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "A valid WhatsApp number (with country code) is required"})
		return
	}

	settings, err := getOrCreateSupportContactSettings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load support settings"})
		return
	}
	settings.SupportEmail = email
	settings.WhatsAppNumber = whatsapp
	if err := utils.DB.Save(&settings).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save support settings"})
		return
	}
	c.JSON(http.StatusOK, settings)
}

func isSuperAdminCtx(c *gin.Context) bool {
	role, _ := c.Get("role")
	roleStr, _ := role.(string)
	return utils.IsSuperAdminRole(roleStr)
}

func preloadSupportRequest(db *gorm.DB) *gorm.DB {
	return db.
		Preload("Requester").
		Preload("Store").
		Preload("Replies", func(d *gorm.DB) *gorm.DB { return d.Order("created_at ASC") }).
		Preload("Replies.Author")
}

func loadSupportRequest(c *gin.Context, id string) (models.SupportRequest, bool) {
	var req models.SupportRequest
	if err := preloadSupportRequest(utils.DB).First(&req, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Support request not found"})
		return req, false
	}
	if !isSuperAdminCtx(c) && req.RequesterID != actorUserID(c) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Support request not found"})
		return req, false
	}
	return req, true
}

// CreateSupportRequest stores a new help request from the signed-in user.
func CreateSupportRequest(c *gin.Context) {
	var input struct {
		Subject  string `json:"subject" binding:"required"`
		Message  string `json:"message" binding:"required"`
		Category string `json:"category"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	subject := strings.TrimSpace(input.Subject)
	message := strings.TrimSpace(input.Message)
	if subject == "" || message == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Subject and message are required"})
		return
	}
	category := strings.TrimSpace(input.Category)
	if category == "" {
		category = "general"
	}
	if !supportRequestCategories[category] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid category"})
		return
	}

	var count int64
	utils.DB.Model(&models.SupportRequest{}).Count(&count)

	req := models.SupportRequest{
		ID:            uuid.New(),
		RequestNumber: fmt.Sprintf("SR-%05d", count+1),
		OwnerUserID:   c.MustGet("user_id").(uuid.UUID),
		RequesterID:   actorUserID(c),
		Subject:       subject,
		Category:      category,
		Message:       message,
		Status:        "open",
	}
	if storeID, ok := currentStoreID(c); ok {
		req.StoreID = &storeID
	}

	if err := utils.DB.Create(&req).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create support request"})
		return
	}
	preloadSupportRequest(utils.DB).First(&req, "id = ?", req.ID)
	c.JSON(http.StatusCreated, req)
}

// ListMySupportRequests returns the requests created by the signed-in user.
func ListMySupportRequests(c *gin.Context) {
	var requests []models.SupportRequest
	if err := preloadSupportRequest(utils.DB).
		Where("requester_id = ?", actorUserID(c)).
		Order("created_at DESC").
		Find(&requests).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load support requests"})
		return
	}
	c.JSON(http.StatusOK, requests)
}

// GetSupportRequest returns a single request with its reply thread. The
// requester and super admins may view it.
func GetSupportRequest(c *gin.Context) {
	req, ok := loadSupportRequest(c, c.Param("id"))
	if !ok {
		return
	}
	c.JSON(http.StatusOK, req)
}

// CloseSupportRequest lets the requester close their own request.
func CloseSupportRequest(c *gin.Context) {
	req, ok := loadSupportRequest(c, c.Param("id"))
	if !ok {
		return
	}
	if req.RequesterID != actorUserID(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Only the requester can close this request"})
		return
	}
	now := time.Now()
	if err := utils.DB.Model(&req).Updates(map[string]interface{}{
		"status":      "closed",
		"resolved_at": &now,
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to close support request"})
		return
	}
	preloadSupportRequest(utils.DB).First(&req, "id = ?", req.ID)
	c.JSON(http.StatusOK, req)
}

// CreateSupportRequestReply appends a message to the thread. Both the
// requester and super admins can reply; admin replies move the request to
// in_progress and notify the requester.
func CreateSupportRequestReply(c *gin.Context) {
	req, ok := loadSupportRequest(c, c.Param("id"))
	if !ok {
		return
	}

	var input struct {
		Message string `json:"message" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	message := strings.TrimSpace(input.Message)
	if message == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Message is required"})
		return
	}

	isAdmin := isSuperAdminCtx(c)
	reply := models.SupportRequestReply{
		ID:               uuid.New(),
		SupportRequestID: req.ID,
		AuthorID:         actorUserID(c),
		IsAdmin:          isAdmin,
		Message:          message,
	}
	if err := utils.DB.Create(&reply).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to send reply"})
		return
	}

	now := time.Now()
	updates := map[string]interface{}{"last_reply_at": &now}
	switch {
	case isAdmin && req.Status == "open":
		updates["status"] = "in_progress"
	case !isAdmin && (req.Status == "resolved" || req.Status == "closed"):
		updates["status"] = "open"
		updates["resolved_at"] = nil
	}
	utils.DB.Model(&req).Updates(updates)

	if isAdmin {
		notifySupportRequestReply(&req, message)
	}

	var author models.User
	utils.DB.Select("id", "name", "email").First(&author, "id = ?", reply.AuthorID)
	reply.Author = &author
	c.JSON(http.StatusCreated, reply)
}

// AdminListSupportRequests lists requests across all users (super admin).
// Supports status/search filters and page/per_page pagination; callers that
// omit pagination params get the legacy plain-array response.
func AdminListSupportRequests(c *gin.Context) {
	query := utils.DB.Model(&models.SupportRequest{})
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		query = query.Where("status = ?", status)
	}
	if search := strings.TrimSpace(c.Query("search")); search != "" {
		like := "%" + strings.ToLower(search) + "%"
		query = query.Where(
			"LOWER(request_number) LIKE ? OR LOWER(subject) LIKE ? OR requester_id IN (SELECT id FROM users WHERE LOWER(name) LIKE ? OR LOWER(email) LIKE ?) OR store_id IN (SELECT id FROM stores WHERE LOWER(name) LIKE ?)",
			like, like, like, like, like,
		)
	}

	if paginationRequested(c) {
		page, perPage := pageParams(c)

		var total int64
		if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load support requests"})
			return
		}

		pageQuery := preloadSupportRequest(query).Order("created_at DESC")
		if perPage > 0 {
			pageQuery = pageQuery.Limit(perPage).Offset((page - 1) * perPage)
		}
		var requests []models.SupportRequest
		if err := pageQuery.Find(&requests).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load support requests"})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"data":     requests,
			"total":    total,
			"page":     page,
			"per_page": perPage,
		})
		return
	}

	var requests []models.SupportRequest
	if err := preloadSupportRequest(query).Order("created_at DESC").Find(&requests).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load support requests"})
		return
	}
	c.JSON(http.StatusOK, requests)
}

// AdminGetSupportRequest returns one request with its thread (super admin).
func AdminGetSupportRequest(c *gin.Context) {
	var req models.SupportRequest
	if err := preloadSupportRequest(utils.DB).First(&req, "id = ?", c.Param("id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Support request not found"})
		return
	}
	c.JSON(http.StatusOK, req)
}

// AdminUpdateSupportRequest updates the workflow status and internal notes.
func AdminUpdateSupportRequest(c *gin.Context) {
	var req models.SupportRequest
	if err := utils.DB.First(&req, "id = ?", c.Param("id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Support request not found"})
		return
	}

	var input struct {
		Status     *string `json:"status"`
		AdminNotes *string `json:"admin_notes"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	updates := map[string]interface{}{}
	if input.Status != nil {
		status := strings.TrimSpace(*input.Status)
		if !supportRequestStatuses[status] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid status"})
			return
		}
		updates["status"] = status
		if status == "resolved" || status == "closed" {
			now := time.Now()
			updates["resolved_at"] = &now
		} else {
			updates["resolved_at"] = nil
		}
	}
	if input.AdminNotes != nil {
		updates["admin_notes"] = *input.AdminNotes
	}
	if len(updates) > 0 {
		if err := utils.DB.Model(&req).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update support request"})
			return
		}
	}
	preloadSupportRequest(utils.DB).First(&req, "id = ?", req.ID)
	c.JSON(http.StatusOK, req)
}

// notifySupportRequestReply alerts the requester about a new admin reply via
// an internal notification and, when SMTP is configured, an email.
func notifySupportRequestReply(req *models.SupportRequest, replyMessage string) {
	var requester models.User
	if err := utils.DB.Select("id", "name", "email").First(&requester, "id = ?", req.RequesterID).Error; err != nil {
		return
	}

	now := time.Now()
	utils.DB.Create(&models.Notification{
		ID:           uuid.New(),
		UserID:       req.OwnerUserID,
		Type:         "support_reply",
		Title:        fmt.Sprintf("Support replied to %s", req.RequestNumber),
		Message:      fmt.Sprintf("Our support team replied to your request \"%s\".", req.Subject),
		Channels:     "internal",
		SentChannels: "internal",
		RelatedID:    &req.ID,
		RelatedType:  "support_request",
		Priority:     "normal",
		Status:       "sent",
		SentAt:       &now,
	})

	if !utils.EmailConfigured() || requester.Email == "" {
		return
	}
	subject := fmt.Sprintf("Re: %s — %s", req.RequestNumber, req.Subject)
	body := fmt.Sprintf(
		"<p>Hi %s,</p><p>Our support team replied to your request <strong>%s</strong> &ldquo;%s&rdquo;:</p><blockquote style=\"border-left:3px solid #ddd;padding-left:12px;color:#444\">%s</blockquote><p>You can view the full conversation under Help &amp; Support in the app.</p>",
		html.EscapeString(requester.Name),
		html.EscapeString(req.RequestNumber),
		html.EscapeString(req.Subject),
		strings.ReplaceAll(html.EscapeString(replyMessage), "\n", "<br>"),
	)
	_ = utils.SendEmail(requester.Email, subject, body)
}
