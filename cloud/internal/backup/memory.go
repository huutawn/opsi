package backup

import (
	"context"
	"sort"
	"sync"
	"time"

	backupv1 "github.com/opsi-dev/opsi/contracts/go/backupv1"
)

type replay struct {
	payload string
	id      string
}

type MemoryStore struct {
	mu      sync.Mutex
	values  map[string]backupv1.Backup
	replays map[string]replay
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{values: map[string]backupv1.Backup{}, replays: map[string]replay{}}
}

func (s *MemoryStore) Create(_ context.Context, value backupv1.Backup, key, payload string) (backupv1.Backup, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	scope := value.ProjectID + ":" + key
	if previous, ok := s.replays[scope]; ok {
		if previous.payload != payload {
			return backupv1.Backup{}, false, Error{Code: "BACKUP_IDEMPOTENCY_CONFLICT", Status: 409, Message: "idempotency key was used with another backup request"}
		}
		return s.values[previous.id], true, nil
	}
	for _, current := range s.values {
		if current.ProjectID == value.ProjectID && current.SourceResourceID == value.SourceResourceID && active(current.Lifecycle) {
			return backupv1.Backup{}, false, Error{Code: backupv1.FailureAlreadyRunning, Status: 409, Message: "a logical backup is already active for this resource"}
		}
	}
	s.values[value.ID] = value
	s.replays[scope] = replay{payload: payload, id: value.ID}
	return value, false, nil
}

func (s *MemoryStore) Get(_ context.Context, projectID, backupID string) (backupv1.Backup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[backupID]
	if !ok || value.ProjectID != projectID {
		return backupv1.Backup{}, ErrNotFound
	}
	return value, nil
}

func (s *MemoryStore) List(_ context.Context, projectID, resourceID string) ([]backupv1.Backup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []backupv1.Backup{}
	for _, value := range s.values {
		if value.ProjectID == projectID && (resourceID == "" || value.SourceResourceID == resourceID) {
			out = append(out, value)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func (s *MemoryStore) Claim(_ context.Context, projectID, nodeID, token string, now, expires time.Time) (backupv1.Backup, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.values))
	for id := range s.values {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		value := s.values[id]
		if value.ProjectID != projectID || value.SourceNodeID != nodeID || !active(value.Lifecycle) || value.Lifecycle != backupv1.LifecycleQueued && value.LeaseExpiresAt.After(now) {
			continue
		}
		value.Lifecycle, value.LeaseToken, value.LeaseExpiresAt = backupv1.LifecycleLeased, token, expires
		leased := now
		value.LeasedAt, value.AttemptCount = &leased, value.AttemptCount+1
		s.values[id] = value
		return value, true, nil
	}
	return backupv1.Backup{}, false, nil
}

func (s *MemoryStore) UpdateClaimed(_ context.Context, value backupv1.Backup, token string) (backupv1.Backup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.values[value.ID]
	if !ok || current.ProjectID != value.ProjectID {
		return backupv1.Backup{}, ErrNotFound
	}
	if current.LeaseToken == "" || current.LeaseToken != token || current.Lifecycle == backupv1.LifecycleSucceeded {
		return backupv1.Backup{}, invalid(backupv1.FailureLeaseLost, "backup lease is invalid")
	}
	if value.Lifecycle == backupv1.LifecycleSucceeded || value.Lifecycle == backupv1.LifecycleFailed {
		value.LeaseToken, value.LeaseExpiresAt = "", time.Time{}
	} else {
		value.LeaseToken = current.LeaseToken
		if value.LeaseExpiresAt.IsZero() {
			value.LeaseExpiresAt = current.LeaseExpiresAt
		}
	}
	s.values[value.ID] = value
	return value, nil
}

func (s *MemoryStore) HasActive(_ context.Context, projectID, resourceID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, value := range s.values {
		if value.ProjectID == projectID && value.SourceResourceID == resourceID && active(value.Lifecycle) {
			return true, nil
		}
	}
	return false, nil
}

func active(lifecycle string) bool {
	return lifecycle == backupv1.LifecycleQueued || lifecycle == backupv1.LifecycleLeased || lifecycle == backupv1.LifecycleRunning
}
