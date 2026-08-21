package cutover

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	cutoverv1 "github.com/opsi-dev/opsi/contracts/go/cutoverv1"
)

type MemoryStore struct {
	mu                  sync.Mutex
	reviews             map[string]cutoverv1.ApplicationCutoverReview
	idempotency         map[string]idempotencyRecord
	cutovers            map[string]cutoverv1.ApplicationCutover
	cutoverIdempotency  map[string]cutoverIdempotencyRecord
	rollbacks               map[string]cutoverv1.ApplicationCutoverRollback
	rollbackIdempotency     map[string]rollbackIdempotencyRecord
	finalizations           map[string]cutoverv1.ApplicationCutoverFinalization
	finalizationIdempotency map[string]finalizationIdempotencyRecord
}

type idempotencyRecord struct {
	reviewID    string
	payloadHash string
}

type cutoverIdempotencyRecord struct {
	cutoverID   string
	payloadHash string
}

type rollbackIdempotencyRecord struct {
	rollbackID  string
	payloadHash string
}

type finalizationIdempotencyRecord struct {
	finalizationID string
	payloadHash    string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		reviews:                 map[string]cutoverv1.ApplicationCutoverReview{},
		idempotency:             map[string]idempotencyRecord{},
		cutovers:                map[string]cutoverv1.ApplicationCutover{},
		cutoverIdempotency:      map[string]cutoverIdempotencyRecord{},
		rollbacks:               map[string]cutoverv1.ApplicationCutoverRollback{},
		rollbackIdempotency:     map[string]rollbackIdempotencyRecord{},
		finalizations:           map[string]cutoverv1.ApplicationCutoverFinalization{},
		finalizationIdempotency: map[string]finalizationIdempotencyRecord{},
	}
}

func (s *MemoryStore) CreateReview(_ context.Context, value cutoverv1.ApplicationCutoverReview, key, payload string) (cutoverv1.ApplicationCutoverReview, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key != "" {
		compoundKey := value.ProjectID + ":" + key
		if record, ok := s.idempotency[compoundKey]; ok {
			if record.payloadHash != payload {
				return cutoverv1.ApplicationCutoverReview{}, false, Error{Code: cutoverv1.FailureIdempotencyConflict, Status: 409, Message: "idempotency key was used with another payload"}
			}
			existing, found := s.reviews[record.reviewID]
			if found {
				return existing, true, nil
			}
		}
		s.idempotency[compoundKey] = idempotencyRecord{reviewID: value.ID, payloadHash: payload}
	}
	s.reviews[value.ID] = value
	return value, false, nil
}

func (s *MemoryStore) GetReview(_ context.Context, projectID, reviewID string) (cutoverv1.ApplicationCutoverReview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	review, ok := s.reviews[reviewID]
	if !ok || review.ProjectID != projectID {
		return cutoverv1.ApplicationCutoverReview{}, ErrNotFound
	}
	return review, nil
}

func (s *MemoryStore) ListReviews(_ context.Context, projectID, applicationID string) ([]cutoverv1.ApplicationCutoverReview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []cutoverv1.ApplicationCutoverReview{}
	for _, review := range s.reviews {
		if review.ProjectID != projectID {
			continue
		}
		if applicationID != "" && review.ApplicationID != applicationID {
			continue
		}
		out = append(out, review)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].RequestedAt.Before(out[j].RequestedAt)
	})
	return out, nil
}

func (s *MemoryStore) ClaimReview(_ context.Context, projectID, nodeID, token string, now, expires time.Time) (cutoverv1.ApplicationCutoverReview, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var candidate *cutoverv1.ApplicationCutoverReview
	for id, review := range s.reviews {
		if review.ProjectID != projectID {
			continue
		}
		if nodeID != "" && review.TargetNodeID != "" && review.TargetNodeID != nodeID {
			continue
		}
		if review.Lifecycle == cutoverv1.ReviewQueued || (review.Lifecycle == cutoverv1.ReviewLeased && review.LeaseExpiresAt.Before(now)) {
			if candidate == nil || review.RequestedAt.Before(candidate.RequestedAt) {
				c := s.reviews[id]
				candidate = &c
			}
		}
	}
	if candidate == nil {
		return cutoverv1.ApplicationCutoverReview{}, false, nil
	}
	candidate.Lifecycle = cutoverv1.ReviewLeased
	candidate.LeaseToken = token
	candidate.LeaseExpiresAt = expires
	candidate.AttemptCount++
	s.reviews[candidate.ID] = *candidate
	return *candidate, true, nil
}

