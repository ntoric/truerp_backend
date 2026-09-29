package controllers

import (
	"errors"
	"net/http"
	"truerp/utils"

	"github.com/gin-gonic/gin"
)

// HealthCheck reports service health, including database connectivity.
// Returns 200 when healthy and 503 when the database is unreachable, so
// Docker healthchecks and load balancers can react to a dead database.
func HealthCheck(c *gin.Context) {
	var dbErr error
	switch {
	case utils.DB == nil:
		dbErr = errors.New("database not initialized")
	default:
		sqlDB, err := utils.DB.DB()
		if err != nil {
			dbErr = err
		} else {
			dbErr = sqlDB.Ping()
		}
	}

	if dbErr != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status":   "error",
			"database": "down",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":   "ok",
		"database": "up",
	})
}
