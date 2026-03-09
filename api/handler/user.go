package handler

import (
	"net/http"
	"runtime"

	"emby-media-portal/internal/auth"
	"emby-media-portal/internal/database"
	"emby-media-portal/internal/ratelimit"
	"emby-media-portal/internal/stats"

	"github.com/gin-gonic/gin"
)

// UserHandler handles user-related API requests
type UserHandler struct {
	identifier     *auth.Identifier
	rulesManager   *ratelimit.RulesManager
	limiterManager *ratelimit.Manager
}

// NewUserHandler creates a new user handler
func NewUserHandler(identifier *auth.Identifier, rulesManager *ratelimit.RulesManager, limiterManager *ratelimit.Manager) *UserHandler {
	return &UserHandler{
		identifier:     identifier,
		rulesManager:   rulesManager,
		limiterManager: limiterManager,
	}
}

// UserResponse represents a user in API response
type UserResponse struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	UploadLimit   int64  `json:"upload_limit"`
	DownloadLimit int64  `json:"download_limit"`
}

type ActiveUsersResponse struct {
	Users []ActiveUserStatus `json:"users"`
}

type ActiveUserStatus struct {
	UserID          string `json:"user_id"`
	UserName        string `json:"user_name,omitempty"`
	SessionWatching bool   `json:"session_watching"`
	Downloading     bool   `json:"downloading"`
	DownloadBps     int64  `json:"download_bps"`
}

// ListUsers returns all users with their rules
func (h *UserHandler) ListUsers(c *gin.Context) {
	// Get all users from database
	rules, err := h.rulesManager.GetAllUserRules()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	users := make([]UserResponse, len(rules))
	for i, rule := range rules {
		users[i] = UserResponse{
			ID:            rule.UserID,
			Name:          rule.UserName,
			UploadLimit:   rule.UploadLimit,
			DownloadLimit: rule.DownloadLimit,
		}
	}

	c.JSON(http.StatusOK, users)
}

// SyncUsers syncs users from Emby server
func (h *UserHandler) SyncUsers(c *gin.Context) {
	users, err := h.identifier.GetAllUsers()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	synced := 0
	for _, user := range users {
		// Check if user already exists
		existing, _ := h.rulesManager.GetUserRule(user.ID)
		if existing == nil {
			// Create new user with default limits
			defaultUpload, defaultDownload := h.limiterManager.GetDefaults()
			rule := &ratelimit.UserRule{
				UserID:        user.ID,
				UserName:      user.Name,
				UploadLimit:   defaultUpload,
				DownloadLimit: defaultDownload,
			}
			if err := h.rulesManager.SetUserRule(rule); err == nil {
				synced++
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Users synced successfully",
		"synced":  synced,
		"total":   len(users),
	})
}

// GetUserRule returns a user's rate limit rule
func (h *UserHandler) GetUserRule(c *gin.Context) {
	userID := c.Param("id")
	if userID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user id required"})
		return
	}

	rule, err := h.rulesManager.GetUserRule(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if rule == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	c.JSON(http.StatusOK, UserResponse{
		ID:            rule.UserID,
		Name:          rule.UserName,
		UploadLimit:   rule.UploadLimit,
		DownloadLimit: rule.DownloadLimit,
	})
}

// UpdateUserRuleRequest represents the request body for updating user rules
type UpdateUserRuleRequest struct {
	Name          string `json:"name"`
	UploadLimit   int64  `json:"upload_limit"`
	DownloadLimit int64  `json:"download_limit"`
}

// UpdateUserRule updates a user's rate limit rule
func (h *UserHandler) UpdateUserRule(c *gin.Context) {
	userID := c.Param("id")
	if userID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user id required"})
		return
	}

	var req UpdateUserRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Get existing rule or create new one
	existing, _ := h.rulesManager.GetUserRule(userID)

	name := req.Name
	if name == "" && existing != nil {
		name = existing.UserName
	}

	rule := &ratelimit.UserRule{
		UserID:        userID,
		UserName:      name,
		UploadLimit:   req.UploadLimit,
		DownloadLimit: req.DownloadLimit,
	}

	if err := h.rulesManager.SetUserRule(rule); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "User rule updated successfully",
		"user": UserResponse{
			ID:            rule.UserID,
			Name:          rule.UserName,
			UploadLimit:   rule.UploadLimit,
			DownloadLimit: rule.DownloadLimit,
		},
	})
}

// DeleteUserRule deletes a user's rule
func (h *UserHandler) DeleteUserRule(c *gin.Context) {
	userID := c.Param("id")
	if userID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user id required"})
		return
	}

	existing, err := h.rulesManager.GetUserRule(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	defaultUpload, defaultDownload := h.limiterManager.GetDefaults()
	name := ""
	if existing != nil {
		name = existing.UserName
	}

	if err := h.rulesManager.SetUserRule(&ratelimit.UserRule{
		UserID:        userID,
		UserName:      name,
		UploadLimit:   defaultUpload,
		DownloadLimit: defaultDownload,
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "User rule reset successfully"})
}

func (h *UserHandler) ListActiveUsers(c *gin.Context) {
	active := make(map[string]ActiveUserStatus)

	sessionUsers, err := h.identifier.GetActiveSessionUsers()
	if err == nil {
		for userID, user := range sessionUsers {
			if userID != "" {
				item := active[userID]
				item.UserID = userID
				item.UserName = user.Name
				item.SessionWatching = true
				active[userID] = item
			}
		}
	}

	trafficUsers, err := stats.ListActiveUserTraffic(32 * 1024)
	if err == nil {
		for _, trafficUser := range trafficUsers {
			if trafficUser.UserID != "" {
				item := active[trafficUser.UserID]
				item.UserID = trafficUser.UserID
				if item.UserName == "" {
					item.UserName = trafficUser.UserName
				}
				item.Downloading = true
				item.DownloadBps = trafficUser.DownloadBps
				active[trafficUser.UserID] = item
			}
		}
	}

	users := make([]ActiveUserStatus, 0, len(active))
	for _, item := range active {
		users = append(users, item)
	}

	c.JSON(http.StatusOK, ActiveUsersResponse{Users: users})
}

// GetServerStats returns database stats
func (h *UserHandler) GetServerStats(c *gin.Context) {
	db := database.Get()
	if db == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database not available"})
		return
	}

	var userCount, serverCount int64
	db.QueryRow("SELECT COUNT(*) FROM users").Scan(&userCount)
	db.QueryRow("SELECT COUNT(*) FROM servers").Scan(&serverCount)
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	rates := stats.GetCurrentTransferRates()

	c.JSON(http.StatusOK, gin.H{
		"user_count":        userCount,
		"server_count":      serverCount,
		"memory_alloc":      mem.Alloc,
		"memory_heap_inuse": mem.HeapInuse,
		"upload_bps":        rates.UploadBps,
		"download_bps":      rates.DownloadBps,
		"active_uploads":    rates.ActiveUploads,
		"active_downloads":  rates.ActiveDownloads,
	})
}
