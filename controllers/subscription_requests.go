package controllers

import (
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

func subscriptionRequestJSON(r models.SubscriptionRequest) gin.H {
	out := gin.H{
		"id":                   r.ID,
		"store_id":             r.StoreID,
		"subscription_id":      r.SubscriptionID,
		"request_type":         r.RequestType,
		"target_package_id":    r.TargetPackageID,
		"target_package_name":  r.TargetPackageName,
		"current_package_name": r.CurrentPackageName,
		"reason":               r.Reason,
		"status":               r.Status,
		"requested_by_name":    r.RequestedByName,
		"resolved_by_name":     r.ResolvedByName,
		"resolved_at":          r.ResolvedAt,
		"resolution_notes":     r.ResolutionNotes,
		"created_at":           r.CreatedAt,
		"updated_at":           r.UpdatedAt,
	}
	if r.Store != nil {
		out["store"] = gin.H{
			"id":   r.Store.ID,
			"name": r.Store.Name,
			"code": r.Store.Code,
		}
	}
	if r.TargetPackage != nil {
		out["target_package"] = subscriptionPackageJSON(*r.TargetPackage, true)
	}
	return out
}

// latestStoreSubscription returns the store's newest non-cancelled subscription.
func latestStoreSubscription(storeID uuid.UUID) (models.StoreSubscription, error) {
	var sub models.StoreSubscription
	err := utils.DB.Preload("Package").Where("store_id = ? AND status <> ?", storeID, "cancelled").
		Order("created_at DESC").First(&sub).Error
	return sub, err
}

// CreateSubscriptionRequest lets a store user request a cancellation or a plan
// change (upgrade/downgrade). Only one pending request per store.
func CreateSubscriptionRequest(c *gin.Context) {
	actor, err := loadActor(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}
	store, ok := resolveRequestStore(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No active store"})
		return
	}

	var input struct {
		RequestType     string `json:"request_type"`
		TargetPackageID string `json:"target_package_id"`
		Reason          string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request data"})
		return
	}
	requestType := strings.TrimSpace(strings.ToLower(input.RequestType))
	if requestType != "cancel" && requestType != "upgrade" && requestType != "downgrade" && requestType != "change" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "request_type must be cancel, upgrade or downgrade"})
		return
	}

	var pending int64
	utils.DB.Model(&models.SubscriptionRequest{}).
		Where("store_id = ? AND status = ?", store.ID, "pending").Count(&pending)
	if pending > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "You already have a pending request. Wait for it to be reviewed or withdraw it."})
		return
	}

	sub, err := latestStoreSubscription(store.ID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No subscription to act on"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load subscription"})
		return
	}

	req := models.SubscriptionRequest{
		ID:                 uuid.New(),
		StoreID:            store.ID,
		SubscriptionID:     &sub.ID,
		CurrentPackageName: sub.PackageName,
		Reason:             strings.TrimSpace(input.Reason),
		Status:             "pending",
		RequestedByID:      &actor.ID,
		RequestedByName:    actor.Name,
	}

	if requestType == "cancel" {
		req.RequestType = "cancel"
	} else {
		targetID, err := uuid.Parse(strings.TrimSpace(input.TargetPackageID))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Select a target package"})
			return
		}
		var pkg models.SubscriptionPackage
		if err := utils.DB.First(&pkg, "id = ?", targetID).Error; err != nil || !pkg.IsActive {
			c.JSON(http.StatusNotFound, gin.H{"error": "Package not found"})
			return
		}
		if sub.PackageID != nil && *sub.PackageID == pkg.ID {
			c.JSON(http.StatusBadRequest, gin.H{"error": "You are already on this package"})
			return
		}
		// Normalize direction from the price comparison, regardless of what the
		// client labelled the request.
		switch {
		case pkg.Price > sub.Price:
			req.RequestType = "upgrade"
		case pkg.Price < sub.Price:
			req.RequestType = "downgrade"
		default:
			req.RequestType = "change"
		}
		req.TargetPackageID = &pkg.ID
		req.TargetPackageName = pkg.Name
	}

	if err := utils.DB.Create(&req).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to submit request"})
		return
	}

	eventType := req.RequestType + "_requested"
	recordSubscriptionEvent(utils.DB, actor, store.ID, &sub.ID, eventType,
		sub.PackageName, req.TargetPackageName, &sub.StartDate, &sub.EndDate, sub.Price,
		strings.TrimSpace("Request: "+input.Reason))

	utils.DB.Preload("Store").Preload("TargetPackage").First(&req, "id = ?", req.ID)
	c.JSON(http.StatusCreated, subscriptionRequestJSON(req))
}

