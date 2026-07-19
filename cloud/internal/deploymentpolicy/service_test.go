package deploymentpolicy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/buildrecord"
	"github.com/opsi-dev/opsi/cloud/internal/topology"
	buildrecordv1 "github.com/opsi-dev/opsi/contracts/go/buildrecordv1"
	deploymentpolicyv1 "github.com/opsi-dev/opsi/contracts/go/deploymentpolicyv1"
	topologyv1 "github.com/opsi-dev/opsi/contracts/go/topologyv1"
)

type recordsFixture struct{ record buildrecordv1.Record }

func (f *recordsFixture) Get(context.Context, string, string) (buildrecordv1.Record, error) {
	return f.record, nil
}

type bindingFixture struct{ binding buildrecord.Binding }

func (f bindingFixture) ResolveBuildBinding(_ context.Context, repositoryID uint64, serviceKey string) (buildrecord.Binding, error) {
	if repositoryID != f.binding.RepositoryID || serviceKey != f.binding.ServiceKey {
		return buildrecord.Binding{}, errors.New("not found")
	}
	return f.binding, nil
}

type policyFacts struct{ facts topology.Facts }

func (f policyFacts) PlacementFacts(context.Context, string) (topology.Facts, error) {
	return f.facts, nil
}

func TestDeterministicRoutingAndExactPolicyMismatch(t *testing.T) {
	now := time.Date(2026, 7, 19, 11, 0, 0, 0, time.UTC)
	fresh := now.Add(-10 * time.Second)
	facts := topology.Facts{ProjectID: "p1", Environments: []topology.EnvironmentFact{{ID: "e1", ProjectID: "p1", Status: "active"}}, Runtimes: []topology.RuntimeFact{{ID: "r1", ProjectID: "p1", EnvironmentID: "e1", Type: "k3s", Status: "ready"}}, Services: []topology.ServiceFact{{ID: "s1", ProjectID: "p1", Key: "api"}}, Nodes: []topology.NodeFact{{ID: "n1", ProjectID: "p1", RuntimeID: "r1", Status: "healthy", CPUCores: 2, MemoryMB: 2048, LastSeenAt: &fresh}}, Agents: []topology.AgentFact{{ID: "a1", ProjectID: "p1", RuntimeID: "r1", NodeID: "n1", Status: "active", Capabilities: map[string]any{"deploy": true}, LastSeenAt: &fresh}}}
	binding := bindingFixture{buildrecord.Binding{ProjectID: "p1", BindingID: "b1", ServiceID: "s1", ServiceKey: "api", RepositoryID: 7, RepositoryOwnerID: 8}}
	topologyService := topology.Service{Store: topology.NewMemoryStore(), Facts: policyFacts{facts}, Now: func() time.Time { return now }}
	topologyDraft := topologyv1.Draft{SchemaVersion: topologyv1.SchemaVersion, ProjectID: "p1", Assignments: []topologyv1.Assignment{{ServiceKey: "api", EnvironmentID: "e1", RuntimeID: "r1", Replicas: 1, CPURequestMillicores: 100, MemoryRequestBytes: 64 << 20, Exposure: topologyv1.ExposureIntent{Mode: "none"}}}}
	topologyResult, err := topologyService.Apply(context.Background(), "p1", "owner", "topology-key", topologyv1.ApplyRequest{Draft: topologyDraft}, false)
	if err != nil {
		t.Fatal(err)
	}
	record := buildrecordv1.Record{SchemaVersion: buildrecordv1.SchemaVersion, ID: "br1", ProjectID: "p1", RepositoryID: 7, RepositoryOwnerID: 8, ActiveBindingID: "b1", ServiceID: "s1", ServiceKey: "api", Workload: buildrecordv1.WorkloadIdentity{RepositoryID: 7, RepositoryOwnerID: 8, Ref: "refs/heads/main", EventName: "push", WorkflowRef: "o/r/.github/workflows/cd.yml@refs/heads/main", RunID: 1, RunAttempt: 1}, Build: buildrecordv1.BuildMetadata{ConfigHash: strings.Repeat("a", 64), PlanHash: strings.Repeat("b", 64), Platform: "linux/amd64", OCIRepository: "ghcr.io/o/r/api", OCIDigest: "sha256:" + strings.Repeat("c", 64), Status: "succeeded"}}
	records := &recordsFixture{record}
	service := Service{Store: NewMemoryStore(), BuildRecords: records, Bindings: binding, Topology: topologyService, Now: func() time.Time { return now }}
	draft := deploymentpolicyv1.Draft{SchemaVersion: deploymentpolicyv1.SchemaVersion, ProjectID: "p1", RepositoryID: 7, ServiceKeys: []string{"api"}, WorkflowRefs: []string{record.Workload.WorkflowRef}, AllowedEvents: []string{"push"}, AllowedGitRefs: []string{record.Workload.Ref}, EnvironmentID: "e1", AllowedRuntimeIDs: []string{"r1"}, AllowedOCIRepositories: []string{record.Build.OCIRepository}, AllowedPlatforms: []string{record.Build.Platform}, AllowedConfigHashes: []string{record.Build.ConfigHash}, AllowedBuildPlanHashes: []string{record.Build.PlanHash}, Enabled: true}
	policyResult, err := service.Apply(context.Background(), "p1", "owner", "policy-key", deploymentpolicyv1.ApplyRequest{Draft: draft})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := service.Route(context.Background(), "p1", deploymentpolicyv1.RoutingRequest{BuildRecordID: "br1", EnvironmentID: "e1"})
	if err != nil || !decision.Eligible || decision.RuntimeID != "r1" || decision.AgentID != "a1" || decision.TopologyRevision != topologyResult.Plan.Revision {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
	tests := []struct {
		name        string
		mutate      func(*buildrecordv1.Record)
		environment string
		code        string
	}{
		{name: "wrong repository", mutate: func(r *buildrecordv1.Record) { r.RepositoryID = 9 }, environment: "e1", code: "ROUTING_BUILD_RECORD_INVALID"},
		{name: "wrong service", mutate: func(r *buildrecordv1.Record) { r.ServiceKey = "worker" }, environment: "e1", code: "ROUTING_BINDING_INACTIVE"},
		{name: "wrong ref", mutate: func(r *buildrecordv1.Record) { r.Workload.Ref = "refs/heads/other" }, environment: "e1", code: "ROUTING_POLICY_MISMATCH"},
		{name: "wrong environment", mutate: func(*buildrecordv1.Record) {}, environment: "e2", code: "ROUTING_POLICY_MISMATCH"},
		{name: "wrong OCI", mutate: func(r *buildrecordv1.Record) { r.Build.OCIRepository = "ghcr.io/o/r/other" }, environment: "e1", code: "ROUTING_POLICY_MISMATCH"},
		{name: "wrong config", mutate: func(r *buildrecordv1.Record) { r.Build.ConfigHash = strings.Repeat("d", 64) }, environment: "e1", code: "ROUTING_POLICY_MISMATCH"},
		{name: "wrong build plan", mutate: func(r *buildrecordv1.Record) { r.Build.PlanHash = strings.Repeat("e", 64) }, environment: "e1", code: "ROUTING_POLICY_MISMATCH"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			copy := record
			tt.mutate(&copy)
			records.record = copy
			_, err := service.Route(context.Background(), "p1", deploymentpolicyv1.RoutingRequest{BuildRecordID: "br1", EnvironmentID: tt.environment})
			if deploymentErrorCode(err) != tt.code {
				t.Fatalf("code=%s err=%v", deploymentErrorCode(err), err)
			}
		})
	}
	records.record = record
	disabled, err := service.Disable(context.Background(), "p1", policyResult.Policy.ID, "owner", "disable-key", deploymentpolicyv1.DisableRequest{ExpectedRevision: policyResult.Policy.Revision, ExpectedStateHash: policyResult.Policy.StateHash})
	if err != nil || disabled.Policy.Draft.Enabled {
		t.Fatalf("disabled=%+v err=%v", disabled, err)
	}
	if _, err = service.Route(context.Background(), "p1", deploymentpolicyv1.RoutingRequest{BuildRecordID: "br1", EnvironmentID: "e1"}); deploymentErrorCode(err) != "ROUTING_POLICY_MISMATCH" {
		t.Fatalf("err=%v", err)
	}
}

