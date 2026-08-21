package restore

import (
	"context"
	"sync"
	"time"

	restorev1 "github.com/opsi-dev/opsi/contracts/go/restorev1"
)

type MemoryStore struct {
	mu       sync.Mutex
	reviews  map[string]restorev1.Review
	restores map[string]restorev1.Restore
	keys     map[string]struct{ payload, id string }
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{reviews: map[string]restorev1.Review{}, restores: map[string]restorev1.Restore{}, keys: map[string]struct{ payload, id string }{}}
}

func (s *MemoryStore) CreateReview(_ context.Context, v restorev1.Review) (restorev1.Review, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reviews[v.ID] = v
	return v, nil
}
func (s *MemoryStore) GetReview(_ context.Context, projectID, id string) (restorev1.Review, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.reviews[id]
	if !ok || v.ProjectID != projectID {
		return restorev1.Review{}, ErrNotFound
	}
	return v, nil
}
func (s *MemoryStore) ClaimReview(_ context.Context, projectID, nodeID, token string, now, expires time.Time) (restorev1.Review, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, v := range s.reviews {
		if v.ProjectID == projectID && v.TargetNodeID == nodeID && v.TargetResourceID != "" && v.Lifecycle != restorev1.ReviewSucceeded && v.Lifecycle != restorev1.ReviewFailed && (v.Lifecycle == restorev1.ReviewQueued || !v.LeaseExpiresAt.After(now)) {
			v.Lifecycle, v.LeaseToken, v.LeaseExpiresAt, v.AttemptCount = restorev1.ReviewLeased, token, expires, v.AttemptCount+1
			s.reviews[id] = v
			return v, true, nil
		}
	}
	return restorev1.Review{}, false, nil
}
func (s *MemoryStore) UpdateReviewClaimed(_ context.Context, v restorev1.Review, token string) (restorev1.Review, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.reviews[v.ID]
	if !ok || current.LeaseToken != token {
		return restorev1.Review{}, invalid(restorev1.FailureLeaseLost, "restore review lease is invalid")
	}
	if v.Lifecycle == restorev1.ReviewSucceeded || v.Lifecycle == restorev1.ReviewFailed {
		v.LeaseToken, v.LeaseExpiresAt = "", time.Time{}
	}
	s.reviews[v.ID] = v
	return v, nil
}

func (s *MemoryStore) Create(_ context.Context, v restorev1.Restore, key, payload string) (restorev1.Restore, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	scoped := v.ProjectID + "\x00" + key
	if old, ok := s.keys[scoped]; ok {
		if old.payload != payload {
			return restorev1.Restore{}, false, invalid("RESTORE_IDEMPOTENCY_CONFLICT", "idempotency key was used with another restore request")
		}
		return s.restores[old.id], true, nil
	}
	for _, current := range s.restores {
		if current.ProjectID == v.ProjectID && current.TargetResourceID == v.TargetResourceID && active(current.Lifecycle) {
			return restorev1.Restore{}, false, invalid(restorev1.FailureAlreadyRunning, "a restore is already active for this target")
		}
	}
	s.restores[v.ID], s.keys[scoped] = v, struct{ payload, id string }{payload, v.ID}
	return v, false, nil
}
func (s *MemoryStore) Get(_ context.Context, projectID, id string) (restorev1.Restore, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.restores[id]
	if !ok || v.ProjectID != projectID {
		return restorev1.Restore{}, ErrNotFound
	}
	return v, nil
}
func (s *MemoryStore) List(_ context.Context, projectID, backupID, targetID string) ([]restorev1.Restore, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []restorev1.Restore{}
	for _, v := range s.restores {
		if v.ProjectID == projectID && (backupID == "" || v.BackupID == backupID) && (targetID == "" || v.TargetResourceID == targetID) {
			out = append(out, v)
		}
	}
	return out, nil
}
func (s *MemoryStore) Claim(_ context.Context, projectID, nodeID, token string, now, expires time.Time) (restorev1.Restore, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, v := range s.restores {
		if v.ProjectID == projectID && v.TargetNodeID == nodeID && active(v.Lifecycle) && (v.Lifecycle == restorev1.LifecycleQueued || !v.LeaseExpiresAt.After(now)) {
			v.Lifecycle, v.LeaseToken, v.LeaseExpiresAt, v.AttemptCount = restorev1.LifecycleLeased, token, expires, v.AttemptCount+1
			s.restores[id] = v
			return v, true, nil
		}
	}
	return restorev1.Restore{}, false, nil
}
func (s *MemoryStore) UpdateClaimed(_ context.Context, v restorev1.Restore, token string) (restorev1.Restore, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.restores[v.ID]
	if !ok || current.LeaseToken != token {
		return restorev1.Restore{}, invalid(restorev1.FailureLeaseLost, "restore lease is invalid")
	}
	if !active(v.Lifecycle) {
		v.LeaseToken, v.LeaseExpiresAt = "", time.Time{}
	}
	s.restores[v.ID] = v
	return v, nil
}
func (s *MemoryStore) HasActive(_ context.Context, projectID, resourceID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.restores {
		if v.ProjectID == projectID && v.TargetResourceID == resourceID && active(v.Lifecycle) {
			return true, nil
		}
	}
	return false, nil
}
func active(v string) bool {
	return v == restorev1.LifecycleQueued || v == restorev1.LifecycleLeased || v == restorev1.LifecycleRunning || v == restorev1.LifecycleVerifying
}