// ListMySubscriptionRequests returns the resolved store's requests.
func ListMySubscriptionRequests(c *gin.Context) {
	store, ok := resolveRequestStore(c)
	if !ok {
		c.JSON(http.StatusOK, []gin.H{})
		return
	}
	var reqs []models.SubscriptionRequest
	if err := utils.DB.Preload("TargetPackage").Where("store_id = ?", store.ID).
		Order("created_at DESC").Limit(100).Find(&reqs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load requests"})
		return
	}
	out := make([]gin.H, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, subscriptionRequestJSON(r))
	}
	c.JSON(http.StatusOK, out)
}

// WithdrawSubscriptionRequest lets the owning store retract a pending request.
func WithdrawSubscriptionRequest(c *gin.Context) {
	store, ok := resolveRequestStore(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No active store"})
		return
	}
	reqID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request id"})
		return
	}
	var req models.SubscriptionRequest
	if err := utils.DB.First(&req, "id = ? AND store_id = ?", reqID, store.ID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Request not found"})
		return
	}
	if req.Status != "pending" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Only pending requests can be withdrawn"})
		return
	}
	if err := utils.DB.Model(&req).Updates(map[string]interface{}{
		"status":      "withdrawn",
		"resolved_at": time.Now(),
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to withdraw request"})
		return
	}
	utils.DB.Preload("Store").Preload("TargetPackage").First(&req, "id = ?", req.ID)
	c.JSON(http.StatusOK, subscriptionRequestJSON(req))
}

// AdminListSubscriptionRequests lists requests across stores (super admin).
func AdminListSubscriptionRequests(c *gin.Context) {
	query := utils.DB.Preload("Store").Preload("TargetPackage").Order("created_at DESC")
	if status := strings.TrimSpace(c.Query("status")); status != "" && status != "all" {
		query = query.Where("status = ?", status)
	}
	if storeID := strings.TrimSpace(c.Query("store_id")); storeID != "" {
		if parsed, err := uuid.Parse(storeID); err == nil {
			query = query.Where("store_id = ?", parsed)
		}
	}
	var reqs []models.SubscriptionRequest
	if err := query.Limit(500).Find(&reqs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load requests"})
		return
	}
	out := make([]gin.H, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, subscriptionRequestJSON(r))
	}
	c.JSON(http.StatusOK, out)
}

