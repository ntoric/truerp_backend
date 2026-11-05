package controllers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Max cards per generated batch — each PNG artwork is rendered client-side, so
// very large batches would blow up memory/zip size in the browser.
const maxLoyaltyCardsPerBatch = 250

var (
	loyaltyCardColorRe  = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	loyaltyCardPrefixRe = regexp.MustCompile(`[^A-Z0-9]`)

	cardFontFamilies  = map[string]bool{"sans": true, "serif": true, "rounded": true, "display": true, "mono": true}
	cardNumberFonts   = map[string]bool{"mono": true, "courier": true, "sans": true, "serif": true}
	cardDecors        = map[string]bool{"orbits": true, "waves": true, "dots": true, "stripes": true, "none": true}
	cardGraphicGlyphs = map[string]bool{
		"none": true, "star": true, "crown": true, "diamond": true, "gift": true,
		"award": true, "contactless": true, "sparkles": true, "shield": true, "heart": true,
	}
)

// defaultBackTerms is printed on the reverse of generated cards when the store
// has not provided its own terms text.
const defaultBackTerms = "This card is the property of the issuing store and is non-transferable. Present this card to earn and redeem loyalty rewards. If found, please return it to the store."

// LoyaltyCardContent is the fully customisable text/element configuration for
// a card. It is stored as JSON on the design row; every string is editable and
// every element can be shown or hidden independently.
type LoyaltyCardContent struct {
	// Front text
	Title       string `json:"title"`        // card headline (falls back to business name)
	Tagline     string `json:"tagline"`      // line under the headline
	StoreLabel  string `json:"store_label"`  // top-right label (falls back to store name)
	MemberLabel string `json:"member_label"` // e.g. "MEMBER", "GOLD MEMBER"
	FooterText  string `json:"footer_text"`  // small line above the barcode (falls back to phone)
	// Front visibility
	ShowTitle          bool `json:"show_title"`
	ShowTagline        bool `json:"show_tagline"`
	ShowStoreName      bool `json:"show_store_name"`
	ShowMember         bool `json:"show_member"`
	ShowCardNumber     bool `json:"show_card_number"`
	ShowFooter         bool `json:"show_footer"`
	ShowBarcode        bool `json:"show_barcode"`
	ShowBarcodeCaption bool `json:"show_barcode_caption"`
	// Back text
	SignatureLabel string `json:"signature_label"`
	TermsHeading   string `json:"terms_heading"`
	Terms          string `json:"terms"`
	FooterLeft     string `json:"footer_left"`
	FooterRight    string `json:"footer_right"` // falls back to phone · email · city
	// Back visibility
	ShowMagStripe        bool `json:"show_mag_stripe"`
	ShowSignatureStrip   bool `json:"show_signature_strip"`
	ShowBackNumber       bool `json:"show_back_number"`
	ShowBackBusinessName bool `json:"show_back_business_name"`
	ShowBackStoreName    bool `json:"show_back_store_name"`
	ShowBackLogo         bool `json:"show_back_logo"`
	ShowTerms            bool `json:"show_terms"`
	ShowFooterLeft       bool `json:"show_footer_left"`
	ShowFooterRight      bool `json:"show_footer_right"`
}

func defaultLoyaltyCardContent() LoyaltyCardContent {
	return LoyaltyCardContent{
		Title:                "",
		Tagline:              "LOYALTY REWARDS",
		StoreLabel:           "",
		MemberLabel:          "MEMBER",
		FooterText:           "",
		ShowTitle:            true,
		ShowTagline:          true,
		ShowStoreName:        true,
		ShowMember:           true,
		ShowCardNumber:       true,
		ShowFooter:           true,
		ShowBarcode:          true,
		ShowBarcodeCaption:   true,
		SignatureLabel:       "Authorized signature — not valid unless signed",
		TermsHeading:         "TERMS & CONDITIONS",
		Terms:                defaultBackTerms,
		FooterLeft:           "Powered by TruERP Loyalty",
		FooterRight:          "",
		ShowMagStripe:        true,
		ShowSignatureStrip:   true,
		ShowBackNumber:       true,
		ShowBackBusinessName: true,
		ShowBackStoreName:    true,
		ShowBackLogo:         true,
		ShowTerms:            true,
		ShowFooterLeft:       true,
		ShowFooterRight:      true,
	}
}

