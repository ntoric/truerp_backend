package controllers

import (
	"truerp/models"
	"truerp/utils"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

var nonDigit = regexp.MustCompile(`\D`)

func normalizePhone(phone string) string {
	return nonDigit.ReplaceAllString(phone, "")
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		if !prevDash && b.Len() > 0 {
			b.WriteByte('-')
			prevDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "portal"
	}
	return out
}

func GetOrCreatePortalSettings(userID uuid.UUID) (*models.CustomerPortalSettings, error) {
	var settings models.CustomerPortalSettings
	err := utils.DB.Where("user_id = ?", userID).First(&settings).Error
	if err == nil {
		return &settings, nil
	}

	var business models.Business
	slug := "portal"
	if utils.DB.Where("user_id = ?", userID).First(&business).Error == nil && business.Name != "" {
		slug = slugify(business.Name)
	}
	base := slug
	for i := 1; ; i++ {
		var count int64
		utils.DB.Model(&models.CustomerPortalSettings{}).Where("slug = ?", slug).Count(&count)
		if count == 0 {
			break
		}
		slug = fmt.Sprintf("%s-%d", base, i)
	}

	settings = models.CustomerPortalSettings{
		ID:                  uuid.New(),
		UserID:              userID,
		IsEnabled:           false,
		Slug:                slug,
		WelcomeMessage:      "View your invoices, payments, and account details.",
		AllowSupportTickets: true,
	}
	if err := utils.DB.Create(&settings).Error; err != nil {
		return nil, err
	}
	return &settings, nil
}

func GetCustomerPortalSettings(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	settings, err := GetOrCreatePortalSettings(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load portal settings"})
		return
	}

	var business models.Business
	utils.DB.Where("user_id = ?", userID).First(&business)

	c.JSON(http.StatusOK, gin.H{
		"settings": settings,
		"business": gin.H{
			"name":     business.Name,
			"logo_url": business.LogoURL,
		},
		"portal_url": fmt.Sprintf("/portal/login?slug=%s", settings.Slug),
	})
}

func UpdateCustomerPortalSettings(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	settings, err := GetOrCreatePortalSettings(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load portal settings"})
		return
	}

	var input struct {
		IsEnabled           *bool  `json:"is_enabled"`
		Slug                string `json:"slug"`
		WelcomeMessage      string `json:"welcome_message"`
		AllowSupportTickets *bool  `json:"allow_support_tickets"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if input.IsEnabled != nil {
		settings.IsEnabled = *input.IsEnabled
	}
	if input.AllowSupportTickets != nil {
		settings.AllowSupportTickets = *input.AllowSupportTickets
	}
	if input.WelcomeMessage != "" {
		settings.WelcomeMessage = input.WelcomeMessage
	}
	if input.Slug != "" {
		newSlug := slugify(input.Slug)
		var existing models.CustomerPortalSettings
		if err := utils.DB.Where("slug = ? AND user_id != ?", newSlug, userID).First(&existing).Error; err == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "This portal URL is already taken"})
			return
		}
		settings.Slug = newSlug
	}

	if err := utils.DB.Save(settings).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update portal settings"})
		return
	}

	c.JSON(http.StatusOK, settings)
}

func ListCustomerPortalAccess(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var parties []models.Party
	if err := utils.DB.Where("user_id = ? AND party_type = ?", userID, "customer").
		Order("name ASC").Find(&parties).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load customers"})
		return
	}

	var accessRows []models.CustomerPortalAccess
	utils.DB.Where("user_id = ?", userID).Find(&accessRows)
	accessByParty := map[uuid.UUID]models.CustomerPortalAccess{}
	for _, row := range accessRows {
		accessByParty[row.PartyID] = row
	}

	type row struct {
		PartyID     uuid.UUID  `json:"party_id"`
		Name        string     `json:"name"`
		Phone       string     `json:"phone"`
		Email       string     `json:"email"`
		IsEnabled   bool       `json:"is_enabled"`
		HasAccess   bool       `json:"has_access"`
		LastLoginAt *time.Time `json:"last_login_at,omitempty"`
	}

	out := make([]row, 0, len(parties))
	for _, p := range parties {
		acc, ok := accessByParty[p.ID]
		out = append(out, row{
			PartyID:     p.ID,
			Name:        p.Name,
			Phone:       p.Phone,
			Email:       p.Email,
			IsEnabled:   ok && acc.IsEnabled,
			HasAccess:   ok,
			LastLoginAt: acc.LastLoginAt,
		})
	}

	c.JSON(http.StatusOK, out)
}

func UpsertCustomerPortalAccess(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	partyID, err := uuid.Parse(c.Param("party_id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid customer id"})
		return
	}

	var party models.Party
	if err := utils.DB.Where("user_id = ? AND id = ? AND party_type = ?", userID, partyID, "customer").First(&party).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Customer not found"})
		return
	}

	var input struct {
		PIN       string `json:"pin"`
		IsEnabled *bool  `json:"is_enabled"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	enabled := true
	if input.IsEnabled != nil {
		enabled = *input.IsEnabled
	}

	var access models.CustomerPortalAccess
	err = utils.DB.Where("user_id = ? AND party_id = ?", userID, partyID).First(&access).Error
	hasExisting := err == nil

	pin := strings.TrimSpace(input.PIN)
	if enabled || !hasExisting {
		if len(pin) < 4 || len(pin) > 8 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "PIN must be 4–8 digits"})
			return
		}
		for _, ch := range pin {
			if ch < '0' || ch > '9' {
				c.JSON(http.StatusBadRequest, gin.H{"error": "PIN must contain digits only"})
				return
			}
		}
	}

	var hash []byte
	if pin != "" {
		var errHash error
		hash, errHash = bcrypt.GenerateFromPassword([]byte(pin), bcrypt.DefaultCost)
		if errHash != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to set PIN"})
			return
		}
	}

	if !hasExisting {
		if len(hash) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "PIN is required"})
			return
		}
		access = models.CustomerPortalAccess{
			ID:        uuid.New(),
			UserID:    userID,
			PartyID:   partyID,
			PINHash:   string(hash),
			IsEnabled: enabled,
		}
		if err := utils.DB.Create(&access).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to enable portal access"})
			return
		}
	} else {
		if len(hash) > 0 {
			access.PINHash = string(hash)
		}
		access.IsEnabled = enabled
		if err := utils.DB.Save(&access).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update portal access"})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{"message": "Portal access saved", "party_id": partyID, "is_enabled": enabled})
}

