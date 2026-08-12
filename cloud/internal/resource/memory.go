package resource

import (
	"context"
	"sync"

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