// resolveCardContent fills defaults → legacy design columns → stored JSON.
// Explicit values in the JSON always win (including empty strings / false).
func resolveCardContent(design *models.LoyaltyCardDesign) LoyaltyCardContent {
	content := defaultLoyaltyCardContent()
	if design.CardTitle != "" {
		content.Title = design.CardTitle
	}
	if design.Tagline != "" {
		content.Tagline = design.Tagline
	}
	if design.FooterText != "" {
		content.FooterText = design.FooterText
	}
	if design.BackTerms != "" {
		content.Terms = design.BackTerms
	}
	if design.ContentJSON != "" {
		// Unmarshal onto the defaults — absent keys keep their defaults.
		_ = json.Unmarshal([]byte(design.ContentJSON), &content)
	}
	return content
}

// resolveCardLayout returns the stored per-element geometry as a generic map.
// The frontend owns the geometry schema/defaults — the backend just persists
// and merges it.
func resolveCardLayout(design *models.LoyaltyCardDesign) map[string]interface{} {
	layout := map[string]interface{}{}
	if design.LayoutJSON != "" {
		_ = json.Unmarshal([]byte(design.LayoutJSON), &layout)
	}
	return layout
}

// mergeCardLayout deep-merges an incoming {front:{el:{…}},back:{el:{…}}} patch
// over the stored layout — each element object is replaced wholesale.
func mergeCardLayout(design *models.LoyaltyCardDesign, incoming map[string]interface{}) {
	layout := resolveCardLayout(design)
	for side, elements := range incoming {
		if side != "front" && side != "back" {
			continue
		}
		inElements, ok := elements.(map[string]interface{})
		if !ok {
			continue
		}
		sideLayout, _ := layout[side].(map[string]interface{})
		if sideLayout == nil {
			sideLayout = map[string]interface{}{}
		}
		for el, geom := range inElements {
			if g, ok := geom.(map[string]interface{}); ok {
				sideLayout[el] = g
			}
		}
		layout[side] = sideLayout
	}
	if payload, err := json.Marshal(layout); err == nil && len(payload) < 16<<10 {
		design.LayoutJSON = string(payload)
	}
}

func defaultLoyaltyCardDesign(userID uuid.UUID, storeID *uuid.UUID) *models.LoyaltyCardDesign {
	return &models.LoyaltyCardDesign{
		ID:           uuid.New(),
		UserID:       userID,
		StoreID:      storeID,
		Theme:        "midnight",
		PrimaryColor: "#0f172a",
		AccentColor:  "#f59e0b",
		CardPrefix:   "LC",
		Tagline:      "LOYALTY REWARDS",
		ShowLogo:     true,
		ShowChip:     true,
		IncludeBack:  true,
		BackTerms:    defaultBackTerms,
		FontFamily:   "sans",
		NumberFont:   "mono",
		Decor:        "orbits",
		FrontGraphic: "none",
		BackGraphic:  "none",
		NextSerial:   1,
	}
}

// loyaltyCardDesignQuery scopes design rows to the current data owner and the
// active store (NULL store when the scope has no store record).
func loyaltyCardDesignQuery(c *gin.Context) (*gorm.DB, *uuid.UUID) {
	userID := c.MustGet("user_id").(uuid.UUID)
	query := utils.DB.Where("user_id = ?", userID)
	var storeID *uuid.UUID
	if id, ok := currentStoreID(c); ok {
		storeID = &id
		query = query.Where("store_id = ?", id)
	} else {
		query = query.Where("store_id IS NULL")
	}
	return query, storeID
}

