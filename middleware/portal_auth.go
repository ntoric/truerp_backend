package middleware

import (
	"truerp/utils"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func PortalAuthRequired() gin.HandlerFunc {
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
		if err != nil || claims.Role != "portal" || claims.PartyID == uuid.Nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired portal session"})
			c.Abort()
			return
		}

		c.Set("user_id", claims.UserID)
		c.Set("party_id", claims.PartyID)
		c.Set("party_name", claims.UserName)
		c.Set("portal_phone", claims.Email)
		c.Next()
	}
}
