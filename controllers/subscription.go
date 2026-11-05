package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

var subscriptionFrequencies = map[string]int{
	"monthly":     1,
	"quarterly":   3,
	"half_yearly": 6,
	"yearly":      12,
}

func subscriptionPeriodMonths(frequency string, custom int) int {
	if frequency == "custom" {
		if custom >= 1 && custom <= 120 {
			return custom
		}
		return 1
	}
	if months, ok := subscriptionFrequencies[frequency]; ok {
		return months
	}
	return 0
}

// parseSubscriptionDate accepts "2006-01-02" or RFC3339 timestamps.
func parseSubscriptionDate(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, errors.New("date is required")
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), nil
	}
	return time.Time{}, errors.New("invalid date format")
}

// subscriptionEffectiveStatus derives the live status from stored state and dates.
func subscriptionEffectiveStatus(s models.StoreSubscription, now time.Time) string {
	if s.Status == "cancelled" {
		return "cancelled"
	}
	if s.StartDate.After(now) {
		return "scheduled"
	}
	if !s.EndDate.After(now) {
		return "expired"
	}
	return "active"
}

func parsePackageFeatures(raw string) []string {
	features := []string{}
	if strings.TrimSpace(raw) == "" {
		return features
	}
	_ = json.Unmarshal([]byte(raw), &features)
	return features
}

// subscriptionPackageJSON renders a package. Non-admin viewers only see the
// price/feature list when the package flags allow it.
func subscriptionPackageJSON(p models.SubscriptionPackage, admin bool) gin.H {
	features := parsePackageFeatures(p.FeaturesJSON)
	out := gin.H{
		"id":               p.ID,
		"name":             p.Name,
		"description":      p.Description,
		"show_price":       p.ShowPrice,
		"show_features":    p.ShowFeatures,
		"is_complimentary": p.IsComplimentary,
		"sort_order":       p.SortOrder,
		"created_at":       p.CreatedAt,
		"updated_at":       p.UpdatedAt,
	}
	if admin {
		out["price"] = p.Price
		out["features"] = features
		out["is_active"] = p.IsActive
		return out
	}
	out["is_active"] = p.IsActive
	if p.ShowPrice {
		out["price"] = p.Price
	}
	if p.ShowFeatures {
		out["features"] = features
	} else {
		out["features"] = []string{}
	}
	return out
}

func storeSubscriptionJSON(s models.StoreSubscription, now time.Time, admin bool) gin.H {
	out := gin.H{
		"id":               s.ID,
		"store_id":         s.StoreID,
		"package_id":       s.PackageID,
		"package_name":     s.PackageName,
		"status":           s.Status,
		"effective_status": subscriptionEffectiveStatus(s, now),
		"start_date":       s.StartDate,
		"end_date":         s.EndDate,
		"frequency":        s.Frequency,
		"period_months":    s.PeriodMonths,
		"auto_renew":       s.AutoRenew,
		"notes":            s.Notes,
		"created_at":       s.CreatedAt,
		"updated_at":       s.UpdatedAt,
	}
	if admin || s.Package == nil || s.Package.ShowPrice {
		out["price"] = s.Price
	}
	days := int(time.Until(s.EndDate).Hours() / 24)
	if s.EndDate.After(now) {
		days = int(s.EndDate.Sub(now).Hours() / 24)
	}
	out["days_remaining"] = days
	if s.Store != nil {
		out["store"] = gin.H{
			"id":   s.Store.ID,
			"name": s.Store.Name,
			"code": s.Store.Code,
		}
	}
	if s.Package != nil {
		out["package"] = subscriptionPackageJSON(*s.Package, admin)
	}
	return out
}

// resolveRequestStore returns the store that scopes the current request:
// the X-Store-ID resolved store, else the store owned by the scoped user.
func resolveRequestStore(c *gin.Context) (models.Store, bool) {
	if id, ok := currentStoreID(c); ok {
		if store, err := utils.FindStoreByID(utils.DB, id); err == nil {
			return store, true
		}
	}
	userID := c.MustGet("user_id").(uuid.UUID)
	if store, err := utils.FindStoreByOwnerUserID(utils.DB, userID); err == nil {
		return store, true
	}
	return models.Store{}, false
}

