package handler

import (
	"net/http"
	"strconv"
	"time"

	"emby-media-portal/internal/stats"

	"github.com/gin-gonic/gin"
)

// StatsHandler handles stats-related API requests
type StatsHandler struct{}

// NewStatsHandler creates a new stats handler
func NewStatsHandler() *StatsHandler {
	return &StatsHandler{}
}

// StatsResponse represents traffic statistics response
type StatsResponse struct {
	UserID        string `json:"user_id,omitempty"`
	UserName      string `json:"user_name,omitempty"`
	ClientID      string `json:"client_id,omitempty"`
	ClientName    string `json:"client_name,omitempty"`
	DeviceID      string `json:"device_id,omitempty"`
	DeviceName    string `json:"device_name,omitempty"`
	ServerID      string `json:"server_id,omitempty"`
	TotalBytesIn  int64  `json:"total_bytes_in"`
	TotalBytesOut int64  `json:"total_bytes_out"`
	RequestCount  int64  `json:"request_count"`
}

// GetUserStats returns stats for a specific user
func (h *StatsHandler) GetUserStats(c *gin.Context) {
	userID := c.Param("id")
	if userID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user id required"})
		return
	}

	// Parse time range
	since := parseTimeRange(c.Query("since"))

	s, err := stats.GetUserStats(userID, since)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, StatsResponse{
		UserID:        s.UserID,
		UserName:      s.UserName,
		TotalBytesIn:  s.TotalBytesIn,
		TotalBytesOut: s.TotalBytesOut,
		RequestCount:  s.RequestCount,
	})
}

// GetAllStats returns stats for all users
func (h *StatsHandler) GetAllStats(c *gin.Context) {
	since := parseTimeRange(c.Query("since"))

	allStats, err := stats.GetAllUserStats(since)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	response := make([]StatsResponse, len(allStats))
	for i, s := range allStats {
		response[i] = StatsResponse{
			UserID:        s.UserID,
			UserName:      s.UserName,
			TotalBytesIn:  s.TotalBytesIn,
			TotalBytesOut: s.TotalBytesOut,
			RequestCount:  s.RequestCount,
		}
	}

	c.JSON(http.StatusOK, response)
}

// GetAllClientStats returns stats for all clients.
func (h *StatsHandler) GetAllClientStats(c *gin.Context) {
	since := parseTimeRange(c.Query("since"))

	allStats, err := stats.GetAllClientStats(since)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	response := make([]StatsResponse, len(allStats))
	for i, s := range allStats {
		response[i] = StatsResponse{
			ClientID:      s.ClientID,
			ClientName:    s.ClientName,
			DeviceID:      s.DeviceID,
			DeviceName:    s.DeviceName,
			TotalBytesIn:  s.TotalBytesIn,
			TotalBytesOut: s.TotalBytesOut,
			RequestCount:  s.RequestCount,
		}
	}

	c.JSON(http.StatusOK, response)
}

// GetClientStats returns stats for a specific client.
func (h *StatsHandler) GetClientStats(c *gin.Context) {
	clientID := c.Param("id")
	if clientID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "client id required"})
		return
	}

	since := parseTimeRange(c.Query("since"))

	s, err := stats.GetClientStats(clientID, since)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, StatsResponse{
		ClientID:      s.ClientID,
		ClientName:    s.ClientName,
		DeviceID:      s.DeviceID,
		DeviceName:    s.DeviceName,
		TotalBytesIn:  s.TotalBytesIn,
		TotalBytesOut: s.TotalBytesOut,
		RequestCount:  s.RequestCount,
	})
}

// GetServerStats returns stats for a specific server
func (h *StatsHandler) GetServerStats(c *gin.Context) {
	serverID := c.Param("id")
	if serverID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "server id required"})
		return
	}

	since := parseTimeRange(c.Query("since"))

	s, err := stats.GetServerStats(serverID, since)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, StatsResponse{
		ServerID:      s.ServerID,
		TotalBytesIn:  s.TotalBytesIn,
		TotalBytesOut: s.TotalBytesOut,
		RequestCount:  s.RequestCount,
	})
}

// GetTrafficSummary returns overall traffic totals for the time range.
func (h *StatsHandler) GetTrafficSummary(c *gin.Context) {
	since := parseTimeRange(c.Query("since"))

	summary, err := stats.GetTrafficSummary(since)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, StatsResponse{
		TotalBytesIn:  summary.TotalBytesIn,
		TotalBytesOut: summary.TotalBytesOut,
		RequestCount:  summary.RequestCount,
	})
}

// CleanStats cleans old statistics
func (h *StatsHandler) CleanStats(c *gin.Context) {
	daysStr := c.DefaultQuery("days", "30")
	days, err := strconv.Atoi(daysStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid days parameter"})
		return
	}

	olderThan := time.Duration(days) * 24 * time.Hour
	if err := stats.CleanOldStats(olderThan); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Old statistics cleaned successfully",
		"days":    days,
	})
}

func parseTimeRange(s string) time.Time {
	switch s {
	case "hour":
		return time.Now().Add(-1 * time.Hour)
	case "day":
		return time.Now().Add(-24 * time.Hour)
	case "week":
		return time.Now().Add(-7 * 24 * time.Hour)
	case "month":
		return time.Now().Add(-30 * 24 * time.Hour)
	default:
		return time.Now().Add(-24 * time.Hour) // Default to 24 hours
	}
}