func getOrCreateLoyaltyCardDesign(c *gin.Context) (*models.LoyaltyCardDesign, error) {
	userID := c.MustGet("user_id").(uuid.UUID)
	query, storeID := loyaltyCardDesignQuery(c)

	var design models.LoyaltyCardDesign
	err := query.Order("created_at asc").First(&design).Error
	if err == nil {
		return &design, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	design = *defaultLoyaltyCardDesign(userID, storeID)
	if err := utils.DB.Create(&design).Error; err != nil {
		return nil, err
	}
	return &design, nil
}

// cardBusinessInfo is the store/business data the client needs to render the
// artwork (business name is the card headline).
type cardBusinessInfo struct {
	Name    string `json:"name"`
	Phone   string `json:"phone"`
	City    string `json:"city"`
	State   string `json:"state"`
	LogoURL string `json:"logo_url"`
	Email   string `json:"email"`
}

type cardStoreInfo struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	Code string    `json:"code"`
}

func loyaltyCardContext(c *gin.Context) (cardBusinessInfo, *cardStoreInfo) {
	userID := c.MustGet("user_id").(uuid.UUID)
	info := cardBusinessInfo{}
	if business, err := ensureBusinessForUser(userID); err == nil {
		info = cardBusinessInfo{
			Name:    business.Name,
			Phone:   business.Phone,
			City:    business.City,
			State:   business.State,
			LogoURL: business.LogoURL,
			Email:   business.Email,
		}
	}
	var store *cardStoreInfo
	if storeID, ok := currentStoreID(c); ok {
		if s, err := utils.FindStoreByID(utils.DB, storeID); err == nil {
			store = &cardStoreInfo{ID: s.ID, Name: s.Name, Code: s.Code}
		}
	}
	return info, store
}

func normalizeCardPrefix(raw string) string {
	prefix := loyaltyCardPrefixRe.ReplaceAllString(strings.ToUpper(strings.TrimSpace(raw)), "")
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}
	if prefix == "" {
		prefix = "LC"
	}
	return prefix
}

func clipCardText(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) > max {
		s = s[:max]
	}
	return s
}

// GetLoyaltyCardDesign returns the active store's card artwork configuration
// plus the business/store details needed to render it.
func GetLoyaltyCardDesign(c *gin.Context) {
	design, err := getOrCreateLoyaltyCardDesign(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load card design"})
		return
	}
	business, store := loyaltyCardContext(c)
	c.JSON(http.StatusOK, gin.H{
		"design":   design,
		"content":  resolveCardContent(design),
		"layout":   resolveCardLayout(design),
		"business": business,
		"store":    store,
	})
}

// clipCardContent bounds every editable string so nothing oversized reaches
// the artwork or the database.
func clipCardContent(content *LoyaltyCardContent) {
	content.Title = clipCardText(content.Title, 48)
	content.Tagline = clipCardText(content.Tagline, 60)
	content.StoreLabel = clipCardText(content.StoreLabel, 48)
	content.MemberLabel = clipCardText(content.MemberLabel, 30)
	content.FooterText = clipCardText(content.FooterText, 80)
	content.SignatureLabel = clipCardText(content.SignatureLabel, 80)
	content.TermsHeading = clipCardText(content.TermsHeading, 60)
	content.Terms = clipCardText(content.Terms, 500)
	content.FooterLeft = clipCardText(content.FooterLeft, 80)
	content.FooterRight = clipCardText(content.FooterRight, 120)
}

