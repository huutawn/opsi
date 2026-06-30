package webhookrelay

import (
	"sync"
	"time"
)

type BootstrapCredential struct {
	AuthMethod string
	Username   string
	PrivateKey []byte
	Password   []byte
}

type credentialEnvelope struct {
	value     BootstrapCredential
	expiresAt time.Time
}

type CredentialStore struct {
	mu    sync.Mutex
	now   func() time.Time
	items map[string]credentialEnvelope
}

func NewCredentialStore() *CredentialStore {
	return &CredentialStore{items: map[string]credentialEnvelope{}}
}

func (s *CredentialStore) Put(sessionID string, credential BootstrapCredential, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredLocked()
	s.items[sessionID] = credentialEnvelope{value: credential, expiresAt: s.clock().Add(ttl)}
}

func (s *CredentialStore) Take(sessionID string) (BootstrapCredential, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredLocked()
	envelope, ok := s.items[sessionID]
	if !ok {
		return BootstrapCredential{}, false
	}
	delete(s.items, sessionID)
	return envelope.value, true
}

func (s *CredentialStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredLocked()
	return len(s.items)
}

func (s *CredentialStore) purgeExpiredLocked() {
	now := s.clock()
	for id, envelope := range s.items {
		if now.After(envelope.expiresAt) {
			delete(s.items, id)
		}
	}
}

func (s *CredentialStore) clock() time.Time {
	if s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

type rateLimiter struct {
	mu      sync.Mutex
	now     func() time.Time
	windows map[string]rateWindow
}

type rateWindow struct {
	count  int
	resets time.Time
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{windows: map[string]rateWindow{}}
}

func (l *rateLimiter) Allow(key string, limit int, window time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock()
	w := l.windows[key]
	if w.resets.IsZero() || !now.Before(w.resets) {
		w = rateWindow{resets: now.Add(window)}
	}
	if w.count >= limit {
		l.windows[key] = w
		return false
	}
	w.count++
	l.windows[key] = w
	return true
}

func (l *rateLimiter) clock() time.Time {
	if l.now != nil {
		return l.now().UTC()
	}
	return time.Now().UTC()
}
