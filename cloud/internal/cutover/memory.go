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
	mu          sync.Mutex
	reviews     map[string]cutoverv1.ApplicationCutoverReview
	idempotency map[string]idempotencyRecord
}

type idempotencyRecord struct {
	reviewID    string
	payloadHash string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		reviews:     map[string]cutoverv1.ApplicationCutoverReview{},
		idempotency: map[string]idempotencyRecord{},
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
