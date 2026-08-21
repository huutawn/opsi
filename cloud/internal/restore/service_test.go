package restore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	backupv1 "github.com/opsi-dev/opsi/contracts/go/backupv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	restorev1 "github.com/opsi-dev/opsi/contracts/go/restorev1"
)

type backupAuthority map[string]backupv1.Backup

func (a backupAuthority) Get(_ context.Context, _ string, id string) (backupv1.Backup, error) {
	v, ok := a[id]
	if !ok {
		return backupv1.Backup{}, errors.New("backup not found")
	}
	return v, nil
}

type resourceAuthority map[string]resourcev1.Resource

func (a resourceAuthority) Get(_ context.Context, _ string, id string) (resourcev1.Resource, error) {
	v, ok := a[id]
	if !ok {
		return resourcev1.Resource{}, ErrNotFound
	}
	return v, nil
}
func (a resourceAuthority) ListBindings(context.Context, string, string) ([]resourcev1.Binding, error) {
	return nil, nil
}

func TestRestoreReviewStaleIdempotencyAndLifecycle(t *testing.T) {
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	target := testReadyResource("target", "env-1", "node-1")
	backup := backupv1.Backup{SchemaVersion: backupv1.SchemaVersion, ID: "bkp-1", ProjectID: "project-1", EnvironmentID: "env-1", SourceResourceID: "source", SourceNodeID: "node-1", ResourceType: resourcev1.TypePostgres, BackupType: backupv1.BackupTypePostgresLogical, SourceDatabase: backupv1.CanonicalDatabase, SourcePostgresVersion: resourcev1.PostgresVersion, SourceProfile: target.Runtime.Spec.Profile, SourceImage: target.Runtime.Spec.Image, SourceSpecRevision: 1, SourceSpecHash: strings.Repeat("a", 64), SourcePVCName: "source-pvc", SourcePVCUID: "source-pvc-uid", SourceStorageHash: strings.Repeat("b", 64), Format: backupv1.FormatCustom, Lifecycle: backupv1.LifecycleSucceeded, ArtifactSize: 100, SHA256: strings.Repeat("c", 64), PGDumpVersion: "pg_dump (PostgreSQL) 18.6", ArchiveVerified: true, CreatedAt: now, CompletedAt: &now}
	if err := backup.ValidateSucceeded(); err != nil {
		t.Fatal(err)
	}
	service := Service{Store: NewMemoryStore(), Backups: backupAuthority{"bkp-1": backup}, Resources: resourceAuthority{"target": target}, Artifacts: backupdomainForTest{}, Now: func() time.Time { return now }}
	review, err := service.Review(context.Background(), "project-1", "bkp-1", "target", "actor")
	if err != nil || review.Lifecycle != restorev1.ReviewQueued {
		t.Fatalf("review=%+v err=%v", review, err)
	}
	lease, ok, err := service.LeaseReview(context.Background(), "project-1", "node-1")
	if err != nil || !ok {
		t.Fatalf("lease=%+v ok=%t err=%v", lease, ok, err)
	}
	objects := restorev1.ObjectSummary{Schemas: 1}
	evidence := restorev1.PristineEvidenceHash(restorev1.Review{TargetResourceID: review.TargetResourceID, TargetNodeID: review.TargetNodeID, TargetSpecHash: review.TargetSpecHash, TargetPVCUID: review.TargetPVCUID, TargetDatabase: review.TargetDatabase, TargetDatabaseOID: "42", Objects: objects})
	reviewed, err := service.CompleteReview(context.Background(), "project-1", review.ID, restorev1.ReviewResult{Status: restorev1.ReviewSucceeded, LeaseToken: lease.LeaseToken, TargetDatabaseOID: "42", Objects: objects, PristineEvidenceHash: evidence})
	if err != nil {
		t.Fatal(err)
	}
	if err := reviewed.ValidateSucceeded(); err != nil {
		t.Fatal(err)
	}
	created, reused, err := service.Create(context.Background(), "project-1", "bkp-1", "target", reviewed.ID, "actor", "restore-key")
	if err != nil || reused || created.Lifecycle != restorev1.LifecycleQueued {
		t.Fatalf("created=%+v reused=%t err=%v", created, reused, err)
	}
	replayed, reused, err := service.Create(context.Background(), "project-1", "bkp-1", "target", reviewed.ID, "actor", "restore-key")
	if err != nil || !reused || replayed.ID != created.ID {
		t.Fatalf("replay=%+v reused=%t err=%v", replayed, reused, err)
	}
	if _, _, err := service.Create(context.Background(), "project-1", "bkp-1", "target", reviewed.ID, "actor", "other-key"); errorCode(err) != restorev1.FailureAlreadyRunning {
		t.Fatalf("active restore err=%v", err)
	}
	rLease, ok, err := service.Lease(context.Background(), "project-1", "node-1")
	if err != nil || !ok {
		t.Fatalf("restore lease=%+v ok=%t err=%v", rLease, ok, err)
	}
	if _, err := service.Complete(context.Background(), "project-1", created.ID, restorev1.Result{Status: restorev1.LifecycleRunning, LeaseToken: rLease.LeaseToken}); err != nil {
		t.Fatal(err)
	}
	finished, err := service.Complete(context.Background(), "project-1", created.ID, restorev1.Result{Status: restorev1.LifecycleSucceeded, LeaseToken: rLease.LeaseToken, PGRestoreVersion: "pg_restore (PostgreSQL) 18.6", ArchiveVerified: true, RestoredObjects: restorev1.ObjectSummary{Schemas: 1, Tables: 1}, VerificationMetadata: map[string]string{"connectivity": "authenticated", "transaction": "committed"}})
	if err != nil || finished.Lifecycle != restorev1.LifecycleSucceeded {
		t.Fatalf("finished=%+v err=%v", finished, err)
	}
	if active, _ := service.HasActive(context.Background(), "project-1", "target"); active {
		t.Fatal("terminal restore remained active")
	}
}

