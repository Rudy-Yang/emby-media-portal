package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"emby-media-portal/internal/config"
)

type UserInfo struct {
	ID      string `json:"Id"`
	Name    string `json:"Name"`
	IsAdmin bool   `json:"Policy:IsAdministrator"`
}

type ClientInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ClientName string `json:"client_name"`
	DeviceID   string `json:"device_id"`
	DeviceName string `json:"device_name"`
	UserAgent  string `json:"user_agent"`
	UserID     string `json:"user_id"`
	Token      string `json:"-"`
}

type CachedUser struct {
	User      *UserInfo
	ExpiresAt time.Time
}

type sessionInfo struct {
	UserID     string `json:"UserId"`
	UserName   string `json:"UserName"`
	Client     string `json:"Client"`
	DeviceID   string `json:"DeviceId"`
	DeviceName string `json:"DeviceName"`
}

type Identifier struct {
	client *http.Client
	cache  map[string]*CachedUser
	mu     sync.RWMutex
}

func NewIdentifier() *Identifier {
	return &Identifier{
		client: &http.Client{Timeout: 10 * time.Second},
		cache:  make(map[string]*CachedUser),
	}
}

// IdentifyUser extracts user info from request
func (i *Identifier) IdentifyUser(r *http.Request) (*UserInfo, error) {
	if userID := i.extractUserID(r); userID != "" {
		return &UserInfo{ID: userID}, nil
	}

	// Try to get token from various sources
	token := i.extractToken(r)
	if token == "" {
		return nil, nil // No token, let request pass through
	}

	// Check cache first
	if user := i.getFromCache(token); user != nil {
		return user, nil
	}

	client := i.IdentifyClient(r)

	// Query Emby API
	user, err := i.queryEmbyAPI(token, client)
	if err != nil {
		return nil, err
	}

	// Cache the result
	if user != nil {
		i.addToCache(token, user)
	}

	return user, nil
}

// IdentifyClient extracts client metadata from the request for client-level rules and stats.
func (i *Identifier) IdentifyClient(r *http.Request) *ClientInfo {
	if r == nil {
		return nil
	}

	authValues := embyAuthorizationValues(r)

	clientName := firstNonEmpty(
		authValues["Client"],
		r.Header.Get("X-Emby-Client"),
		r.URL.Query().Get("Client"),
	)
	deviceID := firstNonEmpty(
		authValues["DeviceId"],
		r.Header.Get("X-Emby-Device-Id"),
		r.URL.Query().Get("DeviceId"),
	)
	deviceName := firstNonEmpty(
		authValues["Device"],
		r.Header.Get("X-Emby-Device-Name"),
		r.URL.Query().Get("Device"),
	)
	userAgent := strings.TrimSpace(r.UserAgent())
	if clientName == "" {
		clientName = detectClientNameFromUserAgent(userAgent)
	}

	userID := i.extractUserID(r)
	clientID := ""
	switch {
	case clientName != "":
		clientID = "client:" + normalizeClientKey(clientName)
	case deviceID != "":
		clientID = "device:" + normalizeClientKey(deviceID)
	case userAgent != "":
		clientID = "ua:" + normalizeClientKey(userAgent)
	}

	if clientID == "" && deviceName == "" {
		return nil
	}

	name := firstNonEmpty(clientName, deviceName, userAgent, "Unknown Client")

	return &ClientInfo{
		ID:         clientID,
		Name:       name,
		ClientName: clientName,
		DeviceID:   deviceID,
		DeviceName: deviceName,
		UserAgent:  userAgent,
		UserID:     userID,
		Token:      i.extractToken(r),
	}
}

func (i *Identifier) extractToken(r *http.Request) string {
	// Check X-Emby-Token header
	if token := r.Header.Get("X-Emby-Token"); token != "" {
		return token
	}

	if token := r.Header.Get("X-MediaBrowser-Token"); token != "" {
		return token
	}

	// Check URL parameter
	if token := r.URL.Query().Get("X-Emby-Token"); token != "" {
		return token
	}

	if token := r.URL.Query().Get("api_key"); token != "" {
		return token
	}

	authValues := embyAuthorizationValues(r)
	if token := authValues["Token"]; token != "" {
		return token
	}

	if token := authValues["ApiKey"]; token != "" {
		return token
	}

	// Check DeviceId as fallback
	if deviceId := r.Header.Get("X-Emby-Device-Id"); deviceId != "" {
		return deviceId
	}

	return ""
}

func (i *Identifier) extractUserID(r *http.Request) string {
	if r == nil {
		return ""
	}

	if userID := strings.TrimSpace(r.Header.Get("X-Emby-User-Id")); userID != "" {
		return userID
	}

	if userID := strings.TrimSpace(r.URL.Query().Get("UserId")); userID != "" {
		return userID
	}

	authValues := embyAuthorizationValues(r)
	if userID := strings.TrimSpace(authValues["UserId"]); userID != "" {
		return userID
	}

	return ""
}

func (i *Identifier) getFromCache(token string) *UserInfo {
	i.mu.RLock()
	defer i.mu.RUnlock()

	cached, ok := i.cache[token]
	if !ok {
		return nil
	}

	if time.Now().After(cached.ExpiresAt) {
		return nil
	}

	return cached.User
}

func (i *Identifier) addToCache(token string, user *UserInfo) {
	i.mu.Lock()
	defer i.mu.Unlock()

	i.cache[token] = &CachedUser{
		User:      user,
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}
}

