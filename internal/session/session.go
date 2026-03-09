package session

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

const CookieName = "emby_mp_session"

type entry struct {
	Username  string
	ExpiresAt time.Time
}

type Manager struct {
	mu       sync.RWMutex
	sessions map[string]entry
	ttl      time.Duration
}

var DefaultManager = NewManager(24 * time.Hour)

func NewManager(ttl time.Duration) *Manager {
	return &Manager{
		sessions: make(map[string]entry),
		ttl:      ttl,
	}
}

func (m *Manager) Create(username string) (string, time.Time, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", time.Time{}, err
	}

	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	expiresAt := time.Now().Add(m.ttl)

	m.mu.Lock()
	m.sessions[token] = entry{Username: username, ExpiresAt: expiresAt}
	m.mu.Unlock()

	return token, expiresAt, nil
}

func (m *Manager) Validate(token string) (string, bool) {
	if token == "" {
		return "", false
	}

	m.mu.RLock()
	session, ok := m.sessions[token]
	m.mu.RUnlock()
	if !ok {
		return "", false
	}
	if time.Now().After(session.ExpiresAt) {
		m.Revoke(token)
		return "", false
	}
	return session.Username, true
}

func (m *Manager) Revoke(token string) {
	if token == "" {
		return
	}
	m.mu.Lock()
	delete(m.sessions, token)
	m.mu.Unlock()
}
