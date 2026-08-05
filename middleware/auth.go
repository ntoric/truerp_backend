package middleware

import (
	"net/http"
	"strings"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func AuthRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Authorization header required"})
			c.Abort()
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid authorization header format"})
			c.Abort()
			return
		}

		claims, err := utils.ParseToken(parts[1])
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired token"})
			c.Abort()
			return
		}

		actorID := claims.UserID
		c.Set("actor_user_id", actorID)
		c.Set("user_name", claims.UserName)
		c.Set("email", claims.Email)
		c.Set("role", claims.Role)

		scopeUserID := actorID
		var activeStoreID *uuid.UUID

		if utils.IsSuperAdminRole(claims.Role) {
			headerStore := strings.TrimSpace(c.GetHeader("X-Store-ID"))
			resolved := false
			if headerStore != "" {
				if storeUUID, parseErr := uuid.Parse(headerStore); parseErr == nil {
					if store, storeErr := utils.FindStoreByID(utils.DB, storeUUID); storeErr == nil && store.IsActive {
						scopeUserID = store.OwnerUserID
						activeStoreID = &store.ID
						resolved = true
					}
				}
			}
			if !resolved && claims.StoreID != nil && *claims.StoreID != uuid.Nil {
				if store, storeErr := utils.FindStoreByID(utils.DB, *claims.StoreID); storeErr == nil && store.IsActive {
					scopeUserID = store.OwnerUserID
					activeStoreID = &store.ID
					resolved = true
				}
			}
			if !resolved {
				// Fall back to the first active store so backoffice data stays store-scoped.
				var first models.Store
				if err := utils.DB.Where("is_active = ?", true).Order("name ASC").First(&first).Error; err == nil {
					scopeUserID = first.OwnerUserID
					activeStoreID = &first.ID
				}
			}
		} else {
			var actor models.User
			if err := utils.DB.Select("id", "store_id", "is_active", "is_store_owner").First(&actor, "id = ?", actorID).Error; err != nil {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "User not found"})
				c.Abort()
				return
			}
			if !actor.IsActive {
				c.JSON(http.StatusForbidden, gin.H{"error": "Account is deactivated"})
				c.Abort()
				return
			}
			storeID := actor.StoreID
			if storeID == nil && claims.StoreID != nil {
				storeID = claims.StoreID
			}
			if storeID == nil {
				c.JSON(http.StatusForbidden, gin.H{"error": "No store assigned. Contact your administrator."})
				c.Abort()
				return
			}
			store, storeErr := utils.FindStoreByID(utils.DB, *storeID)
			if storeErr != nil {
				c.JSON(http.StatusForbidden, gin.H{"error": "Assigned store not found"})
				c.Abort()
				return
			}
			if !store.IsActive {
				c.JSON(http.StatusForbidden, gin.H{"error": "Assigned store is inactive"})
				c.Abort()
				return
			}
			scopeUserID = store.OwnerUserID
			activeStoreID = &store.ID
		}

		c.Set("user_id", scopeUserID)
		if activeStoreID != nil {
			c.Set("store_id", *activeStoreID)
			// Help clients replace a stale X-Store-ID (e.g. after DB recreate).
			c.Header("X-Active-Store-ID", activeStoreID.String())
		}
		c.Next()
	}
}

// SuperAdminRequired restricts access to owner / super_admin roles.
// Must be used after AuthRequired.
func SuperAdminRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		role, _ := c.Get("role")
		roleStr, _ := role.(string)
		if !utils.IsSuperAdminRole(roleStr) {
			c.JSON(http.StatusForbidden, gin.H{"error": "Super admin access required"})
			c.Abort()
			return
		}
		c.Next()
	}
}

func CORSMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, Accept, Origin, Cache-Control, X-Requested-With, X-Store-ID")
		c.Writer.Header().Set("Access-Control-Expose-Headers", "X-Active-Store-ID")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, PATCH, DELETE")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}
