package handler

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"emby-media-portal/internal/ratelimit"
	"emby-media-portal/internal/stats"

	"github.com/gin-gonic/gin"
)

// ClientHandler handles client-level rule APIs.
type ClientHandler struct {
	rulesManager   *ratelimit.RulesManager
	limiterManager *ratelimit.Manager
}

// NewClientHandler creates a new client handler.
func NewClientHandler(rulesManager *ratelimit.RulesManager, limiterManager *ratelimit.Manager) *ClientHandler {
	return &ClientHandler{
		rulesManager:   rulesManager,
		limiterManager: limiterManager,
	}
}

// ClientRuleResponse represents a client rule in API responses.
type ClientRuleResponse struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	MatchType     string `json:"match_type"`
	MatchValue    string `json:"match_value"`
	UploadLimit   int64  `json:"upload_limit"`
	DownloadLimit int64  `json:"download_limit"`
}

type DiscoveredClientResponse struct {
	ClientName   string `json:"client_name"`
	DeviceName   string `json:"device_name"`
	UserAgent    string `json:"user_agent"`
	LastSeen     string `json:"last_seen"`
	RequestCount int64  `json:"request_count"`
}

// SaveClientRuleRequest represents the request body for client rules.
type SaveClientRuleRequest struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	MatchType     string `json:"match_type"`
	MatchValue    string `json:"match_value"`
	UploadLimit   int64  `json:"upload_limit"`
	DownloadLimit int64  `json:"download_limit"`
}

// ListClients lists all client rules.
func (h *ClientHandler) ListClients(c *gin.Context) {
	rules, err := h.rulesManager.GetAllClientRules()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	response := make([]ClientRuleResponse, len(rules))
	for i, rule := range rules {
		response[i] = clientRuleToResponse(rule)
	}

	c.JSON(http.StatusOK, response)
}

func (h *ClientHandler) ListDiscoveredClients(c *gin.Context) {
	limit := 24
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid limit"})
			return
		}
		limit = parsed
	}

	items, err := stats.ListObservedClients(limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	response := make([]DiscoveredClientResponse, len(items))
	for i, item := range items {
		response[i] = DiscoveredClientResponse{
			ClientName:   item.ClientName,
			DeviceName:   item.DeviceName,
			UserAgent:    item.UserAgent,
			LastSeen:     item.LastSeen,
			RequestCount: item.RequestCount,
		}
	}

	c.JSON(http.StatusOK, response)
}

// GetClientRule returns a single client rule.
func (h *ClientHandler) GetClientRule(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "client rule id required"})
		return
	}

	rule, err := h.rulesManager.GetClientRule(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if rule == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "client rule not found"})
		return
	}

	c.JSON(http.StatusOK, clientRuleToResponse(*rule))
}

// SaveClientRule creates or updates a client rule.
func (h *ClientHandler) SaveClientRule(c *gin.Context) {
	var req SaveClientRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	req.MatchType = strings.TrimSpace(req.MatchType)
	if req.MatchType == "" {
		req.MatchType = "user_agent"
	}
	req.Name = normalizeClientRuleValue(req.MatchType, req.Name)
	req.MatchValue = normalizeClientRuleValue(req.MatchType, req.MatchValue)
	if req.MatchType == "user_agent" {
		if req.Name == "" {
			req.Name = req.MatchValue
		}
		req.MatchValue = req.Name
	}
	if req.ID == "" {
		req.ID = strings.TrimSpace(c.Param("id"))
	}
	if req.ID == "" {
		req.ID = slugifyClientRuleID(req.Name)
	}
	if req.ID == "" {
		req.ID = slugifyClientRuleID(req.MatchValue)
	}
	if req.ID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "client rule id required"})
		return
	}
	if !isValidMatchType(req.MatchType) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "match_type must be one of client_name, device_id, user_agent"})
		return
	}
	if req.MatchValue == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "match_value required"})
		return
	}

	rule := &ratelimit.ClientRule{
		ID:            req.ID,
		Name:          strings.TrimSpace(req.Name),
		MatchType:     req.MatchType,
		MatchValue:    req.MatchValue,
		UploadLimit:   req.UploadLimit,
		DownloadLimit: req.DownloadLimit,
	}

	if rule.Name == "" {
		rule.Name = req.MatchValue
	}

	if err := h.rulesManager.SetClientRule(rule); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Client rule saved successfully",
		"client":  clientRuleToResponse(*rule),
	})
}

// DeleteClientRule deletes a client rule.
func (h *ClientHandler) DeleteClientRule(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "client rule id required"})
		return
	}

	if err := h.rulesManager.DeleteClientRule(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Client rule deleted successfully"})
}

func clientRuleToResponse(rule ratelimit.ClientRule) ClientRuleResponse {
	return ClientRuleResponse{
		ID:            rule.ID,
		Name:          rule.Name,
		MatchType:     rule.MatchType,
		MatchValue:    rule.MatchValue,
		UploadLimit:   rule.UploadLimit,
		DownloadLimit: rule.DownloadLimit,
	}
}

func normalizeClientRuleValue(matchType, value string) string {
	value = strings.TrimSpace(value)
	switch matchType {
	case "client_name":
		return strings.ToLower(value)
	case "user_agent":
		if idx := strings.Index(value, "/"); idx >= 0 {
			value = value[:idx]
		}
		return strings.ToLower(value)
	default:
		return value
	}
}

func isValidMatchType(matchType string) bool {
	switch matchType {
	case "client_name", "device_id", "user_agent":
		return true
	default:
		return false
	}
}

var clientRuleSlugPattern = regexp.MustCompile(`[^a-z0-9]+`)

func slugifyClientRuleID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = clientRuleSlugPattern.ReplaceAllString(value, "-")
	return strings.Trim(value, "-")
}
