package topology

import (
	"context"
	"sync"

	topologyv1 "github.com/opsi-dev/opsi/contracts/go/topologyv1"
)

type memoryReplay struct {
	payloadHash string
	response    any
}

type MemoryStore struct {
	mu         sync.Mutex
	plans      map[string]topologyv1.Plan
	capacities map[string]topologyv1.OperatorCapacity
	replays    map[string]memoryReplay
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{plans: map[string]topologyv1.Plan{}, capacities: map[string]topologyv1.OperatorCapacity{}, replays: map[string]memoryReplay{}}
}

func (s *MemoryStore) Get(_ context.Context, projectID string) (topologyv1.Plan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	plan, ok := s.plans[projectID]
	if !ok {
		return topologyv1.Plan{}, ErrNotFound
	}
	return plan, nil
}

func (s *MemoryStore) ReplayPlan(_ context.Context, projectID, key, payloadHash string) (topologyv1.Plan, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if replay, ok := s.replays["plan:"+projectID+":"+key]; ok {
		if replay.payloadHash != payloadHash {
			return topologyv1.Plan{}, false, Error{Code: "IDEMPOTENCY_CONFLICT", Status: 409, Message: "idempotency key was already used with a different payload"}
		}
		return replay.response.(topologyv1.Plan), true, nil
	}
	return topologyv1.Plan{}, false, nil
}

func (s *MemoryStore) Apply(_ context.Context, _ string, key, payloadHash string, request topologyv1.ApplyRequest, plan topologyv1.Plan) (topologyv1.Plan, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	replayKey := "plan:" + plan.ProjectID + ":" + key
	if replay, ok := s.replays[replayKey]; ok {
		if replay.payloadHash != payloadHash {
			return topologyv1.Plan{}, false, Error{Code: "IDEMPOTENCY_CONFLICT", Status: 409, Message: "idempotency key was already used with a different payload"}
		}
		return replay.response.(topologyv1.Plan), true, nil
	}
	current := s.plans[plan.ProjectID]
	if current.Revision != request.ExpectedRevision || current.StateHash != request.ExpectedStateHash {
		return topologyv1.Plan{}, false, Error{Code: "TOPOLOGY_STATE_CONFLICT", Status: 409, Message: "topology state changed; refresh diff and retry"}
	}
	if current.ID == "" {
		plan.ID = newID("topo")
	} else {
		plan.ID = current.ID
		plan.CreatedAt = current.CreatedAt
		plan.CreatedBy = current.CreatedBy
	}
	plan.Revision = current.Revision + 1
	plan.StateHash = stateHash(plan.ID, plan.Revision, plan.PlanHash)
	s.plans[plan.ProjectID] = plan
	s.replays[replayKey] = memoryReplay{payloadHash: payloadHash, response: plan}
	return plan, false, nil
}

func (s *MemoryStore) GetOperatorCapacity(_ context.Context, projectID, runtimeID string) (topologyv1.OperatorCapacity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	capacity, ok := s.capacities[projectID+":"+runtimeID]
	if !ok {
		return topologyv1.OperatorCapacity{}, ErrNotFound
	}
	return capacity, nil
}

func (s *MemoryStore) ReplayOperatorCapacity(_ context.Context, projectID, key, payloadHash string) (topologyv1.OperatorCapacity, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if replay, ok := s.replays["capacity:"+projectID+":"+key]; ok {
		if replay.payloadHash != payloadHash {
			return topologyv1.OperatorCapacity{}, false, Error{Code: "IDEMPOTENCY_CONFLICT", Status: 409, Message: "idempotency key was already used with a different payload"}
		}
		return replay.response.(topologyv1.OperatorCapacity), true, nil
	}
	return topologyv1.OperatorCapacity{}, false, nil
}

func (s *MemoryStore) ApplyOperatorCapacity(_ context.Context, _ string, key, payloadHash string, request topologyv1.OperatorCapacityApplyRequest, capacity topologyv1.OperatorCapacity) (topologyv1.OperatorCapacity, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	replayKey := "capacity:" + capacity.ProjectID + ":" + key
	if replay, ok := s.replays[replayKey]; ok {
		if replay.payloadHash != payloadHash {
			return topologyv1.OperatorCapacity{}, false, Error{Code: "IDEMPOTENCY_CONFLICT", Status: 409, Message: "idempotency key was already used with a different payload"}
		}
		return replay.response.(topologyv1.OperatorCapacity), true, nil
	}
	mapKey := capacity.ProjectID + ":" + capacity.RuntimeID
	current := s.capacities[mapKey]
	if current.Revision != request.ExpectedRevision || current.StateHash != request.ExpectedStateHash {
		return topologyv1.OperatorCapacity{}, false, Error{Code: "TOPOLOGY_CAPACITY_STATE_CONFLICT", Status: 409, Message: "capacity state changed; refresh and retry"}
	}
	if current.ID == "" {
		capacity.ID = newID("cap")
	} else {
		capacity.ID = current.ID
	}
	capacity.Revision = current.Revision + 1
	capacity.StateHash = stateHash(capacity.ID, capacity.Revision, hashMust(request.Draft))
	s.capacities[mapKey] = capacity
	s.replays[replayKey] = memoryReplay{payloadHash: payloadHash, response: capacity}
	return capacity, false, nil
}