func recordSubscriptionEvent(tx *gorm.DB, actor models.User, storeID uuid.UUID, subscriptionID *uuid.UUID, eventType, fromPkg, toPkg string, start, end *time.Time, price float64, notes string) {
	event := models.SubscriptionHistory{
		ID:             uuid.New(),
		StoreID:        storeID,
		SubscriptionID: subscriptionID,
		EventType:      eventType,
		FromPackage:    fromPkg,
		ToPackage:      toPkg,
		StartDate:      start,
		EndDate:        end,
		Price:          price,
		Notes:          strings.TrimSpace(notes),
	}
	if actor.ID != uuid.Nil {
		event.ActorUserID = &actor.ID
		event.ActorName = actor.Name
	}
	tx.Create(&event)
}

// ------------------------------ Packages ------------------------------

// ListSubscriptionPackages returns the plan catalog. Super admins see every
// package with full detail; other users only see active packages and only the
// price/features that each package exposes.
func ListSubscriptionPackages(c *gin.Context) {
	actor, err := loadActor(c)
	admin := err == nil && utils.IsSuperAdminRole(actor.Role)

	query := utils.DB.Order("sort_order ASC, created_at ASC")
	if !admin {
		// Complimentary plans are admin-assigned only — never shown publicly.
		query = query.Where("is_active = ? AND is_complimentary = ?", true, false)
	}
	var packages []models.SubscriptionPackage
	if err := query.Find(&packages).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list subscription packages"})
		return
	}
	out := make([]gin.H, 0, len(packages))
	for _, p := range packages {
		out = append(out, subscriptionPackageJSON(p, admin))
	}
	c.JSON(http.StatusOK, out)
}

type subscriptionPackageInput struct {
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	Price           *float64 `json:"price"`
	Features        []string `json:"features"`
	ShowPrice       *bool    `json:"show_price"`
	ShowFeatures    *bool    `json:"show_features"`
	IsActive        *bool    `json:"is_active"`
	IsComplimentary *bool    `json:"is_complimentary"`
	SortOrder       *int     `json:"sort_order"`
}

func normalizePackageFeatures(features []string) ([]string, string) {
	clean := make([]string, 0, len(features))
	for _, f := range features {
		if trimmed := strings.TrimSpace(f); trimmed != "" {
			clean = append(clean, trimmed)
		}
	}
	payload, _ := json.Marshal(clean)
	return clean, string(payload)
}

func CreateSubscriptionPackage(c *gin.Context) {
	actor, err := loadActor(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	var input subscriptionPackageInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request data"})
		return
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Package name is required"})
		return
	}
	if input.Price != nil && *input.Price < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Price cannot be negative"})
		return
	}

	_, featuresJSON := normalizePackageFeatures(input.Features)
	pkg := models.SubscriptionPackage{
		ID:           uuid.New(),
		Name:         name,
		Description:  strings.TrimSpace(input.Description),
		FeaturesJSON: featuresJSON,
		ShowPrice:    true,
		ShowFeatures: true,
		IsActive:     true,
	}
	if input.Price != nil {
		pkg.Price = *input.Price
	}
	if input.ShowPrice != nil {
		pkg.ShowPrice = *input.ShowPrice
	}
	if input.ShowFeatures != nil {
		pkg.ShowFeatures = *input.ShowFeatures
	}
	if input.IsActive != nil {
		pkg.IsActive = *input.IsActive
	}
	if input.IsComplimentary != nil {
		pkg.IsComplimentary = *input.IsComplimentary
	}
	if input.SortOrder != nil {
		pkg.SortOrder = *input.SortOrder
	}
	if err := utils.DB.Create(&pkg).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create subscription package"})
		return
	}

	CreateAuditLog(actor.ID, actor.Name, "create", "subscription_package", &pkg.ID,
		pkg.Name, "Created subscription package", c.ClientIP(), c.GetHeader("User-Agent"),
		subscriptionPackageJSON(pkg, true), "success", "")

	c.JSON(http.StatusCreated, subscriptionPackageJSON(pkg, true))
}

