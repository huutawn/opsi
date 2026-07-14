package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var (
	ErrInvalidToken  = errors.New("pat invalid")
	ErrExpired       = errors.New("pat expired")
	ErrRevoked       = errors.New("pat revoked")
	ErrNoMembership  = errors.New("project membership not found")
	ErrOAuthIdentity = errors.New("oauth identity not linked")
)

type VerifyRequest struct {
	Token     string
	OrgID     string
	ProjectID string
}

type VerifyResult struct {
	TokenID   string    `json:"-"`
	UserID    string    `json:"user_id"`
	Email     string    `json:"email,omitempty"`
	OrgID     string    `json:"org_id,omitempty"`
	ProjectID string    `json:"project_id"`
	Role      string    `json:"role"`
	ExpiresAt time.Time `json:"expires_at"`
	Revoked   bool      `json:"revoked"`
}

type Candidate struct {
	ID        string
	UserID    string
	Email     string
	OrgID     string
	ProjectID string
	Role      string
	Hash      string
	ExpiresAt time.Time
	Revoked   bool
}

type Store interface {
	PATCandidates(ctx context.Context, projectID string) ([]Candidate, error)
}

type OrgStore interface {
	OrgPATCandidates(ctx context.Context, orgID string) ([]Candidate, error)
}

type LifecycleStore interface {
	IssuePATForUser(ctx context.Context, userID, projectID, tokenHash string, expiresAt time.Time) (Candidate, error)
	RevokePAT(ctx context.Context, tokenID string) error
}

type OAuthStore interface {
	OAuthUser(ctx context.Context, provider, subject string) (string, error)
}

type IssueResult struct {
	Token     string       `json:"token,omitempty"`
	Session   VerifyResult `json:"session"`
	ExpiresAt time.Time    `json:"expires_at"`
}

type Service struct {
	Store Store
	Now   func() time.Time
}

func (s Service) VerifyPAT(ctx context.Context, req VerifyRequest) (VerifyResult, error) {
	if req.Token == "" || req.ProjectID == "" {
		return VerifyResult{}, ErrInvalidToken
	}
	if s.Store == nil {
		return VerifyResult{}, ErrInvalidToken
	}
	candidates, err := s.Store.PATCandidates(ctx, req.ProjectID)
	if err != nil {
		return VerifyResult{}, err
	}
	now := s.now()
	for _, candidate := range candidates {
		if candidate.ProjectID != req.ProjectID || candidate.UserID == "" || candidate.Role == "" {
			continue
		}
		if bcrypt.CompareHashAndPassword([]byte(candidate.Hash), []byte(req.Token)) != nil {
			continue
		}
		if candidate.Revoked {
			return VerifyResult{}, ErrRevoked
		}
		if !candidate.ExpiresAt.IsZero() && !now.Before(candidate.ExpiresAt) {
			return VerifyResult{}, ErrExpired
		}
		return VerifyResult{TokenID: candidate.ID, UserID: candidate.UserID, Email: candidate.Email, OrgID: candidate.OrgID, ProjectID: candidate.ProjectID, Role: normalizeRole(candidate.Role), ExpiresAt: candidate.ExpiresAt, Revoked: candidate.Revoked}, nil
	}
	return VerifyResult{}, ErrInvalidToken
}

func (s Service) VerifyOrgPAT(ctx context.Context, req VerifyRequest) (VerifyResult, error) {
	if req.Token == "" || req.OrgID == "" {
		return VerifyResult{}, ErrInvalidToken
	}
	store, ok := s.Store.(OrgStore)
	if !ok {
		return VerifyResult{}, ErrInvalidToken
	}
	candidates, err := store.OrgPATCandidates(ctx, req.OrgID)
	if err != nil {
		return VerifyResult{}, err
	}
	now := s.now()
	for _, candidate := range candidates {
		if candidate.OrgID != req.OrgID || candidate.UserID == "" || candidate.Role == "" {
			continue
		}
		if bcrypt.CompareHashAndPassword([]byte(candidate.Hash), []byte(req.Token)) != nil {
			continue
		}
		if candidate.Revoked {
			return VerifyResult{}, ErrRevoked
		}
		if !candidate.ExpiresAt.IsZero() && !now.Before(candidate.ExpiresAt) {
			return VerifyResult{}, ErrExpired
		}
		return VerifyResult{TokenID: candidate.ID, UserID: candidate.UserID, Email: candidate.Email, OrgID: candidate.OrgID, Role: normalizeRole(candidate.Role), ExpiresAt: candidate.ExpiresAt, Revoked: candidate.Revoked}, nil
	}
	return VerifyResult{}, ErrInvalidToken
}

func (s Service) IssuePATForOAuth(ctx context.Context, provider, subject, projectID string, ttl time.Duration) (IssueResult, error) {
	store, ok := s.Store.(interface {
		LifecycleStore
		OAuthStore
	})
	if !ok || provider == "" || subject == "" {
		return IssueResult{}, ErrOAuthIdentity
	}
	userID, err := store.OAuthUser(ctx, provider, subject)
	if err != nil {
		return IssueResult{}, err
	}
	token, hash, expiresAt, err := s.newToken(ttl)
	if err != nil {
		return IssueResult{}, err
	}
	candidate, err := store.IssuePATForUser(ctx, userID, projectID, hash, expiresAt)
	if err != nil {
		return IssueResult{}, err
	}
	session := VerifyResult{TokenID: candidate.ID, UserID: candidate.UserID, Email: candidate.Email, OrgID: candidate.OrgID, ProjectID: candidate.ProjectID, Role: normalizeRole(candidate.Role), ExpiresAt: candidate.ExpiresAt}
	return IssueResult{Token: token, Session: session, ExpiresAt: expiresAt}, nil
}

