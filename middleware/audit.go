package middleware

import (
	"truerp/controllers"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// AuditMiddleware logs user actions automatically
func AuditMiddleware(action, entityType string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Get user info if authenticated
		var userID uuid.UUID
		var userName string
		if uid, exists := c.Get("user_id"); exists {
			userID = uid.(uuid.UUID)
		}
		if uname, exists := c.Get("user_name"); exists {
			userName = uname.(string)
		}

		// Get client info
		ipAddress := c.ClientIP()
		userAgent := c.GetHeader("User-Agent")

		// Process the request
		c.Next()

		// Log the action after request is processed
		status := "success"
		errorMessage := ""
		if len(c.Errors) > 0 {
			status = "failed"
			errorMessage = c.Errors.String()
		}

		// Only log if user is authenticated
		if userID != uuid.Nil {
			// Determine entity ID and name based on context
			var entityID *uuid.UUID
			var entityName string

			// Try to get entity ID from response or params
			if eid, exists := c.Get("entity_id"); exists {
				if id, ok := eid.(uuid.UUID); ok {
					entityID = &id
				}
			}

			// Get entity name if available
			if ename, exists := c.Get("entity_name"); exists {
				entityName = ename.(string)
			}

			// Create audit log
			description := action + " " + entityType
			if entityName != "" {
				description += ": " + entityName
			}

			controllers.CreateAuditLog(
				userID,
				userName,
				action,
				entityType,
				entityID,
				entityName,
				description,
				ipAddress,
				userAgent,
				nil, // changes - can be populated by individual controllers
				status,
				errorMessage,
			)
		}
	}
}

// GetClientIP extracts the real client IP address
func GetClientIP(c *gin.Context) string {
	// Check for X-Forwarded-For header (proxy/load balancer)
	if xff := c.GetHeader("X-Forwarded-For"); xff != "" {
		return xff
	}
	// Check for X-Real-IP header
	if xri := c.GetHeader("X-Real-IP"); xri != "" {
		return xri
	}
	// Fall back to RemoteAddr
	return c.ClientIP()
}
