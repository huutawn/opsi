package otp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"
)

var (
	ErrNotFound    = errors.New("otp request not found")
	ErrExpired     = errors.New("otp expired")
	ErrUsed        = errors.New("otp already used")
	ErrInvalidCode = errors.New("otp invalid")
	ErrRateLimited = errors.New("otp rate limited")
)

type Request struct {
	ProjectID string
	UserID    string
	Purpose   string
}

type Response struct {
	RequestID string
	ExpiresAt time.Time
	Code      string
}

type Service struct {
	mu       sync.Mutex
	now      func() time.Time
	items    map[string]entry
	attempts map[string][]time.Time
	Store    Store
	Sender   Sender
	TTL      time.Duration
	Limit    int
	Window   time.Duration
	DevEcho  bool
}

type Store interface {
	Put(ctx context.Context, id string, item entry) error
	Get(ctx context.Context, id string) (entry, error)
	MarkUsed(ctx context.Context, id string, usedAt time.Time) error
}

type Sender interface {
	SendOTP(ctx context.Context, req Request, code string, expiresAt time.Time) error
}

type entry struct {
	Request
	Salt      string
	Hash      string
	ExpiresAt time.Time
	UsedAt    time.Time
}

func NewService() *Service {
	return &Service{items: map[string]entry{}, attempts: map[string][]time.Time{}, TTL: 5 * time.Minute, Limit: 5, Window: 15 * time.Minute}
}

func (s *Service) RequestOTP(ctx context.Context, req Request) (Response, error) {
	if req.ProjectID == "" || req.UserID == "" || req.Purpose == "" {
		return Response{}, errors.New("project_id, user_id and purpose are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	if !s.allowLocked(req.UserID, now) {
		return Response{}, ErrRateLimited
	}
	code, err := randomOTP()
	if err != nil {
		return Response{}, err
	}
	salt := randomID("salt")
	id := randomID("otp")
	ttl := s.TTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	expiresAt := now.Add(ttl)
	item := entry{Request: req, Salt: salt, Hash: hashCode(salt, code), ExpiresAt: expiresAt}
	if s.Store != nil {
		if err := s.Store.Put(ctx, id, item); err != nil {
			return Response{}, err
		}
	} else {
		s.items[id] = item
	}
	if s.Sender != nil {
		if err := s.Sender.SendOTP(ctx, req, code, expiresAt); err != nil {
			return Response{}, err
		}
	}
	resp := Response{RequestID: id, ExpiresAt: expiresAt}
	if s.DevEcho {
		resp.Code = code
	}
	return resp, nil
}

func (s *Service) VerifyOTP(ctx context.Context, requestID, projectID, userID, purpose, code string) error {
	if s.Store != nil {
		return s.verifyStoredOTP(ctx, requestID, projectID, userID, purpose, code)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[requestID]
	if !ok || item.ProjectID != projectID || item.UserID != userID || item.Purpose != purpose {
		return ErrNotFound
	}
	if !item.UsedAt.IsZero() {
		return ErrUsed
	}
	if !s.clock().Before(item.ExpiresAt) {
		return ErrExpired
	}
	if hashCode(item.Salt, code) != item.Hash {
		return ErrInvalidCode
	}
	item.UsedAt = s.clock()
	s.items[requestID] = item
	return nil
}

func (s *Service) verifyStoredOTP(ctx context.Context, requestID, projectID, userID, purpose, code string) error {
	item, err := s.Store.Get(ctx, requestID)
	if err != nil {
		return ErrNotFound
	}
	if item.ProjectID != projectID || item.UserID != userID || item.Purpose != purpose {
		return ErrNotFound
	}
	if !item.UsedAt.IsZero() {
		return ErrUsed
	}
	if !s.clock().Before(item.ExpiresAt) {
		return ErrExpired
	}
	if hashCode(item.Salt, code) != item.Hash {
		return ErrInvalidCode
	}
	return s.Store.MarkUsed(ctx, requestID, s.clock())
}

func (s *Service) allowLocked(userID string, now time.Time) bool {
	limit := s.Limit
	if limit <= 0 {
		limit = 5
	}
	window := s.Window
	if window <= 0 {
		window = 15 * time.Minute
	}
	cutoff := now.Add(-window)
	kept := s.attempts[userID][:0]
	for _, ts := range s.attempts[userID] {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	if len(kept) >= limit {
		s.attempts[userID] = kept
		return false
	}
	kept = append(kept, now)
	s.attempts[userID] = kept
	return true
}

func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

func randomOTP() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func randomID(prefix string) string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(raw[:])
}

func hashCode(salt, code string) string {
	sum := sha256.Sum256([]byte(salt + ":" + code))
	return hex.EncodeToString(sum[:])
}
