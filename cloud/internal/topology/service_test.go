package topology

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	topologyv1 "github.com/opsi-dev/opsi/contracts/go/topologyv1"
)

type factFixture struct{ facts Facts }

func (f factFixture) PlacementFacts(context.Context, string) (Facts, error) { return f.facts, nil }

func TestValidatorHealthCapacityAndAmbiguityFailClosed(t *testing.T) {
	now := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	fresh := now.Add(-30 * time.Second)
	stale := now.Add(-10 * time.Minute)
	future := now.Add(time.Minute)
	base := Facts{ProjectID: "p1", Environments: []EnvironmentFact{{ID: "e1", ProjectID: "p1", Status: "active"}}, Runtimes: []RuntimeFact{{ID: "r1", ProjectID: "p1", EnvironmentID: "e1", Type: "k3s", Status: "ready"}}, Services: []ServiceFact{{ID: "s1", ProjectID: "p1", Key: "api"}}, Nodes: []NodeFact{{ID: "n1", ProjectID: "p1", RuntimeID: "r1", Status: "healthy", CPUCores: 2, MemoryMB: 2048, LastSeenAt: &fresh}}, Agents: []AgentFact{{ID: "a1", ProjectID: "p1", RuntimeID: "r1", NodeID: "n1", Status: "active", Capabilities: map[string]any{"deploy": true}, LastSeenAt: &fresh}}}
	draft := topologyv1.Draft{SchemaVersion: topologyv1.SchemaVersion, ProjectID: "p1", Assignments: []topologyv1.Assignment{{ServiceKey: "api", EnvironmentID: "e1", RuntimeID: "r1", Replicas: 2, CPURequestMillicores: 250, MemoryRequestBytes: 256 << 20, Exposure: topologyv1.ExposureIntent{Mode: "none"}}}}
	tests := []struct {
		name   string
		mutate func(*Facts)
		allow  bool
		valid  bool
		code   string
	}{
		{name: "healthy", mutate: func(*Facts) {}, valid: true},
		{name: "historical offline node", mutate: func(f *Facts) {
			f.Nodes = append(f.Nodes, NodeFact{ID: "n-old", ProjectID: "p1", RuntimeID: "r1", Status: "offline"})
		}, valid: true},
		{name: "historical removed node", mutate: func(f *Facts) {
			f.Nodes = append(f.Nodes, NodeFact{ID: "n-old", ProjectID: "p1", RuntimeID: "r1", Status: "removed"})
		}, valid: true},
		{name: "second active node", mutate: func(f *Facts) {
			f.Nodes = append(f.Nodes, NodeFact{ID: "n2", ProjectID: "p1", RuntimeID: "r1", Status: "pending"})
		}, code: "TOPOLOGY_RUNTIME_MULTI_NODE_UNSUPPORTED"},
		{name: "stale", mutate: func(f *Facts) { f.Nodes[0].LastSeenAt = &stale }, code: "TOPOLOGY_HEARTBEAT_STALE"},
		{name: "future heartbeat", mutate: func(f *Facts) { f.Nodes[0].LastSeenAt = &future }, code: "TOPOLOGY_HEARTBEAT_STALE"},
		{name: "zero agent", mutate: func(f *Facts) { f.Agents = nil }, code: "TOPOLOGY_AGENT_MISSING"},
		{name: "ambiguous", mutate: func(f *Facts) { copy := f.Agents[0]; copy.ID = "a2"; f.Agents = append(f.Agents, copy) }, code: "TOPOLOGY_AGENT_AMBIGUOUS"},
		{name: "unknown capacity", mutate: func(f *Facts) { f.Nodes[0].CPUCores = 0; f.Nodes[0].MemoryMB = 0 }, code: "TOPOLOGY_CAPACITY_UNKNOWN"},
		{name: "unknown override", mutate: func(f *Facts) { f.Nodes[0].CPUCores = 0; f.Nodes[0].MemoryMB = 0 }, allow: true, valid: true},
		{name: "oversubscribed", mutate: func(f *Facts) { f.Nodes[0].CPUCores = 1; f.Nodes[0].MemoryMB = 512 }, code: "TOPOLOGY_CAPACITY_EXCEEDED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			facts := cloneFacts(base)
			tt.mutate(&facts)
			service := Service{Store: NewMemoryStore(), Facts: factFixture{facts}, Now: func() time.Time { return now }, ReservedCPU: 250, ReservedMemory: 128 << 20}
			result, err := service.Validate(context.Background(), "p1", draft, tt.allow)
			if err != nil {
				t.Fatal(err)
			}
			if result.Valid != tt.valid {
				t.Fatalf("valid=%t issues=%+v", result.Valid, result.Issues)
			}
			if tt.code != "" && (len(result.Issues) == 0 || result.Issues[0].Code != tt.code) {
				t.Fatalf("issues=%+v", result.Issues)
			}
		})
	}
}