func AdminListSupportTickets(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	status := c.Query("status")

	query := utils.DB.Where("user_id = ?", userID).Preload("Party").Order("created_at DESC")
	if status != "" {
		query = query.Where("status = ?", status)
	}

	var tickets []models.SupportTicket
	if err := query.Find(&tickets).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load tickets"})
		return
	}
	c.JSON(http.StatusOK, tickets)
}

func AdminUpdateSupportTicket(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var ticket models.SupportTicket
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&ticket).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Ticket not found"})
		return
	}

	var input struct {
		Status     string `json:"status"`
		AdminNotes string `json:"admin_notes"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	allowed := map[string]bool{"open": true, "in_progress": true, "resolved": true, "closed": true}
	if input.Status != "" {
		if !allowed[input.Status] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid status"})
			return
		}
		ticket.Status = input.Status
	}
	if input.AdminNotes != "" {
		ticket.AdminNotes = input.AdminNotes
	}

	if err := utils.DB.Save(&ticket).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update ticket"})
		return
	}
	c.JSON(http.StatusOK, ticket)
}

func GetPortalPublicInfo(c *gin.Context) {
	slug := slugify(c.Param("slug"))
	var settings models.CustomerPortalSettings
	if err := utils.DB.Where("slug = ? AND is_enabled = ?", slug, true).First(&settings).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Portal not found"})
		return
	}

	var business models.Business
	utils.DB.Where("user_id = ?", settings.UserID).First(&business)

	loyaltySettings, _ := GetOrCreateLoyaltySettings(settings.UserID)

	c.JSON(http.StatusOK, gin.H{
		"business_name":         business.Name,
		"logo_url":              business.LogoURL,
		"welcome_message":       settings.WelcomeMessage,
		"allow_support_tickets": settings.AllowSupportTickets,
		"loyalty_enabled":       loyaltySettings.IsEnabled,
		"slug":                  settings.Slug,
	})
}

func PortalLogin(c *gin.Context) {
	var input struct {
		Slug  string `json:"slug" binding:"required"`
		Phone string `json:"phone" binding:"required"`
		PIN   string `json:"pin" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	slug := slugify(input.Slug)
	phone := normalizePhone(input.Phone)
	pin := strings.TrimSpace(input.PIN)

	var settings models.CustomerPortalSettings
	if err := utils.DB.Where("slug = ? AND is_enabled = ?", slug, true).First(&settings).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid portal or portal is disabled"})
		return
	}

	var parties []models.Party
	utils.DB.Where("user_id = ? AND party_type = ?", settings.UserID, "customer").Find(&parties)

	var party *models.Party
	for i := range parties {
		if normalizePhone(parties[i].Phone) == phone {
			party = &parties[i]
			break
		}
	}
	if party == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid phone or PIN"})
		return
	}

	var access models.CustomerPortalAccess
	if err := utils.DB.Where("user_id = ? AND party_id = ? AND is_enabled = ?", settings.UserID, party.ID, true).
		First(&access).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Portal access is not enabled for this customer"})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(access.PINHash), []byte(pin)); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid phone or PIN"})
		return
	}

	now := time.Now()
	access.LastLoginAt = &now
	utils.DB.Save(&access)

	token, err := utils.GeneratePortalToken(settings.UserID, party.ID, party.Name, party.Phone)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create session"})
		return
	}

	loyaltySettings, _ := GetOrCreateLoyaltySettings(settings.UserID)

	c.JSON(http.StatusOK, gin.H{
		"token": token,
		"customer": gin.H{
			"id":    party.ID,
			"name":  party.Name,
			"phone": party.Phone,
		},
		"business": gin.H{
			"name": settings.Slug,
		},
		"loyalty_enabled": loyaltySettings.IsEnabled,
		"slug":            settings.Slug,
	})
}

