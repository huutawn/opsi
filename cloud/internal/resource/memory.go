package resource

import (
	"context"
	"sync"
	"time"

	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

type memoryReplay struct {
	payload string
	id      string
}

type MemoryStore struct {
	mu        sync.Mutex
	resources map[string]resourcev1.Resource
	bindings  map[string]resourcev1.Binding
	replays   map[string]memoryReplay
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{resources: map[string]resourcev1.Resource{}, bindings: map[string]resourcev1.Binding{}, replays: map[string]memoryReplay{}}
}

func (s *MemoryStore) Create(_ context.Context, value resourcev1.Resource, key, payload string) (resourcev1.Resource, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	scope := "resource:" + value.ProjectID + ":" + key
	if replay, ok := s.replays[scope]; ok {
		if replay.payload != payload {
			return resourcev1.Resource{}, false, Error{Code: "RESOURCE_IDEMPOTENCY_CONFLICT", Status: 409, Message: "idempotency key was used with another payload"}
		}
		return s.resources[replay.id], true, nil
	}
	s.resources[value.ID] = value
	s.replays[scope] = memoryReplay{payload: payload, id: value.ID}
	return value, false, nil
}

func (s *MemoryStore) Get(_ context.Context, projectID, resourceID string) (resourcev1.Resource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.resources[resourceID]
	if !ok || value.ProjectID != projectID {
		return resourcev1.Resource{}, ErrNotFound
	}
	return value, nil
}

func (s *MemoryStore) List(_ context.Context, projectID, environmentID string) ([]resourcev1.Resource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []resourcev1.Resource{}
	for _, value := range s.resources {
		if value.ProjectID == projectID && (environmentID == "" || value.EnvironmentID == environmentID) {
			out = append(out, value)
		}
	}
	return out, nil
}

func (s *MemoryStore) Update(_ context.Context, value resourcev1.Resource) (resourcev1.Resource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.resources[value.ID]
	if !ok || current.ProjectID != value.ProjectID {
		return resourcev1.Resource{}, ErrNotFound
	}
	s.resources[value.ID] = value
	return value, nil
}

func (s *MemoryStore) ClaimManaged(_ context.Context, projectID, nodeID, token string, now, expires time.Time) (resourcev1.Resource, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, value := range s.resources {
		if value.ProjectID != projectID || value.Runtime == nil || value.Runtime.Spec.Assignment.NodeID != nodeID || value.Lifecycle != resourcev1.LifecyclePlanned && value.Lifecycle != resourcev1.LifecycleProvisioning && value.Lifecycle != resourcev1.LifecycleDeleting {
			continue
		}
		if value.Runtime.LeaseToken != "" && value.Runtime.LeaseExpiresAt.After(now) {
			continue
		}
		value.Runtime.LeaseToken = token
		value.Runtime.LeaseExpiresAt = expires
		if value.Lifecycle != resourcev1.LifecycleDeleting {
			value.Lifecycle = resourcev1.LifecycleProvisioning
		}
		value.UpdatedAt = now
		s.resources[id] = value
		return value, true, nil
	}
	return resourcev1.Resource{}, false, nil
}

func (s *MemoryStore) UpdateClaimed(_ context.Context, value resourcev1.Resource, token string) (resourcev1.Resource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.resources[value.ID]
	if !ok || current.ProjectID != value.ProjectID {
		return resourcev1.Resource{}, ErrNotFound
	}
	if current.Runtime == nil || current.Runtime.LeaseToken != token {
		return resourcev1.Resource{}, invalid("MANAGED_RESOURCE_APPLY_FAILED", "managed resource lease is invalid")
	}
	value.Runtime.LeaseToken = ""
	value.Runtime.LeaseExpiresAt = time.Time{}
	s.resources[value.ID] = value
	return value, nil
}

func (s *MemoryStore) Delete(_ context.Context, projectID, resourceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.resources[resourceID]
	if !ok || value.ProjectID != projectID {
		return ErrNotFound
	}
	delete(s.resources, resourceID)
	for id, binding := range s.bindings {
		if binding.ProjectID == projectID && binding.Target.ID == resourceID {
			delete(s.bindings, id)
		}
	}
	return nil
}

func (s *MemoryStore) DeleteClaimed(_ context.Context, projectID, resourceID, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.resources[resourceID]
	if !ok || value.ProjectID != projectID {
		return ErrNotFound
	}
	if value.Runtime == nil || value.Runtime.LeaseToken != token {
		return invalid("MANAGED_RESOURCE_DELETE_FAILED", "managed resource delete lease is invalid")
	}
	delete(s.resources, resourceID)
	for id, binding := range s.bindings {
		if binding.ProjectID == projectID && binding.Target.ID == resourceID {
			delete(s.bindings, id)
		}
	}
	return nil
}

func (s *MemoryStore) CreateBinding(_ context.Context, value resourcev1.Binding, key, payload string) (resourcev1.Binding, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	scope := "binding:" + value.ProjectID + ":" + key
	if replay, ok := s.replays[scope]; ok {
		if replay.payload != payload {
			return resourcev1.Binding{}, false, Error{Code: "RESOURCE_IDEMPOTENCY_CONFLICT", Status: 409, Message: "idempotency key was used with another payload"}
		}
		return s.bindings[replay.id], true, nil
	}
	s.bindings[value.ID] = value
	s.replays[scope] = memoryReplay{payload: payload, id: value.ID}
	return value, false, nil
}

func (s *MemoryStore) ListBindings(_ context.Context, projectID, environmentID string) ([]resourcev1.Binding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []resourcev1.Binding{}
	for _, value := range s.bindings {
		if value.ProjectID == projectID && (environmentID == "" || value.EnvironmentID == environmentID) {
			out = append(out, value)
		}
	}
	return out, nil
}
