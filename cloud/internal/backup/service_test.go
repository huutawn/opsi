package backup

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	backupv1 "github.com/opsi-dev/opsi/contracts/go/backupv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

type resourceAuthority map[string]resourcev1.Resource

func (a resourceAuthority) Get(_ context.Context, projectID, resourceID string) (resourcev1.Resource, error) {
	value, ok := a[resourceID]
	if !ok || value.ProjectID != projectID {
		return resourcev1.Resource{}, errors.New("resource not found")
	}
	return value, nil
}

func TestBackupLifecycleIdempotencyConcurrencyAndRecovery(t *testing.T) {
	now := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	resources := resourceAuthority{"res-1": readyResource("res-1"), "res-2": readyResource("res-2")}
	service := Service{Store: NewMemoryStore(), Resources: resources, Artifacts: testArtifacts(), Now: func() time.Time { return now }}
	created, reused, err := service.Create(context.Background(), "project-1", "res-1", "user-1", "backup-key")
	if err != nil || reused || created.Lifecycle != backupv1.LifecycleQueued || created.ObjectKey != "acceptance/projects/project-1/environments/env-1/resources/res-1/backups/"+created.ID+".dump" {
		t.Fatalf("create=%+v reused=%t err=%v", created, reused, err)
	}
	replayed, reused, err := service.Create(context.Background(), "project-1", "res-1", "user-1", "backup-key")
	if err != nil || !reused || replayed.ID != created.ID {
		t.Fatalf("replay=%+v reused=%t err=%v", replayed, reused, err)
	}
	if _, _, err := service.Create(context.Background(), "project-1", "res-2", "user-1", "backup-key"); backupCode(err) != "BACKUP_IDEMPOTENCY_CONFLICT" {
		t.Fatalf("idempotency conflict err=%v", err)
	}
	if _, _, err := service.Create(context.Background(), "project-1", "res-1", "user-1", "another-key"); backupCode(err) != backupv1.FailureAlreadyRunning {
		t.Fatalf("concurrent backup err=%v", err)
	}
	lease, ok, err := service.Lease(context.Background(), "project-1", "node-1")
	if err != nil || !ok || lease.Backup.ID != created.ID || lease.Backup.AttemptCount != 1 || lease.Credential.SecretKey == "" {
		t.Fatalf("lease=%+v ok=%t err=%v", lease, ok, err)
	}
	running, err := service.Complete(context.Background(), "project-1", created.ID, backupv1.Result{Status: backupv1.LifecycleRunning, LeaseToken: lease.LeaseToken})
	if err != nil || running.Lifecycle != backupv1.LifecycleRunning || running.StartedAt == nil {
		t.Fatalf("running=%+v err=%v", running, err)
	}
	startedAt := running.StartedAt
	now = now.Add(leaseTTL - time.Minute)
	heartbeat, err := service.Complete(context.Background(), "project-1", created.ID, backupv1.Result{Status: backupv1.LifecycleRunning, LeaseToken: lease.LeaseToken})
	if err != nil || heartbeat.StartedAt == nil || !heartbeat.StartedAt.Equal(*startedAt) {
		t.Fatalf("heartbeat=%+v err=%v", heartbeat, err)
	}
	now = now.Add(2 * time.Minute)
	if premature, ok, err := service.Lease(context.Background(), "project-1", "node-1"); err != nil || ok {
		t.Fatalf("heartbeat did not retain lease: lease=%+v ok=%t err=%v", premature, ok, err)
	}
	now = now.Add(leaseTTL)
	recovered, ok, err := service.Lease(context.Background(), "project-1", "node-1")
	if err != nil || !ok || recovered.Backup.ID != created.ID || recovered.Backup.AttemptCount != 2 || recovered.LeaseToken == lease.LeaseToken {
		t.Fatalf("recovered=%+v ok=%t err=%v", recovered, ok, err)
	}
	if _, err := service.Complete(context.Background(), "project-1", created.ID, backupv1.Result{Status: backupv1.LifecycleSucceeded, LeaseToken: lease.LeaseToken}); backupCode(err) != backupv1.FailureLeaseLost {
		t.Fatalf("stale lease err=%v", err)
	}
	if _, err := service.Complete(context.Background(), "project-1", created.ID, backupv1.Result{Status: backupv1.LifecycleSucceeded, LeaseToken: recovered.LeaseToken}); backupCode(err) != backupv1.FailureLeaseLost {
		t.Fatalf("success before running err=%v", err)
	}
	if _, err := service.Complete(context.Background(), "project-1", created.ID, backupv1.Result{Status: backupv1.LifecycleRunning, LeaseToken: recovered.LeaseToken}); err != nil {
		t.Fatal(err)
	}
	succeeded, err := service.Complete(context.Background(), "project-1", created.ID, backupv1.Result{
		Status: backupv1.LifecycleSucceeded, LeaseToken: recovered.LeaseToken, SourcePostgresVersion: "18.6", PGDumpVersion: "pg_dump (PostgreSQL) 18.6",
		ArtifactSize: 128, SHA256: strings.Repeat("a", 64), ObjectETag: "etag-1", ArchiveVerified: true,
	})
	if err != nil || succeeded.ValidateSucceeded() != nil || succeeded.LeaseToken != "" {
		t.Fatalf("succeeded=%+v err=%v", succeeded, err)
	}
	if active, err := service.HasActive(context.Background(), "project-1", "res-1"); err != nil || active {
		t.Fatalf("active=%t err=%v", active, err)
	}
	if _, _, err := service.Create(context.Background(), "project-1", "res-1", "user-1", "post-success"); err != nil {
		t.Fatalf("new backup after success: %v", err)
	}
}

