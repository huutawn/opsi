package resource

import (
	"context"
	"time"

	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

func (s *MemoryStore) RetainAndDeleteClaimed(_ context.Context, resource resourcev1.Resource, retained resourcev1.RetainedStorage, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.resources[resource.ID]
	if !ok || current.ProjectID != resource.ProjectID || current.Runtime == nil || current.Runtime.LeaseToken != token {
		return invalid("MANAGED_RESOURCE_DELETE_FAILED", "managed resource delete lease is invalid")
	}
	s.retained[retained.ID] = retained
	delete(s.resources, resource.ID)
	return nil
}

func (s *MemoryStore) GetRetainedStorage(_ context.Context, projectID, id string) (resourcev1.RetainedStorage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.retained[id]
	if !ok || value.ProjectID != projectID {
		return resourcev1.RetainedStorage{}, ErrNotFound
	}
	return value, nil
}

func (s *MemoryStore) GetRetainedStorageByResource(_ context.Context, projectID, resourceID string) (resourcev1.RetainedStorage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, value := range s.retained {
		if value.ProjectID == projectID && value.OriginalResourceID == resourceID {
			return value, nil
		}
	}
	return resourcev1.RetainedStorage{}, ErrNotFound
}

func (s *MemoryStore) ListRetainedStorage(_ context.Context, projectID, environmentID string) ([]resourcev1.RetainedStorage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []resourcev1.RetainedStorage{}
	for _, value := range s.retained {
		if value.ProjectID == projectID && (environmentID == "" || value.EnvironmentID == environmentID) {
			out = append(out, value)
		}
	}
	return out, nil
}

func (s *MemoryStore) SaveRetainedStorageReview(_ context.Context, projectID, id string, revision uint64, token, _ string, _ time.Time) (resourcev1.RetainedStorage, bool, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.retained[id]
	if !ok || value.ProjectID != projectID {
		return resourcev1.RetainedStorage{}, false, false, ErrNotFound
	}
	if value.Revision != revision {
		return resourcev1.RetainedStorage{}, false, false, Error{Code: resourcev1.FailureRetainedStorageStaleReview, Status: 409, Message: "retained storage authority changed during review"}
	}
	value.Revision++
	s.retained[id], s.reviews[id] = value, token
	_, activeResource := s.resources[value.OriginalResourceID]
	activeBinding := false
	for _, binding := range s.bindings {
		activeBinding = activeBinding || binding.ProjectID == projectID && binding.Target.ID == value.OriginalResourceID
	}
	return value, activeResource, activeBinding, nil
}

func (s *MemoryStore) RequestRetainedStorageDestroy(_ context.Context, projectID, id, key, payload, reviewToken, actor string, now time.Time) (resourcev1.RetainedStorage, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	scope := "retained-destroy:" + projectID + ":" + key
	if replay, ok := s.replays[scope]; ok {
		if replay.payload != payload || replay.id != id {
			return resourcev1.RetainedStorage{}, false, Error{Code: "RETAINED_STORAGE_IDEMPOTENCY_CONFLICT", Status: 409, Message: "idempotency key was used with another destruction request"}
		}
		return s.retained[id], true, nil
	}
	value, ok := s.retained[id]
	if !ok || value.ProjectID != projectID {
		return resourcev1.RetainedStorage{}, false, ErrNotFound
	}
	if s.reviews[id] != reviewToken {
		return resourcev1.RetainedStorage{}, false, Error{Code: resourcev1.FailureRetainedStorageStaleReview, Status: 409, Message: "retained storage destruction review is stale"}
	}
	if _, active := s.resources[value.OriginalResourceID]; active {
		return resourcev1.RetainedStorage{}, false, Error{Code: resourcev1.FailureRetainedStorageActiveReference, Status: 409, Message: "original resource authority exists"}
	}
	for _, binding := range s.bindings {
		if binding.ProjectID == projectID && binding.Target.ID == value.OriginalResourceID {
			return resourcev1.RetainedStorage{}, false, Error{Code: resourcev1.FailureRetainedStorageActiveReference, Status: 409, Message: "retained storage has an active binding reference"}
		}
	}
	s.replays[scope] = memoryReplay{payload: payload, id: id}
	reused := value.Lifecycle == resourcev1.RetainedStorageDestroying || value.Lifecycle == resourcev1.RetainedStorageDestroyed
	if value.Lifecycle == resourcev1.RetainedStorageRetained || value.Lifecycle == resourcev1.RetainedStorageDestroyFailed || value.Lifecycle == resourcev1.RetainedStorageUnknown {
		value.Lifecycle = resourcev1.RetainedStorageDestroying
		value.Revision++
		value.DestroyRequestedBy = actor
		value.DestroyRequestedAt = &now
		value.FailureCode, value.FailureMessage = "", ""
		s.retained[id] = value
		delete(s.reviews, id)
	}
	return value, reused, nil
}

func (s *MemoryStore) ClaimRetainedStorageDestroy(_ context.Context, projectID, nodeID, token string, now, expires time.Time) (resourcev1.RetainedStorage, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, value := range s.retained {
		if value.ProjectID != projectID || value.Assignment.NodeID != nodeID || value.Lifecycle != resourcev1.RetainedStorageDestroying || value.LeaseToken != "" && value.LeaseExpiresAt.After(now) {
			continue
		}
		value.LeaseToken, value.LeaseExpiresAt = token, expires
		s.retained[id] = value
		return value, true, nil
	}
	return resourcev1.RetainedStorage{}, false, nil
}

func (s *MemoryStore) UpdateRetainedStorageClaimed(_ context.Context, value resourcev1.RetainedStorage, token string) (resourcev1.RetainedStorage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.retained[value.ID]
	if !ok || current.ProjectID != value.ProjectID {
		return resourcev1.RetainedStorage{}, ErrNotFound
	}
	if current.LeaseToken != token {
		return resourcev1.RetainedStorage{}, invalid(resourcev1.FailureStorageDestroyFailed, "retained storage destruction lease is invalid")
	}
	value.LeaseToken, value.LeaseExpiresAt = "", time.Time{}
	s.retained[value.ID] = value
	return value, nil
}