func TestRestoreRejectsSameResourceAndVersion(t *testing.T) {
	target := testReadyResource("same", "env-1", "node-1")
	b := backupv1.Backup{SchemaVersion: backupv1.SchemaVersion, ID: "b", ProjectID: "p", EnvironmentID: "env-1", SourceResourceID: "same", ResourceType: resourcev1.TypePostgres, BackupType: backupv1.BackupTypePostgresLogical, SourceDatabase: backupv1.CanonicalDatabase, SourcePostgresVersion: resourcev1.PostgresVersion, SourceProfile: target.Runtime.Spec.Profile, SourceImage: target.Runtime.Spec.Image, Lifecycle: backupv1.LifecycleSucceeded, ArtifactSize: 1, SHA256: strings.Repeat("a", 64), PGDumpVersion: "pg_dump (PostgreSQL) 18.6", ArchiveVerified: true, CompletedAt: ptr(time.Now())}
	b.SourcePVCName, b.SourcePVCUID, b.SourceStorageHash, b.Format = "source-pvc", "source-uid", strings.Repeat("b", 64), backupv1.FormatCustom
	s := Service{Store: NewMemoryStore(), Backups: backupAuthority{"b": b}, Resources: resourceAuthority{"same": target}, Artifacts: backupdomainForTest{}}
	if _, err := s.Review(context.Background(), "p", "b", "same", "actor"); errorCode(err) != restorev1.FailureTargetInvalid {
		t.Fatalf("same resource err=%v", err)
	}
}

func TestRestoreReviewNormalizesPostgresVersions(t *testing.T) {
	for _, source := range []string{"18.6 (Debian 18.6-1.pgdg12+2)", "18.6"} {
		t.Run(source, func(t *testing.T) {
			target := testReadyResource("target", "env-1", "node-1")
			backup := testSucceededBackup("bkp", "source", target, source)
			service := Service{Store: NewMemoryStore(), Backups: backupAuthority{backup.ID: backup}, Resources: resourceAuthority{target.ID: target}, Artifacts: backupdomainForTest{}}
			review, err := service.Review(context.Background(), target.ProjectID, backup.ID, target.ID, "actor")
			if err != nil || review.Lifecycle != restorev1.ReviewQueued || review.SourcePostgresVersion != source || review.TargetPostgresVersion != resourcev1.PostgresVersion {
				t.Fatalf("review=%+v err=%v", review, err)
			}
		})
	}
}

func TestRestoreRejectsUnsupportedPostgresVersions(t *testing.T) {
	for _, test := range []struct{ source, target string }{
		{"18.5", "18.6"}, {"17.6", "18.6"}, {"19.0", "18.6"}, {"unknown", "18.6"}, {"18.6", "unknown"}, {"", "18.6"}, {"18.6", ""},
	} {
		t.Run(test.source+"_to_"+test.target, func(t *testing.T) {
			target := testReadyResource("target", "env-1", "node-1")
			target.Runtime.Spec.Version = test.target
			target.Runtime.Spec.SpecHash, _ = target.Runtime.Spec.Hash()
			backup := testSucceededBackup("bkp", "source", target, test.source)
			service := Service{Store: NewMemoryStore(), Backups: backupAuthority{backup.ID: backup}, Resources: resourceAuthority{target.ID: target}, Artifacts: backupdomainForTest{}}
			if _, err := service.Review(context.Background(), target.ProjectID, backup.ID, target.ID, "actor"); errorCode(err) != restorev1.FailureVersionUnsupported {
				t.Fatalf("source=%q target=%q err=%v", test.source, test.target, err)
			}
		})
	}
}

