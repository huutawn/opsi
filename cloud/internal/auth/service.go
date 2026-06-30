package auth

import (
	"context"
	"errors"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var (
	ErrInvalidToken = errors.New("pat invalid")
	ErrExpired      = errors.New("pat expired")
	ErrRevoked      = errors.New("pat revoked")
	ErrNoMembership = errors.New("project membership not found")
)

type VerifyRequest struct {
	Token     string
	OrgID     string
	ProjectID string
}

type VerifyResult struct {
	UserID    string    `json:"user_id"`
	OrgID     string    `json:"org_id,omitempty"`
	ProjectID string    `json:"project_id"`
	Role      string    `json:"role"`
	ExpiresAt time.Time `json:"expires_at"`
	Revoked   bool      `json:"revoked"`
}

type Candidate struct {
	UserID    string
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
		return VerifyResult{UserID: candidate.UserID, OrgID: candidate.OrgID, ProjectID: candidate.ProjectID, Role: normalizeRole(candidate.Role), ExpiresAt: candidate.ExpiresAt, Revoked: candidate.Revoked}, nil
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
		return VerifyResult{UserID: candidate.UserID, OrgID: candidate.OrgID, Role: normalizeRole(candidate.Role), ExpiresAt: candidate.ExpiresAt, Revoked: candidate.Revoked}, nil
	}
	return VerifyResult{}, ErrInvalidToken
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

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

type MemoryStore struct {
	Candidates []Candidate
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

func (s MemoryStore) OrgPATCandidates(_ context.Context, orgID string) ([]Candidate, error) {
	out := make([]Candidate, 0, len(s.Candidates))
	for _, candidate := range s.Candidates {
		if candidate.OrgID == orgID {
			out = append(out, candidate)
		}
	}
	return out, nil
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