// UpdateLoyaltyCardDesign saves the active store's artwork configuration.
// The serial counter is not editable here — it only advances when a batch of
// cards is generated. `content` carries the full text/visibility config.
func UpdateLoyaltyCardDesign(c *gin.Context) {
	var input struct {
		Theme        string           `json:"theme"`
		PrimaryColor string           `json:"primary_color"`
		AccentColor  string           `json:"accent_color"`
		CardPrefix   string           `json:"card_prefix"`
		CardTitle    string           `json:"card_title"`
		Tagline      string           `json:"tagline"`
		FooterText   string           `json:"footer_text"`
		BackTerms    string           `json:"back_terms"`
		ShowLogo     *bool            `json:"show_logo"`
		ShowChip     *bool            `json:"show_chip"`
		IncludeBack  *bool            `json:"include_back"`
		FontFamily   string           `json:"font_family"`
		NumberFont   string           `json:"number_font"`
		Decor        string           `json:"decor"`
		FrontGraphic string           `json:"front_graphic"`
		BackGraphic  string           `json:"back_graphic"`
		Content      *json.RawMessage `json:"content"`
		Layout       *json.RawMessage `json:"layout"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	design, err := getOrCreateLoyaltyCardDesign(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load card design"})
		return
	}

	if theme := clipCardText(input.Theme, 32); theme != "" {
		design.Theme = theme
	}
	if input.PrimaryColor != "" {
		if !loyaltyCardColorRe.MatchString(input.PrimaryColor) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Primary color must be a hex color like #1e3a8a"})
			return
		}
		design.PrimaryColor = strings.ToLower(input.PrimaryColor)
	}
	if input.AccentColor != "" {
		if !loyaltyCardColorRe.MatchString(input.AccentColor) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Accent color must be a hex color like #f59e0b"})
			return
		}
		design.AccentColor = strings.ToLower(input.AccentColor)
	}
	if prefix := normalizeCardPrefix(input.CardPrefix); prefix != "" {
		design.CardPrefix = prefix
	}
	if input.ShowLogo != nil {
		design.ShowLogo = *input.ShowLogo
	}
	if input.ShowChip != nil {
		design.ShowChip = *input.ShowChip
	}
	if input.IncludeBack != nil {
		design.IncludeBack = *input.IncludeBack
	}
	if cardFontFamilies[input.FontFamily] {
		design.FontFamily = input.FontFamily
	}
	if cardNumberFonts[input.NumberFont] {
		design.NumberFont = input.NumberFont
	}
	if cardDecors[input.Decor] {
		design.Decor = input.Decor
	}
	if cardGraphicGlyphs[input.FrontGraphic] {
		design.FrontGraphic = input.FrontGraphic
	}
	if cardGraphicGlyphs[input.BackGraphic] {
		design.BackGraphic = input.BackGraphic
	}
	if input.Layout != nil {
		var layout map[string]interface{}
		if err := json.Unmarshal(*input.Layout, &layout); err == nil {
			mergeCardLayout(design, layout)
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid card layout"})
			return
		}
	}

	if input.Content != nil {
		// Merge the incoming JSON over resolved defaults — absent keys keep
		// their current values instead of being zeroed out.
		content := resolveCardContent(design)
		if err := json.Unmarshal(*input.Content, &content); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid card content"})
			return
		}
		clipCardContent(&content)
		if payload, err := json.Marshal(content); err == nil {
			design.ContentJSON = string(payload)
		}
		// Keep the legacy single-purpose columns in sync.
		design.CardTitle = content.Title
		design.Tagline = content.Tagline
		design.FooterText = content.FooterText
		design.BackTerms = content.Terms
	} else {
		// Legacy clients may still write the flat fields.
		design.CardTitle = clipCardText(input.CardTitle, 48)
		design.Tagline = clipCardText(input.Tagline, 60)
		design.FooterText = clipCardText(input.FooterText, 80)
		design.BackTerms = clipCardText(input.BackTerms, 500)
	}

	if err := utils.DB.Save(design).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save card design"})
		return
	}

	business, store := loyaltyCardContext(c)
	c.JSON(http.StatusOK, gin.H{
		"design":   design,
		"content":  resolveCardContent(design),
		"layout":   resolveCardLayout(design),
		"business": business,
		"store":    store,
	})
}

// Card numbers are plain 6-digit serials (e.g. 000142) — easy to read out and
// type at the counter, and they encode compactly as Code 128-C.
func formatLoyaltyCardNumber(serial int64) string {
	return fmt.Sprintf("%06d", serial)
}

// GenerateLoyaltyCards allocates a batch of unique card numbers from the
// store's series and records them. The client then renders the artwork and
// downloads it — the series counter advances transactionally with the batch so
// card numbers can never repeat.
func GenerateLoyaltyCards(c *gin.Context) {
	var input struct {
		Count int `json:"count" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if input.Count < 1 || input.Count > maxLoyaltyCardsPerBatch {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("Card count must be between 1 and %d per batch", maxLoyaltyCardsPerBatch),
		})
		return
	}

	userID := c.MustGet("user_id").(uuid.UUID)
	// Ensure the design row exists before the transaction.
	if _, err := getOrCreateLoyaltyCardDesign(c); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load card design"})
		return
	}

	batchID := uuid.New()
	var cards []models.LoyaltyCard

	err := utils.DB.Transaction(func(tx *gorm.DB) error {
		var design models.LoyaltyCardDesign
		var storeID *uuid.UUID
		query := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ?", userID)
		if id, ok := currentStoreID(c); ok {
			storeID = &id
			query = query.Where("store_id = ?", id)
		} else {
			query = query.Where("store_id IS NULL")
		}
		if err := query.Order("created_at asc").First(&design).Error; err != nil {
			return err
		}

		start := design.NextSerial
		if start < 1 {
			start = 1
		}
		for i := 0; i < input.Count; i++ {
			serial := start + int64(i)
			cards = append(cards, models.LoyaltyCard{
				ID:         uuid.New(),
				UserID:     userID,
				StoreID:    storeID,
				BatchID:    batchID,
				CardNumber: formatLoyaltyCardNumber(serial),
				Serial:     serial,
				Status:     "active",
			})
		}
		if err := tx.Create(&cards).Error; err != nil {
			return err
		}
		return tx.Model(&design).Update("next_serial", start+int64(input.Count)).Error
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate cards"})
		return
	}

	business, store := loyaltyCardContext(c)
	design, _ := getOrCreateLoyaltyCardDesign(c)
	c.JSON(http.StatusOK, gin.H{
		"batch_id": batchID,
		"count":    len(cards),
		"cards":    cards,
		"design":   design,
		"content":  resolveCardContent(design),
		"layout":   resolveCardLayout(design),
		"business": business,
		"store":    store,
	})
}

