package deploymentpolicy

import (
	"context"
	"sort"
	"sync"
	"time"

	deploymentpolicyv1 "github.com/opsi-dev/opsi/contracts/go/deploymentpolicyv1"
)

type memoryReplay struct {
	payloadHash string
	policy      deploymentpolicyv1.Policy
}
type MemoryStore struct {
	mu       sync.Mutex
	policies map[string]deploymentpolicyv1.Policy
	replays  map[string]memoryReplay
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{policies: map[string]deploymentpolicyv1.Policy{}, replays: map[string]memoryReplay{}}
}
func (s *MemoryStore) Get(_ context.Context, projectID, policyID string) (deploymentpolicyv1.Policy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.policies[policyID]
	if !ok || p.Draft.ProjectID != projectID {
		return p, ErrNotFound
	}
	return p, nil
}
func (s *MemoryStore) List(_ context.Context, projectID string) ([]deploymentpolicyv1.Policy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := []deploymentpolicyv1.Policy{}
	for _, p := range s.policies {
		if p.Draft.ProjectID == projectID {
			result = append(result, p)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}
func (s *MemoryStore) ReplayPolicy(_ context.Context, projectID, operation, key, payloadHash string) (deploymentpolicyv1.Policy, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := map[string]string{"policy_apply": "apply", "policy_disable": "disable"}[operation]
	if prefix == "" {
		return deploymentpolicyv1.Policy{}, false, unavailable()
	}
	if replay, ok := s.replays[prefix+":"+projectID+":"+key]; ok {
		if replay.payloadHash != payloadHash {
			return deploymentpolicyv1.Policy{}, false, Error{Code: "IDEMPOTENCY_CONFLICT", Status: 409, Message: "idempotency key was already used with a different payload"}
		}
		return replay.policy, true, nil
	}
	return deploymentpolicyv1.Policy{}, false, nil
}
func (s *MemoryStore) Apply(_ context.Context, _ string, key, payloadHash string, request deploymentpolicyv1.ApplyRequest, policy deploymentpolicyv1.Policy) (deploymentpolicyv1.Policy, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	replayKey := "apply:" + policy.Draft.ProjectID + ":" + key
	if replay, ok := s.replays[replayKey]; ok {
		if replay.payloadHash != payloadHash {
			return policy, false, Error{Code: "IDEMPOTENCY_CONFLICT", Status: 409, Message: "idempotency key was already used with a different payload"}
		}
		return replay.policy, true, nil
	}
	current := s.policies[request.PolicyID]
	if request.PolicyID != "" && current.ID == "" {
		return policy, false, ErrNotFound
	}
	if current.Revision != request.ExpectedRevision || current.StateHash != request.ExpectedStateHash {
		return policy, false, Error{Code: "DEPLOYMENT_POLICY_STATE_CONFLICT", Status: 409, Message: "policy state changed; refresh diff and retry"}
	}
	if current.ID == "" {
		policy.ID = newID("pol")
	} else {
		policy.ID = current.ID
		policy.CreatedAt = current.CreatedAt
		policy.CreatedBy = current.CreatedBy
	}
	policy.Revision = current.Revision + 1
	policy.StateHash = stateHash(policy.ID, policy.Revision, policy.PolicyHash)
	s.policies[policy.ID] = policy
	s.replays[replayKey] = memoryReplay{payloadHash, policy}
	return policy, false, nil
}
func (s *MemoryStore) Disable(_ context.Context, projectID, policyID, actor, key, payloadHash string, request deploymentpolicyv1.DisableRequest, now time.Time) (deploymentpolicyv1.Policy, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	replayKey := "disable:" + projectID + ":" + key
	if replay, ok := s.replays[replayKey]; ok {
		if replay.payloadHash != payloadHash {
			return deploymentpolicyv1.Policy{}, false, Error{Code: "IDEMPOTENCY_CONFLICT", Status: 409, Message: "idempotency key was already used with a different payload"}
		}
		return replay.policy, true, nil
	}
	current, ok := s.policies[policyID]
	if !ok || current.Draft.ProjectID != projectID {
		return deploymentpolicyv1.Policy{}, false, ErrNotFound
	}
	if current.Revision != request.ExpectedRevision || current.StateHash != request.ExpectedStateHash {
		return deploymentpolicyv1.Policy{}, false, Error{Code: "DEPLOYMENT_POLICY_STATE_CONFLICT", Status: 409, Message: "policy state changed; refresh and retry"}
	}
	if !current.Draft.Enabled {
		return deploymentpolicyv1.Policy{}, false, Error{Code: "DEPLOYMENT_POLICY_ALREADY_DISABLED", Status: 409, Message: "deployment policy is already disabled"}
	}
	current.Revision++
	current.Draft.Enabled = false
	current.AppliedBy = actor
	current.AppliedAt = now
	current.PolicyHash, _ = hashJSON(current.Draft)
	current.StateHash = stateHash(current.ID, current.Revision, current.PolicyHash)
	s.policies[policyID] = current
	s.replays[replayKey] = memoryReplay{payloadHash, current}
	return current, false, nil
}
