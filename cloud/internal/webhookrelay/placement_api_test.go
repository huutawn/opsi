package webhookrelay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
	"github.com/opsi-dev/opsi/cloud/internal/buildrecord"
	"github.com/opsi-dev/opsi/cloud/internal/deploymentpolicy"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
	"github.com/opsi-dev/opsi/cloud/internal/topology"
	deploymentpolicyv1 "github.com/opsi-dev/opsi/contracts/go/deploymentpolicyv1"
	topologyv1 "github.com/opsi-dev/opsi/contracts/go/topologyv1"
)

type placementAPIFacts struct{ facts topology.Facts }

func (f placementAPIFacts) PlacementFacts(context.Context, string) (topology.Facts, error) {
	return f.facts, nil
}

type placementAPIBindings struct{ binding buildrecord.Binding }

func (f placementAPIBindings) ResolveBuildBinding(_ context.Context, repositoryID uint64, serviceKey string) (buildrecord.Binding, error) {
	if repositoryID != f.binding.RepositoryID || serviceKey != f.binding.ServiceKey {
		return buildrecord.Binding{}, errors.New("not found")
	}
	return f.binding, nil
}

func TestPlacementAPIStrictRBACIdempotencyAndCrossProject(t *testing.T) {
	now := time.Now().UTC()
	facts := topology.Facts{ProjectID: "p1", Environments: []topology.EnvironmentFact{{ID: "e1", ProjectID: "p1", Status: "active"}}, Runtimes: []topology.RuntimeFact{{ID: "r1", ProjectID: "p1", EnvironmentID: "e1", Type: "k3s", Status: "ready"}}, Services: []topology.ServiceFact{{ID: "s1", ProjectID: "p1", Key: "api"}}, Nodes: []topology.NodeFact{{ID: "n1", ProjectID: "p1", RuntimeID: "r1", Status: "healthy", CPUCores: 2, MemoryMB: 2048, LastSeenAt: &now}}, Agents: []topology.AgentFact{{ID: "a1", ProjectID: "p1", RuntimeID: "r1", NodeID: "n1", Status: "active", Capabilities: map[string]any{"deploy": true}, LastSeenAt: &now}}}
	binding := placementAPIBindings{buildrecord.Binding{ProjectID: "p1", BindingID: "b1", ServiceID: "s1", ServiceKey: "api", RepositoryID: 7, RepositoryOwnerID: 8}}
	server := NewServer(Config{})
	ownerHash, _ := auth.HashPAT("owner-pat")
	memberHash, _ := auth.HashPAT("member-pat")
	foreignHash, _ := auth.HashPAT("foreign-pat")
	server.Auth = &auth.Service{Store: auth.MemoryStore{Candidates: []auth.Candidate{{UserID: "owner", OrgID: "o1", ProjectID: "p1", Role: "owner", Hash: ownerHash}, {UserID: "member", OrgID: "o1", ProjectID: "p1", Role: "developer", Hash: memberHash}, {UserID: "foreign", OrgID: "o2", ProjectID: "p2", Role: "owner", Hash: foreignHash}}}}
	server.Registry = registry.NewService()
	server.Topology = topology.Service{Store: topology.NewMemoryStore(), Facts: placementAPIFacts{facts}, Now: func() time.Time { return now }}
	server.Policies = deploymentpolicy.Service{Store: deploymentpolicy.NewMemoryStore(), Bindings: binding, Topology: server.Topology, Now: func() time.Time { return now }}
	handler := server.Handler()
	draft := topologyv1.Draft{SchemaVersion: topologyv1.SchemaVersion, ProjectID: "p1", Assignments: []topologyv1.Assignment{{ServiceKey: "api", EnvironmentID: "e1", RuntimeID: "r1", Replicas: 1, CPURequestMillicores: 100, MemoryRequestBytes: 64 << 20, Exposure: topologyv1.ExposureIntent{Mode: "none"}}}}
	unknown := []byte(`{"draft":{"schema_version":"opsi.topology_plan/v1","project_id":"p1","assignments":[]},"unknown":true}`)
	response := placementRequest(t, handler, http.MethodPost, "/api/projects/p1/topology/plan", "owner-pat", unknown, "", false)
	if response.Code != 400 || !strings.Contains(response.Body.String(), "INVALID_JSON") {
		t.Fatalf("strict status=%d body=%s", response.Code, response.Body.String())
	}
	body, _ := json.Marshal(topologyv1.ApplyRequest{Draft: draft})
	response = placementRequest(t, handler, http.MethodPost, "/api/projects/p1/topology/apply", "member-pat", body, "member-key", true)
	if response.Code != 403 || !strings.Contains(response.Body.String(), "PERMISSION_DENIED") {
		t.Fatalf("member status=%d body=%s", response.Code, response.Body.String())
	}
	policyDraft := deploymentpolicyv1.Draft{SchemaVersion: deploymentpolicyv1.SchemaVersion, ProjectID: "p1", RepositoryID: 7, ServiceKeys: []string{"api"}, WorkflowRefs: []string{"wf"}, AllowedEvents: []string{"push"}, AllowedGitRefs: []string{"refs/heads/main"}, EnvironmentID: "e1", AllowedRuntimeIDs: []string{"r1"}, AllowedOCIRepositories: []string{"ghcr.io/o/r/api"}, AllowedPlatforms: []string{"linux/amd64"}, AllowedConfigHashes: []string{strings.Repeat("a", 64)}, AllowedBuildPlanHashes: []string{strings.Repeat("b", 64)}, Enabled: true}
	policyBody, _ := json.Marshal(deploymentpolicyv1.ApplyRequest{Draft: policyDraft})
	response = placementRequest(t, handler, http.MethodPost, "/api/projects/p1/deployment-policies/apply", "owner-pat", policyBody, "policy-key", true)
	if response.Code != 200 {
		t.Fatalf("policy status=%d body=%s", response.Code, response.Body.String())
	}
	var policyResult deploymentpolicyv1.ApplyResult
	if json.Unmarshal(response.Body.Bytes(), &policyResult) != nil {
		t.Fatal("decode policy")
	}
	applyRequest := topologyv1.ApplyRequest{Draft: draft, PolicyID: policyResult.Policy.ID}
	body, _ = json.Marshal(applyRequest)
	response = placementRequest(t, handler, http.MethodPost, "/api/projects/p1/topology/apply", "owner-pat", body, "topology-key", true)
	if response.Code != 200 {
		t.Fatalf("apply status=%d body=%s", response.Code, response.Body.String())
	}
	var first topologyv1.ApplyResult
	_ = json.Unmarshal(response.Body.Bytes(), &first)
	response = placementRequest(t, handler, http.MethodPost, "/api/projects/p1/topology/apply", "owner-pat", body, "topology-key", true)
	var replay topologyv1.ApplyResult
	_ = json.Unmarshal(response.Body.Bytes(), &replay)
	if response.Code != 200 || !replay.Reused || replay.Plan.ID != first.Plan.ID {
		t.Fatalf("replay status=%d result=%+v", response.Code, replay)
	}
	changed := applyRequest
	changed.Draft.Assignments[0].Replicas = 2
	changedBody, _ := json.Marshal(changed)
	response = placementRequest(t, handler, http.MethodPost, "/api/projects/p1/topology/apply", "owner-pat", changedBody, "topology-key", true)
	if response.Code != 409 || !strings.Contains(response.Body.String(), "IDEMPOTENCY_CONFLICT") {
		t.Fatalf("conflict status=%d body=%s", response.Code, response.Body.String())
	}
	response = placementRequest(t, handler, http.MethodGet, "/api/projects/p1/topology", "foreign-pat", nil, "", false)
	if response.Code != 403 || strings.Contains(response.Body.String(), first.Plan.StateHash) {
		t.Fatalf("foreign status=%d body=%s", response.Code, response.Body.String())
	}
}

func placementRequest(t *testing.T, handler http.Handler, method, path, pat string, body []byte, key string, mutation bool) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+pat)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", "req-test")
	if mutation {
		request.Header.Set("Idempotency-Key", key)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
