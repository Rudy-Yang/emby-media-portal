package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"emby-media-portal/internal/auth"
	"emby-media-portal/internal/config"
	"emby-media-portal/internal/ratelimit"
	"emby-media-portal/internal/stats"
)

// Proxy is the main reverse proxy
type Proxy struct {
	identifier     *auth.Identifier
	limiterManager *ratelimit.Manager
	rulesManager   *ratelimit.RulesManager
	statsTracker   *stats.Tracker
	transport      *http.Transport
}

// NewProxy creates a new proxy instance
func NewProxy(
	identifier *auth.Identifier,
	limiterManager *ratelimit.Manager,
	rulesManager *ratelimit.RulesManager,
	statsTracker *stats.Tracker,
) *Proxy {
	return &Proxy{
		identifier:     identifier,
		limiterManager: limiterManager,
		rulesManager:   rulesManager,
		statsTracker:   statsTracker,
		transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 20,
			IdleConnTimeout:     90 * time.Second,
		},
	}
}

// getBackendURL returns the backend URL based on config
func (p *Proxy) getBackendURL() string {
	cfg := config.Get()
	if cfg == nil {
		return "http://localhost:8096"
	}

	if cfg.Backend.Type == "lucky" && cfg.Backend.LuckyURL != "" {
		return cfg.Backend.LuckyURL
	}

	return cfg.Emby.URL
}

func (p *Proxy) getBackendServerID(targetURL *url.URL) string {
	cfg := config.Get()
	if cfg != nil && cfg.Backend.ServerID != "" {
		return cfg.Backend.ServerID
	}
	if targetURL == nil {
		return ""
	}
	return targetURL.Host
}

