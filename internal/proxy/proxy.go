package proxy

import (
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"emby-media-portal/internal/auth"
	"emby-media-portal/internal/config"
	"emby-media-portal/internal/ratelimit"
	"emby-media-portal/internal/stats"
	"emby-media-portal/internal/transcode"
)

// Proxy is the main reverse proxy
type Proxy struct {
	identifier      *auth.Identifier
	limiterManager  *ratelimit.Manager
	rulesManager    *ratelimit.RulesManager
	transcodeCtrl   *transcode.Controller
	statsTracker    *stats.Tracker
	transport       *http.Transport
}

// NewProxy creates a new proxy instance
func NewProxy(
	identifier *auth.Identifier,
	limiterManager *ratelimit.Manager,
	rulesManager *ratelimit.RulesManager,
	transcodeCtrl *transcode.Controller,
	statsTracker *stats.Tracker,
) *Proxy {
	return &Proxy{
		identifier:     identifier,
		limiterManager: limiterManager,
		rulesManager:   rulesManager,
		transcodeCtrl:  transcodeCtrl,
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

	// Identify user
	user, err := p.identifier.IdentifyUser(r)
	if err != nil {
		log.Printf("Error identifying user: %v", err)
	}

	var userID string
	if user != nil {
		userID = user.ID
	}

	// Check transcode permission
	if userID != "" && p.transcodeCtrl.IsTranscodeRequest(r) {
		blocked, err := p.transcodeCtrl.ShouldBlockTranscode(r, userID)
		if err != nil {
			log.Printf("Error checking transcode permission: %v", err)
		} else if blocked {
			http.Error(w, "Transcoding not allowed for this user", http.StatusForbidden)
			return
		}
	}

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

	client := p.identifier.IdentifyClient(r)
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

	serverID := p.getBackendServerID(targetURL)
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

	// Copy response headers
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	w.WriteHeader(resp.StatusCode)

	// Stream response body with rate limiting
	var bytesWritten int
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

	// Record stats
	bytesRead := len(bodyBytes)
	clientID, clientName, deviceID, deviceName := "", "", "", ""
	if client != nil {
		clientID = client.ID
		clientName = client.Name
		deviceID = client.DeviceID
		deviceName = client.DeviceName
	}
	p.statsTracker.Record(userID, clientID, clientName, deviceID, deviceName, serverID, int64(bytesRead), int64(bytesWritten))

	log.Printf("Request: %s %s - User: %s - Status: %d - Bytes: in=%d out=%d - Duration: %v",
		r.Method, r.URL.Path, userID, resp.StatusCode, bytesRead, bytesWritten, time.Since(startTime))
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
