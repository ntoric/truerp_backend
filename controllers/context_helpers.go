package controllers

import (
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// actorUserID returns the authenticated person (not the store data-scope owner).
func actorUserID(c *gin.Context) uuid.UUID {
	if v, ok := c.Get("actor_user_id"); ok {
		if id, ok := v.(uuid.UUID); ok {
			return id
		}
	}
	return c.MustGet("user_id").(uuid.UUID)
}

func currentStoreID(c *gin.Context) (uuid.UUID, bool) {
	if v, ok := c.Get("store_id"); ok {
		if id, ok := v.(uuid.UUID); ok && id != uuid.Nil {
			return id, true
		}
	}
	return uuid.Nil, false
}

func loadActor(c *gin.Context) (models.User, error) {
	var actor models.User
	err := utils.DB.First(&actor, "id = ?", actorUserID(c)).Error
	return actor, err
}
