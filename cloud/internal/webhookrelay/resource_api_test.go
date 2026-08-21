package webhookrelay

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/opsi-dev/opsi/cloud/internal/auth"
	"github.com/opsi-dev/opsi/cloud/internal/postgres"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
	"github.com/opsi-dev/opsi/cloud/internal/resource"
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
			Storage: resourcev1.StorageRequest{Persistent: true, SizeBytes: 1 << 30, PolicyRef: resourcev1.StoragePolicyDefault}, ConnectionPolicy: resourcev1.ExposurePolicy{Mode: "internal"}},
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
	if binding.Code != http.StatusBadRequest || !strings.Contains(binding.Body.String(), "RESOURCE_BINDING_TARGET_NOT_READY") || strings.Contains(binding.Body.String(), `"vault-postgres"`) {
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
	unavailable := requestResourceAPI(t, server, http.MethodPost, "/api/projects/"+project.ID+"/topology/apply", string(applyBody), "unavailable-topology")
	if unavailable.Code != http.StatusServiceUnavailable || !strings.Contains(unavailable.Body.String(), "TOPOLOGY_UNAVAILABLE") {
		t.Fatalf("unavailable status=%d body=%s", unavailable.Code, unavailable.Body.String())
	}
	after, err := server.Topology.Get(t.Context(), project.ID)
	if err == nil || !strings.Contains(err.Error(), "topology not found") || after.Revision != current.Revision || after.StateHash != current.StateHash {
		t.Fatalf("unavailable apply mutated topology: before=%+v after=%+v err=%v", current, after, err)
	}
}