func (s *MemoryStore) UpdateReviewClaimed(_ context.Context, value cutoverv1.ApplicationCutoverReview, token string) (cutoverv1.ApplicationCutoverReview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.reviews[value.ID]
	if !ok || current.ProjectID != value.ProjectID {
		return cutoverv1.ApplicationCutoverReview{}, ErrNotFound
	}
	if current.Lifecycle == cutoverv1.ReviewSucceeded {
		return cutoverv1.ApplicationCutoverReview{}, errors.New("succeeded cutover review is immutable")
	}
	if token != "" && current.LeaseToken != "" && current.LeaseToken != token {
		return cutoverv1.ApplicationCutoverReview{}, invalid(cutoverv1.FailureLeaseLost, "cutover review lease token mismatch")
	}
	s.reviews[value.ID] = value
	return value, nil
}

func (s *MemoryStore) HasActive(_ context.Context, projectID, applicationID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, review := range s.reviews {
		if review.ProjectID == projectID && review.ApplicationID == applicationID {
			if review.Lifecycle == cutoverv1.ReviewQueued || review.Lifecycle == cutoverv1.ReviewLeased {
				return true, nil
			}
		}
	}
	return false, nil
}

func (s *MemoryStore) CreateCutover(_ context.Context, value cutoverv1.ApplicationCutover, key, payload string) (cutoverv1.ApplicationCutover, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key != "" {
		compoundKey := value.ProjectID + ":" + key
		if record, ok := s.cutoverIdempotency[compoundKey]; ok {
			if record.payloadHash != payload {
				return cutoverv1.ApplicationCutover{}, false, Error{Code: cutoverv1.FailureIdempotencyConflict, Status: 409, Message: "idempotency key was used with another payload"}
			}
			existing, found := s.cutovers[record.cutoverID]
			if found {
				return existing, true, nil
			}
		}
	}
	for _, cutover := range s.cutovers {
		if cutover.ProjectID == value.ProjectID && cutover.ApplicationID == value.ApplicationID {
			if cutover.Lifecycle == cutoverv1.CutoverQueued || cutover.Lifecycle == cutoverv1.CutoverValidating || cutover.Lifecycle == cutoverv1.CutoverApplying || cutover.Lifecycle == cutoverv1.CutoverDeploying || cutover.Lifecycle == cutoverv1.CutoverVerifying {
				return cutoverv1.ApplicationCutover{}, false, Error{Code: cutoverv1.FailureCutoverAlreadyRunning, Status: 409, Message: "an active cutover is already running for this application"}
			}
		}
	}
	if key != "" {
		compoundKey := value.ProjectID + ":" + key
		s.cutoverIdempotency[compoundKey] = cutoverIdempotencyRecord{cutoverID: value.ID, payloadHash: payload}
	}
	s.cutovers[value.ID] = value
	return value, false, nil
}

func (s *MemoryStore) GetCutover(_ context.Context, projectID, cutoverID string) (cutoverv1.ApplicationCutover, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutover, ok := s.cutovers[cutoverID]
	if !ok || cutover.ProjectID != projectID {
		return cutoverv1.ApplicationCutover{}, ErrNotFound
	}
	return cutover, nil
}

func (s *MemoryStore) ListCutovers(_ context.Context, projectID, applicationID string) ([]cutoverv1.ApplicationCutover, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []cutoverv1.ApplicationCutover{}
	for _, cutover := range s.cutovers {
		if cutover.ProjectID != projectID {
			continue
		}
		if applicationID != "" && cutover.ApplicationID != applicationID {
			continue
		}
		out = append(out, cutover)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].RequestedAt.Before(out[j].RequestedAt)
	})
	return out, nil
}

func (s *MemoryStore) UpdateCutover(_ context.Context, value cutoverv1.ApplicationCutover) (cutoverv1.ApplicationCutover, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.cutovers[value.ID]
	if !ok || current.ProjectID != value.ProjectID {
		return cutoverv1.ApplicationCutover{}, ErrNotFound
	}
	if current.Lifecycle == cutoverv1.CutoverSucceeded {
		return cutoverv1.ApplicationCutover{}, errors.New("succeeded cutover is immutable")
	}
	s.cutovers[value.ID] = value
	return value, nil
}