func UpdateSubscriptionPackage(c *gin.Context) {
	actor, err := loadActor(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}
	pkgID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid package id"})
		return
	}
	var pkg models.SubscriptionPackage
	if err := utils.DB.First(&pkg, "id = ?", pkgID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Package not found"})
		return
	}

	var input subscriptionPackageInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request data"})
		return
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Package name is required"})
		return
	}
	if input.Price != nil && *input.Price < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Price cannot be negative"})
		return
	}

	updates := map[string]interface{}{
		"name":        name,
		"description": strings.TrimSpace(input.Description),
	}
	if input.Features != nil {
		_, featuresJSON := normalizePackageFeatures(input.Features)
		updates["features_json"] = featuresJSON
	}
	if input.Price != nil {
		updates["price"] = *input.Price
	}
	if input.ShowPrice != nil {
		updates["show_price"] = *input.ShowPrice
	}
	if input.ShowFeatures != nil {
		updates["show_features"] = *input.ShowFeatures
	}
	if input.IsActive != nil {
		updates["is_active"] = *input.IsActive
	}
	if input.IsComplimentary != nil {
		updates["is_complimentary"] = *input.IsComplimentary
	}
	if input.SortOrder != nil {
		updates["sort_order"] = *input.SortOrder
	}
	if err := utils.DB.Model(&pkg).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update subscription package"})
		return
	}
	utils.DB.First(&pkg, "id = ?", pkgID)

	CreateAuditLog(actor.ID, actor.Name, "update", "subscription_package", &pkg.ID,
		pkg.Name, "Updated subscription package", c.ClientIP(), c.GetHeader("User-Agent"),
		subscriptionPackageJSON(pkg, true), "success", "")

	c.JSON(http.StatusOK, subscriptionPackageJSON(pkg, true))
}

func DeleteSubscriptionPackage(c *gin.Context) {
	actor, err := loadActor(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}
	pkgID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid package id"})
		return
	}
	var pkg models.SubscriptionPackage
	if err := utils.DB.First(&pkg, "id = ?", pkgID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Package not found"})
		return
	}

	var inUse int64
	utils.DB.Model(&models.StoreSubscription{}).Where("package_id = ?", pkgID).Count(&inUse)
	if inUse > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Package is used by existing subscriptions. Deactivate it instead."})
		return
	}
	if err := utils.DB.Delete(&pkg).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete subscription package"})
		return
	}

	CreateAuditLog(actor.ID, actor.Name, "delete", "subscription_package", &pkg.ID,
		pkg.Name, "Deleted subscription package", c.ClientIP(), c.GetHeader("User-Agent"),
		nil, "success", "")

	c.JSON(http.StatusOK, gin.H{"message": "Package deleted"})
}

// -------------------------- Store-facing views --------------------------

// GetCurrentSubscription returns the resolved store's latest subscription.
func GetCurrentSubscription(c *gin.Context) {
	actor, actorErr := loadActor(c)
	isAdmin := actorErr == nil && utils.IsSuperAdminRole(actor.Role)
	store, ok := resolveRequestStore(c)
	if !ok {
		c.JSON(http.StatusOK, gin.H{"subscription": nil})
		return
	}
	var sub models.StoreSubscription
	err := utils.DB.Preload("Package").Preload("Store").
		Where("store_id = ?", store.ID).
		Order("created_at DESC").First(&sub).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusOK, gin.H{
			"subscription": nil,
			"store":        gin.H{"id": store.ID, "name": store.Name, "code": store.Code},
		})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load subscription"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"subscription": storeSubscriptionJSON(sub, time.Now(), isAdmin),
		"store":        gin.H{"id": store.ID, "name": store.Name, "code": store.Code},
	})
}

// subscriptionHistoryJSON renders a history event. Price is billing detail and
// is only included for super admins.
func subscriptionHistoryJSON(e models.SubscriptionHistory, admin bool) gin.H {
	out := gin.H{
		"id":              e.ID,
		"store_id":        e.StoreID,
		"subscription_id": e.SubscriptionID,
		"event_type":      e.EventType,
		"from_package":    e.FromPackage,
		"to_package":      e.ToPackage,
		"start_date":      e.StartDate,
		"end_date":        e.EndDate,
		"notes":           e.Notes,
		"actor_name":      e.ActorName,
		"created_at":      e.CreatedAt,
	}
	if admin {
		out["price"] = e.Price
	}
	if e.Store != nil {
		out["store"] = gin.H{
			"id":   e.Store.ID,
			"name": e.Store.Name,
			"code": e.Store.Code,
		}
	}
	return out
}