func TestPostgresBindingDeleteAPIAndActiveResourceConflict(t *testing.T) {
	server := NewServer(Config{})
	project, err := server.Registry.CreateProject("org-1", "Project", "project-binding-delete", "user-1", "project-binding-delete-key")
	if err != nil {
		t.Fatal(err)
	}
	services := server.Registry.(*registry.Service)
	application, err := services.CreateService(project.ID, registry.ServiceDraft{Name: "api"}, "application-binding-delete-key")
	if err != nil {
		t.Fatal(err)
	}
	facts, _ := services.PlacementFacts(t.Context(), project.ID)
	created, _, err := server.Resources.Create(t.Context(), project.ID, "user-1", "postgres-binding-delete-key", resourcev1.CreateRequest{
		EnvironmentID: facts.Environments[0].ID, Name: "postgres", Kind: resourcev1.KindManagedService, Type: resourcev1.TypePostgres,
		Managed: &resourcev1.ManagedSpec{Type: resourcev1.TypePostgres, Replicas: 1, CPUMillicores: 250, MemoryBytes: 256 << 20, Storage: resourcev1.StorageRequest{Persistent: true, SizeBytes: 1 << 30, PolicyRef: resourcev1.StoragePolicyDefault}, ConnectionPolicy: resourcev1.ExposurePolicy{Mode: "internal"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	created.Lifecycle = resourcev1.LifecycleReady
	spec := resourcev1.ManagedResourceSpec{ResourceType: resourcev1.TypePostgres, Image: resourcev1.PostgresImage, Replicas: 1, SpecHash: "ready", Connection: resourcev1.ManagedResourceConnection{Host: "postgres.internal", Port: 5432, Protocol: resourcev1.ProtocolPostgres, Database: "opsi"}}
	created.Runtime = &resourcev1.ManagedResourceRuntime{Spec: spec, Evidence: &resourcev1.ManagedResourceEvidence{ObservedSpecHash: spec.SpecHash, WorkloadReady: true, PodReady: true, ServiceReady: true, SecretReady: true, AuthReady: true, StorageReady: true, VolumeMounted: true, PVCName: "pvc", PVName: "pv", Image: spec.Image, ImageID: spec.Image, AvailableReplicas: 1}}
	if _, err := server.Resources.Store.Update(t.Context(), created); err != nil {
		t.Fatal(err)
	}
	bindingBody, _ := json.Marshal(resourcev1.CreateBindingRequest{EnvironmentID: created.EnvironmentID, Source: resourcev1.EndpointReference{Kind: resourcev1.KindApplication, ID: application.ID}, Target: resourcev1.EndpointReference{Kind: resourcev1.KindManagedService, ID: created.ID}, Protocol: resourcev1.ProtocolPostgres, LogicalName: "DATABASE"})
	bindingResponse := requestResourceAPI(t, server, http.MethodPost, "/api/projects/"+project.ID+"/resource-bindings", string(bindingBody), "binding-delete-create-key")
	var bindingResult struct {
		Binding resourcev1.Binding `json:"binding"`
	}
	if bindingResponse.Code != http.StatusCreated || json.Unmarshal(bindingResponse.Body.Bytes(), &bindingResult) != nil || bindingResult.Binding.Lifecycle != resourcev1.LifecycleProvisioning {
		t.Fatalf("binding status=%d body=%s", bindingResponse.Code, bindingResponse.Body.String())
	}
	activeDelete := requestResourceAPI(t, server, http.MethodDelete, "/api/projects/"+project.ID+"/resources/"+created.ID, "", "resource-active-delete-key")
	if activeDelete.Code != http.StatusConflict || !strings.Contains(activeDelete.Body.String(), resourcev1.FailureBindingActive) {
		t.Fatalf("active delete status=%d body=%s", activeDelete.Code, activeDelete.Body.String())
	}
	bindingDelete := requestResourceAPI(t, server, http.MethodDelete, "/api/projects/"+project.ID+"/resource-bindings/"+bindingResult.Binding.ID, "", "binding-delete-key")
	if bindingDelete.Code != http.StatusAccepted || !strings.Contains(bindingDelete.Body.String(), `"lifecycle":"deleting"`) {
		t.Fatalf("binding delete status=%d body=%s", bindingDelete.Code, bindingDelete.Body.String())
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

func TestRetainedStorageAPIRequiresFreshReviewAndIdempotentDestroy(t *testing.T) {
	server := NewServer(Config{})
	project, err := server.Registry.CreateProject("org-1", "Project", "retained-api", "user-1", "retained-api-project")
	if err != nil {
		t.Fatal(err)
	}
	services := server.Registry.(*registry.Service)
	facts, _ := services.PlacementFacts(t.Context(), project.ID)
	node, err := services.UpsertNode(project.ID, "retained-node", "server", registry.NodeHealthy, "127.0.0.1", "", "retained-api-node")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := services.RegisterAgent(project.ID, node.ID, "sha256:retained-api", "hash", "test", "retained-api-agent", map[string]any{"managed_resources": true})
	if err != nil {
		t.Fatal(err)
	}
	created, _, err := server.Resources.Create(t.Context(), project.ID, "user-1", "retained-api-resource", resourcev1.CreateRequest{
		EnvironmentID: facts.Environments[0].ID, Name: "postgres-retained", Kind: resourcev1.KindManagedService, Type: resourcev1.TypePostgres,
		Managed: &resourcev1.ManagedSpec{Type: resourcev1.TypePostgres, Replicas: 1, CPUMillicores: 250, MemoryBytes: 256 << 20, Storage: resourcev1.StorageRequest{Persistent: true, SizeBytes: 1 << 30, PolicyRef: resourcev1.StoragePolicyDefault}, ConnectionPolicy: resourcev1.ExposurePolicy{Mode: "internal"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	created.Lifecycle = resourcev1.LifecycleReady
	created.Runtime = &resourcev1.ManagedResourceRuntime{Spec: resourcev1.ManagedResourceSpec{
		ResourceID: created.ID, ProjectID: project.ID, EnvironmentID: created.EnvironmentID, ResourceType: resourcev1.TypePostgres,
		Assignment: resourcev1.ManagedResourceAssignment{RuntimeID: facts.Runtimes[0].ID, NodeID: node.ID, AgentID: agent.ID},
		Storage:    resourcev1.StorageRequest{Persistent: true, SizeBytes: 1 << 30, PolicyRef: resourcev1.StoragePolicyDefault}, SpecHash: strings.Repeat("a", 64),
	}}
	if _, err := server.Resources.Store.Update(t.Context(), created); err != nil {
		t.Fatal(err)
	}
	deleted := requestResourceAPI(t, server, http.MethodDelete, "/api/projects/"+project.ID+"/resources/"+created.ID, "", "retain-resource-delete")
	if deleted.Code != http.StatusAccepted || !strings.Contains(deleted.Body.String(), `"lifecycle":"deleting"`) {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	lease, ok, err := server.Resources.LeaseManaged(t.Context(), project.ID, node.ID)
	if err != nil || !ok || lease.Action != "delete" {
		t.Fatalf("lease=%+v ok=%t err=%v", lease, ok, err)
	}
	evidence := &resourcev1.ManagedResourceEvidence{
		ObservedSpecHash: lease.Spec.SpecHash, Deleted: true, StorageRetained: true, Namespace: "opsi-retained-api",
		PVCName: "postgres-data", PVCUID: "pvc-retained-api", PVName: "pv-retained-api", PVUID: "pv-uid-retained-api",
		StorageClass: "local-path", ReclaimPolicy: "Delete", RequestedBytes: lease.Spec.Storage.SizeBytes, ActualStorage: "1Gi",
		StorageHash: resourcev1.ManagedResourceStorageHash(lease.Spec), ObservedAt: time.Now().UTC(),
	}
	if _, err := server.Resources.CompleteManaged(t.Context(), project.ID, created.ID, resource.ManagedResult{Status: "deleted", LeaseToken: lease.LeaseToken, Evidence: evidence}); err != nil {
		t.Fatal(err)
	}
	retained, err := server.Resources.GetRetainedStorageByResource(t.Context(), project.ID, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	list := requestResourceAPI(t, server, http.MethodGet, "/api/projects/"+project.ID+"/retained-storages?environment_id="+created.EnvironmentID, "", "")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), retained.ID) || !strings.Contains(list.Body.String(), `"lifecycle":"retained"`) {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	reviewOne := requestResourceAPI(t, server, http.MethodPost, "/api/projects/"+project.ID+"/retained-storages/"+retained.ID+"/review", "", "review-one")
	reviewTwo := requestResourceAPI(t, server, http.MethodPost, "/api/projects/"+project.ID+"/retained-storages/"+retained.ID+"/review", "", "review-two")
	var one, two struct {
		Review resourcev1.RetainedStorageReview `json:"review"`
	}
	if reviewOne.Code != http.StatusOK || reviewTwo.Code != http.StatusOK || json.Unmarshal(reviewOne.Body.Bytes(), &one) != nil || json.Unmarshal(reviewTwo.Body.Bytes(), &two) != nil || one.Review.ReviewToken == two.Review.ReviewToken {
		t.Fatalf("review one=%s two=%s", reviewOne.Body.String(), reviewTwo.Body.String())
	}
	staleBody, _ := json.Marshal(resourcev1.DestroyRetainedStorageRequest{ReviewToken: one.Review.ReviewToken})
	stale := requestResourceAPI(t, server, http.MethodPost, "/api/projects/"+project.ID+"/retained-storages/"+retained.ID+"/destroy", string(staleBody), "destroy-stale")
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), resourcev1.FailureRetainedStorageStaleReview) {
		t.Fatalf("stale status=%d body=%s", stale.Code, stale.Body.String())
	}
	destroyBody, _ := json.Marshal(resourcev1.DestroyRetainedStorageRequest{ReviewToken: two.Review.ReviewToken})
	destroy := requestResourceAPI(t, server, http.MethodPost, "/api/projects/"+project.ID+"/retained-storages/"+retained.ID+"/destroy", string(destroyBody), "destroy-once")
	replay := requestResourceAPI(t, server, http.MethodPost, "/api/projects/"+project.ID+"/retained-storages/"+retained.ID+"/destroy", string(destroyBody), "destroy-once")
	if destroy.Code != http.StatusAccepted || replay.Code != http.StatusAccepted || !strings.Contains(destroy.Body.String(), `"lifecycle":"destroying"`) || !strings.Contains(replay.Body.String(), `"reused":true`) {
		t.Fatalf("destroy=%s replay=%s", destroy.Body.String(), replay.Body.String())
	}
}

func TestPostgresAgentManagedResourceLeaseEndpoint(t *testing.T) {
	db, err := sql.Open("pgx", requirePostgresTestDSN(t, "managed resource lease endpoint"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err := postgres.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	userID, orgID := "user-lease-http-"+suffix, "org-lease-http-"+suffix
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,email) VALUES($1,$2)`, userID, userID+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO organizations(id,name,slug) VALUES($1,'Lease HTTP',$2)`, orgID, orgID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM organizations WHERE id=$1`, orgID)
		_, _ = db.ExecContext(ctx, `DELETE FROM users WHERE id=$1`, userID)
	})
	registryStore := registry.PostgresService{DB: db}
	project, err := registryStore.CreateProject(orgID, "Lease HTTP", "lease-http-"+suffix, userID, "project-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	facts, err := registryStore.PlacementFacts(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	node, err := registryStore.UpsertNode(project.ID, "lease-node", "server", registry.NodeHealthy, "127.0.0.1", "", "node-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	credentialHash, err := auth.HashPAT("agent-secret")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := registryStore.RegisterAgent(project.ID, node.ID, "sha256:lease-http", credentialHash, "test", "agent-"+suffix, map[string]any{"managed_resources": true})
	if err != nil {
		t.Fatal(err)
	}
	resourceStore := resource.PostgresStore{DB: db}
	credentials := resource.NewMemoryCredentialAuthority()
	resources := resource.Service{Store: resourceStore, Scopes: registryStore, Credentials: credentials}
	created, _, err := resources.Create(ctx, project.ID, userID, "resource-"+suffix, resourcev1.CreateRequest{
		EnvironmentID: facts.Environments[0].ID, Name: "postgres", Kind: resourcev1.KindManagedService, Type: resourcev1.TypePostgres,
		Managed: &resourcev1.ManagedSpec{Type: resourcev1.TypePostgres, Version: "default", Replicas: 1, CPUMillicores: 250, MemoryBytes: 256 << 20, Storage: resourcev1.StorageRequest{Persistent: true, SizeBytes: 1 << 30, PolicyRef: resourcev1.StoragePolicyDefault}, ConnectionPolicy: resourcev1.ExposurePolicy{Mode: "internal"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	credentialID := "mrcred-" + created.ID
	if _, err := credentials.Ensure(ctx, credentialID); err != nil {
		t.Fatal(err)
	}
	created.Lifecycle = resourcev1.LifecyclePlanned
	spec := resourcev1.ManagedResourceSpec{
		SchemaVersion: resourcev1.ManagedResourceSpecSchemaVersion, ResourceID: created.ID, ProjectID: project.ID, EnvironmentID: facts.Environments[0].ID, ResourceType: resourcev1.TypePostgres,
		Profile: "single-node-experimental", Version: resourcev1.PostgresVersion, Image: resourcev1.PostgresImage, CredentialID: credentialID,
		Assignment: resourcev1.ManagedResourceAssignment{RuntimeID: facts.Runtimes[0].ID, NodeID: node.ID, AgentID: agent.ID}, Replicas: 1, CPUMillicores: 250, MemoryBytes: 256 << 20,
		Ports: []resourcev1.ManagedResourcePort{{Name: "postgres", Port: 5432, Protocol: resourcev1.ProtocolPostgres}}, Storage: resourcev1.StorageRequest{Persistent: true, SizeBytes: 1 << 30, PolicyRef: resourcev1.StoragePolicyDefault},
		Connection:        resourcev1.ManagedResourceConnection{ServiceName: "opsi-mr-" + created.ID, Host: "opsi-mr-" + created.ID + ".default.svc.cluster.local", Port: 5432, Protocol: resourcev1.ProtocolPostgres, Database: "opsi"},
		ConfigurationHash: strings.Repeat("a", 64), TopologyRevision: 1, TopologyHash: strings.Repeat("b", 64),
	}
	spec.SpecHash, _ = spec.Hash()
	created.Runtime = &resourcev1.ManagedResourceRuntime{Spec: spec}
	if _, err := resourceStore.Update(ctx, created); err != nil {
		t.Fatal(err)
	}
	server := NewServer(Config{})
	server.Registry = registryStore
	server.Resources = resources
	handler := server.Handler()
	path := "/v1/agents/" + node.ID + "/webhooks/next?project_id=" + project.ID + "&wait=0s"

	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer agent-secret")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"kind":"managed_resource"`) || !strings.Contains(w.Body.String(), `"resource_id":"`+created.ID+`"`) {
		t.Fatalf("lease status=%d body=%s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer agent-secret")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("no-work status=%d body=%s", w.Code, w.Body.String())
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