// ServeHTTP handles incoming requests
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	var (
		userID       string
		userName     string
		client       *auth.ClientInfo
		serverID     string
		trafficKind  string
		transferID   string
		bytesWritten int
		bytesRead    int
		shouldRecord bool
	)

	defer func() {
		if !shouldRecord {
			return
		}
		p.recordStats(r, userID, userName, client, serverID, trafficKind, bytesRead, bytesWritten)
		p.statsTracker.FinishTransfer(transferID)
	}()

	// Identify user
	user, err := p.identifier.IdentifyUser(r)
	if err != nil {
		log.Printf("Error identifying user: %v", err)
	}

	if user != nil {
		userID = user.ID
		userName = user.Name
	}
	trafficKind = classifyTraffic(r, userID)

	// Get rate limiter for user
	var limiter *ratelimit.Limiter
	if userID != "" {
		rule, _ := p.rulesManager.GetUserRule(userID)
		if rule != nil {
			limiter = p.limiterManager.GetUserLimiter(userID, rule.UploadLimit, rule.DownloadLimit)
		} else {
			limiter = p.limiterManager.GetUserLimiter(userID, 0, 0)
		}
	}

	client = p.identifier.IdentifyClient(r)
	var clientLimiter *ratelimit.Limiter
	if client != nil {
		clientRule, ruleErr := p.rulesManager.MatchClientRule(client.ClientName, client.DeviceID, client.UserAgent)
		if ruleErr != nil {
			log.Printf("Error matching client rule: %v", ruleErr)
		} else if clientRule != nil {
			clientLimiter = p.limiterManager.GetUserLimiter("client-rule:"+clientRule.ID, clientRule.UploadLimit, clientRule.DownloadLimit)
		}
	}

	// Get global limiter
	globalLimiter := p.limiterManager.GetGlobalLimiter()

	// Create backend request
	backendURL := p.getBackendURL()
	targetURL, err := url.Parse(backendURL)
	if err != nil {
		http.Error(w, "Invalid backend URL", http.StatusInternalServerError)
		return
	}

	targetURL.Path = r.URL.Path
	targetURL.RawQuery = r.URL.RawQuery

	serverID = p.getBackendServerID(targetURL)
	var serverLimiter *ratelimit.Limiter
	if serverID != "" {
		if rule, ruleErr := p.rulesManager.GetServerRule(serverID); ruleErr != nil {
			log.Printf("Error loading server rule for %s: %v", serverID, ruleErr)
		} else if rule != nil {
			serverLimiter = p.limiterManager.GetServerLimiter(serverID, rule.TotalLimit)
		}
	}

	// Copy request body for later use
	var bodyBytes []byte
	if r.Body != nil {
		bodyBytes, _ = io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(bodyBytes)))
	}
	bytesRead = len(bodyBytes)

	// Create proxy request
	proxyReq, err := http.NewRequestWithContext(
		r.Context(),
		r.Method,
		targetURL.String(),
		strings.NewReader(string(bodyBytes)),
	)
	if err != nil {
		http.Error(w, "Failed to create proxy request", http.StatusInternalServerError)
		return
	}

	// Copy headers
	for key, values := range r.Header {
		for _, value := range values {
			proxyReq.Header.Add(key, value)
		}
	}

	// Forward client IP
	clientIP := clientIPFromRequest(r)
	appendForwardedFor(proxyReq.Header, clientIP)
	if clientIP != "" {
		proxyReq.Header.Set("X-Real-IP", clientIP)
	}

	// Apply upload rate limiting (request body)
	if limiter != nil && len(bodyBytes) > 0 {
		if err := limiter.WaitUploadWithContext(r.Context(), len(bodyBytes)); err != nil {
			http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
			return
		}
	}

	if clientLimiter != nil && len(bodyBytes) > 0 {
		if err := clientLimiter.WaitUploadWithContext(r.Context(), len(bodyBytes)); err != nil {
			http.Error(w, "Client rate limit exceeded", http.StatusTooManyRequests)
			return
		}
	}

	if serverLimiter != nil && len(bodyBytes) > 0 {
		if err := serverLimiter.WaitUploadWithContext(r.Context(), len(bodyBytes)); err != nil {
			http.Error(w, "Server rate limit exceeded", http.StatusTooManyRequests)
			return
		}
	}

	if globalLimiter != nil && len(bodyBytes) > 0 {
		if err := globalLimiter.WaitUploadWithContext(r.Context(), len(bodyBytes)); err != nil {
			http.Error(w, "Global rate limit exceeded", http.StatusTooManyRequests)
			return
		}
	}

	// Send request to backend
	resp, err := p.transport.RoundTrip(proxyReq)
	if err != nil {
		http.Error(w, fmt.Sprintf("Backend error: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	shouldRecord = true
	transferID = p.startTransfer(userID, userName, client, serverID, r.URL.Path, trafficKind, int64(bytesRead))

	// Copy response headers
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	rewriteLocationHeader(w.Header(), p.getBackendURL(), proxyBaseURL(r))
	if rewrittenBody, rewrittenContentLength, rewritten := rewriteDiscoveryResponse(r, resp, p.getBackendURL()); rewritten {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", rewrittenContentLength))
		w.WriteHeader(resp.StatusCode)
		if _, writeErr := w.Write(rewrittenBody); writeErr != nil {
			log.Printf("Write error: %v", writeErr)
			return
		}
		bytesWritten = len(rewrittenBody)
		p.statsTracker.AddTransferProgress(transferID, 0, int64(bytesWritten))
		return
	}

	w.WriteHeader(resp.StatusCode)

	// Stream response body with rate limiting
	buf := make([]byte, 32*1024) // 32KB buffer

	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			// Apply download rate limiting
			if limiter != nil {
				if waitErr := limiter.WaitDownloadWithContext(r.Context(), n); waitErr != nil {
					log.Printf("Rate limit wait error: %v", waitErr)
					return
				}
			}

			if clientLimiter != nil {
				if waitErr := clientLimiter.WaitDownloadWithContext(r.Context(), n); waitErr != nil {
					log.Printf("Client rate limit wait error: %v", waitErr)
					return
				}
			}

			if serverLimiter != nil {
				if waitErr := serverLimiter.WaitDownloadWithContext(r.Context(), n); waitErr != nil {
					log.Printf("Server rate limit wait error: %v", waitErr)
					return
				}
			}

			if globalLimiter != nil {
				if waitErr := globalLimiter.WaitDownloadWithContext(r.Context(), n); waitErr != nil {
					log.Printf("Global rate limit wait error: %v", waitErr)
					return
				}
			}

			written, writeErr := w.Write(buf[:n])
			if writeErr != nil {
				log.Printf("Write error: %v", writeErr)
				return
			}
			bytesWritten += written
			p.statsTracker.AddTransferProgress(transferID, 0, int64(written))

			// Flush if possible
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}

		if err == io.EOF {
			break
		}
		if err != nil {
			log.Printf("Read error: %v", err)
			break
		}
	}

	log.Printf("Request: %s %s - User: %s - Status: %d - Bytes: in=%d out=%d - Duration: %v",
		r.Method, r.URL.Path, userID, resp.StatusCode, bytesRead, bytesWritten, time.Since(startTime))
}

