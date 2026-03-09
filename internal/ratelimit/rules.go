package ratelimit

import (
	"database/sql"
	"strings"

	"emby-media-portal/internal/database"
)

// UserRule represents rate limit rules for a user
type UserRule struct {
	UserID         string `json:"user_id"`
	UserName       string `json:"user_name"`
	UploadLimit    int64  `json:"upload_limit"`
	DownloadLimit  int64  `json:"download_limit"`
	TranscodeAllowed bool  `json:"transcode_allowed"`
}

// ServerRule represents rate limit rules for a server
type ServerRule struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	TotalLimit int64 `json:"total_limit"`
}

// ClientRule represents a shared limit rule for a client family or device.
type ClientRule struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	MatchType     string `json:"match_type"`
	MatchValue    string `json:"match_value"`
	UploadLimit   int64  `json:"upload_limit"`
	DownloadLimit int64  `json:"download_limit"`
}

// RulesManager manages rate limit rules with database persistence
type RulesManager struct {
	limiterManager *Manager
}

// NewRulesManager creates a new rules manager
func NewRulesManager(limiterManager *Manager) *RulesManager {
	return &RulesManager{
		limiterManager: limiterManager,
	}
}

// GetUserRule gets a user's rate limit rule from database
func (r *RulesManager) GetUserRule(userID string) (*UserRule, error) {
	db := database.Get()
	if db == nil {
		return nil, sql.ErrConnDone
	}

	rule := &UserRule{UserID: userID}
	err := db.QueryRow(
		"SELECT name, upload_limit, download_limit, transcode_allowed FROM users WHERE id = ?",
		userID,
	).Scan(&rule.UserName, &rule.UploadLimit, &rule.DownloadLimit, &rule.TranscodeAllowed)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return rule, nil
}

