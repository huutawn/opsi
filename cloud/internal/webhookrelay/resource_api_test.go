package webhookrelay

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/opsi-dev/opsi/cloud/internal/registry"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	topologyv1 "github.com/opsi-dev/opsi/contracts/go/topologyv1"
)

func TestResourceAPIRegistryCreateAndTopologyBoundary(t *testing.T) {
	server := NewServer(Config{})
	project, err := server.Registry.CreateProject("org-1", "Project", "project", "user-1", "project-key")
	if err != nil {
		t.Fatal(err)
	}
	services, ok := server.Registry.(*registry.Service)
	if !ok {
		t.Fatal("memory registry unavailable")
	}
	application, err := services.CreateService(project.ID, registry.ServiceDraft{Name: "api"}, "application-key")
	if err != nil {
		t.Fatal(err)
	}
	facts, err := services.PlacementFacts(t.Context(), project.ID)
	if err != nil || len(facts.Environments) != 1 {
		t.Fatalf("facts=%+v err=%v", facts, err)
	}
	environmentID := facts.Environments[0].ID

	types := requestResourceAPI(t, server, http.MethodGet, "/api/projects/"+project.ID+"/resource-types", "", "")
	if types.Code != http.StatusOK || !strings.Contains(types.Body.String(), `"type":"postgres"`) {
		t.Fatalf("types status=%d body=%s", types.Code, types.Body.String())
	}
	createBody, _ := json.Marshal(resourcev1.CreateRequest{
		EnvironmentID: environmentID, Name: "database", Kind: resourcev1.KindManagedService, Type: resourcev1.TypePostgres,
		Managed: &resourcev1.ManagedSpec{Type: resourcev1.TypePostgres, Replicas: 1, CPUMillicores: 250, MemoryBytes: 256 << 20,
			Storage: resourcev1.StorageRequest{Persistent: true, SizeBytes: 1 << 30}, CredentialRefs: []resourcev1.SecretReference{{SecretID: "vault-postgres"}}, ConnectionPolicy: resourcev1.ExposurePolicy{Mode: "internal"}},
	})
	created := requestResourceAPI(t, server, http.MethodPost, "/api/projects/"+project.ID+"/resources", string(createBody), "resource-key")
	if created.Code != http.StatusCreated || strings.Contains(created.Body.String(), "plaintext") {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var result struct {
		Resource resourcev1.Resource `json:"resource"`
		Reused   bool                `json:"reused"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil || result.Resource.Lifecycle != resourcev1.LifecycleUnplaced || result.Reused {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	replayed := requestResourceAPI(t, server, http.MethodPost, "/api/projects/"+project.ID+"/resources", string(createBody), "resource-key")
	if replayed.Code != http.StatusCreated || !strings.Contains(replayed.Body.String(), `"reused":true`) {
		t.Fatalf("replay status=%d body=%s", replayed.Code, replayed.Body.String())
	}
	bindingBody, _ := json.Marshal(resourcev1.CreateBindingRequest{
		EnvironmentID: environmentID, Source: resourcev1.EndpointReference{Kind: resourcev1.KindApplication, ID: application.ID},
		Target: resourcev1.EndpointReference{Kind: resourcev1.KindManagedService, ID: result.Resource.ID}, Protocol: resourcev1.ProtocolPostgres, LogicalName: "DATABASE",
	})
	binding := requestResourceAPI(t, server, http.MethodPost, "/api/projects/"+project.ID+"/resource-bindings", string(bindingBody), "binding-key")
	if binding.Code != http.StatusCreated || !strings.Contains(binding.Body.String(), `"runtime_refs":null`) || strings.Contains(binding.Body.String(), `"vault-postgres"`) {
		t.Fatalf("binding status=%d body=%s", binding.Code, binding.Body.String())
	}
	topology := requestResourceAPI(t, server, http.MethodGet, "/api/projects/"+project.ID+"/topology/facts", "", "")
	if topology.Code != http.StatusOK || !strings.Contains(topology.Body.String(), result.Resource.ID) || strings.Contains(topology.Body.String(), `"assignments"`) {
		t.Fatalf("topology status=%d body=%s", topology.Code, topology.Body.String())
	}
	node, err := services.UpsertNode(project.ID, "server", "server", registry.NodeHealthy, "127.0.0.1", "", "node-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := services.RegisterAgent(project.ID, node.ID, strings.Repeat("a", 64), "hash", "test", "agent-key", map[string]any{"managed_resources": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := services.RecordAgentHeartbeat(project.ID, node.ID, registry.AgentHeartbeat{Version: "test", Capabilities: map[string]any{"managed_resources": true}, NodeReady: true, K3SStatus: "ready", Capacity: registry.NodeCapacity{CPUCores: 2, MemoryMB: 2048}}); err != nil {
		t.Fatal(err)
	}
	current := topologyv1.Plan{}
	applyBody, _ := json.Marshal(topologyv1.ApplyRequest{ExpectedRevision: current.Revision, ExpectedStateHash: current.StateHash, Draft: topologyv1.Draft{SchemaVersion: topologyv1.SchemaVersion, ProjectID: project.ID, Assignments: []topologyv1.Assignment{{ServiceKey: result.Resource.ID, EnvironmentID: environmentID, RuntimeID: facts.Runtimes[0].ID, Replicas: 1, CPURequestMillicores: 250, MemoryRequestBytes: 256 << 20, Exposure: topologyv1.ExposureIntent{Mode: "none"}}}}})
	unsupported := requestResourceAPI(t, server, http.MethodPost, "/api/projects/"+project.ID+"/topology/apply", string(applyBody), "unsupported-topology")
	if unsupported.Code != http.StatusBadRequest || !strings.Contains(unsupported.Body.String(), "MANAGED_RESOURCE_PROVISIONING_UNSUPPORTED") {
		t.Fatalf("unsupported status=%d body=%s", unsupported.Code, unsupported.Body.String())
	}
	after, err := server.Topology.Get(t.Context(), project.ID)
	if err == nil || !strings.Contains(err.Error(), "topology not found") || after.Revision != current.Revision || after.StateHash != current.StateHash {
		t.Fatalf("unsupported apply mutated topology: before=%+v after=%+v err=%v", current, after, err)
	}
}

func TestResourceAPIRejectsUnknownJSONAndType(t *testing.T) {
	server := NewServer(Config{})
	project, err := server.Registry.CreateProject("org-1", "Project", "project", "user-1", "project-key")
	if err != nil {
		t.Fatal(err)
	}
	facts, _ := server.Registry.PlacementFacts(t.Context(), project.ID)
	path := "/api/projects/" + project.ID + "/resources"
	unknownJSON := requestResourceAPI(t, server, http.MethodPost, path, `{"environment_id":"`+facts.Environments[0].ID+`","name":"x","kind":"managed_service","type":"postgres","manifest":"apiVersion: v1"}`, "unknown-json")
	if unknownJSON.Code != http.StatusBadRequest || !strings.Contains(unknownJSON.Body.String(), "INVALID_RESOURCE_JSON") {
		t.Fatalf("status=%d body=%s", unknownJSON.Code, unknownJSON.Body.String())
	}
	plaintext := requestResourceAPI(t, server, http.MethodPost, path, `{"environment_id":"`+facts.Environments[0].ID+`","name":"x","kind":"managed_service","type":"postgres","managed":{"type":"postgres","password":"plaintext"}}`, "plaintext")
	if plaintext.Code != http.StatusBadRequest || !strings.Contains(plaintext.Body.String(), "INVALID_RESOURCE_JSON") {
		t.Fatalf("status=%d body=%s", plaintext.Code, plaintext.Body.String())
	}
	unknownType := requestResourceAPI(t, server, http.MethodPost, path, `{"environment_id":"`+facts.Environments[0].ID+`","name":"kafka","kind":"managed_service","type":"kafka","managed":{"type":"kafka","replicas":1,"cpu_millicores":100,"memory_bytes":1024,"storage":{"persistent":true,"size_bytes":1024},"credential_refs":[{"secret_id":"vault-kafka"}],"connection_policy":{"mode":"internal"}}}`, "unknown-type")
	if unknownType.Code != http.StatusBadRequest || !strings.Contains(unknownType.Body.String(), "RESOURCE_TYPE_UNSUPPORTED") {
		t.Fatalf("status=%d body=%s", unknownType.Code, unknownType.Body.String())
	}
}

func requestResourceAPI(t *testing.T, server *Server, method, path, body, key string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
		request.Header.Set("X-Request-ID", "request-"+key)
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}