type backupdomainForTest struct{}

func (backupdomainForTest) LeaseConfig() (backupv1.StoreSpec, backupv1.StoreCredential, error) {
	return backupv1.StoreSpec{ID: "s", Provider: backupv1.StoreProviderS3, Bucket: "b", Region: "r"}, backupv1.StoreCredential{AccessKey: "a", SecretKey: "b"}, nil
}
func (backupdomainForTest) ObjectKey(_, _, _, id string) string { return id }
func testReadyResource(id, env, node string) resourcev1.Resource {
	spec := resourcev1.ManagedResourceSpec{SchemaVersion: resourcev1.ManagedResourceSpecSchemaVersion, ResourceID: id, ProjectID: "project-1", EnvironmentID: env, ResourceType: resourcev1.TypePostgres, Profile: "single-node-experimental", Version: resourcev1.PostgresVersion, Image: resourcev1.PostgresImage, Assignment: resourcev1.ManagedResourceAssignment{NodeID: node, RuntimeID: "runtime", AgentID: "agent"}, Replicas: 1, CPUMillicores: 100, MemoryBytes: 1 << 20, Ports: []resourcev1.ManagedResourcePort{{Name: "postgres", Port: 5432, Protocol: resourcev1.ProtocolPostgres}}, Storage: resourcev1.StorageRequest{Persistent: true, SizeBytes: 1, PolicyRef: resourcev1.StoragePolicyDefault}, Connection: resourcev1.ManagedResourceConnection{ServiceName: id, Host: id, Port: 5432, Protocol: resourcev1.ProtocolPostgres, Database: "opsi"}, CredentialID: "cred", ConfigurationHash: strings.Repeat("1", 64), TopologyRevision: 1, TopologyHash: strings.Repeat("2", 64)}
	spec.SpecHash, _ = spec.Hash()
	return resourcev1.Resource{SchemaVersion: resourcev1.SchemaVersion, ID: id, ProjectID: "project-1", EnvironmentID: env, Kind: resourcev1.KindManagedService, Type: resourcev1.TypePostgres, Lifecycle: resourcev1.LifecycleReady, Runtime: &resourcev1.ManagedResourceRuntime{Spec: spec, Evidence: &resourcev1.ManagedResourceEvidence{WorkloadReady: true, AuthReady: true, StorageReady: true, PVCUID: id + "-pvc", PVUID: id + "-pv", PVCName: id + "-pvc", PVName: id + "-pv", StorageHash: strings.Repeat("3", 64)}}}
}

func testSucceededBackup(id, sourceID string, target resourcev1.Resource, version string) backupv1.Backup {
	now := time.Now().UTC()
	return backupv1.Backup{SchemaVersion: backupv1.SchemaVersion, ID: id, ProjectID: target.ProjectID, EnvironmentID: target.EnvironmentID, SourceResourceID: sourceID, SourceNodeID: target.Runtime.Spec.Assignment.NodeID, ResourceType: resourcev1.TypePostgres, BackupType: backupv1.BackupTypePostgresLogical, SourceDatabase: backupv1.CanonicalDatabase, SourcePostgresVersion: version, SourceProfile: target.Runtime.Spec.Profile, SourceImage: target.Runtime.Spec.Image, SourceSpecRevision: 1, SourceSpecHash: strings.Repeat("a", 64), SourcePVCName: "source-pvc", SourcePVCUID: "source-pvc-uid", SourceStorageHash: strings.Repeat("b", 64), Format: backupv1.FormatCustom, Lifecycle: backupv1.LifecycleSucceeded, ArtifactSize: 100, SHA256: strings.Repeat("c", 64), PGDumpVersion: "pg_dump (PostgreSQL) 18.6", ArchiveVerified: true, CreatedAt: now, CompletedAt: &now}
}
func ptr(v time.Time) *time.Time { return &v }
func errorCode(err error) string {
	var value Error
	if err != nil && errors.As(err, &value) {
		return value.Code
	}
	return ""
}