func PortalGetProfile(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	partyID := c.MustGet("party_id").(uuid.UUID)

	var party models.Party
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, partyID).First(&party).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Customer not found"})
		return
	}

	settings, _ := GetOrCreatePortalSettings(userID)
	loyaltySettings, _ := GetOrCreateLoyaltySettings(userID)

	var business models.Business
	utils.DB.Where("user_id = ?", userID).First(&business)

	c.JSON(http.StatusOK, gin.H{
		"customer": gin.H{
			"id":             party.ID,
			"name":           party.Name,
			"phone":          party.Phone,
			"email":          party.Email,
			"balance":        party.Balance,
			"loyalty_points": party.LoyaltyPoints,
		},
		"business": gin.H{
			"name":     business.Name,
			"logo_url": business.LogoURL,
			"phone":    business.Phone,
			"email":    business.Email,
		},
		"portal": gin.H{
			"welcome_message":       settings.WelcomeMessage,
			"allow_support_tickets": settings.AllowSupportTickets,
			"loyalty_enabled":       loyaltySettings.IsEnabled,
			"slug":                  settings.Slug,
		},
	})
}

func PortalListInvoices(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	partyID := c.MustGet("party_id").(uuid.UUID)

	var invoices []models.Invoice
	if err := utils.DB.Where("user_id = ? AND party_id = ? AND status NOT IN ?", userID, partyID, []string{"draft", "cancelled"}).
		Order("date DESC, invoice_number DESC").
		Find(&invoices).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load invoices"})
		return
	}
	c.JSON(http.StatusOK, invoices)
}

func PortalGetInvoice(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	partyID := c.MustGet("party_id").(uuid.UUID)
	id := c.Param("id")

	var invoice models.Invoice
	if err := utils.DB.Where("user_id = ? AND party_id = ? AND id = ?", userID, partyID, id).
		Preload("Party").Preload("Items").
		First(&invoice).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Invoice not found"})
		return
	}
	c.JSON(http.StatusOK, invoice)
}

