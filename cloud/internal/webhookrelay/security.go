package webhookrelay

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

type BootstrapCredential struct {
	AuthMethod string
	Username   string
	PrivateKey []byte
	Password   []byte
}

type CredentialVault interface {
	Put(sessionID string, credential BootstrapCredential, ttl time.Duration)
	Take(sessionID string) (BootstrapCredential, bool)
	Delete(sessionID string)
	Len() int
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

func (s *CredentialStore) Delete(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, sessionID)
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

type BootstrapRegistration struct {
	SessionID string
	OrgID     string
	ProjectID string
	NodeID    string
	Token     string
	ExpiresAt time.Time
}

type RegistrationVault interface {
	Put(sessionID, orgID, projectID, nodeID, token string, ttl time.Duration)
	TakeForWorker(sessionID string) (BootstrapRegistration, bool)
	Exchange(token string) (BootstrapRegistration, bool)
	DeleteSession(sessionID string)
}

type RegistrationTokenStore struct {
	mu        sync.Mutex
	now       func() time.Time
	bySession map[string]BootstrapRegistration
	byHash    map[string]BootstrapRegistration
}

func NewRegistrationTokenStore() *RegistrationTokenStore {
	return &RegistrationTokenStore{bySession: map[string]BootstrapRegistration{}, byHash: map[string]BootstrapRegistration{}}
}

func (s *RegistrationTokenStore) Put(sessionID, orgID, projectID, nodeID, token string, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredLocked()
	reg := BootstrapRegistration{SessionID: sessionID, OrgID: orgID, ProjectID: projectID, NodeID: nodeID, Token: token, ExpiresAt: s.clock().Add(ttl)}
	s.bySession[sessionID] = reg
	s.byHash[tokenHash(token)] = reg
}

func (s *RegistrationTokenStore) TakeForWorker(sessionID string) (BootstrapRegistration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredLocked()
	reg, ok := s.bySession[sessionID]
	if !ok {
		return BootstrapRegistration{}, false
	}
	delete(s.bySession, sessionID)
	return reg, true
}

func (s *RegistrationTokenStore) Exchange(token string) (BootstrapRegistration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredLocked()
	hash := tokenHash(token)
	reg, ok := s.byHash[hash]
	if !ok {
		return BootstrapRegistration{}, false
	}
	delete(s.byHash, hash)
	delete(s.bySession, reg.SessionID)
	reg.Token = ""
	return reg, true
}

func (s *RegistrationTokenStore) DeleteSession(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if reg, ok := s.bySession[sessionID]; ok {
		delete(s.byHash, tokenHash(reg.Token))
	}
	for hash, reg := range s.byHash {
		if reg.SessionID == sessionID {
			delete(s.byHash, hash)
		}
	}
	delete(s.bySession, sessionID)
}

func (s *RegistrationTokenStore) purgeExpiredLocked() {
	now := s.clock()
	for sessionID, reg := range s.bySession {
		if now.After(reg.ExpiresAt) {
			delete(s.bySession, sessionID)
		}
	}
	for hash, reg := range s.byHash {
		if now.After(reg.ExpiresAt) {
			delete(s.byHash, hash)
		}
	}
}

func (s *RegistrationTokenStore) clock() time.Time {
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

type RateLimiter interface {
	Allow(key string, limit int, window time.Duration) bool
}

func newSecret(prefix string) string {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		return prefix + "-" + hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return prefix + "-" + hex.EncodeToString(data[:])
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
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