func (p *Proxy) recordStats(r *http.Request, userID, userName string, client *auth.ClientInfo, serverID, trafficKind string, bytesRead, bytesWritten int) {
	clientID, clientName, deviceID, deviceName, userAgent := "", "", "", "", ""
	if client != nil {
		clientID = client.ID
		clientName = client.Name
		deviceID = client.DeviceID
		deviceName = client.DeviceName
		userAgent = client.UserAgent
	}
	p.statsTracker.Record(userID, userName, clientID, clientName, deviceID, deviceName, userAgent, serverID, r.URL.Path, trafficKind, int64(bytesRead), int64(bytesWritten))
}

func (p *Proxy) startTransfer(userID, userName string, client *auth.ClientInfo, serverID, requestPath, trafficKind string, bytesIn int64) string {
	clientID, clientName, deviceID, deviceName, userAgent := "", "", "", "", ""
	if client != nil {
		clientID = client.ID
		clientName = client.Name
		deviceID = client.DeviceID
		deviceName = client.DeviceName
		userAgent = client.UserAgent
	}
	return p.statsTracker.StartTransfer(userID, userName, clientID, clientName, deviceID, deviceName, userAgent, serverID, requestPath, trafficKind, bytesIn)
}

func rewriteDiscoveryResponse(r *http.Request, resp *http.Response, backendBaseURL string) ([]byte, int, bool) {
	if r == nil || resp == nil || resp.Body == nil {
		return nil, 0, false
	}
	if !shouldRewriteJSONPath(r.URL.Path) {
		return nil, 0, false
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, false
	}

	contentType := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	if !strings.Contains(contentType, "application/json") {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return nil, 0, false
	}

	rewritten, changed := rewriteDiscoveryJSON(r.URL.Path, body, backendBaseURL, proxyBaseURL(r))
	if !changed {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return nil, 0, false
	}

	return rewritten, len(rewritten), true
}

var singleItemPathPattern = regexp.MustCompile(`^/(Users/[^/]+/)?Items/[^/]+$`)

func shouldRewriteJSONPath(path string) bool {
	switch {
	case strings.HasSuffix(path, "/System/Info"):
		return true
	case strings.HasSuffix(path, "/System/Info/Public"):
		return true
	case strings.HasSuffix(path, "/PlaybackInfo"):
		return true
	case singleItemPathPattern.MatchString(path):
		return true
	default:
		return false
	}
}

func rewriteDiscoveryJSON(path string, body []byte, backendBaseURL, proxyBase string) ([]byte, bool) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, false
	}

	changed := false
	if shouldRewriteServerDiscovery(path) {
		for _, key := range []string{"LocalAddress", "WanAddress"} {
			if current, ok := payload[key].(string); ok && current != "" {
				payload[key] = proxyBase
				changed = true
			}
		}
		for _, key := range []string{"LocalAddresses", "RemoteAddresses"} {
			if values, ok := payload[key].([]any); ok {
				next := make([]any, 0, len(values))
				for _, value := range values {
					if _, ok := value.(string); ok {
						next = append(next, proxyBase)
						changed = true
					}
				}
				payload[key] = dedupeAnyStrings(next)
			}
		}
		if _, ok := payload["WebSocketPortNumber"]; ok {
			payload["WebSocketPortNumber"] = proxyPort(proxyBase)
			changed = true
		}
		if _, ok := payload["HttpServerPortNumber"]; ok {
			payload["HttpServerPortNumber"] = proxyPort(proxyBase)
			changed = true
		}
	}

	if shouldSanitizeMediaPaths(path) && sanitizeMediaPaths(payload) {
		changed = true
	}

	serialized, err := json.Marshal(payload)
	if err != nil {
		return nil, false
	}

	serialized = bytes.ReplaceAll(serialized, []byte(backendBaseURL), []byte(proxyBase))
	return serialized, changed
}


func shouldRewriteServerDiscovery(path string) bool {
	switch {
	case strings.HasSuffix(path, "/System/Info"):
		return true
	case strings.HasSuffix(path, "/System/Info/Public"):
		return true
	default:
		return false
	}
}