// AdminApproveSubscriptionRequest applies the requested change:
// cancel -> mark the subscription cancelled; upgrade/downgrade/change ->
// swap the subscription's package (logging the event accordingly).
func AdminApproveSubscriptionRequest(c *gin.Context) {
	actor, err := loadActor(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}
	reqID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request id"})
		return
	}
	var req models.SubscriptionRequest
	if err := utils.DB.First(&req, "id = ?", reqID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Request not found"})
		return
	}
	if req.Status != "pending" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Request is already resolved"})
		return
	}
	if req.SubscriptionID == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Request has no linked subscription"})
		return
	}
	var sub models.StoreSubscription
	if err := utils.DB.First(&sub, "id = ?", *req.SubscriptionID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Linked subscription not found"})
		return
	}

	var input struct {
		Notes string `json:"notes"`
	}
	_ = c.ShouldBindJSON(&input)

	now := time.Now()
	err = utils.DB.Transaction(func(tx *gorm.DB) error {
		resolution := map[string]interface{}{
			"status":           "approved",
			"resolved_by_id":   actor.ID,
			"resolved_by_name": actor.Name,
			"resolved_at":      now,
			"resolution_notes": strings.TrimSpace(input.Notes),
		}
		if err := tx.Model(&models.SubscriptionRequest{}).Where("id = ?", req.ID).Updates(resolution).Error; err != nil {
			return err
		}

		switch req.RequestType {
		case "cancel":
			if sub.Status != "cancelled" {
				if err := tx.Model(&models.StoreSubscription{}).Where("id = ?", sub.ID).
					Update("status", "cancelled").Error; err != nil {
					return err
				}
				recordSubscriptionEvent(tx, actor, sub.StoreID, &sub.ID, "cancelled",
					sub.PackageName, "", &sub.StartDate, &sub.EndDate, sub.Price,
					"Cancellation request approved. "+strings.TrimSpace(input.Notes))
			}
		default:
			if req.TargetPackageID == nil {
				return errors.New("request has no target package")
			}
			var pkg models.SubscriptionPackage
			if err := tx.First(&pkg, "id = ?", *req.TargetPackageID).Error; err != nil {
				return errors.New("target package no longer exists")
			}
			eventType := "changed"
			switch {
			case pkg.Price > sub.Price:
				eventType = "upgraded"
			case pkg.Price < sub.Price:
				eventType = "downgraded"
			}
			price := pkg.Price
			if pkg.IsComplimentary {
				price = 0
			}
			updates := map[string]interface{}{
				"package_id":   pkg.ID,
				"package_name": pkg.Name,
				"price":        price,
			}
			if sub.Status == "cancelled" {
				updates["status"] = "active"
			}
			if err := tx.Model(&models.StoreSubscription{}).Where("id = ?", sub.ID).Updates(updates).Error; err != nil {
				return err
			}
			recordSubscriptionEvent(tx, actor, sub.StoreID, &sub.ID, eventType,
				sub.PackageName, pkg.Name, &sub.StartDate, &sub.EndDate, price,
				"Plan change request approved. "+strings.TrimSpace(input.Notes))
		}
		return nil
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	utils.DB.Preload("Store").Preload("TargetPackage").First(&req, "id = ?", req.ID)
	c.JSON(http.StatusOK, subscriptionRequestJSON(req))
}

// AdminRejectSubscriptionRequest marks a pending request rejected.
func AdminRejectSubscriptionRequest(c *gin.Context) {
	actor, err := loadActor(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}
	reqID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request id"})
		return
	}
	var req models.SubscriptionRequest
	if err := utils.DB.First(&req, "id = ?", reqID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Request not found"})
		return
	}
	if req.Status != "pending" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Request is already resolved"})
		return
	}

	var input struct {
		Notes string `json:"notes"`
	}
	_ = c.ShouldBindJSON(&input)

	now := time.Now()
	err = utils.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.SubscriptionRequest{}).Where("id = ?", req.ID).Updates(map[string]interface{}{
			"status":           "rejected",
			"resolved_by_id":   actor.ID,
			"resolved_by_name": actor.Name,
			"resolved_at":      now,
			"resolution_notes": strings.TrimSpace(input.Notes),
		}).Error; err != nil {
			return err
		}
		recordSubscriptionEvent(tx, actor, req.StoreID, req.SubscriptionID, "request_rejected",
			req.CurrentPackageName, req.TargetPackageName, nil, nil, 0,
			"Request rejected. "+strings.TrimSpace(input.Notes))
		return nil
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reject request"})
		return
	}
	utils.DB.Preload("Store").Preload("TargetPackage").First(&req, "id = ?", req.ID)
	c.JSON(http.StatusOK, subscriptionRequestJSON(req))
}
