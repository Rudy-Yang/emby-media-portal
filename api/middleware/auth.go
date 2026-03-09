package middleware

import (
	"encoding/base64"
	"net/http"
	"strings"

	"emby-media-portal/internal/config"
	"emby-media-portal/internal/session"

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

		username, ok := IsAuthorized(c, cfg)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			c.Abort()
			return
		}
		c.Set("authenticated", true)
		c.Set("admin_username", username)

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

		if username, ok := IsAuthorized(c, cfg); ok {
			c.Set("authenticated", true)
			c.Set("admin_username", username)
		}

		c.Next()
	}
}

func IsAuthorized(c *gin.Context, cfg *config.Config) (string, bool) {
	if cfg == nil {
		return "", false
	}

	authHeader := c.GetHeader("Authorization")
	queryToken := c.Query("token")

	if username, password, ok := parseBasicAuth(authHeader); ok {
		if username == cfg.Server.AdminUsername && config.VerifyAdminPassword(cfg, password) {
			return username, true
		}
		return "", false
	}

	if sessionToken, err := c.Cookie(session.CookieName); err == nil {
		if username, ok := session.DefaultManager.Validate(sessionToken); ok {
			return username, true
		}
	}

	token := strings.TrimSpace(authHeader)
	if token == "" {
		token = strings.TrimSpace(queryToken)
	}
	token = strings.TrimPrefix(token, "Bearer ")
	if token != "" && token == cfg.Server.AdminToken {
		return cfg.Server.AdminUsername, true
	}
	return "", false
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