func TestBackupRejectsNonReadyResourceAndRecordsTypedFailure(t *testing.T) {
	value := readyResource("res-1")
	value.Lifecycle = resourcev1.LifecycleProvisioning
	service := Service{Store: NewMemoryStore(), Resources: resourceAuthority{"res-1": value}, Artifacts: testArtifacts()}
	if _, _, err := service.Create(context.Background(), "project-1", "res-1", "user-1", "key-1"); backupCode(err) != backupv1.FailureResourceNotReady {
		t.Fatalf("not-ready err=%v", err)
	}
	value = readyResource("res-1")
	resources := resourceAuthority{"res-1": value}
	service.Resources = resources
	created, _, err := service.Create(context.Background(), "project-1", "res-1", "user-1", "key-2")
	if err != nil {
		t.Fatal(err)
	}
	value.Lifecycle = resourcev1.LifecycleProvisioning
	resources["res-1"] = value
	terminal, ok, err := service.Lease(context.Background(), "project-1", "node-1")
	if err != nil || ok || terminal.Backup.Lifecycle != backupv1.LifecycleFailed || terminal.Backup.FailureCode != backupv1.FailureResourceNotReady {
		t.Fatalf("pre-execution failure=%+v ok=%t err=%v", terminal, ok, err)
	}
	resources["res-1"] = readyResource("res-1")
	created, _, err = service.Create(context.Background(), "project-1", "res-1", "user-1", "key-3")
	if err != nil {
		t.Fatal(err)
	}
	lease, _, err := service.Lease(context.Background(), "project-1", "node-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Complete(context.Background(), "project-1", created.ID, backupv1.Result{Status: backupv1.LifecycleRunning, LeaseToken: lease.LeaseToken}); err != nil {
		t.Fatal(err)
	}
	failed, err := service.Complete(context.Background(), "project-1", created.ID, backupv1.Result{Status: backupv1.LifecycleFailed, LeaseToken: lease.LeaseToken, FailureCode: backupv1.FailureUploadFailed, FailureMessageRedacted: "object store rejected upload"})
	if err != nil || failed.Lifecycle != backupv1.LifecycleFailed || failed.FailureCode != backupv1.FailureUploadFailed || failed.CompletedAt == nil {
		t.Fatalf("failed=%+v err=%v", failed, err)
	}
}

func readyResource(id string) resourcev1.Resource {
	spec := resourcev1.ManagedResourceSpec{
		SchemaVersion: resourcev1.ManagedResourceSpecSchemaVersion, ResourceID: id, ProjectID: "project-1", EnvironmentID: "env-1", ResourceType: resourcev1.TypePostgres,
		Profile: "single-node-experimental", Version: resourcev1.PostgresVersion, Image: resourcev1.PostgresImage, Assignment: resourcev1.ManagedResourceAssignment{RuntimeID: "runtime-1", NodeID: "node-1", AgentID: "agent-1"},
		Replicas: 1, CPUMillicores: 250, MemoryBytes: 256 << 20, Storage: resourcev1.StorageRequest{Persistent: true, SizeBytes: 1 << 30, PolicyRef: resourcev1.StoragePolicyDefault},
		Ports: []resourcev1.ManagedResourcePort{{Name: "postgres", Port: 5432, Protocol: resourcev1.ProtocolPostgres}}, Connection: resourcev1.ManagedResourceConnection{ServiceName: "postgres-1", Host: "postgres-1.svc", Port: 5432, Protocol: resourcev1.ProtocolPostgres, Database: backupv1.CanonicalDatabase},
		CredentialID: "credential-1", ConfigurationHash: strings.Repeat("a", 64), TopologyRevision: 3, TopologyHash: strings.Repeat("b", 64),
	}
	spec.SpecHash, _ = spec.Hash()
	return resourcev1.Resource{SchemaVersion: resourcev1.SchemaVersion, ID: id, ProjectID: "project-1", EnvironmentID: "env-1", Kind: resourcev1.KindManagedService, Type: resourcev1.TypePostgres, Lifecycle: resourcev1.LifecycleReady, Runtime: &resourcev1.ManagedResourceRuntime{Spec: spec, Evidence: &resourcev1.ManagedResourceEvidence{WorkloadReady: true, AuthReady: true, StorageReady: true, PVCName: "pvc-1", PVCUID: "pvc-uid-1", PVName: "pv-1", PVUID: "pv-uid-1", StorageHash: strings.Repeat("c", 64)}}}
}

func testArtifacts() StaticStoreAuthority {
	return StaticStoreAuthority{Spec: backupv1.StoreSpec{ID: "store-1", Provider: backupv1.StoreProviderS3, Endpoint: "https://s3.example.test", Bucket: "backups", Region: "test-1"}, Credential: backupv1.StoreCredential{AccessKey: "access", SecretKey: "secret"}, Prefix: "acceptance"}
}

func backupCode(err error) string {
	var typed Error
	if errors.As(err, &typed) {
		return typed.Code
	}
	return ""
}