func (i *Identifier) queryEmbyAPI(token string, client *ClientInfo) (*UserInfo, error) {
	cfg := config.Get()
	if cfg == nil {
		return nil, fmt.Errorf("config not loaded")
	}

	sessionUser, sessionErr := i.querySessionUser(cfg.Emby.URL, token, client)
	if sessionErr == nil && sessionUser != nil {
		return sessionUser, nil
	}

	req, err := http.NewRequest("GET", fmt.Sprintf("%s/Users/Me", cfg.Emby.URL), nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("X-Emby-Token", token)

	resp, err := i.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("emby api returned status %d", resp.StatusCode)
	}

	var user UserInfo
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, err
	}

	return &user, nil
}

func (i *Identifier) querySessionUser(baseURL, token string, client *ClientInfo) (*UserInfo, error) {
	req, err := http.NewRequest("GET", fmt.Sprintf("%s/Sessions", baseURL), nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("X-Emby-Token", token)

	query := req.URL.Query()
	if client != nil {
		if client.DeviceID != "" {
			query.Set("deviceId", client.DeviceID)
			query.Set("X-Emby-Device-Id", client.DeviceID)
		}
		if client.ClientName != "" {
			query.Set("X-Emby-Client", client.ClientName)
		}
		if client.DeviceName != "" {
			query.Set("X-Emby-Device-Name", client.DeviceName)
		}
	}
	query.Set("X-Emby-Token", token)
	req.URL.RawQuery = query.Encode()

	resp, err := i.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("emby sessions api returned status %d", resp.StatusCode)
	}

	var sessions []sessionInfo
	if err := json.NewDecoder(resp.Body).Decode(&sessions); err != nil {
		return nil, err
	}

	if len(sessions) == 0 {
		return nil, nil
	}

	if client != nil {
		for _, session := range sessions {
			if sameSession(session, client) && session.UserID != "" {
				return &UserInfo{ID: session.UserID, Name: session.UserName}, nil
			}
		}
	}

	userByID := map[string]string{}
	for _, session := range sessions {
		if session.UserID == "" {
			continue
		}
		userByID[session.UserID] = session.UserName
	}
	if len(userByID) == 1 {
		for id, name := range userByID {
			return &UserInfo{ID: id, Name: name}, nil
		}
	}

	return nil, nil
}

func sameSession(session sessionInfo, client *ClientInfo) bool {
	if client == nil {
		return false
	}

	if client.DeviceID != "" && strings.EqualFold(session.DeviceID, client.DeviceID) {
		return true
	}

	if client.DeviceName != "" && strings.EqualFold(session.DeviceName, client.DeviceName) {
		return true
	}

	if client.ClientName != "" && strings.EqualFold(session.Client, client.ClientName) {
		return true
	}

	return false
}

// GetAllUsers fetches all users from Emby
func (i *Identifier) GetAllUsers() ([]UserInfo, error) {
	cfg := config.Get()
	if cfg == nil {
		return nil, fmt.Errorf("config not loaded")
	}

	url := fmt.Sprintf("%s/Users?api_key=%s", cfg.Emby.URL, cfg.Emby.APIKey)
	resp, err := i.client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("emby api returned status %d", resp.StatusCode)
	}

	var users []UserInfo
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		return nil, err
	}

	return users, nil
}

// ClearCache clears the user cache
func (i *Identifier) ClearCache() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.cache = make(map[string]*CachedUser)
}

func parseEmbyAuthorization(header string) map[string]string {
	values := make(map[string]string)
	header = strings.TrimSpace(header)
	if header == "" {
		return values
	}

	if idx := strings.Index(header, " "); idx >= 0 {
		header = header[idx+1:]
	}

	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}

		key := strings.TrimSpace(kv[0])
		value := strings.Trim(strings.TrimSpace(kv[1]), "\"")
		if key != "" {
			values[key] = value
		}
	}

	return values
}

func embyAuthorizationValues(r *http.Request) map[string]string {
	if r == nil {
		return map[string]string{}
	}

	for _, header := range []string{"X-Emby-Authorization", "Authorization"} {
		values := parseEmbyAuthorization(r.Header.Get(header))
		if len(values) > 0 {
			return values
		}
	}

	return map[string]string{}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func detectClientNameFromUserAgent(userAgent string) string {
	trimmed := strings.TrimSpace(userAgent)
	if trimmed == "" {
		return ""
	}

	lower := strings.ToLower(trimmed)
	knownClients := []struct {
		token string
		name  string
	}{
		{token: "infuse", name: "Infuse"},
		{token: "emby", name: "Emby"},
		{token: "jellyfin", name: "Jellyfin"},
		{token: "vlc", name: "VLC"},
		{token: "swiftfin", name: "Swiftfin"},
		{token: "kodi", name: "Kodi"},
		{token: "mpv", name: "mpv"},
		{token: "cfnetwork", name: "Apple Client"},
	}

	for _, client := range knownClients {
		if strings.Contains(lower, client.token) {
			return client.name
		}
	}

	firstToken := strings.Fields(trimmed)
	if len(firstToken) == 0 {
		return ""
	}

	name := strings.SplitN(firstToken[0], "/", 2)[0]
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}

	return name
}

func normalizeClientKey(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.ReplaceAll(value, " ", "-")
	return value
}