// GetSubscriptionHistory returns lifecycle events for the resolved store.
func GetSubscriptionHistory(c *gin.Context) {
	actor, actorErr := loadActor(c)
	isAdmin := actorErr == nil && utils.IsSuperAdminRole(actor.Role)
	store, ok := resolveRequestStore(c)
	if !ok {
		c.JSON(http.StatusOK, []gin.H{})
		return
	}
	var events []models.SubscriptionHistory
	if err := utils.DB.Where("store_id = ?", store.ID).
		Order("created_at DESC").Limit(500).Find(&events).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load subscription history"})
		return
	}
	out := make([]gin.H, 0, len(events))
	for _, e := range events {
		out = append(out, subscriptionHistoryJSON(e, isAdmin))
	}
	c.JSON(http.StatusOK, out)
}

// --------------------------- Admin management ---------------------------

// AdminListSubscriptions lists subscriptions across all stores (super admin).
func AdminListSubscriptions(c *gin.Context) {
	query := utils.DB.Preload("Package").Preload("Store").
		Order("created_at DESC")
	if storeID := strings.TrimSpace(c.Query("store_id")); storeID != "" {
		if parsed, err := uuid.Parse(storeID); err == nil {
			query = query.Where("store_id = ?", parsed)
		}
	}
	var subs []models.StoreSubscription
	if err := query.Limit(500).Find(&subs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list subscriptions"})
		return
	}
	now := time.Now()
	out := make([]gin.H, 0, len(subs))
	for _, s := range subs {
		out = append(out, storeSubscriptionJSON(s, now, true))
	}
	c.JSON(http.StatusOK, out)
}

// AdminSubscriptionHistory lists lifecycle events across stores (super admin).
func AdminSubscriptionHistory(c *gin.Context) {
	query := utils.DB.Preload("Store").Order("created_at DESC")
	if storeID := strings.TrimSpace(c.Query("store_id")); storeID != "" {
		if parsed, err := uuid.Parse(storeID); err == nil {
			query = query.Where("store_id = ?", parsed)
		}
	}
	var events []models.SubscriptionHistory
	if err := query.Limit(500).Find(&events).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load subscription history"})
		return
	}
	out := make([]gin.H, 0, len(events))
	for _, e := range events {
		out = append(out, subscriptionHistoryJSON(e, true))
	}
	c.JSON(http.StatusOK, out)
}

type storeSubscriptionInput struct {
	StoreID      string   `json:"store_id"`
	PackageID    string   `json:"package_id"`
	StartDate    string   `json:"start_date"`
	EndDate      string   `json:"end_date"`
	Frequency    string   `json:"frequency"`
	PeriodMonths int      `json:"period_months"`
	Price        *float64 `json:"price"`
	AutoRenew    *bool    `json:"auto_renew"`
	Status       string   `json:"status"`
	Notes        string   `json:"notes"`
}