func TestRoutingFailsClosedOnAmbiguousPolicy(t *testing.T) {
	service, record, draft := routingFixture(t)
	for _, key := range []string{"policy-one", "policy-two"} {
		if _, err := service.Apply(context.Background(), "p1", "owner", key, deploymentpolicyv1.ApplyRequest{Draft: draft}); err != nil {
			t.Fatal(err)
		}
	}
	service.BuildRecords = &recordsFixture{record}
	_, err := service.Route(context.Background(), "p1", deploymentpolicyv1.RoutingRequest{BuildRecordID: record.ID, EnvironmentID: "e1"})
	if deploymentErrorCode(err) != "ROUTING_POLICY_AMBIGUOUS" {
		t.Fatalf("err=%v", err)
	}
}

func TestPolicyReplayDoesNotReevaluateChangedBinding(t *testing.T) {
	service, _, draft := routingFixture(t)
	request := deploymentpolicyv1.ApplyRequest{Draft: draft}
	first, err := service.Apply(context.Background(), "p1", "owner", "stable-policy-replay", request)
	if err != nil {
		t.Fatal(err)
	}
	service.Bindings = bindingFixture{}
	replay, err := service.Apply(context.Background(), "p1", "owner", "stable-policy-replay", request)
	if err != nil || !replay.Reused || replay.Policy.StateHash != first.Policy.StateHash {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
}

func TestRoutingUsesTopologyRuntimeBeforePolicyAmbiguity(t *testing.T) {
	service, record, draft := routingFixture(t)
	facts := service.Topology.Facts.(policyFacts)
	facts.facts.Runtimes = append(facts.facts.Runtimes, topology.RuntimeFact{ID: "r2", ProjectID: "p1", EnvironmentID: "e1", Type: "k3s", Status: "ready"})
	service.Topology.Facts = facts
	if _, err := service.Apply(context.Background(), "p1", "owner", "policy-r1", deploymentpolicyv1.ApplyRequest{Draft: draft}); err != nil {
		t.Fatal(err)
	}
	draft.AllowedRuntimeIDs = []string{"r2"}
	if _, err := service.Apply(context.Background(), "p1", "owner", "policy-r2", deploymentpolicyv1.ApplyRequest{Draft: draft}); err != nil {
		t.Fatal(err)
	}
	service.BuildRecords = &recordsFixture{record}
	decision, err := service.Route(context.Background(), "p1", deploymentpolicyv1.RoutingRequest{BuildRecordID: record.ID, EnvironmentID: "e1"})
	if err != nil || !decision.Eligible || decision.RuntimeID != "r1" {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
}

func TestPolicyRejectsWildcardAndExpressionValues(t *testing.T) {
	service, _, draft := routingFixture(t)
	for name, mutate := range map[string]func(*deploymentpolicyv1.Draft){
		"wildcard runtime": func(d *deploymentpolicyv1.Draft) { d.AllowedRuntimeIDs = []string{"r*"} },
		"shell expression": func(d *deploymentpolicyv1.Draft) { d.AllowedOCIRepositories = []string{"ghcr.io/o/r/$(touch-x)"} },
		"invalid hash":     func(d *deploymentpolicyv1.Draft) { d.AllowedConfigHashes = []string{"not-a-hash"} },
	} {
		t.Run(name, func(t *testing.T) {
			value := draft
			mutate(&value)
			if _, err := service.Preview(context.Background(), "p1", value); err == nil {
				t.Fatal("invalid policy unexpectedly accepted")
			}
		})
	}
}

func routingFixture(t *testing.T) (Service, buildrecordv1.Record, deploymentpolicyv1.Draft) {
	t.Helper()
	now := time.Now().UTC()
	facts := topology.Facts{ProjectID: "p1", Environments: []topology.EnvironmentFact{{ID: "e1", ProjectID: "p1", Status: "active"}}, Runtimes: []topology.RuntimeFact{{ID: "r1", ProjectID: "p1", EnvironmentID: "e1", Type: "k3s", Status: "ready"}}, Services: []topology.ServiceFact{{ID: "s1", ProjectID: "p1", Key: "api"}}, Nodes: []topology.NodeFact{{ID: "n1", ProjectID: "p1", RuntimeID: "r1", Status: "healthy", CPUCores: 2, MemoryMB: 2048, LastSeenAt: &now}}, Agents: []topology.AgentFact{{ID: "a1", ProjectID: "p1", RuntimeID: "r1", NodeID: "n1", Status: "active", Capabilities: map[string]any{"deploy": true}, LastSeenAt: &now}}}
	binding := bindingFixture{buildrecord.Binding{ProjectID: "p1", BindingID: "b1", ServiceID: "s1", ServiceKey: "api", RepositoryID: 7, RepositoryOwnerID: 8}}
	ts := topology.Service{Store: topology.NewMemoryStore(), Facts: policyFacts{facts}, Now: func() time.Time { return now }}
	td := topologyv1.Draft{SchemaVersion: topologyv1.SchemaVersion, ProjectID: "p1", Assignments: []topologyv1.Assignment{{ServiceKey: "api", EnvironmentID: "e1", RuntimeID: "r1", Replicas: 1, CPURequestMillicores: 100, MemoryRequestBytes: 1, Exposure: topologyv1.ExposureIntent{Mode: "none"}}}}
	if _, err := ts.Apply(context.Background(), "p1", "owner", "topology-key", topologyv1.ApplyRequest{Draft: td}, false); err != nil {
		t.Fatal(err)
	}
	record := buildrecordv1.Record{SchemaVersion: buildrecordv1.SchemaVersion, ID: "br1", ProjectID: "p1", RepositoryID: 7, RepositoryOwnerID: 8, ActiveBindingID: "b1", ServiceID: "s1", ServiceKey: "api", Workload: buildrecordv1.WorkloadIdentity{RepositoryID: 7, RepositoryOwnerID: 8, Ref: "refs/heads/main", EventName: "push", WorkflowRef: "wf", RunID: 1, RunAttempt: 1}, Build: buildrecordv1.BuildMetadata{ConfigHash: strings.Repeat("a", 64), PlanHash: strings.Repeat("b", 64), Platform: "linux/amd64", OCIRepository: "ghcr.io/o/r/api", Status: "succeeded"}}
	draft := deploymentpolicyv1.Draft{SchemaVersion: deploymentpolicyv1.SchemaVersion, ProjectID: "p1", RepositoryID: 7, ServiceKeys: []string{"api"}, WorkflowRefs: []string{"wf"}, AllowedEvents: []string{"push"}, AllowedGitRefs: []string{"refs/heads/main"}, EnvironmentID: "e1", AllowedRuntimeIDs: []string{"r1"}, AllowedOCIRepositories: []string{"ghcr.io/o/r/api"}, AllowedPlatforms: []string{"linux/amd64"}, AllowedConfigHashes: []string{record.Build.ConfigHash}, AllowedBuildPlanHashes: []string{record.Build.PlanHash}, Enabled: true}
	return Service{Store: NewMemoryStore(), Bindings: binding, Topology: ts, Now: func() time.Time { return now }}, record, draft
}
func deploymentErrorCode(err error) string {
	var value Error
	if errors.As(err, &value) {
		return value.Code
	}
	return ""
}