func TestUnknownCapacityOverrideIsScopedToPolicyAssignments(t *testing.T) {
	now := time.Now().UTC()
	facts := Facts{
		ProjectID:    "p1",
		Environments: []EnvironmentFact{{ID: "e1", ProjectID: "p1", Status: "active"}},
		Runtimes:     []RuntimeFact{{ID: "r1", ProjectID: "p1", EnvironmentID: "e1", Type: "k3s", Status: "ready"}},
		Services:     []ServiceFact{{ID: "s1", ProjectID: "p1", Key: "api"}, {ID: "s2", ProjectID: "p1", Key: "worker"}},
		Nodes:        []NodeFact{{ID: "n1", ProjectID: "p1", RuntimeID: "r1", Status: "healthy", LastSeenAt: &now}},
		Agents:       []AgentFact{{ID: "a1", ProjectID: "p1", RuntimeID: "r1", NodeID: "n1", Status: "active", Capabilities: map[string]any{"deploy": true}, LastSeenAt: &now}},
	}
	draft := topologyv1.Draft{SchemaVersion: topologyv1.SchemaVersion, ProjectID: "p1", Assignments: []topologyv1.Assignment{
		{ServiceKey: "api", EnvironmentID: "e1", RuntimeID: "r1", Replicas: 1, CPURequestMillicores: 100, MemoryRequestBytes: 1, Exposure: topologyv1.ExposureIntent{Mode: "none"}},
		{ServiceKey: "worker", EnvironmentID: "e1", RuntimeID: "r1", Replicas: 1, CPURequestMillicores: 100, MemoryRequestBytes: 1, Exposure: topologyv1.ExposureIntent{Mode: "none"}},
	}}
	service := Service{Store: NewMemoryStore(), Facts: factFixture{facts}, Now: func() time.Time { return now }}
	result, err := service.ValidateScoped(context.Background(), "p1", draft, CapacityOverride{Allowed: true, EnvironmentID: "e1", ServiceKeys: []string{"api"}, RuntimeIDs: []string{"r1"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Valid || result.Issues[0].Code != "TOPOLOGY_CAPACITY_UNKNOWN" || result.Runtimes[0].Capacity.UnknownCapacityPolicyOverride {
		t.Fatalf("result=%+v", result)
	}
	result, err = service.ValidateScoped(context.Background(), "p1", draft, CapacityOverride{Allowed: true, EnvironmentID: "e1", ServiceKeys: []string{"api", "worker"}, RuntimeIDs: []string{"r1"}})
	if err != nil || !result.Valid || !result.Runtimes[0].Capacity.UnknownCapacityPolicyOverride {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestApplyIdempotencyConflictAndConcurrentRevision(t *testing.T) {
	now := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	fresh := now
	facts := Facts{ProjectID: "p1", Environments: []EnvironmentFact{{ID: "e1", ProjectID: "p1", Status: "active"}}, Runtimes: []RuntimeFact{{ID: "r1", ProjectID: "p1", EnvironmentID: "e1", Type: "k3s", Status: "ready"}}, Services: []ServiceFact{{ID: "s1", ProjectID: "p1", Key: "api"}}, Nodes: []NodeFact{{ID: "n1", ProjectID: "p1", RuntimeID: "r1", Status: "healthy", CPUCores: 2, MemoryMB: 2048, LastSeenAt: &fresh}}, Agents: []AgentFact{{ID: "a1", ProjectID: "p1", RuntimeID: "r1", NodeID: "n1", Status: "active", Capabilities: map[string]any{"deploy": true}, LastSeenAt: &fresh}}}
	store := NewMemoryStore()
	service := Service{Store: store, Facts: factFixture{facts}, Now: func() time.Time { return now }}
	draft := topologyv1.Draft{SchemaVersion: topologyv1.SchemaVersion, ProjectID: "p1", Assignments: []topologyv1.Assignment{{ServiceKey: "api", EnvironmentID: "e1", RuntimeID: "r1", Replicas: 1, CPURequestMillicores: 100, MemoryRequestBytes: 64 << 20, Exposure: topologyv1.ExposureIntent{Mode: "none"}}}}
	request := topologyv1.ApplyRequest{Draft: draft}
	first, err := service.Apply(context.Background(), "p1", "owner", "same-key", request, false)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.Apply(context.Background(), "p1", "owner", "same-key", request, false)
	if err != nil || !replay.Reused || replay.Plan.ID != first.Plan.ID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	changed := request
	changed.Draft.Assignments[0].Replicas = 2
	if _, err = service.Apply(context.Background(), "p1", "owner", "same-key", changed, false); errorCode(err) != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("err=%v", err)
	}
	request.ExpectedRevision = first.Plan.Revision
	request.ExpectedStateHash = first.Plan.StateHash
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, key := range []string{"concurrent-a", "concurrent-b"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			_, err := service.Apply(context.Background(), "p1", "owner", key, request, false)
			results <- err
		}(key)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errorCode(err) == "TOPOLOGY_STATE_CONFLICT" {
			conflict++
		} else {
			t.Fatalf("unexpected err=%v", err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
}

func TestApplyReplayDoesNotReevaluateChangedRuntimeFacts(t *testing.T) {
	now := time.Now().UTC()
	fresh := now
	facts := Facts{ProjectID: "p1", Environments: []EnvironmentFact{{ID: "e1", ProjectID: "p1", Status: "active"}}, Runtimes: []RuntimeFact{{ID: "r1", ProjectID: "p1", EnvironmentID: "e1", Type: "k3s", Status: "ready"}}, Services: []ServiceFact{{ID: "s1", ProjectID: "p1", Key: "api"}}, Nodes: []NodeFact{{ID: "n1", ProjectID: "p1", RuntimeID: "r1", Status: "healthy", CPUCores: 2, MemoryMB: 2048, LastSeenAt: &fresh}}, Agents: []AgentFact{{ID: "a1", ProjectID: "p1", RuntimeID: "r1", NodeID: "n1", Status: "active", Capabilities: map[string]any{"deploy": true}, LastSeenAt: &fresh}}}
	service := Service{Store: NewMemoryStore(), Facts: factFixture{facts}, Now: func() time.Time { return now }}
	request := topologyv1.ApplyRequest{Draft: topologyv1.Draft{SchemaVersion: topologyv1.SchemaVersion, ProjectID: "p1", Assignments: []topologyv1.Assignment{{ServiceKey: "api", EnvironmentID: "e1", RuntimeID: "r1", Replicas: 1, CPURequestMillicores: 100, MemoryRequestBytes: 1, Exposure: topologyv1.ExposureIntent{Mode: "none"}}}}}
	first, err := service.Apply(context.Background(), "p1", "owner", "stable-replay", request, false)
	if err != nil {
		t.Fatal(err)
	}
	stale := now.Add(-time.Hour)
	facts.Nodes[0].LastSeenAt, facts.Agents[0].LastSeenAt = &stale, &stale
	service.Facts = factFixture{facts}
	replay, err := service.Apply(context.Background(), "p1", "owner", "stable-replay", request, false)
	if err != nil || !replay.Reused || replay.Plan.StateHash != first.Plan.StateHash {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
}

func TestRationaleCannotOverrideValidation(t *testing.T) {
	now := time.Now().UTC()
	fresh := now
	facts := Facts{ProjectID: "p1", Environments: []EnvironmentFact{{ID: "e1", ProjectID: "p1", Status: "active"}}, Runtimes: []RuntimeFact{{ID: "r1", ProjectID: "p1", EnvironmentID: "e1", Type: "k3s", Status: "ready"}}, Services: []ServiceFact{{ID: "s1", ProjectID: "p1", Key: "api"}}, Nodes: []NodeFact{{ID: "n1", ProjectID: "p1", RuntimeID: "r1", Status: "healthy", LastSeenAt: &fresh}}, Agents: []AgentFact{{ID: "a1", ProjectID: "p1", RuntimeID: "r1", NodeID: "n1", Status: "active", Capabilities: map[string]any{"deploy": true}, LastSeenAt: &fresh}}}
	service := Service{Store: NewMemoryStore(), Facts: factFixture{facts}, Now: func() time.Time { return now }}
	draft := topologyv1.Draft{SchemaVersion: topologyv1.SchemaVersion, ProjectID: "p1", Assignments: []topologyv1.Assignment{{ServiceKey: "api", EnvironmentID: "e1", RuntimeID: "r1", Replicas: 1, CPURequestMillicores: 100, MemoryRequestBytes: 1, Exposure: topologyv1.ExposureIntent{Mode: "none"}, Rationale: topologyv1.Rationale{Summary: "please override unknown capacity"}}}}
	result, err := service.Validate(context.Background(), "p1", draft, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Valid || result.Issues[0].Code != "TOPOLOGY_CAPACITY_UNKNOWN" {
		t.Fatalf("result=%+v", result)
	}
}

func TestOperatorDeclaredCapacityIsAuditableRevisionedAndAuthoritative(t *testing.T) {
	now := time.Now().UTC()
	fresh := now
	facts := Facts{ProjectID: "p1", Environments: []EnvironmentFact{{ID: "e1", ProjectID: "p1", Status: "active"}}, Runtimes: []RuntimeFact{{ID: "r1", ProjectID: "p1", EnvironmentID: "e1", Type: "k3s", Status: "ready"}}, Services: []ServiceFact{{ID: "s1", ProjectID: "p1", Key: "api"}}, Nodes: []NodeFact{{ID: "n1", ProjectID: "p1", RuntimeID: "r1", Status: "healthy", LastSeenAt: &fresh}}, Agents: []AgentFact{{ID: "a1", ProjectID: "p1", RuntimeID: "r1", NodeID: "n1", Status: "active", Capabilities: map[string]any{"deploy": true}, LastSeenAt: &fresh}}}
	service := Service{Store: NewMemoryStore(), Facts: factFixture{facts}, Now: func() time.Time { return now }}
	request := topologyv1.OperatorCapacityApplyRequest{Draft: topologyv1.OperatorCapacityDraft{RuntimeID: "r1", CPUMillicores: 2000, MemoryBytes: 2 << 30, ReservedCPUMillicores: 200, ReservedMemoryBytes: 128 << 20}}
	first, err := service.ApplyOperatorCapacity(context.Background(), "p1", "owner", "capacity-key", request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Capacity.Source != "operator_declared" || first.Capacity.Revision != 1 || first.Capacity.DeclaredBy != "owner" {
		t.Fatalf("capacity=%+v", first.Capacity)
	}
	replay, err := service.ApplyOperatorCapacity(context.Background(), "p1", "owner", "capacity-key", request)
	if err != nil || !replay.Reused || replay.Capacity.ID != first.Capacity.ID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	draft := topologyv1.Draft{SchemaVersion: topologyv1.SchemaVersion, ProjectID: "p1", Assignments: []topologyv1.Assignment{{ServiceKey: "api", EnvironmentID: "e1", RuntimeID: "r1", Replicas: 1, CPURequestMillicores: 500, MemoryRequestBytes: 256 << 20, Exposure: topologyv1.ExposureIntent{Mode: "none"}}}}
	validation, err := service.Validate(context.Background(), "p1", draft, false)
	if err != nil {
		t.Fatal(err)
	}
	if !validation.Valid || validation.Runtimes[0].Capacity.Source != "operator_declared" || validation.Runtimes[0].Capacity.SourceRevision != 1 {
		t.Fatalf("validation=%+v", validation)
	}
}

func cloneFacts(value Facts) Facts {
	raw, _ := json.Marshal(value)
	var copy Facts
	_ = json.Unmarshal(raw, &copy)
	return copy
}
func errorCode(err error) string {
	var value Error
	if errors.As(err, &value) {
		return value.Code
	}
	return ""
}