func (s *MemoryStore) HasActiveCutover(_ context.Context, projectID, applicationID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cutover := range s.cutovers {
		if cutover.ProjectID == projectID && cutover.ApplicationID == applicationID {
			if cutover.Lifecycle == cutoverv1.CutoverQueued || cutover.Lifecycle == cutoverv1.CutoverValidating || cutover.Lifecycle == cutoverv1.CutoverApplying || cutover.Lifecycle == cutoverv1.CutoverDeploying || cutover.Lifecycle == cutoverv1.CutoverVerifying {
				return true, nil
			}
		}
	}
	return false, nil
}

func (s *MemoryStore) CreateRollback(_ context.Context, value cutoverv1.ApplicationCutoverRollback, key, payload string) (cutoverv1.ApplicationCutoverRollback, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key != "" {
		compoundKey := value.ProjectID + ":" + key
		if record, ok := s.rollbackIdempotency[compoundKey]; ok {
			if record.payloadHash != payload {
				return cutoverv1.ApplicationCutoverRollback{}, false, Error{Code: cutoverv1.FailureIdempotencyConflict, Status: 409, Message: "idempotency key was used with another payload"}
			}
			existing, found := s.rollbacks[record.rollbackID]
			if found {
				return existing, true, nil
			}
		}
	}
	for _, rollback := range s.rollbacks {
		if rollback.ProjectID == value.ProjectID && rollback.ApplicationID == value.ApplicationID {
			if rollback.Lifecycle == cutoverv1.RollbackQueued || rollback.Lifecycle == cutoverv1.RollbackValidating || rollback.Lifecycle == cutoverv1.RollbackApplying || rollback.Lifecycle == cutoverv1.RollbackDeploying || rollback.Lifecycle == cutoverv1.RollbackVerifying {
				return cutoverv1.ApplicationCutoverRollback{}, false, Error{Code: cutoverv1.FailureRollbackAlreadyRunning, Status: 409, Message: "an active cutover rollback is already running for this application"}
			}
		}
	}
	if key != "" {
		compoundKey := value.ProjectID + ":" + key
		s.rollbackIdempotency[compoundKey] = rollbackIdempotencyRecord{rollbackID: value.ID, payloadHash: payload}
	}
	s.rollbacks[value.ID] = value
	return value, false, nil
}

func (s *MemoryStore) GetRollback(_ context.Context, projectID, rollbackID string) (cutoverv1.ApplicationCutoverRollback, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rollback, ok := s.rollbacks[rollbackID]
	if !ok || rollback.ProjectID != projectID {
		return cutoverv1.ApplicationCutoverRollback{}, ErrNotFound
	}
	return rollback, nil
}

func (s *MemoryStore) ListRollbacks(_ context.Context, projectID, applicationID string) ([]cutoverv1.ApplicationCutoverRollback, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []cutoverv1.ApplicationCutoverRollback{}
	for _, rollback := range s.rollbacks {
		if rollback.ProjectID != projectID {
			continue
		}
		if applicationID != "" && rollback.ApplicationID != applicationID {
			continue
		}
		out = append(out, rollback)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].RequestedAt.Before(out[j].RequestedAt)
	})
	return out, nil
}

func (s *MemoryStore) UpdateRollback(_ context.Context, value cutoverv1.ApplicationCutoverRollback) (cutoverv1.ApplicationCutoverRollback, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.rollbacks[value.ID]
	if !ok || current.ProjectID != value.ProjectID {
		return cutoverv1.ApplicationCutoverRollback{}, ErrNotFound
	}
	if current.Lifecycle == cutoverv1.RollbackSucceeded {
		return cutoverv1.ApplicationCutoverRollback{}, errors.New("succeeded cutover rollback is immutable")
	}
	s.rollbacks[value.ID] = value
	return value, nil
}

func (s *MemoryStore) HasActiveRollback(_ context.Context, projectID, applicationID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rollback := range s.rollbacks {
		if rollback.ProjectID == projectID && rollback.ApplicationID == applicationID {
			if rollback.Lifecycle == cutoverv1.RollbackQueued || rollback.Lifecycle == cutoverv1.RollbackValidating || rollback.Lifecycle == cutoverv1.RollbackApplying || rollback.Lifecycle == cutoverv1.RollbackDeploying || rollback.Lifecycle == cutoverv1.RollbackVerifying {
				return true, nil
			}
		}
	}
	return false, nil
}

