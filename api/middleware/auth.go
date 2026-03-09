package middleware

import (
	"encoding/base64"
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

		if !IsAuthorized(c.GetHeader("Authorization"), c.Query("token"), cfg) {
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

		if IsAuthorized(c.GetHeader("Authorization"), c.Query("token"), cfg) {
			c.Set("authenticated", true)
		}

		c.Next()
	}
}

func IsAuthorized(authHeader, queryToken string, cfg *config.Config) bool {
	if cfg == nil {
		return false
	}

	if username, password, ok := parseBasicAuth(authHeader); ok {
		return username == cfg.Server.AdminUsername && password == cfg.Server.AdminPassword
	}

	token := strings.TrimSpace(authHeader)
	if token == "" {
		token = strings.TrimSpace(queryToken)
	}
	token = strings.TrimPrefix(token, "Bearer ")
	return token != "" && token == cfg.Server.AdminToken
}

func parseBasicAuth(header string) (string, string, bool) {
	header = strings.TrimSpace(header)
	if !strings.HasPrefix(header, "Basic ") {
		return "", "", false
	}

	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(header, "Basic "))
	if err != nil {
		return "", "", false
	}

	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}

	return parts[0], parts[1], true
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
