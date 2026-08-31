// Package publichostname owns public hostname allocation, quota, publication,
// and release state. It is the only authority for whether a managed hostname
// is available.
package publichostname

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

type Status string

const (
	StatusReserved       Status = "reserved"
	StatusProvisioning   Status = "provisioning"
	StatusActive         Status = "active"
	StatusReleasePending Status = "release_pending"
	StatusFailed         Status = "failed"
	StatusReleased       Status = "released"
)

type Allocation struct {
	ID                   string     `json:"id"`
	Hostname             string     `json:"hostname"`
	OwnerUserID          string     `json:"owner_user_id"`
	ProjectID            string     `json:"project_id"`
	EnvironmentID        string     `json:"environment_id"`
	RuntimeID            string     `json:"runtime_id,omitempty"`
	TargetIP             string     `json:"target_ip,omitempty"`
	CloudflareRecordID   string     `json:"cloudflare_record_id,omitempty"`
	Status               Status     `json:"status"`
	PublicationErrorCode string     `json:"publication_error_code,omitempty"`
	PublicationError     string     `json:"publication_error,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
	ReleasedAt           *time.Time `json:"released_at,omitempty"`
}

type ReserveRequest struct {
	Hostname      string
	OwnerUserID   string
	ProjectID     string
	EnvironmentID string
	RuntimeID     string
}

type Quota struct {
	Used        int          `json:"used"`
	Limit       int          `json:"limit"`
	Remaining   int          `json:"remaining"`
	Allocations []Allocation `json:"allocations"`
}

type Error struct {
	Code       string
	Message    string
	NextAction string
}

func (e Error) Error() string { return e.Message }

var ErrNotFound = errors.New("public hostname allocation not found")

type Store interface {
	Reserve(context.Context, ReserveRequest, int, time.Time) (Allocation, bool, error)
	Get(context.Context, string) (Allocation, error)
	GetByHostname(context.Context, string) (Allocation, error)
	ListForUser(context.Context, string) ([]Allocation, error)
	ListForProject(context.Context, string) ([]Allocation, error)
	ListPending(context.Context, int) ([]Allocation, error)
	UpdatePublication(context.Context, string, string, string, Status, string, string, time.Time) (Allocation, error)
	MarkReleasePending(context.Context, string, time.Time) (Allocation, error)
	MarkReleased(context.Context, string, time.Time) (Allocation, error)
}

type Service struct {
	Store Store
	Limit int
	Now   func() time.Time
}

func (s Service) Reserve(ctx context.Context, req ReserveRequest) (Allocation, bool, error) {
	if s.Store == nil {
		return Allocation{}, false, errors.New("public hostname store unavailable")
	}
	if req.Hostname == "" || req.OwnerUserID == "" || req.ProjectID == "" || req.EnvironmentID == "" {
		return Allocation{}, false, Error{Code: "PUBLIC_HOSTNAME_RESERVATION_INVALID", Message: "Public hostname reservation is missing its owner or deployment scope."}
	}
	return s.Store.Reserve(ctx, req, s.limit(), s.now())
}

func (s Service) Quota(ctx context.Context, userID string) (Quota, error) {
	if s.Store == nil {
		return Quota{}, errors.New("public hostname store unavailable")
	}
	allocations, err := s.Store.ListForUser(ctx, userID)
	if err != nil {
		return Quota{}, err
	}
	used := 0
	for _, allocation := range allocations {
		if allocation.Status != StatusReleased {
			used++
		}
	}
	limit := s.limit()
	remaining := limit - used
	if remaining < 0 {
		remaining = 0
	}
	return Quota{Used: used, Limit: limit, Remaining: remaining, Allocations: allocations}, nil
}

func (s Service) ProjectAllocations(ctx context.Context, projectID string) ([]Allocation, error) {
	if s.Store == nil {
		return nil, errors.New("public hostname store unavailable")
	}
	return s.Store.ListForProject(ctx, projectID)
}

func (s Service) Get(ctx context.Context, id string) (Allocation, error) {
	if s.Store == nil {
		return Allocation{}, errors.New("public hostname store unavailable")
	}
	return s.Store.Get(ctx, id)
}

func (s Service) GetByHostname(ctx context.Context, hostname string) (Allocation, error) {
	if s.Store == nil {
		return Allocation{}, errors.New("public hostname store unavailable")
	}
	return s.Store.GetByHostname(ctx, hostname)
}

func (s Service) Pending(ctx context.Context, limit int) ([]Allocation, error) {
	if s.Store == nil {
		return nil, errors.New("public hostname store unavailable")
	}
	return s.Store.ListPending(ctx, limit)
}

func (s Service) Provisioning(ctx context.Context, id, targetIP string) (Allocation, error) {
	return s.Store.UpdatePublication(ctx, id, targetIP, "", StatusProvisioning, "", "", s.now())
}

func (s Service) Active(ctx context.Context, id, targetIP, recordID string) (Allocation, error) {
	if recordID == "" {
		return Allocation{}, errors.New("cloudflare record id is required")
	}
	return s.Store.UpdatePublication(ctx, id, targetIP, recordID, StatusActive, "", "", s.now())
}

func (s Service) PublicationFailed(ctx context.Context, id, targetIP, code, message string) (Allocation, error) {
	return s.Store.UpdatePublication(ctx, id, targetIP, "", StatusFailed, code, message, s.now())
}

func (s Service) ReleasePending(ctx context.Context, id string) (Allocation, error) {
	return s.Store.MarkReleasePending(ctx, id, s.now())
}

func (s Service) Released(ctx context.Context, id string) (Allocation, error) {
	return s.Store.MarkReleased(ctx, id, s.now())
}

func (s Service) limit() int {
	if s.Limit <= 0 {
		return 3
	}
	return s.Limit
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func newID() (string, error) {
	data := make([]byte, 12)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("generate allocation id: %w", err)
	}
	return "phn-" + hex.EncodeToString(data), nil
}

type MemoryStore struct {
	mu     sync.Mutex
	values map[string]Allocation
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{values: map[string]Allocation{}} }

func (s *MemoryStore) Reserve(_ context.Context, req ReserveRequest, limit int, now time.Time) (Allocation, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, current := range s.values {
		if current.Hostname != req.Hostname {
			continue
		}
		if current.Status != StatusReleased {
			if current.ProjectID == req.ProjectID && current.EnvironmentID == req.EnvironmentID {
				return current, true, nil
			}
			return Allocation{}, false, Error{Code: "PUBLIC_HOSTNAME_UNAVAILABLE", Message: "This public subdomain has already been issued by Opsi.", NextAction: "Choose a different public subdomain."}
		}
		if heldByUser(s.values, req.OwnerUserID) >= limit {
			return Allocation{}, false, quotaError(limit)
		}
		current.OwnerUserID, current.ProjectID, current.EnvironmentID, current.RuntimeID = req.OwnerUserID, req.ProjectID, req.EnvironmentID, req.RuntimeID
		current.TargetIP, current.CloudflareRecordID, current.Status = "", "", StatusReserved
		current.PublicationErrorCode, current.PublicationError, current.ReleasedAt, current.UpdatedAt = "", "", nil, now
		s.values[id] = current
		return current, false, nil
	}
	if heldByUser(s.values, req.OwnerUserID) >= limit {
		return Allocation{}, false, quotaError(limit)
	}
	id, err := newID()
	if err != nil {
		return Allocation{}, false, err
	}
	value := Allocation{ID: id, Hostname: req.Hostname, OwnerUserID: req.OwnerUserID, ProjectID: req.ProjectID, EnvironmentID: req.EnvironmentID, RuntimeID: req.RuntimeID, Status: StatusReserved, CreatedAt: now, UpdatedAt: now}
	s.values[id] = value
	return value, false, nil
}

func heldByUser(values map[string]Allocation, userID string) int {
	count := 0
	for _, value := range values {
		if value.OwnerUserID == userID && value.Status != StatusReleased {
			count++
		}
	}
	return count
}

func quotaError(limit int) Error {
	return Error{Code: "PUBLIC_HOSTNAME_QUOTA_EXCEEDED", Message: fmt.Sprintf("The account is already holding the maximum of %d public hostnames.", limit), NextAction: "Release an unused public hostname before reserving another."}
}

func (s *MemoryStore) Get(_ context.Context, id string) (Allocation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[id]
	if !ok {
		return Allocation{}, ErrNotFound
	}
	return value, nil
}

func (s *MemoryStore) GetByHostname(_ context.Context, hostname string) (Allocation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, value := range s.values {
		if value.Hostname == hostname {
			return value, nil
		}
	}
	return Allocation{}, ErrNotFound
}

func (s *MemoryStore) ListForUser(_ context.Context, userID string) ([]Allocation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	values := []Allocation{}
	for _, value := range s.values {
		if value.OwnerUserID == userID {
			values = append(values, value)
		}
	}
	return values, nil
}

func (s *MemoryStore) ListForProject(_ context.Context, projectID string) ([]Allocation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	values := []Allocation{}
	for _, value := range s.values {
		if value.ProjectID == projectID {
			values = append(values, value)
		}
	}
	return values, nil
}

func (s *MemoryStore) ListPending(_ context.Context, limit int) ([]Allocation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	values := []Allocation{}
	for _, value := range s.values {
		if value.Status == StatusProvisioning || value.Status == StatusFailed || value.Status == StatusReleasePending {
			values = append(values, value)
		}
		if limit > 0 && len(values) == limit {
			break
		}
	}
	return values, nil
}

func (s *MemoryStore) UpdatePublication(_ context.Context, id, targetIP, recordID string, status Status, code, message string, now time.Time) (Allocation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[id]
	if !ok {
		return Allocation{}, ErrNotFound
	}
	if value.Status == StatusReleased {
		return Allocation{}, Error{Code: "PUBLIC_HOSTNAME_RELEASED", Message: "Released public hostnames cannot be published."}
	}
	value.TargetIP, value.Status, value.PublicationErrorCode, value.PublicationError, value.UpdatedAt = targetIP, status, code, message, now
	if recordID != "" {
		value.CloudflareRecordID = recordID
	}
	s.values[id] = value
	return value, nil
}

func (s *MemoryStore) MarkReleasePending(_ context.Context, id string, now time.Time) (Allocation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[id]
	if !ok {
		return Allocation{}, ErrNotFound
	}
	if value.Status == StatusReleased {
		return value, nil
	}
	value.Status, value.UpdatedAt = StatusReleasePending, now
	s.values[id] = value
	return value, nil
}

func (s *MemoryStore) MarkReleased(_ context.Context, id string, now time.Time) (Allocation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[id]
	if !ok {
		return Allocation{}, ErrNotFound
	}
	if value.Status == StatusReleased {
		return value, nil
	}
	value.Status, value.UpdatedAt, value.ReleasedAt = StatusReleased, now, &now
	value.TargetIP, value.CloudflareRecordID, value.PublicationErrorCode, value.PublicationError = "", "", "", ""
	s.values[id] = value
	return value, nil
}