// AdminCreateSubscription grants a package subscription to a store. Any live
// (active/scheduled) subscription for the store is superseded.
func AdminCreateSubscription(c *gin.Context) {
	actor, err := loadActor(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	var input storeSubscriptionInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request data"})
		return
	}
	storeID, err := uuid.Parse(strings.TrimSpace(input.StoreID))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Select a store"})
		return
	}
	store, err := utils.FindStoreByID(utils.DB, storeID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Store not found"})
		return
	}
	var pkg *models.SubscriptionPackage
	var pkgID *uuid.UUID
	if trimmed := strings.TrimSpace(input.PackageID); trimmed != "" {
		parsed, err := uuid.Parse(trimmed)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid package"})
			return
		}
		var found models.SubscriptionPackage
		if err := utils.DB.First(&found, "id = ?", parsed).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Package not found"})
			return
		}
		pkg = &found
		pkgID = &found.ID
	}

	frequency := strings.TrimSpace(input.Frequency)
	if frequency == "" {
		frequency = "monthly"
	}
	months := subscriptionPeriodMonths(frequency, input.PeriodMonths)
	if months <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid billing frequency"})
		return
	}

	start, err := parseSubscriptionDate(input.StartDate)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Valid start date is required"})
		return
	}
	end := start.AddDate(0, months, 0)
	if trimmed := strings.TrimSpace(input.EndDate); trimmed != "" {
		if parsedEnd, err := parseSubscriptionDate(trimmed); err == nil {
			end = parsedEnd
		}
	}
	if !end.After(start) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "End date must be after the start date"})
		return
	}

	price := 0.0
	packageName := "Custom Plan"
	if pkg != nil {
		price = pkg.Price
		packageName = pkg.Name
	}
	if input.Price != nil {
		if *input.Price < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Price cannot be negative"})
			return
		}
		price = *input.Price
	}
	if pkg != nil && pkg.IsComplimentary {
		price = 0
	}

	autoRenew := false
	if input.AutoRenew != nil {
		autoRenew = *input.AutoRenew
	}

	sub := models.StoreSubscription{
		ID:           uuid.New(),
		StoreID:      store.ID,
		PackageID:    pkgID,
		PackageName:  packageName,
		Price:        price,
		Status:       "active",
		StartDate:    start,
		EndDate:      end,
		Frequency:    frequency,
		PeriodMonths: months,
		AutoRenew:    autoRenew,
		Notes:        strings.TrimSpace(input.Notes),
		CreatedBy:    &actor.ID,
	}

	now := time.Now()
	err = utils.DB.Transaction(func(tx *gorm.DB) error {
		// Supersede any live subscription for the store.
		var existing []models.StoreSubscription
		if err := tx.Where("store_id = ? AND status = ?", store.ID, "active").Find(&existing).Error; err != nil {
			return err
		}
		for _, prev := range existing {
			if subscriptionEffectiveStatus(prev, now) == "expired" {
				continue
			}
			if err := tx.Model(&prev).Update("status", "cancelled").Error; err != nil {
				return err
			}
			recordSubscriptionEvent(tx, actor, store.ID, &prev.ID, "superseded",
				prev.PackageName, packageName, &prev.StartDate, &prev.EndDate, prev.Price,
				"Superseded by a new subscription")
		}
		if err := tx.Create(&sub).Error; err != nil {
			return err
		}
		recordSubscriptionEvent(tx, actor, store.ID, &sub.ID, "created",
			"", packageName, &sub.StartDate, &sub.EndDate, sub.Price, input.Notes)
		return nil
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create subscription"})
		return
	}
	utils.DB.Preload("Package").Preload("Store").First(&sub, "id = ?", sub.ID)

	CreateAuditLog(actor.ID, actor.Name, "create", "store_subscription", &sub.ID,
		store.Name, "Created store subscription", c.ClientIP(), c.GetHeader("User-Agent"),
		storeSubscriptionJSON(sub, now, true), "success", "")

	c.JSON(http.StatusCreated, storeSubscriptionJSON(sub, now, true))
}

