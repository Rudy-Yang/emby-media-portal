package ratelimit

import (
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limiter wraps rate.Limiter with upload and download limits
type Limiter struct {
	uploadLimiter   *rate.Limiter
	downloadLimiter *rate.Limiter
	uploadLimit     int64
	downloadLimit   int64
}

// NewLimiter creates a new limiter with specified rates
func NewLimiter(uploadLimit, downloadLimit int64) *Limiter {
	l := &Limiter{
		uploadLimit:   uploadLimit,
		downloadLimit: downloadLimit,
	}

	// 0 means unlimited
	if uploadLimit > 0 {
		l.uploadLimiter = rate.NewLimiter(rate.Limit(uploadLimit), int(uploadLimit))
	}
	if downloadLimit > 0 {
		l.downloadLimiter = rate.NewLimiter(rate.Limit(downloadLimit), int(downloadLimit))
	}

	return l
}

// WaitUpload blocks until upload is allowed
func (l *Limiter) WaitUpload(n int) error {
	if l.uploadLimiter == nil {
		return nil
	}
	return waitWithChunks(context.Background(), l.uploadLimiter, n)
}

// WaitDownload blocks until download is allowed
func (l *Limiter) WaitDownload(n int) error {
	if l.downloadLimiter == nil {
		return nil
	}
	return waitWithChunks(context.Background(), l.downloadLimiter, n)
}

// WaitUploadWithContext blocks until upload is allowed or the context is canceled.
func (l *Limiter) WaitUploadWithContext(ctx context.Context, n int) error {
	if l.uploadLimiter == nil {
		return nil
	}
	return waitWithChunks(ctx, l.uploadLimiter, n)
}

// WaitDownloadWithContext blocks until download is allowed or the context is canceled.
func (l *Limiter) WaitDownloadWithContext(ctx context.Context, n int) error {
	if l.downloadLimiter == nil {
		return nil
	}
	return waitWithChunks(ctx, l.downloadLimiter, n)
}

// AllowUpload checks if upload is allowed without blocking
func (l *Limiter) AllowUpload(n int) bool {
	if l.uploadLimiter == nil {
		return true
	}
	return l.uploadLimiter.AllowN(time.Now(), n)
}

// AllowDownload checks if download is allowed without blocking
func (l *Limiter) AllowDownload(n int) bool {
	if l.downloadLimiter == nil {
		return true
	}
	return l.downloadLimiter.AllowN(time.Now(), n)
}

// UpdateLimits dynamically updates rate limits
func (l *Limiter) UpdateLimits(uploadLimit, downloadLimit int64) {
	l.uploadLimit = uploadLimit
	l.downloadLimit = downloadLimit

	if uploadLimit > 0 {
		if l.uploadLimiter == nil {
			l.uploadLimiter = rate.NewLimiter(rate.Limit(uploadLimit), int(uploadLimit))
		} else {
			l.uploadLimiter.SetLimit(rate.Limit(uploadLimit))
			l.uploadLimiter.SetBurst(int(uploadLimit))
		}
	} else {
		l.uploadLimiter = nil
	}

	if downloadLimit > 0 {
		if l.downloadLimiter == nil {
			l.downloadLimiter = rate.NewLimiter(rate.Limit(downloadLimit), int(downloadLimit))
		} else {
			l.downloadLimiter.SetLimit(rate.Limit(downloadLimit))
			l.downloadLimiter.SetBurst(int(downloadLimit))
		}
	} else {
		l.downloadLimiter = nil
	}
}

// GetLimits returns current limits
func (l *Limiter) GetLimits() (int64, int64) {
	return l.uploadLimit, l.downloadLimit
}

func waitWithChunks(ctx context.Context, limiter *rate.Limiter, n int) error {
	if limiter == nil || n <= 0 {
		return nil
	}

	if ctx == nil {
		ctx = context.Background()
	}

	burst := limiter.Burst()
	if burst <= 0 {
		burst = 1
	}

	remaining := n
	for remaining > 0 {
		chunk := remaining
		if chunk > burst {
			chunk = burst
		}

		if err := limiter.WaitN(ctx, chunk); err != nil {
			return err
		}
		remaining -= chunk
	}

	return nil
}

// Manager manages limiters for users and servers
type Manager struct {
	userLimiters   map[string]*Limiter
	serverLimiters map[string]*Limiter
	globalLimiter  *Limiter
	mu             sync.RWMutex

	defaultUpload   int64
	defaultDownload int64
}

// NewManager creates a new limiter manager
func NewManager(defaultUpload, defaultDownload, globalLimit int64) *Manager {
	m := &Manager{
		userLimiters:    make(map[string]*Limiter),
		serverLimiters:  make(map[string]*Limiter),
		defaultUpload:   defaultUpload,
		defaultDownload: defaultDownload,
	}

	if globalLimit > 0 {
		m.globalLimiter = NewLimiter(globalLimit, globalLimit)
	}

	return m
}

// GetUserLimiter returns limiter for a user
func (m *Manager) GetUserLimiter(userID string, customUpload, customDownload int64) *Limiter {
	m.mu.Lock()
	defer m.mu.Unlock()

	if limiter, ok := m.userLimiters[userID]; ok {
		return limiter
	}

	// Use custom limits if provided, otherwise use defaults
	upload := customUpload
	if upload == 0 {
		upload = m.defaultUpload
	}
	download := customDownload
	if download == 0 {
		download = m.defaultDownload
	}

	limiter := NewLimiter(upload, download)
	m.userLimiters[userID] = limiter
	return limiter
}

// UpdateUserLimiter updates limits for a user
func (m *Manager) UpdateUserLimiter(userID string, uploadLimit, downloadLimit int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if limiter, ok := m.userLimiters[userID]; ok {
		limiter.UpdateLimits(uploadLimit, downloadLimit)
	} else {
		m.userLimiters[userID] = NewLimiter(uploadLimit, downloadLimit)
	}
}

// RemoveUserLimiter removes a user's limiter
func (m *Manager) RemoveUserLimiter(userID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.userLimiters, userID)
}

// GetServerLimiter returns limiter for a server
func (m *Manager) GetServerLimiter(serverID string, limit int64) *Limiter {
	m.mu.Lock()
	defer m.mu.Unlock()

	if limiter, ok := m.serverLimiters[serverID]; ok {
		return limiter
	}

	limiter := NewLimiter(limit, limit)
	m.serverLimiters[serverID] = limiter
	return limiter
}

// UpdateServerLimiter updates limits for a server
func (m *Manager) UpdateServerLimiter(serverID string, limit int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if limiter, ok := m.serverLimiters[serverID]; ok {
		limiter.UpdateLimits(limit, limit)
	} else {
		m.serverLimiters[serverID] = NewLimiter(limit, limit)
	}
}

// GetGlobalLimiter returns the global limiter
func (m *Manager) GetGlobalLimiter() *Limiter {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.globalLimiter
}

// UpdateGlobalLimit updates the global limit
func (m *Manager) UpdateGlobalLimit(limit int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if limit > 0 {
		if m.globalLimiter == nil {
			m.globalLimiter = NewLimiter(limit, limit)
		} else {
			m.globalLimiter.UpdateLimits(limit, limit)
		}
	} else {
		m.globalLimiter = nil
	}
}

// UpdateDefaults updates default limits for new users
func (m *Manager) UpdateDefaults(upload, download int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.defaultUpload = upload
	m.defaultDownload = download
}

// GetDefaults returns default limits
func (m *Manager) GetDefaults() (int64, int64) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.defaultUpload, m.defaultDownload
}