// ListLoyaltyCards returns generated cards for the active store along with a
// per-batch summary.
func ListLoyaltyCards(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	search := strings.TrimSpace(c.Query("search"))
	batch := strings.TrimSpace(c.Query("batch_id"))

	query := utils.DB.Where("user_id = ?", userID)
	if id, ok := currentStoreID(c); ok {
		query = query.Where("store_id = ?", id)
	} else {
		query = query.Where("store_id IS NULL")
	}
	if batch != "" {
		query = query.Where("batch_id = ?", batch)
	}
	if search != "" {
		query = query.Where("LOWER(card_number) LIKE ?", "%"+strings.ToLower(search)+"%")
	}

	var cards []models.LoyaltyCard
	if err := query.Preload("Party").Order("created_at DESC, serial DESC").Limit(300).Find(&cards).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch cards"})
		return
	}

	type batchSummary struct {
		BatchID   uuid.UUID `json:"batch_id"`
		Count     int64     `json:"count"`
		FirstCard string    `json:"first_card"`
		LastCard  string    `json:"last_card"`
		CreatedAt string    `json:"created_at"`
	}
	var batches []batchSummary
	summaryQuery := utils.DB.Model(&models.LoyaltyCard{}).
		Select("batch_id, COUNT(*) as count, MIN(card_number) as first_card, MAX(card_number) as last_card, MAX(created_at) as created_at").
		Where("user_id = ?", userID)
	if id, ok := currentStoreID(c); ok {
		summaryQuery = summaryQuery.Where("store_id = ?", id)
	} else {
		summaryQuery = summaryQuery.Where("store_id IS NULL")
	}
	summaryQuery.Group("batch_id").Order("MAX(created_at) DESC").Limit(30).Scan(&batches)

	c.JSON(http.StatusOK, gin.H{
		"cards":   cards,
		"batches": batches,
	})
}

