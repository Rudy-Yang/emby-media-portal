package transcode

import (
	"database/sql"
	"net/http"
	"strings"

	"emby-media-portal/internal/database"
)

// Controller handles transcoding request control
type Controller struct {
	defaultAllowed bool
}

// NewController creates a new transcode controller
func NewController(defaultAllowed bool) *Controller {
	return &Controller{
		defaultAllowed: defaultAllowed,
	}
}

// IsTranscodeRequest checks if the request is a transcode request
func (c *Controller) IsTranscodeRequest(r *http.Request) bool {
	path := r.URL.Path
	query := r.URL.RawQuery

	// Check URL patterns for transcode requests
	if strings.Contains(path, "/video") {
		// Check for transcode parameters
		if strings.Contains(query, "Transcode") ||
			strings.Contains(query, "VideoCodec") ||
			strings.Contains(query, "AudioCodec") ||
			strings.Contains(query, "MaxStreamingBitrate") ||
			strings.Contains(query, "VideoBitrate") ||
			strings.Contains(query, "AudioBitrate") {
			return true
		}
	}

	// Check for /videos/<id>/master.m3u8 with transcode params
	if strings.HasSuffix(path, "/master.m3u8") && strings.Contains(query, "Transcode") {
		return true
	}

	// Check for direct stream vs transcode
	// Direct play typically has "DirectStream" or "Static" in params
	if strings.Contains(query, "DirectStream=true") ||
		strings.Contains(query, "Static=true") {
		return false
	}

	// Check media source header
	mediaSource := r.URL.Query().Get("MediaSourceId")
	if mediaSource != "" && strings.Contains(query, "Transcode") {
		return true
	}

	return false
}

// IsUserAllowed checks if a user is allowed to transcode
func (c *Controller) IsUserAllowed(userID string) (bool, error) {
	db := database.Get()
	if db == nil {
		return c.defaultAllowed, nil
	}

	var allowed bool
	err := db.QueryRow(
		"SELECT transcode_allowed FROM users WHERE id = ?",
		userID,
	).Scan(&allowed)

	if err == sql.ErrNoRows {
		return c.defaultAllowed, nil
	}
	if err != nil {
		return c.defaultAllowed, err
	}

	return allowed, nil
}

// SetUserPermission sets transcode permission for a user
func (c *Controller) SetUserPermission(userID string, allowed bool) error {
	db := database.Get()
	if db == nil {
		return sql.ErrConnDone
	}

	_, err := db.Exec(
		`INSERT INTO users (id, transcode_allowed)
		 VALUES (?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		 transcode_allowed = excluded.transcode_allowed,
		 updated_at = CURRENT_TIMESTAMP`,
		userID, allowed,
	)

	return err
}

// GetUserPermissions gets all user transcode permissions
func (c *Controller) GetUserPermissions() (map[string]bool, error) {
	db := database.Get()
	if db == nil {
		return nil, sql.ErrConnDone
	}

	rows, err := db.Query("SELECT id, transcode_allowed FROM users")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	permissions := make(map[string]bool)
	for rows.Next() {
		var id string
		var allowed bool
		if err := rows.Scan(&id, &allowed); err != nil {
			return nil, err
		}
		permissions[id] = allowed
	}

	return permissions, nil
}

// ShouldBlockTranscode checks if a transcode request should be blocked
func (c *Controller) ShouldBlockTranscode(r *http.Request, userID string) (bool, error) {
	// If not a transcode request, allow
	if !c.IsTranscodeRequest(r) {
		return false, nil
	}

	// Check user permission
	allowed, err := c.IsUserAllowed(userID)
	if err != nil {
		return false, err
	}

	// Block if not allowed
	return !allowed, nil
}