// AdminUpdateSubscription edits an existing subscription. Changing the package
// is recorded as an upgrade/downgrade; other meaningful changes are logged as
// an update event.
func AdminUpdateSubscription(c *gin.Context) {
	actor, err := loadActor(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}
	subID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid subscription id"})
		return
	}
	var sub models.StoreSubscription
	if err := utils.DB.Preload("Package").First(&sub, "id = ?", subID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Subscription not found"})
		return
	}

	var input storeSubscriptionInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request data"})
		return
	}

	updates := map[string]interface{}{}
	var pkgChanged bool
	var newPkg *models.SubscriptionPackage
	if trimmed := strings.TrimSpace(input.PackageID); trimmed != "" {
		parsed, err := uuid.Parse(trimmed)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid package"})
			return
		}
		if sub.PackageID == nil || *sub.PackageID != parsed {
			var found models.SubscriptionPackage
			if err := utils.DB.First(&found, "id = ?", parsed).Error; err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "Package not found"})
				return
			}
			newPkg = &found
			pkgChanged = true
		}
	}

	newStart := sub.StartDate
	newEnd := sub.EndDate
	if trimmed := strings.TrimSpace(input.StartDate); trimmed != "" {
		if parsed, err := parseSubscriptionDate(trimmed); err == nil {
			newStart = parsed
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid start date"})
			return
		}
	}
	if trimmed := strings.TrimSpace(input.EndDate); trimmed != "" {
		if parsed, err := parseSubscriptionDate(trimmed); err == nil {
			newEnd = parsed
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid end date"})
			return
		}
	}
	newFrequency := sub.Frequency
	newMonths := sub.PeriodMonths
	if trimmed := strings.TrimSpace(input.Frequency); trimmed != "" {
		months := subscriptionPeriodMonths(trimmed, input.PeriodMonths)
		if months <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid billing frequency"})
			return
		}
		newFrequency = trimmed
		newMonths = months
	} else if input.PeriodMonths > 0 && sub.Frequency == "custom" {
		newMonths = input.PeriodMonths
	}
	if !newEnd.After(newStart) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "End date must be after the start date"})
		return
	}

	newPrice := sub.Price
	newName := sub.PackageName
	if pkgChanged {
		newName = newPkg.Name
		newPrice = newPkg.Price
	}
	if input.Price != nil {
		if *input.Price < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Price cannot be negative"})
			return
		}
		newPrice = *input.Price
	}
	if pkgChanged && newPkg.IsComplimentary {
		newPrice = 0
	}

	updates["start_date"] = newStart
	updates["end_date"] = newEnd
	updates["frequency"] = newFrequency
	updates["period_months"] = newMonths
	updates["price"] = newPrice
	updates["package_name"] = newName
	if pkgChanged {
		updates["package_id"] = newPkg.ID
	}
	if input.AutoRenew != nil {
		updates["auto_renew"] = *input.AutoRenew
	}
	if trimmed := strings.TrimSpace(input.Status); trimmed != "" {
		if trimmed != "active" && trimmed != "cancelled" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid status"})
			return
		}
		updates["status"] = trimmed
	}
	notesChanged := strings.TrimSpace(input.Notes) != sub.Notes
	updates["notes"] = strings.TrimSpace(input.Notes)

	datesOrFreqChanged := !newStart.Equal(sub.StartDate) || !newEnd.Equal(sub.EndDate) ||
		newFrequency != sub.Frequency || newMonths != sub.PeriodMonths
	statusChanged := false
	if v, ok := updates["status"]; ok && v.(string) != sub.Status {
		statusChanged = true
	}
	if !pkgChanged && !datesOrFreqChanged && !statusChanged && !notesChanged &&
		newPrice == sub.Price {
		c.JSON(http.StatusOK, storeSubscriptionJSON(sub, time.Now(), true))
		return
	}

	// tx.Model(&sub).Updates mutates sub in place — snapshot the old values
	// used by the history event first.
	oldPackageName := sub.PackageName
	oldPrice := sub.Price

	err = utils.DB.Transaction(func(tx *gorm.DB) error {
		// Update through a bare model: sub.Package is preloaded, and updating
		// via Model(&sub) would make GORM re-save the association and rewrite
		// package_id back to the old package.
		if err := tx.Model(&models.StoreSubscription{}).Where("id = ?", sub.ID).Updates(updates).Error; err != nil {
			return err
		}
		switch {
		case pkgChanged:
			eventType := "changed"
			switch {
			case newPrice > oldPrice:
				eventType = "upgraded"
			case newPrice < oldPrice:
				eventType = "downgraded"
			}
			recordSubscriptionEvent(tx, actor, sub.StoreID, &sub.ID, eventType,
				oldPackageName, newName, &newStart, &newEnd, newPrice, input.Notes)
		case statusChanged && updates["status"].(string) == "cancelled":
			recordSubscriptionEvent(tx, actor, sub.StoreID, &sub.ID, "cancelled",
				oldPackageName, "", &newStart, &newEnd, newPrice, input.Notes)
		case statusChanged && updates["status"].(string) == "active":
			recordSubscriptionEvent(tx, actor, sub.StoreID, &sub.ID, "reactivated",
				oldPackageName, "", &newStart, &newEnd, newPrice, input.Notes)
		default:
			recordSubscriptionEvent(tx, actor, sub.StoreID, &sub.ID, "updated",
				oldPackageName, "", &newStart, &newEnd, newPrice, input.Notes)
		}
		return nil
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update subscription"})
		return
	}
	utils.DB.Preload("Package").Preload("Store").First(&sub, "id = ?", sub.ID)

	CreateAuditLog(actor.ID, actor.Name, "update", "store_subscription", &sub.ID,
		sub.PackageName, "Updated store subscription", c.ClientIP(), c.GetHeader("User-Agent"),
		storeSubscriptionJSON(sub, time.Now(), true), "success", "")

	c.JSON(http.StatusOK, storeSubscriptionJSON(sub, time.Now(), true))
}