// GetAllUserRules gets all user rules from database
func (r *RulesManager) GetAllUserRules() ([]UserRule, error) {
	db := database.Get()
	if db == nil {
		return nil, sql.ErrConnDone
	}

	rows, err := db.Query(
		"SELECT id, name, upload_limit, download_limit, transcode_allowed FROM users",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var rules []UserRule
	for rows.Next() {
		var rule UserRule
		if err := rows.Scan(&rule.UserID, &rule.UserName, &rule.UploadLimit,
			&rule.DownloadLimit, &rule.TranscodeAllowed); err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}

	return rules, nil
}

// SetUserRule sets a user's rate limit rule
func (r *RulesManager) SetUserRule(rule *UserRule) error {
	db := database.Get()
	if db == nil {
		return sql.ErrConnDone
	}

	_, err := db.Exec(
		`INSERT INTO users (id, name, upload_limit, download_limit, transcode_allowed)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		 name = excluded.name,
		 upload_limit = excluded.upload_limit,
		 download_limit = excluded.download_limit,
		 transcode_allowed = excluded.transcode_allowed,
		 updated_at = CURRENT_TIMESTAMP`,
		rule.UserID, rule.UserName, rule.UploadLimit, rule.DownloadLimit, rule.TranscodeAllowed,
	)

	if err != nil {
		return err
	}

	// Update in-memory limiter
	r.limiterManager.UpdateUserLimiter(rule.UserID, rule.UploadLimit, rule.DownloadLimit)

	return nil
}

// DeleteUserRule deletes a user's rule
func (r *RulesManager) DeleteUserRule(userID string) error {
	db := database.Get()
	if db == nil {
		return sql.ErrConnDone
	}

	_, err := db.Exec("DELETE FROM users WHERE id = ?", userID)
	if err != nil {
		return err
	}

	r.limiterManager.RemoveUserLimiter(userID)
	return nil
}

// GetServerRule gets a server's rule from database
func (r *RulesManager) GetServerRule(serverID string) (*ServerRule, error) {
	db := database.Get()
	if db == nil {
		return nil, sql.ErrConnDone
	}

	rule := &ServerRule{ID: serverID}
	err := db.QueryRow(
		"SELECT name, url, total_limit FROM servers WHERE id = ?",
		serverID,
	).Scan(&rule.Name, &rule.URL, &rule.TotalLimit)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return rule, nil
}

// GetAllServerRules gets all server rules from database
func (r *RulesManager) GetAllServerRules() ([]ServerRule, error) {
	db := database.Get()
	if db == nil {
		return nil, sql.ErrConnDone
	}

	rows, err := db.Query("SELECT id, name, url, total_limit FROM servers")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var rules []ServerRule
	for rows.Next() {
		var rule ServerRule
		if err := rows.Scan(&rule.ID, &rule.Name, &rule.URL, &rule.TotalLimit); err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}

	return rules, nil
}

// SetServerRule sets a server's rule
func (r *RulesManager) SetServerRule(rule *ServerRule) error {
	db := database.Get()
	if db == nil {
		return sql.ErrConnDone
	}

	_, err := db.Exec(
		`INSERT INTO servers (id, name, url, total_limit)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		 name = excluded.name,
		 url = excluded.url,
		 total_limit = excluded.total_limit,
		 updated_at = CURRENT_TIMESTAMP`,
		rule.ID, rule.Name, rule.URL, rule.TotalLimit,
	)

	if err != nil {
		return err
	}

	r.limiterManager.UpdateServerLimiter(rule.ID, rule.TotalLimit)
	return nil
}

// DeleteServerRule deletes a server's rule
func (r *RulesManager) DeleteServerRule(serverID string) error {
	db := database.Get()
	if db == nil {
		return sql.ErrConnDone
	}

	_, err := db.Exec("DELETE FROM servers WHERE id = ?", serverID)
	return err
}

// LoadRulesFromDB loads all rules from database into memory
func (r *RulesManager) LoadRulesFromDB() error {
	userRules, err := r.GetAllUserRules()
	if err != nil {
		return err
	}

	for _, rule := range userRules {
		r.limiterManager.UpdateUserLimiter(rule.UserID, rule.UploadLimit, rule.DownloadLimit)
	}

	serverRules, err := r.GetAllServerRules()
	if err != nil {
		return err
	}

	for _, rule := range serverRules {
		r.limiterManager.UpdateServerLimiter(rule.ID, rule.TotalLimit)
	}

	clientRules, err := r.GetAllClientRules()
	if err != nil {
		return err
	}

	for _, rule := range clientRules {
		r.limiterManager.UpdateUserLimiter(clientLimiterKey(rule.ID), rule.UploadLimit, rule.DownloadLimit)
	}

	return nil
}

// GetClientRule gets a client rule by id.
func (r *RulesManager) GetClientRule(id string) (*ClientRule, error) {
	db := database.Get()
	if db == nil {
		return nil, sql.ErrConnDone
	}

	rule := &ClientRule{ID: id}
	err := db.QueryRow(
		"SELECT name, match_type, match_value, upload_limit, download_limit FROM client_rules WHERE id = ?",
		id,
	).Scan(&rule.Name, &rule.MatchType, &rule.MatchValue, &rule.UploadLimit, &rule.DownloadLimit)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return rule, nil
}

// GetAllClientRules returns all client rules.
func (r *RulesManager) GetAllClientRules() ([]ClientRule, error) {
	db := database.Get()
	if db == nil {
		return nil, sql.ErrConnDone
	}

	rows, err := db.Query(
		"SELECT id, name, match_type, match_value, upload_limit, download_limit FROM client_rules ORDER BY name, id",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var rules []ClientRule
	for rows.Next() {
		var rule ClientRule
		if err := rows.Scan(&rule.ID, &rule.Name, &rule.MatchType, &rule.MatchValue, &rule.UploadLimit, &rule.DownloadLimit); err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}

	return rules, nil
}

// SetClientRule creates or updates a client rule.
func (r *RulesManager) SetClientRule(rule *ClientRule) error {
	db := database.Get()
	if db == nil {
		return sql.ErrConnDone
	}

	_, err := db.Exec(
		`INSERT INTO client_rules (id, name, match_type, match_value, upload_limit, download_limit)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		 name = excluded.name,
		 match_type = excluded.match_type,
		 match_value = excluded.match_value,
		 upload_limit = excluded.upload_limit,
		 download_limit = excluded.download_limit,
		 updated_at = CURRENT_TIMESTAMP`,
		rule.ID, rule.Name, rule.MatchType, rule.MatchValue, rule.UploadLimit, rule.DownloadLimit,
	)
	if err != nil {
		return err
	}

	r.limiterManager.UpdateUserLimiter(clientLimiterKey(rule.ID), rule.UploadLimit, rule.DownloadLimit)
	return nil
}

// DeleteClientRule deletes a client rule.
func (r *RulesManager) DeleteClientRule(id string) error {
	db := database.Get()
	if db == nil {
		return sql.ErrConnDone
	}

	if _, err := db.Exec("DELETE FROM client_rules WHERE id = ?", id); err != nil {
		return err
	}

	r.limiterManager.RemoveUserLimiter(clientLimiterKey(id))
	return nil
}

// MatchClientRule finds the best matching client rule.
func (r *RulesManager) MatchClientRule(clientName, deviceID, userAgent string) (*ClientRule, error) {
	db := database.Get()
	if db == nil {
		return nil, sql.ErrConnDone
	}

	type candidate struct {
		matchType  string
		matchValue string
	}

	candidates := []candidate{
		{matchType: "device_id", matchValue: deviceID},
		{matchType: "client_name", matchValue: clientName},
		{matchType: "user_agent", matchValue: userAgent},
	}

	for _, candidate := range candidates {
		if candidate.matchValue == "" {
			continue
		}

		rule := &ClientRule{}
		err := db.QueryRow(
			`SELECT id, name, match_type, match_value, upload_limit, download_limit
			 FROM client_rules
			 WHERE match_type = ? AND match_value = ?`,
			candidate.matchType, normalizeClientMatchValue(candidate.matchType, candidate.matchValue),
		).Scan(&rule.ID, &rule.Name, &rule.MatchType, &rule.MatchValue, &rule.UploadLimit, &rule.DownloadLimit)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, err
		}

		return rule, nil
	}

	return nil, nil
}

func clientLimiterKey(id string) string {
	return "client-rule:" + id
}

func normalizeClientMatchValue(matchType, value string) string {
	value = strings.TrimSpace(value)
	switch matchType {
	case "client_name", "user_agent":
		return strings.ToLower(value)
	default:
		return value
	}
}