func (s Service) ResolveOAuthUser(ctx context.Context, provider, subject string) (string, error) {
	store, ok := s.Store.(OAuthStore)
	if !ok || provider == "" || subject == "" {
		return "", ErrOAuthIdentity
	}
	userID, err := store.OAuthUser(ctx, provider, subject)
	if err != nil {
		return "", err
	}
	if userID == "" {
		return "", ErrOAuthIdentity
	}
	return userID, nil
}

func (s Service) RotatePAT(ctx context.Context, token, projectID string, ttl time.Duration) (IssueResult, VerifyResult, error) {
	current, err := s.VerifyPAT(ctx, VerifyRequest{Token: token, ProjectID: projectID})
	if err != nil {
		return IssueResult{}, VerifyResult{}, err
	}
	store, ok := s.Store.(LifecycleStore)
	if !ok {
		return IssueResult{}, VerifyResult{}, ErrInvalidToken
	}
	newToken, hash, expiresAt, err := s.newToken(ttl)
	if err != nil {
		return IssueResult{}, VerifyResult{}, err
	}
	candidate, err := store.IssuePATForUser(ctx, current.UserID, current.ProjectID, hash, expiresAt)
	if err != nil {
		return IssueResult{}, VerifyResult{}, err
	}
	session := VerifyResult{TokenID: candidate.ID, UserID: candidate.UserID, Email: candidate.Email, OrgID: candidate.OrgID, ProjectID: candidate.ProjectID, Role: normalizeRole(candidate.Role), ExpiresAt: candidate.ExpiresAt}
	return IssueResult{Token: newToken, Session: session, ExpiresAt: expiresAt}, current, nil
}

func (s Service) RevokePAT(ctx context.Context, token, projectID string) (VerifyResult, error) {
	current, err := s.VerifyPAT(ctx, VerifyRequest{Token: token, ProjectID: projectID})
	if err != nil {
		return VerifyResult{}, err
	}
	store, ok := s.Store.(LifecycleStore)
	if !ok {
		return VerifyResult{}, ErrInvalidToken
	}
	return current, store.RevokePAT(ctx, current.TokenID)
}

func HashPAT(token string) (string, error) {
	if token == "" {
		return "", ErrInvalidToken
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(token), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func NewPAT(ttl time.Duration, now time.Time) (string, string, time.Time, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", time.Time{}, err
	}
	token := "opsi_pat_" + base64.RawURLEncoding.EncodeToString(raw[:])
	hash, err := HashPAT(token)
	if err != nil {
		return "", "", time.Time{}, err
	}
	if ttl <= 0 {
		ttl = 90 * 24 * time.Hour
	}
	return token, hash, now.UTC().Add(ttl), nil
}

func (s Service) newToken(ttl time.Duration) (string, string, time.Time, error) {
	return NewPAT(ttl, s.now())
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

type MemoryStore struct {
	Candidates      []Candidate
	OAuthIdentities map[string]string
}

func (s MemoryStore) PATCandidates(_ context.Context, projectID string) ([]Candidate, error) {
	out := make([]Candidate, 0, len(s.Candidates))
	for _, candidate := range s.Candidates {
		if candidate.ProjectID == projectID {
			out = append(out, candidate)
		}
	}
	return out, nil
}

func (s *MemoryStore) IssuePATForUser(_ context.Context, userID, projectID, tokenHash string, expiresAt time.Time) (Candidate, error) {
	for _, candidate := range s.Candidates {
		if candidate.UserID == userID && (projectID == "" || candidate.ProjectID == projectID) {
			candidate.ID = fmt.Sprintf("pat_%d", len(s.Candidates)+1)
			candidate.Hash = tokenHash
			candidate.ExpiresAt = expiresAt
			candidate.Revoked = false
			s.Candidates = append(s.Candidates, candidate)
			return candidate, nil
		}
	}
	return Candidate{}, ErrNoMembership
}

func (s *MemoryStore) RevokePAT(_ context.Context, tokenID string) error {
	for i := range s.Candidates {
		if s.Candidates[i].ID == tokenID {
			s.Candidates[i].Revoked = true
			return nil
		}
	}
	return ErrInvalidToken
}

func (s MemoryStore) OrgPATCandidates(_ context.Context, orgID string) ([]Candidate, error) {
	out := make([]Candidate, 0, len(s.Candidates))
	for _, candidate := range s.Candidates {
		if candidate.OrgID == orgID {
			out = append(out, candidate)
		}
	}
	return out, nil
}

func (s MemoryStore) OAuthUser(_ context.Context, provider, subject string) (string, error) {
	if userID := s.OAuthIdentities[provider+"\x00"+subject]; userID != "" {
		return userID, nil
	}
	return "", ErrOAuthIdentity
}

func normalizeRole(role string) string {
	switch role {
	case "Owner":
		return "owner"
	case "Admin":
		return "admin"
	case "Developer":
		return "developer"
	case "Viewer":
		return "viewer"
	default:
		return role
	}
}