// AdminRenewSubscription extends a subscription by its billing period. The new
// period starts at the current end date (or now if already expired).
func AdminRenewSubscription(c *gin.Context) {
	actor, err := loadActor(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}
	subID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid subscription id"})
		return
	}
	var sub models.StoreSubscription
	if err := utils.DB.Preload("Package").First(&sub, "id = ?", subID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Subscription not found"})
		return
	}
	if sub.Status == "cancelled" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot renew a cancelled subscription"})
		return
	}
	months := sub.PeriodMonths
	if months <= 0 {
		months = 1
	}
	now := time.Now()
	newStart := sub.EndDate
	if newStart.Before(now) {
		newStart = now
	}
	newEnd := newStart.AddDate(0, months, 0)

	var input struct {
		Notes string `json:"notes"`
	}
	_ = c.ShouldBindJSON(&input)

	renewUpdates := map[string]interface{}{
		"start_date": newStart,
		"end_date":   newEnd,
	}
	// Complimentary plans always renew free of cost.
	if sub.Package != nil && sub.Package.IsComplimentary {
		renewUpdates["price"] = 0
	}
	err = utils.DB.Transaction(func(tx *gorm.DB) error {
		// Bare model: sub.Package is preloaded and GORM would re-save the
		// association, rewriting package_id.
		if err := tx.Model(&models.StoreSubscription{}).Where("id = ?", sub.ID).Updates(renewUpdates).Error; err != nil {
			return err
		}
		recordSubscriptionEvent(tx, actor, sub.StoreID, &sub.ID, "renewed",
			sub.PackageName, sub.PackageName, &newStart, &newEnd, sub.Price, input.Notes)
		return nil
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to renew subscription"})
		return
	}
	utils.DB.Preload("Package").Preload("Store").First(&sub, "id = ?", sub.ID)

	CreateAuditLog(actor.ID, actor.Name, "update", "store_subscription", &sub.ID,
		sub.PackageName, "Renewed store subscription", c.ClientIP(), c.GetHeader("User-Agent"),
		storeSubscriptionJSON(sub, now, true), "success", "")

	c.JSON(http.StatusOK, storeSubscriptionJSON(sub, now, true))
}

// AdminCancelSubscription marks a subscription cancelled; it stays in history.
func AdminCancelSubscription(c *gin.Context) {
	actor, err := loadActor(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}
	subID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid subscription id"})
		return
	}
	var sub models.StoreSubscription
	if err := utils.DB.First(&sub, "id = ?", subID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Subscription not found"})
		return
	}
	if sub.Status == "cancelled" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Subscription is already cancelled"})
		return
	}

	var input struct {
		Notes string `json:"notes"`
	}
	_ = c.ShouldBindJSON(&input)

	err = utils.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&sub).Update("status", "cancelled").Error; err != nil {
			return err
		}
		recordSubscriptionEvent(tx, actor, sub.StoreID, &sub.ID, "cancelled",
			sub.PackageName, "", &sub.StartDate, &sub.EndDate, sub.Price, input.Notes)
		return nil
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to cancel subscription"})
		return
	}
	utils.DB.Preload("Package").Preload("Store").First(&sub, "id = ?", sub.ID)

	CreateAuditLog(actor.ID, actor.Name, "update", "store_subscription", &sub.ID,
		sub.PackageName, "Cancelled store subscription", c.ClientIP(), c.GetHeader("User-Agent"),
		storeSubscriptionJSON(sub, time.Now(), true), "success", "")

	c.JSON(http.StatusOK, storeSubscriptionJSON(sub, time.Now(), true))
}
