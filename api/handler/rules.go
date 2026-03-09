package handler

import (
	"net/http"

	"emby-media-portal/internal/config"
	"emby-media-portal/internal/ratelimit"

	"github.com/gin-gonic/gin"
)

// RulesHandler handles rules-related API requests
type RulesHandler struct {
	rulesManager   *ratelimit.RulesManager
	limiterManager *ratelimit.Manager
}

// NewRulesHandler creates a new rules handler
func NewRulesHandler(rulesManager *ratelimit.RulesManager, limiterManager *ratelimit.Manager) *RulesHandler {
	return &RulesHandler{
		rulesManager:   rulesManager,
		limiterManager: limiterManager,
	}
}

// ServerResponse represents a server in API response
type ServerResponse struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	URL        string `json:"url"`
	TotalLimit int64  `json:"total_limit"`
}

// ListServers returns all server rules
func (h *RulesHandler) ListServers(c *gin.Context) {
	rules, err := h.rulesManager.GetAllServerRules()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	servers := make([]ServerResponse, len(rules))
	for i, rule := range rules {
		servers[i] = ServerResponse{
			ID:         rule.ID,
			Name:       rule.Name,
			URL:        rule.URL,
			TotalLimit: rule.TotalLimit,
		}
	}

	c.JSON(http.StatusOK, servers)
}

// GetServerRule returns a server's rule
func (h *RulesHandler) GetServerRule(c *gin.Context) {
	serverID := c.Param("id")
	if serverID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "server id required"})
		return
	}

	rule, err := h.rulesManager.GetServerRule(serverID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if rule == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
		return
	}

	c.JSON(http.StatusOK, ServerResponse{
		ID:         rule.ID,
		Name:       rule.Name,
		URL:        rule.URL,
		TotalLimit: rule.TotalLimit,
	})
}

// CreateServerRequest represents the request body for creating/updating servers
type CreateServerRequest struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	URL        string `json:"url"`
	TotalLimit int64  `json:"total_limit"`
}

// CreateServer creates or updates a server rule
func (h *RulesHandler) CreateServer(c *gin.Context) {
	var req CreateServerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.ID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "server id required"})
		return
	}

	rule := &ratelimit.ServerRule{
		ID:         req.ID,
		Name:       req.Name,
		URL:        req.URL,
		TotalLimit: req.TotalLimit,
	}

	if err := h.rulesManager.SetServerRule(rule); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Server rule saved successfully",
		"server": ServerResponse{
			ID:         rule.ID,
			Name:       rule.Name,
			URL:        rule.URL,
			TotalLimit: rule.TotalLimit,
		},
	})
}

// DeleteServer deletes a server rule
func (h *RulesHandler) DeleteServer(c *gin.Context) {
	serverID := c.Param("id")
	if serverID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "server id required"})
		return
	}

	if err := h.rulesManager.DeleteServerRule(serverID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Server rule deleted successfully"})
}

// DefaultLimitsResponse represents default limits
type DefaultLimitsResponse struct {
	DefaultUpload        int64  `json:"default_upload"`
	DefaultDownload      int64  `json:"default_download"`
	GlobalLimit          int64  `json:"global_limit"`
	EmbyURL              string `json:"emby_url"`
	AdminUsername        string `json:"admin_username"`
	EmbyAPIKeyConfigured bool   `json:"emby_api_key_configured"`
	AdminPasswordSet     bool   `json:"admin_password_set"`
}

// GetDefaultLimits returns default rate limits
func (h *RulesHandler) GetDefaultLimits(c *gin.Context) {
	upload, download := h.limiterManager.GetDefaults()
	globalLimiter := h.limiterManager.GetGlobalLimiter()

	var globalLimit int64
	if globalLimiter != nil {
		globalLimit, _ = globalLimiter.GetLimits()
	}

	c.JSON(http.StatusOK, DefaultLimitsResponse{
		DefaultUpload:        upload,
		DefaultDownload:      download,
		GlobalLimit:          globalLimit,
		EmbyURL:              currentConfigValue(func(cfg *config.Config) string { return cfg.Emby.URL }),
		AdminUsername:        currentConfigValue(func(cfg *config.Config) string { return cfg.Server.AdminUsername }),
		EmbyAPIKeyConfigured: currentConfigValue(func(cfg *config.Config) string { return cfg.Emby.APIKey }) != "",
		AdminPasswordSet:     currentConfigValue(func(cfg *config.Config) string { return cfg.Server.AdminPassword }) != "",
	})
}

// UpdateDefaultLimitsRequest represents request for updating defaults
type UpdateDefaultLimitsRequest struct {
	DefaultUpload   int64  `json:"default_upload"`
	DefaultDownload int64  `json:"default_download"`
	GlobalLimit     int64  `json:"global_limit"`
	EmbyURL         string `json:"emby_url"`
	EmbyAPIKey      string `json:"emby_api_key"`
	AdminUsername   string `json:"admin_username"`
	AdminPassword   string `json:"admin_password"`
}

// UpdateDefaultLimits updates default rate limits
func (h *RulesHandler) UpdateDefaultLimits(c *gin.Context) {
	var req UpdateDefaultLimitsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.limiterManager.UpdateDefaults(req.DefaultUpload, req.DefaultDownload)
	h.limiterManager.UpdateGlobalLimit(req.GlobalLimit)

	cfg := config.Get()
	if cfg == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "config not loaded"})
		return
	}

	updated := *cfg
	updated.Emby = cfg.Emby
	updated.Server = cfg.Server
	updated.Backend = cfg.Backend
	updated.RateLimits = cfg.RateLimits
	updated.Database = cfg.Database
	updated.RateLimits.DefaultUpload = req.DefaultUpload
	updated.RateLimits.DefaultDownload = req.DefaultDownload
	updated.RateLimits.GlobalLimit = req.GlobalLimit
	if req.EmbyURL != "" {
		updated.Emby.URL = req.EmbyURL
	}
	if req.EmbyAPIKey != "" {
		updated.Emby.APIKey = req.EmbyAPIKey
	}
	if req.AdminUsername != "" {
		updated.Server.AdminUsername = req.AdminUsername
	}
	if req.AdminPassword != "" {
		updated.Server.AdminPassword = req.AdminPassword
	}

	config.Update(&updated)
	if err := config.Save(&updated); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Settings updated successfully",
		"defaults": DefaultLimitsResponse{
			DefaultUpload:        req.DefaultUpload,
			DefaultDownload:      req.DefaultDownload,
			GlobalLimit:          req.GlobalLimit,
			EmbyURL:              updated.Emby.URL,
			AdminUsername:        updated.Server.AdminUsername,
			EmbyAPIKeyConfigured: updated.Emby.APIKey != "",
			AdminPasswordSet:     updated.Server.AdminPassword != "",
		},
	})
}

func currentConfigValue(selector func(*config.Config) string) string {
	cfg := config.Get()
	if cfg == nil {
		return ""
	}
	return selector(cfg)
}
