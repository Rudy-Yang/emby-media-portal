package middleware

import (
	"net/http"
	"strings"

	"emby-media-portal/internal/config"

	"github.com/gin-gonic/gin"
)

// AuthRequired checks for valid admin token
func AuthRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg := config.Get()
		if cfg == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "config not loaded"})
			c.Abort()
			return
		}

		// Get token from header or query
		token := c.GetHeader("Authorization")
		if token == "" {
			token = c.Query("token")
		}

		// Remove "Bearer " prefix if present
		token = strings.TrimPrefix(token, "Bearer ")

		// Check if token matches
		if token == "" || token != cfg.Server.AdminToken {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			c.Abort()
			return
		}

		c.Next()
	}
}

// OptionalAuth checks for token but doesn't require it
func OptionalAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg := config.Get()
		if cfg == nil {
			c.Next()
			return
		}

		token := c.GetHeader("Authorization")
		if token == "" {
			token = c.Query("token")
		}

		token = strings.TrimPrefix(token, "Bearer ")

		if token != "" && token == cfg.Server.AdminToken {
			c.Set("authenticated", true)
		}

		c.Next()
	}
}

// CORS middleware for API
func CORS() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Authorization")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}