func shouldSanitizeMediaPaths(path string) bool {
	if strings.HasSuffix(path, "/PlaybackInfo") {
		return true
	}
	return singleItemPathPattern.MatchString(path)
}

func sanitizeMediaPaths(value any) bool {
	changed := false
	switch v := value.(type) {
	case map[string]any:
		for key, nested := range v {
			if key == "Path" {
				if shouldPreserveMediaPath(v) {
					continue
				}
				if pathValue, ok := nested.(string); ok && looksLikeLocalMediaPath(pathValue) {
					v[key] = ""
					changed = true
					continue
				}
			}
			if sanitizeMediaPaths(nested) {
				changed = true
			}
		}
	case []any:
		for _, item := range v {
			if sanitizeMediaPaths(item) {
				changed = true
			}
		}
	}
	return changed
}

func shouldPreserveMediaPath(value map[string]any) bool {
	if value == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(stringMapValue(value, "Type")), "Subtitle") &&
		boolMapValue(value, "IsExternal") &&
		boolMapValue(value, "IsTextSubtitleStream") {
		return true
	}
	return false
}

func looksLikeLocalMediaPath(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	switch {
	case strings.HasPrefix(value, "/mnt/"):
		return true
	case strings.HasPrefix(value, "/media/"):
		return true
	case strings.HasPrefix(value, "/Volumes/"):
		return true
	case strings.HasPrefix(value, "\\\\"):
		return true
	default:
		return len(value) > 2 && value[1] == ':' && (value[2] == '\\' || value[2] == '/')
	}
}

func stringMapValue(value map[string]any, key string) string {
	if value == nil {
		return ""
	}
	raw, ok := value[key]
	if !ok {
		return ""
	}
	text, ok := raw.(string)
	if !ok {
		return ""
	}
	return text
}

func boolMapValue(value map[string]any, key string) bool {
	if value == nil {
		return false
	}
	raw, ok := value[key]
	if !ok {
		return false
	}
	flag, ok := raw.(bool)
	if !ok {
		return false
	}
	return flag
}

func rewriteLocationHeader(header http.Header, backendBaseURL, proxyBase string) {
	if header == nil {
		return
	}
	location := header.Get("Location")
	if location == "" {
		return
	}
	if strings.HasPrefix(location, backendBaseURL) {
		header.Set("Location", strings.Replace(location, backendBaseURL, proxyBase, 1))
	}
}

func proxyBaseURL(r *http.Request) string {
	if r == nil {
		return ""
	}
	scheme := "http"
	if forwardedProto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwardedProto != "" {
		scheme = forwardedProto
	} else if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func proxyPort(base string) int {
	if parsed, err := url.Parse(base); err == nil {
		if port := parsed.Port(); port != "" {
			if value, convErr := strconv.Atoi(port); convErr == nil {
				return value
			}
		}
		if parsed.Scheme == "https" {
			return 443
		}
	}
	return 80
}

func dedupeAnyStrings(values []any) []any {
	seen := map[string]struct{}{}
	out := make([]any, 0, len(values))
	for _, value := range values {
		s, ok := value.(string)
		if !ok {
			continue
		}
		if _, exists := seen[s]; exists {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func classifyTraffic(r *http.Request, userID string) string {
	if r == nil {
		return ""
	}
	if strings.TrimSpace(userID) != "" {
		return "user"
	}

	path := r.URL.Path
	switch {
	case strings.HasSuffix(path, "/System/Info/Public"):
		return "public"
	case strings.HasSuffix(path, "/Users/AuthenticateByName"):
		return "public"
	case strings.HasPrefix(path, "/Sessions"), strings.Contains(path, "/Sessions/"):
		return "public"
	case strings.Contains(path, "/Images/"):
		return "public"
	case strings.HasPrefix(path, "/web/"), strings.Contains(path, "/web/"):
		return "public"
	default:
		return "unknown"
	}
}

func clientIPFromRequest(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		parts := strings.Split(forwarded, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}

	if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
		return realIP
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}

	return r.RemoteAddr
}

func appendForwardedFor(header http.Header, clientIP string) {
	if clientIP == "" {
		return
	}

	existing := strings.TrimSpace(header.Get("X-Forwarded-For"))
	if existing == "" {
		header.Set("X-Forwarded-For", clientIP)
		return
	}

	header.Set("X-Forwarded-For", existing+", "+clientIP)
}
