package webhookrelay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
	backupdomain "github.com/opsi-dev/opsi/cloud/internal/backup"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
	"github.com/opsi-dev/opsi/cloud/internal/resource"
	backupv1 "github.com/opsi-dev/opsi/contracts/go/backupv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

func TestBackupAPICreateListGetLeaseAndResult(t *testing.T) {
	server := NewServer(Config{})
	registryStore := server.Registry.(*registry.Service)
	project, err := registryStore.CreateProject("org-1", "Backup", "backup-api", "user-1", "backup-project-key")
	if err != nil {
		t.Fatal(err)
	}
	facts, err := registryStore.PlacementFacts(context.Background(), project.ID)
	if err != nil {
		t.Fatal(err)
	}
	node, err := registryStore.UpsertNode(project.ID, "backup-node", "server", registry.NodeHealthy, "127.0.0.1", "", "backup-node-key")
	if err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPAT("backup-agent-token")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := registryStore.RegisterAgent(project.ID, node.ID, strings.Repeat("a", 64), hash, "test", "backup-agent-key", map[string]any{"postgres_logical_backup": true})
	if err != nil {
		t.Fatal(err)
	}
	resourceValue := webhookReadyPostgres(project.ID, facts.Environments[0].ID, facts.Runtimes[0].ID, node.ID, agent.ID)
	store := server.Resources.Store.(*resource.MemoryStore)
	if _, _, err := store.Create(context.Background(), resourceValue, "ready-resource", strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	server.Backups.Artifacts = backupdomain.StaticStoreAuthority{Spec: backupv1.StoreSpec{ID: "store-1", Provider: backupv1.StoreProviderS3, Endpoint: "https://s3.example.test", Bucket: "backups", Region: "test-1"}, Credential: backupv1.StoreCredential{AccessKey: "access-canary", SecretKey: "secret-canary"}}
	server.Backups.Resources = server.Resources
	server.Resources.Operations = server.Backups

	path := "/api/projects/" + project.ID + "/resources/" + resourceValue.ID + "/backups"
	created := requestResourceAPI(t, server, http.MethodPost, path, `{}`, "backup-key")
	if created.Code != http.StatusAccepted || strings.Contains(created.Body.String(), "secret-canary") {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var createResult struct {
		Backup backupv1.Backup `json:"backup"`
		Reused bool            `json:"reused"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &createResult); err != nil || createResult.Reused || createResult.Backup.Lifecycle != backupv1.LifecycleQueued {
		t.Fatalf("result=%+v err=%v", createResult, err)
	}
	replay := requestResourceAPI(t, server, http.MethodPost, path, `{}`, "backup-key")
	if replay.Code != http.StatusAccepted || !strings.Contains(replay.Body.String(), `"reused":true`) {
		t.Fatalf("replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	busy := requestResourceAPI(t, server, http.MethodPost, path, `{}`, "backup-key-2")
	if busy.Code != http.StatusConflict || !strings.Contains(busy.Body.String(), backupv1.FailureAlreadyRunning) {
		t.Fatalf("busy status=%d body=%s", busy.Code, busy.Body.String())
	}
	arbitraryDatabase := requestResourceAPI(t, server, http.MethodPost, path, `{"database":"other"}`, "backup-key-3")
	if arbitraryDatabase.Code != http.StatusBadRequest {
		t.Fatalf("arbitrary database status=%d body=%s", arbitraryDatabase.Code, arbitraryDatabase.Body.String())
	}
	listed := requestResourceAPI(t, server, http.MethodGet, path, "", "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), createResult.Backup.ID) {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body.String())
	}
	got := requestResourceAPI(t, server, http.MethodGet, "/api/projects/"+project.ID+"/backups/"+createResult.Backup.ID, "", "")
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), createResult.Backup.ObjectKey) {
		t.Fatalf("get status=%d body=%s", got.Code, got.Body.String())
	}

	leaseRequest := httptest.NewRequest(http.MethodGet, "/v1/agents/"+node.ID+"/webhooks/next?project_id="+project.ID+"&wait=0s", nil)
	leaseRequest.Header.Set("Authorization", "Bearer backup-agent-token")
	leaseResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(leaseResponse, leaseRequest)
	if leaseResponse.Code != http.StatusOK || !strings.Contains(leaseResponse.Body.String(), `"kind":"backup"`) || !strings.Contains(leaseResponse.Body.String(), `"secret_key":"secret-canary"`) {
		t.Fatalf("lease status=%d body=%s", leaseResponse.Code, leaseResponse.Body.String())
	}
	var lease backupv1.Lease
	if err := json.Unmarshal(leaseResponse.Body.Bytes(), &lease); err != nil {
		t.Fatal(err)
	}
	postBackupResult(t, server, node.ID, project.ID, createResult.Backup.ID, "backup-agent-token", backupv1.Result{Status: backupv1.LifecycleRunning, LeaseToken: lease.LeaseToken})
	postBackupResult(t, server, node.ID, project.ID, createResult.Backup.ID, "backup-agent-token", backupv1.Result{Status: backupv1.LifecycleRunning, LeaseToken: lease.LeaseToken})
	result := postBackupResult(t, server, node.ID, project.ID, createResult.Backup.ID, "backup-agent-token", backupv1.Result{Status: backupv1.LifecycleSucceeded, LeaseToken: lease.LeaseToken, SourcePostgresVersion: "18.6", PGDumpVersion: "pg_dump (PostgreSQL) 18.6", ArtifactSize: 64, SHA256: strings.Repeat("c", 64), ObjectETag: "etag-1", ArchiveVerified: true})
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"lifecycle":"succeeded"`) || !strings.Contains(result.Body.String(), strings.Repeat("c", 64)) {
		t.Fatalf("result status=%d body=%s", result.Code, result.Body.String())
	}

	failedCreate := requestResourceAPI(t, server, http.MethodPost, path, `{}`, "backup-key-failed")
	var failedResult struct {
		Backup backupv1.Backup `json:"backup"`
	}
	if failedCreate.Code != http.StatusAccepted || json.Unmarshal(failedCreate.Body.Bytes(), &failedResult) != nil {
		t.Fatalf("failed backup create status=%d body=%s", failedCreate.Code, failedCreate.Body.String())
	}
	leaseResponse = httptest.NewRecorder()
	server.Handler().ServeHTTP(leaseResponse, leaseRequest)
	if err := json.Unmarshal(leaseResponse.Body.Bytes(), &lease); err != nil {
		t.Fatal(err)
	}
	postBackupResult(t, server, node.ID, project.ID, failedResult.Backup.ID, "backup-agent-token", backupv1.Result{Status: backupv1.LifecycleRunning, LeaseToken: lease.LeaseToken})
	postBackupResult(t, server, node.ID, project.ID, failedResult.Backup.ID, "backup-agent-token", backupv1.Result{Status: backupv1.LifecycleFailed, LeaseToken: lease.LeaseToken, FailureCode: backupv1.FailureUploadFailed, FailureMessageRedacted: "controlled upload failure"})
	preExecutionFailure := requestResourceAPI(t, server, http.MethodPost, path, `{}`, "backup-key-store-failed")
	if preExecutionFailure.Code != http.StatusAccepted {
		t.Fatalf("pre-execution failure create status=%d body=%s", preExecutionFailure.Code, preExecutionFailure.Body.String())
	}
	server.Backups.Artifacts = backupdomain.StaticStoreAuthority{}
	leaseResponse = httptest.NewRecorder()
	server.Handler().ServeHTTP(leaseResponse, leaseRequest)
	if leaseResponse.Code != http.StatusNoContent {
		t.Fatalf("pre-execution failure poll status=%d body=%s", leaseResponse.Code, leaseResponse.Body.String())
	}
	audit, err := registryStore.ListAudit(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]bool{}
	startedCount, failedCount := 0, 0
	for _, event := range audit {
		actions[event.Action] = true
		if event.Action == "BACKUP_STARTED" {
			startedCount++
		}
		if event.Action == "BACKUP_FAILED" {
			failedCount++
		}
	}
	if startedCount != 2 || failedCount != 2 {
		t.Fatalf("backup audit counts started=%d failed=%d", startedCount, failedCount)
	}
	for _, action := range []string{"BACKUP_REQUESTED", "BACKUP_STARTED", "BACKUP_SUCCEEDED", "BACKUP_FAILED"} {
		if !actions[action] {
			t.Fatalf("missing backup audit %s: %+v", action, audit)
		}
	}
	auditJSON, _ := json.Marshal(audit)
	if strings.Contains(string(auditJSON), "secret-canary") {
		t.Fatal("backup store credential leaked into audit")
	}
}

func postBackupResult(t *testing.T, server *Server, nodeID, projectID, backupID, token string, result backupv1.Result) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(result)
	request := httptest.NewRequest(http.MethodPost, "/v1/agents/"+nodeID+"/backups/"+backupID+"/result?project_id="+projectID, strings.NewReader(string(body)))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}

func webhookReadyPostgres(projectID, environmentID, runtimeID, nodeID, agentID string) resourcev1.Resource {
	now := time.Now().UTC()
	spec := resourcev1.ManagedResourceSpec{
		SchemaVersion: resourcev1.ManagedResourceSpecSchemaVersion, ResourceID: "res-backup-api", ProjectID: projectID, EnvironmentID: environmentID, ResourceType: resourcev1.TypePostgres,
		Profile: "single-node-experimental", Version: resourcev1.PostgresVersion, Image: resourcev1.PostgresImage, Assignment: resourcev1.ManagedResourceAssignment{RuntimeID: runtimeID, NodeID: nodeID, AgentID: agentID},
		Replicas: 1, CPUMillicores: 250, MemoryBytes: 256 << 20, Ports: []resourcev1.ManagedResourcePort{{Name: "postgres", Port: 5432, Protocol: resourcev1.ProtocolPostgres}}, Storage: resourcev1.StorageRequest{Persistent: true, SizeBytes: 1 << 30, PolicyRef: resourcev1.StoragePolicyDefault},
		Connection: resourcev1.ManagedResourceConnection{ServiceName: "postgres-backup-api", Host: "postgres-backup-api.svc", Port: 5432, Protocol: resourcev1.ProtocolPostgres, Database: backupv1.CanonicalDatabase}, CredentialID: "credential-1", ConfigurationHash: strings.Repeat("d", 64), TopologyRevision: 1, TopologyHash: strings.Repeat("e", 64),
	}
	spec.SpecHash, _ = spec.Hash()
	return resourcev1.Resource{SchemaVersion: resourcev1.SchemaVersion, ID: spec.ResourceID, ProjectID: projectID, EnvironmentID: environmentID, Name: "postgres", Kind: resourcev1.KindManagedService, Provider: "opsi", Type: resourcev1.TypePostgres, Lifecycle: resourcev1.LifecycleReady, Managed: &resourcev1.ManagedSpec{Type: resourcev1.TypePostgres}, CreatedBy: "user-1", CreatedAt: now, UpdatedAt: now, Runtime: &resourcev1.ManagedResourceRuntime{Spec: spec, Evidence: &resourcev1.ManagedResourceEvidence{WorkloadReady: true, AuthReady: true, StorageReady: true, PVCName: "pvc-1", PVCUID: "pvc-uid-1", PVName: "pv-1", PVUID: "pv-uid-1", StorageHash: strings.Repeat("f", 64)}}}
}