func (s *MemoryStore) CreateFinalization(_ context.Context, value cutoverv1.ApplicationCutoverFinalization, key, payload string) (cutoverv1.ApplicationCutoverFinalization, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key != "" {
		compoundKey := value.ProjectID + ":" + key
		if record, ok := s.finalizationIdempotency[compoundKey]; ok {
			if record.payloadHash != payload {
				return cutoverv1.ApplicationCutoverFinalization{}, false, Error{Code: cutoverv1.FailureIdempotencyConflict, Status: 409, Message: "idempotency key was used with another payload"}
			}
			existing, found := s.finalizations[record.finalizationID]
			if found {
				return existing, true, nil
			}
		}
	}
	for _, finalization := range s.finalizations {
		if finalization.ProjectID == value.ProjectID && finalization.ApplicationID == value.ApplicationID {
			if finalization.Lifecycle == cutoverv1.FinalizationQueued || finalization.Lifecycle == cutoverv1.FinalizationValidating || finalization.Lifecycle == cutoverv1.FinalizationRevokingSourceBinding || finalization.Lifecycle == cutoverv1.FinalizationVerifying {
				return cutoverv1.ApplicationCutoverFinalization{}, false, Error{Code: cutoverv1.FailureFinalizeAlreadyRunning, Status: 409, Message: "an active cutover finalization is already running for this application"}
			}
		}
	}
	if key != "" {
		compoundKey := value.ProjectID + ":" + key
		s.finalizationIdempotency[compoundKey] = finalizationIdempotencyRecord{finalizationID: value.ID, payloadHash: payload}
	}
	s.finalizations[value.ID] = value
	return value, false, nil
}

func (s *MemoryStore) GetFinalization(_ context.Context, projectID, finalizationID string) (cutoverv1.ApplicationCutoverFinalization, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	finalization, ok := s.finalizations[finalizationID]
	if !ok || finalization.ProjectID != projectID {
		return cutoverv1.ApplicationCutoverFinalization{}, ErrNotFound
	}
	return finalization, nil
}

func (s *MemoryStore) ListFinalizations(_ context.Context, projectID, applicationID string) ([]cutoverv1.ApplicationCutoverFinalization, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []cutoverv1.ApplicationCutoverFinalization{}
	for _, finalization := range s.finalizations {
		if finalization.ProjectID != projectID {
			continue
		}
		if applicationID != "" && finalization.ApplicationID != applicationID {
			continue
		}
		out = append(out, finalization)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].RequestedAt.Before(out[j].RequestedAt)
	})
	return out, nil
}

func (s *MemoryStore) UpdateFinalization(_ context.Context, value cutoverv1.ApplicationCutoverFinalization) (cutoverv1.ApplicationCutoverFinalization, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.finalizations[value.ID]
	if !ok || current.ProjectID != value.ProjectID {
		return cutoverv1.ApplicationCutoverFinalization{}, ErrNotFound
	}
	if current.Lifecycle == cutoverv1.FinalizationSucceeded {
		return cutoverv1.ApplicationCutoverFinalization{}, errors.New("succeeded cutover finalization is immutable")
	}
	s.finalizations[value.ID] = value
	return value, nil
}

func (s *MemoryStore) HasActiveFinalization(_ context.Context, projectID, applicationID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, finalization := range s.finalizations {
		if finalization.ProjectID == projectID && finalization.ApplicationID == applicationID {
			if finalization.Lifecycle == cutoverv1.FinalizationQueued || finalization.Lifecycle == cutoverv1.FinalizationValidating || finalization.Lifecycle == cutoverv1.FinalizationRevokingSourceBinding || finalization.Lifecycle == cutoverv1.FinalizationVerifying {
				return true, nil
			}
		}
	}
	return false, nil
}

func (s *MemoryStore) IsCutoverFinalized(_ context.Context, projectID, cutoverID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, finalization := range s.finalizations {
		if finalization.ProjectID == projectID && finalization.CutoverID == cutoverID && finalization.Lifecycle == cutoverv1.FinalizationSucceeded {
			return true, nil
		}
	}
	return false, nil
}
