package controllers

import (
	"net/http"
	"strings"
	"time"
	"truerp/models"
	"truerp/services"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Announcement types drive the toast styling on the dashboard.
var announcementTypes = map[string]bool{
	"announcement": true,
	"reminder":     true,
	"release_note": true,
	"maintenance":  true,
	"urgent":       true,
	"note":         true,
}

func normalizeAnnouncementType(raw string) string {
	t := strings.ToLower(strings.TrimSpace(raw))
	if announcementTypes[t] {
		return t
	}
	return "announcement"
}

type announcementInput struct {
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	Type      string     `json:"type"`
	ImageURL  string     `json:"image_url"`
	LinkURL   string     `json:"link_url"`
	LinkLabel string     `json:"link_label"`
	IsActive  *bool      `json:"is_active"`
	StartsAt  *time.Time `json:"starts_at"`
	EndsAt    *time.Time `json:"ends_at"`
}

// ListActiveAnnouncements returns published announcements inside their
// schedule window. Readable by any signed-in user.
func ListActiveAnnouncements(c *gin.Context) {
	now := time.Now()
	var items []models.Announcement
	if err := utils.DB.
		Where("is_active = ?", true).
		Where("(starts_at IS NULL OR starts_at <= ?)", now).
		Where("(ends_at IS NULL OR ends_at >= ?)", now).
		Order("created_at DESC").
		Limit(10).
		Find(&items).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch announcements"})
		return
	}
	c.JSON(http.StatusOK, items)
}

// AdminListAnnouncements lists every announcement (any status) for the
// super-admin backoffice.
func AdminListAnnouncements(c *gin.Context) {
	var items []models.Announcement
	query := utils.DB.Order("created_at DESC")
	if t := strings.TrimSpace(c.Query("type")); t != "" {
		query = query.Where("type = ?", normalizeAnnouncementType(t))
	}
	if active := strings.TrimSpace(c.Query("active")); active != "" {
		query = query.Where("is_active = ?", active == "true")
	}
	if q := strings.TrimSpace(c.Query("q")); q != "" {
		like := "%" + q + "%"
		query = query.Where("title LIKE ? OR body LIKE ?", like, like)
	}
	if err := query.Find(&items).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch announcements"})
		return
	}
	c.JSON(http.StatusOK, items)
}

func findAnnouncement(c *gin.Context) (models.Announcement, bool) {
	var item models.Announcement
	if err := utils.DB.First(&item, "id = ?", c.Param("id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Announcement not found"})
		return item, false
	}
	return item, true
}

// AdminGetAnnouncement returns one announcement.
func AdminGetAnnouncement(c *gin.Context) {
	item, ok := findAnnouncement(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, item)
}

// AdminCreateAnnouncement publishes a new announcement.
func AdminCreateAnnouncement(c *gin.Context) {
	var input announcementInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Title is required"})
		return
	}
	if input.StartsAt != nil && input.EndsAt != nil && input.EndsAt.Before(*input.StartsAt) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "End time must be after start time"})
		return
	}

	var createdBy uuid.UUID
	if actor, ok := c.Get("actor_user_id"); ok {
		createdBy, _ = actor.(uuid.UUID)
	}

	item := models.Announcement{
		ID:          uuid.New(),
		Title:       title,
		Body:        input.Body,
		Type:        normalizeAnnouncementType(input.Type),
		ImageURL:    strings.TrimSpace(input.ImageURL),
		LinkURL:     strings.TrimSpace(input.LinkURL),
		LinkLabel:   strings.TrimSpace(input.LinkLabel),
		IsActive:    boolOr(input.IsActive, true),
		StartsAt:    input.StartsAt,
		EndsAt:      input.EndsAt,
		CreatedByID: createdBy,
	}
	if err := utils.DB.Create(&item).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create announcement"})
		return
	}
	c.JSON(http.StatusCreated, item)
}

// AdminUpdateAnnouncement updates an announcement. The image is replaced (and
// the old upload cleaned up) when image_url changes.
func AdminUpdateAnnouncement(c *gin.Context) {
	item, ok := findAnnouncement(c)
	if !ok {
		return
	}

	var input announcementInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if input.StartsAt != nil && input.EndsAt != nil && input.EndsAt.Before(*input.StartsAt) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "End time must be after start time"})
		return
	}

	if title := strings.TrimSpace(input.Title); title != "" {
		item.Title = title
	}
	item.Body = input.Body
	item.Type = normalizeAnnouncementType(input.Type)
	item.LinkURL = strings.TrimSpace(input.LinkURL)
	item.LinkLabel = strings.TrimSpace(input.LinkLabel)
	item.IsActive = boolOr(input.IsActive, item.IsActive)
	item.StartsAt = input.StartsAt
	item.EndsAt = input.EndsAt

	newImageURL := strings.TrimSpace(input.ImageURL)
	if newImageURL != item.ImageURL {
		cleanupAnnouncementImage(item.ImageURL)
		item.ImageURL = newImageURL
	}

	if err := utils.DB.Save(&item).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update announcement"})
		return
	}
	c.JSON(http.StatusOK, item)
}

// AdminDeleteAnnouncement removes an announcement and its uploaded image.
func AdminDeleteAnnouncement(c *gin.Context) {
	item, ok := findAnnouncement(c)
	if !ok {
		return
	}
	if err := utils.DB.Delete(&item).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete announcement"})
		return
	}
	cleanupAnnouncementImage(item.ImageURL)
	c.JSON(http.StatusOK, gin.H{"message": "Announcement deleted"})
}

// AdminUploadAnnouncementImage accepts a (typically cropped/resized) image and
// returns its public URL for use as announcement image_url.
func AdminUploadAnnouncementImage(c *gin.Context) {
	file, err := c.FormFile("image")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No file uploaded"})
		return
	}
	if !isValidImageFile(file) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid file type. Only JPG, PNG, GIF, and WebP are allowed"})
		return
	}

	var userID uuid.UUID
	if actor, ok := c.Get("actor_user_id"); ok {
		userID, _ = actor.(uuid.UUID)
	}

	storageService := services.GetDefaultStorageService()
	filePath := "announcements/" + services.GenerateUniquePath(file.Filename)
	publicURL, err := storageService.UploadFile(file, filePath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to upload file"})
		return
	}

	mediaFile := models.MediaFile{
		ID:           uuid.New(),
		UserID:       userID,
		FileName:     filePath,
		OriginalName: file.Filename,
		FilePath:     filePath,
		FileSize:     file.Size,
		MimeType:     file.Header.Get("Content-Type"),
		StorageType:  string(services.GetStorageConfig().Type),
		PublicURL:    publicURL,
		EntityType:   "announcement",
	}
	if err := utils.DB.Create(&mediaFile).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create media record"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"image_url": publicURL})
}

// cleanupAnnouncementImage best-effort deletes the media record + stored file
// for an announcement image previously uploaded through this module.
func cleanupAnnouncementImage(publicURL string) {
	if strings.TrimSpace(publicURL) == "" {
		return
	}
	var mediaFile models.MediaFile
	if err := utils.DB.Where("entity_type = ? AND public_url = ?", "announcement", publicURL).
		Order("created_at DESC").
		First(&mediaFile).Error; err != nil {
		return
	}
	storageService := services.GetDefaultStorageService()
	_ = storageService.DeleteFile(mediaFile.FilePath)
	_ = utils.DB.Delete(&mediaFile).Error
}