func PortalInvoicePDF(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	partyID := c.MustGet("party_id").(uuid.UUID)
	id := c.Param("id")

	var invoice models.Invoice
	if err := utils.DB.Where("user_id = ? AND party_id = ? AND id = ?", userID, partyID, id).
		Preload("Party").Preload("Items").
		First(&invoice).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Invoice not found"})
		return
	}
	c.Header("Content-Type", "text/html")
	c.String(http.StatusOK, InvoicePDFHTML(invoice))
}

func PortalListPayments(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	partyID := c.MustGet("party_id").(uuid.UUID)

	var payments []models.Payment
	if err := utils.DB.Where("user_id = ? AND party_id = ?", userID, partyID).
		Order("date DESC").
		Find(&payments).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load payments"})
		return
	}
	c.JSON(http.StatusOK, payments)
}

func PortalGetLoyalty(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	partyID := c.MustGet("party_id").(uuid.UUID)

	settings, _ := GetOrCreateLoyaltySettings(userID)
	if !settings.IsEnabled {
		c.JSON(http.StatusOK, gin.H{"enabled": false})
		return
	}

	var party models.Party
	utils.DB.Where("user_id = ? AND id = ?", userID, partyID).First(&party)

	var transactions []models.LoyaltyTransaction
	utils.DB.Where("user_id = ? AND party_id = ?", userID, partyID).
		Order("created_at DESC").Limit(50).
		Find(&transactions)

	c.JSON(http.StatusOK, gin.H{
		"enabled":      true,
		"points":       party.LoyaltyPoints,
		"settings":     settings,
		"transactions": transactions,
	})
}

func PortalListStatements(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	partyID := c.MustGet("party_id").(uuid.UUID)

	var statements []models.CustomerStatement
	if err := utils.DB.Where("user_id = ? AND party_id = ?", userID, partyID).
		Order("generated_at DESC").
		Find(&statements).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load statements"})
		return
	}
	c.JSON(http.StatusOK, statements)
}

func PortalStatementPDF(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	partyID := c.MustGet("party_id").(uuid.UUID)
	id := c.Param("id")

	var statement models.CustomerStatement
	if err := utils.DB.Where("user_id = ? AND party_id = ? AND id = ?", userID, partyID, id).
		Preload("Party").Preload("Transactions").
		First(&statement).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Statement not found"})
		return
	}
	c.Header("Content-Type", "text/html")
	c.String(http.StatusOK, StatementPDFHTML(statement))
}

func PortalListTickets(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	partyID := c.MustGet("party_id").(uuid.UUID)

	var tickets []models.SupportTicket
	if err := utils.DB.Where("user_id = ? AND party_id = ?", userID, partyID).
		Order("created_at DESC").
		Find(&tickets).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load tickets"})
		return
	}
	c.JSON(http.StatusOK, tickets)
}

func PortalCreateTicket(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	partyID := c.MustGet("party_id").(uuid.UUID)

	settings, _ := GetOrCreatePortalSettings(userID)
	if !settings.AllowSupportTickets {
		c.JSON(http.StatusForbidden, gin.H{"error": "Support tickets are disabled"})
		return
	}

	var input struct {
		Subject     string `json:"subject" binding:"required"`
		Description string `json:"description" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var count int64
	utils.DB.Model(&models.SupportTicket{}).Where("user_id = ?", userID).Count(&count)
	ticket := models.SupportTicket{
		ID:           uuid.New(),
		UserID:       userID,
		PartyID:      partyID,
		TicketNumber: fmt.Sprintf("TKT-%04d", count+1),
		Subject:      strings.TrimSpace(input.Subject),
		Description:  strings.TrimSpace(input.Description),
		Status:       "open",
	}

	if err := utils.DB.Create(&ticket).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create ticket"})
		return
	}
	c.JSON(http.StatusCreated, ticket)
}