// DeleteLoyaltyCard soft-deletes a generated card (e.g. a misprint). The
// serial is never reused.
func DeleteLoyaltyCard(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	cardID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid card id"})
		return
	}

	query := utils.DB.Where("user_id = ? AND id = ?", userID, cardID)
	if id, ok := currentStoreID(c); ok {
		query = query.Where("store_id = ?", id)
	} else {
		query = query.Where("store_id IS NULL")
	}
	res := query.Delete(&models.LoyaltyCard{})
	if res.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete card"})
		return
	}
	if res.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Card not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Card deleted"})
}

// loyaltyCardQuery scopes card rows to the current owner + active store.
func loyaltyCardQuery(c *gin.Context) *gorm.DB {
	userID := c.MustGet("user_id").(uuid.UUID)
	query := utils.DB.Where("user_id = ?", userID)
	if id, ok := currentStoreID(c); ok {
		query = query.Where("store_id = ?", id)
	} else {
		query = query.Where("store_id IS NULL")
	}
	return query
}

// normalizeLoyaltyCardNumber tidies scanned/typed input ("lc 000142" → "LC-000142").
func normalizeLoyaltyCardNumber(raw string) string {
	n := strings.ToUpper(strings.TrimSpace(raw))
	n = strings.ReplaceAll(n, " ", "-")
	for strings.Contains(n, "--") {
		n = strings.ReplaceAll(n, "--", "-")
	}
	return n
}

// LookupLoyaltyCard resolves a scanned/typed card number to the card and its
// linked customer. Used at the counter (POS) to identify the card holder.
func LookupLoyaltyCard(c *gin.Context) {
	number := normalizeLoyaltyCardNumber(c.Query("number"))
	if number == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Card number is required"})
		return
	}

	var card models.LoyaltyCard
	err := loyaltyCardQuery(c).Preload("Party").Where("card_number = ?", number).First(&card).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// Scanners may strip the dash — retry with a dash before the digit run.
		if !strings.Contains(number, "-") {
			if idx := strings.IndexAny(number, "0123456789"); idx > 0 {
				// find where the trailing digit run starts
				last := len(number) - 1
				for last > 0 && number[last-1] >= '0' && number[last-1] <= '9' {
					last--
				}
				if last > idx-1 {
					candidate := number[:last] + "-" + number[last:]
					err = loyaltyCardQuery(c).Preload("Party").Where("card_number = ?", candidate).First(&card).Error
				}
			}
		}
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Card not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Lookup failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"card": card, "party": card.Party})
}

// AssignLoyaltyCard links (or unlinks, with a null party_id) a generated card
// to a customer so the card identifies them at the counter.
func AssignLoyaltyCard(c *gin.Context) {
	cardID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid card id"})
		return
	}

	var input struct {
		PartyID *uuid.UUID `json:"party_id"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var card models.LoyaltyCard
	if err := loyaltyCardQuery(c).Where("id = ?", cardID).First(&card).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Card not found"})
		return
	}

	userID := c.MustGet("user_id").(uuid.UUID)
	if input.PartyID != nil {
		var party models.Party
		if err := utils.DB.Where("user_id = ? AND id = ?", userID, *input.PartyID).First(&party).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Customer not found"})
			return
		}
		card.PartyID = input.PartyID
	} else {
		card.PartyID = nil
	}

	if err := utils.DB.Model(&card).Update("party_id", card.PartyID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to assign card"})
		return
	}

	utils.DB.Preload("Party").First(&card, "id = ?", card.ID)
	c.JSON(http.StatusOK, gin.H{"card": card, "party": card.Party})
}
